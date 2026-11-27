package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/peer"
)

// TestDashboardRedirectBanner verifies the broken-redirect warning renders only
// when RedirectDown is set, and surfaces the fix command.
func TestDashboardRedirectBanner(t *testing.T) {
	render := func(down bool) string {
		var b strings.Builder
		data := dashboardData{TLD: "vibe", RedirectDown: down}
		if err := tmplDashboard.Execute(&b, data); err != nil {
			t.Fatalf("render: %v", err)
		}
		return b.String()
	}

	up := render(false)
	if strings.Contains(up, "HTTPS redirect is down") {
		t.Error("banner shown when redirect is healthy")
	}

	down := render(true)
	if !strings.Contains(down, "HTTPS redirect is down") {
		t.Error("banner missing when redirect is down")
	}
	if !strings.Contains(down, "vibe doctor --fix") {
		t.Error("banner does not surface the fix command")
	}
}

func TestDashboardShowsPeerRoutesReadOnly(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{{Name: "imac", Host: "127.0.0.1", Port: 7444, Fingerprint: "aa", AddedAt: time.Now()}}
	s.peerStates["imac"] = &peerState{lastOK: time.Now(), routes: []peer.RouteSummary{
		{Name: "face", Type: "managed", Running: true, Ready: true},
		{Name: "clash", Type: "sticky", Running: true, Ready: true},
	}}
	s.table.Add(&Route{Name: "clash", Type: RouteSticky, Port: 4000, RegisteredAt: time.Now()})

	req := httptest.NewRequest("GET", "http://local.vibe/", nil)
	req.Host = "local.vibe"
	rec := httptest.NewRecorder()
	s.serveDashboard(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "imac") {
		t.Fatal("peer group header missing")
	}
	if !strings.Contains(body, `href="http://face.vibe"`) && !strings.Contains(body, `href="https://face.vibe"`) {
		t.Fatalf("peer route face must link to its .vibe URL")
	}
	if !strings.Contains(body, "shadowed by") {
		t.Fatal("shadowed peer route not badged")
	}
	// The shadowed clash row must not be a link to clash.vibe inside the
	// peers section — it renders as plain text. The local clash row's link
	// exists in both views — the list row's name cell and the grid tile are
	// each an anchor — so count: exactly two clash.vibe links, neither of
	// them in the peers section.
	if n := strings.Count(body, `href="http://clash.vibe"`) + strings.Count(body, `href="https://clash.vibe"`); n != 2 {
		t.Fatalf("want exactly 2 clash.vibe links (local route: list row + grid tile), got %d", n)
	}
	// No start/stop controls target peer routes.
	if strings.Contains(body, `routeAction(this,'face'`) {
		t.Fatal("peer route has a start/stop control")
	}
}

// peerRowStatuses renders the dashboard and returns each peer list row's
// status word, keyed by route name.
func peerRowStatuses(t *testing.T, s *Server) (string, map[string]string) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://local.vibe/", nil)
	req.Host = "local.vibe"
	rec := httptest.NewRecorder()
	s.serveDashboard(rec, req)
	body := rec.Body.String()
	list := body[strings.Index(body, `id="peer-list"`):strings.Index(body, `id="peer-grid"`)]
	got := map[string]string{}
	for _, row := range strings.Split(list, "<tr>")[1:] {
		name := between(row, `<span class="route-name-link"`, "</span>")
		name = name[strings.Index(name, ">")+1:]
		status := between(row, `<span class="peer-status `, "</span>")
		got[name] = status[strings.Index(status, ">")+1:]
	}
	return body, got
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

// Every unshadowed peer row says whether the route runs on the peer. A
// shadowed row says only that it lost: its peer-side state is irrelevant (the
// name resolves locally) and "stopped" beside a running local route read as
// that route being down. A stopped route must not read as "starting" (the old
// yellow dot did).
func TestDashboardPeerRouteStatus(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{{Name: "imac", Host: "127.0.0.1", Port: 7444, Fingerprint: "aa", AddedAt: time.Now()}}
	s.peerStates["imac"] = &peerState{lastOK: time.Now(), routes: []peer.RouteSummary{
		{Name: "up", Type: "managed", Running: true, Ready: true},
		{Name: "booting", Type: "managed", Running: true},
		{Name: "down", Type: "managed"},
		{Name: "tasks", Type: "managed", Running: true, Ready: true},
	}}
	s.table.Add(&Route{Name: "tasks", Type: RouteManaged, Port: 4000, RegisteredAt: time.Now()})

	body, got := peerRowStatuses(t, s)
	want := map[string]string{"up": "ready", "booting": "starting", "down": "stopped", "tasks": ""}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("peer row %q status = %q, want %q (all: %v)", name, got[name], w, got)
		}
	}
	if !strings.Contains(body, "shadowed by this machine") {
		t.Error("local tasks route must shadow the peer's tasks, with a badge")
	}
	list := body[strings.Index(body, `id="peer-list"`):strings.Index(body, `id="peer-grid"`)]
	downRow := list[strings.Index(list, `>down<`)-400 : strings.Index(list, `>down<`)]
	if !strings.Contains(downRow, "led-gray") || strings.Contains(downRow, "led-yellow") {
		t.Errorf("stopped peer route should have a gray dot: %q", downRow)
	}
}

// A peer whose last good sync is stale has flags we can no longer vouch
// for: its rows read "unreachable", not their cached running state.
func TestDashboardPeerRouteStatusStalePeer(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{{Name: "imac", Host: "127.0.0.1", Port: 7444, Fingerprint: "aa", AddedAt: time.Now()}}
	s.peerStates["imac"] = &peerState{
		lastOK:  time.Now().Add(-3 * peerSyncInterval),
		lastErr: "dial tcp: connection refused",
		routes:  []peer.RouteSummary{{Name: "up", Type: "managed", Running: true, Ready: true}},
	}
	_, got := peerRowStatuses(t, s)
	if got["up"] != "unreachable" {
		t.Errorf("stale peer row status = %q, want unreachable", got["up"])
	}
}

// With peers enabled, the dashboard proper always shows the Peers section
// with an "Add peer" control — even before anything is paired, which is when
// it's needed. A dashboard render on another Host (the unknown-route
// fallback) gets no admin control: the gate would refuse its POSTs anyway.
func TestDashboardAddPeerControl(t *testing.T) {
	render := func(s *Server, host string) string {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		s.serveDashboard(rec, req)
		return rec.Body.String()
	}

	s := newBareServer(t) // peers enabled, nothing paired
	body := render(s, "local.vibe")
	if !strings.Contains(body, `onclick="openPeerModal()"`) || !strings.Contains(body, `id="peer-overlay"`) {
		t.Fatal("peers enabled: dashboard has no Add peer control")
	}
	if !strings.Contains(body, "No machines paired yet") {
		t.Error("empty Peers section should say nothing is paired")
	}
	if other := render(s, "someapp.vibe"); strings.Contains(other, `onclick="openPeerModal()"`) {
		t.Error("Add peer control rendered on a non-dashboard host")
	}

	off := newBareServer(t)
	off.peersOn.Store(false)
	if body := render(off, "local.vibe"); strings.Contains(body, `onclick="openPeerModal()"`) || !strings.Contains(body, `onclick="openEnablePeers()"`) {
		t.Error("peers disabled: want the Enable peers control and no Add peer control")
	}
}

func renderDashboard(t *testing.T, s *Server, host string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+"/", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	s.serveDashboard(rec, req)
	return rec.Body.String()
}

// Peers off: the dashboard proper offers Enable peers with the basics; a
// route host gets no Peers section at all.
func TestPeersSectionDisabled(t *testing.T) {
	s := newDisabledServer(t)
	body := renderDashboard(t, s, "local.vibe")
	for _, want := range []string{
		`onclick="openEnablePeers()"`,
		"open another machine",
		"Nothing is reachable until you pair",
		"port 7444",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("disabled Peers section missing %q", want)
		}
	}
	if strings.Contains(renderDashboard(t, s, "someapp.vibe"), "openEnablePeers()\"") {
		t.Error("Enable control rendered on a route host")
	}
}

// Peers on: header actions, and every paired machine gets a group header
// with its address, status and Edit/Remove — including one that has never
// synced (a management view can't hide unreachable machines).
func TestPeersSectionEnabled(t *testing.T) {
	s := newBareServer(t)
	s.peerList = []peer.Peer{
		{Name: "box", Host: "10.0.0.9", Port: 7444, Fingerprint: "4f2a91c3aa"},
		{Name: "lonely", Host: "10.0.0.10", Port: 7444, Fingerprint: "bb"},
	}
	s.peerStates["box"] = &peerState{lastOK: time.Now(), routes: []peer.RouteSummary{{Name: "app", Ready: true, Running: true}}}
	body := renderDashboard(t, s, "local.vibe")
	for _, want := range []string{
		`onclick="openPeerModal()"`, `onclick="turnOffPeers()"`,
		`title="10.0.0.9:7444"`, `title="10.0.0.10:7444"`, "lonely",
		`openPeerEdit(`, `askRemovePeer(`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("enabled Peers section missing %q", want)
		}
	}
	// The header stays quiet: the address is a hover tooltip, the key lives
	// in the Edit dialog, and a status shows only when a peer is offline.
	list := body[strings.Index(body, `id="peer-list"`):strings.Index(body, `id="peer-grid"`)]
	for _, noise := range []string{">10.0.0.9:7444<", "4F2A\u00b791C3", ">connected<"} {
		if strings.Contains(list, noise) {
			t.Errorf("peer header shows %q; keep it to the name", noise)
		}
	}
	unreachable := newBareServer(t)
	unreachable.peerList = []peer.Peer{{Name: "box", Host: "10.0.0.9", Port: 7444, Fingerprint: "ff"}}
	unreachable.peerStates["box"] = &peerState{lastErr: "dial tcp: timeout"}
	if b := renderDashboard(t, unreachable, "local.vibe"); !strings.Contains(b, ">disconnected<") {
		t.Error("an offline peer must say disconnected")
	}
}

func TestInviteResponseCarriesKeyAndAddresses(t *testing.T) {
	s := newBareServer(t)
	t.Cleanup(func() { closePeerLn(s) })
	req := httptest.NewRequest(http.MethodPost, "/_api/peers/invite", nil)
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	var got struct {
		Code      string   `json:"code"`
		Key       string   `json:"key"`
		Name      string   `json:"name"`
		Addresses []string `json:"addresses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	if _, ok := peer.NormalizeCode(got.Code); !ok || got.Key == "" || got.Name != localPeerName() || got.Addresses == nil {
		t.Fatalf("invite response = %+v", got)
	}
}

// The Add-peer modal is a picker (LAN + Tailscale results and a typed
// address), the edit dialog shows the full fingerprint read-only, and the
// page never uses a browser confirm() (removal confirms inline).
func TestPeerModalMarkup(t *testing.T) {
	s := newBareServer(t)
	body := renderDashboard(t, s, "local.vibe")
	if strings.Contains(body, "confirm(") {
		t.Error("dashboard uses a browser confirm()")
	}
	// One Add peer screen: this machine's code on top (no key), a table of
	// found machines with one action column (Pair / Unpair), and in-dialog
	// views to enter the other machine's code and to confirm an unpair.
	modal := body[strings.Index(body, `id="peer-overlay"`):strings.Index(body, `id="peer-edit-overlay"`)]
	for _, want := range []string{
		`id="peer-code-value"`, "Your code", "Enter it on the other machine",
		`id="peer-devices"`, `id="peer-typed-host"`,
		`id="peer-view-list"`, `id="peer-view-code"`, `id="peer-entry-code"`, "Enter the code shown on",
		`id="peer-view-unpair"`, "Are you sure?",
	} {
		if !strings.Contains(modal, want) {
			t.Errorf("Add peer modal missing %q", want)
		}
	}
	for _, gone := range []string{`id="peer-code-overlay"`, `onclick="openShowCode()"`, `id="peer-code-key"`, "Their code", "says it"} {
		if strings.Contains(body, gone) {
			t.Errorf("dashboard still has %q", gone)
		}
	}
	if !strings.Contains(body, `id="peer-edit-fp"`) || !strings.Contains(body, "readonly") {
		t.Error("edit dialog must keep the read-only fingerprint")
	}
}
