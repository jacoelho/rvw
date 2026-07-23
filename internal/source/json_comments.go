package source

import (
	"context"
	"errors"
	"fmt"
	"io"
)

type remoteCommentScalarRoute uint8

const (
	remoteCommentRepositoryNameRoute remoteCommentScalarRoute = iota + 1
	remoteCommentBaseOIDRoute
	remoteCommentHeadOIDRoute
	remoteCommentNumberRoute
	remoteCommentNodeIDRoute
	remoteCommentPageHasNextRoute
	remoteCommentPageCursorRoute
	remoteCommentIDRoute
	remoteCommentBodyRoute
	remoteCommentCreatedAtRoute
	remoteCommentURLRoute
	remoteCommentAuthorRoute
	remoteCommentLoginRoute
)

type remoteCommentJSONCollector struct {
	scope      *jsonDecodeScope
	visit      func(int, remotePagePosition, remoteCommentPage) (int64, error)
	current    remoteCommentPage
	path       jsonPathTracker
	duplicates jsonDuplicateGuard
	pageMark   int64
	pageCount  int
	pageIndex  int
	pageActive bool
}

//nolint:dupl // Endpoint-specific decode lifecycle deliberately mirrors thread pages.
func decodeRemoteCommentStream(
	ctx context.Context,
	input io.Reader,
	runCPU CPUAdmission,
	ledger *sourceLedger,
	metrics jsonCommandMetrics,
	visit func(int, remotePagePosition, remoteCommentPage) (int64, error),
) error {
	if visit == nil {
		return errors.New("GitHub review comment page visitor is nil")
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return err
	}
	defer scope.Close()
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		return err
	}
	collector := remoteCommentJSONCollector{scope: scope, visit: visit}
	defer collector.path.close()
	if err := walkJSONFrames(ctx, input, runCPU, ledger, metrics, collector.consume); err != nil {
		return fmt.Errorf("decode GitHub review comments: %w", err)
	}
	if collector.pageCount == 0 || collector.pageIndex != collector.pageCount || collector.pageActive {
		return errors.New("GitHub review comment pagination returned incomplete pages")
	}
	return nil
}

func (collector *remoteCommentJSONCollector) consume(frame jsonFrame) error {
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
				return errors.New("GitHub review comment pages overlap")
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

func (collector *remoteCommentJSONCollector) startArray(count int) error {
	switch {
	case collector.path.depth == 1:
		collector.pageCount = count
	case collector.path.suffix("errors"):
		page, err := collector.page()
		if err != nil {
			return err
		}
		page.Errors = make(githubErrors, count)
	case collector.path.suffix("data", "node", "comments", jsonFieldNodes):
		page, err := collector.page()
		if err != nil || page.Data.Node == nil || page.Data.Node.Comments == nil {
			return errors.Join(err, errors.New("GitHub comment nodes are outside their connection"))
		}
		nodes := make(remoteCommentNodes, 0, count)
		page.Data.Node.Comments.Nodes = &nodes
	}
	return nil
}

func (collector *remoteCommentJSONCollector) startObject() error {
	if collector.path.depth == 2 && collector.path.stack[0].kind == jsonArrayStart {
		if collector.pageIndex >= collector.pageCount {
			return errors.New("GitHub comment page count exceeded preflight")
		}
		collector.current = remoteCommentPage{}
		return nil
	}
	page := collector.currentPage()
	switch {
	case collector.path.suffix("data", "repository"):
		if page == nil {
			return errors.New("GitHub repository is outside a comment page")
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
			return errors.New("GitHub comment pull request is outside its repository")
		}
		page.Data.Repository.PullRequest = new(struct {
			BaseRefOID string `json:"baseRefOid"`
			HeadRefOID string `json:"headRefOid"`
			Number     uint64 `json:"number"`
		})
	case collector.path.suffix("data", "node"):
		if page == nil {
			return errors.New("GitHub node is outside a comment page")
		}
		page.Data.Node = new(struct {
			Comments *remoteCommentConnection `json:"comments"`
			ID       string                   `json:"id"`
		})
	case collector.path.suffix("data", "node", "comments"):
		if page == nil || page.Data.Node == nil {
			return errors.New("GitHub comment connection is outside its node")
		}
		page.Data.Node.Comments = new(remoteCommentConnection)
	case collector.path.suffix("data", "node", "comments", "pageInfo"):
		connection, err := collector.connection()
		if err != nil {
			return err
		}
		connection.PageInfo = new(remotePageInfo)
	case collector.path.suffix("data", "node", "comments", jsonFieldNodes):
		connection, err := collector.connection()
		if err != nil || connection.Nodes == nil {
			return errors.Join(err, errors.New("GitHub comment object is outside its nodes"))
		}
		*connection.Nodes = append(*connection.Nodes, remoteCommentNode{})
	case collector.path.suffix("data", "node", "comments", jsonFieldNodes, "author"):
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

//nolint:dupl // Endpoint-specific field assignment must remain schema-local.
func (collector *remoteCommentJSONCollector) scalar(value jsonPathValue, frame jsonFrame) error {
	route := collector.scalarRoute(value.fieldID)
	if route == 0 {
		return nil
	}
	page := collector.currentPage()
	var err error
	switch route {
	case remoteCommentRepositoryNameRoute:
		if page == nil || page.Data.Repository == nil {
			return errors.New("GitHub repository name is outside its object")
		}
		page.Data.Repository.NameWithOwner, err = collector.scope.string(frame)
	case remoteCommentBaseOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.BaseRefOID, err = collector.scope.string(frame)
	case remoteCommentHeadOIDRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.HeadRefOID, err = collector.scope.string(frame)
	case remoteCommentNumberRoute:
		pull, pullErr := collector.pullRequest()
		if pullErr != nil {
			return pullErr
		}
		pull.Number, err = jsonUint64Value(frame)
	case remoteCommentNodeIDRoute:
		if page == nil || page.Data.Node == nil {
			return errors.New("GitHub node identity is outside its object")
		}
		page.Data.Node.ID, err = collector.scope.string(frame)
	case remoteCommentPageHasNextRoute:
		connection, connectionErr := collector.connection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub comment page info is incomplete"))
		}
		connection.PageInfo.HasNextPage, err = collector.scope.nullableBoolean(frame)
	case remoteCommentPageCursorRoute:
		connection, connectionErr := collector.connection()
		if connectionErr != nil || connection.PageInfo == nil {
			return errors.Join(connectionErr, errors.New("GitHub comment page info is incomplete"))
		}
		connection.PageInfo.EndCursor, err = collector.scope.nullableString(frame)
	case remoteCommentIDRoute, remoteCommentBodyRoute, remoteCommentCreatedAtRoute,
		remoteCommentURLRoute, remoteCommentAuthorRoute, remoteCommentLoginRoute:
		comment, commentErr := collector.comment()
		if commentErr != nil {
			return commentErr
		}
		switch route {
		case remoteCommentIDRoute:
			comment.ID, err = collector.scope.string(frame)
		case remoteCommentBodyRoute:
			comment.Body, err = collector.scope.nullableString(frame)
		case remoteCommentCreatedAtRoute:
			comment.CreatedAt, err = collector.scope.string(frame)
		case remoteCommentURLRoute:
			comment.URL, err = collector.scope.string(frame)
		case remoteCommentAuthorRoute:
			if frame.Kind != jsonNull {
				return errors.New("GitHub comment author is not an object or null")
			}
			comment.Author = nil
		case remoteCommentLoginRoute:
			if comment.Author == nil {
				return errors.New("GitHub comment login is outside its author")
			}
			comment.Author.Login, err = collector.scope.string(frame)
		default:
			return errors.New("GitHub comment scalar route is invalid")
		}
	}
	return err
}

func (collector *remoteCommentJSONCollector) scalarRoute(field jsonPresenceField) remoteCommentScalarRoute {
	var route remoteCommentScalarRoute
	switch field {
	case jsonFieldIDNameWithOwner:
		route = remoteCommentRepositoryNameRoute
	case jsonFieldIDBaseRefOID:
		route = remoteCommentBaseOIDRoute
	case jsonFieldIDHeadRefOID:
		route = remoteCommentHeadOIDRoute
	case jsonFieldIDNumber:
		route = remoteCommentNumberRoute
	case jsonFieldIDID:
		parent, _ := collector.path.namedAncestor(0)
		switch parent {
		case jsonFieldIDNode:
			route = remoteCommentNodeIDRoute
		case jsonFieldIDNodes:
			route = remoteCommentIDRoute
		default:
			return 0
		}
	case jsonFieldIDHasNextPage:
		route = remoteCommentPageHasNextRoute
	case jsonFieldIDEndCursor:
		route = remoteCommentPageCursorRoute
	case jsonFieldIDBody:
		route = remoteCommentBodyRoute
	case jsonFieldIDCreatedAt:
		route = remoteCommentCreatedAtRoute
	case jsonFieldIDURL:
		route = remoteCommentURLRoute
	case jsonFieldIDAuthor:
		route = remoteCommentAuthorRoute
	case jsonFieldIDLogin:
		route = remoteCommentLoginRoute
	default:
		return 0
	}
	matched := false
	switch route {
	case remoteCommentRepositoryNameRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository)
	case remoteCommentBaseOIDRoute, remoteCommentHeadOIDRoute, remoteCommentNumberRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	case remoteCommentNodeIDRoute:
		matched = collector.path.parents(jsonFieldIDData, jsonFieldIDNode)
	case remoteCommentPageHasNextRoute, remoteCommentPageCursorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDPageInfo,
		)
	case remoteCommentIDRoute, remoteCommentBodyRoute, remoteCommentCreatedAtRoute,
		remoteCommentURLRoute, remoteCommentAuthorRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDNodes,
		)
	case remoteCommentLoginRoute:
		matched = collector.path.parents(
			jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDNodes, jsonFieldIDAuthor,
		)
	}
	if !matched {
		return 0
	}
	return route
}

func (collector *remoteCommentJSONCollector) page() (*remoteCommentPage, error) {
	page := collector.currentPage()
	if page == nil {
		return nil, errors.New("GitHub comment value is outside a page")
	}
	return page, nil
}

func (collector *remoteCommentJSONCollector) currentPage() *remoteCommentPage {
	if collector == nil || !collector.pageActive {
		return nil
	}
	return &collector.current
}

//nolint:dupl // Endpoint-specific page ownership deliberately mirrors thread pages.
func (collector *remoteCommentJSONCollector) finishPage() error {
	if !collector.pageActive || collector.pageIndex >= collector.pageCount {
		return errors.New("GitHub review comment page completion is invalid")
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
	collector.current = remoteCommentPage{}
	collector.pageIndex++
	collector.pageActive = false
	return nil
}

func (collector *remoteCommentJSONCollector) pullRequest() (*struct {
	BaseRefOID string `json:"baseRefOid"`
	HeadRefOID string `json:"headRefOid"`
	Number     uint64 `json:"number"`
}, error) {
	page, err := collector.page()
	if err != nil || page.Data.Repository == nil || page.Data.Repository.PullRequest == nil {
		return nil, errors.Join(err, errors.New("GitHub comment pull request is unavailable"))
	}
	return page.Data.Repository.PullRequest, nil
}

func (collector *remoteCommentJSONCollector) connection() (*remoteCommentConnection, error) {
	page, err := collector.page()
	if err != nil || page.Data.Node == nil || page.Data.Node.Comments == nil {
		return nil, errors.Join(err, errors.New("GitHub comment connection is unavailable"))
	}
	return page.Data.Node.Comments, nil
}

func (collector *remoteCommentJSONCollector) comment() (*remoteCommentNode, error) {
	connection, err := collector.connection()
	if err != nil || connection.Nodes == nil || len(*connection.Nodes) == 0 {
		return nil, errors.Join(err, errors.New("GitHub comment node is unavailable"))
	}
	return &(*connection.Nodes)[len(*connection.Nodes)-1], nil
}
