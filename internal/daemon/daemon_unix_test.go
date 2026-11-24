//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// killPort resolves a port's holders through this function. It must list only
// LISTENERS: `lsof -i tcp:PORT` also matches sockets whose *remote* port is
// PORT, so a browser (or this very process) holding a keep-alive connection
// to a stale server was reported as a holder and SIGTERMed by preflightPort.
func TestFindPortHoldersIgnoresClientConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	nc := exec.Command("nc", "-lk", fmt.Sprint(port))
	if err := nc.Start(); err != nil {
		t.Skipf("nc unavailable: %v", err)
	}
	defer func() { _ = nc.Process.Kill(); _ = nc.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("nc never listened: %v", err)
	}
	defer conn.Close()

	holders := findPortHoldersDefault(port)
	me := os.Getpid()
	foundNC := false
	for _, pid := range holders {
		if pid == me {
			t.Fatalf("own client connection reported as a holder of port %d: %v", port, holders)
		}
		if pid == nc.Process.Pid {
			foundNC = true
		}
	}
	if !foundNC {
		t.Errorf("listener pid %d missing from holders %v", nc.Process.Pid, holders)
	}
}
