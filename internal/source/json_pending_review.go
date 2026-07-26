package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jacoelho/rvw/internal/change"
)

type pendingReviewScalarRoute uint8

const (
	pendingReviewViewerLoginRoute pendingReviewScalarRoute = iota + 1
	pendingReviewRepositoryNameRoute
	pendingReviewBaseOIDRoute
	pendingReviewHeadOIDRoute
	pendingReviewNumberRoute
	pendingReviewPageHasNextRoute
	pendingReviewPageCursorRoute
	pendingReviewIDRoute
	pendingReviewDatabaseIDRoute
	pendingReviewBodyRoute
	pendingReviewStateRoute
	pendingReviewViewerDidAuthorRoute
	pendingReviewViewerCanUpdateRoute
	pendingReviewCommitOIDRoute
)

type pendingReviewJSONCollector struct {
	currentPage pendingReviewPage
	scope       *jsonDecodeScope
	visit       pendingReviewPageVisitor
	path        jsonPathTracker
	duplicates  jsonDuplicateGuard
	pageMark    int64
	rootCount   int
	pageIndex   int
	rootSeen    bool
	pageActive  bool
}

type pendingReviewPageVisitor func(int, pendingPagePosition, *pendingReviewPage) error

//nolint:dupl // Endpoint-specific visitors and diagnostics keep these boundaries schema-local.
func decodePendingReviewStream(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visit pendingReviewPageVisitor,
) error {
	if visit == nil {
		return errors.New("GitHub pending review page visitor is nil")
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return err
	}
	defer scope.Close()
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		return err
	}
	collector := pendingReviewJSONCollector{scope: scope, visit: visit}
	defer collector.path.close()
	if err := walkJSONFrames(ctx, input, runCPU, ledger, metrics, collector.consume); err != nil {
		return fmt.Errorf("decode GitHub pending reviews: %w", err)
	}
	if !collector.rootSeen || collector.rootCount == 0 || collector.pageActive ||
		collector.pageIndex != collector.rootCount {
		return errors.New("GitHub pending review pagination is incomplete")
	}
	return nil
}

func validatePendingReviewPages(encoded []byte) error {
	ledger, err := newSourceLedger(int64(len(encoded)))
	if err != nil {
		return err
	}
	limits, err := newJSONFramingLimits(change.MaxComments, maxGitHubPageCollectionElements)
	if err != nil {
		return err
	}
	metrics, err := inspectJSONWithLimits(
		context.Background(), bytes.NewReader(encoded), maxGitHubJSONTokenBytes,
		directCPUAdmission, ledger,
		limits,
	)
	if err != nil {
		return err
	}
	defer metrics.release(ledger)
	return decodePendingReviewStream(
		context.Background(), bytes.NewReader(encoded), directCPUAdmission, ledger, metrics,
		func(_ int, _ pendingPagePosition, _ *pendingReviewPage) error { return nil },
	)
}

//nolint:dupl // Endpoint-specific state machines intentionally keep schemas separate.
func (collector *pendingReviewJSONCollector) consume(frame jsonFrame) error {
	switch frame.Kind {
	case jsonObjectKey:
		if err := collector.path.keyFrame(collector.scope, frame); err != nil {
			return err
		}
		return collector.duplicates.key(&collector.path)
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		return collector.startArray(frame.Count)
	case jsonObjectStart:
		if err := collector.path.start(frame.Kind, 0); err != nil {
			return err
		}
		collector.duplicates.startObject(collector.path.depth)
		return collector.startObject()
	case jsonObjectEnd:
		rootItem := collector.path.depth == 2 && collector.pageActive
		collector.duplicates.endObject(collector.path.depth)
		if err := collector.path.end(frame.Kind); err != nil {
			return err
		}
		if rootItem {
			return collector.finishPage()
		}
		return nil
	case jsonArrayEnd:
		return collector.path.end(frame.Kind)
	default:
		if collector.path.depth == 0 || collector.rootItemDepth() {
			return errors.New("GitHub pending review page is not an object")
		}
		if collector.nodeItemContainer() {
			return errors.New("GitHub pending review node is not an object")
		}
		return collector.path.scalar(func(value jsonPathValue) error {
			return collector.scalar(value, frame)
		})
	}
}

func (collector *pendingReviewJSONCollector) startArray(count int) error {
	switch {
	case collector.path.depth == 1:
		if collector.rootSeen || count == 0 {
			return errors.New("GitHub pending review pagination returned no page")
		}
		collector.rootSeen = true
		collector.rootCount = count
	case collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart:
		return errors.New("GitHub pending review page is not an object")
	case collector.nodeItemParent():
		return errors.New("GitHub pending review node is not an object")
	case collector.path.suffix("errors"):
		page, err := collector.page()
		if err != nil {
			return err
		}
		if err := reserveJSONDecodedSlice(collector.scope, count); err != nil {
			return err
		}
		page.Errors = make(githubErrors, count)
	case collector.nodeItemContainer():
		connection, err := collector.connection()
		if err != nil {
			return err
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		if err := reserveJSONDecodedSlice(collector.scope, count); err != nil {
			return err
		}
		nodes := make(pendingReviewNodes, 0, count)
		connection.Nodes = &nodes
	}
	return nil
}

func (collector *pendingReviewJSONCollector) startObject() error {
	if collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart {
		if !collector.rootSeen || collector.pageActive || collector.pageIndex >= collector.rootCount {
			return errors.New("GitHub pending review page count exceeded preflight")
		}
		collector.pageMark = collector.scope.mark()
		collector.currentPage = pendingReviewPage{}
		collector.pageActive = true
		return nil
	}
	if collector.path.depth == 1 {
		return errors.New("GitHub pending review root is not an array")
	}
	page, pageErr := collector.page()
	if pageErr != nil {
		return pageErr
	}
	switch {
	case collector.path.suffix("data", "viewer"):
		if page == nil {
			return errors.New("GitHub viewer is outside a page")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Viewer = new(struct {
			Login string `json:"login"`
		})
	case collector.path.suffix("data", "repository"):
		if page == nil {
			return errors.New("GitHub repository is outside a page")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Repository = new(struct {
			PullRequest *struct {
				Reviews    *pendingReviewConnection `json:"reviews"`
				BaseRefOID string                   `json:"baseRefOid"`
				HeadRefOID string                   `json:"headRefOid"`
				Number     uint64                   `json:"number"`
			} `json:"pullRequest"`
			NameWithOwner string `json:"nameWithOwner"`
		})
	case collector.path.suffix("data", "repository", "pullRequest"):
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub pull request is outside a repository")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Repository.PullRequest = new(struct {
			Reviews    *pendingReviewConnection `json:"reviews"`
			BaseRefOID string                   `json:"baseRefOid"`
			HeadRefOID string                   `json:"headRefOid"`
			Number     uint64                   `json:"number"`
		})
	case collector.path.suffix("data", "repository", "pullRequest", "reviews"):
		pull, err := collector.pullRequest()
		if err != nil {
			return err
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		pull.Reviews = new(pendingReviewConnection)
	case collector.path.suffix("data", "repository", "pullRequest", "reviews", "pageInfo"):
		connection, err := collector.connection()
		if err != nil {
			return err
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		connection.PageInfo = new(remotePageInfo)
	case collector.nodeItemParent():
		connection, err := collector.connection()
		if err != nil || connection.Nodes == nil {
			return errors.Join(err, errors.New("GitHub pending review is outside its nodes"))
		}
		if len(*connection.Nodes) == cap(*connection.Nodes) {
			return errors.New("GitHub pending review node count exceeded preflight")
		}
		*connection.Nodes = append(*connection.Nodes, pendingReviewNode{})
	case collector.path.suffix("data", "repository", "pullRequest", "reviews", jsonFieldNodes, "commit"):
		node, err := collector.node()
		if err != nil {
			return err
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		node.Commit = new(struct {
			OID string `json:"oid"`
		})
	}
	return nil
}

func (collector *pendingReviewJSONCollector) scalar(value jsonPathValue, frame jsonFrame) error {
	route := collector.scalarRoute(value.fieldID)
	if route == 0 {
		return nil
	}
	page, pageErr := collector.page()
	if pageErr != nil {
		return pageErr
	}
	var err error
	switch route {
	case pendingReviewViewerLoginRoute:
		if page == nil || page.Data.Viewer == nil {
			return errors.New("GitHub viewer login is outside its object")
		}
		page.Data.Viewer.Login, err = collector.scope.string(frame)
	case pendingReviewRepositoryNameRoute:
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub repository name is outside its object")
		}
		page.Data.Repository.NameWithOwner, err = collector.scope.string(frame)
	case pendingReviewBaseOIDRoute:
		var pullErr error
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.BaseRefOID, err = collector.scope.string(frame)
	case pendingReviewHeadOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.HeadRefOID, err = collector.scope.string(frame)
	case pendingReviewNumberRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.Number, err = jsonUint64Value(frame)
	case pendingReviewPageHasNextRoute:
		connection, connectionErr := collector.connection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub pending review page info is incomplete"))
		}
		connection.PageInfo.HasNextPage, err = collector.scope.nullableBoolean(frame)
	case pendingReviewPageCursorRoute:
		connection, connectionErr := collector.connection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub pending review page info is incomplete"))
		}
		connection.PageInfo.EndCursor, err = collector.scope.nullableString(frame)
	case pendingReviewIDRoute, pendingReviewDatabaseIDRoute, pendingReviewBodyRoute,
		pendingReviewStateRoute, pendingReviewViewerDidAuthorRoute,
		pendingReviewViewerCanUpdateRoute, pendingReviewCommitOIDRoute:
		node, nodeErr := collector.node()
		if nodeErr != nil {
			return nodeErr
		}
		switch route {
		case pendingReviewIDRoute:
			node.ID, err = collector.scope.string(frame)
		case pendingReviewDatabaseIDRoute:
			node.FullDatabaseID, err = jsonGitHubUint64(frame)
		case pendingReviewBodyRoute:
			node.Body, err = collector.scope.nullableString(frame)
		case pendingReviewStateRoute:
			node.State, err = collector.scope.string(frame)
		case pendingReviewViewerDidAuthorRoute:
			node.ViewerDidAuthor, err = collector.scope.nullableBoolean(frame)
		case pendingReviewViewerCanUpdateRoute:
			node.ViewerCanUpdate, err = collector.scope.nullableBoolean(frame)
		case pendingReviewCommitOIDRoute:
			if node.Commit == nil {
				return errors.New("GitHub pending review commit identity is outside its object")
			}
			node.Commit.OID, err = collector.scope.string(frame)
		default:
			return errors.New("GitHub pending review scalar route is invalid")
		}
	}
	return err
}

func (collector *pendingReviewJSONCollector) scalarRoute(field jsonPresenceField) pendingReviewScalarRoute {
	var route pendingReviewScalarRoute
	switch field {
	case jsonFieldIDLogin:
		route = pendingReviewViewerLoginRoute
	case jsonFieldIDNameWithOwner:
		route = pendingReviewRepositoryNameRoute
	case jsonFieldIDBaseRefOID:
		route = pendingReviewBaseOIDRoute
	case jsonFieldIDHeadRefOID:
		route = pendingReviewHeadOIDRoute
	case jsonFieldIDNumber:
		route = pendingReviewNumberRoute
	case jsonFieldIDHasNextPage:
		route = pendingReviewPageHasNextRoute
	case jsonFieldIDEndCursor:
		route = pendingReviewPageCursorRoute
	case jsonFieldIDID:
		route = pendingReviewIDRoute
	case jsonFieldIDFullDatabaseID:
		route = pendingReviewDatabaseIDRoute
	case jsonFieldIDBody:
		route = pendingReviewBodyRoute
	case jsonFieldIDState:
		route = pendingReviewStateRoute
	case jsonFieldIDViewerDidAuthor:
		route = pendingReviewViewerDidAuthorRoute
	case jsonFieldIDViewerCanUpdate:
		route = pendingReviewViewerCanUpdateRoute
	case jsonFieldIDOID:
		route = pendingReviewCommitOIDRoute
	default:
		return 0
	}
	matched := false
	switch route {
	case pendingReviewViewerLoginRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDViewer)
	case pendingReviewRepositoryNameRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository)
	case pendingReviewBaseOIDRoute, pendingReviewHeadOIDRoute, pendingReviewNumberRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	case pendingReviewPageHasNextRoute, pendingReviewPageCursorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviews, jsonFieldIDPageInfo,
		)
	case pendingReviewIDRoute, pendingReviewDatabaseIDRoute, pendingReviewBodyRoute,
		pendingReviewStateRoute, pendingReviewViewerDidAuthorRoute, pendingReviewViewerCanUpdateRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviews, jsonFieldIDNodes,
		)
	case pendingReviewCommitOIDRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviews, jsonFieldIDNodes, jsonFieldIDCommit,
		)
	}
	if !matched {
		return 0
	}
	return route
}

func (collector *pendingReviewJSONCollector) page() (*pendingReviewPage, error) {
	if !collector.pageActive {
		return nil, errors.New("GitHub pending review value is outside a page")
	}
	return &collector.currentPage, nil
}

func (collector *pendingReviewJSONCollector) rootItemDepth() bool {
	return collector.path.depth == 1 && collector.path.stack[0].kind == jsonArrayStart
}

func (collector *pendingReviewJSONCollector) nodeItemContainer() bool {
	if collector.path.depth == 0 {
		return false
	}
	container := collector.path.stack[collector.path.depth-1]
	return container.kind == jsonArrayStart && container.name == jsonFieldNodes &&
		collector.path.suffix("data", "repository", "pullRequest", "reviews", jsonFieldNodes)
}

func (collector *pendingReviewJSONCollector) nodeItemParent() bool {
	if collector.path.depth < 2 {
		return false
	}
	parent := collector.path.stack[collector.path.depth-2]
	return parent.kind == jsonArrayStart && parent.name == jsonFieldNodes &&
		collector.path.suffix("data", "repository", "pullRequest", "reviews", jsonFieldNodes)
}

func (collector *pendingReviewJSONCollector) finishPage() error {
	if !collector.pageActive || collector.pageIndex >= collector.rootCount {
		return errors.New("GitHub pending review page count exceeded preflight")
	}
	position := pendingJSONPageIntermediate
	if collector.pageIndex+1 == collector.rootCount {
		position = pendingJSONPageFinal
	}
	err := collector.visit(collector.pageIndex, position, &collector.currentPage)
	collector.currentPage = pendingReviewPage{}
	collector.pageActive = false
	collector.pageIndex++
	return errors.Join(err, collector.scope.releaseTo(collector.pageMark))
}

func (collector *pendingReviewJSONCollector) pullRequest() (*struct {
	Reviews    *pendingReviewConnection `json:"reviews"`
	BaseRefOID string                   `json:"baseRefOid"`
	HeadRefOID string                   `json:"headRefOid"`
	Number     uint64                   `json:"number"`
}, error) {
	page, err := collector.page()
	if err != nil || page.Data.Repository == nil || page.Data.Repository.PullRequest == nil {
		return nil, errors.Join(err, errors.New("GitHub pending review pull request is unavailable"))
	}
	return page.Data.Repository.PullRequest, nil
}

func (collector *pendingReviewJSONCollector) connection() (*pendingReviewConnection, error) {
	pull, err := collector.pullRequest()
	if err != nil || pull.Reviews == nil {
		return nil, errors.Join(err, errors.New("GitHub pending review connection is unavailable"))
	}
	return pull.Reviews, nil
}

func (collector *pendingReviewJSONCollector) node() (*pendingReviewNode, error) {
	connection, err := collector.connection()
	if err != nil || connection.Nodes == nil || len(*connection.Nodes) == 0 {
		return nil, errors.Join(err, errors.New("GitHub pending review node is unavailable"))
	}
	return &(*connection.Nodes)[len(*connection.Nodes)-1], nil
}
