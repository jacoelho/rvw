package source

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/jacoelho/rvw/internal/review"
)

const maxRevisionPeelDepth = 64

var emptyTreeHash = plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")

type nativeTreeEntry struct { //nolint:govet // Field order follows Git tree entry semantics.
	path string
	oid  plumbing.Hash
	mode review.FileMode
}

type nativeTreeFrame struct {
	tree   *object.Tree
	prefix string
	next   int
}

type nativeTreeIterator struct {
	session *repositorySession
	frames  []nativeTreeFrame
	closed  bool
}

//nolint:gocognit // Commit-ish grammar is kept in one parser to enforce suffix state transitions.
func (session *repositorySession) resolveTreeish(
	ctx context.Context,
	value RevisionRef,
) (plumbing.Hash, error) {
	hash, suffix, err := session.resolveRevisionBase(value.String())
	if err != nil {
		return plumbing.ZeroHash, err
	}
	objectType, hash, err := session.peelTags(ctx, hash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	for suffix != "" {
		if err := context.Cause(ctx); err != nil {
			return plumbing.ZeroHash, err
		}
		switch suffix[0] {
		case '^':
			if strings.HasPrefix(suffix, "^{tree}") {
				if objectType == plumbing.CommitObject {
					commit, getErr := object.GetCommit(session.objects, hash)
					if getErr != nil {
						return plumbing.ZeroHash, getErr
					}
					hash, objectType = commit.TreeHash, plumbing.TreeObject
				}
				if objectType != plumbing.TreeObject {
					return plumbing.ZeroHash, errors.New("revision does not resolve to a tree")
				}
				suffix = strings.TrimPrefix(suffix, "^{tree}")
				continue
			}
			if strings.HasPrefix(suffix, "^{commit}") {
				if objectType != plumbing.CommitObject {
					return plumbing.ZeroHash, errors.New("revision does not resolve to a commit")
				}
				suffix = strings.TrimPrefix(suffix, "^{commit}")
				continue
			}
			count, rest, parseErr := parseRevisionCount(suffix[1:], 1)
			if parseErr != nil {
				return plumbing.ZeroHash, parseErr
			}
			commit, getErr := object.GetCommit(session.objects, hash)
			if getErr != nil {
				return plumbing.ZeroHash, fmt.Errorf("resolve revision parent: %w", getErr)
			}
			if count == 0 || count > uint64(len(commit.ParentHashes)) {
				return plumbing.ZeroHash, errors.New("revision parent does not exist")
			}
			hash = commit.ParentHashes[count-1]
			objectType = plumbing.CommitObject
			suffix = rest
		case '~':
			count, rest, parseErr := parseRevisionCount(suffix[1:], 1)
			if parseErr != nil {
				return plumbing.ZeroHash, parseErr
			}
			for range count {
				commit, getErr := object.GetCommit(session.objects, hash)
				if getErr != nil {
					return plumbing.ZeroHash, fmt.Errorf("resolve revision ancestor: %w", getErr)
				}
				if len(commit.ParentHashes) == 0 {
					return plumbing.ZeroHash, errors.New("revision ancestor does not exist")
				}
				hash = commit.ParentHashes[0]
			}
			objectType = plumbing.CommitObject
			suffix = rest
		default:
			return plumbing.ZeroHash, fmt.Errorf("unsupported revision suffix %q", suffix)
		}
	}
	if objectType == plumbing.CommitObject {
		commit, err := object.GetCommit(session.objects, hash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return commit.TreeHash, nil
	}
	if objectType != plumbing.TreeObject {
		return plumbing.ZeroHash, errors.New("revision does not resolve to a tree")
	}
	return hash, nil
}

func parseRevisionCount(value string, defaultValue uint64) (uint64, string, error) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return defaultValue, value, nil
	}
	count, err := strconv.ParseUint(value[:end], 10, 31)
	if err != nil {
		return 0, "", errors.New("revision ancestry count is invalid")
	}
	return count, value[end:], nil
}

func (session *repositorySession) resolveRevisionBase(value string) (plumbing.Hash, string, error) {
	if value == "" {
		return plumbing.ZeroHash, "", errors.New("revision is empty")
	}
	base := value
	suffix := ""
	if index := strings.IndexAny(value, "^~"); index >= 0 {
		base, suffix = value[:index], value[index:]
	}
	if len(base) == 40 {
		decoded := make([]byte, 20)
		if _, err := hex.Decode(decoded, []byte(base)); err == nil {
			var hash plumbing.Hash
			copy(hash[:], decoded)
			if hash.IsZero() {
				return plumbing.ZeroHash, "", errors.New("revision object ID is zero")
			}
			if _, err := session.objects.EncodedObject(plumbing.AnyObject, hash); err != nil {
				return plumbing.ZeroHash, "", fmt.Errorf("resolve revision object: %w", err)
			}
			return hash, suffix, nil
		}
	}
	candidates := []plumbing.ReferenceName{plumbing.ReferenceName(base)}
	if !strings.HasPrefix(base, "refs/") {
		candidates = []plumbing.ReferenceName{
			plumbing.ReferenceName("refs/heads/" + base),
			plumbing.ReferenceName("refs/tags/" + base),
			plumbing.ReferenceName("refs/remotes/" + base),
			plumbing.ReferenceName(base),
		}
	}
	if base == "HEAD" {
		candidates = []plumbing.ReferenceName{plumbing.HEAD}
	}
	for _, name := range candidates {
		if name != plumbing.HEAD && name.Validate() != nil {
			continue
		}
		hash, err := session.resolveReference(name)
		if err == nil {
			return hash, suffix, nil
		}
		if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return plumbing.ZeroHash, "", err
		}
	}
	return plumbing.ZeroHash, "", fmt.Errorf("revision %q was not found", base)
}

func (session *repositorySession) resolveReference(name plumbing.ReferenceName) (plumbing.Hash, error) {
	seen := make(map[plumbing.ReferenceName]struct{}, 4)
	for range maxRevisionPeelDepth {
		if _, exists := seen[name]; exists {
			return plumbing.ZeroHash, errors.New("symbolic reference cycle")
		}
		seen[name] = struct{}{}
		reference, err := session.storage.Reference(name)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if reference.Type() == plumbing.HashReference {
			if reference.Hash().IsZero() {
				return plumbing.ZeroHash, errors.New("reference resolves to a zero object ID")
			}
			return reference.Hash(), nil
		}
		name = reference.Target()
	}
	return plumbing.ZeroHash, errors.New("symbolic reference depth exceeds limit")
}

func (session *repositorySession) peelTags(
	ctx context.Context,
	hash plumbing.Hash,
) (plumbing.ObjectType, plumbing.Hash, error) {
	for range maxRevisionPeelDepth {
		if err := context.Cause(ctx); err != nil {
			return plumbing.InvalidObject, plumbing.ZeroHash, err
		}
		encoded, err := session.objects.EncodedObject(plumbing.AnyObject, hash)
		if err != nil {
			return plumbing.InvalidObject, plumbing.ZeroHash, err
		}
		if encoded.Type() != plumbing.TagObject {
			return encoded.Type(), hash, nil
		}
		tag, err := object.DecodeTag(session.objects, encoded)
		if err != nil {
			return plumbing.InvalidObject, plumbing.ZeroHash, err
		}
		hash = tag.Target
	}
	return plumbing.InvalidObject, plumbing.ZeroHash, errors.New("annotated tag depth exceeds limit")
}

func newNativeTreeIterator(
	ctx context.Context,
	session *repositorySession,
	treeHash plumbing.Hash,
) (*nativeTreeIterator, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if treeHash == emptyTreeHash {
		return &nativeTreeIterator{session: session}, nil
	}
	tree, err := object.GetTree(session.objects, treeHash)
	if err != nil {
		return nil, fmt.Errorf("read Git tree %s: %w", treeHash, err)
	}
	return &nativeTreeIterator{
		session: session,
		frames:  []nativeTreeFrame{{tree: tree}},
	}, nil
}

func (session *repositorySession) resolveHeadTree(ctx context.Context) (plumbing.Hash, error) {
	head, err := session.storage.Reference(plumbing.HEAD)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return emptyTreeHash, nil
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if head.Type() == plumbing.SymbolicReference {
		if _, err := session.storage.Reference(head.Target()); errors.Is(err, plumbing.ErrReferenceNotFound) {
			return emptyTreeHash, nil
		} else if err != nil {
			return plumbing.ZeroHash, err
		}
	}
	return session.resolveTreeish(ctx, RevisionRef("HEAD"))
}

func (iterator *nativeTreeIterator) next(ctx context.Context) (nativeTreeEntry, error) {
	for len(iterator.frames) != 0 {
		if err := context.Cause(ctx); err != nil {
			return nativeTreeEntry{}, err
		}
		frame := &iterator.frames[len(iterator.frames)-1]
		if frame.next == len(frame.tree.Entries) {
			iterator.frames = iterator.frames[:len(iterator.frames)-1]
			continue
		}
		entry := frame.tree.Entries[frame.next]
		frame.next++
		if entry.Name == "" || strings.ContainsAny(entry.Name, "\x00/") {
			return nativeTreeEntry{}, errors.New("git tree contains an invalid entry name")
		}
		path := entry.Name
		if frame.prefix != "" {
			path = frame.prefix + "/" + entry.Name
		}
		if err := validateRawPath([]byte(path)); err != nil {
			return nativeTreeEntry{}, err
		}
		if entry.Mode == filemode.Dir {
			if len(iterator.frames) == 4_096 {
				return nativeTreeEntry{}, &LimitError{Resource: LimitWorktreeDepth, Limit: 4_096}
			}
			tree, err := object.GetTree(iterator.session.objects, entry.Hash)
			if err != nil {
				return nativeTreeEntry{}, fmt.Errorf("read Git subtree %q: %w", path, err)
			}
			iterator.frames = append(iterator.frames, nativeTreeFrame{tree: tree, prefix: path})
			continue
		}
		modeText := strings.TrimPrefix(entry.Mode.String(), "0")
		mode, err := review.NewFileMode(modeText)
		if err != nil {
			return nativeTreeEntry{}, fmt.Errorf("git tree path %q: %w", path, err)
		}
		if entry.Hash.IsZero() {
			return nativeTreeEntry{}, fmt.Errorf("git tree path %q has a zero object ID", path)
		}
		return nativeTreeEntry{path: path, oid: entry.Hash, mode: mode}, nil
	}
	iterator.closed = true
	return nativeTreeEntry{}, io.EOF
}

func hashObjectID(hash plumbing.Hash) (review.ObjectID, error) {
	if hash.IsZero() {
		return "", errors.New("git object ID is zero")
	}
	return review.NewObjectID(hash.String())
}
