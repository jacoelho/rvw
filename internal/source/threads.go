package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

const remoteThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$endCursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id isResolved isOutdated path line startLine diffSide comments(first:100){pageInfo{hasNextPage endCursor}nodes{id body createdAt url author{login}}}}}}}}`
const remoteThreadCommentsQuery = `query($owner:String!,$name:String!,$number:Int!,$id:ID!,$endCursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid}}node(id:$id){... on PullRequestReviewThread{id comments(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id body createdAt url author{login}}}}}}`

type remoteThreadPage struct {
	Errors []json.RawMessage `json:"errors"`
	Data   struct {
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
			PullRequest   *struct {
				Number        uint64                  `json:"number"`
				BaseRefOID    string                  `json:"baseRefOid"`
				HeadRefOID    string                  `json:"headRefOid"`
				ReviewThreads *remoteThreadConnection `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type remoteThreadConnection struct {
	PageInfo *remotePageInfo     `json:"pageInfo"`
	Nodes    *[]remoteThreadNode `json:"nodes"`
}

type remotePageInfo struct {
	HasNextPage *bool   `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

type remoteThreadNode struct {
	ID         string                   `json:"id"`
	IsResolved *bool                    `json:"isResolved"`
	IsOutdated *bool                    `json:"isOutdated"`
	Path       string                   `json:"path"`
	Line       *uint32                  `json:"line"`
	StartLine  *uint32                  `json:"startLine"`
	DiffSide   string                   `json:"diffSide"`
	Comments   *remoteCommentConnection `json:"comments"`
}

type remoteCommentConnection struct {
	PageInfo *remotePageInfo      `json:"pageInfo"`
	Nodes    *[]remoteCommentNode `json:"nodes"`
}

type remoteCommentNode struct {
	ID        string  `json:"id"`
	Body      *string `json:"body"`
	CreatedAt string  `json:"createdAt"`
	URL       string  `json:"url"`
	Author    *struct {
		Login string `json:"login"`
	} `json:"author"`
}

type remoteCommentPage struct {
	Errors []json.RawMessage `json:"errors"`
	Data   struct {
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
			PullRequest   *struct {
				Number     uint64 `json:"number"`
				BaseRefOID string `json:"baseRefOid"`
				HeadRefOID string `json:"headRefOid"`
			} `json:"pullRequest"`
		} `json:"repository"`
		Node *struct {
			ID       string                   `json:"id"`
			Comments *remoteCommentConnection `json:"comments"`
		} `json:"node"`
	} `json:"data"`
}

func (service *Service) loadRemoteThreads(
	ctx context.Context,
	details PullRequestDetails,
) ([]review.RemoteThread, error) {
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return nil, errors.New("validated GitHub repository cannot be split")
	}
	result, err := service.runGH(ctx, []string{
		"api", "graphql", "--paginate", "--slurp",
		"-f", "query=" + remoteThreadsQuery,
		"-F", "owner=" + owner,
		"-F", "name=" + repository,
		"-F", "number=" + strconv.FormatUint(details.Number.Uint64(), 10),
	}, review.MaxRemoteThreadBytes)
	if err != nil {
		return nil, fmt.Errorf("load GitHub review threads: %w", err)
	}
	pages, err := decodeRemoteThreadPages(result.Stdout)
	if err != nil {
		return nil, err
	}
	threads := make([]review.RemoteThread, 0)
	threadIDs := make(map[string]struct{})
	commentIDs := make(map[string]struct{})
	responseBytes := int64(len(result.Stdout))
	for pageIndex, page := range pages {
		if err := validateRemoteThreadTarget(page, details); err != nil {
			return nil, err
		}
		pullRequest := page.Data.Repository.PullRequest
		connection := pullRequest.ReviewThreads
		if connection == nil || connection.PageInfo == nil ||
			connection.PageInfo.HasNextPage == nil || connection.Nodes == nil {
			return nil, errors.New("GitHub review thread connection is incomplete")
		}
		finalPage := pageIndex == len(pages)-1
		if *connection.PageInfo.HasNextPage == finalPage ||
			!finalPage && connection.PageInfo.EndCursor == nil {
			return nil, errors.New("GitHub review thread pagination is incomplete")
		}
		for _, node := range *connection.Nodes {
			if node.Comments != nil && node.Comments.PageInfo != nil &&
				node.Comments.PageInfo.HasNextPage != nil &&
				*node.Comments.PageInfo.HasNextPage {
				remaining := review.MaxRemoteThreadBytes - responseBytes
				if remaining <= 0 {
					return nil, &LimitError{
						Resource: LimitRemoteThreads,
						Limit:    uint32(review.MaxRemoteThreadBytes),
					}
				}
				comments, used, err := service.loadRemainingRemoteComments(
					ctx,
					details,
					node.ID,
					node.Comments,
					remaining,
				)
				if err != nil {
					return nil, err
				}
				responseBytes += used
				node.Comments = comments
			}
			if len(threads) >= review.MaxRemoteThreads {
				return nil, &LimitError{
					Resource: LimitRemoteThreads,
					Limit:    review.MaxRemoteThreads,
				}
			}
			thread, err := validateRemoteThread(node)
			if err != nil {
				return nil, err
			}
			if _, exists := threadIDs[thread.ID()]; exists {
				return nil, errors.New("GitHub review threads contain a duplicate identity")
			}
			threadIDs[thread.ID()] = struct{}{}
			for _, comment := range thread.Comments(0, thread.CommentCount()) {
				if _, exists := commentIDs[comment.ID()]; exists {
					return nil, errors.New("GitHub review comments contain a duplicate identity")
				}
				commentIDs[comment.ID()] = struct{}{}
			}
			if len(threadIDs)+len(commentIDs) > review.MaxRemoteThreads {
				return nil, errors.New("GitHub review thread identities exceed their bound")
			}
			threads = append(threads, thread)
		}
	}
	return threads, nil
}

func (service *Service) loadRemainingRemoteComments(
	ctx context.Context,
	details PullRequestDetails,
	threadID string,
	initial *remoteCommentConnection,
	byteLimit int64,
) (*remoteCommentConnection, int64, error) {
	if initial == nil || initial.PageInfo == nil ||
		initial.PageInfo.HasNextPage == nil || initial.Nodes == nil ||
		!*initial.PageInfo.HasNextPage || initial.PageInfo.EndCursor == nil {
		return nil, 0, errors.New("GitHub review comment pagination is incomplete")
	}
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return nil, 0, errors.New("validated GitHub repository cannot be split")
	}
	result, err := service.runGH(ctx, []string{
		"api", "graphql", "--paginate", "--slurp",
		"-f", "query=" + remoteThreadCommentsQuery,
		"-F", "owner=" + owner,
		"-F", "name=" + repository,
		"-F", "number=" + strconv.FormatUint(details.Number.Uint64(), 10),
		"-F", "id=" + threadID,
		"-F", "endCursor=" + *initial.PageInfo.EndCursor,
	}, byteLimit)
	if err != nil {
		return nil, 0, fmt.Errorf("load GitHub review thread comments: %w", err)
	}
	pages, err := decodeRemoteCommentPages(result.Stdout)
	if err != nil {
		return nil, 0, err
	}
	comments := append([]remoteCommentNode(nil), (*initial.Nodes)...)
	seenCursors := map[string]struct{}{*initial.PageInfo.EndCursor: {}}
	for index, page := range pages {
		if err := validateRemoteCommentTarget(page, details, threadID); err != nil {
			return nil, 0, err
		}
		connection := page.Data.Node.Comments
		finalPage := index == len(pages)-1
		if *connection.PageInfo.HasNextPage == finalPage ||
			!finalPage && connection.PageInfo.EndCursor == nil {
			return nil, 0, errors.New("GitHub review comment pagination is incomplete")
		}
		comments = append(comments, (*connection.Nodes)...)
		if len(comments) > review.MaxRemoteComments {
			return nil, 0, &LimitError{
				Resource: LimitRemoteThreads,
				Limit:    review.MaxRemoteComments,
			}
		}
		if connection.PageInfo.EndCursor != nil {
			if _, repeated := seenCursors[*connection.PageInfo.EndCursor]; repeated {
				return nil, 0, errors.New("GitHub review comment cursor repeated")
			}
			seenCursors[*connection.PageInfo.EndCursor] = struct{}{}
		}
	}
	done := false
	return &remoteCommentConnection{
		PageInfo: &remotePageInfo{HasNextPage: &done},
		Nodes:    &comments,
	}, int64(len(result.Stdout)), nil
}

func decodeRemoteCommentPages(value []byte) ([]remoteCommentPage, error) {
	if !utf8.Valid(value) {
		return nil, errors.New("GitHub review comment response is not valid UTF-8")
	}
	var decoded []*remoteCommentPage
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, fmt.Errorf("decode GitHub review comments: %w", err)
	}
	if len(decoded) == 0 {
		return nil, errors.New("GitHub review comment pagination returned no page")
	}
	pages := make([]remoteCommentPage, len(decoded))
	for index, page := range decoded {
		if page == nil {
			return nil, fmt.Errorf("GitHub review comment page %d is null", index+1)
		}
		pages[index] = *page
	}
	return pages, nil
}

func validateRemoteCommentTarget(
	page remoteCommentPage,
	details PullRequestDetails,
	threadID string,
) error {
	if len(page.Errors) != 0 ||
		page.Data.Repository == nil ||
		page.Data.Repository.PullRequest == nil ||
		page.Data.Node == nil ||
		page.Data.Node.ID != threadID ||
		page.Data.Node.Comments == nil ||
		page.Data.Node.Comments.PageInfo == nil ||
		page.Data.Node.Comments.PageInfo.HasNextPage == nil ||
		page.Data.Node.Comments.Nodes == nil {
		return errors.New("GitHub review comment page is incomplete")
	}
	pullRequest := page.Data.Repository.PullRequest
	if page.Data.Repository.NameWithOwner != details.Repository.String() ||
		pullRequest.Number != details.Number.Uint64() ||
		pullRequest.BaseRefOID != details.BaseOID.String() ||
		pullRequest.HeadRefOID != details.HeadOID.String() {
		return errors.New(
			"GitHub review comment target or revision changed during acquisition",
		)
	}
	return nil
}

func validateRemoteThreadTarget(
	page remoteThreadPage,
	details PullRequestDetails,
) error {
	if len(page.Errors) != 0 {
		return errors.New("GitHub review thread query returned GraphQL errors")
	}
	if page.Data.Repository == nil ||
		page.Data.Repository.PullRequest == nil {
		return errors.New("GitHub review thread target is unavailable")
	}
	pullRequest := page.Data.Repository.PullRequest
	if page.Data.Repository.NameWithOwner != details.Repository.String() ||
		pullRequest.Number != details.Number.Uint64() ||
		pullRequest.BaseRefOID != details.BaseOID.String() ||
		pullRequest.HeadRefOID != details.HeadOID.String() {
		return errors.New(
			"GitHub review thread target or revision changed during acquisition",
		)
	}
	return nil
}

func decodeRemoteThreadPages(value []byte) ([]remoteThreadPage, error) {
	if !utf8.Valid(value) {
		return nil, errors.New("GitHub review thread response is not valid UTF-8")
	}
	var decoded []*remoteThreadPage
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, fmt.Errorf("decode GitHub review threads: %w", err)
	}
	if len(decoded) == 0 {
		return nil, errors.New("GitHub review thread pagination returned no page")
	}
	pages := make([]remoteThreadPage, len(decoded))
	for index, page := range decoded {
		if page == nil {
			return nil, fmt.Errorf("GitHub review thread page %d is null", index+1)
		}
		pages[index] = *page
	}
	return pages, nil
}

func validateRemoteThread(node remoteThreadNode) (review.RemoteThread, error) {
	if node.IsResolved == nil || node.IsOutdated == nil ||
		node.Comments == nil || node.Comments.PageInfo == nil ||
		node.Comments.PageInfo.HasNextPage == nil || node.Comments.Nodes == nil {
		return review.RemoteThread{}, errors.New("GitHub review thread is incomplete")
	}
	if err := validateRemoteField(node.ID, review.MaxPathBytes); err != nil {
		return review.RemoteThread{}, fmt.Errorf("invalid GitHub review thread ID: %w", err)
	}
	path, err := review.NewPath([]byte(node.Path))
	if err != nil || strings.HasPrefix(node.Path, "/") {
		return review.RemoteThread{}, errors.New("invalid GitHub review thread path")
	}
	for component := range strings.SplitSeq(node.Path, "/") {
		if component == "" || component == "." || component == ".." {
			return review.RemoteThread{}, errors.New("invalid GitHub review thread path")
		}
	}
	side := review.LineNew
	switch node.DiffSide {
	case "LEFT":
		side = review.LineOld
	case "RIGHT":
	default:
		return review.RemoteThread{}, errors.New("invalid GitHub review thread side")
	}
	lineValue := node.Line
	if lineValue == nil {
		lineValue = node.StartLine
	}
	var line *review.LineNumber
	if lineValue != nil {
		validated, lineErr := review.NewLineNumber(*lineValue)
		if lineErr != nil {
			return review.RemoteThread{}, errors.New("invalid GitHub review thread line")
		}
		line = new(validated)
	}
	if *node.Comments.PageInfo.HasNextPage {
		return review.RemoteThread{}, &LimitError{
			Resource: LimitRemoteThreads,
			Limit:    review.MaxRemoteThreads,
		}
	}
	comments := make([]review.RemoteComment, 0, len(*node.Comments.Nodes))
	for _, raw := range *node.Comments.Nodes {
		comment, commentErr := validateRemoteComment(raw)
		if commentErr != nil {
			return review.RemoteThread{}, commentErr
		}
		comments = append(comments, comment)
	}
	return review.NewRemoteThread(review.RemoteThreadDescriptor{
		ID:       node.ID,
		Resolved: *node.IsResolved,
		Outdated: *node.IsOutdated,
		Path:     path,
		Line:     line,
		Side:     side,
		Comments: comments,
	})
}

func validateRemoteComment(node remoteCommentNode) (review.RemoteComment, error) {
	if err := validateRemoteField(node.ID, review.MaxPathBytes); err != nil {
		return review.RemoteComment{}, fmt.Errorf("invalid GitHub review comment ID: %w", err)
	}
	if node.Body == nil ||
		int64(len(*node.Body)) > review.MaxCommentBodyBytes ||
		!utf8.ValidString(*node.Body) {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment body")
	}
	if _, err := time.Parse(time.RFC3339, node.CreatedAt); err != nil {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment timestamp")
	}
	parsedURL, err := url.ParseRequestURI(node.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host != "github.com" {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment URL")
	}
	author := ""
	if node.Author != nil {
		author = node.Author.Login
		if err := validateRemoteField(author, 256); err != nil {
			return review.RemoteComment{}, errors.New("invalid GitHub review comment author")
		}
	}
	return review.NewRemoteComment(review.RemoteCommentDescriptor{
		ID:        node.ID,
		Author:    author,
		Body:      *node.Body,
		CreatedAt: node.CreatedAt,
		URL:       node.URL,
	})
}

func validateRemoteField(value string, maximum int64) error {
	if value == "" || int64(len(value)) > maximum || !utf8.ValidString(value) {
		return errors.New("field is empty, too long, or invalid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("field contains a control character")
		}
	}
	return nil
}
