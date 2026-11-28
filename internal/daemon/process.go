package daemon

import (
	"fmt"
	"io"
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
	// stopping holds a channel per route name whose Stop is in flight,
	// closed once the old tree is gone. Start waits on it so a restart
	// never races the process it is replacing.
	stopping map[string]chan struct{}
	// starting reserves a route name while its spawn is in flight. Start
	// releases mu for the spawn itself (log open, env hook, cmd.Start and —
	// on Windows — the Job Object syscalls in afterSpawn), so a slow spawn
	// can't block callers for every other route (issue #13). The channel is
	// closed when the spawn resolves; same-name Start/Stop/IsRunning wait.
	starting map[string]chan struct{}

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

	// stopGrace is how long Stop waits for a SIGTERM'd child to exit before
	// escalating to SIGKILL. A child that traps TERM would otherwise keep its
	// port while the route read "stopped".
	stopGrace time.Duration

	// maxLogBytes rotates a route's log at spawn once it exceeds this size.
	// Logs are append-only across restarts and were never rotated; the tail
	// readers only need the recent end.
	maxLogBytes int64
}

const (
	defaultStopGrace   = 3 * time.Second
	defaultMaxLogBytes = 10 << 20
)

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		procs:       make(map[string]*exec.Cmd),
		adopted:     make(map[string]int),
		stopping:    make(map[string]chan struct{}),
		starting:    make(map[string]chan struct{}),
		stopGrace:   defaultStopGrace,
		maxLogBytes: defaultMaxLogBytes,
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

// awaitStartLocked blocks until no spawn for name is in flight. Called with
// pm.mu held; returns with it held. Same-name callers wait so they see the
// spawn's outcome rather than a gap: a Stop issued mid-spawn kills the new
// child instead of reporting "not running" and leaving it alive, and
// IsRunning reports the finished spawn.
func (pm *ProcessManager) awaitStartLocked(name string) {
	for {
		ch, ok := pm.starting[name]
		if !ok {
			return
		}
		pm.mu.Unlock()
		<-ch
		pm.mu.Lock()
	}
}

// Start launches the command for a managed route.
// It returns the PID of the child process.
//
// pm.mu guards only the map reads and writes. The spawn itself runs unlocked
// under a per-name reservation (pm.starting) — see issue #13: holding the
// mutex across cmd.Start and the Windows Job Object syscalls let one slow
// spawn stall IsRunning/Stop/OwnsPID for every route.
func (pm *ProcessManager) Start(route *Route) (int, error) {
	pm.mu.Lock()
	// Wait out both an in-flight Stop (a restart must never race the tree it
	// replaces) and an in-flight Start (a second Start returns the first's
	// pid instead of spawning a duplicate). One loop, re-checking both after
	// every wake: a Stop can begin while we wait on a Start and vice versa.
	for {
		if ch, ok := pm.stopping[route.Name]; ok {
			pm.mu.Unlock()
			<-ch
			pm.mu.Lock()
			continue
		}
		if ch, ok := pm.starting[route.Name]; ok {
			pm.mu.Unlock()
			<-ch
			pm.mu.Lock()
			continue
		}
		break
	}

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

	// Reserve the name and drop the lock for the spawn. The deferred release
	// covers every exit, including a panicking env hook: a leaked reservation
	// would wedge every later Start/Stop/IsRunning for this route.
	spawnDone := make(chan struct{})
	pm.starting[route.Name] = spawnDone
	envHook := pm.envHook
	pm.mu.Unlock()
	committed := false
	defer func() {
		if !committed {
			pm.mu.Lock()
			delete(pm.starting, route.Name)
			pm.mu.Unlock()
			close(spawnDone)
		}
	}()

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
	if envHook != nil {
		cmd.Env = append(cmd.Env, envHook(route)...)
	}

	logDir := config.Dir()
	_ = os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", route.Name))
	rotateLog(logPath, pm.maxLogBytes)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("start %q: %w", route.Cmd, err)
	}
	afterSpawn(route.Name, cmd)

	pm.mu.Lock()
	pm.procs[route.Name] = cmd
	delete(pm.adopted, route.Name) // a real spawn supersedes any prior adoption
	delete(pm.starting, route.Name)
	committed = true
	pid := cmd.Process.Pid
	pm.mu.Unlock()
	close(spawnDone)

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
		// Only the child still tracked under this name owns the per-name
		// hooks. After a Stop → Start the name belongs to a NEW child; running
		// afterExit for the old one here would, on Windows, close the new
		// child's Job Object (KILL_ON_JOB_CLOSE) and kill the app that was
		// just started.
		tracked := false
		if cur, ok := pm.procs[route.Name]; ok && cur == cmd {
			delete(pm.procs, route.Name)
			tracked = true
		}
		if state == stRunning {
			pm.mu.Unlock()
			if tracked {
				afterExit(route.Name)
			}
			pm.fireExit(route.Name, pid) // runtime death → notify
			return
		}
		state = stExitedEarly
		earlyErr = err
		pm.mu.Unlock()
		if tracked {
			afterExit(route.Name)
		}
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

// tailLogWindow bounds how much of a log file tailLogFile reads: the last
// 64 KiB, far more than a dozen lines of any dev server's output. Logs are
// append-only and never rotated by the app, so reading the whole file here
// made every start-page render and every 5xx page load allocate the file.
const tailLogWindow = 64 * 1024

// tailLogFile reads the last n non-empty lines from a log file to provide
// context when a managed process crashes on startup.
func tailLogFile(path string, n int) string {
	data, err := readTail(path, tailLogWindow)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Drop escape sequences and pure decoration (see log_tail.go). Both are
	// pure loss here: the window is only a dozen lines, and a login shell's
	// banner would otherwise spend all of them without saying anything.
	var meaningful []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(stripANSI(l))
		if isDecorationOnly(trimmed) {
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

// readTail returns the last max bytes of the file at path (the whole file
// when smaller), starting at a line boundary when the window cut one.
func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	start := int64(0)
	if size > max {
		start = size - max
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	if start > 0 {
		// Drop the partial first line the window sliced through.
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
}

// Stop terminates the managed process tree for the given route name.
// On unix this sends SIGTERM to the process group, waits up to stopGrace for
// the leader to exit, and escalates to SIGKILL if it hasn't; on Windows it
// terminates the Job Object containing the child and its descendants (which
// is already immediate). It returns once the process is gone, so callers can
// rely on the port being released.
//
// The kill runs outside the lock — a 3s grace period must not serialize every
// other ProcessManager caller — but the child stays tracked until it is gone,
// with an entry in pm.stopping that a concurrent Start (or Stop) waits on.
func (pm *ProcessManager) Stop(name string) error {
	pm.mu.Lock()
	// A Stop arriving mid-spawn waits for the spawn to resolve, so it kills
	// the new child rather than reporting "not running" and leaving it alive.
	// Start never reserves while a Stop is in flight, so after this wait the
	// stopping check below is the only remaining case.
	pm.awaitStartLocked(name)
	if ch, ok := pm.stopping[name]; ok {
		// Another Stop already owns this teardown (the idle timer racing
		// handleStop, say). Wait for it rather than registering a second
		// entry: the first Stop's cleanup would then delete ours, and a Start
		// arriving mid-escalation would spawn alongside the dying tree.
		pm.mu.Unlock()
		<-ch
		return nil
	}
	cmd, hasCmd := pm.procs[name]
	adoptedPID, hasAdopted := pm.adopted[name]
	hasCmd = hasCmd && cmd.Process != nil
	if !hasCmd && !hasAdopted {
		pm.mu.Unlock()
		return fmt.Errorf("%s is not running", name)
	}
	// Keep the child tracked until it is actually gone. Dropping it up front
	// let a concurrent Start for the same name spawn a second copy while the
	// first still held the port, and — on Windows — this Stop's trailing
	// afterExit then closed the NEW child's Job Object. Start waits on this
	// channel instead.
	done := make(chan struct{})
	pm.stopping[name] = done
	pm.mu.Unlock()
	defer func() {
		pm.mu.Lock()
		if pm.stopping[name] == done {
			delete(pm.stopping, name)
		}
		pm.mu.Unlock()
		close(done)
	}()

	if hasCmd {
		pid := cmd.Process.Pid
		_ = killProcessTree(name, cmd)
		if !pm.waitGone(pid) {
			_ = killProcessTreeForce(name, cmd)
			pm.waitGone(pid)
		}
		// The Wait goroutine may already have untracked it (and run
		// afterExit) when the leader exited; only the side that removes the
		// entry runs the per-name hook.
		pm.mu.Lock()
		cur, ok := pm.procs[name]
		mine := ok && cur == cmd
		if mine {
			delete(pm.procs, name)
		}
		pm.mu.Unlock()
		if mine {
			afterExit(name)
		}
		return nil
	}
	// A child re-adopted after a daemon restart has no *exec.Cmd; kill it by
	// its process-group leader PID instead.
	_ = killAdoptedProcess(adoptedPID)
	if !pm.waitGone(adoptedPID) {
		_ = killAdoptedProcessForce(adoptedPID)
		pm.waitGone(adoptedPID)
	}
	pm.mu.Lock()
	cur, ok := pm.adopted[name]
	mine := ok && cur == adoptedPID
	if mine {
		delete(pm.adopted, name)
	}
	pm.mu.Unlock()
	if mine {
		afterExit(name)
	}
	return nil
}

// waitGone polls until the process tree led by pid has exited or the grace
// period lapses. The whole tree, not the leader: see treeAlive. Polling, not
// cmd.Wait: the Wait goroutine owns that call, and an adopted child has no
// Cmd at all. 20ms steps keep a prompt exit prompt.
func (pm *ProcessManager) waitGone(pid int) bool {
	deadline := time.Now().Add(pm.stopGrace)
	for time.Now().Before(deadline) {
		if !treeAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !treeAlive(pid)
}

// rotateLog moves path to path.1 (replacing any previous rotation) once it
// has grown past max. Called at spawn, so a route's log never holds more than
// one old generation plus the current run.
func rotateLog(path string, max int64) {
	if max <= 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= max {
		return
	}
	_ = os.Rename(path, path+".1")
}

// IsRunning checks if the managed process for the given route is alive.
func (pm *ProcessManager) IsRunning(name string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.awaitStartLocked(name)

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
