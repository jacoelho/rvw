package server_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// searchDTO is the /api/search reply. It decodes strictly: a member not named here fails the test.
type searchDTO struct {
	Matches   []matchDTO `json:"matches"`
	Truncated bool       `json:"truncated"`
}

type matchDTO struct {
	Side     string `json:"side"`
	Before   string `json:"before"`
	Match    string `json:"match"`
	After    string `json:"after"`
	File     int    `json:"file"`
	Line     int    `json:"line"`
	CutStart bool   `json:"cut_start"`
	CutEnd   bool   `json:"cut_end"`
}

func decodeSearch(t *testing.T, body []byte) searchDTO {
	t.Helper()
	var s searchDTO
	if err := json.Unmarshal(body, &s, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return s
}

// withContext sends the request under ctx, so cancelling ctx disconnects the client.
func withContext(ctx context.Context) reqOption {
	return func(r *http.Request) { *r = *r.WithContext(ctx) }
}

// uniquePath names a changed file whose raw path must never appear in a reply.
const uniquePath = "only-here-5d1c.txt"

// searchRepo changes a modified file, a renamed file and a deleted file, each holding "zebra".
// The deleted file holds non-UTF-8 text and a NUL past git's binary check. A file holding "zebra"
// changes only its mode, so it is not searched.
func searchRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.write(uniquePath, "one\ntwo\n")
	r.write("ren_old.txt", numberedLines("kept", 20)+"a zebra here\n")
	pad := strings.Repeat("padding line\n", 1000) // over git's 8000-byte binary check
	r.write("del.txt", pad+"bad \xff zebra \xfe\nnul\x00zebra\n")
	r.write("mode.sh", "zebra mode\n")
	r.commit("base")
	r.write(uniquePath, "one\n  before zebra after\nZEBRA upper\n")
	r.remove("ren_old.txt")
	r.write("ren_new.txt", numberedLines("kept", 20)+"a zebra here\n")
	r.remove("del.txt")
	if err := os.Chmod(filepath.Join(r.dir, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.commit("head")
	return r
}

func TestSearchReply(t *testing.T) {
	t.Parallel()
	f := newFixture(t, searchRepo(t), fixtureOptions{})
	f.serveHandler()
	s := f.session()
	mod, ren, del := s.fileIndex(t, uniquePath), s.fileIndex(t, "ren_new.txt"), s.fileIndex(t, "del.txt")
	if s.Files[ren].Status != "R" {
		t.Fatalf("ren_new.txt has status %q, want R", s.Files[ren].Status)
	}

	r := f.do(http.MethodGet, "/api/search?q=zebra", "")
	f.mustStatus(r, http.StatusOK, "search")
	if bytes.Contains(r.body, []byte(uniquePath)) || bytes.Contains(r.body, []byte("ren_new")) {
		t.Errorf("reply holds a raw path: %s", r.body)
	}
	got := decodeSearch(t, r.body)
	want := []matchDTO{
		{File: mod, Side: "new", Line: 2, Before: "  before ", Match: "zebra", After: " after"},
		{File: mod, Side: "new", Line: 3, Before: "", Match: "ZEBRA", After: " upper"},
		{File: ren, Side: "new", Line: 21, Before: "a ", Match: "zebra", After: " here"},
		{File: del, Side: "old", Line: 1001, Before: "bad � ", Match: "zebra", After: " �"},
		{File: del, Side: "old", Line: 1002, Before: "nul\x00", Match: "zebra", After: ""},
	}
	// The reply is ordered by file index then line.
	slices.SortFunc(want, func(a, b matchDTO) int { return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line)) })
	if got.Truncated {
		t.Error("truncated, want complete")
	}
	if len(got.Matches) != len(want) {
		t.Fatalf("matches %+v, want %+v", got.Matches, want)
	}
	for k := range want {
		if got.Matches[k] != want[k] {
			t.Errorf("match %d = %+v, want %+v", k, got.Matches[k], want[k])
		}
	}
	if !bytes.Contains(r.body, []byte(`"nul\u0000"`)) {
		t.Errorf("NUL is not escaped as \\u0000: %s", r.body)
	}
}

func TestSearchRejectsInvalidText(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	for name, query := range map[string]string{
		"no q":      "",
		"empty":     "?q=",
		"two runes": "?q=ab",
		"q twice":   "?q=abc&q=abc",
		"257 bytes": "?q=" + strings.Repeat("a", 257),
		"line feed": "?q=ab%0Acd",
		"NUL":       "?q=ab%00cd",
		"CR":        "?q=ab%0Dcd",
		"not UTF-8": "?q=ab%FFcd",
	} {
		r := f.do(http.MethodGet, "/api/search"+query, "")
		if r.status != http.StatusBadRequest || strings.TrimSpace(string(r.body)) == "" {
			t.Errorf("%s: status %d, body %q; want 400 with a reason", name, r.status, r.body)
		}
	}
}

func TestSearchTruncated(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	repo.write("a.txt", "x\n")
	repo.commit("base")
	repo.write("a.txt", numberedLines("zebra", 600))
	repo.commit("head")
	f := newFixture(t, repo, fixtureOptions{})
	f.serveHandler()
	r := f.do(http.MethodGet, "/api/search?q=zebra", "")
	f.mustStatus(r, http.StatusOK, "search")
	got := decodeSearch(t, r.body)
	if !got.Truncated || len(got.Matches) != 500 {
		t.Errorf("truncated %v with %d matches; want truncated with 500 (REQ-NAV-007)", got.Truncated, len(got.Matches))
	}
}

// TestSessionSearchLimitsMatchSearch: the session document gives REQ-NAV-007's length rules for
// search text, and the search endpoint enforces exactly the values it gives, counting characters
// as runes and the maximum as bytes.
func TestSessionSearchLimitsMatchSearch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	var sess struct {
		Search struct {
			MinChars int `json:"min_chars"`
			MaxBytes int `json:"max_bytes"`
		} `json:"search"`
	}
	sr := f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(sr, http.StatusOK, "session")
	decode(t, sr.body, &sess)
	minChars, maxBytes := sess.Search.MinChars, sess.Search.MaxBytes
	if minChars != 3 || maxBytes != 256 {
		t.Fatalf("session search limits = %d, %d; want 3, 256", minChars, maxBytes)
	}
	for _, tc := range []struct {
		name, q string
		status  int
	}{
		{"min_chars-1 three-byte runes", strings.Repeat("€", minChars-1), http.StatusBadRequest},
		{"min_chars three-byte runes", strings.Repeat("€", minChars), http.StatusOK},
		{"max_bytes bytes", strings.Repeat("a", maxBytes), http.StatusOK},
		{"max_bytes+1 bytes", strings.Repeat("a", maxBytes+1), http.StatusBadRequest},
	} {
		f.mustStatus(f.do(http.MethodGet, "/api/search?q="+url.QueryEscape(tc.q), ""), tc.status, tc.name)
	}
}

// holdBothSlots starts two file views that hold the server's two file slots in a gated patch.
func holdBothSlots(t *testing.T, f *fixture) *gatedGit {
	t.Helper()
	g := newGatedGit(t, patchCommands)
	g.hold()
	for range 2 {
		go func() { _, _ = f.send(http.MethodGet, "/api/file?i=0", "") }()
	}
	g.waitHeld(2)
	return g
}

type searchReply struct {
	err error
	r   response
}

func sendAsync(f *fixture, path string, opts ...reqOption) <-chan searchReply {
	ch := make(chan searchReply, 1)
	go func() {
		r, err := f.send(http.MethodGet, path, "", opts...)
		ch <- searchReply{err: err, r: r}
	}()
	return ch
}

func await(t *testing.T, ch <-chan searchReply, what string) response {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("%s: %v", what, got.err)
		}
		return got.r
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no reply", what)
		return response{}
	}
}

// TestSearchWaitsForAFileSlot: a search runs git only once it holds a file slot. It relies on the
// server's two file slots.
func TestSearchWaitsForAFileSlot(t *testing.T) {
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	g := holdBothSlots(t, f)
	searches := newGatedGit(t, searchCommands)
	searches.hold()

	reply := sendAsync(f, "/api/search?q=line")
	time.Sleep(500 * time.Millisecond)
	if n := searches.held(); n != 0 {
		t.Fatalf("%d searches ran while both file slots were held; want none", n)
	}
	searches.release()
	g.release()
	f.mustStatus(await(t, reply, "search after the slots were freed"), http.StatusOK, "search after the slots were freed")
}

// TestInvalidSearchTakesNoSlot: invalid search text is refused before a file slot is taken.
func TestInvalidSearchTakesNoSlot(t *testing.T) {
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	holdBothSlots(t, f)
	r := await(t, sendAsync(f, "/api/search?q=ab"), "invalid search while both slots are held")
	if r.status != http.StatusBadRequest {
		t.Errorf("status %d, want 400", r.status)
	}
}

// TestSearchClientCancelStopsGit: when the client goes away during a search, its git process is
// stopped and its file slot freed. It relies on the server's two file slots.
func TestSearchClientCancelStopsGit(t *testing.T) {
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	searches := newGatedGit(t, searchCommands)
	searches.hold()
	ctx, cancel := context.WithCancel(t.Context())
	replies := []<-chan searchReply{
		sendAsync(f, "/api/search?q=line", withContext(ctx)),
		sendAsync(f, "/api/search?q=line", withContext(ctx)),
	}
	searches.waitHeld(2)
	cancel()
	for _, ch := range replies {
		<-ch
	}
	searches.waitGone()
	// The gate is still held, so only freed slots let a file view through.
	r := await(t, sendAsync(f, "/api/file?i=0"), "file view after the searches were cancelled")
	f.mustStatus(r, http.StatusOK, "file view after the searches were cancelled")
}
