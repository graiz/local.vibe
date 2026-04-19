package daemon

import "testing"

func TestScanLogForRecovery(t *testing.T) {
	cases := []struct {
		name       string
		tail       string
		wantAction string
		wantPID    int
		wantPort   int
	}{
		{
			name: "nextjs-orphan-pid",
			tail: `✓ Ready in 314ms
⨯ Another next dev server is already running.

- Local:        http://localhost:3001
- PID:          23674
- Dir:          /tmp/web`,
			wantAction: "kill_pid",
			wantPID:    23674,
		},
		{
			name:       "node-eaddrinuse",
			tail:       `Error: listen EADDRINUSE: address already in use :::3000`,
			wantAction: "kill_port",
			wantPort:   3000,
		},
		{
			name:       "generic-address-in-use",
			tail:       `panic: listen tcp 127.0.0.1:8080: bind: address already in use`,
			wantAction: "kill_port",
			wantPort:   8080,
		},
		{
			name:       "no-match",
			tail:       `npm ERR! missing script: dev`,
			wantAction: "",
		},
		{
			name:       "empty",
			tail:       ``,
			wantAction: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanLogForRecovery(tc.tail)
			if tc.wantAction == "" {
				if got != nil {
					t.Fatalf("expected no recovery, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected recovery with action %q, got nil", tc.wantAction)
			}
			if got.Action != tc.wantAction {
				t.Errorf("action = %q; want %q", got.Action, tc.wantAction)
			}
			if got.PID != tc.wantPID {
				t.Errorf("pid = %d; want %d", got.PID, tc.wantPID)
			}
			if got.Port != tc.wantPort {
				t.Errorf("port = %d; want %d", got.Port, tc.wantPort)
			}
			if got.Message == "" {
				t.Error("message is empty")
			}
		})
	}
}
