//go:build windows

package daemon

import "testing"

// TestNetstatListenPortPicksMatchingPID exercises the netstat output
// parser with the canonical `netstat -ano -p TCP` header + a few
// representative rows. We don't actually shell out — we feed the parser
// directly via a unit-test entry point.
func TestNetstatListenPortParse(t *testing.T) {
	// Real netstat output excerpt (re-formatted to keep the test self-contained).
	out := `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1024
  TCP    0.0.0.0:445            0.0.0.0:0              LISTENING       4
  TCP    127.0.0.1:7999         0.0.0.0:0              LISTENING       9876
  TCP    127.0.0.1:50123        127.0.0.1:7999         ESTABLISHED     12345
  TCP    [::]:135               [::]:0                 LISTENING       1024
`

	// Parser is exposed as a free function in the package (alongside
	// portFromProcessGroup) so we can unit-test without netstat itself.
	port, ok := parseNetstatListenPort(out, map[int]bool{9876: true})
	if !ok {
		t.Fatal("expected a match for PID 9876")
	}
	if port != 7999 {
		t.Errorf("port = %d; want 7999", port)
	}

	// PID set that doesn't match any LISTENING row.
	if _, ok := parseNetstatListenPort(out, map[int]bool{99999: true}); ok {
		t.Error("expected no match for PID 99999")
	}
}
