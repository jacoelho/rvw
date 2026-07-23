package source

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

const (
	jsonFieldData          = "data"
	jsonFieldErrors        = "errors"
	jsonFieldNameWithOwner = "nameWithOwner"
	jsonFieldNode          = "node"
	jsonFieldNodes         = "nodes"
	jsonFieldSide          = "side"
	jsonFieldStartLine     = "startLine"
)

const (
	jsonFieldIDData jsonPresenceField = iota + 1
	jsonFieldIDErrors
	jsonFieldIDViewer
	jsonFieldIDRepository
	jsonFieldIDNameWithOwner
	jsonFieldIDPullRequest
	jsonFieldIDBaseRefOID
	jsonFieldIDHeadRefOID
	jsonFieldIDNumber
	jsonFieldIDReviewThreads
	jsonFieldIDReviews
	jsonFieldIDNode
	jsonFieldIDComments
	jsonFieldIDPageInfo
	jsonFieldIDHasNextPage
	jsonFieldIDEndCursor
	jsonFieldIDNodes
	jsonFieldIDID
	jsonFieldIDIsResolved
	jsonFieldIDIsOutdated
	jsonFieldIDPath
	jsonFieldIDLine
	jsonFieldIDStartLine
	jsonFieldIDDiffSide
	jsonFieldIDBody
	jsonFieldIDCreatedAt
	jsonFieldIDURL
	jsonFieldIDAuthor
	jsonFieldIDLogin
	jsonFieldIDFullDatabaseID
	jsonFieldIDState
	jsonFieldIDViewerDidAuthor
	jsonFieldIDViewerCanUpdate
	jsonFieldIDCommit
	jsonFieldIDOID
	jsonFieldIDViewerCanDelete
	jsonFieldIDOriginalStartLine
	jsonFieldIDOriginalLine
	jsonFieldIDInReplyToID
	jsonFieldIDStartLineREST
	jsonFieldIDStartSide
	jsonFieldIDSide
	jsonFieldIDNodeID
	jsonFieldIDSubjectType
	jsonFieldIDOriginalCommitID
	jsonFieldIDCommitID
	jsonFieldIDPullRequestReviewID
	jsonFieldIDMergedAt
	jsonFieldIDUser
	jsonFieldIDBase
	jsonFieldIDRef
	jsonFieldIDSHA
	jsonFieldIDHead
	jsonFieldIDTitle
	jsonFieldIDHTMLURL
	jsonFieldIDChangedFiles
	jsonFieldIDDraft
	jsonFieldIDLimit
)

type jsonPathValue struct {
	name    string
	lease   jsonDecodeLease
	fieldID jsonPresenceField
	named   bool
}

type jsonPathContainer struct {
	name            string
	pendingKey      string
	nameLease       jsonDecodeLease
	pendingKeyLease jsonDecodeLease
	count           int
	index           int
	fieldID         jsonPresenceField
	pendingFieldID  jsonPresenceField
	kind            jsonFrameKind
	named           bool
	pendingNamed    bool
}

// jsonPathTracker belongs to an endpoint collector, not the generic framer.
// It gives endpoint code structural context without teaching the grammar
// machine field names or DTO schemas.
type jsonPathTracker struct {
	stack [maxGitHubJSONDepth]jsonPathContainer
	depth int
}

type jsonPresenceField uint8

type jsonFieldPresence struct {
	bits uint64
}

type jsonDuplicateGuard struct {
	objects [maxGitHubJSONDepth]jsonFieldPresence
}

func (guard *jsonDuplicateGuard) startObject(depth int) {
	guard.objects[depth-1] = jsonFieldPresence{}
}

func (guard *jsonDuplicateGuard) endObject(depth int) {
	guard.objects[depth-1] = jsonFieldPresence{}
}

func (guard *jsonDuplicateGuard) key(path *jsonPathTracker) error {
	if path.depth == 0 {
		return errors.New("JSON duplicate guard is outside an object")
	}
	field := path.stack[path.depth-1].pendingFieldID
	if field == 0 {
		return nil
	}
	return guard.objects[path.depth-1].add(field)
}

//nolint:cyclop,funlen,goconst,maintidx // The closed allocation-free dispatch lists the wire vocabulary.
func recognizedJSONField(name string) (jsonPresenceField, bool) {
	switch name {
	case jsonFieldData:
		return jsonFieldIDData, true
	case jsonFieldErrors:
		return jsonFieldIDErrors, true
	case "viewer":
		return jsonFieldIDViewer, true
	case "repository":
		return jsonFieldIDRepository, true
	case jsonFieldNameWithOwner:
		return jsonFieldIDNameWithOwner, true
	case "pullRequest":
		return jsonFieldIDPullRequest, true
	case "baseRefOid":
		return jsonFieldIDBaseRefOID, true
	case "headRefOid":
		return jsonFieldIDHeadRefOID, true
	case githubFieldNumber:
		return jsonFieldIDNumber, true
	case "reviewThreads":
		return jsonFieldIDReviewThreads, true
	case "reviews":
		return jsonFieldIDReviews, true
	case jsonFieldNode:
		return jsonFieldIDNode, true
	case "comments":
		return jsonFieldIDComments, true
	case "pageInfo":
		return jsonFieldIDPageInfo, true
	case "hasNextPage":
		return jsonFieldIDHasNextPage, true
	case githubFieldEndCursor:
		return jsonFieldIDEndCursor, true
	case jsonFieldNodes:
		return jsonFieldIDNodes, true
	case "id":
		return jsonFieldIDID, true
	case "isResolved":
		return jsonFieldIDIsResolved, true
	case "isOutdated":
		return jsonFieldIDIsOutdated, true
	case graphqlPath:
		return jsonFieldIDPath, true
	case githubSubjectLine:
		return jsonFieldIDLine, true
	case jsonFieldStartLine:
		return jsonFieldIDStartLine, true
	case "diffSide":
		return jsonFieldIDDiffSide, true
	case graphqlBody:
		return jsonFieldIDBody, true
	case "createdAt":
		return jsonFieldIDCreatedAt, true
	case "url":
		return jsonFieldIDURL, true
	case "author":
		return jsonFieldIDAuthor, true
	case "login":
		return jsonFieldIDLogin, true
	case "fullDatabaseId":
		return jsonFieldIDFullDatabaseID, true
	case "state":
		return jsonFieldIDState, true
	case "viewerDidAuthor":
		return jsonFieldIDViewerDidAuthor, true
	case "viewerCanUpdate":
		return jsonFieldIDViewerCanUpdate, true
	case "commit":
		return jsonFieldIDCommit, true
	case "oid":
		return jsonFieldIDOID, true
	case "viewerCanDelete":
		return jsonFieldIDViewerCanDelete, true
	case "original_start_line":
		return jsonFieldIDOriginalStartLine, true
	case "original_line":
		return jsonFieldIDOriginalLine, true
	case "in_reply_to_id":
		return jsonFieldIDInReplyToID, true
	case "start_line":
		return jsonFieldIDStartLineREST, true
	case "start_side":
		return jsonFieldIDStartSide, true
	case jsonFieldSide:
		return jsonFieldIDSide, true
	case "node_id":
		return jsonFieldIDNodeID, true
	case "subject_type":
		return jsonFieldIDSubjectType, true
	case "original_commit_id":
		return jsonFieldIDOriginalCommitID, true
	case "commit_id":
		return jsonFieldIDCommitID, true
	case "pull_request_review_id":
		return jsonFieldIDPullRequestReviewID, true
	case "merged_at":
		return jsonFieldIDMergedAt, true
	case "user":
		return jsonFieldIDUser, true
	case "base":
		return jsonFieldIDBase, true
	case "ref":
		return jsonFieldIDRef, true
	case "sha":
		return jsonFieldIDSHA, true
	case "head":
		return jsonFieldIDHead, true
	case "title":
		return jsonFieldIDTitle, true
	case "html_url":
		return jsonFieldIDHTMLURL, true
	case "changed_files":
		return jsonFieldIDChangedFiles, true
	case "draft":
		return jsonFieldIDDraft, true
	default:
		return 0, false
	}
}

func (presence *jsonFieldPresence) add(field jsonPresenceField) error {
	if field == 0 || field >= jsonFieldIDLimit {
		return errors.New("JSON presence field is invalid")
	}
	bit := uint64(1) << (field - 1)
	if presence.bits&bit != 0 {
		return errors.New("JSON object contains a duplicate recognized field")
	}
	presence.bits |= bit
	return nil
}

func (tracker *jsonPathTracker) keyFrame(scope *jsonDecodeScope, frame jsonFrame) error {
	if tracker.depth == 0 || tracker.stack[tracker.depth-1].kind != jsonObjectStart {
		return errors.New("JSON key is outside an object")
	}
	if scope == nil || frame.Kind != jsonObjectKey {
		return errors.New("JSON key decoding is incomplete")
	}
	parent := &tracker.stack[tracker.depth-1]
	if parent.pendingNamed {
		return errors.New("JSON object key has no value")
	}
	lease, err := scope.acquire(frame.DecodedBytes)
	if err != nil {
		return fmt.Errorf("reserve JSON object key: %w", err)
	}
	key, err := decodeJSONString(frame.Raw)
	if err != nil {
		lease.release()
		return fmt.Errorf("decode JSON object key: %w", err)
	}
	if int64(len(key)) != frame.DecodedBytes {
		lease.release()
		return errors.New("JSON object key size differs from preflight")
	}
	parent.pendingKey = key
	parent.pendingKeyLease = lease
	parent.pendingFieldID, _ = recognizedJSONField(key)
	parent.pendingNamed = true
	return nil
}

func (tracker *jsonPathTracker) start(kind jsonFrameKind, count int) error {
	if tracker.depth == len(tracker.stack) {
		return errors.New("JSON endpoint path exceeds its depth bound")
	}
	value := tracker.takeValue()
	tracker.stack[tracker.depth] = jsonPathContainer{
		name: value.name, nameLease: value.lease, fieldID: value.fieldID,
		kind: kind, count: count, index: -1, named: value.named,
	}
	tracker.depth++
	return nil
}

func (tracker *jsonPathTracker) end(kind jsonFrameKind) error {
	if tracker.depth == 0 {
		return errors.New("JSON endpoint path is unbalanced")
	}
	want := jsonObjectStart
	if kind == jsonArrayEnd {
		want = jsonArrayStart
	}
	if tracker.stack[tracker.depth-1].kind != want {
		return errors.New("JSON endpoint path delimiter is unbalanced")
	}
	tracker.depth--
	container := &tracker.stack[tracker.depth]
	container.nameLease.release()
	container.pendingKeyLease.release()
	*container = jsonPathContainer{}
	return nil
}

func (tracker *jsonPathTracker) scalar(visit func(jsonPathValue) error) error {
	if visit == nil {
		return errors.New("JSON scalar visitor is nil")
	}
	value := tracker.takeValue()
	defer value.lease.release()
	return visit(value)
}

func (tracker *jsonPathTracker) takeValue() jsonPathValue {
	if tracker.depth == 0 {
		return jsonPathValue{}
	}
	parent := &tracker.stack[tracker.depth-1]
	if parent.kind == jsonArrayStart {
		parent.index++
		return jsonPathValue{}
	}
	value := jsonPathValue{
		name: parent.pendingKey, lease: parent.pendingKeyLease,
		fieldID: parent.pendingFieldID, named: parent.pendingNamed,
	}
	parent.pendingKey = ""
	parent.pendingKeyLease = jsonDecodeLease{}
	parent.pendingFieldID = 0
	parent.pendingNamed = false
	return value
}

func (tracker *jsonPathTracker) close() {
	if tracker == nil {
		return
	}
	for index := range tracker.depth {
		tracker.stack[index].nameLease.release()
		tracker.stack[index].pendingKeyLease.release()
		tracker.stack[index] = jsonPathContainer{}
	}
	tracker.depth = 0
}

func (tracker *jsonPathTracker) suffix(names ...string) bool {
	want := len(names) - 1
	for index := tracker.depth - 1; index >= 0; index-- {
		container := tracker.stack[index]
		if !container.named {
			continue
		}
		if want < 0 {
			return false
		}
		if names[want] != container.name {
			return false
		}
		want--
	}
	return want < 0
}

func (tracker *jsonPathTracker) scalarSuffix(value jsonPathValue, names ...string) bool {
	if !value.named || len(names) == 0 || names[len(names)-1] != value.name {
		return false
	}
	return tracker.suffix(names[:len(names)-1]...)
}

func (tracker *jsonPathTracker) parents(fields ...jsonPresenceField) bool {
	want := len(fields) - 1
	for index := tracker.depth - 1; index >= 0; index-- {
		container := tracker.stack[index]
		if !container.named {
			continue
		}
		if want < 0 || container.fieldID != fields[want] {
			return false
		}
		want--
	}
	return want < 0
}

func (tracker *jsonPathTracker) namedAncestor(offset int) (jsonPresenceField, bool) {
	if offset < 0 {
		return 0, false
	}
	for index := tracker.depth - 1; index >= 0; index-- {
		container := tracker.stack[index]
		if !container.named {
			continue
		}
		if offset == 0 {
			return container.fieldID, true
		}
		offset--
	}
	return 0, false
}

func jsonBooleanValue(frame jsonFrame) (bool, error) {
	switch frame.Kind {
	case jsonTrue:
		return true, nil
	case jsonFalse:
		return false, nil
	default:
		return false, errors.New("JSON field is not a Boolean")
	}
}

func jsonUint64Value(frame jsonFrame) (uint64, error) {
	if frame.Kind != jsonNumber || len(frame.Raw) == 0 || bytes.ContainsAny(frame.Raw, ".eE-") {
		return 0, errors.New("JSON field is not an unsigned integer")
	}
	value, err := strconv.ParseUint(string(frame.Raw), 10, 64)
	if err != nil {
		return 0, errors.New("JSON unsigned integer is invalid")
	}
	return value, nil
}

func jsonGitHubUint64(frame jsonFrame) (githubUint64, error) {
	var text string
	switch frame.Kind {
	case jsonString:
		value, err := decodeJSONString(frame.Raw)
		if err != nil {
			return 0, err
		}
		text = value
	case jsonNumber:
		text = string(frame.Raw)
	default:
		return 0, errors.New("GitHub database identity has an invalid JSON type")
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("GitHub database identity is invalid")
	}
	return githubUint64(parsed), nil
}
