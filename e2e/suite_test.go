//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
)

func TestMain(m *testing.M) {
	if helper := filepath.Base(os.Args[0]); slices.Contains(
		[]string{"gh", "pbcopy", "wl-copy", "xclip"},
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
	build := exec.Command("go", "build", "-trimpath", "-o", rvwBinary, "./cmd/rvw")
	build.Dir = repositoryRoot()
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "build rvw E2E binary: %v\n%s", buildErr, output)
		_ = os.RemoveAll(suiteTemporary)
		os.Exit(1)
	}

	status := m.Run()
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
	case "gh":
		return runGHHelper(args, stdin)
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

func runGHHelper(args []string, stdin []byte) int {
	if slices.Equal(args, []string{"repo", "view", "--json", "nameWithOwner"}) {
		_, _ = io.WriteString(os.Stdout, `{"nameWithOwner":"`+fixtureOwner+`/`+fixtureRepository+`"}`)
		return 0
	}
	if len(args) == 2 && args[0] == "api" && strings.HasPrefix(args[1], "repos/") &&
		strings.HasSuffix(args[1], "/pulls/"+strconv.FormatUint(fixturePR, 10)) {
		return copyHelperFixture("RVW_E2E_GH_METADATA")
	}
	if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		path := os.Getenv("RVW_E2E_GH_THREADS")
		if path == "" {
			return helperFailure("RVW_E2E_GH_THREADS is unset")
		}
		return copyHelperFile(path)
	}
	if len(args) == 6 && args[0] == "api" && args[1] == "--method" && args[2] == "POST" &&
		strings.HasSuffix(args[3], "/reviews") && args[4] == "--input" && args[5] == "-" {
		if !json.Valid(stdin) {
			return helperFailure("publication stdin is not JSON")
		}
		switch os.Getenv("RVW_E2E_GH_POST_MODE") {
		case "", "success":
			_, _ = io.WriteString(os.Stdout, `{"id":9001}`)
			return 0
		case "unknown", "unknown-present":
			if os.Getenv("RVW_E2E_GH_POST_MODE") == "unknown-present" {
				var payload struct {
					Body string `json:"body"`
				}
				if err := json.Unmarshal(stdin, &payload); err != nil {
					return helperFailure("decode ambiguous publication: %v", err)
				}
				history, err := json.Marshal([][]map[string]any{{{
					"id":   9002,
					"body": "remote body\n" + payload.Body,
				}}})
				if err != nil {
					return helperFailure("encode reconciliation history: %v", err)
				}
				if err := os.WriteFile(os.Getenv("RVW_E2E_GH_HISTORY"), history, 0o600); err != nil {
					return helperFailure("write reconciliation history: %v", err)
				}
			}
			_, _ = io.WriteString(os.Stderr, "simulated ambiguous GitHub write\n")
			return 75
		case "invalid-response":
			_, _ = io.WriteString(os.Stdout, `{}`)
			return 0
		default:
			return helperFailure("unsupported RVW_E2E_GH_POST_MODE")
		}
	}
	if len(args) == 4 && args[0] == "api" && strings.HasSuffix(args[1], "/reviews") &&
		args[2] == "--paginate" && args[3] == "--slurp" {
		path := os.Getenv("RVW_E2E_GH_HISTORY")
		if path == "" {
			_, _ = io.WriteString(os.Stdout, "[[]]")
			return 0
		}
		return copyHelperFile(path)
	}
	return helperFailure("unsupported gh argv: %q", args)
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

func copyHelperFixture(environmentName string) int {
	path := os.Getenv(environmentName)
	if path == "" {
		return helperFailure("%s is unset", environmentName)
	}
	return copyHelperFile(path)
}

func copyHelperFile(path string) int {
	contents, err := os.ReadFile(path)
	if err != nil {
		return helperFailure("read fixture: %v", err)
	}
	if _, err := os.Stdout.Write(contents); err != nil {
		return helperFailure("write fixture: %v", err)
	}
	return 0
}

func helperFailure(format string, args ...any) int {
	_, _ = fmt.Fprintf(os.Stderr, "rvw-e2e-helper: "+format+"\n", args...)
	return 64
}

type world struct {
	context       context.Context
	cancel        context.CancelFunc
	root          string
	repository    string
	home          string
	configHome    string
	configPath    string
	themes        string
	fakeDirectory string
	callLog       string
	clipboard     string
	metadata      string
	threads       string
	history       string
	remote        string
	baseRevision  string
	headRevision  string
	prBase        string
	prHead        string
	environment   []string
	last          processResult
	pty           *ptySession
}

func newWorld(t testing.TB) *world {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)

	root := t.TempDir()
	w := &world{
		context:       ctx,
		cancel:        cancel,
		root:          root,
		repository:    filepath.Join(root, "repository"),
		home:          filepath.Join(root, "home"),
		configHome:    filepath.Join(root, "config"),
		fakeDirectory: filepath.Join(root, "bin"),
		callLog:       filepath.Join(root, "external-calls.jsonl"),
		clipboard:     filepath.Join(root, "clipboard"),
		metadata:      filepath.Join(root, "github-metadata.json"),
		threads:       filepath.Join(root, "github-threads.json"),
		history:       filepath.Join(root, "github-history.json"),
		remote:        filepath.Join(root, "pull-request.git"),
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
	for _, name := range []string{"gh", "pbcopy", "wl-copy", "xclip"} {
		if err := os.Symlink(testExecutable, filepath.Join(w.fakeDirectory, name)); err != nil {
			t.Fatalf("link %s helper: %v", name, err)
		}
	}

	path := strings.Join([]string{
		w.fakeDirectory,
		filepath.Dir(systemGit),
		"/usr/bin",
		"/bin",
	}, string(os.PathListSeparator))
	w.environment = []string{
		"PATH=" + path,
		"HOME=" + w.home,
		"XDG_CONFIG_HOME=" + w.configHome,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(w.home, ".gitconfig"),
		"GH_CONFIG_DIR=" + filepath.Join(w.configHome, "gh"),
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
		"RVW_E2E_GH_METADATA=" + w.metadata,
		"RVW_E2E_GH_THREADS=" + w.threads,
		"RVW_E2E_GH_HISTORY=" + w.history,
		"RVW_E2E_GH_POST_MODE=success",
	}
	w.initializeRepository(t)
	w.initializePullRequest(t)
	w.writeFile(t, w.configPath, "[github]\nfetch_threads = false\n")
	w.writeFile(t, w.threads, "[]")
	w.writeFile(t, w.history, "[[]]")
	t.Cleanup(func() {
		if w.pty != nil {
			w.pty.close()
		}
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
	runGit(t, seed, w.environment, "push", w.remote, w.prBase+":refs/heads/main")
	runGit(t, seed, w.environment, "push", w.remote, w.prHead+":refs/pull/7/head")
	runGit(t, w.repository, w.environment, "config", "--global", "protocol.file.allow", "always")
	runGit(
		t,
		w.repository,
		w.environment,
		"config",
		"--global",
		"url.file://"+w.remote+".insteadOf",
		"https://github.com/"+fixtureOwner+"/"+fixtureRepository+".git",
	)

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
	t.Helper()
	command := exec.CommandContext(w.context, rvwBinary, args...)
	command.Dir = w.repository
	command.Env = slices.Clone(w.environment)
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
