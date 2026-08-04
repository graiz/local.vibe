package daemon

import "testing"

func TestScanLogForRecovery(t *testing.T) {
	cases := []struct {
		name        string
		tail        string
		cmd         string
		wantAction  string
		wantPID     int
		wantPort    int
		wantSugCmd  string
		wantSubstr  string // optional: must appear in Message
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
		{
			name:       "python-not-found-zsh",
			tail:       `zsh:1: command not found: python`,
			cmd:        "python app.py",
			wantAction: "edit_cmd",
			wantSugCmd: "python3 app.py",
			wantSubstr: "python3",
		},
		{
			name:       "python-not-found-bash",
			tail:       `bash: python: command not found`,
			cmd:        "python -m http.server 8000",
			wantAction: "edit_cmd",
			wantSugCmd: "python3 -m http.server 8000",
		},
		{
			name:       "python-not-found-but-cmd-already-python3",
			tail:       `zsh:1: command not found: python`,
			cmd:        "python3 app.py",
			wantAction: "",
		},
		{
			name:       "python-not-found-no-cmd",
			tail:       `zsh:1: command not found: python`,
			cmd:        "",
			wantAction: "",
		},
		{
			name:       "module-not-found",
			tail:       `Traceback (most recent call last):` + "\n" + `  File "/x/app.py", line 6, in <module>` + "\n" + `    from flask import Flask` + "\n" + `ModuleNotFoundError: No module named 'flask'`,
			cmd:        "python3 app.py",
			wantAction: "info",
			wantSubstr: "flask",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanLogForRecovery(tc.tail, tc.cmd, "")
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
			if tc.wantSugCmd != "" && got.SuggestedCmd != tc.wantSugCmd {
				t.Errorf("suggested_cmd = %q; want %q", got.SuggestedCmd, tc.wantSugCmd)
			}
			if got.Message == "" {
				t.Error("message is empty")
			}
			if tc.wantSubstr != "" {
				if !contains(got.Message, tc.wantSubstr) {
					t.Errorf("message %q missing substring %q", got.Message, tc.wantSubstr)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSuggestPython3Cmd(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"python app.py", "python3 app.py", true},
		{"python -m http.server", "python3 -m http.server", true},
		{"  python app.py  ", "  python3 app.py  ", true},
		{"python3 app.py", "python3 app.py", false},      // already python3
		{"pythonw app.py", "pythonw app.py", false},      // word-boundary stops match
		{"node app.js", "node app.js", false},            // no python
		{"", "", false},                                  // empty
		{"/usr/bin/python app.py", "/usr/bin/python app.py", false}, // paths kept as-is (not safe to rewrite)
	}
	for _, tc := range cases {
		got, ok := suggestPython3Cmd(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("suggestPython3Cmd(%q) = (%q, %v); want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
