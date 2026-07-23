package source

import (
	"context"
	"errors"
	"fmt"
	"io"
)

type remoteThreadScalarRoute uint8

const (
	remoteThreadRepositoryNameRoute remoteThreadScalarRoute = iota + 1
	remoteThreadBaseOIDRoute
	remoteThreadHeadOIDRoute
	remoteThreadNumberRoute
	remoteThreadPageHasNextRoute
	remoteThreadPageCursorRoute
	remoteThreadIDRoute
	remoteThreadPathRoute
	remoteThreadDiffSideRoute
	remoteThreadResolvedRoute
	remoteThreadOutdatedRoute
	remoteThreadLineRoute
	remoteThreadStartLineRoute
	remoteThreadCommentPageHasNextRoute
	remoteThreadCommentPageCursorRoute
	remoteThreadCommentIDRoute
	remoteThreadCommentBodyRoute
	remoteThreadCommentCreatedAtRoute
	remoteThreadCommentURLRoute
	remoteThreadCommentAuthorRoute
	remoteThreadCommentLoginRoute
)

type remoteThreadJSONCollector struct {
	scope      *jsonDecodeScope
	visit      func(int, remotePagePosition, remoteThreadPage) (int64, error)
	current    remoteThreadPage
	path       jsonPathTracker
	duplicates jsonDuplicateGuard
	pageMark   int64
	pageCount  int
	pageIndex  int
	pageActive bool
}

//nolint:dupl // Endpoint-specific decode lifecycle deliberately mirrors comment pages.
func decodeRemoteThreadStream(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visit func(int, remotePagePosition, remoteThreadPage) (int64, error),
) error {
	if visit == nil {
		return errors.New("GitHub review thread page visitor is nil")
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return err
	}
	defer scope.Close()
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		return err
	}
	collector := remoteThreadJSONCollector{scope: scope, visit: visit}
	defer collector.path.close()
	if err := walkJSONFrames(ctx, input, runCPU, ledger, metrics, collector.consume); err != nil {
		return fmt.Errorf("decode GitHub review threads: %w", err)
	}
	if collector.pageCount == 0 || collector.pageIndex != collector.pageCount || collector.pageActive {
		return errors.New("GitHub review thread pagination returned incomplete pages")
	}
	return nil
}

func (collector *remoteThreadJSONCollector) consume(frame jsonFrame) error {
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
		if collector.path.depth > 1 {
			if err := reserveJSONDecodedSlice(collector.scope, frame.Count); err != nil {
				return err
			}
		}
		return collector.startArray(frame.Count)
	case jsonObjectStart:
		if err := collector.path.start(frame.Kind, 0); err != nil {
			return err
		}
		if collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart {
			if collector.pageActive {
				return errors.New("GitHub review thread pages overlap")
			}
			collector.pageMark = collector.scope.mark()
			collector.pageActive = true
		}
		if err := collector.scope.reserve(jsonDecodedElementBytes); err != nil {
			return err
		}
		collector.duplicates.startObject(collector.path.depth)
		return collector.startObject()
	case jsonObjectEnd:
		rootItem := collector.path.depth == 2 &&
			collector.path.stack[0].kind == jsonArrayStart
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
		return collector.path.scalar(func(value jsonPathValue) error {
			return collector.scalar(value, frame)
		})
	}
}

func (collector *remoteThreadJSONCollector) startArray(count int) error {
	switch {
	case collector.path.depth == 1:
		collector.pageCount = count
	case collector.path.suffix("errors"):
		page, err := collector.page()
		if err != nil {
			return err
		}
		page.Errors = make(githubErrors, count)
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes):
		page, err := collector.page()
		if err != nil || page.Data.Repository == nil || page.Data.Repository.PullRequest == nil ||
			page.Data.Repository.PullRequest.ReviewThreads == nil {
			return errors.Join(err, errors.New("GitHub review thread nodes are outside their connection"))
		}
		nodes := make(remoteThreadNodes, 0, count)
		page.Data.Repository.PullRequest.ReviewThreads.Nodes = &nodes
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes, "comments", jsonFieldNodes):
		thread, err := collector.thread()
		if err != nil || thread.Comments == nil {
			return errors.Join(err, errors.New("GitHub review comments are outside their connection"))
		}
		nodes := make(remoteCommentNodes, 0, count)
		thread.Comments.Nodes = &nodes
	}
	return nil
}

func (collector *remoteThreadJSONCollector) startObject() error {
	if collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart {
		if collector.pageIndex >= collector.pageCount {
			return errors.New("GitHub review thread page count exceeded preflight")
		}
		collector.current = remoteThreadPage{}
		return nil
	}
	page := collector.currentPage()
	switch {
	case collector.path.suffix("data", "repository"):
		if page == nil {
			return errors.New("GitHub repository is outside a review thread page")
		}
		page.Data.Repository = new(struct {
			PullRequest *struct {
				ReviewThreads *remoteThreadConnection `json:"reviewThreads"`
				BaseRefOID    string                  `json:"baseRefOid"`
				HeadRefOID    string                  `json:"headRefOid"`
				Number        uint64                  `json:"number"`
			} `json:"pullRequest"`
			NameWithOwner string `json:"nameWithOwner"`
		})
	case collector.path.suffix("data", "repository", "pullRequest"):
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub review thread pull request is outside its repository")
		}
		page.Data.Repository.PullRequest = new(struct {
			ReviewThreads *remoteThreadConnection `json:"reviewThreads"`
			BaseRefOID    string                  `json:"baseRefOid"`
			HeadRefOID    string                  `json:"headRefOid"`
			Number        uint64                  `json:"number"`
		})
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads"):
		pull, err := collector.pullRequest()
		if err != nil {
			return err
		}
		pull.ReviewThreads = new(remoteThreadConnection)
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", "pageInfo"):
		connection, err := collector.threadConnection()
		if err != nil {
			return err
		}
		connection.PageInfo = new(remotePageInfo)
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes):
		connection, err := collector.threadConnection()
		if err != nil || connection.Nodes == nil {
			return errors.Join(err, errors.New("GitHub review thread object is outside its nodes array"))
		}
		*connection.Nodes = append(*connection.Nodes, remoteThreadNode{})
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes, "comments"):
		thread, err := collector.thread()
		if err != nil {
			return err
		}
		thread.Comments = new(remoteCommentConnection)
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes, "comments", "pageInfo"):
		thread, err := collector.thread()
		if err != nil || thread.Comments == nil {
			return errors.Join(err, errors.New("GitHub review comment page info is outside its connection"))
		}
		thread.Comments.PageInfo = new(remotePageInfo)
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes, "comments", jsonFieldNodes):
		thread, err := collector.thread()
		if err != nil || thread.Comments == nil || thread.Comments.Nodes == nil {
			return errors.Join(err, errors.New("GitHub review comment object is outside its nodes array"))
		}
		*thread.Comments.Nodes = append(*thread.Comments.Nodes, remoteCommentNode{})
	case collector.path.suffix("data", "repository", "pullRequest", "reviewThreads", jsonFieldNodes, "comments", jsonFieldNodes, "author"):
		comment, err := collector.comment()
		if err != nil {
			return err
		}
		comment.Author = new(struct {
			Login string `json:"login"`
		})
	}
	return nil
}

//nolint:cyclop,dupl,funlen,maintidx,nilerr // Schema-local assignment preserves optional nested page-info behavior.
func (collector *remoteThreadJSONCollector) scalar(value jsonPathValue, frame jsonFrame) error {
	route := collector.scalarRoute(value.fieldID)
	if route == 0 {
		return nil
	}
	page := collector.currentPage()
	var err error
	switch route {
	case remoteThreadRepositoryNameRoute:
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub repository name is outside its object")
		}
		page.Data.Repository.NameWithOwner, err = collector.scope.string(frame)
	case remoteThreadBaseOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.BaseRefOID, err = collector.scope.string(frame)
	case remoteThreadHeadOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.HeadRefOID, err = collector.scope.string(frame)
	case remoteThreadNumberRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.Number, err = jsonUint64Value(frame)
	case remoteThreadPageHasNextRoute:
		connection, connectionErr := collector.threadConnection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub thread page info is incomplete"))
		}
		connection.PageInfo.HasNextPage, err = collector.scope.nullableBoolean(frame)
	case remoteThreadPageCursorRoute:
		connection, connectionErr := collector.threadConnection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub thread page info is incomplete"))
		}
		connection.PageInfo.EndCursor, err = collector.scope.nullableString(frame)
	case remoteThreadIDRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.ID, err = collector.scope.string(frame)
	case remoteThreadPathRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.Path, err = collector.scope.string(frame)
	case remoteThreadDiffSideRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.DiffSide, err = collector.scope.string(frame)
	case remoteThreadResolvedRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.IsResolved, err = collector.scope.nullableBoolean(frame)
	case remoteThreadOutdatedRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.IsOutdated, err = collector.scope.nullableBoolean(frame)
	case remoteThreadLineRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.Line, err = collector.scope.nullableUint32(frame)
	case remoteThreadStartLineRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil {
			return threadErr
		}
		thread.StartLine, err = collector.scope.nullableUint32(frame)
	case remoteThreadCommentPageHasNextRoute, remoteThreadCommentPageCursorRoute:
		thread, threadErr := collector.thread()
		if threadErr != nil || thread.Comments == nil || thread.Comments.PageInfo == nil {
			return nil
		}
		if route == remoteThreadCommentPageHasNextRoute {
			thread.Comments.PageInfo.HasNextPage, err = collector.scope.nullableBoolean(frame)
		} else {
			thread.Comments.PageInfo.EndCursor, err = collector.scope.nullableString(frame)
		}
	case remoteThreadCommentIDRoute, remoteThreadCommentBodyRoute,
		remoteThreadCommentCreatedAtRoute, remoteThreadCommentURLRoute,
		remoteThreadCommentAuthorRoute, remoteThreadCommentLoginRoute:
		comment, commentErr := collector.comment()
		if commentErr != nil {
			return commentErr
		}
		switch route {
		case remoteThreadCommentIDRoute:
			comment.ID, err = collector.scope.string(frame)
		case remoteThreadCommentBodyRoute:
			comment.Body, err = collector.scope.nullableString(frame)
		case remoteThreadCommentCreatedAtRoute:
			comment.CreatedAt, err = collector.scope.string(frame)
		case remoteThreadCommentURLRoute:
			comment.URL, err = collector.scope.string(frame)
		case remoteThreadCommentAuthorRoute:
			if frame.Kind != jsonNull {
				return errors.New("GitHub review comment author is not an object or null")
			}
			comment.Author = nil
		case remoteThreadCommentLoginRoute:
			if comment.Author == nil {
				return errors.New("GitHub review comment login is outside its author")
			}
			comment.Author.Login, err = collector.scope.string(frame)
		default:
			return errors.New("GitHub review comment scalar route is invalid")
		}
	}
	return err
}

//nolint:cyclop,funlen // The closed switch selects one schema-local route before one exact parent check.
func (collector *remoteThreadJSONCollector) scalarRoute(field jsonPresenceField) remoteThreadScalarRoute {
	var route remoteThreadScalarRoute
	switch field {
	case jsonFieldIDNameWithOwner:
		route = remoteThreadRepositoryNameRoute
	case jsonFieldIDBaseRefOID:
		route = remoteThreadBaseOIDRoute
	case jsonFieldIDHeadRefOID:
		route = remoteThreadHeadOIDRoute
	case jsonFieldIDNumber:
		route = remoteThreadNumberRoute
	case jsonFieldIDHasNextPage:
		ancestor, _ := collector.path.namedAncestor(1)
		switch ancestor {
		case jsonFieldIDReviewThreads:
			route = remoteThreadPageHasNextRoute
		case jsonFieldIDComments:
			route = remoteThreadCommentPageHasNextRoute
		default:
			return 0
		}
	case jsonFieldIDEndCursor:
		ancestor, _ := collector.path.namedAncestor(1)
		switch ancestor {
		case jsonFieldIDReviewThreads:
			route = remoteThreadPageCursorRoute
		case jsonFieldIDComments:
			route = remoteThreadCommentPageCursorRoute
		default:
			return 0
		}
	case jsonFieldIDID:
		ancestor, _ := collector.path.namedAncestor(1)
		switch ancestor {
		case jsonFieldIDReviewThreads:
			route = remoteThreadIDRoute
		case jsonFieldIDComments:
			route = remoteThreadCommentIDRoute
		default:
			return 0
		}
	case jsonFieldIDPath:
		route = remoteThreadPathRoute
	case jsonFieldIDDiffSide:
		route = remoteThreadDiffSideRoute
	case jsonFieldIDIsResolved:
		route = remoteThreadResolvedRoute
	case jsonFieldIDIsOutdated:
		route = remoteThreadOutdatedRoute
	case jsonFieldIDLine:
		route = remoteThreadLineRoute
	case jsonFieldIDStartLine:
		route = remoteThreadStartLineRoute
	case jsonFieldIDBody:
		route = remoteThreadCommentBodyRoute
	case jsonFieldIDCreatedAt:
		route = remoteThreadCommentCreatedAtRoute
	case jsonFieldIDURL:
		route = remoteThreadCommentURLRoute
	case jsonFieldIDAuthor:
		route = remoteThreadCommentAuthorRoute
	case jsonFieldIDLogin:
		route = remoteThreadCommentLoginRoute
	default:
		return 0
	}
	matched := false
	switch route {
	case remoteThreadRepositoryNameRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository)
	case remoteThreadBaseOIDRoute, remoteThreadHeadOIDRoute, remoteThreadNumberRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	case remoteThreadPageHasNextRoute, remoteThreadPageCursorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviewThreads, jsonFieldIDPageInfo,
		)
	case remoteThreadIDRoute, remoteThreadPathRoute, remoteThreadDiffSideRoute,
		remoteThreadResolvedRoute, remoteThreadOutdatedRoute, remoteThreadLineRoute,
		remoteThreadStartLineRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviewThreads, jsonFieldIDNodes,
		)
	case remoteThreadCommentPageHasNextRoute, remoteThreadCommentPageCursorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviewThreads, jsonFieldIDNodes, jsonFieldIDComments, jsonFieldIDPageInfo,
		)
	case remoteThreadCommentIDRoute, remoteThreadCommentBodyRoute,
		remoteThreadCommentCreatedAtRoute, remoteThreadCommentURLRoute,
		remoteThreadCommentAuthorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviewThreads, jsonFieldIDNodes, jsonFieldIDComments, jsonFieldIDNodes,
		)
	case remoteThreadCommentLoginRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest,
			jsonFieldIDReviewThreads, jsonFieldIDNodes, jsonFieldIDComments, jsonFieldIDNodes,
			jsonFieldIDAuthor,
		)
	}
	if !matched {
		return 0
	}
	return route
}

func (collector *remoteThreadJSONCollector) page() (*remoteThreadPage, error) {
	page := collector.currentPage()
	if page == nil {
		return nil, errors.New("GitHub review thread value is outside a page")
	}
	return page, nil
}

func (collector *remoteThreadJSONCollector) currentPage() *remoteThreadPage {
	if collector == nil || !collector.pageActive {
		return nil
	}
	return &collector.current
}

//nolint:dupl // Endpoint-specific page ownership deliberately mirrors comment pages.
func (collector *remoteThreadJSONCollector) finishPage() error {
	if !collector.pageActive || collector.pageIndex >= collector.pageCount {
		return errors.New("GitHub review thread page completion is invalid")
	}
	position := remotePageIntermediate
	if collector.pageIndex+1 == collector.pageCount {
		position = remotePageFinal
	}
	retained, err := collector.visit(collector.pageIndex, position, collector.current)
	if err != nil {
		return err
	}
	if err := collector.scope.transfer(retained); err != nil {
		return err
	}
	if err := collector.scope.releaseTo(collector.pageMark); err != nil {
		return err
	}
	collector.current = remoteThreadPage{}
	collector.pageIndex++
	collector.pageActive = false
	return nil
}

func (collector *remoteThreadJSONCollector) pullRequest() (*struct {
	ReviewThreads *remoteThreadConnection `json:"reviewThreads"`
	BaseRefOID    string                  `json:"baseRefOid"`
	HeadRefOID    string                  `json:"headRefOid"`
	Number        uint64                  `json:"number"`
}, error) {
	page, err := collector.page()
	if err != nil || page.Data.Repository == nil || page.Data.Repository.PullRequest == nil {
		return nil, errors.Join(err, errors.New("GitHub pull request is outside its repository"))
	}
	return page.Data.Repository.PullRequest, nil
}

func (collector *remoteThreadJSONCollector) threadConnection() (*remoteThreadConnection, error) {
	pull, err := collector.pullRequest()
	if err != nil || pull.ReviewThreads == nil {
		return nil, errors.Join(err, errors.New("GitHub review thread connection is unavailable"))
	}
	return pull.ReviewThreads, nil
}

func (collector *remoteThreadJSONCollector) thread() (*remoteThreadNode, error) {
	connection, err := collector.threadConnection()
	if err != nil || connection.Nodes == nil || len(*connection.Nodes) == 0 {
		return nil, errors.Join(err, errors.New("GitHub review thread node is unavailable"))
	}
	return &(*connection.Nodes)[len(*connection.Nodes)-1], nil
}

func (collector *remoteThreadJSONCollector) comment() (*remoteCommentNode, error) {
	thread, err := collector.thread()
	if err != nil || thread.Comments == nil || thread.Comments.Nodes == nil || len(*thread.Comments.Nodes) == 0 {
		return nil, errors.Join(err, errors.New("GitHub review comment node is unavailable"))
	}
	return &(*thread.Comments.Nodes)[len(*thread.Comments.Nodes)-1], nil
}
