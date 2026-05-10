//go:build windows

// Package winutil holds small Windows-only helpers that don't fit cleanly
// into any of the feature packages and would otherwise be duplicated.
package winutil

import (
	"os"
	"path/filepath"
)

// Sys32 resolves a System32-shipped Windows tool to its absolute path. Used
// for certutil/netsh/ipconfig/schtasks/netstat/tasklist — anything that's
// part of Windows itself and lives at %SystemRoot%\System32\<name>.exe.
//
// Some environments (sanitized PowerShell profiles, processes spawned with
// stripped env, tools launched from bash-style shells with a POSIX PATH)
// don't have System32 on PATH. Resolving by absolute path makes setup
// robust regardless. Fall back to the bare name so exec.LookPath still has
// a chance if the file isn't where we expected.
func Sys32(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	abs := filepath.Join(root, "System32", name+".exe")
	if _, err := os.Stat(abs); err == nil {
		return abs
	}
	return name
}
