package server_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jacoelho/rvw/internal/review"
)

// createComment creates comment id on the changed file target names; the server refuses an id
// that exists.
func (f *fixture) createComment(id string, target targetDTO, body string) response {
	f.t.Helper()
	return f.create(id, f.session().placement(f.t, target), body)
}

// create creates comment id at p.
func (f *fixture) create(id string, p placementDTO, body string) response {
	f.t.Helper()
	return f.do(http.MethodPut, "/api/comments/"+id, putCommentBody(f.t, p, body), createOnly)
}

// editComment edits the body of comment id at the version the session reports now.
func (f *fixture) editComment(id, body string) response {
	f.t.Helper()
	return f.edit(id, f.session().version(f.t, id), body)
}

// edit edits the body of comment id at version v.
func (f *fixture) edit(id string, v int64, body string) response {
	f.t.Helper()
	return f.do(http.MethodPut, "/api/comments/"+id, mustJSON(f.t, map[string]string{"body": body}), editAt(v))
}

// deleteAt deletes comment id at version v.
func (f *fixture) deleteAt(id string, v int64) response {
	f.t.Helper()
	return f.do(http.MethodDelete, "/api/comments/"+id, "", editAt(v))
}

func commentIDs(cs []commentDTO) []string {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return ids
}

// TestCommentRoundTrip checks that comments, viewed marks and summary made over the API are what
// the session and the Markdown result show.
func TestCommentRoundTrip(t *testing.T) {
	t.Parallel()
	repo := smallRepo(t)
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()

	line3 := targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 3}
	r := f.createComment("c1", line3, "first\r\nsecond")
	f.mustStatus(r, http.StatusOK, "create c1")
	var c commentDTO
	decode(t, r.body, &c)
	if want := (commentDTO{ID: "c1", Author: "human", Body: "first\nsecond", Target: line3, Version: c.Version}); c != want {
		t.Errorf("create c1 replied %+v, want %+v", c, want)
	}
	if got := f.session().Comments; len(got) != 1 || got[0] != c {
		t.Errorf("session comments %+v, want [%+v]", got, c)
	}

	f.mustStatus(f.editComment("c1", "edited"), http.StatusOK, "edit c1")
	old24 := targetDTO{Target: "range", Path: "a.txt", Side: "old", Start: 2, End: 4}
	f.mustStatus(f.createComment("c2", targetDTO{Target: "review"}, "overall"), http.StatusOK, "create c2")
	f.mustStatus(f.createComment("c3", targetDTO{Target: "file", Path: "a.txt"}, "file note"), http.StatusOK, "create c3")
	f.mustStatus(f.createComment("c4", old24, "old lines"), http.StatusOK, "create c4")
	s := f.session()
	if want := []string{"c2", "c3", "c4", "c1"}; !slices.Equal(commentIDs(s.Comments), want) {
		t.Errorf("comment order %v, want %v (review, file, old lines, new line)", commentIDs(s.Comments), want)
	}
	if got := s.Comments[3]; got.Target != line3 || got.Body != "edited" {
		t.Errorf("edited c1 = %+v", got)
	}
	if got := s.Comments[2]; got.Target != old24 {
		t.Errorf("c4 = %+v, want target %+v", got, old24)
	}

	c3 := f.session().version(t, "c3")
	f.mustStatus(f.deleteAt("c3", c3), http.StatusNoContent, "delete c3")
	f.mustStatus(f.deleteAt("c3", c3), http.StatusNoContent, "delete c3 again")
	if got := commentIDs(f.session().Comments); !slices.Equal(got, []string{"c2", "c4", "c1"}) {
		t.Errorf("after delete: %v", got)
	}

	f.mustStatus(f.do(http.MethodPut, "/api/viewed/0", `{"viewed":true}`), http.StatusNoContent, "mark viewed")
	if got := f.session().Viewed; !slices.Equal(got, []int{0}) {
		t.Errorf("viewed %v, want [0]", got)
	}
	f.mustStatus(f.do(http.MethodPut, "/api/viewed/0", `{"viewed":false}`), http.StatusNoContent, "clear viewed")
	if got := f.session().Viewed; len(got) != 0 {
		t.Errorf("viewed %v after clearing", got)
	}
	f.mustStatus(f.do(http.MethodPut, "/api/viewed/1", `{"viewed":true}`), http.StatusNotFound, "viewed out of range")

	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"a\r\nb"}`), http.StatusNoContent, "summary")
	if got := f.session().Summary; got != "a\nb" {
		t.Errorf("summary %q, want CRLF normalised to %q", got, "a\nb")
	}

	r = f.do(http.MethodGet, "/api/result.md", "")
	f.mustStatus(r, http.StatusOK, "result")
	// The exact bytes are the review package's and the process test's; here the result must
	// reflect the draft: the summary and the comments in result order.
	md := string(r.body)
	if n := strings.Count("\n"+md, "\n# Review\n"); n != 1 {
		t.Errorf("result.md has %d title headings:\n%s", n, md)
	}
	markers := []string{"\n## Summary\n", "\n> a\n> b\n", "\n### Review\n", "\n> overall\n",
		"\n### `a.txt`:2-4 (old)\n", "\nline 2\nline 3\nline 4\n", "\n> old lines\n", "\nline three\n", "\n> edited\n"}
	at := 0
	for _, m := range markers {
		k := strings.Index(md[at:], m)
		if k < 0 {
			t.Errorf("result.md lacks %q after offset %d:\n%s", m, at, md)
			break
		}
		at += k + len(m) - 1
	}
	if strings.Contains(md, "file note") {
		t.Errorf("result.md still holds the deleted comment:\n%s", md)
	}
	if n := f.delivered.count(); n != 0 {
		t.Errorf("reading the result delivered it %d times", n)
	}
}

// TestInvalidMutationsLeaveStateUnchanged checks that each rejected request leaves the session and
// the draft as they were: a malformed request is 400, a well-formed one the review refuses is 422.
func TestInvalidMutationsLeaveStateUnchanged(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("a.txt", numberedLines("line", 5))
	repo.commit("base")
	repo.write("a.txt", strings.Replace(numberedLines("line", 5), "line 3\n", "line three\n", 1))
	repo.write("added.txt", "added\n")
	repo.write("c\td.txt", "not commentable\n")
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	files := map[string]int{}
	for i := range f.snap.FileCount() {
		files[f.snap.File(i).Path()] = i
	}
	a, added, control := files["a.txt"], files["added.txt"], files["c\td.txt"]
	line3 := placementDTO{Target: "line", File: &a, Side: "new", Line: 3}
	f.mustStatus(f.create("c1", line3, "keep me"), http.StatusOK, "create c1")
	f.mustStatus(f.do(http.MethodPut, "/api/summary", `{"summary":"s"}`), http.StatusNoContent, "summary")
	version := f.session().version(t, "c1")
	before := f.session()
	draft := func() string {
		data, err := os.ReadFile(f.store.Path())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	beforeDraft := draft()

	at := func(i int) *int { return &i }
	create := func(id string, target any, body string) string {
		return "/api/comments/" + id + "\x00" + mustJSON(t, map[string]any{"target": target, "body": body})
	}
	edit := "/api/comments/c1\x00"
	for _, c := range []struct {
		name string
		req  string // path NUL body, for PUT
		want int
	}{
		{"whitespace body", create("c2", line3, " \n\t "), http.StatusUnprocessableEntity},
		{"empty edit of c1", edit + `{"body":""}`, http.StatusUnprocessableEntity},
		{"no-break space body", create("c2", line3, "\u00a0"), http.StatusUnprocessableEntity},
		{"next-line body", create("c2", line3, "\u0085"), http.StatusUnprocessableEntity},
		{"no-break space edit of c1", edit + mustJSON(t, map[string]string{"body": "\u00a0"}), http.StatusUnprocessableEntity},
		{"next-line edit of c1", edit + mustJSON(t, map[string]string{"body": " \u0085\n"}), http.StatusUnprocessableEntity},
		{"body over 64 KiB", create("c2", line3, strings.Repeat("x", review.MaxBodyBytes+1)), http.StatusUnprocessableEntity},
		{"invalid id", create("bad.id", line3, "b"), http.StatusUnprocessableEntity},
		{"id over 64 characters", create(strings.Repeat("i", 65), line3, "b"), http.StatusUnprocessableEntity},
		// Malformed requests.
		{"missing target", "/api/comments/c2\x00" + `{"body":"b"}`, http.StatusBadRequest},
		{"unknown target kind", create("c2", placementDTO{Target: "hunk"}, "b"), http.StatusBadRequest},
		{"file as a string", create("c2", map[string]any{"target": "file", "file": "0"}, "b"), http.StatusBadRequest},
		{"line as a string", create("c2", map[string]any{"target": "line", "file": a, "side": "new", "line": "3"}, "b"), http.StatusBadRequest},
		{"side as a number", create("c2", map[string]any{"target": "line", "file": a, "side": 1, "line": 3}, "b"), http.StatusBadRequest},
		{"a path, not a file index", create("c2", map[string]any{"target": "file", "path": "a.txt"}, "b"), http.StatusBadRequest},
		{"unknown member in target", create("c2", map[string]any{"target": "review", "colour": "red"}, "b"), http.StatusBadRequest},
		{"unknown request member", "/api/comments/c2\x00" + `{"target":{"target":"review"},"body":"b","author":"agent"}`, http.StatusBadRequest},
		{"an edit naming a target", edit + `{"target":{"target":"review"},"body":"moved"}`, http.StatusBadRequest},
		// Placements with members their kind does not take.
		{"review target with a file: a review takes no file", create("c2", placementDTO{Target: "review", File: &a}, "b"), http.StatusBadRequest},
		{"file target without a file: a file target needs one", create("c2", placementDTO{Target: "file"}, "b"), http.StatusBadRequest},
		{"line target without a file: a line target needs one", create("c2", placementDTO{Target: "line", Side: "new", Line: 1}, "b"), http.StatusBadRequest},
		{"range target without a file: a range target needs one", create("c2", placementDTO{Target: "range", Side: "new", Start: 1, End: 2}, "b"), http.StatusBadRequest},
		{"file target with a side: a file target takes none", create("c2", placementDTO{Target: "file", File: &a, Side: "new"}, "b"), http.StatusBadRequest},
		{"line target with start and end: those are a range's members", create("c2", placementDTO{Target: "line", File: &a, Side: "new", Line: 1, Start: 1, End: 2}, "b"), http.StatusBadRequest},
		// Placements the review refuses.
		{"negative file index", create("c2", placementDTO{Target: "file", File: at(-2)}, "b"), http.StatusUnprocessableEntity},
		{"file index past the files", create("c2", placementDTO{Target: "file", File: at(len(files))}, "b"), http.StatusUnprocessableEntity},
		{"unknown side", create("c2", placementDTO{Target: "line", File: &a, Side: "middle", Line: 1}, "b"), http.StatusUnprocessableEntity},
		{"a side the file lacks", create("c2", placementDTO{Target: "line", File: &added, Side: "old", Line: 1}, "b"), http.StatusUnprocessableEntity},
		{"line zero", create("c2", map[string]any{"target": "line", "file": a, "side": "new", "line": 0}, "b"), http.StatusUnprocessableEntity},
		{"line beyond the file", create("c2", placementDTO{Target: "line", File: &a, Side: "new", Line: 6}, "b"), http.StatusUnprocessableEntity},
		{"range start equals end", create("c2", placementDTO{Target: "range", File: &a, Side: "new", Start: 2, End: 2}, "b"), http.StatusUnprocessableEntity},
		{"range start after end", create("c2", placementDTO{Target: "range", File: &a, Side: "new", Start: 4, End: 2}, "b"), http.StatusUnprocessableEntity},
		{"range end beyond the file", create("c2", placementDTO{Target: "range", File: &a, Side: "old", Start: 4, End: 6}, "b"), http.StatusUnprocessableEntity},
		{"a file that is not commentable", create("c2", placementDTO{Target: "file", File: &control}, "b"), http.StatusUnprocessableEntity},
		{"summary over 64 KiB", "/api/summary\x00" + mustJSON(t, map[string]string{"summary": strings.Repeat("s", review.MaxSummaryByte+1)}), http.StatusUnprocessableEntity},
	} {
		path, body, _ := strings.Cut(c.req, "\x00")
		var opts []reqOption
		switch {
		case path == "/api/comments/c1":
			opts = append(opts, editAt(version))
		case strings.HasPrefix(path, "/api/comments/"):
			opts = append(opts, createOnly)
		}
		r := f.do(http.MethodPut, path, body, opts...)
		if r.status != c.want {
			t.Errorf("%s: status %d, want %d; body %q", c.name, r.status, c.want, r.body)
		}
	}
	if after := f.session(); mustJSON(t, after) != mustJSON(t, before) {
		t.Errorf("rejected requests changed the session:\nbefore %+v\nafter  %+v", before, after)
	}
	if after := draft(); after != beforeDraft {
		t.Errorf("rejected requests changed the draft:\nbefore %s\nafter  %s", beforeDraft, after)
	}
}

// TestAuthorshipOverAPI covers authorship at the API: the server, not the client, sets the author,
// and only a changed body makes an agent comment the human's.
func TestAuthorshipOverAPI(t *testing.T) {
	t.Parallel()
	seed := `{"comments":[{"target":"line","path":"a.txt","side":"new","line":3,"body":"seeded\n"}]}`
	f := newFixture(t, smallRepo(t), fixtureOptions{seed: seed})
	f.serveHandler()
	s := f.session()
	if len(s.Comments) != 1 || s.Comments[0].Author != "agent" {
		t.Fatalf("seeded comments %+v, want one agent comment", s.Comments)
	}
	id := s.Comments[0].ID

	r := f.editComment(id, "seeded\r\n")
	f.mustStatus(r, http.StatusOK, "save unchanged")
	if got := f.session().Comments[0].Author; got != "agent" {
		t.Errorf("saving the body unchanged (CRLF) made the author %q", got)
	}
	f.mustStatus(f.editComment(id, "seeded, and edited"), http.StatusOK, "edit")
	if got := f.session().Comments[0].Author; got != "human" {
		t.Errorf("editing the body left the author %q", got)
	}
}

// TestCommentableAgreesWithValidation checks that a file accepts file and line comments only when
// every path it has is commentable, so a rename from or to a path with a control character, and a
// path that is not UTF-8, take review comments only. The session's commentable flag must state the
// same rule the server enforces on each side and on the file itself.
func TestCommentableAgreesWithValidation(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	body := numberedLines("line", 5)
	repo.write("a\tb.txt", body)
	repo.write("clean.txt", numberedLines("clean", 5))
	repo.write("del\tme.txt", "gone\n")
	repo.commit("base")
	repo.remove("a\tb.txt")
	repo.write("ab.txt", body)
	repo.remove("clean.txt")
	repo.write("x\ty.txt", numberedLines("clean", 5))
	repo.remove("del\tme.txt")
	repo.write("c\td.txt", "added\n")
	repo.write("plain.txt", "plain\n")
	repo.git("add", "-A")
	// APFS rejects a path that is not UTF-8, so it is staged without a worktree file.
	src := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(src, []byte("not utf-8 path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := repo.git("hash-object", "-w", src)
	repo.git("update-index", "--add", "--cacheinfo", "100644,"+id+",bad\xff.txt")
	repo.git("commit", "-q", "-m", "head")
	want := map[[2]string]bool{
		{"a\tb.txt", "ab.txt"}:    false,
		{"clean.txt", "x\ty.txt"}: false,
		{"del\tme.txt", ""}:       false,
		{"", "c\td.txt"}:          false,
		{"", "bad\xff.txt"}:       false,
		{"", "plain.txt"}:         true,
	}
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	s := f.session()

	if len(s.Files) != len(want) {
		t.Fatalf("%d changed files, want %d", len(s.Files), len(want))
	}
	n := 0
	for i, fl := range s.Files {
		raw := f.snap.File(i)
		key := [2]string{raw.OldPath, raw.NewPath}
		if w, ok := want[key]; !ok {
			t.Errorf("unexpected changed file %q", key)
		} else if fl.Commentable != w {
			t.Errorf("file %q: commentable %v, want %v", key, fl.Commentable, w)
		}
		var targets []placementDTO
		if raw.OldPath != "" {
			targets = append(targets, placementDTO{Target: "line", File: &i, Side: "old", Line: 1})
		}
		if raw.NewPath != "" {
			targets = append(targets, placementDTO{Target: "line", File: &i, Side: "new", Line: 1})
		}
		targets = append(targets, placementDTO{Target: "file", File: &i})
		for _, tg := range targets {
			n++
			r := f.create(fmt.Sprintf("k%d", n), tg, "note")
			accepted := r.status == http.StatusOK
			if accepted != fl.Commentable {
				t.Errorf("file %q -> %q: commentable %v, but a %s comment on the %q side was accepted=%v (status %d)",
					raw.OldPath, raw.NewPath, fl.Commentable, tg.Target, tg.Side, accepted, r.status)
			}
		}
	}
}

// orderedCommentDTO is a comment with the members that place it: the changed-file index its
// target names (null for a review comment) and its opaque sort key.
type orderedCommentDTO struct {
	File   *int      `json:"file"`
	ID     string    `json:"id"`
	Order  []int64   `json:"order"`
	Target targetDTO `json:"target"`
}

func (f *fixture) orderedComments() []orderedCommentDTO {
	f.t.Helper()
	r := f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(r, http.StatusOK, "GET /api/session")
	var s struct {
		Comments []orderedCommentDTO `json:"comments"`
	}
	decode(f.t, r.body, &s)
	return s.Comments
}

// orderRepo's last commit modifies a.txt, renames b_old.txt to b_new.txt with a change, and
// deletes c.txt, so its files are a.txt, b_new.txt and c.txt in that order.
func orderRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.write("a.txt", numberedLines("a", 10))
	r.write("b_old.txt", numberedLines("b", 10))
	r.write("c.txt", numberedLines("c", 5))
	r.commit("base")
	r.write("a.txt", strings.Replace(numberedLines("a", 10), "a 5\n", "a five\n", 1))
	r.remove("b_old.txt")
	r.write("b_new.txt", strings.Replace(numberedLines("b", 10), "b 9\n", "b nine\n", 1))
	r.remove("c.txt")
	r.commit("head")
	return r
}

// TestCommentOrderAndFileIndex covers comment order at the API: the session lists comments in
// result order (review comments, then file order, file comments first, old side before new, start
// line, creation), each names its changed file by index, its sort key increases along the list and
// a body edit does not move it. The Markdown result uses the same order.
func TestCommentOrderAndFileIndex(t *testing.T) {
	t.Parallel()
	f := newFixture(t, orderRepo(t), fixtureOptions{})
	f.serveHandler()
	s := f.session()
	var files [][2]string
	for _, fl := range s.Files {
		files = append(files, [2]string{fl.OldPath, fl.NewPath})
	}
	if want := [][2]string{{"a.txt", "a.txt"}, {"b_old.txt", "b_new.txt"}, {"c.txt", ""}}; !slices.Equal(files, want) {
		t.Fatalf("changed files %q, want %q", files, want)
	}

	line := func(path, side string, n int) targetDTO {
		return targetDTO{Target: "line", Path: path, Side: side, Line: n}
	}
	created := []struct {
		id     string
		target targetDTO
	}{
		{"a-new7", line("a.txt", "new", 7)},
		{"c-file", targetDTO{Target: "file", Path: "c.txt"}},
		{"a-old5", line("a.txt", "old", 5)},
		{"b-new9", line("b_new.txt", "new", 9)},
		{"review-first", targetDTO{Target: "review"}},
		{"a-file", targetDTO{Target: "file", Path: "a.txt"}},
		{"b-old2", targetDTO{Target: "range", Path: "b_old.txt", Side: "old", Start: 2, End: 4}},
		{"a-new2", line("a.txt", "new", 2)},
		{"a-new7-later", line("a.txt", "new", 7)},
		{"c-old1", line("c.txt", "old", 1)},
		{"review-later", targetDTO{Target: "review"}},
	}
	replies := map[string]orderedCommentDTO{}
	for _, c := range created {
		r := f.createComment(c.id, c.target, "body of "+c.id)
		f.mustStatus(r, http.StatusOK, "create "+c.id)
		var got orderedCommentDTO
		decode(t, r.body, &got)
		replies[c.id] = got
	}
	before := f.orderedComments()
	f.mustStatus(f.editComment("a-new7", "edited body of a-new7"), http.StatusOK, "edit a-new7")
	after := f.orderedComments()

	want := []string{
		"review-first", "review-later",
		"a-file", "a-old5", "a-new2", "a-new7", "a-new7-later",
		"b-old2", "b-new9",
		"c-file", "c-old1",
	}
	var ids []string
	for _, c := range after {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("session comment order %v, want %v", ids, want)
	}
	if mustJSON(t, before) != mustJSON(t, after) {
		t.Errorf("a body edit changed the comments' placement:\nbefore %+v\nafter  %+v", before, after)
	}
	for k, c := range after {
		if k > 0 && slices.Compare(after[k-1].Order, c.Order) >= 0 {
			t.Errorf("order of %s %v does not follow %s %v", c.ID, c.Order, after[k-1].ID, after[k-1].Order)
		}
		if r := replies[c.ID]; !slices.Equal(r.Order, c.Order) || mustJSON(t, r.File) != mustJSON(t, c.File) {
			t.Errorf("%s: create replied file %v order %v; the session has file %v order %v",
				c.ID, r.File, r.Order, c.File, c.Order)
		}
		if c.Target.Target == "review" {
			if c.File != nil {
				t.Errorf("review comment %s names file %d", c.ID, *c.File)
			}
			continue
		}
		if c.File == nil || *c.File < 0 || *c.File >= len(s.Files) {
			t.Errorf("%s names file %v of %d", c.ID, c.File, len(s.Files))
			continue
		}
		fl := s.Files[*c.File]
		var named string
		switch {
		case c.Target.Target == "file":
			named = fl.NewPath
			if named == "" {
				named = fl.OldPath
			}
		case c.Target.Side == "old":
			named = fl.OldPath
		default:
			named = fl.NewPath
		}
		if named != c.Target.Path {
			t.Errorf("%s targets %q but names file %d (%q -> %q)", c.ID, c.Target.Path, *c.File, fl.OldPath, fl.NewPath)
		}
	}

	r := f.do(http.MethodGet, "/api/result.md", "")
	f.mustStatus(r, http.StatusOK, "result.md")
	md := string(r.body)
	at := -1
	for _, id := range want {
		body := "body of " + id
		if id == "a-new7" {
			body = "edited body of a-new7"
		}
		// "body of a-new7" is a prefix of "body of a-new7-later": match the whole quoted line.
		k := strings.Index(md, "\n> "+body+"\n")
		if k < 0 {
			t.Errorf("result.md lacks %q:\n%s", body, md)
			continue
		}
		if k <= at {
			t.Errorf("result.md places %s out of order:\n%s", id, md)
		}
		at = k
	}
}

// TestCommentPreconditions covers the create and edit preconditions of PUT /api/comments: a
// create never overwrites, an edit names the version it loaded, so it never resurrects a deleted
// comment nor overwrites another writer's text, and a request that says neither or both, or names
// no version, is refused. A write whose result is already stored, as when a client resends a
// request whose reply was lost, answers 200 with the stored comment; 412 means the stored comment
// differs. A refused request leaves the draft unchanged. TestDeletePreconditions covers deletes.
func TestCommentPreconditions(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	review := placementDTO{Target: "review"}
	bodies := func() map[string]string {
		out := map[string]string{}
		for _, c := range f.session().Comments {
			out[c.ID] = c.Body
		}
		return out
	}

	f.mustStatus(f.edit("c1", 1, "edit before create"), http.StatusPreconditionFailed, "edit of a comment never created")
	r := f.create("c1", review, "c1-original-body")
	f.mustStatus(r, http.StatusOK, "create c1")
	var created commentDTO
	decode(t, r.body, &created)
	if v := f.session().version(t, "c1"); v != created.Version {
		t.Errorf("the session reports c1 at version %d; the create replied %d", v, created.Version)
	}
	r = f.create("c1", review, "c1-original-body")
	f.mustStatus(r, http.StatusOK, "create replayed")
	var replayed commentDTO
	decode(t, r.body, &replayed)
	if !reflect.DeepEqual(replayed, created) {
		t.Errorf("a replayed create replied %+v; want the first reply %+v", replayed, created)
	}
	f.mustStatus(f.create("c1", review, "overwritten"), http.StatusPreconditionFailed, "create of an existing id")
	createBody := putCommentBody(t, review, "overwritten")
	editBody := `{"body":"overwritten"}`
	for _, c := range []struct {
		name string
		body string
		opts []reqOption
		want int
	}{
		{"no precondition", editBody, nil, http.StatusPreconditionRequired},
		{"both preconditions", editBody, []reqOption{createOnly, editAt(created.Version)}, http.StatusBadRequest},
		{"If-Match: *", editBody, []reqOption{withHeader("If-Match", "*")}, http.StatusBadRequest},
		{"If-Match: an unquoted version", editBody, []reqOption{withHeader("If-Match", fmt.Sprint(created.Version))}, http.StatusBadRequest},
		{`If-Match: "abc"`, editBody, []reqOption{withHeader("If-Match", `"abc"`)}, http.StatusBadRequest},
		{`If-Match: "-1"`, editBody, []reqOption{withHeader("If-Match", `"-1"`)}, http.StatusBadRequest},
		{"If-None-Match naming a version", createBody, []reqOption{withHeader("If-None-Match", `"1"`)}, http.StatusBadRequest},
	} {
		f.mustStatus(f.do(http.MethodPut, "/api/comments/c1", c.body, c.opts...), c.want, c.name)
	}
	if got := bodies(); len(got) != 1 || got["c1"] != "c1-original-body" {
		t.Errorf("comments %v after refused writes, want c1 = c1-original-body", got)
	}

	// An edit that changes the body makes a new version. One whose body is already stored answers
	// the stored comment, at any version; one that differs from the stored body at an old version
	// is stale.
	r = f.edit("c1", created.Version, "edited")
	f.mustStatus(r, http.StatusOK, "edit at the created version")
	var edited commentDTO
	decode(t, r.body, &edited)
	if edited.Version == created.Version {
		t.Errorf("an edit that changed the body kept version %d", edited.Version)
	}
	for name, v := range map[string]int64{"the edited version": edited.Version, "the created version": created.Version} {
		r = f.edit("c1", v, "edited")
		f.mustStatus(r, http.StatusOK, "the stored body saved again at "+name)
		var same commentDTO
		decode(t, r.body, &same)
		if !reflect.DeepEqual(same, edited) {
			t.Errorf("the stored body saved again at %s replied %+v; want %+v", name, same, edited)
		}
	}
	same := edited
	f.mustStatus(f.edit("c1", created.Version, "replayed"), http.StatusPreconditionFailed, "a different body at the created version")
	if got := bodies(); got["c1"] != "edited" {
		t.Errorf("c1 = %q after stale edits, want edited", got["c1"])
	}

	f.mustStatus(f.deleteAt("c1", edited.Version), http.StatusNoContent, "delete c1")
	f.mustStatus(f.edit("c1", same.Version, "resurrected"), http.StatusPreconditionFailed, "edit after delete")
	if got := bodies(); len(got) != 0 {
		t.Errorf("comments %v after an edit of a deleted comment, want none", got)
	}
	draft, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(draft), "resurrected") || strings.Contains(string(draft), "edited") {
		t.Errorf("the draft holds the deleted comment or the refused edit: %s", draft)
	}
	r = f.do(http.MethodGet, "/api/result.md", "")
	f.mustStatus(r, http.StatusOK, "result.md")
	if md := string(r.body); strings.Contains(md, "resurrected") || strings.Contains(md, "edited") {
		t.Errorf("result.md holds the deleted comment:\n%s", md)
	}

	// The same id created again is a new comment: the deleted comment's editor cannot write it.
	f.mustStatus(f.create("c1", review, "again"), http.StatusOK, "create c1 again")
	f.mustStatus(f.edit("c1", same.Version, "old editor"), http.StatusPreconditionFailed, "edit at the deleted comment's version")
	if got := bodies(); len(got) != 1 || got["c1"] != "again" {
		t.Errorf("comments %v, want c1 = again", got)
	}
}

// TestDeletePreconditions: DELETE /api/comments names the version the page shows in If-Match, so a
// page that has not seen another writer's edit cannot discard it (412), and a request without a
// version, or with a malformed one, is refused (428, 400). Deleting an absent comment answers 204,
// so a resent delete succeeds. A refused delete leaves the comment in the session, the draft and
// the result (Q-27).
func TestDeletePreconditions(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	review := placementDTO{Target: "review"}
	stored := func(want string) {
		t.Helper()
		var bodies []string
		for _, c := range f.session().Comments {
			bodies = append(bodies, c.Body)
		}
		if want == "" {
			if len(bodies) != 0 {
				t.Errorf("session comments %q, want none", bodies)
			}
		} else if len(bodies) != 1 || bodies[0] != want {
			t.Errorf("session comments %q, want [%q]", bodies, want)
		}
		draft, err := os.ReadFile(f.store.Path())
		if err != nil {
			t.Fatal(err)
		}
		r := f.do(http.MethodGet, "/api/result.md", "")
		f.mustStatus(r, http.StatusOK, "result.md")
		if want != "" && (!strings.Contains(string(draft), want) || !strings.Contains(string(r.body), want)) {
			t.Errorf("the draft or result.md lacks %q:\n%s\n%s", want, draft, r.body)
		}
		for _, gone := range []string{"body-created", "body-edited"} {
			if gone != want && (strings.Contains(string(draft), gone) || strings.Contains(string(r.body), gone)) {
				t.Errorf("the draft or result.md still holds %q:\n%s\n%s", gone, draft, r.body)
			}
		}
	}

	r := f.create("c1", review, "body-created")
	f.mustStatus(r, http.StatusOK, "create c1")
	var created commentDTO
	decode(t, r.body, &created)
	for _, c := range []struct {
		name, id string
		opts     []reqOption
		want     int
	}{
		{"no If-Match", "c1", nil, http.StatusPreconditionRequired},
		{"no If-Match on an unknown id", "nope", nil, http.StatusPreconditionRequired},
		{"If-Match: *", "c1", []reqOption{withHeader("If-Match", "*")}, http.StatusBadRequest},
		{"If-Match: an unquoted version", "c1", []reqOption{withHeader("If-Match", fmt.Sprint(created.Version))}, http.StatusBadRequest},
		{`If-Match: "abc"`, "c1", []reqOption{withHeader("If-Match", `"abc"`)}, http.StatusBadRequest},
		{"a weak If-Match", "c1", []reqOption{withHeader("If-Match", fmt.Sprintf(`W/"%d"`, created.Version))}, http.StatusBadRequest},
		{"a version never assigned", "c1", []reqOption{editAt(created.Version + 1000)}, http.StatusPreconditionFailed},
	} {
		f.mustStatus(f.do(http.MethodDelete, "/api/comments/"+c.id, "", c.opts...), c.want, c.name)
	}
	stored("body-created")

	r = f.edit("c1", created.Version, "body-edited")
	f.mustStatus(r, http.StatusOK, "edit c1")
	var edited commentDTO
	decode(t, r.body, &edited)
	f.mustStatus(f.deleteAt("c1", created.Version), http.StatusPreconditionFailed, "delete at the version before the edit")
	stored("body-edited")

	f.mustStatus(f.deleteAt("c1", edited.Version), http.StatusNoContent, "delete at the current version")
	stored("")
	f.mustStatus(f.deleteAt("c1", edited.Version), http.StatusNoContent, "delete resent")
	f.mustStatus(f.deleteAt("nope", 7), http.StatusNoContent, "delete of an id never used")

	f.mustStatus(f.create("c1", review, "body-again"), http.StatusOK, "create c1 again")
	f.mustStatus(f.deleteAt("c1", edited.Version), http.StatusPreconditionFailed, "delete at the deleted comment's version")
	stored("body-again")
}

// TestConcurrentEditsOfOneVersion: two edits holding the same version race; one wins with 200, the
// other is refused with 412, and the session holds the winner's body.
func TestConcurrentEditsOfOneVersion(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.create("c1", placementDTO{Target: "review"}, "start"), http.StatusOK, "create c1")
	v := f.session().version(t, "c1")
	var statuses [2]int
	var wg sync.WaitGroup
	for k := range statuses {
		wg.Go(func() {
			r, err := f.send(http.MethodPut, "/api/comments/c1", mustJSON(t, map[string]string{"body": fmt.Sprint("writer ", k)}), editAt(v))
			if err != nil {
				t.Error(err)
				return
			}
			statuses[k] = r.status
		})
	}
	wg.Wait()
	winner := slices.Index(statuses[:], http.StatusOK)
	if winner < 0 || statuses[1-winner] != http.StatusPreconditionFailed {
		t.Fatalf("statuses %v, want one 200 and one 412", statuses)
	}
	if got := f.session().Comments[0].Body; got != fmt.Sprint("writer ", winner) {
		t.Errorf("body %q, want the winner's, writer %d", got, winner)
	}
}

// TestQuotes: the Finish dialog's quotes are the result's quote of each requested line or range
// comment, keyed by ID, with the result's note on what is left out; other targets and unknown IDs
// are left out, and the ID count is bounded by the limit the session states.
func TestQuotes(t *testing.T) {
	t.Parallel()
	r := newTestRepo(t)
	r.write("a.txt", numberedLines("line", 5))
	r.commit("base")
	r.write("a.txt", strings.Replace(numberedLines("line", 5), "line 3\n", "line three\n", 1))
	// 25 lines, the first 600 bytes long: a quote of all of them leaves out 5 lines and 100 bytes.
	r.write("b.txt", strings.Repeat("x", 600)+"\n"+numberedLines("b", 24))
	r.commit("head")
	f := newFixture(t, r, fixtureOptions{})
	f.serveHandler()
	f.mustStatus(f.createComment("c1", targetDTO{Target: "range", Path: "a.txt", Side: "old", Start: 2, End: 4}, "x"), http.StatusOK, "c1")
	f.mustStatus(f.createComment("c2", targetDTO{Target: "line", Path: "a.txt", Side: "new", Line: 3}, "x"), http.StatusOK, "c2")
	f.mustStatus(f.createComment("c3", targetDTO{Target: "review"}, "x"), http.StatusOK, "c3")
	f.mustStatus(f.createComment("c4", targetDTO{Target: "range", Path: "b.txt", Side: "new", Start: 1, End: 25}, "x"), http.StatusOK, "c4")

	resp := f.do(http.MethodGet, "/api/quotes?id=c1&id=c2&id=c3&id=c4&id=gone", "")
	f.mustStatus(resp, http.StatusOK, "quotes")
	type quoteDTO struct {
		Code []string `json:"code"`
		Note string   `json:"note"`
	}
	var got map[string]quoteDTO
	decode(t, resp.body, &got)
	wantNote := "… 5 more lines; 100 bytes cut from long lines not shown"
	if c4 := got["c4"]; len(c4.Code) != 20 || len(c4.Code[0]) != 500 || c4.Note != wantNote {
		t.Errorf("c4 quote: %d lines, the first %d bytes, note %q; want 20 lines, 500 bytes, note %q",
			len(c4.Code), len(c4.Code[0]), c4.Note, wantNote)
	}
	delete(got, "c4")
	want := map[string]quoteDTO{
		"c1": {Code: []string{"line 2", "line 3", "line 4"}},
		"c2": {Code: []string{"line three"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("quotes = %+v, want %+v", got, want)
	}
	// The dialog shows what the agent receives: the same note is in the result.
	md := f.do(http.MethodGet, "/api/result.md", "")
	f.mustStatus(md, http.StatusOK, "result")
	if !strings.Contains(string(md.body), "\n"+wantNote+"\n") {
		t.Errorf("result does not hold the note %q:\n%s", wantNote, md.body)
	}

	var sess struct {
		Quotes struct {
			MaxLines int `json:"max_lines"`
			MaxIDs   int `json:"max_ids"`
		} `json:"quotes"`
	}
	sr := f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(sr, http.StatusOK, "session")
	decode(t, sr.body, &sess)
	if sess.Quotes.MaxLines != 20 {
		t.Errorf("session quotes.max_lines = %d; want 20", sess.Quotes.MaxLines)
	}
	limit := sess.Quotes.MaxIDs
	if limit < 1 {
		t.Fatalf("session quotes.max_ids = %d", limit)
	}
	f.mustStatus(f.do(http.MethodGet, "/api/quotes?id=c1"+strings.Repeat("&id=c1", limit-1), ""), http.StatusOK, "max_ids ids")
	f.mustStatus(f.do(http.MethodGet, "/api/quotes?id=c1"+strings.Repeat("&id=c1", limit), ""), http.StatusBadRequest, "max_ids+1 ids")
	f.mustStatus(f.do(http.MethodGet, "/api/quotes", ""), http.StatusBadRequest, "no ids")
}

// TestRenamedFileTargetPaths: a create names a renamed file by index; the reply's path is the
// file's path on the target's side, the old path for the old side and the new path otherwise.
func TestRenamedFileTargetPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t, orderRepo(t), fixtureOptions{})
	f.serveHandler()
	renamed := 1 // b_old.txt -> b_new.txt
	for _, c := range []struct {
		id   string
		at   placementDTO
		want string
	}{
		{"old", placementDTO{Target: "line", File: &renamed, Side: "old", Line: 2}, "b_old.txt"},
		{"new", placementDTO{Target: "range", File: &renamed, Side: "new", Start: 8, End: 9}, "b_new.txt"},
		{"file", placementDTO{Target: "file", File: &renamed}, "b_new.txt"},
	} {
		r := f.create(c.id, c.at, "x")
		f.mustStatus(r, http.StatusOK, "create "+c.id)
		var got commentDTO
		decode(t, r.body, &got)
		if got.Target.Path != c.want {
			t.Errorf("%s: target path %q, want %q", c.id, got.Target.Path, c.want)
		}
	}
}
