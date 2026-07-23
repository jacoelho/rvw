package source

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

const pendingReviewsQuery = `query($owner:String!,$name:String!,$number:Int!,$endCursor:String){viewer{login}repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid reviews(first:100,after:$endCursor,states:[PENDING]){pageInfo{hasNextPage endCursor}nodes{id fullDatabaseId body state viewerDidAuthor viewerCanUpdate commit{oid}}}}}}`
const pendingCommentCapabilitiesQuery = `query($owner:String!,$name:String!,$number:Int!,$id:ID!,$endCursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid}}node(id:$id){... on PullRequestReview{id comments(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id fullDatabaseId viewerCanUpdate viewerCanDelete}}}}}`

var errPendingReviewAmbiguous = errors.New("multiple viewer-owned pending reviews")

type githubUint64 uint64

type pendingReviewPage struct {
	Data struct {
		Viewer *struct {
			Login string `json:"login"`
		} `json:"viewer"`
		Repository *struct {
			PullRequest *struct {
				Reviews    *pendingReviewConnection `json:"reviews"`
				BaseRefOID string                   `json:"baseRefOid"`
				HeadRefOID string                   `json:"headRefOid"`
				Number     uint64                   `json:"number"`
			} `json:"pullRequest"`
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"data"`
	Errors githubErrors `json:"errors"`
}

type pendingReviewConnection struct {
	PageInfo *remotePageInfo     `json:"pageInfo"`
	Nodes    *pendingReviewNodes `json:"nodes"`
}

type pendingReviewNodes []pendingReviewNode

type pendingReviewNode struct {
	Commit *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	Body            *string      `json:"body"`
	ViewerDidAuthor *bool        `json:"viewerDidAuthor"`
	ViewerCanUpdate *bool        `json:"viewerCanUpdate"`
	ID              string       `json:"id"`
	State           string       `json:"state"`
	FullDatabaseID  githubUint64 `json:"fullDatabaseId"`
}

type pendingReviewCommentPage []*pendingReviewCommentPayload

type pendingReviewCommentPayload struct {
	OriginalStartLine   *uint32       `json:"original_start_line"`
	Line                *uint32       `json:"line"`
	Body                *string       `json:"body"`
	OriginalLine        *uint32       `json:"original_line"`
	InReplyToID         *githubUint64 `json:"in_reply_to_id"`
	StartLine           *uint32       `json:"start_line"`
	StartSide           *string       `json:"start_side"`
	Side                string        `json:"side"`
	NodeID              string        `json:"node_id"`
	SubjectType         string        `json:"subject_type"`
	OriginalCommitID    string        `json:"original_commit_id"`
	CommitID            string        `json:"commit_id"`
	Path                string        `json:"path"`
	ID                  githubUint64  `json:"id"`
	PullRequestReviewID githubUint64  `json:"pull_request_review_id"`
	capabilities        review.PendingCapabilities
}

type pendingCommentCapabilityPage struct {
	Data struct {
		Repository *struct {
			PullRequest *struct {
				BaseRefOID string `json:"baseRefOid"`
				HeadRefOID string `json:"headRefOid"`
				Number     uint64 `json:"number"`
			} `json:"pullRequest"`
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		Node *struct {
			Comments *pendingCapabilityConnection `json:"comments"`
			ID       string                       `json:"id"`
		} `json:"node"`
	} `json:"data"`
	Errors githubErrors `json:"errors"`
}

type pendingCapabilityConnection struct {
	PageInfo *remotePageInfo             `json:"pageInfo"`
	Nodes    *pendingCommentCapabilities `json:"nodes"`
}

type pendingCommentCapabilities []pendingCommentCapability

type pendingCommentCapability struct {
	ViewerCanUpdate *bool        `json:"viewerCanUpdate"`
	ViewerCanDelete *bool        `json:"viewerCanDelete"`
	ID              string       `json:"id"`
	FullDatabaseID  githubUint64 `json:"fullDatabaseId"`
}

type pendingPagePosition uint8

const (
	pendingJSONPageIntermediate pendingPagePosition = iota + 1
	pendingJSONPageFinal
)

func (service *Service) loadPendingReview(
	ctx context.Context,
	details PullRequestDetails,
	runCPU CPUAdmission,
) (review.PendingReview, review.PendingReviewStatus, review.OperationDetail) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	return service.loadPendingReviewWithLedger(ctx, details, ledger, runCPU)
}

func (service *Service) loadPendingReviewWithLedger(
	ctx context.Context,
	details PullRequestDetails,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) (review.PendingReview, review.PendingReviewStatus, review.OperationDetail) {
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()
	node, status, err := service.findPendingReviewWithLedger(ctx, details, ledger, runCPU)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	if status != review.PendingReviewReady {
		detail := review.EmptyOperationDetail()
		if status == review.PendingReviewAmbiguous {
			detail = boundedOperationDetail("GitHub returned multiple viewer-owned pending reviews")
		}
		if commitErr := checkpoint.commit(0); commitErr != nil {
			return review.PendingReview{}, pendingFailureStatus(commitErr), boundedOperationDetail(commitErr.Error())
		}
		return review.PendingReview{}, status, detail
	}
	comments, err := service.loadPendingReviewCommentsWithLedger(
		ctx,
		details,
		node,
		ledger,
		runCPU,
	)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	comments, err = service.attachPendingCommentCapabilitiesWithLedger(
		ctx,
		details,
		node,
		comments,
		ledger,
		runCPU,
	)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	accountingScratch := int64(len(comments)) * 48
	if reserveErr := ledger.reserve(accountingScratch); reserveErr != nil {
		return review.PendingReview{}, pendingFailureStatus(reserveErr), boundedOperationDetail(reserveErr.Error())
	}
	constructionBytes := pendingReviewConstructionBytes(node, comments)
	if reserveErr := ledger.reserve(constructionBytes); reserveErr != nil {
		return review.PendingReview{}, pendingFailureStatus(reserveErr), boundedOperationDetail(reserveErr.Error())
	}
	pending, err := validatePendingReview(node, comments)
	if err != nil {
		return review.PendingReview{}, review.PendingReviewUnavailable,
			boundedOperationDetail(err.Error())
	}
	if err := checkpoint.commit(pending.RetainedBytes()); err != nil {
		return review.PendingReview{}, review.PendingReviewUnavailable,
			boundedOperationDetail(err.Error())
	}
	return pending, review.PendingReviewReady, review.EmptyOperationDetail()
}

func (service *Service) attachPendingCommentCapabilitiesWithLedger(
	ctx context.Context,
	details PullRequestDetails,
	node pendingReviewNode,
	comments []pendingReviewCommentPayload,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([]pendingReviewCommentPayload, error) {
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()
	if len(comments) == 0 {
		if err := checkpoint.commit(0); err != nil {
			return nil, err
		}
		return comments, nil
	}
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return nil, errors.New("validated GitHub repository cannot be split")
	}
	spool, err := service.graphQLSpool(
		ctx,
		pendingCommentCapabilitiesQuery,
		"comments",
		map[string]any{
			githubFieldOwner: owner, githubFieldName: repository,
			githubFieldNumber: details.Number.Uint64(), "id": node.ID,
		},
		review.MaxPendingReviewBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("load GitHub pending comment capabilities: %w", err)
	}
	limits, err := newJSONFramingLimits(review.MaxComments, maxGitHubPageCollectionElements)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	metrics, lexicalErr := validateCommandJSONWithLimits(
		ctx, spool, runCPU, ledger,
		limits,
	)
	if lexicalErr != nil {
		return nil, errors.Join(lexicalErr, spool.Close())
	}
	mapBytes := int64(len(comments))*48 + 64
	if reserveErr := ledger.reserve(mapBytes); reserveErr != nil {
		return nil, errors.Join(reserveErr, spool.Close())
	}
	capabilities := make(map[string]pendingCommentCapability, len(comments))
	seenCursors := make(map[string]struct{})
	err = decodePendingCapabilityStream(
		ctx, spool.Reader(), runCPU, ledger, metrics,
		func(
			_ int,
			position pendingPagePosition,
			page *pendingCommentCapabilityPage,
		) error {
			return collectPendingCommentCapabilities(
				page,
				position,
				node,
				details,
				capabilities,
				seenCursors,
				ledger,
			)
		},
	)
	metrics.release(ledger)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	if closeErr := spool.Close(); closeErr != nil {
		return nil, closeErr
	}
	if len(capabilities) != len(comments) {
		return nil, errors.New("GitHub pending comment capability identities are incomplete")
	}
	for index := range comments {
		capability, exists := capabilities[comments[index].NodeID]
		if !exists || uint64(capability.FullDatabaseID) != uint64(comments[index].ID) {
			return nil, errors.New("GitHub pending comment capability identities do not match REST")
		}
		values := make([]review.PendingCapability, 0, 2)
		if *capability.ViewerCanUpdate {
			values = append(values, review.PendingCapabilityUpdate)
		}
		if *capability.ViewerCanDelete {
			values = append(values, review.PendingCapabilityDelete)
		}
		validated, err := review.NewPendingCapabilities(values...)
		if err != nil {
			return nil, fmt.Errorf("validate GitHub pending comment capabilities: %w", err)
		}
		comments[index].capabilities = validated
	}
	if err := checkpoint.commit(0); err != nil {
		return nil, err
	}
	return comments, nil
}

func collectPendingCommentCapabilities(
	page *pendingCommentCapabilityPage,
	position pendingPagePosition,
	node pendingReviewNode,
	details PullRequestDetails,
	capabilities map[string]pendingCommentCapability,
	seenCursors map[string]struct{},
	ledger *sourceLedger,
) error {
	if position != pendingJSONPageIntermediate && position != pendingJSONPageFinal {
		return errors.New("GitHub pending comment capability page position is invalid")
	}
	if page == nil || len(page.Errors) != 0 || page.Data.Repository == nil ||
		page.Data.Repository.PullRequest == nil || page.Data.Node == nil ||
		page.Data.Node.ID != node.ID || page.Data.Node.Comments == nil ||
		page.Data.Node.Comments.PageInfo == nil ||
		page.Data.Node.Comments.PageInfo.HasNextPage == nil ||
		page.Data.Node.Comments.Nodes == nil {
		return errors.New("GitHub pending comment capability response is incomplete")
	}
	pull := page.Data.Repository.PullRequest
	if page.Data.Repository.NameWithOwner != details.Repository.String() ||
		pull.Number != details.Number.Uint64() || pull.BaseRefOID != details.BaseOID.String() ||
		pull.HeadRefOID != details.HeadOID.String() {
		return fmt.Errorf("%w: pull request changed while loading comment capabilities", ErrSourceStale)
	}
	connection := page.Data.Node.Comments
	finalPage := position == pendingJSONPageFinal
	if *connection.PageInfo.HasNextPage == finalPage ||
		!finalPage && connection.PageInfo.EndCursor == nil {
		return errors.New("GitHub pending comment capability pagination is incomplete")
	}
	for _, capability := range *connection.Nodes {
		if capability.ID == "" || capability.FullDatabaseID == 0 ||
			capability.ViewerCanUpdate == nil || capability.ViewerCanDelete == nil {
			return errors.New("GitHub pending comment capability is incomplete")
		}
		if _, duplicate := capabilities[capability.ID]; duplicate {
			return errors.New("GitHub pending comment capabilities contain a duplicate")
		}
		charge := int64(96 + len(capability.ID))
		if err := ledger.reserve(charge); err != nil {
			return err
		}
		capabilities[capability.ID] = capability
	}
	if connection.PageInfo.EndCursor != nil {
		if _, repeated := seenCursors[*connection.PageInfo.EndCursor]; repeated {
			return errors.New("GitHub pending comment capability cursor repeated")
		}
		charge := int64(64 + len(*connection.PageInfo.EndCursor))
		if err := ledger.reserve(charge); err != nil {
			return err
		}
		seenCursors[*connection.PageInfo.EndCursor] = struct{}{}
	}
	return nil
}

func pendingFailureStatus(err error) review.PendingReviewStatus {
	var limited *LimitError
	if errors.As(err, &limited) || errors.Is(err, ErrSourceLimited) {
		return review.PendingReviewLimited
	}
	return review.PendingReviewUnavailable
}

func (service *Service) findPendingReviewWithLedger(
	ctx context.Context,
	details PullRequestDetails,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) (pendingReviewNode, review.PendingReviewStatus, error) {
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return pendingReviewNode{}, 0, errors.New("validated GitHub repository cannot be split")
	}
	spool, err := service.graphQLSpool(
		ctx,
		pendingReviewsQuery,
		"reviews",
		map[string]any{
			githubFieldOwner: owner, githubFieldName: repository,
			githubFieldNumber: details.Number.Uint64(),
		},
		review.MaxPendingReviewBytes,
	)
	if err != nil {
		return pendingReviewNode{}, 0, fmt.Errorf("load GitHub pending reviews: %w", err)
	}
	limits, err := newJSONFramingLimits(review.MaxComments, maxGitHubPageCollectionElements)
	if err != nil {
		return pendingReviewNode{}, 0, errors.Join(err, spool.Close())
	}
	metrics, lexicalErr := validateCommandJSONWithLimits(
		ctx, spool, runCPU, ledger,
		limits,
	)
	if lexicalErr != nil {
		return pendingReviewNode{}, 0, errors.Join(lexicalErr, spool.Close())
	}
	const (
		collectorMapHeaders      = int64(192)
		selectedNodeCapacity     = 2
		selectedNodeStorageBytes = int64(selectedNodeCapacity * 128)
	)
	if reserveErr := ledger.reserve(collectorMapHeaders + selectedNodeStorageBytes); reserveErr != nil {
		return pendingReviewNode{}, 0, errors.Join(reserveErr, spool.Close())
	}
	collector := pendingReviewCollector{
		details:       details,
		seenNodes:     make(map[string]struct{}),
		seenDatabases: make(map[githubUint64]struct{}),
		seenCursors:   make(map[string]struct{}),
		ledger:        ledger,
		selected:      make([]pendingReviewNode, 0, selectedNodeCapacity),
	}
	err = decodePendingReviewStream(
		ctx, spool.Reader(), runCPU, ledger, metrics,
		collector.visit,
	)
	metrics.release(ledger)
	if errors.Is(err, errPendingReviewAmbiguous) {
		if commitErr := checkpoint.commit(0); commitErr != nil {
			return pendingReviewNode{}, 0, commitErr
		}
		return pendingReviewNode{}, review.PendingReviewAmbiguous, spool.Close()
	}
	if err != nil {
		return pendingReviewNode{}, 0, errors.Join(err, spool.Close())
	}
	if closeErr := spool.Close(); closeErr != nil {
		return pendingReviewNode{}, 0, closeErr
	}
	if len(collector.selected) > 1 {
		if err := checkpoint.commit(0); err != nil {
			return pendingReviewNode{}, 0, err
		}
		return pendingReviewNode{}, review.PendingReviewAmbiguous, nil
	}
	if len(collector.selected) == 0 {
		if err := checkpoint.commit(0); err != nil {
			return pendingReviewNode{}, 0, err
		}
		return pendingReviewNode{}, review.PendingReviewAbsent, nil
	}
	selected := collector.selected[0]
	if err := checkpoint.commit(pendingReviewNodeBytes(selected)); err != nil {
		return pendingReviewNode{}, 0, err
	}
	return selected, review.PendingReviewReady, nil
}

type pendingReviewCollector struct {
	seenNodes     map[string]struct{}
	seenDatabases map[githubUint64]struct{}
	seenCursors   map[string]struct{}
	ledger        *sourceLedger
	selected      []pendingReviewNode
	details       PullRequestDetails
}

func (collector *pendingReviewCollector) visit(
	_ int,
	position pendingPagePosition,
	page *pendingReviewPage,
) error {
	if position != pendingJSONPageIntermediate && position != pendingJSONPageFinal {
		return errors.New("GitHub pending review page position is invalid")
	}
	if page == nil {
		return errors.New("GitHub pending review response is nil")
	}
	if err := validatePendingReviewTarget(*page, collector.details); err != nil {
		return err
	}
	connection := page.Data.Repository.PullRequest.Reviews
	finalPage := position == pendingJSONPageFinal
	if *connection.PageInfo.HasNextPage == finalPage ||
		position == pendingJSONPageIntermediate && connection.PageInfo.EndCursor == nil {
		return errors.New("GitHub pending review pagination is incomplete")
	}
	for _, candidate := range *connection.Nodes {
		if err := collector.add(candidate); err != nil {
			return err
		}
	}
	if connection.PageInfo.EndCursor != nil {
		if _, repeated := collector.seenCursors[*connection.PageInfo.EndCursor]; repeated {
			return errors.New("GitHub pending review cursor repeated")
		}
		charge := int64(64 + len(*connection.PageInfo.EndCursor))
		if err := collector.ledger.reserve(charge); err != nil {
			return err
		}
		collector.seenCursors[*connection.PageInfo.EndCursor] = struct{}{}
	}
	return nil
}

func (collector *pendingReviewCollector) add(candidate pendingReviewNode) error {
	if err := validatePendingReviewNode(candidate); err != nil {
		return err
	}
	if _, duplicate := collector.seenNodes[candidate.ID]; duplicate {
		return errors.New("GitHub pending reviews contain a duplicate node identity")
	}
	if _, duplicate := collector.seenDatabases[candidate.FullDatabaseID]; duplicate {
		return errors.New("GitHub pending reviews contain a duplicate database identity")
	}
	identityBytes := int64(128 + len(candidate.ID))
	if err := collector.ledger.reserve(identityBytes); err != nil {
		return err
	}
	collector.seenNodes[candidate.ID] = struct{}{}
	collector.seenDatabases[candidate.FullDatabaseID] = struct{}{}
	if len(collector.seenNodes) > review.MaxComments {
		return &LimitError{Resource: LimitPendingReview, Limit: review.MaxComments}
	}
	if *candidate.ViewerDidAuthor {
		domainBytes := pendingReviewNodeBytes(candidate)
		if err := collector.ledger.reserve(domainBytes); err != nil {
			return err
		}
		collector.selected = append(collector.selected, candidate)
		if len(collector.selected) > 1 {
			return errPendingReviewAmbiguous
		}
	}
	return nil
}

func validatePendingReviewNode(node pendingReviewNode) error {
	if node.ID == "" || node.FullDatabaseID == 0 || node.Body == nil || node.State != githubPending ||
		node.ViewerDidAuthor == nil || node.ViewerCanUpdate == nil || node.Commit == nil {
		return errors.New("GitHub pending review is incomplete")
	}
	if _, err := review.NewReviewNodeID(node.ID); err != nil {
		return err
	}
	if _, err := review.NewReviewDatabaseID(uint64(node.FullDatabaseID)); err != nil {
		return err
	}
	if _, err := review.NewObjectID(node.Commit.OID); err != nil {
		return err
	}
	if !utf8.ValidString(*node.Body) || int64(len(*node.Body)) > review.MaxCommentBodyBytes {
		return errors.New("GitHub pending review body is invalid")
	}
	return nil
}

func validatePendingReviewTarget(page pendingReviewPage, details PullRequestDetails) error {
	if len(page.Errors) != 0 {
		return errors.New("GitHub pending review response contains GraphQL errors")
	}
	if page.Data.Viewer == nil || page.Data.Viewer.Login == "" || page.Data.Repository == nil ||
		page.Data.Repository.NameWithOwner != details.Repository.String() ||
		page.Data.Repository.PullRequest == nil {
		return errors.New("GitHub pending review response target is incomplete")
	}
	pull := page.Data.Repository.PullRequest
	if pull.Number != details.Number.Uint64() || pull.BaseRefOID != details.BaseOID.String() ||
		pull.HeadRefOID != details.HeadOID.String() {
		return fmt.Errorf("%w: pull request changed while loading pending review", ErrSourceStale)
	}
	if pull.Reviews == nil || pull.Reviews.PageInfo == nil ||
		pull.Reviews.PageInfo.HasNextPage == nil || pull.Reviews.Nodes == nil {
		return errors.New("GitHub pending review connection is incomplete")
	}
	return nil
}

func (service *Service) loadPendingReviewCommentsWithLedger(
	ctx context.Context,
	details PullRequestDetails,
	node pendingReviewNode,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([]pendingReviewCommentPayload, error) {
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()
	endpoint := "/repos/" + details.Repository.String() + "/pulls/" +
		strconv.FormatUint(details.Number.Uint64(), 10) + "/reviews/" +
		strconv.FormatUint(uint64(node.FullDatabaseID), 10) + "/comments?per_page=100"
	spool, err := service.restSpool(ctx, endpoint, review.MaxPendingReviewBytes)
	if err != nil {
		return nil, fmt.Errorf("load GitHub pending review comments: %w", err)
	}
	limits, err := newJSONFramingLimits(review.MaxComments, maxGitHubPageCollectionElements)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	metrics, lexicalErr := validateCommandJSONWithLimits(
		ctx, spool, runCPU, ledger,
		limits,
	)
	if lexicalErr != nil {
		return nil, errors.Join(lexicalErr, spool.Close())
	}
	comments := make([]pendingReviewCommentPayload, 0)
	retainedBytes := int64(0)
	commentStorageBytes := int64(0)
	err = decodePendingCommentStream(
		ctx, spool.Reader(), runCPU, ledger, metrics,
		func(_ int, page *pendingReviewCommentPage) error {
			for _, comment := range *page {
				if comment == nil {
					return errors.New("GitHub pending review comments contain a null comment")
				}
				if len(comments) >= review.MaxComments {
					return &LimitError{Resource: LimitPendingReview, Limit: review.MaxComments}
				}
				if len(comments) == cap(comments) {
					capacity := min(max(1, cap(comments)*2), review.MaxComments)
					storageBytes := int64(capacity) * jsonDecodedElementBytes
					if reserveErr := ledger.reserve(storageBytes); reserveErr != nil {
						return reserveErr
					}
					grown := make([]pendingReviewCommentPayload, len(comments), capacity)
					copy(grown, comments)
					ledger.release(commentStorageBytes)
					commentStorageBytes = storageBytes
					comments = grown
				}
				charge := pendingReviewCommentPayloadBytes(*comment)
				if reserveErr := ledger.reserve(charge); reserveErr != nil {
					return reserveErr
				}
				retainedBytes += charge
				comments = append(comments, *comment)
			}
			return nil
		},
	)
	metrics.release(ledger)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	if closeErr := spool.Close(); closeErr != nil {
		return nil, closeErr
	}
	if err := checkpoint.commit(retainedBytes + commentStorageBytes); err != nil {
		return nil, err
	}
	return comments, nil
}

func validatePendingReview(
	node pendingReviewNode,
	comments []pendingReviewCommentPayload,
) (review.PendingReview, error) {
	if node.ID == "" || node.FullDatabaseID == 0 || node.Body == nil || node.State != githubPending ||
		node.ViewerCanUpdate == nil || node.Commit == nil {
		return review.PendingReview{}, errors.New("GitHub pending review is incomplete")
	}
	nodeID, err := review.NewReviewNodeID(node.ID)
	if err != nil {
		return review.PendingReview{}, err
	}
	databaseID, err := review.NewReviewDatabaseID(uint64(node.FullDatabaseID))
	if err != nil {
		return review.PendingReview{}, err
	}
	commit, err := review.NewObjectID(node.Commit.OID)
	if err != nil {
		return review.PendingReview{}, err
	}
	validatedComments, err := validatePendingComments(databaseID, comments)
	if err != nil {
		return review.PendingReview{}, err
	}
	return review.TakePendingReview(review.PendingReviewDescriptor{
		NodeID:       nodeID,
		DatabaseID:   databaseID,
		Commit:       commit,
		Body:         *node.Body,
		Comments:     validatedComments,
		ViewerUpdate: *node.ViewerCanUpdate,
	})
}

func validatePendingComments(
	reviewID review.ReviewDatabaseID,
	values []pendingReviewCommentPayload,
) ([]review.PendingComment, error) {
	nodeByDatabase := make(map[githubUint64]review.CommentNodeID, len(values))
	for _, value := range values {
		if value.ID == 0 || value.NodeID == "" ||
			uint64(value.PullRequestReviewID) != reviewID.Uint64() {
			return nil, errors.New("GitHub pending review comment identity is incomplete")
		}
		nodeID, err := review.NewCommentNodeID(value.NodeID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := nodeByDatabase[value.ID]; duplicate {
			return nil, errors.New("GitHub pending review comments contain a duplicate identity")
		}
		nodeByDatabase[value.ID] = nodeID
	}
	comments := make([]review.PendingComment, 0, len(values))
	for _, value := range values {
		comment, err := validatePendingComment(value, nodeByDatabase)
		if err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, nil
}

func validatePendingComment(
	value pendingReviewCommentPayload,
	nodeByDatabase map[githubUint64]review.CommentNodeID,
) (review.PendingComment, error) {
	if value.Body == nil {
		return review.PendingComment{}, errors.New("GitHub pending review comment body is incomplete")
	}
	for _, line := range []*uint32{
		value.Line,
		value.StartLine,
		value.OriginalLine,
		value.OriginalStartLine,
	} {
		if line != nil && *line == 0 {
			return review.PendingComment{}, errors.New("GitHub pending review comment line is invalid")
		}
	}
	nodeID := nodeByDatabase[value.ID]
	databaseID, err := review.NewCommentDatabaseID(uint64(value.ID))
	if err != nil {
		return review.PendingComment{}, err
	}
	path, err := review.NewPathString(strings.Clone(value.Path))
	if err != nil {
		return review.PendingComment{}, err
	}
	commit, err := review.NewObjectID(value.CommitID)
	if err != nil {
		return review.PendingComment{}, err
	}
	originalCommit, err := review.NewObjectID(value.OriginalCommitID)
	if err != nil {
		return review.PendingComment{}, err
	}
	target := review.PendingCommentTargetDescriptor{
		Path:           path,
		Commit:         commit,
		OriginalCommit: originalCommit,
		Line:           pendingLine(value.Line),
		StartLine:      pendingLine(value.StartLine),
		OriginalLine:   pendingLine(value.OriginalLine),
		OriginalStart:  pendingLine(value.OriginalStartLine),
	}
	switch {
	case value.InReplyToID != nil:
		target.Subject = review.PendingSubjectReply
		replyDatabase, replyErr := review.NewCommentDatabaseID(uint64(*value.InReplyToID))
		if replyErr != nil {
			return review.PendingComment{}, replyErr
		}
		target.ReplyDatabase = replyDatabase
		target.ReplyTo = nodeByDatabase[*value.InReplyToID]
	case value.SubjectType == "file":
		target.Subject = review.PendingSubjectFile
	case value.SubjectType == githubSubjectLine || value.SubjectType == "" && value.Line != nil:
		target.Subject = review.PendingSubjectLine
		if target.Line == nil {
			target.Line = target.OriginalLine
			target.StartLine = target.OriginalStart
			target.Outdated = true
		}
		target.Side, err = pendingSide(value.Side)
		if err != nil {
			return review.PendingComment{}, err
		}
		if target.StartLine != nil {
			if value.StartSide == nil {
				return review.PendingComment{}, errors.New("GitHub pending review start side is incomplete")
			}
			target.StartSide, err = pendingSide(*value.StartSide)
			if err != nil {
				return review.PendingComment{}, err
			}
		}
	default:
		return review.PendingComment{}, errors.New("GitHub pending review comment subject is invalid")
	}
	target.Outdated = target.Outdated || target.Subject == review.PendingSubjectLine && value.Line == nil
	validatedTarget, err := review.NewPendingCommentTarget(target)
	if err != nil {
		return review.PendingComment{}, err
	}
	return review.TakePendingComment(review.PendingCommentDescriptor{
		NodeID:       nodeID,
		DatabaseID:   databaseID,
		Body:         *value.Body,
		Target:       validatedTarget,
		Capabilities: value.capabilities,
	})
}

func pendingLine(value *uint32) *review.LineNumber {
	if value == nil || *value == 0 {
		return nil
	}
	line, err := review.NewLineNumber(*value)
	if err != nil {
		return nil
	}
	return new(line)
}

func pendingSide(value string) (review.LineSide, error) {
	switch value {
	case githubSideLeft:
		return review.LineOld, nil
	case githubSideRight:
		return review.LineNew, nil
	default:
		return 0, errors.New("GitHub pending review comment side is invalid")
	}
}

func pendingReviewNodeBytes(node pendingReviewNode) int64 {
	bytes := int64(128 + len(node.ID) + len(node.State))
	if node.Body != nil {
		bytes += int64(len(*node.Body))
	}
	if node.Commit != nil {
		bytes += int64(len(node.Commit.OID))
	}
	return bytes
}

func pendingReviewCommentPayloadBytes(value pendingReviewCommentPayload) int64 {
	bytes := int64(256 + len(value.Side) + len(value.NodeID) + len(value.SubjectType) +
		len(value.OriginalCommitID) + len(value.CommitID) + len(value.Path))
	if value.Body != nil {
		bytes += int64(len(*value.Body))
	}
	if value.StartSide != nil {
		bytes += int64(len(*value.StartSide))
	}
	return bytes
}

func pendingReviewConstructionBytes(
	node pendingReviewNode,
	comments []pendingReviewCommentPayload,
) int64 {
	retained := int64(64 + len(node.ID))
	if node.Body != nil {
		retained += int64(len(*node.Body))
	}
	if node.Commit != nil {
		retained += int64(len(node.Commit.OID))
	}
	nodeLengths := make(map[githubUint64]int, len(comments))
	for _, comment := range comments {
		nodeLengths[comment.ID] = len(comment.NodeID)
	}
	for _, comment := range comments {
		retained += int64(128 + len(comment.NodeID) + len(comment.Path) +
			len(comment.CommitID) + len(comment.OriginalCommitID))
		if comment.Body != nil {
			retained += int64(len(*comment.Body))
		}
		if comment.InReplyToID != nil {
			retained += int64(nodeLengths[*comment.InReplyToID])
		}
	}
	// Validation owns one database-to-node map and two duplicate-detection maps
	// while the completed immutable capture is also live.
	return retained + int64(len(comments))*192
}
