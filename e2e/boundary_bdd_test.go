//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/jacoelho/bdd"
)

func TestOutputBoundaryContract(t *testing.T) {
	bdd.Feature(t, "Git metadata is represented without losing identity", newWorld, func(feature *bdd.Suite[*world]) {
		feature.Scenario("staged output represents structural Git changes", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("the index contains binary mode rename delete and submodule changes", func(t testing.TB, w *world) {
				trackedObject := strings.TrimSpace(runGit(t, w.repository, w.environment, "rev-parse", "HEAD:tracked.txt"))
				runGit(t, w.repository, w.environment, "update-index", "--cacheinfo", "100755,"+trackedObject+",tracked.txt")
				runGit(t, w.repository, w.environment, "mv", "range.txt", "renamed.txt")
				if err := os.Remove(filepath.Join(w.repository, ".rvwignore")); err != nil {
					t.Fatal(err)
				}
				runGit(t, w.repository, w.environment, "add", "-u", ".rvwignore")
				w.writeBytes(t, filepath.Join(w.repository, "binary.dat"), []byte{0, 1, 2, 0xff})
				runGit(t, w.repository, w.environment, "add", "binary.dat")
				runGit(
					t,
					w.repository,
					w.environment,
					"update-index",
					"--add",
					"--cacheinfo",
					"160000,"+w.headRevision+",submodule",
				)
			})
			scenario.When("canonical JSON is requested", invoke("diff", "--staged", "--json"))
			scenario.Then("each Git change kind and mode remains explicit", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				for _, token := range []string{
					`"kind":"binary"`,
					`"kind":"deleted"`,
					`"kind":"mode-only"`,
					`"kind":"rename-only"`,
					`"kind":"submodule"`,
					`"old":"100644"`,
					`"new":"100755"`,
				} {
					if !bytes.Contains(w.last.stdout, []byte(token)) {
						t.Errorf("JSON lacks %s", token)
					}
				}
				if t.Failed() {
					t.Logf("JSON:\n%s", w.last.stdout)
				}
				golden.RequireEqual(t, canonicalJSON(t, w, w.last.stdout))
			})
		})

		feature.Scenario("non-UTF-8 and hostile paths remain terminal safe", func(scenario *bdd.ScenarioSpec[*world]) {
			var rawPath []byte
			scenario.Given("the index contains a non-UTF-8 path and a terminal-control path", func(t testing.TB, w *world) {
				rawPath = []byte{'o', 'd', 'd', '-', 0xff, '.', 't', 'x', 't'}
				stageRawPath(t, w, rawPath, []byte("raw path contents\n"))
				controlPath := "control-\x1b-\u202e.txt"
				w.writeFile(t, filepath.Join(w.repository, controlPath), "hostile display contents\n")
				runGit(t, w.repository, w.environment, "add", controlPath)
			})
			scenario.When("JSON is requested", invoke("diff", "--staged", "--json"))
			scenario.Then("raw identity is encoded and JSON controls remain escaped", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				encoded := base64.StdEncoding.EncodeToString(rawPath)
				if !bytes.Contains(w.last.stdout, []byte(encoded)) || bytes.Contains(w.last.stdout, []byte{0xff}) ||
					bytes.Contains(w.last.stdout, []byte{0x1b}) {
					t.Fatalf("unsafe or lossy JSON:\n%q", w.last.stdout)
				}
				if !bytes.Contains(w.last.stdout, []byte(`\u001b`)) || !json.Valid(w.last.stdout) {
					t.Fatalf("escaped control identity absent:\n%s", w.last.stdout)
				}
			})
		})

		feature.Scenario("built-in traits and independent hidden reasons accumulate", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("generated vendored lock and minified files are changed", func(t testing.TB, w *world) {
				w.writeFile(t, filepath.Join(w.repository, "vendor", "library.go"), "package library\n")
				w.writeFile(t, filepath.Join(w.repository, "package-lock.json"), "{}\n")
				w.writeFile(t, filepath.Join(w.repository, "assets", "application.min.js"), "let x=1;\n")
				w.writeConfig(t, "[filter]\ninclude = [\"tracked.txt\"]\nexclude = [\"generated.pb.go\"]\n")
			})
			scenario.When("worktree JSON is requested", invoke("diff", "--json"))
			scenario.Then("traits and all matching hidden reasons are retained", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				for _, token := range []string{
					`"generated":true`,
					`"vendored":true`,
					`"lock_file":true`,
					`"minified":true`,
					`"builtin-default"`,
					`"include"`,
					`"exclude"`,
				} {
					if !bytes.Contains(w.last.stdout, []byte(token)) {
						t.Errorf("JSON lacks %s", token)
					}
				}
			})
		})
	})
}

func TestLifecycleAndScaleContract(t *testing.T) {
	bdd.Feature(t, "bounded process lifecycle", newWorld, func(feature *bdd.Suite[*world]) {
		feature.Scenario("Ctrl-c belongs to an active modal before it belongs to quit", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("help owns the terminal", func(t testing.TB, w *world) {
				launchStableWorktree()(t, w)
				w.pty.send(t, "?")
				w.pty.awaitScreen(t, "help", containsScreen("Effective help"))
			})
			scenario.When("Ctrl-c is entered", func(t testing.TB, w *world) {
				w.pty.send(t, "\x03")
			})
			scenario.Then("only the modal closes and a later quit restores the terminal", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "workspace after Ctrl-c", containsScreen("staged contents"))
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
				assertTerminalLifecycle(t, w.pty, true)
			})
		})

		feature.Scenario("one thousand changed-file metadata records remain processable", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("the worktree contains exactly one thousand changed paths", func(t testing.TB, w *world) {
				for index := range 994 {
					path := filepath.Join(w.repository, "scale", formatScaleIndex(index)+".txt")
					w.writeFile(t, path, "metadata only\n")
				}
			})
			scenario.When("metadata JSON is requested", invoke("diff", "--json"))
			scenario.Then("the bounded source returns all one thousand files", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				var value struct {
					FileCount int `json:"file_count"`
					Files     []any
				}
				if err := json.Unmarshal(w.last.stdout, &value); err != nil {
					t.Fatal(err)
				}
				if value.FileCount != 1_000 || len(value.Files) != 1_000 {
					t.Fatalf("metadata count = %d/%d, want 1000", value.FileCount, len(value.Files))
				}
			})
		})

		feature.Scenario("a pull-request files-cap failure is recoverable by explicit reinvocation", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("GitHub reports more files than the accepted source limit", func(t testing.TB, w *world) {
				metadata := readJSONObject(t, w.metadata)
				metadata["changed_files"] = 100_001
				writeJSONObject(t, w, w.metadata, metadata)
			})
			scenario.When("the capped pull request is opened", invoke("pr", "7", "--repo", "fixture/repository", "--json"))
			scenario.Then("startup fails before a remote Git fetch", func(t testing.TB, w *world) {
				assertStartupFailure(t, w.last, "pull request has 100001 files; limit is 100000")
				calls := w.calls(t)
				if len(calls) != 1 || calls[0].Command != "gh" {
					t.Fatalf("calls = %+v", calls)
				}
			})
			scenario.When("the corrected metadata is explicitly reinvoked", func(t testing.TB, w *world) {
				metadata := readJSONObject(t, w.metadata)
				metadata["changed_files"] = 1
				writeJSONObject(t, w, w.metadata, metadata)
				w.run(t, "pr", "7", "--repo", "fixture/repository", "--json")
			})
			scenario.Then("the pull request opens successfully", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				assertJSONSource(t, w.last.stdout, "pr")
			})
		})
	})
}

func stageRawPath(t testing.TB, w *world, path, contents []byte) {
	t.Helper()
	hash := exec.CommandContext(t.Context(), systemGit, "hash-object", "-w", "--stdin")
	hash.Dir = w.repository
	hash.Env = slices.Clone(w.environment)
	hash.Stdin = bytes.NewReader(contents)
	output, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	objectID := strings.TrimSpace(string(output))
	index := exec.CommandContext(t.Context(), systemGit, "update-index", "-z", "--index-info")
	index.Dir = w.repository
	index.Env = slices.Clone(w.environment)
	index.Stdin = bytes.NewReader(append(append([]byte("100644 "+objectID+"\t"), path...), 0))
	if output, err := index.CombinedOutput(); err != nil {
		t.Fatalf("stage raw path: %v\n%s", err, output)
	}
}

func formatScaleIndex(index int) string {
	const width = 6
	value := strconv.Itoa(index)
	if len(value) >= width {
		return value
	}
	return strings.Repeat("0", width-len(value)) + value
}

func readJSONObject(t testing.TB, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeJSONObject(t testing.TB, w *world, path string, value map[string]any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	w.writeBytes(t, path, contents)
}
