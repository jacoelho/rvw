package review

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
)

// MaxSeedBytes caps a seed document.
const MaxSeedBytes = 16 << 20

// SeedComment is one agent comment from a seed document, before snapshot validation.
type SeedComment struct {
	Body   string
	Target Target
}

// Seed is a parsed seed document. Start validates it as a whole, so a comment that is malformed and
// one whose target is wrong for the snapshot are reported together.
type Seed struct {
	entries []seedEntry
}

// seedEntry is one comment of the document: either a well-formed comment or why it is not.
type seedEntry struct {
	problem string
	comment SeedComment
}

// newSeed returns a seed of well-formed comments, numbered in order.
func newSeed(comments ...SeedComment) Seed {
	s := Seed{entries: make([]seedEntry, len(comments))}
	for k, c := range comments {
		s.entries[k].comment = c
	}
	return s
}

// Len returns the number of comments in the document.
func (s Seed) Len() int { return len(s.entries) }

// TargetJSON is a target's JSON shape in the seed document and the JSON result. Its members inline
// into a comment object. The draft keeps its own shape (draftTarget), and the UI places comments
// by file index (Placement).
type TargetJSON struct {
	Kind  string `json:"target"`
	Path  string `json:"path,omitzero"`
	Side  string `json:"side,omitzero"`
	Line  int    `json:"line,omitzero"`
	Start int    `json:"start,omitzero"`
	End   int    `json:"end,omitzero"`
}

// Target checks the shape for w.Kind, then builds the target with its constructor. Shape and
// target problems are *InvalidError.
func (w TargetJSON) Target() (Target, error) {
	kind := TargetKind(w.Kind)
	switch kind {
	case TargetReview:
		if w.Path != "" || w.Side != "" || w.Line != 0 || w.Start != 0 || w.End != 0 {
			return Target{}, invalid("a review comment takes no path, side or lines")
		}
		return ReviewTarget(), nil
	case TargetFile:
		if w.Side != "" || w.Line != 0 || w.Start != 0 || w.End != 0 {
			return Target{}, invalid("a file comment takes a path and no side or lines")
		}
		return FileTarget(w.Path)
	case TargetLine:
		if w.Start != 0 || w.End != 0 {
			return Target{}, invalid("a line comment takes line, not start and end")
		}
		return LineTarget(w.Path, Side(w.Side), w.Line)
	case TargetRange:
		if w.Line != 0 {
			return Target{}, invalid("a range comment takes start and end, not line")
		}
		return RangeTarget(w.Path, Side(w.Side), w.Start, w.End)
	default:
		return Target{}, invalid("unknown target %q", w.Kind)
	}
}

// TargetJSONOf returns t's JSON shape.
func TargetJSONOf(t Target) TargetJSON {
	w := TargetJSON{Kind: string(t.Kind()), Path: t.path, Side: string(t.side)}
	switch t.Kind() {
	case TargetLine:
		w.Line = t.start
	case TargetRange:
		w.Start, w.End = t.start, t.end
	case TargetReview, TargetFile:
	}
	return w
}

// SeedError lists every rejected seed comment.
type SeedError struct{ Problems []string }

func (e *SeedError) Error() string {
	var b bytes.Buffer
	b.WriteString("invalid seed:")
	for _, p := range e.Problems {
		b.WriteString("\n  ")
		b.WriteString(p)
	}
	return b.String()
}

// ParseSeed reads and parses a seed document. It fails only when the document as a whole is
// unusable; per-comment problems are kept for Start to report with target problems.
func ParseSeed(r io.Reader) (Seed, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSeedBytes+1))
	if err != nil {
		return Seed{}, fmt.Errorf("read seed: %w", err)
	}
	if len(data) > MaxSeedBytes {
		return Seed{}, &SeedError{Problems: []string{fmt.Sprintf("seed exceeds %d bytes", MaxSeedBytes)}}
	}
	var doc struct {
		Comments []jsontext.Value `json:"comments"`
	}
	if err := decodeStrict(data, &doc); err != nil {
		return Seed{}, &SeedError{Problems: []string{err.Error()}}
	}
	if doc.Comments == nil {
		return Seed{}, &SeedError{Problems: []string{`missing "comments" array`}}
	}
	seed := Seed{entries: make([]seedEntry, len(doc.Comments))}
	for i, raw := range doc.Comments {
		c, err := parseSeedComment(raw)
		if err != nil {
			seed.entries[i].problem = err.Error()
			continue
		}
		seed.entries[i].comment = c
	}
	return seed, nil
}

func parseSeedComment(raw jsontext.Value) (SeedComment, error) {
	var c struct {
		Body       *string `json:"body"`
		TargetJSON `json:",inline"`
	}
	if err := decodeStrict(raw, &c); err != nil {
		return SeedComment{}, err
	}
	t, err := c.Target()
	if err != nil {
		return SeedComment{}, err
	}
	if c.Body == nil {
		return SeedComment{}, invalid("missing body")
	}
	body, err := normalizeBody(*c.Body)
	if err != nil {
		return SeedComment{}, err
	}
	return SeedComment{Target: t, Body: body}, nil
}

// decodeStrict decodes exactly one JSON value with no unknown members. json/v2 already rejects
// trailing data, duplicate names and invalid UTF-8.
func decodeStrict(data []byte, v any) error {
	return json.Unmarshal(data, v, json.RejectUnknownMembers(true))
}
