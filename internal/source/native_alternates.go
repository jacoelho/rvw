package source

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gitstorage "github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

const (
	maxAlternateLayers        = 32
	maxAlternateMetadataBytes = 64 << 10
	maxAlternatePathBytes     = 16 << 10
)

type layeredObjectStorage struct {
	primary storer.EncodedObjectStorer
	layers  []storer.EncodedObjectStorer
}

func (storage *layeredObjectStorage) NewEncodedObject() plumbing.EncodedObject { //nolint:ireturn // go-git requires its encoded-object interface.
	return storage.primary.NewEncodedObject()
}

func (storage *layeredObjectStorage) SetEncodedObject(
	object plumbing.EncodedObject,
) (plumbing.Hash, error) {
	return storage.primary.SetEncodedObject(object)
}

//nolint:ireturn // go-git requires its encoded-object interface.
func (storage *layeredObjectStorage) EncodedObject(
	kind plumbing.ObjectType,
	hash plumbing.Hash,
) (plumbing.EncodedObject, error) {
	object, err := storage.primary.EncodedObject(kind, hash)
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		if err != nil {
			return nil, err
		}
		return object, nil
	}
	for _, layer := range storage.layers {
		object, err = layer.EncodedObject(kind, hash)
		if !errors.Is(err, plumbing.ErrObjectNotFound) {
			if err != nil {
				return nil, err
			}
			return object, nil
		}
	}
	return nil, plumbing.ErrObjectNotFound
}

//nolint:ireturn // go-git requires its encoded-object iterator interface.
func (storage *layeredObjectStorage) IterEncodedObjects(
	kind plumbing.ObjectType,
) (storer.EncodedObjectIter, error) {
	iters := make([]storer.EncodedObjectIter, 0, len(storage.layers)+1)
	for _, layer := range append([]storer.EncodedObjectStorer{storage.primary}, storage.layers...) {
		iterator, err := layer.IterEncodedObjects(kind)
		if err != nil {
			for _, opened := range iters {
				opened.Close()
			}
			return nil, err
		}
		iters = append(iters, iterator)
	}
	return storer.NewMultiEncodedObjectIter(iters), nil
}

func (storage *layeredObjectStorage) HasEncodedObject(hash plumbing.Hash) error {
	err := storage.primary.HasEncodedObject(hash)
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		return err
	}
	for _, layer := range storage.layers {
		err = layer.HasEncodedObject(hash)
		if !errors.Is(err, plumbing.ErrObjectNotFound) {
			return err
		}
	}
	return plumbing.ErrObjectNotFound
}

func (storage *layeredObjectStorage) EncodedObjectSize(hash plumbing.Hash) (int64, error) {
	size, err := storage.primary.EncodedObjectSize(hash)
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		return size, err
	}
	for _, layer := range storage.layers {
		size, err = layer.EncodedObjectSize(hash)
		if !errors.Is(err, plumbing.ErrObjectNotFound) {
			return size, err
		}
	}
	return 0, plumbing.ErrObjectNotFound
}

func (*layeredObjectStorage) AddAlternate(string) error { return fs.ErrPermission }

type nativeAlternateLayer struct {
	storage *alternateObjectStorage
	root    *os.Root
	info    os.FileInfo
	active  bool
}

type alternateObjectStorage struct{ *gitstorage.ObjectStorage }

func (*alternateObjectStorage) AddAlternate(string) error { return fs.ErrPermission }

//nolint:gocognit // Descriptor pinning, identity-cycle detection, and recursive limit accounting are one boundary.
func loadNativeAlternateLayers(
	primaryObjectDirectory string,
) (_ []*nativeAlternateLayer, returnErr error) {
	if _, err := os.Lstat(primaryObjectDirectory); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	primaryPath, err := canonicalAlternateDirectory(primaryObjectDirectory)
	if err != nil {
		return nil, err
	}
	primaryRoot, err := os.OpenRoot(primaryPath)
	if err != nil {
		return nil, fmt.Errorf("open primary object directory: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, primaryRoot.Close()) }()
	primaryInfo, err := primaryRoot.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect primary object directory: %w", err)
	}
	seen := []*nativeAlternateLayer{{info: primaryInfo, active: true}}
	layers := make([]*nativeAlternateLayer, 0, 2)
	metadataBytes := uint64(0)
	var visit func(string, *os.Root, int) error
	visit = func(path string, root *os.Root, depth int) error {
		contents, present, err := readAlternateMetadata(root, &metadataBytes)
		if err != nil || !present {
			return err
		}
		for value := range strings.SplitSeq(string(contents), "\n") {
			value = strings.TrimSuffix(value, "\r")
			if value == "" {
				continue
			}
			if len(value) > maxAlternatePathBytes || strings.IndexByte(value, 0) >= 0 {
				return errors.New("git object alternate path is invalid")
			}
			if depth == maxAlternateLayers {
				return &LimitError{Resource: LimitConfigFiles, Limit: maxAlternateLayers}
			}
			target := value
			if !filepath.IsAbs(target) {
				target = filepath.Join(path, target)
			}
			target, err = canonicalAlternateDirectory(target)
			if err != nil {
				return err
			}
			targetRoot, err := os.OpenRoot(target)
			if err != nil {
				return fmt.Errorf("open Git object alternate %q: %w", target, err)
			}
			targetInfo, err := targetRoot.Stat(".")
			if err != nil {
				return errors.Join(err, targetRoot.Close())
			}
			var prior *nativeAlternateLayer
			for _, candidate := range seen {
				if os.SameFile(candidate.info, targetInfo) {
					prior = candidate
					break
				}
			}
			if prior != nil {
				closeErr := targetRoot.Close()
				if prior.active {
					return errors.Join(errors.New("git object alternates contain a cycle"), closeErr)
				}
				if closeErr != nil {
					return closeErr
				}
				continue
			}
			if len(layers) == maxAlternateLayers {
				return errors.Join(
					&LimitError{Resource: LimitConfigFiles, Limit: maxAlternateLayers},
					targetRoot.Close(),
				)
			}
			layer := &nativeAlternateLayer{root: targetRoot, info: targetInfo, active: true}
			seen = append(seen, layer)
			layers = append(layers, layer)
			if err := visit(target, targetRoot, depth+1); err != nil {
				return err
			}
			layer.active = false
		}
		return nil
	}
	if err := visit(primaryPath, primaryRoot, 0); err != nil {
		return nil, errors.Join(err, closeNativeAlternateLayers(layers))
	}
	for _, layer := range layers {
		filesystem := &rootedFilesystem{
			root: layer.root, objectsAtRoot: true, hideAlternates: true,
		}
		layer.storage = &alternateObjectStorage{ObjectStorage: gitstorage.NewObjectStorageWithOptions(
			dotgit.New(filesystem),
			cache.NewObjectLRU(4*cache.MiByte),
			gitstorage.Options{MaxOpenDescriptors: 64, LargeObjectThreshold: 1 << 20},
		)}
	}
	return layers, nil
}

func canonicalAlternateDirectory(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("git object alternate path is invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve Git object alternate %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("git object alternate %q is not a directory: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}

func readAlternateMetadata(
	root *os.Root,
	aggregate *uint64,
) ([]byte, bool, error) {
	info, err := root.Lstat("info/alternates")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("git object alternates metadata must be a regular file")
	}
	remaining := uint64(maxAlternateMetadataBytes) - *aggregate
	contents, err := readStableRootFile(root, "info/alternates", int64(remaining))
	if err != nil {
		return nil, false, err
	}
	*aggregate += uint64(len(contents))
	if *aggregate > maxAlternateMetadataBytes {
		return nil, false, &LimitError{
			Resource: LimitConfigBytes,
			Limit:    maxAlternateMetadataBytes,
		}
	}
	return contents, true, nil
}

func closeNativeAlternateLayers(layers []*nativeAlternateLayer) error {
	var result error
	for _, layer := range slices.Backward(layers) {
		if layer.storage != nil {
			result = errors.Join(result, layer.storage.Close())
		}
		if layer.root != nil {
			result = errors.Join(result, layer.root.Close())
		}
	}
	return result
}
