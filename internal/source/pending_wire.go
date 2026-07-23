package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

func encodePendingMutationBatch(
	effects []pendingWireEffect,
) ([]pendingMutationAlias, string, error) {
	return encodePendingMutationBatchCancellable(nil, effects)
}

func encodePendingMutationBatchCancellable(
	cancelled func() error,
	effects []pendingWireEffect,
) ([]pendingMutationAlias, string, error) {
	if len(effects) == 0 || len(effects) > review.MaxPendingMutationBatch {
		return nil, "", errors.New("pending mutation batch size is invalid")
	}
	aliases := make([]pendingMutationAlias, len(effects))
	query, queryErr := pendingMutationQuery(cancelled, effects, aliases)
	if queryErr != nil {
		return nil, "", queryErr
	}
	payload, err := encodePendingPayloadCancellable(cancelled, query, func(builder *sourceJSONBuilder) error {
		return writePendingMutationVariables(builder, effects)
	})
	return aliases, payload, err
}

func encodePendingPayloadCancellable(
	cancelled func() error,
	query string,
	writeVariables func(*sourceJSONBuilder) error,
) (string, error) {
	builder, builderErr := newCancellableSourceJSONBuilder(
		review.MaxPendingMutationBatchBytes,
		cancelled,
	)
	if builderErr != nil {
		return "", builderErr
	}
	if err := builder.Syntax(`{"variables":{`); err != nil {
		return "", err
	}
	if err := writeVariables(builder); err != nil {
		return "", err
	}
	if err := builder.Syntax(`},"query":`); err != nil {
		return "", err
	}
	if err := writePendingJSONString(builder, query); err != nil {
		return "", err
	}
	if err := builder.Syntax("}"); err != nil {
		return "", err
	}
	return builder.Finish()
}

func pendingMutationQuery(
	cancelled func() error,
	effects []pendingWireEffect,
	aliases []pendingMutationAlias,
) (string, error) {
	if len(effects) == 0 || len(aliases) != len(effects) {
		return "", errors.New("pending mutation query inputs are incomplete")
	}
	builder, err := newCancellableSourceJSONBuilder(
		review.MaxPendingMutationBatchBytes,
		cancelled,
	)
	if err != nil {
		return "", err
	}
	if err := builder.Syntax("mutation("); err != nil {
		return "", err
	}
	for index, effect := range effects {
		variable := "v" + strconv.Itoa(index)
		alias := "m" + strconv.Itoa(index)
		if effect.clientMutationID == "" {
			return "", errors.New("pending mutation has no client identity")
		}
		operation, operationErr := pendingMutationOperationFor(effect.mutation.Kind())
		if operationErr != nil {
			return "", operationErr
		}
		aliases[index] = pendingMutationAlias{name: alias, client: effect.clientMutationID}
		if index != 0 {
			if err := builder.Syntax(","); err != nil {
				return "", err
			}
		}
		if err := builder.Syntax("$"); err != nil {
			return "", err
		}
		if err := builder.Syntax(variable); err != nil {
			return "", err
		}
		if err := builder.Syntax(":"); err != nil {
			return "", err
		}
		if err := builder.Syntax(operation.typeName); err != nil {
			return "", err
		}
		if err := builder.Syntax("!"); err != nil {
			return "", err
		}
	}
	if err := builder.Syntax("){"); err != nil {
		return "", err
	}
	for index, effect := range effects {
		operation, operationErr := pendingMutationOperationFor(effect.mutation.Kind())
		if operationErr != nil {
			return "", operationErr
		}
		variable := "v" + strconv.Itoa(index)
		alias := "m" + strconv.Itoa(index)
		for _, part := range [...]string{
			alias, ":", operation.field, "(input:$", variable, "){clientMutationId}",
		} {
			if err := builder.Syntax(part); err != nil {
				return "", err
			}
		}
	}
	if err := builder.Syntax("}"); err != nil {
		return "", err
	}
	return builder.Finish()
}

type pendingMutationOperationDescriptor struct {
	typeName string
	field    string
}

func pendingMutationOperationFor(
	kind review.PendingMutationKind,
) (pendingMutationOperationDescriptor, error) {
	switch kind {
	case review.PendingMutationReviewBody:
		return pendingMutationOperationDescriptor{
			typeName: updatePullRequestReviewInput,
			field:    updatePullRequestReviewField,
		}, nil
	case review.PendingMutationUpdateComment:
		return pendingMutationOperationDescriptor{
			typeName: "UpdatePullRequestReviewCommentInput",
			field:    "updatePullRequestReviewComment",
		}, nil
	case review.PendingMutationDeleteComment:
		return pendingMutationOperationDescriptor{
			typeName: "DeletePullRequestReviewCommentInput",
			field:    "deletePullRequestReviewComment",
		}, nil
	case review.PendingMutationAddComment:
		return pendingMutationOperationDescriptor{
			typeName: "AddPullRequestReviewThreadInput",
			field:    "addPullRequestReviewThread",
		}, nil
	default:
		return pendingMutationOperationDescriptor{}, errors.New("pending review mutation kind is invalid")
	}
}

func writePendingMutationVariables(
	builder *sourceJSONBuilder,
	effects []pendingWireEffect,
) error {
	for index, effect := range effects {
		if index != 0 {
			if err := builder.Syntax(","); err != nil {
				return err
			}
		}
		if err := writePendingJSONString(builder, "v"+strconv.Itoa(index)); err != nil {
			return err
		}
		if err := builder.Syntax(":{"); err != nil {
			return err
		}
		fields := pendingObjectWriter{builder: builder}
		if err := writePendingMutationInput(&fields, effect); err != nil {
			return err
		}
		if err := builder.Syntax("}"); err != nil {
			return err
		}
	}
	return nil
}

func writePendingMutationInput(fields *pendingObjectWriter, effect pendingWireEffect) error {
	mutation := effect.mutation
	switch mutation.Kind() {
	case review.PendingMutationReviewBody:
		return fields.Strings(
			graphqlReviewID, effect.reviewID,
			graphqlBody, effect.after,
			graphqlClientID, effect.clientMutationID,
		)
	case review.PendingMutationUpdateComment:
		return fields.Strings(
			"pullRequestReviewCommentId", mutation.NodeID().String(),
			graphqlBody, effect.after,
			graphqlClientID, effect.clientMutationID,
		)
	case review.PendingMutationDeleteComment:
		return fields.Strings(
			"id", mutation.NodeID().String(),
			graphqlClientID, effect.clientMutationID,
		)
	case review.PendingMutationAddComment:
		return writePendingAddThreadInput(fields, effect)
	default:
		return errors.New("pending review mutation kind is invalid")
	}
}

func writePendingAddThreadInput(fields *pendingObjectWriter, effect pendingWireEffect) error {
	target := effect.mutation.Target()
	path, ok := target.Path().UTF8()
	if !ok || path == "" {
		return errors.New("pending review addition path is not valid UTF-8")
	}
	if err := fields.Strings(
		graphqlReviewID, effect.reviewID,
		graphqlPath, path,
		graphqlBody, effect.after,
		graphqlClientID, effect.clientMutationID,
	); err != nil {
		return err
	}
	switch target.Subject() {
	case review.PendingSubjectFile:
		return fields.String("subjectType", "FILE")
	case review.PendingSubjectLine:
		startSide, start, side, line := target.Lines()
		if line == nil {
			return errors.New("pending review line addition has no line")
		}
		if err := fields.Uint32("line", line.Uint32()); err != nil {
			return err
		}
		if err := fields.String("side", githubLineSide(side)); err != nil {
			return err
		}
		if start == nil {
			return nil
		}
		if err := fields.Uint32(jsonFieldStartLine, start.Uint32()); err != nil {
			return err
		}
		return fields.String("startSide", githubLineSide(startSide))
	default:
		return errors.New("pending review addition target is unsupported")
	}
}

type pendingObjectWriter struct {
	builder *sourceJSONBuilder
	count   int
}

func (writer *pendingObjectWriter) Strings(fields ...string) error {
	if writer == nil || len(fields)%2 != 0 {
		return errors.New("pending mutation string fields are incomplete")
	}
	for len(fields) >= 2 {
		name, value := fields[0], fields[1]
		if err := writer.String(name, value); err != nil {
			return err
		}
		fields = fields[2:]
	}
	return nil
}

func (writer *pendingObjectWriter) String(name, value string) error {
	if err := writer.separator(); err != nil {
		return err
	}
	if err := writePendingJSONString(writer.builder, name); err != nil {
		return err
	}
	if err := writer.builder.Syntax(":"); err != nil {
		return err
	}
	return writePendingJSONString(writer.builder, value)
}

func (writer *pendingObjectWriter) Uint32(name string, value uint32) error {
	if err := writer.separator(); err != nil {
		return err
	}
	if err := writePendingJSONString(writer.builder, name); err != nil {
		return err
	}
	if err := writer.builder.Syntax(":"); err != nil {
		return err
	}
	return writer.builder.Uint64(uint64(value))
}

func (writer *pendingObjectWriter) separator() error {
	if writer == nil || writer.builder == nil {
		return errors.New("pending mutation object writer is unavailable")
	}
	if writer.count != 0 {
		if err := writer.builder.Syntax(","); err != nil {
			return err
		}
	}
	writer.count++
	return nil
}

func writePendingJSONString(builder *sourceJSONBuilder, value string) error {
	if builder == nil || builder.finished {
		return errors.New("JSON wire builder is unavailable")
	}
	encodedBytes, err := pendingJSONStringBytesCancellable(builder.checkCancellation, value)
	if err != nil {
		return err
	}
	if encodedBytes > builder.limit-int64(len(builder.output)) {
		return fmt.Errorf("JSON wire value exceeds %d bytes", builder.limit)
	}
	return builder.String(value)
}

func pendingJSONStringBytes(value string) (int64, error) {
	return pendingJSONStringBytesCancellable(nil, value)
}

func pendingJSONStringBytesCancellable(
	cancelled func() error,
	value string,
) (int64, error) {
	bytes := int64(2)
	nextCancellation := 0
	for offset := 0; offset < len(value); {
		if offset >= nextCancellation {
			if err := pendingWireCancellation(cancelled); err != nil {
				return 0, err
			}
			nextCancellation = offset + sourceBuilderCancellationBytes
		}
		character, size := utf8.DecodeRuneInString(value[offset:])
		if character == utf8.RuneError && size == 1 {
			return 0, errors.New("pending mutation string is not valid UTF-8")
		}
		switch character {
		case '\\', '"', '\b', '\f', '\n', '\r', '\t':
			bytes += 2
		case '<', '>', '&', '\u2028', '\u2029':
			bytes += 6
		default:
			if character < 0x20 {
				bytes += 6
			} else {
				bytes += int64(utf8.RuneLen(character))
			}
		}
		if bytes > review.MaxPendingMutationBatchBytes {
			return bytes, fmt.Errorf(
				"pending mutation string exceeds %d encoded bytes",
				review.MaxPendingMutationBatchBytes,
			)
		}
		offset += size
	}
	return bytes, pendingWireCancellation(cancelled)
}

func pendingWireCancellation(cancelled func() error) error {
	if cancelled == nil {
		return nil
	}
	return cancelled()
}

func (service *Service) runGraphQLMutation(
	ctx context.Context,
	payload string,
) (githubHTTPResult, error) {
	result, err := service.github.mutation(
		ctx, http.MethodPost, "/graphql", []byte(payload), pendingMutationResponseBytes,
	)
	if err == nil && result.status != http.StatusOK {
		err = &githubHTTPError{
			cause:  errors.New("GitHub GraphQL mutation returned an undocumented success status"),
			status: result.status, attempted: result.attempted,
		}
	}
	return result, err
}

func validatePendingMutationResponse(
	ctx context.Context,
	encoded []byte,
	aliases []pendingMutationAlias,
) error {
	collector, err := newPendingMutationResponseCollector(aliases)
	if err != nil {
		return err
	}
	ledger, err := newSourceLedger(int64(cap(encoded)))
	if err != nil {
		return err
	}
	metrics, err := inspectJSONWithCPU(
		ctx,
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		directCPUAdmission,
		ledger,
	)
	if err != nil {
		return fmt.Errorf("decode GitHub pending mutation response: %w", err)
	}
	defer metrics.release(ledger)
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return fmt.Errorf("decode GitHub pending mutation response: %w", err)
	}
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		scope.Close()
		return fmt.Errorf("decode GitHub pending mutation response: %w", err)
	}
	collector.scope = scope
	defer collector.close()
	if err := walkJSONFrames(
		ctx,
		bytes.NewReader(encoded),
		directCPUAdmission,
		ledger,
		metrics,
		collector.consume,
	); err != nil {
		return fmt.Errorf("decode GitHub pending mutation response: %w", err)
	}
	return collector.finish()
}

func encodePendingSubmission(
	input pendingSubmissionInput,
) ([]pendingMutationAlias, string, error) {
	return encodePendingSubmissionCancellable(nil, input)
}

func encodePendingSubmissionCancellable(
	cancelled func() error,
	input pendingSubmissionInput,
) ([]pendingMutationAlias, string, error) {
	if input.reviewID == "" || input.event == "" || input.clientMutationID == "" {
		return nil, "", errors.New("pending submission input is incomplete")
	}
	const query = "mutation($v0:SubmitPullRequestReviewInput!){" +
		"m0:submitPullRequestReview(input:$v0){clientMutationId}}"
	payload, err := encodePendingPayloadCancellable(cancelled, query, func(builder *sourceJSONBuilder) error {
		if err := writePendingJSONString(builder, "v0"); err != nil {
			return err
		}
		if err := builder.Syntax(":{"); err != nil {
			return err
		}
		fields := pendingObjectWriter{builder: builder}
		if err := fields.Strings(
			graphqlReviewID, input.reviewID,
			"event", input.event,
			graphqlClientID, input.clientMutationID,
		); err != nil {
			return err
		}
		return builder.Syntax("}")
	})
	if err != nil {
		return nil, "", err
	}
	return []pendingMutationAlias{{name: "m0", client: input.clientMutationID}}, payload, nil
}

func committedPendingSubmissionID(databaseID uint64) PublicationCommit {
	return PublicationCommit{
		Disposition:  review.PublicationDispositionCommitted,
		RemoteReview: databaseID,
	}
}

func unknownPendingSubmission(err error) PublicationCommit {
	return PublicationCommit{
		Disposition:  review.PublicationDispositionUnknown,
		PendingPhase: review.PendingPublicationSubmitting,
		Detail:       boundedOperationDetail(err.Error()),
	}
}

func (service *Service) acquirePendingSubmission(
	ctx context.Context,
	target review.PublicationTarget,
	databaseID uint64,
) ([]byte, error) {
	endpoint := "/repos/" + target.Repository() + "/pulls/" +
		strconv.FormatUint(target.PullRequest(), 10) + "/reviews/" +
		strconv.FormatUint(databaseID, 10)
	response, err := service.github.request(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.Join(
			fmt.Errorf("GitHub pending submission returned %s", response.Status),
			response.Body.Close(),
		)
	}
	body, readErr := readGitHubBody(response.Body, pendingMutationResponseBytes)
	return body, errors.Join(readErr, response.Body.Close())
}

type pendingSubmissionClassification uint8

const (
	pendingSubmissionCommitted pendingSubmissionClassification = iota + 1
	pendingSubmissionAbsent
)

const (
	pendingSubmissionBodyField pendingSubmissionField = iota + 1
	pendingSubmissionNodeIDField
	pendingSubmissionStateField
	pendingSubmissionDatabaseIDField
	pendingSubmissionFieldLimit
)

type pendingSubmissionField uint8

type pendingSubmissionPresence uint8

const (
	pendingSubmissionNodeIDKey = "node_id"
	pendingSubmissionStateKey  = "state"
)

type pendingSubmissionResponseCollector struct {
	body       string
	nodeID     string
	state      string
	scope      *jsonDecodeScope
	path       jsonPathTracker
	duplicates jsonDuplicateGuard
	databaseID uint64
	presence   pendingSubmissionPresence
	root       bool
}

func classifyPendingSubmissionResponse(
	ctx context.Context,
	encoded []byte,
	reviewID review.ReviewNodeID,
	databaseID uint64,
	expectedBody string,
	expectedState string,
) (pendingSubmissionClassification, error) {
	collector, err := decodePendingSubmissionResponse(ctx, encoded)
	if err != nil {
		return 0, err
	}
	defer collector.close()
	if collector.databaseID != databaseID || collector.nodeID != reviewID.String() {
		return 0, errors.New("GitHub pending review reconciliation response is incomplete")
	}
	if collector.body != expectedBody {
		return 0, errors.New("GitHub pending review body changed during submission")
	}
	if collector.state == githubPending {
		return pendingSubmissionAbsent, nil
	}
	if collector.state == expectedState {
		return pendingSubmissionCommitted, nil
	}
	return 0, errors.New("GitHub pending review submission state is ambiguous")
}

func decodePendingSubmissionResponse(
	ctx context.Context,
	encoded []byte,
) (pendingSubmissionResponseCollector, error) {
	ledger, err := newSourceLedger(int64(cap(encoded)))
	if err != nil {
		return pendingSubmissionResponseCollector{}, err
	}
	metrics, err := inspectJSONWithCPU(
		ctx,
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		directCPUAdmission,
		ledger,
	)
	if err != nil {
		return pendingSubmissionResponseCollector{}, err
	}
	defer metrics.release(ledger)
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return pendingSubmissionResponseCollector{}, err
	}
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		scope.Close()
		return pendingSubmissionResponseCollector{}, err
	}
	collector := pendingSubmissionResponseCollector{scope: scope}
	if err := walkJSONFrames(
		ctx,
		bytes.NewReader(encoded),
		directCPUAdmission,
		ledger,
		metrics,
		collector.consume,
	); err != nil {
		collector.close()
		return pendingSubmissionResponseCollector{}, err
	}
	if !collector.root || collector.path.depth != 0 ||
		!collector.presence.has(pendingSubmissionBodyField) ||
		!collector.presence.has(pendingSubmissionNodeIDField) ||
		!collector.presence.has(pendingSubmissionStateField) ||
		!collector.presence.has(pendingSubmissionDatabaseIDField) {
		collector.close()
		return pendingSubmissionResponseCollector{}, errors.New(
			"GitHub pending review reconciliation response is incomplete",
		)
	}
	return collector, nil
}

func (collector *pendingSubmissionResponseCollector) consume(frame jsonFrame) error {
	switch frame.Kind {
	case jsonObjectKey:
		if err := collector.path.keyFrame(collector.scope, frame); err != nil {
			return err
		}
		return collector.duplicates.key(&collector.path)
	case jsonObjectStart:
		if err := collector.path.start(frame.Kind, 0); err != nil {
			return err
		}
		collector.duplicates.startObject(collector.path.depth)
		if collector.path.depth == 1 {
			collector.root = true
		}
		if collector.recognizedContainer() {
			return errors.New("GitHub pending review reconciliation field is not a scalar")
		}
		return nil
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		if collector.path.depth == 1 || collector.recognizedContainer() {
			return errors.New("GitHub pending review reconciliation has an invalid structure")
		}
		return nil
	case jsonObjectEnd:
		collector.duplicates.endObject(collector.path.depth)
		return collector.path.end(frame.Kind)
	case jsonArrayEnd:
		return collector.path.end(frame.Kind)
	default:
		return collector.path.scalar(func(value jsonPathValue) error {
			if collector.path.depth != 1 {
				return nil
			}
			var err error
			switch value.name {
			case graphqlBody:
				err = collector.presence.add(pendingSubmissionBodyField)
				if err == nil {
					collector.body, err = collector.scope.string(frame)
				}
			case pendingSubmissionNodeIDKey:
				err = collector.presence.add(pendingSubmissionNodeIDField)
				if err == nil {
					collector.nodeID, err = collector.scope.string(frame)
				}
			case pendingSubmissionStateKey:
				err = collector.presence.add(pendingSubmissionStateField)
				if err == nil {
					collector.state, err = collector.scope.string(frame)
				}
			case "id":
				err = collector.presence.add(pendingSubmissionDatabaseIDField)
				if err == nil {
					var id githubUint64
					id, err = jsonGitHubUint64(frame)
					collector.databaseID = uint64(id)
				}
			}
			return err
		})
	}
}

func (collector *pendingSubmissionResponseCollector) close() {
	if collector == nil {
		return
	}
	collector.body = ""
	collector.nodeID = ""
	collector.state = ""
	collector.path.close()
	collector.scope.Close()
	collector.scope = nil
}

func (collector *pendingSubmissionResponseCollector) recognizedContainer() bool {
	if collector.path.depth != 2 {
		return false
	}
	name := collector.path.stack[1].name
	return name == graphqlBody || name == pendingSubmissionNodeIDKey ||
		name == pendingSubmissionStateKey || name == "id"
}

func (presence *pendingSubmissionPresence) add(field pendingSubmissionField) error {
	if field == 0 || field >= pendingSubmissionFieldLimit {
		return errors.New("GitHub pending review reconciliation field is invalid")
	}
	bit := pendingSubmissionPresence(1) << (field - 1)
	if *presence&bit != 0 {
		return errors.New("JSON object contains a duplicate recognized field")
	}
	*presence |= bit
	return nil
}

func (presence *pendingSubmissionPresence) has(field pendingSubmissionField) bool {
	if field == 0 || field >= pendingSubmissionFieldLimit {
		return false
	}
	return *presence&(pendingSubmissionPresence(1)<<(field-1)) != 0
}
