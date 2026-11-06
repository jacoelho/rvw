package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// generatedMarker is the Go generated-code comment (go.dev/s/generatedcode); a trailing CR is
// allowed so CRLF files match. packageClause ends the search: a marker counts only before it.
const (
	generatedMarker = `^// Code generated .* DO NOT EDIT\.` + "\r?$"
	packageClause   = `^package[[:space:]]`
	markerPrefix    = "// Code generated "
)

// GoGenerated reports which changed files are generated Go files: a .go path, stored as a regular
// file, whose shown side (the new blob, or the old blob when deleted) has a line matching
// `^// Code generated .* DO NOT EDIT\.$` before its first package clause line. The result
// has length FileCount() and is indexed like File. Vendored files are never examined and are
// false, so a file is never both vendored and generated (vendoring takes precedence); files with
// a missing blob, submodules, symlinks and other paths are false too. Git reads the blobs, one
// `git grep` per tree batched by pathspec size; nothing is retained. On error it returns nil.
//
// Approximation: a line starting `package` inside a leading block comment ends the search, so
// such a file is not marked generated.
func (s *Snapshot) GoGenerated(ctx context.Context) ([]bool, error) {
	out := make([]bool, len(s.files))
	// Paths are unique within one tree, so each tree's path maps to one file index.
	byTree := map[string]map[string]int{s.headTree: {}, s.baseTree: {}}
	for i, f := range s.files {
		if f.Missing || f.Submodule() || s.vendored[i] != "" {
			continue
		}
		tree, path, mode := s.headTree, f.NewPath, f.NewMode
		if path == "" {
			tree, path, mode = s.baseTree, f.OldPath, f.OldMode
		}
		if !strings.HasSuffix(path, ".go") || (mode != "100644" && mode != "100755") {
			continue
		}
		byTree[tree][path] = i
	}
	for _, tree := range []string{s.headTree, s.baseTree} {
		paths := byTree[tree]
		var batch []string
		size := 0
		for p := range paths {
			if len(batch) > 0 && size+len(p)+1 > maxPathspecBytes {
				if err := s.grepGenerated(ctx, tree, batch, paths, out); err != nil {
					return nil, err
				}
				batch, size = batch[:0], 0
			}
			batch = append(batch, p)
			size += len(p) + 1
		}
		if len(batch) > 0 {
			if err := s.grepGenerated(ctx, tree, batch, paths, out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// grepGenerated runs one git grep over pathspecs in tree and marks the files whose first match
// is the marker. Options a user's grep configuration could change are set explicitly, and -a
// keeps binary blobs in the record format.
func (s *Snapshot) grepGenerated(ctx context.Context, tree string, pathspecs []string, index map[string]int, out []bool) error {
	args := append([]string{
		"grep", "-a", "-z", "-m", "1", "--no-color", "--no-line-number", "--no-column", "-E",
		"-e", generatedMarker, "-e", packageClause, tree, "--",
	}, pathspecs...)
	_, err := s.repo.run.stream(ctx, maxRawOutput, nil, nil, func(r *bufio.Reader) error {
		return parseGrep(r, tree+":", func(path string, marker bool) error {
			i, ok := index[path]
			if !ok {
				return fmt.Errorf("git grep: unexpected path %q", path)
			}
			out[i] = marker
			return nil
		})
	}, args...)
	if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 1 {
		return nil // no line matched in any file
	}
	return err
}

// parseGrep reads `git grep -z` records `<tree>:<path>\0<text>\n`. Paths may hold ':' and LF,
// so the tree prefix is stripped by length and the path read up to NUL. The text is classified
// by its prefix and the rest discarded, since a matched line can be arbitrarily long.
func parseGrep(r *bufio.Reader, prefix string, emit func(path string, marker bool) error) error {
	for {
		rec, err := r.ReadString(0)
		if errors.Is(err, io.EOF) && rec == "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("git grep: %w", err)
		}
		path, ok := strings.CutPrefix(strings.TrimSuffix(rec, "\x00"), prefix)
		if !ok {
			return fmt.Errorf("git grep: malformed record %q", rec)
		}
		head, err := r.Peek(len(markerPrefix))
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
			return fmt.Errorf("git grep: %w", err)
		}
		marker := string(head) == markerPrefix
		for {
			_, err := r.ReadSlice('\n')
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return fmt.Errorf("git grep: truncated record for %q: %w", path, err)
		}
		if err := emit(path, marker); err != nil {
			return err
		}
	}
}
