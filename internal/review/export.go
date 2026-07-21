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

// ExportInput is the immutable export-owned projection of a review. It keeps
// export work independent from presentation patches, search results,
// publication state, and operation history.
type ExportInput struct {
	kinds         []CommentKind
	comments      []exportComment
	retainedBytes int64
	ready         bool
}

type exportComment struct {
	kind       CommentKind
	target     exportCommentTarget
	body       CommentBody
	stale      bool
	unmappable bool
}

type exportCommentTarget struct {
	kind    TargetKind
	path    Path
	hasPath bool
	side    LineSide
	start   LineNumber
	end     LineNumber
}

const (
	// These conservative charges cover the projection element layouts on all
	// supported targets. A test compares them with the compiled layouts.
	exportKindMetadataBytes    int64 = 16
	exportCommentMetadataBytes int64 = 80
)

func (review Review) ExportInput() ExportInput {
	kinds := slices.Clip(slices.Clone(review.kinds))
	comments := make([]exportComment, len(review.comments))
	retained := int64(len(kinds)) * exportKindMetadataBytes
	for _, kind := range kinds {
		retained += int64(len(kind))
	}
	retained += int64(len(comments)) * exportCommentMetadataBytes
	for index, comment := range review.comments {
		target := comment.Target()
		projectedTarget := exportCommentTarget{kind: target.Kind()}
		if path, ok := target.AnchoredPath(); ok {
			projectedTarget.path = path
			projectedTarget.hasPath = true
			retained += int64(len(path.raw))
		}
		projectedTarget.side, projectedTarget.start, projectedTarget.end, _ = target.Lines()
		comments[index] = exportComment{
			kind:       comment.Kind(),
			target:     projectedTarget,
			body:       comment.Body(),
			stale:      comment.Stale(),
			unmappable: comment.Unmappable(),
		}
		retained += int64(len(comments[index].kind) + len(comments[index].body))
	}
	return ExportInput{
		kinds:         kinds,
		comments:      comments,
		retainedBytes: retained,
		ready:         true,
	}
}

func (input ExportInput) RetainedBytes() int64 {
	if !input.ready {
		return 0
	}
	return input.retainedBytes
}

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
	markdown      []exportToken
	comment       []exportToken
	retainedBytes int64
	ready         bool
}

const exportTokenMetadataBytes int64 = 24

func (templates ExportTemplates) Ready() bool {
	return templates.ready
}

func (templates ExportTemplates) RetainedBytes() int64 {
	if !templates.ready {
		return 0
	}
	return templates.retainedBytes
}

func CompileExportTemplates(markdown, comment string) (ExportTemplates, error) {
	// Each compiled template owns source backing with exactly the submitted
	// length. Literal tokens may then safely reference it without retaining a
	// larger caller allocation.
	markdown = strings.Clone(markdown)
	comment = strings.Clone(comment)
	markdownTokens, err := compileTemplate(markdown, map[string]ExportTemplateName{
		"comments":      TemplateComments,
		"comment_types": TemplateCommentTypes,
		"comment_count": TemplateCommentCount,
	})
	if err != nil {
		return ExportTemplates{}, fmt.Errorf("compile Markdown export template: %w", err)
	}
	if len(markdownTokens) == 0 {
		return ExportTemplates{}, fmt.Errorf("compile Markdown export template: %w: template is empty", ErrInvalidExport)
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
	if len(commentTokens) == 0 {
		return ExportTemplates{}, fmt.Errorf("compile comment export template: %w: template is empty", ErrInvalidExport)
	}
	return ExportTemplates{
		markdown: markdownTokens,
		comment:  commentTokens,
		retainedBytes: int64(len(markdown)+len(comment)) +
			int64(len(markdownTokens)+len(commentTokens))*exportTokenMetadataBytes,
		ready: true,
	}, nil
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
	return review.ExportInput().PrepareMarkdownCancellable(nil, templates, options)
}

func (review Review) PrepareMarkdownCancellable(
	cancelled func() error,
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	return review.ExportInput().PrepareMarkdownCancellable(cancelled, templates, options)
}

func (input ExportInput) PrepareMarkdownCancellable(
	cancelled func() error,
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	if !input.ready {
		return PreparedExport{}, fmt.Errorf("%w: export input is unavailable", ErrInvalidExport)
	}
	if !templates.Ready() {
		return PreparedExport{}, fmt.Errorf("%w: export templates are empty", ErrInvalidExport)
	}
	selected := input.exportComments(options)
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
	commentTypes := includedCommentKinds(input.kinds, options)
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

func (input ExportInput) exportComments(options ExportOptions) []exportComment {
	selected := make([]exportComment, 0, len(input.comments))
	for _, comment := range input.comments {
		if !options.Has(ExportUnmappable) && comment.unmappable ||
			!options.Has(ExportNotes) && comment.kind == CommentKindNote ||
			!options.Has(ExportPraise) && comment.kind == CommentKindPraise {
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
	if len(tokens) > 4_096 {
		return nil, fmt.Errorf("%w: template exceeds 4096 tokens", ErrInvalidExport)
	}
	return slices.Clip(tokens), nil
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

func commentTemplateValues(comment exportComment, index int) map[ExportTemplateName]string {
	target, path, lineRange := exportTarget(comment.target)
	return map[ExportTemplateName]string{
		TemplateIndex:       strconv.Itoa(index),
		TemplateKind:        comment.kind.String(),
		TemplateKindUpper:   asciiUpper(comment.kind.String()),
		TemplateTarget:      markdownEscape(target),
		TemplatePath:        markdownEscape(path),
		TemplateLineOrRange: lineRange,
		TemplateComment:     markdownEscape(comment.body.String()),
	}
}

func exportTarget(target exportCommentTarget) (string, string, string) {
	if target.kind == TargetReview {
		return "review", "", ""
	}
	path := ""
	if target.hasPath {
		path = target.path.Display()
	}
	if target.kind == TargetFile {
		return path, path, ""
	}
	if target.kind != TargetLine {
		return path, path, ""
	}
	prefix := ""
	if target.side == LineOld {
		prefix = "~"
	}
	lineRange := prefix + strconv.FormatUint(uint64(target.start.Uint32()), 10)
	if target.start != target.end {
		lineRange += "-" + strconv.FormatUint(uint64(target.end.Uint32()), 10)
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
