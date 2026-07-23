//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fixtureOwner      = "fixture"
	fixtureRepository = "repository"
	fixturePR         = uint64(7)
)

var (
	rvwBinary      string
	testExecutable string
	suiteTemporary string
	systemGit      string
	systemScript   string
	systemShell    string
	systemStty     string
	githubFixture  *httptest.Server
	activeWorld    struct {
		sync.RWMutex
		value *world
	}
)

func TestMain(m *testing.M) {
	if helper := filepath.Base(os.Args[0]); slices.Contains(
		[]string{"pbcopy", "wl-copy", "xclip"},
		helper,
	) {
		os.Exit(runExternalHelper(helper, os.Args[1:]))
	}

	var err error
	testExecutable, err = os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "resolve E2E test executable: %v\n", err)
		os.Exit(1)
	}
	systemGit, err = exec.LookPath("git")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "resolve git: %v\n", err)
		os.Exit(1)
	}
	systemShell, err = exec.LookPath("sh")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "resolve sh: %v\n", err)
		os.Exit(1)
	}
	systemStty, err = exec.LookPath("stty")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "resolve stty: %v\n", err)
		os.Exit(1)
	}
	systemScript = "/usr/bin/script"
	scriptInfo, err := os.Stat(systemScript)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "resolve %s: %v\n", systemScript, err)
		os.Exit(1)
	}
	if scriptInfo.Mode()&0o111 == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "%s is not executable\n", systemScript)
		os.Exit(1)
	}
	suiteTemporary, err = os.MkdirTemp("", "rvw-e2e-suite-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create E2E suite directory: %v\n", err)
		os.Exit(1)
	}
	rvwBinary = filepath.Join(suiteTemporary, "rvw")
	githubFixture = httptest.NewServer(http.HandlerFunc(serveGitHubFixture))
	linkEndpoints := strings.Join([]string{
		"-X=github.com/jacoelho/rvw/internal/source.githubAPIBase=" + githubFixture.URL,
		"-X=github.com/jacoelho/rvw/internal/source.githubGitBase=" + githubFixture.URL,
	}, " ")
	build := exec.Command(
		"go", "build", "-trimpath", "-ldflags", linkEndpoints,
		"-o", rvwBinary, "./cmd/rvw",
	)
	build.Dir = repositoryRoot()
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "build rvw E2E binary: %v\n%s", buildErr, output)
		_ = os.RemoveAll(suiteTemporary)
		os.Exit(1)
	}

	status := m.Run()
	githubFixture.Close()
	if err := os.RemoveAll(suiteTemporary); err != nil && status == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "remove E2E suite directory: %v\n", err)
		status = 1
	}
	os.Exit(status)
}

func repositoryRoot() string {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ".."
	}
	return filepath.Clean(filepath.Join(workingDirectory, ".."))
}

type externalCall struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Stdin   string   `json:"stdin"`
}

func runExternalHelper(name string, args []string) int {
	stdin, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20+1))
	if err != nil {
		return helperFailure("read stdin: %v", err)
	}
	if len(stdin) > 16<<20 {
		return helperFailure("stdin exceeds 16 MiB")
	}
	if err := appendExternalCall(externalCall{Command: name, Args: args, Stdin: string(stdin)}); err != nil {
		return helperFailure("log call: %v", err)
	}

	switch name {
	case "pbcopy", "wl-copy", "xclip":
		return runClipboardHelper(name, args, stdin)
	default:
		return helperFailure("unsupported helper %q", name)
	}
}

func appendExternalCall(call externalCall) error {
	path := os.Getenv("RVW_E2E_CALL_LOG")
	if path == "" {
		return errors.New("RVW_E2E_CALL_LOG is unset")
	}
	return appendExternalCallAt(path, call)
}

func appendExternalCallAt(path string, call externalCall) error {
	encoded, err := json.Marshal(call)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(encoded)
	return errors.Join(writeErr, file.Close())
}

func serveGitHubFixture(writer http.ResponseWriter, request *http.Request) {
	activeWorld.RLock()
	w := activeWorld.value
	activeWorld.RUnlock()
	if w == nil {
		http.Error(writer, "no active E2E world", http.StatusServiceUnavailable)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/fixture/repository.git/") {
		_ = appendExternalCallAt(w.callLog, externalCall{
			Command: "smart-git",
			Args:    []string{request.Method, request.URL.RequestURI()},
		})
		serveWorldGitHTTP(w, writer, request)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 16<<20+1))
	if err != nil || len(body) > 16<<20 {
		http.Error(writer, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := appendExternalCallAt(w.callLog, externalCall{
		Command: "github-http",
		Args:    []string{request.Method, request.URL.RequestURI()},
		Stdin:   string(body),
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	if request.Header.Get("Authorization") != "Bearer e2e-token" {
		http.Error(writer, "missing test token", http.StatusUnauthorized)
		return
	}

	switch {
	case request.Method == http.MethodGet && request.URL.Path ==
		"/repos/fixture/repository/pulls/7":
		serveFixtureFile(writer, w.metadata, false)
	case request.Method == http.MethodPost && request.URL.Path == "/graphql":
		serveGraphQLFixture(w, writer, body)
	case request.Method == http.MethodGet && strings.HasSuffix(
		request.URL.Path, "/reviews/9001/comments",
	):
		serveFixtureFile(writer, w.pendingComments, true)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/reviews"):
		servePublicationFixture(w, writer, body)
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/reviews"):
		serveFixtureFile(writer, w.history, true)
	default:
		http.Error(writer, "unsupported native GitHub fixture request", http.StatusNotFound)
	}
}

func serveGraphQLFixture(w *world, writer http.ResponseWriter, body []byte) {
	var request struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &request); err != nil || request.Query == "" {
		http.Error(writer, "invalid GraphQL request", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(request.Query, "mutation(") {
		response, err := pendingMutationResponse(w, body)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(response)
		return
	}
	path := w.threads
	if strings.Contains(request.Query, "reviewThreads(first:100") {
		path = w.threads
	} else if strings.Contains(request.Query, "reviews(first:100") {
		path = w.pending
	} else if strings.Contains(request.Query, "comments(first:100") {
		path = w.pendingCapabilities
	}
	serveFixtureFile(writer, path, true)
}

func servePublicationFixture(w *world, writer http.ResponseWriter, body []byte) {
	if !json.Valid(body) {
		http.Error(writer, "publication body is not JSON", http.StatusBadRequest)
		return
	}
	switch worldEnvironment(w, "RVW_E2E_GH_POST_MODE") {
	case "", "success":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":9001}`)
	case "unknown", "unknown-present":
		if worldEnvironment(w, "RVW_E2E_GH_POST_MODE") == "unknown-present" {
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			history, err := json.Marshal([][]map[string]any{{{
				"id": 9002, "body": "remote body\n" + payload.Body,
			}}})
			if err != nil || os.WriteFile(w.history, history, 0o600) != nil {
				http.Error(writer, "write reconciliation fixture", http.StatusInternalServerError)
				return
			}
		}
		http.Error(writer, "simulated ambiguous GitHub write", http.StatusInternalServerError)
	case "invalid-response":
		_, _ = io.WriteString(writer, `{}`)
	default:
		http.Error(writer, "unsupported post mode", http.StatusInternalServerError)
	}
}

func serveFixtureFile(writer http.ResponseWriter, path string, firstPage bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	if firstPage {
		var pages []json.RawMessage
		if err := json.Unmarshal(contents, &pages); err != nil || len(pages) == 0 {
			http.Error(writer, "fixture contains no page", http.StatusInternalServerError)
			return
		}
		contents = pages[0]
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(contents)
}

func serveWorldGitHTTP(w *world, writer http.ResponseWriter, request *http.Request) {
	command := exec.CommandContext(request.Context(), systemGit, "http-backend")
	pathInfo := "/pull-request.git" + strings.TrimPrefix(
		request.URL.Path, "/fixture/repository.git",
	)
	command.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+w.root,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+pathInfo,
		"QUERY_STRING="+request.URL.RawQuery,
		"REQUEST_METHOD="+request.Method,
		"CONTENT_TYPE="+request.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.FormatInt(request.ContentLength, 10),
		"REMOTE_USER=rvw-e2e",
	)
	command.Stdin = request.Body
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		http.Error(writer, err.Error()+": "+stderr.String(), http.StatusInternalServerError)
		return
	}
	reader := bufio.NewReader(&stdout)
	status := http.StatusOK
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			http.Error(writer, "malformed smart-Git response", http.StatusInternalServerError)
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			http.Error(writer, "malformed smart-Git header", http.StatusInternalServerError)
			return
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			status, _ = strconv.Atoi(strings.Fields(value)[0])
			continue
		}
		writer.Header().Add(name, value)
	}
	writer.WriteHeader(status)
	_, _ = io.Copy(writer, reader)
}

func worldEnvironment(w *world, name string) string {
	prefix := name + "="
	for _, value := range w.environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func pendingMutationResponse(w *world, stdin []byte) ([]byte, error) {
	var request struct {
		Variables map[string]map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(stdin, &request); err != nil || len(request.Variables) == 0 {
		return nil, fmt.Errorf("decode pending mutation: %w", err)
	}
	keys := make([]string, 0, len(request.Variables))
	for key := range request.Variables {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	response := make(map[string]map[string]string, len(keys))
	for _, key := range keys {
		input := request.Variables[key]
		client, _ := input["clientMutationId"].(string)
		if client == "" {
			return nil, fmt.Errorf("pending mutation %s has no client identity", key)
		}
		index, err := strconv.Atoi(strings.TrimPrefix(key, "v"))
		if err != nil {
			return nil, fmt.Errorf("pending mutation variable %q is invalid", key)
		}
		response["m"+strconv.Itoa(index)] = map[string]string{"clientMutationId": client}
		body, hasBody := input["body"].(string)
		switch {
		case input["pullRequestReviewCommentId"] != nil && hasBody:
			if err := updatePendingCommentFixture(
				w.pendingComments,
				fmt.Sprint(input["pullRequestReviewCommentId"]),
				body,
			); err != nil {
				return nil, fmt.Errorf("update pending comment fixture: %w", err)
			}
		case input["pullRequestReviewId"] != nil && hasBody:
			if err := updatePendingReviewFixture(
				w.pending,
				fmt.Sprint(input["pullRequestReviewId"]),
				body,
			); err != nil {
				return nil, fmt.Errorf("update pending review fixture: %w", err)
			}
		}
	}
	encoded, err := json.Marshal(map[string]any{"data": response, "errors": []any{}})
	if err != nil {
		return nil, fmt.Errorf("encode pending mutation response: %w", err)
	}
	return encoded, nil
}

func updatePendingCommentFixture(path, nodeID, body string) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var pages [][]map[string]any
	if err := json.Unmarshal(encoded, &pages); err != nil {
		return err
	}
	updated := false
	for _, page := range pages {
		for _, comment := range page {
			if comment["node_id"] == nodeID {
				comment["body"] = body
				updated = true
			}
		}
	}
	if !updated {
		return errors.New("pending comment identity is absent")
	}
	return writeHelperJSON(path, pages)
}

func updatePendingReviewFixture(path, nodeID, body string) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var pages []map[string]any
	if err := json.Unmarshal(encoded, &pages); err != nil {
		return err
	}
	updated := false
	for _, page := range pages {
		data, _ := page["data"].(map[string]any)
		repository, _ := data["repository"].(map[string]any)
		pull, _ := repository["pullRequest"].(map[string]any)
		reviews, _ := pull["reviews"].(map[string]any)
		nodes, _ := reviews["nodes"].([]any)
		for _, raw := range nodes {
			reviewNode, _ := raw.(map[string]any)
			if reviewNode["id"] == nodeID {
				reviewNode["body"] = body
				updated = true
			}
		}
	}
	if !updated {
		return errors.New("pending review identity is absent")
	}
	return writeHelperJSON(path, pages)
}

func writeHelperJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func runClipboardHelper(name string, args []string, stdin []byte) int {
	valid := len(args) == 0
	if name == "xclip" {
		valid = slices.Equal(args, []string{"-selection", "clipboard"})
	}
	if !valid {
		return helperFailure("unsupported %s argv: %q", name, args)
	}
	if os.Getenv("RVW_E2E_CLIPBOARD_FAIL") == "1" {
		_, _ = io.WriteString(os.Stderr, "simulated clipboard failure\n")
		return 74
	}
	path := os.Getenv("RVW_E2E_CLIPBOARD")
	if path == "" {
		return helperFailure("RVW_E2E_CLIPBOARD is unset")
	}
	if err := os.WriteFile(path, stdin, 0o600); err != nil {
		return helperFailure("write clipboard: %v", err)
	}
	return 0
}

func helperFailure(format string, args ...any) int {
	_, _ = fmt.Fprintf(os.Stderr, "rvw-e2e-helper: "+format+"\n", args...)
	return 64
}

type world struct {
	context             context.Context
	cancel              context.CancelFunc
	root                string
	repository          string
	home                string
	configHome          string
	configPath          string
	themes              string
	fakeDirectory       string
	callLog             string
	clipboard           string
	metadata            string
	threads             string
	pending             string
	pendingComments     string
	pendingCapabilities string
	history             string
	remote              string
	baseRevision        string
	headRevision        string
	prBase              string
	prHead              string
	environment         []string
	last                processResult
	pty                 *ptySession
}

func newWorld(t testing.TB) *world {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	root := t.TempDir()
	w := &world{
		context:             ctx,
		cancel:              cancel,
		root:                root,
		repository:          filepath.Join(root, "repository"),
		home:                filepath.Join(root, "home"),
		configHome:          filepath.Join(root, "config"),
		fakeDirectory:       filepath.Join(root, "bin"),
		callLog:             filepath.Join(root, "external-calls.jsonl"),
		clipboard:           filepath.Join(root, "clipboard"),
		metadata:            filepath.Join(root, "github-metadata.json"),
		threads:             filepath.Join(root, "github-threads.json"),
		pending:             filepath.Join(root, "github-pending.json"),
		pendingComments:     filepath.Join(root, "github-pending-comments.json"),
		pendingCapabilities: filepath.Join(root, "github-pending-capabilities.json"),
		history:             filepath.Join(root, "github-history.json"),
		remote:              filepath.Join(root, "pull-request.git"),
	}
	w.configPath = filepath.Join(w.configHome, "rvw", "config.toml")
	w.themes = filepath.Join(w.configHome, "rvw", "themes")
	for _, path := range []string{
		w.repository,
		w.home,
		filepath.Dir(w.configPath),
		w.themes,
		w.fakeDirectory,
		filepath.Join(root, "tmp"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}
	for _, name := range []string{"pbcopy", "wl-copy", "xclip"} {
		if err := os.Symlink(testExecutable, filepath.Join(w.fakeDirectory, name)); err != nil {
			t.Fatalf("link %s helper: %v", name, err)
		}
	}

	w.environment = []string{
		// Production receives only the bounded clipboard shims. Fixture setup
		// uses systemGit by absolute path before rvw starts, so any runtime
		// attempt to execute git or gh fails deterministically.
		"PATH=" + w.fakeDirectory,
		"HOME=" + w.home,
		"XDG_CONFIG_HOME=" + w.configHome,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(w.home, ".gitconfig"),
		"GITHUB_TOKEN=e2e-token",
		"LC_ALL=C",
		"LANG=C",
		"TERM=xterm-256color",
		"WAYLAND_DISPLAY=rvw-e2e",
		"NO_COLOR=1",
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"GIT_AUTHOR_DATE=2001-02-03T04:05:06Z",
		"GIT_COMMITTER_DATE=2001-02-03T04:05:06Z",
		"RVW_E2E_CALL_LOG=" + w.callLog,
		"RVW_E2E_CLIPBOARD=" + w.clipboard,
		"RVW_E2E_GH_POST_MODE=success",
	}
	activeWorld.Lock()
	activeWorld.value = w
	activeWorld.Unlock()
	w.initializeRepository(t)
	w.initializePullRequest(t)
	w.writeFile(t, w.configPath, "[github]\nfetch_threads = false\n")
	w.writeFile(t, w.threads, "[]")
	w.writeFile(t, w.pending, fmt.Sprintf(
		`[{"errors":[],"data":{"viewer":{"login":"octo-reviewer"},`+
			`"repository":{"nameWithOwner":"fixture/repository","pullRequest":{`+
			`"number":7,"baseRefOid":%q,"headRefOid":%q,`+
			`"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}}]`,
		w.prBase,
		w.prHead,
	))
	w.writeFile(t, w.pendingComments, "[[]]")
	w.writeFile(t, w.pendingCapabilities, "[]")
	w.writeFile(t, w.history, "[[]]")
	t.Cleanup(func() {
		if w.pty != nil {
			w.pty.close()
		}
		activeWorld.Lock()
		if activeWorld.value == w {
			activeWorld.value = nil
		}
		activeWorld.Unlock()
	})
	return w
}

func (w *world) initializeRepository(t testing.TB) {
	runGit(t, w.repository, w.environment, "init", "-b", "main")
	runGit(t, w.repository, w.environment, "config", "user.email", "rvw@example.test")
	runGit(t, w.repository, w.environment, "config", "user.name", "rvw e2e")
	w.writeFile(t, filepath.Join(w.repository, ".gitignore"), "ignored.txt\n")
	w.writeFile(t, filepath.Join(w.repository, ".rvwignore"), "rvwignored.txt\n")
	w.writeFile(t, filepath.Join(w.repository, "tracked.txt"), "alpha\nbeta\ngamma\n")
	w.writeFile(t, filepath.Join(w.repository, "range.txt"), "before\n")
	runGit(t, w.repository, w.environment, "add", ".")
	runGit(t, w.repository, w.environment, "commit", "-m", "base")
	w.baseRevision = strings.TrimSpace(runGit(t, w.repository, w.environment, "rev-parse", "HEAD"))

	w.writeFile(t, filepath.Join(w.repository, "range.txt"), "after\n")
	runGit(t, w.repository, w.environment, "add", "range.txt")
	runGit(t, w.repository, w.environment, "commit", "-m", "head")
	w.headRevision = strings.TrimSpace(runGit(t, w.repository, w.environment, "rev-parse", "HEAD"))

	w.writeFile(t, filepath.Join(w.repository, "tracked.txt"), "alpha\nbeta changed\ngamma\ndelta\n")
	w.writeFile(t, filepath.Join(w.repository, "staged.txt"), "staged contents\n")
	runGit(t, w.repository, w.environment, "add", "staged.txt")
	w.writeFile(t, filepath.Join(w.repository, "untracked.txt"), "untracked contents\n")
	w.writeFile(t, filepath.Join(w.repository, "ignored.txt"), "gitignored contents\n")
	w.writeFile(t, filepath.Join(w.repository, "rvwignored.txt"), "rvwignored contents\n")
	w.writeFile(t, filepath.Join(w.repository, "generated.pb.go"), "package generated\n")
}

func (w *world) initializePullRequest(t testing.TB) {
	seed := filepath.Join(w.root, "pull-request-seed")
	if err := os.MkdirAll(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, w.environment, "init", "-b", "main")
	runGit(t, seed, w.environment, "config", "user.email", "rvw@example.test")
	runGit(t, seed, w.environment, "config", "user.name", "rvw e2e")
	w.writeFile(t, filepath.Join(seed, "reviewed.go"), "package reviewed\n\nfunc Value() int { return 1 }\n")
	runGit(t, seed, w.environment, "add", "reviewed.go")
	runGit(t, seed, w.environment, "commit", "-m", "pull request base")
	w.prBase = strings.TrimSpace(runGit(t, seed, w.environment, "rev-parse", "HEAD"))
	w.writeFile(t, filepath.Join(seed, "reviewed.go"), "package reviewed\n\nfunc Value() int { return 2 }\n")
	runGit(t, seed, w.environment, "add", "reviewed.go")
	runGit(t, seed, w.environment, "commit", "-m", "pull request head")
	w.prHead = strings.TrimSpace(runGit(t, seed, w.environment, "rev-parse", "HEAD"))

	runGit(t, w.root, w.environment, "init", "--bare", w.remote)
	runGit(t, w.remote, w.environment, "config", "uploadpack.allowFilter", "true")
	runGit(t, w.remote, w.environment, "config", "uploadpack.allowTipSHA1InWant", "true")
	runGit(t, w.remote, w.environment, "config", "uploadpack.allowReachableSHA1InWant", "true")
	runGit(t, seed, w.environment, "push", w.remote, w.prBase+":refs/heads/main")
	runGit(t, seed, w.environment, "push", w.remote, w.prHead+":refs/pull/7/head")

	metadata := map[string]any{
		"number":        fixturePR,
		"title":         "Deterministic pull request",
		"html_url":      "https://github.com/fixture/repository/pull/7",
		"state":         "open",
		"merged_at":     nil,
		"draft":         false,
		"changed_files": 1,
		"user":          map[string]any{"login": "octo-reviewer"},
		"base":          map[string]any{"ref": "main", "sha": w.prBase},
		"head":          map[string]any{"ref": "feature", "sha": w.prHead},
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	w.writeBytes(t, w.metadata, encoded)
}

func (w *world) writeConfig(t testing.TB, contents string) {
	t.Helper()
	w.writeFile(t, w.configPath, contents)
}

func (w *world) writeProjectConfig(t testing.TB, contents string) {
	t.Helper()
	w.writeFile(t, filepath.Join(w.repository, ".rvw.toml"), contents)
}

func (w *world) writeFile(t testing.TB, path, contents string) {
	t.Helper()
	w.writeBytes(t, path, []byte(contents))
}

func (w *world) writeBytes(t testing.TB, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (w *world) setEnvironment(name, value string) {
	prefix := name + "="
	for index, item := range w.environment {
		if strings.HasPrefix(item, prefix) {
			w.environment[index] = prefix + value
			return
		}
	}
	w.environment = append(w.environment, prefix+value)
}

func (w *world) unsetEnvironment(name string) {
	prefix := name + "="
	w.environment = slices.DeleteFunc(w.environment, func(item string) bool {
		return strings.HasPrefix(item, prefix)
	})
}

func (w *world) calls(t testing.TB) []externalCall {
	t.Helper()
	contents, err := os.ReadFile(w.callLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []externalCall
	for line := range bytes.Lines(contents) {
		var call externalCall
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatalf("decode external call %q: %v", line, err)
		}
		calls = append(calls, call)
	}
	return calls
}

type processResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
}

func (w *world) run(t testing.TB, args ...string) processResult {
	return w.runWithInput(t, nil, args...)
}

func (w *world) runWithInput(
	t testing.TB,
	input io.Reader,
	args ...string,
) processResult {
	t.Helper()
	command := exec.CommandContext(w.context, rvwBinary, args...)
	command.Dir = w.repository
	command.Env = slices.Clone(w.environment)
	command.Stdin = input
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := processResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.exitCode = exitError.ExitCode()
		} else {
			result.exitCode = -1
		}
	}
	w.last = result
	return result
}

func runGit(t testing.TB, directory string, environment []string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), systemGit, args...)
	command.Dir = directory
	command.Env = slices.Clone(environment)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
