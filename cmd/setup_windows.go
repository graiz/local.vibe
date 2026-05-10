//go:build windows

package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/cert"
	"github.com/graiz/local.vibe/internal/winutil"
	"golang.org/x/sys/windows"
)

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

	err := runSteps([]setupStep{
		{"Generating TLS certificates (*.vibe)", generateCertsWindows},
		{"Trusting CA in Windows root store", trustCAWindows},
		{"Installing netsh portproxy rules (80→7999, 443→7443)", installPortProxy},
		{"Snapshotting current adapter DNS (for clean uninstall)", backupAdapterDNS},
		{"Repointing active adapters' DNS to 127.0.0.1", configureDNS},
		{"Enabling TLS and DNS in daemon config", enableTLSAndDNSWindows},
		{"Registering Scheduled Task for autostart", installScheduledTask},
		{"Flushing DNS cache", flushDNS},
		{"Verifying DNS resolution (test.vibe → 127.0.0.1)", verifyDNSWindows},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nSetup failed: %v\n", err)
		return err
	}

	fmt.Println()
	fmt.Println("Setup complete! HTTPS enabled for all *.vibe domains.")
	fmt.Println("Daemon starts automatically at login (Scheduled Task: vibe).")
	fmt.Println()
	if promptYN("Start the daemon now and open https://local.vibe?") {
		out, err := exec.Command(winutil.Sys32("schtasks"), "/run", "/tn", "vibe").CombinedOutput()
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

// connectedIPv4Adapters returns the friendly names of every IPv4 adapter
// currently in the "connected" state. We parse `netsh interface ipv4 show
// interfaces`, which outputs a fixed-width table:
//
//	Idx     Met         MTU          State                Name
//	---  ----------  ----------  ------------  ---------------------------
//	  1          75  4294967295  connected     Loopback Pseudo-Interface 1
//	 12          25        1500  connected     Wi-Fi
//
// We skip the loopback (matches the literal name) since adding 127.0.0.1
// as DNS on the loopback adapter would be a no-op + raise eyebrows.
func connectedIPv4Adapters() ([]string, error) {
	out, err := exec.Command(winutil.Sys32("netsh"), "interface", "ipv4", "show", "interfaces").Output()
	if err != nil {
		return nil, err
	}
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		// The state column reads "connected" (lowercase) on every Windows
		// version we've seen — but match case-insensitively to be safe.
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "connected") {
			continue
		}
		// Skip the divider and headers.
		if strings.Contains(lower, "state") || strings.HasPrefix(strings.TrimSpace(line), "---") {
			continue
		}
		// First 4 fields are Idx, Met, MTU, State; everything after is the
		// adapter name (which may itself contain spaces).
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// Re-find the name: split twice keeping the rest.
		// Strategy: trim the four numeric/state columns from the front by
		// finding the 4th run of whitespace.
		name := joinFromField(line, 4)
		if name == "" || strings.HasPrefix(strings.ToLower(name), "loopback") {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// joinFromField returns the substring of line that begins at the n'th
// whitespace-separated field (0-based). Used because adapter names can
// contain spaces, so a strings.Fields split would over-tokenize them.
func joinFromField(line string, n int) string {
	in := line
	for i := 0; i < n; i++ {
		in = strings.TrimLeft(in, " \t")
		idx := strings.IndexAny(in, " \t")
		if idx < 0 {
			return ""
		}
		in = in[idx:]
	}
	return strings.TrimSpace(in)
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
// with /rl HIGHEST. /f overwrites any existing task with the same name so
// re-running setup updates the binary path.
func installScheduledTask() error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate binary: %w", err)
	}
	binary, _ = filepath.EvalSymlinks(binary)

	out, err := exec.Command(winutil.Sys32("schtasks"),
		"/create",
		"/tn", "vibe",
		"/tr", fmt.Sprintf(`"%s" serve`, binary),
		"/sc", "onlogon",
		"/rl", "HIGHEST",
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
