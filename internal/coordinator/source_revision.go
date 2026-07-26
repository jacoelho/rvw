// Package coordinator owns asynchronous work and the source revisions that
// work may lazily materialize.
package coordinator

import (
	"sync"

	"github.com/jacoelho/rvw/internal/change"
	"github.com/jacoelho/rvw/internal/source"
)

// SourceRevision keeps immutable opened facts adjacent to, but distinct from,
// the opaque resource owner transferred to the runtime. Retire is one-shot.
type SourceRevision struct {
	opened   *source.Opened
	revision *source.Revision
	mu       sync.Mutex
}

// NewSourceRevision transfers a valid opened/resource pair to the runtime.
func NewSourceRevision(opened source.Opened, revision *source.Revision) *SourceRevision {
	if opened.Snapshot().SourceID() == 0 || revision == nil {
		return nil
	}
	return &SourceRevision{opened: new(opened), revision: revision}
}

// Snapshot returns immutable source facts.
func (revision *SourceRevision) Snapshot() change.Snapshot {
	if revision == nil {
		return change.Snapshot{}
	}
	return revision.opened.Snapshot()
}

// Options returns immutable open options.
func (revision *SourceRevision) Options() source.OpenOptions {
	if revision == nil {
		return source.OpenOptions{}
	}
	return revision.opened.Options()
}

// Local returns immutable local-source details.
func (revision *SourceRevision) Local() (source.LocalDetails, bool) {
	if revision == nil {
		return source.LocalDetails{}, false
	}
	return revision.opened.Local()
}

// PullRequest returns immutable pull-request details.
func (revision *SourceRevision) PullRequest() (source.PullRequestDetails, bool) {
	if revision == nil {
		return source.PullRequestDetails{}, false
	}
	return revision.opened.PullRequest()
}

// PublicationTarget returns the immutable publication boundary value.
func (revision *SourceRevision) PublicationTarget() (source.PublicationTarget, bool) {
	if revision == nil {
		return source.PublicationTarget{}, false
	}
	return revision.opened.PublicationTarget()
}

// Materialization returns the facts and live resource used by one worker.
// The owning runtime must not retire a revision until its admitted work ends.
func (revision *SourceRevision) Materialization() (source.Opened, *source.Revision, bool) {
	if revision == nil {
		return source.Opened{}, nil, false
	}
	revision.mu.Lock()
	defer revision.mu.Unlock()
	return *revision.opened, revision.revision, revision.revision != nil
}

// Active reports whether resource ownership has not been retired.
func (revision *SourceRevision) Active() bool {
	if revision == nil {
		return false
	}
	revision.mu.Lock()
	defer revision.mu.Unlock()
	return revision.revision != nil
}

// Retire transfers resource cleanup ownership exactly once.
func (revision *SourceRevision) Retire() *source.Cleanup {
	if revision == nil {
		return nil
	}
	revision.mu.Lock()
	defer revision.mu.Unlock()
	if revision.revision == nil {
		return nil
	}
	cleanup := revision.revision.Retire()
	revision.revision = nil
	return cleanup
}
