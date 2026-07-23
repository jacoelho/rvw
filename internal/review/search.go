package review

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"unicode/utf8"
)

// MaxSearchQueryBytes defines the supported search query bytes limit.
const (
	MaxSearchQueryBytes                    = 64 << 10
	MaxSearchHits                          = 100_000
	MaxSearchHitBytes                int64 = 16 << 20
	MaxSearchBatchBytes                    = 256 << 10
	MaxSearchBatchLines                    = 4_096
	MaxSearchExcerptBytes                  = 128
	maxSearchBatchHits                     = 4_096
	searchHitGrowthSeed                    = 8
	searchFileIDStorageBytes               = 4
	searchUint32StorageBytes               = 4
	searchUint64StorageBytes               = 8
	searchTargetMetadataBytes              = 32
	preparedSearchQueryMetadataBytes       = 48
	searchRequestMetadataBytes             = 112
	// SearchHitStorageBytes is the retained slice-slot charge for one search hit.
	SearchHitStorageBytes int64 = 48
	// MaxSearchBatchRetainedBytes bounds one complete retained search batch.
	MaxSearchBatchRetainedBytes int64 = maxSearchBatchHits *
		(SearchHitStorageBytes + MaxSearchExcerptBytes)
	maxSearchBatchGrowthBytes int64 = maxSearchBatchHits/2*MaxSearchExcerptBytes +
		(maxSearchBatchHits/2+maxSearchBatchHits)*SearchHitStorageBytes +
		MaxSearchExcerptBytes
	// MaxSearchBatchConstructionBytes bounds one batch retained while its hits
	// backing grows or while the complete batch is transferred.
	MaxSearchBatchConstructionBytes int64 = max(
		MaxSearchBatchRetainedBytes,
		maxSearchBatchGrowthBytes,
	)
	maxSearchAggregatePredecessorHits       = 65_536
	maxSearchAggregateGrowthBytes     int64 = maxSearchAggregatePredecessorHits*MaxSearchExcerptBytes +
		(maxSearchAggregatePredecessorHits+MaxSearchHits)*SearchHitStorageBytes +
		MaxSearchExcerptBytes
	// MaxSearchAggregateConstructionBytes bounds one aggregate while its hit
	// backing grows, including the simultaneously retained predecessor backing.
	MaxSearchAggregateConstructionBytes int64 = max(
		MaxSearchHitBytes,
		maxSearchAggregateGrowthBytes,
	)
	searchCancellationInterval = 2_048
	searchTargetBitmapWords    = (MaxFiles + 63) / 64
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
	if err := validateSearchQueryCancellable(nil, SearchQuery(value)); err != nil {
		return "", fmt.Errorf("%w: query is not valid UTF-8", ErrInvalidSearch)
	}
	return SearchQuery(value), nil
}

func validateSearchQueryCancellable(cancelled func() error, query SearchQuery) error {
	if query == "" || len(query) > MaxSearchQueryBytes {
		return ErrInvalidSearch
	}
	for offset, nextCheck := 0, 0; offset < len(query); {
		if offset >= nextCheck {
			if err := cancellationError(cancelled); err != nil {
				return err
			}
			nextCheck = offset + searchCancellationInterval - utf8.UTFMax
		}
		value, size := utf8.DecodeRuneInString(string(query)[offset:])
		if value == utf8.RuneError && size == 1 {
			return ErrInvalidSearch
		}
		offset += size
	}
	return nil
}

// String returns the receiver's string value.
func (query SearchQuery) String() string {
	return string(query)
}

// SearchTarget is an immutable ordered set of files owned by one search.
type SearchTarget struct {
	files  []FileID
	source SourceID
}

// NewSearchTarget constructs and validates a search target.
func NewSearchTarget(snapshot Snapshot, files []FileID) (SearchTarget, error) {
	return NewSearchTargetCancellable(nil, snapshot, files)
}

// NewSearchTargetCancellable constructs and validates a search target.
func NewSearchTargetCancellable(
	cancelled func() error,
	snapshot Snapshot,
	files []FileID,
) (SearchTarget, error) {
	if snapshot.SourceID() == 0 || len(files) == 0 || len(files) > MaxFiles {
		return SearchTarget{}, fmt.Errorf("%w: invalid search target size", ErrInvalidSearch)
	}
	if err := cancellationError(cancelled); err != nil {
		return SearchTarget{}, err
	}
	owned := make([]FileID, len(files))
	seen := make([]uint64, searchTargetBitmapWords)
	for index, fileID := range files {
		if index%searchCancellationInterval == 0 {
			if err := cancellationError(cancelled); err != nil {
				return SearchTarget{}, err
			}
		}
		if fileID >= MaxFiles {
			return SearchTarget{}, fmt.Errorf("%w: search target file is unavailable", ErrInvalidSearch)
		}
		word := uint32(fileID) / 64
		bit := uint32(fileID) % 64
		if seen[word]&(uint64(1)<<bit) != 0 {
			return SearchTarget{}, fmt.Errorf("%w: duplicate search target file", ErrInvalidSearch)
		}
		if _, exists := snapshot.File(fileID); !exists {
			return SearchTarget{}, fmt.Errorf("%w: search target file is unavailable", ErrInvalidSearch)
		}
		seen[word] |= uint64(1) << bit
		owned[index] = fileID
	}
	return SearchTarget{source: snapshot.SourceID(), files: owned}, nil
}

// FileCount returns the receiver's file count value.
func (target SearchTarget) FileCount() int {
	return len(target.files)
}

// Files returns the receiver's ordered file identities.
func (target SearchTarget) Files() iter.Seq[FileID] {
	return slices.Values(target.files)
}

// RetainedBytes returns the receiver's retained bytes value.
func (target SearchTarget) RetainedBytes() int64 {
	return int64(cap(target.files)) * searchFileIDStorageBytes
}

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
	excerpt   string
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

// Excerpt returns a bounded excerpt around the match.
func (hit SearchHit) Excerpt() string {
	return hit.excerpt
}

// RetainedBytes returns the receiver's retained bytes value.
func (hit SearchHit) RetainedBytes() int64 {
	return SearchHitStorageBytes + int64(len(hit.excerpt))
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
	hitBytes         int64
	operation        OperationID
	unavailableFiles uint32
	status           SearchStatus
}

// SearchAdmission is an opaque target-independent search admission.
type SearchAdmission struct {
	query     SearchQuery
	operation OperationID
	workspace WorkspaceRevision
	source    SourceID
	context   PatchContext
}

// Operation returns the admitted operation identity.
func (admission SearchAdmission) Operation() OperationID { return admission.operation }

// Workspace returns the admitted workspace identity.
func (admission SearchAdmission) Workspace() WorkspaceRevision { return admission.workspace }

// Source returns the admitted source identity.
func (admission SearchAdmission) Source() SourceID { return admission.source }

// Query returns the admitted query.
func (admission SearchAdmission) Query() SearchQuery { return admission.query }

func (admission SearchAdmission) valid() bool {
	return admission.operation != 0 && admission.workspace != 0 && admission.source != 0 &&
		admission.query != "" && admission.context.valid()
}

// SearchRequest is a complete worker-local search request.
type SearchRequest struct {
	query     PreparedSearchQuery
	target    SearchTarget
	operation OperationID
	workspace WorkspaceRevision
	source    SourceID
	context   PatchContext
}

// Operation returns the request operation identity.
func (request SearchRequest) Operation() OperationID { return request.operation }

// Workspace returns the request workspace identity.
func (request SearchRequest) Workspace() WorkspaceRevision { return request.workspace }

// Source returns the request source identity.
func (request SearchRequest) Source() SourceID { return request.source }

// Context returns the request patch context.
func (request SearchRequest) Context() PatchContext { return request.context }

// Target returns the request target.
func (request SearchRequest) Target() SearchTarget { return request.target }

// Query returns the prepared request query.
func (request SearchRequest) Query() PreparedSearchQuery { return request.query }

// SearchResult describes search result.
type SearchResult struct {
	detail           OperationDetail
	hits             []SearchHit
	operation        OperationID
	workspace        WorkspaceRevision
	source           SourceID
	hitBytes         int64
	unavailableFiles uint32
	status           SearchStatus
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
	return TakeSearchResultCancellable(nil, descriptor, hits)
}

// TakeSearchResultCancellable validates and takes ownership of a search result.
func TakeSearchResultCancellable(
	cancelled func() error,
	descriptor SearchResultDescriptor,
	hits []SearchHit,
) (SearchResult, error) {
	if descriptor.Operation == 0 || descriptor.Workspace == 0 || descriptor.Source == 0 ||
		!terminalSearchStatus(descriptor.Status) || len(hits) > MaxSearchHits {
		return SearchResult{}, fmt.Errorf("%w: malformed terminal result", ErrInvalidSearch)
	}
	if err := cancellationError(cancelled); err != nil {
		return SearchResult{}, err
	}
	retained := int64(cap(hits)) * SearchHitStorageBytes
	for index, hit := range hits {
		if index%searchCancellationInterval == 0 {
			if err := cancellationError(cancelled); err != nil {
				return SearchResult{}, err
			}
		}
		if hit.fileID >= MaxFiles || hit.patchLine >= MaxPatchLines ||
			hit.byteStart > hit.byteEnd || len(hit.excerpt) > MaxSearchExcerptBytes ||
			hit.hunk != noPatchHunk && hit.hunk >= MaxHunks {
			return SearchResult{}, fmt.Errorf("%w: malformed hit", ErrInvalidSearch)
		}
		retained += int64(len(hit.excerpt))
	}
	if retained > MaxSearchHitBytes {
		return SearchResult{}, fmt.Errorf("%w: malformed terminal result", ErrInvalidSearch)
	}
	return SearchResult{
		operation:        descriptor.Operation,
		workspace:        descriptor.Workspace,
		source:           descriptor.Source,
		status:           descriptor.Status,
		hits:             hits,
		hitBytes:         retained,
		unavailableFiles: descriptor.UnavailableFiles,
		detail:           descriptor.Detail,
	}, nil
}

// Operation returns the result operation identity.
func (result SearchResult) Operation() OperationID { return result.operation }

// Workspace returns the result workspace identity.
func (result SearchResult) Workspace() WorkspaceRevision { return result.workspace }

// Source returns the result source identity.
func (result SearchResult) Source() SourceID { return result.source }

// Status returns the terminal result status.
func (result SearchResult) Status() SearchStatus { return result.status }

// RetainedBytes returns the result-owned hit and detail bytes.
func (result SearchResult) RetainedBytes() int64 {
	return result.hitBytes + result.detail.Bytes()
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
	context PatchContext,
) (Review, SearchAdmission, error) {
	if review.search.status == SearchActive || query == "" || !context.valid() ||
		review.workspace == 0 || review.snapshot.SourceID() == 0 {
		return review, SearchAdmission{}, ErrInvalidSearch
	}
	next, err := review.BeginOperation(id, OperationSearch)
	if err != nil {
		return review, SearchAdmission{}, err
	}
	next.search = searchState{
		status:    SearchActive,
		operation: id,
		query:     query,
	}
	return next, SearchAdmission{
		operation: id,
		workspace: review.workspace,
		source:    review.snapshot.SourceID(),
		query:     query,
		context:   context,
	}, nil
}

// ApplySearch performs the apply search operation.
func (review Review) ApplySearch(result SearchResult) (Review, error) {
	if review.search.status != SearchActive ||
		review.search.operation != result.operation ||
		review.workspace != result.workspace ||
		review.snapshot.SourceID() != result.source ||
		!terminalSearchStatus(result.status) || len(result.hits) > MaxSearchHits ||
		result.hitBytes < 0 || result.hitBytes > MaxSearchHitBytes {
		return review, ErrInvalidSearch
	}
	next := review.clone()
	next.search = searchState{
		status:           result.status,
		operation:        result.operation,
		query:            review.search.query,
		hits:             result.hits,
		hitBytes:         result.hitBytes,
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
	prepared, err := PrepareSearchQueryCancellable(nil, query)
	if err != nil {
		return PreparedSearchQuery{}
	}
	return prepared
}

// PrepareSearchQueryCancellable prepares a search query with bounded cancellation work.
func PrepareSearchQueryCancellable(
	cancelled func() error,
	query SearchQuery,
) (PreparedSearchQuery, error) {
	if err := validateSearchQueryCancellable(cancelled, query); err != nil {
		return PreparedSearchQuery{}, err
	}
	bytes := make([]byte, len(query))
	for start := 0; start < len(bytes); start += searchCancellationInterval {
		if err := cancellationError(cancelled); err != nil {
			return PreparedSearchQuery{}, err
		}
		end := min(start+searchCancellationInterval, len(bytes))
		copy(bytes[start:end], string(query)[start:end])
	}
	if err := cancellationError(cancelled); err != nil {
		return PreparedSearchQuery{}, err
	}
	prepared := PreparedSearchQuery{
		bytes:  bytes,
		prefix: make([]uint32, len(bytes)),
	}
	var matched uint32
	for index := 1; index < len(bytes); index++ {
		if index%searchCancellationInterval == 0 {
			if err := cancellationError(cancelled); err != nil {
				return PreparedSearchQuery{}, err
			}
		}
		for matched != 0 && bytes[index] != bytes[matched] {
			matched = prepared.prefix[matched-1]
		}
		if bytes[index] == bytes[matched] {
			matched++
		}
		prepared.prefix[index] = matched
	}
	return prepared, nil
}

// RetainedBytes returns the prepared query backing and slice-header bytes.
func (query PreparedSearchQuery) RetainedBytes() int64 {
	return preparedSearchQueryMetadataBytes + int64(cap(query.bytes)) +
		int64(cap(query.prefix))*searchUint32StorageBytes
}

// SearchCandidateRetainedBytes returns the retained backing charge for candidate capacity.
func SearchCandidateRetainedBytes(capacity int) (int64, error) {
	if capacity < 0 {
		return 0, ErrInvalidSearch
	}
	return int64(capacity) * searchFileIDStorageBytes, nil
}

// SearchTargetConstructionBytes returns the complete target construction peak.
func SearchTargetConstructionBytes(fileCount int) (int64, error) {
	if fileCount <= 0 || fileCount > MaxFiles {
		return 0, ErrInvalidSearch
	}
	targetBytes := int64(fileCount) * searchFileIDStorageBytes
	bitmapBytes := int64(searchTargetBitmapWords) * searchUint64StorageBytes
	return searchTargetMetadataBytes + targetBytes + bitmapBytes, nil
}

// SearchRequestConstructionBytes returns the retained complete-request charge.
func SearchRequestConstructionBytes(query SearchQuery, fileCount int) (int64, error) {
	if query == "" || len(query) > MaxSearchQueryBytes || fileCount <= 0 || fileCount > MaxFiles {
		return 0, ErrInvalidSearch
	}
	targetBytes := int64(fileCount) * searchFileIDStorageBytes
	preparedBytes := int64(len(query)) +
		int64(len(query))*searchUint32StorageBytes
	return searchRequestMetadataBytes + targetBytes + preparedBytes, nil
}

// PrepareSearchRequestCancellable converts an admission and target into a complete request.
func PrepareSearchRequestCancellable(
	cancelled func() error,
	admission SearchAdmission,
	target SearchTarget,
) (SearchRequest, error) {
	if !admission.valid() || target.source != admission.source || target.FileCount() == 0 {
		return SearchRequest{}, ErrInvalidSearch
	}
	query, err := PrepareSearchQueryCancellable(cancelled, admission.query)
	if err != nil {
		return SearchRequest{}, err
	}
	return SearchRequest{
		query:     query,
		target:    target,
		operation: admission.operation,
		workspace: admission.workspace,
		source:    admission.source,
		context:   admission.context,
	}, nil
}

// SearchCursor represents search cursor.
type SearchCursor struct {
	line    uint32
	offset  uint32
	matched uint32
}

// SearchBatch represents search batch.
type SearchBatch struct {
	hits   []SearchHit
	cursor SearchCursor
	done   bool
}

// Cursor returns the cursor immediately after this batch.
func (batch SearchBatch) Cursor() SearchCursor { return batch.cursor }

// Done reports whether the patch scan is complete.
func (batch SearchBatch) Done() bool { return batch.done }

// HitCount returns the number of hits in this batch.
func (batch SearchBatch) HitCount() int { return len(batch.hits) }

// Hit returns one immutable hit value from this batch.
func (batch SearchBatch) Hit(index int) (SearchHit, bool) {
	if index < 0 || index >= len(batch.hits) {
		return SearchHit{}, false
	}
	return batch.hits[index], true
}

// ScanSearchBatch scans search batch.
func ScanSearchBatch(
	patch *PreparedPatch,
	fileID FileID,
	query PreparedSearchQuery,
	cursor SearchCursor,
) SearchBatch {
	result := SearchBatch{cursor: cursor}
	if patch == nil || len(query.bytes) == 0 || int(cursor.line) >= len(patch.lineOffsets) {
		result.done = true
		return result
	}
	inspectedBytes := 0
	inspectedLines := 0
	for int(result.cursor.line) < len(patch.lineOffsets) &&
		inspectedBytes < MaxSearchBatchBytes &&
		inspectedLines < MaxSearchBatchLines &&
		len(result.hits) < maxSearchBatchHits {
		lineIndex := result.cursor.line
		kind := patch.lineKinds[lineIndex].kind()
		if kind != PatchLineContext && kind != PatchLineAddition && kind != PatchLineDeletion {
			result.cursor.line++
			result.cursor.offset = 0
			result.cursor.matched = 0
			inspectedLines++
			continue
		}
		line := patch.lineBytes(lineIndex)
		if len(line) != 0 {
			line = line[1:]
		}
		if int(result.cursor.offset) >= len(line) {
			result.cursor.line++
			result.cursor.offset = 0
			result.cursor.matched = 0
			inspectedLines++
			continue
		}
		for int(result.cursor.offset) < len(line) &&
			inspectedBytes < MaxSearchBatchBytes &&
			len(result.hits) < maxSearchBatchHits {
			value := line[result.cursor.offset]
			for result.cursor.matched != 0 &&
				value != query.bytes[result.cursor.matched] {
				result.cursor.matched = query.prefix[result.cursor.matched-1]
			}
			if value == query.bytes[result.cursor.matched] {
				result.cursor.matched++
			}
			result.cursor.offset++
			inspectedBytes++
			if int(result.cursor.matched) == len(query.bytes) {
				end := result.cursor.offset
				start := end - uint32FromInt(len(query.bytes))
				result.hits = appendSearchBatchHit(result.hits, SearchHit{
					excerpt:   boundedSearchExcerpt(line, start, end),
					fileID:    fileID,
					patchLine: lineIndex,
					hunk:      patch.lineHunks[lineIndex],
					oldLine:   patch.oldLines[lineIndex],
					newLine:   patch.newLines[lineIndex],
					byteStart: start,
					byteEnd:   end,
				})
				result.cursor.matched = 0
			}
		}
	}
	result.done = int(result.cursor.line) >= len(patch.lineOffsets)
	return result
}

func appendSearchBatchHit(hits []SearchHit, hit SearchHit) []SearchHit {
	if len(hits) < cap(hits) {
		return append(hits, hit)
	}
	required := len(hits) + 1
	capacity, ok := nextSearchBatchCapacity(cap(hits), required)
	if !ok {
		return hits
	}
	grown := make([]SearchHit, len(hits), capacity)
	copy(grown, hits)
	return append(grown, hit)
}

func nextSearchBatchCapacity(current, required int) (int, bool) {
	if current < 0 || current > maxSearchBatchHits || required <= current ||
		required > maxSearchBatchHits {
		return 0, false
	}
	candidate := searchHitGrowthSeed
	if current != 0 {
		if current > maxSearchBatchHits/2 {
			candidate = maxSearchBatchHits
		} else {
			candidate = current * 2
		}
	}
	candidate = min(max(candidate, searchHitGrowthSeed), maxSearchBatchHits)
	return candidate, candidate >= required
}

func boundedSearchExcerpt(line []byte, matchStart, matchEnd uint32) string {
	if len(line) <= MaxSearchExcerptBytes {
		return string(line)
	}
	center := (int(matchStart) + int(matchEnd)) / 2
	start := max(center-MaxSearchExcerptBytes/2, 0)
	end := min(start+MaxSearchExcerptBytes, len(line))
	start = max(end-MaxSearchExcerptBytes, 0)
	return string(line[start:end])
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
