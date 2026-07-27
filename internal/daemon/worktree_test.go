package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initGitRepoWithWorktree creates a git repo and one linked worktree on the
// given branch; returns (repoDir, worktreeDir). Skips the test if git is
// missing.
func initGitRepoWithWorktree(t *testing.T, branch string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	wtDir := filepath.Join(t.TempDir(), "wt")
	git("worktree", "add", "-b", branch, wtDir)
	return repo, wtDir
}

func TestParseRouteName(t *testing.T) {
	cases := []struct {
		name       string
		wantParent string
		wantErr    bool
	}{
		{"myapp", "", false},
		{"my-app2", "", false},
		{"feature-auth.myapp", "myapp", false},
		{"a.b", "b", false},
		{"UPPER", "", true}, // caller lowercases first; parse itself rejects
		{"-bad", "", true},
		{"wt.-bad", "", true},
		{"bad-.app", "", true},
		{"a.b.c", "", true},
		{"wt.local", "", true},
		{"local.app", "", true},
		{".app", "", true},
		{"wt.", "", true},
	}
	for _, c := range cases {
		parent, err := parseRouteName(c.name)
		if c.wantErr != (err != nil) {
			t.Errorf("parseRouteName(%q) err = %v; wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if parent != c.wantParent {
			t.Errorf("parseRouteName(%q) parent = %q; want %q", c.name, parent, c.wantParent)
		}
	}
}

// mkWorktreeDir creates a directory that passes for a linked git worktree:
// it contains the .git link file that `git worktree add` creates and
// `git worktree remove` deletes.
func mkWorktreeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorktreeDirGone(t *testing.T) {
	dir := mkWorktreeDir(t)
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Dir: dir}
	if worktreeDirGone(wt) {
		t.Errorf("valid worktree dir; want gone=false")
	}

	// A leftover dir whose .git link was removed (git worktree remove ran,
	// but stray files or a file-sync resurrected the folder) is gone too.
	if err := os.Remove(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if !worktreeDirGone(wt) {
		t.Errorf("dir without .git link; want gone=true")
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !worktreeDirGone(wt) {
		t.Errorf("dir removed; want gone=true")
	}
	// Non-worktree routes are never "gone", even with a missing Dir.
	main := &Route{Name: "app", Type: RouteManaged, Dir: filepath.Join(dir, "nope")}
	if worktreeDirGone(main) {
		t.Errorf("non-worktree route reported gone")
	}
}

// A running worktree route whose worktree was removed gets pruned on its next
// request — the healthy proxy path, not just the stopped/recovery path. This
// is the "process outlives the worktree" case: the child keeps serving from a
// deleted checkout until someone visits.
func TestRunningWorktreeRoutePrunedOnRequest(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	dir := mkWorktreeDir(t)
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Port: 3950, Cmd: "x", Dir: dir, RegisteredAt: time.Now()}
	wt.Running.Store(true)
	wt.Ready.Store(true)
	wt.SetPID(os.Getpid()) // alive PID so the managed path treats it as running
	s.table.Add(wt)

	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "f.app.test"
		w := httptest.NewRecorder()
		s.routeRequest(w, req)
		return w
	}

	// Healthy worktree: request does NOT prune (it will fail readiness and
	// serve the repair page since nothing listens, but the route survives).
	_ = get()
	if _, ok := s.table.Get("f.app"); !ok {
		t.Fatal("healthy worktree route was pruned")
	}

	// Remove the .git link — the worktree is gone; next request prunes and
	// redirects to the parent.
	if err := os.Remove(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	w := get()
	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("code = %d; want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://app.test/" {
		t.Errorf("Location = %q; want http://app.test/", loc)
	}
	if _, ok := s.table.Get("f.app"); ok {
		t.Errorf("route survived prune; want removed")
	}
}

// Persisted worktree routes reload with Parent derived from the dotted name;
// a worktree whose dir vanished while the daemon was down is dropped at load.
func TestPersistenceWorktreeRoundTripAndPrune(t *testing.T) {
	cfgDir := t.TempDir()
	liveDir := mkWorktreeDir(t)
	goneDir := t.TempDir()

	table := NewRouteTable()
	table.Add(&Route{Name: "app", Type: RouteManaged, Port: 3100, Cmd: "sleep 1", RegisteredAt: time.Now()})
	table.Add(&Route{Name: "live.app", Parent: "app", Type: RouteManaged, Port: 3101, Cmd: "sleep 1", Dir: liveDir, RegisteredAt: time.Now()})
	table.Add(&Route{Name: "gone.app", Parent: "app", Type: RouteManaged, Port: 3102, Cmd: "sleep 1", Dir: goneDir, RegisteredAt: time.Now()})
	if err := saveStickyRoutes(table, cfgDir); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(goneDir); err != nil {
		t.Fatal(err)
	}
	fresh := NewRouteTable()
	if err := loadStickyRoutes(fresh, cfgDir); err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.Get("gone.app"); ok {
		t.Errorf("gone.app survived load despite missing dir")
	}
	r, ok := fresh.Get("live.app")
	if !ok {
		t.Fatalf("live.app missing after reload")
	}
	if r.Parent != "app" {
		t.Errorf("reloaded Parent = %q; want \"app\"", r.Parent)
	}
	if _, ok := fresh.Get("app"); !ok {
		t.Errorf("plain route app missing after reload")
	}
}

func TestGoneWorktreeHostRedirectsToParent(t *testing.T) {
	s := testServer() // TLD "test", TLS disabled → scheme http

	parentRoute := &Route{Name: "app", Type: RouteSticky, Port: 3300, RegisteredAt: time.Now()}
	parentRoute.Running.Store(true)
	parentRoute.Ready.Store(true)
	s.table.Add(parentRoute)

	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
		req.Host = host
		w := httptest.NewRecorder()
		s.routeRequest(w, req)
		return w
	}

	// Unknown worktree host with a registered parent → 307 to the parent.
	w := get("gone.app.test")
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("code = %d; want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://app.test/" {
		t.Errorf("Location = %q; want http://app.test/", loc)
	}

	// Parent known only via a sibling worktree (parent-as-string): still 307.
	sib := &Route{Name: "live.other", Parent: "other", Type: RouteManaged, Port: 3301, Cmd: "sleep 1", RegisteredAt: time.Now()}
	s.table.Add(sib)
	if w := get("gone.other.test"); w.Code != http.StatusTemporaryRedirect {
		t.Errorf("sibling-known parent: code = %d; want 307", w.Code)
	}

	// Entirely unknown parent → falls through to the dashboard (200), no redirect.
	if w := get("gone.nobody.test"); w.Code != http.StatusOK {
		t.Errorf("unknown parent: code = %d; want 200 dashboard", w.Code)
	}
}

func TestRecoverManagedRoutePrunesGoneWorktree(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	dir := t.TempDir()
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Port: 3400, Cmd: "sleep 1", Dir: dir, RegisteredAt: time.Now()}
	s.table.Add(wt)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "f.app.test"
	w := httptest.NewRecorder()
	served := s.recoverManagedRoute(w, req, wt)

	if !served {
		t.Fatal("recoverManagedRoute returned served=false; want a served redirect")
	}
	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("code = %d; want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://app.test/" {
		t.Errorf("Location = %q; want http://app.test/", loc)
	}
	if _, ok := s.table.Get("f.app"); ok {
		t.Errorf("route survived prune; want removed")
	}
}

func TestSyncWorktreeRouteSyncsCmdOnly(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	dir := t.TempDir()
	vibeJSON := `{"name":"app","cmd":"npm run dev -- --new","port":3000,"oauth_callback_port":8123,"reserve_ports":{"server":3001}}`
	if err := os.WriteFile(filepath.Join(dir, "vibe.json"), []byte(vibeJSON), 0644); err != nil {
		t.Fatal(err)
	}

	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Port: 3500, Cmd: "npm run dev", Dir: dir,
		ReservePorts: map[string]int{"server": 3555}, RegisteredAt: time.Now()}
	s.table.Add(wt)

	if err := s.syncRouteFromVibeJSON(wt); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, _ := s.table.Get("f.app")
	if got.Cmd != "npm run dev -- --new" {
		t.Errorf("Cmd = %q; want the edited cmd synced", got.Cmd)
	}
	if got.OAuthCallbackPort != 0 {
		t.Errorf("OAuthCallbackPort = %d; want 0 (never imported for worktrees)", got.OAuthCallbackPort)
	}
	if got.ReservePorts["server"] != 3555 {
		t.Errorf("ReservePorts[server] = %d; want worktree-local 3555 preserved", got.ReservePorts["server"])
	}

	// A vibe.json naming some other app entirely must not sync anything.
	stranger := &Route{Name: "f.zzz", Parent: "zzz", Type: RouteManaged, Port: 3501, Cmd: "old", Dir: dir, RegisteredAt: time.Now()}
	s.table.Add(stranger)
	if err := s.syncRouteFromVibeJSON(stranger); err != nil {
		t.Fatalf("stranger sync: %v", err)
	}
	if got, _ := s.table.Get("f.zzz"); got.Cmd != "old" {
		t.Errorf("stranger Cmd = %q; want untouched", got.Cmd)
	}
}

func TestStartPageListsWorktrees(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	main := &Route{Name: "app", Type: RouteManaged, Port: 3600, Cmd: "npm run dev", RegisteredAt: time.Now()}
	s.table.Add(main)
	wt := &Route{Name: "feature-auth.app", Parent: "app", Type: RouteManaged, Port: 3601, Cmd: "npm run dev", Dir: t.TempDir(), RegisteredAt: time.Now()}
	wt.Running.Store(true)
	s.table.Add(wt)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.serveStartPage(w, req, main)
	body := w.Body.String()

	if !strings.Contains(body, "feature-auth") {
		t.Errorf("start page missing worktree name; body:\n%s", body)
	}
	if !strings.Contains(body, "http://feature-auth.app.test/") {
		t.Errorf("start page missing worktree URL")
	}
	// Picker mode must NOT auto-launch main on page load — that's the whole
	// point of the picker. vibeAutoLaunch=true gates the on-load startApp()
	// call, emitted only for worktree-less apps.
	if strings.Contains(body, "vibeAutoLaunch=true") {
		t.Errorf("picker page auto-launches main; want manual Start button")
	}

	// A route with no children renders no worktree section.
	lone := &Route{Name: "solo", Type: RouteManaged, Port: 3602, Cmd: "x", RegisteredAt: time.Now()}
	s.table.Add(lone)
	w2 := httptest.NewRecorder()
	s.serveStartPage(w2, req, lone)
	// Match the section markup, not "wt-list" alone — the CSS class
	// definition is always present in the <style> block.
	if strings.Contains(w2.Body.String(), `<div class="wt-list">`) {
		t.Errorf("childless start page rendered a worktree section")
	}
	// A worktree-less app keeps the auto-launch UX on its start page.
	if !strings.Contains(w2.Body.String(), "vibeAutoLaunch=true") {
		t.Errorf("childless start page lost its auto-launch behavior")
	}
}

func TestDiscoverUnregisteredWorktrees(t *testing.T) {
	s := testServer()
	repo, wtDir := initGitRepoWithWorktree(t, "feature/magic")

	parent := &Route{Name: "app", Type: RouteManaged, Port: 3900, Cmd: "x", Dir: repo, RegisteredAt: time.Now()}
	s.table.Add(parent)

	found := s.discoverUnregisteredWorktrees(parent)
	if len(found) != 1 {
		t.Fatalf("discovered %d worktrees; want 1", len(found))
	}
	if found[0].Slug != "feature-magic" {
		t.Errorf("Slug = %q; want feature-magic", found[0].Slug)
	}

	// Registering the worktree as a child route removes it from discovery.
	s.table.Add(&Route{Name: "feature-magic.app", Parent: "app", Type: RouteManaged, Port: 3901, Cmd: "x", Dir: wtDir, RegisteredAt: time.Now()})
	if found := s.discoverUnregisteredWorktrees(parent); len(found) != 0 {
		t.Errorf("discovered %v after registration; want none", found)
	}

	// Worktree routes and routes without a Dir never discover.
	child, _ := s.table.Get("feature-magic.app")
	if got := s.discoverUnregisteredWorktrees(child); got != nil {
		t.Errorf("child discovery = %v; want nil", got)
	}
	noDir := &Route{Name: "plain", Type: RouteManaged, Port: 3902, Cmd: "x", RegisteredAt: time.Now()}
	if got := s.discoverUnregisteredWorktrees(noDir); got != nil {
		t.Errorf("no-dir discovery = %v; want nil", got)
	}
}

func TestStartPageListsDiscoveredWorktrees(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	repo, _ := initGitRepoWithWorktree(t, "feature/magic")

	parent := &Route{Name: "app", Type: RouteManaged, Port: 3903, Cmd: "x", Dir: repo, RegisteredAt: time.Now()}
	s.table.Add(parent)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.serveStartPage(w, req, parent)
	body := w.Body.String()

	if !strings.Contains(body, "feature-magic") {
		t.Errorf("start page missing discovered worktree; body:\n%s", body)
	}
	if !strings.Contains(body, "startWorktree(") {
		t.Errorf("discovered worktree row missing register-and-start affordance")
	}
}

func TestDashboardGroupsWorktreesUnderParent(t *testing.T) {
	s := testServer()

	// Deliberately name the worktree so a plain name sort would NOT place it
	// after its parent ("aaa.zapp" < "other" < "zapp").
	for _, r := range []*Route{
		{Name: "zapp", Type: RouteManaged, Port: 3700, Cmd: "x", RegisteredAt: time.Now()},
		{Name: "aaa.zapp", Parent: "zapp", Type: RouteManaged, Port: 3701, Cmd: "x", Dir: os.TempDir(), RegisteredAt: time.Now()},
		{Name: "other", Type: RouteSticky, Port: 3702, RegisteredAt: time.Now()},
	} {
		s.table.Add(r)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "local.test"
	w := httptest.NewRecorder()
	s.serveDashboard(w, req)
	body := w.Body.String()

	iZapp := strings.Index(body, ">zapp<")
	iWt := strings.Index(body, ">aaa.zapp<")
	if iZapp == -1 || iWt == -1 {
		t.Fatalf("dashboard missing route rows (zapp@%d wt@%d)", iZapp, iWt)
	}
	if iWt < iZapp {
		t.Errorf("worktree row renders before its parent; want grouped after")
	}
	if !strings.Contains(body, "wt-tr") {
		t.Errorf("worktree row missing wt-tr class")
	}

	// Orphan group: worktree whose parent is only a string gets a header row.
	s.table.Add(&Route{Name: "b.ghost", Parent: "ghost", Type: RouteManaged, Port: 3703, Cmd: "x", Dir: os.TempDir(), RegisteredAt: time.Now()})
	w2 := httptest.NewRecorder()
	s.serveDashboard(w2, req)
	if !strings.Contains(w2.Body.String(), "wt-group-header") {
		t.Errorf("orphan worktree group missing header row")
	}
}
