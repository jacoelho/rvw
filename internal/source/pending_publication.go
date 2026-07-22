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
	"github.com/jacoelho/rvw/internal/subprocess"
)

const (
	pendingMutationResponseBytes = 1 << 20
	updatePullRequestReviewInput = "UpdatePullRequestReviewInput"
	updatePullRequestReviewField = "updatePullRequestReview"
)

type pendingWireEffect struct {
	input    map[string]any
	field    string
	typeName string
	desired  string
	mutation review.PendingMutation
}

type pendingMutationEnvelope struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []json.RawMessage          `json:"errors"`
}

type pendingMutationAlias struct {
	name   string
	client string
}

type pendingEffectState uint8

const (
	pendingEffectCommitted pendingEffectState = iota + 1
	pendingEffectAbsent
	pendingEffectUnknown
)

func (service *Service) publishPendingReview(
	ctx context.Context,
	checked *review.CheckedPublication,
) PublicationCommit {
	prepared, ok := checked.Prepared().PendingPublication()
	if !ok {
		return PublicationCommit{
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail("checked pending review is unavailable"),
		}
	}
	baseline := prepared.Baseline()
	effects, err := pendingWireEffects(prepared, checked.Marker())
	if err != nil {
		return PublicationCommit{
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(err.Error()),
		}
	}
	for start := 0; start < len(effects); {
		end := pendingBatchEnd(effects, start)
		if end == start {
			return PublicationCommit{
				Disposition: review.PublicationDispositionFailedBeforeCommit,
				Detail: boundedOperationDetail(fmt.Sprintf(
					"pending review mutation exceeds %d bytes",
					review.MaxPendingMutationBatchBytes,
				)),
			}
		}
		committed, disposition, detail := service.executePendingBatch(
			ctx,
			checked.Target(),
			baseline,
			effects[start:end],
			true,
		)
		if !committed {
			return PublicationCommit{Disposition: disposition, Detail: detail}
		}
		start = end
	}
	if !prepared.Submit() {
		commit := PublicationCommit{
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: baseline.DatabaseID().Uint64(),
		}
		fresh, captureErr := service.capturePendingReview(ctx, checked.Target(), baseline)
		if captureErr == nil {
			commit.Pending = new(fresh)
		} else {
			commit.Detail = boundedOperationDetail(
				"draft synchronized; refresh pending review: " + captureErr.Error(),
			)
		}
		return commit
	}
	return service.submitPendingReview(ctx, checked, prepared)
}

func (service *Service) capturePendingReview(
	ctx context.Context,
	target review.PublicationTarget,
	baseline review.PendingReview,
) (review.PendingReview, error) {
	details, err := publicationTargetDetails(target)
	if err != nil {
		return review.PendingReview{}, err
	}
	metadata, err := service.pullRequestMetadata(ctx, details.Repository, details.Number)
	if err != nil {
		return review.PendingReview{}, err
	}
	details, err = validatePullRequestPayload(details.Repository, details.Number, metadata)
	if err != nil {
		return review.PendingReview{}, err
	}
	fresh, status, detail := service.loadPendingReview(ctx, details)
	if status != review.PendingReviewReady {
		message := "pending review capture is " + status.String()
		if !detail.IsEmpty() {
			message += ": " + detail.String()
		}
		return review.PendingReview{}, errors.New(message)
	}
	if fresh.NodeID() != baseline.NodeID() || fresh.DatabaseID() != baseline.DatabaseID() {
		return review.PendingReview{}, errors.New("pending review identity changed after synchronization")
	}
	return fresh, nil
}

func pendingWireEffects(
	prepared review.PreparedPendingPublication,
	attempt review.PublicationMarker,
) ([]pendingWireEffect, error) {
	baseline := prepared.Baseline()
	effects := make([]pendingWireEffect, 0, prepared.MutationCount())
	for start := 0; start < prepared.MutationCount(); start += review.MaxPendingMutationBatch {
		mutations := prepared.Mutations(
			start,
			min(review.MaxPendingMutationBatch, prepared.MutationCount()-start),
		)
		if len(mutations) == 0 {
			return nil, errors.New("pending review mutation plan is incomplete")
		}
		for _, mutation := range mutations {
			effect, err := pendingWireEffectFor(baseline, mutation, attempt)
			if err != nil {
				return nil, err
			}
			effects = append(effects, effect)
		}
	}
	return effects, nil
}

func pendingWireEffectFor(
	baseline review.PendingReview,
	mutation review.PendingMutation,
	attempt review.PublicationMarker,
) (pendingWireEffect, error) {
	desired := mutation.RemoteBody(baseline.NodeID(), attempt)
	clientMutationID := strconv.Itoa(int(mutation.Kind())) + ":" +
		strconv.FormatUint(uint64(mutation.LocalCommentID()), 10) + ":" +
		attempt.String()
	effect := pendingWireEffect{mutation: mutation, desired: desired}
	switch mutation.Kind() {
	case review.PendingMutationReviewBody:
		effect.typeName = updatePullRequestReviewInput
		effect.field = updatePullRequestReviewField
		effect.input = map[string]any{
			graphqlReviewID: baseline.NodeID().String(),
			graphqlBody:     desired,
			graphqlClientID: clientMutationID,
		}
	case review.PendingMutationUpdateComment:
		effect.typeName = "UpdatePullRequestReviewCommentInput"
		effect.field = "updatePullRequestReviewComment"
		effect.input = map[string]any{
			"pullRequestReviewCommentId": mutation.NodeID().String(),
			graphqlBody:                  desired,
			graphqlClientID:              clientMutationID,
		}
	case review.PendingMutationDeleteComment:
		effect.typeName = "DeletePullRequestReviewCommentInput"
		effect.field = "deletePullRequestReviewComment"
		effect.input = map[string]any{
			"id":            mutation.NodeID().String(),
			graphqlClientID: clientMutationID,
		}
	case review.PendingMutationAddComment:
		effect.typeName = "AddPullRequestReviewThreadInput"
		effect.field = "addPullRequestReviewThread"
		input, err := pendingAddThreadInput(baseline, mutation, desired, clientMutationID)
		if err != nil {
			return pendingWireEffect{}, err
		}
		effect.input = input
	default:
		return pendingWireEffect{}, errors.New("pending review mutation kind is invalid")
	}
	return effect, nil
}

func pendingAddThreadInput(
	baseline review.PendingReview,
	mutation review.PendingMutation,
	desired, clientMutationID string,
) (map[string]any, error) {
	target := mutation.Target()
	path, ok := target.Path().UTF8()
	if !ok || path == "" {
		return nil, errors.New("pending review addition path is not valid UTF-8")
	}
	input := map[string]any{
		graphqlReviewID: baseline.NodeID().String(),
		graphqlPath:     path,
		graphqlBody:     desired,
		graphqlClientID: clientMutationID,
	}
	switch target.Subject() {
	case review.PendingSubjectFile:
		input["subjectType"] = "FILE"
	case review.PendingSubjectLine:
		startSide, start, side, line := target.Lines()
		if line == nil {
			return nil, errors.New("pending review line addition has no line")
		}
		input["line"] = line.Uint32()
		input["side"] = githubLineSide(side)
		if start != nil {
			input["startLine"] = start.Uint32()
			input["startSide"] = githubLineSide(startSide)
		}
	default:
		return nil, errors.New("pending review addition target is unsupported")
	}
	return input, nil
}

func githubLineSide(side review.LineSide) string {
	if side == review.LineOld {
		return githubSideLeft
	}
	return githubSideRight
}

func pendingBatchEnd(effects []pendingWireEffect, start int) int {
	end := start
	for end < len(effects) && end-start < review.MaxPendingMutationBatch {
		_, encoded, err := encodePendingMutationBatch(effects[start : end+1])
		if err != nil || len(encoded) > review.MaxPendingMutationBatchBytes {
			break
		}
		end++
	}
	return end
}

func encodePendingMutationBatch(
	effects []pendingWireEffect,
) ([]pendingMutationAlias, []byte, error) {
	if len(effects) == 0 || len(effects) > review.MaxPendingMutationBatch {
		return nil, nil, errors.New("pending mutation batch size is invalid")
	}
	aliases := make([]pendingMutationAlias, len(effects))
	variables := make(map[string]any, len(effects))
	var declaration strings.Builder
	var selection strings.Builder
	declaration.WriteString("mutation(")
	selection.WriteByte('{')
	for index, effect := range effects {
		variable := "v" + strconv.Itoa(index)
		alias := "m" + strconv.Itoa(index)
		clientMutationID, ok := effect.input[graphqlClientID].(string)
		if !ok || clientMutationID == "" {
			return nil, nil, errors.New("pending mutation has no client identity")
		}
		aliases[index] = pendingMutationAlias{name: alias, client: clientMutationID}
		if index != 0 {
			declaration.WriteByte(',')
		}
		declaration.WriteByte('$')
		declaration.WriteString(variable)
		declaration.WriteByte(':')
		declaration.WriteString(effect.typeName)
		declaration.WriteByte('!')
		selection.WriteString(alias)
		selection.WriteByte(':')
		selection.WriteString(effect.field)
		selection.WriteString("(input:$")
		selection.WriteString(variable)
		selection.WriteString("){clientMutationId}")
		variables[variable] = effect.input
	}
	declaration.WriteByte(')')
	selection.WriteByte('}')
	payload, err := json.Marshal(struct {
		Variables map[string]any `json:"variables"`
		Query     string         `json:"query"`
	}{
		Query:     declaration.String() + selection.String(),
		Variables: variables,
	})
	if err != nil {
		return nil, nil, err
	}
	return aliases, payload, nil
}

func (service *Service) executePendingBatch(
	ctx context.Context,
	target review.PublicationTarget,
	baseline review.PendingReview,
	effects []pendingWireEffect,
	allowRetry bool,
) (bool, review.PublicationDisposition, review.OperationDetail) {
	aliases, payload, err := encodePendingMutationBatch(effects)
	if err != nil || len(payload) > review.MaxPendingMutationBatchBytes {
		if err == nil {
			err = errors.New("pending mutation batch exceeds its byte limit")
		}
		return false, review.PublicationDispositionFailedBeforeCommit,
			boundedOperationDetail(err.Error())
	}
	command, runErr := service.runGraphQLMutation(ctx, payload)
	if runErr == nil {
		runErr = validatePendingMutationResponse(command.Stdout, aliases)
	}
	if runErr == nil {
		return true, 0, review.EmptyOperationDetail()
	}
	if !command.Started {
		return false, review.PublicationDispositionFailedBeforeCommit,
			boundedOperationDetail(runErr.Error())
	}
	states, reconcileErr := service.reconcilePendingEffects(ctx, target, baseline, effects)
	if reconcileErr != nil {
		return false, review.PublicationDispositionUnknown,
			boundedOperationDetail(errors.Join(runErr, reconcileErr).Error())
	}
	retry := make([]pendingWireEffect, 0, len(effects))
	for index, state := range states {
		switch state {
		case pendingEffectCommitted:
		case pendingEffectAbsent:
			retry = append(retry, effects[index])
		case pendingEffectUnknown:
			return false, review.PublicationDispositionUnknown,
				boundedOperationDetail("pending review mutation outcome is unknown")
		}
	}
	if len(retry) == 0 {
		return true, 0, review.EmptyOperationDetail()
	}
	if !allowRetry {
		return false, review.PublicationDispositionFailedBeforeCommit,
			boundedOperationDetail("pending review mutation was confirmed absent after retry")
	}
	return service.executePendingBatch(ctx, target, baseline, retry, false)
}

func (service *Service) runGraphQLMutation(
	ctx context.Context,
	payload []byte,
) (subprocess.Result, error) {
	return service.runner.Run(ctx, subprocess.Request{
		Name:            "gh",
		Args:            []string{ghAPI, ghGraphQL, "--input", "-"},
		Dir:             service.cwd.String(),
		Env:             map[string]string{localeAllKey: "C", localeLanguageKey: "C", ghPromptDisabled: "1"},
		Stdin:           payload,
		StdoutLimit:     pendingMutationResponseBytes,
		StderrLimit:     pendingMutationResponseBytes,
		OperationDetail: "gh api pending review mutation",
	})
}

func validatePendingMutationResponse(encoded []byte, aliases []pendingMutationAlias) error {
	if !utf8.Valid(encoded) {
		return errors.New("GitHub pending mutation response is not valid UTF-8")
	}
	var envelope pendingMutationEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return fmt.Errorf("decode GitHub pending mutation response: %w", err)
	}
	if len(envelope.Errors) != 0 || envelope.Data == nil {
		return errors.New("GitHub pending mutation response contains errors")
	}
	for _, alias := range aliases {
		raw, exists := envelope.Data[alias.name]
		if !exists || string(raw) == graphqlNull {
			return errors.New("GitHub pending mutation response is incomplete")
		}
		var result struct {
			ClientMutationID string `json:"clientMutationId"`
		}
		if err := json.Unmarshal(raw, &result); err != nil ||
			result.ClientMutationID != alias.client {
			return errors.New("GitHub pending mutation response has no client identity")
		}
	}
	return nil
}

func (service *Service) reconcilePendingEffects(
	ctx context.Context,
	target review.PublicationTarget,
	baseline review.PendingReview,
	effects []pendingWireEffect,
) ([]pendingEffectState, error) {
	details, err := publicationTargetDetails(target)
	if err != nil {
		return nil, err
	}
	metadata, err := service.pullRequestMetadata(ctx, details.Repository, details.Number)
	if err != nil {
		return nil, err
	}
	details, err = validatePullRequestPayload(details.Repository, details.Number, metadata)
	if err != nil {
		return nil, err
	}
	fresh, status, detail := service.loadPendingReview(ctx, details)
	if status != review.PendingReviewReady {
		message := "pending review reconciliation is " + status.String()
		if !detail.IsEmpty() {
			message += ": " + detail.String()
		}
		return nil, errors.New(message)
	}
	if fresh.NodeID() != baseline.NodeID() || fresh.DatabaseID() != baseline.DatabaseID() {
		return nil, errors.New("pending review identity changed during reconciliation")
	}
	comments := make(map[review.CommentNodeID]review.PendingComment, fresh.CommentCount())
	all := fresh.Comments(0, fresh.CommentCount())
	for _, comment := range all {
		comments[comment.NodeID()] = comment
	}
	states := make([]pendingEffectState, len(effects))
	for index, effect := range effects {
		mutation := effect.mutation
		switch mutation.Kind() {
		case review.PendingMutationReviewBody:
			states[index] = classifyPendingBody(fresh.Body(), effect.desired, mutation.Baseline())
		case review.PendingMutationUpdateComment:
			comment, exists := comments[mutation.NodeID()]
			if !exists {
				states[index] = pendingEffectUnknown
			} else {
				states[index] = classifyPendingBody(
					comment.Body(),
					effect.desired,
					mutation.Baseline(),
				)
			}
		case review.PendingMutationDeleteComment:
			comment, exists := comments[mutation.NodeID()]
			if !exists {
				states[index] = pendingEffectCommitted
			} else {
				visible := stripPendingWireMarker(comment.Body(), "<!-- rvw-draft-comment:v1:")
				if visible == mutation.Baseline() {
					states[index] = pendingEffectAbsent
				} else {
					states[index] = pendingEffectUnknown
				}
			}
		case review.PendingMutationAddComment:
			matches := 0
			for _, comment := range all {
				if comment.Body() == effect.desired &&
					pendingAdditionTargetsEqual(comment.Target(), mutation.Target()) {
					matches++
				}
			}
			switch matches {
			case 0:
				states[index] = pendingEffectAbsent
			case 1:
				states[index] = pendingEffectCommitted
			default:
				states[index] = pendingEffectUnknown
			}
		}
	}
	return states, nil
}

func pendingAdditionTargetsEqual(left, right review.PendingCommentTarget) bool {
	if left.Subject() != right.Subject() || left.Outdated() ||
		!bytes.Equal(left.Path().Raw(), right.Path().Raw()) {
		return false
	}
	leftStartSide, leftStart, leftSide, leftLine := left.Lines()
	rightStartSide, rightStart, rightSide, rightLine := right.Lines()
	return leftStartSide == rightStartSide && leftSide == rightSide &&
		pendingLineNumbersEqual(leftStart, rightStart) &&
		pendingLineNumbersEqual(leftLine, rightLine)
}

func pendingLineNumbersEqual(left, right *review.LineNumber) bool {
	return left == nil && right == nil ||
		left != nil && right != nil && left.Uint32() == right.Uint32()
}

func classifyPendingBody(raw, desired, baseline string) pendingEffectState {
	if raw == desired {
		return pendingEffectCommitted
	}
	visible := stripKnownPendingWireMarker(raw)
	if visible == stripKnownPendingWireMarker(desired) {
		return pendingEffectCommitted
	}
	if visible == baseline {
		return pendingEffectAbsent
	}
	return pendingEffectUnknown
}

func stripKnownPendingWireMarker(body string) string {
	visible := stripPendingWireMarker(body, "<!-- rvw-draft-comment:v1:")
	if visible == body {
		visible = stripPendingWireMarker(body, "<!-- rvw-publication:v1:")
	}
	return visible
}

func stripPendingWireMarker(body, prefix string) string {
	const suffix = " -->"
	markerSize := len(prefix) + 32 + len(suffix)
	if len(body) < markerSize {
		return body
	}
	marker := body[len(body)-markerSize:]
	if !strings.HasPrefix(marker, prefix) || !strings.HasSuffix(marker, suffix) {
		return body
	}
	for _, value := range marker[len(prefix) : len(prefix)+32] {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return body
		}
	}
	return strings.TrimSuffix(body[:len(body)-markerSize], "\n\n")
}

func publicationTargetDetails(target review.PublicationTarget) (PullRequestDetails, error) {
	repository, err := NewOwnerRepository(target.Repository())
	if err != nil {
		return PullRequestDetails{}, err
	}
	return PullRequestDetails{
		Repository: repository,
		Number:     PullRequestNumber(target.PullRequest()),
		BaseOID:    target.Base(),
		HeadOID:    target.Head(),
	}, nil
}

func (service *Service) submitPendingReview(
	ctx context.Context,
	checked *review.CheckedPublication,
	prepared review.PreparedPendingPublication,
) PublicationCommit {
	baseline := prepared.Baseline()
	input := map[string]any{
		graphqlReviewID: baseline.NodeID().String(),
		"event":         checked.Prepared().Options().Event().GitHubValue(),
		graphqlClientID: checked.Marker().String(),
	}
	effect := pendingWireEffect{
		field:    "submitPullRequestReview",
		typeName: "SubmitPullRequestReviewInput",
		input:    input,
	}
	aliases, payload, err := encodePendingMutationBatch([]pendingWireEffect{effect})
	if err != nil {
		return PublicationCommit{
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(err.Error()),
		}
	}
	command, runErr := service.runGraphQLMutation(ctx, payload)
	if runErr == nil {
		runErr = validatePendingMutationResponse(command.Stdout, aliases)
	}
	if runErr == nil {
		return PublicationCommit{
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: baseline.DatabaseID().Uint64(),
		}
	}
	if !command.Started {
		return PublicationCommit{
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(runErr.Error()),
		}
	}
	committed, absent, reconcileErr := service.reconcilePendingSubmission(ctx, checked, prepared)
	if reconcileErr != nil || !committed && !absent {
		return PublicationCommit{
			Disposition: review.PublicationDispositionUnknown,
			Detail:      boundedOperationDetail(errors.Join(runErr, reconcileErr).Error()),
		}
	}
	if committed {
		return PublicationCommit{
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: baseline.DatabaseID().Uint64(),
		}
	}
	command, runErr = service.runGraphQLMutation(ctx, payload)
	if runErr == nil {
		runErr = validatePendingMutationResponse(command.Stdout, aliases)
	}
	if runErr == nil {
		return PublicationCommit{
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: baseline.DatabaseID().Uint64(),
		}
	}
	if !command.Started {
		return PublicationCommit{
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail(runErr.Error()),
		}
	}
	committed, absent, reconcileErr = service.reconcilePendingSubmission(ctx, checked, prepared)
	if committed {
		return PublicationCommit{
			Disposition:  review.PublicationDispositionCommitted,
			RemoteReview: baseline.DatabaseID().Uint64(),
		}
	}
	if reconcileErr != nil || !absent {
		return PublicationCommit{
			Disposition: review.PublicationDispositionUnknown,
			Detail:      boundedOperationDetail(errors.Join(runErr, reconcileErr).Error()),
		}
	}
	return PublicationCommit{
		Disposition: review.PublicationDispositionFailedBeforeCommit,
		Detail: boundedOperationDetail(
			errors.Join(runErr, errors.New("pending review submission was confirmed absent after retry")).Error(),
		),
	}
}

func (service *Service) reconcilePendingSubmission(
	ctx context.Context,
	checked *review.CheckedPublication,
	prepared review.PreparedPendingPublication,
) (committed, absent bool, returnErr error) {
	target := checked.Target()
	baseline := prepared.Baseline()
	endpoint := "repos/" + target.Repository() + "/pulls/" +
		strconv.FormatUint(target.PullRequest(), 10) + "/reviews/" +
		strconv.FormatUint(baseline.DatabaseID().Uint64(), 10)
	result, err := service.runGH(ctx, []string{ghAPI, endpoint}, pendingMutationResponseBytes)
	if err != nil {
		return false, false, err
	}
	if !utf8.Valid(result.Stdout) {
		return false, false, errors.New("GitHub pending review reconciliation is not valid UTF-8")
	}
	var response struct {
		Body   *string      `json:"body"`
		NodeID string       `json:"node_id"`
		State  string       `json:"state"`
		ID     githubUint64 `json:"id"`
	}
	if err := json.Unmarshal(result.Stdout, &response); err != nil || response.Body == nil ||
		uint64(response.ID) != baseline.DatabaseID().Uint64() ||
		response.NodeID != baseline.NodeID().String() {
		return false, false, errors.New("GitHub pending review reconciliation response is incomplete")
	}
	desired := prepared.DesiredBody()
	visible := stripPendingWireMarker(*response.Body, "<!-- rvw-publication:v1:")
	if visible != desired {
		return false, false, errors.New("GitHub pending review body changed during submission")
	}
	if response.State == githubPending {
		return false, true, nil
	}
	want := "COMMENTED"
	switch checked.Prepared().Options().Event() {
	case review.PublicationEventComment, review.PublicationEventDraft:
	case review.PublicationEventApprove:
		want = "APPROVED"
	case review.PublicationEventRequestChanges:
		want = "CHANGES_REQUESTED"
	}
	return response.State == want, false, nil
}
