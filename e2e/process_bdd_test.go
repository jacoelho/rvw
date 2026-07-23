//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/jacoelho/bdd"
)

func TestCommandLineContract(t *testing.T) {
	type helpExample struct {
		args []string
		want string
	}
	helpCases := []bdd.Example[helpExample]{
		bdd.Case("help command", helpExample{[]string{"help"}, "usage:\n"}),
		bdd.Case("global short option", helpExample{[]string{"-h"}, "usage:\n"}),
		bdd.Case("global long option", helpExample{[]string{"--help"}, "usage:\n"}),
		bdd.Case("diff help command", helpExample{[]string{"help", "diff"}, "usage: rvw diff"}),
		bdd.Case("diff option", helpExample{[]string{"diff", "--help"}, "usage: rvw diff"}),
		bdd.Case("pull request help command", helpExample{[]string{"help", "pr"}, "usage: rvw pr"}),
		bdd.Case("pull request option", helpExample{[]string{"pr", "--help"}, "usage: rvw pr"}),
	}

	type invalidExample struct {
		args []string
		want string
	}
	invalidCases := []bdd.Example[invalidExample]{
		bdd.Case("missing command", invalidExample{nil, "expected diff, pr, or help"}),
		bdd.Case("unknown command", invalidExample{[]string{"review"}, `unexpected argument "review"`}),
		bdd.Case("unknown option", invalidExample{[]string{"diff", "--wat"}, "unknown option --wat"}),
		bdd.Case("missing option value", invalidExample{[]string{"diff", "--range"}, "--range requires a value"}),
		bdd.Case("exclusive diff selectors", invalidExample{[]string{"diff", "--staged", "--range", "HEAD~1..HEAD"}, "mutually exclusive"}),
		bdd.Case("staged before command", invalidExample{[]string{"--staged", "diff"}, "--staged is valid only after diff"}),
		bdd.Case("three dot range", invalidExample{[]string{"diff", "--range", "main...HEAD"}, "invalid review target"}),
		bdd.Case("empty range base", invalidExample{[]string{"diff", "--range", "..HEAD"}, "invalid review target"}),
		bdd.Case("range base begins with dash", invalidExample{[]string{"diff", "--range", "-base..HEAD"}, "invalid review target"}),
		bdd.Case("range head begins with dot", invalidExample{[]string{"diff", "--range", "main...bad"}, "invalid review target"}),
		bdd.Case("missing pull request selector", invalidExample{[]string{"pr"}, "pr requires a selector"}),
		bdd.Case("zero pull request", invalidExample{[]string{"pr", "0", "--repo", "fixture/repository"}, "invalid review target"}),
		bdd.Case("overflowing pull request", invalidExample{[]string{"pr", "18446744073709551616", "--repo", "fixture/repository"}, "invalid review target"}),
		bdd.Case("noncanonical pull request URL", invalidExample{[]string{"pr", "http://github.com/fixture/repository/pull/7"}, "invalid review target"}),
		bdd.Case("invalid repository", invalidExample{[]string{"pr", "7", "--repo", "-bad/repository"}, "invalid review target"}),
		bdd.Case("invalid theme path", invalidExample{[]string{"diff", "--theme", "../dark"}, "invalid configuration"}),
	}

	bdd.Feature(t, "command line parsing precedes composition", newWorld, func(feature *bdd.Suite[*world]) {
		bdd.Outline(feature, "every documented help form succeeds", helpCases, func(example helpExample, scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("rvw is invoked", invoke(example.args...))
			scenario.Then("help is written to stdout", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				if !strings.HasPrefix(string(w.last.stdout), example.want) || len(w.last.stderr) != 0 {
					t.Fatalf("stdout=%q stderr=%q", w.last.stdout, w.last.stderr)
				}
			})
		})

		bdd.Outline(feature, "invalid invocations fail safely", invalidCases, func(example invalidExample, scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("rvw is invoked", invoke(example.args...))
			scenario.Then("startup fails before external composition", func(t testing.TB, w *world) {
				assertStartupFailure(t, w.last, example.want)
				if calls := w.calls(t); len(calls) != 0 {
					t.Fatalf("invalid invocation called external helpers: %+v", calls)
				}
			})
		})

		feature.Scenario("invalid UTF-8 is rejected before composition", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("an argument is not UTF-8", invoke("diff", "--include", string([]byte{0xff})))
			scenario.Then("the terminal-safe usage error is returned", func(t testing.TB, w *world) {
				assertStartupFailure(t, w.last, "argument is not valid UTF-8")
			})
		})

		feature.Scenario("command-line failure wins over invalid configuration", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("the global configuration is invalid", func(t testing.TB, w *world) {
				w.writeConfig(t, "unknown = true\n")
			})
			scenario.When("an unknown option is supplied", invoke("diff", "--unknown"))
			scenario.Then("only the command-line defect is reported", func(t testing.TB, w *world) {
				assertStartupFailure(t, w.last, "unknown option --unknown")
				if bytes.Contains(w.last.stderr, []byte("configuration")) {
					t.Fatalf("configuration composed before CLI validation: %q", w.last.stderr)
				}
			})
		})

		feature.Scenario("common options may surround the subcommand and the last output wins", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When(
				"repeatable filters and output modes surround diff",
				invoke("--include", "*.txt", "--stdout", "diff", "--exclude", "range.txt", "--json"),
			)
			scenario.Then("JSON is the selected output", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				var document map[string]any
				if err := json.Unmarshal(w.last.stdout, &document); err != nil {
					t.Fatalf("decode JSON: %v\n%s", err, w.last.stdout)
				}
			})
		})

		feature.Scenario("default interactive mode rejects null stdin and piped stdout", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("rvw is invoked without terminal streams", invoke("diff"))
			scenario.Then("startup fails before terminal or source composition", func(t testing.TB, w *world) {
				assertInteractiveTerminalFailure(t, w.last)
			})
		})

		feature.Scenario("piped quit input does not start the interactive program", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("quit is piped to default interactive mode", func(t testing.TB, w *world) {
				w.runWithInput(t, strings.NewReader("q"), "diff")
			})
			scenario.Then("startup fails without emitting terminal controls", func(t testing.TB, w *world) {
				assertInteractiveTerminalFailure(t, w.last)
			})
		})

		feature.Scenario("nonterminal pull-request mode is rejected before GitHub", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("a pull request is requested without terminal streams", invoke("pr", "7", "--repo", "fixture/repository"))
			scenario.Then("startup fails without calling GitHub", func(t testing.TB, w *world) {
				assertInteractiveTerminalFailure(t, w.last)
				if calls := w.calls(t); len(calls) != 0 {
					t.Fatalf("terminal admission called external helpers: %+v", calls)
				}
			})
		})
	})
}

func TestSourceAndOutputContract(t *testing.T) {
	type example struct {
		args          func(*world) []string
		wantSource    string
		wantFile      string
		externalCalls int
	}
	cases := []bdd.Example[example]{
		bdd.Case("worktree human", example{func(*world) []string { return []string{"diff", "--stdout"} }, "", "tracked.txt", 0}),
		bdd.Case("worktree JSON", example{func(*world) []string { return []string{"diff", "--json"} }, "diff", "tracked.txt", 0}),
		bdd.Case("staged human", example{func(*world) []string { return []string{"diff", "--staged", "--stdout"} }, "", "staged.txt", 0}),
		bdd.Case("staged JSON", example{func(*world) []string { return []string{"diff", "--staged", "--json"} }, "diff", "staged.txt", 0}),
		bdd.Case("range human", example{func(w *world) []string {
			return []string{"diff", "--range", w.baseRevision + ".." + w.headRevision, "--stdout"}
		}, "", "range.txt", 0}),
		bdd.Case("range JSON", example{func(w *world) []string {
			return []string{"diff", "--range", w.baseRevision + ".." + w.headRevision, "--json"}
		}, "diff", "range.txt", 0}),
		bdd.Case("numeric pull request human", example{func(*world) []string { return []string{"pr", "7", "--repo", "fixture/repository", "--stdout"} }, "", "reviewed.go", 1}),
		bdd.Case("URL pull request JSON", example{func(*world) []string { return []string{"pr", "https://github.com/fixture/repository/pull/7", "--json"} }, "pr", "reviewed.go", 1}),
	}

	bdd.Feature(t, "source and output matrix", newWorld, func(feature *bdd.Suite[*world]) {
		bdd.Outline(feature, "compiled rvw emits the accepted output", cases, func(example example, scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("the source is opened in a noninteractive mode", func(t testing.TB, w *world) {
				w.run(t, example.args(w)...)
			})
			scenario.Then("the source output is contract complete", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				if !bytes.Contains(w.last.stdout, []byte(example.wantFile)) {
					t.Fatalf("output does not contain %q:\n%s", example.wantFile, w.last.stdout)
				}
				if example.wantSource != "" {
					assertJSONSource(t, w.last.stdout, example.wantSource)
					golden.RequireEqual(t, canonicalJSON(t, w, w.last.stdout))
				} else {
					golden.RequireEqual(t, string(w.last.stdout))
				}
				calls := w.calls(t)
				if example.externalCalls == 0 && len(calls) != 0 {
					t.Fatalf("unexpected external activity: %+v", calls)
				}
				if example.externalCalls != 0 &&
					(!slices.ContainsFunc(calls, func(call externalCall) bool {
						return call.Command == "github-http"
					}) || !slices.ContainsFunc(calls, func(call externalCall) bool {
						return call.Command == "smart-git"
					})) {
					t.Fatalf("native GitHub/smart-Git activity is incomplete: %+v", calls)
				}
			})
		})
	})
}

func TestConfigurationContract(t *testing.T) {
	bdd.Feature(t, "configuration boundaries and precedence", newWorld, func(feature *bdd.Suite[*world]) {
		feature.Scenario("the complete user schema and a custom theme compose", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("every user configuration table is populated", func(t testing.TB, w *world) {
				w.writeFile(t, filepath.Join(w.themes, "contract.toml"), customTheme)
				w.writeConfig(t, completeConfiguration)
			})
			scenario.When("a CLI appearance override selects the user theme", invoke("diff", "--theme", "contract", "--appearance", "dark", "--json"))
			scenario.Then("composition succeeds with deterministic filtering", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				if !bytes.Contains(w.last.stdout, []byte(`"hidden_reasons":["include"`)) {
					t.Fatalf("configured include filter was not applied:\n%s", w.last.stdout)
				}
			})
		})

		feature.Scenario("project keybindings are rejected", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a project attempts to redefine a key", func(t testing.TB, w *world) {
				w.writeProjectConfig(t, "[[ui.keybindings]]\ncontext = \"normal\"\nchord = \"x\"\naction = \"quit\"\n")
			})
			scenario.When("rvw composes configuration", invoke("diff", "--stdout"))
			scenario.Then("the owning boundary rejects the project binding", func(t testing.TB, w *world) {
				assertStartupFailure(t, w.last, "project keybindings are forbidden")
			})
		})

		feature.Scenario("CLI filtering has highest precedence", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("global and project filters disagree", func(t testing.TB, w *world) {
				w.writeConfig(t, "[filter]\ninclude = \"range.txt\"\n")
				w.writeProjectConfig(t, "[filter]\ninclude = [\"tracked.txt\"]\n")
			})
			scenario.When("the CLI adds its own include", invoke("diff", "--include", "staged.txt", "--json"))
			scenario.Then("all merged independent patterns are represented", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
				if !bytes.Contains(w.last.stdout, []byte(`"hidden_reasons":["include"`)) {
					t.Fatalf("merged include filtering is absent:\n%s", w.last.stdout)
				}
			})
		})

		feature.Scenario("NO_COLOR is accepted without suppressing explicit theme selection", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("an explicit built-in theme is requested under NO_COLOR", invoke("diff", "--theme", "gruvbox-light", "--stdout"))
			scenario.Then("the source still composes", func(t testing.TB, w *world) {
				assertProcessSuccess(t, w.last)
			})
		})
	})
}

func invoke(args ...string) bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		w.run(t, args...)
	}
}

func assertProcessSuccess(t testing.TB, result processResult) {
	t.Helper()
	if result.err != nil || result.exitCode != 0 || len(result.stderr) != 0 {
		t.Fatalf("rvw failed: exit=%d err=%v\nstdout:\n%s\nstderr:\n%s", result.exitCode, result.err, result.stdout, result.stderr)
	}
}

func assertStartupFailure(t testing.TB, result processResult, want string) {
	t.Helper()
	if result.exitCode != 1 || result.err == nil {
		t.Fatalf("exit=%d err=%v, want status 1\nstdout=%q\nstderr=%q", result.exitCode, result.err, result.stdout, result.stderr)
	}
	if len(result.stdout) != 0 || !strings.HasPrefix(string(result.stderr), "rvw: ") ||
		!strings.Contains(string(result.stderr), want) {
		t.Fatalf("stdout=%q stderr=%q, want error containing %q", result.stdout, result.stderr, want)
	}
	if !utf8.Valid(result.stderr) {
		t.Fatalf("stderr is not UTF-8: %q", result.stderr)
	}
	for _, character := range string(result.stderr) {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			t.Fatalf("stderr contains terminal control U+%04X: %q", character, result.stderr)
		}
	}
}

func assertInteractiveTerminalFailure(t testing.TB, result processResult) {
	t.Helper()
	const diagnostic = "interactive mode requires terminal stdin and stdout; use --stdout or --json for noninteractive output"
	assertStartupFailure(t, result, diagnostic)
	if len(result.stdout) != 0 || bytes.Contains(result.stderr, []byte{0x1b}) ||
		string(result.stderr) != "rvw: "+diagnostic+"\n" {
		t.Fatalf("stdout=%q stderr=%q", result.stdout, result.stderr)
	}
}

func assertJSONSource(t testing.TB, output []byte, want string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var document struct {
		Source    string      `json:"source"`
		SourceID  json.Number `json:"source_id"`
		FileCount json.Number `json:"file_count"`
		Files     []struct {
			ID          json.Number `json:"id"`
			PatchHandle struct {
				ID       json.Number `json:"id"`
				SourceID json.Number `json:"source_id"`
				FileID   json.Number `json:"file_id"`
			} `json:"patch_handle"`
		} `json:"files"`
	}
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output)
	}
	if document.Source != want || document.SourceID.String() != "1" || document.FileCount.String() == "0" {
		t.Fatalf("unexpected JSON identity: %+v", document)
	}
	for _, file := range document.Files {
		if file.ID.String() != file.PatchHandle.FileID.String() ||
			file.PatchHandle.SourceID.String() != document.SourceID.String() {
			t.Fatalf("cross-inconsistent patch handle: %+v", file)
		}
	}
}

func canonicalJSON(t testing.TB, w *world, output []byte) string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	replacements := map[string]string{
		w.repository: "<repository>",
		w.remote:     "<pull-request-remote>",
	}
	for path, replacement := range replacements {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("resolve sandbox path %q: %v", path, err)
		}
		replacements[resolved] = replacement
	}
	normalizeJSONStrings(value, replacements)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

func normalizeJSONStrings(value any, replacements map[string]string) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			normalizeJSONStrings(item, replacements)
		}
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok {
				if replacement, exists := replacements[text]; exists {
					typed[key] = replacement
				}
				continue
			}
			normalizeJSONStrings(item, replacements)
		}
	}
}

const completeConfiguration = `mouse = false
theme = "dark"
theme_dark = "gruvbox-dark"
theme_light = "gruvbox-light"
appearance = "system"

[ui]
mouse = true
theme = "contract"
theme_dark = "contract"
theme_light = "contract"
appearance = "light"

[[ui.keybindings]]
context = "normal.diff"
chord = "x t"
action = "toggle-wrap"

[diff]
wrap = false
view = "side-by-side"
context_lines = 0
include_untracked = true

[filter]
builtin_defaults = true
gitignore = true
use_gitignore = false
rvwignore = true
ignore_file = ".rvwignore"
include = "tracked.txt"
exclude = ["never/**"]

[github]
submit_default = "approve"
fetch_threads = false

[export]
markdown_template = "# Contract {{comment_count}}\n\n{{comments}}"
comment_template = "{{index}} {{target}} {{path}} {{line_or_range}} {{comment}}"
`

const customTheme = `panel_bg = "#101010"
fg_primary = "white"
fg_secondary = "gray"
fg_dim = "darkgray"
diff_add = "green"
diff_del = "red"
diff_context = "white"
pending = "yellow"
border_focused = "cyan"
status_bar_bg = "#202020"
cursor_line_bg = "#303030"
diff_add_bg = "#102010"
diff_del_bg = "#201010"
diff_hunk_header = "blue"
message_warning_fg = "yellow"
message_warning_bg = "black"
`
