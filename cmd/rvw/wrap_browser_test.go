//go:build darwin

package main

import (
	"fmt"
	"strings"
	"testing"
)

// Line-wrapping (REQ-DIFF-013, AC-DIFF-005) locators.
const (
	wrapBox = `document.querySelector(".wrap-toggle input")`
	// rowsTiled holds when the mounted diff rows, in order, each start where the one before ends:
	// no measured row overlaps or leaves a gap beside its neighbours. Notes, whose border overhangs
	// their fixed slot whether or not lines wrap, are left out.
	rowsTiled = `(() => { const top = (r) => parseFloat(r.style.top); const rows = [...document.querySelectorAll(".diff .vl-row:not(.d-note)")].sort((a, b) => top(a) - top(b));
		return rows.every((r, k) => k === 0 || Math.abs(top(r) - (top(rows[k - 1]) + rows[k - 1].offsetHeight)) <= 1); })()`
)

// wrapLine is 2,000 characters without a space, so only breaking inside a word wraps it.
var wrapLine = strings.Repeat("abcdefghij", 200)

// wrapWorld's last commit replaces line 3 of a/wide.go with a 2,000-character line followed by 60
// added short lines, and changes alternate runs of 10 lines in b/many.txt, 10,000 lines of 200
// characters each.
func wrapWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.write("a/wide.go", "package a\n\nvar s = \"short\"\n\nfunc f() {}\n")
	many := func(tag string) string {
		var b strings.Builder
		for n := 1; n <= 10000; n++ {
			if (n-1)/10%2 == 0 {
				fmt.Fprintf(&b, "%s %05d %s\n", tag, n, strings.Repeat("y", 188))
			} else {
				fmt.Fprintf(&b, "same %05d %s\n", n, strings.Repeat("z", 188))
			}
		}
		return b.String()
	}
	w.write("b/many.txt", many("base"))
	w.commit("base")
	var tail strings.Builder
	for n := 1; n <= 60; n++ {
		fmt.Fprintf(&tail, "var v%d = %d\n", n, n)
	}
	w.write("a/wide.go", "package a\n\nvar s = \""+wrapLine+"\"\n"+tail.String()+"\nfunc f() {}\n")
	w.write("b/many.txt", many("head"))
	w.commit("head")
	return w
}

// wrappedRow is the geometry of the row holding new-side line 3, in px.
type wrappedRow struct {
	ScrollWidth, ClientWidth float64
	Top, Height, NextTop     float64
	NumberTop, NumberHeight  float64
	MarkTop, MarkHeight      float64
	TextRight, CodeRight     float64
	TextBottom               float64
	// Halves are the heights of a split row's two halves; empty in unified view.
	Halves []float64
	Text   string
}

func (pg *page) wrappedRow() wrappedRow {
	pg.t.Helper()
	var got wrappedRow
	pg.eval(`(() => { const ln = `+newLine(3)+`; const row = ln.closest(".vl-row"); const d = `+diffScroller+`;
		const cell = ln.closest(".d-half") ?? row;
		const box = (n) => { const r = document.createRange(); r.selectNodeContents(n); return r.getBoundingClientRect(); };
		const rr = row.getBoundingClientRect(), num = box(ln), mark = box(cell.querySelector(".d-mark")), text = box(cell.querySelector(".d-text"));
		const next = [...document.querySelectorAll(".diff .vl-row")].filter((r) => r.offsetTop > row.offsetTop).sort((a, b) => a.offsetTop - b.offsetTop)[0];
		return { ScrollWidth: d.scrollWidth, ClientWidth: d.clientWidth, Top: rr.top, Height: rr.height, NextTop: next.getBoundingClientRect().top,
			NumberTop: num.top, NumberHeight: num.height, MarkTop: mark.top, MarkHeight: mark.height,
			TextRight: text.right, CodeRight: cell.querySelector(".d-code").getBoundingClientRect().right, TextBottom: text.bottom,
			Halves: [...row.querySelectorAll(".d-half")].map((h) => h.getBoundingClientRect().height),
			Text: cell.querySelector(".d-text").textContent }; })()`, &got)
	return got
}

// checkWrapped checks the 2,000-character line is shown whole within its column, on rows that
// tile, with its number and change mark on the first visual line only.
func checkWrapped(pg *page, view string) {
	pg.t.Helper()
	pg.frames()
	r := pg.wrappedRow()
	if want := `"` + wrapLine + `"`; !strings.Contains(r.Text, want) {
		pg.t.Errorf("%s: the line's text is %d characters, want it to hold the whole 2,000-character string", view, len(r.Text))
	}
	if r.ScrollWidth > r.ClientWidth {
		pg.t.Errorf("%s: the diff scrolls sideways: scroll width %v > client width %v", view, r.ScrollWidth, r.ClientWidth)
	}
	// 2,000 characters of at least 6px cannot fit a 1,280px window in fewer than 10 rows of 20px.
	if r.Height < 200 {
		pg.t.Errorf("%s: the wrapped row is %vpx tall, want at least 200", view, r.Height)
	}
	if r.TextRight > r.CodeRight+1 || r.TextBottom > r.Top+r.Height+1 {
		pg.t.Errorf("%s: the text ends at (%v, %v), outside its column (right %v, bottom %v)", view, r.TextRight, r.TextBottom, r.CodeRight, r.Top+r.Height)
	}
	for _, c := range []struct {
		what        string
		top, height float64
	}{{"line number", r.NumberTop, r.NumberHeight}, {"change mark", r.MarkTop, r.MarkHeight}} {
		// The text's box is its glyphs' height, inside the 20px line box.
		if c.top < r.Top || c.top+c.height > r.Top+20 {
			pg.t.Errorf("%s: the %s spans %v..%v, want the first visual line only (%v..%v)", view, c.what, c.top, c.top+c.height, r.Top, r.Top+20)
		}
	}
	if d := r.NextTop - (r.Top + r.Height); d < -1 || d > 1 {
		pg.t.Errorf("%s: the next row starts %vpx from the wrapped row's end, want 0", view, d)
	}
	for k, h := range r.Halves {
		if d := h - r.Height; d < -1 || d > 1 {
			pg.t.Errorf("%s: half %d is %vpx tall in a %vpx row, want the taller half's height", view, k, h, r.Height)
		}
	}
	if !pg.bool(rowsTiled) {
		pg.t.Errorf("%s: mounted rows overlap or leave gaps", view)
	}
}

// TestBrowserWrapLongLine: with Wrap lines on, a 2,000-character line shows whole in its column
// in the unified and split views, and the setting survives a reload.
func TestBrowserWrapLongLine(t *testing.T) {
	t.Parallel()
	p := wrapWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.waitTrue("a/wide.go shown", shownPath+` === "a/wide.go"`)
	if pg.bool(`(` + wrapBox + `).checked`) {
		pg.fatalf("Wrap lines is on before it was chosen")
	}
	pg.waitTrue("new line 3 shown", newLine(3)+` !== null`)
	if r := pg.wrappedRow(); r.ScrollWidth <= r.ClientWidth || r.Height != 20 {
		pg.fatalf("test would be vacuous: unwrapped, the diff's scroll width is %v of %v and the row %vpx tall", r.ScrollWidth, r.ClientWidth, r.Height)
	}

	pg.click("Wrap lines", wrapBox, 0)
	pg.waitTrue("wrapping on", `document.querySelector(".diff.wrap") !== null`)
	// Keys typed while the checkbox has focus are not shortcuts.
	pg.eval(`document.activeElement.blur()`, nil)
	checkWrapped(pg, "unified")
	pg.typeKey("s")
	pg.waitTrue("split view", `document.querySelector(".diff.split") !== null`)
	checkWrapped(pg, "split")
	if pg.bool(`document.querySelector(".d-hscroll").hidden === false`) {
		t.Errorf("split view shows its sideways scrollbar while lines wrap")
	}

	pg.reload()
	pg.ready()
	pg.waitTrue("wrapping on after the reload", `document.querySelector(".diff.wrap.split") !== null && (`+wrapBox+`).checked`)
	checkWrapped(pg, "split after a reload")
	pg.noPageErrors()
}

// TestBrowserWrapKeepsView: in a 10,000-line file of wrapped lines the document holds the visible
// rows plus at most 50 overscan rows; toggling Wrap lines, and the view while it is on, keeps the
// line in view; and jumping to the last hunk and back leaves the first row at the top.
func TestBrowserWrapKeepsView(t *testing.T) {
	t.Parallel()
	p := wrapWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.click("many.txt", treeRow("many.txt"), 0)
	pg.waitTrue("b/many.txt loaded", shownPath+` === "b/many.txt" && `+fileLoaded+` && `+newLine(1)+` !== null`)
	pg.typeKey("w")
	pg.waitTrue("wrapping on", `document.querySelector(".diff.wrap") !== null && (`+wrapBox+`).checked`)
	pg.frames()
	var h float64
	pg.eval(newLine(1)+`.closest(".vl-row").offsetHeight`, &h)
	if h < 40 {
		pg.fatalf("test would be vacuous: a 200-character line's row is %vpx tall, so it does not wrap", h)
	}

	pg.eval(diffScroller+`.scrollTop = `+diffScroller+`.scrollHeight / 2`, nil)
	pg.frames()
	var rows struct{ Mounted, Visible int }
	pg.eval(`(() => { const v = `+diffScroller+`.getBoundingClientRect(); const all = [...document.querySelectorAll(".diff .vl-row")];
		return { Mounted: all.length, Visible: all.filter((r) => { const b = r.getBoundingClientRect(); return b.bottom > v.top && b.top < v.bottom; }).length }; })()`, &rows)
	if rows.Mounted > rows.Visible+50 {
		t.Errorf("the document holds %d rows with %d visible, want at most %d", rows.Mounted, rows.Visible, rows.Visible+50)
	}
	if !pg.bool(rowsTiled) {
		t.Errorf("mounted rows overlap or leave gaps after scrolling to the middle")
	}

	inView := func(after string, line string) {
		t.Helper()
		pg.frames()
		if !pg.bool(`(() => { const e = ` + line + `; if (!e) return false; const r = e.getBoundingClientRect(), v = ` + diffScroller + `.getBoundingClientRect();
			return r.top >= v.top && r.bottom <= v.bottom; })()`) {
			t.Errorf("after %s, %s is not in view", after, line)
		}
		if !pg.bool(rowsTiled) {
			t.Errorf("after %s, mounted rows overlap or leave gaps", after)
		}
	}
	// The views anchor on new-side lines: unified shows a run's deleted lines above its added ones.
	shownNew := func() string {
		t.Helper()
		var n int
		pg.eval(`(() => { const v = `+diffScroller+`.getBoundingClientRect(); let best = null;
			for (const g of document.querySelectorAll('.d-ln[data-side="new"][data-line]')) {
				const r = g.getBoundingClientRect();
				if (r.top >= v.top + 40 && r.bottom <= v.bottom - 20 && (!best || r.top < best.top)) best = { line: Number(g.dataset.line), top: r.top };
			}
			return best?.line ?? 0; })()`, &n)
		if n == 0 {
			pg.fatalf("no new-side line is wholly in view")
		}
		return newLine(n)
	}
	line := shownNew()
	pg.typeKey("w")
	pg.waitTrue("wrapping off", `document.querySelector(".diff.wrap") === null`)
	inView("turning Wrap lines off", line)
	line = shownNew()
	pg.typeKey("w")
	pg.waitTrue("wrapping on", `document.querySelector(".diff.wrap") !== null`)
	inView("turning Wrap lines on", line)
	line = shownNew()
	pg.typeKey("s")
	pg.waitTrue("split view", `document.querySelector(".diff.split") !== null`)
	inView("choosing split view", line)
	line = shownNew()
	pg.typeKey("s")
	pg.waitTrue("unified view", `document.querySelector(".diff.split") === null`)
	inView("choosing unified view", line)

	pg.eval(diffScroller+`.scrollTop = 1e9`, nil)
	pg.frames()
	pg.typeKey("p")
	pg.frames()
	if !pg.bool(`(() => { const v = ` + diffScroller + `.getBoundingClientRect(); return [...document.querySelectorAll(".d-hunk")].some((e) => { const r = e.getBoundingClientRect(); return r.top >= v.top - 1 && r.bottom <= v.bottom; }); })()`) {
		t.Errorf("after p at the end of the file, no hunk header is in view")
	}
	pg.eval(diffScroller+`.scrollTop = 0`, nil)
	pg.frames()
	var first struct {
		Top  string
		Gap  float64
		Text string
	}
	pg.eval(`(() => { const rows = [...document.querySelectorAll(".diff .vl-row")].sort((a, b) => parseFloat(a.style.top) - parseFloat(b.style.top));
		return { Top: rows[0].style.top, Gap: document.querySelector(".diff .vl-inner").getBoundingClientRect().top - `+diffScroller+`.getBoundingClientRect().top, Text: rows[0].textContent }; })()`, &first)
	if first.Top != "0px" || first.Gap != 0 || !strings.HasPrefix(first.Text, "Large file") {
		t.Errorf("back at the top, the first row %q is placed at %s with the rows %vpx below the view's top, want the large-file note at 0px and 0", first.Text, first.Top, first.Gap)
	}
	if !pg.bool(rowsTiled) {
		t.Errorf("back at the top, mounted rows overlap or leave gaps: %s", pg.text(`JSON.stringify([...document.querySelectorAll(".diff .vl-row")].sort((a, b) => parseFloat(a.style.top) - parseFloat(b.style.top)).slice(0, 12).map((r) => [r.style.top, r.offsetHeight, r.className]))`))
	}
	pg.noPageErrors()
}

// TestBrowserWrapKeepsTallLine: when a wrapped line mostly scrolled past shrinks, because the
// window widened or Wrap lines was turned off, the line stays in view.
func TestBrowserWrapKeepsTallLine(t *testing.T) {
	t.Parallel()
	p := wrapWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.waitTrue("a/wide.go shown", shownPath+` === "a/wide.go" && `+newLine(3)+` !== null`)
	pg.typeKey("w")
	pg.waitTrue("wrapping on", `document.querySelector(".diff.wrap") !== null`)
	resize := func(width int) {
		t.Helper()
		pg.send("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": viewHeight, "deviceScaleFactor": 1, "mobile": false})
		pg.frames()
	}
	type span struct{ Top, Bottom, ViewTop, ViewBottom float64 }
	row := func() span {
		t.Helper()
		var got span
		pg.eval(`(() => { const e = `+newLine(3)+`; const v = `+diffScroller+`.getBoundingClientRect(); if (!e) return { Top: 0, Bottom: 0, ViewTop: v.top, ViewBottom: v.bottom };
			const r = e.closest(".vl-row").getBoundingClientRect(); return { Top: r.top, Bottom: r.bottom, ViewTop: v.top, ViewBottom: v.bottom }; })()`, &got)
		return got
	}
	// Only the wrapped line's last 40px stay in view.
	scrollPast := func() span {
		t.Helper()
		pg.eval(`(() => { const row = `+newLine(3)+`.closest(".vl-row"); `+diffScroller+`.scrollTop = row.offsetTop + row.offsetHeight - 40; })()`, nil)
		pg.frames()
		r := row()
		if r.Top > r.ViewTop-100 || r.Bottom-r.ViewTop > 41 {
			pg.fatalf("test would be vacuous: the wrapped row spans %v..%v with the view's top at %v", r.Top, r.Bottom, r.ViewTop)
		}
		return r
	}
	shown := func(after string) {
		t.Helper()
		if r := row(); r.Bottom == 0 || r.Bottom <= r.ViewTop || r.Top >= r.ViewBottom {
			t.Errorf("after %s, new line 3's row spans %v..%v, outside the view %v..%v", after, r.Top, r.Bottom, r.ViewTop, r.ViewBottom)
		}
		if !pg.bool(rowsTiled) {
			t.Errorf("after %s, mounted rows overlap or leave gaps", after)
		}
	}

	resize(800)
	narrow := scrollPast()
	resize(viewWidth)
	if r := row(); r.Bottom-r.Top >= narrow.Bottom-narrow.Top-60 {
		pg.fatalf("test would be vacuous: widening the window shrank the wrapped row from %vpx to %vpx", narrow.Bottom-narrow.Top, r.Bottom-r.Top)
	}
	shown("widening the window")

	scrollPast()
	pg.typeKey("w")
	pg.waitTrue("wrapping off", `document.querySelector(".diff.wrap") === null`)
	pg.frames()
	shown("turning Wrap lines off")
	pg.noPageErrors()
}
