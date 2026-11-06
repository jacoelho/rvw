package source_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/jacoelho/rvw/internal/source"
)

// emptyTree is Git's well-known empty tree ID (SHA-1 repositories).
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// cleanEnv is the environment of the test's own git commands: an allowlist, so variables a test
// sets with t.Setenv to attack the code under test never reach the oracle.
var cleanEnv []string

// TestMain isolates every git command, including those the package runs, from the user's
// configuration, attributes and ignore files.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	home, err := os.MkdirTemp("", "rvw-source-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	vars := map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
		// No detached auto-maintenance writing into fixtures after a test returns.
		"GIT_CONFIG_COUNT":    "2",
		"GIT_CONFIG_KEY_0":    "maintenance.auto",
		"GIT_CONFIG_VALUE_0":  "false",
		"GIT_CONFIG_KEY_1":    "gc.auto",
		"GIT_CONFIG_VALUE_1":  "0",
		"GIT_AUTHOR_NAME":     "Test Author",
		"GIT_AUTHOR_EMAIL":    "author@example.com",
		"GIT_AUTHOR_DATE":     "2026-01-02T03:04:05Z",
		"GIT_COMMITTER_NAME":  "Test Committer",
		"GIT_COMMITTER_EMAIL": "committer@example.com",
		"GIT_COMMITTER_DATE":  "2026-01-02T03:04:05Z",
	}
	cleanEnv = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir()}
	for k, v := range vars {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cleanEnv = append(cleanEnv, k+"="+v)
	}
	return m.Run()
}

// repo is a fixture repository driven by the real git executable.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &repo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	return r
}

// git runs git in the fixture's worktree and returns stdout without the final newline.
func (r *repo) git(args ...string) string {
	r.t.Helper()
	return strings.TrimSuffix(string(r.gitIn(r.dir, nil, args...)), "\n")
}

// gitIn runs git in dir with the clean environment plus extra and returns raw stdout.
func (r *repo) gitIn(dir string, extra []string, args ...string) []byte {
	r.t.Helper()
	out, err := gitOutput(r.t.Context(), dir, extra, nil, args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// gitInStdin runs git in the fixture's worktree with stdin and returns raw stdout.
func (r *repo) gitInStdin(stdin string, args ...string) []byte {
	r.t.Helper()
	out, err := gitOutput(r.t.Context(), r.dir, nil, strings.NewReader(stdin), args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

func gitOutput(ctx context.Context, dir string, extra []string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Env = append(slices.Clone(cleanEnv), extra...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// on returns the fixture bound to t, for use from a subtest.
func (r *repo) on(t *testing.T) *repo {
	t.Helper()
	return &repo{t: t, dir: r.dir}
}

func (r *repo) path(rel string) string { return filepath.Join(r.dir, filepath.FromSlash(rel)) }

func (r *repo) write(rel, content string) {
	r.t.Helper()
	if err := writeFile(r.path(rel), content); err != nil {
		r.t.Fatal(err)
	}
}

func writeFile(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func (r *repo) remove(rel string) {
	r.t.Helper()
	if err := os.Remove(r.path(rel)); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything and commits, returning the commit ID.
func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "--no-verify", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func (r *repo) tree(rev string) string {
	r.t.Helper()
	return r.git("rev-parse", "--verify", rev+"^{tree}")
}

// uncommittedTree computes the worktree's uncommitted tree independently of any index stat
// cache: a fresh private index loaded from HEAD (or empty when HEAD is unborn) has no stat data,
// so git add -A hashes every file and the tree reflects the worktree's content exactly.
func (r *repo) uncommittedTree(worktree string) string {
	r.t.Helper()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(r.t.TempDir(), "index")}
	if _, err := gitOutput(r.t.Context(), worktree, nil, nil, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		r.gitIn(worktree, env, "read-tree", "HEAD")
	} else {
		r.gitIn(worktree, env, "read-tree", "--empty")
	}
	r.gitIn(worktree, env, "add", "-A")
	return strings.TrimSpace(string(r.gitIn(worktree, env, "write-tree")))
}

// treePaths lists the paths in a tree.
func (r *repo) treePaths(tree string) []string {
	r.t.Helper()
	out := r.gitIn(r.dir, nil, "ls-tree", "-r", "-z", "--name-only", tree)
	return splitZ(out)
}

func splitZ(out []byte) []string {
	s := strings.TrimSuffix(string(out), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// open opens dir with the package under test and closes it at cleanup.
func open(t *testing.T, dir string) *source.Repo {
	t.Helper()
	repo, err := source.Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return repo
}

var quiet = slog.New(slog.DiscardHandler)

func resolve(t *testing.T, dir string, src source.Source) *source.Snapshot {
	t.Helper()
	snap, err := open(t, dir).Resolve(t.Context(), src, quiet)
	if err != nil {
		t.Fatalf("Resolve(%s) in %s: %v", src, dir, err)
	}
	return snap
}

// change is one changed file as git's name-status output reports it.
type change struct {
	Status  byte
	OldPath string
	NewPath string
}

func (c change) String() string { return fmt.Sprintf("%c %q -> %q", c.Status, c.OldPath, c.NewPath) }

// gitChanges is the oracle for a snapshot's changed files: git diff-tree with the rename
// options of the snapshot diff command, in git's order.
func (r *repo) gitChanges(base, head string) []change {
	r.t.Helper()
	fields := splitZ(r.gitIn(r.dir, nil, "diff-tree", "-r", "-z", "-M", "-l1000", "--name-status", base, head))
	var out []change
	for i := 0; i < len(fields); {
		st := fields[i][0]
		switch st {
		case 'R':
			out = append(out, change{Status: st, OldPath: fields[i+1], NewPath: fields[i+2]})
			i += 3
		case 'A':
			out = append(out, change{Status: st, NewPath: fields[i+1]})
			i += 2
		case 'D':
			out = append(out, change{Status: st, OldPath: fields[i+1]})
			i += 2
		default:
			out = append(out, change{Status: st, OldPath: fields[i+1], NewPath: fields[i+1]})
			i += 2
		}
	}
	return out
}

func snapshotChanges(snap *source.Snapshot) []change {
	out := make([]change, 0, snap.FileCount())
	for i := range snap.FileCount() {
		f := snap.File(i)
		out = append(out, change{Status: byte(f.Status), OldPath: f.OldPath, NewPath: f.NewPath})
	}
	return out
}

// checkSnapshot compares a snapshot's trees and changed files with git's.
func (r *repo) checkSnapshot(snap *source.Snapshot, wantBase, wantHead string) {
	r.t.Helper()
	if snap.BaseTree() != wantBase {
		r.t.Errorf("base tree = %s, want %s", snap.BaseTree(), wantBase)
	}
	if snap.HeadTree() != wantHead {
		r.t.Errorf("head tree = %s, want %s", snap.HeadTree(), wantHead)
	}
	got, want := snapshotChanges(snap), r.gitChanges(wantBase, wantHead)
	if !slices.Equal(got, want) {
		r.t.Errorf("changed files:\n got %v\nwant %v", got, want)
	}
}

// numstat is git's line counts keyed by "old\x00new", with the unrenamed side's path filled in
// from the changed-file list.
func (r *repo) numstat(base, head string) map[string]source.LineCount {
	r.t.Helper()
	out := string(r.gitIn(r.dir, nil, "diff-tree", "-r", "-z", "-M", "-l1000", "--numstat", base, head))
	byPath := map[string]change{}
	for _, c := range r.gitChanges(base, head) {
		if c.Status != 'R' {
			// Unrenamed: one path is empty or both are equal, so max is the path numstat prints.
			byPath[max(c.OldPath, c.NewPath)] = c
		}
	}
	counts := map[string]source.LineCount{}
	for out != "" {
		rec, rest, _ := strings.Cut(out, "\x00")
		fields := strings.SplitN(rec, "\t", 3)
		var c source.LineCount
		if fields[0] == "-" {
			c.Binary = true
		} else {
			c.Added, _ = strconv.Atoi(fields[0])
			c.Deleted, _ = strconv.Atoi(fields[1])
		}
		if fields[2] != "" {
			ch := byPath[fields[2]]
			counts[ch.OldPath+"\x00"+ch.NewPath] = c
			out = rest
			continue
		}
		oldPath, rest, _ := strings.Cut(rest, "\x00")
		newPath, rest, _ := strings.Cut(rest, "\x00")
		counts[oldPath+"\x00"+newPath] = c
		out = rest
	}
	return counts
}

// blobLines splits blob content into lines the way a patch numbers them: on LF only, keeping
// any CR, with a final line without LF counted.
func blobLines(data []byte) []string {
	s := string(data)
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func (r *repo) blob(rev, path string) []byte {
	r.t.Helper()
	return r.gitIn(r.dir, nil, "cat-file", "blob", rev+":"+path)
}

func (r *repo) objectID(rev, path string) string {
	r.t.Helper()
	return r.git("rev-parse", "--verify", rev+":"+path)
}

// mode returns the tree entry mode of path at rev.
func (r *repo) mode(rev, path string) string {
	r.t.Helper()
	out := r.gitIn(r.dir, nil, "ls-tree", "-z", rev, "--", path)
	mode, _, _ := strings.Cut(string(out), " ")
	return mode
}

// checkHunks verifies a text diff against the two blobs: each hunk's context and deleted lines
// are the old side at its old range, its context and added lines are the new side at its new
// range, and a NoEOL mark sits only on a side's final line when that side lacks a final LF.
func checkHunks(t *testing.T, name string, hunks []source.Hunk, oldData, newData []byte) (added, deleted int) {
	t.Helper()
	oldLines, newLines := blobLines(oldData), blobLines(newData)
	for hi, h := range hunks {
		var oldSide, newSide []string
		for li, l := range h.Lines {
			switch l.Op {
			case source.OpContext:
				oldSide, newSide = append(oldSide, l.Text), append(newSide, l.Text)
			case source.OpDelete:
				oldSide = append(oldSide, l.Text)
				deleted++
			case source.OpAdd:
				newSide = append(newSide, l.Text)
				added++
			default:
				t.Errorf("%s hunk %d line %d: op %q", name, hi, li, l.Op)
			}
			if l.NoEOL {
				checkNoEOL(t, name, h, li, oldSide, newSide, oldData, newData)
			}
		}
		checkSideRange(t, name+" old", hi, oldSide, oldLines, h.OldStart, h.OldCount)
		checkSideRange(t, name+" new", hi, newSide, newLines, h.NewStart, h.NewCount)
	}
	return added, deleted
}

func checkNoEOL(t *testing.T, name string, h source.Hunk, li int, oldSide, newSide []string, oldData, newData []byte) {
	t.Helper()
	l := h.Lines[li]
	onOld := l.Op != source.OpAdd && h.OldStart+len(oldSide)-1 == len(blobLines(oldData)) && !bytes.HasSuffix(oldData, []byte("\n"))
	onNew := l.Op != source.OpDelete && h.NewStart+len(newSide)-1 == len(blobLines(newData)) && !bytes.HasSuffix(newData, []byte("\n"))
	if !onOld && !onNew {
		t.Errorf("%s: NoEOL on line %d %q, which is not a final line without LF", name, li, l.Text)
	}
}

func checkSideRange(t *testing.T, name string, hi int, side, lines []string, start, count int) {
	t.Helper()
	if len(side) != count {
		t.Errorf("%s hunk %d: %d lines, header count %d", name, hi, len(side), count)
		return
	}
	if count == 0 {
		return
	}
	if start < 1 || start-1+count > len(lines) {
		t.Errorf("%s hunk %d: range %d,%d outside %d blob lines", name, hi, start, count, len(lines))
		return
	}
	if want := lines[start-1 : start-1+count]; !slices.Equal(side, want) {
		t.Errorf("%s hunk %d lines:\n got %q\nwant %q", name, hi, side, want)
	}
}

// fileIndex finds the snapshot index of the changed file with display path p.
func fileIndex(t *testing.T, snap *source.Snapshot, p string) int {
	t.Helper()
	for i := range snap.FileCount() {
		if f := snap.File(i); f.Path() == p {
			return i
		}
	}
	t.Fatalf("no changed file %q in snapshot", p)
	return -1
}

// linesOf returns the texts of snap.Lines, keeping up to LargeBytes of each line.
func linesOf(ctx context.Context, snap *source.Snapshot, i int, newSide bool, from, to int) ([]string, error) {
	quotes, err := snap.Lines(ctx, i, newSide, []source.LineRange{{Start: from, End: to}}, source.LargeBytes)
	if quotes == nil {
		return nil, err
	}
	lines := quotes[0]
	texts := make([]string, len(lines))
	for k, l := range lines {
		texts[k] = l.Text
	}
	return texts, err
}

// branchSource is source.Branch for a base ref the test knows is valid.
func branchSource(tb testing.TB, baseRef string, uncommitted bool) source.Source {
	tb.Helper()
	src, err := source.Branch(baseRef, uncommitted)
	if err != nil {
		tb.Fatal(err)
	}
	return src
}

// logGitPIDs makes each git command the package runs whose arguments match pattern, a sh case
// pattern over " $* ", append its process ID to the returned log first. The shim then execs git,
// which keeps the ID, so the logged ID is git's own. Not for parallel tests: it changes PATH.
func logGitPIDs(t *testing.T, pattern string) string {
	t.Helper()
	return shimGit(t, pattern+`) echo $$ >> "$LOG";;`)
}

// readPIDs returns the process IDs logged at path. Finding none fails the test, so a check over
// them cannot pass on an empty list.
func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var pids []int
	for f := range strings.FieldsSeq(string(data)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("PID log %s: %v", path, err)
		}
		pids = append(pids, pid)
	}
	if len(pids) == 0 {
		t.Fatal("no git process was logged; the check would prove nothing")
	}
	return pids
}

// processesGone fails the test for each of pids that still exists. A child that exited but was
// never waited for is a zombie, which still exists, so this also checks that each was reaped.
func processesGone(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("git process %d still exists (kill 0: %v)", pid, err)
		}
	}
}
