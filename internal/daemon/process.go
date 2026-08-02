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
	mu      sync.Mutex
	procs   map[string]*exec.Cmd // keyed by route name; spawned by this daemon
	adopted map[string]int       // keyed by route name; pid of a child re-adopted after a daemon restart (no *exec.Cmd)

	// onExit is invoked (event-based, no polling) when a managed child exits
	// after having started running — from cmd.Wait for spawned children, and
	// from a per-OS PID-exit watcher for adopted ones. The daemon uses it to
	// flip the route to not-running immediately. pid identifies the exited
	// process so a stale exit from a since-restarted route can be ignored.
	onExit func(name string, pid int)

	// envHook supplies additional "KEY=value" bindings for a route being
	// spawned. It lets the daemon inject values it derives from the route
	// table (which the ProcessManager can't see) without threading the
	// Server through Start.
	envHook func(*Route) []string
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		procs:   make(map[string]*exec.Cmd),
		adopted: make(map[string]int),
	}
}

// SetExitHandler registers the callback fired when a managed child exits.
func (pm *ProcessManager) SetExitHandler(fn func(name string, pid int)) {
	pm.mu.Lock()
	pm.onExit = fn
	pm.mu.Unlock()
}

// SetEnvHook registers a callback supplying extra env bindings per route.
func (pm *ProcessManager) SetEnvHook(fn func(*Route) []string) {
	pm.mu.Lock()
	pm.envHook = fn
	pm.mu.Unlock()
}

func (pm *ProcessManager) fireExit(name string, pid int) {
	pm.mu.Lock()
	fn := pm.onExit
	pm.mu.Unlock()
	if fn != nil {
		fn(name, pid)
	}
}

// Adopt records a managed child the daemon re-attached to after a restart.
// The daemon has the process-group leader PID but no *exec.Cmd, so Stop kills
// it via killAdoptedProcess (process-group SIGTERM) rather than through cmd.
// Calling Start for the same route later supersedes the adoption.
//
// Re-adopting a (name, pid) that is already tracked is a no-op. Recovery runs
// on every failed proxy round-trip, and a transient upstream error against a
// perfectly healthy child lands here with the pid it already has. Spawning a
// second watcher each time would leak a goroutine and a kqueue/pidfd fd that
// only closes when the process finally dies — unbounded growth against
// RLIMIT_NOFILE on a long-lived daemon with a flaky dev server.
func (pm *ProcessManager) Adopt(name string, pid int) {
	pm.mu.Lock()
	if cur, ok := pm.adopted[name]; ok && cur == pid {
		pm.mu.Unlock()
		return
	}
	pm.adopted[name] = pid
	pm.mu.Unlock()

	// Adopted children have no *exec.Cmd to Wait on, so watch their PID for exit
	// via a per-OS primitive (kqueue on darwin, pidfd on linux; no-op elsewhere
	// — Windows never adopts). When it exits, notify so the route flips to
	// not-running, replacing the sweep's role for adopted children. Event-based,
	// no polling.
	go watchPIDExit(pid, func() {
		pm.mu.Lock()
		cur, ok := pm.adopted[name]
		stillOurs := ok && cur == pid
		if stillOurs {
			delete(pm.adopted, name)
		}
		pm.mu.Unlock()
		if stillOurs {
			afterExit(name)
			pm.fireExit(name, pid)
		}
	})
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

	// A child re-adopted after a daemon restart lives in pm.adopted (no
	// *exec.Cmd). If it's still alive, Start is a no-op: spawning a second copy
	// would strand the adopted child — the `delete(pm.adopted)` on the spawn
	// path below drops it from tracking without killing it, so Stop/Deregister
	// could never reach it, and the new process would crash-loop on EADDRINUSE
	// against the still-bound port. A dead adopted entry falls through so the
	// stale record is cleared by the real spawn.
	if pid, ok := pm.adopted[route.Name]; ok && processAlive(pid) {
		pm.mu.Unlock()
		return pid, nil // adopted child still running
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
	// Extra route-derived bindings the daemon supplies (e.g. the OAuth base
	// URL for a route with a callback bridge). Appended last so they win over
	// anything inherited from the daemon's own environment.
	if pm.envHook != nil {
		cmd.Env = append(cmd.Env, pm.envHook(route)...)
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
	delete(pm.adopted, route.Name) // a real spawn supersedes any prior adoption
	pid := cmd.Process.Pid
	pm.mu.Unlock()

	// One goroutine owns the child's lifetime via cmd.Wait. The first second is
	// a "startup window": an exit there is an immediate failure that Start
	// reports synchronously (StartError). An exit *after* the window is a
	// runtime death that fires onExit so the daemon flips the route — event
	// based, no polling.
	//
	// startup state transitions (stStarting → stRunning | stExitedEarly) all
	// happen under pm.mu, so the goroutine and Start can't disagree about which
	// case occurred even when the exit races the 1s boundary exactly.
	const (
		stStarting = iota
		stRunning
		stExitedEarly
	)
	state := stStarting
	var earlyErr error // exit error when the child dies in the startup window
	exitedEarly := make(chan struct{})
	go func() {
		err := cmd.Wait()
		logFile.Close()
		pm.mu.Lock()
		if cur, ok := pm.procs[route.Name]; ok && cur == cmd {
			delete(pm.procs, route.Name)
		}
		if state == stRunning {
			pm.mu.Unlock()
			afterExit(route.Name)
			pm.fireExit(route.Name, pid) // runtime death → notify
			return
		}
		state = stExitedEarly
		earlyErr = err
		pm.mu.Unlock()
		afterExit(route.Name)
		close(exitedEarly)
	}()

	startupFailed := func() (int, error) {
		tail := tailLogFile(logPath, 12)
		var inner error
		if earlyErr != nil {
			inner = fmt.Errorf("process exited immediately: %w", earlyErr)
		} else {
			inner = fmt.Errorf("process exited immediately with status 0")
		}
		return 0, &StartError{Err: inner, Tail: tail}
	}

	// Wait briefly to catch immediate failures (command not found, missing deps, etc.)
	select {
	case <-exitedEarly:
		return startupFailed()
	case <-time.After(1 * time.Second):
		pm.mu.Lock()
		if state == stExitedEarly {
			// Raced the boundary: the child already exited and the goroutine
			// handled it as an early exit.
			pm.mu.Unlock()
			return startupFailed()
		}
		state = stRunning // commit: a later exit becomes a runtime death
		pm.mu.Unlock()
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

	if cmd, ok := pm.procs[name]; ok && cmd.Process != nil {
		_ = killProcessTree(name, cmd)
		delete(pm.procs, name)
		delete(pm.adopted, name)
		afterExit(name)
		return nil
	}

	// A child re-adopted after a daemon restart has no *exec.Cmd; kill it by
	// its process-group leader PID instead.
	if pid, ok := pm.adopted[name]; ok {
		_ = killAdoptedProcess(pid)
		delete(pm.adopted, name)
		afterExit(name)
		return nil
	}

	return fmt.Errorf("%s is not running", name)
}

// IsRunning checks if the managed process for the given route is alive.
func (pm *ProcessManager) IsRunning(name string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if cmd, ok := pm.procs[name]; ok && cmd.Process != nil {
		return processAlive(cmd.Process.Pid)
	}
	if pid, ok := pm.adopted[name]; ok {
		return processAlive(pid)
	}
	return false
}

// OwnsPID reports whether pid belongs to one of the daemon's managed children.
// Used to prevent the "kill and retry" recovery flow from SIGTERM'ing our own
// processes — the caller should stop the owning route instead.
// It matches descendants, not just the process-group leader vibe spawned: the
// leader is a login shell ($SHELL -lc "npm run dev"), so the process actually
// holding the route's port is almost always a grandchild. lsof reports that
// listener's pid, so a leader-only comparison misses it and the "kill the port
// holder" paths would SIGTERM another route's live dev server.
func (pm *ProcessManager) OwnsPID(pid int) bool {
	pm.mu.Lock()
	leaders := make([]int, 0, len(pm.procs)+len(pm.adopted))
	for _, cmd := range pm.procs {
		if cmd.Process != nil {
			leaders = append(leaders, cmd.Process.Pid)
		}
	}
	for _, p := range pm.adopted {
		leaders = append(leaders, p)
	}
	pm.mu.Unlock()

	for _, l := range leaders {
		if l == pid {
			return true
		}
	}
	// Not a leader — check whether it belongs to a leader's process group.
	// Done outside the lock: Getpgid is a syscall, and holding pm.mu across it
	// would serialize every caller behind the kernel.
	pgid, ok := processGroupOf(pid)
	if !ok {
		return false
	}
	for _, l := range leaders {
		if pgid == l {
			return true
		}
	}
	return false
}
