//go:build darwin

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// macOS refuses to exec a file carrying com.apple.quarantine, failing with
// EPERM ("operation not permitted"). Anything that arrives from outside the
// machine gets the flag: a browser download, an unzipped archive, AirDrop, and
// every file-sync provider (Dropbox, Google Drive, iCloud, Synology, ...).
//
// That is invisible in a dev server's log, because the failure happens in the
// exec itself. Package runners make it worse by swallowing it: `bunx pkg` and
// `npx pkg` exec the shim in node_modules/.bin, and when that returns EPERM
// they exit non-zero having printed nothing at all. The route then fails with
// a bare "exit status 1" and a log holding only the shell's banner — no hint
// as to why.
//
// So this probe runs on the failure path when the log yields nothing: check
// whether the executables the command would have needed are quarantined, and
// say so. Keyed on the xattr, not on any provider or path — the flag is the
// actual cause, and where the files came from is only useful as context.

const quarantineAttr = "com.apple.quarantine"

// maxQuarantineChecks bounds the probe. It runs only after a start has already
// failed, but a node_modules tree can be enormous and there is no reason to
// walk it — .bin directories hold tens of entries, not thousands.
const maxQuarantineChecks = 400

// isQuarantined reports whether path carries the quarantine attribute, and the
// name of the agent that set it. The value is a semicolon-separated record:
// flags;timestamp;AgentName;uuid — field 3 is the agent (e.g. "Safari",
// "SynologyDriveFileProvider").
func isQuarantined(path string) (agent string, ok bool) {
	buf := make([]byte, 512)
	n, err := unix.Getxattr(path, quarantineAttr, buf)
	if err != nil || n <= 0 {
		return "", false
	}
	fields := strings.Split(string(buf[:n]), ";")
	if len(fields) >= 3 && strings.TrimSpace(fields[2]) != "" {
		return strings.TrimSpace(fields[2]), true
	}
	return "", true
}

// execCandidates returns the files a failed command plausibly needed to exec:
// the command's own leading path token (for `./run.sh` style commands) and the
// contents of any node_modules/.bin directory at the project root or one level
// down (client/, server/, packages/*, ...). Bounded by maxQuarantineChecks.
func execCandidates(dir, cmd string) []string {
	var out []string
	// A command frequently names the very shim that also shows up in
	// node_modules/.bin ("node_modules/.bin/vite --port $PORT"). Without
	// deduping, that one file is checked twice and reported as two.
	seen := map[string]bool{}
	add := func(p string) {
		if c, err := filepath.Abs(filepath.Clean(p)); err == nil {
			p = c
		}
		if seen[p] || len(out) >= maxQuarantineChecks {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// A command that names a path directly, e.g. "./scripts/dev.sh --port $PORT"
	// or ".venv/bin/python app.py".
	if f := strings.Fields(cmd); len(f) > 0 && strings.Contains(f[0], "/") {
		p := f[0]
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		add(p)
	}

	binDirs := []string{filepath.Join(dir, "node_modules", ".bin")}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			binDirs = append(binDirs, filepath.Join(dir, e.Name(), "node_modules", ".bin"))
		}
	}
	for _, bd := range binDirs {
		entries, err := os.ReadDir(bd)
		if err != nil {
			continue
		}
		for _, e := range entries {
			add(filepath.Join(bd, e.Name()))
			if len(out) >= maxQuarantineChecks {
				return out
			}
		}
	}
	return out
}

// scanQuarantinedExecutables returns a recovery hint when the command's
// executables are quarantined. Returns nil when nothing is quarantined, which
// is the overwhelmingly common case.
func scanQuarantinedExecutables(dir, cmd string) *Recovery {
	if dir == "" {
		return nil
	}
	agents := map[string]bool{}
	roots := map[string]bool{}
	count := 0
	for _, p := range execCandidates(dir, cmd) {
		agent, ok := isQuarantined(p)
		if !ok {
			continue
		}
		count++
		if agent != "" {
			agents[agent] = true
		}
		// Report the directory to clean, not every file. For a .bin entry that
		// is the enclosing node_modules; for a loose script, its own directory.
		root := filepath.Dir(p)
		if i := strings.LastIndex(root, string(filepath.Separator)+"node_modules"); i >= 0 {
			root = root[:i+len(string(filepath.Separator))+len("node_modules")]
		}
		roots[root] = true
	}
	if count == 0 {
		return nil
	}

	rootList := make([]string, 0, len(roots))
	for r := range roots {
		rootList = append(rootList, r)
	}
	sort.Strings(rootList)

	var by string
	if len(agents) > 0 {
		names := make([]string, 0, len(agents))
		for a := range agents {
			names = append(names, a)
		}
		sort.Strings(names)
		by = fmt.Sprintf(" (set by %s)", strings.Join(names, ", "))
	}

	plural := "file is"
	if count > 1 {
		plural = "files are"
	}
	return &Recovery{
		Action: "info",
		Message: fmt.Sprintf(
			"macOS is blocking execution: %d %s marked com.apple.quarantine%s, "+
				"so the command's executables can't run. Anything downloaded, unzipped, "+
				"AirDropped, or synced from a cloud provider carries this flag; package "+
				"runners such as bunx and npx hit it and exit without printing anything. "+
				"Clear it and retry:\n\nxattr -dr %s %s",
			count, plural, by, quarantineAttr, strings.Join(rootList, " ")),
	}
}
