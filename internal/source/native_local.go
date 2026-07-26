package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/jacoelho/rvw/internal/change"
)

type nativeIndexGroup struct {
	path    string
	entries []streamedIndexEntry
}

type nativeTreeCursor struct {
	iterator *nativeTreeIterator
	entry    nativeTreeEntry
	valid    bool
	done     bool
}

func (cursor *nativeTreeCursor) advance(ctx context.Context) error {
	if cursor.done {
		return nil
	}
	entry, err := cursor.iterator.next(ctx)
	if errors.Is(err, io.EOF) {
		cursor.valid = false
		cursor.done = true
		return nil
	}
	if err != nil {
		return err
	}
	cursor.entry = entry
	cursor.valid = true
	return nil
}

func nativeLocalRecords(
	ctx context.Context,
	session *repositorySession,
	target Target,
	options OpenOptions,
) (change.ObjectID, change.ObjectID, []diffRecord, map[string]change.FileSemanticIdentity, error) {
	if session == nil {
		return "", "", nil, nil, errors.New("repository session is nil")
	}
	switch target.Kind() {
	case TargetRange:
		baseRef, headRef, ok := target.Range()
		if !ok {
			return "", "", nil, nil, fmt.Errorf("%w: missing range", ErrInvalidTarget)
		}
		baseHash, err := session.resolveTreeish(ctx, baseRef)
		if err != nil {
			return "", "", nil, nil, fmt.Errorf("resolve range base: %w", err)
		}
		headHash, err := session.resolveTreeish(ctx, headRef)
		if err != nil {
			return "", "", nil, nil, fmt.Errorf("resolve range head: %w", err)
		}
		records, err := nativeTreeDiff(ctx, session, baseHash, headHash)
		if err != nil {
			return "", "", nil, nil, err
		}
		base, err := hashObjectID(baseHash)
		if err != nil {
			return "", "", nil, nil, err
		}
		head, err := hashObjectID(headHash)
		return base, head, records, nil, err
	case TargetStaged, TargetWorktree:
		baseHash, err := session.resolveHeadTree(ctx)
		if err != nil {
			return "", "", nil, nil, fmt.Errorf("resolve HEAD tree: %w", err)
		}
		base, err := hashObjectID(baseHash)
		if err != nil {
			return "", "", nil, nil, err
		}
		indexPath, err := session.captureIndexCopy(ctx)
		if err != nil {
			return "", "", nil, nil, err
		}
		records, unmerged, err := nativeIndexProjection(
			ctx, session, baseHash, indexPath, target.Kind(), options.IncludeUntracked,
		)
		if indexPath != "" {
			if removeErr := os.Remove(indexPath); removeErr != nil &&
				!errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove copied Git index: %w", removeErr))
			}
		}
		return base, base, records, unmerged, err
	default:
		return "", "", nil, nil, fmt.Errorf("%w: target is not local", ErrInvalidTarget)
	}
}

func nativeTreeDiff(
	ctx context.Context,
	session *repositorySession,
	oldTree, newTree plumbing.Hash,
) ([]diffRecord, error) {
	records, err := nativeTreeRecords(ctx, session, oldTree, newTree, change.MaxFiles)
	if err != nil {
		return nil, err
	}
	if populateErr := populateNativeRecords(ctx, session, records); populateErr != nil {
		return nil, populateErr
	}
	records = enrichExactNativeRenames(records)
	if nativeSimilarityCandidateCount(records) <= 1_000 {
		records, err = enrichInexactNativeRenames(ctx, session, records)
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}

func nativeTreeRecords(
	ctx context.Context,
	session *repositorySession,
	oldTree, newTree plumbing.Hash,
	recordLimit int,
) ([]diffRecord, error) {
	records := make([]diffRecord, 0, min(recordLimit, 128))
	err := walkNativeTreeDiff(ctx, session, oldTree, newTree, recordLimit, func(record diffRecord) error {
		records = append(records, record)
		return nil
	})
	return records, err
}

func walkNativeTreeDiff(
	ctx context.Context,
	session *repositorySession,
	oldTree, newTree plumbing.Hash,
	recordLimit int,
	consume func(diffRecord) error,
) error {
	if recordLimit < 0 || consume == nil {
		return errors.New("native tree diff has invalid admission")
	}
	oldIterator, err := newNativeTreeIterator(ctx, session, oldTree)
	if err != nil {
		return err
	}
	newIterator, err := newNativeTreeIterator(ctx, session, newTree)
	if err != nil {
		return err
	}
	oldCursor := nativeTreeCursor{iterator: oldIterator}
	newCursor := nativeTreeCursor{iterator: newIterator}
	if err := oldCursor.advance(ctx); err != nil {
		return err
	}
	if err := newCursor.advance(ctx); err != nil {
		return err
	}
	records := 0
	for oldCursor.valid || newCursor.valid {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		var record *diffRecord
		switch {
		case !newCursor.valid || oldCursor.valid && oldCursor.entry.path < newCursor.entry.path:
			value, err := nativeRecord(&oldCursor.entry, nil)
			if err != nil {
				return err
			}
			record = new(value)
			if err := oldCursor.advance(ctx); err != nil {
				return err
			}
		case !oldCursor.valid || newCursor.entry.path < oldCursor.entry.path:
			value, err := nativeRecord(nil, &newCursor.entry)
			if err != nil {
				return err
			}
			record = new(value)
			if err := newCursor.advance(ctx); err != nil {
				return err
			}
		default:
			if oldCursor.entry.oid != newCursor.entry.oid || oldCursor.entry.mode != newCursor.entry.mode {
				value, err := nativeRecord(&oldCursor.entry, &newCursor.entry)
				if err != nil {
					return err
				}
				record = new(value)
			}
			if err := oldCursor.advance(ctx); err != nil {
				return err
			}
			if err := newCursor.advance(ctx); err != nil {
				return err
			}
		}
		if record == nil {
			continue
		}
		records++
		if records > recordLimit {
			return &LimitError{Resource: LimitPullRequestRawRecords, Limit: uint64(recordLimit)}
		}
		if err := consume(*record); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gocognit,cyclop,funlen,maintidx // The streaming three-way merge keeps tree, index stages, and worktree ownership in one pass.
func nativeIndexProjection(
	ctx context.Context,
	session *repositorySession,
	baseTree plumbing.Hash,
	indexPath string,
	target TargetKind,
	includeUntracked bool,
) (_ []diffRecord, _ map[string]change.FileSemanticIdentity, returnErr error) {
	iterator, err := newNativeTreeIterator(ctx, session, baseTree)
	if err != nil {
		return nil, nil, err
	}
	cursor := nativeTreeCursor{iterator: iterator}
	if advanceErr := cursor.advance(ctx); advanceErr != nil {
		return nil, nil, advanceErr
	}
	records := make([]diffRecord, 0, 128)
	unmerged := make(map[string]change.FileSemanticIdentity)
	var tracked *trackedPathSet
	if target == TargetWorktree && includeUntracked {
		workspace, workspaceErr := session.ensureWorkspace()
		if workspaceErr != nil {
			return nil, nil, workspaceErr
		}
		tracked, err = newTrackedPathSet(workspace, indexPath)
		if err != nil {
			return nil, nil, err
		}
		defer func() { returnErr = errors.Join(returnErr, tracked.Close()) }()
	}
	group := nativeIndexGroup{entries: make([]streamedIndexEntry, 0, 4)}
	flush := func() error {
		if group.path == "" {
			return nil
		}
		if validationErr := validateNativeIndexGroup(group); validationErr != nil {
			return validationErr
		}
		if tracked != nil {
			if trackedErr := tracked.add(group.path); trackedErr != nil {
				return trackedErr
			}
		}
		for cursor.valid && cursor.entry.path < group.path {
			record, recordErr := nativeRecord(&cursor.entry, nil)
			if recordErr != nil {
				return recordErr
			}
			records = append(records, record)
			if advanceErr := cursor.advance(ctx); advanceErr != nil {
				return advanceErr
			}
		}
		var base *nativeTreeEntry
		if cursor.valid && cursor.entry.path == group.path {
			base = new(cursor.entry)
			if advanceErr := cursor.advance(ctx); advanceErr != nil {
				return advanceErr
			}
		}
		var record *diffRecord
		var conflict bool
		if target == TargetStaged {
			record, conflict, err = stagedIndexGroupRecord(base, group)
		} else {
			record, conflict, err = worktreeIndexGroupRecord(ctx, session, base, group)
		}
		if err != nil {
			return err
		}
		if record != nil {
			if conflict {
				if record.unmerged == nil {
					return errors.New("unmerged index record has no stage tuple")
				}
				unmerged[group.path] = *record.unmerged
			}
			records = append(records, *record)
		}
		group.path = ""
		group.entries = group.entries[:0]
		if len(records) > change.MaxFiles {
			return &LimitError{Resource: LimitChangedFiles, Limit: change.MaxFiles}
		}
		return nil
	}
	_, err = streamIndex(ctx, indexPath, func(entry streamedIndexEntry) error {
		if group.path != "" && group.path != entry.path {
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}
		}
		if group.path == "" {
			group.path = entry.path
		}
		group.entries = append(group.entries, entry)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if flushErr := flush(); flushErr != nil {
		return nil, nil, flushErr
	}
	for cursor.valid {
		record, recordErr := nativeRecord(&cursor.entry, nil)
		if recordErr != nil {
			return nil, nil, recordErr
		}
		records = append(records, record)
		if len(records) > change.MaxFiles {
			return nil, nil, &LimitError{Resource: LimitChangedFiles, Limit: change.MaxFiles}
		}
		if advanceErr := cursor.advance(ctx); advanceErr != nil {
			return nil, nil, advanceErr
		}
	}
	if target == TargetWorktree && includeUntracked {
		if untrackedErr := appendNativeUntracked(ctx, session, tracked, &records); untrackedErr != nil {
			return nil, nil, untrackedErr
		}
	}
	if populateErr := populateNativeRecords(ctx, session, records); populateErr != nil {
		return nil, nil, populateErr
	}
	records = enrichExactNativeRenames(records)
	if nativeSimilarityCandidateCount(records) <= 1_000 {
		records, err = enrichInexactNativeRenames(ctx, session, records)
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(records, func(left, right int) bool {
		return primaryPath(records[left]) < primaryPath(records[right])
	})
	return records, unmerged, nil
}

func stagedIndexGroupRecord(
	base *nativeTreeEntry,
	group nativeIndexGroup,
) (*diffRecord, bool, error) {
	if len(group.entries) == 1 && group.entries[0].stage == 0 {
		entry := group.entries[0]
		if entry.intentToAdd {
			return nil, false, nil
		}
		final, err := nativeTreeEntryFromIndex(entry)
		if err != nil {
			return nil, false, err
		}
		if nativeEntriesEqual(base, &final) {
			return nil, false, nil
		}
		record, err := nativeRecord(base, &final)
		return &record, false, err
	}
	record, err := stagedConflictRecord(group)
	return record, true, err
}

func validateNativeIndexGroup(group nativeIndexGroup) error {
	if group.path == "" || len(group.entries) == 0 {
		return errors.New("git index path group is empty")
	}
	if group.entries[0].stage == 0 {
		if len(group.entries) != 1 {
			return errors.New("git index path mixes stage zero with unmerged stages")
		}
		return nil
	}
	var seen uint8
	for _, entry := range group.entries {
		if entry.path != group.path || entry.stage < 1 || entry.stage > 3 {
			return errors.New("git index path has an invalid unmerged stage")
		}
		bit := uint8(1 << (entry.stage - 1))
		if seen&bit != 0 {
			return errors.New("git index path has a duplicate unmerged stage")
		}
		seen |= bit
	}
	return nil
}

func worktreeIndexGroupRecord(
	ctx context.Context,
	session *repositorySession,
	base *nativeTreeEntry,
	group nativeIndexGroup,
) (*diffRecord, bool, error) {
	for _, entry := range group.entries {
		if entry.assumeValid || entry.skipWorktree {
			return nil, false, errors.New(
				"assume-unchanged and skip-worktree paths cannot be snapshotted safely",
			)
		}
	}
	if len(group.entries) == 1 && group.entries[0].stage == 0 {
		entry := group.entries[0]
		mode, err := indexEntryMode(entry)
		if err != nil {
			return nil, false, err
		}
		hash, actualMode, present, err := session.captureWorktreeContent(ctx, group.path, mode)
		if err != nil {
			return nil, false, err
		}
		var final *nativeTreeEntry
		if present {
			final = &nativeTreeEntry{path: group.path, oid: hash, mode: actualMode}
		}
		if nativeEntriesEqual(base, final) {
			return nil, false, nil
		}
		record, err := nativeRecord(base, final)
		return &record, false, err
	}
	mode, err := preferredConflictMode(group.entries)
	if err != nil {
		return nil, false, err
	}
	hash, actualMode, present, captureErr := session.captureWorktreeContent(ctx, group.path, mode)
	if captureErr != nil && !strings.Contains(captureErr.Error(), "disappeared") {
		return nil, false, captureErr
	}
	var final *nativeTreeEntry
	if present {
		final = &nativeTreeEntry{path: group.path, oid: hash, mode: actualMode}
	}
	identity, err := nativeUnmergedIdentity(group)
	if err != nil {
		return nil, false, err
	}
	if base == nil && final == nil {
		record, conflictErr := stagedConflictRecord(group)
		return record, true, conflictErr
	}
	record, err := nativeRecord(base, final)
	if err == nil {
		record.additions = 0
		record.deletions = 0
		record.unmerged = new(identity)
	}
	return &record, true, err
}

func stagedConflictRecord(group nativeIndexGroup) (*diffRecord, error) {
	identity, err := nativeUnmergedIdentity(group)
	if err != nil {
		return nil, err
	}
	var oldEntry, newEntry *nativeTreeEntry
	for _, preferred := range []uint8{1, 2, 3} {
		for _, entry := range group.entries {
			if entry.stage != preferred {
				continue
			}
			converted, conversionErr := nativeTreeEntryFromIndex(entry)
			if conversionErr != nil {
				return nil, conversionErr
			}
			if oldEntry == nil {
				oldEntry = new(converted)
			}
			if preferred == 3 || newEntry == nil {
				newEntry = new(converted)
			}
		}
	}
	if oldEntry == nil || newEntry == nil {
		return nil, errors.New("unmerged index path has no semantic stages")
	}
	record, err := nativeRecord(oldEntry, newEntry)
	if err != nil {
		return nil, err
	}
	record.status = 'U'
	record.additions = 0
	record.deletions = 0
	record.unmerged = new(identity)
	return &record, nil
}

func nativeUnmergedIdentity(group nativeIndexGroup) (change.FileSemanticIdentity, error) {
	stages := make([]change.FileSemanticStage, 0, len(group.entries))
	for _, entry := range group.entries {
		if entry.stage == 0 {
			return change.FileSemanticIdentity{}, errors.New("stage-zero index entry is not unmerged")
		}
		mode, err := indexEntryMode(entry)
		if err != nil {
			return change.FileSemanticIdentity{}, err
		}
		oid, err := hashObjectID(entry.oid)
		if err != nil {
			return change.FileSemanticIdentity{}, err
		}
		object, err := change.NewGitObject(objectKind(mode), oid, mode)
		if err != nil {
			return change.FileSemanticIdentity{}, err
		}
		stage, err := change.NewFileSemanticStage(entry.stage, object)
		if err != nil {
			return change.FileSemanticIdentity{}, err
		}
		stages = append(stages, stage)
	}
	return change.NewFileSemanticIdentity(stages...)
}

func nativeTreeEntryFromIndex(entry streamedIndexEntry) (nativeTreeEntry, error) {
	mode, err := indexEntryMode(entry)
	if err != nil {
		return nativeTreeEntry{}, err
	}
	return nativeTreeEntry{path: entry.path, oid: entry.oid, mode: mode}, nil
}

func indexEntryMode(entry streamedIndexEntry) (change.FileMode, error) {
	return change.NewFileMode(fmt.Sprintf("%06o", entry.mode))
}

func preferredConflictMode(entries []streamedIndexEntry) (change.FileMode, error) {
	for _, preferred := range []uint8{2, 3, 1} {
		for _, entry := range entries {
			if entry.stage == preferred {
				return indexEntryMode(entry)
			}
		}
	}
	return "", errors.New("unmerged index path has no stages")
}

func nativeEntriesEqual(left, right *nativeTreeEntry) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.path == right.path && left.oid == right.oid && left.mode == right.mode
}

func nativeRecord(oldEntry, newEntry *nativeTreeEntry) (diffRecord, error) {
	zeroMode, err := change.NewFileMode("000000")
	if err != nil {
		return diffRecord{}, err
	}
	record := diffRecord{oldMode: zeroMode, newMode: zeroMode}
	switch {
	case oldEntry == nil && newEntry != nil:
		oid, err := hashObjectID(newEntry.oid)
		if err != nil {
			return diffRecord{}, err
		}
		record.newOID = new(oid)
		record.newMode = newEntry.mode
		record.newPath = newEntry.path
		record.status = 'A'
	case oldEntry != nil && newEntry == nil:
		oid, err := hashObjectID(oldEntry.oid)
		if err != nil {
			return diffRecord{}, err
		}
		record.oldOID = new(oid)
		record.oldMode = oldEntry.mode
		record.oldPath = oldEntry.path
		record.status = 'D'
	case oldEntry != nil && newEntry != nil:
		oldOID, err := hashObjectID(oldEntry.oid)
		if err != nil {
			return diffRecord{}, err
		}
		newOID, err := hashObjectID(newEntry.oid)
		if err != nil {
			return diffRecord{}, err
		}
		record.oldOID = new(oldOID)
		record.newOID = new(newOID)
		record.oldMode = oldEntry.mode
		record.newMode = newEntry.mode
		record.oldPath = oldEntry.path
		record.newPath = newEntry.path
		record.status = 'M'
		if objectKind(oldEntry.mode) != objectKind(newEntry.mode) {
			record.status = 'T'
		}
	default:
		return diffRecord{}, errors.New("native diff record has no sides")
	}
	return record, nil
}

func populateNativeRecords(
	ctx context.Context,
	session *repositorySession,
	records []diffRecord,
) error {
	for index := range records {
		if err := pinNativeRecordSides(ctx, session, &records[index]); err != nil {
			return err
		}
		if records[index].unmerged == nil {
			if err := populateNativeDiff(ctx, session, &records[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func enrichExactNativeRenames(records []diffRecord) []diffRecord {
	type exactKey struct {
		oid  change.ObjectID
		kind change.ObjectKind
	}
	deletions := make(map[exactKey][]int)
	surviving := make(map[exactKey][]int)
	for index, record := range records {
		if record.unmerged != nil || record.oldOID == nil {
			continue
		}
		key := exactKey{oid: *record.oldOID, kind: objectKind(record.oldMode)}
		if record.status == 'D' {
			deletions[key] = append(deletions[key], index)
		} else {
			surviving[key] = append(surviving[key], index)
		}
	}
	removed := make([]bool, len(records))
	for index := range records {
		record := &records[index]
		if record.unmerged != nil || record.status != 'A' || record.newOID == nil {
			continue
		}
		key := exactKey{oid: *record.newOID, kind: objectKind(record.newMode)}
		if sources := deletions[key]; len(sources) != 0 {
			source := sources[0]
			deletions[key] = sources[1:]
			old := records[source]
			record.oldOID = old.oldOID
			record.oldMode = old.oldMode
			record.oldPath = old.oldPath
			record.status = 'R'
			record.similarity = 60_000
			record.additions = 0
			record.deletions = 0
			removed[source] = true
			continue
		}
		if sources := surviving[key]; len(sources) != 0 {
			old := records[sources[0]]
			record.oldOID = old.oldOID
			record.oldMode = old.oldMode
			record.oldPath = old.oldPath
			record.status = 'C'
			record.similarity = 60_000
			record.additions = 0
			record.deletions = 0
		}
	}
	result := records[:0]
	for index, record := range records {
		if !removed[index] {
			result = append(result, record)
		}
	}
	return result
}

//nolint:gocognit // The visitor keeps traversal decisions and record mutation in one ordered pass.
func appendNativeUntracked(
	ctx context.Context,
	session *repositorySession,
	tracked *trackedPathSet,
	records *[]diffRecord,
) error {
	var embedded uint64
	return walkNativeWorktree(ctx, session, func(
		relative string,
		entry os.DirEntry,
		_ uint32,
	) (bool, error) {
		if relative == ".git" {
			return false, nil
		}
		if entry.IsDir() {
			trackedPath, trackedErr := tracked.contains(relative)
			if trackedErr != nil {
				return false, trackedErr
			}
			if trackedPath {
				return false, nil
			}
			commit, valid, probeErr := resolveNativeEmbeddedRepository(
				ctx,
				filepath.Join(session.path.String(), filepath.FromSlash(relative)),
			)
			if probeErr != nil {
				return false, probeErr
			}
			if valid {
				embedded++
				if embedded > 100_000 {
					return false, &LimitError{Resource: LimitEmbeddedRepositories, Limit: 100_000}
				}
				mode, modeErr := change.NewFileMode("160000")
				if modeErr != nil {
					return false, modeErr
				}
				newEntry := nativeTreeEntry{path: relative, oid: commit, mode: mode}
				record, recordErr := nativeRecord(nil, &newEntry)
				if recordErr != nil {
					return false, recordErr
				}
				prefix := relative + "/"
				retained := (*records)[:0]
				for _, existing := range *records {
					if !strings.HasPrefix(primaryPath(existing), prefix) {
						retained = append(retained, existing)
					}
				}
				retained = append(retained, record)
				*records = retained
				if len(*records) > change.MaxFiles {
					return false, &LimitError{Resource: LimitChangedFiles, Limit: change.MaxFiles}
				}
				return false, nil
			}
			return true, nil
		}
		exists, trackedErr := tracked.contains(relative)
		if trackedErr != nil {
			return false, trackedErr
		}
		if exists {
			return false, nil
		}
		mode, err := change.NewFileMode("100644")
		if err != nil {
			return false, err
		}
		hash, actualMode, present, err := session.captureWorktreeContent(ctx, relative, mode)
		if err != nil {
			return false, err
		}
		if !present {
			return false, nil
		}
		newEntry := nativeTreeEntry{path: relative, oid: hash, mode: actualMode}
		record, err := nativeRecord(nil, &newEntry)
		if err != nil {
			return false, err
		}
		*records = append(*records, record)
		if len(*records) > change.MaxFiles {
			return false, &LimitError{Resource: LimitChangedFiles, Limit: change.MaxFiles}
		}
		return false, nil
	})
}

func resolveNativeEmbeddedRepository(
	ctx context.Context,
	path string,
) (plumbing.Hash, bool, error) {
	marker := filepath.Join(path, ".git")
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		return plumbing.ZeroHash, false, nil
	} else if err != nil {
		return plumbing.ZeroHash, false, err
	}
	if _, _, err := resolveRepositoryLayout(path); err != nil {
		return plumbing.ZeroHash, false, nil
	}
	repositoryPath, err := NewRepositoryPath(path)
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	child, err := openRepositorySession(repositoryPath)
	if err != nil {
		return plumbing.ZeroHash, true, err
	}
	defer child.Close() //nolint:errcheck // Read-only child close cannot change the captured commit identity.
	commit, err := child.resolveCommitish(ctx, RevisionRef("HEAD"))
	if err != nil {
		return plumbing.ZeroHash, true, fmt.Errorf(
			"embedded repository %q HEAD does not resolve to a commit: %w", path, err,
		)
	}
	return commit, true, nil
}
