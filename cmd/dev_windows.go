//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// defaultVibeInstallPath is the fallback when `where vibe` fails.
// Matches setup.ps1's install location.
func defaultVibeInstallPath() string {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return filepath.Join(local, "Programs", "vibe", "vibe.exe")
	}
	return `C:\Program Files\vibe\vibe.exe`
}

// replaceVibeBinary swaps in a freshly-built vibe.exe. On Windows we have
// two file locks to work around:
//
//  1. The running daemon (a separate `vibe serve` process) holds an image
//     handle on vibe.exe.
//  2. THIS process — running `vibe dev` — also runs from the very vibe.exe
//     we want to replace, and so holds its own image handle on it.
//
// Windows refuses to overwrite or delete an .exe with an open image handle
// ("Access is denied" on os.Rename). It DOES allow the file to be renamed
// to a new path, which is the trick every Windows self-updater uses:
//
//	a. stop the daemon (releases handle #1)
//	b. rename current vibe.exe → vibe.exe.old (releases the path; the .old
//	   file stays in use by us until we exit, but the path "vibe.exe" is
//	   now free)
//	c. rename vibe.exe.tmp → vibe.exe
//	d. start the daemon (it loads the new binary)
//
// The .old file lingers until next dev run because we're still executing
// from it; cleanStaleOldBinaries() at the top of replaceVibeBinary mops
// up leftovers from previous dev invocations.
func replaceVibeBinary(srcDir, binary string) error {
	cleanStaleOldBinaries(binary)

	tmpBin := binary + ".tmp"
	build := exec.Command("go", "build", "-o", tmpBin, ".")
	build.Dir = srcDir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		os.Remove(tmpBin)
		return fmt.Errorf("build failed: %w", err)
	}

	// Best-effort stop. If the daemon wasn't running, rename will still work.
	stopRunningDaemon()

	// Wait briefly for the OS to release the daemon's file handle. The
	// rename-to-.old can race with a still-shutting-down daemon; a short
	// retry loop is friendlier than asking the user to re-run dev.
	oldBin := binary + ".old." + fmt.Sprintf("%d", time.Now().UnixNano())
	var renameErr error
	for i := 0; i < 20; i++ {
		if _, statErr := os.Stat(binary); os.IsNotExist(statErr) {
			// Target is gone (fresh install or someone deleted it) — skip the
			// move-aside step; the install rename will create it.
			renameErr = nil
			break
		}
		if err := os.Rename(binary, oldBin); err == nil {
			renameErr = nil
			break
		} else {
			renameErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if renameErr != nil {
		os.Remove(tmpBin)
		return fmt.Errorf("could not move running binary aside: %w", renameErr)
	}

	if err := os.Rename(tmpBin, binary); err != nil {
		// Try to put the old binary back so the user isn't left with nothing.
		_ = os.Rename(oldBin, binary)
		os.Remove(tmpBin)
		return fmt.Errorf("install failed: %w", err)
	}
	return nil
}

// cleanStaleOldBinaries removes vibe.exe.old.* files left behind by
// previous dev invocations. The current dev process is still executing
// from one of them, so its remove will fail — that's fine, next dev run
// will catch it once we've exited.
func cleanStaleOldBinaries(binary string) {
	dir := filepath.Dir(binary)
	base := filepath.Base(binary) + ".old."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if len(name) < len(base) || name[:len(base)] != base {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// stopRunningDaemon prefers the Scheduled Task path (one schtasks /end
// call ends the task instance and releases its handles), and falls back
// to a direct PID kill if no task is registered or the schtasks call
// errors out.
func stopRunningDaemon() {
	if handled, err := tryPlatformDaemonStop(); handled && err == nil {
		return
	}
	pid, err := readDaemonPID()
	if err != nil || pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Kill()
}

// restartDaemonForDev brings the daemon back up using the same priority
// the regular `vibe daemon start` flow uses: Scheduled Task first
// (so the daemon gets the elevated rights it needs to bind :53), then a
// plain forkDaemon if no task is registered.
func restartDaemonForDev() error {
	if handled, err := tryPlatformDaemonStart(); handled {
		return err
	}
	return forkDaemon()
}
