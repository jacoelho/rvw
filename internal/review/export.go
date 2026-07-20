package review

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const MaxPreparedExportBytes int64 = 16 << 20

var ErrInvalidExport = errors.New("invalid review export")

type ExportFormat uint8

const (
	ExportMarkdown ExportFormat = iota + 1
	ExportJSON
)

type ExportOption uint8

const (
	ExportNotes ExportOption = iota + 1
	ExportPraise
	ExportUnmappable
)

type ExportOptions struct {
	bits uint8
}

func NewExportOptions(values ...ExportOption) (ExportOptions, error) {
	var options ExportOptions
	for _, value := range values {
		if value < ExportNotes || value > ExportUnmappable {
			return ExportOptions{}, fmt.Errorf("%w: unknown option %d", ErrInvalidExport, value)
		}
		options.bits |= 1 << (value - 1)
	}
	return options, nil
}

func (options ExportOptions) Has(value ExportOption) bool {
	return value >= ExportNotes && value <= ExportUnmappable &&
		options.bits&(1<<(value-1)) != 0
}

func (options ExportOptions) Toggle(value ExportOption) (ExportOptions, error) {
	if value < ExportNotes || value > ExportUnmappable {
		return options, fmt.Errorf("%w: unknown option %d", ErrInvalidExport, value)
	}
	options.bits ^= 1 << (value - 1)
	return options, nil
}

type ExportTemplateName uint8

const (
	TemplateComments ExportTemplateName = iota + 1
	TemplateCommentTypes
	TemplateCommentCount
	TemplateIndex
	TemplateKind
	TemplateKindUpper
	TemplateTarget
	TemplatePath
	TemplateLineOrRange
	TemplateComment
)

type exportToken struct {
	literal string
	name    ExportTemplateName
}

type ExportTemplates struct {
	markdown []exportToken
	comment  []exportToken
}

func (templates ExportTemplates) Bytes() int64 {
	var total int64
	for _, token := range templates.markdown {
		total += int64(len(token.literal))
	}
	for _, token := range templates.comment {
		total += int64(len(token.literal))
	}
	return total
}

func CompileExportTemplates(markdown, comment string) (ExportTemplates, error) {
	markdownTokens, err := compileTemplate(markdown, map[string]ExportTemplateName{
		"comments":      TemplateComments,
		"comment_types": TemplateCommentTypes,
		"comment_count": TemplateCommentCount,
	})
	if err != nil {
		return ExportTemplates{}, fmt.Errorf("compile Markdown export template: %w", err)
	}
	commentTokens, err := compileTemplate(comment, map[string]ExportTemplateName{
		"index":         TemplateIndex,
		"kind":          TemplateKind,
		"kind_upper":    TemplateKindUpper,
		"target":        TemplateTarget,
		"path":          TemplatePath,
		"line_or_range": TemplateLineOrRange,
		"comment":       TemplateComment,
	})
	if err != nil {
		return ExportTemplates{}, fmt.Errorf("compile comment export template: %w", err)
	}
	return ExportTemplates{markdown: markdownTokens, comment: commentTokens}, nil
}

type PreparedExport struct {
	format ExportFormat
	body   string
}

func (prepared PreparedExport) Format() ExportFormat {
	return prepared.format
}

func (prepared PreparedExport) Body() string {
	return prepared.body
}

func (prepared PreparedExport) Bytes() int64 {
	return int64(len(prepared.body))
}

func (review Review) PrepareMarkdown(
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	return review.PrepareMarkdownCancellable(nil, templates, options)
}

func (review Review) PrepareMarkdownCancellable(
	cancelled func() error,
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	if len(templates.markdown) == 0 || len(templates.comment) == 0 {
		return PreparedExport{}, fmt.Errorf("%w: export templates are empty", ErrInvalidExport)
	}
	selected := review.exportComments(options)
	var commentBlockBytes int64
	for index, comment := range selected {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedExport{}, err
			}
		}
		values := commentTemplateValues(comment, index+1)
		separator := int64(0)
		if index != 0 {
			separator = 1
		}
		size, err := renderedTemplateSize(
			templates.comment,
			values,
			MaxPreparedExportBytes-commentBlockBytes-separator,
		)
		if err != nil {
			return PreparedExport{}, err
		}
		commentBlockBytes += separator + size
	}
	if len(selected) == 0 {
		commentBlockBytes = int64(len("_No comments._"))
	}
	commentTypes := includedCommentKinds(review.kinds, options)
	values := map[ExportTemplateName]string{
		TemplateCommentTypes: strings.Join(commentTypes, ", "),
		TemplateCommentCount: strconv.Itoa(len(selected)),
	}
	bodyBytes, err := markdownExportSize(
		templates.markdown,
		values,
		commentBlockBytes,
	)
	if err != nil {
		return PreparedExport{}, err
	}
	var body strings.Builder
	body.Grow(int(bodyBytes))
	work := 0
	for _, token := range templates.markdown {
		if err := exportCancellation(cancelled, &work); err != nil {
			return PreparedExport{}, err
		}
		if token.name != TemplateComments {
			writeExportToken(&body, token, values)
			continue
		}
		if len(selected) == 0 {
			body.WriteString("_No comments._")
			continue
		}
		for index, comment := range selected {
			if err := exportCancellation(cancelled, &work); err != nil {
				return PreparedExport{}, err
			}
			if index != 0 {
				body.WriteByte('\n')
			}
			commentValues := commentTemplateValues(comment, index+1)
			if err := writeExportTemplate(
				&body,
				templates.comment,
				commentValues,
				cancelled,
				&work,
			); err != nil {
				return PreparedExport{}, err
			}
		}
	}
	return PreparedExport{format: ExportMarkdown, body: body.String()}, nil
}

func markdownExportSize(
	tokens []exportToken,
	values map[ExportTemplateName]string,
	commentBytes int64,
) (int64, error) {
	var size int64
	for _, token := range tokens {
		addition := int64(len(token.literal))
		if token.name == TemplateComments {
			addition += commentBytes
		} else {
			addition += int64(len(values[token.name]))
		}
		if addition > MaxPreparedExportBytes-size {
			return 0, fmt.Errorf(
				"%w: prepared body exceeds %d bytes",
				ErrInvalidExport,
				MaxPreparedExportBytes,
			)
		}
		size += addition
	}
	return size, nil
}

func renderedTemplateSize(
	tokens []exportToken,
	values map[ExportTemplateName]string,
	limit int64,
) (int64, error) {
	var size int64
	for _, token := range tokens {
		addition := int64(len(token.literal) + len(values[token.name]))
		if addition > limit-size {
			return 0, fmt.Errorf(
				"%w: prepared body exceeds %d bytes",
				ErrInvalidExport,
				MaxPreparedExportBytes,
			)
		}
		size += addition
	}
	return size, nil
}

func writeExportTemplate(
	body *strings.Builder,
	tokens []exportToken,
	values map[ExportTemplateName]string,
	cancelled func() error,
	work *int,
) error {
	for _, token := range tokens {
		if err := exportCancellation(cancelled, work); err != nil {
			return err
		}
		writeExportToken(body, token, values)
	}
	return nil
}

func writeExportToken(
	body *strings.Builder,
	token exportToken,
	values map[ExportTemplateName]string,
) {
	if token.name == 0 {
		body.WriteString(token.literal)
	} else {
		body.WriteString(values[token.name])
	}
}

func exportCancellation(cancelled func() error, work *int) error {
	if *work%256 == 0 {
		if err := cancellationError(cancelled); err != nil {
			return err
		}
	}
	*work++
	return nil
}

func (review Review) exportComments(options ExportOptions) []Comment {
	selected := make([]Comment, 0, len(review.comments))
	for _, comment := range review.comments {
		if !options.Has(ExportUnmappable) && comment.Unmappable() ||
			!options.Has(ExportNotes) && comment.Kind() == CommentKindNote ||
			!options.Has(ExportPraise) && comment.Kind() == CommentKindPraise {
			continue
		}
		selected = append(selected, comment)
	}
	return selected
}

const (
	CommentKindNote   CommentKind = "note"
	CommentKindPraise CommentKind = "praise"
)

func compileTemplate(
	value string,
	allowed map[string]ExportTemplateName,
) ([]exportToken, error) {
	var tokens []exportToken
	for len(value) > 0 {
		open := strings.Index(value, "{{")
		if open < 0 {
			tokens = append(tokens, exportToken{literal: value})
			break
		}
		if open > 0 {
			tokens = append(tokens, exportToken{literal: value[:open]})
		}
		value = value[open+2:]
		closeIndex := strings.Index(value, "}}")
		if closeIndex < 0 {
			return nil, fmt.Errorf("%w: unterminated placeholder", ErrInvalidExport)
		}
		name := value[:closeIndex]
		typedName, ok := allowed[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown placeholder %q", ErrInvalidExport, name)
		}
		tokens = append(tokens, exportToken{name: typedName})
		if len(tokens) > 4_096 {
			return nil, fmt.Errorf("%w: template exceeds 4096 tokens", ErrInvalidExport)
		}
		value = value[closeIndex+2:]
	}
	return tokens, nil
}

func renderTemplate(
	tokens []exportToken,
	values map[ExportTemplateName]string,
	limit int64,
) (string, error) {
	var size int64
	for _, token := range tokens {
		size += int64(len(token.literal))
		size += int64(len(values[token.name]))
		if size > limit {
			return "", fmt.Errorf("%w: prepared body exceeds %d bytes", ErrInvalidExport, limit)
		}
	}
	var body strings.Builder
	body.Grow(int(size))
	for _, token := range tokens {
		if token.name == 0 {
			body.WriteString(token.literal)
		} else {
			body.WriteString(values[token.name])
		}
	}
	return body.String(), nil
}

func commentTemplateValues(comment Comment, index int) map[ExportTemplateName]string {
	target, path, lineRange := exportTarget(comment.Target())
	return map[ExportTemplateName]string{
		TemplateIndex:       strconv.Itoa(index),
		TemplateKind:        comment.Kind().String(),
		TemplateKindUpper:   asciiUpper(comment.Kind().String()),
		TemplateTarget:      markdownEscape(target),
		TemplatePath:        markdownEscape(path),
		TemplateLineOrRange: lineRange,
		TemplateComment:     markdownEscape(comment.Body().String()),
	}
}

func exportTarget(target CommentTarget) (string, string, string) {
	if target.Kind() == TargetReview {
		return "review", "", ""
	}
	path := ""
	if anchored, ok := target.AnchoredPath(); ok {
		path = anchored.Display()
	}
	if target.Kind() == TargetFile {
		return path, path, ""
	}
	side, start, end, ok := target.Lines()
	if !ok {
		return path, path, ""
	}
	prefix := ""
	if side == LineOld {
		prefix = "~"
	}
	lineRange := prefix + strconv.FormatUint(uint64(start.Uint32()), 10)
	if start != end {
		lineRange += "-" + strconv.FormatUint(uint64(end.Uint32()), 10)
	}
	return path + ":" + lineRange, path, lineRange
}

func includedCommentKinds(kinds []CommentKind, options ExportOptions) []string {
	values := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if !options.Has(ExportNotes) && kind == CommentKindNote ||
			!options.Has(ExportPraise) && kind == CommentKindPraise {
			continue
		}
		values = append(values, asciiUpper(kind.String()))
	}
	slices.Sort(values)
	return values
}

func asciiUpper(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, character := range value {
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		}
		result.WriteRune(character)
	}
	return result.String()
}

func markdownEscape(value string) string {
	const punctuation = `\` + "`*_{}[]()#+-.!|>"
	var result strings.Builder
	result.Grow(len(value))
	for _, character := range value {
		if strings.ContainsRune(punctuation, character) {
			result.WriteByte('\\')
		}
		result.WriteRune(character)
	}
	return result.String()
}
