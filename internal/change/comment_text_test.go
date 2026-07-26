package change

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
)

var (
	commentTextNonSpaceSink bool
	errCommentTextSink      error
)

func TestScanCommentTextRejectsEveryControlExceptLineFeed(t *testing.T) {
	for value := rune(0); value <= '\u009f'; value++ {
		if !unicode.IsControl(value) {
			continue
		}
		t.Run(fmt.Sprintf("U+%04X", value), func(t *testing.T) {
			nonSpace, err := scanCommentText(nil, string(value))
			if value == '\n' {
				if err != nil || nonSpace {
					t.Fatalf("line feed = non-space %t, error %v", nonSpace, err)
				}
				return
			}
			if err != errInvalidCommentText || nonSpace { //nolint:errorlint // Scanner identity is the contract.
				t.Fatalf("control = non-space %t, error %v", nonSpace, err)
			}
		})
	}
}

func TestScanCommentTextRejectsExactBidirectionalControls(t *testing.T) {
	rejected := []rune{
		'\u061c', '\u200e', '\u200f',
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069',
	}
	for _, value := range rejected {
		t.Run(fmt.Sprintf("U+%04X", value), func(t *testing.T) {
			if _, err := scanCommentText(nil, string(value)); err != errInvalidCommentText { //nolint:errorlint // Scanner identity is the contract.
				t.Fatalf("error = %v", err)
			}
		})
	}

	for _, value := range []rune{'\u061b', '\u061d', '\u200d', '\u2010', '\u2029', '\u202f', '\u2065', '\u206a'} {
		t.Run(fmt.Sprintf("allowed-U+%04X", value), func(t *testing.T) {
			if _, err := scanCommentText(nil, string(value)); err != nil {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestScanCommentTextRejectsInvalidUTF8AndReplacementRune(t *testing.T) {
	for _, value := range []string{string([]byte{0xff}), "\ufffd"} {
		if _, err := scanCommentText(nil, value); err != errInvalidCommentText { //nolint:errorlint // Scanner identity is the contract.
			t.Fatalf("value %q error = %v", value, err)
		}
	}
}

func TestScanCommentTextReportsNonWhitespaceWithoutRejectingSafeFormatText(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		nonSpace bool
	}{
		{name: "empty", value: ""},
		{name: "spaces and line feed", value: " \n ", nonSpace: false},
		{name: "ordinary", value: "café\n界", nonSpace: true},
		{name: "non-bidi format", value: "\u200d", nonSpace: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nonSpace, err := scanCommentText(nil, test.value)
			if err != nil || nonSpace != test.nonSpace {
				t.Fatalf("non-space %t, error %v", nonSpace, err)
			}
		})
	}
}

func TestScanCommentTextCancellationBoundariesAndPrecedence(t *testing.T) {
	cause := errors.New("cancelled")
	tests := []struct {
		name      string
		value     string
		cancelAt  int
		wantCalls int
		want      error
	}{
		{name: "initial", value: "a", cancelAt: 1, wantCalls: 1, want: cause},
		{name: "two-kib boundary", value: strings.Repeat("a", 2_048) + "\x00", cancelAt: 2, wantCalls: 2, want: cause},
		{name: "final", value: "a", cancelAt: 2, wantCalls: 2, want: cause},
		{name: "complete", value: strings.Repeat("a", 4_097), wantCalls: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			cancelled := func() error {
				calls++
				if calls == test.cancelAt {
					return cause
				}
				return nil
			}
			_, err := scanCommentText(cancelled, test.value)
			if !errors.Is(err, test.want) || calls != test.wantCalls {
				t.Fatalf("error %v, calls %d", err, calls)
			}
		})
	}

	calls := 0
	_, err := scanCommentText(func() error {
		calls++
		return cause
	}, strings.Repeat("a", int(MaxCommentBodyBytes)+1))
	if err != errInvalidCommentText || calls != 0 { //nolint:errorlint // Scanner identity is the contract.
		t.Fatalf("oversize error %v, cancellation calls %d", err, calls)
	}
	calls = 0
	_, err = commentBodyCancellable(func() error {
		calls++
		return cause
	}, strings.Repeat("a", int(MaxCommentBodyBytes)+1))
	if !errors.Is(err, ErrInvalidComment) || calls != 0 {
		t.Fatalf("oversize saved body error %v, cancellation calls %d", err, calls)
	}

	calls = 0
	_, err = commentBodyCancellable(func() error {
		calls++
		return cause
	}, "")
	if !errors.Is(err, ErrInvalidComment) || calls != 0 {
		t.Fatalf("empty saved body error %v, cancellation calls %d", err, calls)
	}
}

func TestScanCommentTextMaximumBodyAllocatesNothing(t *testing.T) {
	value := strings.Repeat("a", int(MaxCommentBodyBytes))
	allocations := testing.AllocsPerRun(1_000, func() {
		commentTextNonSpaceSink, errCommentTextSink = scanCommentText(nil, value)
	})
	if allocations != 0 {
		t.Fatalf("allocations = %f, want 0", allocations)
	}
	if !commentTextNonSpaceSink || errCommentTextSink != nil {
		t.Fatalf("non-space %t, error %v", commentTextNonSpaceSink, errCommentTextSink)
	}
	allocations = testing.AllocsPerRun(1_000, func() {
		errCommentTextSink = ValidateCommentTextFragment(value)
	})
	if allocations != 0 || errCommentTextSink != nil {
		t.Fatalf("fragment allocations = %f, error %v", allocations, errCommentTextSink)
	}
}

func TestCommentTextOwnersPreserveBodyPoliciesAndErrors(t *testing.T) {
	for _, value := range []string{"\x1b", "\u0085", "\u202e", "\ufffd", string([]byte{0xff})} {
		if _, err := NewCommentBody(value); !errors.Is(err, ErrInvalidComment) {
			t.Fatalf("NewCommentBody(%q) error = %v", value, err)
		}
		if err := ValidateCommentTextFragment(value); !errors.Is(err, ErrInvalidReviewTransition) {
			t.Fatalf("ValidateCommentTextFragment(%q) error = %v", value, err)
		}
	}
	if _, err := NewCommentBody(" \n"); !errors.Is(err, ErrInvalidComment) {
		t.Fatalf("whitespace body error = %v", err)
	}
	for _, value := range []string{"", " \n", "safe\nbody"} {
		if err := ValidateCommentTextFragment(value); err != nil {
			t.Fatalf("fragment %q error = %v", value, err)
		}
	}

	current := testReview(t, testSnapshot(t, 1, "one.go"))
	current, err := current.BeginDraft(ReviewCommentTarget())
	if err != nil {
		t.Fatal(err)
	}
	current, err = current.UpdateDraft(" \n")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := current.Draft()
	next, err := current.UpdateDraft("unsafe\u202e")
	if !errors.Is(err, ErrInvalidReviewTransition) {
		t.Fatalf("UpdateDraft error = %v", err)
	}
	after, _ := next.Draft()
	if before.Body() != after.Body() {
		t.Fatalf("rejected update changed draft from %q to %q", before.Body(), after.Body())
	}
}

func TestPendingTextConstructorsRejectUnsafeSummaryAndInlineBodies(t *testing.T) {
	pending, _ := pendingTestSnapshot(t).PendingReview()
	comment := pending.Comments(0, 1)[0]
	for _, construct := range []struct {
		name string
		run  func(string) error
	}{
		{
			name: "new comment",
			run: func(body string) error {
				_, err := NewPendingComment(PendingCommentDescriptor{
					NodeID: comment.nodeID, DatabaseID: comment.databaseID, Body: body,
					Target: comment.target, Capabilities: comment.capabilities,
				})
				return err
			},
		},
		{
			name: "take comment",
			run: func(body string) error {
				_, err := TakePendingComment(PendingCommentDescriptor{
					NodeID: comment.nodeID, DatabaseID: comment.databaseID, Body: body,
					Target: comment.target, Capabilities: comment.capabilities,
				})
				return err
			},
		},
		{
			name: "new review",
			run: func(body string) error {
				_, err := NewPendingReview(PendingReviewDescriptor{
					NodeID: pending.nodeID, DatabaseID: pending.databaseID, Commit: pending.commit,
					Body: body, Comments: pending.comments, ViewerUpdate: pending.viewerUpdate,
				})
				return err
			},
		},
		{
			name: "take review",
			run: func(body string) error {
				_, err := TakePendingReview(PendingReviewDescriptor{
					NodeID: pending.nodeID, DatabaseID: pending.databaseID, Commit: pending.commit,
					Body: body, Comments: pending.comments, ViewerUpdate: pending.viewerUpdate,
				})
				return err
			},
		},
	} {
		t.Run(construct.name, func(t *testing.T) {
			if err := construct.run("unsafe\u202e"); !errors.Is(err, ErrInvalidPendingReview) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSnapshotPendingInstallationAcceptsValidAndRejectsZeroOrCancellation(t *testing.T) {
	pending, _ := pendingTestSnapshot(t).PendingReview()
	base := testSnapshot(t, 2, "one.go")
	installed, err := base.WithPendingReview(pending)
	if err != nil || installed.PendingReviewSummary().Status != PendingReviewReady {
		t.Fatalf("valid install status %s, error %v", installed.PendingReviewSummary().Status, err)
	}
	if _, err := base.TakePendingReview(PendingReview{}); !errors.Is(err, ErrInvalidPendingReview) {
		t.Fatalf("zero install error = %v", err)
	}
	cause := errors.New("cancelled")
	if _, err := base.takePendingReviewCancellable(func() error { return cause }, pending); !errors.Is(err, cause) {
		t.Fatalf("cancelled install error = %v", err)
	}
	wrapped := fmt.Errorf("cancelled: %w", errInvalidCommentText)
	calls := 0
	_, err = base.takePendingReviewCancellable(func() error {
		calls++
		if calls == 2 {
			return wrapped
		}
		return nil
	}, pending)
	if err != wrapped { //nolint:errorlint // Exact cancellation identity is the behavior under test.
		t.Fatalf("wrapped cancellation changed to %v", err)
	}
}
