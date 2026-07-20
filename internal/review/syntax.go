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

const (
	MaxBlobBytes       int64 = 16 << 20
	MaxSyntaxLines           = 1_000
	MaxSyntaxSpans           = 16_384
	MaxSyntaxBytes     int64 = 1 << 20
	MaxSyntaxLineBytes       = 256 << 10
)

var ErrInvalidSyntax = errors.New("invalid syntax preparation")
var ErrSyntaxLimited = errors.New("syntax preparation exceeded its limit")

type SyntaxSelector string

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

func (selector SyntaxSelector) String() string {
	return string(selector)
}

type SyntaxIdentity struct {
	content  ObjectID
	selector SyntaxSelector
}

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

func (identity SyntaxIdentity) Content() ObjectID {
	return identity.content
}

func (identity SyntaxIdentity) Selector() SyntaxSelector {
	return identity.selector
}

type SyntaxDemand struct {
	Side     LineSide
	Identity SyntaxIdentity
	Lines    []LineNumber
}

type SyntaxRequest struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	File      FileID
	Demands   []SyntaxDemand
}

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
		side  LineSide
		lines []LineNumber
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

type SyntaxKind uint8

const (
	SyntaxKeyword SyntaxKind = iota + 1
	SyntaxString
	SyntaxComment
	SyntaxNumber
)

type SyntaxSpan struct {
	Start uint32
	End   uint32
	Kind  SyntaxKind
}

type PreparedSyntax struct {
	identity SyntaxIdentity
	lines    map[LineNumber][]SyntaxSpan
	bytes    int64
}

func (prepared *PreparedSyntax) Identity() SyntaxIdentity {
	if prepared == nil {
		return SyntaxIdentity{}
	}
	return prepared.identity
}

func (prepared *PreparedSyntax) Spans(line LineNumber) iter.Seq[SyntaxSpan] {
	return func(yield func(SyntaxSpan) bool) {
		if prepared == nil {
			return
		}
		for _, span := range prepared.lines[line] {
			if !yield(span) {
				return
			}
		}
	}
}

func (prepared *PreparedSyntax) Covers(line LineNumber) bool {
	if prepared == nil {
		return false
	}
	_, exists := prepared.lines[line]
	return exists
}

func (prepared *PreparedSyntax) RetainedBytes() int64 {
	if prepared == nil {
		return 0
	}
	return prepared.bytes
}

func PrepareSyntax(
	identity SyntaxIdentity,
	lines []LineNumber,
	blob []byte,
) (*PreparedSyntax, error) {
	return PrepareSyntaxCancellable(nil, identity, lines, blob)
}

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
		lines:    make(map[LineNumber][]SyntaxSpan, len(requested)),
	}
	var (
		lineNumber   uint32 = 1
		offset              = 0
		requestIndex        = 0
		spanCount           = 0
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
			next := bytes.IndexByte(blob[offset:], '\n')
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
		if next := bytes.IndexByte(blob[offset:], '\n'); next >= 0 {
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
		spans := syntaxSpans(line)
		spanCount += len(spans)
		prepared.bytes += int64(len(spans) * 12)
		if spanCount > MaxSyntaxSpans || prepared.bytes > MaxSyntaxBytes {
			return nil, fmt.Errorf("%w: result capacity reached", ErrSyntaxLimited)
		}
		prepared.lines[requested[requestIndex]] = spans
		requestIndex++
		if end == len(blob) {
			break
		}
		offset = end + 1
		lineNumber++
	}
	return prepared, nil
}

func syntaxSpans(line []byte) []SyntaxSpan {
	spans := make([]SyntaxSpan, 0, 8)
	for index := 0; index < len(line); {
		switch {
		case index+1 < len(line) && line[index] == '/' && line[index+1] == '/':
			spans = append(spans, SyntaxSpan{
				Start: uint32(index),
				End:   uint32(len(line)),
				Kind:  SyntaxComment,
			})
			return spans
		case line[index] == '"' || line[index] == '\'' || line[index] == '`':
			quote := line[index]
			start := index
			index++
			for index < len(line) {
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
			spans = append(spans, SyntaxSpan{
				Start: uint32(start),
				End:   uint32(index),
				Kind:  SyntaxString,
			})
		case line[index] >= '0' && line[index] <= '9':
			start := index
			for index < len(line) &&
				(line[index] >= '0' && line[index] <= '9' ||
					strings.ContainsRune("abcdefABCDEFxXoObB._", rune(line[index]))) {
				index++
			}
			spans = append(spans, SyntaxSpan{
				Start: uint32(start),
				End:   uint32(index),
				Kind:  SyntaxNumber,
			})
		case asciiIdentifierStart(line[index]):
			start := index
			for index < len(line) && asciiIdentifierContinue(line[index]) {
				index++
			}
			if syntaxKeywords[string(line[start:index])] {
				spans = append(spans, SyntaxSpan{
					Start: uint32(start),
					End:   uint32(index),
					Kind:  SyntaxKeyword,
				})
			}
		default:
			index++
		}
		if len(spans) == 256 {
			return spans
		}
	}
	return spans
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

type SyntaxStatus uint8

const (
	SyntaxReady SyntaxStatus = iota + 1
	SyntaxUnavailable
	SyntaxLimited
	SyntaxFailed
	SyntaxCancelled
)

type SyntaxResult struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	File      FileID
	Status    SyntaxStatus
	Old       *PreparedSyntax
	New       *PreparedSyntax
	Detail    OperationDetail
}
