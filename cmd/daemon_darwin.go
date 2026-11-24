//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// tryPlatformDaemonStart loads the LaunchAgent if it's installed. Returns
// (handled=true) when the LaunchAgent took over the lifecycle so the caller
// shouldn't fork a separate daemon process.
func tryPlatformDaemonStart() (bool, error) {
	agentPlist := launchAgentPlist()
	if _, err := os.Stat(agentPlist); err != nil {
		return false, nil
	}
	out, err := exec.Command(launchctlPath, "load", "-w", agentPlist).CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("launchctl load: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// tryPlatformDaemonStop unloads the LaunchAgent if installed.
func tryPlatformDaemonStop() (bool, error) {
	agentPlist := launchAgentPlist()
	if _, err := os.Stat(agentPlist); err != nil {
		return false, nil
	}
	out, err := exec.Command(launchctlPath, "unload", agentPlist).CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("launchctl unload: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
