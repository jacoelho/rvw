//go:build darwin

package main

import (
	"strings"
	"testing"
	"time"
)

const (
	changedBanner    = `document.querySelector(".changed-banner")`
	movedBanner      = `document.querySelector(".moved-banner")`
	supersededBanner = `document.querySelector(".superseded-banner")`
)

// TestBrowserRefresh covers REQ-SES-010, REQ-SES-013, REQ-SES-015 and REQ-DRF-006 through rvw and
// a browser: a worktree edit made while `rvw uncommitted` runs is noticed within 20 s; Refresh from
// the notice reloads the page on the new snapshot, with the comment whose line changed moved to its
// file and listed; a second tab still on the old snapshot reloads by itself; the result is the
// refreshed review.
func TestBrowserRefresh(t *testing.T) {
	// Not parallel, like TestBrowserPageSurvivesReloadAndSiblingClose.
	w := newWorld(t)
	w.write("b/x.go", "package b\n\nvar Y = 1\n\nvar Z = 1\n")
	w.commit("base")
	w.write("b/x.go", "package b\n\nvar Y = 2\n\nvar Z = 2\n")
	p := w.start(procOptions{}, "uncommitted")
	p.waitURL()
	p.createComment("on-y", `{"target":{"target":"line","file":0,"side":"new","line":3},"body":"Why 2?"}`)
	p.createComment("on-z", `{"target":{"target":"line","file":0,"side":"new","line":5},"body":"And Z?"}`)
	pg := openPage(t, p)
	pg.ready()
	other := openTab(pg)
	other.ready()
	if pg.bool(changedBanner + ` !== null`) {
		pg.fatalf("the change notice shows before anything changed")
	}

	// Line 3 changes again; a new line 2 shifts line 5 to 6.
	w.write("b/x.go", "package b\n// Package b.\n\nvar Y = 3\n\nvar Z = 2\n")
	changed := time.Now()
	for !pg.bool(changedBanner + ` !== null`) {
		if time.Since(changed) > 20*time.Second {
			pg.fatalf("no change notice 20 s after the worktree changed")
		}
		time.Sleep(100 * time.Millisecond)
	}

	pg.eval(`window.rvwBeforeReload = true`, nil)
	other.eval(`window.rvwBeforeReload = true`, nil)
	pg.click("Refresh in the notice", button(".changed-banner", "Refresh"), 0)
	pg.waitTrue("the reloaded page", `window.rvwBeforeReload === undefined && document.readyState === "complete" && document.querySelector(".tree-row") !== null`)
	pg.ready()
	pg.waitTrue("the moved-comments notice", movedBanner+`?.textContent.includes("b/x.go · line 3 (new) → b/x.go") === true`)
	if pg.bool(changedBanner + ` !== null`) {
		pg.fatalf("the change notice still shows after the refresh")
	}
	other.waitTrue("the other tab reloaded", `window.rvwBeforeReload === undefined && document.readyState === "complete" && document.querySelector(".tree-row") !== null`)

	cs := p.sessionComments()
	got := map[string]sessionComment{}
	for _, c := range cs {
		got[c.ID] = c
	}
	if y := got["on-y"]; len(cs) != 2 || y.Body != "Why 2?" || y.Target.Target != "file" || y.Target.Path != "b/x.go" {
		pg.fatalf("after the refresh, on-y is %+v; want a file comment on b/x.go (all: %+v)", y, cs)
	}
	if z := got["on-z"]; z.Target.Target != "line" || z.Target.Line != 6 {
		pg.fatalf("after the refresh, on-z is %+v; want line 6", z)
	}

	pg.click("Dismiss", button(".moved-banner", "Dismiss"), 0)
	pg.reload()
	if pg.bool(movedBanner + ` !== null`) {
		pg.fatalf("the dismissed moved-comments notice came back on reload")
	}
	pg.click("Finish…", button("header", "Finish…"), 0)
	out := pg.sendAndCheck()
	for _, want := range []string{"Why 2?", "And Z?", "var Z = 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result lacks %q:\n%s", want, out)
		}
	}
	pg.noPageErrors()
}

// TestBrowserRefreshKeepsTextTypedWhileInFlight covers REQ-SES-015 for the page's own refresh: the
// human opens Finish and types a summary while the refresh is in flight, so when it moves the
// review to a new generation the page offers a reload instead of reloading, and the text stays.
func TestBrowserRefreshKeepsTextTypedWhileInFlight(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write("b/x.go", "package b\n\nvar Y = 1\n")
	w.commit("base")
	w.write("b/x.go", "package b\n\nvar Y = 2\n")
	p := w.start(procOptions{}, "uncommitted")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	w.write("b/x.go", "package b\n\nvar Y = 3\n")

	// The server moves the review only once Finish is open, whichever reply, the refresh's or a
	// status poll's, then reaches the page first.
	pg.eval(`window.rvwBeforeReload = true`, nil)
	pg.holdRefresh()
	pg.click("Refresh", button("header", "Refresh"), 0)
	pg.waitTrue("the refresh request", `window.rvwRefreshHeld === true`)
	pg.click("Finish…", button("header", "Finish…"), 0)
	pg.waitFocus("the summary", summaryField)
	pg.insertText("Typed during the refresh")
	pg.eval(`window.rvwReleaseRefresh()`, nil)

	pg.waitTrue("the reload prompt", supersededBanner+` !== null`)
	if !pg.bool(`window.rvwBeforeReload === true`) {
		pg.fatalf("the refresh reloaded the page while the Finish dialog was open")
	}
	if got := pg.text(summaryField + `.value`); got != "Typed during the refresh" {
		pg.fatalf("the summary is %q after the refresh; want the typed text", got)
	}
	if !pg.bool(`(` + button("header", "Refresh") + `)?.disabled === true`) {
		pg.fatalf("Refresh is enabled on a page whose review moved to a new generation")
	}
	pg.noPageErrors()
}

// holdRefresh holds the page's refresh requests before they are sent: window.rvwRefreshHeld is
// set once one is held, and window.rvwReleaseRefresh() sends it.
func (pg *page) holdRefresh() {
	pg.t.Helper()
	pg.eval(`(() => {
		const real = window.fetch;
		window.fetch = async (input, init) => {
			if (String(input).startsWith("/api/refresh")) {
				window.rvwRefreshHeld = true;
				await new Promise((release) => (window.rvwReleaseRefresh = release));
			}
			return real(input, init);
		};
	})()`, nil)
}

// TestBrowserRefreshNothingChanged: Refresh with nothing changed says so and keeps the page.
func TestBrowserRefreshNothingChanged(t *testing.T) {
	t.Parallel()
	w := oneFileWorld(t)
	p := w.start(procOptions{}, "last")
	p.waitURL()
	pg := openPage(t, p)
	pg.ready()
	pg.eval(`window.rvwBeforeReload = true`, nil)
	pg.click("Refresh", button("header", "Refresh"), 0)
	pg.waitTrue("the nothing-changed toast", `document.querySelector(".toast")?.textContent.startsWith("Nothing changed") === true`)
	if !pg.bool(`window.rvwBeforeReload === true`) {
		pg.fatalf("an unchanged refresh reloaded the page")
	}
	pg.noPageErrors()
}
