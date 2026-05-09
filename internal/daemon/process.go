package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// StartError wraps a Start() failure with the tail of the process's log
// output so callers can scan it for an actionable recovery hint (orphan PID,
// EADDRINUSE, etc.) and surface it to the user.
type StartError struct {
	Err  error
	Tail string
}

func (e *StartError) Error() string {
	if e.Tail != "" {
		return e.Err.Error() + "\n" + e.Tail
	}
	return e.Err.Error()
}

func (e *StartError) Unwrap() error { return e.Err }

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
		if processAlive(cmd.Process.Pid) {
			pid := cmd.Process.Pid
			pm.mu.Unlock()
			return pid, nil // already running
		}
	}

	if route.Cmd == "" {
		pm.mu.Unlock()
		return 0, fmt.Errorf("no command configured for %s", route.Name)
	}

	cmd := buildShellCommand(route.Cmd)
	cmd.Dir = route.Dir
	applySpawnAttrs(cmd)
	// Inject PORT (primary) plus PORT_<UPPER_NAME> for each reserve_ports entry
	// so the cmd can reference auxiliary ports by semantic name (e.g.
	// $PORT_SERVER) instead of hardcoding values that drift from vibe.json.
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", route.Port))
	for name, p := range route.ReservePorts {
		cmd.Env = append(cmd.Env, fmt.Sprintf("PORT_%s=%d", strings.ToUpper(name), p))
	}

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
	afterSpawn(route.Name, cmd)

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
		afterExit(route.Name)
		tail := tailLogFile(logPath, 12)
		var inner error
		if err != nil {
			inner = fmt.Errorf("process exited immediately: %w", err)
		} else {
			inner = fmt.Errorf("process exited immediately with status 0")
		}
		return 0, &StartError{Err: inner, Tail: tail}
	case <-time.After(1 * time.Second):
		// Still running after 1s — likely a real server process.
	}

	return pid, nil
}

// tailLogFile reads the last n non-empty lines from a log file to provide
// context when a managed process crashes on startup.
func tailLogFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Filter out empty lines and ANSI-heavy shell banner noise.
	var meaningful []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		meaningful = append(meaningful, trimmed)
	}
	if len(meaningful) == 0 {
		return ""
	}
	start := len(meaningful) - n
	if start < 0 {
		start = 0
	}
	return strings.Join(meaningful[start:], "\n")
}

// Stop terminates the managed process tree for the given route name.
// On unix this sends SIGTERM to the process group; on Windows (Phase 2) it
// terminates the Job Object containing the child and its descendants.
func (pm *ProcessManager) Stop(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cmd, ok := pm.procs[name]
	if !ok || cmd.Process == nil {
		return fmt.Errorf("%s is not running", name)
	}

	_ = killProcessTree(name, cmd)
	delete(pm.procs, name)
	afterExit(name)
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
	return processAlive(cmd.Process.Pid)
}

// OwnsPID reports whether pid belongs to one of the daemon's managed children.
// Used to prevent the "kill and retry" recovery flow from SIGTERM'ing our own
// processes — the caller should stop the owning route instead.
func (pm *ProcessManager) OwnsPID(pid int) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, cmd := range pm.procs {
		if cmd.Process != nil && cmd.Process.Pid == pid {
			return true
		}
	}
	return false
}
