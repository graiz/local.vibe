//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"os/exec"
)

// signLocalBuild signs a freshly built binary with the machine's own
// code-signing identity, if it has one (see pickSigningIdentity), so macOS
// keeps vibe's privacy grants across rebuilds. No identity, or a signing
// failure, leaves Go's ad-hoc signature in place — the build still works.
func signLocalBuild(path string) {
	out, _ := exec.Command("/usr/bin/security", "find-identity", "-v", "-p", "codesigning").Output()
	id, kind := pickSigningIdentity(string(out), os.Getenv("VIBE_CODESIGN_IDENTITY"))
	if id == "" {
		return
	}
	cmd := exec.Command("/usr/bin/codesign", codesignArgs(id, path)...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't sign the build (%v): %s — macOS may re-ask for permissions\n", err, msg)
		return
	}
	fmt.Printf("signed with %s identity (privacy permissions persist across rebuilds)\n", kind)
}
