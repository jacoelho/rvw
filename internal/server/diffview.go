package server

import (
	"encoding/json/jsontext"
	"fmt"
	"strings"

	"github.com/jacoelho/rvw/internal/highlight"
	"github.com/jacoelho/rvw/internal/source"
	"github.com/jacoelho/rvw/internal/worddiff"
)

// The file response carries everything the UI shows, derived here once: line numbers, change
// blocks that pair deleted with added lines, word changes and the expandable gaps. The UI only
// lays these out as unified or split rows.

// lineJSON is one shown line. Old or New is zero when the line has no number on that side.
type lineJSON struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	Old   int              `json:"old,omitzero"`
	New   int              `json:"new,omitzero"`
	Text  string           `json:"t"`
	NoEOL bool             `json:"n,omitzero"`
	Spans spansJSON        `json:"s,omitzero"`
	Words []worddiff.Range `json:"w,omitzero"`
}

// spansJSON is a line's highlight spans on the wire: [{"c":class,"t":text}], with c omitted for
// plain text. A conversion from []highlight.Span copies nothing.
type spansJSON []highlight.Span

// MarshalJSONTo writes the spans token by token, with no intermediate values.
func (sp spansJSON) MarshalJSONTo(enc *jsontext.Encoder) error {
	var err error
	write := func(t jsontext.Token) {
		if err == nil {
			err = enc.WriteToken(t)
		}
	}
	write(jsontext.BeginArray)
	for _, x := range sp {
		write(jsontext.BeginObject)
		if x.Class != "" {
			write(jsontext.String("c"))
			write(jsontext.String(x.Class))
		}
		write(jsontext.String("t"))
		write(jsontext.String(x.Text))
		write(jsontext.EndObject)
	}
	write(jsontext.EndArray)
	return err
}

// blockJSON is a run of context lines, or a change: deleted lines followed by added lines, where
// Del[k] pairs with Add[k] for word changes and the split view.
type blockJSON struct {
	Context []lineJSON `json:"ctx,omitzero"`
	Del     []lineJSON `json:"del,omitzero"`
	Add     []lineJSON `json:"add,omitzero"`
}

// gapJSON is a run of unchanged lines not in any hunk: new-side lines From..To, which are old-side
// lines OldFrom onwards.
type gapJSON struct {
	From    int `json:"from"`
	To      int `json:"to"`
	OldFrom int `json:"old_from"`
}

type hunkJSON struct {
	Header    string      `json:"header"`
	GapBefore *gapJSON    `json:"gap_before,omitzero"`
	Blocks    []blockJSON `json:"blocks"`
}

// hunkHeader formats a hunk header as git prints it.
func hunkHeader(h source.Hunk) string {
	r := func(start, count int) string {
		if count == 1 {
			return fmt.Sprint(start)
		}
		return fmt.Sprintf("%d,%d", start, count)
	}
	s := fmt.Sprintf("@@ -%s +%s @@", r(h.OldStart, h.OldCount), r(h.NewStart, h.NewCount))
	if h.Section != "" {
		s += " " + h.Section
	}
	return s
}

// gapOf is g on the wire, nil for the zero Gap.
func gapOf(g source.Gap) *gapJSON {
	if g == (source.Gap{}) {
		return nil
	}
	return &gapJSON{From: g.From, To: g.To, OldFrom: g.OldFrom}
}

// buildHunks derives the shown hunks with their expandable gaps; trailing is the gap after the
// last hunk.
func buildHunks(d source.FileDiff, oldSpans, newSpans [][]highlight.Span) (hunks []hunkJSON, trailing *gapJSON) {
	gs := d.Facts.Gaps()
	gapAt := func(k int) *gapJSON {
		if gs == nil {
			return nil
		}
		return gapOf(gs[k])
	}
	hunks = make([]hunkJSON, 0, len(d.Hunks))
	for k, h := range d.Hunks {
		hj := hunkJSON{Header: hunkHeader(h), GapBefore: gapAt(k), Blocks: []blockJSON{}}
		var cur *blockJSON
		for _, l := range h.Lines {
			line := lineJSON{Old: l.Old, New: l.New, Text: strings.ToValidUTF8(l.Text, "\uFFFD"), NoEOL: l.NoEOL}
			switch l.Op {
			case source.OpContext:
				line.Spans = spansAt(newSpans, l.New)
				if cur == nil || cur.Context == nil {
					hj.Blocks = append(hj.Blocks, blockJSON{Context: []lineJSON{}})
					cur = &hj.Blocks[len(hj.Blocks)-1]
				}
				cur.Context = append(cur.Context, line)
			case source.OpDelete:
				line.Spans = spansAt(oldSpans, l.Old)
				if cur == nil || cur.Context != nil || cur.Add != nil {
					hj.Blocks = append(hj.Blocks, blockJSON{})
					cur = &hj.Blocks[len(hj.Blocks)-1]
				}
				cur.Del = append(cur.Del, line)
			case source.OpAdd:
				line.Spans = spansAt(newSpans, l.New)
				if cur == nil || cur.Context != nil {
					hj.Blocks = append(hj.Blocks, blockJSON{})
					cur = &hj.Blocks[len(hj.Blocks)-1]
				}
				cur.Add = append(cur.Add, line)
			}
		}
		for b := range hj.Blocks {
			pairWords(&hj.Blocks[b])
		}
		hunks = append(hunks, hj)
	}
	return hunks, gapAt(len(d.Hunks))
}

func pairWords(b *blockJSON) {
	for k := range min(len(b.Del), len(b.Add)) {
		if wd, wa, ok := worddiff.Diff(b.Del[k].Text, b.Add[k].Text); ok {
			b.Del[k].Words, b.Add[k].Words = wd, wa
		}
	}
}

func spansAt(lines [][]highlight.Span, n int) []highlight.Span {
	if n < 1 || n > len(lines) {
		return nil
	}
	return lines[n-1]
}

// validText replaces invalid UTF-8 so that text, its highlight spans and its word ranges agree
// with what JSON carries.
func validText(b []byte) string {
	return strings.ToValidUTF8(string(b), "�")
}
