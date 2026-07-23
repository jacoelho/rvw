package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxCommentKinds bounds the configured comment-kind vocabulary.
	MaxCommentKinds = 32
	// MaxCommentKindBytes bounds one stored UTF-8 comment-kind label.
	MaxCommentKindBytes = 256
	// MaxEncodedCommentKindPrefixBytes is the largest escaped `[kind] ` prefix.
	MaxEncodedCommentKindPrefixBytes       = 1 + 4*MaxCommentKindBytes + 2
	commentKindMetadataBytes         int64 = 16
)

// ErrInvalidCommentKinds identifies invalid configured comment-kind state.
var ErrInvalidCommentKinds = errors.New("invalid comment kinds")

// CommentKind is one canonical member of a review's configured kind set.
// Values are obtained only from CommentKinds.
type CommentKind struct {
	label string
}

// String returns the stored comment-kind label.
func (kind CommentKind) String() string { return kind.label }

// CommentKinds is an immutable, lexically ordered comment-kind vocabulary.
type CommentKinds struct {
	values        []CommentKind
	retainedBytes int64
}

// NewCommentKinds constructs and validates a comment-kind vocabulary.
func NewCommentKinds(labels ...string) (CommentKinds, error) {
	if len(labels) == 0 || len(labels) > MaxCommentKinds {
		return CommentKinds{}, fmt.Errorf(
			"%w: expected 1..%d labels",
			ErrInvalidCommentKinds,
			MaxCommentKinds,
		)
	}
	normalized := make([]string, len(labels))
	for index, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || len(label) > MaxCommentKindBytes || !utf8.ValidString(label) {
			return CommentKinds{}, fmt.Errorf(
				"%w: labels must be nonempty UTF-8 of at most %d bytes",
				ErrInvalidCommentKinds,
				MaxCommentKindBytes,
			)
		}
		for _, character := range label {
			if unicode.IsControl(character) {
				return CommentKinds{}, fmt.Errorf(
					"%w: label contains a control character",
					ErrInvalidCommentKinds,
				)
			}
		}
		normalized[index] = strings.Clone(label)
	}
	slices.Sort(normalized)
	values := make([]CommentKind, len(normalized))
	retained := int64(cap(values)) * commentKindMetadataBytes
	for index, label := range normalized {
		if index != 0 && label == normalized[index-1] {
			return CommentKinds{}, fmt.Errorf("%w: duplicate label %q", ErrInvalidCommentKinds, label)
		}
		values[index] = CommentKind{label: label}
		retained += int64(len(label))
	}
	return CommentKinds{values: slices.Clip(values), retainedBytes: retained}, nil
}

// Len returns the configured kind count.
func (kinds CommentKinds) Len() int { return len(kinds.values) }

// Ready reports whether the vocabulary was constructed and validated.
func (kinds CommentKinds) Ready() bool { return kinds.valid() }

// At returns the kind at one lexical index.
func (kinds CommentKinds) At(index int) (CommentKind, bool) {
	if index < 0 || index >= len(kinds.values) {
		return CommentKind{}, false
	}
	return kinds.values[index], true
}

// Lookup returns the canonical kind with the stored label.
func (kinds CommentKinds) Lookup(label string) (CommentKind, bool) {
	index, found := slices.BinarySearchFunc(kinds.values, label, func(kind CommentKind, label string) int {
		return strings.Compare(kind.label, label)
	})
	if !found {
		return CommentKind{}, false
	}
	return kinds.values[index], true
}

func (kinds CommentKinds) contains(kind CommentKind) bool {
	canonical, ok := kinds.Lookup(kind.label)
	return ok && canonical == kind
}

func (kinds CommentKinds) first() CommentKind {
	if len(kinds.values) == 0 {
		return CommentKind{}
	}
	return kinds.values[0]
}

func (kinds CommentKinds) valid() bool {
	return len(kinds.values) >= 1 && len(kinds.values) <= MaxCommentKinds &&
		kinds.retainedBytes >= int64(cap(kinds.values))*commentKindMetadataBytes
}

func (kinds CommentKinds) retained() int64 {
	if !kinds.valid() {
		return 0
	}
	return kinds.retainedBytes
}

// CommentPolicy identifies one independent stable comment policy.
type CommentPolicy uint8

// CommentTypePrefixForGitHub enables kind prefixes in RVW-owned GitHub text.
const CommentTypePrefixForGitHub CommentPolicy = 1

// CommentPolicies is an initialized opaque set of stable comment policies.
type CommentPolicies struct {
	bits        uint8
	initialized bool
}

// NewCommentPolicies constructs and validates stable comment policies.
func NewCommentPolicies(values ...CommentPolicy) (CommentPolicies, error) {
	policies := CommentPolicies{initialized: true}
	for _, value := range values {
		if value != CommentTypePrefixForGitHub {
			return CommentPolicies{}, fmt.Errorf("%w: unknown policy %d", ErrInvalidCommentKinds, value)
		}
		policies.bits |= 1 << (value - 1)
	}
	return policies, nil
}

// Has reports policy membership.
func (policies CommentPolicies) Has(value CommentPolicy) bool {
	return policies.valid() && value == CommentTypePrefixForGitHub &&
		policies.bits&(1<<(value-1)) != 0
}

// Ready reports whether the policy set was explicitly constructed.
func (policies CommentPolicies) Ready() bool { return policies.valid() }

func (policies CommentPolicies) valid() bool {
	const validBits = 1<<CommentTypePrefixForGitHub - 1
	return policies.initialized && policies.bits&^validBits == 0
}
