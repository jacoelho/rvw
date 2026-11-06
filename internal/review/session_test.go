package review_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// TestEditingAnAgentCommentBodyMakesItHuman covers authorship on edit.
func TestEditingAnAgentCommentBodyMakesItHuman(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), parseSeed(t, seedOf(
		`{"target": "line", "path": "a.go", "side": "new", "line": 2, "body": "first\r\nsecond"}`,
		`{"target": "file", "path": "b.go", "body": "keep"}`,
	)))
	byBody := commentsByBody(s.Draft())
	edited, kept := byBody["first\nsecond"], byBody["keep"]
	if edited.Author != review.Agent || kept.Author != review.Agent {
		t.Fatalf("seeded comments = %+v; want author agent", s.Draft().Comments)
	}

	// Saved unchanged, in the CRLF form an editor may send back.
	c, err := s.EditComment(edited.ID, versionOf(t, s, edited.ID), "first\r\nsecond")
	if err != nil {
		t.Fatal(err)
	}
	if c.Author != review.Agent {
		t.Fatalf("author after saving unchanged = %s; want agent", c.Author)
	}

	c, err = s.EditComment(edited.ID, versionOf(t, s, edited.ID), "first\nsecond, edited")
	if err != nil {
		t.Fatal(err)
	}
	if c.Author != review.Human {
		t.Fatalf("author after editing = %s; want human", c.Author)
	}
	// Reverting the text does not restore agent authorship: the human saved these words.
	c, err = s.EditComment(edited.ID, versionOf(t, s, edited.ID), "first\nsecond")
	if err != nil {
		t.Fatal(err)
	}
	if c.Author != review.Human {
		t.Fatalf("author after reverting the edit = %s; want human", c.Author)
	}

	got := commentsByBody(s.Draft())
	if got["first\nsecond"].Author != review.Human || got["keep"].Author != review.Agent {
		t.Fatalf("comments = %+v; want the edited comment by human and the other by agent", s.Draft().Comments)
	}
	if len(got) != 2 {
		t.Fatalf("comments = %+v; editing must not add a comment", s.Draft().Comments)
	}
}

func TestNewCommentsAreHuman(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	c, err := s.CreateComment(t.Context(), "c1", on(t, s, line(pathA, review.New, 2)), "why?")
	if err != nil {
		t.Fatal(err)
	}
	if c.Author != review.Human {
		t.Fatalf("author = %s; want human", c.Author)
	}
}

// TestDeleteComment checks that the human may delete any comment, whatever its author.
func TestDeleteComment(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed(review.SeedComment{Target: reviewTarget, Body: "agent says"}))
	if _, err := s.CreateComment(t.Context(), "mine", on(t, s, reviewTarget), "human says"); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Draft().Comments {
		if err := s.DeleteComment(c.ID, c.Version); err != nil {
			t.Fatalf("delete %s: %v", c.ID, err)
		}
	}
	if got := s.Draft().Comments; len(got) != 0 {
		t.Fatalf("comments after deleting all = %+v", got)
	}
	if err := s.DeleteComment("mine", 1); err != nil {
		t.Fatalf("deleting a deleted comment again: %v", err)
	}
}

// Creating takes a free id and editing needs an existing one, so an edit racing a delete cannot
// bring the comment back, and a create cannot overwrite another comment.
func TestCreateAndEditNeedTheirPrecondition(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, line(pathA, review.New, 2)), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "overwrite"); !errors.Is(err, review.ErrCommentExists) {
		t.Errorf("CreateComment with a taken id: err = %v; want ErrCommentExists", err)
	}
	if got := commentByID(t, s.Draft(), "c1"); got.Body != "first" || got.Target != line(pathA, review.New, 2) {
		t.Errorf("comment after a refused create = %+v; want it unchanged", got)
	}
	deleted := versionOf(t, s, "c1")
	if err := s.DeleteComment("c1", deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditComment("c1", deleted, "edit after delete"); !errors.Is(err, review.ErrNoComment) {
		t.Errorf("EditComment of a deleted comment: err = %v; want ErrNoComment", err)
	}
	if _, err := s.CreateComment(t.Context(), "c2", on(t, s, reviewTarget), "fresh"); err != nil {
		t.Fatalf("CreateComment with a fresh id: %v", err)
	}
	want := s.Draft()
	if len(want.Comments) != 1 || want.Comments[0].ID != "c2" {
		t.Fatalf("comments = %+v; want only c2", want.Comments)
	}
	s.End()
	assertDraftRestores(t, fx, st, want)
}

// TestRepeatedCreateReturnsTheStoredComment: a create repeated with the same target and body, as a
// UI resends one whose reply was lost, returns the stored comment and writes nothing: no second
// comment, no new version, no draft write, no sequence number spent. The body is compared after
// normalisation.
func TestRepeatedCreateReturnsTheStoredComment(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	first, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "a\r\nb")
	if err != nil {
		t.Fatal(err)
	}
	draft := readFile(t, st.Path())
	again, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "a\nb")
	if err != nil || again != first {
		t.Fatalf("repeated create = %+v, %v; want %+v", again, err, first)
	}
	if n := len(s.Draft().Comments); n != 1 {
		t.Errorf("%d comments after a repeated create; want 1", n)
	}
	if got := readFile(t, st.Path()); got != draft {
		t.Errorf("the repeated create rewrote the draft:\n%s\nwant\n%s", got, draft)
	}
	next, err := s.CreateComment(t.Context(), "c2", review.OnReview(), "next")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := next.SortKey()[3], first.SortKey()[3]+1; got != want {
		t.Errorf("the comment after a repeated create has sequence %d; want %d", got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCreateWithATakenIDAndAnotherTarget: a create is a repeat only with the same resolved target
// and body; any other create of a taken id fails with ErrCommentExists and changes nothing.
func TestCreateWithATakenIDAndAnotherTarget(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	stored := map[string]review.Target{
		"line":   line(pathA, review.New, 2),
		"file":   file(pathA),
		"review": reviewTarget,
	}
	for id, target := range stored {
		if _, err := s.CreateComment(t.Context(), id, on(t, s, target), "body"); err != nil {
			t.Fatal(err)
		}
	}
	before := s.Draft()
	for name, c := range map[string]struct {
		id     string
		target review.Target
		body   string
	}{
		"the same line on the other side": {"line", line(pathA, review.Old, 2), "body"},
		"a range from the same line":      {"line", span(pathA, review.New, 2, 3), "body"},
		"a line of the commented file":    {"file", line(pathA, review.New, 2), "body"},
		"a file instead of the review":    {"review", file(pathA), "body"},
		"another body":                    {"line", line(pathA, review.New, 2), "other body"},
	} {
		if _, err := s.CreateComment(t.Context(), c.id, on(t, s, c.target), c.body); !errors.Is(err, review.ErrCommentExists) {
			t.Errorf("%s: err = %v; want ErrCommentExists", name, err)
		}
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Errorf("view after refused creates = %+v; want %+v", after, before)
	}
}

// TestConcurrentCreatesOfOneID: identical creates racing all get the one comment; creates with
// different bodies racing for one id leave exactly one winner.
func TestConcurrentCreatesOfOneID(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	const n = 8
	got := make([]review.Comment, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for k := range n {
		wg.Go(func() { got[k], errs[k] = s.CreateComment(t.Context(), "same", review.OnReview(), "same body") })
	}
	wg.Wait()
	for k := range n {
		if errs[k] != nil || got[k] != got[0] {
			t.Errorf("identical create %d = %+v, %v; want %+v", k, got[k], errs[k], got[0])
		}
	}
	for k := range n {
		wg.Go(func() {
			_, errs[k] = s.CreateComment(t.Context(), "raced", review.OnReview(), fmt.Sprintf("body %d", k))
		})
	}
	wg.Wait()
	winners := 0
	for k, err := range errs {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, review.ErrCommentExists):
			t.Errorf("create %d: err = %v; want nil or ErrCommentExists", k, err)
		}
	}
	if winners != 1 {
		t.Errorf("%d creates of one id with different bodies succeeded; want 1", winners)
	}
	if n := len(s.Draft().Comments); n != 2 {
		t.Errorf("%d comments; want 2", n)
	}
}

// TestWhitespaceOnlyBodiesAreEmpty pins which characters count as whitespace in REQ-CMT-001: Unicode
// White_Space, as unicode.IsSpace has it (decisions.md, "Empty comment bodies"). Each is rejected
// alone and in a mixed run, by create and by edit.
func TestWhitespaceOnlyBodiesAreEmpty(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	kept, err := s.CreateComment(t.Context(), "kept", review.OnReview(), "kept")
	if err != nil {
		t.Fatal(err)
	}
	space := []rune{'\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000}
	for r := rune(0x2000); r <= 0x200a; r++ {
		space = append(space, r)
	}
	for _, r := range space {
		for _, body := range []string{string(r), " " + string(r) + "\u00a0\n"} {
			_, err := s.CreateComment(t.Context(), "c", review.OnReview(), body)
			if _, ok := errors.AsType[*review.InvalidError](err); !ok {
				t.Errorf("create with body %q: err = %v; want an *InvalidError", body, err)
			}
			_, err = s.EditComment("kept", kept.Version, body)
			if _, ok := errors.AsType[*review.InvalidError](err); !ok {
				t.Errorf("edit with body %q: err = %v; want an *InvalidError", body, err)
			}
		}
	}
	if n := len(s.Draft().Comments); n != 1 {
		t.Errorf("%d comments after whitespace-only creates; want 1", n)
	}
}

// TestByteOrderMarkIsNotWhitespace pins the decision in decisions.md ("Empty comment bodies") that
// U+FEFF is not whitespace, so a body of it alone is accepted.
func TestByteOrderMarkIsNotWhitespace(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c", review.OnReview(), "\ufeff"); err != nil {
		t.Errorf("create with body U+FEFF: %v", err)
	}
}

// TestSessionNormalisesLineEndings checks that every entry point stores LF line endings.
func TestSessionNormalisesLineEndings(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	c, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "one\r\ntwo\rthree")
	if err != nil {
		t.Fatal(err)
	}
	if c.Body != "one\ntwo\nthree" {
		t.Fatalf("body = %q; want LF line endings", c.Body)
	}
	if err := s.SetSummary("sum\r\nmary\r"); err != nil {
		t.Fatal(err)
	}
	if got := s.Draft().Summary; got != "sum\nmary\n" {
		t.Fatalf("summary = %q; want LF line endings", got)
	}
}

// TestPutCommentRejectsInvalidComments covers comment validation through the human's entry point; a
// rejected comment leaves the draft unchanged.
func TestPutCommentRejectsInvalidComments(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "keep", on(t, s, line(pathA, review.New, 1)), "existing"); err != nil {
		t.Fatal(err)
	}
	before := s.Draft()

	tests := []struct {
		target   review.Target
		name, id string
		body     string
		edit     bool
	}{
		{name: "whitespace-only body", id: "c", target: reviewTarget, body: " \r\n\t "},
		{name: "body over 64 KiB", id: "c", target: reviewTarget, body: strings.Repeat("x", review.MaxBodyBytes+1)},
		{name: "body not UTF-8", id: "c", target: reviewTarget, body: "\xff"},
		{name: "line beyond the file", id: "c", target: line(pathA, review.New, 11), body: "x"},
		{name: "range ending beyond the file", id: "c", target: span(pathA, review.New, 9, 11), body: "x"},
		{name: "old side of an added file", id: "c", target: line(pathAdded, review.Old, 1), body: "x"},
		{name: "non-UTF-8 path, file", id: "c", target: file(pathNonUTF8), body: "x"},
		{name: "non-UTF-8 path, line", id: "c", target: line(pathNonUTF8, review.New, 1), body: "x"},
		{name: "control character path", id: "c", target: file(pathTab), body: "x"},
		{name: "binary file line", id: "c", target: line(pathBinary, review.New, 1), body: "x"},
		{name: "line beyond a renamed file with no hunks", id: "c", target: line(pathRenNew, review.New, 11), body: "x"},
		{name: "edit with whitespace-only body", id: "keep", body: " ", edit: true},
		{name: "edit with an id over 64 characters", id: strings.Repeat("i", 65), body: "x", edit: true},
		{name: "empty id", id: "", target: reviewTarget, body: "x"},
		{name: "id with a slash", id: "a/b", target: reviewTarget, body: "x"},
		{name: "id over 64 characters", id: strings.Repeat("i", 65), target: reviewTarget, body: "x"},
	}
	for _, tt := range tests {
		var err error
		if tt.edit {
			_, err = s.EditComment(tt.id, versionOf(t, s, tt.id), tt.body)
		} else {
			_, err = s.CreateComment(t.Context(), tt.id, on(t, s, tt.target), tt.body)
		}
		if _, ok := errors.AsType[*review.InvalidError](err); !ok {
			t.Errorf("%s: err = %v; want an *InvalidError", tt.name, err)
		}
	}
	// Placements are unchecked data: CreateComment alone applies the file index, line, range and
	// side rules, each as one *InvalidError.
	a, deleted := fileIndexOf(t, s, pathA), fileIndexOf(t, s, pathDeleted)
	for name, p := range map[string]review.Placement{
		"negative file index":              review.OnFile(-1),
		"file index past the files":        review.OnFile(s.Snapshot().FileCount()),
		"line zero":                        review.OnLine(a, review.New, 0),
		"range start equals end":           review.OnRange(a, review.New, 3, 3),
		"range start after end":            review.OnRange(a, review.New, 4, 2),
		"old side of an added file":        review.OnLine(fileIndexOf(t, s, pathAdded), review.Old, 1),
		"unknown side of a deleted file":   review.OnLine(deleted, "middle", 1),
		"unknown side of a file with both": review.OnLine(a, "middle", 1),
		"negative file index on a line":    review.OnLine(-1, review.New, 1),
		"range past the files":             review.OnRange(s.Snapshot().FileCount(), review.New, 1, 2),
	} {
		_, err := s.CreateComment(t.Context(), "c", p, "x")
		if invalid, ok := errors.AsType[*review.InvalidError](err); !ok || invalid.Error() != err.Error() {
			t.Errorf("%s: err = %v; want one *InvalidError", name, err)
		}
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Fatalf("draft changed after rejected comments:\n%+v\nwant\n%+v", after, before)
	}
	assertDraftRestores(t, fx, st, before)
}

// TestInvalidSideIsNamedBeforeTheFileSides: a side that is neither old nor new is reported as an
// invalid side whatever sides the file has, not as a side the file lacks.
func TestInvalidSideIsNamedBeforeTheFileSides(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	files := map[string]int{
		"deleted":  fileIndexOf(t, s, pathDeleted),
		"added":    fileIndexOf(t, s, pathAdded),
		"modified": fileIndexOf(t, s, pathA),
	}
	for name, i := range files {
		for _, side := range []review.Side{"x", "", "Old"} {
			for kind, p := range map[string]review.Placement{
				"line":  review.OnLine(i, side, 1),
				"range": review.OnRange(i, side, 1, 2),
			} {
				_, err := s.CreateComment(t.Context(), "c", p, "x")
				invalid, ok := errors.AsType[*review.InvalidError](err)
				if !ok {
					t.Errorf("%s %s, side %q: err = %v; want an *InvalidError", name, kind, side, err)
					continue
				}
				r := invalid.Reason
				if !strings.Contains(r, `"old"`) || !strings.Contains(r, `"new"`) || strings.Contains(r, "has no") {
					t.Errorf("%s %s, side %q: reason %q; want it to name old and new, not a missing side", name, kind, side, r)
				}
			}
		}
	}

	// A valid side the file lacks still names the missing side, and file comments take no side.
	for name, p := range map[string]review.Placement{
		"old side of an added file":  review.OnLine(files["added"], review.Old, 1),
		"new side of a deleted file": review.OnLine(files["deleted"], review.New, 1),
	} {
		_, err := s.CreateComment(t.Context(), "c", p, "x")
		if invalid, ok := errors.AsType[*review.InvalidError](err); !ok || !strings.Contains(invalid.Reason, "has no") {
			t.Errorf("%s: err = %v; want the missing-side *InvalidError", name, err)
		}
	}
	if _, err := s.CreateComment(t.Context(), "f", review.OnFile(files["deleted"]), "x"); err != nil {
		t.Errorf("file comment on a deleted file: %v", err)
	}
}

// fileIndexOf is the index of the changed file whose display path is path.
func fileIndexOf(tb testing.TB, s *review.Session, path string) int {
	tb.Helper()
	snap := s.Snapshot()
	for i := range snap.FileCount() {
		if snap.File(i).Path() == path {
			return i
		}
	}
	tb.Fatalf("no changed file %q", path)
	return -1
}

// A target's shape is checked when it is built, so a malformed target never reaches a session.
// TestPutCommentAcceptsShowableLines covers line targets: in a file that is not large, any line the
// file view can show is a target, on either side, whether it is in a hunk, in the context around
// one, or in a renamed file with no hunks.
func TestPutCommentAcceptsShowableLines(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	for name, target := range map[string]review.Target{
		"hunk line":                    line(pathA, review.New, 2),
		"context far from the hunk":    line(pathA, review.New, 10),
		"range across hunk and gap":    span(pathA, review.Old, 1, 10),
		"renamed file, new side":       line(pathRenNew, review.New, 5),
		"renamed file, whole old side": span(pathRenOld, review.Old, 1, 10),
	} {
		id := strings.Map(func(r rune) rune {
			if r == ' ' || r == ',' {
				return '-'
			}
			return r
		}, name)
		if _, err := s.CreateComment(t.Context(), id, on(t, s, target), "x"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestPutCommentLargeFile covers a large file: only lines inside a hunk are targets, also when the
// file is too big for its lines to be counted.
func TestPutCommentLargeFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	// Over LargeBytes, so neither side's lines are counted.
	big := numbered("large file line", 60_000)
	write(t, dir, "big.txt", big)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "one")
	changed := slices.Clone(big)
	changed[29_999] = "changed"
	write(t, dir, "big.txt", changed)
	git(t, dir, "commit", "-q", "-am", "two")
	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	snap := resolve(t, repo, source.Last())
	s, _, err := review.Start(t.Context(), openStore(t, t.TempDir(), review.DraftKey(snap)), snap, review.Fresh{Seed: review.NewSeed()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(t.Context(), "in", on(t, s, line("big.txt", review.New, 30_000)), "x"); err != nil {
		t.Errorf("the changed line of a large file: %v", err)
	}
	for name, target := range map[string]review.Target{
		"outside the hunk": line("big.txt", review.New, 10),
		"across the hunk":  span("big.txt", review.New, 29_990, 30_000),
		"beyond the file":  line("big.txt", review.Old, 70_000),
	} {
		_, err := s.CreateComment(t.Context(), "out", on(t, s, target), "x")
		if _, ok := errors.AsType[*review.InvalidError](err); !ok {
			t.Errorf("%s: err = %v; want an *InvalidError", name, err)
		}
	}
}

func TestTargetConstructorsRejectMalformedTargets(t *testing.T) {
	t.Parallel()
	tests := map[string]func() (review.Target, error){
		"file without path":      func() (review.Target, error) { return review.FileTarget("") },
		"line without path":      func() (review.Target, error) { return review.LineTarget("", review.New, 1) },
		"line with unknown side": func() (review.Target, error) { return review.LineTarget(pathA, "both", 1) },
		"line zero":              func() (review.Target, error) { return review.LineTarget(pathA, review.New, 0) },
		"negative line":          func() (review.Target, error) { return review.LineTarget(pathA, review.Old, -1) },
		"range without path":     func() (review.Target, error) { return review.RangeTarget("", review.New, 1, 2) },
		"range without side":     func() (review.Target, error) { return review.RangeTarget(pathA, "", 1, 2) },
		"range from line zero":   func() (review.Target, error) { return review.RangeTarget(pathA, review.New, 0, 2) },
		"range start equals end": func() (review.Target, error) { return review.RangeTarget(pathA, review.New, 2, 2) },
		"range start after end":  func() (review.Target, error) { return review.RangeTarget(pathA, review.New, 3, 2) },
	}
	for name, build := range tests {
		target, err := build()
		if _, ok := errors.AsType[*review.InvalidError](err); !ok {
			t.Errorf("%s: target %+v, err = %v; want an *InvalidError", name, target, err)
		}
	}
	got := []review.Target{reviewTarget, file(pathA), line(pathA, review.Old, 3), span(pathA, review.New, 2, 5)}
	type parts struct {
		kind       review.TargetKind
		path       string
		side       review.Side
		start, end int
	}
	want := []parts{
		{review.TargetReview, "", "", 0, 0},
		{review.TargetFile, pathA, "", 0, 0},
		{review.TargetLine, pathA, review.Old, 3, 3},
		{review.TargetRange, pathA, review.New, 2, 5},
	}
	for i, target := range got {
		start, end := target.Lines()
		if p := (parts{target.Kind(), target.Path(), target.Side(), start, end}); p != want[i] {
			t.Errorf("target %d reads as %+v; want %+v", i, p, want[i])
		}
	}
}

// TestCommentCountLimit checks that the 4,097th comment is rejected and the existing 4,096 are
// unchanged.
func TestCommentCountLimit(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), reviewSeed(review.MaxComments-1, "seeded"))
	if _, err := s.CreateComment(t.Context(), "last", on(t, s, reviewTarget), "the 4096th"); err != nil {
		t.Fatalf("the %dth comment: %v", review.MaxComments, err)
	}
	before := s.Draft()

	_, err := s.CreateComment(t.Context(), "over", on(t, s, reviewTarget), "the 4097th")
	if _, ok := errors.AsType[*review.InvalidError](err); !ok {
		t.Fatalf("the %dth comment: err = %v; want an *InvalidError", review.MaxComments+1, err)
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Fatal("the comments changed after the rejected comment")
	}
	// At the limit, existing comments stay editable and a deletion makes room.
	if _, err := s.EditComment("last", versionOf(t, s, "last"), "edited"); err != nil {
		t.Fatalf("editing at the limit: %v", err)
	}
	if err := s.DeleteComment("last", versionOf(t, s, "last")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(t.Context(), "over", on(t, s, reviewTarget), "fits now"); err != nil {
		t.Fatalf("a comment after a deletion at the limit: %v", err)
	}
}

func TestTotalBodyBytesLimit(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	full := strings.Repeat("x", review.MaxBodyBytes)
	n := review.MaxTotalBytes / review.MaxBodyBytes
	s, _ := fx.start(t, t.TempDir(), reviewSeed(n-1, full))
	if _, err := s.CreateComment(t.Context(), "last", on(t, s, reviewTarget), strings.Repeat("y", review.MaxBodyBytes-1)); err != nil {
		t.Fatalf("a comment one byte under the total: %v", err)
	}
	before := s.Draft()

	// Growing an existing body past the total is rejected like a new comment.
	_, err := s.EditComment("last", versionOf(t, s, "last"), strings.Repeat("y", review.MaxBodyBytes)+"\n")
	if _, ok := errors.AsType[*review.InvalidError](err); !ok {
		t.Fatalf("growing a body past the total: err = %v; want an *InvalidError", err)
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Fatal("the comments changed after the rejected edit")
	}
	if _, err := s.EditComment("last", versionOf(t, s, "last"), full); err != nil {
		t.Fatalf("a body reaching the total exactly: %v", err)
	}
	before = s.Draft()
	_, err = s.CreateComment(t.Context(), "extra", on(t, s, reviewTarget), "x")
	if _, ok := errors.AsType[*review.InvalidError](err); !ok {
		t.Fatalf("a comment past the total: err = %v; want an *InvalidError", err)
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Fatal("the comments changed after the rejected comment")
	}
}

// TestCommentOrder covers the result's comment order.
func TestCommentOrder(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	put := []struct {
		target review.Target
		id     string
	}{
		{line(pathB, review.New, 4), "b-new-4"},
		{reviewTarget, "review-1"},
		{line(pathA, review.New, 2), "a-new-2-first"},
		{span(pathA, review.Old, 5, 6), "a-old-5-6"},
		{file(pathB), "b-file"},
		{line(pathA, review.New, 2), "a-new-2-second"},
		{reviewTarget, "review-2"},
		{line(pathA, review.Old, 1), "a-old-1"},
		{line(pathB, review.Old, 3), "b-old-3"},
		{file(pathA), "a-file"},
		{line(pathA, review.New, 1), "a-new-1"},
	}
	for _, p := range put {
		if _, err := s.CreateComment(t.Context(), p.id, on(t, s, p.target), p.id); err != nil {
			t.Fatalf("put %s: %v", p.id, err)
		}
	}
	keyBefore := commentByID(t, s.Draft(), "a-new-2-first").SortKey()
	// Editing a comment keeps its place in creation order.
	if _, err := s.EditComment("a-new-2-first", versionOf(t, s, "a-new-2-first"), "a-new-2-first, edited"); err != nil {
		t.Fatal(err)
	}
	if after := commentByID(t, s.Draft(), "a-new-2-first").SortKey(); after != keyBefore {
		t.Errorf("sort key after an edit = %v; want %v", after, keyBefore)
	}

	want := []string{
		"review-1", "review-2",
		"a-file", "a-old-1", "a-old-5-6", "a-new-1", "a-new-2-first", "a-new-2-second",
		"b-file", "b-old-3", "b-new-4",
	}
	var ids []string
	view := s.Draft()
	for k, c := range view.Comments {
		ids = append(ids, c.ID)
		assertFileIndex(t, fx, c)
		if k > 0 {
			prev, cur := view.Comments[k-1].SortKey(), c.SortKey()
			if slices.Compare(prev[:], cur[:]) >= 0 {
				t.Errorf("sort key of %s %v does not follow %v", c.ID, cur, prev)
			}
		}
	}
	if !slices.Equal(ids, want) {
		t.Errorf("Draft order = %q\nwant %q", ids, want)
	}
	r, err := s.Result(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range r.Comments {
		bodies = append(bodies, strings.TrimSuffix(c.Body, ", edited"))
	}
	if !slices.Equal(bodies, want) {
		t.Errorf("Result order = %q\nwant %q", bodies, want)
	}
}

// TestResultQuotesTargetLines checks that the result quotes each target's lines from its side.
func TestResultQuotesTargetLines(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	// add.go holds "short", 499 a's then é (straddling byte 500) then "tail", 500 b's and 501 c's.
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	for id, target := range map[string]review.Target{
		"review": reviewTarget,
		"file":   file(pathA),
		"old":    line(pathA, review.Old, 2),
		"new":    line(pathA, review.New, 2),
		"cut":    span(pathAdded, review.New, 1, 4),
		"thirty": span(pathLong, review.New, 1, 30),
		"twenty": span(pathLong, review.New, 11, 30),
	} {
		if _, err := s.CreateComment(t.Context(), id, on(t, s, target), id); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	if err := s.SetSummary("Summary."); err != nil {
		t.Fatal(err)
	}
	got, err := s.Result(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := review.Result{
		Source:   fx.last.Source(),
		BaseTree: strings.TrimSpace(git(t, fx.dir, "rev-parse", "HEAD~1^{tree}")),
		HeadTree: strings.TrimSpace(git(t, fx.dir, "rev-parse", "HEAD^{tree}")),
		Summary:  "Summary.",
		Comments: []review.ResultComment{
			{Author: review.Human, Body: "review", Target: reviewTarget},
			{Author: review.Human, Body: "file", Target: file(pathA)},
			{Author: review.Human, Body: "old", Target: line(pathA, review.Old, 2), Code: []string{"a 2"}},
			{Author: review.Human, Body: "new", Target: line(pathA, review.New, 2), Code: []string{"a 2 changed"}},
			{
				Author: review.Human, Body: "cut", Target: span(pathAdded, review.New, 1, 4),
				Code:         []string{"short", strings.Repeat("a", 499), strings.Repeat("b", 500), strings.Repeat("c", 500)},
				OmittedBytes: 6 + 1,
			},
			{
				Author: review.Human, Body: "thirty", Target: span(pathLong, review.New, 1, 30),
				Code: numbered("long", 20), OmittedLines: 10,
			},
			{
				Author: review.Human, Body: "twenty", Target: span(pathLong, review.New, 11, 30),
				Code: numbered("long", 30)[10:],
			},
		},
	}
	if got.Source.String() != want.Source.String() {
		t.Errorf("source = %s; want %s", got.Source, want.Source)
	}
	got.Source = want.Source
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Result =\n%+v\nwant\n%+v", got, want)
	}
}

// The Finish dialog shows each comment's quote: Quotes returns, for the requested line and range
// comments, exactly what the result quotes, and leaves out other targets and unknown IDs.
func TestQuotesMatchResult(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	for id, target := range map[string]review.Target{
		"review": reviewTarget,
		"file":   file(pathA),
		"old":    line(pathA, review.Old, 2),
		"thirty": span(pathLong, review.New, 1, 30),
	} {
		if _, err := s.CreateComment(t.Context(), id, on(t, s, target), id); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	got, err := s.Quotes(t.Context(), []string{"thirty", "review", "file", "missing", "old"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]review.Quote{
		"old":    {Code: []string{"a 2"}},
		"thirty": {Code: numbered("long", 20), OmittedLines: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Quotes =\n%+v\nwant\n%+v", got, want)
	}
	r, err := s.Result(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Comments {
		if q, ok := got[c.Body]; ok && !reflect.DeepEqual(q, c.Quote) {
			t.Errorf("comment %s: Quotes = %+v; the result quotes %+v", c.Body, q, c.Quote)
		}
	}
}

// TestFinish checks that finishing delivers the result, ends the session and deletes the draft.
func TestFinish(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "done"); err != nil {
		t.Fatal(err)
	}
	var delivered []review.Result
	d, err := s.Finish(t.Context(), func(r review.Result) error {
		if !fileExists(t, st.Path()) {
			t.Error("the draft was deleted before delivery")
		}
		delivered = append(delivered, r)
		return nil
	})
	if err != nil || d.DraftErr != nil {
		t.Fatalf("Finish: err = %v, DraftErr = %v; want both nil", err, d.DraftErr)
	}
	if len(delivered) != 1 || len(delivered[0].Comments) != 1 || !reflect.DeepEqual(d.Result, delivered[0]) {
		t.Fatalf("delivered %+v, Finish returned %+v; want the same one result with one comment", delivered, d.Result)
	}
	if fileExists(t, st.Path()) {
		t.Fatal("the draft survived a delivered result")
	}
	assertEnded(t, s, review.EndedByFinish)
}

// TestFinishDeliveryFailureKeepsDraft checks that a failed delivery ends the session and keeps the
// draft.
func TestFinishDeliveryFailureKeepsDraft(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "keep me"); err != nil {
		t.Fatal(err)
	}
	want := s.Draft()
	errBroken := errors.New("broken pipe")
	_, err := s.Finish(t.Context(), func(review.Result) error { return errBroken })
	if de, ok := errors.AsType[*review.DeliveryError](err); !ok || !errors.Is(de, errBroken) {
		t.Fatalf("Finish: err = %v; want a *DeliveryError wrapping the delivery error", err)
	}
	assertEnded(t, s, review.EndedByFinish)
	assertDraftRestores(t, fx, st, want)
}

// TestCancel checks that cancelling ends the session and deletes the draft.
func TestCancel(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(); err != nil {
		t.Fatal(err)
	}
	if fileExists(t, st.Path()) {
		t.Fatal("the draft survived cancel")
	}
	assertEnded(t, s, review.EndedByCancel)
}

// TestEndKeepsDraft checks that ending on interruption keeps the draft.
func TestEndKeepsDraft(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, reviewTarget), "x"); err != nil {
		t.Fatal(err)
	}
	want := s.Draft()
	if got := s.End(); got != review.EndedByEnd {
		t.Fatalf("End on an open session = %v; want EndedByEnd", got)
	}
	assertEnded(t, s, review.EndedByEnd)
	assertDraftRestores(t, fx, st, want)
}

// TestConcurrentMutations checks that concurrent mutations are all applied and persisted.
func TestConcurrentMutations(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	const workers, each = 8, 10
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				id := fmt.Sprintf("w%d-%d", w, i)
				if _, err := s.CreateComment(t.Context(), id, on(t, s, line(pathA, review.New, 1+i)), id); err != nil {
					t.Error(err)
				}
				if err := s.SetViewed([]int{w}, i%2 == 0); err != nil {
					t.Error(err)
				}
				if err := s.SetSummary(id); err != nil {
					t.Error(err)
				}
				_ = s.Draft()
			}
		})
	}
	wg.Wait()
	v := s.Draft()
	if len(v.Comments) != workers*each {
		t.Fatalf("%d comments; want %d", len(v.Comments), workers*each)
	}
	seqs := map[int64]bool{}
	for _, c := range v.Comments {
		seqs[c.Seq] = true
	}
	if len(seqs) != workers*each {
		t.Fatalf("comments share creation sequence numbers: %d distinct of %d", len(seqs), workers*each)
	}
	s.End()
	assertDraftRestores(t, fx, st, v)
}

// TestFinishRacingCancel checks that when finishing and cancelling race, exactly one wins.
func TestFinishRacingCancel(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	for range 20 {
		s, st := fx.start(t, t.TempDir(), review.NewSeed())
		var finishErr, cancelErr error
		var delivered int
		var wg sync.WaitGroup
		wg.Go(func() {
			_, finishErr = s.Finish(t.Context(), func(review.Result) error { delivered++; return nil })
		})
		wg.Go(func() { cancelErr = s.Cancel() })
		wg.Wait()

		switch {
		case finishErr == nil && errors.Is(cancelErr, review.ErrEnded):
			if delivered != 1 {
				t.Fatalf("finish won but delivered %d times", delivered)
			}
		case cancelErr == nil && errors.Is(finishErr, review.ErrEnded):
			if delivered != 0 {
				t.Fatal("cancel won but a result was delivered")
			}
		default:
			t.Fatalf("Finish = %v, Cancel = %v; want exactly one to succeed and the other ErrEnded", finishErr, cancelErr)
		}
		if fileExists(t, st.Path()) {
			t.Fatal("the draft survived")
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEndAnswersWhileDeliveryBlocks checks that delivery runs outside the session lock, so an
// interruption is answered while a write blocks, and that End then reports the finish.
func TestEndAnswersWhileDeliveryBlocks(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	// A saved draft, so the test can see a failed delivery keep it.
	if err := s.SetSummary("kept"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- second(s.Finish(t.Context(), func(review.Result) error {
			close(entered)
			<-release
			return errors.New("stdout closed")
		}))
	}()
	<-entered

	answered := make(chan review.Ending, 1)
	go func() { answered <- s.End() }()
	select {
	case got := <-answered:
		if got != review.EndedByFinish {
			t.Errorf("End during delivery = %v; want EndedByFinish", got)
		}
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("End blocked behind a blocked delivery")
	}
	if _, err := s.CreateComment(t.Context(), "late", on(t, s, reviewTarget), "x"); !errors.Is(err, review.ErrEnded) {
		t.Errorf("a mutation during delivery: err = %v; want ErrEnded", err)
	}
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("Finish = nil; want the delivery error")
	}
	if !fileExists(t, st.Path()) {
		t.Fatal("the draft was deleted although delivery failed")
	}
}

// assertEnded checks that s rejects every mutation and that End reports want.
func assertEnded(t *testing.T, s *review.Session, want review.Ending) {
	t.Helper()
	ctx := t.Context()
	checks := map[string]error{
		"CreateComment":      second(s.CreateComment(ctx, "after", on(t, s, reviewTarget), "x")),
		"CreateComment line": second(s.CreateComment(ctx, "after", on(t, s, line(pathA, review.New, 1)), "x")),
		"EditComment":        second(s.EditComment("c1", versionOf(t, s, "c1"), "x")),
		"DeleteComment":      s.DeleteComment("c1", 1),
		"SetViewed":          s.SetViewed([]int{0}, true),
		"SetSummary":         s.SetSummary("x"),
		"Cancel":             s.Cancel(),
		"Finish":             second(s.Finish(ctx, func(review.Result) error { return nil })),
	}
	for name, err := range checks {
		if !errors.Is(err, review.ErrEnded) {
			t.Errorf("%s after the session ended: err = %v; want ErrEnded", name, err)
		}
	}
	if got := s.End(); got != want {
		t.Errorf("End after the session ended = %v; want %v", got, want)
	}
}

// assertDraftRestores closes st, reopens the draft and checks that a new session restores want.
func assertDraftRestores(t *testing.T, fx *fixture, st *review.Store, want review.Draft) {
	t.Helper()
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2 := openStore(t, filepath.Dir(path), fx.key(fx.last))
	s, o, err := review.Start(t.Context(), st2, fx.last, review.Fresh{Seed: review.NewSeed(review.SeedComment{Target: reviewTarget, Body: "ignored seed"})})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if moved, ok, reason := o.MovedAside(); ok {
		t.Fatalf("restart moved the draft aside to %s: %v", moved, reason)
	}
	if !o.Resumed() {
		t.Fatal("restart did not resume the draft")
	}
	// Restoring renumbers sequence numbers; what persists is the order, and View lists comments in
	// result order, where the sequence number breaks ties. Versions belong to one session.
	if got := withoutSeqs(s.Draft()); !reflect.DeepEqual(got, withoutSeqs(want)) {
		t.Fatalf("restored draft =\n%+v\nwant\n%+v", got, want)
	}
}

// withoutSeqs clears what a restore does not keep: sequence numbers and versions.
func withoutSeqs(v review.Draft) review.Draft {
	v.Comments = slices.Clone(v.Comments)
	for i := range v.Comments {
		v.Comments[i].Seq, v.Comments[i].Version = 0, 0
	}
	return v
}

func commentByID(t *testing.T, v review.Draft, id string) review.Comment {
	t.Helper()
	for _, c := range v.Comments {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no comment %s in %+v", id, v.Comments)
	return review.Comment{}
}

// assertFileIndex checks that c names the changed file its target does: -1 for the review, else
// the file whose old path (old side), new path (new side) or display path (file target) matches.
func assertFileIndex(t *testing.T, fx *fixture, c review.Comment) {
	t.Helper()
	if c.Target.Kind() == review.TargetReview {
		if c.File != -1 {
			t.Errorf("review comment %s has file %d; want -1", c.ID, c.File)
		}
		return
	}
	if c.File < 0 || c.File >= fx.last.FileCount() {
		t.Errorf("comment %s on %q has file %d, out of range", c.ID, c.Target.Path(), c.File)
		return
	}
	f := fx.last.File(c.File)
	path := f.Path()
	switch c.Target.Side() {
	case review.Old:
		path = f.OldPath
	case review.New:
		path = f.NewPath
	}
	if path != c.Target.Path() {
		t.Errorf("comment %s on %q has file %d (%+v)", c.ID, c.Target.Path(), c.File, f)
	}
}

func commentsByBody(v review.Draft) map[string]review.Comment {
	m := map[string]review.Comment{}
	for _, c := range v.Comments {
		m[c.Body] = c
	}
	return m
}

func second[T any](_ T, err error) error { return err }

// Comments on one side of a file quote independently however their ranges relate: overlapping,
// nested, identical, single-line, over the quote limit, and ending on the last line, alongside
// comments on both sides of another file. Each keeps its own code and omission counts, and the
// result keeps its order.
func TestResultQuotesOverlappingRanges(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	long := numbered("long", 30)
	put := []struct {
		id     string
		target review.Target
	}{
		{"overlap", span(pathLong, review.New, 15, 25)},
		{"eof", span(pathLong, review.New, 28, 30)},
		{"nested", span(pathLong, review.New, 5, 8)},
		{"identical", span(pathLong, review.New, 5, 8)},
		{"wide", span(pathLong, review.New, 1, 30)},
		{"single", line(pathLong, review.New, 6)},
		{"new", line(pathA, review.New, 2)},
		{"old", line(pathA, review.Old, 2)},
	}
	for _, p := range put {
		if _, err := s.CreateComment(t.Context(), p.id, on(t, s, p.target), p.id); err != nil {
			t.Fatalf("put %s: %v", p.id, err)
		}
	}
	got, err := s.Result(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []review.ResultComment{
		{Author: review.Human, Body: "old", Target: line(pathA, review.Old, 2), Code: []string{"a 2"}},
		{Author: review.Human, Body: "new", Target: line(pathA, review.New, 2), Code: []string{"a 2 changed"}},
		{Author: review.Human, Body: "wide", Target: span(pathLong, review.New, 1, 30), Code: long[:20], OmittedLines: 10},
		{Author: review.Human, Body: "nested", Target: span(pathLong, review.New, 5, 8), Code: long[4:8]},
		{Author: review.Human, Body: "identical", Target: span(pathLong, review.New, 5, 8), Code: long[4:8]},
		{Author: review.Human, Body: "single", Target: line(pathLong, review.New, 6), Code: long[5:6]},
		{Author: review.Human, Body: "overlap", Target: span(pathLong, review.New, 15, 25), Code: long[14:25]},
		{Author: review.Human, Body: "eof", Target: span(pathLong, review.New, 28, 30), Code: long[27:30]},
	}
	if !reflect.DeepEqual(got.Comments, want) {
		t.Fatalf("comments =\n%+v\nwant\n%+v", got.Comments, want)
	}
}

// A quote that cannot be read fails the result naming the quoted path, and the session recovers
// once the content is back; a cancelled caller gets the context's error.
func TestResultQuoteFailure(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, _ := fx.start(t, t.TempDir(), review.NewSeed())
	if _, err := s.CreateComment(t.Context(), "c1", on(t, s, line(pathA, review.New, 2)), "quoted"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Result(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Result with a cancelled context = %v; want context.Canceled", err)
	}

	restore := fx.hideBlob(t, "HEAD:"+pathA)
	if _, err := s.Result(t.Context()); err == nil || !strings.Contains(err.Error(), pathA) {
		t.Errorf("Result with the quoted blob missing = %v; want an error naming %s", err, pathA)
	}
	restore()
	r, err := s.Result(t.Context())
	if err != nil {
		t.Fatalf("Result with the blob restored: %v", err)
	}
	if len(r.Comments) != 1 || !slices.Equal(r.Comments[0].Code, []string{"a 2 changed"}) {
		t.Errorf("result comments = %+v; want the quoted line", r.Comments)
	}
}

// TestEditNeedsTheLoadedVersion: an edit names the version its editor loaded. An edit whose body
// equals the stored one returns the stored comment unchanged at any version, as a replay after a
// lost reply needs; a differing edit at an outdated version is stale and leaves the draft
// unchanged; a comment deleted and created again under its id is a new comment.
func TestEditNeedsTheLoadedVersion(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	created, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "one")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := s.EditComment("c1", created.Version, "two")
	if err != nil {
		t.Fatalf("EditComment with the created version: %v", err)
	}
	for name, v := range map[string]int64{"current": edited.Version, "stale": created.Version} {
		same, err := s.EditComment("c1", v, "two")
		if err != nil || same != edited {
			t.Errorf("EditComment keeping the body at the %s version = %+v, %v; want %+v unchanged", name, same, err, edited)
		}
	}
	latest, err := s.EditComment("c1", edited.Version, "three")
	if err != nil {
		t.Fatalf("EditComment with the edited version: %v", err)
	}
	before := s.Draft()
	for name, v := range map[string]int64{"created": created.Version, "edited": edited.Version} {
		if _, err := s.EditComment("c1", v, "stale"); !errors.Is(err, review.ErrStale) {
			t.Errorf("EditComment replaying the %s version: err = %v; want ErrStale", name, err)
		}
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Errorf("view after stale edits = %+v; want %+v", after, before)
	}
	if err := s.DeleteComment("c1", versionOf(t, s, "c1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditComment("c1", latest.Version, "three"); !errors.Is(err, review.ErrNoComment) {
		t.Errorf("EditComment keeping a deleted comment's body: err = %v; want ErrNoComment", err)
	}
	again, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "again")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditComment("c1", latest.Version, "old editor"); !errors.Is(err, review.ErrStale) {
		t.Errorf("EditComment with the deleted comment's version: err = %v; want ErrStale", err)
	}
	if got, err := s.EditComment("c1", latest.Version, "again"); err != nil || got != again {
		t.Errorf("EditComment keeping the recreated comment's body = %+v, %v; want %+v unchanged", got, err, again)
	}
	if got := commentByID(t, s.Draft(), "c1"); got != again {
		t.Errorf("comment after stale edits = %+v; want %+v", got, again)
	}
	want := s.Draft()
	s.End()
	assertDraftRestores(t, fx, st, want)
}

// TestDeleteNeedsTheLoadedVersion: a delete names the version its page shows. A comment saved
// since, or deleted and created again under its id, is not deleted (ErrStale) and the draft is
// unchanged; deleting an absent comment succeeds, so a resent delete does not fail (Q-27).
func TestDeleteNeedsTheLoadedVersion(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	created, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteComment("c1", created.Version+1000); !errors.Is(err, review.ErrStale) {
		t.Errorf("DeleteComment at a version never assigned: err = %v; want ErrStale", err)
	}
	edited, err := s.EditComment("c1", created.Version, "second")
	if err != nil {
		t.Fatal(err)
	}
	before := s.Draft()
	if err := s.DeleteComment("c1", created.Version); !errors.Is(err, review.ErrStale) {
		t.Errorf("DeleteComment at the version before another edit: err = %v; want ErrStale", err)
	}
	if after := s.Draft(); !reflect.DeepEqual(after, before) {
		t.Errorf("view after a stale delete = %+v; want %+v", after, before)
	}

	if err := s.DeleteComment("c1", edited.Version); err != nil {
		t.Fatalf("DeleteComment at the current version: %v", err)
	}
	if got := s.Draft().Comments; len(got) != 0 {
		t.Fatalf("comments after the delete = %+v; want none", got)
	}
	if err := s.DeleteComment("c1", edited.Version); err != nil {
		t.Errorf("DeleteComment resent after it succeeded: %v", err)
	}
	if err := s.DeleteComment("never-existed", 999); err != nil {
		t.Errorf("DeleteComment of an id never used: %v", err)
	}

	again, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "again")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteComment("c1", edited.Version); !errors.Is(err, review.ErrStale) {
		t.Errorf("DeleteComment with the deleted comment's version: err = %v; want ErrStale", err)
	}
	if got := commentByID(t, s.Draft(), "c1"); got != again {
		t.Errorf("comment after a delayed delete = %+v; want %+v", got, again)
	}
	want := s.Draft()
	s.End()
	assertDraftRestores(t, fx, st, want)
}

// Seeded and restored comments are edited with the version the session reports for them.
func TestSeededAndRestoredCommentsAreEditable(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	dir := t.TempDir()
	s, st := fx.start(t, dir, review.NewSeed(review.SeedComment{Target: reviewTarget, Body: "seeded"}))
	seeded := s.Draft().Comments[0]
	if _, err := s.EditComment(seeded.ID, seeded.Version, "seeded, edited"); err != nil {
		t.Fatalf("EditComment of a seeded comment: %v", err)
	}
	s.End()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2 := openStore(t, dir, fx.key(fx.last))
	restored, o, err := review.Start(t.Context(), st2, fx.last, review.Fresh{Seed: review.NewSeed()})
	if err != nil || !o.Resumed() {
		t.Fatalf("restart: resumed %v, err %v", o.Resumed(), err)
	}
	c := restored.Draft().Comments[0]
	if _, err := restored.EditComment(c.ID, c.Version, "restored, edited"); err != nil {
		t.Fatalf("EditComment of a restored comment: %v", err)
	}
}

// Concurrent edits holding the same version: exactly one wins, and the session and the draft hold
// its body.
func TestConcurrentEditsOfOneVersion(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	s, st := fx.start(t, t.TempDir(), review.NewSeed())
	c, err := s.CreateComment(t.Context(), "c1", review.OnReview(), "start")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for k := range n {
		wg.Go(func() {
			_, errs[k] = s.EditComment("c1", c.Version, fmt.Sprintf("edit %d", k))
		})
	}
	wg.Wait()
	winner := -1
	for k, err := range errs {
		switch {
		case err == nil && winner < 0:
			winner = k
		case err == nil:
			t.Errorf("edits %d and %d both succeeded", winner, k)
		case !errors.Is(err, review.ErrStale):
			t.Errorf("edit %d: err = %v; want nil or ErrStale", k, err)
		}
	}
	if winner < 0 {
		t.Fatal("no edit succeeded")
	}
	want := s.Draft()
	if got := commentByID(t, want, "c1").Body; got != fmt.Sprintf("edit %d", winner) {
		t.Errorf("body = %q; want the winner's, edit %d", got, winner)
	}
	s.End()
	assertDraftRestores(t, fx, st, want)
}
