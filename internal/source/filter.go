package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	maxFilterFileBytes        int64 = 256 << 10
	maxGitIgnoreBatchBytes    int64 = 8 << 20
	filterCancellationRecords       = 2_048
)

type ignoreRule struct {
	pattern   string
	negated   bool
	gitignore bool
}

type filterProjection struct {
	flags []change.Flags
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
	rules []ignoreRule,
	ledger *sourceLedger,
) (filterProjection, error) {
	if ledger == nil {
		return filterProjection{}, errors.New("filter projection ownership is invalid")
	}
	matcher, err := newCompiledFilterSet(ctx, ledger, options.Include, options.Exclude, rules)
	if err != nil {
		return filterProjection{}, err
	}
	defer matcher.Close()
	projection := filterProjection{flags: make([]change.Flags, len(records))}
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
		if match.Has(filterMatchGitignore) {
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

func filterFlags(
	path string,
	facts filterPathFacts,
	options OpenOptions,
	match filterMatchFlags,
) (change.Flags, error) {
	if !match.valid() {
		return change.Flags{}, errors.New("filter match flags are invalid")
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

	var reasons []change.HiddenReason
	if options.BuiltinDefaults && (generated || vendored || lockFile || minified) {
		reasons = append(reasons, change.HiddenBuiltinDefault)
	}
	if facts.gitignore == filterGitignored {
		reasons = append(reasons, change.HiddenGitignore)
	}
	if match.Has(filterMatchIgnore) {
		reasons = append(reasons, change.HiddenRVWIgnore)
	}
	if len(options.Include) != 0 && !match.Has(filterMatchInclude) {
		reasons = append(reasons, change.HiddenInclude)
	}
	if match.Has(filterMatchExclude) {
		reasons = append(reasons, change.HiddenExclude)
	}
	traitValues := make([]change.FileTrait, 0, 4)
	if generated {
		traitValues = append(traitValues, change.TraitGenerated)
	}
	if vendored {
		traitValues = append(traitValues, change.TraitVendored)
	}
	if lockFile {
		traitValues = append(traitValues, change.TraitLockFile)
	}
	if minified {
		traitValues = append(traitValues, change.TraitMinified)
	}
	traits, err := change.NewFileTraits(traitValues...)
	if err != nil {
		return change.Flags{}, err
	}
	hidden, err := change.NewHiddenReasons(reasons...)
	if err != nil {
		return change.Flags{}, err
	}
	return change.NewFlags(traits, hidden)
}

func readIgnoreRules(
	repository *repositorySession,
	name IgnoreFile,
) ([]ignoreRule, error) {
	contents, err := readIgnoreRuleContents(repository, name)
	if err != nil {
		return nil, err
	}
	return parseIgnoreRules(context.Background(), name, contents)
}

func readIgnoreRuleContents(
	repository *repositorySession,
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
