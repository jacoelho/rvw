package tui

import (
	"errors"
	"fmt"

	"github.com/jacoelho/rvw/internal/change"
)

// ProgramResult contains effects that must be applied after Bubble Tea has
// restored the terminal and the coordinator has settled its workers.
type ProgramResult struct {
	Err    error
	Stdout change.PreparedExport
	write  bool
}

// HasStdout reports whether Stdout contains a prepared post-program value.
func (result ProgramResult) HasStdout() bool {
	return result.write
}

// Result normalizes TUI- and coordinator-owned termination facts into the
// single result consumed by the command adapter.
func (model *Model) Result(settlement ShutdownSettlement) ProgramResult {
	merged := mergePublicationSettlements(
		model.PublicationShutdownSettlement(),
		settlement,
	)
	result := ProgramResult{Err: publicationShutdownError(merged)}
	if pending, ok := model.PendingStdout(); ok {
		result.Stdout = pending
		result.write = true
	}
	return result
}

func mergePublicationSettlements(settlements ...ShutdownSettlement) ShutdownSettlement {
	merged := ShutdownSettlement{}
	indexes := make(map[string]int)
	for _, settlement := range settlements {
		for _, fact := range settlement.Publications {
			key := fmt.Sprintf(
				"%s#%d/%s",
				fact.Target.Repository(),
				fact.Target.PullRequest(),
				fact.Marker,
			)
			if index, exists := indexes[key]; exists {
				if publicationFactRank(fact) > publicationFactRank(merged.Publications[index]) {
					merged.Publications[index] = fact
				}
				continue
			}
			indexes[key] = len(merged.Publications)
			merged.Publications = append(merged.Publications, fact)
		}
	}
	return merged
}

func publicationFactRank(fact PublicationShutdownFact) int {
	if fact.Disposition == change.PublicationDispositionCommitted ||
		fact.Disposition == change.PublicationDispositionFailedBeforeCommit ||
		fact.Disposition == change.PublicationDispositionCancelledBeforeCommit ||
		fact.Reconciliation == change.ReconciliationCommitted ||
		fact.Reconciliation == change.ReconciliationNotCommitted {
		return 2
	}
	return 1
}

func publicationShutdownError(settlement ShutdownSettlement) error {
	var facts []error
	for _, fact := range settlement.Publications {
		target := fmt.Sprintf(
			"%s#%d",
			fact.Target.Repository(),
			fact.Target.PullRequest(),
		)
		switch {
		case fact.Disposition == change.PublicationDispositionUnknown ||
			fact.Reconciliation == change.ReconciliationStillUnknown:
			facts = append(facts, fmt.Errorf(
				"GitHub publication outcome is unknown for %s marker %s; retry risks a duplicate review",
				target,
				fact.Marker,
			))
		case fact.Disposition == change.PublicationDispositionCommitted ||
			fact.Reconciliation == change.ReconciliationCommitted:
			facts = append(facts, fmt.Errorf(
				"GitHub publication committed during shutdown for %s marker %s",
				target,
				fact.Marker,
			))
		}
	}
	return errors.Join(facts...)
}
