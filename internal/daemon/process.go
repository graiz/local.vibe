package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/localvibe/vibe/internal/config"
)

// ProcessManager tracks managed child processes spawned by the daemon.
type ProcessManager struct {
	mu    sync.Mutex
	procs map[string]*exec.Cmd // keyed by route name
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{procs: make(map[string]*exec.Cmd)}
}

// Start launches the command for a managed route.
// It returns the PID of the child process.
func (pm *ProcessManager) Start(route *Route) (int, error) {
	pm.mu.Lock()

	if cmd, ok := pm.procs[route.Name]; ok && cmd.Process != nil {
		if cmd.Process.Signal(syscall.Signal(0)) == nil {
			pid := cmd.Process.Pid
			pm.mu.Unlock()
			return pid, nil // already running
		}
	}

	if route.Cmd == "" {
		pm.mu.Unlock()
		return 0, fmt.Errorf("no command configured for %s", route.Name)
	}

	// Use interactive login shell so the user's full PATH is available,
	// including tools initialized in .zshrc/.bashrc (rbenv, nvm, pyenv, etc.)
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	cmd := exec.Command(shell, "-lic", route.Cmd)
	cmd.Dir = route.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logDir := config.Dir()
	_ = os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", route.Name))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		pm.mu.Unlock()
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		pm.mu.Unlock()
		return 0, fmt.Errorf("start %q: %w", route.Cmd, err)
	}

	pm.procs[route.Name] = cmd
	pid := cmd.Process.Pid
	pm.mu.Unlock()

	// Monitor in background — when process exits, close log file.
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		logFile.Close()
		exited <- err
	}()

	// Wait briefly to catch immediate failures (command not found, missing deps, etc.)
	select {
	case err := <-exited:
		pm.mu.Lock()
		delete(pm.procs, route.Name)
		pm.mu.Unlock()
		if err != nil {
			return 0, fmt.Errorf("process exited immediately: %w", err)
		}
		return 0, fmt.Errorf("process exited immediately with status 0")
	case <-time.After(1 * time.Second):
		// Still running after 1s — likely a real server process.
	}

	return pid, nil
}

// Stop sends SIGTERM to the managed process for the given route name.
func (pm *ProcessManager) Stop(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cmd, ok := pm.procs[name]
	if !ok || cmd.Process == nil {
		return fmt.Errorf("%s is not running", name)
	}

	// Kill the process group so child processes also get the signal.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	delete(pm.procs, name)
	return nil
}

// IsRunning checks if the managed process for the given route is alive.
func (pm *ProcessManager) IsRunning(name string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cmd, ok := pm.procs[name]
	if !ok || cmd.Process == nil {
		return false
	}
	return cmd.Process.Signal(syscall.Signal(0)) == nil
}
