//go:build darwin

package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/vibeskill"
)

// installVibeSkillBestEffort runs the skill install as a labeled step but never
// fails setup — on error it prints a "skipped" note and returns. No-op when
// --no-skill was passed.
func installVibeSkillBestEffort() {
	if setupNoSkill {
		return
	}
	fmt.Printf("  %-54s", "Installing agent skill (~/.claude/skills/local-vibe)...")
	if err := installVibeSkill(); err != nil {
		fmt.Printf("skipped (%v)\n", err)
		return
	}
	fmt.Println("ok")
}

// installVibeSkill writes the global local.vibe agent skill into the real
// user's home (not root's, when invoked via sudo) and chowns the created tree
// back to that user so they can later edit or remove it. The files are
// world-readable regardless, so the user-mode daemon and coding agents can read
// them either way.
func installVibeSkill() error {
	home, err := realUserHome()
	if err != nil {
		return err
	}
	if _, err := vibeskill.InstallTo(home); err != nil {
		return err
	}
	// Best-effort chown of every directory level we may have created plus the
	// file itself. chownToUser no-ops when not running under sudo.
	chownToUser(filepath.Join(home, ".claude"))
	chownToUser(filepath.Join(home, ".claude", "skills"))
	chownToUser(filepath.Join(home, ".claude", "skills", vibeskill.SkillName))
	chownToUser(vibeskill.Path(home))
	return nil
}

// launchDaemonPlist is a root-owned LaunchDaemon that applies vibe's pf redirect
// at boot and re-applies it on every network change (WatchPaths on resolv.conf),
// so a VPN flushing pf doesn't silently break *.vibe HTTPS until reboot.
const launchDaemonPlist = "/Library/LaunchDaemons/com.vibe.pf.plist"

// launchAgentPlist is a user-level LaunchAgent that keeps the daemon running.
// System tools invoked while running as root are addressed by absolute
// path. `sudo vibe setup` keeps the caller's PATH on macOS, so a bare name
// would let a user-writable PATH entry run as root — the same hijack class
// winutil.Sys32 closes on Windows.
const (
	launchctlPath = "/bin/launchctl"
	securityPath  = "/usr/bin/security"
	sudoPath      = "/usr/bin/sudo"
)

func launchAgentPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.vibe.daemon.plist")
}

func setupPlatform() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("setup requires root — run: sudo vibe setup")
	}

	fmt.Println("Setting up local.vibe on macOS...")
	fmt.Println()

	steps := []setupStep{
		{"Checking for Homebrew", checkHomebrew},
		{"Installing dnsmasq", installDNSMasq},
		{"Configuring dnsmasq (*.vibe → 127.0.0.1)", configureDNSMasq},
		{"Creating /etc/resolver/vibe", createResolver},
		{"Starting dnsmasq as system service", startDNSMasq},
		{fmt.Sprintf("Installing pf forwarding rules (80→%d, 443→%d)", config.DefaultPort, config.DefaultTLSPort), installPFLaunchDaemon},
		{"Generating TLS certificates (*.vibe)", generateCerts},
		{"Trusting CA in macOS Keychain", trustCA},
		{"Enabling TLS in daemon config", enableTLSConfig},
		{"Installing daemon LaunchAgent (start at login)", installLaunchAgent},
		{"Verifying DNS resolution (test.vibe → 127.0.0.1)", verifyDNS},
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
	fmt.Println("Daemon starts automatically at login.")

	if promptYN("Start the daemon now and open https://local.vibe?") {
		return startDaemonAsUser()
	}
	fmt.Println()
	fmt.Println("When ready:  vibe daemon start")
	return nil
}

// brewCmd de-escalates from root to the original sudo user for brew operations.
func brewCmd(args ...string) *exec.Cmd {
	brewBin := brewBinary()
	if os.Getuid() == 0 {
		if u, ok := sudoUser(); ok {
			fullArgs := append([]string{"-u", u.Username, "-i", brewBin}, args...)
			return exec.Command(sudoPath, fullArgs...)
		}
	}
	return exec.Command(brewBin, args...)
}

func brewBinary() string {
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if path, err := exec.LookPath("brew"); err == nil {
		return path
	}
	return "brew"
}

func checkHomebrew() error {
	if _, err := os.Stat(brewBinary()); err != nil {
		if _, err2 := exec.LookPath("brew"); err2 != nil {
			return fmt.Errorf("Homebrew not found — install from https://brew.sh")
		}
	}
	return nil
}

func installDNSMasq() error {
	out, _ := brewCmd("list", "--formula", "dnsmasq").Output()
	if strings.TrimSpace(string(out)) != "" {
		return nil
	}
	cmd := brewCmd("install", "dnsmasq")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func configureDNSMasq() error {
	for _, confPath := range []string{
		"/opt/homebrew/etc/dnsmasq.conf",
		"/usr/local/etc/dnsmasq.conf",
	} {
		if _, err := os.Stat(confPath); err != nil {
			continue
		}
		data, _ := os.ReadFile(confPath)
		marker := "address=/.vibe/127.0.0.1"
		if strings.Contains(string(data), marker) {
			return nil
		}
		f, err := os.OpenFile(confPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(f, "\n# vibe\n%s\n", marker)
		f.Close()
		return err
	}
	return fmt.Errorf("dnsmasq.conf not found")
}

func createResolver() error {
	if err := os.MkdirAll("/etc/resolver", 0755); err != nil {
		return err
	}
	const content = "nameserver 127.0.0.1\n"
	existing, _ := os.ReadFile("/etc/resolver/vibe")
	if string(existing) == content {
		return nil
	}
	return os.WriteFile("/etc/resolver/vibe", []byte(content), 0644)
}

func startDNSMasq() error {
	brew := brewBinary()
	_ = exec.Command(brew, "services", "stop", "dnsmasq").Run()
	cmd := exec.Command(sudoPath, brew, "services", "start", "dnsmasq")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// pfHelperDir is a root-owned directory holding a root-owned copy of the vibe
// binary that the pf LaunchDaemon executes. See stagePFHelper.
const pfHelperDir = "/Library/Application Support/local.vibe"

// stagePFHelper copies the vibe binary to a root-owned, root-only-writable
// location and returns that path, so the root pf LaunchDaemon never executes a
// user-writable file.
//
// The LaunchDaemon runs as root at boot and on every network change. Pointing
// it at the install path (typically /opt/homebrew/bin/vibe, whose Homebrew
// prefix is user-owned) would let any process running as the user replace that
// binary and gain root code execution on the next VPN toggle — the same
// privilege-escalation class winutil.Sys32 guards against on Windows. Staging a
// root:wheel 0755 copy (only root can overwrite it) closes that surface.
//
// Refreshed on every `sudo vibe setup`. `vibe dev` rebuilds don't refresh it,
// but pf-apply is a stable, self-contained subcommand; a user who changes its
// behavior must re-run setup.
func stagePFHelper(src string) (string, error) {
	if err := os.MkdirAll(pfHelperDir, 0755); err != nil {
		return "", fmt.Errorf("create helper dir: %w", err)
	}
	// Lock the dir to root:wheel so a non-root user can't swap the helper via a
	// writable parent (setup runs as root; belt-and-suspenders if the dir
	// pre-existed with looser ownership).
	_ = os.Chown(pfHelperDir, 0, 0)
	_ = os.Chmod(pfHelperDir, 0755)

	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read binary: %w", err)
	}
	dst := filepath.Join(pfHelperDir, "vibe-pf")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0755); err != nil {
		return "", fmt.Errorf("write helper: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("install helper: %w", err)
	}
	_ = os.Chown(dst, 0, 0)
	_ = os.Chmod(dst, 0755)
	return dst, nil
}

// configuredDaemonPorts reads the daemon's HTTP and TLS ports from the REAL
// user's config, falling back to vibe's defaults. Setup runs as root, so the
// config has to be read from the invoking user's home — and the ports have to
// be baked into the pf plist as arguments, because `vibe pf-apply` also runs
// as root at boot and would otherwise look in root's home and silently use
// the defaults. A user on a non-default port would then get a redirect
// pointing at a dead port that `vibe doctor --fix` could never repair.
func configuredDaemonPorts() (httpPort, tlsPort int) {
	home, err := realUserHome()
	if err != nil {
		return config.DefaultPort, config.DefaultTLSPort
	}
	return configuredDaemonPortsIn(home)
}

// installPFLaunchDaemon writes vibe's pf anchor, references it from
// /etc/pf.conf, and installs a root LaunchDaemon that re-applies it at boot
// and on every network change. Port 80 and 443 forward to the daemon's
// configured HTTP/TLS ports. The daemon itself runs as the user — no root
// required at runtime.
func installPFLaunchDaemon() error {
	// Seed the anchor and the pf.conf reference up front. pf-apply does this
	// too (and re-does it if a macOS update rewrites pf.conf), but doing it
	// here means setup surfaces a bad custom pf.conf immediately rather than
	// leaving the user to discover a dead redirect later.
	httpPort, tlsPort := configuredDaemonPorts()
	pfHTTPPort, pfTLSPort = httpPort, tlsPort
	if err := writePFAnchorFile(); err != nil {
		return err
	}
	if _, err := ensurePFConfReferencesAnchor(); err != nil {
		// Not fatal: the rest of setup is still worth completing, and the user
		// can add two lines. Say exactly which.
		fmt.Printf("  ⚠ could not update %s: %v\n", pfConfPath, err)
		fmt.Printf("    add manually — first line in the translation section, second at the end:\n")
		fmt.Printf("      %s\n      %s\n", pfRdrAnchorLine, pfLoadLine)
	}

	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate binary: %w", err)
	}
	src, _ = filepath.EvalSymlinks(src)

	// Stage a root-owned copy and point the plist at it — never at the
	// user-writable install path (privilege escalation; see stagePFHelper).
	binary, err := stagePFHelper(src)
	if err != nil {
		return fmt.Errorf("stage pf helper: %w", err)
	}
	// ports already resolved above

	// The daemon runs `vibe pf-apply` (which idempotently merges vibe's redirect
	// into the live pf ruleset) at boot via RunAtLoad and on every network change
	// via WatchPaths on resolv.conf — macOS rewrites resolv.conf on VPN up/down,
	// which is exactly when a VPN's pf reload tends to flush our rules. Event
	// driven, no polling.
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.vibe.pf</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>pf-apply</string>
		<string>--http-port</string>
		<string>%d</string>
		<string>--tls-port</string>
		<string>%d</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>WatchPaths</key>
	<array>
		<string>/etc/resolv.conf</string>
		<string>/var/run/resolv.conf</string>
	</array>
	<key>StandardErrorPath</key>
	<string>/var/log/vibe-pf.log</string>
	<key>StandardOutPath</key>
	<string>/var/log/vibe-pf.log</string>
</dict>
</plist>
`, binary, httpPort, tlsPort)

	existing, _ := os.ReadFile(launchDaemonPlist)
	if string(existing) != plist {
		if err := os.WriteFile(launchDaemonPlist, []byte(plist), 0644); err != nil {
			return fmt.Errorf("write plist: %w", err)
		}
	}
	// launchctl load against an already-loaded label silently no-ops, so any
	// updated plist content wouldn't activate until reboot. Unload unconditionally
	// first (ignore error: not-loaded is fine) so the new content takes effect now.
	_ = exec.Command(launchctlPath, "unload", launchDaemonPlist).Run()
	out, err := exec.Command(launchctlPath, "load", "-w", launchDaemonPlist).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installLaunchAgent installs a user-level LaunchAgent that starts the daemon
// at login. No root required — runs as the current user on port 7999.
func installLaunchAgent() error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate binary: %w", err)
	}
	binary, _ = filepath.EvalSymlinks(binary)

	// When running as sudo, write the agent to the real user's LaunchAgents dir.
	home, agentDir := realUserHomeAndAgentDir()
	logPath := filepath.Join(home, ".vibe", "daemon.log")

	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.vibe.daemon</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, binary, home, logPath, logPath)

	agentPlist := filepath.Join(agentDir, "com.vibe.daemon.plist")

	// Unload existing agent before overwriting
	_ = exec.Command(launchctlPath, "unload", agentPlist).Run()

	if err := os.WriteFile(agentPlist, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write agent plist: %w", err)
	}
	return nil
}

func realUserHomeAndAgentDir() (home, agentDir string) {
	home, _ = realUserHome()
	return home, filepath.Join(home, "Library", "LaunchAgents")
}

// startDaemonAsUser runs `vibe daemon start` as the real (non-root) user.
// When setup is invoked via sudo, we must de-escalate so launchctl loads the
// LaunchAgent into the user's session, not root's.
func startDaemonAsUser() error {
	binary, _ := os.Executable()
	binary, _ = filepath.EvalSymlinks(binary)

	if os.Getuid() == 0 {
		if u, ok := sudoUser(); ok {
			cmd := exec.Command(sudoPath, "-u", u.Username, binary, "daemon", "start")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			return cmd.Run()
		}
	}

	return daemonStartCmd.RunE(nil, nil)
}

func verifyDNS() error {
	// Give dnsmasq a moment to become ready after restart.
	var lastErr error
	for i := 0; i < 5; i++ {
		addrs, err := net.LookupHost("test.vibe")
		if err == nil {
			for _, addr := range addrs {
				if addr == "127.0.0.1" {
					return nil
				}
			}
			return fmt.Errorf("test.vibe resolved to %v, expected 127.0.0.1", addrs)
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("DNS lookup failed for test.vibe: %w\nCheck that dnsmasq is running and /etc/resolver/vibe exists", lastErr)
}

func generateCerts() error {
	home, _ := realUserHome()
	vibeDir := filepath.Join(home, ".vibe")

	// Ensure ~/.vibe/ exists and is owned by the real user before any cert
	// work. Without this, cert.EnsureCA's MkdirAll creates ~/.vibe/ as root,
	// and the user-mode daemon (LaunchAgent) later can't write daemon.pid,
	// daemon.log, or the unix socket. See issues #2 and #5.
	if err := os.MkdirAll(vibeDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", vibeDir, err)
	}
	chownToUser(vibeDir)
	if err := generateCertsIn(home); err != nil {
		return err
	}
	// Chown cert files to real user when running as sudo
	chownCertsToUser(filepath.Join(vibeDir, "certs"))
	return nil
}

func trustCA() error {
	home, _ := realUserHome()
	return trustCAIn(home)
}

func enableTLSConfig() error {
	home, _ := realUserHome()
	cfgPath := filepath.Join(home, ".vibe", "config.json")
	if err := updateConfigJSON(cfgPath, func(daemon map[string]any) {
		daemon["tls"] = tlsConfigFor(home)
	}); err != nil {
		return err
	}

	chownToUser(cfgPath)
	return nil
}

// sudoUser returns the account that invoked `sudo vibe setup`, when there is
// one other than root. Everything setup writes into the user's home — certs,
// config, the LaunchAgent, the agent skill — resolves and chowns through it.
func sudoUser() (*user.User, bool) {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return nil, false
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, false
	}
	return u, true
}

// realUserHome is the invoking user's home: the sudo caller's under sudo,
// else the current user's.
func realUserHome() (string, error) {
	if u, ok := sudoUser(); ok {
		return u.HomeDir, nil
	}
	return os.UserHomeDir()
}

// chownToUser hands a path setup created as root back to the sudo caller.
// No-op when not running under sudo.
func chownToUser(path string) {
	u, ok := sudoUser()
	if !ok {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = os.Chown(path, uid, gid)
}

// chownCertsToUser is chownToUser over a directory and its direct entries.
func chownCertsToUser(dir string) {
	chownToUser(dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		chownToUser(filepath.Join(dir, e.Name()))
	}
}

func openTTYPlatform() (*os.File, error) {
	return os.Open("/dev/tty")
}
