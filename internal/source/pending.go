package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

const pendingReviewsQuery = `query($owner:String!,$name:String!,$number:Int!,$endCursor:String){viewer{login}repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid reviews(first:100,after:$endCursor,states:[PENDING]){pageInfo{hasNextPage endCursor}nodes{id fullDatabaseId body state viewerDidAuthor viewerCanUpdate commit{oid}}}}}}`
const pendingCommentCapabilitiesQuery = `query($owner:String!,$name:String!,$number:Int!,$id:ID!,$endCursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid}}node(id:$id){... on PullRequestReview{id comments(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id fullDatabaseId viewerCanUpdate viewerCanDelete}}}}}`

type githubUint64 uint64

func (value *githubUint64) UnmarshalJSON(encoded []byte) error {
	if bytes.Equal(encoded, []byte("null")) {
		return errors.New("GitHub database identity is null")
	}
	var text string
	if len(encoded) > 0 && encoded[0] == '"' {
		if err := json.Unmarshal(encoded, &text); err != nil {
			return err
		}
	} else {
		text = string(encoded)
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil || parsed == 0 {
		return errors.New("GitHub database identity is invalid")
	}
	*value = githubUint64(parsed)
	return nil
}

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
	Errors []json.RawMessage `json:"errors"`
}

type pendingReviewConnection struct {
	PageInfo *remotePageInfo      `json:"pageInfo"`
	Nodes    *[]pendingReviewNode `json:"nodes"`
}

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
	viewerUpdate        bool
	viewerDelete        bool
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
	Errors []json.RawMessage `json:"errors"`
}

type pendingCapabilityConnection struct {
	PageInfo *remotePageInfo             `json:"pageInfo"`
	Nodes    *[]pendingCommentCapability `json:"nodes"`
}

type pendingCommentCapability struct {
	ViewerCanUpdate *bool        `json:"viewerCanUpdate"`
	ViewerCanDelete *bool        `json:"viewerCanDelete"`
	ID              string       `json:"id"`
	FullDatabaseID  githubUint64 `json:"fullDatabaseId"`
}

type pendingCapabilityPagePosition uint8

const (
	pendingCapabilityPageIntermediate pendingCapabilityPagePosition = iota + 1
	pendingCapabilityPageFinal
)

func (service *Service) loadPendingReview(
	ctx context.Context,
	details PullRequestDetails,
) (review.PendingReview, review.PendingReviewStatus, review.OperationDetail) {
	node, status, err := service.findPendingReview(ctx, details)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	if status != review.PendingReviewReady {
		detail := review.EmptyOperationDetail()
		if status == review.PendingReviewAmbiguous {
			detail = boundedOperationDetail("GitHub returned multiple viewer-owned pending reviews")
		}
		return review.PendingReview{}, status, detail
	}
	comments, err := service.loadPendingReviewComments(ctx, details, node)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	comments, err = service.attachPendingCommentCapabilities(ctx, details, node, comments)
	if err != nil {
		return review.PendingReview{}, pendingFailureStatus(err), boundedOperationDetail(err.Error())
	}
	pending, err := validatePendingReview(node, comments)
	if err != nil {
		return review.PendingReview{}, review.PendingReviewUnavailable,
			boundedOperationDetail(err.Error())
	}
	return pending, review.PendingReviewReady, review.EmptyOperationDetail()
}

func (service *Service) attachPendingCommentCapabilities(
	ctx context.Context,
	details PullRequestDetails,
	node pendingReviewNode,
	comments []pendingReviewCommentPayload,
) ([]pendingReviewCommentPayload, error) {
	if len(comments) == 0 {
		return comments, nil
	}
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return nil, errors.New("validated GitHub repository cannot be split")
	}
	result, err := service.runGH(ctx, []string{
		ghAPI, ghGraphQL, ghPaginate, ghSlurp,
		"-f", "query=" + pendingCommentCapabilitiesQuery,
		"-F", "owner=" + owner,
		"-F", "name=" + repository,
		"-F", "number=" + strconv.FormatUint(details.Number.Uint64(), 10),
		"-F", "id=" + node.ID,
	}, review.MaxPendingReviewBytes)
	if err != nil {
		return nil, fmt.Errorf("load GitHub pending comment capabilities: %w", err)
	}
	if !utf8.Valid(result.Stdout) {
		return nil, errors.New("GitHub pending comment capabilities are not valid UTF-8")
	}
	var pages []*pendingCommentCapabilityPage
	if err := json.Unmarshal(result.Stdout, &pages); err != nil || len(pages) == 0 {
		return nil, errors.New("decode GitHub pending comment capabilities")
	}
	capabilities := make(map[string]pendingCommentCapability, len(comments))
	seenCursors := make(map[string]struct{})
	for index, page := range pages {
		position := pendingCapabilityPageIntermediate
		if index == len(pages)-1 {
			position = pendingCapabilityPageFinal
		}
		if err := collectPendingCommentCapabilities(
			page,
			position,
			node,
			details,
			capabilities,
			seenCursors,
		); err != nil {
			return nil, err
		}
	}
	if len(capabilities) != len(comments) {
		return nil, errors.New("GitHub pending comment capability identities are incomplete")
	}
	owned := append([]pendingReviewCommentPayload(nil), comments...)
	for index := range owned {
		capability, exists := capabilities[owned[index].NodeID]
		if !exists || uint64(capability.FullDatabaseID) != uint64(owned[index].ID) {
			return nil, errors.New("GitHub pending comment capability identities do not match REST")
		}
		owned[index].viewerUpdate = *capability.ViewerCanUpdate
		owned[index].viewerDelete = *capability.ViewerCanDelete
	}
	return owned, nil
}

func collectPendingCommentCapabilities(
	page *pendingCommentCapabilityPage,
	position pendingCapabilityPagePosition,
	node pendingReviewNode,
	details PullRequestDetails,
	capabilities map[string]pendingCommentCapability,
	seenCursors map[string]struct{},
) error {
	if position != pendingCapabilityPageIntermediate && position != pendingCapabilityPageFinal {
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
	finalPage := position == pendingCapabilityPageFinal
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
		capabilities[capability.ID] = capability
	}
	if connection.PageInfo.EndCursor != nil {
		if _, repeated := seenCursors[*connection.PageInfo.EndCursor]; repeated {
			return errors.New("GitHub pending comment capability cursor repeated")
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

func (service *Service) findPendingReview(
	ctx context.Context,
	details PullRequestDetails,
) (pendingReviewNode, review.PendingReviewStatus, error) {
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return pendingReviewNode{}, 0, errors.New("validated GitHub repository cannot be split")
	}
	result, err := service.runGH(ctx, []string{
		ghAPI, ghGraphQL, ghPaginate, ghSlurp,
		"-f", "query=" + pendingReviewsQuery,
		"-F", "owner=" + owner,
		"-F", "name=" + repository,
		"-F", "number=" + strconv.FormatUint(details.Number.Uint64(), 10),
	}, review.MaxPendingReviewBytes)
	if err != nil {
		return pendingReviewNode{}, 0, fmt.Errorf("load GitHub pending reviews: %w", err)
	}
	pages, err := decodePendingReviewPages(result.Stdout)
	if err != nil {
		return pendingReviewNode{}, 0, err
	}
	var selected []pendingReviewNode
	seenNodes := make(map[string]struct{})
	seenDatabases := make(map[githubUint64]struct{})
	seenCursors := make(map[string]struct{})
	for index, page := range pages {
		if err := validatePendingReviewTarget(page, details); err != nil {
			return pendingReviewNode{}, 0, err
		}
		connection := page.Data.Repository.PullRequest.Reviews
		finalPage := index == len(pages)-1
		if *connection.PageInfo.HasNextPage == finalPage ||
			!finalPage && connection.PageInfo.EndCursor == nil {
			return pendingReviewNode{}, 0, errors.New("GitHub pending review pagination is incomplete")
		}
		for _, candidate := range *connection.Nodes {
			if err := validatePendingReviewNode(candidate); err != nil {
				return pendingReviewNode{}, 0, err
			}
			if _, duplicate := seenNodes[candidate.ID]; duplicate {
				return pendingReviewNode{}, 0, errors.New("GitHub pending reviews contain a duplicate node identity")
			}
			if _, duplicate := seenDatabases[candidate.FullDatabaseID]; duplicate {
				return pendingReviewNode{}, 0, errors.New("GitHub pending reviews contain a duplicate database identity")
			}
			seenNodes[candidate.ID] = struct{}{}
			seenDatabases[candidate.FullDatabaseID] = struct{}{}
			if len(seenNodes) > review.MaxComments {
				return pendingReviewNode{}, 0, &LimitError{
					Resource: LimitPendingReview,
					Limit:    review.MaxComments,
				}
			}
			if *candidate.ViewerDidAuthor {
				selected = append(selected, candidate)
				if len(selected) > 1 {
					return pendingReviewNode{}, review.PendingReviewAmbiguous, nil
				}
			}
		}
		if connection.PageInfo.EndCursor != nil {
			if _, repeated := seenCursors[*connection.PageInfo.EndCursor]; repeated {
				return pendingReviewNode{}, 0, errors.New("GitHub pending review cursor repeated")
			}
			seenCursors[*connection.PageInfo.EndCursor] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return pendingReviewNode{}, review.PendingReviewAbsent, nil
	}
	return selected[0], review.PendingReviewReady, nil
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

func decodePendingReviewPages(encoded []byte) ([]pendingReviewPage, error) {
	if !utf8.Valid(encoded) {
		return nil, errors.New("GitHub pending review response is not valid UTF-8")
	}
	var pages []*pendingReviewPage
	if err := json.Unmarshal(encoded, &pages); err != nil {
		return nil, fmt.Errorf("decode GitHub pending reviews: %w", err)
	}
	if len(pages) == 0 {
		return nil, errors.New("GitHub pending review pagination returned no page")
	}
	values := make([]pendingReviewPage, len(pages))
	for index, page := range pages {
		if page == nil {
			return nil, errors.New("GitHub pending review pagination returned a null page")
		}
		values[index] = *page
	}
	return values, nil
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

func (service *Service) loadPendingReviewComments(
	ctx context.Context,
	details PullRequestDetails,
	node pendingReviewNode,
) ([]pendingReviewCommentPayload, error) {
	endpoint := "repos/" + details.Repository.String() + "/pulls/" +
		strconv.FormatUint(details.Number.Uint64(), 10) + "/reviews/" +
		strconv.FormatUint(uint64(node.FullDatabaseID), 10) + "/comments"
	result, err := service.runGH(ctx, []string{
		ghAPI, "--method", "GET", ghPaginate, ghSlurp,
		"-f", "per_page=100", endpoint,
	}, review.MaxPendingReviewBytes)
	if err != nil {
		return nil, fmt.Errorf("load GitHub pending review comments: %w", err)
	}
	if !utf8.Valid(result.Stdout) {
		return nil, errors.New("GitHub pending review comments are not valid UTF-8")
	}
	var pages []*pendingReviewCommentPage
	if err := json.Unmarshal(result.Stdout, &pages); err != nil {
		return nil, fmt.Errorf("decode GitHub pending review comments: %w", err)
	}
	if len(pages) == 0 {
		return nil, errors.New("GitHub pending review comment pagination returned no page")
	}
	comments := make([]pendingReviewCommentPayload, 0)
	for _, page := range pages {
		if page == nil {
			return nil, errors.New("GitHub pending review comment pagination returned a null page")
		}
		for _, comment := range *page {
			if comment == nil {
				return nil, errors.New("GitHub pending review comments contain a null comment")
			}
			if len(comments) >= review.MaxComments {
				return nil, &LimitError{Resource: LimitPendingReview, Limit: review.MaxComments}
			}
			comments = append(comments, *comment)
		}
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
	return review.NewPendingReview(review.PendingReviewDescriptor{
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
	path, err := review.NewPath([]byte(value.Path))
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
	return review.NewPendingComment(review.PendingCommentDescriptor{
		NodeID:       nodeID,
		DatabaseID:   databaseID,
		Body:         *value.Body,
		Target:       validatedTarget,
		ViewerUpdate: value.viewerUpdate,
		ViewerDelete: value.viewerDelete,
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
