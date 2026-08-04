//go:build darwin

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// quarantineValue mimics what macOS writes: flags;timestamp;AgentName;uuid.
const quarantineValue = "0086;6a701233;TestSyncProvider;"

func markQuarantined(t *testing.T, path string) {
	t.Helper()
	if err := unix.Setxattr(path, quarantineAttr, []byte(quarantineValue), 0); err != nil {
		t.Skipf("cannot set %s on this filesystem: %v", quarantineAttr, err)
	}
}

// writeBin creates dir/node_modules/.bin/<name> as an executable file.
func writeBin(t *testing.T, root, sub, name string) string {
	t.Helper()
	bin := filepath.Join(root, sub, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(bin, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The failure this exists for: bunx/npx exec a node_modules/.bin shim, macOS
// returns EPERM because the file is quarantined, and the runner exits non-zero
// having printed nothing. The log holds no clue, so the log scanner alone
// cannot explain it — only probing the files can.
func TestScanQuarantinedExecutablesFindsBinShims(t *testing.T) {
	dir := t.TempDir()
	p := writeBin(t, dir, ".", "concurrently")
	writeBin(t, dir, "client", "vite") // nested workspace, left clean
	markQuarantined(t, p)

	rec := scanQuarantinedExecutables(dir, `bunx concurrently "a" "b"`)
	if rec == nil {
		t.Fatal("expected a recovery hint for a quarantined .bin shim")
	}
	if rec.Action != "info" {
		t.Errorf("Action = %q, want \"info\"", rec.Action)
	}
	if !strings.Contains(rec.Message, "TestSyncProvider") {
		t.Errorf("message should name the agent that set the flag, got: %s", rec.Message)
	}
	if !strings.Contains(rec.Message, "xattr -dr com.apple.quarantine") {
		t.Errorf("message should carry the exact fix command, got: %s", rec.Message)
	}
	// The hint names the directory to clean, not every individual file.
	if !strings.Contains(rec.Message, filepath.Join(dir, "node_modules")) {
		t.Errorf("message should name the node_modules root, got: %s", rec.Message)
	}
}

// Nested workspaces (client/, server/) are where this actually bites, since a
// monorepo installs a .bin tree per package.
func TestScanQuarantinedExecutablesFindsNestedWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeBin(t, dir, ".", "clean-tool")
	p := writeBin(t, dir, "client", "vite")
	markQuarantined(t, p)

	rec := scanQuarantinedExecutables(dir, "bun run dev")
	if rec == nil {
		t.Fatal("expected a hint for a quarantined shim in a nested workspace")
	}
	if !strings.Contains(rec.Message, filepath.Join(dir, "client", "node_modules")) {
		t.Errorf("message should name the nested node_modules, got: %s", rec.Message)
	}
}

// A command naming a script directly must be checked too — this is not a
// Node-specific problem.
func TestScanQuarantinedExecutablesFindsScriptInCmd(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "scripts", "dev.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	markQuarantined(t, p)

	rec := scanQuarantinedExecutables(dir, "./scripts/dev.sh --port $PORT")
	if rec == nil {
		t.Fatal("expected a hint for a quarantined script named in the cmd")
	}
	if !strings.Contains(rec.Message, filepath.Join(dir, "scripts")) {
		t.Errorf("message should name the script's directory, got: %s", rec.Message)
	}
}

// The overwhelmingly common case: nothing is flagged, so the probe must stay
// silent rather than adding noise to every unrelated start failure.
func TestScanQuarantinedExecutablesQuietWhenClean(t *testing.T) {
	dir := t.TempDir()
	writeBin(t, dir, ".", "vite")
	writeBin(t, dir, "server", "tsx")

	if rec := scanQuarantinedExecutables(dir, "bunx concurrently"); rec != nil {
		t.Errorf("expected no hint when nothing is quarantined, got: %s", rec.Message)
	}
	if rec := scanQuarantinedExecutables("", "bunx concurrently"); rec != nil {
		t.Error("expected no hint when the route has no dir")
	}
}

// The noisy variants DO reach the log, and the scanner should turn them into
// the same actionable hint, upgrading to specifics when files are flagged.
func TestScanLogForRecoveryExecDenied(t *testing.T) {
	dir := t.TempDir()
	p := writeBin(t, dir, ".", "vite")
	markQuarantined(t, p)

	tails := []string{
		"zsh:1: operation not permitted: node_modules/.bin/concurrently",
		"/bin/bash: /x/node_modules/.bin/vite: /usr/bin/env: bad interpreter: Operation not permitted",
		`error: script "dev" exited with code 126`,
	}
	for _, tail := range tails {
		rec := scanLogForRecovery(tail, "bun run dev", dir)
		if rec == nil {
			t.Errorf("no hint for tail %q", tail)
			continue
		}
		if !strings.Contains(rec.Message, "xattr -dr com.apple.quarantine") {
			t.Errorf("tail %q produced no fix command: %s", tail, rec.Message)
		}
	}

	// Same log lines with nothing actually flagged still explain the cause,
	// just generically — the shell can report EPERM for other reasons.
	clean := t.TempDir()
	rec := scanLogForRecovery("zsh:1: operation not permitted: ./run.sh", "./run.sh", clean)
	if rec == nil || !strings.Contains(rec.Message, "quarantine") {
		t.Errorf("expected a generic quarantine explanation, got: %+v", rec)
	}
}

// The cmd's leading token is often the same file that also appears in
// node_modules/.bin, which previously counted one file as two.
func TestScanQuarantinedExecutablesDoesNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	p := writeBin(t, dir, ".", "faketool")
	markQuarantined(t, p)

	rec := scanQuarantinedExecutables(dir, "node_modules/.bin/faketool")
	if rec == nil {
		t.Fatal("expected a hint")
	}
	if !strings.Contains(rec.Message, "1 file is marked") {
		t.Errorf("one quarantined file should be reported once, got: %s", rec.Message)
	}
}
