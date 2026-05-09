//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows we wrap each managed process in an anonymous Job Object with
// the KILL_ON_JOB_CLOSE flag. AssignProcessToJobObject pulls the spawned
// child (and, transitively, any descendants it creates) into the job, so
// TerminateJobObject becomes the moral equivalent of `kill(-pgid, SIGTERM)`
// on unix — one call kills the whole tree.
//
// jobHandles maps route name → job handle. It's keyed off route name because
// process.go's afterSpawn/afterExit pass the route name; the actual *exec.Cmd
// is already in ProcessManager.procs.
var (
	jobHandlesMu sync.Mutex
	jobHandles   = map[string]windows.Handle{}
)

// buildShellCommand uses %COMSPEC% (cmd.exe) /C. Users who want PowerShell
// can wrap their command (`powershell -Command "..."`) inside route.Cmd —
// supporting both shells is a Phase 2.5 concern, not blocking.
func buildShellCommand(routeCmd string) *exec.Cmd {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	return exec.Command(shell, "/C", routeCmd)
}

// applySpawnAttrs sets up the spawn so the child starts in its own console
// group and (more importantly) is created suspended-friendly relative to
// the Job Object. We use CREATE_NEW_PROCESS_GROUP so our daemon can later
// signal the child without flowing into our own console.
//
// Note: CREATE_BREAKAWAY_FROM_JOB is intentionally NOT set — modern Windows
// allows nested jobs, and we want the child's process tree to belong to
// our job.
func applySpawnAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	const (
		createNewProcessGroup = 0x00000200
		createNoWindow        = 0x08000000
	)
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup | createNoWindow
}

// jobObjectExtendedLimitInformation matches the Windows JOBOBJECT_EXTENDED_LIMIT_INFORMATION
// struct layout — needed for SetInformationJobObject with the KILL_ON_JOB_CLOSE flag.
// x/sys/windows provides the named constant but not always the full struct.
type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation windows.JOBOBJECT_BASIC_LIMIT_INFORMATION
	IoInfo                windows.IO_COUNTERS
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// jobObjectBasicProcessIdList matches JOBOBJECT_BASIC_PROCESS_ID_LIST.
// Used to enumerate PIDs in a job for port discovery. Caller allocates
// extra bytes for the variable-length ProcessIdList tail.
type jobObjectBasicProcessIdList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIdList             [1]uintptr
}

// afterSpawn assigns the just-started child to a fresh Job Object so
// TerminateJobObject can later kill the whole tree.
func afterSpawn(name string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		// Non-fatal: the child still ran, we just won't be able to kill its
		// descendants. Log to stderr; the daemon's caller will see it in
		// daemon.log.
		fmt.Fprintf(os.Stderr, "warning: create job object for %q: %v\n", name, err)
		return
	}
	procHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		fmt.Fprintf(os.Stderr, "warning: open process for %q (pid %d): %v\n", name, cmd.Process.Pid, err)
		return
	}
	defer windows.CloseHandle(procHandle)

	if err := windows.AssignProcessToJobObject(job, procHandle); err != nil {
		windows.CloseHandle(job)
		fmt.Fprintf(os.Stderr, "warning: assign %q to job: %v\n", name, err)
		return
	}

	jobHandlesMu.Lock()
	if old, ok := jobHandles[name]; ok {
		windows.CloseHandle(old)
	}
	jobHandles[name] = job
	jobHandlesMu.Unlock()
}

// afterExit closes the job handle when a managed process has exited so we
// don't leak handles (and so KILL_ON_JOB_CLOSE finally fires for any
// stragglers we missed).
func afterExit(name string) {
	jobHandlesMu.Lock()
	job, ok := jobHandles[name]
	if ok {
		delete(jobHandles, name)
	}
	jobHandlesMu.Unlock()
	if ok {
		windows.CloseHandle(job)
	}
}

// killProcessTree terminates the entire Job Object — equivalent to unix
// `kill(-pgid, SIGTERM)`. Falls back to Process.Kill if no job is tracked
// (which can happen on a spawn-time failure path).
func killProcessTree(name string, cmd *exec.Cmd) error {
	jobHandlesMu.Lock()
	job, ok := jobHandles[name]
	jobHandlesMu.Unlock()
	if ok {
		// Exit code 1 — the same we'd see for a SIGKILL on unix.
		if err := windows.TerminateJobObject(job, 1); err != nil {
			return fmt.Errorf("terminate job for %s: %w", name, err)
		}
		return nil
	}
	if cmd != nil && cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// createKillOnCloseJob creates an anonymous Job Object and configures it
// so closing the last handle kills every process inside. That's the
// safety net: if the daemon crashes without calling TerminateJobObject,
// Windows still cleans up our managed children.
func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	var info jobObjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return job, nil
}

// jobPIDsForRoute returns every PID currently in the route's Job Object.
// Used by port discovery to locate listening sockets owned by the route's
// process tree (analog of the unix `pidsInGroup` helper).
//
// Returns (nil, nil) when no job is tracked for the route — the caller
// should treat that as "no info available" rather than an error.
func jobPIDsForRoute(name string) ([]int, error) {
	jobHandlesMu.Lock()
	job, ok := jobHandles[name]
	jobHandlesMu.Unlock()
	if !ok {
		return nil, nil
	}

	// Allocate enough room for ~256 PIDs. JOBOBJECT_BASIC_PROCESS_ID_LIST is
	// variable-sized; if the job has more processes than the buffer fits,
	// QueryInformationJobObject returns ERROR_MORE_DATA and we'd grow.
	const maxPIDs = 256
	bufSize := uint32(unsafe.Sizeof(jobObjectBasicProcessIdList{})) + uint32((maxPIDs-1)*int(unsafe.Sizeof(uintptr(0))))
	buf := make([]byte, bufSize)
	var ret uint32
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&buf[0])),
		bufSize,
		&ret,
	)
	if err != nil {
		return nil, fmt.Errorf("QueryInformationJobObject: %w", err)
	}
	hdr := (*jobObjectBasicProcessIdList)(unsafe.Pointer(&buf[0]))
	count := int(hdr.NumberOfProcessIdsInList)
	if count == 0 {
		return nil, nil
	}
	pids := make([]int, count)
	pidPtr := unsafe.Pointer(&hdr.ProcessIdList[0])
	for i := 0; i < count; i++ {
		raw := *(*uintptr)(unsafe.Pointer(uintptr(pidPtr) + uintptr(i)*unsafe.Sizeof(uintptr(0))))
		pids[i] = int(raw)
	}
	return pids, nil
}
