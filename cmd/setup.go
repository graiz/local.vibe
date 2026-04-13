package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/localvibe/vibe/internal/cert"
	"github.com/spf13/cobra"
)

// launchDaemonPlist is a root-owned LaunchDaemon that only applies pf rules at boot.
const launchDaemonPlist = "/Library/LaunchDaemons/com.vibe.pf.plist"

// launchAgentPlist is a user-level LaunchAgent that keeps the daemon running.
func launchAgentPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.vibe.daemon.plist")
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure DNS and autostart (one-time, requires sudo)",
	Long: `Sets up dnsmasq so *.vibe resolves to 127.0.0.1, installs a port
forwarding rule (80 → 7999), and registers a LaunchAgent so the daemon
starts automatically at login.

This command is idempotent — safe to run multiple times.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		switch runtime.GOOS {
		case "darwin":
			return setupMacOS()
		case "linux":
			return setupLinux()
		default:
			fmt.Printf("Automatic setup is not supported on %s.\n", runtime.GOOS)
			return nil
		}
	},
}

type setupStep struct {
	name string
	fn   func() error
}

func runSteps(steps []setupStep) error {
	for _, step := range steps {
		fmt.Printf("  %-54s", step.name+"...")
		if err := step.fn(); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("%s: %w", step.name, err)
		}
		fmt.Println("ok")
	}
	return nil
}

func setupMacOS() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("setup requires root — run: sudo vibe setup")
	}

	fmt.Println("Setting up local.vibe on macOS...")
	fmt.Println()

	err := runSteps([]setupStep{
		{"Checking for Homebrew", checkHomebrew},
		{"Installing dnsmasq", installDNSMasq},
		{"Configuring dnsmasq (*.vibe → 127.0.0.1)", configureDNSMasq},
		{"Creating /etc/resolver/vibe", createResolver},
		{"Starting dnsmasq as system service", startDNSMasq},
		{"Installing pf forwarding rules (80→7999, 443→7443)", installPFLaunchDaemon},
		{"Generating TLS certificates (*.vibe)", generateCerts},
		{"Trusting CA in macOS Keychain", trustCA},
		{"Enabling TLS in daemon config", enableTLSConfig},
		{"Installing daemon LaunchAgent (start at login)", installLaunchAgent},
		{"Verifying DNS resolution (test.vibe → 127.0.0.1)", verifyDNS},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nSetup failed: %v\n", err)
		return err
	}

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
		sudoUser := os.Getenv("SUDO_USER")
		if sudoUser == "" {
			if u, err := user.Current(); err == nil {
				sudoUser = u.Username
			}
		}
		if sudoUser != "" && sudoUser != "root" {
			fullArgs := append([]string{"-u", sudoUser, "-i", brewBin}, args...)
			return exec.Command("sudo", fullArgs...)
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
	cmd := exec.Command("sudo", brew, "services", "start", "dnsmasq")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// installPFLaunchDaemon installs a root LaunchDaemon that applies pf rules
// forwarding port 80 → 7999 and port 443 → 7443 at each boot. The daemon
// itself runs as the user — no root required at runtime.
func installPFLaunchDaemon() error {
	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.vibe.pf</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>printf 'rdr pass on lo0 proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port 7999\nrdr pass on lo0 proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port 7443\npass all\n' | /sbin/pfctl -ef -</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>/var/log/vibe-pf.log</string>
</dict>
</plist>
`
	existing, _ := os.ReadFile(launchDaemonPlist)
	if string(existing) == plist {
		// Already installed — reload to apply immediately
		_ = exec.Command("launchctl", "unload", launchDaemonPlist).Run()
	} else {
		if err := os.WriteFile(launchDaemonPlist, []byte(plist), 0644); err != nil {
			return fmt.Errorf("write plist: %w", err)
		}
	}
	out, err := exec.Command("launchctl", "load", "-w", launchDaemonPlist).CombinedOutput()
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
	_ = exec.Command("launchctl", "unload", agentPlist).Run()

	if err := os.WriteFile(agentPlist, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write agent plist: %w", err)
	}
	return nil
}

func realUserHomeAndAgentDir() (home, agentDir string) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		if u, err := user.Lookup(sudoUser); err == nil {
			home = u.HomeDir
			agentDir = filepath.Join(home, "Library", "LaunchAgents")
			return
		}
	}
	home, _ = os.UserHomeDir()
	agentDir = filepath.Join(home, "Library", "LaunchAgents")
	return
}

// startDaemonAsUser runs `vibe daemon start` as the real (non-root) user.
// When setup is invoked via sudo, we must de-escalate so launchctl loads the
// LaunchAgent into the user's session, not root's.
func startDaemonAsUser() error {
	binary, _ := os.Executable()
	binary, _ = filepath.EvalSymlinks(binary)

	if os.Getuid() == 0 {
		sudoUser := os.Getenv("SUDO_USER")
		if sudoUser != "" && sudoUser != "root" {
			cmd := exec.Command("sudo", "-u", sudoUser, binary, "daemon", "start")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			return cmd.Run()
		}
	}

	return daemonStartCmd.RunE(nil, nil)
}

func promptYN(question string) bool {
	// Re-open /dev/tty directly so the prompt works even under sudo
	// (sudo may redirect stdin away from the terminal).
	tty, err := os.Open("/dev/tty")
	if err != nil {
		// Truly non-interactive (pipe, CI) — default yes, say so.
		fmt.Printf("\n%s — defaulting to yes\n", question)
		return true
	}
	defer tty.Close()

	fmt.Printf("\n%s [Y/n] ", question)
	scanner := bufio.NewScanner(tty)
	if !scanner.Scan() {
		return true
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "" || answer == "y" || answer == "yes"
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
	certsDir := filepath.Join(home, ".vibe", "certs")

	caCert, caKey, err := cert.EnsureCA(certsDir)
	if err != nil {
		return err
	}
	// Generate initial cert with local.vibe — daemon will regenerate with
	// actual route names on startup and whenever routes change.
	if _, _, err := cert.EnsureLeaf(certsDir, caCert, caKey, []string{"local.vibe"}); err != nil {
		return err
	}

	// Chown cert files to real user when running as sudo
	chownCertsToUser(certsDir)
	return nil
}

func trustCA() error {
	home, _ := realUserHome()
	certsDir := filepath.Join(home, ".vibe", "certs")
	return cert.TrustCA(certsDir)
}

func enableTLSConfig() error {
	home, _ := realUserHome()
	cfgPath := filepath.Join(home, ".vibe", "config.json")

	// Load existing config as a map to preserve unknown fields
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
	cfgMap["daemon"] = daemon

	out, err := json.MarshalIndent(cfgMap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		return err
	}

	chownToUser(cfgPath)
	return nil
}

func realUserHome() (string, error) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		if u, err := user.Lookup(sudoUser); err == nil {
			return u.HomeDir, nil
		}
	}
	return os.UserHomeDir()
}

func chownCertsToUser(dir string) {
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

func chownToUser(path string) {
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

func setupLinux() error {
	fmt.Println("Linux setup:")
	fmt.Println()
	fmt.Println("1. Install and configure dnsmasq:")
	fmt.Println("     sudo apt install dnsmasq")
	fmt.Println("     echo 'address=/.vibe/127.0.0.1' | sudo tee -a /etc/dnsmasq.conf")
	fmt.Println("     sudo systemctl restart dnsmasq")
	fmt.Println()
	fmt.Println("2. Port 80 forwarding:")
	fmt.Println("     sudo iptables -t nat -A OUTPUT -p tcp -d 127.0.0.1 --dport 80 -j REDIRECT --to-port 7999")
	fmt.Println()
	fmt.Println("3. Autostart (systemd user service):")
	fmt.Println("     systemctl --user enable --now vibe")
	return nil
}
