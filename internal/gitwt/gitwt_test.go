package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"feature/auth":                  "feature-auth",
		"Feature/OAuth_2.0":             "feature-oauth-2-0",
		"bugfix-123":                    "bugfix-123",
		"--weird--":                     "weird",
		"///":                           "",
		"UPPER":                         "upper",
		"worktree-add-trouve-portfolio": "add-trouve-portfolio", // harness prefix stripped
		"worktree-":                     "worktree",             // bare prefix: nothing to strip to
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestListLinked(t *testing.T) {
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

	// No linked worktrees yet.
	wts, err := ListLinked(main)
	if err != nil {
		t.Fatalf("ListLinked: %v", err)
	}
	if len(wts) != 0 {
		t.Fatalf("ListLinked = %v; want empty (main checkout excluded)", wts)
	}

	wtDir := filepath.Join(t.TempDir(), "app-feat")
	git(main, "worktree", "add", "-b", "feature/x", wtDir)

	wts, err = ListLinked(main)
	if err != nil {
		t.Fatalf("ListLinked: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("ListLinked returned %d worktrees; want 1", len(wts))
	}
	// git resolves symlinks (macOS: /var → /private/var); compare real paths.
	wantPath, _ := filepath.EvalSymlinks(wtDir)
	gotPath, _ := filepath.EvalSymlinks(wts[0].Path)
	if gotPath != wantPath {
		t.Errorf("Path = %q; want %q", gotPath, wantPath)
	}
	if wts[0].Branch != "feature/x" {
		t.Errorf("Branch = %q; want feature/x", wts[0].Branch)
	}

	// A non-repo dir errors (or returns nothing) rather than panicking.
	if wts, err := ListLinked(t.TempDir()); err == nil && len(wts) != 0 {
		t.Errorf("non-repo ListLinked = %v; want error or empty", wts)
	}
}
