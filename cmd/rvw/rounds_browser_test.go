//go:build darwin

package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// numberedLines is "line 1\n" .. "line n\n", line k replaced by edits[k], which may hold several lines.
func numberedLines(n int, edits map[int]string) string {
	var b strings.Builder
	for k := 1; k <= n; k++ {
		if e, ok := edits[k]; ok {
			b.WriteString(e + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", k)
	}
	return b.String()
}

const (
	sinceButton = `[...document.querySelectorAll(".round-banner .seg button")].find((b) => b.textContent === "Since last review") ?? null`
	allButton   = `[...document.querySelectorAll(".round-banner .seg button")].find((b) => b.textContent === "All changes") ?? null`
	sinceChosen = `(` + sinceButton + `)?.getAttribute("aria-pressed") === "true"`
	allChosen   = `(` + allButton + `)?.getAttribute("aria-pressed") === "true"`
	headerRound = `(document.querySelector(".fh-round")?.textContent ?? "")`
	openEditors = `document.querySelectorAll(".d-editor").length`
	// sinceMarked is the new-side line numbers of the rows marked as added since review, as JSON.
	sinceMarked = `JSON.stringify([...document.querySelectorAll(".d-row.since .d-ln[data-side='new'][data-line]")].map((e) => Number(e.dataset.line)))`
	// addedLines is the new-side line numbers of the added rows drawn, as JSON.
	addedLines = `JSON.stringify([...document.querySelectorAll(".d-row.opadd .d-ln[data-side='new'][data-line]")].map((e) => Number(e.dataset.line)))`
	hiddenWS   = `document.querySelector(".d-hidden-ws")`
	hiddenRows = `document.querySelectorAll(".d-hidden-ws").length`
	wsBox      = `document.querySelector(".ws-toggle input")`
)

// earlierCard is the diff's card of the earlier comment whose body is body.
func earlierCard(body string) string {
	return `[...document.querySelectorAll(".d-comment.earlier")].find((c) => c.querySelector(".c-body")?.textContent === ` + jsString(body) + `) ?? null`
}

// rowTag is the round tag text of the tree row named name.
func rowTag(name string) string {
	return `((` + treeRow(name) + `)?.querySelector(".rtag")?.textContent ?? "")`
}

// dirDCheck is the viewed checkbox of the d/ directory row.
var dirDCheck = `(` + treeRow("d/") + `)?.querySelector(".view-check") ?? null`

// TestBrowserRoundSinceView (AC-RND-003, AC-CMT-004): round 1 marks a.go and b.go viewed and
// comments on a.go's line 10; the agent then inserts two lines after a.go's line 1 and edits its
// line 15. Round 2 groups b.go as reviewed unchanged and viewed, labels a.go changed, and starts at
// Since last review, showing only the agent's changes, offering no comment on the reviewed
// version's lines, and switching to All changes for an old-side draft comment. All changes marks
// exactly the agent's added lines. Still applies on the earlier comment adds it at its mapped line
// once.
func TestBrowserRoundSinceView(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("a.go", numberedLines(20, nil))
	w.write("b.go", "package b\n")
	w.write("c.go", "package c\n")
	w.commit("base")
	w.git("checkout", "-q", "-b", "feature")
	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature"}))
	w.write("b.go", "package b\n\nvar B = 1\n")
	w.write("c.go", "package c\n\nvar C = 1\n")
	w.commit("feature")

	// Files in path order: a.go 0, b.go 1, c.go 2.
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/viewed", `{"files":[0,1],"viewed":true}`, http.StatusNoContent)
	p.createComment("r1", `{"target":{"target":"line","file":0,"side":"new","line":10},"body":"Check this."}`)
	p.finishOK()

	w.write("a.go", numberedLines(20, map[int]string{1: "line 1\nins 1\nins 2", 5: "line 5 feature", 15: "line 15 agent"}))
	w.commit("agent edit")
	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	// An old-side draft comment on base's line 5, which the interdiff does not show at its line.
	p.createComment("old", `{"target":{"target":"line","file":0,"side":"old","line":5},"body":"Old side."}`)
	pg := openPage(t, p)
	pg.ready()

	pg.waitTrue("the reviewed-unchanged row", `(`+treeRow("Reviewed, unchanged (1)")+`) !== null`)
	pg.click("the reviewed-unchanged row", treeRow("Reviewed, unchanged (1)"), 0)
	pg.waitTrue("b.go under it", `(`+treeRow("b.go")+`) !== null`)
	if got := pg.text(`String((` + treeRow("b.go") + `)?.classList.contains("viewed"))`); got != "true" {
		pg.fatalf("b.go is not shown viewed in round 2 (viewed class %s)", got)
	}
	if tag := pg.text(rowTag("a.go")); tag != "Δ" {
		pg.fatalf("a.go's tree tag is %q, want Δ (changed since review)", tag)
	}
	if tag := pg.text(rowTag("c.go")); tag != "" {
		pg.fatalf("c.go, never reviewed, has tree tag %q", tag)
	}
	pg.waitTrue("Since last review chosen", sinceChosen)

	pg.click("a.go", treeRow("a.go"), 0)
	pg.waitTrue("a.go shown", shownPath+` === "a.go" && `+fileLoaded)
	if got := pg.text(headerRound); !strings.HasPrefix(got, "Changed since review") {
		pg.fatalf("a.go's header round label is %q, want Changed since review", got)
	}
	// The interdiff from round 1's a.go: lines 2 and 3 inserted, line 17 (r1's 15) replaced. The
	// feature's own line 7 change, reviewed in round 1, is not shown as added.
	pg.waitTrue("the interdiff's added lines", addedLines+` === "[2,3,17]"`)

	// The reviewed version's line 15 is on the interdiff's old side, outside the snapshot.
	reviewedLine := `[...document.querySelectorAll(".d-row.opdel .d-ln")].find((e) => e.textContent === "15") ?? null`
	if got := pg.text(`String((` + reviewedLine + `)?.title === "")`); got != "true" {
		pg.fatalf("the interdiff's old line 15 offers a comment (or is not drawn): %s", got)
	}
	pg.click("old line 15", reviewedLine, 0)
	pg.frames()
	if n := pg.text(`String(` + openEditors + `)`); n != "0" {
		pg.fatalf("clicking an old-side line of the interdiff opened %s editors, want none", n)
	}
	pg.click("new line 17", newLine(17), 0)
	pg.waitTrue("the new-side editor", editorTarget+`.startsWith("line 17")`)
	pg.click("Close", button(".d-editor", "Close"), 0)
	pg.waitTrue("the editor closed", openEditors+` === 0`)

	// REQ-RND-014: navigating to the old-side draft comment switches to All changes.
	pg.click("c.go", treeRow("c.go"), 0)
	pg.waitTrue("c.go shown", shownPath+` === "c.go" && `+fileLoaded)
	pg.press("[", "BracketLeft", 219, 0)
	pg.waitTrue("a.go shown again", shownPath+` === "a.go"`)
	pg.waitTrue("All changes chosen", allChosen)
	pg.waitTrue("the old-side comment at its line", `(() => { const c = [...document.querySelectorAll(".d-comment")].find((e) => e.querySelector(".c-body")?.textContent === "Old side."); return c !== undefined && c.previousElementSibling?.querySelector('.d-ln[data-side="old"]')?.dataset.line === "5"; })()`)

	// REQ-RND-013: All changes marks exactly the lines the agent added.
	pg.waitTrue("the since marks", sinceMarked+` === "[2,3,17]"`)

	// The earlier line comment maps from r1's line 10 to line 12 and is current.
	card := earlierCard("Check this.")
	pg.waitTrue("the earlier comment", `(`+card+`) !== null`)
	if got := pg.text(`(` + card + `).querySelector(".chip").textContent`); got != "Current" {
		pg.fatalf("the earlier comment's status is %q, want Current", got)
	}
	stillApplies := `[...(` + card + `).querySelectorAll("button")].find((b) => b.textContent === "Still applies") ?? null`
	// Scrolling the card into view re-renders the virtual list; click once it has settled.
	pg.locate("Still applies", stillApplies)
	pg.waitIdle()
	pg.frames()
	pg.click("Still applies", stillApplies, 0)
	pg.waitTrue("the card marked copied", `(`+card+`)?.textContent.includes("Copied") === true`)
	var copies []sessionComment
	for _, c := range p.sessionComments() {
		if c.Body == "Check this." {
			copies = append(copies, c)
		}
	}
	if len(copies) != 1 || copies[0].Target.Target != "line" || copies[0].Target.Side != "new" || copies[0].Target.Line != 12 {
		pg.fatalf("draft copies of the earlier comment = %+v; want one line comment on new line 12", copies)
	}
	if got := pg.text(`String([...(` + card + `).querySelectorAll("button")].some((b) => b.textContent === "Still applies"))`); got != "false" {
		pg.fatalf("Still applies is still offered after the copy")
	}

	pg.click("Since last review", sinceButton, 0)
	pg.waitTrue("Since last review chosen again", sinceChosen+` && `+addedLines+` === "[2,3,17]"`)

	// The interdiff shows the old-side comment in its first row; ] goes to it, not past it.
	oldCard := `[...document.querySelectorAll(".d-comment")].find((e) => e.querySelector(".c-body")?.textContent === "Old side.") ?? null`
	pg.waitTrue("the old-side comment in the first row", `(() => { const r = (`+oldCard+`)?.closest(".vl-row"); const top = document.querySelector(".diff .vl-inner")?.getBoundingClientRect().top; return r != null && `+diffScroller+`.scrollTop === 0 && r.getBoundingClientRect().top === top; })()`)
	pg.press("]", "BracketRight", 221, 0)
	pg.waitTrue("All changes chosen by ]", allChosen)
	pg.waitTrue("the old-side comment at its line after ]", `(() => { const c = `+oldCard+`; return c !== null && c.previousElementSibling?.querySelector('.d-ln[data-side="old"]')?.dataset.line === "5"; })()`)
	pg.noPageErrors()
}

// TestBrowserHideWhitespaceAndDirectoryMarks (AC-DIFF-004, AC-NAV-004): with whitespace hidden a
// re-indent-only hunk is one row that shows it when activated, a re-indent-only hunk holding a
// comment and a hunk mixing re-indentation with an edit are shown, a palette jump into a hidden
// hunk shows it, and a file changed only in indentation is in the whitespace-only row until
// whitespace is shown again. A directory's viewed box marks all its files, clears them, and
// reflects a file cleared from its own header. A file row's viewed box marks that file without
// selecting it.
func TestBrowserHideWhitespaceAndDirectoryMarks(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("m.go", numberedLines(40, nil))
	w.write("ws.go", "a\nb\nc\n")
	for _, f := range []string{"d/x.go", "d/y.go", "d/z.go"} {
		w.write(f, "package d\n")
	}
	w.commit("base")
	// Four hunks: re-indents at 3, 12 (commented) and 33; at 22-23 a re-indent beside an edit.
	w.write("m.go", numberedLines(40, map[int]string{3: "  line 3", 12: "  line 12", 22: "  line 22", 23: "line 23 edited", 33: "  line 33"}))
	w.write("ws.go", "  a\n  b\n  c\n")
	for _, f := range []string{"d/x.go", "d/y.go", "d/z.go"} {
		w.write(f, "package d\n\nvar V = 1\n")
	}
	w.commit("head")

	// Files in path order: d/x.go 0, d/y.go 1, d/z.go 2, m.go 3, ws.go 4.
	p := w.start(procOptions{}, "last")
	p.waitURL()
	p.must(http.MethodPut, "/api/viewed", `{"files":[1],"viewed":true}`, http.StatusNoContent)
	p.createComment("ws", `{"target":{"target":"line","file":3,"side":"new","line":12},"body":"On a re-indent."}`)
	pg := openPage(t, p)
	pg.ready()

	pg.click("m.go", treeRow("m.go"), 0)
	pg.waitTrue("m.go shown", shownPath+` === "m.go" && `+fileLoaded)
	if got := pg.text(`String(` + hiddenWS + ` !== null)`); got != "false" {
		pg.fatalf("a whitespace-only hunk is hidden while whitespace is shown")
	}
	pg.click("Hide whitespace", wsBox, 0)
	pg.waitTrue("two hidden-whitespace rows", hiddenRows+` === 2`)
	if !p.roundSession().HideWhitespace {
		pg.fatalf("the session's Hide whitespace setting is off after checking it")
	}
	shown := func(n int) bool { return pg.text(`String(`+newLine(n)+` !== null)`) == "true" }
	if shown(3) || shown(33) || !shown(12) || !shown(22) || !shown(23) {
		pg.fatalf("with whitespace hidden, new lines 3, 12, 22, 23, 33 shown = %v %v %v %v %v; want the uncommented re-indents (3, 33) hidden and the rest shown",
			shown(3), shown(12), shown(22), shown(23), shown(33))
	}
	pg.waitTrue("the whitespace-only row", `(`+treeRow("Whitespace only (1)")+`) !== null`)
	// A palette jump to a line in a hidden hunk shows that hunk only.
	pg.goVia("m.go:33", "m.go")
	pg.waitTrue("line 33 shown and selected", newLine(33)+` !== null && `+selectedLine("new", 33))
	if n := pg.text(`String(` + hiddenRows + `)`); n != "1" || shown(3) {
		pg.fatalf("after the jump to line 33, %s hidden rows and line 3 shown %v; want line 3's hunk still hidden", n, shown(3))
	}
	pg.click("Show", `(`+hiddenWS+`)?.querySelector("button") ?? null`, 0)
	pg.waitTrue("the re-indented line shown", hiddenWS+` === null && `+newLine(3)+` !== null`)
	pg.click("Hide whitespace", wsBox, 0)
	pg.waitTrue("the whitespace-only row gone", `(`+treeRow("Whitespace only (1)")+`) === null`)
	if p.roundSession().HideWhitespace {
		pg.fatalf("the session's Hide whitespace setting is on after clearing it")
	}

	viewed := func() []int { return p.roundSession().Viewed }
	waitViewed := func(what string, want []int) {
		pg.t.Helper()
		pg.waitTrue(what, fmt.Sprintf(`(%s)?.checked === %v`, dirDCheck, len(want) == 3))
		if got := viewed(); !slices.Equal(got, want) {
			pg.fatalf("%s: the session's viewed files are %v, want %v", what, got, want)
		}
	}
	if pg.text(`String((`+dirDCheck+`)?.checked)`) != "false" {
		pg.fatalf("d/ shows viewed with one of its three files viewed")
	}
	pg.click("d/ viewed", dirDCheck, 0)
	waitViewed("d/ marked", []int{0, 1, 2})
	pg.click("d/ viewed", dirDCheck, 0)
	waitViewed("d/ cleared", nil)
	pg.click("d/ viewed", dirDCheck, 0)
	waitViewed("d/ marked again", []int{0, 1, 2})

	pg.click("d/y.go", treeRow("y.go"), 0)
	pg.waitTrue("d/y.go shown", shownPath+` === "d/y.go" && `+fileLoaded)
	pg.click("y.go's Viewed", `document.querySelector(".fh-viewed input")`, 0)
	waitViewed("y.go cleared from its header", []int{0, 2})
	pg.click("x.go viewed", `(`+treeRow("x.go")+`)?.querySelector(".view-check") ?? null`, 0)
	waitViewed("x.go cleared from its row", []int{2})
	if got := pg.text(shownPath); got != "d/y.go" {
		pg.fatalf("after x.go's row box, %q is shown, want d/y.go", got)
	}
	pg.noPageErrors()
}

// TestBrowserRoundBaseChangedAndUnavailable (AC-RND-003): round 1 reviews a.go uncommitted and b.go
// against base0, and offers no view choice since there is no ledger yet. Round 2 reviews against
// base1, which changes only b.go's old side, after the agent commits a.go anew and round 1's
// retention ref is deleted and gc prunes its a.go blob. b.go is labelled base changed and is not
// reviewed unchanged; a.go is changed since review and shows its snapshot hunks with the
// unavailable notice. d.go, changed on both sides, is labelled changed since review with its base
// changed.
func TestBrowserRoundBaseChangedAndUnavailable(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("a.go", numberedLines(20, nil))
	w.write("b.go", "package b\n")
	w.write("c.go", "package c\n")
	w.write("d.go", "package d\n")
	w.commit("base")
	w.git("tag", "base0")
	w.write("b.go", "package b\n\n// mid\n")
	w.write("d.go", "package d\n\n// mid\n")
	w.commit("mid")
	w.git("tag", "base1")
	w.git("checkout", "-q", "-b", "feature")
	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature"}))
	w.write("b.go", "package b\n\nvar B = 1\n")
	w.write("c.go", "package c\n\nvar C = 1\n")
	w.write("d.go", "package d\n\nvar D = 1\n")
	w.commit("feature")
	round1 := numberedLines(20, map[int]string{5: "line 5 feature", 10: "line 10 round 1"})
	w.write("a.go", round1)
	reviewed := w.git("hash-object", "a.go")

	// Files in path order: a.go 0, b.go 1, c.go 2, d.go 3.
	p := w.start(procOptions{}, "branch", "base0", "--uncommitted")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	if got := pg.text(`String(document.querySelector(".round-banner")?.hidden === true && (` + sinceButton + `) === null)`); got != "true" {
		pg.fatalf("with no ledger, the round strip is shown or offers the view choice")
	}
	p.must(http.MethodPut, "/api/viewed", `{"files":[0,1,3],"viewed":true}`, http.StatusNoContent)
	p.finishOK()

	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature", 10: "line 10 round 2"}))
	w.write("d.go", "package d\n\nvar D = 2\n")
	w.commit("agent edit")
	for ref := range strings.FieldsSeq(w.git("for-each-ref", "--format=%(refname)", "refs/rvw/")) {
		w.git("update-ref", "-d", ref)
	}
	w.git("gc", "-q", "--prune=now")
	exists := exec.CommandContext(t.Context(), "git", "-C", w.repo, "cat-file", "-e", reviewed)
	exists.Env = w.env()
	if exists.Run() == nil {
		t.Fatal("fixture: round 1's a.go blob survived gc")
	}

	p = w.start(procOptions{}, "branch", "base1")
	p.waitURL()
	pg = openPage(t, p)
	pg.ready()
	if got := p.roundSession().states(); got["a.go"] != "changed" || got["b.go"] != "base" || got["c.go"] != "" || got["d.go"] != "changed_base" {
		pg.fatalf("round states %v; want a.go changed, b.go base, c.go unreviewed, d.go changed_base", got)
	}
	pg.waitTrue("a.go tagged changed", rowTag("a.go")+` === "Δ"`)
	if tag := pg.text(rowTag("b.go")); tag != "base" {
		pg.fatalf("b.go's tree tag is %q, want base", tag)
	}
	if got := pg.text(`String((` + treeRow("Reviewed, unchanged (1)") + `) === null)`); got != "true" {
		pg.fatalf("a reviewed-unchanged row is shown, though b.go's base changed")
	}

	pg.click("b.go", treeRow("b.go"), 0)
	pg.waitTrue("b.go shown", shownPath+` === "b.go" && `+fileLoaded)
	if got := pg.text(headerRound); got != "Base changed" {
		pg.fatalf("b.go's header round label is %q, want Base changed", got)
	}
	if tag := pg.text(rowTag("d.go")); tag != "Δ base" {
		pg.fatalf("d.go's tree tag is %q, want Δ base", tag)
	}
	pg.click("d.go", treeRow("d.go"), 0)
	pg.waitTrue("d.go shown", shownPath+` === "d.go" && `+fileLoaded)
	if got := pg.text(headerRound); !strings.HasPrefix(got, "Changed since review") || !strings.HasSuffix(got, " · Base changed") {
		pg.fatalf("d.go's header round label is %q, want changed since review with base changed", got)
	}

	pg.click("a.go", treeRow("a.go"), 0)
	pg.waitTrue("a.go shown", shownPath+` === "a.go" && `+fileLoaded)
	pg.waitTrue("Since last review chosen", sinceChosen)
	pg.waitTrue("the unavailable notice",
		`[...document.querySelectorAll(".d-note")].some((n) => n.textContent === "The reviewed version is unavailable; showing all changes.")`)
	// The snapshot's hunks from base1: lines 5 and 10 changed.
	pg.waitTrue("the snapshot's added lines", addedLines+` === "[5,10]"`)
	pg.noPageErrors()
}

// TestBrowserRoundReviewedWithWhitespaceHidden (AC-DIFF-004, REQ-RND-011): m.go marked viewed with
// whitespace hidden is reviewed unchanged in the next round while whitespace is hidden, which the
// round starts with; once whitespace is shown it leaves the reviewed-unchanged row and is labelled
// reviewed with whitespace hidden.
func TestBrowserRoundReviewedWithWhitespaceHidden(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("c.go", "package c\n")
	w.write("m.go", numberedLines(30, nil))
	w.commit("base")
	w.git("checkout", "-q", "-b", "feature")
	w.write("c.go", "package c\n\nvar C = 1\n")
	w.write("m.go", numberedLines(30, map[int]string{3: "  line 3", 25: "line 25 edited"}))
	w.commit("feature")

	// Files in path order: c.go 0, m.go 1.
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/hide-whitespace", `{"hide":true}`, http.StatusNoContent)
	p.must(http.MethodPut, "/api/viewed", `{"files":[1],"viewed":true}`, http.StatusNoContent)
	p.finishOK()

	w.write("c.go", "package c\n\nvar C = 2\n")
	w.commit("agent edit")
	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()

	pg.waitTrue("Hide whitespace on from the last round", `(`+wsBox+`)?.checked === true`)
	pg.waitTrue("the reviewed-unchanged row", `(`+treeRow("Reviewed, unchanged (1)")+`) !== null`)
	pg.click("Hide whitespace", wsBox, 0)
	pg.waitTrue("the reviewed-unchanged row gone", `(`+treeRow("Reviewed, unchanged (1)")+`) === null && (`+treeRow("m.go")+`) !== null`)
	if tag := pg.text(rowTag("m.go")); tag != "ws" {
		pg.fatalf("m.go's tree tag is %q with whitespace shown, want ws", tag)
	}
	pg.click("m.go", treeRow("m.go"), 0)
	pg.waitTrue("m.go shown", shownPath+` === "m.go" && `+fileLoaded)
	if got := pg.text(headerRound); got != "Reviewed with whitespace hidden" {
		pg.fatalf("m.go's header round label is %q, want Reviewed with whitespace hidden", got)
	}
	pg.click("Hide whitespace", wsBox, 0)
	pg.waitTrue("the reviewed-unchanged row back", `(`+treeRow("Reviewed, unchanged (1)")+`) !== null`)
	pg.noPageErrors()
}

// TestBrowserDirectoryMarksAtScale (AC-NAV-004): the viewed box of a collapsed vendored row marks
// its files without expanding it, and the box of a directory of 1,000 files marks them all in one
// request.
func TestBrowserDirectoryMarksAtScale(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("go.sum", "")
	w.write("main.go", "package main\n")
	w.commit("base")
	const many = 1000
	for k := range many {
		w.write(fmt.Sprintf("big/f%04d.txt", k), "x\n")
	}
	for _, f := range []string{"vendor/v1.go", "vendor/v2.go"} {
		w.write(f, "package v\n")
	}
	w.write("main.go", "package main\n\nfunc main() {}\n")
	w.commit("head")

	// Files in path order: big/ 0..999, main.go 1000, vendor/ 1001 and 1002.
	p := w.start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.eval(`(() => {
		const real = window.fetch;
		window.rvwViewedPuts = 0;
		window.fetch = (input, init) => {
			if (init?.method === "PUT" && String(input).startsWith("/api/viewed")) window.rvwViewedPuts++;
			return real(input, init);
		};
	})()`, nil)
	puts := func() int {
		var n int
		pg.eval(`window.rvwViewedPuts`, &n)
		return n
	}
	dirCheck := func(name string) string { return `(` + treeRow(name) + `)?.querySelector(".view-check") ?? null` }

	before := puts()
	pg.click("big/ viewed", dirCheck("big/"), 0)
	pg.waitTrue("big/ marked", `(`+dirCheck("big/")+`)?.checked === true`)
	if n := puts() - before; n != 1 {
		pg.fatalf("marking a directory of %d files sent %d viewed requests, want 1", many, n)
	}
	if got := p.roundSession().Viewed; len(got) != many || got[0] != 0 || got[many-1] != many-1 {
		pg.fatalf("viewed after marking big/ has %d files, want big/'s %d", len(got), many)
	}

	// Collapsing big/ brings the vendored row into the tree's rendered rows.
	pg.click("big/", treeRow("big/"), 0)
	pg.waitTrue("the vendored row", `(`+treeRow("vendor/")+`) !== null`)
	if got := pg.text(`String((` + treeRow("v1.go") + `) === null)`); got != "true" {
		pg.fatalf("the vendored row does not start collapsed")
	}
	pg.click("vendor/ viewed", dirCheck("vendor/"), 0)
	pg.waitTrue("vendor/ marked", `(`+dirCheck("vendor/")+`)?.checked === true`)
	if got := p.roundSession().Viewed; len(got) != many+2 || got[many] != many+1 || got[many+1] != many+2 {
		pg.fatalf("viewed after marking vendor/ has %d files ending %v, want big/ and vendor/'s [%d %d]", len(got), got[max(0, len(got)-2):], many+1, many+2)
	}
	if got := pg.text(`String((` + treeRow("v1.go") + `) === null)`); got != "true" {
		pg.fatalf("marking the vendored row expanded it")
	}
	pg.noPageErrors()
}

// TestBrowserRoundRefusedViewChoice: choosing All changes while the open editor's save fails keeps
// the file under Since last review, and the choice shows Since last review with it, so the view
// choice never claims a view the file is not in.
func TestBrowserRoundRefusedViewChoice(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("a.go", numberedLines(20, nil))
	w.commit("base")
	w.git("checkout", "-q", "-b", "feature")
	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature"}))
	w.commit("feature")
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
	p.finishOK()

	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature", 15: "line 15 agent"}))
	w.commit("agent edit")
	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.click("a.go", treeRow("a.go"), 0)
	pg.waitTrue("a.go's interdiff", sinceChosen+` && `+addedLines+` === "[15]"`)

	pg.click("new line 15", newLine(15), 0)
	pg.waitTrue("the editor", editorTarget+`.startsWith("line 15")`)
	pg.eval(`(() => {
		window.rvwFailedPuts = 0;
		const real = window.fetch;
		window.fetch = (input, init) => {
			if (String(input).startsWith("/api/comments/") && init?.method === "PUT") {
				window.rvwFailedPuts++;
				return Promise.resolve(new Response("down", { status: 500 }));
			}
			return real(input, init);
		};
	})()`, nil)
	pg.click("the editor's text", `document.querySelector(".d-editor textarea")`, 0)
	pg.insertText("Unsaved.")
	pg.waitTrue("the typed text", `document.querySelector(".d-editor textarea")?.value === "Unsaved."`)
	pg.click("All changes", allButton, 0)
	pg.waitTrue("the failed save", `window.rvwFailedPuts > 0`)
	pg.waitTrue("Since last review chosen again", sinceChosen)
	pg.frames()
	if got := pg.text(`String(` + openEditors + `)`); got != "1" {
		pg.fatalf("%s editors open after the refused choice; want the unsaved one", got)
	}
	if got := pg.text(addedLines); got != "[15]" {
		pg.fatalf("added lines after the refused choice = %s; want the interdiff's [15]", got)
	}
}

// TestBrowserRoundSupersededViewChoice: a view choice whose load is overtaken by another reload of
// the same file, here Hide whitespace, stays chosen, and the file is shown under it (REQ-RND-010).
func TestBrowserRoundSupersededViewChoice(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("a.go", numberedLines(20, nil))
	w.commit("base")
	w.git("checkout", "-q", "-b", "feature")
	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature"}))
	w.commit("feature")
	p := w.start(procOptions{}, "branch", "main")
	p.waitURL()
	p.must(http.MethodPut, "/api/viewed", `{"files":[0],"viewed":true}`, http.StatusNoContent)
	p.finishOK()

	w.write("a.go", numberedLines(20, map[int]string{5: "line 5 feature", 15: "line 15 agent"}))
	w.commit("agent edit")
	p = w.start(procOptions{}, "branch", "main")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.click("a.go", treeRow("a.go"), 0)
	pg.waitTrue("a.go's interdiff", sinceChosen+` && `+addedLines+` === "[15]"`)
	pg.click("All changes", allButton, 0)
	pg.waitTrue("a.go's full diff", allChosen+` && `+addedLines+` === "[5,15]"`)

	// The next interdiff load hangs until it is aborted.
	pg.eval(`(() => {
		window.rvwHeld = 0;
		const real = window.fetch;
		window.fetch = (input, init) => {
			if (window.rvwHeld === 0 && String(input).includes("scope=since")) {
				window.rvwHeld++;
				return new Promise((_, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))));
			}
			return real(input, init);
		};
	})()`, nil)
	pg.click("Since last review", sinceButton, 0)
	pg.waitTrue("the held interdiff load", `window.rvwHeld === 1`)
	pg.click("Hide whitespace", wsBox, 0)
	pg.waitTrue("a.go's interdiff after the whitespace reload", `(`+wsBox+`)?.checked === true && `+addedLines+` === "[15]"`)
	pg.frames()
	if !pg.bool(sinceChosen) {
		pg.fatalf("Since last review is not chosen after its load was superseded; the file shows the interdiff")
	}
}
