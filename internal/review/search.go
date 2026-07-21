package review

import (
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// MaxSearchQueryBytes defines the supported search query bytes limit.
const (
	MaxSearchQueryBytes          = 64 << 10
	MaxSearchHits                = 100_000
	MaxSearchHitBytes      int64 = 16 << 20
	MaxSearchBatchBytes          = 256 << 10
	MaxSearchBatchLines          = 4_096
	maxSearchBatchHits           = 4_096
	searchHitRetainedBytes       = 32
)

// ErrInvalidSearch identifies an invalid search failure.
var ErrInvalidSearch = errors.New("invalid search transition")

// SearchQuery represents search query.
type SearchQuery string

// NewSearchQuery constructs and validates search query.
func NewSearchQuery(value string) (SearchQuery, error) {
	if value == "" {
		return "", fmt.Errorf("%w: query must not be empty", ErrInvalidSearch)
	}
	if len(value) > MaxSearchQueryBytes {
		return "", fmt.Errorf(
			"%w: query is %d bytes; maximum is %d",
			ErrInvalidSearch,
			len(value),
			MaxSearchQueryBytes,
		)
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: query is not valid UTF-8", ErrInvalidSearch)
	}
	return SearchQuery(value), nil
}

// String returns the receiver's string value.
func (query SearchQuery) String() string {
	return string(query)
}

// SearchScope describes search scope.
type SearchScope uint8

// SearchCurrentFile identifies a supported search current file value.
const (
	SearchCurrentFile SearchScope = iota + 1
	SearchAllFiles
)

// SearchStatus describes search status.
type SearchStatus uint8

// SearchIdle identifies a supported search idle value.
const (
	SearchIdle SearchStatus = iota + 1
	SearchActive
	SearchComplete
	SearchPartial
	SearchLimited
	SearchFailed
	SearchCancelled
	SearchSuperseded
)

// SearchHit represents search hit.
type SearchHit struct {
	fileID    FileID
	patchLine uint32
	hunk      uint32
	oldLine   uint32
	newLine   uint32
	byteStart uint32
	byteEnd   uint32
}

// FileID returns the receiver's file id value.
func (hit SearchHit) FileID() FileID {
	return hit.fileID
}

// PatchLine returns the receiver's patch line value.
func (hit SearchHit) PatchLine() uint32 {
	return hit.patchLine
}

// Hunk returns the receiver's hunk value.
func (hit SearchHit) Hunk() (HunkID, bool) {
	if hit.hunk == noPatchHunk {
		return HunkID{}, false
	}
	index, err := NewHunkIndex(hit.hunk)
	if err != nil {
		return HunkID{}, false
	}
	id, err := NewHunkID(hit.fileID, index)
	return id, err == nil
}

// OldLine returns the receiver's old line value.
func (hit SearchHit) OldLine() (LineNumber, bool) {
	if hit.oldLine == 0 {
		return 0, false
	}
	return LineNumber(hit.oldLine), true
}

// NewLine constructs and validates line.
func (hit SearchHit) NewLine() (LineNumber, bool) {
	if hit.newLine == 0 {
		return 0, false
	}
	return LineNumber(hit.newLine), true
}

// ByteRange returns the receiver's byte range value.
func (hit SearchHit) ByteRange() (uint32, uint32) {
	return hit.byteStart, hit.byteEnd
}

// SearchSummary describes search summary.
type SearchSummary struct {
	Query            SearchQuery
	Detail           OperationDetail
	Operation        OperationID
	HitCount         int
	UnavailableFiles uint32
	Status           SearchStatus
}

type searchState struct {
	query            SearchQuery
	detail           OperationDetail
	hits             []SearchHit
	operation        OperationID
	unavailableFiles uint32
	status           SearchStatus
}

// SearchRequest describes search request.
type SearchRequest struct {
	Query     SearchQuery
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	File      FileID
	Context   PatchContext
	Scope     SearchScope
}

// SearchResult describes search result.
type SearchResult struct {
	detail           OperationDetail
	hits             []SearchHit
	Operation        OperationID
	Workspace        WorkspaceRevision
	Source           SourceID
	unavailableFiles uint32
	Status           SearchStatus
}

// SearchResultDescriptor describes search result descriptor.
type SearchResultDescriptor struct {
	Detail           OperationDetail
	Operation        OperationID
	Workspace        WorkspaceRevision
	Source           SourceID
	UnavailableFiles uint32
	Status           SearchStatus
}

// TakeSearchResult takes ownership of search result.
func TakeSearchResult(
	descriptor SearchResultDescriptor,
	hits []SearchHit,
) (SearchResult, error) {
	if descriptor.Operation == 0 || descriptor.Workspace == 0 || descriptor.Source == 0 ||
		!terminalSearchStatus(descriptor.Status) ||
		len(hits) > MaxSearchHits ||
		int64(len(hits))*searchHitRetainedBytes > MaxSearchHitBytes {
		return SearchResult{}, fmt.Errorf("%w: malformed terminal result", ErrInvalidSearch)
	}
	for _, hit := range hits {
		if hit.fileID >= MaxFiles || hit.patchLine >= MaxPatchLines ||
			hit.byteStart > hit.byteEnd {
			return SearchResult{}, fmt.Errorf("%w: malformed hit", ErrInvalidSearch)
		}
	}
	return SearchResult{
		Operation:        descriptor.Operation,
		Workspace:        descriptor.Workspace,
		Source:           descriptor.Source,
		Status:           descriptor.Status,
		hits:             hits,
		unavailableFiles: descriptor.UnavailableFiles,
		detail:           descriptor.Detail,
	}, nil
}

// HitCount returns the receiver's hit count value.
func (result SearchResult) HitCount() int {
	return len(result.hits)
}

// UnavailableFiles returns the receiver's unavailable files value.
func (result SearchResult) UnavailableFiles() uint32 {
	return result.unavailableFiles
}

// Detail returns the receiver's detail value.
func (result SearchResult) Detail() OperationDetail {
	return result.detail
}

// StartSearch performs the start search operation.
func (review Review) StartSearch(
	id OperationID,
	query SearchQuery,
	scope SearchScope,
	context PatchContext,
) (Review, SearchRequest, error) {
	if review.search.status == SearchActive || query == "" || !context.valid() ||
		scope != SearchCurrentFile && scope != SearchAllFiles {
		return review, SearchRequest{}, ErrInvalidSearch
	}
	var fileID FileID
	if scope == SearchCurrentFile {
		var ok bool
		fileID, ok = review.ActiveFile()
		if !ok {
			return review, SearchRequest{}, fmt.Errorf("%w: review has no active file", ErrInvalidSearch)
		}
	}
	next, err := review.BeginOperation(id, OperationSearch)
	if err != nil {
		return review, SearchRequest{}, err
	}
	next.search = searchState{
		status:    SearchActive,
		operation: id,
		query:     query,
	}
	return next, SearchRequest{
		Operation: id,
		Workspace: review.workspace,
		Source:    review.snapshot.SourceID(),
		Query:     query,
		Context:   context,
		Scope:     scope,
		File:      fileID,
	}, nil
}

// ApplySearch performs the apply search operation.
func (review Review) ApplySearch(result SearchResult) (Review, error) {
	if review.search.status != SearchActive ||
		review.search.operation != result.Operation ||
		review.workspace != result.Workspace ||
		review.snapshot.SourceID() != result.Source ||
		!terminalSearchStatus(result.Status) {
		return review, ErrInvalidSearch
	}
	if len(result.hits) > MaxSearchHits ||
		int64(len(result.hits))*searchHitRetainedBytes > MaxSearchHitBytes {
		return review, fmt.Errorf("%w: result capacity exceeded", ErrInvalidSearch)
	}
	next := review.clone()
	next.search = searchState{
		status:           result.Status,
		operation:        result.Operation,
		query:            review.search.query,
		hits:             result.hits,
		unavailableFiles: result.unavailableFiles,
		detail:           result.detail,
	}
	return next, nil
}

// SupersedeSearch returns the receiver's supersede search value.
func (review Review) SupersedeSearch() Review {
	if review.search.status != SearchActive {
		return review
	}
	next := review.clone()
	next.search.status = SearchSuperseded
	next.search.hits = nil
	next.search.detail = mustOperationDetail("search was superseded")
	return next
}

// ClearSearch returns the receiver's clear search value.
func (review Review) ClearSearch() Review {
	if review.search.status == SearchIdle {
		return review
	}
	next := review.clone()
	next.search = searchState{status: SearchIdle}
	return next
}

// SearchSummary returns the receiver's search summary value.
func (review Review) SearchSummary() SearchSummary {
	return SearchSummary{
		Status:           review.search.status,
		Operation:        review.search.operation,
		Query:            review.search.query,
		HitCount:         len(review.search.hits),
		UnavailableFiles: review.search.unavailableFiles,
		Detail:           review.search.detail,
	}
}

// SearchHit performs the search hit operation.
func (review Review) SearchHit(index int) (SearchHit, bool) {
	if index < 0 || index >= len(review.search.hits) {
		return SearchHit{}, false
	}
	return review.search.hits[index], true
}

// SearchHits performs the search hits operation.
func (review Review) SearchHits(start, count int) []SearchHit {
	if start < 0 || start >= len(review.search.hits) || count <= 0 {
		return nil
	}
	end := min(start+count, len(review.search.hits))
	return slices.Clone(review.search.hits[start:end])
}

func terminalSearchStatus(status SearchStatus) bool {
	return status >= SearchComplete && status <= SearchCancelled
}

// PreparedSearchQuery represents prepared search query.
type PreparedSearchQuery struct {
	bytes  []byte
	prefix []uint32
}

// PrepareSearchQuery prepares search query.
func PrepareSearchQuery(query SearchQuery) PreparedSearchQuery {
	bytes := []byte(query)
	prepared := PreparedSearchQuery{
		bytes:  bytes,
		prefix: make([]uint32, len(bytes)),
	}
	var matched uint32
	for index := 1; index < len(bytes); index++ {
		for matched != 0 && bytes[index] != bytes[matched] {
			matched = prepared.prefix[matched-1]
		}
		if bytes[index] == bytes[matched] {
			matched++
		}
		prepared.prefix[index] = matched
	}
	return prepared
}

// SearchCursor represents search cursor.
type SearchCursor struct {
	line    uint32
	offset  uint32
	matched uint32
}

// SearchBatch represents search batch.
type SearchBatch struct {
	Hits   []SearchHit
	Cursor SearchCursor
	Done   bool
}

// ScanSearchBatch scans search batch.
func ScanSearchBatch(
	patch *PreparedPatch,
	fileID FileID,
	query PreparedSearchQuery,
	cursor SearchCursor,
) SearchBatch {
	result := SearchBatch{
		Cursor: cursor,
		Hits:   make([]SearchHit, 0, 16),
	}
	if patch == nil || len(query.bytes) == 0 || int(cursor.line) >= len(patch.lineOffsets) {
		result.Done = true
		return result
	}
	inspectedBytes := 0
	inspectedLines := 0
	for int(result.Cursor.line) < len(patch.lineOffsets) &&
		inspectedBytes < MaxSearchBatchBytes &&
		inspectedLines < MaxSearchBatchLines &&
		len(result.Hits) < maxSearchBatchHits {
		lineIndex := result.Cursor.line
		kind := patch.lineKinds[lineIndex]
		if kind != PatchLineContext && kind != PatchLineAddition && kind != PatchLineDeletion {
			result.Cursor.line++
			result.Cursor.offset = 0
			result.Cursor.matched = 0
			inspectedLines++
			continue
		}
		line := patch.lineBytes(lineIndex)
		if len(line) != 0 {
			line = line[1:]
		}
		if int(result.Cursor.offset) >= len(line) {
			result.Cursor.line++
			result.Cursor.offset = 0
			result.Cursor.matched = 0
			inspectedLines++
			continue
		}
		for int(result.Cursor.offset) < len(line) && inspectedBytes < MaxSearchBatchBytes {
			value := line[result.Cursor.offset]
			for result.Cursor.matched != 0 &&
				value != query.bytes[result.Cursor.matched] {
				result.Cursor.matched = query.prefix[result.Cursor.matched-1]
			}
			if value == query.bytes[result.Cursor.matched] {
				result.Cursor.matched++
			}
			result.Cursor.offset++
			inspectedBytes++
			if int(result.Cursor.matched) == len(query.bytes) {
				end := result.Cursor.offset
				result.Hits = append(result.Hits, SearchHit{
					fileID:    fileID,
					patchLine: lineIndex,
					hunk:      patch.lineHunks[lineIndex],
					oldLine:   patch.oldLines[lineIndex],
					newLine:   patch.newLines[lineIndex],
					byteStart: end - uint32FromInt(len(query.bytes)),
					byteEnd:   end,
				})
				result.Cursor.line++
				result.Cursor.offset = 0
				result.Cursor.matched = 0
				inspectedLines++
				break
			}
		}
	}
	result.Done = int(result.Cursor.line) >= len(patch.lineOffsets)
	return result
}

func (patch *PreparedPatch) lineBytes(line uint32) []byte {
	start := patch.lineOffsets[line].Uint32()
	end := uint32FromInt(len(patch.text))
	if int(line)+1 < len(patch.lineOffsets) {
		end = patch.lineOffsets[line+1].Uint32()
	}
	for end > start && (patch.text[end-1] == '\n' || patch.text[end-1] == '\r') {
		end--
	}
	return patch.text[start:end]
}
