//go:build windows

package cmd

import "syscall"

// hideConsoleOnDaemonStart detaches the daemon from any inherited console
// window so it runs invisibly in the background. Called from serveCmd's
// RunE before the daemon binds its sockets.
//
// We use FreeConsole rather than ShowWindow(SW_HIDE) because FreeConsole
// disconnects entirely — even the brief flash of the window during startup
// goes away. The daemon writes to %USERPROFILE%\.vibe\daemon.log via its
// own file handles, so losing stdout/stderr to the parent console is fine.
//
// FreeConsole is a no-op when there's no console attached (e.g. when
// vibe.exe is launched via DETACHED_PROCESS by forkDaemon), so it's safe
// to call unconditionally. x/sys/windows doesn't surface FreeConsole, so
// we resolve it from kernel32.dll directly.
var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procFreeConsole = kernel32.NewProc("FreeConsole")
)

func hideConsoleOnDaemonStart() {
	_, _, _ = procFreeConsole.Call()
}
