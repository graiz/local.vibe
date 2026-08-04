package daemon

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Recovery is an actionable hint extracted from a failed managed process's
// log tail. The dashboard surfaces Message to the user and uses Action +
// (PID|Port|SuggestedCmd) to offer a one-click remediation button.
type Recovery struct {
	Message      string `json:"message"`
	Action       string `json:"action"` // "kill_pid" | "kill_port" | "restart" | "edit_cmd" | "info"
	PID          int    `json:"pid,omitempty"`
	Port         int    `json:"port,omitempty"`
	SuggestedCmd string `json:"suggested_cmd,omitempty"`
}

type recoveryPattern struct {
	re      *regexp.Regexp
	message string // %d is substituted with the extracted pid/port
	action  string // "kill_pid" or "kill_port"
}

// recoveryPatterns matches common log output from dev servers when a prior
// or orphan process is blocking startup. Order matters — PID-bearing patterns
// come before generic port-based ones so we offer the more specific action.
var recoveryPatterns = []recoveryPattern{
	// Next.js: "⨯ Another next dev server is already running. ... PID: 23674"
	{
		re:      regexp.MustCompile(`Another\s+next\s+dev\s+server\s+is\s+already\s+running[\s\S]{0,400}?PID:\s*(\d+)`),
		message: "A previous Next.js dev server is still running (PID %d). Kill it and retry?",
		action:  "kill_pid",
	},
	// Generic "already running" hints that name a PID.
	{
		re:      regexp.MustCompile(`already running[\s\S]{0,200}?PID[:\s]*(\d+)`),
		message: "Another instance is already running (PID %d). Kill it and retry?",
		action:  "kill_pid",
	},
	// Node: "Error: listen EADDRINUSE: address already in use :::3001"
	{
		re:      regexp.MustCompile(`EADDRINUSE[\s\S]{0,80}?:(\d{2,5})\b`),
		message: "Port %d is already in use. Kill the process holding it and retry?",
		action:  "kill_port",
	},
	// Port appears before the phrase: "listen tcp 127.0.0.1:8080: bind: address already in use"
	{
		re:      regexp.MustCompile(`(?i):(\d{2,5})\b[^\n]{0,80}?address already in use`),
		message: "Port %d is already in use. Kill the process holding it and retry?",
		action:  "kill_port",
	},
	// Port appears after the phrase: "address already in use: :3001"
	{
		re:      regexp.MustCompile(`(?i)address already in use[^\n]{0,80}?:(\d{2,5})\b`),
		message: "Port %d is already in use. Kill the process holding it and retry?",
		action:  "kill_port",
	},
}

// execDeniedRe matches a shell refusing to execute a file. On macOS this is
// almost always com.apple.quarantine — the kernel returns EPERM on exec of a
// quarantined file, and the shell reports it as "operation not permitted".
// Exit code 126 means "found but not executable", the same condition surfaced
// by a runner rather than the shell.
//
// Examples:
//
//	zsh:1: operation not permitted: node_modules/.bin/concurrently
//	/bin/bash: .../vite: /usr/bin/env: bad interpreter: Operation not permitted
//	error: script "dev" exited with code 126
var execDeniedRe = regexp.MustCompile(`(?i)(bad interpreter:\s*operation not permitted|operation not permitted:\s*\S+|exited with code 126\b)`)

// pythonNotFoundRe matches shell errors from invoking a missing `python`
// interpreter. macOS no longer ships /usr/bin/python — projects that hard-code
// `python` in their command crash here while `python3` works fine.
//
// Examples:
//
//	zsh:1: command not found: python
//	bash: python: command not found
//	/bin/sh: python: command not found
var pythonNotFoundRe = regexp.MustCompile(`(?m)(?:command not found:\s*python|python:\s*command not found)\b`)

// moduleNotFoundRe extracts the missing package name from Python's
// ModuleNotFoundError. Example:
//
//	ModuleNotFoundError: No module named 'flask'
var moduleNotFoundRe = regexp.MustCompile(`ModuleNotFoundError:\s+No module named\s+['"]([a-zA-Z_][a-zA-Z0-9_.]*)['"]`)

// pythonTokenRe finds a standalone `python` token (word-boundary on both sides,
// not preceded by `python`-suffix chars like `3` or `2`). Used to rewrite
// `python app.py` → `python3 app.py` without mangling `python3`, `pythonw`,
// or paths like `/usr/bin/python`.
var pythonTokenRe = regexp.MustCompile(`(^|[\s;&|"'` + "`" + `])python(\b)`)

// suggestPython3Cmd rewrites the first standalone `python` in cmd to `python3`.
// Returns (newCmd, true) if a rewrite happened, (cmd, false) otherwise.
//
// Skips the rewrite when cmd already contains `python3` — the user has been
// explicit, and another bare `python` somewhere later (rare) probably means
// something else.
func suggestPython3Cmd(cmd string) (string, bool) {
	if cmd == "" {
		return cmd, false
	}
	if strings.Contains(cmd, "python3") {
		return cmd, false
	}
	if !pythonTokenRe.MatchString(cmd) {
		return cmd, false
	}
	// Rewrite only the first match so we don't accidentally double-rewrite
	// in pathological inputs.
	replaced := false
	out := pythonTokenRe.ReplaceAllStringFunc(cmd, func(m string) string {
		if replaced {
			return m
		}
		replaced = true
		return pythonTokenRe.ReplaceAllString(m, "${1}python3${2}")
	})
	return out, replaced
}

// scanLogForRecovery inspects the tail of a failed process's log output and
// returns a recovery hint if one of the known patterns matches. The route's
// current cmd is consulted for cmd-rewrite suggestions (e.g. python → python3);
// pass an empty string when no cmd is available.
func scanLogForRecovery(tail, cmd, dir string) *Recovery {
	// An exec-denied message names the cause directly. Ask the quarantine
	// probe for specifics (how many files, which agent, what to run); fall
	// back to a generic message if nothing is actually flagged, since the
	// shell can report EPERM for other reasons (a noexec mount, say).
	if execDeniedRe.MatchString(tail) {
		if rec := scanQuarantinedExecutables(dir, cmd); rec != nil {
			return rec
		}
		return &Recovery{
			Action: "info",
			Message: "The shell refused to execute part of this command " +
				"(\"operation not permitted\"). On macOS this is usually the " +
				"com.apple.quarantine flag, which anything downloaded, unzipped, " +
				"AirDropped, or synced from a cloud provider carries. Clear it with " +
				"`xattr -dr com.apple.quarantine <path>` and retry.",
		}
	}
	if tail == "" {
		return nil
	}

	// Cmd-aware patterns first — they offer the most direct fix.
	if pythonNotFoundRe.MatchString(tail) {
		if newCmd, ok := suggestPython3Cmd(cmd); ok {
			return &Recovery{
				Action:       "edit_cmd",
				Message:      "`python` is not on PATH but `python3` is. Switch the command to use `python3` and retry?",
				SuggestedCmd: newCmd,
			}
		}
	}
	if m := moduleNotFoundRe.FindStringSubmatch(tail); len(m) >= 2 {
		mod := m[1]
		return &Recovery{
			Action: "info",
			Message: fmt.Sprintf(
				"Missing Python module `%s`. Install it (e.g. `pip3 install %s`, or activate your project's venv) and retry.",
				mod, mod,
			),
		}
	}

	for _, p := range recoveryPatterns {
		m := p.re.FindStringSubmatch(tail)
		if len(m) < 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			continue
		}
		r := &Recovery{
			Action:  p.action,
			Message: fmt.Sprintf(p.message, n),
		}
		switch p.action {
		case "kill_pid":
			r.PID = n
		case "kill_port":
			r.Port = n
		}
		return r
	}
	return nil
}
