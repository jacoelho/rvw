//go:build e2e && (darwin || linux)

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/jacoelho/bdd"
)

// kittyRepeatedCtrlC is Ctrl-c with Kitty keyboard event type 2 (repeat).
const (
	kittyEscape        = "\x1b[27u"
	kittyRepeatedCtrlC = "\x1b[99;5:2u"
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

		feature.Scenario("an existing pending review is loaded as editable draft state", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("GitHub has one viewer-owned pending review", func(t testing.TB, w *world) {
				configurePendingReview(t, w)
			})
			scenario.When("the pull request is opened", func(t testing.TB, w *world) {
				w.launchPTY(t, 120, 24, "pr", "7", "--repo", "fixture/repository")
			})
			scenario.Then("the pending comments are editable draft rows rather than remote duplicates", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "loaded pending review", containsScreen("reviewed.go"))
				w.pty.send(t, "zc")
				screen := w.pty.awaitScreen(t, "pending draft rows", func(screen string) bool {
					return strings.Contains(screen, "[draft]") &&
						strings.Contains(screen, "reviewed.go:3") &&
						!strings.Contains(screen, "[remote open]")
				})
				assertNoEscapedWidgetStyles(t, w, screen, "pending review")
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
			})
		})

		feature.Scenario("draft save updates and reloads the existing pending review", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("GitHub has one viewer-owned pending review", func(t testing.TB, w *world) {
				configurePendingReview(t, w)
			})
			scenario.Given("the existing inline draft is edited", func(t testing.TB, w *world) {
				w.launchPTY(t, 120, 24, "pr", "7", "--repo", "fixture/repository")
				w.pty.awaitScreen(t, "pending source", containsScreen("reviewed.go"))
				w.pty.send(t, "zc\tj")
				w.pty.awaitScreen(t, "selected inline draft", containsScreen("▸ [draft] reviewed.go:3"))
				w.pty.send(t, "e edited")
				w.pty.send(t, "\x13")
			})
			scenario.When("the default draft preflight is committed", func(t testing.TB, w *world) {
				w.pty.send(t, "s")
				w.pty.awaitScreen(t, "draft preflight", func(screen string) bool {
					return strings.Contains(screen, "GitHub draft synchronization preflight") &&
						strings.Contains(screen, "checked at current head")
				})
				w.pty.send(t, "\r")
			})
			scenario.Then("the same remote comment is updated and the local binding is clean", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "draft synchronized", containsScreen("Publication committed."))
				calls := w.calls(t)
				if !slices.ContainsFunc(calls, func(call externalCall) bool {
					return slices.Equal(call.Args, []string{"api", "graphql", "--input", "-"}) &&
						strings.Contains(call.Stdin, "updatePullRequestReviewComment") &&
						strings.Contains(call.Stdin, "existing inline edited")
				}) {
					t.Fatalf("pending update mutation not observed: %+v", calls)
				}
				w.pty.send(t, "\x1b")
				w.pty.awaitScreen(t, "review restored", containsScreen("reviewed.go"))
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
			})
		})
	})
}

func configurePendingReview(t testing.TB, w *world) {
	t.Helper()
	w.writeFile(t, w.pending, fmt.Sprintf(
		`[{"errors":[],"data":{"viewer":{"login":"octo-reviewer"},`+
			`"repository":{"nameWithOwner":"fixture/repository","pullRequest":{`+
			`"number":7,"baseRefOid":%q,"headRefOid":%q,`+
			`"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{`+
			`"id":"PRR_pending","fullDatabaseId":"9001","body":"existing summary",`+
			`"state":"PENDING","viewerDidAuthor":true,"viewerCanUpdate":true,`+
			`"commit":{"oid":%q}}]}}}}}]`,
		w.prBase,
		w.prHead,
		w.prHead,
	))
	w.writeFile(t, w.pendingComments, fmt.Sprintf(
		`[[{"id":9002,"node_id":"PRRC_pending","pull_request_review_id":9001,`+
			`"body":"existing inline","path":"reviewed.go","commit_id":%q,`+
			`"original_commit_id":%q,"subject_type":"line","line":3,`+
			`"start_line":null,"side":"RIGHT","start_side":null,`+
			`"original_line":3,"original_start_line":null,"in_reply_to_id":null}]]`,
		w.prHead,
		w.prHead,
	))
	w.writeFile(t, w.pendingCapabilities, fmt.Sprintf(
		`[{"errors":[],"data":{"repository":{"nameWithOwner":"fixture/repository",`+
			`"pullRequest":{"number":7,"baseRefOid":%q,"headRefOid":%q}},`+
			`"node":{"id":"PRR_pending","comments":{"pageInfo":{`+
			`"hasNextPage":false,"endCursor":null},"nodes":[{`+
			`"id":"PRRC_pending","fullDatabaseId":"9002",`+
			`"viewerCanUpdate":true,"viewerCanDelete":true}]}}}}]`,
		w.prBase,
		w.prHead,
	))
}

func TestTerminalBehaviorAndExportContract(t *testing.T) {
	bdd.Feature(t, "interactive review journeys", newWorld, func(feature *bdd.Suite[*world]) {
		feature.Scenario("help search command and clean quit route through their contexts", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable worktree screen", launchStableWorktree())
			scenario.When("help is opened and closed", func(t testing.TB, w *world) {
				w.pty.send(t, "?")
				screen := w.pty.awaitScreen(t, "help", containsScreen("Effective help"))
				assertNoEscapedWidgetStyles(t, w, screen, "help")
				w.pty.send(t, "q")
				w.pty.awaitScreen(t, "workspace after help", containsScreen("staged contents"))
			})
			scenario.When("a current-file search completes", func(t testing.TB, w *world) {
				w.pty.send(t, "/")
				screen := w.pty.awaitScreen(t, "current-file find", func(screen string) bool {
					return strings.Contains(screen, "Find") && strings.Contains(screen, "Current") &&
						strings.Contains(screen, "Enter searches changed patch lines")
				})
				assertNoEscapedWidgetStyles(t, w, screen, "current-file find")
				w.pty.send(t, "staged\r")
				w.pty.awaitScreen(t, "current-file find result", func(screen string) bool {
					return strings.Contains(screen, "1 match") &&
						strings.Contains(screen, "staged.txt") && strings.Contains(screen, "staged contents")
				})
				w.pty.send(t, kittyEscape)
				w.pty.awaitScreen(t, "workspace after find", containsScreen("staged contents"))
			})
			scenario.When("the command prompt opens help", func(t testing.TB, w *world) {
				w.pty.send(t, ":")
				screen := w.pty.awaitScreen(t, "command footer", func(screen string) bool {
					return strings.Contains(screen, "[focus:command]") &&
						strings.Contains(screen, "staged contents")
				})
				assertNoEscapedWidgetStyles(t, w, screen, "command prompt")
				lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
				if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], ":") {
					t.Fatalf("command input is not docked to the footer:\n%s", screen)
				}
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

		feature.Scenario("find scopes preview accept and restore navigation", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable worktree screen", launchStableWorktree())
			scenario.When("review text find previews a match in another file", func(t testing.TB, w *world) {
				w.pty.send(t, "g/")
				w.pty.awaitScreen(t, "review find", func(screen string) bool {
					return strings.Contains(screen, "Find") && strings.Contains(screen, "Review")
				})
				w.pty.send(t, "untracked\r")
				w.pty.awaitScreen(t, "review find result", func(screen string) bool {
					return strings.Contains(screen, "1 match") &&
						strings.Contains(screen, "untracked.txt") &&
						strings.Contains(screen, "untracked contents")
				})
			})
			scenario.Then("escape restores the file active before the preview", func(t testing.TB, w *world) {
				w.pty.send(t, kittyEscape)
				w.pty.awaitScreen(t, "restored staged file", func(screen string) bool {
					return strings.Contains(screen, "[focus:diff]  staged.txt") &&
						strings.Contains(screen, "staged contents")
				})
			})
			scenario.When("file find accepts a fuzzy path match", func(t testing.TB, w *world) {
				w.pty.send(t, "\x10")
				w.pty.awaitScreen(t, "file find", func(screen string) bool {
					return strings.Contains(screen, "Find") && strings.Contains(screen, "Files")
				})
				w.pty.send(t, "untr")
				w.pty.awaitScreen(t, "file find result", func(screen string) bool {
					return strings.Contains(screen, "1 match") && strings.Contains(screen, "untracked.txt")
				})
				w.pty.send(t, "\r")
			})
			scenario.Then("accepted file navigation remains active", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "accepted untracked file", func(screen string) bool {
					return strings.Contains(screen, "[focus:diff]  untracked.txt") &&
						strings.Contains(screen, "untracked contents")
				})
			})
			scenario.When("a later file preview is cancelled", func(t testing.TB, w *world) {
				w.pty.send(t, "\x10staged")
				w.pty.awaitScreen(t, "staged file preview", func(screen string) bool {
					return strings.Contains(screen, "1 match") && strings.Contains(screen, "staged.txt")
				})
				w.pty.send(t, kittyEscape)
			})
			scenario.Then("the accepted file is restored and the terminal exits cleanly", func(t testing.TB, w *world) {
				w.pty.awaitScreen(t, "restored accepted file", func(screen string) bool {
					return strings.Contains(screen, "[focus:diff]  untracked.txt") &&
						strings.Contains(screen, "untracked contents")
				})
				w.pty.send(t, "q")
				w.pty.awaitExit(t)
				assertTerminalLifecycle(t, w.pty, true)
			})
		})

		feature.Scenario("a saved comment fences quit and exports exact Markdown", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable wide worktree screen", launchStableWorktreeAt(180, 30))
			scenario.When(
				"a bracketed-paste line comment is saved",
				savePastedLineComment("Please keep this deterministic."),
			)
			scenario.Then("its anchor is visible and both comment navigation paths reveal its line", func(t testing.TB, w *world) {
				w.pty.send(t, "gg")
				w.pty.awaitScreen(t, "visible comment anchor away from cursor", unfocusedCommentAnchor)
				w.pty.send(t, "]c")
				w.pty.awaitScreen(t, "next-comment line", focusedCommentAnchor)
				w.pty.send(t, "gg")
				w.pty.awaitScreen(t, "comment anchor before pane activation", unfocusedCommentAnchor)
				w.pty.send(t, "\t")
				w.pty.awaitScreen(t, "comments pane focus", containsScreen("[focus:comments]"))
				w.pty.send(t, "\r")
				w.pty.awaitScreen(t, "comments-pane activation line", focusedCommentAnchor)
			})
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
				if !bytes.Contains(contents, []byte(`Please keep this deterministic.`)) ||
					!bytes.Contains(contents, []byte(`staged.txt:1`)) {
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

		feature.Scenario("post-terminal JSON export preserves comments without emitting controls", func(scenario *bdd.ScenarioSpec[*world]) {
			scenario.Given("a stable worktree with a bidirectional-control comment", bdd.Compose(
				launchStableWorktreeAt(180, 30),
				saveLineComment("before\u202eafter"),
			))
			scenario.When("JSON stdout export is selected", func(t testing.TB, w *world) {
				w.pty.send(t, "J")
				w.pty.awaitScreen(t, "JSON export picker", containsScreen("Export selection"))
				w.pty.send(t, "\r")
				w.pty.awaitExit(t)
			})
			scenario.Then("the restored-terminal payload is valid escaped JSON with the exact comment", func(t testing.TB, w *world) {
				assertTerminalLifecycle(t, w.pty, true)
				payload := postTerminalJSONPayload(t, w.pty)
				if !json.Valid(payload) {
					t.Fatalf("post-terminal JSON is invalid: %q\n%s", payload, w.pty.diagnostics(t))
				}
				if bytes.Contains(payload, []byte("\u202e")) ||
					bytes.Contains(payload, []byte(`\u{`)) ||
					bytes.Contains(payload, []byte{0x1b}) {
					t.Fatalf("post-terminal JSON contains terminal-unsafe bytes: %q", payload)
				}
				if !bytes.Contains(payload, []byte(`\u202e`)) {
					t.Fatalf("post-terminal JSON lacks JSON escapes: %q", payload)
				}
				for _, value := range payload {
					if value < 0x20 && value != '\n' && value != '\r' && value != '\t' {
						t.Fatalf("post-terminal JSON contains raw control 0x%02x: %q", value, payload)
					}
				}
				var decoded struct {
					Comments []struct {
						Comment string `json:"comment"`
					} `json:"comments"`
				}
				if err := json.Unmarshal(payload, &decoded); err != nil {
					t.Fatal(err)
				}
				if len(decoded.Comments) != 1 || decoded.Comments[0].Comment != "before\u202eafter" {
					t.Fatalf("post-terminal comments = %+v", decoded.Comments)
				}
			})
		})

		feature.Scenario("holding Ctrl-c at dirty quit does not redraw the workspace", func(scenario *bdd.ScenarioSpec[*world]) {
			var transcriptOffset int
			scenario.Given("a stable worktree with one saved comment", bdd.Compose(
				launchStableWorktree(),
				saveLineComment("quit restoration remains readable"),
			))
			scenario.When("Ctrl-c requests dirty quit and a repeat arrives before acceptance", func(t testing.TB, w *world) {
				w.pty.send(t, "\x03")
				screen := w.pty.awaitScreen(t, "dirty quit confirmation", containsScreen("Discard local review changes and quit?"))
				assertNoEscapedWidgetStyles(t, w, screen, "dirty quit")
				transcriptOffset = len(w.pty.rawTranscript())
				w.pty.send(t, kittyRepeatedCtrlC+"y")
				w.pty.awaitExit(t)
			})
			scenario.Then("the terminal is restored without redrawing the workspace", func(t testing.TB, w *world) {
				assertTerminalLifecycle(t, w.pty, true)
				raw := w.pty.rawTranscript()
				if transcriptOffset > len(raw) {
					t.Fatalf("terminal transcript shrank from %d to %d bytes", transcriptOffset, len(raw))
				}
				if bytes.Contains(raw[transcriptOffset:], []byte("staged contents")) {
					t.Fatalf("dirty quit redrew the workspace before restoration\n%s", w.pty.diagnostics(t))
				}
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
	return launchStableWorktreeAt(120, 24)
}

func launchStableWorktreeAt(width, height int) bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		w.launchPTY(t, width, height, "diff")
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

func savePastedLineComment(body string) bdd.Step[*world] {
	return func(t testing.TB, w *world) {
		w.pty.send(t, "Gc")
		w.pty.awaitScreen(t, "comment editor", containsScreen("Comment ·"))
		w.pty.send(t, "\x1b[200~"+body+"\x1b[201~")
		w.pty.awaitScreen(t, "pasted comment body", containsScreen(body))
		w.pty.send(t, "\x13")
		w.pty.awaitScreen(t, "saved comment", containsScreen("comment saved"))
	}
}

func containsScreen(text string) func(string) bool {
	return func(screen string) bool { return strings.Contains(screen, text) }
}

func assertNoEscapedWidgetStyles(t testing.TB, w *world, screen, context string) {
	t.Helper()
	if strings.Contains(screen, `\x1B[`) {
		t.Fatalf("%s exposed escaped widget styling\n%s", context, w.pty.diagnostics(t))
	}
}

func focusedCommentAnchor(screen string) bool {
	return commentAnchorHasFocus(screen, true)
}

func unfocusedCommentAnchor(screen string) bool {
	return commentAnchorHasFocus(screen, false)
}

func commentAnchorHasFocus(screen string, focused bool) bool {
	for line := range strings.Lines(screen) {
		if strings.Contains(line, "staged contents") &&
			strings.Contains(line, "●") &&
			strings.Contains(line, "▸") == focused {
			return true
		}
	}
	return false
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

func postTerminalJSONPayload(t testing.TB, session *ptySession) []byte {
	t.Helper()
	raw := session.rawTranscript()
	restore := bytes.LastIndex(raw, []byte("\x1b[?1049l"))
	if restore < 0 {
		t.Fatalf("alternate-screen restore is absent\n%s", session.diagnostics(t))
	}
	afterRestore := raw[restore+len("\x1b[?1049l"):]
	start := bytes.Index(afterRestore, []byte(`{"comments":`))
	if start < 0 {
		t.Fatalf("post-terminal JSON start is absent\n%s", session.diagnostics(t))
	}
	status := bytes.Index(afterRestore[start:], []byte("__RVW_E2E_STATUS__="))
	if status < 0 {
		t.Fatalf("post-terminal status marker is absent\n%s", session.diagnostics(t))
	}
	return bytes.TrimSpace(afterRestore[start : start+status])
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
