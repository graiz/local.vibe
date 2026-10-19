//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startGroupChild starts cmd in its own process group and kills the group at
// cleanup. Port discovery inspects a route's process group, so the child must
// not share the test binary's (whose listeners would be "discovered").
func startGroupChild(t *testing.T, name string, args ...string) int {
	t.Helper()
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Skipf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		c.Wait()
	})
	return c.Process.Pid
}

func darkPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func TestPeerServeRouteRemoteRecovery(t *testing.T) {
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)

	// Alive, port not bound yet, nothing recorded: genuinely starting.
	boot := &Route{Name: "boot", Type: RouteManaged, Port: darkPort(t), RegisteredAt: time.Now()}
	boot.Running.Store(true)
	boot.SetPID(startGroupChild(t, "sleep", "60"))
	b.table.Add(boot)

	// Alive, but the readiness probe gave up and recorded why: nothing here
	// will retry, so the page must not promise to.
	stuck := &Route{Name: "stuck", Type: RouteManaged, Port: darkPort(t), RegisteredAt: time.Now()}
	stuck.Running.Store(true)
	stuck.SetPID(startGroupChild(t, "sleep", "60"))
	stuck.SetFailure(&Failure{Message: "Server started but never bound port"})
	b.table.Add(stuck)

	for _, c := range []struct {
		host        string
		wantRefresh bool
		wantText    string
	}{
		{"boot.vibe", true, "is starting"},
		{"stuck.vibe", false, "isn't running"},
	} {
		code, body := peerGetRoute(t, a, b, c.host)
		if code != http.StatusServiceUnavailable {
			t.Errorf("%s: got %d, want 503", c.host, code)
		}
		if got := strings.Contains(body, `http-equiv="refresh"`); got != c.wantRefresh {
			t.Errorf("%s: auto-refresh=%v, want %v: %q", c.host, got, c.wantRefresh, body)
		}
		if !strings.Contains(body, c.wantText) {
			t.Errorf("%s: missing %q: %q", c.host, c.wantText, body)
		}
	}
}

// A live managed child that rebound to another port is rediscovered on the
// remote path too — remote viewers can't drive /repair themselves.
func TestPeerServeRouteRepairsMovedPort(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	b := newBareServer(t)
	a := newBareServer(t)
	pairAB(t, a, b)

	newPort := darkPort(t)
	pid := startGroupChild(t, "python3", "-c", fmt.Sprintf(
		`import http.server, socketserver
with socketserver.TCPServer(("127.0.0.1", %d), http.server.BaseHTTPRequestHandler) as s:
    s.serve_forever()`, newPort))
	deadline := time.Now().Add(5 * time.Second)
	for !b.isPortReady(newPort) {
		if time.Now().After(deadline) {
			t.Fatalf("child never bound %d", newPort)
		}
		time.Sleep(50 * time.Millisecond)
	}

	moved := &Route{Name: "moved", Type: RouteManaged, Port: darkPort(t), RegisteredAt: time.Now()}
	moved.Running.Store(true)
	moved.SetPID(pid)
	b.table.Add(moved)

	code, body := peerGetRoute(t, a, b, "moved.vibe")
	if code == http.StatusServiceUnavailable {
		t.Fatalf("moved port not rediscovered; got the 503 page: %q", body)
	}
	if moved.Port != newPort {
		t.Errorf("route port = %d, want rediscovered %d", moved.Port, newPort)
	}
}
