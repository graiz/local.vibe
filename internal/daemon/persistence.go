package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type stickyStore struct {
	StickyRoutes map[string]*stickyEntry `json:"sticky_routes"`
}

// stickyEntry is a persisted route: the Route's own JSON shape (its runtime
// fields are tagged "-") plus the process-group leader PID of a running
// managed route. The PID is what lets a new daemon re-adopt the surviving
// child after a restart (managed children, in their own process group,
// outlive the daemon) instead of reporting the route stopped. A stale PID is
// harmless: adoption re-verifies that it is alive and still listening.
type stickyEntry struct {
	*Route
	PID int `json:"pid,omitempty"`
}

// loadStickyRoutes restores persisted routes (sticky, managed, bookmark)
// from ~/.vibe/routes.json on daemon startup. Entries whose names fail
// the validName regex are skipped — a hand-edited routes.json must not
// be able to inject `../` sequences that later flow into log or cert paths.
func loadStickyRoutes(table *RouteTable, dir string) error {
	path := filepath.Join(dir, "routes.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var store stickyStore
	if err := json.Unmarshal(data, &store); err != nil {
		// Move the file aside rather than leave it to be overwritten: the
		// daemon would otherwise start with an empty table and the next save
		// — any register, any favicon fetch — would replace every route the
		// user had with that empty table. A hand-edit with a trailing comma,
		// or a partial write from the file-sync provider hosting ~, is enough.
		bad := fmt.Sprintf("%s.bad-%d", path, time.Now().Unix())
		if renameErr := os.Rename(path, bad); renameErr == nil {
			return fmt.Errorf("%s is not valid JSON (%v); moved it to %s — fix and rename it back, then restart the daemon", path, err, bad)
		}
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	for name, entry := range store.StickyRoutes {
		if entry == nil || entry.Route == nil {
			continue
		}
		lower := strings.ToLower(name)
		parent, perr := parseRouteName(lower)
		if perr != nil || lower == "local" {
			continue
		}
		// A worktree removed while the daemon was down is dead — drop it at
		// load; the next save rewrites routes.json without it. The .git-link
		// probe matches worktreeDirGone: a leftover dir without it is gone.
		// A child that survived the restart (managed children outlive the
		// daemon on unix) must be killed here too — dropping the entry alone
		// would orphan a running server nobody tracks.
		if parent != "" && entry.Dir != "" && dirIsGoneWorktree(entry.Dir) {
			// Only kill a PID we can still prove is ours. The daemon may have
			// been down long enough for the OS to recycle it, and
			// killAdoptedProcess signals a whole process group — anchoring on
			// "this pid's group holds the port we registered" is the same
			// check adoptOrphan uses before claiming a survivor, and is what
			// keeps a stranger's process tree from being SIGTERM'd at startup.
			if entry.PID > 0 && processAlive(entry.PID) && pidGroupHoldsPort(entry.PID, entry.Port) {
				_ = killAdoptedProcess(entry.PID)
			}
			continue
		}
		// The decoded Route is the live one: the map key is authoritative
		// for its name, Parent is derived from that name, and the runtime
		// fields start fresh (they're tagged "-" and never persisted).
		r := entry.Route
		r.Name = lower
		r.Parent = parent
		r.ExpiresAt = nil // TTL routes are never persisted
		if r.Type == "" {
			r.Type = RouteSticky
		}
		// Managed routes start not running until launched (or re-adopted on
		// startup); others are assumed ready. The persisted PID is stashed so
		// the startup adoption pass can check whether the managed child
		// survived a `vibe daemon restart` — but Running stays false until that
		// check confirms the process is alive and listening.
		r.Running.Store(r.Type != RouteManaged)
		r.Ready.Store(r.Type != RouteManaged)
		if r.Type == RouteManaged && entry.PID > 0 {
			r.SetPID(entry.PID)
		}
		table.Add(r)
	}
	return nil
}

// saveStickyRoutes writes all persistent routes (sticky, managed, bookmark)
// to ~/.vibe/routes.json. Called after any route change.
func saveStickyRoutes(table *RouteTable, dir string) error {
	store := stickyStore{StickyRoutes: make(map[string]*stickyEntry)}
	for _, r := range table.List() {
		if r.Type == RouteSticky || r.Type == RouteManaged || r.Type == RouteBookmark {
			// Persist the PID only for managed routes the daemon believes are
			// running, so a restart can re-adopt the surviving child. Other
			// route types never own a process.
			var pid int
			if r.Type == RouteManaged && r.Running.Load() {
				if p, ok := r.PIDValue(); ok {
					pid = p
				}
			}
			store.StickyRoutes[r.Name] = &stickyEntry{Route: r, PID: pid}
		}
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Write-then-rename so a crash mid-write can't leave routes.json
	// truncated or half-written. The tmp file lives in the same dir so
	// rename stays on one filesystem and is atomic.
	target := filepath.Join(dir, "routes.json")
	tmp, err := os.CreateTemp(dir, "routes.json.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
