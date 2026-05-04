package daemon

import (
	"fmt"
	"regexp"
	"strconv"
)

// Recovery is an actionable hint extracted from a failed managed process's
// log tail. The dashboard surfaces Message to the user and uses Action + PID
// or Port to offer a one-click "kill and retry" button.
type Recovery struct {
	Message string `json:"message"`
	Action  string `json:"action"` // "kill_pid" | "kill_port" | "restart"
	PID     int    `json:"pid,omitempty"`
	Port    int    `json:"port,omitempty"`
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

// scanLogForRecovery inspects the tail of a failed process's log output and
// returns a recovery hint if one of the known patterns matches.
func scanLogForRecovery(tail string) *Recovery {
	if tail == "" {
		return nil
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
