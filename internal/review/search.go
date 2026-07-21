package review

import (
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

const (
	MaxSearchQueryBytes          = 64 << 10
	MaxSearchHits                = 100_000
	MaxSearchHitBytes      int64 = 16 << 20
	MaxSearchBatchBytes          = 256 << 10
	MaxSearchBatchLines          = 4_096
	maxSearchBatchHits           = 4_096
	searchHitRetainedBytes       = 32
)

var ErrInvalidSearch = errors.New("invalid search transition")

type SearchQuery string

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

func (query SearchQuery) String() string {
	return string(query)
}

type SearchScope uint8

const (
	SearchCurrentFile SearchScope = iota + 1
	SearchAllFiles
)

type SearchStatus uint8

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

type SearchHit struct {
	fileID    FileID
	patchLine uint32
	hunk      uint32
	oldLine   uint32
	newLine   uint32
	byteStart uint32
	byteEnd   uint32
}

func (hit SearchHit) FileID() FileID {
	return hit.fileID
}

func (hit SearchHit) PatchLine() uint32 {
	return hit.patchLine
}

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

func (hit SearchHit) OldLine() (LineNumber, bool) {
	if hit.oldLine == 0 {
		return 0, false
	}
	return LineNumber(hit.oldLine), true
}

func (hit SearchHit) NewLine() (LineNumber, bool) {
	if hit.newLine == 0 {
		return 0, false
	}
	return LineNumber(hit.newLine), true
}

func (hit SearchHit) ByteRange() (uint32, uint32) {
	return hit.byteStart, hit.byteEnd
}

type SearchSummary struct {
	Status           SearchStatus
	Operation        OperationID
	Query            SearchQuery
	HitCount         int
	UnavailableFiles uint32
	Detail           OperationDetail
}

type searchState struct {
	status           SearchStatus
	operation        OperationID
	query            SearchQuery
	hits             []SearchHit
	unavailableFiles uint32
	detail           OperationDetail
}

type SearchRequest struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	Query     SearchQuery
	Context   PatchContext
	Scope     SearchScope
	File      FileID
}

type SearchResult struct {
	Operation OperationID
	Workspace WorkspaceRevision
	Source    SourceID
	Status    SearchStatus

	hits             []SearchHit
	unavailableFiles uint32
	detail           OperationDetail
}

type SearchResultDescriptor struct {
	Operation        OperationID
	Workspace        WorkspaceRevision
	Source           SourceID
	Status           SearchStatus
	UnavailableFiles uint32
	Detail           OperationDetail
}

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

func (result SearchResult) HitCount() int {
	return len(result.hits)
}

func (result SearchResult) UnavailableFiles() uint32 {
	return result.unavailableFiles
}

func (result SearchResult) Detail() OperationDetail {
	return result.detail
}

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

func (review Review) ClearSearch() Review {
	if review.search.status == SearchIdle {
		return review
	}
	next := review.clone()
	next.search = searchState{status: SearchIdle}
	return next
}

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

func (review Review) SearchHit(index int) (SearchHit, bool) {
	if index < 0 || index >= len(review.search.hits) {
		return SearchHit{}, false
	}
	return review.search.hits[index], true
}

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

type PreparedSearchQuery struct {
	bytes  []byte
	prefix []uint32
}

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

type SearchCursor struct {
	line    uint32
	offset  uint32
	matched uint32
}

type SearchBatch struct {
	Cursor SearchCursor
	Hits   []SearchHit
	Done   bool
}

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
					byteStart: end - uint32(len(query.bytes)),
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
	end := uint32(len(patch.text))
	if int(line)+1 < len(patch.lineOffsets) {
		end = patch.lineOffsets[line+1].Uint32()
	}
	for end > start && (patch.text[end-1] == '\n' || patch.text[end-1] == '\r') {
		end--
	}
	return patch.text[start:end]
}
