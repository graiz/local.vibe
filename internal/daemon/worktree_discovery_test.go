//go:build !windows

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeRepoWithWorktree builds a real git repo plus one linked worktree and
// returns (mainDir, worktreeDir). Real git is used rather than a hand-faked
// .git layout because discovery shells out to `git worktree list`.
func makeRepoWithWorktree(t *testing.T, branch string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", main, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	git("worktree", "add", "-b", branch, wt)
	return main, wt
}

// A worktree created while its app is running used to be invisible: the picker
// that lists on-disk worktrees only renders on the stopped-parent recovery
// path. This endpoint makes discovery reachable regardless of parent state.
func TestListWorktreesEndpoint(t *testing.T) {
	mainDir, wtDir := makeRepoWithWorktree(t, "feature/x")

	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name: "app", Type: RouteManaged, Port: 3300,
		Cmd: "sleep 30", Dir: mainDir, RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/_api/worktrees", nil)
	req.Host = "local.test"
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []worktreeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d worktrees, want 1: %+v", len(got), got)
	}
	if got[0].Parent != "app" || got[0].Name != "feature-x.app" {
		t.Errorf("got %+v, want parent=app name=feature-x.app", got[0])
	}
	// Compare resolved paths: git reports the real path, while t.TempDir()
	// hands back the /var symlink on macOS.
	if realpath(got[0].Path) != realpath(wtDir) {
		t.Errorf("path = %q, want %q", got[0].Path, wtDir)
	}

	// Once registered it is no longer "unregistered" and must drop out, or the
	// dashboard would show it twice — once as a route, once as discoverable.
	s.table.Add(&Route{
		Name: "feature-x.app", Parent: "app", Type: RouteManaged,
		Port: 3301, Cmd: "sleep 30", Dir: wtDir, RegisteredAt: time.Now(),
	})
	w2 := httptest.NewRecorder()
	s.apiHandler(w2, httptest.NewRequest(http.MethodGet, "/_api/worktrees", nil))
	var after []worktreeResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &after)
	if len(after) != 0 {
		t.Errorf("registered worktree still reported as discoverable: %+v", after)
	}
}

// An app with no worktrees must produce an empty array, not null — the CLI and
// dashboard both decode this directly.
func TestListWorktreesEndpointEmpty(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{Name: "plain", Type: RouteManaged, Port: 3310, Dir: t.TempDir(), RegisteredAt: time.Now()})

	w := httptest.NewRecorder()
	s.apiHandler(w, httptest.NewRequest(http.MethodGet, "/_api/worktrees", nil))
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

// The dashboard must render the discovered worktree with a launch control, and
// group it under its parent app.
func TestDashboardShowsDiscoveredWorktree(t *testing.T) {
	mainDir, wtDir := makeRepoWithWorktree(t, "feature/y")

	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name: "app", Type: RouteManaged, Port: 3320,
		Cmd: "sleep 30", Dir: mainDir, RegisteredAt: time.Now(),
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "local.test"
	s.serveDashboard(w, req)
	body := w.Body.String()

	if !strings.Contains(body, "feature-y.app") {
		t.Error("dashboard does not list the discovered worktree")
	}
	if !strings.Contains(body, "startDiscoveredWorktree") {
		t.Error("dashboard has no launch control for the discovered worktree")
	}
	// The launch call must carry the parent and the discovered path, since the
	// daemon validates the path against what git reports.
	if !strings.Contains(body, filepath.Base(wtDir)) {
		t.Errorf("dashboard does not pass the worktree path to the launch control")
	}
	// Grouped under its parent: the app row comes first.
	if ai, wi := strings.Index(body, ">app<"), strings.Index(body, "feature-y.app"); ai >= 0 && wi >= 0 && ai > wi {
		t.Error("discovered worktree should render after its parent app")
	}
}
