package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWrapParseOAuthState(t *testing.T) {
	wrapped := wrapOAuthState("feat.app", "abc!def")
	name, orig, ok := parseWrappedOAuthState(wrapped)
	if !ok || name != "feat.app" || orig != "abc!def" {
		t.Errorf("round trip = (%q,%q,%v); want (feat.app, abc!def, true)", name, orig, ok)
	}
	if _, _, ok := parseWrappedOAuthState("plain-state"); ok {
		t.Errorf("unwrapped state parsed as wrapped")
	}
	if _, _, ok := parseWrappedOAuthState("vibewt1!noseparator"); ok {
		t.Errorf("malformed wrapper parsed as wrapped")
	}
}

func fakeRedirect(location string) *http.Response {
	h := http.Header{}
	h.Set("Location", location)
	return &http.Response{StatusCode: http.StatusFound, Header: h}
}

func TestTagOAuthAuthorizeRedirect(t *testing.T) {
	auth := "https://accounts.google.com/o/oauth2/v2/auth?client_id=x&redirect_uri=" +
		url.QueryEscape("http://localhost:3000/api/auth/callback/google") + "&state=orig123"

	// Matching bridge port → state is wrapped, redirect_uri untouched.
	resp := fakeRedirect(auth)
	tagOAuthAuthorizeRedirect(resp, "feat.app", 3000)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if got := loc.Query().Get("state"); got != "vibewt1!feat.app!orig123" {
		t.Errorf("state = %q; want wrapped", got)
	}
	if got := loc.Query().Get("redirect_uri"); got != "http://localhost:3000/api/auth/callback/google" {
		t.Errorf("redirect_uri changed: %q", got)
	}

	// redirect_uri pointing at some other port → untouched.
	other := fakeRedirect(strings.Replace(auth, "3000", "5555", 1))
	before := other.Header.Get("Location")
	tagOAuthAuthorizeRedirect(other, "feat.app", 3000)
	if other.Header.Get("Location") != before {
		t.Errorf("non-bridge redirect_uri was rewritten")
	}

	// No state param (PKCE-only flow, e.g. Auth.js v5 Google) → a synthetic
	// state is injected so the bridge can still route the callback.
	nostate := fakeRedirect("https://accounts.google.com/auth?redirect_uri=" + url.QueryEscape("http://localhost:3000/cb"))
	tagOAuthAuthorizeRedirect(nostate, "feat.app", 3000)
	nsLoc, _ := url.Parse(nostate.Header.Get("Location"))
	if got := nsLoc.Query().Get("state"); got != "vibewt1!feat.app!" {
		t.Errorf("stateless redirect state = %q; want synthetic vibewt1!feat.app!", got)
	}

	// Relative Location (in-app redirect) → untouched.
	rel := fakeRedirect("/login?state=x&redirect_uri=" + url.QueryEscape("http://localhost:3000/cb"))
	before = rel.Header.Get("Location")
	tagOAuthAuthorizeRedirect(rel, "feat.app", 3000)
	if rel.Header.Get("Location") != before {
		t.Errorf("relative redirect was rewritten")
	}

	// Non-3xx → untouched.
	ok200 := fakeRedirect(auth)
	ok200.StatusCode = 200
	before = ok200.Header.Get("Location")
	tagOAuthAuthorizeRedirect(ok200, "feat.app", 3000)
	if ok200.Header.Get("Location") != before {
		t.Errorf("200 response was rewritten")
	}
}

func TestTagAuthorizeJSONBody(t *testing.T) {
	authURL := "https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=" +
		url.QueryEscape("http://localhost:3000/api/auth/callback/google") + "&response_type=code"

	// Auth.js client-side signin shape: {"url": "..."} — URL gets tagged.
	body := []byte(`{"url":"` + authURL + `"}`)
	out, changed := tagAuthorizeJSONBody(body, "feat.app", 3000)
	if !changed {
		t.Fatalf("signin JSON not tagged: %s", out)
	}
	var doc struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("tagged body is not valid JSON: %v\n%s", err, out)
	}
	u, _ := url.Parse(doc.URL)
	if got := u.Query().Get("state"); got != "vibewt1!feat.app!" {
		t.Errorf("state = %q; want synthetic tag", got)
	}
	if got := u.Query().Get("redirect_uri"); got != "http://localhost:3000/api/auth/callback/google" {
		t.Errorf("redirect_uri changed: %q", got)
	}

	// Unrelated JSON without a bridge-bound URL → untouched.
	plain := []byte(`{"url":"https://example.com/?redirect_uri=x","n":1}`)
	if _, changed := tagAuthorizeJSONBody(plain, "feat.app", 3000); changed {
		t.Errorf("unrelated JSON was rewritten")
	}
	// Non-JSON → untouched.
	if _, changed := tagAuthorizeJSONBody([]byte("<html>redirect_uri</html>"), "feat.app", 3000); changed {
		t.Errorf("non-JSON was rewritten")
	}

	// A body that JSON-escapes the URL (\/ and & are both legal) must
	// still be tagged — the old substring splice missed these entirely
	// because the decoded value never matched the raw bytes.
	escaped := []byte(`{"url":"https:\/\/accounts.google.com\/o\/oauth2\/v2\/auth?redirect_uri=http%3A%2F%2Flocalhost%3A3000%2Fcb&response_type=code"}`)
	out, changed = tagAuthorizeJSONBody(escaped, "feat.app", 3000)
	if !changed {
		t.Fatalf("escaped-JSON body not tagged: %s", out)
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("tagged escaped body invalid: %v\n%s", err, out)
	}
	eu, _ := url.Parse(doc.URL)
	if got := eu.Query().Get("state"); got != "vibewt1!feat.app!" {
		t.Errorf("escaped-body state = %q; want tag", got)
	}

	// The same URL appearing twice must be tagged once each, never
	// double-tagged (the old first-occurrence splice could re-match inside an
	// already-tagged value and emit a duplicate state param).
	dup := []byte(`{"a":"` + authURL + `","b":"` + authURL + `"}`)
	out, changed = tagAuthorizeJSONBody(dup, "feat.app", 3000)
	if !changed {
		t.Fatalf("duplicate-URL body not tagged")
	}
	var two map[string]string
	if err := json.Unmarshal(out, &two); err != nil {
		t.Fatalf("tagged duplicate body invalid: %v\n%s", err, out)
	}
	for k, v := range two {
		pu, _ := url.Parse(v)
		if got := pu.Query()["state"]; len(got) != 1 || got[0] != "vibewt1!feat.app!" {
			t.Errorf("key %q state = %v; want exactly one tag", k, got)
		}
	}
}

func TestOAuthEnvForRoute(t *testing.T) {
	s := testServer()

	owner := &Route{Name: "app", Type: RouteManaged, Port: 3990, Cmd: "x", OAuthCallbackPort: 3000, RegisteredAt: time.Now()}
	s.table.Add(owner)
	wt := &Route{Name: "feat.app", Parent: "app", Type: RouteManaged, Port: 3991, Cmd: "x", RegisteredAt: time.Now()}
	s.table.Add(wt)
	plain := &Route{Name: "solo", Type: RouteManaged, Port: 3992, Cmd: "x", RegisteredAt: time.Now()}
	s.table.Add(plain)

	want := []string{"AUTH_URL=http://localhost:3000", "NEXTAUTH_URL=http://localhost:3000"}
	for _, tc := range []struct {
		name  string
		route *Route
		want  []string
	}{
		{"bridge owner", owner, want},
		{"worktree inherits parent bridge", wt, want},
		{"no bridge", plain, nil},
	} {
		got := s.oauthEnvForRoute(tc.route)
		if len(got) != len(tc.want) {
			t.Errorf("%s: env = %v; want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: env[%d] = %q; want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestRewriteBridgeHostRedirect(t *testing.T) {
	// Post-login bounce to the bridge host → rewritten to the route origin.
	resp := fakeRedirect("http://localhost:3000/dashboard?tab=1")
	rewriteBridgeHostRedirect(resp, "http", "feat.app.test", 3000)
	if got := resp.Header.Get("Location"); got != "http://feat.app.test/dashboard?tab=1" {
		t.Errorf("Location = %q; want rewritten to route origin", got)
	}
	// Other hosts and relative Locations → untouched.
	for _, loc := range []string{"https://example.com/x", "/relative", "http://localhost:9999/x"} {
		r := fakeRedirect(loc)
		rewriteBridgeHostRedirect(r, "http", "feat.app.test", 3000)
		if r.Header.Get("Location") != loc {
			t.Errorf("Location %q was rewritten to %q", loc, r.Header.Get("Location"))
		}
	}
}

func TestBridgeRoutesWrappedStateToWorktree(t *testing.T) {
	s := testServer()

	owner := &Route{Name: "app", Type: RouteManaged, Port: 3970, Cmd: "x", OAuthCallbackPort: 53123, RegisteredAt: time.Now()}
	s.table.Add(owner)
	wt := &Route{Name: "feat.app", Parent: "app", Type: RouteManaged, Port: 3971, Cmd: "x", Dir: os.TempDir(), RegisteredAt: time.Now()}
	s.table.Add(wt)

	get := func(uri string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, uri, nil)
		w := httptest.NewRecorder()
		s.handleOAuthCallbackBridge(w, req, 53123)
		return w
	}

	// Wrapped state naming a worktree of the owner → forwarded to the
	// worktree's host with the original state restored.
	w := get("/api/auth/callback/google?code=c1&state=" + url.QueryEscape(wrapOAuthState("feat.app", "orig456")))
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("code = %d; want 307", w.Code)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Host != "feat.app.test" {
		t.Errorf("forwarded to %q; want feat.app.test", loc.Host)
	}
	if got := loc.Query().Get("state"); got != "orig456" {
		t.Errorf("state = %q; want original restored", got)
	}
	if got := loc.Query().Get("code"); got != "c1" {
		t.Errorf("code = %q; want c1", got)
	}

	// Synthetic state (empty original) → forwarded to the worktree with the
	// state param REMOVED — the app never sent one and must not receive one.
	wSyn := get("/cb?code=c9&state=" + url.QueryEscape(wrapOAuthState("feat.app", "")))
	locSyn, _ := url.Parse(wSyn.Header().Get("Location"))
	if locSyn.Host != "feat.app.test" {
		t.Errorf("synthetic state forwarded to %q; want feat.app.test", locSyn.Host)
	}
	if _, has := locSyn.Query()["state"]; has {
		t.Errorf("synthetic state not stripped: %q", locSyn.RawQuery)
	}
	if got := locSyn.Query().Get("code"); got != "c9" {
		t.Errorf("code = %q; want c9", got)
	}

	// Plain state → legacy behavior: forwarded to the owner.
	w2 := get("/cb?code=c2&state=plain")
	loc2, _ := url.Parse(w2.Header().Get("Location"))
	if loc2.Host != "app.test" {
		t.Errorf("plain state forwarded to %q; want app.test", loc2.Host)
	}

	// Wrapped state naming a route that is NOT a worktree of the owner →
	// owner (no cross-app callback hijack via crafted state).
	stranger := &Route{Name: "feat.zzz", Parent: "zzz", Type: RouteManaged, Port: 3972, Cmd: "x", RegisteredAt: time.Now()}
	s.table.Add(stranger)
	w3 := get("/cb?code=c3&state=" + url.QueryEscape(wrapOAuthState("feat.zzz", "x")))
	loc3, _ := url.Parse(w3.Header().Get("Location"))
	if loc3.Host != "app.test" {
		t.Errorf("foreign wrapped state forwarded to %q; want app.test (owner)", loc3.Host)
	}
}

// End-to-end through the reverse proxy: a worktree app's 302 toward the OAuth
// provider gets its state tagged in flight.
func TestProxyTagsWorktreeOAuthRedirect(t *testing.T) {
	s := testServer()

	authURL := "https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=" +
		url.QueryEscape("http://localhost:53124/cb") + "&state=orig789"
	var sawAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAcceptEncoding = r.Header.Get("Accept-Encoding")
		if r.URL.Path == "/signin-json" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"url":"` + authURL + `"}`))
			return
		}
		w.Header().Set("Location", authURL)
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	owner := &Route{Name: "app", Type: RouteManaged, Port: 3973, Cmd: "x", OAuthCallbackPort: 53124, RegisteredAt: time.Now()}
	s.table.Add(owner)
	wt := &Route{Name: "feat.app", Parent: "app", Type: RouteManaged, Port: port, Cmd: "x", Dir: mkWorktreeDir(t), RegisteredAt: time.Now()}
	wt.Running.Store(true)
	wt.Ready.Store(true)
	wt.SetPID(os.Getpid())
	s.table.Add(wt)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/signin/google", nil)
	req.Host = "feat.app.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("code = %d; want 302 proxied through", w.Code)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if got := loc.Query().Get("state"); got != "vibewt1!feat.app!orig789" {
		t.Errorf("proxied state = %q; want tagged", got)
	}

	// Client-side flow: the authorize URL travels in a JSON body, not a
	// Location header — it must come out tagged too. The client's own
	// Accept-Encoding is stripped so Go's transport owns the encoding (it
	// re-adds gzip but then transparently decompresses, so ModifyResponse
	// always sees plaintext); the upstream never sees the client's value.
	req2 := httptest.NewRequest(http.MethodPost, "/signin-json", nil)
	req2.Host = "feat.app.test"
	req2.Header.Set("Accept-Encoding", "br;q=1.0, zstd")
	w2 := httptest.NewRecorder()
	s.routeRequest(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("json signin = %d; want 200", w2.Code)
	}
	if strings.Contains(sawAcceptEncoding, "zstd") || strings.Contains(sawAcceptEncoding, "br") {
		t.Errorf("upstream saw client Accept-Encoding %q; want it replaced by the transport's own", sawAcceptEncoding)
	}
	var doc struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &doc); err != nil {
		t.Fatalf("proxied JSON invalid: %v\n%s", err, w2.Body.String())
	}
	ju, _ := url.Parse(doc.URL)
	if got := ju.Query().Get("state"); got != "vibewt1!feat.app!orig789" {
		t.Errorf("JSON body state = %q; want tagged", got)
	}
}

// prepareWorktreeEnv must refuse a Dir that isn't actually a linked worktree
// of the parent: Parent is derived syntactically from a dotted route name and
// Dir is caller-supplied, so without the git check a route named
// "<anything>.<app>" with an arbitrary dir would relocate the app's real
// credentials there.
func TestPrepareWorktreeEnvRefusesNonWorktreeDir(t *testing.T) {
	s := testServer()
	repo, _ := initGitRepoWithWorktree(t, "feature/real")

	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := &Route{Name: "app", Type: RouteManaged, Port: 3993, Cmd: "x", Dir: repo, RegisteredAt: time.Now()}
	s.table.Add(parent)

	// An arbitrary directory that is NOT a worktree of the parent repo.
	evil := t.TempDir()
	imposter := &Route{Name: "evil.app", Parent: "app", Type: RouteManaged, Port: 3994, Cmd: "x", Dir: evil, RegisteredAt: time.Now()}
	s.table.Add(imposter)

	s.prepareWorktreeEnv(imposter)

	if _, err := os.Stat(filepath.Join(evil, ".env")); !os.IsNotExist(err) {
		t.Errorf("secrets copied into a non-worktree directory (err=%v); want refusal", err)
	}
}

func TestPrepareWorktreeEnv(t *testing.T) {
	s := testServer()

	// A real repo + linked worktree: prepareWorktreeEnv only copies into a
	// directory git actually reports as a worktree of the parent.
	parentDir, wtDir := initGitRepoWithWorktree(t, "feature/env")
	if err := os.WriteFile(filepath.Join(parentDir, ".env"), []byte("A=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, ".env.local"), []byte("B=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := &Route{Name: "app", Type: RouteManaged, Port: 3980, Cmd: "x", Dir: parentDir, RegisteredAt: time.Now()}
	s.table.Add(parent)

	// One env file already exists in the worktree — must NOT be overwritten.
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"), []byte("LOCAL=yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wt := &Route{Name: "feat.app", Parent: "app", Type: RouteManaged, Port: 3981, Cmd: "x", Dir: wtDir, RegisteredAt: time.Now()}
	s.table.Add(wt)

	s.prepareWorktreeEnv(wt)

	got, err := os.ReadFile(filepath.Join(wtDir, ".env"))
	if err != nil || string(got) != "A=1\n" {
		t.Errorf(".env = %q, err %v; want copied A=1", got, err)
	}
	got, _ = os.ReadFile(filepath.Join(wtDir, ".env.local"))
	if string(got) != "LOCAL=yes\n" {
		t.Errorf(".env.local = %q; want existing file preserved", got)
	}
	// Non-worktree routes are a no-op.
	s.prepareWorktreeEnv(parent)
}
