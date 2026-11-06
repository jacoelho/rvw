package round_test

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/round"
	"github.com/jacoelho/rvw/internal/source"
)

// target fails t when a target constructor fails.
func target(t *testing.T) func(review.Target, error) review.Target {
	t.Helper()
	return func(v review.Target, err error) review.Target {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// A review finished with no comments must round-trip: REQ-RND-002 replaces the earlier comments
// with the delivery's, here none, and the ledger stays loadable.
func TestRecordWithoutCommentsLoads(t *testing.T) {
	t.Parallel()
	for name, comments := range map[string][]round.Earlier{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := openStoreIn(t, dir, "/repo/.git", "/repo")
			snap := round.Snapshot{Base: tree1, Head: tree2}
			d := marked(t0, snap, map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)})
			d.Comments = comments
			record(t, s, newFakeRefs(), d)

			l, err := s.Load(t0)
			if err != nil {
				t.Fatalf("Load after a delivery with no comments: %v", err)
			}
			if got := dirNames(t, dir); slices.ContainsFunc(got, func(n string) bool { return strings.Contains(n, "discarded") }) {
				t.Errorf("ledger was moved aside: %v", got)
			}
			if _, ok := l.Reviewed(modified("a.go")); !ok {
				t.Error("reviewed version of a.go lost")
			}
			if got := l.Comments().Items; len(got) != 0 {
				t.Errorf("earlier comments = %v, want none", got)
			}
		})
	}
}

// fullDelivery has every kind of key, mode and target the format must keep.
func fullDelivery(t *testing.T) round.Delivery {
	t.Helper()
	at := time.Date(2026, 4, 5, 6, 7, 8, 123456789, time.UTC)
	snap := round.Snapshot{Base: tree1, Head: tree2}
	renamed := round.FileKey{OldPath: "old/name.go", NewPath: "new/name.go"}
	deleted := round.FileKey{OldPath: "gone.txt"}
	link := modified("link")
	return round.Delivery{
		At: at, Snapshot: snap, HideWhitespace: true,
		Reviewed: map[round.FileKey]round.Reviewed{
			renamed: {At: at, Snapshot: snap, Version: modVersion(blobA, blobB), Whitespace: true},
			deleted: {At: at, Snapshot: snap, Version: round.Version{Old: regular(blobC)}},
			link: {At: at, Snapshot: snap, Version: round.Version{
				Old: source.Blob{Mode: "120000", ID: blobA}, New: source.Blob{Mode: "120000", ID: blobC},
			}},
		},
		Comments: []round.Earlier{
			{ID: "0123456789abcdef", Author: review.Human, Body: "réview: ✓ 日本", Target: review.ReviewTarget()},
			{
				ID: "1111222233334444", Author: review.Agent, Body: "file note", Key: renamed,
				Target: target(t)(review.FileTarget("new/name.go")),
			},
			{
				ID: "aaaabbbbccccdddd", Author: review.Human, Body: "line", Key: renamed, Blob: regular(blobB),
				Target: target(t)(review.LineTarget("new/name.go", review.New, 7)),
			},
			{
				ID: "fedcba9876543210", Author: review.Human, Body: "range", Key: deleted, Blob: regular(blobC),
				Target: target(t)(review.RangeTarget("gone.txt", review.Old, 2, 4)),
			},
		},
	}
}

// What Record writes, Load reads back unchanged (REQ-RND-002).
func TestLedgerRoundTrip(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	d := fullDelivery(t)
	record(t, s, newFakeRefs(), d)
	l := load(t, s, t0)

	for key, want := range d.Reviewed {
		got, ok := l.Reviewed(key)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("Reviewed(%+v) = %+v, %v; want %+v", key, got, ok, want)
		}
	}
	want := round.Comments{At: d.At, Snapshot: d.Snapshot, Items: d.Comments}
	if got := l.Comments(); !reflect.DeepEqual(got, want) {
		t.Errorf("Comments() =\n%+v\nwant\n%+v", got, want)
	}
	if !l.HideWhitespace() || !l.LastAt().Equal(d.At) || l.Empty() {
		t.Errorf("HideWhitespace %v, LastAt %v, Empty %v; want true, %v, false", l.HideWhitespace(), l.LastAt(), l.Empty(), d.At)
	}
}

// An invalid ledger is moved aside with its bytes, and the session starts with an empty ledger
// (REQ-RND-007). Each case corrupts a valid ledger in one way.
func TestLoadMovesInvalidLedgerAside(t *testing.T) {
	t.Parallel()
	type doc = map[string]any
	asDoc := func(v any) doc {
		d, ok := v.(doc)
		if !ok {
			panic(fmt.Sprintf("not an object: %v", v))
		}
		return d
	}
	asList := func(v any) []any {
		l, ok := v.([]any)
		if !ok {
			panic(fmt.Sprintf("not an array: %v", v))
		}
		return l
	}
	reviewed := func(m doc, i int) doc { return asDoc(asList(m["reviewed"])[i]) }
	item := func(m doc, i int) doc { return asDoc(asList(asDoc(m["comments"])["items"])[i]) }
	// findReviewed returns the reviewed entry with old path p.
	findReviewed := func(m doc, p string) doc {
		for _, e := range asList(m["reviewed"]) {
			if asDoc(e)["old_path"] == p {
				return asDoc(e)
			}
		}
		panic("no reviewed entry " + p)
	}
	mutate := func(f func(doc)) func([]byte) []byte {
		return func(b []byte) []byte {
			var m doc
			if err := json.Unmarshal(b, &m); err != nil {
				panic(err)
			}
			f(m)
			out, err := json.Marshal(m)
			if err != nil {
				panic(err)
			}
			return out
		}
	}
	cases := map[string]func([]byte) []byte{
		"truncated":     func(b []byte) []byte { return b[:len(b)/2] },
		"trailing data": func(b []byte) []byte { return append(slices.Clone(b), []byte(` {}`)...) },
		"empty file":    func([]byte) []byte { return nil },
		"unknown member": mutate(func(m doc) {
			m["extra"] = 1
		}),
		"version 2": mutate(func(m doc) { m["version"] = 2 }),
		"blob id is a path": mutate(func(m doc) {
			reviewed(m, 0)["new_blob"] = "../x"
		}),
		"entry after last round": mutate(func(m doc) {
			reviewed(m, 0)["at"] = "2026-04-05T07:00:00Z"
		}),
		"repeated file key": mutate(func(m doc) {
			m["reviewed"] = append(asList(m["reviewed"]), reviewed(m, 0))
		}),
		"absent side has a blob": mutate(func(m doc) {
			e := findReviewed(m, "gone.txt")
			e["new_mode"], e["new_blob"] = "100644", blobA
		}),
		"bad comment id": mutate(func(m doc) {
			item(m, 0)["id"] = "NOT-HEX-0123456"
		}),
		"target path is not the key's": mutate(func(m doc) {
			asDoc(item(m, 1)["target"])["path"] = "old/name.go"
		}),
		"comments without a delivery time": mutate(func(m doc) {
			asDoc(m["comments"])["at"] = "0001-01-01T00:00:00Z"
		}),
		"line comment blob is a path": mutate(func(m doc) {
			item(m, 2)["blob"] = "../x"
		}),
		"line comment on a gitlink": mutate(func(m doc) {
			item(m, 2)["mode"] = "160000"
		}),
		"file comment has a blob": mutate(func(m doc) {
			item(m, 1)["mode"], item(m, 1)["blob"] = "100644", blobA
		}),
		"unknown author": mutate(func(m doc) {
			item(m, 0)["author"] = "robot"
		}),
		"empty body": mutate(func(m doc) {
			item(m, 0)["body"] = ""
		}),
		"comment on the absent side": mutate(func(m doc) {
			asDoc(item(m, 3)["target"])["side"] = "new"
		}),
		"repeated comment id": mutate(func(m doc) {
			item(m, 1)["id"] = item(m, 0)["id"]
		}),
		"too many reviewed versions": mutate(func(m doc) {
			first := findReviewed(m, "link")
			many := make([]any, 0, round.MaxReviewed+1)
			for i := range round.MaxReviewed + 1 {
				e := maps.Clone(first)
				e["old_path"], e["new_path"] = fmt.Sprintf("f%d", i), fmt.Sprintf("f%d", i)
				many = append(many, e)
			}
			m["reviewed"] = many
		}),
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := openStoreIn(t, dir, "/repo/.git", "/repo")
			record(t, s, newFakeRefs(), fullDelivery(t))
			bad := corrupt(readFile(t, s.Path()))
			if err := os.WriteFile(s.Path(), bad, 0o600); err != nil {
				t.Fatal(err)
			}

			l, err := s.Load(t0)
			moved, ok := errors.AsType[*round.MovedAsideError](err)
			if !ok {
				t.Fatalf("Load error = %v, want *MovedAsideError", err)
			}
			if !l.Empty() {
				t.Error("Load returned a non-empty ledger for an invalid file")
			}
			if filepath.Dir(moved.Path) != dir || moved.Path == s.Path() {
				t.Errorf("moved to %s, want a sibling of %s", moved.Path, s.Path())
			}
			if got := readFile(t, moved.Path); string(got) != string(bad) {
				t.Errorf("moved ledger bytes changed:\n got %q\nwant %q", got, bad)
			}
			if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("ledger still at %s: %v", s.Path(), err)
			}
			if l, err := s.Load(t0); err != nil || !l.Empty() {
				t.Errorf("second Load = empty %v, %v; want an empty ledger and no error", l.Empty(), err)
			}
		})
	}
}

// REQ-RND-004 TTL: a version delivered exactly TTL before the newest delivery is kept; one second
// older is dropped, with its retention ref.
func TestRecordDropsVersionsOlderThanTTL(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	oldSnap := round.Snapshot{Base: tree1, Head: tree2}
	record(t, s, refs, marked(t0, oldSnap, map[round.FileKey]round.Version{modified("old.go"): modVersion(blobA, blobB)}))

	record(t, s, refs, marked(t0.Add(30*day), round.Snapshot{Base: tree1, Head: tree3},
		map[round.FileKey]round.Version{modified("b.go"): modVersion(blobA, blobC)}))
	if _, ok := load(t, s, t0.Add(30*day)).Reviewed(modified("old.go")); !ok {
		t.Error("version exactly 30 days old was dropped")
	}
	oldRef := slices.IndexFunc(refs.names(), func(n string) bool { return strings.HasSuffix(n, "/"+tree1+"-"+tree2) })
	if oldRef < 0 {
		t.Errorf("ref of the 30-day-old snapshot was deleted: %v", refs.names())
	}

	record(t, s, refs, marked(t0.Add(30*day+time.Second), round.Snapshot{Base: tree2, Head: tree3},
		map[round.FileKey]round.Version{modified("c.go"): modVersion(blobB, blobC)}))
	l := load(t, s, t0.Add(30*day))
	if _, ok := l.Reviewed(modified("old.go")); ok {
		t.Error("version 30 days and 1 second old was kept")
	}
	if _, ok := l.Reviewed(modified("b.go")); !ok {
		t.Error("newer version b.go dropped")
	}
	for _, n := range refs.names() {
		if strings.HasSuffix(n, "/"+tree1+"-"+tree2) {
			t.Errorf("ref of the expired snapshot kept: %s", n)
		}
	}
}

// A delivery time within a second is kept as long as its ledger entry: git's commit date is
// truncated to the second, so the ref must not age out before the version does.
func TestRecordKeepsRefWhileSubsecondVersionLives(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	at := t0.Add(500 * time.Millisecond)
	snap := round.Snapshot{Base: tree1, Head: tree2}
	record(t, s, refs, marked(at, snap, map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)}))
	// TTL after the delivery exactly: the version stays (not older than TTL), and so must its ref.
	record(t, s, refs, marked(at.Add(round.TTL), round.Snapshot{Base: tree1, Head: tree3},
		map[round.FileKey]round.Version{modified("b.go"): modVersion(blobA, blobC)}))
	if _, ok := load(t, s, t0).Reviewed(modified("a.go")); !ok {
		t.Fatal("version exactly TTL old was dropped")
	}
	found := false
	for _, n := range refs.names() {
		found = found || strings.HasSuffix(n, "/"+tree1+"-"+tree2)
	}
	if !found {
		t.Errorf("ref of a snapshot the ledger keeps was deleted; refs %v", refs.names())
	}
}

// REQ-RND-004 snapshot bound: the 257th delivered snapshot drops the oldest snapshot's versions
// and its retention ref.
func TestRecordBoundsSnapshots(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	for k := range round.MaxSnapshots + 1 {
		at := t0.Add(time.Duration(k) * time.Second)
		snap := round.Snapshot{Base: tree1, Head: objectID(k)}
		record(t, s, refs, marked(at, snap, map[round.FileKey]round.Version{
			modified(fmt.Sprintf("f%03d.go", k)): modVersion(blobA, blobB),
		}))
	}
	l := load(t, s, t0)
	if _, ok := l.Reviewed(modified("f000.go")); ok {
		t.Error("oldest snapshot's version kept past 256 snapshots")
	}
	for _, k := range []int{1, round.MaxSnapshots} {
		if _, ok := l.Reviewed(modified(fmt.Sprintf("f%03d.go", k))); !ok {
			t.Errorf("version of snapshot %d dropped", k)
		}
	}
	if got := len(refs.names()); got != round.MaxSnapshots {
		t.Errorf("%d retention refs, want %d", got, round.MaxSnapshots)
	}
	if slices.ContainsFunc(refs.names(), func(n string) bool { return strings.HasSuffix(n, "-"+objectID(0)) }) {
		t.Error("oldest snapshot's ref kept")
	}
}

// The earlier comments' snapshot counts towards the 256 snapshots whose refs a ledger keeps.
func TestRecordBoundsSnapshotsCountingComments(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	for k := range round.MaxSnapshots {
		at := t0.Add(time.Duration(k) * time.Second)
		record(t, s, refs, marked(at, round.Snapshot{Base: tree1, Head: objectID(k)}, map[round.FileKey]round.Version{
			modified(fmt.Sprintf("f%03d.go", k)): modVersion(blobA, blobB),
		}))
	}
	last := round.Delivery{
		At: t0.Add(time.Hour), Snapshot: round.Snapshot{Base: tree2, Head: tree3},
		Comments: []round.Earlier{{ID: "0123456789abcdef", Author: review.Human, Body: "x", Target: review.ReviewTarget()}},
	}
	record(t, s, refs, last)
	l := load(t, s, t0)
	if got := len(refs.names()); got != round.MaxSnapshots {
		t.Errorf("%d retention refs, want %d", got, round.MaxSnapshots)
	}
	if _, ok := l.Reviewed(modified("f000.go")); ok {
		t.Error("oldest snapshot's version kept although 256 newer snapshots are named")
	}
	if len(l.Comments().Items) != 1 {
		t.Errorf("earlier comments = %v, want the last delivery's one", l.Comments().Items)
	}
}

// REQ-RND-004 entry bound: of 20,001 versions, the earliest delivered goes.
func TestRecordBoundsReviewedVersions(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	snap := round.Snapshot{Base: tree1, Head: tree2}
	n := round.MaxReviewed + 1
	end := t0.Add(time.Duration(n) * time.Millisecond)
	d := round.Delivery{At: end, Snapshot: snap, Reviewed: make(map[round.FileKey]round.Reviewed, n)}
	for k := range n {
		// File k was delivered k milliseconds after t0, so f00000 is the earliest.
		d.Reviewed[modified(fmt.Sprintf("f%05d", k))] = round.Reviewed{
			At: t0.Add(time.Duration(k) * time.Millisecond), Snapshot: snap, Version: modVersion(blobA, blobB),
		}
	}
	record(t, s, newFakeRefs(), d)
	l := load(t, s, t0)
	if _, ok := l.Reviewed(modified("f00000")); ok {
		t.Error("earliest of 20,001 versions kept")
	}
	kept := 0
	for k := range n {
		if _, ok := l.Reviewed(modified(fmt.Sprintf("f%05d", k))); ok {
			kept++
		}
	}
	if kept != round.MaxReviewed {
		t.Errorf("kept %d versions, want %d", kept, round.MaxReviewed)
	}
}

// Per file key the later delivery wins whatever the recording order; the earlier comments and the
// setting are the latest delivery's (REQ-RND-002, REQ-RND-003).
func TestRecordLaterDeliveryWins(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	key := modified("shared.go")
	newer := marked(t0.Add(10*time.Second), round.Snapshot{Base: tree1, Head: tree3},
		map[round.FileKey]round.Version{key: modVersion(blobA, blobC)})
	newer.Comments = []round.Earlier{{ID: "2222222222222222", Author: review.Agent, Body: "newer", Target: review.ReviewTarget()}}
	newer.HideWhitespace = true
	older := marked(t0.Add(5*time.Second), round.Snapshot{Base: tree1, Head: tree2},
		map[round.FileKey]round.Version{key: modVersion(blobA, blobB), modified("only-older.go"): modVersion(blobB, blobC)})
	older.Comments = []round.Earlier{{ID: "1111111111111111", Author: review.Human, Body: "older", Target: review.ReviewTarget()}}

	record(t, s, refs, newer)
	record(t, s, refs, older)
	l := load(t, s, t0)
	if got, _ := l.Reviewed(key); got.Version != modVersion(blobA, blobC) {
		t.Errorf("shared key version = %+v, want the newer delivery's", got.Version)
	}
	if _, ok := l.Reviewed(modified("only-older.go")); !ok {
		t.Error("older delivery's key that nothing newer replaced was dropped")
	}
	if got := l.Comments().Items; len(got) != 1 || got[0].Body != "newer" {
		t.Errorf("earlier comments = %+v, want the newer delivery's", got)
	}
	if !l.HideWhitespace() || !l.LastAt().Equal(newer.At) {
		t.Errorf("HideWhitespace %v LastAt %v, want true %v", l.HideWhitespace(), l.LastAt(), newer.At)
	}
}

// A later delivery with no comments empties the earlier comments, and an older delivery recorded
// after it does not bring them back.
func TestRecordEmptyCommentsReplace(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	first := round.Delivery{
		At: t0, Snapshot: round.Snapshot{Base: tree1, Head: tree2},
		Comments: []round.Earlier{{ID: "1111111111111111", Author: review.Human, Body: "first", Target: review.ReviewTarget()}},
	}
	record(t, s, refs, first)
	record(t, s, refs, marked(t0.Add(2*time.Minute), round.Snapshot{Base: tree1, Head: tree3},
		map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)}))
	if got := load(t, s, t0).Comments().Items; len(got) != 0 {
		t.Fatalf("after a delivery with no comments, earlier comments = %+v", got)
	}
	late := first
	late.At = t0.Add(time.Minute)
	record(t, s, refs, late)
	if got := load(t, s, t0).Comments().Items; len(got) != 0 {
		t.Errorf("an older delivery restored earlier comments %+v", got)
	}
}

// Sessions of one worktree record at once without losing a delivery (REQ-RND-003).
func TestRecordConcurrentSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	refs := newFakeRefs()
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for k := range n {
		wg.Go(func() {
			s, err := round.OpenStore(dir, "/repo/.git", "/repo")
			if err == nil {
				err = s.Record(t.Context(), refs, marked(t0.Add(time.Duration(k)*time.Second),
					round.Snapshot{Base: tree1, Head: objectID(k)},
					map[round.FileKey]round.Version{modified(fmt.Sprintf("f%d.go", k)): modVersion(blobA, blobB)}))
			}
			errs[k] = err
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	l := load(t, openStoreIn(t, dir, "/repo/.git", "/repo"), t0)
	for k := range n {
		if _, ok := l.Reviewed(modified(fmt.Sprintf("f%d.go", k))); !ok {
			t.Errorf("delivery %d lost", k)
		}
	}
	if got := len(refs.names()); got != n {
		t.Errorf("%d refs, want %d: %v", got, n, refs.names())
	}
}

// One ledger per repository and worktree (REQ-RND-001).
func TestStorePathPerWorktree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := map[string]string{}
	for name, p := range map[string][2]string{
		"main":           {"/r/.git", "/r"},
		"other worktree": {"/r/.git", "/r-wt"},
		"other repo":     {"/s/.git", "/r"},
		"split a|bc":     {"a", "bc"},
		"split ab|c":     {"ab", "c"},
	} {
		got := openStoreIn(t, dir, p[0], p[1]).Path()
		for other, q := range paths {
			if q == got {
				t.Errorf("%s and %s share ledger %s", name, other, got)
			}
		}
		paths[name] = got
	}
	if a, b := openStoreIn(t, dir, "/r/.git", "/r").Path(), paths["main"]; a != b {
		t.Errorf("same worktree gave %s and %s", a, b)
	}
}

// Record keeps the trees before writing the ledger and deletes refs after (REQ-RND-005).
func TestRecordOrder(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	record(t, s, refs, marked(t0, round.Snapshot{Base: tree1, Head: tree2},
		map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)}))
	before := readFile(t, s.Path())

	var atKeep, atDelete []byte
	refs.onKeep = func() error { atKeep = readFile(t, s.Path()); return nil }
	refs.onDelete = func() error { atDelete = readFile(t, s.Path()); return nil }
	// Re-marking a.go in a new snapshot leaves the first snapshot unnamed, so its ref is deleted.
	record(t, s, refs, marked(t0.Add(time.Minute), round.Snapshot{Base: tree1, Head: tree3},
		map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobC)}))
	after := readFile(t, s.Path())

	if string(atKeep) != string(before) {
		t.Error("KeepTrees ran after the ledger was written")
	}
	if string(atDelete) != string(after) || string(after) == string(before) {
		t.Error("DeleteRefs ran before the new ledger was written")
	}
	if got := refs.names(); len(got) != 1 || !strings.HasSuffix(got[0], "/"+tree1+"-"+tree3) {
		t.Errorf("refs = %v, want only the new snapshot's", got)
	}
}

func TestRecordFailures(t *testing.T) {
	t.Parallel()
	first := marked(t0, round.Snapshot{Base: tree1, Head: tree2},
		map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)})
	second := marked(t0.Add(time.Minute), round.Snapshot{Base: tree1, Head: tree3},
		map[round.FileKey]round.Version{modified("b.go"): modVersion(blobA, blobC)})

	t.Run("keep trees fails", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		refs := newFakeRefs()
		record(t, s, refs, first)
		before := readFile(t, s.Path())
		deletes := len(refs.deletes)
		refs.onKeep = func() error { return errors.New("disk full") }
		if err := s.Record(t.Context(), refs, second); err == nil {
			t.Fatal("Record succeeded although KeepTrees failed")
		}
		if got := readFile(t, s.Path()); string(got) != string(before) {
			t.Error("ledger changed although the retention ref was not written")
		}
		if len(refs.deletes) != deletes {
			t.Errorf("refs pruned after a failed keep: %v", refs.deletes[deletes:])
		}
	})
	t.Run("ledger write fails", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		s1 := round.Snapshot{Base: tree1, Head: tree2}
		for name, tc := range map[string]struct {
			prior []round.Delivery
			d     round.Delivery
		}{
			// A new snapshot's ref is deleted again.
			"new snapshot": {[]round.Delivery{first}, second},
			// A snapshot the ledger names keeps its ref as it was.
			"delivered again": {[]round.Delivery{first}, marked(t0.Add(time.Hour), first.Snapshot,
				map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)})},
			// The ref is dated by the second delivery, which the third superseded on a.go, so the
			// ledger alone implies the first delivery's date: the ref must keep the second's.
			"dated by a superseded delivery": {[]round.Delivery{
				marked(t0, s1, map[round.FileKey]round.Version{
					modified("a.go"): modVersion(blobA, blobB), modified("b.go"): modVersion(blobA, blobB),
				}),
				marked(t0.Add(time.Minute), s1, map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)}),
				marked(t0.Add(2*time.Minute), round.Snapshot{Base: tree1, Head: tree3},
					map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobC)}),
			}, marked(t0.Add(time.Hour), s1, map[round.FileKey]round.Version{modified("b.go"): modVersion(blobA, blobB)})},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				s := openStoreIn(t, dir, "/repo/.git", "/repo")
				refs := newFakeRefs()
				for _, d := range tc.prior {
					record(t, s, refs, d)
				}
				d := tc.d
				keeps := refs.keepCount()
				before, beforeRefs := readFile(t, s.Path()), refState(t, refs)
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				if err := s.Record(t.Context(), refs, d); err == nil {
					t.Fatal("Record succeeded although the ledger could not be written")
				}
				if got := readFile(t, s.Path()); string(got) != string(before) {
					t.Error("ledger changed")
				}
				if got := refState(t, refs); !maps.Equal(got, beforeRefs) {
					t.Errorf("retention refs after the failed write %v, want them unchanged %v", got, beforeRefs)
				}
				if refs.keepCount() == keeps {
					t.Error("fixture: Record failed before writing the retention ref, so nothing was restored")
				}
			})
		}
	})
	t.Run("delete refs fails", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		refs := newFakeRefs()
		refs.onDelete = func() error { return errors.New("locked ref") }
		if err := s.Record(t.Context(), refs, first); err == nil {
			t.Fatal("Record hid the DeleteRefs failure")
		}
		if _, ok := load(t, s, t0).Reviewed(modified("a.go")); !ok {
			t.Error("ledger not written before pruning failed")
		}
	})
	t.Run("invalid ledger on disk", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		refs := newFakeRefs()
		bad := []byte(`{"version":1,"garbage"`)
		if err := os.WriteFile(s.Path(), bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.Record(t.Context(), refs, first); err == nil {
			t.Fatal("Record over an invalid ledger succeeded")
		}
		if got := readFile(t, s.Path()); string(got) != string(bad) {
			t.Error("invalid ledger was overwritten")
		}
		if refs.keepCount() != 0 || len(refs.deletes) != 0 {
			t.Errorf("refs touched: keeps %v deletes %v", refs.keeps, refs.deletes)
		}
	})
	t.Run("lock held by another session", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		holder := openStoreIn(t, dir, "/repo/.git", "/repo")
		release := make(chan struct{})
		holding := make(chan struct{})
		refs := newFakeRefs()
		refs.onKeep = func() error { close(holding); <-release; return nil }
		done := make(chan error, 1)
		go func() { done <- holder.Record(t.Context(), refs, first) }()
		<-holding

		s := openStoreIn(t, dir, "/repo/.git", "/repo")
		start := time.Now()
		err := s.Record(t.Context(), newFakeRefs(), second)
		took := time.Since(start)
		close(release)
		if err == nil {
			t.Fatal("Record succeeded while another session held the lock")
		}
		// REQ-RND-003: the lock is waited for up to 2 seconds, then given up.
		if took < 2*time.Second || took > 10*time.Second {
			t.Errorf("Record gave up after %v, want 2s of waiting", took)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, ok := load(t, s, t0).Reviewed(modified("b.go")); ok {
			t.Error("the timed-out delivery was recorded")
		}
	})
	t.Run("lock released while waiting", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		holder := openStoreIn(t, dir, "/repo/.git", "/repo")
		holding := make(chan struct{})
		refs := newFakeRefs()
		refs.onKeep = func() error { close(holding); time.Sleep(300 * time.Millisecond); return nil }
		done := make(chan error, 1)
		go func() { done <- holder.Record(t.Context(), refs, first) }()
		<-holding

		s := openStoreIn(t, dir, "/repo/.git", "/repo")
		if err := s.Record(t.Context(), newFakeRefs(), second); err != nil {
			t.Fatalf("Record after the holder released within 2s: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		l := load(t, s, t0)
		for _, k := range []round.FileKey{modified("a.go"), modified("b.go")} {
			if _, ok := l.Reviewed(k); !ok {
				t.Errorf("%v not recorded; both deliveries should merge", k)
			}
		}
	})
	t.Run("nothing to keep", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		refs := newFakeRefs()
		record(t, s, refs, round.Delivery{At: t0, Snapshot: round.Snapshot{Base: tree1, Head: tree2}})
		if refs.keepCount() != 0 {
			t.Errorf("a delivery with no marks and no comments wrote refs %v", refs.keeps)
		}
	})
}

// ExpireLedgers deletes moved-aside ledgers older than TTL and never a ledger (REQ-RND-007).
func TestExpireLedgers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := openStoreIn(t, dir, "/repo/.git", "/repo")
	if err := os.WriteFile(s.Path(), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load(t0)
	moved, ok := errors.AsType[*round.MovedAsideError](err)
	if !ok {
		t.Fatalf("Load = %v, want *MovedAsideError", err)
	}
	other := openStoreIn(t, dir, "/other/.git", "/other")
	record(t, other, newFakeRefs(), round.Delivery{At: t0, Snapshot: round.Snapshot{Base: tree1, Head: tree2}})

	now := time.Now()
	old := now.Add(-round.TTL - time.Minute)
	for _, p := range []string{moved.Path, other.Path()} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := round.ExpireLedgers(dir, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(moved.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("moved-aside ledger older than TTL kept: %v", err)
	}
	if _, err := os.Stat(other.Path()); err != nil {
		t.Errorf("a ledger was deleted: %v", err)
	}
}

// An older delivery of a snapshot recorded after a newer one keeps the ref dated by the newer: the
// ref lives as long as the ledger keeps the snapshot (REQ-RND-005).
func TestRetentionRefDatedByNewestDelivery(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	snap := round.Snapshot{Base: tree1, Head: tree2}
	newer := marked(t0.Add(10*time.Second), snap, map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)})
	older := marked(t0.Add(5*time.Second), snap, map[round.FileKey]round.Version{modified("b.go"): modVersion(blobA, blobC)})
	record(t, s, refs, newer)
	record(t, s, refs, older)
	got, err := refs.RetentionRefs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Committed.Equal(newer.At.Truncate(time.Second)) {
		t.Errorf("retention refs %+v; want one committed at %v", got, newer.At.Truncate(time.Second))
	}
}

// refState returns each retention ref refs holds by name.
func refState(t *testing.T, refs round.Refs) map[string]source.RetentionRef {
	t.Helper()
	all, err := refs.RetentionRefs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]source.RetentionRef{}
	for _, r := range all {
		out[r.Name] = r
	}
	return out
}

// Pruning deletes a ref only while it holds the commit listed: a ref another worktree's session
// re-dates between the listing and the delete is kept (REQ-RND-005).
func TestPruneKeepsARefRedatedMeanwhile(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	refs := newFakeRefs()
	other := source.RetentionPrefix + "0123456789abcdef/" + tree1 + "-" + tree3
	now := time.Now()
	refs.set(other, now.Add(-round.TTL-time.Hour))
	var once sync.Once
	refs.onDelete = func() error {
		once.Do(func() { refs.set(other, now) })
		return nil
	}
	record(t, s, refs, marked(now, round.Snapshot{Base: tree1, Head: tree2},
		map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)}))
	if got, ok := refState(t, refs)[other]; !ok || !got.Committed.Equal(now.Truncate(time.Second)) {
		t.Errorf("re-dated ref %s = %+v, %v; want it kept at %v", other, got, ok, now.Truncate(time.Second))
	}
}

// REQ-RND-004: the ledger is read as of now, so a ledger that sat idle names nothing older than
// TTL, whichever worktree's age prune deleted the refs meanwhile. Only reviewed versions and
// earlier comments expire: the Hide whitespace setting and last-review time name no snapshot.
func TestLoadExpiresDeliveries(t *testing.T) {
	t.Parallel()
	snap1 := round.Snapshot{Base: tree1, Head: tree2}
	snap2 := round.Snapshot{Base: tree1, Head: tree3}
	comment := func(id string) []round.Earlier {
		return []round.Earlier{{ID: id, Author: review.Human, Body: "x", Target: review.ReviewTarget()}}
	}
	// loadUnchanged loads as of now and requires that the ledger file is not rewritten.
	loadUnchanged := func(t *testing.T, s *round.Store, now time.Time) round.Ledger {
		t.Helper()
		before := readFile(t, s.Path())
		l := load(t, s, now)
		if after := readFile(t, s.Path()); !bytes.Equal(before, after) {
			t.Error("Load rewrote the ledger file")
		}
		return l
	}

	t.Run("two deliveries", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		refs := newFakeRefs()
		d1 := marked(t0, snap1, map[round.FileKey]round.Version{modified("old.go"): modVersion(blobA, blobB)})
		d1.HideWhitespace = true
		d1.Comments = comment("1111111111111111")
		record(t, s, refs, d1)
		d2 := marked(t0.Add(20*day), snap2, map[round.FileKey]round.Version{modified("new.go"): modVersion(blobA, blobC)})
		d2.HideWhitespace = true
		record(t, s, refs, d2)
		lastAt := t0.Add(20 * day)

		l := loadUnchanged(t, s, t0.Add(round.TTL))
		_, hasOld := l.Reviewed(modified("old.go"))
		_, hasNew := l.Reviewed(modified("new.go"))
		if !hasOld || !hasNew || len(l.Comments().Items) != 0 || !l.HideWhitespace() || !l.LastAt().Equal(lastAt) {
			t.Errorf("at exactly TTL: old %v new %v comments %d hide %v last %v; want old and new kept, no comments, hide, %v",
				hasOld, hasNew, len(l.Comments().Items), l.HideWhitespace(), l.LastAt(), lastAt)
		}

		l = loadUnchanged(t, s, t0.Add(round.TTL+time.Nanosecond))
		_, hasOld = l.Reviewed(modified("old.go"))
		_, hasNew = l.Reviewed(modified("new.go"))
		if hasOld || !hasNew || l.Empty() || !l.HideWhitespace() || !l.LastAt().Equal(lastAt) {
			t.Errorf("just past TTL: old %v new %v empty %v hide %v last %v; want only new kept",
				hasOld, hasNew, l.Empty(), l.HideWhitespace(), l.LastAt())
		}

		l = loadUnchanged(t, s, lastAt.Add(round.TTL+time.Nanosecond))
		_, hasOld = l.Reviewed(modified("old.go"))
		_, hasNew = l.Reviewed(modified("new.go"))
		if hasOld || hasNew || len(l.Comments().Items) != 0 || l.Empty() || !l.HideWhitespace() || !l.LastAt().Equal(lastAt) {
			t.Errorf("all past TTL: old %v new %v comments %d empty %v hide %v last %v; want nothing but the setting and time",
				hasOld, hasNew, len(l.Comments().Items), l.Empty(), l.HideWhitespace(), l.LastAt())
		}
	})

	t.Run("earlier comments", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		d := marked(t0, snap1, map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)})
		d.Comments = comment("1111111111111111")
		record(t, s, newFakeRefs(), d)
		if l := loadUnchanged(t, s, t0.Add(round.TTL)); len(l.Comments().Items) != 1 {
			t.Errorf("at exactly TTL: comments %+v, want the one", l.Comments().Items)
		}
		l := loadUnchanged(t, s, t0.Add(round.TTL+time.Nanosecond))
		if _, ok := l.Reviewed(modified("a.go")); ok || len(l.Comments().Items) != 0 {
			t.Errorf("past TTL: reviewed kept %v, comments %+v; want none", ok, l.Comments().Items)
		}
	})
}

// A ledger over maxLedgerBytes would be rejected on the next read, so Record refuses to write it
// and leaves the readable ledger and the retention refs as they were.
//
// Not parallel: it holds about 70 MB of encoded JSON plus the path strings, more under -race.
func TestRecordRefusesOversizedLedger(t *testing.T) {
	s := openStore(t)
	refs := newFakeRefs()
	first := marked(t0, round.Snapshot{Base: tree1, Head: tree2},
		map[round.FileKey]round.Version{modified("a.go"): modVersion(blobA, blobB)})
	record(t, s, refs, first)
	before := readFile(t, s.Path())
	namesBefore := refs.names()

	big := round.Snapshot{Base: tree2, Head: tree3}
	at := t0.Add(time.Hour)
	d := round.Delivery{At: at, Snapshot: big, Reviewed: map[round.FileKey]round.Reviewed{}}
	for k := range 9000 {
		p := fmt.Sprintf("%04d", k) + strings.Repeat("p", 4000-4)
		d.Reviewed[modified(p)] = round.Reviewed{At: at, Snapshot: big, Version: modVersion(blobA, blobB)}
	}
	err := s.Record(t.Context(), refs, d)
	if err == nil || !strings.Contains(err.Error(), "over 67108864") {
		t.Fatalf("Record of an oversized ledger = %v, want an error naming the bound", err)
	}
	if _, moved := errors.AsType[*round.MovedAsideError](err); moved {
		t.Errorf("Record moved the ledger aside: %v", err)
	}
	if after := readFile(t, s.Path()); !bytes.Equal(before, after) {
		t.Error("the ledger file changed")
	}
	if got := refs.names(); !slices.Equal(got, namesBefore) {
		t.Errorf("refs %v, want %v", got, namesBefore)
	}
	l := load(t, s, at)
	if _, ok := l.Reviewed(modified("a.go")); !ok {
		t.Error("the first delivery's version is not readable")
	}
	if _, ok := l.Reviewed(modified("0000" + strings.Repeat("p", 4000-4))); ok {
		t.Error("the refused delivery's version is in the ledger")
	}
}
