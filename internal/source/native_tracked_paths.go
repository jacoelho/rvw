package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"github.com/jacoelho/rvw/internal/change"
)

const trackedPathSlotBytes = 40

type trackedPathSet struct {
	table      *os.File
	paths      *os.File
	tablePath  string
	pathsPath  string
	pathOffset uint64
	slots      uint64
}

func newTrackedPathSet(workspace, indexPath string) (_ *trackedPathSet, returnErr error) {
	count, err := declaredIndexEntryCount(indexPath)
	if err != nil {
		return nil, err
	}
	if uint64(count) > uint64(maxIndexEncodedBytes/indexEntryHeaderBytes) {
		return nil, errors.New("git index entry count cannot fit its encoded size")
	}
	slots := uint64(2)
	for slots < max(uint64(count)*2, uint64(2)) {
		slots *= 2
	}
	table, err := os.CreateTemp(workspace, "tracked-path-table-")
	if err != nil {
		return nil, err
	}
	set := &trackedPathSet{table: table, tablePath: table.Name(), slots: slots}
	keep := false
	defer func() {
		if !keep {
			returnErr = errors.Join(returnErr, set.Close())
		}
	}()
	if truncateErr := table.Truncate(int64(slots * trackedPathSlotBytes)); truncateErr != nil {
		return nil, truncateErr
	}
	paths, err := os.CreateTemp(workspace, "tracked-path-data-")
	if err != nil {
		return nil, err
	}
	set.paths, set.pathsPath = paths, paths.Name()
	keep = true
	return set, nil
}

func declaredIndexEntryCount(path string) (uint32, error) {
	if path == "" {
		return 0, nil
	}
	file, err := os.Open(path) //nolint:gosec // path is the owner-created private index copy.
	if err != nil {
		return 0, err
	}
	var header [indexHeaderBytes]byte
	_, readErr := io.ReadFull(file, header[:])
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return 0, err
	}
	if string(header[:4]) != "DIRC" {
		return 0, errors.New("git index signature is invalid")
	}
	return binary.BigEndian.Uint32(header[8:12]), nil
}

func (set *trackedPathSet) add(path string) error {
	if set == nil || set.table == nil || set.paths == nil || set.slots == 0 || path == "" {
		return errors.New("tracked path set is unavailable")
	}
	digest := sha256.Sum256([]byte(path))
	start := binary.BigEndian.Uint64(digest[:8]) & (set.slots - 1)
	for probe := range set.slots {
		slot := (start + probe) & (set.slots - 1)
		entry, err := set.readSlot(slot)
		if err != nil {
			return err
		}
		if entry[0] != 0 {
			if bytes.Equal(entry[24:40], digest[:16]) {
				equal, compareErr := set.slotPathEqual(entry, path)
				if compareErr != nil || equal {
					return compareErr
				}
			}
			continue
		}
		if _, err := set.paths.WriteAt([]byte(path), int64(set.pathOffset)); err != nil { //nolint:gosec // Index path-byte admission bounds the offset.
			return err
		}
		entry[0] = 1
		binary.BigEndian.PutUint64(entry[8:16], set.pathOffset)
		binary.BigEndian.PutUint32(entry[16:20], uint32(len(path))) //nolint:gosec // Git path admission is below uint32.
		copy(entry[24:40], digest[:16])
		if _, err := set.table.WriteAt(entry[:], int64(slot*trackedPathSlotBytes)); err != nil { //nolint:gosec // Table admission bounds the offset.
			return err
		}
		set.pathOffset += uint64(len(path))
		return nil
	}
	return errors.New("tracked path table is full")
}

func (set *trackedPathSet) contains(path string) (bool, error) {
	if set == nil || set.table == nil || set.paths == nil || set.slots == 0 || path == "" {
		return false, nil
	}
	digest := sha256.Sum256([]byte(path))
	start := binary.BigEndian.Uint64(digest[:8]) & (set.slots - 1)
	for probe := range set.slots {
		slot := (start + probe) & (set.slots - 1)
		entry, err := set.readSlot(slot)
		if err != nil {
			return false, err
		}
		if entry[0] == 0 {
			return false, nil
		}
		if !bytes.Equal(entry[24:40], digest[:16]) {
			continue
		}
		equal, err := set.slotPathEqual(entry, path)
		if err != nil || equal {
			return equal, err
		}
	}
	return false, errors.New("tracked path table has no empty slot")
}

func (set *trackedPathSet) readSlot(slot uint64) ([trackedPathSlotBytes]byte, error) {
	var entry [trackedPathSlotBytes]byte
	_, err := set.table.ReadAt(entry[:], int64(slot*trackedPathSlotBytes)) //nolint:gosec // Table admission bounds the offset.
	return entry, err
}

func (set *trackedPathSet) slotPathEqual(
	entry [trackedPathSlotBytes]byte,
	path string,
) (bool, error) {
	length := binary.BigEndian.Uint32(entry[16:20])
	if uint64(length) != uint64(len(path)) {
		return false, nil
	}
	if int64(length) > change.MaxPathBytes {
		return false, errors.New("tracked path table contains an invalid path length")
	}
	var stored [change.MaxPathBytes]byte
	offset := binary.BigEndian.Uint64(entry[8:16])
	if _, err := set.paths.ReadAt(stored[:length], int64(offset)); err != nil { //nolint:gosec // Index path-byte admission bounds the offset.
		return false, err
	}
	for index := range path {
		if stored[index] != path[index] {
			return false, nil
		}
	}
	return true, nil
}

func (set *trackedPathSet) Close() error {
	if set == nil {
		return nil
	}
	var result error
	if set.table != nil {
		result = errors.Join(result, set.table.Close())
	}
	if set.paths != nil {
		result = errors.Join(result, set.paths.Close())
	}
	for _, path := range []string{set.tablePath, set.pathsPath} {
		if path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}
	*set = trackedPathSet{}
	return result
}
