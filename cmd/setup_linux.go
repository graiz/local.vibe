//go:build linux

package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/cert"
)

// File paths owned by `vibe setup` on Linux. Each is reversed by uninstall_linux.go.
const (
	nftRulesetPath          = "/etc/nftables.d/vibe.nft"
	nftServicePath          = "/etc/systemd/system/vibe-nft.service"
	vibeResolvedServicePath = "/etc/systemd/system/vibe-resolved.service"
	userUnitFilename        = "vibe.service"
	resolvedStubResolver    = "127.0.0.53"
	// linuxDNSListen is where the daemon's embedded DNS resolver binds.
	// Port 53 needs CAP_NET_BIND_SERVICE (which `vibe dev` erases on every
	// rebuild), so we use an unprivileged port and have systemd-resolved
	// forward .vibe queries here via per-link routing on `lo`.
	linuxDNSListen = "127.0.0.1:5354"
)

func setupPlatform() error {
	return setupLinux()
}

func setupLinux() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("setup requires root — run: sudo vibe setup")
	}
	if err := requireSystemd(); err != nil {
		return err
	}

	fmt.Println("Setting up local.vibe on Linux...")
	fmt.Println()

	err := runSteps([]setupStep{
		{"Routing .vibe DNS to 127.0.0.1:5354 (vibe0, systemd-resolved)", configureResolvedRouting},
		{"Installing nftables redirect (80→7999, 443→7443)", installNFTRules},
		{"Generating TLS certificates (*.vibe)", generateLinuxCerts},
		{"Trusting CA in system store", trustLinuxCA},
		{"Installing CA in user NSS db (Chrome/Chromium)", trustLinuxCANSS},
		{"Enabling TLS + embedded DNS in daemon config", enableLinuxDaemonConfig},
		{"Installing user systemd unit (start at login)", installUserUnit},
		{"Reloading user systemd + starting vibe.service", enableAndStartUserUnit},
		{"Waiting for daemon to bind 7999/5354", waitForLinuxDaemonReady},
		{"Verifying DNS resolution (test.vibe → 127.0.0.1)", verifyLinuxDNS},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nSetup failed: %v\n", err)
		return err
	}

	fmt.Println()
	fmt.Println("Setup complete. HTTPS enabled for all *.vibe domains.")
	fmt.Println("Daemon starts automatically at login via systemd --user.")
	if !resolvConfPointsAtStub() {
		fmt.Println()
		fmt.Println("Note: /etc/resolv.conf is not pointing at systemd-resolved's stub")
		fmt.Println("(127.0.0.53). Per-domain DNS routing only kicks in when it is. If")
		fmt.Println("*.vibe doesn't resolve in your browser, run:")
		fmt.Println("  sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf")
	}
	if !firefoxNoticeSuppressed() {
		fmt.Println()
		fmt.Println("Firefox note: visit about:config and set")
		fmt.Println("  security.enterprise_roots.enabled = true")
		fmt.Println("Firefox uses its own NSS store; this flag tells it to read the")
		fmt.Println("system root store where we installed the CA.")
	}

	if promptYN("Open https://local.vibe in your browser now?") {
		_, _ = runAsRealUserCombined("xdg-open", "https://local.vibe")
	}
	return nil
}

// requireSystemd refuses to proceed on non-systemd distros. Every step below
// depends on systemctl, so fail fast with a clear message rather than
// half-installing.
func requireSystemd() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd not detected (systemctl not in PATH). Linux setup currently requires a systemd-based distro")
	}
	return nil
}

// configureResolvedRouting routes .vibe DNS queries to the daemon on
// 127.0.0.1:5354 via per-link config on a dummy interface (vibe0).
//
// Per-link rather than a global [Resolve] drop-in: with our server in the
// global list, systemd-resolved races it against upstream resolvers in
// parallel and Cloudflare's NXDOMAIN wins. Per-link routing makes our
// server authoritative for .vibe.
//
// Dummy interface rather than `lo`: resolvectl refuses to configure DNS on
// the loopback ("Link lo is loopback device"). A dummy is a kernel virtual
// link we can own outright.
//
// 192.0.2.1/32 address: systemd-resolved only activates a DNS scope on a
// link that has an IP in that family — without it the link shows
// `Current Scopes: none` and DNS routing is silently skipped. 192.0.2.0/24
// is RFC 5737 TEST-NET-1; /32 means the address only, no route entry.
func configureResolvedRouting() error {
	resolvectl, err := exec.LookPath("resolvectl")
	if err != nil {
		return fmt.Errorf("systemd-resolved not installed (resolvectl missing) — please install it before running vibe setup")
	}
	ipBin, err := exec.LookPath("ip")
	if err != nil {
		return fmt.Errorf("iproute2 not installed (`ip` missing) — please install it before running vibe setup")
	}

	unit := fmt.Sprintf(`# Managed by vibe — do not edit.
[Unit]
Description=local.vibe DNS routing (.vibe → 127.0.0.1:5354 via vibe0)
After=systemd-resolved.service network-pre.target
Wants=systemd-resolved.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStartPre=-%s link delete dev vibe0
ExecStart=%s link add dev vibe0 type dummy
ExecStart=%s link set dev vibe0 up
ExecStart=%s addr add 192.0.2.1/32 dev vibe0
ExecStart=%s dns vibe0 %s
ExecStart=%s domain vibe0 ~vibe
ExecStop=%s link delete dev vibe0

[Install]
WantedBy=multi-user.target
`, ipBin, ipBin, ipBin, ipBin, resolvectl, linuxDNSListen, resolvectl, ipBin)

	if err := os.WriteFile(vibeResolvedServicePath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write %s: %w", vibeResolvedServicePath, err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w — %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "vibe-resolved.service").CombinedOutput(); err != nil {
		return fmt.Errorf("enable vibe-resolved.service: %w — %s", err, strings.TrimSpace(string(out)))
	}
	// `enable --now` is start-not-restart; if the unit was already active,
	// the ExecStart commands wouldn't re-fire. Restart forces the current
	// config to take effect.
	_ = exec.Command("systemctl", "restart", "vibe-resolved.service").Run()
	return nil
}

// installNFTRules writes the nft ruleset, installs a one-shot service to load
// it at boot, and applies it now. This mirrors the macOS pf LaunchDaemon —
// the daemon process itself stays unprivileged; only setup needs root.
func installNFTRules() error {
	nftPath, err := exec.LookPath("nft")
	if err != nil {
		return fmt.Errorf("nft not found — install nftables (e.g. `sudo apt install nftables` or `sudo pacman -S nftables`)")
	}

	if err := os.MkdirAll(filepath.Dir(nftRulesetPath), 0755); err != nil {
		return err
	}
	const ruleset = `#!/usr/sbin/nft -f
# Managed by vibe — do not edit. Removed by `+"`vibe uninstall`"+`.
# Redirects local browser traffic on :80/:443 to the daemon on :7999/:7443.

table inet vibe {
    chain output {
        type nat hook output priority dstnat; policy accept;
        ip daddr 127.0.0.1 tcp dport 80 redirect to :7999
        ip daddr 127.0.0.1 tcp dport 443 redirect to :7443
    }
}
`
	if err := os.WriteFile(nftRulesetPath, []byte(ruleset), 0644); err != nil {
		return err
	}

	serviceUnit := fmt.Sprintf(`# Managed by vibe — do not edit.
[Unit]
Description=local.vibe nftables redirect (80->7999, 443->7443)
After=network-pre.target
Before=network.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s -f %s
ExecStop=%s delete table inet vibe

[Install]
WantedBy=multi-user.target
`, nftPath, nftRulesetPath, nftPath)

	if err := os.WriteFile(nftServicePath, []byte(serviceUnit), 0644); err != nil {
		return err
	}

	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w — %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "vibe-nft.service").CombinedOutput(); err != nil {
		return fmt.Errorf("enable vibe-nft.service: %w — %s", err, strings.TrimSpace(string(out)))
	}
	// Restart, not start: if the unit was already active with a stale
	// ruleset, `start` is a no-op and the old rules stay loaded.
	_ = exec.Command("systemctl", "restart", "vibe-nft.service").Run()
	return nil
}

func generateLinuxCerts() error {
	home, _ := realLinuxUserHome()
	vibeDir := filepath.Join(home, ".vibe")
	certsDir := filepath.Join(vibeDir, "certs")

	// Create ~/.vibe owned by the real user before any cert work. Otherwise
	// MkdirAll under sudo leaves the directory unwritable by the user-mode
	// daemon and it can't drop its pidfile / socket on startup.
	if err := os.MkdirAll(vibeDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", vibeDir, err)
	}
	chownLinuxToUser(vibeDir)

	caCert, caKey, err := cert.EnsureCA(certsDir)
	if err != nil {
		return err
	}
	if _, _, err := cert.EnsureLeaf(certsDir, caCert, caKey, []string{"local.vibe"}); err != nil {
		return err
	}
	chownLinuxCertsToUser(certsDir)
	return nil
}

func trustLinuxCA() error {
	home, _ := realLinuxUserHome()
	return cert.TrustCA(filepath.Join(home, ".vibe", "certs"))
}

func trustLinuxCANSS() error {
	home, _ := realLinuxUserHome()
	certsDir := filepath.Join(home, ".vibe", "certs")
	if err := cert.TrustCAInUserNSS(certsDir, home); err != nil {
		return err
	}
	// certutil may have created ~/.pki as root if the directory didn't exist
	// when we started. Hand it back to the user.
	nssRoot := filepath.Join(home, ".pki")
	chownLinuxRecursive(nssRoot)
	return nil
}

// enableLinuxDaemonConfig writes the TLS and embedded-DNS sections into
// ~/.vibe/config.json. Unlike macOS (which uses dnsmasq for DNS and only
// needs TLS toggled), Linux drives DNS through the daemon's embedded
// resolver on an unprivileged port; systemd-resolved forwards .vibe queries
// to it via the per-link config on vibe0.
func enableLinuxDaemonConfig() error {
	home, _ := realLinuxUserHome()
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
	// Preserve any user-customized upstream; only overwrite enabled+listen.
	dns, _ := daemon["dns"].(map[string]interface{})
	if dns == nil {
		dns = make(map[string]interface{})
	}
	dns["enabled"] = true
	dns["listen"] = linuxDNSListen
	if _, ok := dns["upstream"]; !ok {
		dns["upstream"] = "8.8.8.8:53"
	}
	daemon["dns"] = dns
	cfgMap["daemon"] = daemon

	out, err := json.MarshalIndent(cfgMap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		return err
	}
	chownLinuxToUser(cfgPath)
	return nil
}

func installUserUnit() error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate vibe binary: %w", err)
	}
	binary, _ = filepath.EvalSymlinks(binary)

	home, uid, gid, _ := realLinuxUserInfo()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("create user unit dir: %w", err)
	}
	logPath := filepath.Join(home, ".vibe", "daemon.log")

	unit := fmt.Sprintf(`# Managed by vibe — do not edit.
[Unit]
Description=local.vibe daemon
After=network.target

[Service]
ExecStart=%s serve
Environment=HOME=%s
Restart=on-failure
RestartSec=2
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, binary, home, logPath, logPath)

	unitPath := filepath.Join(unitDir, userUnitFilename)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write %s: %w", unitPath, err)
	}
	// Hand back ownership of any directories we may have created as root.
	// Without this, `systemctl --user daemon-reload` fails to read its own
	// unit dir.
	for _, p := range []string{
		filepath.Join(home, ".config"),
		filepath.Join(home, ".config", "systemd"),
		unitDir,
		unitPath,
	} {
		_ = os.Chown(p, uid, gid)
	}
	return nil
}

// enableAndStartUserUnit reloads user systemd, enables vibe.service for
// next login, and restarts it so a re-run picks up the current binary and
// config. `restart` rather than `start`: if the unit is already active,
// `start` is a no-op and the stale daemon keeps running. All commands run
// as the real user — root's --user systemd instance is a separate one.
func enableAndStartUserUnit() error {
	if out, err := runAsRealUserCombined("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w — %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := runAsRealUserCombined("systemctl", "--user", "enable", userUnitFilename); err != nil {
		return fmt.Errorf("systemctl --user enable: %w — %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := runAsRealUserCombined("systemctl", "--user", "restart", userUnitFilename); err != nil {
		return fmt.Errorf("systemctl --user restart: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitForLinuxDaemonReady waits until both the HTTP API (7999) and the
// embedded DNS resolver (5354) are bound.
//
// Can't reuse the shared waitForDaemonReady: it reads ~/.vibe/daemon.pid
// via os.UserHomeDir(), which under sudo resolves to /root — while the
// daemon (started by systemctl --user) writes its pidfile to the real
// user's home. Direct socket probes are the only reliable signal here.
func waitForLinuxDaemonReady() error {
	deadline := time.Now().Add(15 * time.Second)
	httpUp, dnsUp := false, false
	for time.Now().Before(deadline) {
		if !httpUp {
			if c, err := net.DialTimeout("tcp", "127.0.0.1:7999", 200*time.Millisecond); err == nil {
				c.Close()
				httpUp = true
			}
		}
		if !dnsUp {
			if c, err := net.DialTimeout("udp", linuxDNSListen, 200*time.Millisecond); err == nil {
				c.Close()
				dnsUp = true
			}
		}
		if httpUp && dnsUp {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	switch {
	case !httpUp && !dnsUp:
		return fmt.Errorf("daemon did not bind 127.0.0.1:7999 or %s within 15s — check `systemctl --user status vibe.service` and ~/.vibe/daemon.log", linuxDNSListen)
	case !httpUp:
		return fmt.Errorf("daemon did not bind 127.0.0.1:7999 within 15s (DNS is up) — check ~/.vibe/daemon.log")
	default:
		return fmt.Errorf("daemon did not bind DNS on %s within 15s (HTTP is up) — check ~/.vibe/daemon.log for resolver listen errors", linuxDNSListen)
	}
}

func verifyLinuxDNS() error {
	// Give systemd-resolved a moment to apply the per-domain config.
	var lastErr error
	for i := 0; i < 5; i++ {
		addrs, err := net.LookupHost("test.vibe")
		if err == nil {
			for _, addr := range addrs {
				if addr == "127.0.0.1" {
					return nil
				}
			}
			return fmt.Errorf("test.vibe resolved to %v, expected 127.0.0.1 (the daemon's DNS server may be unreachable from systemd-resolved)", addrs)
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	hint := "check systemd-resolved status: `resolvectl status` should show the .vibe per-domain route"
	if !resolvConfPointsAtStub() {
		hint = "/etc/resolv.conf is not pointing at systemd-resolved (127.0.0.53), so per-domain routing won't kick in — fix with:\n  sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf"
	}
	return fmt.Errorf("DNS lookup failed for test.vibe: %w\n%s", lastErr, hint)
}

// runAsRealUserCombined runs cmd as the SUDO_USER with XDG_RUNTIME_DIR set so
// `systemctl --user` and other user-session-aware tools find the right
// session manager. Falls back to direct exec when no sudo wrapping is in
// effect (CI, manual `sudo -E`).
func runAsRealUserCombined(name string, args ...string) ([]byte, error) {
	_, uid, _, username := realLinuxUserInfo()
	if username == "" || os.Getuid() != 0 {
		return exec.Command(name, args...).CombinedOutput()
	}
	xdg := fmt.Sprintf("/run/user/%d", uid)
	full := append([]string{
		"-u", username,
		"env",
		"XDG_RUNTIME_DIR=" + xdg,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(xdg, "bus"),
		name,
	}, args...)
	return exec.Command("sudo", full...).CombinedOutput()
}

// resolvConfPointsAtStub returns true when /etc/resolv.conf is wired up to
// systemd-resolved's stub (127.0.0.53). Per-domain DNS routing requires this.
func resolvConfPointsAtStub() bool {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), resolvedStubResolver)
}

// firefoxNoticeSuppressed returns true when no Firefox profile exists, so we
// don't nag users who don't have Firefox installed about a setting they don't
// need.
func firefoxNoticeSuppressed() bool {
	home, _ := realLinuxUserHome()
	if home == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(home, ".mozilla", "firefox"))
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.IsDir() && strings.Contains(e.Name(), "default") {
			return false
		}
	}
	return true
}

// realLinuxUserHome returns SUDO_USER's home directory when running under
// sudo, the current user's home otherwise.
func realLinuxUserHome() (string, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		if u, err := user.Lookup(sudoUser); err == nil {
			return u.HomeDir, nil
		}
	}
	return os.UserHomeDir()
}

// realLinuxUserInfo returns home, uid, gid, and username for the real user.
// When not under sudo, returns the current user. Username is empty when
// lookup fails — callers should treat that as "stay as current user."
func realLinuxUserInfo() (home string, uid, gid int, username string) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		if u, err := user.Lookup(sudoUser); err == nil {
			home = u.HomeDir
			uid, _ = strconv.Atoi(u.Uid)
			gid, _ = strconv.Atoi(u.Gid)
			username = u.Username
			return
		}
	}
	u, err := user.Current()
	if err == nil {
		home = u.HomeDir
		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
		username = u.Username
	} else {
		home, _ = os.UserHomeDir()
		uid = os.Getuid()
		gid = os.Getgid()
	}
	return
}

func chownLinuxToUser(path string) {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" || sudoUser == "root" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = os.Chown(path, uid, gid)
}

func chownLinuxCertsToUser(dir string) {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" || sudoUser == "root" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = os.Chown(dir, uid, gid)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		_ = os.Chown(filepath.Join(dir, e.Name()), uid, gid)
	}
}

// chownLinuxRecursive walks a tree and chowns each entry. Used for NSS
// where certutil creates ~/.pki/nssdb with several files.
func chownLinuxRecursive(root string) {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" || sudoUser == "root" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chown(path, uid, gid)
		}
		return nil
	})
}

func openTTYPlatform() (*os.File, error) {
	return os.Open("/dev/tty")
}
