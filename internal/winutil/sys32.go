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
// Always returns an absolute path; never falls back to a bare name. The
// callers run as Administrator during setup, so a PATH search would be a
// privilege-escalation surface — a working directory or environment that
// shadows `netsh.exe` with a malicious binary would silently get exec'd
// otherwise. If the System32 binary genuinely doesn't exist (extremely
// unusual; would mean a broken Windows install), exec.Command surfaces a
// clean "no such file" error instead.
func Sys32(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name+".exe")
}
