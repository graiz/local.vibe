package daemon

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func addAppRoute(s *Server, name string, port int, running bool, reserve map[string]int) *Route {
	r := &Route{Name: name, Port: port, Type: RouteManaged, Cmd: "true", ReservePorts: reserve}
	r.Running.Store(running)
	s.table.Add(r)
	return r
}

func doAPI(t *testing.T, s *Server, method, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	return w
}

// Issue #10: a callback port equal to a *running* route's app port is
// rejected — the bridge would bind it and steal (or fail against) that app's
// localhost traffic.
func TestRegisterRejectsOAuthPortHeldByRunningRoute(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	p := freeLoopbackPort(t)
	addAppRoute(s, "web", p, true, nil)

	w := doAPI(t, s, http.MethodPost, "/_api/routes", map[string]any{
		"name": "auth", "port": p + 1, "oauth_callback_port": p,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `\"web\"`) {
		t.Errorf("error should name the colliding route; got %s", w.Body.String())
	}
	if _, ok := s.table.Get("auth"); ok {
		t.Error("rejected route was left in the table")
	}
}

func TestRegisterRejectsOAuthPortInRunningRouteReservePorts(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	p := freeLoopbackPort(t)
	addAppRoute(s, "web", p+2, true, map[string]int{"api": p})

	w := doAPI(t, s, http.MethodPost, "/_api/routes", map[string]any{
		"name": "auth", "port": p + 1, "oauth_callback_port": p,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", w.Code, w.Body.String())
	}
}

// A stopped route's port only collides once it starts, so registration is
// allowed but the response carries a warning (start-time vibePortClaim then
// refuses to start the other route, naming the bridge).
func TestRegisterWarnsOnOAuthPortOfStoppedRoute(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	p := freeLoopbackPort(t)
	addAppRoute(s, "web", p, false, nil)

	w := doAPI(t, s, http.MethodPost, "/_api/routes", map[string]any{
		"name": "auth", "port": p + 1, "oauth_callback_port": p,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], `"web"`) {
		t.Errorf("warnings = %q; want one naming \"web\"", resp.Warnings)
	}
	// And the stopped route can no longer start onto the bridged port.
	web, _ := s.table.Get("web")
	if msg := s.checkVibePortCollisions(web); !strings.Contains(msg, "oauth_callback_port") {
		t.Errorf("start-time collision check = %q; want it to name the oauth_callback_port", msg)
	}
}

func TestUpdateRejectsOAuthPortHeldByRunningRoute(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	p := freeLoopbackPort(t)
	addAppRoute(s, "web", p, true, nil)
	auth := &Route{Name: "auth", Port: p + 1, Type: RouteSticky}
	auth.Running.Store(true)
	s.table.Add(auth)

	w := doAPI(t, s, http.MethodPut, "/_api/routes/auth", map[string]any{"oauth_callback_port": p})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", w.Code, w.Body.String())
	}
	if got, _ := s.table.Get("auth"); got.OAuthCallbackPort != 0 {
		t.Errorf("rejected update left oauth_callback_port = %d", got.OAuthCallbackPort)
	}
}

// An edit that doesn't touch the callback port must not be blocked by a
// collision that already existed (e.g. the other route started later via
// some path that bypassed the claim check) — only new/changed ports are vetted.
func TestUpdateUnrelatedFieldIgnoresExistingOAuthCollision(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	p := freeLoopbackPort(t)
	auth := &Route{Name: "auth", Port: p + 1, Type: RouteSticky, OAuthCallbackPort: p}
	auth.Running.Store(true)
	s.table.Add(auth)
	addAppRoute(s, "web", p, true, nil)

	w := doAPI(t, s, http.MethodPut, "/_api/routes/auth", map[string]any{"icon": "🔑"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
}

// holdPort binds a loopback port for the test's lifetime, standing in for an
// unrelated process that grabbed a callback port after it was registered.
func holdPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// Issue #11 (conflict handling when another process binds a callback port
// after registration): reconcile used to return on the first unbindable port,
// so one route's busy bridge made every unrelated register/update 409 — the
// new route was rejected for a port it never asked for.
func TestRegisterUnaffectedByAnotherRoutesUnbindableBridge(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	busy := holdPort(t)
	x := &Route{Name: "x", Port: freeLoopbackPort(t), Type: RouteSticky, OAuthCallbackPort: busy}
	x.Running.Store(true)
	s.table.Add(x)

	w := doAPI(t, s, http.MethodPost, "/_api/routes", map[string]any{"name": "z", "port": freeLoopbackPort(t)})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (z has no bridge); body: %s", w.Code, w.Body.String())
	}
	w = doAPI(t, s, http.MethodPut, "/_api/routes/z", map[string]any{"icon": "🔑"})
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
}

// ...and the same early return left every *other* bridge (in random map
// order) unbound, e.g. at daemon startup.
func TestReconcileBindsRemainingBridgesWhenOneFails(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	busy := holdPort(t)
	bad := &Route{Name: "bad", Port: freeLoopbackPort(t), Type: RouteSticky, OAuthCallbackPort: busy}
	s.table.Add(bad)
	var good []int
	for i := 0; i < 6; i++ {
		p := freeLoopbackPort(t)
		good = append(good, p)
		s.table.Add(&Route{Name: "good" + string(rune('a'+i)), Port: freeLoopbackPort(t), Type: RouteSticky, OAuthCallbackPort: p})
	}

	errs := s.reconcileOAuthBridgeListeners()
	if errs[busy] == nil {
		t.Errorf("want an error recorded for the busy port %d; got %v", busy, errs)
	}
	s.oauthBridgeMu.Lock()
	defer s.oauthBridgeMu.Unlock()
	for _, p := range good {
		if _, ok := s.oauthBridgeServers[p]; !ok {
			t.Errorf("bridge on free port %d was not bound because another port failed", p)
		}
	}
}

// Checklist item: API update can clear oauth_callback_port explicitly, and
// clearing it releases the listener.
func TestUpdateClearsOAuthCallbackPortAndReleasesListener(t *testing.T) {
	s := testServer(t)
	defer s.stopOAuthBridgeListeners()
	cb := freeLoopbackPort(t)
	w := doAPI(t, s, http.MethodPost, "/_api/routes", map[string]any{
		"name": "auth", "port": freeLoopbackPort(t), "oauth_callback_port": cb,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}
	w = doAPI(t, s, http.MethodPut, "/_api/routes/auth", map[string]any{"oauth_callback_port": 0})
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d; body: %s", w.Code, w.Body.String())
	}
	if got, _ := s.table.Get("auth"); got.OAuthCallbackPort != 0 {
		t.Errorf("oauth_callback_port = %d after clearing; want 0", got.OAuthCallbackPort)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cb)))
	if err != nil {
		t.Fatalf("callback port %d still held after clearing: %v", cb, err)
	}
	ln.Close()
}
