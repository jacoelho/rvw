package source

import "testing"

type endpointScalarRouteCase struct {
	name         string
	field        jsonPresenceField
	parents      []jsonPresenceField
	want         uint8
	unnamedDepth int
}

func TestRepositoryAndPullRequestScalarRoutes(t *testing.T) {
	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "repository name", field: jsonFieldIDNameWithOwner, want: uint8(repositoryNameRoute)},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := repositoryJSONCollector{path: path}
		return uint8(collector.route(jsonPathValue{fieldID: field, named: true}))
	})

	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "merged at", field: jsonFieldIDMergedAt, want: uint8(prMergedAtField)},
		{name: "user null", field: jsonFieldIDUser, want: uint8(prUserField)},
		{name: "user login", field: jsonFieldIDLogin, parents: ids(jsonFieldIDUser), want: uint8(prUserLoginField)},
		{name: "base ref", field: jsonFieldIDRef, parents: ids(jsonFieldIDBase), want: uint8(prBaseRefField)},
		{name: "base sha", field: jsonFieldIDSHA, parents: ids(jsonFieldIDBase), want: uint8(prBaseSHAField)},
		{name: "head ref", field: jsonFieldIDRef, parents: ids(jsonFieldIDHead), want: uint8(prHeadRefField)},
		{name: "head sha", field: jsonFieldIDSHA, parents: ids(jsonFieldIDHead), want: uint8(prHeadSHAField)},
		{name: "title", field: jsonFieldIDTitle, want: uint8(prTitleField)},
		{name: "HTML URL", field: jsonFieldIDHTMLURL, want: uint8(prHTMLURLField)},
		{name: "state", field: jsonFieldIDState, want: uint8(prStateField)},
		{name: "number", field: jsonFieldIDNumber, want: uint8(prNumberField)},
		{name: "changed files", field: jsonFieldIDChangedFiles, want: uint8(prChangedFilesField)},
		{name: "draft", field: jsonFieldIDDraft, want: uint8(prDraftField)},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := pullRequestJSONCollector{path: path}
		route, matched := collector.field(jsonPathValue{fieldID: field, named: true})
		if !matched {
			return 0
		}
		return uint8(route)
	})
}

func TestRemoteThreadScalarRoutes(t *testing.T) {
	target := ids(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	threadPage := appendIDs(target, jsonFieldIDReviewThreads, jsonFieldIDPageInfo)
	thread := appendIDs(target, jsonFieldIDReviewThreads, jsonFieldIDNodes)
	commentPage := appendIDs(
		target, jsonFieldIDReviewThreads, jsonFieldIDNodes, jsonFieldIDComments, jsonFieldIDPageInfo,
	)
	comment := appendIDs(
		target, jsonFieldIDReviewThreads, jsonFieldIDNodes, jsonFieldIDComments, jsonFieldIDNodes,
	)
	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "repository name", field: jsonFieldIDNameWithOwner, parents: ids(jsonFieldIDData, jsonFieldIDRepository), want: uint8(remoteThreadRepositoryNameRoute)},
		{name: "base oid", field: jsonFieldIDBaseRefOID, parents: target, want: uint8(remoteThreadBaseOIDRoute)},
		{name: "head oid", field: jsonFieldIDHeadRefOID, parents: target, want: uint8(remoteThreadHeadOIDRoute)},
		{name: "number", field: jsonFieldIDNumber, parents: target, want: uint8(remoteThreadNumberRoute)},
		{name: "thread page next", field: jsonFieldIDHasNextPage, parents: threadPage, want: uint8(remoteThreadPageHasNextRoute)},
		{name: "thread page cursor", field: jsonFieldIDEndCursor, parents: threadPage, want: uint8(remoteThreadPageCursorRoute)},
		{name: "thread id", field: jsonFieldIDID, parents: thread, want: uint8(remoteThreadIDRoute)},
		{name: "thread path", field: jsonFieldIDPath, parents: thread, want: uint8(remoteThreadPathRoute)},
		{name: "thread side", field: jsonFieldIDDiffSide, parents: thread, want: uint8(remoteThreadDiffSideRoute)},
		{name: "thread resolved", field: jsonFieldIDIsResolved, parents: thread, want: uint8(remoteThreadResolvedRoute)},
		{name: "thread outdated", field: jsonFieldIDIsOutdated, parents: thread, want: uint8(remoteThreadOutdatedRoute)},
		{name: "thread line", field: jsonFieldIDLine, parents: thread, want: uint8(remoteThreadLineRoute)},
		{name: "thread start line", field: jsonFieldIDStartLine, parents: thread, want: uint8(remoteThreadStartLineRoute)},
		{name: "comment page next", field: jsonFieldIDHasNextPage, parents: commentPage, want: uint8(remoteThreadCommentPageHasNextRoute)},
		{name: "comment page cursor", field: jsonFieldIDEndCursor, parents: commentPage, want: uint8(remoteThreadCommentPageCursorRoute)},
		{name: "comment id", field: jsonFieldIDID, parents: comment, want: uint8(remoteThreadCommentIDRoute)},
		{name: "comment body", field: jsonFieldIDBody, parents: comment, want: uint8(remoteThreadCommentBodyRoute)},
		{name: "comment created", field: jsonFieldIDCreatedAt, parents: comment, want: uint8(remoteThreadCommentCreatedAtRoute)},
		{name: "comment URL", field: jsonFieldIDURL, parents: comment, want: uint8(remoteThreadCommentURLRoute)},
		{name: "comment author", field: jsonFieldIDAuthor, parents: comment, want: uint8(remoteThreadCommentAuthorRoute)},
		{name: "comment login", field: jsonFieldIDLogin, parents: appendIDs(comment, jsonFieldIDAuthor), want: uint8(remoteThreadCommentLoginRoute)},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := remoteThreadJSONCollector{path: path}
		return uint8(collector.scalarRoute(field))
	})
}

func TestRemoteCommentScalarRoutes(t *testing.T) {
	target := ids(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	comment := ids(jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDNodes)
	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "repository name", field: jsonFieldIDNameWithOwner, parents: ids(jsonFieldIDData, jsonFieldIDRepository), want: uint8(remoteCommentRepositoryNameRoute)},
		{name: "base oid", field: jsonFieldIDBaseRefOID, parents: target, want: uint8(remoteCommentBaseOIDRoute)},
		{name: "head oid", field: jsonFieldIDHeadRefOID, parents: target, want: uint8(remoteCommentHeadOIDRoute)},
		{name: "number", field: jsonFieldIDNumber, parents: target, want: uint8(remoteCommentNumberRoute)},
		{name: "node id", field: jsonFieldIDID, parents: ids(jsonFieldIDData, jsonFieldIDNode), want: uint8(remoteCommentNodeIDRoute)},
		{name: "page next", field: jsonFieldIDHasNextPage, parents: ids(jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDPageInfo), want: uint8(remoteCommentPageHasNextRoute)},
		{name: "page cursor", field: jsonFieldIDEndCursor, parents: ids(jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDPageInfo), want: uint8(remoteCommentPageCursorRoute)},
		{name: "comment id", field: jsonFieldIDID, parents: comment, want: uint8(remoteCommentIDRoute)},
		{name: "comment body", field: jsonFieldIDBody, parents: comment, want: uint8(remoteCommentBodyRoute)},
		{name: "comment created", field: jsonFieldIDCreatedAt, parents: comment, want: uint8(remoteCommentCreatedAtRoute)},
		{name: "comment URL", field: jsonFieldIDURL, parents: comment, want: uint8(remoteCommentURLRoute)},
		{name: "comment author", field: jsonFieldIDAuthor, parents: comment, want: uint8(remoteCommentAuthorRoute)},
		{name: "comment login", field: jsonFieldIDLogin, parents: appendIDs(comment, jsonFieldIDAuthor), want: uint8(remoteCommentLoginRoute)},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := remoteCommentJSONCollector{path: path}
		return uint8(collector.scalarRoute(field))
	})
}

func TestPendingReviewScalarRoutes(t *testing.T) {
	target := ids(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	node := appendIDs(target, jsonFieldIDReviews, jsonFieldIDNodes)
	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "viewer login", field: jsonFieldIDLogin, parents: ids(jsonFieldIDData, jsonFieldIDViewer), want: uint8(pendingReviewViewerLoginRoute)},
		{name: "repository name", field: jsonFieldIDNameWithOwner, parents: ids(jsonFieldIDData, jsonFieldIDRepository), want: uint8(pendingReviewRepositoryNameRoute)},
		{name: "base oid", field: jsonFieldIDBaseRefOID, parents: target, want: uint8(pendingReviewBaseOIDRoute)},
		{name: "head oid", field: jsonFieldIDHeadRefOID, parents: target, want: uint8(pendingReviewHeadOIDRoute)},
		{name: "number", field: jsonFieldIDNumber, parents: target, want: uint8(pendingReviewNumberRoute)},
		{name: "page next", field: jsonFieldIDHasNextPage, parents: appendIDs(target, jsonFieldIDReviews, jsonFieldIDPageInfo), want: uint8(pendingReviewPageHasNextRoute)},
		{name: "page cursor", field: jsonFieldIDEndCursor, parents: appendIDs(target, jsonFieldIDReviews, jsonFieldIDPageInfo), want: uint8(pendingReviewPageCursorRoute)},
		{name: "review id", field: jsonFieldIDID, parents: node, want: uint8(pendingReviewIDRoute)},
		{name: "database id", field: jsonFieldIDFullDatabaseID, parents: node, want: uint8(pendingReviewDatabaseIDRoute)},
		{name: "body", field: jsonFieldIDBody, parents: node, want: uint8(pendingReviewBodyRoute)},
		{name: "state", field: jsonFieldIDState, parents: node, want: uint8(pendingReviewStateRoute)},
		{name: "authored", field: jsonFieldIDViewerDidAuthor, parents: node, want: uint8(pendingReviewViewerDidAuthorRoute)},
		{name: "update", field: jsonFieldIDViewerCanUpdate, parents: node, want: uint8(pendingReviewViewerCanUpdateRoute)},
		{name: "commit oid", field: jsonFieldIDOID, parents: appendIDs(node, jsonFieldIDCommit), want: uint8(pendingReviewCommitOIDRoute)},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := pendingReviewJSONCollector{path: path}
		return uint8(collector.scalarRoute(field))
	})
}

func TestPendingCommentScalarRoutes(t *testing.T) {
	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "original start", field: jsonFieldIDOriginalStartLine, want: uint8(pendingCommentOriginalStartLineRoute), unnamedDepth: 3},
		{name: "line", field: jsonFieldIDLine, want: uint8(pendingCommentLineRoute), unnamedDepth: 3},
		{name: "body", field: jsonFieldIDBody, want: uint8(pendingCommentBodyRoute), unnamedDepth: 3},
		{name: "original line", field: jsonFieldIDOriginalLine, want: uint8(pendingCommentOriginalLineRoute), unnamedDepth: 3},
		{name: "reply", field: jsonFieldIDInReplyToID, want: uint8(pendingCommentInReplyToIDRoute), unnamedDepth: 3},
		{name: "start line", field: jsonFieldIDStartLineREST, want: uint8(pendingCommentStartLineRoute), unnamedDepth: 3},
		{name: "start side", field: jsonFieldIDStartSide, want: uint8(pendingCommentStartSideRoute), unnamedDepth: 3},
		{name: "side", field: jsonFieldIDSide, want: uint8(pendingCommentSideRoute), unnamedDepth: 3},
		{name: "node id", field: jsonFieldIDNodeID, want: uint8(pendingCommentNodeIDRoute), unnamedDepth: 3},
		{name: "subject", field: jsonFieldIDSubjectType, want: uint8(pendingCommentSubjectTypeRoute), unnamedDepth: 3},
		{name: "original commit", field: jsonFieldIDOriginalCommitID, want: uint8(pendingCommentOriginalCommitIDRoute), unnamedDepth: 3},
		{name: "commit", field: jsonFieldIDCommitID, want: uint8(pendingCommentCommitIDRoute), unnamedDepth: 3},
		{name: "path", field: jsonFieldIDPath, want: uint8(pendingCommentPathRoute), unnamedDepth: 3},
		{name: "id", field: jsonFieldIDID, want: uint8(pendingCommentIDRoute), unnamedDepth: 3},
		{name: "review id", field: jsonFieldIDPullRequestReviewID, want: uint8(pendingCommentPullRequestReviewIDRoute), unnamedDepth: 3},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := pendingCommentJSONCollector{path: path}
		return uint8(collector.scalarRoute(field))
	})
}

func TestPendingCapabilityScalarRoutes(t *testing.T) {
	target := ids(jsonFieldIDData, jsonFieldIDRepository, jsonFieldIDPullRequest)
	capability := ids(jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDNodes)
	assertEndpointScalarRoutes(t, []endpointScalarRouteCase{
		{name: "repository name", field: jsonFieldIDNameWithOwner, parents: ids(jsonFieldIDData, jsonFieldIDRepository), want: uint8(pendingCapabilityRepositoryNameRoute)},
		{name: "base oid", field: jsonFieldIDBaseRefOID, parents: target, want: uint8(pendingCapabilityBaseOIDRoute)},
		{name: "head oid", field: jsonFieldIDHeadRefOID, parents: target, want: uint8(pendingCapabilityHeadOIDRoute)},
		{name: "number", field: jsonFieldIDNumber, parents: target, want: uint8(pendingCapabilityNumberRoute)},
		{name: "node id", field: jsonFieldIDID, parents: ids(jsonFieldIDData, jsonFieldIDNode), want: uint8(pendingCapabilityNodeIDRoute)},
		{name: "page next", field: jsonFieldIDHasNextPage, parents: ids(jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDPageInfo), want: uint8(pendingCapabilityPageHasNextRoute)},
		{name: "page cursor", field: jsonFieldIDEndCursor, parents: ids(jsonFieldIDData, jsonFieldIDNode, jsonFieldIDComments, jsonFieldIDPageInfo), want: uint8(pendingCapabilityPageCursorRoute)},
		{name: "capability id", field: jsonFieldIDID, parents: capability, want: uint8(pendingCapabilityIDRoute)},
		{name: "database id", field: jsonFieldIDFullDatabaseID, parents: capability, want: uint8(pendingCapabilityDatabaseIDRoute)},
		{name: "update", field: jsonFieldIDViewerCanUpdate, parents: capability, want: uint8(pendingCapabilityViewerCanUpdateRoute)},
		{name: "delete", field: jsonFieldIDViewerCanDelete, parents: capability, want: uint8(pendingCapabilityViewerCanDeleteRoute)},
	}, func(field jsonPresenceField, path jsonPathTracker) uint8 {
		collector := pendingCapabilityJSONCollector{path: path}
		return uint8(collector.scalarRoute(field))
	})
}

func assertEndpointScalarRoutes(
	t *testing.T,
	cases []endpointScalarRouteCase,
	route func(jsonPresenceField, jsonPathTracker) uint8,
) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := endpointRoutePath(test.unnamedDepth, test.parents)
			if got := route(test.field, path); got != test.want {
				t.Fatalf("route = %d, want %d", got, test.want)
			}
			if got := route(0, path); got != 0 {
				t.Fatalf("ID-zero leaf route = %d", got)
			}
			blocked := path
			blocked.stack[blocked.depth] = jsonPathContainer{named: true, fieldID: 0}
			blocked.depth++
			if got := route(test.field, blocked); got != 0 {
				t.Fatalf("route through unknown named barrier = %d", got)
			}
		})
	}
}

func endpointRoutePath(unnamedDepth int, fields []jsonPresenceField) jsonPathTracker {
	path := jsonPathTracker{depth: unnamedDepth + len(fields)}
	for index, field := range fields {
		path.stack[unnamedDepth+index] = jsonPathContainer{named: true, fieldID: field}
	}
	return path
}

func ids(fields ...jsonPresenceField) []jsonPresenceField {
	return fields
}

func appendIDs(prefix []jsonPresenceField, fields ...jsonPresenceField) []jsonPresenceField {
	result := make([]jsonPresenceField, 0, len(prefix)+len(fields))
	result = append(result, prefix...)
	return append(result, fields...)
}
