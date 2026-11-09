//go:build darwin

package main

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// A minimal Chrome DevTools Protocol client over --remote-debugging-pipe: Chrome reads commands
// on fd 3 and writes replies and events on fd 4, each a NUL-terminated JSON message. The pipe
// opens no port, and Chrome exits when it closes.

// defaultChrome is where macOS installs Google Chrome; RVW_TEST_CHROME overrides it.
const defaultChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// The viewport every page gets, so the virtual lists lay out the same everywhere.
const viewWidth, viewHeight = 1280, 900

// pageWait bounds every wait on the page; it is shorter than waitLimit so a stuck page fails
// with the page's own state rather than a process timeout.
const pageWait = 10 * time.Second

// One Chrome serves every browser test in the binary; tests that need none never start it.
var (
	chromeOnce sync.Once
	chrome     *browser
	errChrome  error
)

// sharedBrowser returns the test binary's Chrome, starting it on first use. Without Chrome the
// test is skipped locally and fails in CI, where the browser checks must run.
func sharedBrowser(t *testing.T) *browser {
	t.Helper()
	path := os.Getenv("RVW_TEST_CHROME")
	if path == "" {
		path = defaultChrome
	}
	if _, err := os.Stat(path); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("Chrome is required in CI for the browser tests: %v", err)
		}
		t.Skipf("the browser checks did not run: Chrome not found (%v); set RVW_TEST_CHROME", err)
	}
	chromeOnce.Do(func() { chrome, errChrome = startBrowser(path) })
	if errChrome != nil {
		t.Fatalf("start Chrome: %v", errChrome)
	}
	return chrome
}

// stopSharedBrowser ends the shared Chrome, if one started. runTests calls it after every test.
func stopSharedBrowser() {
	if chrome != nil {
		chrome.close()
	}
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cdpMessage struct {
	ID        int64          `json:"id,omitzero"`
	Method    string         `json:"method,omitzero"`
	SessionID string         `json:"sessionId,omitzero"`
	Params    jsontext.Value `json:"params,omitzero"`
	Result    jsontext.Value `json:"result,omitzero"`
	Error     *cdpError      `json:"error,omitzero"`
}

type browser struct {
	cmd     *exec.Cmd
	dir     string
	version string
	stderr  *lockedBuffer

	writeMu sync.Mutex
	in      *os.File

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan cdpMessage
	sessions map[string]func(cdpMessage)
	dead     chan struct{} // closed when the reader stops
	deadErr  error
}

func startBrowser(path string) (*browser, error) {
	dir, err := os.MkdirTemp("", "rvw-chrome-")
	if err != nil {
		return nil, err
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(context.Background(), path,
		"--headless",
		"--remote-debugging-pipe",
		"--user-data-dir="+dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		// Parallel tests share this browser, one tab each, and only one tab is in front. Chrome
		// may stop drawing frames in background tabs and throttle their timers, which would time
		// out a test waiting on requestAnimationFrame or a status poll.
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-component-update",
		"--disable-sync",
		"--password-store=basic",
		"--use-mock-keychain",
		fmt.Sprintf("--window-size=%d,%d", viewWidth, viewHeight),
		"about:blank")
	// Chrome's own state stays in the temp directory, never in the user's Library.
	cmd.Env = append(os.Environ(), "HOME="+dir)
	cmd.ExtraFiles = []*os.File{inR, outW}
	b := &browser{cmd: cmd, dir: dir, stderr: &lockedBuffer{}, in: inW,
		pending: map[int64]chan cdpMessage{}, sessions: map[string]func(cdpMessage){}, dead: make(chan struct{})}
	cmd.Stderr = b.stderr
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	_ = inR.Close()
	_ = outW.Close()
	go b.read(outR)
	var v struct {
		Product string `json:"product"`
	}
	if err := b.call("", "Browser.getVersion", nil, &v); err != nil {
		b.close()
		return nil, fmt.Errorf("%w; Chrome stderr:\n%s", err, b.stderr)
	}
	b.version = v.Product
	return b, nil
}

func (b *browser) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	var err error
	for {
		var raw []byte
		raw, err = br.ReadBytes(0)
		if err != nil {
			break
		}
		var m cdpMessage
		if err = json.Unmarshal(raw[:len(raw)-1], &m); err != nil {
			break
		}
		b.mu.Lock()
		if m.ID != 0 {
			if ch, ok := b.pending[m.ID]; ok {
				delete(b.pending, m.ID)
				ch <- m
			}
		} else if fn := b.sessions[m.SessionID]; fn != nil {
			fn(m)
		}
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.deadErr = fmt.Errorf("Chrome closed the pipe: %w", err)
	b.mu.Unlock()
	close(b.dead)
}

// call sends one command and decodes its result into out, when out is not nil.
func (b *browser) call(session, method string, params, out any) error {
	ch := make(chan cdpMessage, 1)
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.pending[id] = ch
	b.mu.Unlock()
	data, err := json.Marshal(struct {
		ID        int64  `json:"id"`
		Method    string `json:"method"`
		SessionID string `json:"sessionId,omitzero"`
		Params    any    `json:"params,omitzero"`
	}{id, method, session, params})
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	_, err = b.in.Write(append(data, 0))
	b.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	timer := time.NewTimer(pageWait)
	defer timer.Stop()
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: %s (%d)", method, m.Error.Message, m.Error.Code)
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-b.dead:
		b.mu.Lock()
		defer b.mu.Unlock()
		return fmt.Errorf("%s: %w", method, b.deadErr)
	case <-timer.C:
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return fmt.Errorf("%s: no reply within %v", method, pageWait)
	}
}

// close asks Chrome to exit by closing its command pipe, then kills it if it lingers.
func (b *browser) close() {
	_ = b.in.Close()
	exited := make(chan struct{})
	go func() {
		_ = b.cmd.Wait() // Chrome's exit status says nothing about the tests
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = b.cmd.Process.Kill()
		<-exited
	}
	_ = os.RemoveAll(b.dir)
}

// page is one tab. openPage gives each its own browser context, so tests share no storage; openTab
// adds a tab to an existing page's context.
type page struct {
	t       *testing.T
	b       *browser
	session string
	target  string
	context string
	proc    *proc // the rvw the page reviews, for failure reports

	mu       sync.Mutex
	errors   []string
	inflight map[string]bool // request IDs the page has sent and not yet seen end
}

// openPage opens p's review URL in a fresh context and waits for the file tree.
func openPage(t *testing.T, p *proc) *page {
	t.Helper()
	b := sharedBrowser(t)
	var bc struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := b.call("", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &bc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.call("", "Target.disposeBrowserContext", map[string]any{"browserContextId": bc.BrowserContextID}, nil)
	})
	return newTab(t, b, p, bc.BrowserContextID)
}

// openTab opens pg's review URL in a second tab of pg's context, as a human opening the URL again
// would: the tabs share the origin, so a BroadcastChannel reaches both.
func openTab(pg *page) *page {
	pg.t.Helper()
	return newTab(pg.t, pg.b, pg.proc, pg.context)
}

func newTab(t *testing.T, b *browser, p *proc, context string) *page {
	t.Helper()
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := b.call("", "Target.createTarget", map[string]any{"url": "about:blank", "browserContextId": context}, &target); err != nil {
		t.Fatal(err)
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call("", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &att); err != nil {
		t.Fatal(err)
	}
	pg := &page{t: t, b: b, session: att.SessionID, target: target.TargetID, context: context, proc: p, inflight: map[string]bool{}}
	b.mu.Lock()
	b.sessions[att.SessionID] = pg.event
	b.mu.Unlock()
	// Registered after the context's disposal, so it runs first.
	t.Cleanup(func() {
		_ = b.call("", "Target.closeTarget", map[string]any{"targetId": target.TargetID}, nil)
		b.mu.Lock()
		delete(b.sessions, att.SessionID)
		b.mu.Unlock()
	})
	for _, m := range []string{"Runtime.enable", "Log.enable", "Page.enable", "Network.enable"} {
		pg.send(m, nil)
	}
	pg.send("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": viewWidth, "height": viewHeight, "deviceScaleFactor": 1, "mobile": false})
	pg.navigate(p.url)
	return pg
}

// closeTab closes the tab as a human closing it would: the page runs its pagehide handlers.
// Target.closeTarget and Page.close both did so in an experiment; Target.closeTarget is the one
// the cleanup already uses.
func (pg *page) closeTab() {
	pg.t.Helper()
	if err := pg.b.call("", "Target.closeTarget", map[string]any{"targetId": pg.target}, nil); err != nil {
		pg.fatalf("close the tab: %v", err)
	}
}

func (pg *page) navigate(url string) {
	pg.t.Helper()
	pg.send("Page.navigate", map[string]any{"url": url})
	pg.waitTrue("the file tree", `document.querySelector(".tree-row") !== null`)
}

// reload loads the page again, as a human pressing reload would.
func (pg *page) reload() {
	pg.t.Helper()
	// The marker lives only in the old document, so the wait cannot pass before the new one loads.
	pg.eval(`window.rvwBeforeReload = true`, nil)
	pg.send("Page.reload", nil)
	pg.waitTrue("the reloaded file tree", `window.rvwBeforeReload === undefined && document.readyState === "complete" && document.querySelector(".tree-row") !== null`)
}

// event records what the page reports as an error. Failed requests are logged by the network
// layer and are expected where a test makes the server refuse or vanish; the UI reports those.
func (pg *page) event(m cdpMessage) {
	var msg string
	switch m.Method {
	case "Network.requestWillBeSent", "Network.loadingFinished", "Network.loadingFailed":
		var e struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal(m.Params, &e) // an undecodable event names no request to track
		pg.mu.Lock()
		if m.Method == "Network.requestWillBeSent" {
			pg.inflight[e.RequestID] = true
		} else {
			delete(pg.inflight, e.RequestID)
		}
		pg.mu.Unlock()
		return
	case "Runtime.exceptionThrown":
		var e struct {
			ExceptionDetails struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		_ = json.Unmarshal(m.Params, &e) // an undecodable event is still recorded, without detail
		msg = "exception: " + e.ExceptionDetails.Text + " " + e.ExceptionDetails.Exception.Description
	case "Runtime.consoleAPICalled":
		var e struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
		}
		_ = json.Unmarshal(m.Params, &e)
		if e.Type != "error" && e.Type != "assert" {
			return
		}
		msg = fmt.Sprintf("console.%s: %v", e.Type, e.Args)
	case "Log.entryAdded":
		var e struct {
			Entry struct {
				Level  string `json:"level"`
				Source string `json:"source"`
				Text   string `json:"text"`
			} `json:"entry"`
		}
		_ = json.Unmarshal(m.Params, &e)
		if e.Entry.Level != "error" || e.Entry.Source == "network" {
			return
		}
		msg = "log: " + e.Entry.Text
	default:
		return
	}
	pg.mu.Lock()
	pg.errors = append(pg.errors, msg)
	pg.mu.Unlock()
}

// noPageErrors fails the test if the page threw or logged an error.
func (pg *page) noPageErrors() {
	pg.t.Helper()
	pg.mu.Lock()
	defer pg.mu.Unlock()
	if len(pg.errors) > 0 {
		pg.t.Errorf("page errors:\n%s", strings.Join(pg.errors, "\n"))
	}
}

func (pg *page) send(method string, params any) {
	pg.t.Helper()
	if err := pg.b.call(pg.session, method, params, nil); err != nil {
		pg.fatalf("%v", err)
	}
}

// eval evaluates a JavaScript expression in the page and decodes its value into out.
func (pg *page) eval(expr string, out any) {
	pg.t.Helper()
	if err := pg.tryEval(expr, out); err != nil {
		pg.fatalf("%v", err)
	}
}

func (pg *page) tryEval(expr string, out any) error {
	var r struct {
		Result struct {
			Value jsontext.Value `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := pg.b.call(pg.session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &r); err != nil {
		return err
	}
	if d := r.ExceptionDetails; d != nil {
		return fmt.Errorf("evaluate %s: %s %s", expr, d.Text, d.Exception.Description)
	}
	if out == nil || len(r.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result.Value, out)
}

func (pg *page) text(expr string) string {
	pg.t.Helper()
	var s string
	pg.eval(expr, &s)
	return s
}

// waitTrue polls a boolean expression until it holds.
func (pg *page) waitTrue(what, expr string) {
	pg.t.Helper()
	deadline := time.Now().Add(pageWait)
	for {
		var ok bool
		err := pg.tryEval(expr, &ok)
		if err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				pg.fatalf("waiting for %s: %v", what, err)
			}
			pg.fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitIdle waits until the page has no request in flight.
func (pg *page) waitIdle() {
	pg.t.Helper()
	deadline := time.Now().Add(pageWait)
	for {
		pg.mu.Lock()
		n := len(pg.inflight)
		pg.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			pg.fatalf("%d requests still in flight", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fatalf ends the test with what the page shows, the server's session, and rvw's stderr.
func (pg *page) fatalf(format string, args ...any) {
	pg.t.Helper()
	var body string
	_ = pg.tryEval(`document.body ? document.body.innerText.slice(0, 2000) : ""`, &body) // best effort: the report is already a failure
	var session string
	if pg.proc != nil {
		select {
		case <-pg.proc.done:
			session = "(rvw exited)"
		default:
			if status, data := pg.proc.do("GET", "/api/session", ""); status == 200 {
				v := jsontext.Value(data)
				_ = v.Indent() // best effort: unindented JSON is still readable
				session = string(v)
			}
		}
	}
	stderr := ""
	if pg.proc != nil {
		stderr = pg.proc.stderr.String()
	}
	pg.mu.Lock()
	errs := strings.Join(pg.errors, "\n")
	pg.mu.Unlock()
	pg.t.Fatalf("%s\n--- %s\n--- page text:\n%s\n--- page errors:\n%s\n--- session:\n%s\n--- rvw stderr:\n%s",
		fmt.Sprintf(format, args...), pg.b.version, body, errs, session, stderr)
}

// Modifier bits for Input.dispatch*Event.
const (
	modAlt   = 1
	modCtrl  = 2
	modMeta  = 4
	modShift = 8
)

// click finds the element a JavaScript expression yields, scrolls it into view, and presses the
// mouse at its centre. The element is found again on every call, since rows re-render.
func (pg *page) click(what, elementExpr string, modifiers int) {
	pg.t.Helper()
	x, y := pg.locate(what, elementExpr)
	pg.send("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "modifiers": modifiers})
	pg.press1(x, y, modifiers, 1)
}

// doubleClick double-clicks the element as click finds it: two presses with no wait between them.
func (pg *page) doubleClick(what, elementExpr string) {
	pg.t.Helper()
	x, y := pg.locate(what, elementExpr)
	pg.send("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y})
	pg.press1(x, y, 0, 1)
	pg.press1(x, y, 0, 2)
}

func (pg *page) press1(x, y float64, modifiers, count int) {
	pg.t.Helper()
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		pg.send("Input.dispatchMouseEvent", map[string]any{"type": typ, "x": x, "y": y, "modifiers": modifiers,
			"button": "left", "clickCount": count})
	}
}

// locate finds the element a JavaScript expression yields, scrolls it into view and returns its
// centre.
func (pg *page) locate(what, elementExpr string) (x, y float64) {
	pg.t.Helper()
	var at *struct{ X, Y float64 }
	expr := `(() => { const e = (` + elementExpr + `);
		if (!(e instanceof Element)) return null;
		e.scrollIntoView({ block: "nearest" });
		const r = e.getBoundingClientRect();
		if (r.width === 0 || r.height === 0) return null;
		return { X: r.left + r.width / 2, Y: r.top + r.height / 2 }; })()`
	deadline := time.Now().Add(pageWait)
	for {
		at = nil
		err := pg.tryEval(expr, &at)
		if err == nil && at != nil {
			return at.X, at.Y
		}
		if time.Now().After(deadline) {
			pg.fatalf("could not find %s to click (%v)", what, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// allowClipboard lets the page write and read the clipboard as a focused page with the
// permission granted would; headless Chrome otherwise refuses both.
func (pg *page) allowClipboard() {
	pg.t.Helper()
	if err := pg.b.call("", "Browser.grantPermissions", map[string]any{
		"permissions":      []string{"clipboardReadWrite", "clipboardSanitizedWrite"},
		"origin":           "http://" + pg.proc.addr,
		"browserContextId": pg.context,
	}, nil); err != nil {
		pg.fatalf("grant clipboard permissions: %v", err)
	}
	pg.send("Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true})
}

// clipboardText reads the clipboard's text through the page.
func (pg *page) clipboardText() string {
	pg.t.Helper()
	var r struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := pg.b.call(pg.session, "Runtime.evaluate", map[string]any{
		"expression": "navigator.clipboard.readText()", "awaitPromise": true, "returnByValue": true}, &r); err != nil {
		pg.fatalf("read the clipboard: %v", err)
	}
	if r.ExceptionDetails != nil {
		pg.fatalf("read the clipboard: %s", r.ExceptionDetails.Text)
	}
	return r.Result.Value
}

// press sends a key without text, such as Enter with ⌘ or Escape.
func (pg *page) press(key, code string, keyCode, modifiers int) {
	pg.t.Helper()
	base := map[string]any{"key": key, "code": code, "windowsVirtualKeyCode": keyCode, "modifiers": modifiers}
	down := map[string]any{"type": "rawKeyDown"}
	up := map[string]any{"type": "keyUp"}
	for k, v := range base {
		down[k], up[k] = v, v
	}
	pg.send("Input.dispatchKeyEvent", down)
	pg.send("Input.dispatchKeyEvent", up)
}

func (pg *page) cmdEnter() { pg.press("Enter", "Enter", 13, modMeta) }

// typeKey sends a printable key, as the human typing one character would.
func (pg *page) typeKey(ch string) {
	pg.t.Helper()
	code := "Key" + strings.ToUpper(ch)
	pg.send("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": ch, "code": code, "text": ch})
	pg.send("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": ch, "code": code})
}

// insertText types text into the focused element.
func (pg *page) insertText(s string) {
	pg.t.Helper()
	pg.send("Input.insertText", map[string]any{"text": s})
}

// selectAll selects the focused text field's content, so the next insertText replaces it.
func (pg *page) selectAll() {
	pg.t.Helper()
	pg.eval(`document.activeElement.select()`, nil)
}

// waitFocus waits until the element the expression yields has the focus.
func (pg *page) waitFocus(what, elementExpr string) {
	pg.t.Helper()
	pg.waitTrue(what+" focused", `(`+elementExpr+`) !== null && document.activeElement === (`+elementExpr+`)`)
}

// jsString quotes s as a JavaScript string literal.
func jsString(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err) // a Go string always marshals
	}
	return string(data)
}
