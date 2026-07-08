//go:build windows

package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/cert"
	"github.com/graiz/local.vibe/internal/vibeskill"
	"github.com/graiz/local.vibe/internal/winutil"
	"golang.org/x/sys/windows"
)

// installVibeSkillBestEffort runs the skill install as a labeled step but never
// fails setup — on error it prints a "skipped" note and returns. No-op when
// --no-skill was passed.
func installVibeSkillBestEffort() {
	if setupNoSkill {
		return
	}
	fmt.Printf("  %-54s", "Installing agent skill (~/.claude/skills/local-vibe)...")
	if err := installVibeSkillWindows(); err != nil {
		fmt.Printf("skipped (%v)\n", err)
		return
	}
	fmt.Println("ok")
}

// installVibeSkillWindows writes the global local.vibe agent skill so coding
// agents discover local.vibe. Windows setup runs as the user (medium
// integrity), so no ownership fixups are needed.
func installVibeSkillWindows() error {
	home, err := vibeHomeDir()
	if err != nil {
		return err
	}
	_, err = vibeskill.InstallTo(home)
	return err
}

func setupPlatform() error {
	if !isElevated() {
		return fmt.Errorf("setup requires Administrator — right-click PowerShell, choose \"Run as administrator\", then re-run: vibe setup")
	}

	// Detect port collisions BEFORE any state-changing step. If the user
	// declines to continue, we exit without having modified DNS, certs,
	// portproxy rules, or the Scheduled Task — they're exactly where they
	// started.
	if err := precheckPortCollisions(); err != nil {
		return err
	}

	fmt.Println("Setting up local.vibe on Windows...")
	fmt.Println()

	steps := []setupStep{
		{"Generating TLS certificates (*.vibe)", generateCertsWindows},
		{"Trusting CA in Windows root store", trustCAWindows},
		{"Installing netsh portproxy rules (80→7999, 443→7443)", installPortProxy},
		{"Snapshotting current adapter DNS (for clean uninstall)", backupAdapterDNS},
		{"Repointing active adapters' DNS to 127.0.0.1", configureDNS},
		{"Enabling TLS and DNS in daemon config", enableTLSAndDNSWindows},
		{"Registering Scheduled Task for autostart", installScheduledTask},
		{"Flushing DNS cache", flushDNS},
		{"Verifying DNS resolution (test.vibe → 127.0.0.1)", verifyDNSWindows},
	}

	err := runSteps(steps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nSetup failed: %v\n", err)
		return err
	}

	// Best-effort: the agent skill is a convenience layer, not part of the
	// request path, so a write failure must not fail setup or suppress the
	// "start daemon now?" prompt below.
	installVibeSkillBestEffort()

	fmt.Println()
	fmt.Println("Setup complete! HTTPS enabled for all *.vibe domains.")
	fmt.Println("Daemon starts automatically at login (Scheduled Task: vibe).")
	fmt.Println()
	if promptYN("Start the daemon now and open https://local.vibe?") {
		out, err := exec.Command(winutil.Sys32("schtasks"), "/run", "/tn", scheduledTaskName).CombinedOutput()
		if err != nil {
			return fmt.Errorf("schtasks /run: %w — %s", err, strings.TrimSpace(string(out)))
		}
		// Give the task a moment to come up before opening the dashboard.
		time.Sleep(800 * time.Millisecond)
		openDashboard()
		return nil
	}
	fmt.Println()
	fmt.Println("When ready:  vibe daemon start")
	return nil
}

// isElevated reports whether the current process token belongs to the
// Administrators group with elevated rights. We use IsElevated rather than
// SID membership so a non-elevated admin (UAC-split token) still hits the
// "rerun as Admin" error — which is what we want.
func isElevated() bool {
	token := windows.GetCurrentProcessToken()
	return token.IsElevated()
}

// vibeHomeDir returns the user's home directory. On Windows, %USERPROFILE%
// is the canonical source — os.UserHomeDir already prefers it.
func vibeHomeDir() (string, error) {
	return os.UserHomeDir()
}

func generateCertsWindows() error {
	home, err := vibeHomeDir()
	if err != nil {
		return err
	}
	vibeDir := filepath.Join(home, ".vibe")
	certsDir := filepath.Join(vibeDir, "certs")

	if err := os.MkdirAll(vibeDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", vibeDir, err)
	}

	caCert, caKey, err := cert.EnsureCA(certsDir)
	if err != nil {
		return err
	}
	if _, _, err := cert.EnsureLeaf(certsDir, caCert, caKey, []string{"local.vibe"}); err != nil {
		return err
	}
	return nil
}

func trustCAWindows() error {
	home, err := vibeHomeDir()
	if err != nil {
		return err
	}
	return cert.TrustCA(filepath.Join(home, ".vibe", "certs"))
}

// installPortProxy adds netsh forwarding rules so 80 → 7999 and 443 → 7443
// on 127.0.0.1. Idempotent: netsh's `add` rejects an existing identical
// rule, so we delete-then-add to make re-running setup cheap. The rules
// persist across reboots automatically (they live in registry under the
// IP Helper service).
func installPortProxy() error {
	pairs := [][2]string{
		{"80", "7999"},
		{"443", "7443"},
	}
	for _, pair := range pairs {
		listen, connect := pair[0], pair[1]
		// best-effort delete first; ignore failure (rule may not exist)
		_ = exec.Command(winutil.Sys32("netsh"), "interface", "portproxy", "delete", "v4tov4",
			"listenport="+listen, "listenaddress=127.0.0.1").Run()

		out, err := exec.Command(winutil.Sys32("netsh"), "interface", "portproxy", "add", "v4tov4",
			"listenport="+listen,
			"listenaddress=127.0.0.1",
			"connectport="+connect,
			"connectaddress=127.0.0.1",
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("netsh portproxy add %s→%s: %w — %s", listen, connect, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// backupAdapterDNS snapshots each adapter's pre-vibe DNS configuration to
// ~/.vibe/dns-backup.json so `vibe uninstall` can restore exactly what was
// there. Best-effort: if the snapshot fails, we surface a warning but let
// setup continue — uninstall will fall back to DHCP-reset for any adapter
// missing from the backup.
//
// Preserves any existing backup file rather than overwriting. Re-running
// setup AFTER a prior setup already repointed adapters would otherwise
// capture our own listener as the "previous" DNS and lose the original
// configuration. The snapshot's loopback filter helps too, but skipping
// the write when a backup already exists is the load-bearing safety here:
// uninstall must always be able to restore the very first pre-vibe state.
func backupAdapterDNS() error {
	if existing, err := loadDNSBackup(); err == nil && len(existing) > 0 {
		fmt.Fprintf(os.Stderr, "  preserving existing DNS backup (%d adapter(s)) at %s\n",
			len(existing), dnsBackupFile())
		return nil
	}
	snap, err := snapshotAdapterDNS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not snapshot DNS settings (%v) — uninstall will reset to DHCP\n", err)
		return nil
	}
	if err := saveDNSBackup(snap); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write %s (%v) — uninstall will reset to DHCP\n", dnsBackupFile(), err)
		return nil
	}
	return nil
}

// configureDNS sets every "Connected" IPv4 interface's primary DNS to
// 127.0.0.1 so .vibe queries hit our embedded resolver. Non-.vibe queries
// are forwarded by the resolver to the upstream chosen in
// enableTLSAndDNSWindows (probed from the current adapter DNS, with public
// fallbacks). On uninstall we restore each adapter from dns-backup.json.
func configureDNS() error {
	adapters, err := connectedIPv4Adapters()
	if err != nil {
		return fmt.Errorf("enumerate adapters: %w", err)
	}
	if len(adapters) == 0 {
		return fmt.Errorf("no connected IPv4 adapters found — check network connectivity")
	}
	var firstErr error
	configured := 0
	for _, name := range adapters {
		out, err := exec.Command(winutil.Sys32("netsh"), "interface", "ipv4", "set", "dnsservers",
			"name="+name, "static", "127.0.0.1", "primary",
		).CombinedOutput()
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("netsh set dnsservers %q: %w — %s", name, err, strings.TrimSpace(string(out)))
			}
			continue
		}
		configured++
	}
	if configured == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

// connectedAdaptersPowerShellScript emits the names of every adapter
// currently in the "Up" state as a JSON array. We use Get-NetAdapter
// (a Windows-shipped cmdlet, not a netsh shim) because its Status
// property is an enum value that's the same on every locale — the
// previous netsh approach matched the literal English word "connected"
// and would silently return zero adapters on a German or French Windows.
//
// `-InputObject @(...)` forces a JSON array even with one element, so the
// Go side never has to handle "single bare string" output.
const connectedAdaptersPowerShellScript = `
$names = @(Get-NetAdapter | Where-Object { $_.Status -eq 'Up' } | Select-Object -ExpandProperty Name)
ConvertTo-Json -InputObject $names -Compress
`

// connectedIPv4Adapters returns the friendly names of every adapter
// currently in the "Up" state, excluding loopback (DNS on the loopback
// pseudo-interface would be a no-op).
func connectedIPv4Adapters() ([]string, error) {
	out, err := winutil.PowerShellJSON(connectedAdaptersPowerShellScript)
	if err != nil {
		return nil, fmt.Errorf("powershell get-netadapter: %w", err)
	}
	return parseConnectedAdaptersJSON(out), nil
}

// parseConnectedAdaptersJSON parses the JSON array (or single-element
// fallback) emitted by connectedAdaptersPowerShellScript and filters out
// loopback adapters. Pure function for testability.
func parseConnectedAdaptersJSON(data []byte) []string {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	var names []string
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
			return nil
		}
	case '"':
		// PowerShell occasionally unwraps a one-element array to a bare
		// string despite the @() wrap; handle it defensively.
		var single string
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil
		}
		names = []string{single}
	default:
		return nil
	}

	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(n), "loopback") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// enableTLSAndDNSWindows updates ~/.vibe/config.json to enable both the
// TLS listener and the embedded DNS resolver. Cross-platform JSON edit —
// preserves any unknown fields the user added by hand.
func enableTLSAndDNSWindows() error {
	home, err := vibeHomeDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(home, ".vibe", "config.json")

	var cfgMap map[string]interface{}
	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfgMap); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if cfgMap == nil {
		cfgMap = make(map[string]interface{})
	}

	daemon, _ := cfgMap["daemon"].(map[string]interface{})
	if daemon == nil {
		daemon = make(map[string]interface{})
	}
	daemon["tls"] = map[string]interface{}{
		"enabled":   true,
		"port":      7443,
		"certs_dir": filepath.Join(home, ".vibe", "certs"),
	}
	// Pick the upstream the resolver should forward non-.vibe queries to.
	// Prefer whatever the user already had configured (probed for liveness)
	// so corporate / split-horizon networks keep working; fall back to the
	// public list if nothing answers. We do this *after* snapshotAdapterDNS,
	// which runs as its own setup step before adapters are repointed.
	snap, _ := loadDNSBackup()
	upstream := pickUpstreamResolver(snap)
	daemon["dns"] = map[string]interface{}{
		"enabled":  true,
		"listen":   "127.0.0.1:53",
		"upstream": upstream,
	}
	cfgMap["daemon"] = daemon

	out, err := json.MarshalIndent(cfgMap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0644)
}

// installScheduledTask creates a logon-triggered task that runs the daemon
// at the user's normal (medium) integrity level. /f overwrites any existing
// task with the same name so re-running setup updates the binary path.
//
// Why no /rl HIGHEST: the daemon's runtime needs are all unprivileged on
// Windows — binding low ports (UDP :53, TCP :7443) doesn't require admin
// (unlike POSIX, which gates ports < 1024 on uid=0), TLS hot-reload is a
// pure user-space cert swap, and child-process spawning works at any IL.
// All the privileged operations (netsh portproxy, certutil -addstore,
// adapter DNS repointing) happen during `vibe setup` itself; they persist
// in registry/system state and never need to be re-applied at runtime.
//
// Running the daemon elevated would mean every reverse-proxied dev server,
// every dashboard HTTP handler, and every managed child process inherits
// Administrator — turning any future bug in that surface into a privilege
// escalation. Keeping the task at medium IL is the strict-better default.
//
// /tr quoting note: the binary path comes from os.Executable() and is
// quote-wrapped so paths with spaces (e.g. "C:\Program Files\…") work.
// Windows file paths cannot contain `"` characters, so no escaping of
// `binary` is needed — but never substitute user-supplied data here.
func installScheduledTask() error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate binary: %w", err)
	}
	binary, _ = filepath.EvalSymlinks(binary)

	out, err := exec.Command(winutil.Sys32("schtasks"),
		"/create",
		"/tn", scheduledTaskName,
		"/tr", fmt.Sprintf(`"%s" serve`, binary),
		"/sc", "onlogon",
		"/f",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /create: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func flushDNS() error {
	// ipconfig /flushdns is best-effort: clear the resolver cache so the
	// adapter's new DNS server takes effect immediately. Failure is not a
	// hard stop — the cache will expire on its own within a minute or two.
	_ = exec.Command(winutil.Sys32("ipconfig"), "/flushdns").Run()
	return nil
}

func verifyDNSWindows() error {
	// We just changed the active adapter's DNS to 127.0.0.1. The daemon may
	// not be running yet (we'll start it momentarily), so this verification
	// hits our embedded resolver only if either the daemon is already up or
	// the system DNS cache happens to have an answer. Run the daemon
	// briefly via fork so we can confirm the wiring before exiting setup.
	if isDaemonRunning() {
		return verifyDNSAddrs()
	}
	// Best-effort: try a few times; if it fails, surface a clear next step
	// rather than failing setup outright. The Scheduled Task will start the
	// daemon at next logon regardless.
	var lastErr error
	for i := 0; i < 3; i++ {
		if err := verifyDNSAddrs(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "\n  (DNS verify deferred — daemon not running yet: %v)\n", lastErr)
	return nil
}

func verifyDNSAddrs() error {
	addrs, err := net.LookupHost("test.vibe")
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if a == "127.0.0.1" {
			return nil
		}
	}
	return fmt.Errorf("test.vibe resolved to %v, expected 127.0.0.1", addrs)
}

// openTTYPlatform on Windows: stdin is the closest analog of /dev/tty.
// promptYN's caller will get the "default yes" path when stdin isn't a
// terminal (e.g. piped input from a CI runner).
func openTTYPlatform() (*os.File, error) {
	return os.Stdin, nil
}
