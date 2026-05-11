//go:build windows

package cmd

import (
	"reflect"
	"sort"
	"testing"
)

func TestParsePowerShellDNSJSON_Array(t *testing.T) {
	// Canonical shape: ConvertTo-Json -InputObject @(...) emits an array.
	in := []byte(`[
		{"Name":"Loopback Pseudo-Interface 1","DHCP":true,"Servers":[]},
		{"Name":"Wi-Fi","DHCP":true,"Servers":["192.168.1.1"]},
		{"Name":"Ethernet","DHCP":false,"Servers":["1.1.1.1","1.0.0.1"]},
		{"Name":"vEthernet (WSL)","DHCP":true,"Servers":[]}
	]`)
	got := parsePowerShellDNSJSON(in)
	want := map[string]adapterDNS{
		"Loopback Pseudo-Interface 1": {DHCP: true},
		"Wi-Fi":                       {DHCP: true, Servers: []string{"192.168.1.1"}},
		"Ethernet":                    {DHCP: false, Servers: []string{"1.1.1.1", "1.0.0.1"}},
		"vEthernet (WSL)":             {DHCP: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePowerShellDNSJSON got %#v\nwant %#v", got, want)
	}
}

// TestParsePowerShellDNSJSON_GermanLocale locks down the load-bearing
// promise of switching to PowerShell: the adapter name "WLAN" (the
// German rendering of Wi-Fi on a German Windows install) round-trips
// unchanged. The previous netsh parser keyed on English column headers
// and would silently return zero adapters here.
func TestParsePowerShellDNSJSON_GermanLocale(t *testing.T) {
	in := []byte(`[
		{"Name":"WLAN","DHCP":true,"Servers":["192.168.178.1"]},
		{"Name":"Ethernet 2","DHCP":false,"Servers":["10.0.0.1"]}
	]`)
	got := parsePowerShellDNSJSON(in)
	want := map[string]adapterDNS{
		"WLAN":       {DHCP: true, Servers: []string{"192.168.178.1"}},
		"Ethernet 2": {DHCP: false, Servers: []string{"10.0.0.1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("German-locale parse got %#v\nwant %#v", got, want)
	}
}

// TestParsePowerShellDNSJSON_SingleObject covers the defensive fallback
// for "ConvertTo-Json unwrapped a one-element array to a plain object"
// — we wrap with @() in the script to prevent this, but the parser
// stays robust against it in case an older PowerShell or an edited
// script omits the wrap.
func TestParsePowerShellDNSJSON_SingleObject(t *testing.T) {
	in := []byte(`{"Name":"Wi-Fi","DHCP":true,"Servers":["192.168.1.1"]}`)
	got := parsePowerShellDNSJSON(in)
	want := map[string]adapterDNS{
		"Wi-Fi": {DHCP: true, Servers: []string{"192.168.1.1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single-object parse got %#v\nwant %#v", got, want)
	}
}

func TestParsePowerShellDNSJSON_EmptyAndMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want map[string]adapterDNS
	}{
		{"empty bytes", []byte(``), nil},
		{"empty array", []byte(`[]`), map[string]adapterDNS{}},
		{"malformed", []byte(`not json at all`), map[string]adapterDNS{}},
		{"empty name skipped", []byte(`[{"Name":"","DHCP":true,"Servers":[]}]`), map[string]adapterDNS{}},
		{"whitespace-only servers stripped", []byte(`[{"Name":"Wi-Fi","DHCP":false,"Servers":["  ","8.8.8.8"]}]`),
			map[string]adapterDNS{"Wi-Fi": {DHCP: false, Servers: []string{"8.8.8.8"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePowerShellDNSJSON(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v\nwant %#v", got, tc.want)
			}
		})
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
		// Whitespace tolerance — defense in depth.
		"  127.0.0.1  ": true,
	}
	for input, want := range cases {
		if got := isLoopbackServer(input); got != want {
			t.Errorf("isLoopbackServer(%q) = %v; want %v", input, got, want)
		}
	}
}

func TestParseConnectedAdaptersJSON(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []string
	}{
		{
			"happy path",
			[]byte(`["Wi-Fi","Ethernet"]`),
			[]string{"Wi-Fi", "Ethernet"},
		},
		{
			"loopback filtered",
			[]byte(`["Wi-Fi","Loopback Pseudo-Interface 1","Ethernet"]`),
			[]string{"Wi-Fi", "Ethernet"},
		},
		{
			"German locale interface name",
			[]byte(`["WLAN","Ethernet 2"]`),
			[]string{"WLAN", "Ethernet 2"},
		},
		{
			"single bare string fallback",
			[]byte(`"Wi-Fi"`),
			[]string{"Wi-Fi"},
		},
		{
			"null returns nil",
			[]byte(`null`),
			nil,
		},
		{
			"empty array returns empty",
			[]byte(`[]`),
			nil,
		},
		{
			"malformed returns nil",
			[]byte(`{"not":"a string array"}`),
			nil,
		},
		{
			"whitespace-only entries skipped",
			[]byte(`["","Wi-Fi","   "]`),
			[]string{"Wi-Fi"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseConnectedAdaptersJSON(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}
