package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxFilterFileBytes        int64 = 256 << 10
	maxGitIgnoreBatchBytes    int64 = 8 << 20
	filterCancellationRecords       = 2_048
)

type gitignoreMode uint8

const (
	skipGitignore gitignoreMode = iota
	useGitignore
)

type ignoreRule struct {
	pattern string
	negated bool
}

type filterProjection struct {
	flags []review.Flags
}

type filterPathEncoding uint8

const (
	filterPathInvalidUTF8 filterPathEncoding = iota
	filterPathValidUTF8
)

type filterGitignoreMatch uint8

const (
	filterNotGitignored filterGitignoreMatch = iota
	filterGitignored
)

type filterPathFacts struct {
	encoding  filterPathEncoding
	gitignore filterGitignoreMatch
}

func buildFilterProjection(
	ctx context.Context,
	options OpenOptions,
	records []diffRecord,
	ignored []bool,
	rules []ignoreRule,
	ledger *sourceLedger,
) (filterProjection, error) {
	if len(ignored) != len(records) || ledger == nil {
		return filterProjection{}, errors.New("filter projection ownership is invalid")
	}
	matcher, err := newCompiledFilterSet(ctx, ledger, options.Include, options.Exclude, rules)
	if err != nil {
		return filterProjection{}, err
	}
	defer matcher.Close()
	projection := filterProjection{flags: make([]review.Flags, len(records))}
	for index, record := range records {
		if index%filterCancellationRecords == 0 {
			if err := context.Cause(ctx); err != nil {
				return filterProjection{}, err
			}
		}
		path := primaryPath(record)
		encoding := filterPathInvalidUTF8
		if utf8.ValidString(path) {
			encoding = filterPathValidUTF8
		}
		match, err := matcher.matchPrepared(path, encoding)
		if err != nil {
			return filterProjection{}, err
		}
		gitignore := filterNotGitignored
		if ignored[index] {
			gitignore = filterGitignored
		}
		projection.flags[index], err = filterFlags(
			path,
			filterPathFacts{encoding: encoding, gitignore: gitignore},
			options,
			match,
		)
		if err != nil {
			return filterProjection{}, err
		}
	}
	return projection, nil
}

func (service *Service) gitignored(
	ctx context.Context,
	runCPU CPUAdmission,
	repository *rootedRepository,
	environment map[string]string,
	records []diffRecord,
	mode gitignoreMode,
	ledger *sourceLedger,
) ([]bool, error) {
	ignored := make([]bool, len(records))
	if mode != useGitignore || len(records) == 0 {
		return ignored, nil
	}
	scratchBytes := gitignoreScratchBytes(len(records))
	if err := ledger.reserve(scratchBytes); err != nil {
		return nil, err
	}
	defer ledger.release(scratchBytes)

	for start := 0; start < len(records); {
		var input bytes.Buffer
		var pathToIndexes map[string][]int
		end := start
		if err := runCPU(ctx, func() error {
			pathToIndexes = make(map[string][]int)
			for end < len(records) {
				if cancellationErr := context.Cause(ctx); cancellationErr != nil {
					return cancellationErr
				}
				path := primaryPath(records[end])
				if input.Len() != 0 &&
					int64(input.Len())+int64(len(path))+1 > maxGitIgnoreBatchBytes {
					break
				}
				input.WriteString(path)
				input.WriteByte(0)
				pathToIndexes[path] = append(pathToIndexes[path], end)
				end++
			}
			return nil
		}); err != nil {
			return nil, err
		}
		result, err := service.runGitWithInputIn(
			ctx,
			repository.gitDirectory(),
			environment,
			[]string{"check-ignore", "--no-index", "-z", gitStdin},
			input.Bytes(),
			[]int{0, 1},
			maxGitIgnoreBatchBytes,
		)
		if err != nil {
			return nil, fmt.Errorf("evaluate .gitignore: %w", err)
		}
		if err := runCPU(ctx, func() error {
			for output := result.Stdout; len(output) > 0; {
				path, remaining, ok, splitErr := nextNULTerminatedCancellable(ctx, output)
				if splitErr != nil {
					return splitErr
				}
				if !ok {
					return errors.New("git check-ignore returned an unterminated path")
				}
				output = remaining
				indexes, exists := pathToIndexes[string(path)]
				if !exists {
					return errors.New("git check-ignore returned an unknown path")
				}
				for _, index := range indexes {
					ignored[index] = true
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		start = end
	}
	return ignored, nil
}

func nextNULTerminatedCancellable(
	ctx context.Context,
	value []byte,
) ([]byte, []byte, bool, error) {
	index, err := indexSourceByteCancellable(ctx, value, 0)
	if err != nil {
		return nil, value, false, err
	}
	if index < 0 {
		return nil, value, false, nil
	}
	return value[:index], value[index+1:], true, nil
}

func filterFlags(
	path string,
	facts filterPathFacts,
	options OpenOptions,
	match filterMatchFlags,
) (review.Flags, error) {
	if !match.valid() {
		return review.Flags{}, errors.New("filter match flags are invalid")
	}
	generated, vendored, lockFile, minified := false, false, false, false
	if facts.encoding == filterPathValidUTF8 {
		lower := strings.ToLower(path)
		base := strings.ToLower(filepath.Base(path))
		generated = strings.Contains(lower, "/generated/") ||
			strings.Contains(base, ".generated.") ||
			strings.HasSuffix(base, ".pb.go")
		vendored = hasPathSegment(lower, "vendor") ||
			hasPathSegment(lower, "node_modules") ||
			hasPathSegment(lower, "third_party")
		lockFile = strings.HasSuffix(base, ".lock") ||
			base == "go.sum" || base == "package-lock.json" ||
			base == "pnpm-lock.yaml" || base == "yarn.lock"
		minified = strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css")
	}

	var reasons []review.HiddenReason
	if options.BuiltinDefaults && (generated || vendored || lockFile || minified) {
		reasons = append(reasons, review.HiddenBuiltinDefault)
	}
	if facts.gitignore == filterGitignored {
		reasons = append(reasons, review.HiddenGitignore)
	}
	if match.Has(filterMatchIgnore) {
		reasons = append(reasons, review.HiddenRVWIgnore)
	}
	if len(options.Include) != 0 && !match.Has(filterMatchInclude) {
		reasons = append(reasons, review.HiddenInclude)
	}
	if match.Has(filterMatchExclude) {
		reasons = append(reasons, review.HiddenExclude)
	}
	traitValues := make([]review.FileTrait, 0, 4)
	if generated {
		traitValues = append(traitValues, review.TraitGenerated)
	}
	if vendored {
		traitValues = append(traitValues, review.TraitVendored)
	}
	if lockFile {
		traitValues = append(traitValues, review.TraitLockFile)
	}
	if minified {
		traitValues = append(traitValues, review.TraitMinified)
	}
	traits, err := review.NewFileTraits(traitValues...)
	if err != nil {
		return review.Flags{}, err
	}
	hidden, err := review.NewHiddenReasons(reasons...)
	if err != nil {
		return review.Flags{}, err
	}
	return review.NewFlags(traits, hidden)
}

func gitignoreScratchBytes(records int) int64 {
	return 2*maxGitIgnoreBatchBytes + int64(records)*80 +
		MaxCommandErrorConstructionBytes + maxSourceProtocolToken
}

func readIgnoreRules(
	repository *rootedRepository,
	name IgnoreFile,
) ([]ignoreRule, error) {
	contents, err := readIgnoreRuleContents(repository, name)
	if err != nil {
		return nil, err
	}
	return parseIgnoreRules(context.Background(), name, contents)
}

func readIgnoreRuleContents(
	repository *rootedRepository,
	name IgnoreFile,
) (contents []byte, returnErr error) {
	file, err := repository.filesystem.Open(name.String())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	contents, err = io.ReadAll(io.LimitReader(file, maxFilterFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(contents)) > maxFilterFileBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxFilterFileBytes)
	}
	return contents, nil
}

func parseIgnoreRules(
	ctx context.Context,
	name IgnoreFile,
	contents []byte,
) ([]ignoreRule, error) {
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("%s is not valid UTF-8", name)
	}
	rules := make([]ignoreRule, 0, 16)
	for len(contents) > 0 {
		lineEnd, err := indexSourceByteCancellable(ctx, contents, '\n')
		if err != nil {
			return nil, err
		}
		lineBytes := contents
		if lineEnd >= 0 {
			lineBytes = contents[:lineEnd]
			contents = contents[lineEnd+1:]
		} else {
			contents = nil
		}
		if len(lineBytes) > 4_096 {
			return nil, fmt.Errorf("%s contains a pattern exceeding 4096 bytes", name)
		}
		line := string(lineBytes)
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimPrefix(line, "!")
		}
		if line == "" {
			return nil, fmt.Errorf("%s contains an empty negation pattern", name)
		}
		rules = append(rules, ignoreRule{pattern: line, negated: negated})
		if len(rules) > 4_096 {
			return nil, fmt.Errorf("%s contains more than 4096 patterns", name)
		}
	}
	return rules, nil
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || int64(len(pattern)) > 4_096 || !utf8.ValidString(pattern) {
		return nil, errors.New("invalid glob pattern")
	}
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	directory := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return nil, errors.New("empty glob pattern")
	}

	var expression strings.Builder
	switch {
	case anchored:
		expression.WriteByte('^')
	case !strings.Contains(pattern, "/"):
		expression.WriteString(`(?:^|.*/)`)
	default:
		expression.WriteString(`^(?:.*/)?`)
	}
	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				expression.WriteString(`.*`)
				index += 2
			} else {
				expression.WriteString(`[^/]*`)
				index++
			}
		case '?':
			expression.WriteString(`[^/]`)
			index++
		case '[':
			closeIndex := strings.IndexByte(pattern[index+1:], ']')
			if closeIndex < 0 {
				return nil, errors.New("unterminated character class")
			}
			closeIndex += index + 1
			class := pattern[index+1 : closeIndex]
			if class == "" || strings.ContainsRune(class, '/') {
				return nil, errors.New("invalid character class")
			}
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			expression.WriteByte('[')
			expression.WriteString(class)
			expression.WriteByte(']')
			index = closeIndex + 1
		default:
			_, size := utf8.DecodeRuneInString(pattern[index:])
			expression.WriteString(regexp.QuoteMeta(pattern[index : index+size]))
			index += size
		}
	}
	if directory {
		expression.WriteString(`(?:/.*)?`)
	}
	expression.WriteByte('$')
	compiled, err := regexp.Compile(expression.String())
	if err != nil {
		return nil, err
	}
	return compiled, nil
}

func primaryPath(record diffRecord) string {
	if record.newPath != "" {
		return record.newPath
	}
	return record.oldPath
}

func hasPathSegment(path, segment string) bool {
	for value := range strings.SplitSeq(path, "/") {
		if value == segment {
			return true
		}
	}
	return false
}
