package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A worktree's vibe.json is a copy, and agents commonly "helpfully" rename the
// app in it to avoid a collision that vibe already prevents. The rename breaks
// the parent link — no dashboard grouping, no picker entry, no inherited
// oauth_callback_port — so the app identity is taken from the main checkout.
func TestAppNameComesFromMainCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(main, "init")
	if err := os.WriteFile(filepath.Join(main, "vibe.json"), []byte(`{"name":"edge","cmd":"npm run dev"}`), 0644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", ".")
	git(main, "commit", "-m", "init")

	wtDir := filepath.Join(t.TempDir(), "wt")
	git(main, "worktree", "add", "-b", "feature/x", wtDir)
	// The renamed copy, as an agent would leave it.
	if err := os.WriteFile(filepath.Join(wtDir, "vibe.json"), []byte(`{"name":"edge-wt","cmd":"npm run dev"}`), 0644); err != nil {
		t.Fatal(err)
	}

	gotMain, err := filepath.EvalSymlinks(mainCheckoutDir(wtDir))
	if err != nil {
		t.Fatal(err)
	}
	wantMain, _ := filepath.EvalSymlinks(main)
	if gotMain != wantMain {
		t.Errorf("mainCheckoutDir = %q; want %q", gotMain, wantMain)
	}

	if got := vibeJSONName(main); got != "edge" {
		t.Errorf("vibeJSONName(main) = %q; want edge", got)
	}
	// The worktree's own copy still reports its (renamed) value — the caller
	// is what prefers the main checkout.
	if got := vibeJSONName(wtDir); got != "edge-wt" {
		t.Errorf("vibeJSONName(worktree) = %q; want edge-wt", got)
	}

	// Missing or unreadable vibe.json yields "" so the caller can fall back.
	if got := vibeJSONName(t.TempDir()); got != "" {
		t.Errorf("vibeJSONName(empty dir) = %q; want \"\"", got)
	}
	if got := mainCheckoutDir(t.TempDir()); got != "" {
		t.Errorf("mainCheckoutDir(non-repo) = %q; want \"\"", got)
	}
}

func TestDetectWorktreeAndSlug(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(main, "init")
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", ".")
	git(main, "commit", "-m", "init")

	wtDir := filepath.Join(t.TempDir(), "myapp-feature-auth")
	git(main, "worktree", "add", "-b", "feature/auth", wtDir)

	if detectWorktree(main) {
		t.Errorf("main checkout detected as worktree")
	}
	if !detectWorktree(wtDir) {
		t.Errorf("linked worktree not detected")
	}
	if got := worktreeSlug(wtDir); got != "feature-auth" {
		t.Errorf("worktreeSlug = %q; want feature-auth", got)
	}
	if detectWorktree(t.TempDir()) {
		t.Errorf("non-repo dir detected as worktree")
	}
}
