package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"
)

type githubHTTPResult struct {
	body      []byte
	status    int
	attempted bool
}

type githubHTTPError struct {
	cause     error
	status    int
	attempted bool
}

func (failure *githubHTTPError) Error() string {
	if failure.status != 0 {
		return fmt.Sprintf("GitHub returned HTTP %d: %v", failure.status, failure.cause)
	}
	return failure.cause.Error()
}

func (failure *githubHTTPError) Unwrap() error { return failure.cause }

type countingRequestBody struct {
	reader    *bytes.Reader
	attempted *bool
}

func (body *countingRequestBody) Read(buffer []byte) (int, error) {
	*body.attempted = true
	return body.reader.Read(buffer)
}

func (body *countingRequestBody) Close() error { return nil }

func (client *githubClient) mutation(
	ctx context.Context,
	method, path string,
	payload []byte,
	limit int64,
) (githubHTTPResult, error) {
	if client == nil || client.http == nil || len(client.token) == 0 {
		return githubHTTPResult{}, &githubHTTPError{
			cause: errors.New("GITHUB_TOKEN is required for GitHub access"),
		}
	}
	attempted := false
	body := &countingRequestBody{reader: bytes.NewReader(payload), attempted: &attempted}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, client.apiBase+path, body)
	if err != nil {
		return githubHTTPResult{}, &githubHTTPError{cause: err}
	}
	request.ContentLength = int64(len(payload))
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "rvw")
	request.Header.Set("Authorization", "Bearer "+string(client.token))
	response, err := client.http.Do(request)
	if err != nil {
		if context.Cause(ctx) != nil {
			err = context.Cause(ctx)
		}
		return githubHTTPResult{attempted: attempted}, &githubHTTPError{
			cause: err, attempted: attempted,
		}
	}
	encoded, readErr := readGitHubBody(response.Body, limit)
	closeErr := response.Body.Close()
	result := githubHTTPResult{body: encoded, status: response.StatusCode, attempted: attempted}
	if responseErr := errors.Join(readErr, closeErr); responseErr != nil {
		return result, &githubHTTPError{
			cause: responseErr, status: response.StatusCode, attempted: attempted,
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, &githubHTTPError{
			cause:  errors.New(strings.TrimSpace(string(encoded))),
			status: response.StatusCode, attempted: attempted,
		}
	}
	return result, nil
}

func readGitHubBody(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("GitHub response limit is invalid")
	}
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	value, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, &LimitError{Resource: LimitPackWireBytes, Limit: uint64(limit)}
	}
	return value, nil
}

func githubMutationFailedBeforeCommit(err error) bool {
	var failure *githubHTTPError
	if !errors.As(err, &failure) {
		return false
	}
	if !failure.attempted {
		return true
	}
	switch failure.status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func githubGraphQLMutationFailedBeforeCommit(err error) bool {
	var failure *githubHTTPError
	if !errors.As(err, &failure) {
		return false
	}
	if !failure.attempted {
		return true
	}
	switch failure.status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func (client *githubClient) graphQLPages(
	ctx context.Context,
	query string,
	connection string,
	variables map[string]any,
	limit int64,
) ([]byte, error) {
	if query == "" {
		return nil, errors.New("GitHub GraphQL query is missing")
	}
	if limit <= 0 {
		return nil, errors.New("GitHub GraphQL response limit is invalid")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fields := maps.Clone(variables)
	if fields == nil {
		fields = make(map[string]any)
	}
	pages := make([]json.RawMessage, 0, 4)
	seen := make(map[string]struct{})
	for {
		payload, err := json.Marshal(map[string]any{"query": query, "variables": fields})
		if err != nil {
			return nil, err
		}
		result, err := client.mutation(requestCtx, http.MethodPost, "/graphql", payload, limit)
		if err != nil {
			return nil, err
		}
		pages = append(pages, append(json.RawMessage(nil), result.body...))
		if encodedJSONBytes(pages) > limit {
			return nil, &LimitError{Resource: LimitPackWireBytes, Limit: uint64(limit)}
		}
		hasNext, cursor, err := githubGraphQLPageInfo(result.body, connection)
		if err != nil {
			return nil, err
		}
		if !hasNext {
			break
		}
		if cursor == "" {
			return nil, errors.New("GitHub pagination has no end cursor")
		}
		if _, repeated := seen[cursor]; repeated {
			return nil, errors.New("GitHub pagination cursor repeated")
		}
		seen[cursor] = struct{}{}
		fields[githubFieldEndCursor] = cursor
	}
	return json.Marshal(pages)
}

func (client *githubClient) restPages(
	ctx context.Context,
	path string,
	limit int64,
) ([]byte, error) {
	if path == "" || !strings.HasPrefix(path, "/") {
		return nil, errors.New("GitHub REST endpoint is missing")
	}
	if limit <= 0 {
		return nil, errors.New("GitHub REST response limit is invalid")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pages := make([]json.RawMessage, 0, 4)
	seen := make(map[string]struct{})
	for path != "" {
		if _, repeated := seen[path]; repeated {
			return nil, errors.New("GitHub REST pagination repeated a page")
		}
		seen[path] = struct{}{}
		response, err := client.request(requestCtx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		body, readErr := readGitHubBody(response.Body, limit)
		closeErr := response.Body.Close()
		if responseErr := errors.Join(readErr, closeErr); responseErr != nil {
			return nil, responseErr
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub returned %s: %s", response.Status, body)
		}
		pages = append(pages, append(json.RawMessage(nil), body...))
		if encodedJSONBytes(pages) > limit {
			return nil, &LimitError{Resource: LimitPackWireBytes, Limit: uint64(limit)}
		}
		path, err = nextGitHubRESTPath(response.Header.Get("Link"), client.apiBase)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(pages)
}

func githubGraphQLPageInfo(encoded []byte, connection string) (bool, string, error) {
	if connection == "" {
		return false, "", nil
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return false, "", err
	}
	var found bool
	var hasNext bool
	var cursor string
	var visit func(any) error
	visit = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			if connectionValue, ok := value[connection].(map[string]any); ok {
				page, pageOK := connectionValue["pageInfo"].(map[string]any)
				if !pageOK {
					return errors.New("GitHub connection has invalid pageInfo")
				}
				next, nextOK := page["hasNextPage"].(bool)
				if !nextOK {
					return errors.New("GitHub pageInfo has invalid hasNextPage")
				}
				if found && next {
					return errors.New("GitHub response contains multiple active page cursors")
				}
				if next {
					found, hasNext = true, true
					var cursorOK bool
					cursor, cursorOK = page[githubFieldEndCursor].(string)
					if !cursorOK {
						return errors.New("GitHub pageInfo has invalid endCursor")
					}
				}
			}
			for _, child := range value {
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(document); err != nil {
		return false, "", err
	}
	return hasNext, cursor, nil
}

func encodedJSONBytes(pages []json.RawMessage) int64 {
	bytes := int64(2 + max(0, len(pages)-1))
	for _, page := range pages {
		bytes += int64(len(page))
	}
	return bytes
}

func nextGitHubRESTPath(header, apiBase string) (string, error) {
	for part := range strings.SplitSeq(header, ",") {
		sections := strings.Split(strings.TrimSpace(part), ";")
		if len(sections) < 2 || !strings.Contains(sections[1], `rel="next"`) {
			continue
		}
		address := strings.Trim(sections[0], "<>")
		if !strings.HasPrefix(address, apiBase+"/") {
			return "", errors.New("GitHub pagination escaped the API origin")
		}
		return strings.TrimPrefix(address, apiBase), nil
	}
	return "", nil
}
