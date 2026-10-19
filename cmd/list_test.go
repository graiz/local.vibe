package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/client"
)

func TestDisplayPath(t *testing.T) {
	home := filepath.FromSlash("/Users/greg")
	sep := string(filepath.Separator)
	cases := []struct{ in, want string }{
		{"", ""},
		{home, "~"},
		{filepath.FromSlash("/Users/greg/dev/app"), "~" + sep + filepath.FromSlash("dev/app")},
		// A sibling whose name merely starts with home is not under it.
		{filepath.FromSlash("/Users/gregory/app"), filepath.FromSlash("/Users/gregory/app")},
		{filepath.FromSlash("/opt/app"), filepath.FromSlash("/opt/app")},
	}
	for _, c := range cases {
		if got := displayPath(c.in, home); got != c.want {
			t.Errorf("displayPath(%q) = %q; want %q", c.in, got, c.want)
		}
	}
	if got := displayPath(filepath.FromSlash("/x/y"), ""); got != filepath.FromSlash("/x/y") {
		t.Errorf("empty home: got %q", got)
	}
}

func TestWriteRouteTableLong(t *testing.T) {
	now := time.Now()
	home := filepath.FromSlash("/Users/greg")
	routes := []client.RouteInfo{
		{Name: "dialt", URL: "https://dialt.vibe", Port: 3012, Type: "managed", Running: true, Ready: true,
			RegisteredAt: now, Dir: filepath.FromSlash("/Users/greg/dev/dialit"), Cmd: "node server.js"},
		{Name: "api", URL: "https://api.vibe", Port: 4000, Type: "sticky", Running: true, Ready: true, RegisteredAt: now},
	}
	wts := []client.WorktreeInfo{{Name: "fix.dialt", URL: "https://fix.dialt.vibe", Branch: "fix", Path: filepath.FromSlash("/Users/greg/dev/dialit-fix")}}

	var short bytes.Buffer
	if err := writeRouteTable(&short, routes, wts, false, home, now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(short.String(), "DIR") || strings.Contains(short.String(), "node server.js") {
		t.Errorf("default listing must not include long columns:\n%s", short.String())
	}

	var long bytes.Buffer
	if err := writeRouteTable(&long, routes, wts, true, home, now); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(long.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 3 rows, got %d:\n%s", len(lines), long.String())
	}
	if f := strings.Fields(lines[0]); strings.Join(f[len(f)-2:], " ") != "DIR CMD" {
		t.Errorf("header missing DIR/CMD: %q", lines[0])
	}
	dialit := "~" + string(filepath.Separator) + filepath.FromSlash("dev/dialit")
	if !strings.Contains(lines[1], dialit) || !strings.HasSuffix(lines[1], "node server.js") {
		t.Errorf("dialt row: %q", lines[1])
	}
	if f := strings.Fields(lines[2]); strings.Join(f[len(f)-2:], " ") != "— —" {
		t.Errorf("sticky row should dash DIR and CMD: %q", lines[2])
	}
	if !strings.Contains(lines[3], filepath.FromSlash("dev/dialit-fix")) {
		t.Errorf("worktree row should show its path: %q", lines[3])
	}
}

// `vibe ls -la` must parse: pflag combines boolean shorthands.
func TestListFlagsCombine(t *testing.T) {
	defer func() { listLong, listAll = false, false }()
	if err := listCmd.Flags().Parse([]string{"-la"}); err != nil {
		t.Fatalf("parse -la: %v", err)
	}
	if !listLong || !listAll {
		t.Errorf("-la: long=%v all=%v; want both true", listLong, listAll)
	}
}
