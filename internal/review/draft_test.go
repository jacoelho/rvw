package review_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jacoelho/rvw/internal/review"
)

// installDraft writes data as the draft of st, which is how a draft left by an earlier session
// (of this or another rvw version) is found.
func installDraft(t *testing.T, st *review.Store, data []byte) {
	t.Helper()
	if err := os.WriteFile(st.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// discarded lists the moved-aside drafts in dir.
func discarded(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.discarded-*"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// assertDraftUntouched checks that st's draft still holds want and nothing was moved aside.
func assertDraftUntouched(t *testing.T, st *review.Store, want []byte) {
	t.Helper()
	got, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("the draft is gone: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the draft changed:\n%s\nwant\n%s", got, want)
	}
	if moved := discarded(t, filepath.Dir(st.Path())); len(moved) != 0 {
		t.Errorf("drafts moved aside: %q", moved)
	}
}

// TestDraftFormatRestores checks that a hand-written draft of the persisted format restores
// exactly; this pins the format a later rvw must read. Sequence numbers keep their relative order,
// however large, and a comment created after the restore comes after every restored one, also after
// a restart.
func TestDraftFormatRestores(t *testing.T) {
	t.Parallel()
	valid := readTestdata(t, "draft_valid.json")
	for name, data := range map[string][]byte{
		"as written":  valid,
		"largest seq": bytes.Replace(valid, []byte(`"seq":4`), []byte(`"seq":9223372036854775807`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t)
			dir := t.TempDir()
			st := openStore(t, dir, fx.key(fx.last))
			installDraft(t, st, data)
			s, o, err := review.Start(t.Context(), st, fx.last, review.NewSeed(review.SeedComment{Target: reviewTarget, Body: "ignored"}))
			if err != nil {
				t.Fatal(err)
			}
			if _, moved, reason := o.MovedAside(); moved || !o.Resumed() {
				t.Fatalf("Start: resumed = %v, moved aside = %v (%v); want the draft resumed", o.Resumed(), moved, reason)
			}
			v := s.View()
			if v.Summary != "Needs work.\n" {
				t.Errorf("summary %q; want the draft's summary", v.Summary)
			}
			wantViewed := []int{fx.fileIndex(t, pathA), fx.fileIndex(t, pathDeleted)}
			slices.Sort(wantViewed)
			if !slices.Equal(v.Viewed, wantViewed) {
				t.Errorf("viewed = %v; want %v", v.Viewed, wantViewed)
			}
			want := map[string]review.Comment{
				"r1": {ID: "r1", Author: review.Human, Body: "overall", Target: reviewTarget, File: -1},
				"f1": {ID: "f1", Author: review.Agent, Body: "why delete?", Target: file(pathDeleted), File: fx.fileIndex(t, pathDeleted)},
				"o1": {ID: "o1", Author: review.Human, Body: "renamed lines", Target: span(pathRenOld, review.Old, 2, 5), File: fx.fileIndex(t, pathRenNew)},
				"l1": {ID: "l1", Author: review.Agent, Body: "changed\nline", Target: line(pathA, review.New, 2), File: fx.fileIndex(t, pathA)},
			}
			seq := map[string]int64{}
			got := map[string]review.Comment{}
			for _, c := range v.Comments {
				seq[c.ID] = c.Seq
				c.Seq, c.Version = 0, 0
				got[c.ID] = c
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("comments =\n%+v\nwant\n%+v", got, want)
			}
			// The draft's creation order: l1, f1, o1, r1.
			if seq["l1"] >= seq["f1"] || seq["f1"] >= seq["o1"] || seq["o1"] >= seq["r1"] {
				t.Errorf("restored seqs %v lost the draft's creation order l1, f1, o1, r1", seq)
			}

			c, err := s.CreateComment(t.Context(), "n1", on(t, s, line(pathA, review.New, 2)), "later")
			if err != nil {
				t.Fatal(err)
			}
			if c.Seq <= seq["r1"] {
				t.Errorf("new comment seq = %d; want it after the restored seqs %v", c.Seq, seq)
			}
			s.End()
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			again := openStore(t, dir, fx.key(fx.last))
			s2, o2, err := review.Start(t.Context(), again, fx.last, review.NewSeed())
			if err != nil {
				t.Fatal(err)
			}
			if !o2.Resumed() {
				t.Fatalf("the draft rvw saved did not resume")
			}
			var ids []string
			for _, c := range s2.View().Comments {
				if c.Target == line(pathA, review.New, 2) {
					ids = append(ids, c.ID)
				}
			}
			if !slices.Equal(ids, []string{"l1", "n1"}) {
				t.Errorf("comments on the same line after a restart = %v; want creation order [l1 n1]", ids)
			}
		})
	}
}

// TestUnusableDraftIsMovedAside checks that a draft that is not a usable draft of this format, or
// that no longer validates against the snapshot, is moved aside unchanged and the session starts as
// if there were none.
func TestUnusableDraftIsMovedAside(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	tests := map[string][]byte{
		"over the comment limit": tooManyComments(review.MaxComments + 1),
	}
	for _, name := range []string{
		"draft_line_beyond_file.json", "draft_binary_line.json", "draft_path_not_changed.json",
		"draft_viewed_absent_file.json", "draft_viewed_wrong_pair.json", "draft_malformed_target.json",
		"draft_unknown_author.json", "draft_bad_id.json",
		"draft_empty_body.json", "draft_duplicate_id.json", "draft_duplicate_seq.json",
		"draft_negative_seq.json", "draft_unknown_member.json", "draft_trailing_data.json",
		"draft_empty.json", "draft_truncated.json", "draft_version1.json", "draft_version99.json",
	} {
		tests[name] = readTestdata(t, name)
	}
	for name, draft := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			st := openStore(t, dir, fx.key(fx.last))
			installDraft(t, st, draft)
			assertMovedAside(t, fx, st, draft)
		})
	}
	t.Run("over the size limit", func(t *testing.T) {
		t.Parallel()
		st := openStore(t, t.TempDir(), fx.key(fx.last))
		// A sparse file: its size is what matters, not its bytes.
		f, err := os.Create(st.Path())
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(f.Truncate(64<<20+1), f.Close()); err != nil {
			t.Fatal(err)
		}
		draft, err := os.ReadFile(st.Path())
		if err != nil {
			t.Fatal(err)
		}
		assertMovedAside(t, fx, st, draft)
	})
}

// tooManyComments is a draft of the persisted format, valid but for holding n review comments.
func tooManyComments(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"version":2,"comments":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"c%d","author":"human","target":{"kind":"review"},"body":"x","seq":%d}`, i, i)
	}
	b.WriteString(`],"summary":"","viewed":[]}`)
	return []byte(b.String())
}

// assertMovedAside starts a session over st's draft, which holds draft, and checks that the draft
// was moved aside byte for byte, the seed applied, and a restart resumes the seeded draft.
func assertMovedAside(t *testing.T, fx *fixture, st *review.Store, draft []byte) {
	t.Helper()
	seed := review.NewSeed(review.SeedComment{Target: reviewTarget, Body: "seeded"})
	s, o, err := review.Start(t.Context(), st, fx.last, seed)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	moved, ok, reason := o.MovedAside()
	if !ok || o.Resumed() {
		t.Fatalf("Start: resumed = %v, moved aside = %v; want the draft moved aside", o.Resumed(), ok)
	}
	if reason == nil {
		t.Error("MovedAside reports no reason")
	}
	dir := filepath.Dir(st.Path())
	if filepath.Dir(moved) != dir || !strings.HasPrefix(filepath.Base(moved), fx.key(fx.last)+".discarded-") {
		t.Errorf("moved to %s; want a discarded sibling of %s", moved, st.Path())
	}
	if got, err := os.ReadFile(moved); err != nil || !bytes.Equal(got, draft) {
		t.Errorf("moved draft: %v; want the original bytes", err)
	}
	if got := s.View().Comments; len(got) != 1 || got[0].Body != "seeded" || got[0].Author != review.Agent {
		t.Errorf("comments = %+v; want the seed", got)
	}
	if now, err := os.ReadFile(st.Path()); err != nil || bytes.Equal(now, draft) {
		t.Errorf("the draft path after the move: %v; want the seeded draft", err)
	}

	s.End()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2 := openStore(t, dir, fx.key(fx.last))
	_, o, err = review.Start(t.Context(), st2, fx.last, review.NewSeed())
	if _, again, _ := o.MovedAside(); err != nil || again || !o.Resumed() {
		t.Errorf("restart: resumed = %v, moved aside = %v, err = %v; want the seeded draft resumed", o.Resumed(), again, err)
	}
	if n := len(discarded(t, dir)); n != 1 {
		t.Errorf("%d drafts moved aside; want 1", n)
	}
}

// A draft that could not be read or checked is not judged: Start fails and leaves it in place, so
// a cancellation or an IO error never discards the human's work.
func TestDraftThatCannotBeCheckedStaysInPlace(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	draft := readTestdata(t, "draft_valid.json")

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		st := openStore(t, t.TempDir(), fx.key(fx.last))
		installDraft(t, st, draft)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		// The draft's line comment needs git to validate, so the cancellation is observed.
		if _, _, err := review.Start(ctx, st, fx.last, review.NewSeed()); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start with a cancelled context = %v; want context.Canceled", err)
		}
		assertDraftUntouched(t, st, draft)
	})
	t.Run("unreadable", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		st := openStore(t, t.TempDir(), fx.key(fx.last))
		installDraft(t, st, draft)
		if err := os.Chmod(st.Path(), 0); err != nil {
			t.Fatal(err)
		}
		_, _, err := review.Start(t.Context(), st, fx.last, review.NewSeed())
		if err == nil || !strings.Contains(err.Error(), st.Path()) {
			t.Fatalf("Start with an unreadable draft = %v; want an error naming %s", err, st.Path())
		}
		if err := os.Chmod(st.Path(), 0o600); err != nil {
			t.Fatal(err)
		}
		assertDraftUntouched(t, st, draft)
	})
}

// TestExpireDiscardedDrafts checks that a draft moved aside is kept for 30 days, then expires; live
// drafts are unaffected.
//
// Not parallel: a git child forked by a parallel test briefly shares a closed store's lock (see
// openStore), and expiry would then rightly skip the draft as held.
func TestExpireDiscardedDrafts(t *testing.T) {
	fx := newFixture(t)
	dir := t.TempDir()
	for range 2 {
		st := openStore(t, dir, fx.key(fx.last))
		installDraft(t, st, []byte("{corrupt"))
		s, o, err := review.Start(t.Context(), st, fx.last, review.NewSeed())
		if _, moved, _ := o.MovedAside(); err != nil || !moved {
			t.Fatalf("Start: moved aside = %v, err = %v; want the corrupt draft moved aside", moved, err)
		}
		if err := s.SetSummary("live"); err != nil {
			t.Fatal(err)
		}
		s.End()
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	moved := discarded(t, dir)
	if len(moved) != 2 {
		t.Fatalf("moved aside %q; want two drafts", moved)
	}
	old, fresh := moved[0], moved[1]
	month := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(old, month, month); err != nil {
		t.Fatal(err)
	}

	if err := review.ExpireDrafts(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fileExists(t, old) {
		t.Error("a draft moved aside 31 days ago was kept")
	}
	if !fileExists(t, fresh) {
		t.Error("a draft moved aside today expired")
	}
	if !fileExists(t, filepath.Join(dir, fx.key(fx.last)+".json")) {
		t.Error("the live draft expired")
	}
}
