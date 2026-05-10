//go:build windows

package daemon

import "testing"

// englishNetstatOutput is the canonical English `netstat -ano` shape.
const englishNetstatOutput = `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1024
  TCP    0.0.0.0:445            0.0.0.0:0              LISTENING       4
  TCP    127.0.0.1:7999         0.0.0.0:0              LISTENING       9876
  TCP    127.0.0.1:50123        127.0.0.1:7999         ESTABLISHED     12345
  TCP    [::]:135               [::]:0                 LISTENING       1024
`

// germanNetstatOutput is the same shape from a German Windows install.
// The state column is translated ("ABHÖREN" = LISTENING, "HERGESTELLT" =
// ESTABLISHED). The parser MUST NOT depend on the literal word; it keys
// off the foreign address being the unspecified-address form.
const germanNetstatOutput = `
Aktive Verbindungen

  Proto  Lokale Adresse         Remoteadresse          Status          PID
  TCP    0.0.0.0:135            0.0.0.0:0              ABHÖREN         1024
  TCP    127.0.0.1:7999         0.0.0.0:0              ABHÖREN         9876
  TCP    127.0.0.1:50123        127.0.0.1:7999         HERGESTELLT     12345
`

func TestNetstatListenPortParse(t *testing.T) {
	port, ok := parseNetstatListenPort(englishNetstatOutput, map[int]bool{9876: true})
	if !ok {
		t.Fatal("expected a match for PID 9876")
	}
	if port != 7999 {
		t.Errorf("port = %d; want 7999", port)
	}

	if _, ok := parseNetstatListenPort(englishNetstatOutput, map[int]bool{99999: true}); ok {
		t.Error("expected no match for PID 99999")
	}
}

// TestNetstatListenersGerman ensures locale-translated netstat output
// (German here) still parses, since the state column word changes by
// locale but the foreign-address shape doesn't.
func TestNetstatListenersGerman(t *testing.T) {
	listeners := parseNetstatListeners(germanNetstatOutput)
	got := map[int]int{}
	for _, l := range listeners {
		got[l.PID] = l.Port
	}
	if got[9876] != 7999 {
		t.Errorf("PID 9876 → port %d; want 7999", got[9876])
	}
	if _, ok := got[12345]; ok {
		t.Errorf("ESTABLISHED row (PID 12345) should not be reported as a listener")
	}
}

// TestFindPortHoldersFromListeners verifies the parser exposes every PID
// listening on a port (the input parseNetstatListeners gives findPortHoldersDefault).
func TestFindPortHoldersFromListeners(t *testing.T) {
	multi := `
  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:3000           0.0.0.0:0              LISTENING       1111
  TCP    [::]:3000              [::]:0                 LISTENING       1111
  TCP    127.0.0.1:3000         0.0.0.0:0              LISTENING       2222
  TCP    127.0.0.1:50500        127.0.0.1:3000         ESTABLISHED     3333
`
	pidsOnPort := map[int]bool{}
	for _, l := range parseNetstatListeners(multi) {
		if l.Port == 3000 {
			pidsOnPort[l.PID] = true
		}
	}
	if !pidsOnPort[1111] || !pidsOnPort[2222] {
		t.Errorf("expected PIDs 1111 and 2222; got %v", pidsOnPort)
	}
	if pidsOnPort[3333] {
		t.Errorf("ESTABLISHED PID 3333 should not be in the listener set")
	}
}
