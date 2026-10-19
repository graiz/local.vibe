package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func listTestServer() *Server {
	s := testServer()
	s.cfg.Daemon.TLD = "vibe"
	s.cfg.Daemon.Port = 7999
	return s
}

func listRoutesAs(t *testing.T, s *Server, host, remoteAddr string) map[string]routeResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/_api/routes", nil)
	req.Host = host
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	var routes []routeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &routes); err != nil {
		t.Fatalf("host %q: unmarshal: %v; body: %s", host, err, w.Body.String())
	}
	got := map[string]routeResponse{}
	for _, r := range routes {
		got[r.Name] = r
	}
	return got
}

// `vibe ls -l` reads a route's directory and command from the list API.
func TestListRoutesIncludesDirAndCmd(t *testing.T) {
	s := listTestServer()
	s.table.Add(&Route{Name: "app", Port: 3000, Type: RouteManaged, Cmd: "node server.js", Dir: "/work/app"})
	s.table.Add(&Route{Name: "plain", Port: 3001, Type: RouteSticky})

	for _, c := range []struct{ name, host, remote string }{
		{"unix socket (CLI)", "local", "@"},
		{"unix socket, empty peer", "local", ""},
		{"TCP fallback (CLI)", "127.0.0.1:7999", "127.0.0.1:50000"},
		{"dashboard", "local.vibe", "127.0.0.1:50000"},
	} {
		got := listRoutesAs(t, s, c.host, c.remote)
		if r := got["app"]; r.Dir != "/work/app" || r.Cmd != "node server.js" {
			t.Errorf("%s: app dir=%q cmd=%q; want /work/app, node server.js", c.name, r.Dir, r.Cmd)
		}
		if r := got["plain"]; r.Dir != "" || r.Cmd != "" {
			t.Errorf("%s: plain dir=%q cmd=%q; want both empty", c.name, r.Dir, r.Cmd)
		}
	}
}

// GET /_api/routes is answered on every .vibe host, so a page served on one
// (a proxy bookmark's third-party JS, or any app) can read it same-origin.
// Paths and command lines — which can carry inline secrets — must only go to
// the CLI and the dashboard.
func TestListRoutesHidesDirAndCmdFromOtherHosts(t *testing.T) {
	s := listTestServer()
	s.table.Add(&Route{Name: "app", Port: 3000, Type: RouteManaged, Cmd: "DATABASE_URL=secret npm run dev", Dir: "/work/app"})
	s.table.Add(&Route{Name: "ha", Type: RouteBookmark, ExternalURL: "https://ha.example.com", Proxy: true})

	for _, c := range []struct{ name, host string }{
		{"proxy bookmark host", "ha.vibe"},
		{"route host", "app.vibe"},
		{"DNS rebinding", "attacker.example:7999"},
		{"local.vibe on a foreign port", "local.vibe:31337"},
		{"loopback on a foreign port", "127.0.0.1:31337"},
	} {
		got := listRoutesAs(t, s, c.host, "127.0.0.1:50000")
		r, ok := got["app"]
		if !ok {
			t.Fatalf("%s: app missing from listing", c.name)
		}
		if r.Dir != "" || r.Cmd != "" {
			t.Errorf("%s: leaked dir=%q cmd=%q", c.name, r.Dir, r.Cmd)
		}
	}
}
