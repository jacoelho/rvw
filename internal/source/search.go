package source

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The length rules of SearchText, which NewSearchText enforces. They are exported for the UI,
// which checks them before sending text.
const (
	MinSearchTextRunes = 3
	MaxSearchTextBytes = 256
)

// Search bounds. A search reads at most maxSearchOutput bytes of git output, keeps at most
// maxMatches matches and runs for at most searchBudget; past any of them the result is partial.
const (
	maxMatches      = 500
	maxSearchOutput = 16 << 20
	searchBudget    = 2 * time.Second
	// matchContext is the most bytes of a matched line kept on each side of the match.
	matchContext = 120
	// maxMatchText bounds len(Before)+len(Matched)+len(After) of a SearchMatch: the longest text and
	// its context on both sides.
	maxMatchText = matchContext + MaxSearchTextBytes + matchContext
)

// ErrInvalidSearchText is wrapped by the error of search text that cannot be searched for.
var ErrInvalidSearchText = errors.New("invalid search text")

// SearchText is validated search text: at least 3 runes and at most 256 bytes of valid UTF-8 with no
// NUL, CR or LF. Whitespace is part of the text. Construct it with NewSearchText.
type SearchText struct {
	text string
	// fold is set when text has no ASCII uppercase letter: the search then ignores ASCII case.
	fold bool
}

// NewSearchText validates text as SearchText. The search ignores ASCII case unless text holds an ASCII
// uppercase letter.
func NewSearchText(text string) (SearchText, error) {
	switch {
	case !utf8.ValidString(text):
		return SearchText{}, fmt.Errorf("%w: not valid UTF-8", ErrInvalidSearchText)
	case len(text) > MaxSearchTextBytes:
		return SearchText{}, fmt.Errorf("%w: longer than %d bytes", ErrInvalidSearchText, MaxSearchTextBytes)
	case utf8.RuneCountInString(text) < MinSearchTextRunes:
		return SearchText{}, fmt.Errorf("%w: shorter than %d characters", ErrInvalidSearchText, MinSearchTextRunes)
	case strings.ContainsAny(text, "\x00\r\n"):
		return SearchText{}, fmt.Errorf("%w: holds a NUL, CR or line feed", ErrInvalidSearchText)
	}
	fold := !strings.ContainsFunc(text, func(r rune) bool { return 'A' <= r && r <= 'Z' })
	return SearchText{text: text, fold: fold}, nil
}

// SearchMatch is one matched line's window around its first match. Before, Matched and After are
// consecutive bytes of the line, cut at rune starts. Matched is the line's own bytes at the
// match: never empty, and equal to the search text up to ASCII case when the text has no uppercase
// letter. CutStart and CutEnd report that the line goes on before or after the window.
type SearchMatch struct {
	Before, Matched, After    string
	File, Line                int
	NewSide, CutStart, CutEnd bool
}

// SearchResult is a search's matches, ordered by file index then line. Truncated reports that
// the search stopped at one of its bounds, so files may hold matches that are not listed.
type SearchResult struct {
	Matches   []SearchMatch
	Truncated bool
}

var (
	errSearchFull   = errors.New("search match limit reached")
	errSearchBudget = errors.New("search time budget spent")
)

// Search finds the lines of the changed files that contain the text, one match per line: the
// first. It searches each file's displayed side as stored in the snapshot (the new side, or the
// old side of a deleted file) and skips files with a missing blob, submodules, mode-only files
// (ChangedFile.modeOnly, which the diff shows without lines) and sides larger than LargeBytes;
// `git grep -I` drops binary blobs, and git grep reads no symlink blob. Git reads the blobs, one `git grep` per tree batched by pathspec
// size; nothing is retained.
//
// The search is bounded by maxMatches, maxSearchOutput and searchBudget; reaching one returns
// the matches found so far with Truncated set. Head-tree files are searched before base-tree
// files, so a partial result favours them. ctx ending is an error.
func (s *Snapshot) Search(ctx context.Context, n SearchText) (SearchResult, error) {
	if n.text == "" {
		return SearchResult{}, fmt.Errorf("%w: empty", ErrInvalidSearchText)
	}
	paths := map[string][]string{}
	index := map[string]map[string]int{s.headTree: {}, s.baseTree: {}}
	for i, f := range s.files {
		if f.Missing || f.Submodule() || f.modeOnly() {
			continue
		}
		tree, path, blob := s.headTree, f.NewPath, f.NewBlob
		if path == "" {
			tree, path, blob = s.baseTree, f.OldPath, f.OldBlob
		}
		if s.sizes[blob] > LargeBytes {
			continue
		}
		paths[tree] = append(paths[tree], path)
		index[tree][path] = i
	}

	searchCtx, cancel := context.WithTimeoutCause(ctx, searchBudget, errSearchBudget)
	defer cancel()
	var res SearchResult
	used := 0
	var err error
	for _, tree := range []string{s.headTree, s.baseTree} {
		err = pathspecBatches(paths[tree], func(batch []string) error {
			return s.grepText(searchCtx, n, tree, batch, index[tree], &res.Matches, &used)
		})
		if err != nil {
			break
		}
	}
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return SearchResult{}, fmt.Errorf("search: %w", ctx.Err())
		case errors.Is(context.Cause(searchCtx), errSearchBudget):
			res.Truncated = true
		case errors.Is(err, errSearchFull) || errors.Is(err, errOutputLimit):
			if _, failed := errors.AsType[*gitError](err); failed {
				return SearchResult{}, err
			}
			res.Truncated = true
		default:
			return SearchResult{}, err
		}
	}
	slices.SortFunc(res.Matches, func(a, b SearchMatch) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	return res, nil
}

// grepText runs one git grep for n over pathspecs in tree, appending to matches. used counts
// the search's output bytes so far; this command may read the rest of maxSearchOutput. Options a
// user's grep configuration could change are set explicitly, and LC_ALL=C makes -i fold ASCII
// only, as SearchText promises.
func (s *Snapshot) grepText(ctx context.Context, n SearchText, tree string, pathspecs []string, index map[string]int, matches *[]SearchMatch, used *int) error {
	args := []string{"grep", "-I", "-z", "-n", "--column", noColor, "-F"}
	if n.fold {
		args = append(args, "-i")
	}
	args = append(args, "--no-recurse-submodules", "-e", n.text, tree, "--")
	args = append(args, pathspecs...)
	newSide := tree == s.headTree
	limit := int64(max(maxSearchOutput-*used, 0))
	_, err := s.repo.run.stream(ctx, limit, []string{"LC_ALL=C"}, nil, func(r *bufio.Reader) error {
		return parseSearch(r, tree+":", n, func(path string, m SearchMatch, size int) error {
			*used += size
			i, ok := index[path]
			if !ok {
				return fmt.Errorf("git grep: unexpected path %q", path)
			}
			if len(*matches) == maxMatches {
				return errSearchFull
			}
			m.File, m.NewSide = i, newSide
			*matches = append(*matches, m)
			return nil
		})
	}, args...)
	if ge, ok := errors.AsType[*gitError](err); ok && ge.exitCode() == 1 {
		return nil // no line matched in any file
	}
	return err
}

// parseSearch reads `git grep -z -n --column` records `<tree>:<path>\0<line>\0<col>\0<text>\n`.
// Paths may hold ':' and LF, so the tree prefix is stripped by length and the path read up to
// NUL. Only the window of text around the match is kept, since a line can be arbitrarily long.
// emit gets each record's path, its SearchMatch without File and NewSide, and the record's size.
func parseSearch(r *bufio.Reader, prefix string, n SearchText, emit func(path string, m SearchMatch, size int) error) error {
	for {
		rec, err := r.ReadString(0)
		if errors.Is(err, io.EOF) && rec == "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("git grep: %w", err)
		}
		size := len(rec)
		path, ok := strings.CutPrefix(strings.TrimSuffix(rec, "\x00"), prefix)
		if !ok {
			return fmt.Errorf("git grep: malformed record %q", truncate([]byte(rec), 80))
		}
		line, k, err := readPositive(r, "line", path)
		if err != nil {
			return err
		}
		size += k
		col, k, err := readPositive(r, "column", path)
		if err != nil {
			return err
		}
		size += k
		m, k, err := readWindow(r, n, col-1)
		if err != nil {
			return fmt.Errorf("git grep: line %d of %q: %w", line, truncate([]byte(path), 80), err)
		}
		m.Line = line
		if err := emit(path, m, size+k); err != nil {
			return err
		}
	}
}

// readPositive reads a NUL-terminated decimal field that must be at least 1, and its size.
func readPositive(r *bufio.Reader, name, path string) (int, int, error) {
	field, err := r.ReadString(0)
	if err != nil {
		return 0, 0, fmt.Errorf("git grep: truncated record for %q: %w", truncate([]byte(path), 80), err)
	}
	v, convErr := strconv.Atoi(strings.TrimSuffix(field, "\x00"))
	if convErr != nil || v < 1 {
		return 0, 0, fmt.Errorf("git grep: invalid %s %q for %q", name, truncate([]byte(field), 80), truncate([]byte(path), 80))
	}
	return v, len(field), nil
}

// readWindow reads a matched line up to its LF and returns the SearchMatch of the match at byte at,
// with the line's size. It keeps the bytes from matchContext before the match to matchContext
// after it, plus one to tell whether the window's end splits a rune, and checks that the kept
// match is the search text.
func readWindow(r *bufio.Reader, n SearchText, at int) (SearchMatch, int, error) {
	lo, hi := max(at-matchContext, 0), at+len(n.text)+matchContext
	kept := make([]byte, 0, maxMatchText+1)
	size, pos := 0, 0
	for {
		chunk, err := r.ReadSlice('\n')
		size += len(chunk)
		body := bytes.TrimSuffix(chunk, []byte{'\n'})
		// Keep body's part of [lo, hi], the byte at hi included.
		if from, to := max(lo-pos, 0), min(hi+1-pos, len(body)); from < to {
			kept = append(kept, body[from:to]...)
		}
		pos += len(body)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return SearchMatch{}, 0, fmt.Errorf("truncated record: %w", err)
	}
	lineLen := pos
	if at+len(n.text) > lineLen {
		return SearchMatch{}, 0, fmt.Errorf("match at byte %d past the line's %d bytes", at, lineLen)
	}
	// kept spans [lo, min(hi+1, lineLen)). Move each cut off a continuation byte, staying in the
	// context so the match is kept whole.
	start, end := 0, min(hi, lineLen)-lo
	for lo > 0 && start < at-lo && !utf8.RuneStart(kept[start]) {
		start++
	}
	if end < len(kept) {
		for end > at-lo+len(n.text) && !utf8.RuneStart(kept[end]) {
			end--
		}
	}
	// One string backs the three parts.
	w := string(kept[start:end])
	from := at - lo - start
	to := from + len(n.text)
	m := SearchMatch{
		Before:   w[:from],
		Matched:  w[from:to],
		After:    w[to:],
		CutStart: lo+start > 0,
		CutEnd:   lo+end < lineLen,
	}
	if matched := m.Matched == n.text || n.fold && asciiFoldEqual(m.Matched, n.text); !matched {
		return SearchMatch{}, 0, fmt.Errorf("matched text %q is not the search text", m.Matched)
	}
	return m, size, nil
}

// asciiFoldEqual reports whether a and b are equal ignoring ASCII case only.
func asciiFoldEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
