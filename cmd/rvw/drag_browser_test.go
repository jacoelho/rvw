package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// selTexts is the code text of the highlighted lines, in display order, as JSON.
const selTexts = `JSON.stringify([...document.querySelectorAll(".sel .d-code")].map((e) => e.textContent))`

// selNewLines is the lowest and highest highlighted new-side line among the rows drawn, as "lo hi",
// or "0 0" when none is.
const selNewLines = `(() => { const ns = [...document.querySelectorAll('.sel .d-ln[data-side="new"][data-line]')].map((e) => Number(e.dataset.line));
	return ns.length ? Math.min(...ns) + " " + Math.max(...ns) : "0 0"; })()`

// dragWorld's last commit replaces lines 4 to 6 of a/x.txt, so its diff shows old rows "old 4" to
// "old 6" before new rows "new 4" to "new 6", between context lines.
func dragWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	var base, head strings.Builder
	for n := 1; n <= 10; n++ {
		fmt.Fprintf(&base, "line %d\n", n)
		if n >= 4 && n <= 6 {
			fmt.Fprintf(&head, "new %d\n", n)
		} else {
			fmt.Fprintf(&head, "line %d\n", n)
		}
	}
	w.write("a/x.txt", strings.ReplaceAll(base.String(), "line 4\nline 5\nline 6\n", "old 4\nold 5\nold 6\n"))
	w.commit("base")
	w.write("a/x.txt", head.String())
	w.commit("head")
	return w
}

// pressAt moves the mouse onto the element as locate finds it and presses the left button there.
func (pg *page) pressAt(what, elementExpr string, modifiers int) (x, y float64) {
	pg.t.Helper()
	x, y = pg.locate(what, elementExpr)
	pg.send("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "modifiers": modifiers})
	pg.send("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "modifiers": modifiers,
		"button": "left", "buttons": 1, "clickCount": 1})
	return x, y
}

// dragTo moves the mouse to (x, y) with the left button held.
func (pg *page) dragTo(x, y float64, modifiers int) {
	pg.t.Helper()
	pg.send("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "modifiers": modifiers,
		"button": "left", "buttons": 1})
}

// dragToEl moves the mouse, button held, onto the element as locate finds it.
func (pg *page) dragToEl(what, elementExpr string) (x, y float64) {
	pg.t.Helper()
	x, y = pg.locate(what, elementExpr)
	pg.dragTo(x, y, 0)
	return x, y
}

// releaseAt releases the left button at (x, y).
func (pg *page) releaseAt(x, y float64, modifiers int) {
	pg.t.Helper()
	pg.send("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x, "y": y, "modifiers": modifiers,
		"button": "left", "buttons": 0, "clickCount": 1})
}

func (pg *page) waitSel(want ...string) {
	pg.t.Helper()
	pg.waitTrue(fmt.Sprintf("the highlighted lines %q", want), selTexts+` === `+jsString(jsonList(want)))
}

func jsonList(s []string) string {
	q := make([]string, len(s))
	for i, v := range s {
		q[i] = strconv.Quote(v)
	}
	return "[" + strings.Join(q, ",") + "]"
}

// TestBrowserDragSelectsRange: dragging across the gutter highlights the lines from the pressed line
// to the line under the pointer, on the pressed side only, keeps the last line over a row without
// one, and opens one editor on that range on release; the click the release produces opens nothing
// else, and the next click is an ordinary one.
func TestBrowserDragSelectsRange(t *testing.T) {
	t.Parallel()
	p := dragWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.waitTrue("old and new rows", oldLine(4)+` !== null && `+newLine(4)+` !== null`)
	pg.commentOnNewLine(4, "seeded")
	if got := pg.text(selTexts); got != "[]" {
		pg.fatalf("lines are highlighted before any drag: %s", got)
	}

	pg.pressAt("new line 3", newLine(3), 0)
	pg.dragToEl("new line 5", newLine(5))
	pg.waitSel("line 3", "new 4", "new 5")
	pg.dragToEl("the seeded comment", `document.querySelector(".d-comment")`)
	pg.frames()
	if got, want := pg.text(selTexts), jsonList([]string{"line 3", "new 4", "new 5"}); got != want {
		pg.fatalf("over the comment the highlighted lines are %s, want %s", got, want)
	}
	x, y := pg.dragToEl("new line 7", newLine(7))
	pg.waitSel("line 3", "new 4", "new 5", "new 6", "line 7")
	pg.releaseAt(x, y, 0)

	pg.waitTrue("the range editor", editorTarget+`.startsWith("lines 3–7")`)
	pg.frames()
	if got := pg.text(editorTarget); !strings.HasPrefix(got, "lines 3–7") {
		pg.fatalf("after the release's click the editor targets %q", got)
	}
	if n := pg.text(`String(document.querySelectorAll(".d-editor").length)`); n != "1" {
		pg.fatalf("%s editors are open", n)
	}
	pg.waitFocus("the diff editor", diffEditor)
	pg.insertText("ranged")
	pg.cmdEnter()
	pg.waitTrue("the editor closed", diffEditor+` === null`)
	var found bool
	for _, c := range p.sessionComments() {
		if c.Body == "ranged" {
			found = c.Target.Target == "range" && c.Target.Side == "new" && c.Target.Start == 3 && c.Target.End == 7
		}
	}
	if !found {
		pg.fatalf("the session holds %+v, want a range comment on new lines 3–7", p.sessionComments())
	}

	pg.click("new line 1", newLine(1), 0)
	pg.waitTrue("the line editor", editorTarget+`.startsWith("line 1")`)
	pg.noPageErrors()
}

// TestBrowserDragRetargetsDraft: dragging upwards with a draft open moves the draft, its text kept,
// to the range from the lower line to the higher.
func TestBrowserDragRetargetsDraft(t *testing.T) {
	t.Parallel()
	p := dragWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.click("new line 2", newLine(2), 0)
	pg.waitFocus("the diff editor", diffEditor)
	pg.insertText("keep")
	pg.waitTrue("the line editor", editorTarget+`.startsWith("line 2")`)

	pg.pressAt("new line 6", newLine(6), 0)
	x, y := pg.dragToEl("new line 4", newLine(4))
	pg.waitSel("new 4", "new 5", "new 6")
	pg.releaseAt(x, y, 0)

	pg.waitTrue("the range editor", editorTarget+`.startsWith("lines 4–6")`)
	if v := pg.text(diffEditor + `.value`); v != "keep" {
		pg.fatalf("the retargeted editor holds %q", v)
	}
	if n := pg.text(`String(document.querySelectorAll(".d-editor").length)`); n != "1" {
		pg.fatalf("%s editors are open", n)
	}
	pg.waitFocus("the diff editor", diffEditor)
	pg.cmdEnter()
	pg.waitTrue("the editor closed", diffEditor+` === null`)
	cs := p.sessionComments()
	if len(cs) != 1 || cs[0].Body != "keep" || cs[0].Target.Target != "range" || cs[0].Target.Side != "new" || cs[0].Target.Start != 4 || cs[0].Target.End != 6 {
		pg.fatalf("the session holds %+v, want one range comment on new lines 4–6", cs)
	}
	pg.noPageErrors()
}

// TestBrowserDragWithinOneLine: a press and release that stay on one line, the pointer jittering,
// is a click: a single line, or with Shift an extension of the selection.
func TestBrowserDragWithinOneLine(t *testing.T) {
	t.Parallel()
	p := dragWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()

	x, y := pg.pressAt("new line 2", newLine(2), 0)
	pg.dragTo(x+3, y+2, 0)
	pg.releaseAt(x+3, y+2, 0)
	pg.waitTrue("the line editor", editorTarget+`.startsWith("line 2")`)

	x, y = pg.pressAt("new line 5", newLine(5), modShift)
	pg.dragTo(x+3, y, modShift)
	pg.releaseAt(x+3, y, modShift)
	pg.waitTrue("the extended editor", editorTarget+`.startsWith("lines 2–5")`)
	pg.noPageErrors()
}

// TestBrowserDragRefusedRetarget: a drag that cannot move the open editor, whose create's reply was
// lost, says why and shows the editor's own line highlighted again, not the dragged range.
func TestBrowserDragRefusedRetarget(t *testing.T) {
	t.Parallel()
	p := dragWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.wrapCommentPuts()
	pg.click("new line 3", newLine(3), 0)
	pg.waitFocus("the diff editor", diffEditor)
	pg.insertText("here")
	pg.sendLost()

	pg.pressAt("new line 5", newLine(5), 0)
	x, y := pg.dragToEl("new line 7", newLine(7))
	pg.waitSel("new 5", "new 6", "line 7")
	pg.releaseAt(x, y, 0)

	pg.waitTrue("the retarget notice", editorError+`.startsWith("This comment may already be saved here")`)
	pg.waitSel("line 3")
	if got := pg.text(editorTarget); !strings.HasPrefix(got, "line 3") {
		pg.fatalf("after the refused drag the editor targets %q", got)
	}
	pg.noPageErrors()
}

// TestBrowserDragAutoScroll: holding a drag at the diff's bottom or top edge scrolls it, and the
// range keeps extending to the lines scrolled into view without the pointer moving; the scrolling
// stops on release.
func TestBrowserDragAutoScroll(t *testing.T) {
	t.Parallel()
	lines := func(pg *page) (lo, hi int) {
		pg.t.Helper()
		if _, err := fmt.Sscan(pg.text(selNewLines), &lo, &hi); err != nil {
			pg.fatalf("highlighted lines: %v", err)
		}
		return lo, hi
	}
	// inView is the lowest and highest new line wholly inside the diff's view.
	inView := func(pg *page) (lo, hi int) {
		pg.t.Helper()
		var got struct{ Lo, Hi int }
		pg.eval(`(() => { const v = `+diffScroller+`.getBoundingClientRect(); let lo = 0, hi = 0;
			for (const g of document.querySelectorAll('.d-ln[data-side="new"][data-line]')) {
				const r = g.getBoundingClientRect(), n = Number(g.dataset.line);
				if (r.top >= v.top && r.bottom <= v.bottom) { if (!lo || n < lo) lo = n; if (n > hi) hi = n; }
			}
			return { Lo: lo, Hi: hi }; })()`, &got)
		return got.Lo, got.Hi
	}
	edge := func(pg *page, bottom bool) float64 {
		pg.t.Helper()
		var y float64
		side := "top + 5"
		if bottom {
			side = "bottom - 5"
		}
		pg.eval(diffScroller+`.getBoundingClientRect().`+side, &y)
		return y
	}
	saved := func(pg *page, p *proc, body string) sessionComment {
		pg.t.Helper()
		pg.waitFocus("the diff editor", diffEditor)
		pg.insertText(body)
		pg.cmdEnter()
		pg.waitTrue("the editor closed", diffEditor+` === null`)
		for _, c := range p.sessionComments() {
			if c.Body == body {
				return c
			}
		}
		pg.fatalf("no comment %q was saved", body)
		return sessionComment{}
	}

	t.Run("bottom", func(t *testing.T) {
		t.Parallel()
		p := longWorld(t).start(procOptions{}, "last")
		p.waitURL()
		pg := openPage(t, p)
		pg.ready()
		pg.waitTrue("long.go shown", shownPath+` === "a/long.go"`)
		_, last := inView(pg)
		if last == 0 || last >= 150 {
			pg.fatalf("test would be vacuous: the last line in view is %d", last)
		}
		x, _ := pg.pressAt("new line 2", newLine(2), 0)
		y := edge(pg, true)
		pg.dragTo(x, y, 0)
		pg.waitTrue("the range past the view", fmt.Sprintf(`Number(%s.split(" ")[1]) > %d`, selNewLines, last))
		_, m := lines(pg)
		pg.waitTrue("the range still growing", fmt.Sprintf(`Number(%s.split(" ")[1]) > %d`, selNewLines, m))
		pg.releaseAt(x, y, 0)
		pg.waitTrue("the range editor", editorTarget+`.startsWith("lines 2–")`)
		pg.frames()
		before := pg.text(`String(` + diffScroller + `.scrollTop)`)
		pg.frames()
		if after := pg.text(`String(` + diffScroller + `.scrollTop)`); after != before {
			pg.fatalf("the diff kept scrolling after the release: %s, then %s", before, after)
		}
		c := saved(pg, p, "down")
		if c.Target.Target != "range" || c.Target.Start != 2 || c.Target.End <= m {
			pg.fatalf("saved %+v, want a range from line 2 past line %d", c.Target, m)
		}
		pg.noPageErrors()
	})

	t.Run("top", func(t *testing.T) {
		t.Parallel()
		p := longWorld(t).start(procOptions{}, "last")
		p.waitURL()
		pg := openPage(t, p)
		pg.ready()
		pg.waitTrue("long.go shown", shownPath+` === "a/long.go"`)
		pg.eval(diffScroller+`.scrollTop = `+diffScroller+`.scrollHeight`, nil)
		pg.frames()
		first, last := inView(pg)
		if first <= 100 {
			pg.fatalf("test would be vacuous: the first line in view is %d", first)
		}
		x, _ := pg.pressAt(fmt.Sprintf("new line %d", last), newLine(last), 0)
		y := edge(pg, false)
		pg.dragTo(x, y, 0)
		pg.waitTrue("the range above the view", fmt.Sprintf(`(() => { const lo = Number(%s.split(" ")[0]); return lo > 0 && lo < %d; })()`, selNewLines, first))
		lo, _ := lines(pg)
		pg.releaseAt(x, y, 0)
		pg.waitTrue("the range editor", editorTarget+`.startsWith("lines ")`)
		c := saved(pg, p, "up")
		if c.Target.Target != "range" || c.Target.End != last || c.Target.Start > lo {
			pg.fatalf("saved %+v, want a range ending at line %d starting at or above line %d", c.Target, last, lo)
		}
		pg.noPageErrors()
	})
}

// TestBrowserDragReleasedOutsideList: a drag released outside the diff list still opens its
// editor, and the click that release produced, which the list never sees, leaves the next click
// inside the list (here Save's, not from a pointer press) to do its work.
func TestBrowserDragReleasedOutsideList(t *testing.T) {
	t.Parallel()
	p := dragWorld(t).start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()

	pg.pressAt("new line 3", newLine(3), 0)
	pg.dragToEl("new line 5", newLine(5))
	pg.waitSel("line 3", "new 4", "new 5")
	var at struct {
		X, Y    float64
		Outside bool
	}
	pg.eval(`(() => { const r = document.querySelector(".file-header").getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + r.height / 2;
		const e = document.elementFromPoint(x, y); return { X: x, Y: y, Outside: !!e && !`+diffScroller+`.contains(e) }; })()`, &at)
	if !at.Outside {
		pg.fatalf("the release point is inside the list")
	}
	pg.releaseAt(at.X, at.Y, 0)

	pg.waitTrue("the range editor", editorTarget+`.startsWith("lines 3–5")`)
	pg.waitFocus("the diff editor", diffEditor)
	pg.frames()
	pg.insertText("outside")
	pg.clickEl("Save", button(".d-editor", "Save"))
	pg.waitTrue("the editor closed after Save", diffEditor+` === null`)
	var found bool
	for _, c := range p.sessionComments() {
		found = found || c.Body == "outside" && c.Target.Target == "range" && c.Target.Side == "new" && c.Target.Start == 3 && c.Target.End == 5
	}
	if !found {
		pg.fatalf("the session holds %+v, want a range comment on new lines 3–5", p.sessionComments())
	}
	pg.noPageErrors()
}
