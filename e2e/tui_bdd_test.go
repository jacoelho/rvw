//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/jacoelho/bdd"
)

func TestTerminalRenderingContract(t *testing.T) {
	type sizeExample struct {
		width   int
		height  int
		ready   string
		minimum bool
	}
	sizes := []bdd.Example[sizeExample]{
		bdd.Case("wide", sizeExample{180, 36, "staged contents", false}),
		bdd.Case("normal", sizeExample{120, 24, "staged contents", false}),
		bdd.Case("compact", sizeExample{80, 18, "staged contents", false}),
		bdd.Case("narrow", sizeExample{48, 14, "staged contents", false}),
		bdd.Case("below minimum", sizeExample{19, 5, "rvw needs at least", true}),
	}

	bdd.Feature(t, "terminal rendering is reconstructed from a real PTY", newWorld, func(feature *bdd.Suite[*world]) {
		bdd.Outline(feature, "layout breakpoints render stable monochrome screens", sizes, func(example sizeExample, scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("rvw starts at the requested terminal size", func(t testing.TB, w *world) {
				w.launchPTY(t, example.width, example.height, "diff")
			})
			scenario.Then("the final screen matches the readable golden", func(t testing.TB, w *world) {
				screen := w.pty.awaitScreen(t, "stable layout", func(screen string) bool {
					if example.minimum {
						return strings.Contains(screen, example.ready)
					}
					return strings.Contains(screen, example.ready) && strings.Contains(screen, "work 0/64")
				})
				golden.RequireEqual(t, screen)
			})
			scenario.Then("the terminal protocol and restoration contracts hold", func(t testing.TB, w *world) {
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
				assertTerminalLifecycle(t, w.pty, true)
			})
		})

		feature.Scenario("explicit color preserves semantic styling", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("NO_COLOR is absent and a theme is explicit", func(_ testing.TB, w *world) {
				w.unsetEnvironment("NO_COLOR")
			})
			scenario.When("the worktree opens", func(t testing.TB, w *world) {
				w.launchPTY(t, 120, 24, "diff", "--theme", "gruvbox-dark")
			})
			scenario.Then("the virtual terminal contains colored semantic cells", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "colored stable screen", func(screen string) bool {
					return strings.Contains(screen, "staged contents") && strings.Contains(screen, "work 0/64")
				})
				if !w.pty.hasStyledCells() {
					t.Fatalf("screen has no colored or emphasized cells\n%s", w.pty.diagnostics(t))
				}
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
			})
		})

		feature.Scenario("no-mouse suppresses mouse tracking", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("rvw starts with mouse disabled", func(t testing.TB, w *world) {
				w.launchPTY(t, 120, 24, "diff", "--no-mouse")
			})
			scenario.Then("no mouse enable sequence is emitted", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "stable no-mouse screen", func(screen string) bool {
					return strings.Contains(screen, "staged contents") && strings.Contains(screen, "work 0/64")
				})
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
				raw := w.pty.rawTranscript()
				for _, sequence := range [][]byte{[]byte("\x1b[?1000h"), []byte("\x1b[?1002h"), []byte("\x1b[?1003h")} {
					if bytes.Contains(raw, sequence) {
						t.Fatalf("mouse enable sequence %q emitted\n%s", sequence, w.pty.diagnostics(t))
					}
				}
			})
		})
	})
}

func TestInteractiveSourceContract(t *testing.T) {
	type example struct {
		args func(*world) []string
		want string
	}
	cases := []bdd.Example[example]{
		bdd.Case("worktree", example{func(*world) []string { return []string{"diff"} }, "staged.txt"}),
		bdd.Case("staged", example{func(*world) []string { return []string{"diff", "--staged"} }, "staged.txt"}),
		bdd.Case("range", example{func(w *world) []string { return []string{"diff", "--range", w.baseRevision + ".." + w.headRevision} }, "range.txt"}),
		bdd.Case("pull request", example{func(*world) []string { return []string{"pr", "7", "--repo", "fixture/repository"} }, "reviewed.go"}),
	}

	bdd.Feature(t, "all review sources enter the TUI", newWorld, func(feature *bdd.Suite[*world]) {
		bdd.Outline(feature, "interactive source opens and quits cleanly", cases, func(example example, scenario *bdd.ScenarioSpec[*world]) {
			scenario.When("the source is opened", func(t testing.TB, w *world) {
				w.launchPTY(t, 120, 24, example.args(w)...)
			})
			scenario.Then("the source is visible and terminal state is restored", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "source file", func(screen string) bool {
					return strings.Contains(screen, example.want) && strings.Contains(screen, "work 0/64")
				})
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
				assertTerminalLifecycle(t, w.pty, true)
			})
		})
	})
}

func TestTerminalBehaviorAndExportContract(t *testing.T) {
	bdd.Feature(t, "interactive review journeys", newWorld, func(feature *bdd.Suite[*world]) {
		feature.Scenario("help search command and clean quit route through their contexts", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable worktree screen", launchStableWorktree())
			scenario.When("help is opened and closed", func(t testing.TB, w *world) {
				w.pty.send(t, "?")
				w.pty.awaitScreen(t, "help", containsScreen("Effective help"))
				w.pty.send(t, "q")
				w.pty.awaitScreen(t, "workspace after help", containsScreen("staged contents"))
			})
			scenario.When("a current-file search completes", func(t testing.TB, w *world) {
				w.pty.send(t, "/")
				w.pty.awaitScreen(t, "search prompt", containsScreen("Search changed patches"))
				w.pty.send(t, "staged\r")
				w.pty.awaitScreen(t, "search result", func(screen string) bool {
					return strings.Contains(screen, "search hits") && strings.Contains(screen, "work 0/64")
				})
			})
			scenario.When("the command prompt opens help", func(t testing.TB, w *world) {
				w.pty.send(t, ":")
				w.pty.awaitScreen(t, "command prompt", containsScreen("Command"))
				w.pty.send(t, "help\r")
				w.pty.awaitScreen(t, "command-selected help", containsScreen("Effective help"))
				w.pty.send(t, "q")
			})
			scenario.Then("a clean quit restores the terminal", func(t testing.TB, w *world) {
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
				assertTerminalLifecycle(t, w.pty, true)
			})
		})

		feature.Scenario("a saved comment fences quit and exports exact Markdown", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable worktree screen", launchStableWorktree())
			scenario.When("a line comment is saved", saveLineComment("Please keep this deterministic."))
			scenario.When("quit is declined", func(t testing.TB, w *world) {
				w.pty.send(t, "q")
				w.pty.awaitScreen(t, "dirty quit confirmation", containsScreen("Discard local review changes and quit?"))
				w.pty.send(t, "n")
				w.pty.awaitScreen(t, "workspace after declined quit", containsScreen("staged contents"))
			})
			scenario.When("Markdown is delivered through the fake clipboard", func(t testing.TB, w *world) {
				w.pty.send(t, "y")
				w.pty.awaitScreen(t, "export picker", containsScreen("Export selection"))
				w.pty.send(t, "\r")
				w.pty.awaitScreen(t, "clipboard delivery", containsScreen("export delivered"))
			})
			scenario.Then("the exact Markdown and helper argv are observable", func(t testing.TB, w *world) {
				contents, err := os.ReadFile(w.clipboard)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(contents, []byte(`Please keep this deterministic\.`)) ||
					!bytes.Contains(contents, []byte(`staged\.txt:1`)) {
					t.Fatalf("clipboard Markdown:\n%s", contents)
				}
				golden.RequireEqual(t, string(contents))
				calls := w.calls(t)
				if len(calls) != 1 || calls[0].Command == "gh" || calls[0].Stdin != string(contents) {
					t.Fatalf("clipboard calls = %+v", calls)
				}
				w.pty.send(t, ":q!\r")
				w.pty.awaitExit(t)
			})
		})

		feature.Scenario("clean reload incorporates a repository mutation", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable worktree screen", launchStableWorktree())
			scenario.When("the worktree changes and reload is requested", func(t testing.TB, w *world) {
				w.writeFile(t, filepath.Join(w.repository, "reloaded.txt"), "appeared during review\n")
				w.pty.send(t, "\x12")
			})
			scenario.Then("the replacement source is reconciled", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "reload completion", func(screen string) bool {
					return strings.Contains(screen, "[source:2 workspace:2]") &&
						strings.Contains(screen, "reloaded.txt") &&
						strings.Contains(screen, "work 0/64")
				})
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
			})
		})

		feature.Scenario("clipboard failure is explicit and leaves the review dirty", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("clipboard delivery will fail", func(_ testing.TB, w *world) {
				w.setEnvironment("RVW_E2E_CLIPBOARD_FAIL", "1")
			})
			scenario.Given("a stable worktree with one comment", bdd.Compose(launchStableWorktree(), saveLineComment("failure stays visible")))
			scenario.When("clipboard export is selected", func(t testing.TB, w *world) {
				w.pty.send(t, "y\r")
			})
			scenario.Then("the unknown delivery outcome is shown", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "clipboard failure", func(screen string) bool {
					return strings.Contains(screen, "clipboard delivery") && strings.Contains(screen, "fenced")
				})
				w.pty.send(t, ":q!\r")
				w.pty.awaitExit(t)
			})
		})
	})
}

func launchStableWorktree() bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		w.launchPTY(t, 120, 24, "diff")
		w.pty.awaitScreen(t, "stable worktree", func(screen string) bool {
			return strings.Contains(screen, "staged contents") && strings.Contains(screen, "work 0/64")
		})
	}
}

func saveLineComment(body string) bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		// Patch presentation opens on its metadata header. Move to the final
		// changed line before asking the line-comment action to anchor a draft.
		w.pty.send(t, "Gc")
		w.pty.awaitScreen(t, "comment editor", containsScreen("Comment ·"))
		w.pty.send(t, body+"\x13")
		w.pty.awaitScreen(t, "saved comment", containsScreen("comment saved"))
	}
}

func containsScreen(text string) func(string) bool {
	return func(screen string) bool { return strings.Contains(screen, text) }
}

func assertTerminalLifecycle(t testing.TB, session *ptySession, wantMouse bool) {
	t.Helper()
	raw := session.rawTranscript()
	for _, sequence := range [][]byte{
		[]byte("\x1b[?1049h"),
		[]byte("\x1b[?1049l"),
		[]byte("\x1b[?25l"),
		[]byte("\x1b[?25h"),
	} {
		if !bytes.Contains(raw, sequence) {
			t.Fatalf("terminal control %q is absent\n%s", sequence, session.diagnostics(t))
		}
	}
	if wantMouse && !bytes.Contains(raw, []byte("\x1b[?1002h")) &&
		!bytes.Contains(raw, []byte("\x1b[?1003h")) {
		t.Fatalf("mouse tracking was not enabled\n%s", session.diagnostics(t))
	}
	status, before, after, ok := session.terminalState()
	if !ok || status != 0 || stableTerminalState(before) != stableTerminalState(after) {
		t.Fatalf("terminal was not restored: status=%d before=%q after=%q parsed=%t\n%s", status, before, after, ok, session.diagnostics(t))
	}
}

func (p *ptySession) hasStyledCells() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for y := range p.terminal.Height() {
		for x := range p.terminal.Width() {
			cell := p.terminal.CellAt(x, y)
			if cell != nil && cell.Content != "" && cell.Content != " " &&
				(cell.Style.Fg != nil || cell.Style.Bg != nil || cell.Style.Attrs != 0) {
				return true
			}
		}
	}
	return false
}

func TestGitHubPublicationContract(t *testing.T) {
	type eventExample struct {
		configured string
		payload    string
	}
	events := []bdd.Example[eventExample]{
		bdd.Case("comment", eventExample{"comment", "COMMENT"}),
		bdd.Case("draft", eventExample{"draft", ""}),
		bdd.Case("approve", eventExample{"approve", "APPROVE"}),
		bdd.Case("request changes", eventExample{"request-changes", "REQUEST_CHANGES"}),
	}

	bdd.Feature(t, "GitHub publication is checked and observable", newWorld, func(feature *bdd.Suite[*world]) {
		bdd.Outline(feature, "all four publication events send the canonical request", events, func(example eventExample, scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("the publication event is configured", func(t testing.TB, w *world) {
				w.writeConfig(t, "[github]\nfetch_threads = false\nsubmit_default = \""+example.configured+"\"\n")
			})
			scenario.Given("a pull request has one local comment", launchCommentedPullRequest())
			scenario.When("preflight is accepted and publication is confirmed", publishReview())
			scenario.Then("the exact event and marker shape reached gh", func(t testing.TB, w *world) {
				call := publicationCall(t, w.calls(t))
				var payload struct {
					Event string `json:"event"`
					Body  string `json:"body"`
				}
				if err := json.Unmarshal([]byte(call.Stdin), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Event != example.payload ||
					!regexp.MustCompile(`<!-- rvw-publication:v1:[0-9a-f]{32} -->`).MatchString(payload.Body) {
					t.Fatalf("publication payload = %s", call.Stdin)
				}
				quitDirtyReview(t, w)
			})
		})

		feature.Scenario("an ambiguous write is fenced and reconciled by its exact marker", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("GitHub will report an ambiguous POST", func(_ testing.TB, w *world) {
				w.setEnvironment("RVW_E2E_GH_POST_MODE", "unknown-present")
			})
			scenario.Given("a pull request has one local comment", launchCommentedPullRequest())
			scenario.When("publication reaches the remote write", publishReview())
			scenario.Then("the started failure is reconciled as committed", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "committed reconciliation", containsScreen("Publication committed."))
				call := publicationCall(t, w.calls(t))
				var payload struct {
					Body string `json:"body"`
				}
				if err := json.Unmarshal([]byte(call.Stdin), &payload); err != nil {
					t.Fatal(err)
				}
				marker := regexp.MustCompile(`<!-- rvw-publication:v1:[0-9a-f]{32} -->`).FindString(payload.Body)
				if marker == "" {
					t.Fatalf("publication marker absent: %s", call.Stdin)
				}
				calls := w.calls(t)
				if !slices.ContainsFunc(calls, func(call externalCall) bool {
					return slices.Contains(call.Args, "--paginate")
				}) {
					t.Fatalf("ambiguous publication was not reconciled: %+v", calls)
				}
				quitDirtyReview(t, w)
			})
		})
	})
}

func launchCommentedPullRequest() bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		w.launchPTY(t, 120, 24, "pr", "7", "--repo", "fixture/repository")
		w.pty.awaitScreen(t, "pull request patch", func(screen string) bool {
			return strings.Contains(screen, "reviewed.go") && strings.Contains(screen, "work 0/64")
		})
		saveLineComment("This should be published.")(t, w)
	}
}

func publishReview() bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		w.pty.send(t, "s")
		w.pty.awaitScreen(t, "checked publication", containsScreen("checked at current head"))
		w.pty.send(t, "\r")
		w.pty.awaitScreen(t, "publication result", func(screen string) bool {
			return strings.Contains(screen, "Publication committed.") ||
				strings.Contains(screen, "Outcome unknown; retry is fenced.")
		})
	}
}

func publicationCall(t testing.TB, calls []externalCall) externalCall {
	t.Helper()
	for _, call := range calls {
		if call.Command == "gh" && slices.Contains(call.Args, "POST") {
			return call
		}
	}
	t.Fatalf("publication POST not found in %+v", calls)
	return externalCall{}
}

func quitDirtyReview(t testing.TB, w *world) {
	t.Helper()
	w.pty.send(t, "\x1b")
	w.pty.awaitScreen(t, "workspace after closing modal", containsScreen("reviewed.go"))
	w.pty.send(t, ":q!\r")
	w.pty.awaitExit(t)
}
