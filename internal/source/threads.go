package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

const remoteThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$endCursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id isResolved isOutdated path line startLine diffSide comments(first:100){pageInfo{hasNextPage endCursor}nodes{id body createdAt url author{login}}}}}}}}`
const remoteThreadCommentsQuery = `query($owner:String!,$name:String!,$number:Int!,$id:ID!,$endCursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number baseRefOid headRefOid}}node(id:$id){... on PullRequestReviewThread{id comments(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id body createdAt url author{login}}}}}}`

type remoteThreadPage struct {
	Data struct {
		Repository *struct {
			PullRequest *struct {
				ReviewThreads *remoteThreadConnection `json:"reviewThreads"`
				BaseRefOID    string                  `json:"baseRefOid"`
				HeadRefOID    string                  `json:"headRefOid"`
				Number        uint64                  `json:"number"`
			} `json:"pullRequest"`
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"data"`
	Errors githubErrors `json:"errors"`
}

type remoteThreadConnection struct {
	PageInfo *remotePageInfo    `json:"pageInfo"`
	Nodes    *remoteThreadNodes `json:"nodes"`
}

type remoteThreadNodes []remoteThreadNode

type remotePageInfo struct {
	HasNextPage *bool   `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

type remoteThreadNode struct {
	IsResolved *bool                    `json:"isResolved"`
	IsOutdated *bool                    `json:"isOutdated"`
	Line       *uint32                  `json:"line"`
	StartLine  *uint32                  `json:"startLine"`
	Comments   *remoteCommentConnection `json:"comments"`
	ID         string                   `json:"id"`
	Path       string                   `json:"path"`
	DiffSide   string                   `json:"diffSide"`
}

type remoteCommentConnection struct {
	PageInfo *remotePageInfo     `json:"pageInfo"`
	Nodes    *remoteCommentNodes `json:"nodes"`
}

type remoteCommentNodes []remoteCommentNode

type remoteCommentNode struct {
	Body   *string `json:"body"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
	URL       string `json:"url"`
}

type remoteCommentPage struct {
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
			Comments *remoteCommentConnection `json:"comments"`
			ID       string                   `json:"id"`
		} `json:"node"`
	} `json:"data"`
	Errors githubErrors `json:"errors"`
}

type remotePagePosition uint8

const (
	remotePageIntermediate remotePagePosition = iota + 1
	remotePageFinal
)

func (service *Service) loadRemoteThreads(
	ctx context.Context,
	details PullRequestDetails,
) ([]review.RemoteThread, error) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		return nil, err
	}
	return service.loadRemoteThreadsWithLedgerCPU(ctx, details, ledger, directCPUAdmission)
}

func (service *Service) loadRemoteThreadsWithLedgerCPU(
	ctx context.Context,
	details PullRequestDetails,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) ([]review.RemoteThread, error) {
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return nil, errors.New("validated GitHub repository cannot be split")
	}
	spool, err := service.graphQLSpool(
		ctx,
		remoteThreadsQuery,
		"reviewThreads",
		map[string]any{
			githubFieldOwner: owner, githubFieldName: repository,
			githubFieldNumber: details.Number.Uint64(),
		},
		review.MaxRemoteThreadBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("load GitHub review threads: %w", err)
	}
	responseBytes, err := spool.Size()
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	limits, err := newJSONFramingLimits(
		review.MaxRemoteThreads, maxGitHubPageCollectionElements,
	)
	if err != nil {
		return nil, err
	}
	metrics, lexicalErr := validateCommandJSONWithLimits(
		ctx, spool, runCPU, ledger,
		limits,
	)
	if lexicalErr != nil {
		return nil, errors.Join(lexicalErr, spool.Close())
	}
	collector := remoteThreadCollector{
		service:       service,
		ctx:           ctx,
		details:       details,
		threadIDs:     make(map[string]struct{}),
		commentIDs:    make(map[string]struct{}),
		responseBytes: responseBytes,
		ledger:        ledger,
		runCPU:        runCPU,
	}
	complete := false
	defer func() {
		if !complete {
			collector.discard()
		}
	}()
	err = decodeRemoteThreadStream(
		ctx, spool.Reader(), runCPU, ledger, metrics, collector.stagePage,
	)
	metrics.release(ledger)
	if err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	if closeErr := spool.Close(); closeErr != nil {
		return nil, closeErr
	}
	for index := range collector.staged {
		staged := &collector.staged[index]
		if err := collector.addNode(staged.node); err != nil {
			return nil, err
		}
		collector.ledger.release(staged.decodedBytes)
		staged.node = remoteThreadNode{}
		staged.decodedBytes = 0
	}
	collector.ledger.release(collector.stagedStorageBytes)
	collector.staged = nil
	collector.stagedStorageBytes = 0
	if err := checkpoint.commit(collector.domainBytes); err != nil {
		return nil, err
	}
	complete = true
	return collector.threads, nil
}

type remoteThreadCollector struct {
	ctx                context.Context
	threadIDs          map[string]struct{}
	commentIDs         map[string]struct{}
	service            *Service
	ledger             *sourceLedger
	threads            []review.RemoteThread
	staged             []pendingRemoteThread
	runCPU             CPUAdmission
	details            PullRequestDetails
	responseBytes      int64
	domainBytes        int64
	stagedStorageBytes int64
	poisoned           bool
}

type pendingRemoteThread struct {
	node         remoteThreadNode
	decodedBytes int64
}

func (collector *remoteThreadCollector) stagePage(
	_ int,
	position remotePagePosition,
	page remoteThreadPage,
) (int64, error) {
	if collector.poisoned {
		return 0, errors.New("GitHub review thread collector is discarded")
	}
	if err := validateRemoteThreadPage(page, position, collector.details); err != nil {
		return 0, err
	}
	connection := page.Data.Repository.PullRequest.ReviewThreads
	retained := int64(0)
	for _, node := range *connection.Nodes {
		if len(collector.staged) >= review.MaxRemoteThreads {
			return 0, &LimitError{
				Resource: LimitRemoteThreads,
				Limit:    review.MaxRemoteThreads,
			}
		}
		if len(collector.staged) == cap(collector.staged) {
			capacity := max(16, cap(collector.staged)*2)
			capacity = min(capacity, review.MaxRemoteThreads)
			bytes := int64(capacity) * jsonDecodedElementBytes
			if err := collector.ledger.reserve(bytes); err != nil {
				return 0, err
			}
			grown := make([]pendingRemoteThread, len(collector.staged), capacity)
			copy(grown, collector.staged)
			collector.ledger.release(collector.stagedStorageBytes)
			collector.stagedStorageBytes = bytes
			collector.staged = grown
		}
		decodedBytes := remoteThreadNodeDecodedBytes(node)
		collector.staged = append(collector.staged, pendingRemoteThread{
			node: node, decodedBytes: decodedBytes,
		})
		retained += decodedBytes
	}
	return retained, nil
}

func (collector *remoteThreadCollector) addNode(node remoteThreadNode) (returnErr error) {
	if collector.poisoned {
		return errors.New("GitHub review thread collector is discarded")
	}
	defer func() {
		if returnErr != nil {
			collector.discard()
		}
	}()
	if node.Comments != nil && node.Comments.PageInfo != nil &&
		node.Comments.PageInfo.HasNextPage != nil && *node.Comments.PageInfo.HasNextPage {
		remaining := review.MaxRemoteThreadBytes - collector.responseBytes
		if remaining <= 0 {
			return &LimitError{Resource: LimitRemoteThreads, Limit: uint64(review.MaxRemoteThreadBytes)}
		}
		comments, used, err := collector.service.loadRemainingRemoteComments(
			collector.ctx,
			collector.details,
			node.ID,
			node.Comments,
			remaining,
			collector.ledger,
			collector.runCPU,
		)
		if err != nil {
			return err
		}
		collector.responseBytes += used
		node.Comments = comments
	}
	if len(collector.threads) >= review.MaxRemoteThreads {
		return &LimitError{Resource: LimitRemoteThreads, Limit: review.MaxRemoteThreads}
	}
	return collector.runCPU(collector.ctx, func() error {
		cancelled := func() error { return context.Cause(collector.ctx) }
		domainBytes, identityBytes, err := remoteThreadNodeConstructionBytes(
			cancelled,
			node,
		)
		if err != nil {
			return err
		}
		if reserveErr := collector.ledger.reserve(domainBytes + identityBytes); reserveErr != nil {
			return reserveErr
		}
		thread, err := validateRemoteThreadCancellable(cancelled, node)
		if err != nil {
			return err
		}
		if err := collector.installIdentities(cancelled, thread); err != nil {
			return err
		}
		collector.threads = append(collector.threads, thread)
		collector.domainBytes += domainBytes
		return nil
	})
}

func (collector *remoteThreadCollector) installIdentities(
	cancelled func() error,
	thread review.RemoteThread,
) error {
	if _, exists := collector.threadIDs[thread.ID()]; exists {
		return errors.New("GitHub review threads contain a duplicate identity")
	}
	collector.threadIDs[thread.ID()] = struct{}{}
	for index := range thread.CommentCount() {
		if err := cancelled(); err != nil {
			return err
		}
		comment, exists := thread.Comment(index)
		if !exists {
			return errors.New("GitHub review thread comment index is invalid")
		}
		if _, exists := collector.commentIDs[comment.ID()]; exists {
			return errors.New("GitHub review comments contain a duplicate identity")
		}
		collector.commentIDs[comment.ID()] = struct{}{}
	}
	if err := cancelled(); err != nil {
		return err
	}
	if len(collector.threadIDs)+len(collector.commentIDs) > review.MaxRemoteThreads {
		return errors.New("GitHub review thread identities exceed their bound")
	}
	return nil
}

func (collector *remoteThreadCollector) discard() {
	collector.poisoned = true
	collector.threadIDs = nil
	collector.commentIDs = nil
	collector.threads = nil
	collector.staged = nil
	collector.domainBytes = 0
	collector.stagedStorageBytes = 0
}

func validateRemoteThreadPage(
	page remoteThreadPage,
	position remotePagePosition,
	details PullRequestDetails,
) error {
	if position != remotePageIntermediate && position != remotePageFinal {
		return errors.New("GitHub review thread page position is invalid")
	}
	if err := validateRemoteThreadTarget(page, details); err != nil {
		return err
	}
	connection := page.Data.Repository.PullRequest.ReviewThreads
	if connection == nil || connection.PageInfo == nil ||
		connection.PageInfo.HasNextPage == nil || connection.Nodes == nil {
		return errors.New("GitHub review thread connection is incomplete")
	}
	finalPage := position == remotePageFinal
	if *connection.PageInfo.HasNextPage == finalPage ||
		position == remotePageIntermediate && connection.PageInfo.EndCursor == nil {
		return errors.New("GitHub review thread pagination is incomplete")
	}
	return nil
}

func (service *Service) loadRemainingRemoteComments(
	ctx context.Context,
	details PullRequestDetails,
	threadID string,
	initial *remoteCommentConnection,
	byteLimit int64,
	ledger *sourceLedger,
	runCPU CPUAdmission,
) (*remoteCommentConnection, int64, error) {
	checkpoint := ledger.checkpoint()
	defer checkpoint.rollback()
	if initial == nil || initial.PageInfo == nil ||
		initial.PageInfo.HasNextPage == nil || initial.Nodes == nil ||
		!*initial.PageInfo.HasNextPage || initial.PageInfo.EndCursor == nil {
		return nil, 0, errors.New("GitHub review comment pagination is incomplete")
	}
	owner, repository, ok := strings.Cut(details.Repository.String(), "/")
	if !ok {
		return nil, 0, errors.New("validated GitHub repository cannot be split")
	}
	spool, err := service.graphQLSpool(
		ctx,
		remoteThreadCommentsQuery,
		"comments",
		map[string]any{
			githubFieldOwner: owner, githubFieldName: repository,
			githubFieldNumber: details.Number.Uint64(), "id": threadID,
			githubFieldEndCursor: *initial.PageInfo.EndCursor,
		},
		byteLimit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("load GitHub review thread comments: %w", err)
	}
	responseBytes, err := spool.Size()
	if err != nil {
		return nil, 0, errors.Join(err, spool.Close())
	}
	limits, err := newJSONFramingLimits(
		review.MaxRemoteComments, maxGitHubPageCollectionElements,
	)
	if err != nil {
		return nil, 0, errors.Join(err, spool.Close())
	}
	metrics, lexicalErr := validateCommandJSONWithLimits(
		ctx, spool, runCPU, ledger,
		limits,
	)
	if lexicalErr != nil {
		return nil, 0, errors.Join(lexicalErr, spool.Close())
	}
	retainedBytes := int64(len(*initial.Nodes)) * remoteCommentPayloadMetadataBytes
	if reserveErr := ledger.reserve(retainedBytes); reserveErr != nil {
		return nil, 0, errors.Join(reserveErr, spool.Close())
	}
	comments := append(remoteCommentNodes(nil), (*initial.Nodes)...)
	commentStorageBytes := retainedBytes
	cursorBytes := int64(64 + len(*initial.PageInfo.EndCursor))
	if reserveErr := ledger.reserve(cursorBytes); reserveErr != nil {
		return nil, 0, errors.Join(reserveErr, spool.Close())
	}
	seenCursors := map[string]struct{}{*initial.PageInfo.EndCursor: {}}
	err = decodeRemoteCommentStream(
		ctx, spool.Reader(), runCPU, ledger, metrics,
		func(_ int, position remotePagePosition, page remoteCommentPage) (int64, error) {
			retained, collectErr := collectRemoteCommentPage(
				page,
				position,
				details,
				threadID,
				&comments,
				seenCursors,
				ledger,
				&commentStorageBytes,
				&retainedBytes,
			)
			retainedBytes += retained
			return retained, collectErr
		},
	)
	metrics.release(ledger)
	if err != nil {
		return nil, 0, errors.Join(err, spool.Close())
	}
	const connectionBytes = int64(64)
	if reserveErr := ledger.reserve(connectionBytes); reserveErr != nil {
		return nil, 0, errors.Join(reserveErr, spool.Close())
	}
	done := false
	connection := &remoteCommentConnection{
		PageInfo: &remotePageInfo{HasNextPage: &done},
		Nodes:    &comments,
	}
	if closeErr := spool.Close(); closeErr != nil {
		return nil, 0, closeErr
	}
	retainedBytes += connectionBytes
	if err := checkpoint.commit(retainedBytes); err != nil {
		return nil, 0, err
	}
	return connection, responseBytes, nil
}

const remoteCommentPayloadMetadataBytes int64 = 192

func remoteThreadNodeDecodedBytes(node remoteThreadNode) int64 {
	bytes := int64(len(node.ID) + len(node.Path) + len(node.DiffSide))
	for _, present := range []bool{
		node.IsResolved != nil,
		node.IsOutdated != nil,
		node.Line != nil,
		node.StartLine != nil,
	} {
		if present {
			bytes += jsonDecodedPointerBytes
		}
	}
	if node.Comments == nil {
		return bytes
	}
	bytes += jsonDecodedElementBytes
	if node.Comments.PageInfo != nil {
		bytes += jsonDecodedElementBytes
		if node.Comments.PageInfo.HasNextPage != nil {
			bytes += jsonDecodedPointerBytes
		}
		if node.Comments.PageInfo.EndCursor != nil {
			bytes += jsonDecodedPointerBytes + int64(len(*node.Comments.PageInfo.EndCursor))
		}
	}
	if node.Comments.Nodes == nil {
		return bytes
	}
	bytes += int64(len(*node.Comments.Nodes)) * jsonDecodedElementBytes
	for _, comment := range *node.Comments.Nodes {
		bytes += int64(len(comment.ID) + len(comment.CreatedAt) + len(comment.URL))
		if comment.Body != nil {
			bytes += jsonDecodedPointerBytes + int64(len(*comment.Body))
		}
		if comment.Author != nil {
			bytes += jsonDecodedElementBytes + int64(len(comment.Author.Login))
		}
	}
	return bytes
}

func remoteThreadNodeConstructionBytes(
	cancelled func() error,
	node remoteThreadNode,
) (int64, int64, error) {
	if err := cancelled(); err != nil {
		return 0, 0, err
	}
	domain := int64(96 + len(node.ID) + len(node.Path))
	identities := int64(48)
	if node.Comments == nil || node.Comments.Nodes == nil {
		return domain, identities, cancelled()
	}
	for index, comment := range *node.Comments.Nodes {
		domain += 96 + int64(
			len(comment.ID)+len(comment.CreatedAt)+len(comment.URL),
		)
		if comment.Body != nil {
			domain += int64(len(*comment.Body))
		}
		if comment.Author != nil {
			domain += int64(len(comment.Author.Login))
		}
		identities += 48
		if (index+1)%256 == 0 {
			if err := cancelled(); err != nil {
				return 0, 0, err
			}
		}
	}
	if err := cancelled(); err != nil {
		return 0, 0, err
	}
	return domain, identities, nil
}

func remoteThreadInstallationBytes(
	ctx context.Context,
	snapshot review.Snapshot,
	threads []review.RemoteThread,
) (int64, int64, error) {
	return remoteThreadInstallationBytesCancellable(
		func() error { return context.Cause(ctx) },
		snapshot,
		threads,
	)
}

func remoteThreadInstallationBytesCancellable(
	cancelled func() error,
	snapshot review.Snapshot,
	threads []review.RemoteThread,
) (int64, int64, error) {
	if err := cancelled(); err != nil {
		return 0, 0, err
	}
	identities := len(threads)
	for index, thread := range threads {
		identities += thread.CommentCount()
		if (index+1)%256 == 0 {
			if err := cancelled(); err != nil {
				return 0, 0, err
			}
		}
	}
	if err := cancelled(); err != nil {
		return 0, 0, err
	}
	scratch := int64(snapshot.FileCount()*2*48 + identities*48)
	retained := int64(min(len(threads), review.MaxRemoteThreads) * 32)
	return scratch, retained, nil
}

func collectRemoteCommentPage(
	page remoteCommentPage,
	position remotePagePosition,
	details PullRequestDetails,
	threadID string,
	comments *remoteCommentNodes,
	seenCursors map[string]struct{},
	ledger *sourceLedger,
	storageBytes *int64,
	retainedBytes *int64,
) (int64, error) {
	if comments == nil || seenCursors == nil || ledger == nil || storageBytes == nil ||
		retainedBytes == nil {
		return 0, errors.New("GitHub review comment collection ownership is incomplete")
	}
	if position != remotePageIntermediate && position != remotePageFinal {
		return 0, errors.New("GitHub review comment page position is invalid")
	}
	if err := validateRemoteCommentTarget(page, details, threadID); err != nil {
		return 0, err
	}
	connection := page.Data.Node.Comments
	finalPage := position == remotePageFinal
	if *connection.PageInfo.HasNextPage == finalPage ||
		position == remotePageIntermediate && connection.PageInfo.EndCursor == nil {
		return 0, errors.New("GitHub review comment pagination is incomplete")
	}
	if len(*connection.Nodes) > review.MaxRemoteComments-len(*comments) {
		return 0, &LimitError{Resource: LimitRemoteThreads, Limit: review.MaxRemoteComments}
	}
	charge := remoteCommentPayloadBytes(*connection.Nodes)
	needed := len(*comments) + len(*connection.Nodes)
	if needed > cap(*comments) {
		capacity := max(16, cap(*comments)*2)
		capacity = max(capacity, needed)
		capacity = min(capacity, review.MaxRemoteComments)
		bytes := int64(capacity) * remoteCommentPayloadMetadataBytes
		if err := ledger.reserve(bytes); err != nil {
			return 0, err
		}
		grown := make(remoteCommentNodes, len(*comments), capacity)
		copy(grown, *comments)
		ledger.release(*storageBytes)
		*retainedBytes += bytes - *storageBytes
		*storageBytes = bytes
		*comments = grown
	}
	*comments = append(*comments, (*connection.Nodes)...)
	if connection.PageInfo.EndCursor != nil {
		if _, repeated := seenCursors[*connection.PageInfo.EndCursor]; repeated {
			return 0, errors.New("GitHub review comment cursor repeated")
		}
		cursorBytes := int64(64 + len(*connection.PageInfo.EndCursor))
		if err := ledger.reserve(cursorBytes); err != nil {
			return 0, err
		}
		*retainedBytes += cursorBytes
		seenCursors[*connection.PageInfo.EndCursor] = struct{}{}
	}
	return charge, nil
}

func remoteCommentPayloadBytes(comments []remoteCommentNode) int64 {
	bytes := int64(len(comments)) * remoteCommentPayloadMetadataBytes
	for _, comment := range comments {
		bytes += int64(len(comment.ID) + len(comment.CreatedAt) + len(comment.URL))
		if comment.Body != nil {
			bytes += int64(len(*comment.Body))
		}
		if comment.Author != nil {
			bytes += int64(len(comment.Author.Login))
		}
	}
	return bytes
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
	ledger, err := newSourceLedger(int64(len(value)))
	if err != nil {
		return nil, err
	}
	limits, err := newJSONFramingLimits(
		review.MaxRemoteThreads, maxGitHubPageCollectionElements,
	)
	if err != nil {
		return nil, err
	}
	metrics, err := inspectJSONWithLimits(
		context.Background(), bytes.NewReader(value),
		maxGitHubJSONTokenBytes, directCPUAdmission, ledger,
		limits,
	)
	if err != nil {
		return nil, err
	}
	defer metrics.release(ledger)
	pages := make([]remoteThreadPage, 0)
	err = decodeRemoteThreadStream(
		context.Background(), bytes.NewReader(value),
		directCPUAdmission, ledger, metrics,
		func(_ int, _ remotePagePosition, page remoteThreadPage) (int64, error) {
			pages = append(pages, page)
			return 0, nil
		},
	)
	return pages, err
}

func validateRemoteThread(node remoteThreadNode) (review.RemoteThread, error) {
	return validateRemoteThreadCancellable(nil, node)
}

func validateRemoteThreadCancellable(
	cancelled func() error,
	node remoteThreadNode,
) (review.RemoteThread, error) {
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
	case githubSideLeft:
		side = review.LineOld
	case githubSideRight:
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
	if cancelled != nil {
		if err := cancelled(); err != nil {
			return review.RemoteThread{}, err
		}
	}
	comments := make([]review.RemoteComment, 0, len(*node.Comments.Nodes))
	for _, raw := range *node.Comments.Nodes {
		if cancelled != nil {
			if err := cancelled(); err != nil {
				return review.RemoteThread{}, err
			}
		}
		comment, commentErr := validateRemoteComment(raw)
		if commentErr != nil {
			return review.RemoteThread{}, commentErr
		}
		comments = append(comments, comment)
	}
	if cancelled != nil {
		if err := cancelled(); err != nil {
			return review.RemoteThread{}, err
		}
	}
	return review.TakeRemoteThreadCancellable(cancelled, review.RemoteThreadDescriptor{
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
	if err := validateRemoteField(node.CreatedAt, review.MaxPathBytes); err != nil {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment timestamp")
	}
	if _, err := time.Parse(time.RFC3339, node.CreatedAt); err != nil {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment timestamp")
	}
	if err := validateRemoteField(node.URL, 16<<10); err != nil {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment URL")
	}
	parsedURL, err := url.ParseRequestURI(node.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host != githubHost {
		return review.RemoteComment{}, errors.New("invalid GitHub review comment URL")
	}
	author := ""
	if node.Author != nil {
		author = node.Author.Login
		if err := validateRemoteField(author, 256); err != nil {
			return review.RemoteComment{}, errors.New("invalid GitHub review comment author")
		}
	}
	return review.TakeRemoteComment(review.RemoteCommentDescriptor{
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
