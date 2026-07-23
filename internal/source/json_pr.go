package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

const (
	prMergedAtField pullRequestField = iota + 1
	prUserField
	prUserLoginField
	prBaseField
	prBaseRefField
	prBaseSHAField
	prHeadField
	prHeadRefField
	prHeadSHAField
	prTitleField
	prHTMLURLField
	prStateField
	prNumberField
	prChangedFilesField
	prDraftField
	prFieldLimit
)

const repositoryNameRoute repositoryScalarRoute = 1

type pullRequestField uint8

type pullRequestFieldPresence uint16

type repositoryScalarRoute uint8

type pullRequestJSONCollector struct {
	scope      *jsonDecodeScope
	payload    pullRequestPayload
	path       jsonPathTracker
	duplicates jsonDuplicateGuard
	presence   pullRequestFieldPresence
	root       bool
}

type repositoryJSONCollector struct {
	scope      *jsonDecodeScope
	value      string
	path       jsonPathTracker
	duplicates jsonDuplicateGuard
	presence   bool
	root       bool
}

func decodeRepositoryJSON(
	ctx context.Context,
	encoded []byte,
	runCPU CPUAdmission,
) (string, error) {
	ledger, err := newSourceLedger(int64(len(encoded)))
	if err != nil {
		return "", err
	}
	metrics, err := inspectJSONWithCPU(
		ctx,
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		runCPU,
		ledger,
	)
	if err != nil {
		return "", err
	}
	defer metrics.release(ledger)
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return "", err
	}
	defer scope.Close()
	if err := scope.reserve(jsonDecodedCollectorBytes); err != nil {
		return "", err
	}
	collector := repositoryJSONCollector{scope: scope}
	defer collector.path.close()
	if err := walkJSONFrames(
		ctx,
		bytes.NewReader(encoded),
		runCPU,
		ledger,
		metrics,
		collector.consume,
	); err != nil {
		return "", err
	}
	if !collector.root || !collector.presence || collector.path.depth != 0 {
		return "", errors.New("repository JSON is missing nameWithOwner")
	}
	return collector.value, nil
}

func (collector *repositoryJSONCollector) consume(frame jsonFrame) error {
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
		if err := collector.scope.reserve(jsonDecodedElementBytes); err != nil {
			return err
		}
		if collector.path.depth == 1 {
			collector.root = true
		}
		collector.duplicates.startObject(collector.path.depth)
		return nil
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		return reserveJSONDecodedSlice(collector.scope, frame.Count)
	case jsonObjectEnd:
		collector.duplicates.endObject(collector.path.depth)
		return collector.path.end(frame.Kind)
	case jsonArrayEnd:
		return collector.path.end(frame.Kind)
	default:
		return collector.path.scalar(func(value jsonPathValue) error {
			if collector.route(value) == 0 {
				return nil
			}
			collector.presence = true
			decoded, err := collector.scope.string(frame)
			collector.value = decoded
			return err
		})
	}
}

func decodePullRequestJSON(
	ctx context.Context,
	encoded []byte,
	runCPU CPUAdmission,
) (pullRequestPayload, error) {
	ledger, err := newSourceLedger(int64(len(encoded)))
	if err != nil {
		return pullRequestPayload{}, err
	}
	metrics, err := inspectJSONWithCPU(
		ctx,
		bytes.NewReader(encoded),
		maxGitHubJSONTokenBytes,
		runCPU,
		ledger,
	)
	if err != nil {
		return pullRequestPayload{}, err
	}
	defer metrics.release(ledger)
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		return pullRequestPayload{}, err
	}
	defer scope.Close()
	if reserveErr := scope.reserve(jsonDecodedCollectorBytes); reserveErr != nil {
		return pullRequestPayload{}, reserveErr
	}
	collector := pullRequestJSONCollector{scope: scope}
	defer collector.path.close()
	err = walkJSONFrames(
		ctx,
		bytes.NewReader(encoded),
		runCPU,
		ledger,
		metrics,
		collector.consume,
	)
	if err != nil {
		return pullRequestPayload{}, err
	}
	if err := collector.finish(); err != nil {
		return pullRequestPayload{}, err
	}
	return collector.payload, nil
}

func (collector *pullRequestJSONCollector) consume(frame jsonFrame) error {
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
		if err := collector.scope.reserve(jsonDecodedElementBytes); err != nil {
			return err
		}
		if collector.path.depth == 1 {
			if collector.root {
				return errors.New("pull-request JSON has multiple roots")
			}
			collector.root = true
		}
		collector.duplicates.startObject(collector.path.depth)
		return collector.startObject()
	case jsonArrayStart:
		if err := collector.path.start(frame.Kind, frame.Count); err != nil {
			return err
		}
		return reserveJSONDecodedSlice(collector.scope, frame.Count)
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

func (collector *pullRequestJSONCollector) startObject() error {
	switch {
	case collector.path.suffix("user"):
		if err := collector.presence.add(prUserField); err != nil {
			return err
		}
		collector.payload.User = new(struct {
			Login string `json:"login"`
		})
	case collector.path.suffix("base"):
		return collector.presence.add(prBaseField)
	case collector.path.suffix("head"):
		return collector.presence.add(prHeadField)
	}
	return nil
}

func (collector *pullRequestJSONCollector) scalar(value jsonPathValue, frame jsonFrame) error {
	field, recognized := collector.field(value)
	if !recognized {
		return nil
	}
	if err := collector.presence.add(field); err != nil {
		return err
	}
	var err error
	switch field {
	case prMergedAtField:
		collector.payload.MergedAt, err = collector.scope.nullableString(frame)
	case prUserField:
		if frame.Kind != jsonNull {
			return errors.New("pull-request user is not an object or null")
		}
		collector.payload.User = nil
	case prUserLoginField:
		if collector.payload.User == nil {
			return errors.New("pull-request user login is outside a user object")
		}
		collector.payload.User.Login, err = collector.scope.string(frame)
	case prBaseRefField:
		collector.payload.Base.Ref, err = collector.scope.string(frame)
	case prBaseSHAField:
		collector.payload.Base.SHA, err = collector.scope.string(frame)
	case prHeadRefField:
		collector.payload.Head.Ref, err = collector.scope.string(frame)
	case prHeadSHAField:
		collector.payload.Head.SHA, err = collector.scope.string(frame)
	case prTitleField:
		collector.payload.Title, err = collector.scope.string(frame)
	case prHTMLURLField:
		collector.payload.HTMLURL, err = collector.scope.string(frame)
	case prStateField:
		collector.payload.State, err = collector.scope.string(frame)
	case prNumberField:
		collector.payload.Number, err = jsonUint64Value(frame)
	case prChangedFilesField:
		collector.payload.ChangedFiles, err = jsonUint64Value(frame)
	case prDraftField:
		collector.payload.Draft, err = jsonBooleanValue(frame)
	default:
		return errors.New("pull-request JSON field state is invalid")
	}
	if err != nil {
		return fmt.Errorf("decode pull-request field %q: %w", value.name, err)
	}
	return nil
}

func (collector *pullRequestJSONCollector) field(value jsonPathValue) (pullRequestField, bool) {
	var field pullRequestField
	switch value.fieldID {
	case jsonFieldIDMergedAt:
		field = prMergedAtField
	case jsonFieldIDUser:
		field = prUserField
	case jsonFieldIDLogin:
		field = prUserLoginField
	case jsonFieldIDRef:
		parent, _ := collector.path.namedAncestor(0)
		switch parent {
		case jsonFieldIDBase:
			field = prBaseRefField
		case jsonFieldIDHead:
			field = prHeadRefField
		default:
			return 0, false
		}
	case jsonFieldIDSHA:
		parent, _ := collector.path.namedAncestor(0)
		switch parent {
		case jsonFieldIDBase:
			field = prBaseSHAField
		case jsonFieldIDHead:
			field = prHeadSHAField
		default:
			return 0, false
		}
	case jsonFieldIDTitle:
		field = prTitleField
	case jsonFieldIDHTMLURL:
		field = prHTMLURLField
	case jsonFieldIDState:
		field = prStateField
	case jsonFieldIDNumber:
		field = prNumberField
	case jsonFieldIDChangedFiles:
		field = prChangedFilesField
	case jsonFieldIDDraft:
		field = prDraftField
	default:
		return 0, false
	}
	var matched bool
	switch field {
	case prUserLoginField:
		matched = collector.path.parents(jsonFieldIDUser)
	case prBaseRefField, prBaseSHAField:
		matched = collector.path.parents(jsonFieldIDBase)
	case prHeadRefField, prHeadSHAField:
		matched = collector.path.parents(jsonFieldIDHead)
	default:
		matched = collector.path.parents()
	}
	return field, matched
}

func (collector *pullRequestJSONCollector) finish() error {
	if !collector.root || collector.path.depth != 0 {
		return errors.New("pull-request JSON root is not an object")
	}
	required := [...]pullRequestField{
		prMergedAtField,
		prUserField,
		prBaseField,
		prBaseRefField,
		prBaseSHAField,
		prHeadField,
		prHeadRefField,
		prHeadSHAField,
		prTitleField,
		prHTMLURLField,
		prStateField,
		prNumberField,
		prChangedFilesField,
		prDraftField,
	}
	for _, field := range required {
		if !collector.presence.has(field) {
			return errors.New("pull-request JSON is missing a required field")
		}
	}
	return nil
}

func (collector *repositoryJSONCollector) route(value jsonPathValue) repositoryScalarRoute {
	if value.fieldID == jsonFieldIDNameWithOwner && collector.path.parents() {
		return repositoryNameRoute
	}
	return 0
}

func (presence *pullRequestFieldPresence) add(field pullRequestField) error {
	if field == 0 || field >= prFieldLimit {
		return errors.New("pull-request JSON field state is invalid")
	}
	bit := pullRequestFieldPresence(1) << (field - 1)
	if *presence&bit != 0 {
		return errors.New("JSON object contains a duplicate recognized field")
	}
	*presence |= bit
	return nil
}

func (presence *pullRequestFieldPresence) has(field pullRequestField) bool {
	if field == 0 || field >= prFieldLimit {
		return false
	}
	return *presence&(pullRequestFieldPresence(1)<<(field-1)) != 0
}
