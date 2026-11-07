package highlight

import (
	"context"
	"strings"
)

// checkEvery is how many lines pass between context checks.
const checkEvery = 1024

// out cuts classed byte ranges of the text into per-line spans. Scanners report tokens in
// increasing order; the bytes between tokens are plain. Adjacent ranges of one class merge into
// one span. Span texts are substrings of the text.
//
// Every token boundary a scanner reports is at an ASCII byte or the end of the text, so no span
// splits a UTF-8 sequence: scanners treat bytes of 0x80 and above as part of a word or of the
// surrounding token.
type out struct {
	ctx context.Context
	// err is set once ctx ends. Scanners stop when stopped reports true; the output is then
	// discarded.
	err   error
	keep  func(line int) bool // nil keeps every line
	text  string
	class string // the open span's class
	lines [][]Span
	// spans is the current chunk of span storage. The open line's spans are spans[lineStart:];
	// earlier lines' spans are clipped sub-slices of this or earlier chunks, so a line costs no
	// allocation of its own.
	spans     []Span
	lineStart int
	cur       int // the bytes before cur are emitted
	// The span being built, not yet appended to the line.
	start, end int
	open       bool
	keeping    bool // the current line's spans are kept
	capped     bool // the open span is the plain rest of a line at maxLineSpans
}

// maxLineSpans bounds one line's spans, so a minified line costs the reply and the UI a bounded
// number of spans: past maxLineSpans-1 spans, the rest of the line is one plain span.
const maxLineSpans = 500

// Span chunks start small, so a call that keeps few lines allocates little, and grow to at most
// maxChunk spans, so a full chunk wastes a bounded tail.
const (
	minChunk = 64
	maxChunk = 4096
)

func newOut(ctx context.Context, text string, keep func(line int) bool) *out {
	return &out{
		ctx: ctx, text: text, keep: keep,
		lines:   make([][]Span, 1, strings.Count(text, "\n")+1),
		keeping: keep == nil || keep(1),
	}
}

// stopped reports whether ctx has ended, so the scanner should return.
func (o *out) stopped() bool { return o.err != nil }

// tok classes text[a:e]. The part of it before the emitted end is ignored, so a scanner never
// emits a byte twice.
func (o *out) tok(a, e int, class string) {
	a = max(a, o.cur)
	if e <= a {
		return
	}
	if a > o.cur {
		o.emit(o.cur, a, "")
	}
	o.emit(a, e, class)
	o.cur = e
}

func (o *out) emit(a, e int, class string) {
	for a < e {
		nl := strings.IndexByte(o.text[a:e], '\n')
		if nl < 0 {
			o.piece(a, e, class)
			return
		}
		o.piece(a, a+nl, class)
		o.endLine()
		o.lines = append(o.lines, nil)
		n := len(o.lines)
		if n%checkEvery == 0 && o.err == nil {
			o.err = o.ctx.Err()
		}
		o.keeping = o.keep == nil || o.keep(n)
		a += nl + 1
	}
}

func (o *out) piece(a, e int, class string) {
	if a == e || !o.keeping {
		return
	}
	// A line's pieces are contiguous, so the capped rest only grows.
	if o.capped || o.open && o.class == class && o.end == a {
		o.end = e
		return
	}
	n := len(o.spans) - o.lineStart
	if o.open {
		n++
	}
	if n == maxLineSpans-1 {
		o.capped = true
		if o.open && o.class == "" {
			o.end = e
			return
		}
		class = ""
	}
	o.flush()
	o.open, o.start, o.end, o.class = true, a, e, class
}

func (o *out) flush() {
	if !o.open {
		return
	}
	o.open = false
	if len(o.spans) == cap(o.spans) {
		// Move the open line's spans to a new chunk, so they stay contiguous.
		n := len(o.spans) - o.lineStart
		next := make([]Span, n, max(min(2*cap(o.spans), maxChunk), minChunk, 2*n))
		copy(next, o.spans[o.lineStart:])
		o.spans, o.lineStart = next, 0
	}
	o.spans = append(o.spans, Span{Class: o.class, Text: o.text[o.start:o.end]})
}

// endLine ends the last line, giving it its spans.
func (o *out) endLine() {
	o.flush()
	o.capped = false
	if n := len(o.spans); n > o.lineStart {
		o.lines[len(o.lines)-1] = o.spans[o.lineStart:n:n]
		o.lineStart = n
	}
}

// finish emits the rest of the text as plain and returns the lines.
func (o *out) finish() [][]Span {
	o.emit(o.cur, len(o.text), "")
	o.cur = len(o.text)
	o.endLine()
	return o.lines
}
