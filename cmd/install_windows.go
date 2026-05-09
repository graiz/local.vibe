//go:build windows

package cmd

import (
	"os"
	"path/filepath"
)

// installDestination on Windows uses %LOCALAPPDATA%\Programs\vibe\vibe.exe
// — the standard "single-user, no admin" install location used by tools
// like VS Code. Falls back to the executable's current directory if
// LOCALAPPDATA is unset.
func installDestination() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		// Fallback: %USERPROFILE%\AppData\Local
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, "AppData", "Local")
		}
	}
	if base == "" {
		// Last-ditch: install next to the source binary.
		return "vibe.exe"
	}
	return filepath.Join(base, "Programs", "vibe", "vibe.exe")
}
