package cmd

import (
	"io"
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
//
// And it must never rewrite the installed file in place: the daemon runs from
// that path, and modifying a running signed Mach-O gets it (and the next exec)
// killed. So: copy to a temp file, sign that, rename over dest.
func TestInstallSignsThenSwaps(t *testing.T) {
	dir := t.TempDir()
	src, dest := filepath.Join(dir, "vibe-src"), filepath.Join(dir, "bin", "vibe")
	if err := os.WriteFile(src, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	running, err := os.Open(dest) // the daemon's view of its own binary
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	var signed []string
	prev := signBuild
	signBuild = func(p string) {
		signed = append(signed, p)
		if got, _ := os.ReadFile(dest); string(got) != "old" {
			t.Errorf("dest already replaced (%q) before signing finished", got)
		}
		if got, _ := os.ReadFile(p); string(got) != "new" {
			t.Errorf("signed file holds %q; want the new build", got)
		}
	}
	t.Cleanup(func() { signBuild = prev })

	if err := installTo(src, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Fatalf("dest = %q; want new", got)
	}
	if old, _ := io.ReadAll(running); string(old) != "old" {
		t.Fatalf("the running binary's file was rewritten in place: reads %q", old)
	}
	if len(signed) != 1 || signed[0] == dest || filepath.Dir(signed[0]) != filepath.Dir(dest) {
		t.Fatalf("signed %v; want one temp file beside %s", signed, dest)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), "*.tmp*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestCodesignCommandSignsPath(t *testing.T) {
	var signed []string
	prev := signBuild
	signBuild = func(p string) { signed = append(signed, p) }
	t.Cleanup(func() { signBuild = prev })
	rootCmd.SetArgs([]string{"codesign", "/tmp/some-vibe"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) }) // rootCmd is shared by the package's tests
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(signed) != 1 || signed[0] != "/tmp/some-vibe" {
		t.Fatalf("signed %v", signed)
	}
}
