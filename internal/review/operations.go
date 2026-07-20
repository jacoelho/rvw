package review

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"
)

var ErrInvalidOperation = errors.New("invalid operation transition")

const (
	MaxPendingOperations       = 64
	MaxStatusRecords           = 256
	MaxStatusBytes       int64 = 512 << 10
	MaxOperationDetail   int64 = 8 << 10
)

type OperationKind uint8

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

type OperationStatus uint8

const (
	OperationPending OperationStatus = iota + 1
	OperationSucceeded
	OperationFailed
	OperationCancelled
	OperationSuperseded
	OperationLimited
	OperationUnknown
)

type Operation struct {
	ID     OperationID
	Kind   OperationKind
	Status OperationStatus
	Detail OperationDetail
}

type OperationDetail string

const EmptyOperationDetail OperationDetail = ""

func (detail OperationDetail) String() string {
	return string(detail)
}

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
		return "invalid"
	}
}

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
		return "invalid"
	}
}

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

func (review Review) ResolveOperation(
	id OperationID,
	status OperationStatus,
	detail OperationDetail,
) (Review, error) {
	operation, exists := review.operations[id]
	if !exists || operation.Status != OperationPending || !terminalOperationStatus(status) {
		return review, ErrInvalidOperation
	}
	if int64(len(detail)) > MaxOperationDetail || !utf8.ValidString(detail.String()) {
		return review, fmt.Errorf("%w: operation detail is invalid or exceeds %d bytes", ErrInvalidOperation, MaxOperationDetail)
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

func (review Review) SettleUnknownOperation(
	id OperationID,
	status OperationStatus,
	detail OperationDetail,
) (Review, error) {
	operation, exists := review.operations[id]
	if !exists || operation.Status != OperationUnknown ||
		status != OperationSucceeded && status != OperationFailed ||
		int64(len(detail)) > MaxOperationDetail ||
		!utf8.ValidString(detail.String()) {
		return review, ErrInvalidOperation
	}
	next := review.clone()
	next.operations = maps.Clone(review.operations)
	operation.Status = status
	operation.Detail = detail
	next.operations[id] = operation
	return next, nil
}

func (review Review) Operations() map[OperationID]Operation {
	return maps.Clone(review.operations)
}

func (review Review) OperationCount() int {
	return len(review.operationIDs)
}

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
		detailBytes += int64(len(operation.Detail))
		if detailBytes > MaxStatusBytes {
			return nil, fmt.Errorf("%w: unresolved status bytes exceed %d", ErrInvalidOperation, MaxStatusBytes)
		}
		result[id] = operation
	}
	for index := len(settled) - 1; index >= 0 && len(result) < limit; index-- {
		operation := operations[settled[index]]
		if detailBytes+int64(len(operation.Detail)) > MaxStatusBytes {
			continue
		}
		detailBytes += int64(len(operation.Detail))
		result[operation.ID] = operation
	}
	return result, nil
}
