package source

import (
	"errors"
	"strings"
	"testing"
)

func TestRecognizedJSONFieldIDsAreExhaustiveAndUnique(t *testing.T) {
	fields := []struct {
		name string
		id   jsonPresenceField
	}{
		{jsonFieldData, jsonFieldIDData},
		{jsonFieldErrors, jsonFieldIDErrors},
		{"viewer", jsonFieldIDViewer},
		{"repository", jsonFieldIDRepository},
		{"nameWithOwner", jsonFieldIDNameWithOwner},
		{"pullRequest", jsonFieldIDPullRequest},
		{"baseRefOid", jsonFieldIDBaseRefOID},
		{"headRefOid", jsonFieldIDHeadRefOID},
		{"number", jsonFieldIDNumber},
		{"reviewThreads", jsonFieldIDReviewThreads},
		{"reviews", jsonFieldIDReviews},
		{jsonFieldNode, jsonFieldIDNode},
		{"comments", jsonFieldIDComments},
		{"pageInfo", jsonFieldIDPageInfo},
		{"hasNextPage", jsonFieldIDHasNextPage},
		{"endCursor", jsonFieldIDEndCursor},
		{jsonFieldNodes, jsonFieldIDNodes},
		{"id", jsonFieldIDID},
		{"isResolved", jsonFieldIDIsResolved},
		{"isOutdated", jsonFieldIDIsOutdated},
		{graphqlPath, jsonFieldIDPath},
		{githubSubjectLine, jsonFieldIDLine},
		{"startLine", jsonFieldIDStartLine},
		{"diffSide", jsonFieldIDDiffSide},
		{graphqlBody, jsonFieldIDBody},
		{"createdAt", jsonFieldIDCreatedAt},
		{"url", jsonFieldIDURL},
		{"author", jsonFieldIDAuthor},
		{"login", jsonFieldIDLogin},
		{"fullDatabaseId", jsonFieldIDFullDatabaseID},
		{"state", jsonFieldIDState},
		{"viewerDidAuthor", jsonFieldIDViewerDidAuthor},
		{"viewerCanUpdate", jsonFieldIDViewerCanUpdate},
		{"commit", jsonFieldIDCommit},
		{"oid", jsonFieldIDOID},
		{"viewerCanDelete", jsonFieldIDViewerCanDelete},
		{"original_start_line", jsonFieldIDOriginalStartLine},
		{"original_line", jsonFieldIDOriginalLine},
		{"in_reply_to_id", jsonFieldIDInReplyToID},
		{"start_line", jsonFieldIDStartLineREST},
		{"start_side", jsonFieldIDStartSide},
		{jsonFieldSide, jsonFieldIDSide},
		{"node_id", jsonFieldIDNodeID},
		{"subject_type", jsonFieldIDSubjectType},
		{"original_commit_id", jsonFieldIDOriginalCommitID},
		{"commit_id", jsonFieldIDCommitID},
		{"pull_request_review_id", jsonFieldIDPullRequestReviewID},
		{"merged_at", jsonFieldIDMergedAt},
		{"user", jsonFieldIDUser},
		{"base", jsonFieldIDBase},
		{"ref", jsonFieldIDRef},
		{"sha", jsonFieldIDSHA},
		{"head", jsonFieldIDHead},
		{"title", jsonFieldIDTitle},
		{"html_url", jsonFieldIDHTMLURL},
		{"changed_files", jsonFieldIDChangedFiles},
		{"draft", jsonFieldIDDraft},
	}
	if len(fields) != int(jsonFieldIDLimit-1) {
		t.Fatalf("mapping entries = %d, want %d", len(fields), jsonFieldIDLimit-1)
	}
	seen := make(map[jsonPresenceField]string, len(fields))
	for _, field := range fields {
		got, recognized := recognizedJSONField(field.name)
		if !recognized || got != field.id {
			t.Fatalf("recognizedJSONField(%q) = %d, %t; want %d, true", field.name, got, recognized, field.id)
		}
		if prior, exists := seen[got]; exists {
			t.Fatalf("fields %q and %q share ID %d", prior, field.name, got)
		}
		seen[got] = field.name
	}
	for _, unknown := range []string{"", "unknown", "m0", graphqlClientID} {
		if id, recognized := recognizedJSONField(unknown); recognized || id != 0 {
			t.Fatalf("unknown field %q mapped to %d, %t", unknown, id, recognized)
		}
	}
}

func TestDecodedRecognizedKeyRejectsEscapedDuplicate(t *testing.T) {
	_, err := decodePublicationReviewID(t.Context(), []byte(`{"id":1,"\u0069d":2}`))
	if err == nil || err.Error() != "JSON object contains a duplicate recognized field" {
		t.Fatalf("escaped duplicate error = %v", err)
	}
}

func TestJSONNamedUnknownWrappersAreRouteBarriers(t *testing.T) {
	encoded := []byte(`{"":{"nameWithOwner":123},"unknown":{"nameWithOwner":123},` +
		`"nameWithOwner":"owner/repository"}`)
	got, err := decodeRepositoryJSON(t.Context(), encoded, directCPUAdmission)
	if err != nil {
		t.Fatal(err)
	}
	if got != "owner/repository" {
		t.Fatalf("repository = %q", got)
	}
	for _, wrapper := range []string{`""`, `"unknown"`} {
		_, err := decodeRepositoryJSON(
			t.Context(),
			[]byte(`{`+wrapper+`:{"nameWithOwner":"shadow"}}`),
			directCPUAdmission,
		)
		if err == nil || err.Error() != "repository JSON is missing nameWithOwner" {
			t.Fatalf("wrapper %s error = %v", wrapper, err)
		}
	}
}

func TestWrongParentRecognizedScalarsAreIgnoredButDuplicateGuarded(t *testing.T) {
	got, err := decodeRepositoryJSON(
		t.Context(),
		[]byte(`{"unknown":{"nameWithOwner":123},"nameWithOwner":"owner/repository"}`),
		directCPUAdmission,
	)
	if err != nil || got != "owner/repository" {
		t.Fatalf("wrong-parent field changed result: repository=%q error=%v", got, err)
	}
	_, err = decodeRepositoryJSON(
		t.Context(),
		[]byte(`{"unknown":{"nameWithOwner":123,"nameWithOwner":456},`+
			`"nameWithOwner":"owner/repository"}`),
		directCPUAdmission,
	)
	if err == nil || err.Error() != "JSON object contains a duplicate recognized field" {
		t.Fatalf("wrong-parent duplicate error = %v", err)
	}
}

func TestMatchedScalarTypeErrorsAndDuplicateOrderRemainExact(t *testing.T) {
	_, err := decodeRepositoryJSON(
		t.Context(), []byte(`{"nameWithOwner":123}`), directCPUAdmission,
	)
	if err == nil || err.Error() != "JSON field is not a string" {
		t.Fatalf("matched wrong-type error = %v", err)
	}
	_, err = decodeRepositoryJSON(
		t.Context(),
		[]byte(`{"nameWithOwner":"owner/repository","nameWithOwner":123}`),
		directCPUAdmission,
	)
	if err == nil || err.Error() != "JSON object contains a duplicate recognized field" {
		t.Fatalf("duplicate-before-type error = %v", err)
	}
}

func TestPendingMutationDynamicIDZeroAliasStillRoutes(t *testing.T) {
	err := validatePendingMutationResponse(
		t.Context(),
		[]byte(`{"data":{"m0":{"clientMutationId":"client-0"}},"errors":[]}`),
		[]pendingMutationAlias{{name: "m0", client: "client-0"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if id, recognized := recognizedJSONField("m0"); recognized || id != 0 {
		t.Fatalf("dynamic alias unexpectedly mapped to static ID %d, %t", id, recognized)
	}
}

func TestJSONPathCleanupAfterRoutingFailures(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		ledger, scope, baseline := newJSONPathTestScope(t)
		var path jsonPathTracker
		var duplicates jsonDuplicateGuard
		if err := path.start(jsonObjectStart, 0); err != nil {
			t.Fatal(err)
		}
		duplicates.startObject(path.depth)
		key := jsonFrame{Kind: jsonObjectKey, Raw: []byte(`"id"`), DecodedBytes: 2}
		if err := path.keyFrame(scope, key); err != nil {
			t.Fatal(err)
		}
		if err := duplicates.key(&path); err != nil {
			t.Fatal(err)
		}
		if err := path.scalar(func(jsonPathValue) error { return nil }); err != nil {
			t.Fatal(err)
		}
		escaped := jsonFrame{Kind: jsonObjectKey, Raw: []byte(`"\u0069d"`), DecodedBytes: 2}
		if err := path.keyFrame(scope, escaped); err != nil {
			t.Fatal(err)
		}
		if err := duplicates.key(&path); err == nil {
			t.Fatal("duplicate key was accepted")
		}
		assertJSONPathReleased(t, &path, scope, ledger, baseline)
	})

	t.Run("depth start", func(t *testing.T) {
		ledger, scope, baseline := newJSONPathTestScope(t)
		var path jsonPathTracker
		for range maxGitHubJSONDepth {
			if err := path.start(jsonObjectStart, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := path.keyFrame(scope, jsonFrame{
			Kind: jsonObjectKey, Raw: []byte(`"overflow"`), DecodedBytes: 8,
		}); err != nil {
			t.Fatal(err)
		}
		if err := path.start(jsonObjectStart, 0); err == nil {
			t.Fatal("over-depth start was accepted")
		}
		assertJSONPathReleased(t, &path, scope, ledger, baseline)
	})

	t.Run("wrong delimiter", func(t *testing.T) {
		ledger, scope, baseline := newJSONPathTestScope(t)
		var path jsonPathTracker
		if err := path.start(jsonObjectStart, 0); err != nil {
			t.Fatal(err)
		}
		if err := path.keyFrame(scope, jsonFrame{
			Kind: jsonObjectKey, Raw: []byte(`"child"`), DecodedBytes: 5,
		}); err != nil {
			t.Fatal(err)
		}
		if err := path.start(jsonObjectStart, 0); err != nil {
			t.Fatal(err)
		}
		if err := path.end(jsonArrayEnd); err == nil {
			t.Fatal("wrong delimiter was accepted")
		}
		assertJSONPathReleased(t, &path, scope, ledger, baseline)
	})

	t.Run("scalar visitor", func(t *testing.T) {
		ledger, scope, baseline := newJSONPathTestScope(t)
		var path jsonPathTracker
		if err := path.start(jsonObjectStart, 0); err != nil {
			t.Fatal(err)
		}
		if err := path.keyFrame(scope, jsonFrame{
			Kind: jsonObjectKey, Raw: []byte(`"unknown"`), DecodedBytes: 7,
		}); err != nil {
			t.Fatal(err)
		}
		visitorErr := errors.New("stop scalar")
		if err := path.scalar(func(jsonPathValue) error { return visitorErr }); !errors.Is(err, visitorErr) {
			t.Fatalf("scalar visitor error = %v", err)
		}
		assertJSONPathReleased(t, &path, scope, ledger, baseline)
	})
}

func newJSONPathTestScope(t *testing.T) (*sourceLedger, *jsonDecodeScope, int64) {
	t.Helper()
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		t.Fatal(err)
	}
	return ledger, scope, ledger.used
}

func assertJSONPathReleased(
	t *testing.T,
	path *jsonPathTracker,
	scope *jsonDecodeScope,
	ledger *sourceLedger,
	baseline int64,
) {
	t.Helper()
	path.close()
	if ledger.used != baseline {
		t.Fatalf("path retained %d bytes after close", ledger.used-baseline)
	}
	scope.Close()
	if ledger.used != 0 {
		t.Fatalf("scope retained %d bytes after close", ledger.used)
	}
}

func TestJSONPathValueTransfersDecodedIDAndNamedState(t *testing.T) {
	ledger, scope, baseline := newJSONPathTestScope(t)
	var path jsonPathTracker
	if err := path.start(jsonObjectStart, 0); err != nil {
		t.Fatal(err)
	}
	frame := jsonFrame{Kind: jsonObjectKey, Raw: []byte(`"\u0069d"`), DecodedBytes: 2}
	if err := path.keyFrame(scope, frame); err != nil {
		t.Fatal(err)
	}
	if err := path.scalar(func(value jsonPathValue) error {
		if value.name != "id" || value.fieldID != jsonFieldIDID || !value.named {
			t.Fatalf("path value = %+v", value)
		}
		if ledger.used != baseline+2 {
			t.Fatalf("visitor key ownership = %d, want %d", ledger.used, baseline+2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ledger.used != baseline {
		t.Fatalf("scalar retained %d key bytes", ledger.used-baseline)
	}
	assertJSONPathReleased(t, &path, scope, ledger, baseline)
}

func TestJSONPathUnknownEmptyNameIsNamedBarrier(t *testing.T) {
	ledger, scope, baseline := newJSONPathTestScope(t)
	var path jsonPathTracker
	if err := path.start(jsonObjectStart, 0); err != nil {
		t.Fatal(err)
	}
	if err := path.keyFrame(scope, jsonFrame{
		Kind: jsonObjectKey, Raw: []byte(`""`), DecodedBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := path.start(jsonObjectStart, 0); err != nil {
		t.Fatal(err)
	}
	if path.parents() {
		t.Fatal("named empty wrapper was skipped by numeric routing")
	}
	id, found := path.namedAncestor(0)
	if !found || id != 0 {
		t.Fatalf("empty wrapper ancestor = %d, %t", id, found)
	}
	assertJSONPathReleased(t, &path, scope, ledger, baseline)
}

func TestWrongParentWrongTypeDoesNotPrecedeMatchedRoute(t *testing.T) {
	encoded := `{"unknown":{"nameWithOwner":false},"nameWithOwner":"owner/repository"}`
	got, err := decodeRepositoryJSON(t.Context(), []byte(encoded), directCPUAdmission)
	if err != nil || !strings.Contains(got, "/") {
		t.Fatalf("repository = %q, error = %v", got, err)
	}
}
