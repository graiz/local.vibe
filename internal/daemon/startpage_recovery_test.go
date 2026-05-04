package daemon

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/config"
)

func TestServeStartPageRecoveryUI(t *testing.T) {
	cfg := &config.Config{Daemon: config.DaemonConfig{TLD: "vibe"}}
	s := NewServer(cfg)
	route := &Route{Name: "demo", Port: 3000, Type: RouteManaged, Cmd: "make dev"}
	w := httptest.NewRecorder()
	s.serveStartPage(w, httptest.NewRequest("GET", "/", nil), route)
	body := w.Body.String()
	for _, token := range []string{`id="recovery"`, `id="recovery-btn"`, `showRecovery`, `recoverAndRetry`, `kill_pid`, `cancelLaunch`, `startApp();`, `>Cancel<`} {
		if !strings.Contains(body, token) {
			t.Errorf("start page missing %q", token)
		}
	}
}

// When the route has a stored Failure with a Recovery hint, the start page
// must emit a showRecovery(...) call so the user sees the "Kill PID X and
// Retry" button immediately — not only after clicking Start.
func TestServeStartPageEmitsStoredRecoveryOnLoad(t *testing.T) {
	cfg := &config.Config{Daemon: config.DaemonConfig{TLD: "vibe"}}
	s := NewServer(cfg)
	route := &Route{Name: "demo", Port: 3002, Type: RouteManaged, Cmd: "make dev"}
	route.SetFailure(&Failure{
		Message:  "process exited before becoming ready",
		Log:      "Another next dev server is already running.\n- PID: 73266\n",
		Recovery: &Recovery{Action: "kill_pid", PID: 73266, Message: "Kill PID 73266 and retry"},
	})

	w := httptest.NewRecorder()
	s.serveStartPage(w, httptest.NewRequest("GET", "/", nil), route)
	body := w.Body.String()

	if !strings.Contains(body, `showRecovery(`) {
		t.Fatalf("expected showRecovery(...) inline init call; body:\n%s", body)
	}
	if !strings.Contains(body, `"pid":73266`) {
		t.Errorf("expected PID 73266 in emitted recovery JSON")
	}
	if !strings.Contains(body, `"action":"kill_pid"`) {
		t.Errorf("expected kill_pid action in emitted recovery JSON")
	}
}

// When the in-memory Failure is gone (e.g., daemon restarted) but the log
// file still contains recovery-triggering output, the start page must scan
// the log and surface the recovery. Otherwise the user has to click Start,
// wait for it to fail again, and only then see the orphan hint.
func TestServeStartPageScansLogWhenFailureMissing(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{Daemon: config.DaemonConfig{TLD: "vibe"}}
	s := NewServer(cfg)
	s.ConfigDir = tmp

	route := &Route{Name: "lp", Port: 3002, Type: RouteManaged, Cmd: "make dev"}
	// No route.SetFailure — simulate post-restart state.

	logBody := "▲ Next.js 16.2.4\n" +
		"- Local:  http://localhost:3002\n" +
		"⨯ Another next dev server is already running.\n" +
		"- Local:  http://localhost:3000\n" +
		"- PID:    73266\n" +
		"- Dir:    /tmp/web\n" +
		"make: *** [dev] Error 1\n"
	if err := os.WriteFile(filepath.Join(tmp, "lp.log"), []byte(logBody), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	w := httptest.NewRecorder()
	s.serveStartPage(w, httptest.NewRequest("GET", "/", nil), route)
	body := w.Body.String()

	if !strings.Contains(body, `showRecovery(`) {
		t.Fatalf("expected log-scan fallback to emit showRecovery(...); body:\n%s", body)
	}
	if !strings.Contains(body, `"pid":73266`) {
		t.Errorf("expected PID 73266 from scanned log in emitted recovery JSON")
	}
}

// No stored failure and no log file → no recovery init call. Page is still
// a valid start page (asserted by the sibling test above).
func TestServeStartPageEmitsNoRecoveryWhenNoSignal(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{Daemon: config.DaemonConfig{TLD: "vibe"}}
	s := NewServer(cfg)
	s.ConfigDir = tmp

	route := &Route{Name: "quiet", Port: 3000, Type: RouteManaged, Cmd: "make dev"}
	w := httptest.NewRecorder()
	s.serveStartPage(w, httptest.NewRequest("GET", "/", nil), route)
	body := w.Body.String()

	// The page defines showRecovery but must not call it on load when
	// nothing is wrong.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "showRecovery(") {
			t.Errorf("unexpected inline showRecovery call: %q", trimmed)
		}
	}
}
