package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gitattributesformat "github.com/go-git/go-git/v5/plumbing/format/gitattributes"
)

type nativeAttributeCache struct {
	files     map[string][]gitattributesformat.MatchAttribute
	missing   map[string]struct{}
	fileCount uint64
	bytes     uint64
	rules     uint64
}

type nativeConversion struct {
	normalizeEOL bool
	autoText     bool
	ident        bool
}

func (session *repositorySession) attributesForPath(
	ctx context.Context,
	path string,
) (map[string]gitattributesformat.Attribute, error) {
	if session.attributes.files == nil {
		session.attributes.files = make(map[string][]gitattributesformat.MatchAttribute)
		session.attributes.missing = make(map[string]struct{})
	}
	stack := make([]gitattributesformat.MatchAttribute, 0, 16)
	directories := make([]string, 0, strings.Count(path, "/")+1)
	for directory := parentGitPath(path); ; directory = parentGitPath(directory) {
		directories = append(directories, directory)
		if directory == "" {
			break
		}
	}
	for left, right := 0, len(directories)-1; left < right; left, right = left+1, right-1 {
		directories[left], directories[right] = directories[right], directories[left]
	}
	for _, directory := range directories {
		name := ".gitattributes"
		var domain []string
		if directory != "" {
			name = directory + "/.gitattributes"
			domain = strings.Split(directory, "/")
		}
		patterns, err := session.readAttributePatterns(
			ctx, session.filesystem, name, domain, directory == "",
		)
		if err != nil {
			return nil, err
		}
		stack = append(stack, patterns...)
	}
	info, err := session.readAttributePatterns(
		ctx, session.commonRoot, "info/attributes", nil, true,
	)
	if err != nil {
		return nil, err
	}
	stack = append(stack, info...)
	matcher := gitattributesformat.NewMatcher(stack)
	attributes, _ := matcher.Match(strings.Split(path, "/"), []string{
		"filter", "working-tree-encoding", "text", "eol", "ident", "diff",
	})
	return attributes, nil
}

func (session *repositorySession) readAttributePatterns(
	ctx context.Context,
	root *os.Root,
	name string,
	domain []string,
	allowMacro bool,
) ([]gitattributesformat.MatchAttribute, error) {
	key := root.Name() + "\x00" + name
	if patterns, ok := session.attributes.files[key]; ok {
		return patterns, nil
	}
	if _, missing := session.attributes.missing[key]; missing {
		return nil, nil
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	remaining := int64(maxIgnoreBytes - session.attributes.bytes)
	contents, err := readStableRootFile(root, name, remaining)
	if errors.Is(err, os.ErrNotExist) {
		session.attributes.missing[key] = struct{}{}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read attribute file %q: %w", name, err)
	}
	session.attributes.fileCount++
	session.attributes.bytes += uint64(len(contents))
	if session.attributes.fileCount > 100_000 {
		return nil, &LimitError{Resource: LimitAttributeFiles, Limit: 100_000}
	}
	if session.attributes.bytes > 64<<20 {
		return nil, &LimitError{Resource: LimitAttributeBytes, Limit: 64 << 20}
	}
	original := contents
	for len(contents) != 0 {
		line, remaining, found := bytes.Cut(contents, []byte{'\n'})
		if len(line) > maxPolicyLine {
			return nil, errors.New("attribute rule exceeds 16 KiB")
		}
		contents = remaining
		if !found {
			break
		}
	}
	patterns, err := gitattributesformat.ReadAttributes(bytes.NewReader(original), domain, allowMacro)
	if err != nil {
		return nil, fmt.Errorf("parse attribute file %q: %w", name, err)
	}
	session.attributes.rules += uint64(len(patterns))
	if session.attributes.rules > 1_000_000 {
		return nil, &LimitError{Resource: LimitAttributeRules, Limit: 1_000_000}
	}
	session.attributes.files[key] = patterns
	return patterns, nil
}

//nolint:cyclop // Attribute precedence is an explicit closed Git conversion matrix.
func (session *repositorySession) nativeConversionForPath(
	ctx context.Context,
	path string,
) (nativeConversion, error) {
	attributes, err := session.attributesForPath(ctx, path)
	if err != nil {
		return nativeConversion{}, err
	}
	if encoding, ok := attributes["working-tree-encoding"]; ok && encoding.IsValueSet() &&
		!nativeUTF8Encoding(encoding.Value()) {
		return nativeConversion{}, fmt.Errorf(
			"working-tree encoding %q for %q is unsupported; only UTF-8 identity is supported",
			encoding.Value(), path,
		)
	}
	filter, ok := attributes["filter"]
	if ok && !filter.IsUnset() && !filter.IsUnspecified() && filter.IsValueSet() {
		driver := filter.Value()
		clean, hasClean := session.config.value("filter", driver, "clean")
		process, hasProcess := session.config.value("filter", driver, "process")
		if hasClean && clean != "" || hasProcess && process != "" {
			if driver == "unspecified" || driver == "unset" {
				return nativeConversion{}, fmt.Errorf(
					"filter attribute is ambiguous with configured driver name %q", driver,
				)
			}
			return nativeConversion{}, fmt.Errorf(
				"worktree clean/process filters are unsupported for %q", path,
			)
		}
		requiredText, required := session.config.value("filter", driver, "required")
		if required {
			isRequired, err := parseGitBoolean(requiredText)
			if err != nil {
				return nativeConversion{}, err
			}
			if isRequired {
				return nativeConversion{}, fmt.Errorf(
					"required filter driver %q is unavailable for %q", driver, path,
				)
			}
		}
	}
	conversion := nativeConversion{}
	if text, ok := attributes["text"]; ok {
		switch {
		case text.IsUnset():
			conversion.normalizeEOL = false
		case text.IsSet():
			conversion.normalizeEOL = true
		case text.IsValueSet() && strings.EqualFold(text.Value(), "auto"):
			conversion.normalizeEOL = true
			conversion.autoText = true
		}
	} else if autocrlf, ok := session.config.value("core", "", "autocrlf"); ok {
		switch strings.ToLower(strings.TrimSpace(autocrlf)) {
		case gitBooleanTrue, "input":
			conversion.normalizeEOL = true
			conversion.autoText = true
		case gitBooleanFalse, "":
		default:
			return nativeConversion{}, fmt.Errorf("invalid core.autocrlf value %q", autocrlf)
		}
	}
	if eol, ok := attributes["eol"]; ok && eol.IsValueSet() {
		if !strings.EqualFold(eol.Value(), "lf") && !strings.EqualFold(eol.Value(), "crlf") {
			return nativeConversion{}, fmt.Errorf("unsupported eol attribute %q", eol.Value())
		}
		conversion.normalizeEOL = true
		conversion.autoText = false
	}
	if coreEOL, ok := session.config.value("core", "", "eol"); ok {
		switch strings.ToLower(strings.TrimSpace(coreEOL)) {
		case "", "native", "lf", "crlf":
		default:
			return nativeConversion{}, fmt.Errorf("invalid core.eol value %q", coreEOL)
		}
	}
	if safeCRLF, ok := session.config.value("core", "", "safecrlf"); ok {
		if !strings.EqualFold(strings.TrimSpace(safeCRLF), "warn") {
			if _, err := parseGitBoolean(safeCRLF); err != nil {
				return nativeConversion{}, fmt.Errorf("core.safecrlf: %w", err)
			}
		}
	}
	if ident, ok := attributes["ident"]; ok && ident.IsSet() {
		conversion.ident = true
	}
	return conversion, nil
}

func nativeUTF8Encoding(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	return normalized == "utf-8" || normalized == "utf8"
}
