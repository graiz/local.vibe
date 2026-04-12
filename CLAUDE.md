# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

local.vibe — a local DNS daemon that gives dev servers friendly `.vibe` names. CLI binary is `vibe`. Single Go binary, minimal dependencies (only Cobra for CLI, rest is stdlib).

## Build & test

```bash
vibe dev              # rebuild binary + restart daemon (do this after every change)
go build -o vibe .    # just build
go test ./...         # run tests
go vet ./...          # lint (also runs in CI)
```

The daemon runs a compiled binary at `/opt/homebrew/bin/vibe`, not source — changes aren't picked up until rebuilt. `vibe dev` handles the full cycle: build → install → kill old daemon → LaunchAgent auto-restarts with new binary.

## Architecture

**Request flow:** Browser → dnsmasq (*.vibe → 127.0.0.1) → pf (port 80 → 7999) → daemon → reverse proxy → app on target port. Bookmark routes redirect (307) to external URLs instead of proxying.

**Three layers, strictly separated:**

1. **CLI commands** (`cmd/`) — Cobra commands. Know nothing about daemon internals. Use `internal/client` to talk to daemon.
2. **Client** (`internal/client/`) — HTTP wrapper. Tries Unix socket (`~/.vibe/vibe.sock`) first, falls back to TCP (`127.0.0.1:7999`).
3. **Daemon** (`internal/daemon/`) — HTTP server with embedded HTML dashboard. Core components:
   - `daemon.go` — Server struct, Start/Stop, HTTP routing by Host header
   - `api.go` — REST endpoints under `/_api/` (register, deregister, update, list, health, start, stop, ready, preferences). Route names validated as DNS-safe (lowercase alphanumeric + hyphens). All user input HTML-escaped in dashboard output.
   - `routes.go` — Thread-safe RouteTable (RWMutex + map), RouteType enum
   - `process.go` — ProcessManager spawns/kills managed child processes (uses process groups for clean shutdown). On immediate crash, tails the route's log file and includes output in the error message.
   - `monitor.go` — Background goroutine sweeps dead PIDs and expired TTLs every 5s
   - `persistence.go` — Saves/loads sticky, managed, and bookmark routes to `~/.vibe/routes.json`
   - `dashboard.go` — Embedded HTML dashboard with modal UI for adding/editing routes
   - `startpage.go` — "Not running" page for stopped managed routes with Start button
   - `theme.go` — Shared CSS/HTML head (Geist fonts, Vercel-inspired dark theme)
   - `setup_md.go` — Markdown setup guide served at `/setup.md`

**Config** (`internal/config/`) — Loads `~/.vibe/config.json`, falls back to defaults. Daemon port 7999, TLD "vibe", log level "warn", dashboard view "list".

## Route types

Five route types with different lifecycle semantics:
- **sticky** — `vibe register`; persists across daemon restarts; reverse-proxied
- **pid** — API only; auto-removed when tracked PID dies
- **ttl** — `--ttl` flag on register; auto-expires after N seconds
- **managed** — `vibe start` (reads `vibe.json` or inline args); daemon manages the child process, dashboard has start/stop buttons
- **bookmark** — External URL (e.g. Tailscale address); persists across restarts; visiting `name.vibe` redirects (307) to the external URL. Added/edited via dashboard modal.

## Key patterns

- **Route status:** Two separate fields — `Running` (process is alive) and `Ready` (port is accepting TCP connections). Managed routes start `Running=true, Ready=false`; a background goroutine polls the port every 500ms for up to 30s and flips `Ready=true` once the port responds. This handles REPL-wrapped servers where the process is alive before the HTTP server binds.
- **Dual communication:** Unix socket (preferred) with TCP fallback. Same HTTP mux serves both.
- **Thread safety:** RouteTable uses RWMutex, ProcessManager uses Mutex.
- **Process groups:** Managed processes use `Setpgid: true` and SIGTERM to `-pgid` to kill entire process trees.
- **Shell login:** Spawns `$SHELL -lc` so managed processes get full PATH (nvm, Homebrew, etc.).
- **PID file safety:** Only written after successful TCP bind (tested in `daemon_test.go`).
- **Zero-downtime restarts:** `vibe dev` kills daemon → LaunchAgent restarts → persisted routes survive.
- **Input validation:** Route names must match `[a-z0-9-]`, "local" is reserved. All user-supplied strings HTML-escaped in dashboard output to prevent XSS.
- **Port conflict detection:** Before starting a managed process, verifies the port is free. If `killPort` fails to clear it, returns 409 with a clear error. The `/ready` endpoint returns both `ready` and `running` so poll loops detect post-start crashes immediately.
- **Route icons:** Two fields — `Icon` (user-chosen emoji) and `AutoIcon` (auto-detected favicon as data URI). Display priority: Icon > AutoIcon > deterministic hash-based pool pick. Dashboard modal shows preview + emoji picker, never raw data URIs.
- **Dashboard view persistence:** List/grid toggle saved server-side via `PUT /_api/preferences` into `config.json`. Rendered server-side on page load — no flash of wrong view.
- **Embedded UI:** All HTML/CSS/JS is inline Go strings — no external assets, no build step. Dashboard includes a modal for CRUD operations on routes. Toast notifications surface errors from async actions.

## System setup (macOS)

`sudo vibe setup` installs: dnsmasq, `/etc/resolver/vibe`, pf LaunchDaemon (port 80→7999 at boot), user LaunchAgent (daemon at login). De-escalates brew/launchctl ops via `SUDO_USER`.

## Files at runtime

- `~/.vibe/config.json` — optional config (daemon port, TLD, dashboard view preference)
- `~/.vibe/routes.json` — persisted routes (sticky, managed, bookmark)
- `~/.vibe/daemon.pid` — daemon PID
- `~/.vibe/vibe.sock` — Unix socket
- `~/.vibe/daemon.log` — daemon log
- `~/.vibe/{name}.log` — per-route process logs
