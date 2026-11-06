package review

import "fmt"

// draftVersion is the persisted draft format. A draft of another version is moved aside, not
// migrated.
const draftVersion = 2

// draftFile is the persisted draft. Its types belong to this format alone, so a change to the
// agent-facing formats cannot silently change it. Comments are revalidated on load.
type draftFile struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	Version  int            `json:"version"`
	Comments []draftComment `json:"comments"`
	Summary  string         `json:"summary"`
	Viewed   []viewedFile   `json:"viewed"`
}

type draftComment struct { //nolint:govet // fieldalignment: field order is the JSON member order.
	ID     string      `json:"id"`
	Author string      `json:"author"`
	Target draftTarget `json:"target"`
	Body   string      `json:"body"`
	Seq    int64       `json:"seq"`
}

type draftTarget struct {
	Kind  string `json:"kind"`
	Path  string `json:"path,omitzero"`
	Side  string `json:"side,omitzero"`
	Start int    `json:"start,omitzero"`
	End   int    `json:"end,omitzero"`
}

// viewedFile keys a viewed mark by the file's paths, which are unique within a snapshot.
type viewedFile struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

func draftTargetOf(t Target) draftTarget {
	return draftTarget{Kind: string(t.Kind()), Path: t.path, Side: string(t.side), Start: t.start, End: t.end}
}

func (d draftTarget) target() (Target, error) {
	switch TargetKind(d.Kind) {
	case TargetReview:
		if d != (draftTarget{Kind: d.Kind}) {
			return Target{}, invalid("a review target takes no path, side or lines")
		}
		return ReviewTarget(), nil
	case TargetFile:
		if d.Side != "" || d.Start != 0 || d.End != 0 {
			return Target{}, invalid("a file target takes no side or lines")
		}
		return FileTarget(d.Path)
	case TargetLine:
		if d.End != d.Start {
			return Target{}, invalid("a line target ends where it starts")
		}
		return LineTarget(d.Path, Side(d.Side), d.Start)
	case TargetRange:
		return RangeTarget(d.Path, Side(d.Side), d.Start, d.End)
	default:
		return Target{}, invalid("unknown target kind %q", d.Kind)
	}
}

func authorOf(s string) (Author, error) {
	switch a := Author(s); a {
	case Human, Agent:
		return a, nil
	default:
		return "", invalid("unknown author %q", s)
	}
}

// encodeDraft is the persisted form of st; comments are in result order.
func encodeDraft(st state, comments []Comment, viewed []viewedFile) draftFile {
	d := draftFile{
		Version: draftVersion, Summary: st.summary,
		Comments: make([]draftComment, 0, len(comments)), Viewed: viewed,
	}
	for _, c := range comments {
		d.Comments = append(d.Comments, draftComment{
			ID: c.ID, Author: string(c.Author), Target: draftTargetOf(c.Target), Body: c.Body, Seq: c.Seq,
		})
	}
	return d
}

// commentError places a draft comment's problem.
func commentError(id string, err error) error { return fmt.Errorf("comment %q: %w", id, err) }
