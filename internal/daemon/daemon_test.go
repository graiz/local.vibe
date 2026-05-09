package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// pickFreePort returns an available TCP port on loopback.
func pickFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// TestPIDWriteOnlyOnSuccessfulBind verifies that a daemon instance that fails
// to bind (port already in use) does NOT overwrite the PID file written by
// the successfully running instance.
func TestPIDWriteOnlyOnSuccessfulBind(t *testing.T) {
	dir := t.TempDir()
	port := pickFreePort(t)

	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Port:             port,
			Socket:           filepath.Join(dir, "test.sock"),
			TLD:              "test",
			PIDCheckInterval: 1,
		},
	}
	// Override config dir so the PID file lands in our temp dir. os.UserHomeDir
	// reads HOME on unix and USERPROFILE on Windows — set both so this test
	// works on every platform without per-OS branching.
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Start the first (winning) server.
	srv1 := NewServer(cfg)
	started := make(chan struct{})
	go func() {
		// Signal just before Start blocks on Serve.
		// We detect this by polling the PID file.
		srv1.Start() //nolint:errcheck
	}()

	// Wait up to 2s for the PID file to appear (means srv1 bound successfully).
	pidFile := filepath.Join(dir, ".vibe", "daemon.pid")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.ReadFile(pidFile); err == nil {
			close(started)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-started:
	default:
		t.Fatal("first server did not write PID file within 2s")
	}

	pidData, _ := os.ReadFile(pidFile)
	originalPID := string(pidData)

	// Now try to start a second server on the same port — must fail to bind.
	srv2 := NewServer(cfg)
	errCh := make(chan error, 1)
	go func() { errCh <- srv2.Start() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("second server should have failed to bind but succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second server did not return within 2s")
	}

	// PID file must still contain the first server's PID.
	pidData2, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("PID file disappeared: %v", err)
	}
	if got := string(pidData2); got != originalPID {
		t.Errorf("PID overwritten: want %q, got %q", originalPID, got)
	}

	// Clean up: stop srv1.
	srv1.Stop()
	_ = fmt.Sprint(started) // suppress unused warning
}
