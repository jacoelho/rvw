package review

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxBlobBytes defines the supported blob bytes limit.
const (
	MaxBlobBytes       int64 = 16 << 20
	MaxSyntaxLines           = 1_000
	MaxSyntaxSpans           = 16_384
	MaxSyntaxBytes     int64 = 1 << 20
	MaxSyntaxLineBytes       = 256 << 10
	syntaxPollBytes          = 16 << 10
)

// ErrInvalidSyntax identifies an invalid syntax failure.
var ErrInvalidSyntax = errors.New("invalid syntax preparation")

// ErrSyntaxLimited identifies an syntax limited failure.
var ErrSyntaxLimited = errors.New("syntax preparation exceeded its limit")

// SyntaxSelector represents syntax selector.
type SyntaxSelector string

// NewSyntaxSelector constructs and validates syntax selector.
func NewSyntaxSelector(value string) (SyntaxSelector, error) {
	if value == "" || len(value) > 4_096 || !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: selector is invalid", ErrInvalidSyntax)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: selector contains a control character", ErrInvalidSyntax)
		}
	}
	return SyntaxSelector(value), nil
}

// String returns the receiver's string value.
func (selector SyntaxSelector) String() string {
	return string(selector)
}

// SyntaxIdentity represents syntax identity.
type SyntaxIdentity struct {
	content  ObjectID
	selector SyntaxSelector
}

// NewSyntaxIdentity constructs and validates syntax identity.
func NewSyntaxIdentity(
	content ObjectID,
	selector SyntaxSelector,
) (SyntaxIdentity, error) {
	validatedContent, contentErr := NewObjectID(content.String())
	validatedSelector, selectorErr := NewSyntaxSelector(selector.String())
	if contentErr != nil || selectorErr != nil {
		return SyntaxIdentity{}, ErrInvalidSyntax
	}
	return SyntaxIdentity{content: validatedContent, selector: validatedSelector}, nil
}

// Content returns the receiver's content value.
func (identity SyntaxIdentity) Content() ObjectID {
	return identity.content
}

// Selector returns the receiver's selector value.
func (identity SyntaxIdentity) Selector() SyntaxSelector {
	return identity.selector
}

// SyntaxDemand represents syntax demand.
type SyntaxDemand struct {
	Identity SyntaxIdentity
	Lines    []LineNumber
	Side     LineSide
}

// SyntaxRequest describes syntax request.
type SyntaxRequest struct {
	Demands   []SyntaxDemand
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	File      FileID
}

// RequestSyntax performs the request syntax operation.
func (review Review) RequestSyntax(
	id OperationID,
	fileID FileID,
	selector SyntaxSelector,
	oldLines, newLines []LineNumber,
) (Review, SyntaxRequest, error) {
	file, ok := review.file(fileID)
	if !ok || selector == "" || len(oldLines)+len(newLines) > MaxSyntaxLines {
		return review, SyntaxRequest{}, ErrInvalidSyntax
	}
	demands := make([]SyntaxDemand, 0, 2)
	for _, sideLines := range []struct {
		lines []LineNumber
		side  LineSide
	}{
		{side: LineOld, lines: oldLines},
		{side: LineNew, lines: newLines},
	} {
		if len(sideLines.lines) == 0 {
			continue
		}
		content, exists := file.Content(sideLines.side)
		if !exists {
			continue
		}
		lines, err := validatedSyntaxLines(sideLines.lines)
		if err != nil {
			return review, SyntaxRequest{}, err
		}
		identity, err := NewSyntaxIdentity(content, selector)
		if err != nil {
			return review, SyntaxRequest{}, err
		}
		demands = append(demands, SyntaxDemand{
			Side:     sideLines.side,
			Identity: identity,
			Lines:    lines,
		})
	}
	if len(demands) == 0 {
		return review, SyntaxRequest{}, fmt.Errorf("%w: file has no requested blob content", ErrInvalidSyntax)
	}
	next, err := review.BeginOperation(id, OperationSyntax)
	if err != nil {
		return review, SyntaxRequest{}, err
	}
	return next, SyntaxRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		File:      fileID,
		Demands:   demands,
	}, nil
}

func validatedSyntaxLines(values []LineNumber) ([]LineNumber, error) {
	lines := slices.Clone(values)
	slices.Sort(lines)
	for index, line := range lines {
		if line == 0 || index != 0 && line == lines[index-1] {
			return nil, ErrInvalidSyntax
		}
	}
	return lines, nil
}

// SyntaxKind describes syntax kind.
type SyntaxKind uint8

// SyntaxKeyword identifies a supported syntax keyword value.
const (
	SyntaxKeyword SyntaxKind = iota + 1
	SyntaxString
	SyntaxComment
	SyntaxNumber
)

// SyntaxSpan represents syntax span.
type SyntaxSpan struct {
	Start uint32
	End   uint32
	Kind  SyntaxKind
}

// PreparedSyntax represents prepared syntax.
type PreparedSyntax struct {
	identity SyntaxIdentity
	lines    []syntaxLineIndex
	spans    []SyntaxSpan
	bytes    int64
	peak     int64
}

type syntaxLineIndex struct {
	line  LineNumber
	first uint32
	last  uint32
}

const (
	preparedSyntaxMetadataBytes int64 = 96
	syntaxLineIndexBytes        int64 = 12
	syntaxSpanBytes             int64 = 12
	syntaxDemandMetadataBytes   int64 = 64
	syntaxRequestMetadataBytes  int64 = 56
)

// Identity returns the receiver's identity value.
func (prepared *PreparedSyntax) Identity() SyntaxIdentity {
	if prepared == nil {
		return SyntaxIdentity{}
	}
	return prepared.identity
}

// Spans performs the spans operation.
func (prepared *PreparedSyntax) Spans(line LineNumber) iter.Seq[SyntaxSpan] {
	entry, ok := prepared.line(line)
	if !ok || entry.first == entry.last {
		return nil
	}
	spans := prepared.spans[entry.first:entry.last]
	return func(yield func(SyntaxSpan) bool) {
		for _, span := range spans {
			if !yield(span) {
				return
			}
		}
	}
}

// Covers performs the covers operation.
func (prepared *PreparedSyntax) Covers(line LineNumber) bool {
	_, ok := prepared.line(line)
	return ok
}

func (prepared *PreparedSyntax) line(line LineNumber) (syntaxLineIndex, bool) {
	if prepared == nil {
		return syntaxLineIndex{}, false
	}
	index, found := slices.BinarySearchFunc(
		prepared.lines,
		line,
		func(entry syntaxLineIndex, target LineNumber) int {
			switch {
			case entry.line < target:
				return -1
			case entry.line > target:
				return 1
			default:
				return 0
			}
		},
	)
	if !found {
		return syntaxLineIndex{}, false
	}
	return prepared.lines[index], true
}

// RetainedBytes returns the receiver's retained bytes value.
func (prepared *PreparedSyntax) RetainedBytes() int64 {
	if prepared == nil {
		return 0
	}
	return prepared.bytes
}

// ConstructionBytes returns the maximum simultaneous retained and temporary
// storage used while preparing this result, excluding the caller-owned blob.
func (prepared *PreparedSyntax) ConstructionBytes() int64 {
	if prepared == nil {
		return 0
	}
	return prepared.peak
}

// RetainedBytes returns the complete request ownership prolonged by a syntax
// worker, including slice capacities and identity strings.
func (request SyntaxRequest) RetainedBytes() int64 {
	if request.Operation == 0 || request.Source == 0 {
		return 0
	}
	total := syntaxRequestMetadataBytes + int64(cap(request.Demands))*syntaxDemandMetadataBytes
	for _, demand := range request.Demands {
		total += int64(cap(demand.Lines))*4 +
			int64(len(demand.Identity.content)+len(demand.Identity.selector))
	}
	return total
}

// PrepareSyntax prepares syntax.
func PrepareSyntax(
	identity SyntaxIdentity,
	lines []LineNumber,
	blob []byte,
) (*PreparedSyntax, error) {
	return PrepareSyntaxCancellable(nil, identity, lines, blob)
}

// PrepareSyntaxCancellable prepares syntax cancellable.
func PrepareSyntaxCancellable(
	cancelled func() error,
	identity SyntaxIdentity,
	lines []LineNumber,
	blob []byte,
) (*PreparedSyntax, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	if identity.content == "" || identity.selector == "" ||
		int64(len(blob)) > MaxBlobBytes {
		return nil, ErrInvalidSyntax
	}
	requested, err := validatedSyntaxLines(lines)
	if err != nil || len(requested) > MaxSyntaxLines {
		return nil, ErrInvalidSyntax
	}
	prepared := &PreparedSyntax{
		identity: identity,
		lines:    make([]syntaxLineIndex, 0, len(requested)),
	}
	requestedBytes := int64(cap(requested)) * 4
	prepared.refreshRetainedBytes()
	prepared.peak = prepared.bytes + requestedBytes
	if prepared.bytes > MaxSyntaxBytes {
		return nil, fmt.Errorf("%w: result capacity reached", ErrSyntaxLimited)
	}
	var (
		lineNumber   uint32 = 1
		offset              = 0
		requestIndex        = 0
	)
	work := 0
	for requestIndex < len(requested) && offset <= len(blob) {
		if work%2_048 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return nil, err
			}
		}
		work++
		target := requested[requestIndex].Uint32()
		if lineNumber < target {
			next, scanErr := indexSyntaxByteCancellable(cancelled, blob[offset:], '\n')
			if scanErr != nil {
				return nil, scanErr
			}
			if next < 0 {
				break
			}
			offset += next + 1
			lineNumber++
			continue
		}
		if lineNumber > target {
			return nil, ErrInvalidSyntax
		}
		end := len(blob)
		next, scanErr := indexSyntaxByteCancellable(cancelled, blob[offset:], '\n')
		if scanErr != nil {
			return nil, scanErr
		}
		if next >= 0 {
			end = offset + next
		}
		line := blob[offset:end]
		if len(line) > MaxSyntaxLineBytes {
			return nil, fmt.Errorf(
				"%w: requested line exceeds %d bytes",
				ErrSyntaxLimited,
				MaxSyntaxLineBytes,
			)
		}
		first := len(prepared.spans)
		spanErr := appendSyntaxSpansCancellable(cancelled, prepared, requestedBytes, line)
		if spanErr != nil {
			return nil, spanErr
		}
		prepared.lines = append(prepared.lines, syntaxLineIndex{
			line:  requested[requestIndex],
			first: uint32FromInt(first),
			last:  uint32FromInt(len(prepared.spans)),
		})
		requestIndex++
		if end == len(blob) {
			break
		}
		offset = end + 1
		lineNumber++
	}
	prepared.refreshRetainedBytes()
	prepared.peak = max(prepared.peak, prepared.bytes+requestedBytes)
	return prepared, nil
}

func (prepared *PreparedSyntax) refreshRetainedBytes() {
	prepared.bytes = preparedSyntaxMetadataBytes +
		int64(len(prepared.identity.content)+len(prepared.identity.selector)) +
		int64(cap(prepared.lines))*syntaxLineIndexBytes +
		int64(cap(prepared.spans))*syntaxSpanBytes
}

func (prepared *PreparedSyntax) appendSpan(span SyntaxSpan, requestedBytes int64) error {
	if len(prepared.spans) == MaxSyntaxSpans {
		return fmt.Errorf("%w: result capacity reached", ErrSyntaxLimited)
	}
	if len(prepared.spans) == cap(prepared.spans) {
		next := min(max(cap(prepared.spans)*2, 8), MaxSyntaxSpans)
		fixed := preparedSyntaxMetadataBytes +
			int64(len(prepared.identity.content)+len(prepared.identity.selector)) +
			int64(cap(prepared.lines))*syntaxLineIndexBytes
		if fixed+int64(next)*syntaxSpanBytes > MaxSyntaxBytes {
			next = int((MaxSyntaxBytes - fixed) / syntaxSpanBytes)
		}
		if next <= cap(prepared.spans) {
			return fmt.Errorf("%w: result capacity reached", ErrSyntaxLimited)
		}
		prepared.peak = max(
			prepared.peak,
			fixed+requestedBytes+int64(cap(prepared.spans)+next)*syntaxSpanBytes,
		)
		replacement := make([]SyntaxSpan, len(prepared.spans), next)
		copy(replacement, prepared.spans)
		prepared.spans = replacement
	}
	prepared.spans = append(prepared.spans, span)
	prepared.refreshRetainedBytes()
	prepared.peak = max(prepared.peak, prepared.bytes+requestedBytes)
	return nil
}

func indexSyntaxByteCancellable(
	cancelled func() error,
	value []byte,
	target byte,
) (int, error) {
	for start := 0; start < len(value); start += syntaxPollBytes {
		if err := cancellationError(cancelled); err != nil {
			return -1, err
		}
		end := min(start+syntaxPollBytes, len(value))
		if index := bytes.IndexByte(value[start:end], target); index >= 0 {
			return start + index, nil
		}
	}
	return -1, cancellationError(cancelled)
}

func appendSyntaxSpansCancellable(
	cancelled func() error,
	prepared *PreparedSyntax,
	requestedBytes int64,
	line []byte,
) error {
	first := len(prepared.spans)
	nextPoll := 0
	check := func(index int) error {
		if index < nextPoll {
			return nil
		}
		nextPoll = index + syntaxPollBytes
		return cancellationError(cancelled)
	}
	for index := 0; index < len(line); {
		if err := check(index); err != nil {
			return err
		}
		switch {
		case index+1 < len(line) && line[index] == '/' && line[index+1] == '/':
			if err := prepared.appendSpan(SyntaxSpan{
				Start: uint32(index),
				End:   uint32FromInt(len(line)),
				Kind:  SyntaxComment,
			}, requestedBytes); err != nil {
				return err
			}
			return nil
		case line[index] == '"' || line[index] == '\'' || line[index] == '`':
			next, err := prepared.appendStringSpan(line, index, requestedBytes, check)
			if err != nil {
				return err
			}
			index = next
		case line[index] >= '0' && line[index] <= '9':
			next, err := prepared.appendNumberSpan(line, index, requestedBytes, check)
			if err != nil {
				return err
			}
			index = next
		case asciiIdentifierStart(line[index]):
			next, err := prepared.appendIdentifierSpan(line, index, requestedBytes, check)
			if err != nil {
				return err
			}
			index = next
		default:
			index++
		}
		if len(prepared.spans)-first == 256 {
			return nil
		}
	}
	return cancellationError(cancelled)
}

type syntaxCancelCheck func(int) error

func (prepared *PreparedSyntax) appendStringSpan(
	line []byte,
	start int,
	requestedBytes int64,
	check syntaxCancelCheck,
) (int, error) {
	quote := line[start]
	index := start + 1
	for index < len(line) {
		if err := check(index); err != nil {
			return 0, err
		}
		if line[index] == '\\' && quote != '`' && index+1 < len(line) {
			index += 2
			continue
		}
		value := line[index]
		index++
		if value == quote {
			break
		}
	}
	return index, prepared.appendSpan(SyntaxSpan{
		Start: uint32FromInt(start), End: uint32FromInt(index), Kind: SyntaxString,
	}, requestedBytes)
}

func (prepared *PreparedSyntax) appendNumberSpan(
	line []byte,
	start int,
	requestedBytes int64,
	check syntaxCancelCheck,
) (int, error) {
	index := start
	for index < len(line) &&
		(line[index] >= '0' && line[index] <= '9' ||
			strings.ContainsRune("abcdefABCDEFxXoObB._", rune(line[index]))) {
		index++
		if err := check(index); err != nil {
			return 0, err
		}
	}
	return index, prepared.appendSpan(SyntaxSpan{
		Start: uint32FromInt(start), End: uint32FromInt(index), Kind: SyntaxNumber,
	}, requestedBytes)
}

func (prepared *PreparedSyntax) appendIdentifierSpan(
	line []byte,
	start int,
	requestedBytes int64,
	check syntaxCancelCheck,
) (int, error) {
	index := start
	for index < len(line) && asciiIdentifierContinue(line[index]) {
		index++
		if err := check(index); err != nil {
			return 0, err
		}
	}
	if !syntaxKeywords[string(line[start:index])] {
		return index, nil
	}
	return index, prepared.appendSpan(SyntaxSpan{
		Start: uint32FromInt(start), End: uint32FromInt(index), Kind: SyntaxKeyword,
	}, requestedBytes)
}

func asciiIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func asciiIdentifierContinue(value byte) bool {
	return asciiIdentifierStart(value) || value >= '0' && value <= '9'
}

var syntaxKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "class": true, "const": true,
	"continue": true, "default": true, "defer": true, "else": true, "enum": true,
	"false": true, "fn": true, "for": true, "func": true, "go": true, "if": true,
	"impl": true, "import": true, "interface": true, "let": true, "map": true,
	"match": true, "nil": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "true": true, "type": true,
	"use": true, "var": true,
}

// SyntaxStatus describes syntax status.
type SyntaxStatus uint8

// SyntaxReady identifies a supported syntax ready value.
const (
	SyntaxReady SyntaxStatus = iota + 1
	SyntaxUnavailable
	SyntaxLimited
	SyntaxFailed
	SyntaxCancelled
)

// SyntaxResult describes syntax result.
type SyntaxResult struct {
	Old       *PreparedSyntax
	New       *PreparedSyntax
	Detail    OperationDetail
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	File      FileID
	Status    SyntaxStatus
}
