package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/config"
)

func TestScanLogForPort(t *testing.T) {
	cases := []struct {
		name     string
		tail     string
		wantPort int
		wantOK   bool
	}{
		{
			name:     "vite-local-line",
			tail:     "  VITE v5.0.0  ready in 312 ms\n\n  ➜  Local:   http://localhost:5173/\n  ➜  Network: use --host to expose\n",
			wantPort: 5173,
			wantOK:   true,
		},
		{
			name:     "nextjs-ready-on-new-port",
			tail:     "⚠  Port 3000 is in use, trying 3001 instead.\n   ▲ Next.js 14.0.4\n   - Local:        http://localhost:3001\n   - Network:      http://192.168.1.7:3001\n ✓ Ready in 2.1s",
			wantPort: 3001,
			wantOK:   true,
		},
		{
			name:     "nodejs-listening-on",
			tail:     "server booted\nlistening on :8080\n",
			wantPort: 8080,
			wantOK:   true,
		},
		{
			name:     "express-style",
			tail:     "Express server listening at port 4000\n",
			wantPort: 4000,
			wantOK:   true,
		},
		{
			name:     "ipv6-loopback",
			tail:     "serving on http://[::1]:7777\n",
			wantPort: 7777,
			wantOK:   true,
		},
		{
			name:     "prefers-newest-http-match",
			tail:     "initially http://localhost:3000\nreload: http://localhost:3002\n",
			wantPort: 3002,
			wantOK:   true,
		},
		{
			name:     "ignores-sub-1024",
			tail:     "listening on :80\n",
			wantPort: 0,
			wantOK:   false,
		},
		{
			name:     "no-match",
			tail:     "npm ERR! missing script: dev\n",
			wantPort: 0,
			wantOK:   false,
		},
		{
			name:     "empty",
			tail:     "",
			wantPort: 0,
			wantOK:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := scanLogForPort(tc.tail)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v; want %v (got port %d)", ok, tc.wantOK, got)
			}
			if got != tc.wantPort {
				t.Errorf("port = %d; want %d", got, tc.wantPort)
			}
		})
	}
}

func TestParseLsofListenPort(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantPort int
		wantOK   bool
	}{
		{
			name: "standard-macos-output",
			input: `COMMAND    PID USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node     12345 greg   24u  IPv4 0x1234567890      0t0  TCP 127.0.0.1:3001 (LISTEN)
node     12345 greg   25u  IPv6 0x1234567891      0t0  TCP [::1]:3001 (LISTEN)
`,
			wantPort: 3001,
			wantOK:   true,
		},
		{
			name:     "wildcard-bind",
			input:    "node 99 user 5u IPv4 0 0t0 TCP *:4567 (LISTEN)\n",
			wantPort: 4567,
			wantOK:   true,
		},
		{
			name:     "no-listener",
			input:    "",
			wantPort: 0,
			wantOK:   false,
		},
		{
			name:     "established-not-listen",
			input:    "node 99 user 5u IPv4 0 0t0 TCP 127.0.0.1:3001->127.0.0.1:4000 (ESTABLISHED)\n",
			wantPort: 0,
			wantOK:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLsofListenPort(tc.input)
			if ok != tc.wantOK || got != tc.wantPort {
				t.Errorf("= (%d, %v); want (%d, %v)", got, ok, tc.wantPort, tc.wantOK)
			}
		})
	}
}

// TestDiscoverRoutePortFromLog exercises the round trip: a sticky route
// registered at a bogus port whose log file announces a different port
// where a real listener is answering. discoverRoutePort should return
// the listener's port.
func TestDiscoverRoutePortFromLog(t *testing.T) {
	tmp := t.TempDir()

	// Real listener on an OS-picked port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	realPort := ln.Addr().(*net.TCPAddr).Port

	// Seed a log file with a Vite-style "Local: http://localhost:NNNN" line.
	name := "myroute"
	logPath := filepath.Join(tmp, name+".log")
	logBody := fmt.Sprintf("  ➜  Local:   http://localhost:%d/\n", realPort)
	if err := os.WriteFile(logPath, []byte(logBody), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	route := &Route{Name: name, Port: 9, Type: RouteSticky} // port 9 is discard — nothing there
	s.table.Add(route)

	got, ok := s.discoverRoutePort(route)
	if !ok {
		t.Fatalf("discoverRoutePort returned not found")
	}
	if got != realPort {
		t.Errorf("got port %d; want %d", got, realPort)
	}
}

// TestHandleRepairUpdatesPort drives the /_api/routes/{name}/repair
// endpoint end-to-end: register a sticky route at a dead port, start a
// real listener on a different port and record it in the route's log,
// then hit the endpoint and confirm the route was rewritten and
// persisted.
func TestHandleRepairUpdatesPort(t *testing.T) {
	tmp := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	realPort := ln.Addr().(*net.TCPAddr).Port

	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	name := "lp"
	oldPort := 9 // discard port — no listener
	route := &Route{Name: name, Port: oldPort, Type: RouteSticky}
	route.Running.Store(true)
	s.table.Add(route)

	// Write log with new port so the repair can discover it.
	if err := os.WriteFile(filepath.Join(tmp, name+".log"),
		[]byte(fmt.Sprintf("ready on http://localhost:%d\n", realPort)), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	req := httptest.NewRequest("GET", "/_api/routes/"+name+"/repair", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("ok = %v; want true; resp: %s", resp["ok"], w.Body.String())
	}
	// JSON numbers decode as float64.
	if gotPort, _ := resp["port"].(float64); int(gotPort) != realPort {
		t.Errorf("resp.port = %v; want %d", resp["port"], realPort)
	}
	if gotFrom, _ := resp["from"].(float64); int(gotFrom) != oldPort {
		t.Errorf("resp.from = %v; want %d", resp["from"], oldPort)
	}

	// In-memory route should reflect the update.
	if r, ok := s.table.Get(name); !ok || r.Port != realPort {
		t.Errorf("route.Port = %d; want %d", r.Port, realPort)
	}

	// routes.json must have been rewritten with the new port.
	data, err := os.ReadFile(filepath.Join(tmp, "routes.json"))
	if err != nil {
		t.Fatalf("read routes.json: %v", err)
	}
	if !bytes.Contains(data, []byte(fmt.Sprintf(`"port": %d`, realPort))) {
		t.Errorf("routes.json does not contain new port %d:\n%s", realPort, data)
	}
}

// TestHandleRepairAlreadyReachable short-circuits discovery when the
// registered port is actually still listening.
func TestHandleRepairAlreadyReachable(t *testing.T) {
	tmp := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	route := &Route{Name: "live", Port: port, Type: RouteSticky}
	s.table.Add(route)

	req := httptest.NewRequest("GET", "/_api/routes/live/repair", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v; want true", resp["ok"])
	}
	if resp["note"] != "already reachable" {
		t.Errorf("note = %v; want 'already reachable'", resp["note"])
	}
}

// TestHandleRepairRefusesToAdoptPortOwnedByAnotherRoute guards against the
// "lp points at apply's port 3000 orphan" scenario: if the port we'd like
// to adopt is already registered to a different route, silently rewriting
// would make both .vibe names proxy to the same process. Refuse and return
// ok:false so the user can clean up (usually by killing the orphan).
func TestHandleRepairRefusesToAdoptPortOwnedByAnotherRoute(t *testing.T) {
	tmp := t.TempDir()

	// Real listener on an OS-picked port that will be "owned" by another route.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	otherPort := ln.Addr().(*net.TCPAddr).Port

	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	// The route whose log points at the other listener. Its registered port
	// is "dead" (9 / discard).
	lp := &Route{Name: "lp", Port: 9, Type: RouteManaged, Cmd: "make dev"}
	s.table.Add(lp)

	// Another route that's registered on the port the log scan would find.
	apply := &Route{Name: "apply", Port: otherPort, Type: RouteManaged, Cmd: "npm run dev"}
	s.table.Add(apply)

	if err := os.WriteFile(filepath.Join(tmp, "lp.log"),
		[]byte(fmt.Sprintf("ready on http://localhost:%d\n", otherPort)), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	req := httptest.NewRequest("GET", "/_api/routes/lp/repair", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["ok"] != false {
		t.Fatalf("ok = %v; want false (adoption refused); body: %s", resp["ok"], w.Body.String())
	}
	reason, _ := resp["reason"].(string)
	if !strings.Contains(reason, "apply") {
		t.Errorf("reason = %q; want it to name the conflicting route", reason)
	}
	if resp["restartable"] != true {
		t.Errorf("restartable = %v; want true for managed route with dead child", resp["restartable"])
	}
	// Route must NOT have been rewritten.
	if got, _ := s.table.Get("lp"); got.Port != 9 {
		t.Errorf("lp.Port = %d; want unchanged 9", got.Port)
	}
}

// TestHandleRepairNoCandidate returns ok:false when there's no listener
// and no log hint; for managed routes with a dead child it signals
// restartable:true so the browser can offer a Restart button.
func TestHandleRepairNoCandidate(t *testing.T) {
	tmp := t.TempDir()
	s := NewServer(&config.Config{Daemon: config.DaemonConfig{Port: 0, TLD: "test"}})
	s.ConfigDir = tmp

	route := &Route{Name: "dead", Port: 9, Type: RouteManaged, Cmd: "echo hi"}
	s.table.Add(route)

	req := httptest.NewRequest("GET", "/_api/routes/dead/repair", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ok"] != false {
		t.Errorf("ok = %v; want false", resp["ok"])
	}
	if resp["restartable"] != true {
		t.Errorf("restartable = %v; want true for managed route without a live child", resp["restartable"])
	}
}

// portFromLog reads through tailLogFile, which strips ANSI (log_tail.go).
// That matters here and not only cosmetically: Vite colours the URL *and
// bolds the port itself*, so an escape lands between the colon and the
// digits — "http://localhost:<ESC>[1m5173<ESC>[22m/". Matching the raw bytes
// misses the port entirely, and the log-tail fallback is what recovers a
// managed route whose registered port went stale.
func TestPortFromLogFindsPortInColouredViteOutput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vite.log")
	line := "  \x1b[32m➜\x1b[39m  \x1b[1mLocal\x1b[22m:   " +
		"\x1b[36mhttp://localhost:\x1b[1m5173\x1b[22m/\x1b[39m"
	if err := os.WriteFile(p, []byte("VITE v5.0.0  ready in 300 ms\n"+line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, ok := portFromLog(p)
	if !ok {
		t.Fatal("no port found in coloured Vite output")
	}
	if got != 5173 {
		t.Errorf("port = %d, want 5173", got)
	}
}

// The decoration filter must never drop a line carrying a port: any such
// line has digits, which disqualifies it as decoration. Guards the other
// half of the tail filtering against the same recovery path.
func TestPortFromLogSurvivesBannerHeavyLog(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "banner.log")
	body := strings.Join([]string{
		"  .==================.",
		"  | |--.__.--.__.--| |",
		"  ▀▀▀▀▀▀▀▀▀▀",
		"listening on port 4321",
		"  ╰─────────╯",
	}, "\n")
	if err := os.WriteFile(p, []byte(body+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, ok := portFromLog(p)
	if !ok || got != 4321 {
		t.Errorf("portFromLog = (%d, %v), want (4321, true)", got, ok)
	}
}
