package source

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jacoelho/rvw/internal/review"
)

const (
	pendingMutationResponseBytes        = 1 << 20
	pendingExecutionMetadataBytes int64 = 1_024
	pendingBatchMetadataBytes     int64 = 256
	pendingEffectMetadataBytes    int64 = 512
	pendingAliasMetadataBytes     int64 = 64
	updatePullRequestReviewInput        = "UpdatePullRequestReviewInput"
	updatePullRequestReviewField        = "updatePullRequestReview"
)

type pendingEffect struct {
	before   string
	after    string
	marker   string
	mutation review.PendingMutation
}

type pendingWireEffect struct {
	reviewID         string
	clientMutationID string
	pendingEffect
}

type pendingSubmissionInput struct {
	reviewID         string
	event            string
	clientMutationID string
}

type pendingMutationAlias struct {
	name   string
	client string
}

type pendingMutationAliasPresence uint64

func (presence *pendingMutationAliasPresence) add(index int) error {
	if presence == nil || index < 0 || index >= review.MaxPendingMutationBatch {
		return errors.New("pending mutation response alias index is invalid")
	}
	bit := pendingMutationAliasPresence(1) << index
	if *presence&bit != 0 {
		return errors.New("GitHub pending mutation response repeats an alias field")
	}
	*presence |= bit
	return nil
}

func (presence *pendingMutationAliasPresence) has(index int) bool {
	if presence == nil || index < 0 || index >= review.MaxPendingMutationBatch {
		return false
	}
	return *presence&(pendingMutationAliasPresence(1)<<index) != 0
}

type pendingMutationResponseCollector struct {
	aliases          []pendingMutationAlias
	scope            *jsonDecodeScope
	path             jsonPathTracker
	duplicates       jsonDuplicateGuard
	aliasesSeen      pendingMutationAliasPresence
	clientsSeen      pendingMutationAliasPresence
	clientsConfirmed pendingMutationAliasPresence
	root             bool
	data             bool
}

func newPendingMutationResponseCollector(
	aliases []pendingMutationAlias,
) (pendingMutationResponseCollector, error) {
	if len(aliases) == 0 || len(aliases) > review.MaxPendingMutationBatch {
		return pendingMutationResponseCollector{}, errors.New(
			"pending mutation response aliases are unavailable",
		)
	}
	for index, alias := range aliases {
		if alias.name == "" || alias.client == "" {
			return pendingMutationResponseCollector{}, errors.New(
				"pending mutation response alias is incomplete",
			)
		}
		for prior := range index {
			if aliases[prior].name == alias.name {
				return pendingMutationResponseCollector{}, errors.New(
					"pending mutation response aliases are not unique",
				)
			}
		}
	}
	return pendingMutationResponseCollector{aliases: aliases}, nil
}

func (collector *pendingMutationResponseCollector) consume(frame jsonFrame) error {
	switch frame.Kind {
	case jsonObjectKey:
		if err := collector.path.keyFrame(collector.scope, frame); err != nil {
			return err
		}
		if err := collector.duplicates.key(&collector.path); err != nil {
			return err
		}
		return collector.recordDynamicKey()
	case jsonObjectStart:
		if err := collector.path.start(frame.Kind, 0); err != nil {
			return err
		}
		collector.duplicates.startObject(collector.path.depth)
		return collector.startObject()
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		if collector.path.suffix(jsonFieldErrors) {
			if frame.Count != 0 {
				return errors.New("GitHub pending mutation response contains errors")
			}
			return nil
		}
		if collector.path.depth == 1 || collector.path.suffix(jsonFieldData) ||
			collector.expectedAliasContainer() >= 0 {
			return errors.New("GitHub pending mutation response has an invalid structure")
		}
		return nil
	case jsonObjectEnd:
		collector.duplicates.endObject(collector.path.depth)
		return collector.path.end(frame.Kind)
	case jsonArrayEnd:
		return collector.path.end(frame.Kind)
	default:
		return collector.path.scalar(func(value jsonPathValue) error {
			return collector.scalar(value, frame)
		})
	}
}

func (collector *pendingMutationResponseCollector) recordDynamicKey() error {
	if collector.path.depth == 2 &&
		collector.path.stack[collector.path.depth-1].name == jsonFieldData {
		index := collector.aliasIndex(collector.path.stack[collector.path.depth-1].pendingKey)
		if index >= 0 {
			return collector.aliasesSeen.add(index)
		}
	}
	index := collector.expectedAliasContainer()
	if index >= 0 && collector.path.stack[collector.path.depth-1].pendingKey == graphqlClientID {
		return collector.clientsSeen.add(index)
	}
	return nil
}

func (collector *pendingMutationResponseCollector) startObject() error {
	switch {
	case collector.path.depth == 1:
		if collector.root {
			return errors.New("GitHub pending mutation response has multiple roots")
		}
		collector.root = true
	case collector.path.suffix(jsonFieldData):
		collector.data = true
	case collector.path.suffix(jsonFieldErrors):
		return errors.New("GitHub pending mutation response errors field is not an array")
	}
	return nil
}

func (collector *pendingMutationResponseCollector) scalar(value jsonPathValue, frame jsonFrame) error {
	switch {
	case collector.path.depth == 0:
		return errors.New("GitHub pending mutation response root is not an object")
	case collector.path.scalarSuffix(value, jsonFieldData):
		return errors.New("GitHub pending mutation response contains errors")
	case collector.path.scalarSuffix(value, jsonFieldErrors):
		if frame.Kind == jsonNull {
			return nil
		}
		return errors.New("GitHub pending mutation response errors field is not an array")
	}
	index := collector.expectedAliasScalar(value.name)
	if index >= 0 {
		return errors.New("GitHub pending mutation response is incomplete")
	}
	index = collector.expectedClientScalar(value.name)
	if index < 0 {
		return nil
	}
	client, err := collector.scope.string(frame)
	if err != nil {
		return errors.New("GitHub pending mutation response has no client identity")
	}
	confirmed := client == collector.aliases[index].client
	collector.scope.release(frame.DecodedBytes)
	if !confirmed {
		return errors.New("GitHub pending mutation response has no client identity")
	}
	if collector.clientsConfirmed.has(index) {
		return errors.New("GitHub pending mutation response repeats a client identity")
	}
	return collector.clientsConfirmed.add(index)
}

func (collector *pendingMutationResponseCollector) close() {
	if collector == nil {
		return
	}
	collector.path.close()
	collector.scope.Close()
	collector.scope = nil
}

func (collector *pendingMutationResponseCollector) finish() error {
	if !collector.root || !collector.data || collector.path.depth != 0 {
		return errors.New("GitHub pending mutation response contains errors")
	}
	for index := range collector.aliases {
		if !collector.aliasesSeen.has(index) || !collector.clientsSeen.has(index) ||
			!collector.clientsConfirmed.has(index) {
			return errors.New("GitHub pending mutation response is incomplete")
		}
	}
	return nil
}

func (collector *pendingMutationResponseCollector) aliasIndex(name string) int {
	for index, alias := range collector.aliases {
		if alias.name == name {
			return index
		}
	}
	return -1
}

func (collector *pendingMutationResponseCollector) expectedAliasContainer() int {
	if collector.path.depth != 3 || collector.path.stack[1].name != jsonFieldData {
		return -1
	}
	return collector.aliasIndex(collector.path.stack[2].name)
}

func (collector *pendingMutationResponseCollector) expectedAliasScalar(name string) int {
	if collector.path.depth != 2 || collector.path.stack[1].name != jsonFieldData {
		return -1
	}
	return collector.aliasIndex(name)
}

func (collector *pendingMutationResponseCollector) expectedClientScalar(name string) int {
	if name != graphqlClientID {
		return -1
	}
	return collector.expectedAliasContainer()
}

type pendingEffectState uint8

const (
	pendingEffectCommitted pendingEffectState = iota + 1
	pendingEffectAbsent
	pendingEffectUnknown
)

type pendingMutationBatch struct {
	payload string
	effects []pendingWireEffect
	aliases []pendingMutationAlias
}

func (batch pendingMutationBatch) retainedBytes() int64 {
	retained := pendingBatchMetadataBytes + int64(len(batch.payload)) +
		int64(cap(batch.effects))*pendingEffectMetadataBytes +
		int64(cap(batch.aliases))*pendingAliasMetadataBytes
	for _, effect := range batch.effects {
		retained += pendingWireEffectRetainedBytes(effect)
	}
	for _, alias := range batch.aliases {
		retained += int64(len(alias.name) + len(alias.client))
	}
	return retained
}

type pendingBatchSizer struct {
	variableBytes    int64
	declarationBytes int64
	selectionBytes   int64
	count            int
}

func newPendingBatchSizer() pendingBatchSizer {
	return pendingBatchSizer{
		declarationBytes: int64(len("mutation(")),
		selectionBytes:   1,
	}
}

func (sizer pendingBatchSizer) add(effect pendingWireEffect) (pendingBatchSizer, error) {
	index := sizer.count
	variableBytes, err := pendingMutationVariableBytes(index, effect)
	if err != nil {
		return pendingBatchSizer{}, err
	}
	digits := len(strconv.Itoa(index))
	operation, err := pendingMutationOperationFor(effect.mutation.Kind())
	if err != nil {
		return pendingBatchSizer{}, err
	}
	if index != 0 {
		sizer.variableBytes++
		sizer.declarationBytes++
	}
	sizer.variableBytes += variableBytes
	sizer.declarationBytes += int64(
		len("$v") + digits + len(":") + len(operation.typeName) + len("!"),
	)
	sizer.selectionBytes += int64(
		len("m") + digits + len(":") + len(operation.field) + len("(input:$v") + digits +
			len("){clientMutationId}"),
	)
	sizer.count++
	return sizer, nil
}

func (sizer pendingBatchSizer) payloadBytes() int64 {
	if sizer.count == 0 {
		return 0
	}
	queryBytes := sizer.declarationBytes + 1 + sizer.selectionBytes + 1
	return int64(len(`{"variables":{`)) + sizer.variableBytes +
		int64(len(`},"query":`)) + queryBytes + 2 + 1
}

// PendingPublicationExecutionConstructionBytes returns a conservative bound
// for prepared wire ownership and the largest one-batch encoding scratch.
func PendingPublicationExecutionConstructionBytes(
	ctx context.Context,
	checked *review.CheckedPublication,
) (int64, error) {
	prepared, ok := pendingPublicationInputs(checked)
	if !ok {
		return 0, errors.New("checked pending publication is unavailable")
	}
	retained := pendingExecutionMetadataBytes
	sizer := newPendingBatchSizer()
	batches := 0
	for index := range prepared.MutationCount() {
		if err := context.Cause(ctx); err != nil {
			return 0, err
		}
		mutation, exists := prepared.MutationAt(index)
		if !exists {
			return 0, errors.New("pending review mutation plan is incomplete")
		}
		effect, err := pendingWireEffectFor(
			prepared.BaselineNodeID(),
			prepared.BaselineBody(),
			mutation,
			checked.Marker(),
		)
		if err != nil {
			return 0, err
		}
		candidate, err := sizer.add(effect)
		if err != nil {
			return 0, err
		}
		if candidate.count > review.MaxPendingMutationBatch ||
			candidate.payloadBytes() > review.MaxPendingMutationBatchBytes {
			if sizer.count == 0 {
				return 0, fmt.Errorf(
					"pending review mutation exceeds %d bytes",
					review.MaxPendingMutationBatchBytes,
				)
			}
			retained += sizer.payloadBytes() + pendingBatchMetadataBytes
			batches++
			sizer = newPendingBatchSizer()
			candidate, err = sizer.add(effect)
			if err != nil || candidate.payloadBytes() > review.MaxPendingMutationBatchBytes {
				if err != nil {
					return 0, err
				}
				return 0, fmt.Errorf(
					"pending review mutation exceeds %d bytes",
					review.MaxPendingMutationBatchBytes,
				)
			}
		}
		sizer = candidate
		retained += pendingWireEffectRetainedBytes(effect) +
			pendingEffectMetadataBytes + pendingAliasMetadataBytes
	}
	if sizer.count != 0 {
		retained += sizer.payloadBytes() + pendingBatchMetadataBytes
		batches++
	}
	retained += int64(batches)*pendingBatchMetadataBytes +
		int64(len(checked.Target().Repository())+len(prepared.BaselineNodeID())+len(checked.Marker()))
	if prepared.Submit() {
		// Submission input is small, but reserve the protocol maximum so its
		// construction never precedes admission.
		retained += review.MaxPendingMutationBatchBytes + pendingBatchMetadataBytes +
			pendingAliasMetadataBytes
	}
	return retained + 3*review.MaxPendingMutationBatchBytes +
		2*pendingMutationResponseBytes + 4_096, nil
}

// PreparePendingPublicationExecution constructs every first-attempt payload
// once. The caller must reserve PendingPublicationExecutionConstructionBytes
// and hold a CPU permit while this runs.
func PreparePendingPublicationExecution(
	ctx context.Context,
	checked *review.CheckedPublication,
) (PendingPublicationExecution, error) {
	return preparePendingPublicationExecution(ctx, checked, encodePendingMutationBatchCancellable)
}

func preparePendingPublicationExecution(
	ctx context.Context,
	checked *review.CheckedPublication,
	encode func(func() error, []pendingWireEffect) ([]pendingMutationAlias, string, error),
) (PendingPublicationExecution, error) {
	prepared, ok := pendingPublicationInputs(checked)
	if !ok || encode == nil {
		return PendingPublicationExecution{}, errors.New("checked pending publication is unavailable")
	}
	state := &pendingPublicationExecutionState{
		target:         checked.Target(),
		reviewID:       prepared.BaselineNodeID(),
		marker:         checked.Marker(),
		databaseID:     prepared.BaselineDatabaseID().Uint64(),
		reconciliation: pendingPublicationNoReconciliation,
		progress:       pendingPublicationUncommitted,
		status:         pendingPublicationExecutionOpen,
	}
	execution := PendingPublicationExecution{state: state}
	current := make([]pendingWireEffect, 0, review.MaxPendingMutationBatch)
	sizer := newPendingBatchSizer()
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		aliases, payload, err := encode(func() error { return context.Cause(ctx) }, current)
		if err != nil || int64(len(payload)) != sizer.payloadBytes() {
			return errors.Join(err, errors.New("pending mutation sizing does not match encoding"))
		}
		state.batches = append(state.batches, pendingMutationBatch{
			effects: current,
			aliases: aliases,
			payload: payload,
		})
		current = make([]pendingWireEffect, 0, review.MaxPendingMutationBatch)
		sizer = newPendingBatchSizer()
		return nil
	}
	for index := range prepared.MutationCount() {
		if err := context.Cause(ctx); err != nil {
			return PendingPublicationExecution{}, err
		}
		mutation, exists := prepared.MutationAt(index)
		if !exists {
			return PendingPublicationExecution{}, errors.New("pending review mutation plan is incomplete")
		}
		effect, err := pendingWireEffectFor(
			prepared.BaselineNodeID(),
			prepared.BaselineBody(),
			mutation,
			checked.Marker(),
		)
		if err != nil {
			return PendingPublicationExecution{}, err
		}
		candidate, err := sizer.add(effect)
		if err != nil {
			return PendingPublicationExecution{}, err
		}
		if candidate.count > review.MaxPendingMutationBatch ||
			candidate.payloadBytes() > review.MaxPendingMutationBatchBytes {
			if flushErr := flush(); flushErr != nil {
				return PendingPublicationExecution{}, flushErr
			}
			candidate, err = sizer.add(effect)
			if err != nil || candidate.payloadBytes() > review.MaxPendingMutationBatchBytes {
				return PendingPublicationExecution{}, errors.Join(
					err,
					errors.New("pending review mutation exceeds its byte limit"),
				)
			}
		}
		current = append(current, effect)
		sizer = candidate
	}
	if err := flush(); err != nil {
		return PendingPublicationExecution{}, err
	}
	if prepared.Submit() {
		if len(state.batches) == 0 || len(state.batches[0].effects) == 0 ||
			state.batches[0].effects[0].mutation.Kind() != review.PendingMutationReviewBody {
			return PendingPublicationExecution{}, errors.New("pending review submission has no body effect")
		}
		state.expectedBody = state.batches[0].effects[0].after
		state.expectedState = pendingSubmissionState(checked.Prepared().Options().Event())
		aliases, payload, err := encodePendingSubmissionCancellable(
			func() error { return context.Cause(ctx) },
			pendingSubmissionInput{
				reviewID:         state.reviewID.String(),
				event:            checked.Prepared().Options().Event().GitHubValue(),
				clientMutationID: state.marker.String(),
			},
		)
		if err != nil {
			return PendingPublicationExecution{}, err
		}
		state.submission = pendingMutationBatch{aliases: aliases, payload: payload}
	}
	state.refreshRetainedBytes()
	return execution, nil
}

func pendingSubmissionState(event review.PublicationEvent) string {
	switch event {
	case review.PublicationEventApprove:
		return "APPROVED"
	case review.PublicationEventRequestChanges:
		return "CHANGES_REQUESTED"
	default:
		return "COMMENTED"
	}
}

func pendingPublicationInputs(
	checked *review.CheckedPublication,
) (review.PendingPublicationView, bool) {
	if checked == nil || !checked.Pending() || checked.PayloadSize() == 0 {
		return review.PendingPublicationView{}, false
	}
	prepared, ok := checked.Prepared().BorrowPendingPublication()
	if !ok || prepared.MutationCount() == 0 {
		return review.PendingPublicationView{}, false
	}
	if prepared.BaselineNodeID() == "" || prepared.BaselineDatabaseID().Uint64() == 0 {
		return review.PendingPublicationView{}, false
	}
	return prepared, true
}

func pendingWireEffectRetainedBytes(effect pendingWireEffect) int64 {
	target := effect.mutation.Target()
	path, _ := target.Path().UTF8()
	reply, _ := target.ReplyTo()
	return int64(
		len(effect.reviewID) + len(effect.clientMutationID) + len(effect.before) +
			len(effect.after) + len(effect.marker) + len(effect.mutation.NodeID()) +
			len(effect.mutation.Body()) + len(effect.mutation.Baseline()) +
			len(effect.mutation.Marker()) + len(path) + len(reply),
	)
}

func pendingMutationVariableBytes(index int, effect pendingWireEffect) (int64, error) {
	variable := "v" + strconv.Itoa(index)
	inputBytes, err := pendingMutationInputBytes(effect)
	if err != nil {
		return 0, err
	}
	nameBytes, err := pendingJSONStringBytes(variable)
	if err != nil {
		return 0, err
	}
	return nameBytes + int64(len(":{}")) + inputBytes, nil
}

func pendingMutationInputBytes(effect pendingWireEffect) (int64, error) {
	mutation := effect.mutation
	var fields [][2]string
	var uintFields [][2]uint32
	switch mutation.Kind() {
	case review.PendingMutationReviewBody:
		fields = [][2]string{
			{graphqlReviewID, effect.reviewID},
			{graphqlBody, effect.after},
			{graphqlClientID, effect.clientMutationID},
		}
	case review.PendingMutationUpdateComment:
		fields = [][2]string{
			{"pullRequestReviewCommentId", mutation.NodeID().String()},
			{graphqlBody, effect.after},
			{graphqlClientID, effect.clientMutationID},
		}
	case review.PendingMutationDeleteComment:
		fields = [][2]string{
			{"id", mutation.NodeID().String()},
			{graphqlClientID, effect.clientMutationID},
		}
	case review.PendingMutationAddComment:
		target := mutation.Target()
		path, ok := target.Path().UTF8()
		if !ok || path == "" {
			return 0, errors.New("pending review addition path is not valid UTF-8")
		}
		fields = [][2]string{
			{graphqlReviewID, effect.reviewID},
			{graphqlPath, path},
			{graphqlBody, effect.after},
			{graphqlClientID, effect.clientMutationID},
		}
		switch target.Subject() {
		case review.PendingSubjectFile:
			fields = append(fields, [2]string{"subjectType", "FILE"})
		case review.PendingSubjectLine:
			startSide, start, side, line := target.Lines()
			if line == nil {
				return 0, errors.New("pending review line addition has no line")
			}
			lineField, err := pendingUintField(fields, line.Uint32())
			if err != nil {
				return 0, err
			}
			uintFields = append(uintFields, lineField)
			fields = append(fields,
				[2]string{githubSubjectLine, ""},
				[2]string{"side", githubLineSide(side)},
			)
			if start != nil {
				startField, err := pendingUintField(fields, start.Uint32())
				if err != nil {
					return 0, err
				}
				uintFields = append(uintFields, startField)
				fields = append(fields,
					[2]string{jsonFieldStartLine, ""},
					[2]string{"startSide", githubLineSide(startSide)},
				)
			}
		default:
			return 0, errors.New("pending review addition target is unsupported")
		}
	default:
		return 0, errors.New("pending review mutation kind is invalid")
	}
	bytes := int64(0)
	for index, field := range fields {
		if index != 0 {
			bytes++
		}
		nameBytes, err := pendingJSONStringBytes(field[0])
		if err != nil {
			return 0, err
		}
		bytes += nameBytes + 1
		value, numeric := uint32(0), false
		for _, uintField := range uintFields {
			if int(uintField[0]) == index {
				value, numeric = uintField[1], true
				break
			}
		}
		if numeric {
			bytes += int64(len(strconv.FormatUint(uint64(value), 10)))
			continue
		}
		valueBytes, err := pendingJSONStringBytes(field[1])
		if err != nil {
			return 0, err
		}
		bytes += valueBytes
	}
	return bytes, nil
}

func pendingUintField(fields [][2]string, value uint32) ([2]uint32, error) {
	const maxPendingMutationFields = 8
	if len(fields) > maxPendingMutationFields {
		return [2]uint32{}, errors.New("pending review mutation has too many fields")
	}
	return [2]uint32{
		uint32(len(fields)), //nolint:gosec // len is bounded by maxPendingMutationFields.
		value,
	}, nil
}

func pendingWireEffectFor(
	reviewID review.ReviewNodeID,
	baselineBody string,
	mutation review.PendingMutation,
	attempt review.PublicationMarker,
) (pendingWireEffect, error) {
	effect, err := pendingEffectFor(reviewID, baselineBody, mutation, attempt)
	if err != nil {
		return pendingWireEffect{}, err
	}
	clientMutationID := strconv.Itoa(int(mutation.Kind())) + ":" +
		strconv.FormatUint(uint64(mutation.LocalCommentID()), 10) + ":" +
		attempt.String()
	wire := pendingWireEffect{
		reviewID: reviewID.String(), clientMutationID: clientMutationID,
		pendingEffect: effect,
	}
	switch mutation.Kind() {
	case review.PendingMutationReviewBody,
		review.PendingMutationUpdateComment,
		review.PendingMutationDeleteComment:
	case review.PendingMutationAddComment:
		if err := validatePendingAddThread(mutation); err != nil {
			return pendingWireEffect{}, err
		}
	default:
		return pendingWireEffect{}, errors.New("pending review mutation kind is invalid")
	}
	return wire, nil
}

func pendingEffectFor(
	reviewID review.ReviewNodeID,
	baselineBody string,
	mutation review.PendingMutation,
	attempt review.PublicationMarker,
) (pendingEffect, error) {
	effect := pendingEffect{
		mutation: mutation,
		after:    mutation.RemoteBody(reviewID, attempt),
	}
	switch mutation.Kind() {
	case review.PendingMutationReviewBody:
		effect.before = baselineBody
	case review.PendingMutationUpdateComment, review.PendingMutationDeleteComment:
		effect.before = mutation.BaselineRemoteBody()
	case review.PendingMutationAddComment:
		effect.marker = mutation.DraftMarker(reviewID)
		if effect.marker == "" {
			return pendingEffect{}, errors.New("pending review addition has no stable marker")
		}
	default:
		return pendingEffect{}, errors.New("pending review mutation kind is invalid")
	}
	return effect, nil
}

func validatePendingAddThread(mutation review.PendingMutation) error {
	target := mutation.Target()
	path, ok := target.Path().UTF8()
	if !ok || path == "" {
		return errors.New("pending review addition path is not valid UTF-8")
	}
	switch target.Subject() {
	case review.PendingSubjectFile:
	case review.PendingSubjectLine:
		_, _, _, line := target.Lines()
		if line == nil {
			return errors.New("pending review line addition has no line")
		}
	default:
		return errors.New("pending review addition target is unsupported")
	}
	return nil
}

func githubLineSide(side review.LineSide) string {
	if side == review.LineOld {
		return githubSideLeft
	}
	return githubSideRight
}
