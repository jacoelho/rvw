package source

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RetentionPrefix is the namespace of retention refs (INV-PLAT-004): the only refs rvw writes.
const RetentionPrefix = "refs/rvw/"

// maxRetentionRefs bounds one listing of retention refs. Each worktree's ledger names at most 256
// snapshots, so this is far above any honest listing.
const maxRetentionRefs = 1 << 16

// refWriteConfig keeps ref updates from running hooks or writing a reflog (REQ-RND-005).
var refWriteConfig = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.logAllRefUpdates=false"}

// RetentionRef is a ref under RetentionPrefix, the commit it points at, and that commit's
// committer date.
type RetentionRef struct {
	Committed time.Time
	Name      string
	Commit    string
}

// RefUpdate is one write KeepTrees made: Name moved from Old, "" when it was absent, to New.
type RefUpdate struct {
	Name, Old, New string
}

func checkRetentionRef(name string) error {
	rest, ok := strings.CutPrefix(name, RetentionPrefix)
	if !ok || rest == "" || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return fmt.Errorf("invalid retention ref %q", name)
	}
	for _, c := range []byte(rest) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && c != '-' && c != '/' {
			return fmt.Errorf("invalid retention ref %q", name)
		}
	}
	return nil
}

// KeepTrees points ref, under RetentionPrefix, at a new commit whose tree holds base as `base`
// and head as `head`, authored and committed by `rvw <rvw@localhost>` at at, so git gc keeps both
// trees' objects, and returns the update for UndoKeep. It runs no hook, signs nothing and writes no
// reflog. It writes loose objects before the ref, so a failure can leave unreachable objects but
// never a ref to a partial commit. The ref is written only if it still holds what was read just
// before, so a concurrent write by another worktree's session fails this call instead of being lost.
func (r *Repo) KeepTrees(ctx context.Context, ref, base, head string, at time.Time) (RefUpdate, error) {
	if err := checkRetentionRef(ref); err != nil {
		return RefUpdate{}, err
	}
	if !ValidObjectID(base) || !ValidObjectID(head) {
		return RefUpdate{}, fmt.Errorf("invalid tree ids %q, %q", base, head)
	}
	old, err := r.refCommit(ctx, ref)
	if err != nil {
		return RefUpdate{}, fmt.Errorf("keep trees: %w", err)
	}
	listing := fmt.Sprintf("040000 tree %s\tbase\n040000 tree %s\thead\n", base, head)
	tree, err := r.objectLine(ctx, nil, listing, "mktree")
	if err != nil {
		return RefUpdate{}, fmt.Errorf("keep trees: %w", err)
	}
	date := fmt.Sprintf("@%d +0000", at.Unix())
	env := []string{
		"GIT_AUTHOR_NAME=rvw", "GIT_AUTHOR_EMAIL=rvw@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=rvw", "GIT_COMMITTER_EMAIL=rvw@localhost", "GIT_COMMITTER_DATE=" + date,
	}
	commit, err := r.objectLine(ctx, env, "", "commit-tree", "--no-gpg-sign", "-m", "rvw retention", tree)
	if err != nil {
		return RefUpdate{}, fmt.Errorf("keep trees: %w", err)
	}
	expect := old
	if expect == "" {
		expect = strings.Repeat("0", len(commit)) // update-ref's "must not exist"
	}
	if err := r.updateRefs(ctx, fmt.Sprintf("update %s %s %s\n", ref, commit, expect)); err != nil {
		return RefUpdate{}, fmt.Errorf("keep trees: %w", err)
	}
	return RefUpdate{Name: ref, Old: old, New: commit}, nil
}

// UndoKeep returns u's ref to what it held before KeepTrees wrote u: u.Old, or absent. It changes
// the ref only while it still points at u.New, so a later write by another session is kept.
func (r *Repo) UndoKeep(ctx context.Context, u RefUpdate) error {
	if err := checkRetentionRef(u.Name); err != nil {
		return err
	}
	if !ValidObjectID(u.New) || (u.Old != "" && !ValidObjectID(u.Old)) {
		return fmt.Errorf("invalid ref update %+v", u)
	}
	cmd := fmt.Sprintf("update %s %s %s\n", u.Name, u.Old, u.New)
	if u.Old == "" {
		cmd = fmt.Sprintf("delete %s %s\n", u.Name, u.New)
	}
	if err := r.updateRefs(ctx, cmd); err != nil {
		return fmt.Errorf("undo retention ref: %w", err)
	}
	return nil
}

// refCommit returns the object ref points at, or "" when there is no such ref.
func (r *Repo) refCommit(ctx context.Context, ref string) (string, error) {
	out, err := r.run.output(ctx, maxReplyLine*4, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", ref)
	if err != nil {
		return "", err
	}
	for line := range bytes.Lines(out) {
		// The pattern also matches refs below ref, which KeepTrees never writes.
		if name, id, ok := strings.Cut(strings.TrimSuffix(string(line), "\n"), "\x00"); ok && name == ref {
			if !ValidObjectID(id) {
				return "", fmt.Errorf("for-each-ref: unexpected object %q", id)
			}
			return id, nil
		}
	}
	return "", nil
}

// updateRefs runs one update-ref transaction, running no hook and writing no reflog: every
// command applies, or none does.
func (r *Repo) updateRefs(ctx context.Context, commands string) error {
	args := append(append([]string(nil), refWriteConfig...), "update-ref", "--no-deref", "--stdin")
	_, err := r.run.outputStdin(ctx, maxStderr, nil, strings.NewReader(commands), args...)
	return err
}

// objectLine runs a git command that prints one object ID and returns it.
func (r *Repo) objectLine(ctx context.Context, env []string, stdin string, args ...string) (string, error) {
	out, err := r.run.outputStdin(ctx, maxReplyLine, env, strings.NewReader(stdin), args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(string(out), "\n")
	if !ValidObjectID(id) {
		return "", fmt.Errorf("%s: unexpected output %q", args[0], truncate(out, 80))
	}
	return id, nil
}

// RetentionRefs lists the refs under RetentionPrefix that KeepTrees could have written, with
// their commits' committer dates. A ref there with another name, or pointing at no commit, was
// written by something else: it is not listed, so it is never deleted and never stops pruning.
func (r *Repo) RetentionRefs(ctx context.Context) ([]RetentionRef, error) {
	out, err := r.run.output(ctx, maxRetentionRefs*256, nil,
		"for-each-ref", "--format=%(refname)%00%(objectname)%00%(committerdate:unix)", RetentionPrefix)
	if err != nil {
		return nil, err
	}
	var refs []RetentionRef
	for line := range bytes.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(string(line), "\n"), "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("for-each-ref: unexpected line %q", truncate(line, 120))
		}
		name, commit, unix := fields[0], fields[1], fields[2]
		secs, perr := strconv.ParseInt(unix, 10, 64)
		if perr != nil || checkRetentionRef(name) != nil || !ValidObjectID(commit) {
			continue
		}
		refs = append(refs, RetentionRef{Name: name, Commit: commit, Committed: time.Unix(secs, 0)})
	}
	return refs, nil
}

// DeleteRefs deletes the listed retention refs in one transaction, each only while it still points
// at its listed commit, so a ref another session rewrote or deleted since the listing makes the
// call fail and delete none; the next listing sees the change. Without --batch-updates (Git 2.51)
// a transaction cannot skip the one ref that moved.
func (r *Repo) DeleteRefs(ctx context.Context, refs []RetentionRef) error {
	if len(refs) == 0 {
		return nil
	}
	var in strings.Builder
	for _, ref := range refs {
		if err := checkRetentionRef(ref.Name); err != nil {
			return err
		}
		if !ValidObjectID(ref.Commit) {
			return fmt.Errorf("invalid commit %q for %s", ref.Commit, ref.Name)
		}
		fmt.Fprintf(&in, "delete %s %s\n", ref.Name, ref.Commit)
	}
	if err := r.updateRefs(ctx, in.String()); err != nil {
		return fmt.Errorf("delete retention refs: %w", err)
	}
	return nil
}
