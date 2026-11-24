//go:build !windows

package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// handleStart's kill_pid / kill_port recovery is destructive. A start that is
// going to fail on config (here: no launch command) must fail before it, not
// kill the named process on its way to a 400.
func TestStartConfigFailureKillsNothing(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap in the background: an unreaped child stays a zombie, which
	// processAlive (signal 0) still reports as alive.
	exited := make(chan struct{})
	go func() { _ = victim.Wait(); close(exited) }()
	t.Cleanup(func() { _ = victim.Process.Kill(); <-exited })
	s.table.Add(&Route{Name: "nocmd", Type: RouteManaged, Port: 4300, Dir: t.TempDir(), RegisteredAt: time.Now()})

	body := fmt.Sprintf(`{"kill_pid":%d}`, victim.Process.Pid)
	req := httptest.NewRequest(http.MethodPost, "/_api/routes/nocmd/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("start with no cmd = %d %s; want 400", w.Code, w.Body.String())
	}
	select {
	case <-exited:
		t.Fatal("kill_pid ran even though the start was going to fail on config")
	case <-time.After(200 * time.Millisecond):
	}
}

// A route's own origin (which includes every third-party script its app
// loads) may send kill_pid only for the PID the start page is offering —
// the route's current kill_pid recovery hint. Before, any page on app.vibe
// could SIGTERM any of the user's processes.
func TestRouteOriginKillPIDLimitedToOfferedPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	route := &Route{Name: "app", Type: RouteManaged, Port: 1, Cmd: "true", Dir: t.TempDir(), RegisteredAt: time.Now()}
	s.table.Add(route)

	spawnVictim := func() (*exec.Cmd, chan struct{}) {
		v := exec.Command("sleep", "60")
		if err := v.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() { _ = v.Wait(); close(exited) }()
		t.Cleanup(func() { _ = v.Process.Kill(); <-exited })
		return v, exited
	}
	startFromRoute := func(pid int) int {
		req := httptest.NewRequest(http.MethodPost, "/_api/routes/app/start", strings.NewReader(fmt.Sprintf(`{"kill_pid":%d}`, pid)))
		req.Host = "app.test"
		req.Header.Set("Origin", "https://app.test")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		return w.Code
	}

	victim, exited := spawnVictim()
	if code := startFromRoute(victim.Process.Pid); code != http.StatusForbidden {
		t.Fatalf("kill_pid of an arbitrary process from the route origin = %d; want 403", code)
	}
	select {
	case <-exited:
		t.Fatal("route origin killed an arbitrary process")
	case <-time.After(200 * time.Millisecond):
	}

	// The offered PID is allowed — the start page's one-click recovery.
	offered, offeredExited := spawnVictim()
	route.SetFailure(&Failure{Message: "port held", Recovery: &Recovery{Action: "kill_pid", PID: offered.Process.Pid}})
	if code := startFromRoute(offered.Process.Pid); code == http.StatusForbidden {
		t.Fatal("kill_pid of the offered recovery PID was refused")
	}
	select {
	case <-offeredExited:
	case <-time.After(2 * time.Second):
		t.Fatal("offered recovery PID was not killed")
	}
}

// `vibe stop <sticky>` must stop the server on the route's port (as it did
// before the stop-path consolidation), not report success and leave it up.
// Bookmarks have nothing local to stop and say so.
func TestStopStickyKillsItsPortHolder(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	server := exec.Command("sleep", "60")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = server.Wait(); close(exited) }()
	t.Cleanup(func() { _ = server.Process.Kill(); <-exited })

	prev := findPortHoldersFn
	findPortHoldersFn = func(port int) []int {
		if port == 4400 {
			return []int{server.Process.Pid}
		}
		return nil
	}
	t.Cleanup(func() { findPortHoldersFn = prev })

	s.table.Add(&Route{Name: "sticky", Type: RouteSticky, Port: 4400, RegisteredAt: time.Now()})
	s.table.Add(&Route{Name: "bm", Type: RouteBookmark, ExternalURL: "https://example.com", RegisteredAt: time.Now()})

	call := func(name string) int {
		req := httptest.NewRequest(http.MethodPost, "/_api/routes/"+name+"/stop", nil)
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		return w.Code
	}
	if code := call("sticky"); code != http.StatusOK {
		t.Fatalf("stop sticky = %d; want 200", code)
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("stop sticky left the server on its port running")
	}
	if code := call("bm"); code != http.StatusBadRequest {
		t.Fatalf("stop bookmark = %d; want 400", code)
	}
}
