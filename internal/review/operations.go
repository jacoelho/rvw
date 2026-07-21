package review

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	invalidValue            = "invalid"
	publicationCommentValue = "comment"
)

// ErrInvalidOperation identifies an invalid operation failure.
var ErrInvalidOperation = errors.New("invalid operation transition")

// MaxPendingOperations defines the supported pending operations limit.
const (
	MaxPendingOperations       = 64
	MaxStatusRecords           = 256
	MaxStatusBytes       int64 = 512 << 10
	MaxOperationDetail   int64 = 8 << 10
)

// OperationKind describes operation kind.
type OperationKind uint8

// OperationPatch identifies a supported operation patch value.
const (
	OperationPatch OperationKind = iota + 1
	OperationReload
	OperationSearch
	OperationSyntax
	OperationExport
	OperationDelivery
	OperationPublication
	OperationReconciliation
)

// OperationStatus describes operation status.
type OperationStatus uint8

// OperationPending identifies a supported operation pending value.
const (
	OperationPending OperationStatus = iota + 1
	OperationSucceeded
	OperationFailed
	OperationCancelled
	OperationSuperseded
	OperationLimited
	OperationUnknown
)

// Operation represents operation.
type Operation struct {
	Detail OperationDetail
	ID     OperationID
	Kind   OperationKind
	Status OperationStatus
}

// OperationDetail represents operation detail.
type OperationDetail struct {
	value string
}

// EmptyOperationDetail performs the empty operation detail operation.
func EmptyOperationDetail() OperationDetail {
	return OperationDetail{}
}

// NewOperationDetail constructs and validates operation detail.
func NewOperationDetail(value string) (OperationDetail, error) {
	if int64(len(value)) > MaxOperationDetail || !utf8.ValidString(value) {
		return OperationDetail{}, fmt.Errorf(
			"%w: operation detail is invalid or exceeds %d bytes",
			ErrInvalidOperation,
			MaxOperationDetail,
		)
	}
	return OperationDetail{value: strings.Clone(value)}, nil
}

// NewBoundedOperationDetail converts external text to a valid operation
// detail while preserving whitespace. Work and temporary allocation are
// bounded independently of the original input length.
func NewBoundedOperationDetail(value string) OperationDetail {
	return mustOperationDetail(boundedOperationDetailText(value))
}

// NewBoundedTrimmedOperationDetail converts external text to a valid
// operation detail and trims its bounded result. Work and temporary allocation
// are bounded independently of the original input length.
func NewBoundedTrimmedOperationDetail(value string) OperationDetail {
	return mustOperationDetail(strings.TrimSpace(boundedOperationDetailText(value)))
}

func boundedOperationDetailText(value string) string {
	if len(value) > int(MaxOperationDetail) {
		value = value[:MaxOperationDetail]
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) > int(MaxOperationDetail) {
		value = value[:MaxOperationDetail]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

// String returns the receiver's string value.
func (detail OperationDetail) String() string {
	return detail.value
}

// Bytes returns the receiver's bytes value.
func (detail OperationDetail) Bytes() int64 {
	return int64(len(detail.value))
}

// IsEmpty reports the receiver's is empty state.
func (detail OperationDetail) IsEmpty() bool {
	return detail.value == ""
}

func mustOperationDetail(value string) OperationDetail {
	detail, err := NewOperationDetail(value)
	if err != nil {
		panic(err)
	}
	return detail
}

// String returns the receiver's string value.
func (kind OperationKind) String() string {
	switch kind {
	case OperationPatch:
		return "patch"
	case OperationReload:
		return "reload"
	case OperationSearch:
		return "search"
	case OperationSyntax:
		return "syntax"
	case OperationExport:
		return "export"
	case OperationDelivery:
		return "delivery"
	case OperationPublication:
		return "publication"
	case OperationReconciliation:
		return "reconciliation"
	default:
		return invalidValue
	}
}

// String returns the receiver's string value.
func (status OperationStatus) String() string {
	switch status {
	case OperationPending:
		return "pending"
	case OperationSucceeded:
		return "succeeded"
	case OperationFailed:
		return "failed"
	case OperationCancelled:
		return "cancelled"
	case OperationSuperseded:
		return "superseded"
	case OperationLimited:
		return "limited"
	case OperationUnknown:
		return "unknown"
	default:
		return invalidValue
	}
}

// BeginOperation performs the begin operation operation.
func (review Review) BeginOperation(id OperationID, kind OperationKind) (Review, error) {
	if id == 0 || !validOperationKind(kind) {
		return review, ErrInvalidOperation
	}
	if _, exists := review.operations[id]; exists {
		return review, fmt.Errorf("%w: operation %d already exists", ErrInvalidOperation, id)
	}
	pending := 0
	for _, operation := range review.operations {
		if operation.Status == OperationPending {
			pending++
		}
	}
	if pending >= MaxPendingOperations {
		return review, fmt.Errorf("%w: pending-operation capacity reached", ErrInvalidOperation)
	}
	operations, err := compactOperations(review.operations, MaxStatusRecords-1)
	if err != nil {
		return review, err
	}
	next := review.clone()
	next.operations = operations
	next.operations[id] = Operation{ID: id, Kind: kind, Status: OperationPending}
	next.operationIDs = sortedOperationIDs(next.operations)
	return next, nil
}

// ResolveOperation performs the resolve operation operation.
func (review Review) ResolveOperation(
	id OperationID,
	status OperationStatus,
	detail OperationDetail,
) (Review, error) {
	operation, exists := review.operations[id]
	if !exists || operation.Status != OperationPending || !terminalOperationStatus(status) {
		return review, ErrInvalidOperation
	}
	next := review.clone()
	next.operations = maps.Clone(review.operations)
	operation.Status = status
	operation.Detail = detail
	next.operations[id] = operation
	compacted, err := compactOperations(next.operations, MaxStatusRecords)
	if err != nil {
		return review, err
	}
	next.operations = compacted
	next.operationIDs = sortedOperationIDs(next.operations)
	return next, nil
}

// SettleUnknownOperation performs the settle unknown operation operation.
func (review Review) SettleUnknownOperation(
	id OperationID,
	status OperationStatus,
	detail OperationDetail,
) (Review, error) {
	operation, exists := review.operations[id]
	if !exists || operation.Status != OperationUnknown ||
		status != OperationSucceeded && status != OperationFailed {
		return review, ErrInvalidOperation
	}
	next := review.clone()
	next.operations = maps.Clone(review.operations)
	operation.Status = status
	operation.Detail = detail
	next.operations[id] = operation
	return next, nil
}

// Operations returns the receiver's operations value.
func (review Review) Operations() map[OperationID]Operation {
	return maps.Clone(review.operations)
}

// OperationCount returns the receiver's operation count value.
func (review Review) OperationCount() int {
	return len(review.operationIDs)
}

// NewestOperations constructs and validates est operations.
func (review Review) NewestOperations(offset, count int) []Operation {
	if offset < 0 || count <= 0 || offset >= len(review.operationIDs) {
		return nil
	}
	end := min(offset+count, len(review.operationIDs))
	result := make([]Operation, 0, end-offset)
	for index := offset; index < end; index++ {
		id := review.operationIDs[len(review.operationIDs)-1-index]
		result = append(result, review.operations[id])
	}
	return result
}

func sortedOperationIDs(operations map[OperationID]Operation) []OperationID {
	ids := make([]OperationID, 0, len(operations))
	for id := range operations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func validOperationKind(kind OperationKind) bool {
	return kind >= OperationPatch && kind <= OperationReconciliation
}

func terminalOperationStatus(status OperationStatus) bool {
	return status >= OperationSucceeded && status <= OperationUnknown
}

func compactOperations(
	operations map[OperationID]Operation,
	limit int,
) (map[OperationID]Operation, error) {
	mandatory := make([]OperationID, 0, len(operations))
	settled := make([]OperationID, 0, len(operations))
	for id, operation := range operations {
		if operation.Status == OperationPending || operation.Status == OperationUnknown {
			mandatory = append(mandatory, id)
		} else {
			settled = append(settled, id)
		}
	}
	if len(mandatory) > limit {
		return nil, fmt.Errorf("%w: unresolved status capacity reached", ErrInvalidOperation)
	}
	slices.Sort(mandatory)
	slices.Sort(settled)
	result := make(map[OperationID]Operation, min(limit, len(operations)))
	var detailBytes int64
	for _, id := range mandatory {
		operation := operations[id]
		detailBytes += operation.Detail.Bytes()
		if detailBytes > MaxStatusBytes {
			return nil, fmt.Errorf("%w: unresolved status bytes exceed %d", ErrInvalidOperation, MaxStatusBytes)
		}
		result[id] = operation
	}
	for index := len(settled) - 1; index >= 0 && len(result) < limit; index-- {
		operation := operations[settled[index]]
		if detailBytes+operation.Detail.Bytes() > MaxStatusBytes {
			continue
		}
		detailBytes += operation.Detail.Bytes()
		result[operation.ID] = operation
	}
	return result, nil
}
