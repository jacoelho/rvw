package source

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIDecodedLimit     = 1 << 20
	githubAPICompressedLimit  = 1 << 20
	githubUnknownJSONMaxDepth = 64
	githubUnknownJSONTokens   = 1_000_000
	githubFieldMergedAt       = "merged_at"
	githubFieldUser           = "user"
	githubFieldBase           = "base"
	githubFieldHead           = "head"
	githubFieldTitle          = "title"
	githubFieldHTMLURL        = "html_url"
	githubFieldState          = "state"
	githubFieldNumber         = "number"
	githubFieldChangedFiles   = "changed_files"
	githubFieldDraft          = "draft"
	githubFieldLogin          = "login"
	githubFieldName           = "name"
	githubFieldOwner          = "owner"
	githubFieldEndCursor      = "endCursor"
	githubFieldRef            = "ref"
	githubFieldSHA            = "sha"
	githubJSONNull            = "null"
	githubHost                = "github.com"
)

// These are variables solely so the separately built E2E binary can be linked
// to its hermetic native HTTP fixture. Production builds retain the official
// endpoints and no runtime configuration can redirect them.
var (
	githubAPIBase = "https://api.github.com"
	githubGitBase = "https://github.com"
)

type githubClient struct { //nolint:govet // Field order groups the client and its endpoint configuration.
	http    *http.Client
	token   []byte
	apiBase string
	gitBase string
}

type githubResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *githubResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func newGitHubClient(token *GitHubToken) *githubClient {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &githubClient{
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("GitHub redirects are unsupported")
			},
		},
		apiBase: githubAPIBase,
		gitBase: githubGitBase,
	}
	if token != nil {
		client.token = append([]byte(nil), token.value...)
	}
	return client
}

func (client *githubClient) close() {
	if client == nil || client.http == nil {
		return
	}
	if transport, ok := client.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (client *githubClient) pullRequest(
	ctx context.Context,
	repository OwnerRepository,
	number PullRequestNumber,
) (payload pullRequestPayload, returnErr error) {
	path := "/repos/" + repository.String() + "/pulls/" +
		strconv.FormatUint(number.Uint64(), 10)
	response, err := client.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return pullRequestPayload{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		detail, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		if readErr != nil {
			return pullRequestPayload{}, readErr
		}
		return pullRequestPayload{}, fmt.Errorf(
			"GitHub pull-request metadata returned %s: %s",
			response.Status, strings.TrimSpace(string(detail)),
		)
	}
	reader, closeReader, err := boundedGitHubResponse(response)
	if err != nil {
		return pullRequestPayload{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeReader()) }()
	payload, err = decodePullRequestDTO(reader)
	if err != nil {
		return pullRequestPayload{}, fmt.Errorf("decode GitHub pull request: %w", err)
	}
	return payload, nil
}

func (client *githubClient) request(
	ctx context.Context,
	method, path string,
	body io.Reader,
) (*http.Response, error) {
	if client == nil || client.http == nil {
		return nil, errors.New("GitHub client is unavailable")
	}
	if len(client.token) == 0 {
		return nil, errors.New("GITHUB_TOKEN is required for GitHub access")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	request, err := http.NewRequestWithContext(requestCtx, method, client.apiBase+path, body)
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "rvw")
	request.Header.Set("Authorization", "Bearer "+string(client.token))
	response, err := client.http.Do(request)
	if err != nil {
		cancel()
		if context.Cause(ctx) != nil {
			return nil, context.Cause(ctx)
		}
		return nil, err
	}
	response.Body = &githubResponseBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

func boundedGitHubResponse(response *http.Response) (io.Reader, func() error, error) {
	compressed := &io.LimitedReader{R: response.Body, N: githubAPICompressedLimit + 1}
	var reader io.Reader = compressed
	closeReader := func() error { return nil }
	if strings.EqualFold(response.Header.Get("Content-Encoding"), "gzip") {
		gzipReader, err := gzip.NewReader(compressed)
		if err != nil {
			return nil, closeReader, err
		}
		reader = gzipReader
		closeReader = gzipReader.Close
	} else if encoding := response.Header.Get("Content-Encoding"); encoding != "" &&
		!strings.EqualFold(encoding, "identity") {
		return nil, closeReader, fmt.Errorf("unsupported GitHub content encoding %q", encoding)
	}
	return &io.LimitedReader{R: reader, N: githubAPIDecodedLimit + 1}, closeReader, nil
}

func decodePullRequestDTO(reader io.Reader) (pullRequestPayload, error) {
	decoder := json.NewDecoder(reader)
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return pullRequestPayload{}, errors.New("pull-request response must be an object")
	}
	var payload pullRequestPayload
	var recognized uint64
	for decoder.More() {
		name, fieldErr := nextJSONField(decoder)
		if fieldErr != nil {
			return pullRequestPayload{}, fieldErr
		}
		bit := pullRequestFieldBit(name)
		if bit != 0 {
			if recognized&bit != 0 {
				return pullRequestPayload{}, fmt.Errorf("duplicate recognized field %q", name)
			}
			recognized |= bit
		}
		var decodeErr error
		switch name {
		case githubFieldMergedAt:
			var value *string
			value, decodeErr = decodeNullableJSONString(decoder)
			if decodeErr != nil {
				return pullRequestPayload{}, fieldTypeError(name, decodeErr)
			}
			payload.MergedAt = value
		case githubFieldUser:
			var user *struct {
				Login string `json:"login"`
			}
			user, decodeErr = decodeGitHubUser(decoder)
			if decodeErr != nil {
				return pullRequestPayload{}, fieldTypeError(name, decodeErr)
			}
			payload.User = user
		case githubFieldBase:
			var ref, sha string
			ref, sha, decodeErr = decodeGitHubRevision(decoder)
			if decodeErr != nil {
				return pullRequestPayload{}, fieldTypeError(name, decodeErr)
			}
			payload.Base.Ref, payload.Base.SHA = ref, sha
		case githubFieldHead:
			var ref, sha string
			ref, sha, decodeErr = decodeGitHubRevision(decoder)
			if decodeErr != nil {
				return pullRequestPayload{}, fieldTypeError(name, decodeErr)
			}
			payload.Head.Ref, payload.Head.SHA = ref, sha
		case githubFieldTitle:
			decodeErr = decoder.Decode(&payload.Title)
		case githubFieldHTMLURL:
			decodeErr = decoder.Decode(&payload.HTMLURL)
		case githubFieldState:
			decodeErr = decoder.Decode(&payload.State)
		case githubFieldNumber:
			decodeErr = decoder.Decode(&payload.Number)
		case githubFieldChangedFiles:
			decodeErr = decoder.Decode(&payload.ChangedFiles)
		case githubFieldDraft:
			decodeErr = decoder.Decode(&payload.Draft)
		default:
			decodeErr = skipJSONValue(decoder, 0, new(int))
		}
		if decodeErr != nil {
			return pullRequestPayload{}, fieldTypeError(name, decodeErr)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return pullRequestPayload{}, errors.New("pull-request object is unterminated")
	}
	if _, trailingErr := decoder.Token(); !errors.Is(trailingErr, io.EOF) {
		return pullRequestPayload{}, errors.New("pull-request response has trailing JSON data")
	}
	const required = uint64(1<<10 - 1)
	if recognized&required != required {
		return pullRequestPayload{}, errors.New("pull-request response is missing required fields")
	}
	return payload, nil
}

func pullRequestFieldBit(name string) uint64 {
	switch name {
	case githubFieldMergedAt:
		return 1 << 0
	case githubFieldUser:
		return 1 << 1
	case githubFieldBase:
		return 1 << 2
	case githubFieldHead:
		return 1 << 3
	case githubFieldTitle:
		return 1 << 4
	case githubFieldHTMLURL:
		return 1 << 5
	case githubFieldState:
		return 1 << 6
	case githubFieldNumber:
		return 1 << 7
	case githubFieldChangedFiles:
		return 1 << 8
	case githubFieldDraft:
		return 1 << 9
	default:
		return 0
	}
}

func nextJSONField(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	name, ok := token.(string)
	if !ok {
		return "", errors.New("JSON object field name is not a string")
	}
	return name, nil
}

//nolint:nilnil // JSON null is a valid optional string.
func decodeNullableJSONString(decoder *json.Decoder) (*string, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if string(raw) == githubJSONNull {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return new(value), nil
}

//nolint:nilnil // JSON null is a valid absent user.
func decodeGitHubUser(decoder *json.Decoder) (*struct {
	Login string `json:"login"`
}, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if string(raw) == githubJSONNull {
		return nil, nil
	}
	inner := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := inner.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("user must be an object or null")
	}
	user := &struct {
		Login string `json:"login"`
	}{}
	seen := false
	for inner.More() {
		name, err := nextJSONField(inner)
		if err != nil {
			return nil, err
		}
		if name == githubFieldLogin {
			if seen {
				return nil, errors.New("duplicate recognized field \"login\"")
			}
			seen = true
			err = inner.Decode(&user.Login)
		} else {
			err = skipJSONValue(inner, 0, new(int))
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := inner.Token(); err != nil || !seen {
		return nil, errors.New("user object is invalid")
	}
	return user, nil
}

func decodeGitHubRevision(decoder *json.Decoder) (string, string, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", "", errors.New("revision must be an object")
	}
	ref, sha := "", ""
	seen := uint8(0)
	for decoder.More() {
		name, fieldErr := nextJSONField(decoder)
		if fieldErr != nil {
			return "", "", fieldErr
		}
		switch name {
		case githubFieldRef:
			if seen&1 != 0 {
				return "", "", errors.New("duplicate recognized field \"ref\"")
			}
			seen |= 1
			err = decoder.Decode(&ref)
		case githubFieldSHA:
			if seen&2 != 0 {
				return "", "", errors.New("duplicate recognized field \"sha\"")
			}
			seen |= 2
			err = decoder.Decode(&sha)
		default:
			err = skipJSONValue(decoder, 0, new(int))
		}
		if err != nil {
			return "", "", err
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || seen != 3 {
		return "", "", errors.New("revision object is invalid")
	}
	return ref, sha, nil
}

func skipJSONValue(decoder *json.Decoder, depth int, tokens *int) error {
	if depth > githubUnknownJSONMaxDepth || *tokens >= githubUnknownJSONTokens {
		return errors.New("unknown JSON value exceeds structural limits")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	*tokens++
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for decoder.More() {
			if _, fieldErr := nextJSONField(decoder); fieldErr != nil {
				return fieldErr
			}
			if skipErr := skipJSONValue(decoder, depth+1, tokens); skipErr != nil {
				return skipErr
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if skipErr := skipJSONValue(decoder, depth+1, tokens); skipErr != nil {
				return skipErr
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected closing JSON delimiter")
	}
}

func fieldTypeError(name string, err error) error {
	return fmt.Errorf("field %q has an invalid value: %w", name, err)
}
