package review

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/source"
)

// Quote limits.
const (
	MaxQuoteLines     = 20
	MaxQuoteLineBytes = 500
)

// Result is the finished review (Definitions: review result).
type Result struct {
	Source   source.Source
	BaseTree string
	HeadTree string
	Summary  string
	Comments []ResultComment
}

// ResultComment is a comment with its quoted code.
type ResultComment struct {
	Author Author
	Body   string
	Target Target
	Quote
}

// Quote is the code a line or range comment quotes; review and file comments quote nothing.
type Quote struct {
	// Code holds at most MaxQuoteLines lines, each cut at MaxQuoteLineBytes.
	Code         []string
	OmittedLines int
	OmittedBytes int
}

// result builds the review result for st. It reads only immutable session fields, so callers run
// it without the lock on a state copied under it.
func (s *Session) result(ctx context.Context, st state) (Result, error) {
	cs := sorted(st)
	quotes, err := s.quote(ctx, cs)
	if err != nil {
		return Result{}, err
	}
	r := Result{
		Source: s.snap.Source(), BaseTree: s.snap.BaseTree(), HeadTree: s.snap.HeadTree(), Summary: st.summary,
		Comments: make([]ResultComment, 0, len(cs)),
	}
	for k, c := range cs {
		r.Comments = append(r.Comments, ResultComment{Author: c.Author, Target: c.Target, Body: c.Body, Quote: quotes[k]})
	}
	return r, nil
}

// Quotes returns the quotes of the comments with the given IDs, keyed by ID. IDs of comments that
// do not exist, or quote nothing, are left out. Quoting reads blobs, so it runs on comments copied
// under the lock without holding it.
func (s *Session) Quotes(ctx context.Context, ids []string) (map[string]Quote, error) {
	s.mu.Lock()
	cs := make([]Comment, 0, len(ids))
	for _, id := range ids {
		if c, ok := s.state.comments[id]; ok {
			cs = append(cs, c)
		}
	}
	s.mu.Unlock()
	quotes, err := s.quote(ctx, cs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Quote, len(cs))
	for k, c := range cs {
		if kind := c.Target.Kind(); kind == TargetLine || kind == TargetRange {
			out[c.ID] = quotes[k]
		}
	}
	return out, nil
}

// quote reads the quote of each comment: out[k] is cs[k]'s. Each file side is read once, whatever
// the number of comments on it.
func (s *Session) quote(ctx context.Context, cs []Comment) ([]Quote, error) {
	type sideKey struct {
		side Side
		file int
	}
	type quoteGroup struct {
		comments []int // indexes into cs
		ranges   []source.LineRange
		key      sideKey
	}
	out := make([]Quote, len(cs))
	var groups []*quoteGroup
	byKey := map[sideKey]*quoteGroup{}
	for k, c := range cs {
		if kind := c.Target.Kind(); kind != TargetLine && kind != TargetRange {
			continue
		}
		key := sideKey{file: c.File, side: c.Target.side}
		g := byKey[key]
		if g == nil {
			g = &quoteGroup{key: key}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.comments = append(g.comments, k)
		start, end := c.Target.Lines()
		g.ranges = append(g.ranges, QuoteRange(start, end))
	}
	for _, g := range groups {
		lines, err := s.snap.Lines(ctx, g.key.file, g.key.side == New, g.ranges, QuoteKeep)
		if err != nil {
			f := s.snap.File(g.key.file)
			return nil, fmt.Errorf("quote %s: %w", f.Path(), err)
		}
		for j, k := range g.comments {
			start, end := cs[k].Target.Lines()
			out[k] = CutQuote(start, end, lines[j])
		}
	}
	return out, nil
}

// QuoteRange is the line range to read for a quote of lines start to end: at most MaxQuoteLines.
func QuoteRange(start, end int) source.LineRange {
	return source.LineRange{Start: start, End: min(end, start+MaxQuoteLines-1)}
}

// QuoteKeep is the line prefix to read for a quote: a few bytes past MaxQuoteLineBytes let CutQuote
// find a rune boundary.
const QuoteKeep = MaxQuoteLineBytes + utf8.UTFMax

// CutQuote is the quote of lines start to end from lines, read with QuoteRange and QuoteKeep: each
// line cut at MaxQuoteLineBytes, with what was left out counted.
func CutQuote(start, end int, lines []source.SideLine) Quote {
	q := Quote{OmittedLines: end - start + 1 - len(lines)}
	for _, l := range lines {
		cut := cutLine(l.Text)
		q.OmittedBytes += l.Bytes - len(cut)
		q.Code = append(q.Code, cut)
	}
	return q
}

// cutLine cuts a line after MaxQuoteLineBytes at a UTF-8 boundary.
func cutLine(l string) string {
	if len(l) <= MaxQuoteLineBytes {
		return l
	}
	n := MaxQuoteLineBytes
	for n > 0 && !utf8.RuneStart(l[n]) {
		n--
	}
	return l[:n]
}

// sourceDescription is the Markdown "Source:" line, part of the agent-facing format. Its text is
// source.Source.String, the description the human sees on stderr and in the UI.
func sourceDescription(r Result) string {
	return fmt.Sprintf("%s (base %s, head %s)", r.Source, short(r.BaseTree), short(r.HeadTree))
}

func short(id string) string { return id[:min(len(id), 7)] }

// RenderMarkdown renders the result as Markdown. Every text a person or agent wrote, the summary
// and each comment body, is a block quote, so no body can end the code fence, open a heading or
// otherwise change the structure around it.
func RenderMarkdown(r Result) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Review\n\nSource: %s\n", sourceDescription(r))
	if strings.TrimSpace(r.Summary) != "" {
		b.WriteString("\n## Summary\n\n")
		writeQuote(&b, r.Summary)
	}
	if len(r.Comments) == 0 {
		return b.Bytes()
	}
	b.WriteString("\n## Comments\n")
	inReview := false
	for _, c := range r.Comments {
		kind := c.Target.Kind()
		switch {
		case kind == TargetReview && !inReview:
			b.WriteString("\n### Review\n")
			inReview = true
		case kind == TargetReview:
		default:
			b.WriteString("\n### ")
			b.WriteString(codeSpan(c.Target.Path()))
			start, end := c.Target.Lines()
			switch kind {
			case TargetFile, TargetReview:
				b.WriteString(" (file)\n")
			case TargetLine:
				fmt.Fprintf(&b, ":%d (%s)\n", start, c.Target.Side())
			case TargetRange:
				fmt.Fprintf(&b, ":%d-%d (%s)\n", start, end, c.Target.Side())
			}
			if kind == TargetLine || kind == TargetRange {
				writeCode(&b, c)
			}
		}
		fmt.Fprintf(&b, "\n(%s)\n", c.Author)
		writeQuote(&b, c.Body)
	}
	return b.Bytes()
}

// writeQuote writes text as a block quote, one quoted line per line; an empty line is a bare
// ">" so the quote does not end.
func writeQuote(b *bytes.Buffer, text string) {
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func writeCode(b *bytes.Buffer, c ResultComment) {
	body := strings.Join(c.Code, "\n")
	fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
	fmt.Fprintf(b, "\n%s\n%s\n%s\n", fence, body, fence)
	if note := c.Note(); note != "" {
		fmt.Fprintf(b, "\n%s\n", note)
	}
}

// Note returns the line that tells what q leaves out, or "" when it leaves out nothing. The result
// and the Finish dialog both show it, so the wording has this one owner.
func (q Quote) Note() string {
	var parts []string
	if q.OmittedLines > 0 {
		parts = append(parts, fmt.Sprintf("%d more %s", q.OmittedLines, plural(q.OmittedLines, "line", "lines")))
	}
	if q.OmittedBytes > 0 {
		parts = append(parts, fmt.Sprintf("%d %s cut from long lines", q.OmittedBytes, plural(q.OmittedBytes, "byte", "bytes")))
	}
	if parts == nil {
		return ""
	}
	return "… " + strings.Join(parts, "; ") + " not shown"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// codeSpan wraps s in a Markdown code span using more backticks than any run inside it.
func codeSpan(s string) string {
	ticks := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return ticks + " " + s + " " + ticks
	}
	return ticks + s + ticks
}

func longestRun(s string, c byte) int {
	best, run := 0, 0
	for i := range len(s) {
		if s[i] == c {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

type jsonResult struct {
	Source   jsonSource    `json:"source"`
	BaseTree string        `json:"base_tree"`
	HeadTree string        `json:"head_tree"`
	Summary  string        `json:"summary"`
	Comments []jsonComment `json:"comments"`
}

type jsonSource struct {
	Kind        source.Kind `json:"kind"`
	Branch      string      `json:"branch,omitzero"`
	Uncommitted bool        `json:"uncommitted"`
}

type jsonComment struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	Author       Author `json:"author"`
	TargetJSON   `json:",inline"`
	Code         []string `json:"code,omitzero"`
	OmittedLines int      `json:"code_omitted_lines,omitzero"`
	OmittedBytes int      `json:"code_omitted_bytes,omitzero"`
	Body         string   `json:"body"`
}

// RenderJSON renders the result as one JSON document.
func RenderJSON(r Result) ([]byte, error) {
	out := jsonResult{
		BaseTree: r.BaseTree, HeadTree: r.HeadTree, Summary: r.Summary,
		Source:   jsonSource{Kind: r.Source.Kind(), Branch: r.Source.BaseRef(), Uncommitted: r.Source.IncludesUncommitted()},
		Comments: make([]jsonComment, 0, len(r.Comments)),
	}
	for _, c := range r.Comments {
		out.Comments = append(out.Comments, jsonComment{
			Author: c.Author, TargetJSON: TargetJSONOf(c.Target), Code: c.Code,
			OmittedLines: c.OmittedLines, OmittedBytes: c.OmittedBytes, Body: c.Body,
		})
	}
	// Quoted code may hold invalid UTF-8; JSON carries it as U+FFFD.
	data, err := json.Marshal(out, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return append(data, '\n'), nil
}

// SourceAttrs describes snap's source for the start record on stderr: the source, the base and
// head trees, the HEAD commit ("unborn" when there is none) and the changed-file count.
func SourceAttrs(snap *source.Snapshot) []slog.Attr {
	head := snap.HeadCommit()
	if head == "" {
		head = "unborn"
	}
	return []slog.Attr{
		slog.String("source", snap.Source().String()), slog.String("base", short(snap.BaseTree())),
		slog.String("head", short(snap.HeadTree())), slog.String("HEAD", short(head)),
		slog.Int("files", snap.FileCount()),
	}
}
