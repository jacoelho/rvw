// Package review defines rvw's validated review domain model.
package review

import (
	"errors"
	"fmt"
)

// ErrInvalidChange identifies an invalid change failure.
var ErrInvalidChange = errors.New("invalid file change")

// ChangeKind describes change kind.
type ChangeKind string

// ChangeAdded identifies a supported change added value.
const (
	ChangeAdded             ChangeKind = "added"
	ChangeDeleted           ChangeKind = "deleted"
	ChangeModified          ChangeKind = "modified"
	ChangeRenamed           ChangeKind = "renamed"
	ChangeCopied            ChangeKind = "copied"
	ChangeBinary            ChangeKind = "binary"
	ChangeRenameOnly        ChangeKind = "rename-only"
	ChangeModeOnly          ChangeKind = "mode-only"
	ChangeSubmodule         ChangeKind = "submodule"
	ChangeObjectTypeChanged ChangeKind = "object-type-changed"
)

// Classification represents classification.
type Classification string

// ClassificationUnclassified identifies a supported classification unclassified value.
const ClassificationUnclassified Classification = "unclassified"

// FileMode describes file mode.
type FileMode string

// NewFileMode constructs and validates file mode.
func NewFileMode(value string) (FileMode, error) {
	if !validMode(value) {
		return "", fmt.Errorf("%w: unsupported Git file mode %q", ErrInvalidChange, value)
	}
	return FileMode(value), nil
}

// String returns the receiver's string value.
func (mode FileMode) String() string {
	return string(mode)
}

// Mode describes mode.
type Mode struct {
	old FileMode
	new FileMode
}

// NewMode constructs and validates mode.
func NewMode(oldMode, newMode string) (Mode, error) {
	oldValue, err := NewFileMode(oldMode)
	if err != nil {
		return Mode{}, err
	}
	newValue, err := NewFileMode(newMode)
	if err != nil {
		return Mode{}, err
	}
	return Mode{old: oldValue, new: newValue}, nil
}

// Old returns the receiver's old value.
func (mode Mode) Old() FileMode {
	return mode.old
}

// New returns the receiver's new value.
func (mode Mode) New() FileMode {
	return mode.new
}

// ObjectKind describes object kind.
type ObjectKind string

// ObjectBlob identifies a supported object blob value.
const (
	ObjectBlob    ObjectKind = "blob"
	ObjectGitlink ObjectKind = "gitlink"
)

// GitObject represents git object.
type GitObject struct {
	kind ObjectKind
	oid  ObjectID
	mode FileMode
}

// ObjectID represents object id.
type ObjectID string

// NewObjectID constructs and validates object id.
func NewObjectID(value string) (ObjectID, error) {
	if !validObjectID(value) {
		return "", fmt.Errorf("%w: invalid object ID %q", ErrInvalidChange, value)
	}
	return ObjectID(value), nil
}

// String returns the receiver's string value.
func (id ObjectID) String() string {
	return string(id)
}

// NewGitObject constructs and validates git object.
func NewGitObject(kind ObjectKind, oid ObjectID, mode FileMode) (GitObject, error) {
	if kind != ObjectBlob && kind != ObjectGitlink {
		return GitObject{}, fmt.Errorf("%w: unsupported Git object kind %q", ErrInvalidChange, kind)
	}
	if !validObjectID(oid.String()) {
		return GitObject{}, fmt.Errorf("%w: invalid object ID %q", ErrInvalidChange, oid)
	}
	if !validMode(mode.String()) {
		return GitObject{}, fmt.Errorf("%w: invalid object mode %q", ErrInvalidChange, mode)
	}
	if kind == ObjectGitlink && mode != FileMode("160000") ||
		kind == ObjectBlob &&
			mode != FileMode("100644") &&
			mode != FileMode("100755") &&
			mode != FileMode("120000") {
		return GitObject{}, fmt.Errorf(
			"%w: object kind %q is incompatible with mode %q",
			ErrInvalidChange,
			kind,
			mode,
		)
	}
	return GitObject{kind: kind, oid: oid, mode: mode}, nil
}

// Kind returns the receiver's kind value.
func (object GitObject) Kind() ObjectKind {
	return object.kind
}

// OID returns the receiver's oid value.
func (object GitObject) OID() ObjectID {
	return object.oid
}

// Mode returns the receiver's mode value.
func (object GitObject) Mode() FileMode {
	return object.mode
}

// Change represents change.
type Change struct {
	mode           *Mode
	oldCommit      *ObjectID
	newCommit      *ObjectID
	oldObject      *GitObject
	newObject      *GitObject
	kind           ChangeKind
	classification Classification
	additions      uint32
	deletions      uint32
	hunks          uint32
}

// NewUnclassifiedChange constructs and validates unclassified change.
func NewUnclassifiedChange(kind ChangeKind, additions, deletions uint32) (Change, error) {
	if !isPathKind(kind) {
		return Change{}, fmt.Errorf("%w: %q is not a path kind", ErrInvalidChange, kind)
	}
	if err := validateCounts(additions, deletions, 0); err != nil {
		return Change{}, err
	}
	return Change{
		kind:           kind,
		classification: ClassificationUnclassified,
		additions:      additions,
		deletions:      deletions,
	}, nil
}

// NewClassifiedChange constructs and validates classified change.
func NewClassifiedChange(
	kind ChangeKind,
	additions, deletions, hunks uint32,
	mode *Mode,
) (Change, error) {
	if !isPathKind(kind) {
		return Change{}, fmt.Errorf("%w: %q is not a path kind", ErrInvalidChange, kind)
	}
	if err := validateCounts(additions, deletions, hunks); err != nil {
		return Change{}, err
	}
	return Change{
		kind: kind, additions: additions, deletions: deletions, hunks: hunks,
		mode: cloneValue(mode),
	}, nil
}

// NewSimpleChange constructs and validates simple change.
func NewSimpleChange(kind ChangeKind, mode *Mode) (Change, error) {
	if kind != ChangeBinary && kind != ChangeRenameOnly && kind != ChangeModeOnly {
		return Change{}, fmt.Errorf("%w: %q is not a simple change kind", ErrInvalidChange, kind)
	}
	if kind == ChangeModeOnly &&
		(mode == nil || mode.old == mode.new ||
			mode.old == FileMode("000000") || mode.new == FileMode("000000")) {
		return Change{}, fmt.Errorf(
			"%w: mode-only change requires distinct present modes",
			ErrInvalidChange,
		)
	}
	return Change{kind: kind, mode: cloneValue(mode)}, nil
}

// NewSubmoduleChange constructs and validates submodule change.
func NewSubmoduleChange(oldCommit, newCommit *ObjectID) (Change, error) {
	if oldCommit != nil && !validObjectID(oldCommit.String()) {
		return Change{}, fmt.Errorf("%w: invalid old submodule commit", ErrInvalidChange)
	}
	if newCommit != nil && !validObjectID(newCommit.String()) {
		return Change{}, fmt.Errorf("%w: invalid new submodule commit", ErrInvalidChange)
	}
	if oldCommit == nil && newCommit == nil ||
		oldCommit != nil && newCommit != nil &&
			(*oldCommit == *newCommit ||
				len(oldCommit.String()) != len(newCommit.String())) {
		return Change{}, fmt.Errorf(
			"%w: submodule change requires a changed commit",
			ErrInvalidChange,
		)
	}
	return Change{kind: ChangeSubmodule, oldCommit: cloneValue(oldCommit), newCommit: cloneValue(newCommit)}, nil
}

// NewObjectTypeChange constructs and validates object type change.
func NewObjectTypeChange(
	additions, deletions, hunks uint32,
	oldObject, newObject GitObject,
) (Change, error) {
	if err := validateCounts(additions, deletions, hunks); err != nil {
		return Change{}, err
	}
	if oldObject.kind == "" || newObject.kind == "" || oldObject.kind == newObject.kind {
		return Change{}, fmt.Errorf("%w: object type change requires distinct valid object kinds", ErrInvalidChange)
	}
	if len(oldObject.oid.String()) != len(newObject.oid.String()) {
		return Change{}, fmt.Errorf(
			"%w: object type change requires one object ID format",
			ErrInvalidChange,
		)
	}
	return Change{
		kind:      ChangeObjectTypeChanged,
		additions: additions,
		deletions: deletions,
		hunks:     hunks,
		oldObject: new(oldObject),
		newObject: new(newObject),
	}, nil
}

// Kind returns the receiver's kind value.
func (change Change) Kind() ChangeKind {
	return change.kind
}

// Classification returns the receiver's classification value.
func (change Change) Classification() (Classification, bool) {
	return change.classification, change.classification != ""
}

// Additions returns the receiver's additions value.
func (change Change) Additions() uint32 {
	return change.additions
}

// Deletions returns the receiver's deletions value.
func (change Change) Deletions() uint32 {
	return change.deletions
}

// Hunks returns the receiver's hunks value.
func (change Change) Hunks() uint32 {
	return change.hunks
}

// Mode returns the receiver's mode value.
func (change Change) Mode() (Mode, bool) {
	if change.mode == nil {
		return Mode{}, false
	}
	return *change.mode, true
}

// SubmoduleCommits returns the receiver's submodule commits value.
func (change Change) SubmoduleCommits() (oldCommit, newCommit *ObjectID) {
	return cloneValue(change.oldCommit), cloneValue(change.newCommit)
}

// Objects returns the receiver's objects value.
func (change Change) Objects() (oldObject, newObject GitObject, ok bool) {
	if change.oldObject == nil || change.newObject == nil {
		return GitObject{}, GitObject{}, false
	}
	return *change.oldObject, *change.newObject, true
}

func (change Change) valid() bool {
	if validateCounts(change.additions, change.deletions, change.hunks) != nil {
		return false
	}
	switch change.kind {
	case ChangeAdded, ChangeDeleted, ChangeModified, ChangeRenamed, ChangeCopied:
		return change.validPathChange()
	case ChangeBinary, ChangeRenameOnly, ChangeModeOnly:
		return change.validMetadataChange()
	case ChangeSubmodule:
		return change.validSubmoduleChange()
	case ChangeObjectTypeChanged:
		return change.validObjectTypeChange()
	default:
		return false
	}
}

func (change Change) validPathChange() bool {
	if change.classification != "" && change.classification != ClassificationUnclassified {
		return false
	}
	return change.oldCommit == nil && change.newCommit == nil &&
		change.oldObject == nil && change.newObject == nil
}

func (change Change) validMetadataChange() bool {
	if change.kind == ChangeModeOnly &&
		(change.mode == nil || change.mode.old == change.mode.new ||
			change.mode.old == FileMode("000000") ||
			change.mode.new == FileMode("000000")) {
		return false
	}
	return change.classification == "" &&
		change.additions == 0 && change.deletions == 0 && change.hunks == 0 &&
		change.oldCommit == nil && change.newCommit == nil &&
		change.oldObject == nil && change.newObject == nil
}

func (change Change) validSubmoduleChange() bool {
	return change.classification == "" && change.mode == nil &&
		change.additions == 0 && change.deletions == 0 && change.hunks == 0 &&
		change.oldObject == nil && change.newObject == nil &&
		(change.oldCommit != nil || change.newCommit != nil) &&
		(change.oldCommit == nil || change.newCommit == nil ||
			*change.oldCommit != *change.newCommit)
}

func (change Change) validObjectTypeChange() bool {
	return change.classification == "" && change.mode == nil &&
		change.oldCommit == nil && change.newCommit == nil &&
		change.oldObject != nil && change.newObject != nil &&
		change.oldObject.kind != change.newObject.kind
}

func isPathKind(kind ChangeKind) bool {
	return kind == ChangeAdded || kind == ChangeDeleted || kind == ChangeModified ||
		kind == ChangeRenamed || kind == ChangeCopied
}

func validateCounts(additions, deletions, hunks uint32) error {
	if additions > MaxAdditions {
		return fmt.Errorf("%w: additions %d exceed %d", ErrInvalidChange, additions, MaxAdditions)
	}
	if deletions > MaxDeletions {
		return fmt.Errorf("%w: deletions %d exceed %d", ErrInvalidChange, deletions, MaxDeletions)
	}
	if hunks > MaxHunks {
		return fmt.Errorf("%w: hunks %d exceed %d", ErrInvalidChange, hunks, MaxHunks)
	}
	return nil
}

func validMode(mode string) bool {
	return mode == "000000" || mode == "100644" || mode == "100755" ||
		mode == "120000" || mode == "160000"
}

func validObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	nonzero := false
	for _, digit := range oid {
		if digit < '0' || digit > '9' && digit < 'a' || digit > 'f' {
			return false
		}
		nonzero = nonzero || digit != '0'
	}
	return nonzero
}

func cloneValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}
