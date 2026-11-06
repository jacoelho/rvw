package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func smallRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.write("a.txt", numberedLines("line", 5))
	r.commit("base")
	r.write("a.txt", strings.Replace(numberedLines("line", 5), "line 3\n", "line three\n", 1))
	r.commit("head")
	return r
}

// cspDirectives parses a Content-Security-Policy header into directive name -> source list.
func cspDirectives(h string) map[string]string {
	out := map[string]string{}
	for d := range strings.SplitSeq(h, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		if name != "" {
			out[name] = strings.TrimSpace(value)
		}
	}
	return out
}

// assertCSP checks the CSP: scripts and connections only from the UI's own origin.
func assertCSP(t *testing.T, what string, h http.Header) {
	t.Helper()
	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		t.Errorf("%s: no Content-Security-Policy header", what)
		return
	}
	d := cspDirectives(csp)
	for _, name := range []string{"script-src", "connect-src"} {
		v, ok := d[name]
		if !ok {
			v, ok = d["default-src"]
		}
		if !ok || v != "'self'" && v != "'none'" {
			t.Errorf("%s: CSP %q allows %s from %q, want only 'self'", what, csp, name, v)
		}
	}
}

// TestSecurityMatrix covers local request protection (token, Host, cross-origin checks and CSP):
// each request breaks exactly one rule, is rejected, and has no effect on the draft. The finish
// cases make the delivery count meaningful, the cancel and page-closed cases the open session.
func TestSecurityMatrix(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	summary := `{"summary":"changed"}`

	type matrixCase struct {
		name   string
		method string
		path   string
		body   string
		opts   []reqOption
		want   int
	}
	cases := []matrixCase{
		{"API without token", http.MethodGet, "/api/session", "", []reqOption{withoutToken()}, http.StatusUnauthorized},
		{"API with wrong token", http.MethodGet, "/api/session", "", []reqOption{withToken("wrong")}, http.StatusUnauthorized},
		{"API with token as query", http.MethodGet, "/api/session?token=" + f.token, "", []reqOption{withoutToken()}, http.StatusUnauthorized},
		{"write without token", http.MethodPut, "/api/summary", summary, []reqOption{withoutToken()}, http.StatusUnauthorized},
		{"quotes without token", http.MethodGet, "/api/quotes?id=x", "", []reqOption{withoutToken()}, http.StatusUnauthorized},
		{"search without token", http.MethodGet, "/api/search?q=line", "", []reqOption{withoutToken()}, http.StatusUnauthorized},
		{"search with token as query", http.MethodGet, "/api/search?q=line&token=" + f.token, "", []reqOption{withoutToken()}, http.StatusUnauthorized},
		{"API with foreign Host", http.MethodGet, "/api/session", "", []reqOption{withForeignHost()}, http.StatusMisdirectedRequest},
		{"asset with foreign Host", http.MethodGet, "/", "", []reqOption{withForeignHost()}, http.StatusMisdirectedRequest},
		{"write with foreign Host", http.MethodPut, "/api/summary", summary, []reqOption{withForeignHost()}, http.StatusMisdirectedRequest},
		{"unknown member", http.MethodPut, "/api/summary", `{"summary":"changed","extra":1}`, nil, http.StatusBadRequest},
		{"duplicate member", http.MethodPut, "/api/summary", `{"summary":"a","summary":"changed"}`, nil, http.StatusBadRequest},
		{"trailing data", http.MethodPut, "/api/summary", `{"summary":"changed"} {}`, nil, http.StatusBadRequest},
		{"not JSON content type", http.MethodPut, "/api/summary", summary, []reqOption{withContentType("text/plain")}, http.StatusUnsupportedMediaType},
		// The body is valid JSON cut at the limit inside a string, so only its size is wrong.
		{"body over 1 MiB", http.MethodPut, "/api/summary", `{"summary":"` + strings.Repeat("x", 1<<20) + `"}`, nil, http.StatusRequestEntityTooLarge},
	}
	writes := []struct{ name, method, path, body string }{
		{"summary", http.MethodPut, "/api/summary", summary},
		{"cancel", http.MethodPost, "/api/cancel", ""},
		{"finish", http.MethodPost, "/api/finish", ""},
		{"page-closed", http.MethodPost, "/api/page-closed", ""},
	}
	for _, w := range writes {
		for _, c := range []struct {
			name string
			opts []reqOption
		}{
			// The default same-origin Origin stays, so Sec-Fetch-Site alone refuses these.
			{"cross-site Sec-Fetch-Site", []reqOption{withSecFetchSite("cross-site")}},
			{"same-site Sec-Fetch-Site", []reqOption{withSecFetchSite("same-site")}},
			{"foreign Origin", []reqOption{withOrigin("http://evil.example")}},
			{"foreign Origin on another loopback port", []reqOption{withOrigin("http://localhost:1")}},
			{"null Origin", []reqOption{withOrigin("null")}},
		} {
			cases = append(cases, matrixCase{w.name + " with " + c.name, w.method, w.path, w.body, c.opts, http.StatusForbidden})
		}
	}
	for _, c := range cases {
		r := f.do(c.method, c.path, c.body, c.opts...)
		if r.status != c.want {
			t.Errorf("%s: status %d, want %d; body %q", c.name, r.status, c.want, r.body)
		}
		assertCSP(t, c.name, r.header)
	}

	s := f.session()
	if s.Summary != "" {
		t.Errorf("rejected requests changed the draft: summary %q", s.Summary)
	}
	if r := f.do(http.MethodGet, "/api/session", ""); r.status != http.StatusOK {
		t.Errorf("session still open after rejected cancel: status %d", r.status)
	}
	if n := f.delivered.count(); n != 0 {
		t.Errorf("rejected requests delivered %d results", n)
	}
}

// TestCrossOriginAccepted: a state-changing request with the token is accepted unless its
// Sec-Fetch-Site marks it cross-site or, without Sec-Fetch-Site, its Origin's host differs from
// the listener's. Sec-Fetch-Site takes precedence over Origin, and Origin's scheme is not compared.
func TestCrossOriginAccepted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		opts func(addr string) []reqOption
	}{
		{"neither header", func(string) []reqOption { return []reqOption{withOrigin("")} }},
		{"Sec-Fetch-Site none without Origin", func(string) []reqOption {
			return []reqOption{withOrigin(""), withSecFetchSite("none")}
		}},
		{"Sec-Fetch-Site none with a foreign Origin", func(string) []reqOption {
			return []reqOption{withOrigin("http://evil.example"), withSecFetchSite("none")}
		}},
		{"Sec-Fetch-Site same-origin with a foreign Origin", func(string) []reqOption {
			return []reqOption{withOrigin("http://evil.example"), withSecFetchSite("same-origin")}
		}},
		{"Origin of another scheme on the listener's host", func(addr string) []reqOption {
			return []reqOption{withOrigin("https://" + addr)}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, smallRepo(t), fixtureOptions{})
			f.serveHandler()
			r := f.do(http.MethodPut, "/api/summary", `{"summary":"accepted"}`, c.opts(f.addr)...)
			if r.status < 200 || r.status > 299 {
				t.Errorf("status %d, want 2xx", r.status)
			}
			assertCSP(t, c.name, r.header)
			if s := f.session(); s.Summary != "accepted" {
				t.Errorf("summary %q, want the accepted write", s.Summary)
			}
		})
	}
}

// TestAssetsServedWithoutToken: the UI loads before it can read the token from the fragment, so
// assets need none, while the API does.
func TestAssetsServedWithoutToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()

	r := f.do(http.MethodGet, "/", "", withoutToken())
	f.mustStatus(r, http.StatusOK, "GET / without token")
	if string(r.body) != indexHTML {
		t.Errorf("GET / body %q, want the embedded index.html", r.body)
	}
	assertCSP(t, "GET /", r.header)

	r = f.do(http.MethodGet, "/missing.js", "", withoutToken())
	f.mustStatus(r, http.StatusNotFound, "GET /missing.js")
	assertCSP(t, "GET /missing.js", r.header)

	r = f.do(http.MethodGet, "/api/session", "", withoutToken())
	f.mustStatus(r, http.StatusUnauthorized, "GET /api/session without token")
	if strings.Contains(string(r.body), "a.txt") {
		t.Errorf("unauthorised API response leaks snapshot data: %q", r.body)
	}

	r = f.do(http.MethodGet, "/api/session", "")
	f.mustStatus(r, http.StatusOK, "GET /api/session with token")
	assertCSP(t, "GET /api/session", r.header)
}

// TestAssetWritesRefused: the asset tree is read-only.
func TestAssetWritesRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, smallRepo(t), fixtureOptions{})
	f.serveHandler()
	r := f.do(http.MethodPost, "/", `{}`)
	if r.status < 400 || r.status >= 500 {
		t.Errorf("POST / status %d, want a 4xx rejection", r.status)
	}
	assertCSP(t, "POST /", r.header)
}
