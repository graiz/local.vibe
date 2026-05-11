//go:build !darwin && !linux && !windows

package cmd

import (
	"errors"
	"runtime"
)

func tryPlatformDaemonStart() (bool, error) { return false, nil }
func tryPlatformDaemonStop() (bool, error)  { return false, nil }

func forkDaemon() error {
	return errors.New("forking the daemon is not supported on " + runtime.GOOS)
}

func cliProcessAlive(pid int) bool {
	return pid > 0
}

func signalDaemonStop(pid int) error {
	_ = pid
	return errors.New("daemon stop is not supported on " + runtime.GOOS)
}
