//go:build darwin

package main

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

// Search palette locators.
const (
	palInput  = `document.querySelector(".pal-input")`
	palOpen   = `(document.querySelector("dialog.palette")?.open === true)`
	palStatus = `(document.querySelector(".pal-status")?.textContent ?? "")`
	palMode   = `(document.querySelector(".pal-mode")?.textContent ?? "")`
	// palRows are the mounted result rows' texts in list order.
	palRows = `JSON.stringify([...document.querySelectorAll(".pal-row")].sort((a, b) => parseFloat(a.style.top) - parseFloat(b.style.top)).map((r) => r.textContent))`
	// palCount is the number of result rows, mounted or not.
	palCount  = `Math.round(parseFloat(document.querySelector(".pal-list .vl-inner").style.height || "0") / 26)`
	toastText = `(document.querySelector(".toast:not([hidden])")?.textContent ?? "")`
)

// selectedLine holds when exactly line n of side is the selected gutter line.
func selectedLine(side string, n int) string {
	return fmt.Sprintf(`(() => { const s = [...document.querySelectorAll(".d-line.sel, .d-half.sel")];
		return s.length === 1 && s[0].querySelector('.d-ln[data-side="%s"][data-line="%d"]') !== null; })()`, side, n)
}

// centred holds when line n of side is within one row of its scroller's centre, or as near it as
// the scroller can bring it.
func centred(side string, n int) string {
	return fmt.Sprintf(`(() => { const e = document.querySelector('.d-ln[data-side="%s"][data-line="%d"]'); if (!e) return false;
		let sc = e.parentElement; while (sc && !["auto", "scroll"].includes(getComputedStyle(sc).overflowY)) sc = sc.parentElement;
		if (!sc) return false;
		const r = e.getBoundingClientRect(), d = sc.getBoundingClientRect();
		const off = (r.top + r.bottom) / 2 - (d.top + d.bottom) / 2;
		if (Math.abs(off) <= 20) return true;
		if (off < 0) return sc.scrollTop <= 0;
		return sc.scrollTop + sc.clientHeight >= sc.scrollHeight - 1; })()`, side, n)
}

// searchWorld's last commit changes the files the palette tests search and navigate.
func searchWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.write("README", "base\n")
	w.write("long.txt", numbered("long", 300))
	w.write("legacy/oldname_util.txt", numbered("keep", 20))
	w.write("deleted.txt", "one\ntwo\ngonefile marker\nfour\nfive\n")
	w.write("big.txt", bigText("base"))
	w.write("go.sum", "")
	w.write("vendor/lib/v.go", "package lib\n")
	w.commit("base")

	for _, p := range []string{"pkg/test_util.go", "pkg/util/other.go", "internal/util.go", "web/fooBarBaz.ts", "docs/a b:c.md"} {
		w.write(p, "content of "+p+"\n")
	}
	w.write("long.txt", strings.Replace(numbered("long", 300), "long 10\n", "long ten\n", 1))
	w.write("moved/renamed.txt", numbered("keep", 20))
	w.git("rm", "-q", "legacy/oldname_util.txt")
	w.git("rm", "-q", "deleted.txt")
	w.write("big.txt", bigText("head"))
	w.write("vendor/lib/v.go", "package lib\n\nvar V = 1\n")
	w.write("gen/zz.go", generatedGo)
	w.write("needle.txt", numbered("needle", 600))
	w.write("xss.txt", "<img src=x onerror=window.pwn=1>\n<b>x</b>\n")
	w.write("odd/<a&b>.txt", "odd\n")
	// İ (U+0130) lowercases to two UTF-16 units; NFC, as written.
	w.write("src/\u0130stanbul.go", "package src\n")
	w.write("src/main.go", "package main\n")
	for i := range 60 {
		w.write(fmt.Sprintf("fill/f%02d.txt", i), fmt.Sprintf("fill %d\n", i))
	}
	w.commit("head")
	return w
}

func numbered(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", prefix, i)
	}
	return b.String()
}

// bigText is over 1 MiB, so the file is large; tag differs on line 5 only.
func bigText(tag string) string {
	var b strings.Builder
	for i := 1; i <= 20000; i++ {
		if i == 5 {
			fmt.Fprintf(&b, "big %s line, padded to make the file large ...........................\n", tag)
			continue
		}
		fmt.Fprintf(&b, "big line %05d, padded to make the file large .............................\n", i)
	}
	return b.String()
}

func startSearch(t *testing.T) (*proc, *page) {
	t.Helper()
	p := searchWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	return p, pg
}

func (pg *page) cmdK() { pg.press("k", "KeyK", 75, modMeta) }

func (pg *page) enter() { pg.press("Enter", "Enter", 13, 0) }

func (pg *page) escape() { pg.press("Escape", "Escape", 27, 0) }

func (pg *page) openPalette() {
	pg.t.Helper()
	pg.cmdK()
	pg.waitTrue("the palette open", palOpen)
	pg.waitFocus("the palette input", palInput)
}

// palType sets the palette input to each value in turn, with an input event for each, in one
// evaluation: no timer can run between them. window.rvwLastInput is the page time of the last.
func (pg *page) palType(values ...string) {
	pg.t.Helper()
	data, err := json.Marshal(values)
	if err != nil {
		pg.t.Fatal(err)
	}
	pg.eval(`(() => { const i = `+palInput+`; for (const v of `+string(data)+`) { i.value = v; i.dispatchEvent(new Event("input")); }
		window.rvwLastInput = performance.now(); })()`, nil)
}

func (pg *page) rows() []string {
	pg.t.Helper()
	var s string
	pg.eval(palRows, &s)
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		pg.t.Fatal(err)
	}
	return out
}

// goVia opens the palette, types q, waits until the first row holds first, and presses Enter.
func (pg *page) goVia(q, first string) {
	pg.t.Helper()
	pg.openPalette()
	pg.palType(q)
	pg.waitTrue("first row "+first, `JSON.parse(`+palRows+`)[0]?.startsWith(`+jsString(first)+`) === true`)
	pg.enter()
	pg.waitTrue("the palette closed", `!`+palOpen)
}

// wrapFetch records every request URL in window.rvwURLs. A search is recorded in
// window.rvwSearches with the page time it was sent (t), whether its signal was aborted, and
// whether it settled: its reply body was read, or it failed. It waits for the hold current when
// it started (window.rvwSearchHold), is answered by window.rvwSearchFake when set, and with
// window.rvwIgnoreAbort reaches the server without its signal. A context request waits for
// window.rvwContextHold.
func (pg *page) wrapFetch() {
	pg.t.Helper()
	pg.eval(`(() => {
		const real = window.fetch;
		window.rvwURLs = [];
		window.rvwSearches = [];
		window.rvwSearchHold = null;
		window.rvwSearchFake = null;
		window.rvwIgnoreAbort = false;
		window.rvwContextHold = null;
		window.fetch = async (input, init) => {
			const url = String(input);
			window.rvwURLs.push(url);
			if (url.startsWith("/api/file/context") && window.rvwContextHold) await window.rvwContextHold;
			if (!url.startsWith("/api/search")) return real(input, init);
			const rec = { url, t: performance.now(), aborted: false, settled: false };
			window.rvwSearches.push(rec);
			init?.signal?.addEventListener("abort", () => (rec.aborted = true));
			const hold = window.rvwSearchHold;
			const fake = window.rvwSearchFake;
			try {
				if (hold) await hold;
				const resp = fake
					? new Response(typeof fake.body === "string" ? fake.body : JSON.stringify(fake.body), { status: fake.status })
					: await real(input, window.rvwIgnoreAbort ? { ...init, signal: undefined } : init);
				await resp.clone().text();
				return resp;
			} finally {
				rec.settled = true;
			}
		};
	})()`, nil)
}

const holdSearches = `window.rvwSearchHold = new Promise((r) => (window.rvwRelease = r))`

// searchesSettled waits until every search has settled or been aborted, and then a little longer
// for the palette to handle the last reply, so a check that a reply was not applied cannot pass
// before the reply arrived.
func (pg *page) searchesSettled() {
	pg.t.Helper()
	pg.waitTrue("every search settled", `window.rvwSearches.every((r) => r.settled || r.aborted)`)
	pg.waitMs(50)
}

func (pg *page) searches() int {
	pg.t.Helper()
	var n int
	pg.eval(`window.rvwSearches.length`, &n)
	return n
}

// waitMs waits until ms have passed on the page's clock, by polling.
func (pg *page) waitMs(ms int) {
	pg.t.Helper()
	pg.eval(`window.rvwT0 = performance.now()`, nil)
	pg.waitTrue(fmt.Sprintf("%d ms", ms), fmt.Sprintf(`performance.now() - window.rvwT0 > %d`, ms))
}

// TestBrowserPaletteOpenClose: ⌘K opens the palette with its input focused; Esc closes it and
// gives the focus back, and leaves an editor behind it open; ⌘K in a comment editor opens nothing.
func TestBrowserPaletteOpenClose(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.click("the filter", `document.querySelector(".filter")`, 0)
	pg.openPalette()
	pg.escape()
	pg.waitTrue("the palette closed", `!`+palOpen)
	pg.waitFocus("the filter", `document.querySelector(".filter")`)

	pg.goVia("long.txt", "long.txt")
	pg.waitTrue("long.txt loaded", shownPath+` === "long.txt" && `+fileLoaded)
	pg.click("new line 10", newLine(10), 0)
	pg.waitFocus("the diff editor", diffEditor)
	pg.insertText("draft")
	pg.cmdK()
	pg.waitMs(100)
	if pg.bool(palOpen) || pg.text(diffEditor+`.value`) != "draft" {
		pg.fatalf("⌘K in the comment editor opened the palette or changed its text")
	}
	pg.eval(`document.activeElement.blur()`, nil)
	pg.openPalette()
	pg.escape()
	pg.waitTrue("the palette closed", `!`+palOpen)
	if pg.bool(diffEditor+` === null`) || pg.text(diffEditor+`.value`) != "draft" {
		pg.fatalf("Esc in the palette closed the editor behind it")
	}
	pg.noPageErrors()
}

// TestBrowserPaletteRanking covers the fuzzy file ranking.
func TestBrowserPaletteRanking(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.openPalette()

	pg.wantFullTreeOrder()

	pg.palType("util")
	pg.waitTrue("util rows", `JSON.parse(`+palRows+`)[0] === "internal/util.go"`)
	r := pg.rows()
	at := func(s string) int {
		for k, v := range r {
			if strings.HasPrefix(v, s) {
				return k
			}
		}
		pg.fatalf("no row %q in %q", s, r)
		return -1
	}
	if at("internal/util.go") >= at("pkg/test_util.go") || at("pkg/test_util.go") >= at("pkg/util/other.go") {
		pg.fatalf("util rows %q; want internal/util.go, pkg/test_util.go, then pkg/util/other.go", r)
	}

	pg.palType("fbb")
	pg.waitTrue("camelCase hump", `JSON.parse(`+palRows+`)[0] === "web/fooBarBaz.ts"`)

	pg.palType("oldname")
	pg.waitTrue("the rename by its old path", `JSON.parse(`+palRows+`)[0] === "moved/renamed.txt ← legacy/oldname_util.txt"`)
	if !pg.bool(`(() => { const m = [...document.querySelectorAll(".pal-row mark")]; return m.length > 0 && m.every((e) => e.closest(".pal-path").classList.contains("old")); })()`) {
		pg.fatalf("old-path match is not highlighted on the old path only")
	}

	pg.palType("f0")
	pg.waitTrue("ties", `JSON.parse(`+palRows+`)[0] === "fill/f00.txt"`)
	r = pg.rows()
	for k := range 10 {
		if r[k] != fmt.Sprintf("fill/f%02d.txt", k) {
			pg.fatalf("equal scores are not in tree order: %q", r[:10])
		}
	}

	pg.palType("fill")
	pg.waitTrue("at most 50 rows", palCount+` === 50`)

	pg.palType("zzzqqq")
	pg.waitTrue("the empty state", palStatus+` === "No matching files" && `+palCount+` === 0`)

	if !pg.bool(`(` + treeRow("v.go") + `) === null`) {
		pg.fatalf("vendor/lib/v.go's row is visible before the palette opens it")
	}
	pg.palType("lib/v.go")
	pg.waitTrue("the vendored file", `JSON.parse(`+palRows+`)[0] === "vendor/lib/v.go"`)
	pg.enter()
	pg.waitTrue("the vendored file shown with its group expanded", shownPath+` === "vendor/lib/v.go" && (`+treeRow("v.go")+`) !== null`)
	pg.noPageErrors()
}

// wantFullTreeOrder waits for the empty query's rows and checks they are the first 50 changed
// files in the unfiltered tree order.
func (pg *page) wantFullTreeOrder() {
	pg.t.Helper()
	pg.waitTrue("50 rows for the empty query", palCount+` === 50`)
	if r := pg.rows(); len(r) < 2 || r[0] != "docs/a b:c.md" || r[1] != "fill/f00.txt" {
		pg.fatalf("empty query rows start %q; want tree order", r[:min(len(r), 3)])
	}
}

// TestBrowserPaletteIgnoresFilter: the palette lists changed files regardless of the file filter
// (REQ-NAV-006), so a file the filter hides opens through it, and the filter is kept.
func TestBrowserPaletteIgnoresFilter(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.click("the filter", `document.querySelector(".filter")`, 0)
	pg.insertText("long")
	pg.waitTrue("the tree filtered to long.txt", `document.querySelectorAll(".tree-row").length === 1 && (`+treeRow("long.txt")+`) !== null`)
	if !pg.bool(`(` + treeRow("util.go") + `) === null`) {
		pg.fatalf("the filter shows internal/util.go")
	}
	pg.openPalette()
	pg.wantFullTreeOrder()
	pg.goVia("util", "internal/util.go")
	pg.waitTrue("internal/util.go loaded", shownPath+` === "internal/util.go" && `+fileLoaded)
	if v := pg.text(`document.querySelector(".filter").value`); v != "long" {
		pg.fatalf("filter is %q after opening a file through the palette; want long", v)
	}
	pg.noPageErrors()
}

// palMarks is the first row's path text and the UTF-16 positions its marks cover.
const palMarks = `(() => { const p = document.querySelector(".pal-row .pal-path"); if (!p) return null;
	const pos = []; let at = 0;
	for (const n of p.childNodes) { const len = n.textContent.length; if (n.nodeName === "MARK") for (let k = 0; k < len; k++) pos.push(at + k); at += len; }
	return { path: p.textContent, marks: [...p.querySelectorAll("mark")].map((m) => m.textContent).join(""), pos }; })()`

// TestBrowserPaletteFoldPositions: a character whose lowercase is longer than itself does not
// shift the highlight off the matched characters.
func TestBrowserPaletteFoldPositions(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.openPalette()
	const istanbul = "src/\u0130stanbul.go"
	for _, tc := range []struct {
		q, path, marks string
		pos            []int
	}{
		{"stanbul", istanbul, "stanbul", []int{5, 6, 7, 8, 9, 10, 11}},
		{"\u0130st", istanbul, "\u0130st", []int{4, 5, 6}},
		{"MAIN", "src/main.go", "main", []int{4, 5, 6, 7}},
	} {
		pg.palType(tc.q)
		pg.waitTrue(tc.q+" ranked", `JSON.parse(`+palRows+`)[0] === `+jsString(tc.path))
		var got struct {
			Path  string `json:"path"`
			Marks string `json:"marks"`
			Pos   []int  `json:"pos"`
		}
		pg.eval(palMarks, &got)
		if got.Path != tc.path || got.Marks != tc.marks || fmt.Sprint(got.Pos) != fmt.Sprint(tc.pos) {
			pg.fatalf("%q: first row %q marks %q at %v; want %q marks %q at %v", tc.q, got.Path, got.Marks, got.Pos, tc.path, tc.marks, tc.pos)
		}
	}
	pg.noPageErrors()
}

// TestBrowserPaletteQueries: how the input is read, through the mode label and the rows.
func TestBrowserPaletteQueries(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.wrapFetch()
	pg.openPalette()
	for _, tc := range []struct{ q, mode string }{
		{"a/b.go:12", "Go to line 12 of a file"},
		{"long.txt:300", "Go to line 300 of a file"},
		{"docs/a b:c.md", "Files"},
		{"x:0", "Files"},
		{"x:007", "Files"},
		{"x:-1", "Files"},
		{"x:1.5", "Files"},
		{":0", "Files"},
		{":12", "Go to line 12 of the shown file"},
		{"#", "Search file contents"},
		{"#ab", "Search file contents"},
		{"#abc", "Search file contents"},
		{"#   ", "Search file contents"},
	} {
		pg.palType(tc.q)
		if got := pg.text(palMode); got != tc.mode {
			pg.fatalf("%q: mode %q, want %q", tc.q, got, tc.mode)
		}
	}
	pg.palType("docs/a b:c.md")
	pg.waitTrue("the colon path found", `JSON.parse(`+palRows+`)[0] === "docs/a b:c.md"`)
	pg.palType("#ab")
	pg.waitTrue("the minimum", palStatus+` === "Type at least 3 characters"`)
	pg.palType("#   ")
	pg.waitTrue("spaces searched", `window.rvwSearches.some((r) => r.url === "/api/search?q=%20%20%20")`)
	if pg.bool(`window.rvwSearches.some((r) => r.url === "/api/search?q=ab")`) {
		pg.fatalf("#ab, under the minimum, was searched")
	}
	pg.noPageErrors()
}

// TestBrowserPalettePathLine covers path:line navigation.
func TestBrowserPalettePathLine(t *testing.T) {
	t.Parallel()
	p, pg := startSearch(t)

	pg.goVia("long.txt:10", "long.txt")
	pg.waitTrue("line 10 selected", shownPath+` === "long.txt" && `+selectedLine("new", 10))
	pg.waitTrue("line 10 centred", centred("new", 10))

	pg.goVia("long.txt:200", "long.txt")
	pg.waitTrue("line 200 expanded and selected", selectedLine("new", 200))
	pg.waitTrue("line 200 centred", centred("new", 200))

	pg.goVia("long.txt:9999", "long.txt")
	pg.waitTrue("the notice", toastText+`.includes("Line 9999 is not shown")`)

	pg.goVia("big.txt:15000", "big.txt")
	pg.waitTrue("the large-file notice", shownPath+` === "big.txt" && `+toastText+`.includes("Line 15000 is not shown")`)

	pg.goVia("deleted.txt:3", "deleted.txt")
	pg.waitTrue("old line 3 selected", shownPath+` === "deleted.txt" && `+selectedLine("old", 3))

	pg.goVia("renamed.txt:5", "moved/renamed.txt")
	pg.waitTrue("the rename's new line 5", shownPath+` === "moved/renamed.txt" && `+selectedLine("new", 5))

	if !pg.bool(`(` + treeRow("zz.go") + `) === null`) {
		pg.fatalf("gen/zz.go's row is visible before the palette opens it")
	}
	pg.goVia("zz.go:3", "gen/zz.go")
	pg.waitTrue("the generated file revealed", shownPath+` === "gen/zz.go" && (`+treeRow("zz.go")+`) !== null`)

	pg.openPalette()
	pg.palType("qqqnothing:3")
	pg.waitTrue("no matching file", palStatus+` === "No matching file"`)
	pg.enter()
	pg.waitMs(100)
	if !pg.bool(palOpen) || pg.text(shownPath) != "gen/zz.go" {
		pg.fatalf("Enter on an unknown path navigated")
	}
	pg.escape()
	pg.waitTrue("the palette closed", `!`+palOpen)

	pg.goVia("long.txt:200", "long.txt")
	pg.waitTrue("line 200 selected", selectedLine("new", 200))
	pg.typeKey("c")
	pg.waitFocus("the diff editor", diffEditor)
	if got := pg.text(editorTarget); !strings.Contains(got, "200") {
		pg.fatalf("c after a reveal targets %q; want line 200", got)
	}
	pg.insertText("at 200")
	pg.cmdEnter()
	pg.waitBodies("at 200")
	c := p.commentNamed("at 200")
	if c.Target.Side != "new" || c.Target.Line != 200 || c.Target.Path != "long.txt" {
		pg.fatalf("comment target %+v; want long.txt new line 200", c.Target)
	}
	pg.noPageErrors()
}

// TestBrowserPaletteLineBeforeCounts: a line past the end is reported before the line counts
// arrive, from the file's own diff.
func TestBrowserPaletteLineBeforeCounts(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.send("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `(() => {
		const real = window.fetch;
		window.fetch = (input, init) => String(input).startsWith("/api/status") ? new Promise(() => {}) : real(input, init);
	})()`})
	pg.reload()
	pg.waitTrue("the first file loaded", shownPath+` !== "" && `+fileLoaded)
	pg.goVia("long.txt:9999", "long.txt")
	pg.waitTrue("the notice", shownPath+` === "long.txt" && `+toastText+`.includes("Line 9999 is not shown")`)
	if pg.bool(countsSettled) {
		pg.fatalf("the line counts arrived; the test needs them held")
	}
	pg.noPageErrors()
}

// TestBrowserPaletteSameFile: :N reveals a line of the shown file without reloading it, keeping
// context expanded earlier.
func TestBrowserPaletteSameFile(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.goVia("long.txt:200", "long.txt")
	pg.waitTrue("line 200", selectedLine("new", 200))
	pg.wrapFetch()
	pg.openPalette()
	pg.palType(":150")
	pg.enter()
	pg.waitTrue("line 150 selected", selectedLine("new", 150))
	pg.waitIdle()
	pg.eval(`window.rvwURLs.length = 0`, nil)
	pg.openPalette()
	pg.palType(":200")
	pg.enter()
	pg.waitTrue("line 200 selected", selectedLine("new", 200))
	pg.waitIdle()
	if pg.bool(`window.rvwURLs.some((u) => u.startsWith("/api/file?"))`) {
		pg.fatalf("a same-file reveal reloaded the file")
	}
	// Line 200 was expanded by the first reveal; a reveal that kept it fetches no context.
	if pg.bool(`window.rvwURLs.some((u) => u.startsWith("/api/file/context"))`) {
		pg.fatalf("context expanded earlier was lost: line 200 was fetched again")
	}
	pg.noPageErrors()
}

// TestBrowserPaletteContent covers #text results and navigation.
func TestBrowserPaletteContent(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.openPalette()
	pg.palType("#needle")
	pg.waitTrue("needle rows", palStatus+`.includes("matches")`)
	if !pg.bool(`[...document.querySelectorAll(".pal-row")].every((r) => r.querySelectorAll("mark").length === 1)`) {
		pg.fatalf("a content row does not have exactly one mark")
	}
	pg.enter()
	pg.waitTrue("needle.txt line 1", shownPath+` === "needle.txt" && `+selectedLine("new", 1))

	pg.goVia("#long 150", "long.txt:150")
	pg.waitTrue("line 150 outside hunks", shownPath+` === "long.txt" && `+selectedLine("new", 150))

	pg.goVia("#gonefile", "deleted.txt:3")
	pg.waitTrue("the deleted file's old line 3", shownPath+` === "deleted.txt" && `+selectedLine("old", 3))
	pg.noPageErrors()
}

// TestBrowserPaletteRequests covers the debounce, aborts, stale replies and failures.
func TestBrowserPaletteRequests(t *testing.T) {
	t.Parallel()
	p, pg := startSearch(t)
	pg.wrapFetch()
	pg.openPalette()

	pg.palType("long")
	pg.palType("#ne")
	pg.waitMs(400)
	if pg.searches() != 0 || pg.text(palStatus) != "Type at least 3 characters" {
		pg.fatalf("files mode or a short query sent a search")
	}
	pg.palType("#" + strings.Repeat("é", 130))
	pg.waitMs(400)
	if pg.searches() != 0 || !strings.Contains(pg.text(palStatus), "longer than 256 bytes") {
		pg.fatalf("an over-long query sent a search or showed no message")
	}

	pg.palType("#nee", "#need")
	pg.waitTrue("one reply", palStatus+`.includes("matches")`)
	if n := pg.searches(); n != 1 || !pg.bool(`window.rvwSearches[0].url === "/api/search?q=need"`) {
		pg.fatalf("%d searches; want one, for need", n)
	}
	// The debounce is 200 ms after the last keystroke; 5 ms allows for timer rounding.
	var wait float64
	pg.eval(`window.rvwSearches[0].t - window.rvwLastInput`, &wait)
	if wait < 195 {
		pg.fatalf("the search was sent %.1f ms after the last keystroke; want at least 200", wait)
	}

	// A superseded request is aborted and shows nothing.
	pg.eval(holdSearches, nil)
	pg.palType("#needle")
	pg.waitTrue("the held search", `window.rvwSearches.length === 2`)
	pg.eval(`window.rvwSearchHold = null`, nil)
	pg.palType("#gonefile")
	pg.waitTrue("gonefile rows", `JSON.parse(`+palRows+`)[0]?.startsWith("deleted.txt:3") === true`)
	pg.eval(`window.rvwRelease()`, nil)
	pg.searchesSettled()
	if r := pg.rows(); !pg.bool(`window.rvwSearches[1].aborted`) || pg.text(palStatus) != "1 match" || len(r) != 1 || !strings.HasPrefix(r[0], "deleted.txt:3") {
		pg.fatalf("the superseded search was not aborted, or changed the palette: %q, rows %q", pg.text(palStatus), r)
	}

	// A stale reply that ignores its signal is not applied.
	pg.eval(`window.rvwIgnoreAbort = true; `+holdSearches, nil)
	pg.palType("#needle")
	pg.waitTrue("the held search", `window.rvwSearches.length === 4`)
	pg.eval(`window.rvwSearchHold = null`, nil)
	pg.palType("#gonefile")
	pg.waitTrue("gonefile rows", `window.rvwSearches.length === 5 && `+palStatus+` === "1 match"`)
	pg.eval(`window.rvwRelease()`, nil)
	pg.searchesSettled()
	if r := pg.rows(); len(r) != 1 || !strings.HasPrefix(r[0], "deleted.txt:3") {
		pg.fatalf("a stale reply replaced the rows: %q", r)
	}

	// Close and reopen, and a mode change, drop a pending reply.
	for _, leave := range []func(){
		func() { pg.escape(); pg.waitTrue("closed", `!`+palOpen); pg.openPalette() },
		func() { pg.palType("long") },
	} {
		pg.eval(holdSearches, nil)
		n := pg.searches()
		pg.palType("#needle")
		pg.waitTrue("the held search sent", fmt.Sprintf(`window.rvwSearches.length === %d && window.rvwSearches.at(-1).url === "/api/search?q=needle"`, n+1))
		leave()
		pg.eval(`window.rvwSearchHold = null; window.rvwRelease()`, nil)
		pg.searchesSettled()
		if strings.Contains(pg.text(palStatus), "match") {
			pg.fatalf("a dropped reply was rendered: %q", pg.text(palStatus))
		}
	}

	// Enter while a reply is pending does not navigate.
	shown := pg.text(shownPath)
	pg.eval(holdSearches, nil)
	n := pg.searches()
	pg.palType("#needle")
	pg.waitTrue("the held search sent", fmt.Sprintf(`window.rvwSearches.length === %d && window.rvwSearches.at(-1).url === "/api/search?q=needle"`, n+1))
	pg.enter()
	pg.waitMs(100)
	if !pg.bool(palOpen) || pg.text(shownPath) != shown {
		pg.fatalf("Enter while a reply was pending navigated")
	}
	pg.eval(`window.rvwSearchHold = null; window.rvwRelease()`, nil)
	pg.waitTrue("the reply", palStatus+`.includes("matches")`)

	needle, deleted, renamed := p.fileIndex("needle.txt"), p.oldFileIndex("deleted.txt"), p.fileIndex("moved/renamed.txt")
	const badReply = "Search failed: bad reply from rvw"
	for _, tc := range []struct {
		fake, want string
	}{
		{`{ status: 400, body: "too short" }`, "Search text not accepted: too short"},
		{`{ status: 503, body: "busy" }`, "Search unavailable: busy"},
		{`{ status: 500, body: "boom" }`, "Search failed: boom"},
		// Controls: a match on a side its file has is accepted, so the refusals below are the side's.
		{fakeMatch("file", needle, "new", "x"), "1 match"},
		{fakeMatch("file", deleted, "old", "x"), "1 match"},
		{fakeMatch("file", renamed, "old", "x"), "1 match"},
		{fakeMatch("file", deleted, "new", "x"), badReply},
		{fakeMatch("file", needle, "old", "x"), badReply},
		{fakeMatch("file", 99999, "new", "x"), badReply},
		{fakeMatch("file", -1, "new", "x"), badReply},
		{fakeMatch("file", 1.5, "new", "x"), badReply},
		{fakeMatch("i", needle, "new", "x"), badReply},
		{fakeMatch("file", needle, "both", "x"), badReply},
		{fakeMatch("file", needle, "new", ""), badReply},
	} {
		pg.eval(`window.rvwSearchFake = `+tc.fake, nil)
		pg.palType("#failing")
		if tc.want == "1 match" {
			pg.waitTrue(tc.want, palStatus+` === "1 match" && `+palCount+` === 1`)
			// Clear the row, so the next case's empty list is its own.
			pg.palType("")
			continue
		}
		pg.waitTrue(tc.want, palStatus+` === `+jsString(tc.want)+` && `+palCount+` === 0`)
	}
	pg.eval(`window.rvwSearchFake = null`, nil)
	pg.palType("#gonefile")
	pg.waitTrue("usable after failures", palStatus+` === "1 match"`)
	pg.noPageErrors()
}

// fakeMatch is a window.rvwSearchFake reply holding one match whose file index is member.
func fakeMatch(member string, file any, side, match string) string {
	return fmt.Sprintf(`{ status: 200, body: { matches: [{ %s: %v, side: %q, line: 1, before: "", match: %q, after: "", cut_start: false, cut_end: false }], truncated: false } }`,
		member, file, side, match)
}

// TestBrowserPaletteTruncation: a search stopped early says so, and the list stays virtual.
func TestBrowserPaletteTruncation(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.wrapFetch()
	pg.openPalette()
	pg.palType("#needle")
	pg.waitTrue("the truncation message", palStatus+` === "500 matches. Search stopped early; some files may not be listed"`)
	if !pg.bool(`document.querySelectorAll(".pal-row").length <= Math.ceil(document.querySelector(".pal-list").clientHeight / 26) + 51`) {
		pg.fatalf("more result rows in the document than visible plus overscan")
	}
	pg.eval(`window.rvwSearchFake = { status: 200, body: { matches: [], truncated: true } }`, nil)
	pg.palType("#needles")
	pg.waitTrue("the message with no matches", palStatus+` === "0 matches. Search stopped early; some files may not be listed"`)
	pg.noPageErrors()
}

// TestBrowserPaletteEditorGate: choosing a result leaves the open editor first (Q-46).
func TestBrowserPaletteEditorGate(t *testing.T) {
	t.Parallel()
	p, pg := startSearch(t)
	pg.goVia("long.txt", "long.txt")
	pg.waitTrue("long.txt loaded", shownPath+` === "long.txt" && `+fileLoaded)

	pg.click("new line 10", newLine(10), 0)
	pg.waitFocus("the diff editor", diffEditor)
	pg.insertText("unsaved")
	pg.eval(`document.activeElement.blur()`, nil)
	pg.goVia("needle.txt", "needle.txt")
	pg.waitTrue("needle.txt shown", shownPath+` === "needle.txt" && `+fileLoaded)
	p.commentNamed("unsaved")

	pg.goVia("long.txt", "long.txt")
	pg.waitTrue("long.txt loaded", shownPath+` === "long.txt" && `+fileLoaded)
	pg.editInDiff("unsaved", "  ")
	pg.eval(`document.activeElement.blur()`, nil)
	for _, q := range []string{"needle.txt", ":100"} {
		pg.openPalette()
		pg.palType(q)
		pg.enter()
		pg.waitTrue("the empty-edit error", editorError+`.includes("cannot be empty")`)
		pg.waitIdle()
		if pg.text(shownPath) != "long.txt" || pg.bool(diffEditor+` === null`) || pg.bool(selectedLine("new", 100)) {
			pg.fatalf("%s: a refused save let the palette navigate", q)
		}
		pg.eval(editorError+` && (document.querySelector(".d-editor .editor-error").textContent = "")`, nil)
	}
	pg.noPageErrors()
}

// TestBrowserPaletteSupersession: a reveal whose context arrives after another file was shown
// changes nothing.
func TestBrowserPaletteSupersession(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.wrapFetch()
	pg.eval(`window.rvwContextHold = new Promise((r) => (window.rvwContextRelease = r))`, nil)
	pg.goVia("long.txt:200", "long.txt")
	pg.waitTrue("the context request", `window.rvwURLs.some((u) => u.startsWith("/api/file/context"))`)
	pg.typeKey("j")
	pg.waitTrue("another file", shownPath+` !== "long.txt" && `+fileLoaded)
	rows := pg.text(`String(document.querySelectorAll(".d-row").length)`)
	pg.eval(`window.rvwContextHold = null; window.rvwContextRelease()`, nil)
	pg.waitIdle()
	if pg.bool(`document.querySelector(".d-line.sel") !== null`) || pg.text(`String(document.querySelectorAll(".d-row").length)`) != rows {
		pg.fatalf("a superseded reveal changed the new file")
	}
	pg.noPageErrors()
}

// TestBrowserPaletteTextOnly: file text and paths render as text, never markup.
func TestBrowserPaletteTextOnly(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	pg.openPalette()
	pg.palType("#<b>x")
	pg.waitTrue("the markup line", `JSON.parse(`+palRows+`)[0] === "xss.txt:2<b>x</b>"`)
	pg.palType("#onerror")
	pg.waitTrue("the img line", `JSON.parse(`+palRows+`)[0] === "xss.txt:1<img src=x onerror=window.pwn=1>"`)
	pg.palType("odd/")
	pg.waitTrue("the odd path", `JSON.parse(`+palRows+`)[0] === "odd/<a&b>.txt"`)
	if !pg.bool(`[...document.querySelectorAll(".pal-row *")].every((e) => e.tagName === "SPAN" || e.tagName === "MARK")`) || pg.bool(`window.pwn !== undefined`) {
		pg.fatalf("a result row holds markup from file text or a path")
	}
	pg.noPageErrors()
}

// TestBrowserPaletteSearchLimits: the palette applies the search limits the session gives
// (REQ-NAV-007), and a session with invalid limits stops the page loading.
func TestBrowserPaletteSearchLimits(t *testing.T) {
	t.Parallel()
	_, pg := startSearch(t)
	// sessionStorage.rvwSearch, when set, replaces the session's search limits; "delete" removes them.
	pg.send("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `(() => {
		const real = window.fetch;
		window.fetch = async (input, init) => {
			const resp = await real(input, init);
			const set = sessionStorage.getItem("rvwSearch");
			if (!String(input).startsWith("/api/session") || set === null || !resp.ok) return resp;
			const s = await resp.json();
			if (set === "delete") delete s.search;
			else s.search = JSON.parse(set);
			return new Response(JSON.stringify(s), { status: resp.status, headers: { "Content-Type": "application/json" } });
		};
	})()`})

	pg.eval(`sessionStorage.setItem("rvwSearch", '{"min_chars":5,"max_bytes":8}')`, nil)
	pg.reload()
	pg.ready()
	pg.wrapFetch()
	pg.openPalette()
	pg.palType("#abcd")
	if got := pg.text(palStatus); got != "Type at least 5 characters" {
		pg.fatalf("#abcd: status %q; want Type at least 5 characters", got)
	}
	pg.waitMs(400)
	if n := pg.searches(); n != 0 {
		pg.fatalf("#abcd, under the minimum, sent %d searches", n)
	}
	pg.palType("#abcde")
	pg.waitTrue("the search for abcde", `window.rvwSearches.length === 1 && window.rvwSearches[0].url === "/api/search?q=abcde"`)
	// €€€€€ is 5 characters in 15 bytes.
	for _, q := range []string{"#abcdefghi", "#€€€€€"} {
		pg.palType(q)
		if got := pg.text(palStatus); got != "Search text is longer than 8 bytes" {
			pg.fatalf("%s: status %q; want Search text is longer than 8 bytes", q, got)
		}
		pg.waitMs(400)
		if n := pg.searches(); n != 1 {
			pg.fatalf("%s, over the maximum, sent a search: %d searches", q, n)
		}
	}

	for _, tc := range []struct{ limits, want string }{
		{`{"min_chars":0,"max_bytes":256}`, "search limits"},
		{`{"min_chars":3.5,"max_bytes":256}`, "min_chars"},
		{`{"min_chars":5,"max_bytes":4}`, "search limits"},
		{`delete`, "search limits"},
		{`{"min_chars":"3","max_bytes":256}`, "min_chars"},
	} {
		pg.eval(`sessionStorage.setItem("rvwSearch", `+jsString(tc.limits)+`)`, nil)
		// The marker lives only in the old document, so the wait cannot pass before the new one loads.
		pg.eval(`window.rvwBeforeReload = true`, nil)
		pg.send("Page.reload", nil)
		pg.waitTrue(tc.limits+": the load failure", `window.rvwBeforeReload === undefined && document.querySelector(".end h1")?.textContent === "Could not load the review"`)
		got := pg.text(`document.querySelector(".end p").textContent`)
		if !strings.HasPrefix(got, "unexpected server reply: ") || !strings.Contains(got, tc.want) || pg.bool(`document.querySelector(".tree-row") !== null`) {
			pg.fatalf("%s: load failure %q with the review shown or without %q", tc.limits, got, tc.want)
		}
	}
	pg.eval(`sessionStorage.removeItem("rvwSearch")`, nil)
	pg.noPageErrors()
}

// TestBrowserPaletteListedButNotShown: a match the view cannot show opens its file with a notice
// (REQ-NAV-006): a text side of a binary diff, and a line outside the hunks of a file too long to
// expand context in.
func TestBrowserPaletteListedButNotShown(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("a.txt", "a\n")
	w.write("bin2text.dat", "bin\x00ary\n")
	// Over LargeLines but under LargeBytes, so it is searched but large.
	many := strings.Replace(numbered("many", 20001), "many 15000\n", "manymarker\n", 1)
	w.write("many.txt", many)
	w.commit("base")
	w.write("a.txt", "a\nb\n")
	w.write("bin2text.dat", "binmarker\n")
	w.write("many.txt", strings.Replace(many, "many 5\n", "many five\n", 1))
	w.commit("head")
	p := w.start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()

	pg.goVia("#binmarker", "bin2text.dat:1")
	pg.waitTrue("the binary notice", shownPath+` === "bin2text.dat" && `+fileLoaded+` && `+toastText+`.includes("Line 1 is not shown")`)
	pg.goVia("#manymarker", "many.txt:15000")
	pg.waitTrue("the large-file notice", shownPath+` === "many.txt" && `+fileLoaded+` && `+toastText+`.includes("Line 15000 is not shown")`)
	pg.noPageErrors()
}
