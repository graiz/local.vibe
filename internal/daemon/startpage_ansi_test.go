package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every log tail the dashboard renders comes from tailLogFile, which strips
// escape sequences (log_tail.go). This guards the boundary rather than the
// helper: it fails the moment a render path starts sourcing a tail some other
// way, which is how raw "[2m"/"[37m" text reached the start page before.
func TestStartPageRecoveryJSCarriesNoEscapeBytes(t *testing.T) {
	s := testServer()
	s.ConfigDir = t.TempDir()

	route := &Route{Name: "ansi-route", Cmd: "bun run dev"}
	logPath := filepath.Join(s.ConfigDir, route.Name+".log")
	body := strings.Join([]string{
		"\x1b[2m  .==================.\x1b[0m",
		"\x1b[31mError:\x1b[0m listen EADDRINUSE: address already in use :::3003",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(body+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	js := s.startPageRecoveryInitJS(route)

	if js == "" {
		t.Fatal("expected recovery JS for an EADDRINUSE log")
	}
	// The tail is JSON-marshalled into the page, so a raw ESC byte never
	// survives as itself - it arrives as the six literal characters \u001b,
	// which the browser turns back into a real escape and renders as noise.
	// Asserting only on "\x1b" therefore can never fire, and passes happily
	// against the unfixed code; both forms have to be checked.
	jsonEscapedESC := "\\u001b"
	if strings.Contains(js, "\x1b") || strings.Contains(js, jsonEscapedESC) {
		t.Errorf("rendered JS carries escape sequences: %q", js)
	}
	// Stripping must remove the escapes, not the content around them.
	if !strings.Contains(js, "EADDRINUSE") {
		t.Errorf("stripping ate the real log content: %q", js)
	}
	if !strings.Contains(js, "3003") {
		t.Errorf("expected the extracted port in the recovery, got: %q", js)
	}
}
