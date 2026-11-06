package source

import (
	"context"
	"fmt"
	"strings"
)

// WhitespaceOnly reports, for each of d's hunks, whether it is whitespace-only (Definitions):
// out[k] is d.Hunks[k]'s. It diffs d's own record again with `-w`, so it applies to a snapshot
// diff and an interdiff alike. Every entry is false when the `-w` patch changes a line that no
// hunk of d changes, because git then paired the lines differently and no hunk can be trusted to
// be whitespace-only, and for a diff that is not one text section, such as a type change.
func (s *Snapshot) WhitespaceOnly(ctx context.Context, d FileDiff) ([]bool, error) {
	out := make([]bool, len(d.Hunks))
	if d.record == "" || d.Facts.Kind != KindText || len(d.Hunks) == 0 {
		return out, nil
	}
	sideLine := func(l Line) (lineKey, bool) {
		switch l.Op {
		case OpDelete:
			return lineKey{n: l.Old}, true
		case OpAdd:
			return lineKey{n: l.New, newSide: true}, true
		case OpContext:
		}
		return lineKey{}, false
	}
	stdout, err := s.repo.run.outputStdin(ctx, maxPatch, nil, strings.NewReader(d.record), diffPairs("-w", "-p")...)
	if err != nil {
		return nil, err
	}
	sections := splitSections(stdout)
	if len(sections) > 1 {
		return out, nil
	}
	changed := map[lineKey]bool{}
	if len(sections) == 1 {
		binary, hunks, err := parsePatch(sections[0])
		if err != nil {
			return nil, fmt.Errorf("whitespace patch: %w", err)
		}
		if binary {
			return out, nil
		}
		for _, h := range hunks {
			for _, l := range h.Lines {
				if k, ok := sideLine(l); ok {
					changed[k] = true
				}
			}
		}
	}
	seen := 0
	for k, h := range d.Hunks {
		wsOnly := true
		for _, l := range h.Lines {
			if key, ok := sideLine(l); ok && changed[key] {
				wsOnly = false
				seen++
			}
		}
		out[k] = wsOnly
	}
	if seen != len(changed) {
		// A -w change outside every hunk's changed lines: the patches disagree on pairing.
		clear(out)
	}
	return out, nil
}

type lineKey struct {
	n       int
	newSide bool
}

// WhitespaceOnlyFiles calls yield, in list order, with the index of each changed file that is
// whitespace-only (Definitions) as soon as git has counted it. Only files that can be — modified
// in place with equal modes, every blob present and no side over LargeBytes — are counted, in one
// `git diff-pairs -w --numstat` run; differing blobs of a text file always count at least one
// changed line without -w, so that count is not run. A yielded index is final, but an error can
// follow earlier yields. ctx is checked between files.
func (s *Snapshot) WhitespaceOnlyFiles(ctx context.Context, yield func(i int)) error {
	var candidates []int
	for i, f := range s.files {
		if f.Status != StatusModified || f.Missing || f.Submodule() || f.OldMode != f.NewMode || f.OldBlob == f.NewBlob {
			continue
		}
		if s.sizes[f.OldBlob] > LargeBytes || s.sizes[f.NewBlob] > LargeBytes {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return nil
	}
	return s.numStat(ctx, candidates, true, func(i int, c LineCount) {
		if !c.Binary && c.Added == 0 && c.Deleted == 0 {
			yield(i)
		}
	})
}
