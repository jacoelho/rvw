package server

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jacoelho/rvw/internal/highlight"
	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

type fileJSON struct {
	Status      string `json:"status"`
	OldPath     string `json:"old_path,omitzero"`
	NewPath     string `json:"new_path,omitzero"`
	OldMode     string `json:"old_mode,omitzero"`
	NewMode     string `json:"new_mode,omitzero"`
	VendoredDir string `json:"vendored_dir,omitzero"`
	Commentable bool   `json:"commentable"`
}

// commentJSON is a comment as the UI shows it. File is the changed-file index, null for a review
// comment. Version is the comment's entity tag: an edit sends it back in If-Match. Order is opaque:
// the UI sorts comments by comparing it element by element.
type commentJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	ID      string        `json:"id"`
	Version int64         `json:"version"`
	Author  review.Author `json:"author"`
	Target  targetJSON    `json:"target"`
	Body    string        `json:"body"`
	File    *int          `json:"file"`
	Order   [4]int64      `json:"order"`
}

// targetJSON is a comment's target as the UI shows it. Path is review's choice for the target's
// side; the UI displays it and never sends it back.
type targetJSON struct {
	Kind  review.TargetKind `json:"target"`
	Path  string            `json:"path,omitzero"`
	Side  review.Side       `json:"side,omitzero"`
	Line  int               `json:"line,omitzero"`
	Start int               `json:"start,omitzero"`
	End   int               `json:"end,omitzero"`
}

func toTargetJSON(t review.Target) targetJSON {
	j := targetJSON{Kind: t.Kind(), Path: t.Path(), Side: t.Side()}
	switch start, end := t.Lines(); t.Kind() {
	case review.TargetLine:
		j.Line = start
	case review.TargetRange:
		j.Start, j.End = start, end
	case review.TargetReview, review.TargetFile:
	}
	return j
}

func toCommentJSON(c review.Comment) commentJSON {
	j := commentJSON{
		ID: c.ID, Version: c.Version, Author: c.Author, Target: toTargetJSON(c.Target), Body: c.Body, Order: c.SortKey(),
	}
	if c.File >= 0 {
		j.File = &c.File
	}
	return j
}

type sessionJSON struct {
	Source          string        `json:"source"`
	RenameDetection string        `json:"rename_detection"`
	Files           []fileJSON    `json:"files"`
	Comments        []commentJSON `json:"comments"`
	Summary         string        `json:"summary"`
	Viewed          []int         `json:"viewed"`
	Quotes          quoteLimits   `json:"quotes"`
	Search          searchLimits  `json:"search"`
	// StatusIntervalMs is how often the UI polls GET /api/status, in milliseconds.
	StatusIntervalMs int64 `json:"status_interval_ms"`
}

// quoteLimits tells the Finish dialog how much of a target a quote shows, so it can size a row
// before the quote arrives, and how many IDs one quotes request takes.
type quoteLimits struct {
	MaxLines int `json:"max_lines"`
	MaxIDs   int `json:"max_ids"`
}

// searchLimits tells the palette the length rules of search text, so it sends only text the
// server accepts (REQ-NAV-007). source.NewSearchText owns them.
type searchLimits struct {
	MinChars int `json:"min_chars"`
	MaxBytes int `json:"max_bytes"`
}

// The wire names of source's enums. Source values carry no spelling, so an unknown value is a
// server fault reported as an error, never a guess.

func renameDetectionName(d source.RenameDetection) (string, error) {
	switch d {
	case source.RenameDetectionFull:
		return "full", nil
	case source.RenameDetectionMissingBlobs:
		return "missing_blobs", nil
	case source.RenameDetectionLimitExceeded:
		return "limit_exceeded", nil
	default:
		return "", fmt.Errorf("unknown rename detection %d", d)
	}
}

func statusName(s source.Status) (string, error) {
	switch s {
	case source.StatusAdded, source.StatusDeleted, source.StatusModified, source.StatusRenamed, source.StatusType:
		return string(rune(s)), nil
	default:
		return "", fmt.Errorf("unknown file status %q", rune(s))
	}
}

func kindName(k source.FileKind) (string, error) {
	switch k {
	case source.KindText:
		return "text", nil
	case source.KindBinary:
		return "binary", nil
	case source.KindSubmodule:
		return "submodule", nil
	case source.KindModeOnly:
		return "mode", nil
	case source.KindUnavailable:
		return "unavailable", nil
	default:
		return "", fmt.Errorf("unknown file kind %d", k)
	}
}

// displayPath escapes a path that is not commentable, so control characters and invalid UTF-8
// are shown rather than interpreted. Such a file accepts only review comments, so the UI never
// needs the raw path.
func displayPath(p string, commentable bool) string {
	if commentable || p == "" {
		return p
	}
	q := strconv.Quote(p)
	return q[1 : len(q)-1]
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	snap := s.snap
	renames, err := renameDetectionName(snap.RenameDetection())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := sessionJSON{
		Source: snap.Source().String(), RenameDetection: renames,
		Files:            make([]fileJSON, snap.FileCount()),
		Quotes:           quoteLimits{MaxLines: review.MaxQuoteLines, MaxIDs: maxQuoteIDs},
		Search:           searchLimits{MinChars: source.MinSearchTextRunes, MaxBytes: source.MaxSearchTextBytes},
		StatusIntervalMs: s.statusInterval.Milliseconds(),
	}
	for i := range out.Files {
		f := snap.File(i)
		status, err := statusName(f.Status)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		commentable := review.CommentableFile(f)
		oldPath, newPath := displayPath(f.OldPath, commentable), displayPath(f.NewPath, commentable)
		// The UI keys its tree rows on the shown path, so the vendored directory is escaped like
		// it. Escaping works rune by rune and the directory ends before a '/', so the escaped
		// directory is a prefix of the escaped path.
		out.Files[i] = fileJSON{
			Status: status, OldPath: oldPath, NewPath: newPath,
			OldMode: f.OldMode, NewMode: f.NewMode, Commentable: commentable,
			VendoredDir: displayPath(snap.VendoredDir(i), commentable),
		}
	}
	v := s.cfg.Session.View()
	out.Comments = make([]commentJSON, len(v.Comments))
	for k, c := range v.Comments {
		out.Comments[k] = toCommentJSON(c)
	}
	out.Summary, out.Viewed = v.Summary, v.Viewed
	s.writeJSON(w, out)
}

type sideJSON struct {
	Present bool  `json:"present"`
	Lines   int   `json:"lines"`
	Size    int64 `json:"size"`
}

type fileDiffJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	Kind        string     `json:"kind"`
	Large       bool       `json:"large"`
	Highlighted bool       `json:"highlighted"`
	Old         sideJSON   `json:"old"`
	New         sideJSON   `json:"new"`
	Hunks       []hunkJSON `json:"hunks"`
	GapAfter    *gapJSON   `json:"gap_after,omitzero"`
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	i, ok := s.fileIndex(w, r.URL.Query().Get("i"))
	if !ok {
		return
	}
	if err := s.acquire(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.release()
	d, err := s.snap.Diff(r.Context(), i)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	kind, err := kindName(d.Facts.Kind)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	facts := d.Facts
	out := fileDiffJSON{
		Kind: kind, Large: facts.Large,
		Old: sideJSON{facts.Old.Present, facts.Old.Lines, facts.Old.Size},
		New: sideJSON{facts.New.Present, facts.New.Lines, facts.New.Size},
	}
	var oldSpans, newSpans [][]highlight.Span
	if facts.Kind == source.KindText && !facts.Large {
		oldSpans, newSpans, out.Highlighted = highlightSides(r.Context(), s.highlighter(i), d)
	}
	out.Hunks, out.GapAfter = buildHunks(d, oldSpans, newSpans)
	s.writeJSON(w, out)
}

// highlighter tokenizes file i's text. Its language is decided once, from the file's shown path,
// for both sides and for context lines.
type highlighter struct {
	s    *Server
	path string
	lang highlight.Language
}

func (s *Server) highlighter(i int) highlighter {
	f := s.snap.File(i)
	return highlighter{s: s, path: f.Path(), lang: highlight.LanguageOf(f.Path())}
}

func (h highlighter) enabled() bool { return h.lang != (highlight.Language{}) }

// lines tokenizes one side within highlightBudget, logging a failure.
func (h highlighter) lines(ctx context.Context, data []byte) ([][]highlight.Span, error) {
	ctx, cancel := context.WithTimeout(ctx, highlightBudget)
	defer cancel()
	spans, err := highlight.Lines(ctx, h.lang, validText(data))
	if err != nil {
		h.s.logf("rvw: highlight %s: %v", h.path, err)
	}
	return spans, err
}

// highlightSides tokenizes each present side's whole content, each within highlightBudget.
// Highlighting is all or nothing per file, so a failure on either side drops both.
func highlightSides(ctx context.Context, h highlighter, d source.FileDiff) (oldSpans, newSpans [][]highlight.Span, ok bool) {
	if !h.enabled() {
		return nil, nil, false
	}
	var err error
	if d.Old != nil {
		if oldSpans, err = h.lines(ctx, d.Old); err != nil {
			return nil, nil, false
		}
	}
	if d.New != nil {
		if newSpans, err = h.lines(ctx, d.New); err != nil {
			return nil, nil, false
		}
	}
	return oldSpans, newSpans, true
}

type contextJSON struct {
	Lines       []lineJSON `json:"lines"`
	Highlighted bool       `json:"highlighted"`
}

// getContext returns the unchanged lines from..to, numbered by the new side, for context expansion.
// The range must lie inside one gap; source.FileFacts.Gaps decides which files have any.
func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	i, ok := s.fileIndex(w, q.Get("i"))
	if !ok {
		return
	}
	from, errFrom := strconv.Atoi(q.Get("from"))
	to, errTo := strconv.Atoi(q.Get("to"))
	if errFrom != nil || errTo != nil || from < 1 || to < from {
		http.Error(w, "from and to must be line numbers with from <= to", http.StatusBadRequest)
		return
	}
	if err := s.acquire(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.release()
	d, err := s.snap.Diff(r.Context(), i)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	lines, err := d.Context(from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := contextJSON{Lines: make([]lineJSON, 0, len(lines))}
	var spans [][]highlight.Span
	if h := s.highlighter(i); h.enabled() {
		spans, err = h.lines(r.Context(), d.New)
		out.Highlighted = err == nil
	}
	for _, l := range lines {
		out.Lines = append(out.Lines, lineJSON{
			Old: l.Old, New: l.New, Text: strings.ToValidUTF8(l.Text, "\uFFFD"), Spans: spansAt(spans, l.New),
		})
	}
	s.writeJSON(w, out)
}

// putComment creates or edits a comment; the precondition says which. If-None-Match: * creates and
// never overwrites. If-Match: "<version>" edits the body of the comment at that version, so an edit
// never resurrects a deleted comment nor overwrites another writer's text. The server sends no ETag:
// comments arrive in bulk, so the JSON version is the entity tag.
func (s *Server) putComment(w http.ResponseWriter, r *http.Request) {
	create := r.Header.Get("If-None-Match")
	match := r.Header.Get("If-Match")
	switch {
	case create == "" && match == "":
		http.Error(w, `send If-None-Match: * to create or If-Match: "<version>" to edit`, http.StatusPreconditionRequired)
	case create != "" && match != "":
		http.Error(w, "send one of If-None-Match and If-Match, not both", http.StatusBadRequest)
	case create != "":
		s.createComment(w, r, create)
	default:
		s.editComment(w, r, match)
	}
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request, ifNoneMatch string) {
	if ifNoneMatch != "*" {
		http.Error(w, "a create sends If-None-Match: *", http.StatusBadRequest)
		return
	}
	var req struct {
		Target *placementJSON `json:"target"`
		Body   string         `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Target == nil {
		http.Error(w, "a create needs a target", http.StatusBadRequest)
		return
	}
	p, err := req.Target.placement()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if k := p.Kind(); k == review.TargetLine || k == review.TargetRange {
		// Validating a line reads the file's diff, so it shares the file-view bound.
		if err = s.acquire(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		defer s.release()
	}
	c, err := s.cfg.Session.CreateComment(r.Context(), r.PathValue("id"), p, req.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, toCommentJSON(c))
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request, ifMatch string) {
	version, ok := parseVersion(ifMatch)
	if !ok {
		http.Error(w, `an edit sends If-Match: "<version>", the comment's version in quotes`, http.StatusBadRequest)
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c, err := s.cfg.Session.EditComment(r.PathValue("id"), version, req.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, toCommentJSON(c))
}

// parseVersion reads an If-Match entity tag: one comment version as a quoted decimal.
func parseVersion(tag string) (int64, bool) {
	digits, ok := strings.CutPrefix(tag, `"`)
	if !ok {
		return 0, false
	}
	if digits, ok = strings.CutSuffix(digits, `"`); !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	return v, err == nil
}

// placementJSON is where the UI creates a comment: a changed-file index, never a path. It owns
// the request's shape: a known kind and exactly the members that kind takes. Members a kind does not
// take are rejected, not ignored. Whether the placement fits the snapshot is review's (422).
type placementJSON struct {
	File  *int   `json:"file"`
	Line  *int   `json:"line"`
	Start *int   `json:"start"`
	End   *int   `json:"end"`
	Kind  string `json:"target"`
	Side  string `json:"side"`
}

// placement converts j, or says why its shape is wrong.
func (j placementJSON) placement() (review.Placement, error) {
	file := j.File != nil
	lines := j.Line != nil || j.Start != nil || j.End != nil
	switch review.TargetKind(j.Kind) {
	case review.TargetReview:
		if file || j.Side != "" || lines {
			return review.Placement{}, errors.New("a review comment takes no file, side or lines")
		}
		return review.OnReview(), nil
	case review.TargetFile:
		if !file || j.Side != "" || lines {
			return review.Placement{}, errors.New("a file comment takes a file and no side or lines")
		}
		return review.OnFile(*j.File), nil
	case review.TargetLine:
		if !file || j.Line == nil || j.Start != nil || j.End != nil {
			return review.Placement{}, errors.New("a line comment takes a file, a side and a line")
		}
		return review.OnLine(*j.File, review.Side(j.Side), *j.Line), nil
	case review.TargetRange:
		if !file || j.Start == nil || j.End == nil || j.Line != nil {
			return review.Placement{}, errors.New("a range comment takes a file, a side, a start and an end")
		}
		return review.OnRange(*j.File, review.Side(j.Side), *j.Start, *j.End), nil
	default:
		return review.Placement{}, fmt.Errorf("unknown target %q", j.Kind)
	}
}

// deleteComment deletes the comment at the If-Match version, so a page that has not seen a newer
// edit cannot discard it (412). An absent comment is already deleted (204).
func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	match := r.Header.Get("If-Match")
	if match == "" {
		http.Error(w, `send If-Match: "<version>" to delete`, http.StatusPreconditionRequired)
		return
	}
	version, ok := parseVersion(match)
	if !ok {
		http.Error(w, `a delete sends If-Match: "<version>", the comment's version in quotes`, http.StatusBadRequest)
		return
	}
	if err := s.cfg.Session.DeleteComment(r.PathValue("id"), version); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putViewed(w http.ResponseWriter, r *http.Request) {
	i, ok := s.fileIndex(w, r.PathValue("i"))
	if !ok {
		return
	}
	var req struct {
		Viewed bool `json:"viewed"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.cfg.Session.SetViewed(i, req.Viewed); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Summary string `json:"summary"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.cfg.Session.SetSummary(req.Summary); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxQuoteIDs bounds one quotes request. The Finish dialog asks for the comments it has mounted,
// at most its visible rows plus overscan.
const maxQuoteIDs = 128

// quoteJSON is a quote as the result shows it: its code and the note on what it leaves out.
type quoteJSON struct {
	Note string   `json:"note,omitzero"`
	Code []string `json:"code"`
}

// getQuotes returns the code the result quotes for each requested line or range comment, so the
// Finish dialog shows what the agent will receive. Comments that do not exist or quote nothing are
// left out.
func (s *Server) getQuotes(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["id"]
	if len(ids) == 0 || len(ids) > maxQuoteIDs {
		http.Error(w, fmt.Sprintf("give 1 to %d id parameters", maxQuoteIDs), http.StatusBadRequest)
		return
	}
	// Quoting reads blobs, so it shares the file-view bound.
	if err := s.acquire(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.release()
	quotes, err := s.cfg.Session.Quotes(r.Context(), ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make(map[string]quoteJSON, len(quotes))
	for id, q := range quotes {
		code := make([]string, len(q.Code))
		for k, l := range q.Code {
			code[k] = strings.ToValidUTF8(l, "\uFFFD")
		}
		out[id] = quoteJSON{Code: code, Note: q.Note()}
	}
	s.writeJSON(w, out)
}

// getResult returns the Markdown result of the current draft for Copy.
func (s *Server) getResult(w http.ResponseWriter, r *http.Request) {
	// Rendering quotes every line and range comment, so it shares the file-view bound.
	if err := s.acquire(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.release()
	res, err := s.cfg.Session.Result(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := w.Write(review.RenderMarkdown(res)); err != nil {
		s.logf("rvw: write %s: %v", r.URL.Path, err)
	}
}

type finishJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	Delivered bool   `json:"delivered"`
	Markdown  string `json:"markdown,omitzero"`
	Error     string `json:"error,omitzero"`
}

// postFinish delivers the result and only then replies. A finish that reaches delivery ends the
// session whether or not delivery succeeded; Run stops serving after this response.
func (s *Server) postFinish(w http.ResponseWriter, r *http.Request) {
	// Delivery must not depend on the browser staying connected.
	d, err := s.cfg.Session.Finish(context.WithoutCancel(r.Context()), s.cfg.Deliver)
	if deliveryErr, ok := errors.AsType[*review.DeliveryError](err); ok {
		s.logf("rvw: %v", deliveryErr)
		s.writeJSON(w, finishJSON{Error: "the review result was not delivered: " + deliveryErr.Err.Error()})
		s.end(DeliveryFailed)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.DraftErr != nil {
		// The result was delivered; the draft expires later.
		s.logf("rvw: %v", d.DraftErr)
	}
	s.writeJSON(w, finishJSON{Delivered: true, Markdown: string(review.RenderMarkdown(d.Result))})
	s.end(Finished)
}

func (s *Server) postCancel(w http.ResponseWriter, r *http.Request) {
	err := s.cfg.Session.Cancel()
	if errors.Is(err, review.ErrEnded) {
		s.fail(w, r, err)
		return
	}
	if err != nil {
		// The session has ended; a draft that could not be deleted expires later.
		s.logf("rvw: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
	s.end(Cancelled)
}

// postPageClosed records a page's close notice. It always replies 204, also after the session has
// ended: the page is going away and reads no reply.
func (s *Server) postPageClosed(w http.ResponseWriter, _ *http.Request) {
	s.presence.closed()
	w.WriteHeader(http.StatusNoContent)
}

type countJSON struct {
	I       int  `json:"i"`
	Added   int  `json:"added"`
	Deleted int  `json:"deleted"`
	Binary  bool `json:"binary,omitzero"`
}

type statusJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	HeadMoved      bool        `json:"head_moved"`
	Counts         []countJSON `json:"counts"`
	Next           int         `json:"next"`
	CountsDone     bool        `json:"counts_done"`
	CountsError    string      `json:"counts_error,omitzero"`
	Generated      []int       `json:"generated,omitzero"`
	GeneratedDone  bool        `json:"generated_done"`
	GeneratedError string      `json:"generated_error,omitzero"`
}

// getStatus reports line counts found since index `since` and whether HEAD moved.
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		var err error
		if since, err = strconv.Atoi(v); err != nil {
			http.Error(w, "since must be an integer", http.StatusBadRequest)
			return
		}
	}
	moved, err := s.head.movedSince(r.Context(), s.snap)
	if err != nil {
		s.logf("rvw: %v", err)
	}
	v := s.facts.since(since)
	out := statusJSON{
		HeadMoved: moved, Counts: make([]countJSON, len(v.counts)),
		Next: v.next, CountsDone: v.countDone, CountsError: v.countErr,
		Generated: v.generated, GeneratedDone: v.genDone, GeneratedError: v.genErr,
	}
	for k, fc := range v.counts {
		out.Counts[k] = countJSON{I: fc.i, Added: fc.c.Added, Deleted: fc.c.Deleted, Binary: fc.c.Binary}
	}
	s.writeJSON(w, out)
}

func (s *Server) fileIndex(w http.ResponseWriter, v string) (int, bool) {
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 || i >= s.snap.FileCount() {
		http.Error(w, "unknown file index", http.StatusNotFound)
		return 0, false
	}
	return i, true
}

// fail maps a domain error to a status. Unexpected errors are logged in full and reported as
// internal errors.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *review.InvalidError
	switch {
	case errors.As(err, &invalid):
		http.Error(w, invalid.Reason, http.StatusUnprocessableEntity)
	case errors.Is(err, review.ErrEnded):
		http.Error(w, err.Error(), http.StatusGone)
	case errors.Is(err, review.ErrCommentExists), errors.Is(err, review.ErrNoComment), errors.Is(err, review.ErrStale):
		http.Error(w, err.Error(), http.StatusPreconditionFailed)
	case errors.Is(err, source.ErrNoContext):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, source.ErrOutsideGap):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case r.Context().Err() != nil:
		// The client went away, or the session ended and the server is stopping.
		http.Error(w, "the request was cancelled", http.StatusServiceUnavailable)
	default:
		s.logf("rvw: %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Printf(format, args...)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if err := json.UnmarshalRead(r.Body, v, json.RejectUnknownMembers(true)); err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// writeJSON replies 200 with v. Repository content may hold invalid UTF-8; JSON carries it as
// U+FFFD.
func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, v, jsontext.AllowInvalidUTF8(true)); err != nil {
		// The status is sent; the client sees a truncated body that fails to parse.
		s.logf("rvw: encode response: %v", err)
	}
}
