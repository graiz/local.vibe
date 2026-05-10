//go:build windows

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/winutil"
)

// dnsBackupFile is where we persist each adapter's pre-setup DNS configuration
// so `vibe uninstall` can restore the user's original setup instead of resetting
// to DHCP. Lives next to the daemon's other state in ~/.vibe.
func dnsBackupFile() string {
	return filepath.Join(config.Dir(), "dns-backup.json")
}

// adapterDNS records the pre-vibe DNS configuration for one adapter.
// DHCP=true means "ipconfig was set to DHCP, no static servers"; in that case
// Servers should be empty. DHCP=false with a non-empty Servers slice means
// "static configuration with these IPv4 servers, in order".
type adapterDNS struct {
	DHCP    bool     `json:"dhcp"`
	Servers []string `json:"servers,omitempty"`
}

// dnsSnapshotPowerShellScript is the PowerShell pipeline that emits each
// adapter's IPv4 DNS configuration as JSON. We use the structured cmdlets
// (Get-DnsClientServerAddress + Get-NetIPInterface) instead of parsing
// `netsh ... show dnsservers` because netsh's output is locale-translated
// — on a German Windows the section header reads "Konfiguration für
// Schnittstelle", not "Configuration for interface". The cmdlets return
// the same data on every locale.
//
// `-InputObject @(...)` is the canonical idiom to force the result into a
// JSON array even when there's only one adapter — without it, ConvertTo-Json
// unwraps single-element arrays to plain objects and our parser would have
// to handle both shapes.
const dnsSnapshotPowerShellScript = `
$rows = Get-DnsClientServerAddress -AddressFamily IPv4 | ForEach-Object {
  $alias = $_.InterfaceAlias
  $ipif = Get-NetIPInterface -AddressFamily IPv4 -InterfaceAlias $alias -ErrorAction SilentlyContinue
  $dhcp = $false
  if ($ipif -and $ipif.Dhcp) { $dhcp = ($ipif.Dhcp.ToString() -eq 'Enabled') }
  [PSCustomObject]@{
    Name    = $alias
    DHCP    = $dhcp
    Servers = @($_.ServerAddresses)
  }
}
ConvertTo-Json -InputObject @($rows) -Compress -Depth 3
`

// snapshotAdapterDNS returns each adapter's current IPv4 DNS configuration.
// Used at setup time so uninstall can restore exactly what the user had.
//
// Loopback servers (127.x.x.x) are stripped before the snapshot is returned —
// re-running setup would otherwise capture our own resolver listener as the
// "previous" DNS and uninstall would loop the adapter back to a service that
// has just been removed. See stripLoopbackServers for the policy.
func snapshotAdapterDNS() (map[string]adapterDNS, error) {
	out, err := winutil.PowerShellJSON(dnsSnapshotPowerShellScript)
	if err != nil {
		return nil, fmt.Errorf("powershell get-dnsclientserveraddress: %w", err)
	}
	return stripLoopbackServers(parsePowerShellDNSJSON(out)), nil
}

// stripLoopbackServers removes 127.x.x.x entries from each adapter's Servers
// list. If filtering leaves a static entry empty (i.e. the adapter was
// previously configured to use ONLY our own resolver), the entry is demoted
// to DHCP — the safe restore target when no real upstream is recorded.
//
// Pure function for testability; called by snapshotAdapterDNS and again at
// restore time as a defense-in-depth check against hand-edited backups.
func stripLoopbackServers(snap map[string]adapterDNS) map[string]adapterDNS {
	out := make(map[string]adapterDNS, len(snap))
	for name, entry := range snap {
		kept := entry.Servers[:0:0] // new slice, no aliasing
		for _, srv := range entry.Servers {
			if isLoopbackServer(srv) {
				continue
			}
			kept = append(kept, srv)
		}
		if !entry.DHCP && len(kept) == 0 {
			out[name] = adapterDNS{DHCP: true}
			continue
		}
		out[name] = adapterDNS{DHCP: entry.DHCP, Servers: kept}
	}
	return out
}

// isLoopbackServer matches the IPv4 loopback range (127.0.0.0/8) and the
// IPv6 loopback literal. Cheap prefix checks are sufficient — we don't
// need full address validation here, false positives just mean we'd skip a
// non-loopback server that happens to start with "127." (impossible in
// IPv4 since 127.0.0.0/8 is reserved entirely for loopback).
func isLoopbackServer(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "127.") || s == "::1"
}

// verifyAndFixLoopbackDNS re-reads adapter DNS state and forces any adapter
// still pointing at a 127.x.x.x server back to DHCP. Final safety net for
// the uninstall path: even if restoreAdapterDNS misses a case (new adapter
// appeared mid-restore, hand-edited backup with mixed loopback + real
// servers, etc.), this leaves the user with a working resolver instead of
// a stale pointer to our removed listener.
//
// Returns the names of adapters that were forced to DHCP.
func verifyAndFixLoopbackDNS() []string {
	out, err := winutil.PowerShellJSON(dnsSnapshotPowerShellScript)
	if err != nil {
		return nil
	}
	candidates := adaptersNeedingDHCPReset(parsePowerShellDNSJSON(out))
	var fixed []string
	for _, name := range candidates {
		out, err := exec.Command(winutil.Sys32("netsh"), "interface", "ipv4", "set", "dnsservers",
			"name="+name, "dhcp",
		).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not force DHCP on %q: %v — %s\n",
				name, err, strings.TrimSpace(string(out)))
			continue
		}
		fixed = append(fixed, name)
	}
	return fixed
}

// adaptersNeedingDHCPReset returns the names of adapters whose Servers list
// contains a loopback address — these are still pointing at vibe's removed
// listener after uninstall and must be forced back to DHCP. Skips the
// loopback pseudo-interface, which legitimately points at itself.
//
// Pure function — pulled out of verifyAndFixLoopbackDNS for testability.
func adaptersNeedingDHCPReset(live map[string]adapterDNS) []string {
	var out []string
	for name, entry := range live {
		if strings.HasPrefix(strings.ToLower(name), "loopback") {
			continue
		}
		for _, srv := range entry.Servers {
			if isLoopbackServer(srv) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// psAdapterDNSRow is the JSON shape produced by dnsSnapshotPowerShellScript
// for a single adapter. Field names match the PSCustomObject keys exactly
// (case-sensitive in encoding/json).
type psAdapterDNSRow struct {
	Name    string   `json:"Name"`
	DHCP    bool     `json:"DHCP"`
	Servers []string `json:"Servers"`
}

// parsePowerShellDNSJSON parses the JSON output of dnsSnapshotPowerShellScript
// into the same map shape the rest of the code consumes. PowerShell's
// ConvertTo-Json unwraps single-element arrays to plain objects unless we
// force-array them with `@(...)`; we still defensively handle both shapes
// in case an older PowerShell or a future script tweak produces a bare
// object.
//
// Returns nil for empty input. Malformed input returns an empty map (not
// nil) so callers can treat "parsed zero adapters" the same as "PowerShell
// returned an empty array".
func parsePowerShellDNSJSON(data []byte) map[string]adapterDNS {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}

	var rows []psAdapterDNSRow
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return map[string]adapterDNS{}
		}
	case '{':
		var single psAdapterDNSRow
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return map[string]adapterDNS{}
		}
		rows = []psAdapterDNSRow{single}
	default:
		return map[string]adapterDNS{}
	}

	result := make(map[string]adapterDNS, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			continue
		}
		var servers []string
		for _, s := range r.Servers {
			s = strings.TrimSpace(s)
			if s != "" {
				servers = append(servers, s)
			}
		}
		result[r.Name] = adapterDNS{DHCP: r.DHCP, Servers: servers}
	}
	return result
}

// saveDNSBackup persists the snapshot to ~/.vibe/dns-backup.json so uninstall
// can restore from it. Idempotent: overwrites any existing backup.
func saveDNSBackup(snap map[string]adapterDNS) error {
	if err := os.MkdirAll(config.Dir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dnsBackupFile(), data, 0644)
}

// loadDNSBackup reads the snapshot. Missing file returns (nil, nil) so the
// caller can fall through to a DHCP reset.
func loadDNSBackup() (map[string]adapterDNS, error) {
	data, err := os.ReadFile(dnsBackupFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap map[string]adapterDNS
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// restoreAdapterDNS rewrites each adapter's DNS to the snapshot's value:
// DHCP for adapters that were DHCP-configured, static for adapters that had
// hand-set servers. Adapters not present in the snapshot fall through to
// DHCP (safe default for newly-connected interfaces). Errors are logged but
// don't abort the loop — uninstall is best-effort.
//
// Re-applies stripLoopbackServers as defense-in-depth: even a hand-edited
// or pre-filter-era backup file can't poison the restore by sending the
// adapter back to a 127.0.0.1 listener that no longer exists.
func restoreAdapterDNS(snap map[string]adapterDNS) {
	snap = stripLoopbackServers(snap)
	adapters, _ := connectedIPv4Adapters()
	for _, name := range adapters {
		entry, found := snap[name]
		if !found || entry.DHCP || len(entry.Servers) == 0 {
			out, err := exec.Command(winutil.Sys32("netsh"), "interface", "ipv4", "set", "dnsservers",
				"name="+name, "dhcp",
			).CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not reset DNS on %q: %v — %s\n", name, err, strings.TrimSpace(string(out)))
			}
			continue
		}
		// Static restore: first server "static <ip> primary", then add the rest
		// as additional servers via "add dnsservers ... index=N".
		out, err := exec.Command(winutil.Sys32("netsh"), "interface", "ipv4", "set", "dnsservers",
			"name="+name, "static", entry.Servers[0], "primary",
		).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not restore primary DNS on %q: %v — %s\n", name, err, strings.TrimSpace(string(out)))
			continue
		}
		for i, srv := range entry.Servers[1:] {
			out, err := exec.Command(winutil.Sys32("netsh"), "interface", "ipv4", "add", "dnsservers",
				"name="+name, "address="+srv, fmt.Sprintf("index=%d", i+2),
			).CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not add secondary DNS %s on %q: %v — %s\n", srv, name, err, strings.TrimSpace(string(out)))
			}
		}
	}
}
