package source

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestLimitedPackReaderAcceptsExactLimitAndRejectsNextByte(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		value   string
		want    string
		limited bool
	}{
		{name: "below", value: "abc", want: "abc"},
		{name: "exact", value: "abcd", want: "abcd"},
		{name: "above", value: "abcde", want: "abcd", limited: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &limitedPackReader{reader: strings.NewReader(test.value), limit: 4}
			value, err := io.ReadAll(reader)
			if string(value) != test.want {
				t.Fatalf("value = %q, want %q", value, test.want)
			}
			var limit *LimitError
			if errors.As(err, &limit) != test.limited {
				t.Fatalf("error = %v, limited=%v", err, test.limited)
			}
		})
	}
}

func TestPackLayerExpandedLimitAndRequiredType(t *testing.T) {
	workspace := t.TempDir()
	session, err := createPullRequestRepository(workspace, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	layer, err := session.newPackLayer()
	if err != nil {
		t.Fatal(err)
	}
	defer layer.closeAndRemove()
	object := &plumbing.MemoryObject{}
	object.SetType(plumbing.BlobObject)
	writer, err := object.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "four"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	expectedHash := plumbing.ComputeHash(plumbing.BlobObject, []byte("four"))
	if err := os.MkdirAll(
		filepath.Join(layer.path, "objects", expectedHash.String()[:2]),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	hash, err := layer.storage.SetEncodedObject(object)
	if err != nil {
		t.Fatal(err)
	}
	if expanded, err := validatePackLayer(
		layer.storage, []plumbing.Hash{hash}, plumbing.BlobObject, 4,
	); err != nil || expanded != 4 {
		t.Fatalf("exact expanded limit = %d, %v", expanded, err)
	}
	if _, err := validatePackLayer(
		layer.storage, []plumbing.Hash{hash}, plumbing.BlobObject, 3,
	); !errors.Is(err, ErrSourceLimited) {
		t.Fatalf("over-limit error = %v, want limited", err)
	}
	if _, err := validatePackLayer(
		layer.storage, []plumbing.Hash{hash}, plumbing.TreeObject, 4,
	); err == nil || !strings.Contains(err.Error(), "missing required tree") {
		t.Fatalf("wrong required type error = %v", err)
	}
}

func TestPullRequestUsesNativeGitHubAndFilteredSmartGit(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, seed, "init", "-b", "main")
	runGitTest(t, seed, "config", "user.email", "rvw@example.test")
	runGitTest(t, seed, "config", "user.name", "rvw test")
	writeFile(t, filepath.Join(seed, "reviewed.go"), "package reviewed\n\nconst Value = 1\n")
	runGitTest(t, seed, "add", "reviewed.go")
	runGitTest(t, seed, "commit", "-m", "base")
	base := strings.TrimSpace(runGitTest(t, seed, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(seed, "reviewed.go"), "package reviewed\n\nconst Value = 2\n")
	runGitTest(t, seed, "commit", "-am", "head")
	head := strings.TrimSpace(runGitTest(t, seed, "rev-parse", "HEAD"))

	projectRoot := filepath.Join(root, "projects")
	bare := filepath.Join(projectRoot, "fixture", "repository.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, seed, "init", "--bare", bare)
	runGitTest(t, bare, "config", "uploadpack.allowFilter", "true")
	runGitTest(t, bare, "config", "uploadpack.allowTipSHA1InWant", "true")
	runGitTest(t, bare, "config", "uploadpack.allowReachableSHA1InWant", "true")
	runGitTest(t, seed, "push", bare, base+":refs/heads/main", head+":refs/pull/7/head")

	metadata := fmt.Sprintf(
		`{"number":7,"title":"Native PR","html_url":"https://github.com/fixture/repository/pull/7",`+
			`"state":"open","merged_at":null,"draft":false,"changed_files":1,`+
			`"user":{"login":"reviewer"},"base":{"ref":"main","sha":%q},`+
			`"head":{"ref":"feature","sha":%q}}`,
		base,
		head,
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/repos/fixture/repository/pulls/7" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, metadata)
			return
		}
		serveGitHTTPBackend(t, projectRoot, writer, request)
	}))
	defer server.Close()

	token, err := NewGitHubToken("test-token")
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := NewWorkingDirectory(seed)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(workingDirectory, ServiceOptions{GitHubToken: &token})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestService(t, service)
	service.github.apiBase = server.URL
	service.github.gitBase = server.URL

	repository, err := NewOwnerRepository("fixture/repository")
	if err != nil {
		t.Fatal(err)
	}
	selector, err := NewPullRequestSelector("7", &repository)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewPullRequestTarget(selector)
	if err != nil {
		t.Fatal(err)
	}
	document, documentRevision, err := service.Open(t.Context(), OpenRequest{Target: target, Options: OpenOptions{BuiltinDefaults: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRevision(t, documentRevision)
	file, ok := document.Snapshot().File(0)
	if !ok || document.Snapshot().FileCount() != 1 || file.DisplayPath() != "reviewed.go" {
		t.Fatalf("pull-request snapshot = %+v, present=%v", document.Snapshot(), ok)
	}
}

func serveGitHTTPBackend(
	t *testing.T,
	projectRoot string,
	writer http.ResponseWriter,
	request *http.Request,
) {
	t.Helper()
	command := exec.CommandContext(request.Context(), "git", "http-backend")
	command.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+projectRoot,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+request.URL.Path,
		"QUERY_STRING="+request.URL.RawQuery,
		"REQUEST_METHOD="+request.Method,
		"CONTENT_TYPE="+request.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.FormatInt(request.ContentLength, 10),
		"REMOTE_USER=rvw-test",
	)
	command.Stdin = request.Body
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		http.Error(writer, "git http-backend: "+err.Error()+": "+stderr.String(), http.StatusInternalServerError)
		return
	}
	reader := bufio.NewReader(&stdout)
	status := http.StatusOK
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			http.Error(writer, "malformed git http-backend response", http.StatusInternalServerError)
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			http.Error(writer, "malformed git http-backend header", http.StatusInternalServerError)
			return
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			code, parseErr := strconv.Atoi(strings.Fields(value)[0])
			if parseErr != nil {
				http.Error(writer, "malformed git http-backend status", http.StatusInternalServerError)
				return
			}
			status = code
			continue
		}
		writer.Header().Add(name, value)
	}
	writer.WriteHeader(status)
	_, _ = io.Copy(writer, reader)
}
