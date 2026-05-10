//go:build windows

package winutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// powerShellPath returns the absolute path to powershell.exe under
// %SystemRoot%\System32. We don't search PATH because, like Sys32, this
// runs as Administrator during setup — a hijacked powershell.exe on PATH
// would be a privilege-escalation vector. Windows PowerShell 5.1 is
// guaranteed to live at this path on every Windows install since Win7.
//
// We deliberately don't fall back to PowerShell 7 (pwsh.exe). The cmdlets
// we use (Get-DnsClientServerAddress, Get-NetAdapter, Get-NetIPInterface)
// are part of Windows-shipped modules and behave identically under both
// shells; sticking to the System32-rooted Windows PowerShell keeps our
// trust assumption simple.
func powerShellPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

// PowerShellJSON runs `powershell.exe -NoProfile -Command <script>` and
// returns its stdout. Used to talk to Windows-shipped cmdlets (Get-NetAdapter,
// Get-DnsClientServerAddress, etc.) for locale-invariant structured data —
// the alternative is screen-scraping netsh output, which keys on
// English-language column headers and silently breaks on non-English Windows.
//
// Caller is responsible for piping its script through `ConvertTo-Json
// -Compress` so the output is deterministic and parseable. Errors include
// the trimmed stderr to make diagnosis easier.
func PowerShellJSON(script string) ([]byte, error) {
	cmd := exec.Command(powerShellPath(),
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", script,
	)
	out, err := cmd.Output()
	if err != nil {
		// Surface stderr if the exec failed with a non-zero exit; otherwise
		// the caller gets an opaque "exit status 1".
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("powershell: %w — %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("powershell: %w", err)
	}
	return out, nil
}
