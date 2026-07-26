package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitstorage "github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/jacoelho/rvw/internal/change"
)

const (
	pullRequestCommitWireLimit     uint64 = 32 << 20
	pullRequestCommitExpandedLimit uint64 = 64 << 20
	pullRequestTreeWireLimit       uint64 = 128 << 20
	pullRequestTreeExpandedLimit   uint64 = 256 << 20
	pullRequestBlobWireLimit       uint64 = 4 << 30
	pullRequestBlobExpandedLimit   uint64 = 8 << 30
	pullRequestWorkspaceLimit      uint64 = 5 << 30
	pullRequestWantLimit                  = 512
	pullRequestBlobRequestLimit           = 12
	pullRequestBlobOIDLimit               = 6_000
)

type smartGitAcquisition struct { //nolint:govet // Fields are grouped by acquisition ownership then counters.
	client       *githubClient
	repository   OwnerRepository
	session      *repositorySession
	advertised   *packp.AdvRefs
	requested    map[plumbing.Hash]struct{}
	blobRequests int
	blobWire     uint64
	blobExpanded uint64
	commitWire   uint64
	treeWire     uint64
}

type limitedPackReader struct {
	reader io.Reader
	read   uint64
	limit  uint64
}

type repositoryPackLayer struct {
	storage *gitstorage.Storage
	root    *os.Root
	path    string
}

func (reader *limitedPackReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.read == reader.limit {
		var probe [1]byte
		count, err := reader.reader.Read(probe[:])
		if count < 0 {
			return 0, errors.New("pack reader returned a negative byte count")
		}
		reader.read += uint64(count)
		if count != 0 {
			return 0, &LimitError{Resource: LimitPackWireBytes, Limit: reader.limit}
		}
		return 0, err
	}
	if reader.read > reader.limit {
		return 0, &LimitError{Resource: LimitPackWireBytes, Limit: reader.limit}
	}
	if uint64(len(buffer)) > reader.limit-reader.read+1 {
		buffer = buffer[:int(reader.limit-reader.read+1)] //nolint:gosec // branch proves the value is smaller than len(buffer).
	}
	count, err := reader.reader.Read(buffer)
	if count < 0 {
		return 0, errors.New("pack reader returned a negative byte count")
	}
	reader.read += uint64(count)
	if reader.read > reader.limit {
		return count - 1, &LimitError{Resource: LimitPackWireBytes, Limit: reader.limit}
	}
	return count, err
}

func createPullRequestRepository(
	workspace string,
	options ServiceOptions,
) (*repositorySession, error) {
	bare := filepath.Join(workspace, "repository.git")
	for _, directory := range []string{
		bare,
		filepath.Join(bare, "objects"),
		filepath.Join(bare, "objects", "pack"),
		filepath.Join(bare, "objects", "info"),
		filepath.Join(bare, "refs"),
	} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/rvw\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(
		filepath.Join(bare, "config"),
		[]byte("[core]\n\tbare = true\n[extensions]\n\tobjectFormat = sha1\n"),
		0o600,
	); err != nil {
		return nil, err
	}
	path, err := NewRepositoryPath(bare)
	if err != nil {
		return nil, err
	}
	session, err := openRepositorySession(path, options)
	if err != nil {
		return nil, err
	}
	session.cleanup = workspace
	session.contentLimit = pullRequestBlobExpandedLimit
	return session, nil
}

func newSmartGitAcquisition(
	ctx context.Context,
	client *githubClient,
	repository OwnerRepository,
	session *repositorySession,
) (*smartGitAcquisition, error) {
	if client == nil || len(client.token) == 0 {
		return nil, errors.New("GITHUB_TOKEN is required for pull-request acquisition")
	}
	acquisition := &smartGitAcquisition{
		client: client, repository: repository, session: session,
		requested: make(map[plumbing.Hash]struct{}),
	}
	advertised, err := acquisition.advertise(ctx)
	if err != nil {
		return nil, err
	}
	acquisition.advertised = advertised
	return acquisition, nil
}

func (acquisition *smartGitAcquisition) advertise(ctx context.Context) (*packp.AdvRefs, error) {
	endpoint, transportClient, auth, err := acquisition.transport()
	if err != nil {
		return nil, err
	}
	session, err := transportClient.NewUploadPackSession(endpoint, auth)
	if err != nil {
		return nil, err
	}
	defer session.Close() //nolint:errcheck // No mutation is committed by closing an advertisement session.
	advertised, err := session.AdvertisedReferencesContext(ctx)
	if err != nil {
		return nil, err
	}
	if values := advertised.Capabilities.Get(capability.ObjectFormat); len(values) != 0 {
		for _, value := range values {
			if !strings.EqualFold(value, "sha1") {
				return nil, fmt.Errorf("remote advertises unsupported object format %q", value)
			}
		}
	}
	return advertised, nil
}

func (acquisition *smartGitAcquisition) transport() ( //nolint:ireturn // go-git selects transport through its required interface.
	*transport.Endpoint,
	transport.Transport,
	transport.AuthMethod,
	error,
) {
	endpoint, err := transport.NewEndpoint(
		acquisition.client.gitBase + "/" + acquisition.repository.String() + ".git",
	)
	if err != nil {
		return nil, nil, nil, err
	}
	client := githttp.NewClientWithOptions(acquisition.client.http, &githttp.ClientOptions{
		CacheMaxEntries: 4,
		RedirectPolicy:  githttp.NoFollowRedirects,
	})
	auth := &githttp.BasicAuth{Username: "x-access-token", Password: string(acquisition.client.token)}
	return endpoint, client, auth, nil
}

func (acquisition *smartGitAcquisition) fetchCommitAncestry(
	ctx context.Context,
	base, head plumbing.Hash,
	baseRef GitReference,
	number PullRequestNumber,
	state GitHubState,
) error {
	if !acquisition.advertised.Capabilities.Supports(capability.Filter) ||
		!acquisition.advertised.Capabilities.Supports(capability.Shallow) {
		return errors.New("remote does not advertise required filter and shallow capabilities")
	}
	required := []capability.Capability{capability.Shallow, capability.Filter}
	baseAdvertised := acquisition.advertised.References["refs/heads/"+baseRef.String()]
	if baseAdvertised != base {
		switch state {
		case GitHubOpen:
			return fmt.Errorf("%w: advertised base branch tip changed", ErrSourceStale)
		case GitHubClosed, GitHubMerged:
			if !acquisition.advertised.Capabilities.Supports(
				capability.AllowReachableSHA1InWant,
			) {
				return errors.New(
					"remote cannot fetch the historical pull-request base revision",
				)
			}
			required = append(required, capability.AllowReachableSHA1InWant)
		default:
			return errors.New("pull request has an invalid GitHub state")
		}
	}
	headAdvertised := false
	for name, hash := range acquisition.advertised.References {
		if hash == head && (name == pullReference(number) || strings.HasPrefix(name, "refs/")) {
			headAdvertised = true
			break
		}
	}
	if !headAdvertised && !acquisition.advertised.Capabilities.Supports(capability.AllowTipSHA1InWant) {
		return errors.New("remote does not allow the unadvertised pull-request tip")
	}
	read, _, err := acquisition.fetchPack(
		ctx,
		[]plumbing.Hash{base, head},
		packp.DepthCommits(1_000),
		packp.FilterTreeDepth(0),
		pullRequestCommitWireLimit,
		pullRequestCommitExpandedLimit,
		plumbing.CommitObject,
		required...,
	)
	acquisition.commitWire += read
	return err
}

func (acquisition *smartGitAcquisition) fetchTrees(
	ctx context.Context,
	trees ...plumbing.Hash,
) error {
	if !acquisition.advertised.Capabilities.Supports(capability.AllowReachableSHA1InWant) {
		return errors.New("remote does not advertise allow-reachable-sha1-in-want")
	}
	read, _, err := acquisition.fetchPack(
		ctx,
		trees,
		packp.DepthCommits(0),
		packp.FilterBlobNone(),
		pullRequestTreeWireLimit,
		pullRequestTreeExpandedLimit,
		plumbing.TreeObject,
		capability.Filter,
		capability.AllowReachableSHA1InWant,
	)
	acquisition.treeWire += read
	return err
}

func (acquisition *smartGitAcquisition) fetchBlobs(
	ctx context.Context,
	hashes []plumbing.Hash,
) error {
	unique := make([]plumbing.Hash, 0, len(hashes))
	for _, hash := range hashes {
		if _, requested := acquisition.requested[hash]; requested {
			continue
		}
		acquisition.requested[hash] = struct{}{}
		unique = append(unique, hash)
	}
	if len(acquisition.requested) > pullRequestBlobOIDLimit {
		return &LimitError{Resource: LimitPackExpandedBytes, Limit: pullRequestBlobOIDLimit}
	}
	for start := 0; start < len(unique); start += pullRequestWantLimit {
		end := min(start+pullRequestWantLimit, len(unique))
		acquisition.blobRequests++
		if acquisition.blobRequests > pullRequestBlobRequestLimit {
			return &LimitError{Resource: LimitPackWireBytes, Limit: pullRequestBlobRequestLimit}
		}
		if acquisition.blobWire >= pullRequestBlobWireLimit {
			return &LimitError{Resource: LimitPackWireBytes, Limit: pullRequestBlobWireLimit}
		}
		remaining := pullRequestBlobWireLimit - acquisition.blobWire
		if acquisition.blobExpanded >= pullRequestBlobExpandedLimit {
			return &LimitError{Resource: LimitPackExpandedBytes, Limit: pullRequestBlobExpandedLimit}
		}
		expandedRemaining := pullRequestBlobExpandedLimit - acquisition.blobExpanded
		read, expanded, err := acquisition.fetchPack(
			ctx,
			unique[start:end],
			packp.DepthCommits(0),
			packp.Filter(""),
			remaining,
			expandedRemaining,
			plumbing.BlobObject,
			capability.AllowReachableSHA1InWant,
		)
		acquisition.blobWire += read
		acquisition.blobExpanded += expanded
		if err != nil {
			return err
		}
	}
	return acquisition.checkWorkspace()
}

func (acquisition *smartGitAcquisition) fetchPack(
	ctx context.Context,
	wants []plumbing.Hash,
	depth packp.Depth,
	filter packp.Filter,
	wireLimit uint64,
	expandedLimit uint64,
	wantType plumbing.ObjectType,
	required ...capability.Capability,
) (_ uint64, _ uint64, returnErr error) {
	if len(wants) == 0 || len(wants) > pullRequestWantLimit {
		return 0, 0, errors.New("smart Git request has an invalid want count")
	}
	if expandedLimit == 0 || wantType == plumbing.AnyObject || wantType == plumbing.InvalidObject {
		return 0, 0, errors.New("smart Git request has invalid object limits")
	}
	endpoint, transportClient, auth, err := acquisition.transport()
	if err != nil {
		return 0, 0, err
	}
	session, err := transportClient.NewUploadPackSession(endpoint, auth)
	if err != nil {
		return 0, 0, err
	}
	defer session.Close() //nolint:errcheck // Pack request errors are reported before transport cleanup.
	packCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	advertised, err := session.AdvertisedReferencesContext(packCtx)
	if err != nil {
		return 0, 0, err
	}
	for _, requiredCapability := range required {
		if !advertised.Capabilities.Supports(requiredCapability) {
			return 0, 0, fmt.Errorf("remote no longer advertises required capability %q", requiredCapability)
		}
	}
	request := packp.NewUploadPackRequestFromCapabilities(advertised.Capabilities)
	request.Capabilities.Delete(capability.ThinPack)
	for _, requiredCapability := range required {
		if !request.Capabilities.Supports(requiredCapability) {
			if capabilityErr := request.Capabilities.Add(requiredCapability); capabilityErr != nil {
				return 0, 0, capabilityErr
			}
		}
	}
	request.Wants = append(request.Wants, wants...)
	sort.Slice(request.Wants, func(left, right int) bool {
		return request.Wants[left].String() < request.Wants[right].String()
	})
	request.Depth = depth
	request.Filter = filter
	response, err := session.UploadPack(packCtx, request)
	if err != nil {
		return 0, 0, err
	}
	defer response.Close() //nolint:errcheck // Pack validation reports stream errors before response cleanup.
	reader := &limitedPackReader{reader: response, limit: wireLimit}
	var pack io.Reader = reader
	if request.Capabilities.Supports(capability.Sideband64k) {
		pack = sideband.NewDemuxer(sideband.Sideband64k, reader)
	} else if request.Capabilities.Supports(capability.Sideband) {
		pack = sideband.NewDemuxer(sideband.Sideband, reader)
	}
	layer, err := acquisition.session.newPackLayer()
	if err != nil {
		return reader.read, 0, err
	}
	attached := false
	defer func() {
		if !attached {
			returnErr = errors.Join(returnErr, layer.closeAndRemove())
		}
	}()
	if ingestErr := packfile.UpdateObjectStorage(layer.storage, pack); ingestErr != nil {
		return reader.read, 0, fmt.Errorf("ingest smart Git pack: %w", ingestErr)
	}
	expanded, err := validatePackLayer(layer.storage, wants, wantType, expandedLimit)
	if err != nil {
		return reader.read, expanded, err
	}
	if err := acquisition.checkWorkspace(); err != nil {
		return reader.read, expanded, err
	}
	acquisition.session.attachPackLayer(layer)
	attached = true
	return reader.read, expanded, nil
}

func (acquisition *smartGitAcquisition) checkWorkspace() error {
	var bytes uint64
	err := filepath.WalkDir(acquisition.session.cleanup, func(
		_ string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Size() < 0 {
				return errors.New("pull-request workspace entry has a negative size")
			}
			bytes += uint64(info.Size()) //nolint:gosec // negative sizes are rejected immediately above.
			if bytes > pullRequestWorkspaceLimit {
				return &LimitError{Resource: LimitPullRequestWorkspace, Limit: pullRequestWorkspaceLimit}
			}
		}
		return nil
	})
	return err
}

func (repository *repositorySession) newPackLayer() (*repositoryPackLayer, error) {
	if repository == nil || repository.cleanup == "" || repository.objectLayers == nil {
		return nil, errors.New("pull-request repository cannot own a pack layer")
	}
	path, err := os.MkdirTemp(repository.cleanup, "pack-layer-")
	if err != nil {
		return nil, fmt.Errorf("create smart Git pack layer: %w", err)
	}
	for _, directory := range []string{
		filepath.Join(path, "objects", "pack"),
		filepath.Join(path, "objects", "info"),
	} {
		if mkdirErr := os.MkdirAll(directory, 0o700); mkdirErr != nil {
			return nil, errors.Join(mkdirErr, os.RemoveAll(path))
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(path))
	}
	return &repositoryPackLayer{
		storage: newRepositoryStorage(root, root, repositoryStorageWritable),
		root:    root,
		path:    path,
	}, nil
}

func (repository *repositorySession) attachPackLayer(layer *repositoryPackLayer) {
	repository.packLayers = append(repository.packLayers, layer)
	repository.objectLayers.layers = append(repository.objectLayers.layers, layer.storage)
}

func validatePackLayer(
	storage *gitstorage.Storage,
	wants []plumbing.Hash,
	wantType plumbing.ObjectType,
	expandedLimit uint64,
) (uint64, error) {
	iterator, err := storage.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return 0, fmt.Errorf("inspect smart Git pack objects: %w", err)
	}
	defer iterator.Close()
	var expanded uint64
	for {
		object, err := iterator.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return expanded, fmt.Errorf("inspect smart Git pack object: %w", err)
		}
		if object.Size() < 0 {
			return expanded, errors.New("smart Git pack object has a negative expanded size")
		}
		size := uint64(object.Size()) //nolint:gosec // Negative sizes are rejected above.
		if size > expandedLimit-expanded {
			return expanded, &LimitError{
				Resource: LimitPackExpandedBytes,
				Limit:    expandedLimit,
			}
		}
		expanded += size
	}
	for _, want := range wants {
		if _, err := storage.EncodedObject(wantType, want); err != nil {
			return expanded, fmt.Errorf(
				"smart Git pack is missing required %s object %s: %w",
				wantType,
				want,
				err,
			)
		}
	}
	return expanded, nil
}

func (layer *repositoryPackLayer) closeAndRemove() error {
	if layer == nil {
		return nil
	}
	var storageErr error
	if layer.storage != nil {
		storageErr = layer.storage.Close()
	}
	var rootErr error
	if layer.root != nil {
		rootErr = layer.root.Close()
	}
	removeErr := os.RemoveAll(layer.path)
	layer.storage = nil
	layer.root = nil
	layer.path = ""
	return errors.Join(storageErr, rootErr, removeErr)
}

func closeRepositoryPackLayers(layers []*repositoryPackLayer) error {
	var result error
	for _, layer := range slices.Backward(layers) {
		if layer == nil {
			continue
		}
		var storageErr error
		if layer.storage != nil {
			storageErr = layer.storage.Close()
		}
		var rootErr error
		if layer.root != nil {
			rootErr = layer.root.Close()
		}
		result = errors.Join(result, storageErr, rootErr)
		layer.storage = nil
		layer.root = nil
	}
	return result
}

//nolint:gocognit,funlen // Two-color ancestry propagation and best-common selection share one bounded graph.
func nativeMergeBase(
	ctx context.Context,
	session *repositorySession,
	base, head plumbing.Hash,
) (plumbing.Hash, error) {
	type ancestryNode struct {
		parents           []plumbing.Hash
		childrenRemaining int
		colors            uint8
		loaded            bool
		queued            bool
		belowCommon       bool
	}
	nodes := make(map[plumbing.Hash]*ancestryNode)
	queue := make([]plumbing.Hash, 0, 256)
	seed := func(hash plumbing.Hash, color uint8) {
		node := nodes[hash]
		if node == nil {
			node = &ancestryNode{}
			nodes[hash] = node
		}
		node.colors |= color
		if !node.queued {
			node.queued = true
			queue = append(queue, hash)
		}
	}
	seed(base, 1)
	seed(head, 2)
	missing := false
	for len(queue) != 0 {
		if err := context.Cause(ctx); err != nil {
			return plumbing.ZeroHash, err
		}
		hash := queue[0]
		queue = queue[1:]
		node := nodes[hash]
		node.queued = false
		if !node.loaded {
			commit, err := object.GetCommit(session.objects, hash)
			if errors.Is(err, plumbing.ErrObjectNotFound) {
				missing = true
				continue
			}
			if err != nil {
				return plumbing.ZeroHash, err
			}
			node.loaded = true
			node.parents = append(node.parents, commit.ParentHashes...)
		}
		for _, parentHash := range node.parents {
			parent := nodes[parentHash]
			if parent == nil {
				if len(nodes) == 4_096 {
					return plumbing.ZeroHash, &LimitError{
						Resource: LimitPullRequestDepth,
						Limit:    1_000,
					}
				}
				parent = &ancestryNode{}
				nodes[parentHash] = parent
			}
			colors := parent.colors | node.colors
			if colors != parent.colors {
				parent.colors = colors
				if !parent.queued {
					parent.queued = true
					queue = append(queue, parentHash)
				}
			}
		}
	}
	commonCount := 0
	for _, node := range nodes {
		if node.loaded && node.colors == 3 {
			commonCount++
		}
		for _, parentHash := range node.parents {
			nodes[parentHash].childrenRemaining++
		}
	}
	if commonCount == 0 {
		if missing {
			return plumbing.ZeroHash, &LimitError{Resource: LimitPullRequestDepth, Limit: 1_000}
		}
		return plumbing.ZeroHash, errors.New("pull-request revisions have no common ancestor")
	}
	topological := make([]plumbing.Hash, 0, len(nodes))
	for hash, node := range nodes {
		if node.childrenRemaining == 0 {
			topological = append(topological, hash)
		}
	}
	best := make([]plumbing.Hash, 0, commonCount)
	processed := 0
	for len(topological) != 0 {
		hash := topological[len(topological)-1]
		topological = topological[:len(topological)-1]
		node := nodes[hash]
		processed++
		common := node.loaded && node.colors == 3
		if common && !node.belowCommon {
			best = append(best, hash)
		}
		propagate := node.belowCommon || common
		for _, parentHash := range node.parents {
			parent := nodes[parentHash]
			parent.belowCommon = parent.belowCommon || propagate
			parent.childrenRemaining--
			if parent.childrenRemaining == 0 {
				topological = append(topological, parentHash)
			}
		}
	}
	if processed != len(nodes) || len(best) == 0 {
		return plumbing.ZeroHash, errors.New("git commit ancestry graph is cyclic or incomplete")
	}
	sort.Slice(best, func(left, right int) bool {
		return best[left].String() < best[right].String()
	})
	return best[0], nil
}

func reviewHash(value change.ObjectID) plumbing.Hash {
	return plumbing.NewHash(value.String())
}
