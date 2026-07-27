# Worktree Routes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `vibe start` inside a git worktree registers `https://<branch-slug>.<app>.vibe` on its own auto-assigned port instead of clobbering the main checkout's route; worktree routes clean themselves up when the worktree disappears.

**Architecture:** A worktree route is an ordinary managed route whose name is dotted (`feature-auth.myapp`) and whose `Parent` field names the app. The routing fast path is unchanged (dotted names are exact RouteTable map hits); TLS SANs and DNS already handle nested hosts. New behavior lives at the edges: name validation, registration overrides (port/oauth/reserve), a 307-to-parent for gone worktree hosts, touch-based dir-gone pruning, a worktree section on the start page, dashboard grouping, and CLI worktree detection.

**Tech Stack:** Go stdlib only (plus existing Cobra CLI). Tests: `go test` with the existing `testServer()` helper (`internal/daemon/test_helpers_test.go`), `httptest`, and real `git` for CLI detection tests.

**Spec:** `docs/superpowers/specs/2026-07-20-worktree-routes-design.md`

---

## ⚠️ Preflight — read before Task 1

The working tree on `feature/worktree-routes` contains **unrelated in-flight changes** (repairpage/port-collision work): modified `internal/daemon/api.go`, `autostart.go`, `port_collision_test.go`, `repairpage.go`, `sync_config.go`, `templates/repairpage.html.tmpl`, and untracked `repairpage_test.go`. Three of those files are also modified by this plan.

**Do not start until the user has committed or stashed that work.** Every commit step below stages explicit file paths — never use `git add -A` or `git add .`. If the overlapping files still carry foreign changes when you reach a commit step, stop and ask the user.

Project rules that apply to every task: after code changes run `go build ./... && go vet ./... && go test ./...`; run `vibe dev` before any manual daemon check (the daemon runs a compiled binary). Line numbers below are as of branch creation — treat them as anchors, not gospel.

---

### Task 1: `Parent` field, name parsing, dir-gone helper, persistence

**Files:**
- Create: `internal/daemon/worktree.go`
- Create: `internal/daemon/worktree_test.go`
- Modify: `internal/daemon/routes.go` (Route struct, ~line 33)
- Modify: `internal/daemon/persistence.go` (`loadStickyRoutes`, ~lines 55-90)
- Modify: `internal/daemon/api.go` (`routeResponse` struct ~line 300, `toResponse` ~line 320)

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/worktree_test.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		{"UPPER", "", true},           // caller lowercases first; parse itself rejects
		{"-bad", "", true},            // validName violation
		{"wt.-bad", "", true},         // bad parent label
		{"bad-.app", "", true},        // bad worktree label
		{"a.b.c", "", true},           // deeper nesting rejected
		{"wt.local", "", true},        // 'local' reserved as parent
		{"local.app", "", true},       // 'local' reserved as worktree label
		{".app", "", true},            // empty label
		{"wt.", "", true},             // empty label
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

func TestWorktreeDirGone(t *testing.T) {
	dir := t.TempDir()
	wt := &Route{Name: "f.app", Parent: "app", Type: RouteManaged, Dir: dir}
	if worktreeDirGone(wt) {
		t.Errorf("dir exists; want gone=false")
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

// Persisted worktree routes reload with Parent derived from the dotted name;
// a worktree whose dir vanished while the daemon was down is dropped at load.
func TestPersistenceWorktreeRoundTripAndPrune(t *testing.T) {
	cfgDir := t.TempDir()
	liveDir := t.TempDir()
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run 'TestParseRouteName|TestWorktreeDirGone|TestPersistenceWorktree' -v`
Expected: compile FAIL — `undefined: parseRouteName`, `unknown field Parent`.

- [ ] **Step 3: Implement**

Create `internal/daemon/worktree.go`:

```go
package daemon

import (
	"fmt"
	"os"
	"strings"
)

// defaultWorktreeIdleMinutes is the idle_timeout applied to worktree routes
// that don't specify one. Agents abandon worktree servers far more often than
// humans abandon main, so they stop themselves instead of holding ports and
// CPU forever; the route survives the stop and the next visit auto-starts it.
const defaultWorktreeIdleMinutes = 60

// parseRouteName validates a route name and returns the parent app for
// worktree names. Single-label names ("myapp") return parent "". A dotted
// name must be exactly <worktree>.<app> with both labels validName-conforming
// and neither label "local"; deeper nesting is rejected.
func parseRouteName(name string) (parent string, err error) {
	parts := strings.Split(name, ".")
	switch len(parts) {
	case 1:
		if !validName.MatchString(name) {
			return "", fmt.Errorf("name must be lowercase letters, digits, or hyphens")
		}
		return "", nil
	case 2:
		for _, p := range parts {
			if !validName.MatchString(p) {
				return "", fmt.Errorf("each label of %q must be lowercase letters, digits, or hyphens", name)
			}
			if p == "local" {
				return "", fmt.Errorf("'local' is reserved for the dashboard")
			}
		}
		return parts[1], nil
	default:
		return "", fmt.Errorf("route names allow at most one dot: <worktree>.<app>")
	}
}

// worktreeParent returns the parent label of a two-label host name
// ("feature-auth.myapp" → "myapp") and whether the name has that shape.
// Purely syntactic — no table lookup.
func worktreeParent(name string) (string, bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// worktreeDirGone reports whether a worktree route's source directory has
// been deleted (git worktree remove, branch merged). Only meaningful for
// routes with a Parent. A stat error other than IsNotExist counts as "still
// there" so a transient FS hiccup can't deregister a live route.
func worktreeDirGone(r *Route) bool {
	if r.Parent == "" || r.Dir == "" {
		return false
	}
	_, err := os.Stat(r.Dir)
	return os.IsNotExist(err)
}
```

In `internal/daemon/routes.go`, add to the `Route` struct directly under the `Name` field:

```go
	// Parent is set on worktree routes: the app this route is a worktree of.
	// The name of a worktree route is always "<worktree>.<Parent>", so Parent
	// is derivable from Name; it's stored for cheap filtering (dashboard
	// grouping, dir-gone pruning, picker) without re-parsing.
	Parent string `json:"parent,omitempty"`
```

In `internal/daemon/persistence.go`, replace the name-validation lines in `loadStickyRoutes` (currently `lower := strings.ToLower(name)` / `if !validName.MatchString(lower) || lower == "local" { continue }`) with:

```go
		lower := strings.ToLower(name)
		parent, perr := parseRouteName(lower)
		if perr != nil || lower == "local" {
			continue
		}
		// A worktree whose source dir vanished while the daemon was down is
		// dead — drop it at load; the next save rewrites routes.json without it.
		if parent != "" && entry.Dir != "" {
			if _, statErr := os.Stat(entry.Dir); os.IsNotExist(statErr) {
				continue
			}
		}
```

and add `Parent: parent,` to the `&Route{...}` literal in the same loop. (`os` and `strings` are already imported; if `strings` is now unused, remove the import.) `saveStickyRoutes` needs no change — Parent is re-derived at load, so `routes.json`'s schema is untouched.

In `internal/daemon/api.go`: add `Parent string \`json:"parent,omitempty"\`` to the `routeResponse` struct (~line 300) and `Parent: r.Parent,` in the `toResponse` literal (~line 320).

Note (no code needed): `tlsHostnames` (`daemon.go:233`) iterates all route names, so `feature-auth.myapp.vibe` lands in the cert SANs automatically, and both DNS resolvers already match any-depth `*.vibe` hosts. Zero cert/DNS work is intentional.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run 'TestParseRouteName|TestWorktreeDirGone|TestPersistenceWorktree' -v` then `go build ./... && go vet ./... && go test ./...`
Expected: PASS, full suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/worktree.go internal/daemon/worktree_test.go internal/daemon/routes.go internal/daemon/persistence.go internal/daemon/api.go
git commit -m "daemon: worktree route model — Parent field, dotted-name parsing, load-time dir prune"
```

---

### Task 2: Register worktree routes (port/oauth/reserve overrides, idle default)

**Files:**
- Modify: `internal/daemon/api.go` (`handleRegister` ~lines 428-598, `reservePortsClaim` ~line 137)
- Create: `internal/daemon/worktree_api_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/worktree_api_test.go` (build-tagged like `autostart_test.go` because success paths spawn a real child):

```go
//go:build !windows

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postRegister(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/routes", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRegister(w, req)
	return w
}

func TestRegisterWorktreeRejectsBadShapes(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()
	cases := []struct {
		body string
		want int
	}{
		{`{"name":"wt.app","url":"https://example.com"}`, http.StatusBadRequest},   // bookmark
		{`{"name":"wt.app","port":3000}`, http.StatusBadRequest},                    // no cmd
		{`{"name":"wt.app","cmd":"sleep 9","oauth_callback_port":8123}`, http.StatusBadRequest},
		{`{"name":"a.b.c","cmd":"sleep 9"}`, http.StatusBadRequest},                 // nesting
		{`{"name":"wt.local","cmd":"sleep 9"}`, http.StatusBadRequest},              // reserved
	}
	for _, c := range cases {
		if w := postRegister(t, s, c.body); w.Code != c.want {
			t.Errorf("register %s = %d; want %d (body: %s)", c.body, w.Code, c.want, w.Body.String())
		}
	}
}

func TestRegisterWorktreeOverrides(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	// Port 3200 is what the copied vibe.json would claim; it must be ignored.
	// reserve_ports keeps its name but must get a fresh value.
	w := postRegister(t, s, `{"name":"feat.app","cmd":"sleep 30","port":3200,"dir":"`+t.TempDir()+`","reserve_ports":{"server":3201}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("register = %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feat.app") })

	var resp struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	route, ok := s.table.Get("feat.app")
	if !ok {
		t.Fatal("route missing after register")
	}
	if route.Parent != "app" {
		t.Errorf("Parent = %q; want \"app\"", route.Parent)
	}
	if route.Port == 3200 || route.Port == 0 {
		t.Errorf("Port = %d; want a fresh auto-assigned port, not the vibe.json value", route.Port)
	}
	if resp.Port != route.Port {
		t.Errorf("response port %d != route port %d", resp.Port, route.Port)
	}
	if !strings.Contains(resp.URL, "feat.app.test") {
		t.Errorf("URL = %q; want host feat.app.test", resp.URL)
	}
	if p := route.ReservePorts["server"]; p == 3201 || p == 0 {
		t.Errorf("reserve_ports[server] = %d; want fresh non-zero value", p)
	}
	if route.IdleTimeout != defaultWorktreeIdleMinutes {
		t.Errorf("IdleTimeout = %d; want default %d", route.IdleTimeout, defaultWorktreeIdleMinutes)
	}

	// An explicit idle_timeout wins over the worktree default.
	w2 := postRegister(t, s, `{"name":"feat2.app","cmd":"sleep 30","idle_timeout":5}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("register feat2 = %d: %s", w2.Code, w2.Body.String())
	}
	t.Cleanup(func() { _ = s.procs.Stop("feat2.app") })
	r2, _ := s.table.Get("feat2.app")
	if r2.IdleTimeout != 5 {
		t.Errorf("explicit IdleTimeout = %d; want 5", r2.IdleTimeout)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run TestRegisterWorktree -v`
Expected: FAIL — dotted names currently rejected with "name must be lowercase letters, digits, or hyphens".

- [ ] **Step 3: Implement in `handleRegister`**

**(a)** Replace the name-validation block (currently `req.Name = strings.ToLower(req.Name)` through the `'local' is reserved` early-return, ~lines 442-450) with:

```go
	req.Name = strings.ToLower(req.Name)
	parent, err := parseRouteName(req.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "local" {
		writeJSONError(w, http.StatusBadRequest, "'local' is reserved for the dashboard")
		return
	}
	if parent != "" {
		// Worktree route: managed only, and the copied vibe.json's fixed
		// ports belong to the main checkout — honoring them is exactly the
		// clobbering this feature exists to prevent.
		if req.URL != "" {
			writeJSONError(w, http.StatusBadRequest, "worktree routes (<worktree>.<app>) cannot be bookmarks")
			return
		}
		if req.Cmd == "" {
			writeJSONError(w, http.StatusBadRequest, "worktree routes (<worktree>.<app>) require a cmd")
			return
		}
		if req.OAuthCallbackPort != nil && *req.OAuthCallbackPort > 0 {
			writeJSONError(w, http.StatusBadRequest, "oauth_callback_port is not supported on worktree routes — a fixed localhost port can't be shared across worktrees")
			return
		}
		req.Port = 0 // always auto-assign
	}
```

**(b)** Directly after the existing `reservePorts, err := validateReservePorts(...)` error check, add:

```go
	if parent != "" {
		// Keep the names (the cmd references $PORT_<NAME>), zero the values;
		// fresh ports are assigned once the route is in the table below.
		for k := range reservePorts {
			reservePorts[k] = 0
		}
	}
```

**(c)** In `reservePortsClaim` (~line 137), skip zeroed placeholders — add as the first line of the loop body:

```go
		if kv.Port == 0 {
			continue
		}
```

**(d)** In the `route := &Route{...}` literal, add `Parent: parent,`. After the `if req.IdleTimeout != nil { ... }` block add:

```go
	if parent != "" && req.IdleTimeout == nil {
		route.IdleTimeout = defaultWorktreeIdleMinutes
	}
```

(Both must happen before `s.table.Add(route)` so the add-hook arms the idle timer.)

**(e)** Inside the `if route.Type == RouteManaged {` block, immediately after the primary-port auto-assign `if route.Port == 0 { ... }` and before the reserve-port preflight loop, add:

```go
		// Assign fresh reserve-port values for worktree routes. The route is
		// already in the table, so each findFreePort call sees the values
		// assigned so far and can't hand out a duplicate.
		if route.Parent != "" {
			for _, kv := range reservePortValuesSorted(route.ReservePorts) {
				if kv.Port != 0 {
					continue
				}
				p, err := findFreePort(s.table)
				if err != nil {
					s.table.Remove(route.Name)
					writeJSONError(w, http.StatusInternalServerError, "could not find a free reserve port")
					return
				}
				route.ReservePorts[kv.Name] = p
			}
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestRegisterWorktree -v` then the full `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/api.go internal/daemon/worktree_api_test.go
git commit -m "daemon: register worktree routes — auto port, fresh reserve ports, no oauth bridge, 60m idle default"
```

---

### Task 3: 307-to-parent for gone worktree hosts

**Files:**
- Modify: `internal/daemon/daemon.go` (`routeRequest` tail, ~line 384)
- Modify: `internal/daemon/worktree_test.go`

- [ ] **Step 1: Write the failing test** (append to `worktree_test.go`; no build tag needed — nothing spawns)

```go
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
```

Add `"net/http"`, `"net/http/httptest"` to `worktree_test.go` imports.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestGoneWorktreeHost -v`
Expected: FAIL — currently 200 (dashboard with unknown banner) for all three.

- [ ] **Step 3: Implement**

In `internal/daemon/daemon.go`, add these methods near `routeRequest`:

```go
// parentKnown reports whether an app name is known to the daemon either as a
// registered route or as the Parent of at least one worktree route — parents
// are plain strings, not necessarily routes themselves.
func (s *Server) parentKnown(app string) bool {
	if _, ok := s.table.Get(app); ok {
		return true
	}
	for _, r := range s.table.List() {
		if r.Parent == app {
			return true
		}
	}
	return false
}

// redirectToParent sends a request for a dead or unknown worktree host to its
// parent app. 307, never 301 — a permanent redirect would be cached by the
// browser and poison a future worktree that reuses the same branch name.
func (s *Server) redirectToParent(w http.ResponseWriter, r *http.Request, parent string) {
	http.Redirect(w, r, fmt.Sprintf("%s://%s.%s/", s.vibeScheme(), parent, s.cfg.Daemon.TLD), http.StatusTemporaryRedirect)
}
```

In `routeRequest`, inside the `if strings.HasSuffix(host, "."+s.cfg.Daemon.TLD) {` block, right after the closing brace of `if route, ok := s.table.Get(name); ok { ... }` (i.e. the name was NOT found), add:

```go
		// A worktree host whose route is missing — never registered, or just
		// pruned because its dir vanished — goes to its parent app instead of
		// dead-ending on the "unknown route" dashboard.
		if parent, ok := worktreeParent(name); ok && s.parentKnown(parent) {
			s.redirectToParent(w, r, parent)
			return
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestGoneWorktreeHost -v` then full suite.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/worktree_test.go
git commit -m "daemon: 307 gone worktree hosts to their parent app"
```

---

### Task 4: Touch-time dir-gone prune (request + explicit start)

**Files:**
- Modify: `internal/daemon/autostart.go` (`recoverManagedRoute`, top of function)
- Modify: `internal/daemon/api.go` (`handleStart` — find with `grep -n "func (s \*Server) handleStart" internal/daemon/api.go`)
- Modify: `internal/daemon/worktree_test.go`

- [ ] **Step 1: Write the failing test** (append to `worktree_test.go`)

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestRecoverManagedRoutePrunes -v`
Expected: FAIL — today this serves the start page (200) and the route survives.

- [ ] **Step 3: Implement**

At the very top of `recoverManagedRoute` in `internal/daemon/autostart.go` (before the stale-PID normalization), add:

```go
	// A worktree whose source dir vanished (git worktree remove, merged
	// branch) is dead — deregister it and send the visitor to the parent app
	// in the same response.
	if worktreeDirGone(route) {
		_ = s.procs.Stop(route.Name)
		s.table.Remove(route.Name)
		if err := s.saveStickyRoutes(); err != nil {
			fmt.Fprintf(os.Stderr, "vibe: failed to persist worktree prune of %s: %v\n", route.Name, err)
		}
		s.redirectToParent(w, r, route.Parent)
		return true
	}
```

In `handleStart` in `internal/daemon/api.go`, right after the route lookup succeeds (and before any preflight/spawn logic), add:

```go
	if worktreeDirGone(route) {
		s.table.Remove(route.Name)
		_ = s.saveStickyRoutes()
		writeJSONError(w, http.StatusGone, fmt.Sprintf("worktree directory %s is gone — route removed", route.Dir))
		return
	}
```

(If `handleStart`'s route variable has a different name, adapt; keep the semantics.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestRecoverManagedRoutePrunes -v` then full suite.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/autostart.go internal/daemon/api.go internal/daemon/worktree_test.go
git commit -m "daemon: prune worktree routes whose dir vanished, on request and on start"
```

---

### Task 5: vibe.json sync for worktrees — cmd only

**Files:**
- Modify: `internal/daemon/sync_config.go` (`syncRouteFromVibeJSON`)
- Modify: `internal/daemon/worktree_test.go`

Why: on every Start the daemon re-reads `vibe.json` from `route.Dir`. A worktree's copy names the *parent* app and carries the parent's fixed ports; re-importing them would clobber the worktree overrides from Task 2. Today the `cfg.Name != route.Name` guard makes the whole sync a silent no-op for worktrees — meaning `cmd` edits in the worktree would never take effect either. Sync `cmd` only.

- [ ] **Step 1: Write the failing test** (append to `worktree_test.go`)

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestSyncWorktreeRoute -v`
Expected: FAIL — `Cmd` stays `"npm run dev"` (the name-mismatch guard no-ops the sync).

- [ ] **Step 3: Implement**

In `syncRouteFromVibeJSON`, insert after the `json.Unmarshal` error handling and **before** the existing `if cfg.Name != "" && cfg.Name != route.Name` guard:

```go
	if route.Parent != "" {
		// Worktree route: the file is a copy of the parent's vibe.json, so
		// its name identifies the app, and its port/oauth/reserve values are
		// the parent's — never re-import them over the worktree-local
		// assignments. Only cmd edits sync.
		if cfg.Name != "" && cfg.Name != route.Parent {
			return nil
		}
		if cfg.Cmd == "" || cfg.Cmd == route.Cmd {
			return nil
		}
		if !s.table.UpdateManagedConfig(route.Name, cfg.Cmd, route.OAuthCallbackPort, route.ReservePorts) {
			return nil
		}
		return s.saveStickyRoutes()
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestSyncWorktreeRoute -v` then full suite.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/sync_config.go internal/daemon/worktree_test.go
git commit -m "daemon: worktree vibe.json sync imports cmd only, never the parent's ports"
```

---

### Task 6: Start page becomes the picker

**Files:**
- Modify: `internal/daemon/startpage.go`
- Modify: `internal/daemon/templates/startpage.html.tmpl` (card body ~line 63-68, CSS block ~line 10+)
- Modify: `internal/daemon/worktree_test.go`

- [ ] **Step 1: Write the failing test** (append to `worktree_test.go`)

```go
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

	// A route with no children renders no worktree section.
	lone := &Route{Name: "solo", Type: RouteManaged, Port: 3602, Cmd: "x", RegisteredAt: time.Now()}
	s.table.Add(lone)
	w2 := httptest.NewRecorder()
	s.serveStartPage(w2, req, lone)
	if strings.Contains(w2.Body.String(), "wt-list") {
		t.Errorf("childless start page rendered a worktree section")
	}
}
```

Add `"strings"` to `worktree_test.go` imports.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestStartPageListsWorktrees -v`
Expected: FAIL — no worktree markup in the rendered page.

- [ ] **Step 3: Implement**

In `internal/daemon/startpage.go`, add `"strings"` to imports, then:

```go
// startPageWorktree is one row in the start page's worktree picker section.
type startPageWorktree struct {
	Name    string // subdomain label only, e.g. "feature-auth"
	URL     string
	Running bool
}
```

Add `Worktrees []startPageWorktree` to `startPageData`, and in `serveStartPage` before the `data := startPageData{...}` literal:

```go
	// The start page doubles as the worktree picker: when the app itself is
	// stopped, its running worktrees are one click away. List() is
	// name-sorted, so siblings render in stable order.
	var worktrees []startPageWorktree
	for _, rt := range s.table.List() {
		if rt.Parent == route.Name {
			worktrees = append(worktrees, startPageWorktree{
				Name:    strings.TrimSuffix(rt.Name, "."+route.Name),
				URL:     fmt.Sprintf("%s://%s.%s/", s.vibeScheme(), rt.Name, tld),
				Running: rt.Running.Load(),
			})
		}
	}
```

then `Worktrees: worktrees,` in the literal.

In `templates/startpage.html.tmpl`, immediately after the Start button line (`<button class="start-btn" id="btn" ...>`, ~line 68) — keeping it inside `<div class="card">` — add:

```html
{{if .Worktrees}}
<div class="wt-list">
  <div class="wt-list-title">Worktrees</div>
  {{range .Worktrees}}
  <a class="wt-item" href="{{.URL}}"><span class="wt-dot{{if .Running}} on{{end}}"></span>{{.Name}}</a>
  {{end}}
</div>
{{end}}
```

and in the `<style>` block:

```css
.wt-list{margin-top:24px;text-align:left;border-top:1px solid var(--border);padding-top:16px}
.wt-list-title{font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;color:var(--text-muted);margin-bottom:8px}
.wt-item{display:flex;align-items:center;gap:8px;padding:8px 10px;border-radius:var(--radius);color:var(--text-secondary);text-decoration:none;font-size:.85rem}
.wt-item:hover{background:var(--bg);color:var(--text)}
.wt-dot{width:7px;height:7px;border-radius:50%;background:var(--text-muted);opacity:.4;flex-shrink:0}
.wt-dot.on{background:#4ade80;opacity:1;box-shadow:0 0 6px rgba(74,222,128,.5)}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestStartPageListsWorktrees -v` then full suite.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/startpage.go internal/daemon/templates/startpage.html.tmpl internal/daemon/worktree_test.go
git commit -m "daemon: start page lists running/stopped worktrees — the picker"
```

---

### Task 7: Dashboard grouping

**Files:**
- Modify: `internal/daemon/dashboard.go`
- Modify: `internal/daemon/templates/dashboard.html.tmpl` (list-view `{{range .Routes}}` ~line 211; style block)
- Modify: `internal/daemon/worktree_test.go`

- [ ] **Step 1: Write the failing test** (append to `worktree_test.go`)

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestDashboardGroups -v`
Expected: FAIL (ordering and missing classes).

- [ ] **Step 3: Implement**

In `internal/daemon/dashboard.go`: add `"sort"` to imports; add to `dashboardRoute`:

```go
	Parent      string // worktree routes: the app this belongs to
	IsWorktree  bool
	GroupHeader string // non-empty on the first row of a parent-less worktree group
```

In `serveDashboard`'s route loop, when building each `dashboardRoute`, add `Parent: rt.Parent,` and `IsWorktree: rt.Parent != "",`. After the loop, before rendering:

```go
	// Group worktrees under their parent app: sort by group (the parent's
	// name, or the route's own name), parents before their worktrees, then
	// by name within each tier. Parent-less groups (the app was never
	// registered — Parent is just a string) get a header label on their
	// first row so the cluster is still visibly an app.
	groupKey := func(r dashboardRoute) string {
		if r.Parent != "" {
			return r.Parent
		}
		return r.Name
	}
	sort.SliceStable(data.Routes, func(i, j int) bool {
		gi, gj := groupKey(data.Routes[i]), groupKey(data.Routes[j])
		if gi != gj {
			return gi < gj
		}
		if data.Routes[i].IsWorktree != data.Routes[j].IsWorktree {
			return !data.Routes[i].IsWorktree
		}
		return data.Routes[i].Name < data.Routes[j].Name
	})
	registered := make(map[string]bool, len(data.Routes))
	for _, rt := range data.Routes {
		if !rt.IsWorktree {
			registered[rt.Name] = true
		}
	}
	prevGroup := ""
	for i := range data.Routes {
		rt := &data.Routes[i]
		if rt.IsWorktree && !registered[rt.Parent] && groupKey(*rt) != prevGroup {
			rt.GroupHeader = rt.Parent
		}
		prevGroup = groupKey(*rt)
	}
```

In `templates/dashboard.html.tmpl`, in the **list view** loop (~line 211), change:

```html
{{range .Routes}}
<tr>
```

to:

```html
{{range .Routes}}
{{if .GroupHeader}}<tr class="wt-group-header"><td colspan="6">{{.GroupHeader}}</td></tr>{{end}}
<tr{{if .IsWorktree}} class="wt-tr"{{end}}>
```

(Count the `<th>` columns in the table header and set `colspan` to match — 6 is a guess.) Add to the style block:

```css
tr.wt-tr td:first-child .route-name-cell{padding-left:26px}
tr.wt-group-header td{padding:10px 4px 2px 8px;font-size:.72rem;text-transform:uppercase;letter-spacing:.06em;color:var(--text-muted);border-bottom:none}
```

The grid view shares the sorted `data.Routes`, so tiles cluster by app without further changes — leave it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -run TestDashboardGroups -v` then full suite.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/dashboard.go internal/daemon/templates/dashboard.html.tmpl internal/daemon/worktree_test.go
git commit -m "daemon: dashboard groups worktree routes under their parent app"
```

---

### Task 8: CLI — worktree detection, slug, `vibe start` wiring, `--as`

**Files:**
- Create: `cmd/worktree.go`
- Create: `cmd/worktree_test.go`
- Modify: `cmd/start.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/worktree_test.go`:

```go
package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"feature/auth":        "feature-auth",
		"Feature/OAuth_2.0":   "feature-oauth-2-0",
		"bugfix-123":          "bugfix-123",
		"--weird--":           "weird",
		"///":                 "",
		"UPPER":               "upper",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestDetectWorktreeAndSlug(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(main, "init")
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", ".")
	git(main, "commit", "-m", "init")

	wtDir := filepath.Join(t.TempDir(), "myapp-feature-auth")
	git(main, "worktree", "add", "-b", "feature/auth", wtDir)

	if detectWorktree(main) {
		t.Errorf("main checkout detected as worktree")
	}
	if !detectWorktree(wtDir) {
		t.Errorf("linked worktree not detected")
	}
	if got := worktreeSlug(wtDir); got != "feature-auth" {
		t.Errorf("worktreeSlug = %q; want feature-auth", got)
	}
	if detectWorktree(t.TempDir()) {
		t.Errorf("non-repo dir detected as worktree")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./cmd/ -run 'TestSlugify|TestDetectWorktree' -v`
Expected: compile FAIL — `undefined: slugify` etc.

- [ ] **Step 3: Implement `cmd/worktree.go`**

```go
package cmd

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// detectWorktree reports whether dir is inside a *linked* git worktree (not
// the main checkout). In a linked worktree, --git-dir resolves under the main
// repo's .git/worktrees/<name> and therefore differs from --git-common-dir.
// Any git failure (not a repo, git missing) reads as "not a worktree".
func detectWorktree(dir string) bool {
	gitDir, err1 := gitOut(dir, "rev-parse", "--git-dir")
	commonDir, err2 := gitOut(dir, "rev-parse", "--git-common-dir")
	if err1 != nil || err2 != nil {
		return false
	}
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return filepath.Clean(p)
	}
	return abs(gitDir) != abs(commonDir)
}

// worktreeSlug derives the subdomain label for a worktree route: the current
// branch name slugified, falling back to the directory basename (detached
// HEAD, or a branch that slugs to nothing).
func worktreeSlug(dir string) string {
	if branch, err := gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && branch != "" && branch != "HEAD" {
		if s := slugify(branch); s != "" {
			return s
		}
	}
	return slugify(filepath.Base(dir))
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns an arbitrary branch or directory name into a
// validName-conforming label: lowercase, illegal runs collapse to a single
// hyphen, leading/trailing hyphens trimmed. May return "".
func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugStrip.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run 'TestSlugify|TestDetectWorktree' -v`
Expected: PASS.

- [ ] **Step 5: Wire into `vibe start`**

In `cmd/start.go`:

**(a)** Add the flag. Above `func init()`:

```go
// worktreeAs overrides the auto-derived worktree subdomain (vibe start --as).
var worktreeAs string
```

and inside `init()` before `rootCmd.AddCommand(startCmd)`:

```go
	startCmd.Flags().StringVar(&worktreeAs, "as", "", "worktree subdomain override, e.g. --as feature-auth (worktrees only)")
```

**(b)** In `startFromConfig`, replace the final `return startNew(cfg.Name, cfg.Port, cfg.Cmd, cfg.OAuthCallbackPort, cfg.ReservePorts)` with:

```go
	name, port, oauthPort := cfg.Name, cfg.Port, cfg.OAuthCallbackPort
	if detectWorktree(dir) {
		slug := worktreeAs
		if slug == "" {
			slug = worktreeSlug(dir)
		}
		if slug == "" {
			return fmt.Errorf("could not derive a worktree name from the branch or directory — use: vibe start --as <name>")
		}
		name = slug + "." + cfg.Name
		// The copied vibe.json's fixed ports belong to the main checkout:
		// the daemon auto-assigns the primary port (and fresh reserve_ports
		// values); the oauth bridge can't be shared, so it's dropped here.
		port = 0
		if oauthPort > 0 {
			fmt.Println("note: oauth_callback_port is ignored for worktree routes")
			oauthPort = 0
		}
		fmt.Printf("worktree of %s (branch %s) → %s\n", cfg.Name, worktreeSlug(dir), name)
	}
	return startNew(name, port, cfg.Cmd, oauthPort, cfg.ReservePorts)
```

**(c)** In the `startCmd` `Long` help text, after the `vibe.json fields` block, add:

```
Inside a git worktree, 'vibe start' registers <branch-slug>.<name>.vibe on an
auto-assigned port instead of <name>.vibe, so worktrees never clobber the main
checkout or each other. Override the subdomain with --as <name>.
```

- [ ] **Step 6: Build + full tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git add cmd/worktree.go cmd/worktree_test.go cmd/start.go
git commit -m "cli: vibe start auto-detects git worktrees, registers <slug>.<app>.vibe"
```

---

### Task 9: Docs — SKILL.md (+ marketplace sync), setup.md, CLAUDE.md

**Files:**
- Modify: `internal/vibeskill/SKILL.md`
- Modify: `plugins/local-vibe/skills/local-vibe/SKILL.md` (via `cp`, enforced by `marketplace_test.go`)
- Modify: `internal/daemon/setup_md.go`
- Modify: `CLAUDE.md`

- [ ] **Step 1: SKILL.md**

In `internal/vibeskill/SKILL.md`, add near the `vibe start` guidance (match the file's existing bullet style):

```markdown
- **Git worktrees:** run `vibe start` inside a linked worktree and it registers
  `https://<branch-slug>.<app>.vibe` on its own auto-assigned port — the main
  checkout's `<app>.vibe` and other worktrees are untouched. Override the
  subdomain with `vibe start --as <name>`. Deleting the worktree cleans up the
  route automatically.
```

Then sync the marketplace copy:

```bash
cp internal/vibeskill/SKILL.md plugins/local-vibe/skills/local-vibe/SKILL.md
```

- [ ] **Step 2: setup.md**

In `internal/daemon/setup_md.go`, find the managed-routes / `vibe start` section and add a matching paragraph:

```markdown
### Git worktrees

Run `vibe start` inside a linked git worktree and vibe registers
`<branch-slug>.<app>.vibe` (e.g. `feature-auth.myapp.vibe`) on its own
auto-assigned port instead of `<app>.vibe` — worktrees never clobber the main
checkout or each other. `--as <name>` overrides the subdomain. Worktree routes
auto-stop after 60 idle minutes (the next visit restarts them) and are removed
automatically once the worktree directory is deleted; visiting a removed
worktree's URL redirects to the parent app. When the main app is stopped, its
start page lists the running worktrees.
```

- [ ] **Step 3: CLAUDE.md**

In the **Route types** section's `managed` bullet, append:

```
Inside a git worktree, `vibe start` auto-registers `<branch-slug>.<app>` (dotted name, `Parent` field set) on a fresh port — see the worktree-routes pattern below.
```

Add to **Key patterns**:

```markdown
- **Worktree routes:** a managed route named `<worktree>.<app>` with `Parent` set (`internal/daemon/worktree.go`). `vibe start` in a linked worktree detects it (`cmd/worktree.go`, `--git-dir` ≠ `--git-common-dir`), slugs the branch name, and registers the dotted route; the daemon forces port auto-assign, reassigns `reserve_ports` values, rejects `oauth_callback_port`, and defaults `idle_timeout` to 60m. Routing is the normal map hit (dotted names are ordinary keys); a missing worktree host whose parent is known 307s to `<app>.vibe` (never 301 — reused branch names). Dir-gone pruning is touch-based: load, request-recovery, and start paths stat `Dir` and deregister. `vibe.json` sync for worktrees imports `cmd` only. The start page doubles as the picker (lists sibling worktrees); the dashboard groups them under the parent. TLS SANs and DNS needed zero changes.
```

- [ ] **Step 4: Verify the marketplace sync test**

Run: `go test ./... -run TestMarketplace -v` then the full suite.
Expected: PASS (the drift test is the reason for the `cp`).

- [ ] **Step 5: Commit**

```bash
git add internal/vibeskill/SKILL.md plugins/local-vibe/skills/local-vibe/SKILL.md internal/daemon/setup_md.go CLAUDE.md
git commit -m "docs: worktree routes — skill, setup.md, CLAUDE.md"
```

---

### Task 10: End-to-end verification

**Files:** none (verification only)

- [ ] **Step 1: Full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all green. Investigate any failure before proceeding (project rule).

- [ ] **Step 2: Rebuild + restart the daemon**

Run: `vibe dev`
Expected: build, install, daemon restart, readiness confirmation.

- [ ] **Step 3: Live smoke test with a real worktree**

```bash
# Scratch app with a vibe.json (uses $PORT, so auto-assign works)
SCRATCH=$(mktemp -d)/wtdemo && mkdir -p "$SCRATCH" && cd "$SCRATCH"
git init -q -b main
printf '{"name":"wtdemo","cmd":"python3 -m http.server $PORT"}\n' > vibe.json
git add . && git -c user.name=t -c user.email=t@t commit -qm init
vibe start                      # → https://wtdemo.vibe

# Worktree
git worktree add -b feature/picker ../wtdemo-picker
cd ../wtdemo-picker
vibe start                      # expect: worktree of wtdemo → feature-picker.wtdemo.vibe, distinct port

curl -sk https://feature-picker.wtdemo.vibe/ -o /dev/null -w '%{http_code}\n'   # expect 200
curl -sk https://wtdemo.vibe/ -o /dev/null -w '%{http_code}\n'                  # expect 200 (main untouched)
vibe list                        # both routes, distinct ports

# Picker: stop main, load wtdemo.vibe → start page should list feature-picker
vibe stop wtdemo
curl -sk https://wtdemo.vibe/ | grep -o feature-picker | head -1               # expect feature-picker

# Gone-worktree redirect
vibe stop feature-picker.wtdemo
cd "$SCRATCH" && git worktree remove --force ../wtdemo-picker
curl -sk -o /dev/null -w '%{http_code} %{redirect_url}\n' https://feature-picker.wtdemo.vibe/
# expect: 307 https://wtdemo.vibe/

# Cleanup
vibe stop wtdemo 2>/dev/null; curl -s -X DELETE http://localhost:7999/_api/routes/wtdemo >/dev/null
```

Expected outputs as annotated. Also open `https://local.vibe` and eyeball the grouped dashboard while both routes exist.

- [ ] **Step 4: Report**

Summarize results to the user, including any deviation observed. Do not push; do not merge — integration is the user's call (see superpowers:finishing-a-development-branch).

---

## Self-review notes (already applied)

- **Spec coverage:** naming/routing §1 → Tasks 1-3; registration §2 → Tasks 2, 8; picker/dashboard §3 → Tasks 6-7; lifecycle §4 → Tasks 1 (load), 2 (idle default), 4 (touch prune + 307); certs/DNS §5 → no-op by design (noted in Task 1); docs/tests §6-7 → Tasks 9-10.
- **Known deviations, all intentional:** dashboard rename of a worktree route via the edit modal stays rejected (single-label `validName` in `handleUpdate`) — acceptable; grid view clusters but has no indent/header treatment; `worktreeParent` (syntactic split) is distinct from `parseRouteName` (validating) — don't merge them, the redirect path must fire even for names that would fail validation.
- **Type consistency:** `parseRouteName(name) (string, error)`; `worktreeParent(name) (string, bool)`; `worktreeDirGone(*Route) bool`; `defaultWorktreeIdleMinutes = 60`; `Route.Parent string`; `startPageWorktree{Name,URL,Running}`; `dashboardRoute.{Parent,IsWorktree,GroupHeader}`; `(*Server).parentKnown/redirectToParent`.
