package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	maxIgnoreFiles = 100_000
	maxIgnoreBytes = 64 << 20
	maxIgnoreRules = 1_000_000
	maxPolicyLine  = 16 << 10
)

// nativeGitignoreRules reads stable policy files and translates Git's
// directory-relative anchoring into canonical repository-root globs. The
// returned ordered tags are compiled with include, exclude, and rvwignore by
// the single bounded path automaton.
//
//nolint:gocognit // Ordered per-directory Git ignore precedence is assembled in one bounded pass.
func nativeGitignoreRules(
	ctx context.Context,
	session *repositorySession,
	records []diffRecord,
) ([]ignoreRule, error) {
	directories := map[string]struct{}{"": {}}
	for index, record := range records {
		if index%4_096 == 0 {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
		}
		path := primaryPath(record)
		for directory := parentGitPath(path); ; directory = parentGitPath(directory) {
			directories[directory] = struct{}{}
			if directory == "" {
				break
			}
		}
	}
	ordered := make([]string, 0, len(directories))
	for directory := range directories {
		ordered = append(ordered, directory)
	}
	sort.Slice(ordered, func(left, right int) bool {
		leftDepth := strings.Count(ordered[left], "/")
		rightDepth := strings.Count(ordered[right], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return ordered[left] < ordered[right]
	})
	rules := make([]ignoreRule, 0, 128)
	files, bytesRead, sourceRules := uint64(0), uint64(0), uint64(0)
	appendFile := func(contents []byte, domain string) error {
		files++
		bytesRead += uint64(len(contents))
		if files > maxIgnoreFiles {
			return &LimitError{Resource: LimitIgnoreFiles, Limit: maxIgnoreFiles}
		}
		if bytesRead > maxIgnoreBytes {
			return &LimitError{Resource: LimitIgnoreBytes, Limit: maxIgnoreBytes}
		}
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		scanner.Buffer(make([]byte, 4<<10), maxPolicyLine+1)
		for scanner.Scan() {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if len(line) > maxPolicyLine {
				return errors.New("ignore rule exceeds 16 KiB")
			}
			parsed, negated, present, err := parseGitignoreLine(line)
			if err != nil {
				return err
			}
			if !present {
				continue
			}
			sourceRules++
			if sourceRules > maxIgnoreRules {
				return &LimitError{Resource: LimitIgnoreRules, Limit: maxIgnoreRules}
			}
			for _, pattern := range canonicalGitignoreGlobs(domain, parsed) {
				rules = append(rules, ignoreRule{
					pattern: pattern, negated: negated, gitignore: true,
				})
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("parse ignore file: %w", err)
		}
		return nil
	}
	if contents, err := readStableRootFile(session.commonRoot, "info/exclude", maxIgnoreBytes); err == nil {
		if appendErr := appendFile(contents, ""); appendErr != nil {
			return nil, appendErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read Git exclude file: %w", err)
	}
	for _, directory := range ordered {
		name := ".gitignore"
		if directory != "" {
			name = directory + "/.gitignore"
		}
		contents, err := readStableRootFile(session.filesystem, name, maxIgnoreBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if appendErr := appendFile(contents, directory); appendErr != nil {
			return nil, appendErr
		}
	}
	return rules, nil
}

func parseGitignoreLine(line string) (string, bool, bool, error) {
	line = trimUnescapedTrailingSpaces(line)
	if line == "" || line[0] == '#' {
		return "", false, false, nil
	}
	negated := false
	if line[0] == '!' {
		negated = true
		line = line[1:]
	}
	if line == "" {
		return "", false, false, errors.New("git ignore contains an empty negation")
	}
	if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	}
	return line, negated, true, nil
}

func trimUnescapedTrailingSpaces(value string) string {
	for value != "" && value[len(value)-1] == ' ' {
		backslashes := 0
		for index := len(value) - 2; index >= 0 && value[index] == '\\'; index-- {
			backslashes++
		}
		if backslashes%2 != 0 {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}

func canonicalGitignoreGlobs(domain, line string) []string {
	directory := strings.HasSuffix(line, "/")
	line = strings.TrimSuffix(line, "/")
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	hasSlash := strings.Contains(line, "/")
	patterns := make([]string, 0, 4)
	appendPattern := func(pattern string) {
		patterns = append(patterns, pattern)
		if !directory {
			patterns = append(patterns, pattern+"/")
		}
	}
	switch {
	case domain == "" && !anchored && !hasSlash:
		appendPattern(line)
	case domain == "":
		appendPattern("/" + line)
	case anchored || hasSlash:
		appendPattern("/" + domain + "/" + line)
	default:
		appendPattern("/" + domain + "/" + line)
		appendPattern("/" + domain + "/**/" + line)
	}
	return patterns
}

func parentGitPath(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[:index]
	}
	return ""
}
