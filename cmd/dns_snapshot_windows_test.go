//go:build windows

package cmd

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseShowDnsservers(t *testing.T) {
	out := `
Configuration for interface "Loopback Pseudo-Interface 1"
    DNS servers configured through DHCP:  None
    Register with which suffix:           None

Configuration for interface "Wi-Fi"
    DNS servers configured through DHCP:  192.168.1.1
    Register with which suffix:           Primary only

Configuration for interface "Ethernet"
    Statically Configured DNS Servers:    1.1.1.1
                                          1.0.0.1
    Register with which suffix:           Primary only

Configuration for interface "vEthernet (WSL)"
    DNS servers configured through DHCP:  None
    Register with which suffix:           Primary only
`
	got := parseShowDnsservers(out)

	want := map[string]adapterDNS{
		"Loopback Pseudo-Interface 1": {DHCP: true},
		"Wi-Fi":                       {DHCP: true, Servers: []string{"192.168.1.1"}},
		"Ethernet":                    {DHCP: false, Servers: []string{"1.1.1.1", "1.0.0.1"}},
		"vEthernet (WSL)":             {DHCP: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseShowDnsservers got %#v\nwant %#v", got, want)
	}
}

// Even when an adapter has been re-pointed by us already (DNS is 127.0.0.1),
// we should NOT pick that as an upstream candidate — that's the daemon's
// own listener and would loop.
func TestCandidateUpstreamsSkipsLoopback(t *testing.T) {
	snap := map[string]adapterDNS{
		"Wi-Fi": {DHCP: true, Servers: []string{"127.0.0.1"}},
	}
	got := candidateUpstreams(snap)
	for _, c := range got {
		if c == "127.0.0.1:53" {
			t.Errorf("candidateUpstreams should skip loopback, got %v", got)
		}
	}
	// Should still produce the public fallbacks.
	if len(got) == 0 {
		t.Errorf("expected public fallbacks, got empty list")
	}
}

func TestCandidateUpstreamsPrefersSnapshot(t *testing.T) {
	snap := map[string]adapterDNS{
		"Wi-Fi":    {DHCP: true, Servers: []string{"192.168.1.1"}},
		"Ethernet": {DHCP: false, Servers: []string{"10.0.0.53", "10.0.0.54"}},
	}
	got := candidateUpstreams(snap)
	if len(got) < 3 {
		t.Fatalf("expected at least 3 candidates, got %v", got)
	}
	// Snapshot servers must come before public fallbacks.
	snapshotEntries := map[string]bool{
		"192.168.1.1:53": true, "10.0.0.53:53": true, "10.0.0.54:53": true,
	}
	for i, c := range got {
		if snapshotEntries[c] {
			delete(snapshotEntries, c)
		} else if i < 3 {
			t.Errorf("public fallback %q appeared before snapshot was exhausted (position %d)", c, i)
		}
	}
	if len(snapshotEntries) != 0 {
		t.Errorf("missing snapshot entries: %v", snapshotEntries)
	}
}

// TestStripLoopbackServers_FiltersFromMixedStatic confirms that 127.x.x.x
// servers are dropped while real upstream servers are preserved. This is
// the primary defense against re-running setup overwriting the backup with
// our own listener — without this, uninstall would loop the adapter back
// to a service that's just been removed.
func TestStripLoopbackServers_FiltersFromMixedStatic(t *testing.T) {
	in := map[string]adapterDNS{
		"Wi-Fi": {DHCP: false, Servers: []string{"127.0.0.1", "8.8.8.8"}},
	}
	got := stripLoopbackServers(in)
	want := map[string]adapterDNS{
		"Wi-Fi": {DHCP: false, Servers: []string{"8.8.8.8"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stripLoopbackServers got %#v\nwant %#v", got, want)
	}
}

// TestStripLoopbackServers_DemotesEmptyStaticToDHCP locks down the policy
// for the worst-case input: a snapshot taken AFTER setup ran where the
// only "previous" DNS we recorded was our own listener. Restoring that
// would point the adapter at a removed service. Demoting to DHCP is the
// safe fallback — the user gets their router's DNS instead of a dead
// pointer.
func TestStripLoopbackServers_DemotesEmptyStaticToDHCP(t *testing.T) {
	in := map[string]adapterDNS{
		"Wi-Fi": {DHCP: false, Servers: []string{"127.0.0.1"}},
	}
	got := stripLoopbackServers(in)
	want := map[string]adapterDNS{
		"Wi-Fi": {DHCP: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stripLoopbackServers got %#v\nwant %#v", got, want)
	}
}

// TestStripLoopbackServers_PreservesDHCPWithoutServers confirms the
// common case (DHCP-leased, no static override) round-trips unchanged.
func TestStripLoopbackServers_PreservesDHCPWithoutServers(t *testing.T) {
	in := map[string]adapterDNS{
		"Wi-Fi":    {DHCP: true},
		"Ethernet": {DHCP: true, Servers: []string{"192.168.1.1"}},
	}
	got := stripLoopbackServers(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("stripLoopbackServers should be a no-op on non-loopback input; got %#v\nwant %#v", got, in)
	}
}

// TestStripLoopbackServers_HandlesIPv6Loopback covers ::1 in case it
// somehow ends up in a Servers list (parseShowDnsservers is IPv4-only
// today, but defense in depth — if the input source ever broadens, the
// filter still handles it).
func TestStripLoopbackServers_HandlesIPv6Loopback(t *testing.T) {
	in := map[string]adapterDNS{
		"Wi-Fi": {DHCP: false, Servers: []string{"::1", "8.8.8.8"}},
	}
	got := stripLoopbackServers(in)
	want := map[string]adapterDNS{
		"Wi-Fi": {DHCP: false, Servers: []string{"8.8.8.8"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stripLoopbackServers got %#v\nwant %#v", got, want)
	}
}

// TestAdaptersNeedingDHCPReset is the policy decision behind the post-
// uninstall verify-and-fix step: which adapters still point at our
// removed listener and must be forced to DHCP?
func TestAdaptersNeedingDHCPReset(t *testing.T) {
	live := map[string]adapterDNS{
		"Loopback Pseudo-Interface 1": {DHCP: false, Servers: []string{"127.0.0.1"}},
		"Wi-Fi":                       {DHCP: false, Servers: []string{"127.0.0.1"}},
		"Ethernet":                    {DHCP: true, Servers: []string{"192.168.1.1"}},
		"vEthernet (WSL)":             {DHCP: false, Servers: []string{"127.0.0.1", "8.8.8.8"}},
	}
	got := adaptersNeedingDHCPReset(live)
	sort.Strings(got)
	want := []string{"Wi-Fi", "vEthernet (WSL)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("adaptersNeedingDHCPReset got %v\nwant %v", got, want)
	}
}

// TestIsLoopbackServer covers the prefix matcher for both v4 and v6.
func TestIsLoopbackServer(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true,
		"127.1.2.3": true,
		"::1":       true,
		"8.8.8.8":   false,
		"192.168.1.1": false,
		"":            false,
		// Whitespace tolerance: parseShowDnsservers may leave trimmed
		// values, but defense in depth.
		"  127.0.0.1  ": true,
	}
	for input, want := range cases {
		if got := isLoopbackServer(input); got != want {
			t.Errorf("isLoopbackServer(%q) = %v; want %v", input, got, want)
		}
	}
}
