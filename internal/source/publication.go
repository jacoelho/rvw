package source

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/subprocess"
)

func (service *Service) CheckPublication(
	ctx context.Context,
	document *Document,
	prepared review.PreparedPublication,
) (*review.CheckedPublication, error) {
	if document == nil || prepared.Source() != document.Snapshot().SourceID() {
		return nil, sourceFailure(
			OperationCheckPublication,
			errors.New("publication source revision is stale"),
		)
	}
	expected, ok := document.PullRequest()
	if !ok {
		return nil, sourceFailure(
			OperationCheckPublication,
			errors.New("publication is available only for a GitHub pull request"),
		)
	}
	current, err := service.pullRequestMetadata(ctx, expected.Repository, expected.Number)
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	currentDetails, err := validatePullRequestPayload(
		expected.Repository,
		expected.Number,
		current,
	)
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	if currentDetails.Repository != expected.Repository ||
		currentDetails.Number != expected.Number {
		return nil, sourceFailure(
			OperationCheckPublication,
			errors.New("GitHub publication target identity changed"),
		)
	}
	staleHead := currentDetails.BaseOID != expected.BaseOID ||
		currentDetails.HeadOID != expected.HeadOID
	if staleHead &&
		!prepared.Options().Policies().Has(review.PublicationAllowStaleHead) {
		return nil, sourceFailure(
			OperationCheckPublication,
			errors.New("GitHub pull request revision changed before publication"),
		)
	}
	target, err := review.NewPublicationTarget(review.PublicationTargetDescriptor{
		Repository:  expected.Repository.String(),
		PullRequest: expected.Number.Uint64(),
		Base:        expected.BaseOID,
		Head:        expected.HeadOID,
	})
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	marker, err := newPublicationMarker()
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	checked, err := review.TakeCheckedPublication(review.CheckedPublicationDescriptor{
		Target:    target,
		Marker:    marker,
		StaleHead: staleHead,
	}, prepared)
	if err != nil {
		return nil, sourceFailure(OperationCheckPublication, err)
	}
	return checked, nil
}

func newPublicationMarker() (review.PublicationMarker, error) {
	var attempt [16]byte
	if _, err := rand.Read(attempt[:]); err != nil {
		return "", fmt.Errorf("generate publication identity: %w", err)
	}
	return review.NewPublicationMarker(
		"<!-- rvw-publication:v1:" + hex.EncodeToString(attempt[:]) + " -->",
	)
}

type PublicationCommit struct {
	Disposition  review.PublicationDisposition
	RemoteReview uint64
	Detail       review.OperationDetail
}

func (service *Service) Publish(
	ctx context.Context,
	checked *review.CheckedPublication,
) PublicationCommit {
	if checked == nil || checked.PayloadSize() == 0 {
		return PublicationCommit{
			Disposition: review.PublicationDispositionFailedBeforeCommit,
			Detail:      boundedOperationDetail("checked publication is unavailable"),
		}
	}
	target := checked.Target()
	endpoint := "repos/" + target.Repository() + "/pulls/" +
		strconv.FormatUint(target.PullRequest(), 10) + "/reviews"
	result, err := service.runner.Run(ctx, subprocess.Request{
		Name: "gh",
		Args: []string{
			"api",
			"--method", "POST",
			endpoint,
			"--input", "-",
		},
		Dir:             service.cwd.String(),
		Env:             map[string]string{"LC_ALL": "C", "LANG": "C", "GH_PROMPT_DISABLED": "1"},
		Stdin:           []byte(checked.Payload()),
		StdoutLimit:     1 << 20,
		StderrLimit:     1 << 20,
		OperationDetail: "gh api publication",
	})
	if err != nil {
		disposition := review.PublicationDispositionFailedBeforeCommit
		if result.Started {
			disposition = review.PublicationDispositionUnknown
		}
		return PublicationCommit{
			Disposition: disposition,
			Detail:      boundedOperationDetail(err.Error()),
		}
	}
	if !utf8.Valid(result.Stdout) {
		return PublicationCommit{
			Disposition: review.PublicationDispositionUnknown,
			Detail:      boundedOperationDetail("GitHub publication response is not valid UTF-8"),
		}
	}
	var response struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(result.Stdout, &response); err != nil || response.ID == 0 {
		detail := "GitHub publication response has no review identity"
		if err != nil {
			detail = "decode GitHub publication response: " + err.Error()
		}
		return PublicationCommit{
			Disposition: review.PublicationDispositionUnknown,
			Detail:      boundedOperationDetail(detail),
		}
	}
	return PublicationCommit{
		Disposition:  review.PublicationDispositionCommitted,
		RemoteReview: response.ID,
	}
}

func (service *Service) ReconcilePublication(
	ctx context.Context,
	request review.ReconciliationRequest,
) review.ReconciliationResult {
	result := review.ReconciliationResult{
		Operation:   request.Operation,
		Workspace:   request.Workspace,
		Source:      request.Source,
		Disposition: review.ReconciliationStillUnknown,
	}
	target := request.Target
	endpoint := "repos/" + target.Repository() + "/pulls/" +
		strconv.FormatUint(target.PullRequest(), 10) + "/reviews"
	command, err := service.runGH(
		ctx,
		[]string{"api", endpoint, "--paginate", "--slurp"},
		review.MaxPublicationBytes,
	)
	if err != nil {
		result.Detail = boundedOperationDetail(err.Error())
		return result
	}
	if !utf8.Valid(command.Stdout) {
		result.Detail = boundedOperationDetail("GitHub review history is not valid UTF-8")
		return result
	}
	bodies, err := decodeReviewBodies(command.Stdout)
	if err != nil {
		result.Detail = boundedOperationDetail(err.Error())
		return result
	}
	matches := 0
	for _, body := range bodies {
		matches += strings.Count(body, request.Marker.String())
		if matches > 1 {
			result.Detail = boundedOperationDetail("publication marker is ambiguous in GitHub review history")
			return result
		}
	}
	if matches == 1 {
		result.Disposition = review.ReconciliationCommitted
		result.Detail = boundedOperationDetail("publication marker confirmed")
	} else {
		result.Disposition = review.ReconciliationNotCommitted
		result.Detail = boundedOperationDetail("publication marker confirmed absent")
	}
	return result
}

func decodeReviewBodies(value []byte) ([]string, error) {
	var pages [][]reviewHistoryItem
	if err := json.Unmarshal(value, &pages); err != nil {
		return nil, fmt.Errorf("decode GitHub review history: %w", err)
	}
	if len(pages) == 0 {
		return nil, errors.New("GitHub review history contains no pagination result")
	}
	bodies := make([]string, 0)
	seen := make(map[uint64]struct{})
	for pageIndex, page := range pages {
		if page == nil {
			return nil, fmt.Errorf(
				"GitHub review history page %d is null",
				pageIndex+1,
			)
		}
		for itemIndex, item := range page {
			if item.id == nil {
				return nil, fmt.Errorf(
					"GitHub review history page %d item %d has no identity",
					pageIndex+1,
					itemIndex+1,
				)
			}
			var id uint64
			if err := json.Unmarshal(item.id, &id); err != nil || id == 0 {
				return nil, fmt.Errorf(
					"GitHub review history page %d item %d has an invalid identity",
					pageIndex+1,
					itemIndex+1,
				)
			}
			if _, exists := seen[id]; exists {
				return nil, fmt.Errorf("GitHub review history repeats review %d", id)
			}
			seen[id] = struct{}{}

			if item.body == nil {
				return nil, fmt.Errorf(
					"GitHub review history page %d item %d has no body",
					pageIndex+1,
					itemIndex+1,
				)
			}
			rawBody := bytes.TrimSpace(item.body)
			if len(rawBody) == 0 || rawBody[0] != '"' {
				return nil, fmt.Errorf(
					"GitHub review history page %d item %d has an invalid body",
					pageIndex+1,
					itemIndex+1,
				)
			}
			var body string
			if err := json.Unmarshal(rawBody, &body); err != nil {
				return nil, fmt.Errorf(
					"GitHub review history page %d item %d has an invalid body",
					pageIndex+1,
					itemIndex+1,
				)
			}
			bodies = append(bodies, body)
		}
	}
	return bodies, nil
}

type reviewHistoryItem struct {
	id   json.RawMessage
	body json.RawMessage
}

func (item *reviewHistoryItem) UnmarshalJSON(value []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("GitHub review history item is not an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("GitHub review history item key is not a string")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("GitHub review history item repeats field %q", name)
		}
		seen[name] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		switch name {
		case "id":
			item.id = raw
		case "body":
			item.body = raw
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("GitHub review history item is not terminated")
	}
	return nil
}

func boundedOperationDetail(value string) review.OperationDetail {
	return review.NewBoundedOperationDetail(value)
}
