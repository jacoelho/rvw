package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

const (
	maxFilterFileBytes     int64 = 256 << 10
	maxGitIgnoreBatchBytes int64 = 8 << 20
)

type gitignoreMode uint8

const (
	skipGitignore gitignoreMode = iota
	useGitignore
)

type ignoreRule struct {
	pattern *regexp.Regexp
	negated bool
}

type filterSet struct {
	include []*regexp.Regexp
	exclude []*regexp.Regexp
	rvw     []ignoreRule
	options OpenOptions
}

func newFilterSet(repository RepositoryPath, options OpenOptions) (filterSet, error) {
	set := filterSet{options: options}
	var err error
	set.include, err = compilePatterns(options.Include)
	if err != nil {
		return filterSet{}, fmt.Errorf("compile include patterns: %w", err)
	}
	set.exclude, err = compilePatterns(options.Exclude)
	if err != nil {
		return filterSet{}, fmt.Errorf("compile exclude patterns: %w", err)
	}
	if options.RVWIgnore && options.IgnoreFile != "" {
		set.rvw, err = readIgnoreRules(repository, options.IgnoreFile)
		if err != nil {
			return filterSet{}, err
		}
	}
	return set, nil
}

func (service *Service) gitignored(
	ctx context.Context,
	repository RepositoryPath,
	environment map[string]string,
	records []diffRecord,
	mode gitignoreMode,
) ([]bool, error) {
	ignored := make([]bool, len(records))
	if mode != useGitignore || len(records) == 0 {
		return ignored, nil
	}

	for start := 0; start < len(records); {
		var input bytes.Buffer
		pathToIndexes := make(map[string][]int)
		end := start
		for end < len(records) {
			path := primaryPath(records[end])
			if input.Len() != 0 &&
				int64(input.Len())+int64(len(path))+1 > maxGitIgnoreBatchBytes {
				break
			}
			input.Write(path)
			input.WriteByte(0)
			pathToIndexes[string(path)] = append(pathToIndexes[string(path)], end)
			end++
		}
		result, err := service.runner.Run(ctx, subprocessRequest(
			repository,
			environment,
			[]string{"check-ignore", "--no-index", "-z", gitStdin},
			input.Bytes(),
			[]int{0, 1},
		))
		if err != nil {
			return nil, fmt.Errorf("evaluate .gitignore: %w", err)
		}
		for output := result.Stdout; len(output) > 0; {
			path, remaining, ok := nextNULTerminated(output)
			if !ok {
				return nil, errors.New("git check-ignore returned an unterminated path")
			}
			output = remaining
			indexes, exists := pathToIndexes[string(path)]
			if !exists {
				return nil, fmt.Errorf("git check-ignore returned an unknown path")
			}
			for _, index := range indexes {
				ignored[index] = true
			}
		}
		start = end
	}
	return ignored, nil
}

func subprocessRequest(
	repository RepositoryPath,
	environment map[string]string,
	arguments []string,
	stdin []byte,
	acceptedExits []int,
) subprocess.Request {
	merged := baseGitEnvironment()
	maps.Copy(merged, environment)
	return subprocess.Request{
		Name:            "git",
		Args:            arguments,
		Dir:             repository.String(),
		Env:             merged,
		Stdin:           stdin,
		AcceptedExits:   acceptedExits,
		OperationDetail: "git check-ignore",
	}
}

type gitignoreMatch uint8

const (
	notGitignored gitignoreMatch = iota
	gitignored
)

func (set filterSet) flagsFor(record diffRecord, ignored gitignoreMatch) (review.Flags, error) {
	rawPath := primaryPath(record)
	path, validUTF8 := string(rawPath), utf8.Valid(rawPath)
	generated, vendored, lockFile, minified := false, false, false, false
	if validUTF8 {
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
	if set.options.BuiltinDefaults && (generated || vendored || lockFile || minified) {
		reasons = append(reasons, review.HiddenBuiltinDefault)
	}
	if ignored == gitignored {
		reasons = append(reasons, review.HiddenGitignore)
	}
	if validUTF8 && matchesIgnoreRules(set.rvw, path) {
		reasons = append(reasons, review.HiddenRVWIgnore)
	}
	if len(set.include) != 0 && (!validUTF8 || !matchesAny(set.include, path)) {
		reasons = append(reasons, review.HiddenInclude)
	}
	if validUTF8 && matchesAny(set.exclude, path) {
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

func readIgnoreRules(
	repository RepositoryPath,
	name IgnoreFile,
) (rules []ignoreRule, returnErr error) {
	root, err := os.OpenRoot(repository.String())
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	file, err := root.Open(name.String())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	contents, err := io.ReadAll(io.LimitReader(file, maxFilterFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(contents)) > maxFilterFileBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxFilterFileBytes)
	}
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("%s is not valid UTF-8", name)
	}

	for line := range strings.Lines(string(contents)) {
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
		compiled, err := compileGlob(line)
		if err != nil {
			return nil, fmt.Errorf("%s pattern %q: %w", name, line, err)
		}
		rules = append(rules, ignoreRule{pattern: compiled, negated: negated})
		if len(rules) > 4_096 {
			return nil, fmt.Errorf("%s contains more than 4096 patterns", name)
		}
	}
	return rules, nil
}

func compilePatterns(patterns []FilterPattern) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, len(patterns))
	for index, pattern := range patterns {
		value, err := compileGlob(pattern.String())
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", pattern, err)
		}
		compiled[index] = value
	}
	return compiled, nil
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

func matchesAny(patterns []*regexp.Regexp, path string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func matchesIgnoreRules(rules []ignoreRule, path string) bool {
	matched := false
	for _, rule := range rules {
		if rule.pattern.MatchString(path) {
			matched = !rule.negated
		}
	}
	return matched
}

func primaryPath(record diffRecord) []byte {
	if record.newPath != nil {
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
