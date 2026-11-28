package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoIdentities = `  1) 9BEB1EC9A86FBC3CB29E47C1746D310FBF9967D1 "Apple Development: Someone (ABCDE12345)"
  2) 1111111111111111111111111111111111111111 "Developer ID Application: Someone (ABCDE12345)"
     2 valid identities found
`

func TestPickSigningIdentity(t *testing.T) {
	cases := []struct {
		name, out, env, wantID, wantKind string
	}{
		{"prefers Developer ID", twoIdentities, "", "1111111111111111111111111111111111111111", "Developer ID Application"},
		{"Apple Development only", `  1) 9BEB1EC9A86FBC3CB29E47C1746D310FBF9967D1 "Apple Development: Someone (ABCDE12345)"
     1 valid identities found
`, "", "9BEB1EC9A86FBC3CB29E47C1746D310FBF9967D1", "Apple Development"},
		{"none found", "     0 valid identities found\n", "", "", ""},
		{"other kinds ignored", `  1) 2222222222222222222222222222222222222222 "Mac Developer Installer: Someone"
     1 valid identities found
`, "", "", ""},
		{"env override", twoIdentities, "ABCD", "ABCD", "VIBE_CODESIGN_IDENTITY"},
		{"env disables", twoIdentities, "-", "", ""},
	}
	for _, c := range cases {
		id, kind := pickSigningIdentity(c.out, c.env)
		if id != c.wantID || kind != c.wantKind {
			t.Errorf("%s: got (%q, %q); want (%q, %q)", c.name, id, kind, c.wantID, c.wantKind)
		}
	}
}

// Review: a Developer ID signature asks Apple's timestamp server by default,
// so `vibe dev` offline (or behind a VPN) failed to sign and re-triggered the
// privacy prompts. Local builds never need a secure timestamp.
func TestCodesignArgsSkipTimestamp(t *testing.T) {
	args := codesignArgs("ABC123", "/tmp/vibe")
	want := []string{"--force", "--sign", "ABC123", "--identifier", codesignIdentifier, "--timestamp=none", "/tmp/vibe"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("codesign args = %q; want %q", args, want)
	}
}

// A fresh install must be signed the same way `vibe dev` builds are: setup.sh
// and `vibe install` used a plain copy, so macOS re-prompted for privacy
// grants on every update until the user happened to run `vibe dev`.
func TestInstallSignsDestination(t *testing.T) {
	var signed []string
	prev := signBuild
	signBuild = func(p string) { signed = append(signed, p) }
	t.Cleanup(func() { signBuild = prev })

	dir := t.TempDir()
	src, dest := filepath.Join(dir, "vibe-src"), filepath.Join(dir, "bin", "vibe")
	if err := os.WriteFile(src, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installTo(src, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "binary" {
		t.Fatalf("dest = %q", got)
	}
	if len(signed) != 1 || signed[0] != dest {
		t.Fatalf("signed %v; want [%s]", signed, dest)
	}
}

func TestCodesignCommandSignsPath(t *testing.T) {
	var signed []string
	prev := signBuild
	signBuild = func(p string) { signed = append(signed, p) }
	t.Cleanup(func() { signBuild = prev })
	rootCmd.SetArgs([]string{"codesign", "/tmp/some-vibe"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(signed) != 1 || signed[0] != "/tmp/some-vibe" {
		t.Fatalf("signed %v", signed)
	}
}
