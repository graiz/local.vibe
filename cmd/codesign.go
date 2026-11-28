package cmd

import (
	"regexp"
	"strings"
)

// codesignIdentifier is the stable identifier `vibe dev` signs local builds
// with on macOS. With a real (Apple-issued) signing identity, macOS privacy
// grants — camera, Documents, Local Network, which it charges to vibe because
// vibe launches the dev servers — survive rebuilds. An ad-hoc signature
// (Go's default) is keyed by content hash, so every rebuild re-prompts.
const codesignIdentifier = "local.vibe"

// signBuild is signLocalBuild, swappable in tests.
var signBuild = signLocalBuild

var identityLine = regexp.MustCompile(`^\s*\d+\)\s+([0-9A-Fa-f]{40})\s+"([^":]+):`)

// pickSigningIdentity chooses the identity to sign with from `security
// find-identity -v -p codesigning` output. env is VIBE_CODESIGN_IDENTITY:
// "-" disables signing, any other value is used as-is. Otherwise prefer a
// Developer ID Application identity, then Apple Development. Returns the
// identity's SHA-1 (never its name — the build output shouldn't print whose
// account it is) and a label for the kind; "" when there is none.
func pickSigningIdentity(findIdentityOut, env string) (id, kind string) {
	switch env {
	case "-":
		return "", ""
	case "":
	default:
		return env, "VIBE_CODESIGN_IDENTITY"
	}
	found := map[string]string{}
	for _, line := range strings.Split(findIdentityOut, "\n") {
		if m := identityLine.FindStringSubmatch(line); m != nil {
			if _, dup := found[m[2]]; !dup {
				found[m[2]] = m[1]
			}
		}
	}
	for _, k := range []string{"Developer ID Application", "Apple Development"} {
		if h, ok := found[k]; ok {
			return h, k
		}
	}
	return "", ""
}

// codesignArgs is the codesign command line for a local build. No secure
// timestamp: a Developer ID signature asks Apple's timestamp server by
// default, which fails offline or behind a VPN — and a failed sign falls back
// to ad-hoc, re-triggering every privacy prompt. Local builds don't need one.
func codesignArgs(id, path string) []string {
	return []string{"--force", "--sign", id, "--identifier", codesignIdentifier, "--timestamp=none", path}
}
