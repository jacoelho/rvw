package source

import (
	"context"
	"errors"
	"fmt"
	"io"
)

type pendingCommentScalarRoute uint8

const (
	pendingCommentOriginalStartLineRoute pendingCommentScalarRoute = iota + 1
	pendingCommentLineRoute
	pendingCommentBodyRoute
	pendingCommentOriginalLineRoute
	pendingCommentInReplyToIDRoute
	pendingCommentStartLineRoute
	pendingCommentStartSideRoute
	pendingCommentSideRoute
	pendingCommentNodeIDRoute
	pendingCommentSubjectTypeRoute
	pendingCommentOriginalCommitIDRoute
	pendingCommentCommitIDRoute
	pendingCommentPathRoute
	pendingCommentIDRoute
	pendingCommentPullRequestReviewIDRoute
)

type pendingCapabilityScalarRoute uint8

const (
	pendingCapabilityRepositoryNameRoute pendingCapabilityScalarRoute = iota + 1
	pendingCapabilityBaseOIDRoute
	pendingCapabilityHeadOIDRoute
	pendingCapabilityNumberRoute
	pendingCapabilityNodeIDRoute
	pendingCapabilityPageHasNextRoute
	pendingCapabilityPageCursorRoute
	pendingCapabilityIDRoute
	pendingCapabilityDatabaseIDRoute
	pendingCapabilityViewerCanUpdateRoute
	pendingCapabilityViewerCanDeleteRoute
)

type pendingCommentJSONCollector struct {
	currentPage pendingReviewCommentPage
	scope       *jsonDecodeScope
	visit       pendingCommentPageVisitor
	path        jsonPathTracker
	duplicates  jsonDuplicateGuard
	pageMark    int64
	rootCount   int
	pageIndex   int
	rootSeen    bool
	pageActive  bool
}

type pendingCommentPageVisitor func(int, *pendingReviewCommentPage) error

//nolint:dupl // Endpoint-specific visitors and diagnostics keep these boundaries schema-local.
func decodePendingCommentStream(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visit pendingCommentPageVisitor,
) error {
	if visit == nil {
		return errors.New("GitHub pending comment page visitor is nil")
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return err
	}
	defer scope.Close()
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		return err
	}
	collector := pendingCommentJSONCollector{scope: scope, visit: visit}
	defer collector.path.close()
	if err := walkJSONFrames(ctx, input, runCPU, ledger, metrics, collector.consume); err != nil {
		return fmt.Errorf("decode GitHub pending review comments: %w", err)
	}
	if !collector.rootSeen || collector.rootCount == 0 || collector.pageActive ||
		collector.pageIndex != collector.rootCount {
		return errors.New("GitHub pending comment pagination is incomplete")
	}
	return nil
}

func (collector *pendingCommentJSONCollector) consume(frame jsonFrame) error {
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
		if collector.path.depth == 1 || collector.path.depth == 2 {
			return errors.New("GitHub pending comment page is not an array")
		}
		if collector.path.depth == 3 {
			page, err := collector.page()
			if err != nil {
				return err
			}
			if len(*page) == cap(*page) {
				return errors.New("GitHub pending comment count exceeded preflight")
			}
			if err := collector.scope.reserve(jsonDecodedElementBytes); err != nil {
				return err
			}
			*page = append(*page, new(pendingReviewCommentPayload))
		}
		collector.duplicates.startObject(collector.path.depth)
		return nil
	case jsonObjectEnd:
		collector.duplicates.endObject(collector.path.depth)
		return collector.path.end(frame.Kind)
	case jsonArrayEnd:
		rootItem := collector.path.depth == 2 && collector.pageActive
		if err := collector.path.end(frame.Kind); err != nil {
			return err
		}
		if rootItem {
			return collector.finishPage()
		}
		return nil
	default:
		if collector.path.depth == 0 || collector.rootItemDepth() {
			return errors.New("GitHub pending comment page is not an array")
		}
		if collector.pageActive && collector.path.depth == 2 {
			return errors.New("GitHub pending review comment is not an object")
		}
		return collector.path.scalar(func(value jsonPathValue) error {
			return collector.scalar(value, frame)
		})
	}
}

func (collector *pendingCommentJSONCollector) startArray(count int) error {
	switch collector.path.depth {
	case 1:
		if collector.rootSeen || count == 0 {
			return errors.New("GitHub pending comment pagination returned no page")
		}
		collector.rootSeen = true
		collector.rootCount = count
	case 2:
		if !collector.rootSeen || collector.pageActive || collector.pageIndex >= collector.rootCount {
			return errors.New("GitHub pending comment page count exceeded preflight")
		}
		collector.pageMark = collector.scope.mark()
		if err := reserveJSONDecodedSlice(collector.scope, count); err != nil {
			return err
		}
		collector.currentPage = make(pendingReviewCommentPage, 0, count)
		collector.pageActive = true
	case 3:
		if collector.pageActive && collector.path.stack[1].kind == jsonArrayStart {
			return errors.New("GitHub pending review comment is not an object")
		}
	}
	return nil
}

func (collector *pendingCommentJSONCollector) scalar(value jsonPathValue, frame jsonFrame) error {
	route := collector.scalarRoute(value.fieldID)
	if route == 0 {
		return nil
	}
	comment, err := collector.comment()
	if err != nil {
		return err
	}
	switch route {
	case pendingCommentOriginalStartLineRoute:
		comment.OriginalStartLine, err = collector.scope.nullableUint32(frame)
	case pendingCommentLineRoute:
		comment.Line, err = collector.scope.nullableUint32(frame)
	case pendingCommentBodyRoute:
		comment.Body, err = collector.scope.nullableString(frame)
	case pendingCommentOriginalLineRoute:
		comment.OriginalLine, err = collector.scope.nullableUint32(frame)
	case pendingCommentInReplyToIDRoute:
		comment.InReplyToID, err = collector.scope.nullableGitHubUint64(frame)
	case pendingCommentStartLineRoute:
		comment.StartLine, err = collector.scope.nullableUint32(frame)
	case pendingCommentStartSideRoute:
		comment.StartSide, err = collector.scope.nullableString(frame)
	case pendingCommentSideRoute:
		comment.Side, err = collector.scope.string(frame)
	case pendingCommentNodeIDRoute:
		comment.NodeID, err = collector.scope.string(frame)
	case pendingCommentSubjectTypeRoute:
		comment.SubjectType, err = collector.scope.string(frame)
	case pendingCommentOriginalCommitIDRoute:
		comment.OriginalCommitID, err = collector.scope.string(frame)
	case pendingCommentCommitIDRoute:
		comment.CommitID, err = collector.scope.string(frame)
	case pendingCommentPathRoute:
		comment.Path, err = collector.scope.string(frame)
	case pendingCommentIDRoute:
		comment.ID, err = jsonGitHubUint64(frame)
	case pendingCommentPullRequestReviewIDRoute:
		comment.PullRequestReviewID, err = jsonGitHubUint64(frame)
	}
	return err
}

func (collector *pendingCommentJSONCollector) scalarRoute(field jsonPresenceField) pendingCommentScalarRoute {
	if field == 0 || collector.path.depth != 3 {
		return 0
	}
	var route pendingCommentScalarRoute
	switch field {
	case jsonFieldIDOriginalStartLine:
		route = pendingCommentOriginalStartLineRoute
	case jsonFieldIDLine:
		route = pendingCommentLineRoute
	case jsonFieldIDBody:
		route = pendingCommentBodyRoute
	case jsonFieldIDOriginalLine:
		route = pendingCommentOriginalLineRoute
	case jsonFieldIDInReplyToID:
		route = pendingCommentInReplyToIDRoute
	case jsonFieldIDStartLineREST:
		route = pendingCommentStartLineRoute
	case jsonFieldIDStartSide:
		route = pendingCommentStartSideRoute
	case jsonFieldIDSide:
		route = pendingCommentSideRoute
	case jsonFieldIDNodeID:
		route = pendingCommentNodeIDRoute
	case jsonFieldIDSubjectType:
		route = pendingCommentSubjectTypeRoute
	case jsonFieldIDOriginalCommitID:
		route = pendingCommentOriginalCommitIDRoute
	case jsonFieldIDCommitID:
		route = pendingCommentCommitIDRoute
	case jsonFieldIDPath:
		route = pendingCommentPathRoute
	case jsonFieldIDID:
		route = pendingCommentIDRoute
	case jsonFieldIDPullRequestReviewID:
		route = pendingCommentPullRequestReviewIDRoute
	default:
		return 0
	}
	if !collector.path.parents() {
		return 0
	}
	return route
}

func (collector *pendingCommentJSONCollector) page() (*pendingReviewCommentPage, error) {
	if !collector.pageActive {
		return nil, errors.New("GitHub pending comment value is outside a page")
	}
	return &collector.currentPage, nil
}

func (collector *pendingCommentJSONCollector) rootItemDepth() bool {
	return collector.path.depth == 1 && collector.path.stack[0].kind == jsonArrayStart
}

func (collector *pendingCommentJSONCollector) finishPage() error {
	if !collector.pageActive || collector.pageIndex >= collector.rootCount {
		return errors.New("GitHub pending comment page count exceeded preflight")
	}
	err := collector.visit(collector.pageIndex, &collector.currentPage)
	collector.currentPage = nil
	collector.pageActive = false
	collector.pageIndex++
	return errors.Join(err, collector.scope.releaseTo(collector.pageMark))
}

func (collector *pendingCommentJSONCollector) comment() (*pendingReviewCommentPayload, error) {
	page, err := collector.page()
	if err != nil || len(*page) == 0 {
		return nil, errors.Join(err, errors.New("GitHub pending comment value is outside a comment"))
	}
	return (*page)[len(*page)-1], nil
}

type pendingCapabilityJSONCollector struct {
	currentPage pendingCommentCapabilityPage
	scope       *jsonDecodeScope
	visit       pendingCapabilityPageVisitor
	path        jsonPathTracker
	duplicates  jsonDuplicateGuard
	pageMark    int64
	rootCount   int
	pageIndex   int
	rootSeen    bool
	pageActive  bool
}

type pendingCapabilityPageVisitor func(int, pendingPagePosition, *pendingCommentCapabilityPage) error

//nolint:dupl // Endpoint-specific visitors and diagnostics keep these boundaries schema-local.
func decodePendingCapabilityStream(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visit pendingCapabilityPageVisitor,
) error {
	if visit == nil {
		return errors.New("GitHub pending capability page visitor is nil")
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return err
	}
	defer scope.Close()
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		return err
	}
	collector := pendingCapabilityJSONCollector{scope: scope, visit: visit}
	defer collector.path.close()
	if err := walkJSONFrames(ctx, input, runCPU, ledger, metrics, collector.consume); err != nil {
		return fmt.Errorf("decode GitHub pending comment capabilities: %w", err)
	}
	if !collector.rootSeen || collector.rootCount == 0 || collector.pageActive ||
		collector.pageIndex != collector.rootCount {
		return errors.New("GitHub pending capability pagination is incomplete")
	}
	return nil
}

//nolint:dupl // Endpoint-specific state machines intentionally keep schemas separate.
func (collector *pendingCapabilityJSONCollector) consume(frame jsonFrame) error {
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
			return errors.New("GitHub pending capability page is not an object")
		}
		if collector.capabilityItemContainer() {
			return errors.New("GitHub pending comment capability is not an object")
		}
		return collector.path.scalar(func(value jsonPathValue) error {
			return collector.scalar(value, frame)
		})
	}
}

func (collector *pendingCapabilityJSONCollector) startArray(count int) error {
	switch {
	case collector.path.depth == 1:
		if collector.rootSeen || count == 0 {
			return errors.New("GitHub pending capability pagination returned no page")
		}
		collector.rootSeen = true
		collector.rootCount = count
	case collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart:
		return errors.New("GitHub pending capability page is not an object")
	case collector.capabilityItemParent():
		return errors.New("GitHub pending comment capability is not an object")
	case collector.path.suffix("errors"):
		page, err := collector.page()
		if err != nil {
			return err
		}
		if err := reserveJSONDecodedSlice(collector.scope, count); err != nil {
			return err
		}
		page.Errors = make(githubErrors, count)
	case collector.capabilityItemContainer():
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
		nodes := make(pendingCommentCapabilities, 0, count)
		connection.Nodes = &nodes
	}
	return nil
}

func (collector *pendingCapabilityJSONCollector) startObject() error {
	if collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart {
		if !collector.rootSeen || collector.pageActive || collector.pageIndex >= collector.rootCount {
			return errors.New("GitHub pending capability page count exceeded preflight")
		}
		collector.pageMark = collector.scope.mark()
		collector.currentPage = pendingCommentCapabilityPage{}
		collector.pageActive = true
		return nil
	}
	if collector.path.depth == 1 {
		return errors.New("GitHub pending capability root is not an array")
	}
	page, pageErr := collector.page()
	if pageErr != nil {
		return pageErr
	}
	switch {
	case collector.path.suffix("data", "repository"):
		if page == nil {
			return errors.New("GitHub repository is outside a capability page")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Repository = new(struct {
			PullRequest *struct {
				BaseRefOID string `json:"baseRefOid"`
				HeadRefOID string `json:"headRefOid"`
				Number     uint64 `json:"number"`
			} `json:"pullRequest"`
			NameWithOwner string `json:"nameWithOwner"`
		})
	case collector.path.suffix("data", "repository", "pullRequest"):
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub pull request is outside a capability repository")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Repository.PullRequest = new(struct {
			BaseRefOID string `json:"baseRefOid"`
			HeadRefOID string `json:"headRefOid"`
			Number     uint64 `json:"number"`
		})
	case collector.path.suffix("data", "node"):
		if page == nil {
			return errors.New("GitHub review node is outside a capability page")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Node = new(struct {
			Comments *pendingCapabilityConnection `json:"comments"`
			ID       string                       `json:"id"`
		})
	case collector.path.suffix("data", "node", "comments"):
		if page == nil || page.Data.Node == nil {
			return errors.New("GitHub comment connection is outside a capability node")
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		page.Data.Node.Comments = new(pendingCapabilityConnection)
	case collector.path.suffix("data", "node", "comments", "pageInfo"):
		connection, err := collector.connection()
		if err != nil {
			return err
		}
		if err := collector.scope.reserve(jsonDecodedPointerBytes); err != nil {
			return err
		}
		connection.PageInfo = new(remotePageInfo)
	case collector.capabilityItemParent():
		connection, err := collector.connection()
		if err != nil || connection.Nodes == nil {
			return errors.Join(err, errors.New("GitHub capability is outside its nodes"))
		}
		if len(*connection.Nodes) == cap(*connection.Nodes) {
			return errors.New("GitHub capability count exceeded preflight")
		}
		*connection.Nodes = append(*connection.Nodes, pendingCommentCapability{})
	}
	return nil
}

func (collector *pendingCapabilityJSONCollector) scalar(value jsonPathValue, frame jsonFrame) error {
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
	case pendingCapabilityRepositoryNameRoute:
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub repository name is outside its object")
		}
		page.Data.Repository.NameWithOwner, err = collector.scope.string(frame)
	case pendingCapabilityBaseOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.BaseRefOID, err = collector.scope.string(frame)
	case pendingCapabilityHeadOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.HeadRefOID, err = collector.scope.string(frame)
	case pendingCapabilityNumberRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.Number, err = jsonUint64Value(frame)
	case pendingCapabilityNodeIDRoute:
		if page == nil || page.Data.Node == nil {
			return errors.New("GitHub review identity is outside its node")
		}
		page.Data.Node.ID, err = collector.scope.string(frame)
	case pendingCapabilityPageHasNextRoute:
		connection, connectionErr := collector.connection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub capability page info is incomplete"))
		}
		connection.PageInfo.HasNextPage, err = collector.scope.nullableBoolean(frame)
	case pendingCapabilityPageCursorRoute:
		connection, connectionErr := collector.connection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub capability page info is incomplete"))
		}
		connection.PageInfo.EndCursor, err = collector.scope.nullableString(frame)
	case pendingCapabilityIDRoute, pendingCapabilityDatabaseIDRoute,
		pendingCapabilityViewerCanUpdateRoute, pendingCapabilityViewerCanDeleteRoute:
		capability, capabilityErr := collector.capability()
		if capabilityErr != nil {
			return capabilityErr
		}
		switch route {
		case pendingCapabilityIDRoute:
			capability.ID, err = collector.scope.string(frame)
		case pendingCapabilityDatabaseIDRoute:
			capability.FullDatabaseID, err = jsonGitHubUint64(frame)
		case pendingCapabilityViewerCanUpdateRoute:
			capability.ViewerCanUpdate, err = collector.scope.nullableBoolean(frame)
		case pendingCapabilityViewerCanDeleteRoute:
			capability.ViewerCanDelete, err = collector.scope.nullableBoolean(frame)
		default:
			return errors.New("GitHub pending capability scalar route is invalid")
		}
	}
	return err
}

func (collector *pendingCapabilityJSONCollector) scalarRoute(field jsonPresenceField) pendingCapabilityScalarRoute {
	var route pendingCapabilityScalarRoute
	switch field {
	case jsonFieldIDNameWithOwner:
		route = pendingCapabilityRepositoryNameRoute
	case jsonFieldIDBaseRefOID:
		route = pendingCapabilityBaseOIDRoute
	case jsonFieldIDHeadRefOID:
		route = pendingCapabilityHeadOIDRoute
	case jsonFieldIDNumber:
		route = pendingCapabilityNumberRoute
	case jsonFieldIDID:
		parent, _ := collector.path.namedAncestor(0)
		switch parent {
		case jsonFieldIDNode:
			route = pendingCapabilityNodeIDRoute
		case jsonFieldIDNodes:
			route = pendingCapabilityIDRoute
		default:
			return 0
		}
	case jsonFieldIDHasNextPage:
		route = pendingCapabilityPageHasNextRoute
	case jsonFieldIDEndCursor:
		route = pendingCapabilityPageCursorRoute
	case jsonFieldIDFullDatabaseID:
		route = pendingCapabilityDatabaseIDRoute
	case jsonFieldIDViewerCanUpdate:
		route = pendingCapabilityViewerCanUpdateRoute
	case jsonFieldIDViewerCanDelete:
		route = pendingCapabilityViewerCanDeleteRoute
	default:
		return 0
	}
	var matched bool
	switch route {
	case pendingCapabilityRepositoryNameRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository)
	case pendingCapabilityBaseOIDRoute, pendingCapabilityHeadOIDRoute, pendingCapabilityNumberRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	case pendingCapabilityNodeIDRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDNode)
	case pendingCapabilityPageHasNextRoute, pendingCapabilityPageCursorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDPageInfo,
		)
	case pendingCapabilityIDRoute, pendingCapabilityDatabaseIDRoute,
		pendingCapabilityViewerCanUpdateRoute, pendingCapabilityViewerCanDeleteRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDNodes,
		)
	}
	if !matched {
		return 0
	}
	return route
}

func (collector *pendingCapabilityJSONCollector) page() (*pendingCommentCapabilityPage, error) {
	if !collector.pageActive {
		return nil, errors.New("GitHub capability value is outside a page")
	}
	return &collector.currentPage, nil
}

func (collector *pendingCapabilityJSONCollector) rootItemDepth() bool {
	return collector.path.depth == 1 && collector.path.stack[0].kind == jsonArrayStart
}

func (collector *pendingCapabilityJSONCollector) capabilityItemContainer() bool {
	if collector.path.depth == 0 {
		return false
	}
	container := collector.path.stack[collector.path.depth-1]
	return container.kind == jsonArrayStart && container.name == jsonFieldNodes &&
		collector.path.suffix("data", "node", "comments", jsonFieldNodes)
}

func (collector *pendingCapabilityJSONCollector) capabilityItemParent() bool {
	if collector.path.depth < 2 {
		return false
	}
	parent := collector.path.stack[collector.path.depth-2]
	return parent.kind == jsonArrayStart && parent.name == jsonFieldNodes &&
		collector.path.suffix("data", "node", "comments", jsonFieldNodes)
}

func (collector *pendingCapabilityJSONCollector) finishPage() error {
	if !collector.pageActive || collector.pageIndex >= collector.rootCount {
		return errors.New("GitHub pending capability page count exceeded preflight")
	}
	position := pendingJSONPageIntermediate
	if collector.pageIndex+1 == collector.rootCount {
		position = pendingJSONPageFinal
	}
	err := collector.visit(collector.pageIndex, position, &collector.currentPage)
	collector.currentPage = pendingCommentCapabilityPage{}
	collector.pageActive = false
	collector.pageIndex++
	return errors.Join(err, collector.scope.releaseTo(collector.pageMark))
}

func (collector *pendingCapabilityJSONCollector) pullRequest() (*struct {
	BaseRefOID string `json:"baseRefOid"`
	HeadRefOID string `json:"headRefOid"`
	Number     uint64 `json:"number"`
}, error) {
	page, err := collector.page()
	if err != nil || page.Data.Repository == nil || page.Data.Repository.PullRequest == nil {
		return nil, errors.Join(err, errors.New("GitHub capability pull request is unavailable"))
	}
	return page.Data.Repository.PullRequest, nil
}

func (collector *pendingCapabilityJSONCollector) connection() (*pendingCapabilityConnection, error) {
	page, err := collector.page()
	if err != nil || page.Data.Node == nil || page.Data.Node.Comments == nil {
		return nil, errors.Join(err, errors.New("GitHub capability connection is unavailable"))
	}
	return page.Data.Node.Comments, nil
}

func (collector *pendingCapabilityJSONCollector) capability() (*pendingCommentCapability, error) {
	connection, err := collector.connection()
	if err != nil || connection.Nodes == nil || len(*connection.Nodes) == 0 {
		return nil, errors.Join(err, errors.New("GitHub capability is unavailable"))
	}
	return &(*connection.Nodes)[len(*connection.Nodes)-1], nil
}
