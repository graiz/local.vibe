package daemon

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

func apiReq(s *Server, method, path, host, body string) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:50000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	return w
}

// PUT must run the same port collision checks as register: a route pointed at
// the daemon's own listener self-proxies, and via the peer relay would hand a
// paired machine a path to the loopback API.
func TestUpdateRejectsClaimedOrInvalidPort(t *testing.T) {
	s := testServer(t)
	s.cfg.Daemon.TLS.Enabled = true
	s.cfg.Daemon.TLS.Port = 7443
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{Name: "a", Type: RouteSticky, Port: 4100, RegisteredAt: time.Now()})
	s.table.Add(&Route{Name: "b", Type: RouteSticky, Port: 4200, RegisteredAt: time.Now()})

	cases := map[string]int{
		`{"port":4100}`:  http.StatusConflict,   // route a's port
		`{"port":7443}`:  http.StatusConflict,   // the daemon's TLS listener
		`{"port":70000}`: http.StatusBadRequest, // out of range
		`{"port":-1}`:    http.StatusBadRequest,
	}
	for body, want := range cases {
		w := apiReq(s, http.MethodPut, "/_api/routes/b", "local.test", body)
		if w.Code != want {
			t.Errorf("PUT %s = %d; want %d (%s)", body, w.Code, want, w.Body.String())
		}
	}
	if r, _ := s.table.Get("b"); r.Port != 4200 {
		t.Errorf("rejected update changed the port to %d", r.Port)
	}
	if w := apiReq(s, http.MethodPut, "/_api/routes/b", "local.test", `{"port":4300}`); w.Code != http.StatusOK {
		t.Errorf("valid port update = %d: %s", w.Code, w.Body.String())
	}
}

// Filesystem paths (/worktrees) and peer hosts/fingerprints (/peers) are
// machine-local detail: only the dashboard host, loopback, and the unix socket
// may read them, like dir/cmd on /routes.
func TestWorktreesAndPeersReadsAreLocalOnly(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	for _, path := range []string{"/_api/worktrees", "/_api/peers"} {
		if w := apiReq(s, http.MethodGet, path, "other.test", ""); w.Code != http.StatusForbidden {
			t.Errorf("GET %s from a route host = %d; want 403", path, w.Code)
		}
		if w := apiReq(s, http.MethodGet, path, "local.test", ""); w.Code != http.StatusOK {
			t.Errorf("GET %s from the dashboard = %d; want 200", path, w.Code)
		}
	}
}

// A bookmark URL may carry basic-auth credentials. They are the user's to see
// locally, not any .vibe page's.
func TestRouteListingRedactsBookmarkCredentials(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{Name: "nas", Type: RouteBookmark, ExternalURL: "https://admin:hunter2@nas.local:5001/", RegisteredAt: time.Now()})

	w := apiReq(s, http.MethodGet, "/_api/routes", "other.test", "")
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Errorf("credentials leaked to a route host: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nas.local:5001") {
		t.Errorf("redaction dropped the host too: %s", w.Body.String())
	}
	w = apiReq(s, http.MethodGet, "/_api/routes", "local.test", "")
	if !strings.Contains(w.Body.String(), "hunter2") {
		t.Errorf("dashboard listing lost the credentials: %s", w.Body.String())
	}
}

// JSON bodies are decoded with a size cap so a client can't make the daemon
// buffer an unbounded request.
func TestAPIRejectsOversizedBody(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	big := `{"name":"huge","port":4100,"icon":"` + strings.Repeat("x", 2<<20) + `"}`
	w := apiReq(s, http.MethodPost, "/_api/routes", "local.test", big)
	if w.Code == http.StatusOK {
		t.Fatal("2 MiB register body accepted")
	}
	if _, ok := s.table.Get("huge"); ok {
		t.Error("oversized request created a route")
	}
}

// Every http.Server the daemon runs gets header/idle timeouts: the peer
// listener is LAN-facing, and even the loopback ones shouldn't be stallable
// by a half-open connection forever.
func TestHTTPServersHaveTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout=%v IdleTimeout=%v; want both > 0", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
}

// Five wrong proofs close the invite: a 6-digit code is otherwise
// brute-forceable online inside the 5-minute window by anyone on the LAN.
func TestPairingClosesInviteAfterFailedAttempts(t *testing.T) {
	b := newBareServer(t)
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePeerLn(b) })
	host, port := splitAddr(t, b.peerLn.Addr().String())

	for i := 0; i < peerPairMaxAttempts; i++ {
		req := httptest.NewRequest(http.MethodPost, "/peer/pair", strings.NewReader(`{"proof":"nope","name":"x"}`))
		req.RemoteAddr = "192.168.1.9:4000"
		w := httptest.NewRecorder()
		b.handlePeerPair(w, req, "deadbeef")
		if w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: code %d", i, w.Code)
		}
	}

	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pairWithPeer(host, port, code); err == nil {
		t.Fatal("pairing succeeded with the correct code after the invite should have closed")
	}
	if b.peerByFingerprint(a.peerFP) != nil {
		t.Fatal("peer was pinned after the invite closed")
	}
}

// What a peer sends is data: names that aren't valid route names would break
// leaf-cert generation for every later save, and a remote-image icon is a
// tracking beacon rendered on this machine's dashboard.
func TestSanitizePeerSummaries(t *testing.T) {
	in := []peer.RouteSummary{
		{Name: "ok", Icon: "🚀"},
		{Name: "feat.app", Icon: "data:image/png;base64,AAAA"},
		{Name: "Bad Name!", Icon: "x"},
		{Name: "local"},
		{Name: "tracker", Icon: "https://evil.example/pixel.gif"},
		{Name: "a.b.c"},
	}
	out := sanitizePeerSummaries(in)
	var names []string
	for _, r := range out {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "ok,feat.app,tracker" {
		t.Errorf("names = %v", names)
	}
	if out[2].Icon != "" {
		t.Errorf("remote icon kept: %q", out[2].Icon)
	}
	if out[1].Icon == "" {
		t.Error("data:image icon dropped")
	}
}

// The favicon fetch follows what the app's HTML says. Only its own origin (or
// an inline data:image) is acceptable — an absolute href would otherwise make
// the daemon fetch an arbitrary external URL and store the result.
func TestResolveFaviconHref(t *testing.T) {
	base, _ := url.Parse("http://localhost:3000")
	cases := []struct {
		href string
		want string
		ok   bool
	}{
		{"/favicon.png", "http://localhost:3000/favicon.png", true},
		{"icons/f.ico", "http://localhost:3000/icons/f.ico", true},
		{"http://localhost:3000/f.ico", "http://localhost:3000/f.ico", true},
		{"https://cdn.example/f.ico", "", false},
		{"//cdn.example/f.ico", "", false},
		{"data:image/png;base64,AAAA", "data:image/png;base64,AAAA", true},
		{"data:text/html,<script>", "", false},
		{"javascript:alert(1)", "", false},
	}
	for _, c := range cases {
		got, ok := resolveFaviconHref(c.href, base)
		if ok != c.ok || got != c.want {
			t.Errorf("resolveFaviconHref(%q) = (%q, %v); want (%q, %v)", c.href, got, ok, c.want, c.ok)
		}
	}
}

// One transport per upstream, not one per request: each http.Transport owns
// an idle-connection pool.
func TestBookmarkTransportCached(t *testing.T) {
	s := testServer(t)
	a := s.bookmarkTransport("nas.local:5001", true)
	b := s.bookmarkTransport("nas.local:5001", true)
	c := s.bookmarkTransport("nas.local:5001", false)
	if a != b {
		t.Error("same upstream returned different transports")
	}
	if a == c {
		t.Error("insecure and verified transports must differ")
	}
}

// routes.json carries command lines (users put tokens inline) and per-route
// logs carry whatever the app prints. Neither is other local users' business.
func TestStateFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; Windows reports 0666 for writable files (ACLs govern access)")
	}
	dir := t.TempDir()
	table := NewRouteTable()
	table.Add(&Route{Name: "x", Type: RouteSticky, Port: 4100, RegisteredAt: time.Now()})
	if err := saveStickyRoutes(table, dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "routes.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("routes.json mode = %v, err %v; want 0600", info.Mode().Perm(), err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pm := NewProcessManager()
	_, _ = pm.Start(&Route{Name: "logperm", Type: RouteManaged, Cmd: "true", Dir: home, Port: 1})
	logPath := filepath.Join(config.Dir(), "logperm.log")
	if info, err := os.Stat(logPath); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("%s mode = %v, err %v; want 0600", logPath, info.Mode().Perm(), err)
	}
}

// The cap must hold against requests already in flight: a handler that
// snapshotted the invite on entry and verified after a slow body used to check
// its guess even after the cap closed the invite, so K parked connections
// bought K guesses. Here every request is parked on its body before any guess
// is checked; once the attempts are spent, even the correct proof is refused.
func TestPairingCapHoldsForConcurrentRequests(t *testing.T) {
	b := newBareServer(t)
	if err := b.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	const clientFP = "deadbeef"

	pair := func(body io.Reader) int {
		req := httptest.NewRequest(http.MethodPost, "/peer/pair", body)
		req.RemoteAddr = "192.168.1.9:4000"
		w := httptest.NewRecorder()
		b.handlePeerPair(w, req, clientFP)
		return w.Code
	}
	proofBody := func(code string) string {
		return fmt.Sprintf(`{"proof":%q,"name":"x","port":7444}`, peer.Proof(code, clientFP, b.peerFP))
	}

	// Control: with attempts to spare, the correct proof pairs.
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	if got := pair(strings.NewReader(proofBody(code))); got != http.StatusOK {
		t.Fatalf("control: correct proof = %d; want 200", got)
	}
	b.peerMu.Lock()
	b.peerList = nil // forget the control pin
	b.peerMu.Unlock()

	code, _, err = b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	const parked = peerPairMaxAttempts * 3
	writers := make([]*io.PipeWriter, parked)
	results := make([]chan int, parked)
	for i := range writers {
		pr, pw := io.Pipe()
		writers[i] = pw
		results[i] = make(chan int, 1)
		go func(i int, body io.Reader) { results[i] <- pair(body) }(i, pr)
	}
	time.Sleep(100 * time.Millisecond) // every handler is past its entry check
	for i := 0; i < parked-1; i++ {
		io.WriteString(writers[i], `{"proof":"nope","name":"x"}`)
		writers[i].Close()
		if got := <-results[i]; got != http.StatusForbidden {
			t.Fatalf("wrong guess %d = %d; want 403", i, got)
		}
	}
	last := parked - 1
	io.WriteString(writers[last], proofBody(code))
	writers[last].Close()
	if got := <-results[last]; got != http.StatusForbidden {
		t.Fatalf("correct proof after %d parked wrong guesses = %d; want 403 (cap bypassed)", parked-1, got)
	}
	if b.peerByFingerprint(clientFP) != nil {
		t.Fatal("parked request pinned a peer after the invite's attempts were spent")
	}
}
