package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
