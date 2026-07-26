package projection

import (
	"context"
	"errors"

	"github.com/jacoelho/rvw/internal/change"
)

const (
	maxFileRecordsPerStep = 2_048
	fileOrderCharge       = 4
	pathIndexCharge       = 48
	ambiguousCharge       = 32
)

// PathStatus describes an exact path lookup.
type PathStatus uint8

// Path lookup outcomes.
const (
	PathMissing PathStatus = iota + 1
	PathUnique
	PathAmbiguous
)

// Index is one immutable, source-bound file-order and path projection.
// Returned slices are borrowed and must not be mutated.
type Index struct {
	paths     map[string]change.FileID
	ambiguous map[string]struct{}
	all       []change.FileID
	visible   []change.FileID
	source    change.SourceID
	retained  int64
}

// BuildIndex constructs one complete index under the caller's retained-byte
// ledger. It never publishes partially built fields.
func BuildIndex(
	ctx context.Context,
	snapshot change.Snapshot,
	ledger *Ledger,
) (Index, error) {
	if ctx == nil || snapshot.SourceID() == 0 || ledger == nil {
		return Index{}, errors.New("invalid projection index request")
	}
	maximum := int64(snapshot.FileCount())
	reserved := maximum * (2*fileOrderCharge + pathIndexCharge + ambiguousCharge)
	if err := ledger.Reserve(reserved); err != nil {
		return Index{}, err
	}
	all := make([]change.FileID, 0, snapshot.FileCount())
	visible := make([]change.FileID, 0, snapshot.FileCount())
	paths := make(map[string]change.FileID)
	ambiguous := make(map[string]struct{})
	count := 0
	for file := range snapshot.AllFiles() {
		if count%maxFileRecordsPerStep == 0 {
			if err := context.Cause(ctx); err != nil {
				return Index{}, err
			}
		}
		count++
		all = append(all, file.ID())
		if !file.Flags().Hidden() {
			visible = append(visible, file.ID())
		}
		path, ok := file.IdentityPath().UTF8()
		if !ok {
			continue
		}
		if existing, exists := paths[path]; exists && existing != file.ID() {
			delete(paths, path)
			ambiguous[path] = struct{}{}
		} else if _, exists := ambiguous[path]; !exists {
			paths[path] = file.ID()
		}
	}
	retained := int64(
		(cap(all)+cap(visible))*fileOrderCharge +
			len(paths)*pathIndexCharge +
			len(ambiguous)*ambiguousCharge,
	)
	if retained > reserved {
		return Index{}, errors.New("projection index retained accounting is incomplete")
	}
	ledger.Release(reserved - retained)
	return Index{
		paths: paths, ambiguous: ambiguous, all: all, visible: visible,
		source: snapshot.SourceID(), retained: retained,
	}, nil
}

// Ready reports whether the index was built for a concrete source.
func (index Index) Ready() bool { return index.source != 0 }

// SourceID returns the source identity fenced by this index.
func (index Index) SourceID() change.SourceID { return index.source }

// AllFiles returns the borrowed canonical file order.
func (index Index) AllFiles() []change.FileID { return index.all }

// VisibleFiles returns the borrowed non-hidden file order.
func (index Index) VisibleFiles() []change.FileID { return index.visible }

// LookupPath resolves a UTF-8 path without exposing the index maps.
func (index Index) LookupPath(path string) (change.FileID, PathStatus) {
	if _, exists := index.ambiguous[path]; exists {
		return 0, PathAmbiguous
	}
	id, exists := index.paths[path]
	if !exists {
		return 0, PathMissing
	}
	return id, PathUnique
}

// RetainedBytes returns owned slice and map storage.
func (index Index) RetainedBytes() int64 { return index.retained }
