package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	maxWorktreeInventoryBytes int64 = 64 << 20
	maxWorktreeRawBytes       int64 = 128 << 20
	indexUpdateBatchBytes           = 1 << 20
)

type rawWorktreeEntry struct {
	path []byte
	mode review.FileMode
	file string
	oid  *review.ObjectID
}

type indexUpdate struct {
	path   []byte
	mode   review.FileMode
	object review.ObjectID
	remove bool
}

func (service *Service) synthesizeRawWorktree(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
	contents worktreeContents,
	workspace string,
) (review.ObjectID, error) {
	liveIndex, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{"ls-files", "--stage", "-z"},
	)
	if err != nil {
		return "", fmt.Errorf("read live Git index: %w", err)
	}
	if err := service.rejectWorktreeIndexFlags(ctx, root, environment); err != nil {
		return "", err
	}
	privateEnvironment, err := initializePrivateIndex(
		service,
		ctx,
		root,
		environment,
		workspace,
		liveIndex,
	)
	if err != nil {
		return "", err
	}

	changed, err := service.worktreeChanges(ctx, root, environment)
	if err != nil {
		return "", err
	}
	untracked := []byte(nil)
	if contents == worktreeWithUntracked {
		untracked, err = service.worktreeInventory(
			ctx,
			root,
			environment,
			[]string{"ls-files", "--others", "-z", "--"},
		)
		if err != nil {
			return "", fmt.Errorf("read untracked files: %w", err)
		}
	}

	raw, removals, paths, err := prepareWorktreeCandidates(changed, untracked)
	if err != nil {
		return "", err
	}
	if err := service.rejectWorktreeFilterDrivers(ctx, root, environment, paths); err != nil {
		return "", err
	}
	raw, err = stageRawWorktreeEntries(ctx, root, workspace, raw)
	if err != nil {
		return "", err
	}
	raw, err = service.resolveRawWorktreeGitlinks(ctx, root, environment, raw)
	if err != nil {
		return "", err
	}
	if err := service.rejectWorktreeFilterDrivers(ctx, root, environment, paths); err != nil {
		return "", err
	}

	currentIndex, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{"ls-files", "--stage", "-z"},
	)
	if err != nil {
		return "", fmt.Errorf("verify live Git index: %w", err)
	}
	if !bytes.Equal(liveIndex, currentIndex) {
		return "", errors.New("live Git index changed while snapshotting worktree")
	}
	if contents == worktreeWithUntracked {
		currentUntracked, err := service.worktreeInventory(
			ctx,
			root,
			environment,
			[]string{"ls-files", "--others", "-z", "--"},
		)
		if err != nil {
			return "", fmt.Errorf("verify untracked files: %w", err)
		}
		if !bytes.Equal(untracked, currentUntracked) {
			return "", errors.New("untracked file inventory changed while snapshotting worktree")
		}
	}

	updates, err := service.hashRawWorktreeEntries(
		ctx,
		root,
		privateEnvironment,
		raw,
	)
	if err != nil {
		return "", err
	}
	updates = append(updates, removals...)
	slices.SortFunc(updates, func(left, right indexUpdate) int {
		return bytes.Compare(left.path, right.path)
	})
	if err := service.applyIndexUpdates(
		ctx,
		root,
		privateEnvironment,
		base,
		updates,
	); err != nil {
		return "", err
	}
	tree, err := service.writeIndexTree(ctx, root, privateEnvironment)
	if err != nil {
		return "", err
	}
	if err := service.verifyRawWorktreeGitlinks(ctx, root, environment, raw); err != nil {
		return "", err
	}
	return tree, nil
}

func (service *Service) rejectWorktreeIndexFlags(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
) error {
	inventory, err := service.worktreeInventory(
		ctx,
		root,
		environment,
		[]string{"ls-files", "-v", "-z", "--"},
	)
	if err != nil {
		return fmt.Errorf("inspect worktree index flags: %w", err)
	}
	for len(inventory) != 0 {
		end := bytes.IndexByte(inventory, 0)
		if end < 0 {
			return errors.New("worktree index flag entry is not NUL terminated")
		}
		entry := inventory[:end]
		inventory = inventory[end+1:]
		if len(entry) < 3 || entry[1] != ' ' {
			return errors.New("worktree index flag entry is malformed")
		}
		if entry[0] >= 'a' && entry[0] <= 'z' || entry[0] == 'S' {
			return errors.New(
				"assume-unchanged and skip-worktree paths cannot be snapshotted safely",
			)
		}
	}
	return nil
}

func initializePrivateIndex(
	service *Service,
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	workspace string,
	liveIndex []byte,
) (map[string]string, error) {
	privateEnvironment := cloneEnvironment(environment)
	indexPath := filepath.Join(workspace, "index")
	privateEnvironment["GIT_INDEX_FILE"] = indexPath
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("prepare private worktree index: %w", err)
	}
	if _, err := service.runGitWithInput(
		ctx,
		root.String(),
		privateEnvironment,
		[]string{"update-index", "-z", "--index-info"},
		liveIndex,
		nil,
		64<<10,
	); err != nil {
		return nil, fmt.Errorf("copy live Git index: %w", err)
	}
	return privateEnvironment, nil
}

func (service *Service) worktreeInventory(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	arguments []string,
) ([]byte, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		arguments,
		nil,
		maxWorktreeInventoryBytes,
	)
	if err != nil {
		return nil, err
	}
	return result.Stdout, nil
}

func (service *Service) worktreeChanges(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
) ([]diffRecord, error) {
	result, err := service.runGit(
		ctx,
		root.String(),
		environment,
		[]string{
			"diff-files", "--raw", "--no-abbrev", "-z", "--no-renames",
			"--no-ext-diff", "--no-textconv",
		},
		nil,
		maxWorktreeInventoryBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("read worktree changes: %w", err)
	}
	records, err := parseRawDiff(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse worktree changes: %w", err)
	}
	return records, nil
}

func prepareWorktreeCandidates(
	changed []diffRecord,
	untracked []byte,
) ([]rawWorktreeEntry, []indexUpdate, [][]byte, error) {
	raw := make([]rawWorktreeEntry, 0, len(changed))
	removals := make([]indexUpdate, 0)
	paths := make([][]byte, 0, len(changed))
	seen := make(map[string]struct{}, len(changed))
	for _, record := range changed {
		path := record.newPath
		if record.status == 'D' {
			path = record.oldPath
		}
		if err := validateRepositoryRelativePath(path); err != nil {
			return nil, nil, nil, err
		}
		key := string(path)
		if _, exists := seen[key]; exists {
			return nil, nil, nil, fmt.Errorf("worktree inventory repeats path %q", path)
		}
		seen[key] = struct{}{}
		owned := bytes.Clone(path)
		paths = append(paths, owned)
		if record.status == 'D' {
			removals = append(removals, indexUpdate{path: owned, remove: true})
			continue
		}
		raw = append(raw, rawWorktreeEntry{path: owned, mode: record.newMode})
	}

	for len(untracked) > 0 {
		path, remaining, ok := nextNULTerminated(untracked)
		if !ok {
			return nil, nil, nil, errors.New("untracked file inventory is not NUL-terminated")
		}
		untracked = remaining
		if err := validateRepositoryRelativePath(path); err != nil {
			return nil, nil, nil, err
		}
		key := string(path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		owned := bytes.Clone(path)
		paths = append(paths, owned)
		raw = append(raw, rawWorktreeEntry{path: owned})
	}
	if len(seen) > review.MaxFiles {
		return nil, nil, nil, fmt.Errorf("worktree paths exceed %d", review.MaxFiles)
	}
	slices.SortFunc(paths, bytes.Compare)
	return raw, removals, paths, nil
}

func validateRepositoryRelativePath(path []byte) error {
	if err := validateRawPath(path); err != nil {
		return err
	}
	if path[0] == '/' {
		return errors.New("Git path is absolute")
	}
	for component := range bytes.SplitSeq(path, []byte{'/'}) {
		if len(component) == 0 || bytes.Equal(component, []byte(".")) ||
			bytes.Equal(component, []byte("..")) {
			return errors.New("Git path contains an invalid component")
		}
	}
	return nil
}

func (service *Service) rejectWorktreeFilterDrivers(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	paths [][]byte,
) error {
	for start := 0; start < len(paths); {
		input := make([]byte, 0, indexUpdateBatchBytes)
		end := start
		for end < len(paths) &&
			len(input)+len(paths[end])+1 <= indexUpdateBatchBytes {
			input = append(input, paths[end]...)
			input = append(input, 0)
			end++
		}
		if end == start {
			return errors.New("worktree filter path exceeds batch capacity")
		}
		result, err := service.runGitWithInput(
			ctx,
			root.String(),
			environment,
			[]string{"check-attr", "-z", "--stdin", "filter"},
			input,
			nil,
			int64(len(input))*3+64,
		)
		if err != nil {
			return fmt.Errorf("inspect Git filter attributes: %w", err)
		}
		if err := validateFilterAttributeOutput(result.Stdout, paths[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func validateFilterAttributeOutput(output []byte, paths [][]byte) error {
	for index, path := range paths {
		actualPath, rest, ok := nextNULTerminated(output)
		if !ok {
			return errors.New("Git filter attribute output omits a path")
		}
		attribute, rest, ok := nextNULTerminated(rest)
		if !ok {
			return errors.New("Git filter attribute output omits an attribute")
		}
		value, rest, ok := nextNULTerminated(rest)
		if !ok {
			return errors.New("Git filter attribute output omits a value")
		}
		output = rest
		if !bytes.Equal(actualPath, path) || !bytes.Equal(attribute, []byte("filter")) {
			return fmt.Errorf("Git returned an unexpected filter attribute at path %d", index+1)
		}
		if !bytes.Equal(value, []byte("unspecified")) &&
			!bytes.Equal(value, []byte("unset")) {
			return errors.New(
				"worktree files using Git clean/process filters cannot be snapshotted safely",
			)
		}
	}
	if len(output) != 0 {
		return errors.New("Git filter attribute output contains extra records")
	}
	return nil
}

func stageRawWorktreeEntries(
	ctx context.Context,
	root RepositoryPath,
	workspace string,
	entries []rawWorktreeEntry,
) ([]rawWorktreeEntry, error) {
	staging := filepath.Join(workspace, "raw-blobs")
	if strings.ContainsAny(staging, "\r\n") {
		return nil, errors.New("raw blob staging path contains a line break")
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create raw blob staging directory: %w", err)
	}
	var total int64
	for index := range entries {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		body, mode, err := readRawWorktreePath(
			filepath.Join(root.String(), string(entries[index].path)),
			entries[index].mode,
		)
		if err != nil {
			return nil, fmt.Errorf("read worktree path %q: %w", entries[index].path, err)
		}
		if mode.String() == "160000" {
			entries[index].file = ""
			continue
		}
		total += int64(len(body))
		if total > maxWorktreeRawBytes {
			return nil, fmt.Errorf(
				"raw worktree bytes exceed %d",
				maxWorktreeRawBytes,
			)
		}
		name := "blob-" + strconv.Itoa(index)
		file, err := os.OpenFile(
			filepath.Join(staging, name),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			return nil, fmt.Errorf("create raw blob staging file: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("write raw blob staging file: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close raw blob staging file: %w", err)
		}
		entries[index].mode = mode
		entries[index].file = filepath.Join(staging, name)
	}
	return entries, nil
}

func readRawWorktreePath(
	path string,
	indexMode review.FileMode,
) ([]byte, review.FileMode, error) {
	before, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, review.FileMode(""), errors.New("worktree path disappeared")
		}
		return nil, review.FileMode(""), err
	}
	switch {
	case before.Mode().IsRegular():
		descriptor, err := unix.Open(
			path,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0,
		)
		if err != nil {
			return nil, review.FileMode(""), err
		}
		file := os.NewFile(uintptr(descriptor), path)
		defer file.Close()
		opened, err := file.Stat()
		if err != nil {
			return nil, review.FileMode(""), err
		}
		if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
			return nil, review.FileMode(""), errors.New("worktree path changed while opening")
		}
		body, err := io.ReadAll(io.LimitReader(file, maxWorktreeRawBytes+1))
		if err != nil {
			return nil, review.FileMode(""), err
		}
		if int64(len(body)) > maxWorktreeRawBytes {
			return nil, review.FileMode(""), errors.New("worktree file exceeds raw byte limit")
		}
		after, err := file.Stat()
		if err != nil {
			return nil, review.FileMode(""), err
		}
		current, err := os.Lstat(path)
		if err != nil || !sameWorktreeFile(opened, after) ||
			!sameWorktreeFile(opened, current) {
			return nil, review.FileMode(""), errors.New("worktree file changed while reading")
		}
		mode := "100644"
		if before.Mode()&0o111 != 0 {
			mode = "100755"
		}
		if indexMode.String() == "100644" || indexMode.String() == "100755" {
			mode = indexMode.String()
		}
		validated, err := review.NewFileMode(mode)
		return body, validated, err
	case before.Mode()&os.ModeSymlink != 0:
		body, err := os.Readlink(path)
		if err != nil {
			return nil, review.FileMode(""), err
		}
		after, err := os.Lstat(path)
		if err != nil || !sameWorktreeFile(before, after) {
			return nil, review.FileMode(""), errors.New("worktree symlink changed while reading")
		}
		mode, err := review.NewFileMode("120000")
		return []byte(body), mode, err
	case before.IsDir() && indexMode.String() == "160000":
		return nil, indexMode, nil
	default:
		return nil, review.FileMode(""), errors.New("worktree path has an unsupported file type")
	}
}

func sameWorktreeFile(left, right os.FileInfo) bool {
	return right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() &&
		left.Size() == right.Size() &&
		left.ModTime() == right.ModTime()
}

func (service *Service) hashRawWorktreeEntries(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	entries []rawWorktreeEntry,
) ([]indexUpdate, error) {
	var input []byte
	count := 0
	for _, entry := range entries {
		if entry.file == "" {
			continue
		}
		input = append(input, entry.file...)
		input = append(input, '\n')
		count++
	}
	var lines []string
	if count != 0 {
		result, err := service.runGitWithInput(
			ctx,
			root.String(),
			environment,
			[]string{"hash-object", "--no-filters", "-w", "--stdin-paths"},
			input,
			nil,
			int64(count)*80,
		)
		if err != nil {
			return nil, fmt.Errorf("hash raw worktree blobs: %w", err)
		}
		lines = strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n")
		if len(lines) != count {
			return nil, fmt.Errorf(
				"Git returned %d raw blob identities, want %d",
				len(lines),
				count,
			)
		}
	}
	updates := make([]indexUpdate, 0, len(entries))
	line := 0
	for _, entry := range entries {
		if entry.oid != nil {
			updates = append(updates, indexUpdate{
				path:   entry.path,
				mode:   entry.mode,
				object: *entry.oid,
			})
			continue
		}
		if entry.file == "" {
			continue
		}
		object, err := review.NewObjectID(lines[line])
		if err != nil {
			return nil, fmt.Errorf("validate raw blob identity: %w", err)
		}
		updates = append(updates, indexUpdate{
			path:   entry.path,
			mode:   entry.mode,
			object: object,
		})
		line++
	}
	return updates, nil
}

func (service *Service) resolveRawWorktreeGitlinks(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	entries []rawWorktreeEntry,
) ([]rawWorktreeEntry, error) {
	for index := range entries {
		if entries[index].mode.String() != "160000" {
			continue
		}
		oid, err := service.worktreeGitlinkHead(
			ctx,
			root,
			environment,
			entries[index].path,
		)
		if err != nil {
			return nil, err
		}
		entries[index].oid = new(oid)
	}
	return entries, nil
}

func (service *Service) verifyRawWorktreeGitlinks(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	entries []rawWorktreeEntry,
) error {
	for _, entry := range entries {
		if entry.oid == nil {
			continue
		}
		current, err := service.worktreeGitlinkHead(ctx, root, environment, entry.path)
		if err != nil {
			return err
		}
		if current != *entry.oid {
			return fmt.Errorf("submodule %q changed while snapshotting worktree", entry.path)
		}
	}
	return nil
}

func (service *Service) worktreeGitlinkHead(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	path []byte,
) (review.ObjectID, error) {
	directory := filepath.Join(root.String(), string(path))
	gitlinkEnvironment := cloneEnvironment(environment)
	delete(gitlinkEnvironment, "GIT_OBJECT_DIRECTORY")
	delete(gitlinkEnvironment, "GIT_ALTERNATE_OBJECT_DIRECTORIES")
	delete(gitlinkEnvironment, "GIT_INDEX_FILE")
	result, err := service.runGit(
		ctx,
		directory,
		gitlinkEnvironment,
		[]string{"rev-parse", "--verify", "HEAD^{commit}"},
		nil,
		128,
	)
	if err != nil {
		return "", fmt.Errorf("resolve initialized submodule %q HEAD: %w", path, err)
	}
	value := strings.TrimSuffix(string(result.Stdout), "\n")
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("submodule %q returned multiple HEAD records", path)
	}
	oid, err := review.NewObjectID(value)
	if err != nil {
		return "", fmt.Errorf("validate submodule %q HEAD: %w", path, err)
	}
	return oid, nil
}

func (service *Service) applyIndexUpdates(
	ctx context.Context,
	root RepositoryPath,
	environment map[string]string,
	base review.ObjectID,
	updates []indexUpdate,
) error {
	zero := strings.Repeat("0", len(base.String()))
	for start := 0; start < len(updates); {
		input := make([]byte, 0, indexUpdateBatchBytes)
		end := start
		for end < len(updates) {
			update := updates[end]
			record := make([]byte, 0, len(update.path)+96)
			if update.remove {
				record = append(record, "0 "...)
				record = append(record, zero...)
			} else {
				record = append(record, update.mode.String()...)
				record = append(record, ' ')
				record = append(record, update.object.String()...)
			}
			record = append(record, '\t')
			record = append(record, update.path...)
			record = append(record, 0)
			if len(input)+len(record) > indexUpdateBatchBytes && len(input) != 0 {
				break
			}
			input = append(input, record...)
			end++
		}
		if _, err := service.runGitWithInput(
			ctx,
			root.String(),
			environment,
			[]string{"update-index", "-z", "--index-info"},
			input,
			nil,
			64<<10,
		); err != nil {
			return fmt.Errorf("update private worktree index: %w", err)
		}
		start = end
	}
	return nil
}
