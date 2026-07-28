package cmd

import (
	"strings"
	"testing"
)

// A VPN kill-switch that filters direct loopback connections makes a plain
// dial to the daemon's port time out while the daemon is serving perfectly
// through the privileged-port redirect. Doctor must not call that a dead
// listener — everything the user actually uses still works.
func TestClassifyListener(t *testing.T) {
	cases := []struct {
		name                          string
		direct, redirectOpen, daemonUp bool
		want                          checkStatus
	}{
		{"direct dial works", true, true, true, statusOK},
		{"direct works even with redirect down", true, false, true, statusOK},
		{"filtered: reachable via redirect", false, true, true, statusWarn},
		{"genuinely down: nothing answers", false, false, true, statusFail},
		{"daemon down, redirect answers (stale/foreign listener)", false, true, false, statusFail},
		{"daemon down and nothing answers", false, false, false, statusFail},
	}
	for _, tc := range cases {
		if got := classifyListener(tc.direct, tc.redirectOpen, tc.daemonUp); got != tc.want {
			t.Errorf("%s: classifyListener(%v,%v,%v) = %v, want %v",
				tc.name, tc.direct, tc.redirectOpen, tc.daemonUp, got, tc.want)
		}
	}
}

func TestListenerDetail(t *testing.T) {
	if got, want := listenerDetail(7999, 80, statusOK), "127.0.0.1:7999"; got != want {
		t.Errorf("ok detail = %q, want %q", got, want)
	}
	if got, want := listenerDetail(7999, 80, statusFail), "127.0.0.1:7999"; got != want {
		t.Errorf("fail detail = %q, want %q", got, want)
	}
	// The warn line must say the service is fine, or it reads like a fault.
	got := listenerDetail(7443, 443, statusWarn)
	for _, want := range []string{"127.0.0.1:7443", "direct dial blocked", ":443 redirect"} {
		if !strings.Contains(got, want) {
			t.Errorf("warn detail %q missing %q", got, want)
		}
	}
}

func TestCheckStatusMark(t *testing.T) {
	for status, want := range map[checkStatus]string{
		statusOK:   "✓",
		statusWarn: "⚠",
		statusFail: "✗",
	} {
		if got := status.mark(); got != want {
			t.Errorf("status %d mark = %q, want %q", status, got, want)
		}
	}
}

func TestDoctorHasLoopback(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  bool
	}{
		{"loopback present", []string{"::1", "127.0.0.1"}, true},
		{"only loopback", []string{"127.0.0.1"}, true},
		{"no loopback", []string{"192.168.1.10", "::1"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		if got := hasLoopback(tc.addrs); got != tc.want {
			t.Errorf("%s: hasLoopback(%v) = %v, want %v", tc.name, tc.addrs, got, tc.want)
		}
	}
}
