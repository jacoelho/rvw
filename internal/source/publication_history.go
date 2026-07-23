package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/jacoelho/rvw/internal/review"
)

// Finish validates and classifies acquired history without network I/O.
func (acquisition *PublicationReconciliationAcquisition) Finish(
	ctx context.Context,
	runCPU CPUAdmission,
) review.ReconciliationResult {
	if acquisition == nil || acquisition.spool == nil {
		return review.ReconciliationResult{
			Disposition: review.ReconciliationStillUnknown,
			Detail:      boundedOperationDetail("publication reconciliation acquisition is unavailable"),
		}
	}
	request := acquisition.request
	result := review.ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: review.ReconciliationStillUnknown,
	}
	spool := acquisition.spool
	acquisition.spool = nil
	if runCPU == nil {
		result.Detail = boundedOperationDetail(errors.Join(
			errors.New("publication reconciliation CPU admission is unavailable"),
			spool.Close(),
		).Error())
		return result
	}
	ledger, ledgerErr := newSourceLedger(0)
	if ledgerErr != nil {
		result.Detail = boundedOperationDetail(errors.Join(ledgerErr, spool.Close()).Error())
		return result
	}
	limits, limitsErr := newJSONFramingLimits(
		maxGitHubReviewHistoryItems,
		maxGitHubPageCollectionElements,
	)
	if limitsErr != nil {
		result.Detail = boundedOperationDetail(errors.Join(limitsErr, spool.Close()).Error())
		return result
	}
	metrics, lexicalErr := validateCommandJSONWithLimits(
		ctx,
		spool,
		runCPU,
		ledger,
		limits,
	)
	if lexicalErr != nil {
		result.Detail = boundedOperationDetail(errors.Join(lexicalErr, spool.Close()).Error())
		return result
	}
	matches, decodeErr := countReviewHistoryMarkersReaderContext(
		ctx,
		spool.Reader(),
		request.Marker.String(),
		runCPU,
		ledger,
		metrics,
	)
	metrics.release(ledger)
	err := errors.Join(decodeErr, spool.Close())
	if err != nil {
		result.Detail = boundedOperationDetail(err.Error())
		return result
	}
	if matches > 1 {
		result.Detail = boundedOperationDetail("publication marker is ambiguous in GitHub review history")
		return result
	}
	if matches == 1 {
		result.Disposition = review.ReconciliationCommitted
		result.Detail = boundedOperationDetail("publication marker confirmed")
	} else {
		result.Disposition = review.ReconciliationNotCommitted
		result.Detail = boundedOperationDetail("publication marker confirmed absent")
	}
	return result
}

func countReviewHistoryMarkersReaderContext(
	ctx context.Context,
	input io.Reader,
	marker string,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
) (int, error) {
	return walkReviewHistoryFrames(ctx, input, runCPU, ledger, metrics, marker)
}

func countPublicationMarkersCancellable(
	ctx context.Context,
	body string,
	marker string,
) (int, error) {
	if marker == "" {
		return 0, errors.New("publication marker is empty")
	}
	matches := 0
	searchStart := 0
	for searchStart <= len(body)-len(marker) {
		if err := context.Cause(ctx); err != nil {
			return 0, err
		}
		checkpoint := min(searchStart+sourceCPUPollBytes, len(body))
		windowEnd := min(checkpoint+len(marker)-1, len(body))
		match := strings.Index(body[searchStart:windowEnd], marker)
		if match < 0 || searchStart+match >= checkpoint {
			searchStart = checkpoint
			continue
		}
		matches++
		searchStart += match + len(marker)
	}
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	return matches, nil
}

type reviewHistoryJSONCollector struct {
	ctx          context.Context
	marker       string
	ledger       *sourceLedger
	identities   []uint64
	body         string
	scope        *jsonDecodeScope
	path         jsonPathTracker
	duplicates   jsonDuplicateGuard
	identityByte int64
	itemMark     int64
	id           uint64
	plannedItems int
	matches      int
	items        int
	pages        int
	root         bool
	hasID        bool
	hasBody      bool
}

func walkReviewHistoryFrames(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	marker string,
) (int, error) {
	if marker == "" {
		return 0, errors.New("GitHub review history marker is unavailable")
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return 0, err
	}
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		scope.Close()
		return 0, err
	}
	collector := reviewHistoryJSONCollector{
		ctx: ctx, marker: marker, ledger: ledger, scope: scope, itemMark: -1,
	}
	defer collector.close()
	if err := walkJSONFrames(ctx, input, runCPU, ledger, metrics, collector.consume); err != nil {
		return 0, fmt.Errorf("decode GitHub review history: %w", err)
	}
	return collector.finish(runCPU)
}

func (collector *reviewHistoryJSONCollector) consume(frame jsonFrame) error {
	switch frame.Kind {
	case jsonObjectKey:
		if err := collector.path.keyFrame(collector.scope, frame); err != nil {
			return err
		}
		return collector.duplicates.key(&collector.path)
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		return collector.startArray(frame.Count)
	case jsonObjectStart:
		if err := collector.path.start(frame.Kind, 0); err != nil {
			return err
		}
		collector.duplicates.startObject(collector.path.depth)
		return collector.startObject()
	case jsonArrayEnd:
		return collector.path.end(frame.Kind)
	case jsonObjectEnd:
		if collector.isItemObject() {
			if err := collector.finishItem(); err != nil {
				return err
			}
		}
		collector.duplicates.endObject(collector.path.depth)
		return collector.path.end(frame.Kind)
	default:
		return collector.path.scalar(func(value jsonPathValue) error {
			return collector.scalar(value.name, frame)
		})
	}
}

func (collector *reviewHistoryJSONCollector) startArray(count int) error {
	switch {
	case collector.path.depth == 1:
		if collector.root {
			return errors.New("GitHub review history has multiple roots")
		}
		collector.root = true
	case collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart:
		collector.pages++
		return collector.reservePage(count)
	case collector.path.depth == 3 && collector.path.stack[1].kind == jsonArrayStart:
		return fmt.Errorf("GitHub review history page %d item is not an object", collector.pages)
	case collector.recognizedItemContainer():
		return collector.invalidItemField()
	}
	return nil
}

func (collector *reviewHistoryJSONCollector) startObject() error {
	switch {
	case collector.path.depth == 1:
		return errors.New("GitHub review history root is not a pagination array")
	case collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart:
		return errors.New("GitHub review history page is not an array")
	case collector.path.depth == 3 && collector.path.stack[1].kind == jsonArrayStart:
		collector.itemMark = collector.scope.mark()
		if collector.itemMark < 0 {
			return errors.New("GitHub review history item scope is unavailable")
		}
		collector.id = 0
		collector.body = ""
		collector.hasID = false
		collector.hasBody = false
	case collector.recognizedItemContainer():
		return collector.invalidItemField()
	}
	return nil
}

func (collector *reviewHistoryJSONCollector) scalar(name string, frame jsonFrame) error {
	switch collector.path.depth {
	case 0:
		return errors.New("GitHub review history root is not a pagination array")
	case 1:
		return errors.New("GitHub review history page is not an array")
	case 2:
		return fmt.Errorf("GitHub review history page %d item is not an object", collector.pages)
	case 3:
		switch name {
		case "id":
			id, err := jsonUint64Value(frame)
			if err != nil || id == 0 {
				return collector.invalidItemField()
			}
			collector.id = id
			collector.hasID = true
		case graphqlBody:
			body, err := collector.scope.string(frame)
			if err != nil {
				return collector.invalidItemField()
			}
			collector.body = body
			collector.hasBody = true
		}
	}
	return nil
}

func (collector *reviewHistoryJSONCollector) finishItem() error {
	if err := context.Cause(collector.ctx); err != nil {
		return err
	}
	item := collector.items + 1
	if !collector.hasID {
		return fmt.Errorf(
			"GitHub review history page %d item %d has no identity",
			collector.pages,
			item,
		)
	}
	if !collector.hasBody {
		return fmt.Errorf(
			"GitHub review history page %d item %d has no body",
			collector.pages,
			item,
		)
	}
	if len(collector.identities) == cap(collector.identities) {
		return errors.New("GitHub review history item accounting is incomplete")
	}
	collector.identities = append(collector.identities, collector.id)
	matches, err := countPublicationMarkersCancellable(
		collector.ctx,
		collector.body,
		collector.marker,
	)
	if err != nil {
		return err
	}
	if matches > math.MaxInt-collector.matches {
		return errors.New("GitHub review history marker count overflows")
	}
	collector.matches += matches
	collector.items++
	collector.body = ""
	if err := collector.scope.releaseTo(collector.itemMark); err != nil {
		return err
	}
	collector.itemMark = -1
	return nil
}

func (collector *reviewHistoryJSONCollector) reservePage(count int) error {
	if count < 0 || collector.plannedItems > maxGitHubReviewHistoryItems-count {
		return fmt.Errorf("GitHub review history exceeds %d items", maxGitHubReviewHistoryItems)
	}
	collector.plannedItems += count
	if collector.plannedItems <= cap(collector.identities) {
		return nil
	}
	bytes, err := reviewHistoryIdentitiesBytes(collector.plannedItems)
	if err != nil {
		return err
	}
	if err := collector.ledger.reserve(bytes); err != nil {
		return err
	}
	grown := make([]uint64, len(collector.identities), collector.plannedItems)
	copy(grown, collector.identities)
	collector.ledger.release(collector.identityByte)
	collector.identities = grown
	collector.identityByte = bytes
	return nil
}

func (collector *reviewHistoryJSONCollector) finish(runCPU CPUAdmission) (int, error) {
	if !collector.root || collector.pages == 0 || collector.path.depth != 0 ||
		collector.items != collector.plannedItems {
		return 0, errors.New("GitHub review history contains no complete pagination result")
	}
	if err := validateReviewHistoryIdentities(
		collector.ctx,
		collector.identities,
		runCPU,
		collector.ledger,
	); err != nil {
		return 0, err
	}
	return collector.matches, nil
}

func (collector *reviewHistoryJSONCollector) close() {
	collector.body = ""
	collector.path.close()
	collector.scope.Close()
	collector.scope = nil
	collector.ledger.release(collector.identityByte)
	collector.identities = nil
	collector.identityByte = 0
	collector.itemMark = -1
}

func (collector *reviewHistoryJSONCollector) isItemObject() bool {
	return collector.path.depth == 3 && collector.path.stack[1].kind == jsonArrayStart &&
		collector.path.stack[2].kind == jsonObjectStart
}

func (collector *reviewHistoryJSONCollector) recognizedItemContainer() bool {
	if collector.path.depth != 4 || !collector.isNestedInItem() {
		return false
	}
	name := collector.path.stack[3].name
	return name == "id" || name == graphqlBody
}

func (collector *reviewHistoryJSONCollector) isNestedInItem() bool {
	return collector.path.stack[1].kind == jsonArrayStart &&
		collector.path.stack[2].kind == jsonObjectStart
}

func (collector *reviewHistoryJSONCollector) invalidItemField() error {
	return fmt.Errorf(
		"GitHub review history page %d item %d has an invalid identity or body",
		collector.pages,
		collector.items+1,
	)
}
