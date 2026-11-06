package round_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/source"
)

// priorOf records view of first in a fresh ledger and derives second's round facts from it.
func priorOf(t *testing.T, first *source.Snapshot, view review.Draft, at time.Time, second *source.Snapshot) *round.Prior {
	t.Helper()
	s := openStore(t)
	record(t, s, newFakeRefs(), round.Delivered(first, view, at))
	return round.NewPrior(second, load(t, s, at))
}

func comment(t *testing.T, snap *source.Snapshot, id string, tg review.Target) review.Comment {
	t.Helper()
	file := -1
	if tg.Kind() != review.TargetReview {
		file = fileIndex(t, snap, tg.Path())
	}
	return review.Comment{ID: id, Author: review.Human, Body: "about " + id, Target: tg, File: file}
}

// Earlier comment IDs are the first 16 hex digits of SHA-256(delivery nanoseconds, NUL, draft ID)
// (Definitions: earlier comment).
func TestDeliveredEarlierIDs(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("base")
	r.write("a.txt", "b\n")
	snap := r.uncommitted(r.open())
	at := time.Date(2026, 5, 6, 7, 8, 9, 10, time.UTC)
	d := round.Delivered(snap, review.Draft{Comments: []review.Comment{comment(t, snap, "c1", review.ReviewTarget())}}, at)
	sum := sha256.Sum256([]byte(strconv.FormatInt(at.UnixNano(), 10) + "\x00c1"))
	if want := hex.EncodeToString(sum[:])[:16]; len(d.Comments) != 1 || d.Comments[0].ID != want {
		t.Errorf("earlier comments %+v, want one with ID %s", d.Comments, want)
	}
	later := round.Delivered(snap, review.Draft{Comments: []review.Comment{comment(t, snap, "c1", review.ReviewTarget())}}, at.Add(1))
	if later.Comments[0].ID == d.Comments[0].ID {
		t.Error("two deliveries gave one comment the same earlier ID")
	}
}

// placeFixture is two uncommitted snapshots of one repository: round 1 with each file at version 1,
// round 2 with each file edited as its name says. HEAD has h1..h3 in every file.
type placeFixture struct {
	r             *repo
	first, second *source.Snapshot
}

func newPlaceFixture(t *testing.T) placeFixture {
	t.Helper()
	r := newRepo(t)
	names := []string{
		"shift.txt", "neighbour.txt", "deleted.txt", "boundary.txt", "rangein.txt", "rangeedge.txt", "same.txt",
		"reverted.txt", "removed.txt", "adjacent.txt", "rangeshift.txt", "oldpend.txt",
	}
	for _, n := range names {
		r.write(n, lines("h", 3))
	}
	r.commit("base")
	for _, n := range names {
		r.write(n, lines("n", 10))
	}
	sr := r.open()
	first := r.uncommitted(sr)
	r.write("shift.txt", "x1\nx2\n"+lines("n", 10))
	r.write("neighbour.txt", "n1\nn2\nn3\nedited\nn5\nn6\nn7\nn8\nn9\nn10\n")
	r.write("deleted.txt", "n1\nn2\nn3\nn4\nn6\nn7\nn8\nn9\nn10\n")
	r.write("boundary.txt", "n1\nn2\nn3\nn4\na\nn5\nb\nn6\nn7\nn8\nn9\nn10\n")
	r.write("rangein.txt", "n1\nn2\nn3\nn4\na\nn5\nn6\nn7\nn8\nn9\nn10\n")
	r.write("rangeedge.txt", "n1\nn2\nn3\na\nn4\nn5\nn6\nb\nn7\nn8\nn9\nn10\n")
	r.write("reverted.txt", lines("h", 3))
	r.write("removed.txt", "n3\nn4\nn5\nn6\nn7\nn8\nn9\nn10\n")
	r.write("adjacent.txt", "n1\nn2\nn3\nn4\nn5\nn6\nedited\nn8\nn9\nn10\n")
	r.write("rangeshift.txt", "x1\nx2\n"+lines("n", 10))
	// oldpend.txt's old side moves: HEAD gains a line above h2, the worktree keeps version 1.
	r.write("oldpend.txt", "h0\n"+lines("h", 3))
	r.git("add", "oldpend.txt")
	r.git("commit", "-q", "--no-verify", "-m", "move oldpend base")
	r.write("oldpend.txt", lines("n", 10))
	return placeFixture{r: r, first: first, second: r.uncommitted(sr)}
}

// Earlier line and range comments are placed through the zero-context diff from their blob
// (REQ-CMT-009). Expected lines are counted by hand from each edit.
func TestPlaceEarlierComments(t *testing.T) {
	t.Parallel()
	f := newPlaceFixture(t)
	tg := target(t)
	cs := []review.Comment{
		comment(t, f.first, "review", review.ReviewTarget()),
		comment(t, f.first, "file", tg(review.FileTarget("same.txt"))),
		comment(t, f.first, "shift", tg(review.LineTarget("shift.txt", review.New, 5))),
		comment(t, f.first, "oldside", tg(review.LineTarget("shift.txt", review.Old, 2))),
		comment(t, f.first, "neighbour", tg(review.LineTarget("neighbour.txt", review.New, 5))),
		comment(t, f.first, "deleted", tg(review.RangeTarget("deleted.txt", review.New, 4, 6))),
		comment(t, f.first, "boundary", tg(review.LineTarget("boundary.txt", review.New, 5))),
		comment(t, f.first, "rangein", tg(review.RangeTarget("rangein.txt", review.New, 4, 6))),
		comment(t, f.first, "rangeedge", tg(review.RangeTarget("rangeedge.txt", review.New, 4, 6))),
		comment(t, f.first, "same", tg(review.LineTarget("same.txt", review.New, 3))),
		comment(t, f.first, "reverted", tg(review.LineTarget("reverted.txt", review.New, 2))),
		comment(t, f.first, "removed", tg(review.LineTarget("removed.txt", review.New, 5))),
		comment(t, f.first, "adjacent", tg(review.RangeTarget("adjacent.txt", review.New, 4, 6))),
		comment(t, f.first, "rangeshift", tg(review.RangeTarget("rangeshift.txt", review.New, 3, 6))),
		comment(t, f.first, "oldpend", tg(review.LineTarget("oldpend.txt", review.Old, 2))),
	}
	p := priorOf(t, f.first, review.Draft{Comments: cs}, t0, f.second)

	// Before any git work: final where the changed-file list decides, pending where blobs differ.
	ids := map[string]string{} // draft ID -> earlier ID
	wantStatic := map[string]round.Status{
		"review": round.StatusCurrent, "file": round.StatusCurrent, "oldside": round.StatusCurrent,
		"same": round.StatusCurrent, "reverted": round.StatusUnplaced,
		"shift": round.StatusPending, "neighbour": round.StatusPending, "deleted": round.StatusPending,
		"boundary": round.StatusPending, "rangein": round.StatusPending, "rangeedge": round.StatusPending,
		"removed": round.StatusPending, "adjacent": round.StatusPending, "rangeshift": round.StatusPending,
		"oldpend": round.StatusPending,
	}
	earlier := p.Earlier()
	if len(earlier) != len(cs) {
		t.Fatalf("%d earlier comments, want %d", len(earlier), len(cs))
	}
	for k, e := range earlier {
		name := cs[k].ID
		ids[name] = e.ID
		if e.Status != wantStatic[name] {
			t.Errorf("%s: status before placing = %v, want %v", name, e.Status, wantStatic[name])
		}
	}
	if e := earlier[10]; e.File != -1 {
		t.Errorf("reverted.txt is no longer changed, but its comment has file %d", e.File)
	}

	type want struct {
		status     round.Status
		start, end int
		quote      []string
	}
	cases := map[string]struct {
		file string
		want map[string]want
	}{
		"inserted above":         {"shift.txt", map[string]want{"shift": {round.StatusCurrent, 7, 7, nil}, "oldside": {round.StatusCurrent, 2, 2, nil}}},
		"edited neighbour":       {"neighbour.txt", map[string]want{"neighbour": {round.StatusCurrent, 5, 5, nil}}},
		"deleted target line":    {"deleted.txt", map[string]want{"deleted": {round.StatusOutdated, 0, 0, []string{"n4", "n5", "n6"}}}},
		"inserted at boundaries": {"boundary.txt", map[string]want{"boundary": {round.StatusCurrent, 6, 6, nil}}},
		"inserted inside range":  {"rangein.txt", map[string]want{"rangein": {round.StatusOutdated, 0, 0, []string{"n4", "n5", "n6"}}}},
		"inserted around range":  {"rangeedge.txt", map[string]want{"rangeedge": {round.StatusCurrent, 5, 7, nil}}},
		"unchanged blob":         {"same.txt", map[string]want{"same": {round.StatusCurrent, 3, 3, nil}}},
		"removed above":          {"removed.txt", map[string]want{"removed": {round.StatusCurrent, 3, 3, nil}}},
		"edited after range":     {"adjacent.txt", map[string]want{"adjacent": {round.StatusCurrent, 4, 6, nil}}},
		"range moved down":       {"rangeshift.txt", map[string]want{"rangeshift": {round.StatusCurrent, 5, 8, nil}}},
		"old side moved":         {"oldpend.txt", map[string]want{"oldpend": {round.StatusCurrent, 3, 3, nil}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			placed, err := p.Place(t.Context(), fileIndex(t, f.second, c.file))
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]want{}
			for _, pl := range placed {
				w := want{status: pl.Status, start: pl.Start, end: pl.End}
				if pl.Quote != nil {
					w.quote = pl.Quote.Code
				}
				for draft, id := range ids {
					if id == pl.ID {
						got[draft] = w
					}
				}
			}
			if len(got) != len(c.want) {
				t.Errorf("%s: placed %+v, want %+v", c.file, got, c.want)
			}
			for draft, w := range c.want {
				g := got[draft]
				if g.status != w.status || g.start != w.start || g.end != w.end || !equalStrings(g.quote, w.quote) {
					t.Errorf("%s comment %q: got %+v, want %+v", c.file, draft, g, w)
				}
			}
		})
	}
}

// StillApplies orders a copy's targets most specific first (REQ-CMT-011): the placed lines on the
// comment's side while current, then its file; only the file while outdated; nothing for a review
// comment or a key no changed file has.
func TestStillApplies(t *testing.T) {
	t.Parallel()
	f := newPlaceFixture(t)
	tg := target(t)
	cs := []review.Comment{
		comment(t, f.first, "review", review.ReviewTarget()),
		comment(t, f.first, "file", tg(review.FileTarget("same.txt"))),
		comment(t, f.first, "rangeshift", tg(review.RangeTarget("rangeshift.txt", review.New, 3, 6))),
		comment(t, f.first, "oldpend", tg(review.LineTarget("oldpend.txt", review.Old, 2))),
		comment(t, f.first, "deleted", tg(review.RangeTarget("deleted.txt", review.New, 4, 6))),
		comment(t, f.first, "reverted", tg(review.LineTarget("reverted.txt", review.New, 2))),
	}
	p := priorOf(t, f.first, review.Draft{Comments: cs}, t0, f.second)
	idx := func(name string) int { return fileIndex(t, f.second, name) }
	want := map[string][]review.Placement{
		"review":     nil,
		"file":       {review.OnFile(idx("same.txt"))},
		"rangeshift": {review.OnRange(idx("rangeshift.txt"), review.New, 5, 8), review.OnFile(idx("rangeshift.txt"))},
		"oldpend":    {review.OnLine(idx("oldpend.txt"), review.Old, 3), review.OnFile(idx("oldpend.txt"))},
		"deleted":    {review.OnFile(idx("deleted.txt"))},
		"reverted":   nil,
	}
	for k, e := range p.Earlier() {
		got, err := p.StillApplies(t.Context(), e)
		if err != nil {
			t.Errorf("%s: %v", cs[k].ID, err)
			continue
		}
		if w := want[cs[k].ID]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s: StillApplies = %+v, want %+v", cs[k].ID, got, w)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A comment whose blob was pruned is pending at start, then unplaced (Definitions: unplaced).
func TestPlacePrunedBlob(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("f.txt", "base\n")
	r.commit("base")
	r.write("f.txt", "reviewed only once\n")
	sr := r.open()
	first := r.uncommitted(sr)
	r.write("f.txt", "now\n")
	second := r.uncommitted(sr)
	// The index keeps the current blob; nothing keeps the reviewed one.
	r.git("add", "f.txt")
	r.git("gc", "-q", "--prune=now")
	if r.gitOK("cat-file", "-e", first.File(0).NewBlob) {
		t.Fatal("fixture: the reviewed blob survived gc")
	}
	tg := target(t)
	p := priorOf(t, first, review.Draft{Comments: []review.Comment{
		comment(t, first, "c", tg(review.LineTarget("f.txt", review.New, 1))),
	}}, t0, second)
	if got := p.Earlier()[0].Status; got != round.StatusPending {
		t.Errorf("status at start = %v, want pending", got)
	}
	placed, err := p.Place(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(placed) != 1 || placed[0].Status != round.StatusUnplaced {
		t.Errorf("placed %+v, want one unplaced", placed)
	}
}

// A placement that cannot be computed leaves only the comments it serves unplaced (Definitions:
// unplaced): a comment whose recorded blob is not a blob degrades, while another comment of the
// same file is still placed. Copying the degraded comment fails, so it can be retried, rather than
// falling back to its file; a cancelled request fails the whole placement.
func TestPlaceDegradesPerComment(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	lines := func(edit map[int]string) string {
		var b strings.Builder
		for n := 1; n <= 10; n++ {
			l, ok := edit[n]
			if !ok {
				l = "line " + strconv.Itoa(n)
			}
			b.WriteString(l + "\n")
		}
		return b.String()
	}
	r.write("f.txt", lines(nil))
	r.commit("base")
	r.write("f.txt", lines(map[int]string{5: "line 5 round 1"}))
	sr := r.open()
	first := r.uncommitted(sr)
	r.write("f.txt", lines(map[int]string{5: "line 5 round 1", 8: "line 8 round 2"}))
	second := r.uncommitted(sr)

	tg := target(t)
	d := round.Delivered(first, review.Draft{Comments: []review.Comment{
		comment(t, first, "broken", tg(review.LineTarget("f.txt", review.New, 5))),
		comment(t, first, "kept", tg(review.LineTarget("f.txt", review.New, 2))),
	}}, t0)
	// A commit ID where a blob ID belongs makes git fail with something other than a missing or
	// binary object.
	d.Comments[0].Blob.ID = strings.TrimSpace(r.git("rev-parse", "HEAD"))
	s := openStore(t)
	record(t, s, newFakeRefs(), d)
	p := round.NewPrior(second, load(t, s, t0))
	broken, kept := p.Earlier()[0], p.Earlier()[1]

	placed, err := p.Place(t.Context(), 0)
	if err != nil {
		t.Fatalf("Place: %v, want per-comment degrade", err)
	}
	if len(placed) != 2 {
		t.Fatalf("placed %+v, want two comments", placed)
	}
	if b := placed[0]; b.ID != broken.ID || b.Status != round.StatusUnplaced || b.Cause == nil {
		t.Errorf("broken comment placed %+v, want unplaced with a cause", b)
	}
	if k := placed[1]; k.ID != kept.ID || k.Status != round.StatusCurrent || k.Start != 2 || k.End != 2 || k.Cause != nil {
		t.Errorf("kept comment placed %+v, want current on line 2", k)
	}

	if got, err := p.StillApplies(t.Context(), broken); err == nil {
		t.Errorf("StillApplies(broken) = %+v, want an error", got)
	}
	got, err := p.StillApplies(t.Context(), kept)
	if want := []review.Placement{review.OnLine(0, review.New, 2), review.OnFile(0)}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("StillApplies(kept) = %+v, %v; want %+v", got, err, want)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := p.Place(ctx, 0); err == nil {
		t.Errorf("Place with a cancelled context = %+v, want an error", got)
	}
}

// Round states and the marks a new draft starts with (Definitions: round state; REQ-RND-009): a
// file reviewed unchanged is preset, unless its mark was set with whitespace hidden and the new
// draft shows whitespace.
func TestRoundStatesAndPresetMarks(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	names := []string{"same.txt", "samews.txt", "changed.txt", "base.txt", "both.txt", "unreviewed.txt"}
	for _, n := range names {
		r.write(n, "0 "+n+"\n")
	}
	r.commit("round 0")
	for _, n := range names {
		r.write(n, "1 "+n+"\n")
	}
	sr := r.open()
	first := r.uncommitted(sr)
	// Round 2: changed.txt's new side moves; base.txt's and both.txt's old side moves in HEAD.
	r.write("base.txt", "X\n")
	r.write("both.txt", "X\n")
	r.git("add", "base.txt", "both.txt")
	r.git("commit", "-q", "--no-verify", "-m", "move base")
	r.write("base.txt", "1 base.txt\n")
	r.write("both.txt", "2 both.txt\n")
	r.write("changed.txt", "2 changed.txt\n")
	second := r.uncommitted(sr)

	marks := []review.Mark{}
	for _, n := range names[:5] {
		marks = append(marks, review.Mark{File: fileIndex(t, first, n), Whitespace: n != "same.txt"})
	}
	wantState := map[string]round.State{
		"same.txt": round.StateSame, "samews.txt": round.StateSame, "changed.txt": round.StateChanged,
		"base.txt": round.StateBaseChanged, "both.txt": round.StateChangedAndBase, "unreviewed.txt": round.StateUnreviewed,
	}
	for _, hide := range []bool{false, true} {
		t.Run("hide whitespace "+strconv.FormatBool(hide), func(t *testing.T) {
			t.Parallel()
			at := t0.Add(123 * time.Millisecond)
			p := priorOf(t, first, review.Draft{Viewed: marks, HideWhitespace: hide}, at, second)
			for n, want := range wantState {
				st, rev := p.State(fileIndex(t, second, n))
				if st != want {
					t.Errorf("%s: state %v, want %v", n, st, want)
				}
				if want != round.StateUnreviewed && (!rev.At.Equal(at) || rev.Whitespace != (n != "same.txt")) {
					t.Errorf("%s: reviewed at %v whitespace %v, want %v %v", n, rev.At, rev.Whitespace, at, n != "same.txt")
				}
			}
			want := []review.Mark{{File: fileIndex(t, second, "same.txt")}}
			if hide {
				want = append(want, review.Mark{File: fileIndex(t, second, "samews.txt"), Whitespace: true})
			}
			slices.SortFunc(want, func(a, b review.Mark) int { return a.File - b.File })
			if got := p.PresetMarks(); !slices.Equal(got, want) {
				t.Errorf("PresetMarks = %v, want %v", got, want)
			}
			if p.HideWhitespace() != hide || !p.LastAt().Equal(at) || p.Empty() {
				t.Errorf("HideWhitespace %v LastAt %v Empty %v", p.HideWhitespace(), p.LastAt(), p.Empty())
			}
		})
	}
}

// With an empty ledger every file is unreviewed and nothing is preset.
func TestEmptyPrior(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("base")
	r.write("a.txt", "b\n")
	p := round.NewPrior(r.uncommitted(r.open()), round.Ledger{})
	if st, _ := p.State(0); st != round.StateUnreviewed || len(p.PresetMarks()) != 0 || len(p.Earlier()) != 0 || !p.Empty() {
		t.Errorf("state %v marks %v earlier %v empty %v", st, p.PresetMarks(), p.Earlier(), p.Empty())
	}
}
