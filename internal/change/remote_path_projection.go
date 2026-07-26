package change

import (
	"sort"
)

const remotePathAliasStorageBytes int64 = 32

type remotePathAlias struct {
	path   string
	file   FileID
	status RemoteMappingStatus
}

// remotePathProjection is an immutable sorted alias index. Its strings alias
// validated snapshot paths, while the compact metadata backing is projection-owned.
type remotePathProjection struct {
	aliases       []remotePathAlias
	metadataBytes int64
	pathBytes     int64
}

func newRemotePathProjection(aliases []remotePathAlias) remotePathProjection {
	projection, err := newRemotePathProjectionCancellable(nil, aliases)
	if err != nil {
		panic(err)
	}
	return projection
}

func newRemotePathProjectionCancellable(
	cancelled func() error,
	aliases []remotePathAlias,
) (remotePathProjection, error) {
	if err := sortRemotePathAliasesCancellable(cancelled, aliases); err != nil {
		return remotePathProjection{}, err
	}
	compacted := aliases[:0]
	var pathBytes int64
	for start := 0; start < len(aliases); {
		if start%16 == 0 {
			if err := cancellationError(cancelled); err != nil {
				return remotePathProjection{}, err
			}
		}
		end := start + 1
		entry := aliases[start]
		for end < len(aliases) && aliases[end].path == entry.path {
			if end%16 == 0 {
				if err := cancellationError(cancelled); err != nil {
					return remotePathProjection{}, err
				}
			}
			if aliases[end].file != entry.file {
				entry.file = 0
				entry.status = RemoteMappingAmbiguous
			}
			end++
		}
		compacted = append(compacted, entry)
		pathBytes += int64(len(entry.path))
		start = end
	}
	if len(compacted) != len(aliases) {
		exact := make([]remotePathAlias, len(compacted))
		copy(exact, compacted)
		clear(aliases)
		compacted = exact
	}
	return remotePathProjection{
		aliases:       compacted,
		metadataBytes: int64(cap(compacted)) * remotePathAliasStorageBytes,
		pathBytes:     pathBytes,
	}, nil
}

func sortRemotePathAliasesCancellable(
	cancelled func() error,
	aliases []remotePathAlias,
) error {
	work := 0
	poll := func() error {
		work++
		if work%16 != 0 {
			return nil
		}
		return cancellationError(cancelled)
	}
	less := func(left, right remotePathAlias) bool {
		if left.path != right.path {
			return left.path < right.path
		}
		return left.file < right.file
	}
	siftDown := func(root, end int) error {
		for child := root*2 + 1; child < end; child = root*2 + 1 {
			if err := poll(); err != nil {
				return err
			}
			if child+1 < end && less(aliases[child], aliases[child+1]) {
				child++
			}
			if !less(aliases[root], aliases[child]) {
				return nil
			}
			aliases[root], aliases[child] = aliases[child], aliases[root]
			root = child
		}
		return nil
	}
	if err := cancellationError(cancelled); err != nil {
		return err
	}
	for root := len(aliases)/2 - 1; root >= 0; root-- {
		if err := siftDown(root, len(aliases)); err != nil {
			return err
		}
	}
	for end := len(aliases) - 1; end > 0; end-- {
		aliases[0], aliases[end] = aliases[end], aliases[0]
		if err := siftDown(0, end); err != nil {
			return err
		}
	}
	return cancellationError(cancelled)
}

func (projection remotePathProjection) lookup(path string) (remotePathMapping, bool) {
	index := sort.Search(len(projection.aliases), func(index int) bool {
		return projection.aliases[index].path >= path
	})
	if index == len(projection.aliases) || projection.aliases[index].path != path {
		return remotePathMapping{}, false
	}
	entry := projection.aliases[index]
	return remotePathMapping{status: entry.status, file: entry.file}, true
}

func (projection remotePathProjection) retainedBytes() int64 {
	return projection.metadataBytes + projection.pathBytes
}
