package review

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxPreparedExportBytes defines the supported prepared export bytes limit.
const MaxPreparedExportBytes int64 = 16 << 20

// ErrInvalidExport identifies an invalid export failure.
var ErrInvalidExport = errors.New("invalid review export")

// ExportFormat describes export format.
type ExportFormat uint8

// ExportMarkdown identifies a supported export markdown value.
const (
	ExportMarkdown ExportFormat = iota + 1
	ExportJSON
)

// ExportInput is the immutable export-owned projection of a review. It keeps
// export work independent from presentation patches, search results,
// publication state, and operation history.
type ExportInput struct {
	comments      []exportComment
	retainedBytes int64
	ready         bool
}

// ExportSource is a narrow immutable view of the comments needed for export.
type ExportSource struct {
	comments commentSource
}

// ExportSource returns an O(1) immutable comment source for worker projection.
func (review Review) ExportSource() ExportSource {
	return ExportSource{comments: review.source()}
}

// Ready reports whether the export source was constructed by Review.
func (source ExportSource) Ready() bool {
	return source.comments.ready()
}

// RetainedBytes returns the immutable comment ownership prolonged by export.
func (source ExportSource) RetainedBytes() int64 {
	if !source.Ready() {
		return 0
	}
	return source.comments.retainedBytes + 64
}

// ConstructionBytes returns a conservative simultaneous source, projected
// input, and prepared-export charge for coordinator admission.
func (source ExportSource) ConstructionBytes() int64 {
	if !source.Ready() {
		return 0
	}
	projected := source.comments.retainedBytes +
		int64(len(source.comments.comments))*exportCommentMetadataBytes + 64
	return source.RetainedBytes() + projected + 2*MaxPreparedExportBytes
}

// ProjectCancellable builds the export-owned projection off the event loop.
func (source ExportSource) ProjectCancellable(cancelled func() error) (ExportInput, error) {
	if !source.Ready() {
		return ExportInput{}, ErrInvalidExport
	}
	comments := make([]exportComment, len(source.comments.comments))
	retained := int64(cap(comments)) * exportCommentMetadataBytes
	for index, comment := range source.comments.comments {
		if index%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return ExportInput{}, err
			}
		}
		target := comment.Target()
		projectedTarget := exportCommentTarget{kind: target.Kind()}
		if path, ok := target.AnchoredPath(); ok {
			projectedTarget.path = path
			projectedTarget.hasPath = true
			retained += int64(len(path.raw))
		}
		projectedTarget.side, projectedTarget.start, projectedTarget.end, _ = target.Lines()
		comments[index] = exportComment{
			target:     projectedTarget,
			body:       comment.Body(),
			stale:      comment.Stale(),
			unmappable: comment.Unmappable(),
		}
		retained += int64(len(comments[index].body))
	}
	if err := cancellationError(cancelled); err != nil {
		return ExportInput{}, err
	}
	return ExportInput{
		comments: comments, retainedBytes: retained, ready: true,
	}, nil
}

type exportComment struct {
	body       CommentBody
	target     exportCommentTarget
	stale      bool
	unmappable bool
}

type exportCommentTarget struct {
	path    Path
	start   LineNumber
	end     LineNumber
	kind    TargetKind
	hasPath bool
	side    LineSide
}

const (
	// These conservative charges cover the projection element layouts on all
	// supported targets. A test compares them with the compiled layouts.
	exportCommentMetadataBytes int64 = 72
)

// ExportInput returns the receiver's export input value.
func (review Review) ExportInput() ExportInput {
	input, err := review.ExportSource().ProjectCancellable(nil)
	if err != nil {
		panic("valid review export source failed synchronous projection")
	}
	return input
}

// RetainedBytes returns the receiver's retained bytes value.
func (input ExportInput) RetainedBytes() int64 {
	if !input.ready {
		return 0
	}
	return input.retainedBytes
}

// Ready reports the receiver's ready state.
func (input ExportInput) Ready() bool {
	return input.ready
}

// ExportOption represents export option.
type ExportOption uint8

// ExportUnmappable identifies unmappable comments included in an export.
const ExportUnmappable ExportOption = 1

// ExportOptions describes export options.
type ExportOptions struct {
	bits        uint8
	initialized bool
}

// NewExportOptions constructs and validates export options.
func NewExportOptions(values ...ExportOption) (ExportOptions, error) {
	options := ExportOptions{initialized: true}
	for _, value := range values {
		if value != ExportUnmappable {
			return ExportOptions{}, fmt.Errorf("%w: unknown option %d", ErrInvalidExport, value)
		}
		options.bits |= 1 << (value - 1)
	}
	return options, nil
}

// DefaultExportOptions returns the accepted interactive export selection.
func DefaultExportOptions() ExportOptions {
	options, err := NewExportOptions()
	if err != nil {
		panic("valid default export options were rejected")
	}
	return options
}

// Ready reports whether the options were explicitly constructed.
func (options ExportOptions) Ready() bool { return options.valid() }

// Has performs the has operation.
func (options ExportOptions) Has(value ExportOption) bool {
	return options.valid() && value == ExportUnmappable &&
		options.bits&(1<<(value-1)) != 0
}

// Toggle performs the toggle operation.
func (options ExportOptions) Toggle(value ExportOption) (ExportOptions, error) {
	if !options.valid() || value != ExportUnmappable {
		return options, fmt.Errorf("%w: unknown option %d", ErrInvalidExport, value)
	}
	options.bits ^= 1 << (value - 1)
	return options, nil
}

func (options ExportOptions) valid() bool {
	const validBits = 1<<ExportUnmappable - 1
	return options.initialized && options.bits&^validBits == 0
}

// ExportTemplateName represents export template name.
type ExportTemplateName uint8

// TemplateComments identifies a supported template comments value.
const (
	TemplateComments ExportTemplateName = iota + 1
	TemplateCommentCount
	TemplateIndex
	TemplateTarget
	TemplatePath
	TemplateLineOrRange
	TemplateComment
)

type exportToken struct {
	literal string
	name    ExportTemplateName
}

// ExportTemplates represents export templates.
type ExportTemplates struct {
	markdown      []exportToken
	comment       []exportToken
	retainedBytes int64
	ready         bool
}

const exportTokenMetadataBytes int64 = 24

// Ready reports the receiver's ready state.
func (templates ExportTemplates) Ready() bool {
	return templates.ready
}

// RetainedBytes returns the receiver's retained bytes value.
func (templates ExportTemplates) RetainedBytes() int64 {
	if !templates.ready {
		return 0
	}
	return templates.retainedBytes
}

// CompileExportTemplates compiles export templates.
func CompileExportTemplates(markdown, comment string) (ExportTemplates, error) {
	// Each compiled template owns source backing with exactly the submitted
	// length. Literal tokens may then safely reference it without retaining a
	// larger caller allocation.
	markdown = strings.Clone(markdown)
	comment = strings.Clone(comment)
	markdownTokens, err := compileTemplate(markdown, map[string]ExportTemplateName{
		"comments":      TemplateComments,
		"comment_count": TemplateCommentCount,
	})
	if err != nil {
		return ExportTemplates{}, fmt.Errorf("compile Markdown export template: %w", err)
	}
	if len(markdownTokens) == 0 {
		return ExportTemplates{}, fmt.Errorf("compile Markdown export template: %w: template is empty", ErrInvalidExport)
	}
	commentTokens, err := compileTemplate(comment, map[string]ExportTemplateName{
		"index":                 TemplateIndex,
		"target":                TemplateTarget,
		"path":                  TemplatePath,
		"line_or_range":         TemplateLineOrRange,
		publicationCommentValue: TemplateComment,
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

// PreparedExport represents prepared export.
type PreparedExport struct {
	body   string
	format ExportFormat
}

// Format returns the receiver's format value.
func (prepared PreparedExport) Format() ExportFormat {
	return prepared.format
}

// Body returns the receiver's body value.
func (prepared PreparedExport) Body() string {
	return prepared.body
}

// Bytes returns the receiver's bytes value.
func (prepared PreparedExport) Bytes() int64 {
	return int64(len(prepared.body))
}

// PrepareMarkdown prepares markdown.
func (review Review) PrepareMarkdown(
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	return review.ExportInput().PrepareMarkdownCancellable(nil, templates, options)
}

// PrepareMarkdownCancellable prepares markdown cancellable.
func (review Review) PrepareMarkdownCancellable(
	cancelled func() error,
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	return review.ExportInput().PrepareMarkdownCancellable(cancelled, templates, options)
}

// PrepareMarkdownCancellable prepares markdown cancellable.
func (input ExportInput) PrepareMarkdownCancellable(
	cancelled func() error,
	templates ExportTemplates,
	options ExportOptions,
) (PreparedExport, error) {
	if !input.ready {
		return PreparedExport{}, fmt.Errorf("%w: export input is unavailable", ErrInvalidExport)
	}
	if !options.valid() {
		return PreparedExport{}, fmt.Errorf("%w: export options contain unknown bits", ErrInvalidExport)
	}
	if !templates.Ready() {
		return PreparedExport{}, fmt.Errorf("%w: export templates are empty", ErrInvalidExport)
	}
	selected, err := input.exportCommentsCancellable(cancelled, options)
	if err != nil {
		return PreparedExport{}, err
	}
	var commentBlockBytes int64
	sizeWork := 0
	for index, comment := range selected {
		values, valueErr := commentTemplateValuesCancellable(cancelled, comment, index+1)
		if valueErr != nil {
			return PreparedExport{}, valueErr
		}
		separator := int64(0)
		if index != 0 {
			separator = 1
		}
		size, renderErr := renderedTemplateSize(
			templates.comment,
			values,
			MaxPreparedExportBytes-commentBlockBytes-separator,
			cancelled,
			&sizeWork,
		)
		if renderErr != nil {
			return PreparedExport{}, renderErr
		}
		commentBlockBytes += separator + size
	}
	if len(selected) == 0 {
		commentBlockBytes = int64(len("_No comments._"))
	}
	values := map[ExportTemplateName]string{
		TemplateCommentCount: strconv.Itoa(len(selected)),
	}
	bodyBytes, err := markdownExportSize(
		templates.markdown,
		values,
		commentBlockBytes,
		cancelled,
		&sizeWork,
	)
	if err != nil {
		return PreparedExport{}, err
	}
	body, builderErr := newBoundedBuilder(MaxPreparedExportBytes, cancelled)
	if builderErr != nil {
		return PreparedExport{}, builderErr
	}
	if err := body.ReserveExact(bodyBytes); err != nil {
		return PreparedExport{}, err
	}
	if err := renderMarkdownExportCancellable(
		cancelled,
		body,
		templates,
		selected,
		values,
	); err != nil {
		return PreparedExport{}, err
	}
	finished, finishErr := body.Finish()
	if finishErr != nil {
		return PreparedExport{}, finishErr
	}
	if int64(len(finished)) != bodyBytes {
		return PreparedExport{}, fmt.Errorf("%w: Markdown size pass diverged", ErrInvalidExport)
	}
	return PreparedExport{format: ExportMarkdown, body: finished}, nil
}

func renderMarkdownExportCancellable(
	cancelled func() error,
	body *boundedBuilder,
	templates ExportTemplates,
	selected []exportComment,
	values map[ExportTemplateName]string,
) error {
	work := 0
	for _, token := range templates.markdown {
		if err := exportCancellation(cancelled, &work); err != nil {
			return err
		}
		if token.name != TemplateComments {
			if err := writeExportToken(body, token, values); err != nil {
				return err
			}
			continue
		}
		if len(selected) == 0 {
			if err := body.WriteString("_No comments._"); err != nil {
				return err
			}
			continue
		}
		for index, comment := range selected {
			if err := exportCancellation(cancelled, &work); err != nil {
				return err
			}
			if index != 0 {
				if err := body.WriteString("\n"); err != nil {
					return err
				}
			}
			commentValues, valueErr := commentTemplateValuesCancellable(
				cancelled,
				comment,
				index+1,
			)
			if valueErr != nil {
				return valueErr
			}
			if err := writeExportTemplate(
				body,
				templates.comment,
				commentValues,
				cancelled,
				&work,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func markdownExportSize(
	tokens []exportToken,
	values map[ExportTemplateName]string,
	commentBytes int64,
	cancelled func() error,
	work *int,
) (int64, error) {
	var size int64
	for _, token := range tokens {
		if err := exportCancellation(cancelled, work); err != nil {
			return 0, err
		}
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
	cancelled func() error,
	work *int,
) (int64, error) {
	var size int64
	for _, token := range tokens {
		if err := exportCancellation(cancelled, work); err != nil {
			return 0, err
		}
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
	body *boundedBuilder,
	tokens []exportToken,
	values map[ExportTemplateName]string,
	cancelled func() error,
	work *int,
) error {
	for _, token := range tokens {
		if err := exportCancellation(cancelled, work); err != nil {
			return err
		}
		if err := writeExportToken(body, token, values); err != nil {
			return err
		}
	}
	return nil
}

func writeExportToken(
	body *boundedBuilder,
	token exportToken,
	values map[ExportTemplateName]string,
) error {
	if token.name == 0 {
		return body.WriteString(token.literal)
	}
	return body.WriteString(values[token.name])
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

func (input ExportInput) exportCommentsCancellable(
	cancelled func() error,
	options ExportOptions,
) ([]exportComment, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	selected := make([]exportComment, 0, len(input.comments))
	for index, comment := range input.comments {
		if index%256 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		if !includeExportComment(comment, options) {
			continue
		}
		selected = append(selected, comment)
	}
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	return selected, nil
}

func includeExportComment(comment exportComment, options ExportOptions) bool {
	return options.Has(ExportUnmappable) || !comment.unmappable
}

func compileTemplate(
	value string,
	allowed map[string]ExportTemplateName,
) ([]exportToken, error) {
	var tokens []exportToken
	for value != "" {
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

func commentTemplateValuesCancellable(
	cancelled func() error,
	comment exportComment,
	index int,
) (map[ExportTemplateName]string, error) {
	target, path, lineRange, err := exportTargetCancellable(cancelled, comment.target)
	if err != nil {
		return nil, err
	}
	target, err = markdownEscapeCancellable(cancelled, target)
	if err != nil {
		return nil, err
	}
	path, err = markdownEscapeCancellable(cancelled, path)
	if err != nil {
		return nil, err
	}
	body, err := markdownEscapeCancellable(cancelled, comment.body.String())
	if err != nil {
		return nil, err
	}
	return map[ExportTemplateName]string{
		TemplateIndex:       strconv.Itoa(index),
		TemplateTarget:      target,
		TemplatePath:        path,
		TemplateLineOrRange: lineRange,
		TemplateComment:     body,
	}, nil
}

func exportTargetCancellable(
	cancelled func() error,
	target exportCommentTarget,
) (string, string, string, error) {
	if target.kind == TargetReview {
		return "review", "", "", cancellationError(cancelled)
	}
	path := ""
	if target.hasPath {
		var err error
		path, err = escapePathCancellable(cancelled, target.path.raw)
		if err != nil {
			return "", "", "", err
		}
	}
	if target.kind == TargetFile {
		return path, path, "", nil
	}
	if target.kind != TargetLine {
		return path, path, "", nil
	}
	prefix := ""
	if target.side == LineOld {
		prefix = "~"
	}
	lineRange := prefix + strconv.FormatUint(uint64(target.start.Uint32()), 10)
	if target.start != target.end {
		lineRange += "-" + strconv.FormatUint(uint64(target.end.Uint32()), 10)
	}
	return path + ":" + lineRange, path, lineRange, cancellationError(cancelled)
}

func markdownEscape(value string) string {
	escaped, err := markdownEscapeCancellable(nil, value)
	if err != nil {
		panic("nil Markdown cancellation returned an error")
	}
	return escaped
}

func markdownEscapeCancellable(cancelled func() error, value string) (string, error) {
	const punctuation = `\` + "`*_{}[]()#+!|>"
	if err := cancellationError(cancelled); err != nil {
		return "", err
	}
	result, builderErr := newBoundedBuilder(MaxPreparedExportBytes, cancelled)
	if builderErr != nil {
		return "", builderErr
	}
	lineStart := 0
	nextCancellation := 2_048
	for offset, character := range value {
		if offset >= nextCancellation {
			if err := cancellationError(cancelled); err != nil {
				return "", err
			}
			nextCancellation = offset + 2_048
		}
		if strings.ContainsRune(punctuation, character) ||
			markdownStructuralPunctuation(value, lineStart, offset, character) {
			if err := result.WriteString("\\"); err != nil {
				return "", err
			}
		}
		if err := result.WriteString(string(character)); err != nil {
			return "", err
		}
		if character == '\n' {
			lineStart = offset + 1
		}
	}
	if err := cancellationError(cancelled); err != nil {
		return "", err
	}
	return result.Finish()
}

func markdownStructuralPunctuation(value string, lineStart, offset int, character rune) bool {
	if offset-lineStart > 12 {
		return false
	}
	prefix := value[lineStart:offset]
	indent := 0
	for indent < len(prefix) && prefix[indent] == ' ' {
		indent++
	}
	if indent > 3 {
		return false
	}
	prefix = prefix[indent:]
	switch character {
	case '-':
		return prefix == "" && markdownListDelimiterFollows(value, offset, '-')
	case '.':
		if prefix == "" || len(prefix) > 9 {
			return false
		}
		for index := range len(prefix) {
			if prefix[index] < '0' || prefix[index] > '9' {
				return false
			}
		}
		return markdownListDelimiterFollows(value, offset, 0)
	default:
		return false
	}
}

func markdownListDelimiterFollows(value string, offset int, extra byte) bool {
	if offset+1 == len(value) {
		return true
	}
	next := value[offset+1]
	return next == ' ' || next == '\t' || next == '\r' || next == '\n' ||
		extra != 0 && next == extra
}
