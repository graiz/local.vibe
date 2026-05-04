# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

local.vibe — a local DNS daemon that gives dev servers friendly `.vibe` names. CLI binary is `vibe`. Single Go binary, minimal dependencies (only Cobra for CLI, rest is stdlib).

## Workflow

- **Never commit unless the user explicitly asks.** Do not auto-commit after completing a task or bundle commits into other actions.
- **Always run `vibe dev` after changes** to rebuild and restart the daemon.
- **Tests must pass before declaring a task done or pushing.** Run `go build ./... && go vet ./... && go test ./...` after any change. If a test fails — even one that looks unrelated or flaky — investigate before continuing. Do not push or open PRs over a red test suite. If a failure is genuinely flaky, re-run a few times; if it reproduces, fix it or call it out explicitly to the user before moving on.
- **Check CI on `main` periodically.** If CI is red on `main`, the next change should fix it before adding new work — don't keep stacking commits on a broken pipeline.

## Build & test

```bash
vibe dev              # rebuild binary + restart daemon (do this after every change)
go build -o vibe .    # just build
go test ./...         # run tests
go vet ./...          # lint (also runs in CI)
```

The daemon runs a compiled binary at `/opt/homebrew/bin/vibe`, not source — changes aren't picked up until rebuilt. `vibe dev` handles the full cycle: build → install → kill old daemon → LaunchAgent auto-restarts with new binary.

## Architecture

**Request flow:** Browser → dnsmasq (*.vibe → 127.0.0.1) → pf (443 → 7443, 80 → 7999) → daemon (HTTPS or HTTP) → reverse proxy → app on target port. Bookmark routes either redirect (307) to an external URL or reverse-proxy to it (when `proxy=true`). HTTP requests redirect (301) to HTTPS when TLS is enabled.

**Three layers, strictly separated:**

1. **CLI commands** (`cmd/`) — Cobra commands. Know nothing about daemon internals. Use `internal/client` to talk to daemon.
2. **Client** (`internal/client/`) — HTTP wrapper. Tries Unix socket (`~/.vibe/vibe.sock`) first, falls back to TCP (`127.0.0.1:7999`).
3. **Daemon** (`internal/daemon/`) — HTTP server with embedded HTML dashboard. Core components:
   - `daemon.go` — Server struct, Start/Stop, HTTP routing by Host header, TLS listener with cert hot-reload
   - `api.go` — REST endpoints under `/_api/` (register, deregister, update, list, health, start, stop, ready, repair, preferences). Route names validated as DNS-safe (lowercase alphanumeric + hyphens). All user input HTML-escaped in dashboard output.
   - `routes.go` — Thread-safe RouteTable (RWMutex + map), RouteType enum, atomic per-route Failure diagnostics
   - `process.go` — ProcessManager spawns/kills managed child processes (uses process groups for clean shutdown). On immediate crash, returns a structured `StartError` with the tail of the log file.
   - `monitor.go` — Background goroutine sweeps dead PIDs and expired TTLs every 5s
   - `persistence.go` — Saves/loads sticky, managed, and bookmark routes to `~/.vibe/routes.json`
   - `dashboard.go` — Embedded HTML dashboard with modal UI for adding/editing routes
   - `startpage.go` — "Not running" page for stopped managed routes with Start button; surfaces recovery hints as a one-click "Kill PID X and Retry"
   - `repairpage.go` — "Reconnecting..." page shown when a route's port goes dark; polls `/_api/routes/{name}/repair` to auto-discover the new port
   - `log_scan.go` — regex patterns for extracting recovery hints (orphan PID, EADDRINUSE) from failed process log tails
   - `port_discover.go` — finds a managed route's real listening port via `lsof` on the process group and log-tail regex
   - `proxy_bookmark.go` — reverse-proxy for bookmark routes with `proxy=true` (landing path redirect, Location/cookie rewrites, X-Forwarded-For suppression)
   - `sync_config.go` — re-reads `vibe.json` from a managed route's `Dir` on each `Start` so edits to `cmd`, `oauth_callback_port`, or `reserve_ports` take effect without re-registering
   - `oauth_bridge.go` — per-route localhost OAuth callback listeners (see `oauth_callback_port`); 307-forward `?code=...` from `localhost:N` to `https://name.vibe/...` so OAuth providers that require a localhost redirect URI work without leaving the .vibe origin
   - `theme.go` — Shared CSS/HTML head (Geist fonts, Vercel-inspired dark theme)
   - `setup_md.go` — Markdown setup guide served at `/setup.md`

**Cert** (`internal/cert/`) — Generates local ECDSA CA + wildcard leaf certs using Go stdlib. Installs CA in macOS Keychain. Leaf certs use explicit SANs per route (Chrome rejects `*.vibe` wildcards).

**Config** (`internal/config/`) — Loads `~/.vibe/config.json`, falls back to defaults. Daemon port 7999, TLS port 7443 (disabled by default, enabled by `vibe setup`), TLD "vibe", log level "warn", dashboard view "list".

## Route types

Five route types with different lifecycle semantics:
- **sticky** — `vibe register`; persists across daemon restarts; reverse-proxied
- **pid** — API only; auto-removed when tracked PID dies
- **ttl** — `--ttl` flag on register; auto-expires after N seconds
- **managed** — `vibe start` (reads `vibe.json` or inline args); daemon manages the child process, dashboard has start/stop buttons. Port can be omitted for auto-assignment. Optional fields: `oauth_callback_port` (binds a localhost listener that 307-forwards to the .vibe URL — for OAuth providers that require a localhost redirect URI), `reserve_ports` (`{"name": port}` map of auxiliary ports the cmd also binds; reserved across the route table to prevent collisions, exposed to the child as `PORT_<UPPER_NAME>` env vars), `idle_timeout` (minutes; 0 = never auto-stop).
- **bookmark** — External URL (e.g. Tailscale address); persists across restarts; visiting `name.vibe` either 307-redirects to the external URL or reverse-proxies to it (per-route `proxy` flag). Added/edited via dashboard modal.

## Key patterns

- **Route status:** Two separate fields — `Running` (process is alive) and `Ready` (port is accepting TCP connections). Managed routes start `Running=true, Ready=false`; a background goroutine polls the port every 500ms for up to 30s and flips `Ready=true` once the port responds. This handles REPL-wrapped servers where the process is alive before the HTTP server binds.
- **Dual communication:** Unix socket (preferred) with TCP fallback. Same HTTP mux serves both.
- **Thread safety:** RouteTable uses RWMutex, ProcessManager uses Mutex.
- **Process groups:** Managed processes use `Setpgid: true` and SIGTERM to `-pgid` to kill entire process trees.
- **Shell login:** Spawns `$SHELL -lc` so managed processes get full PATH (nvm, Homebrew, etc.).
- **PID file safety:** Only written after successful TCP bind (tested in `daemon_test.go`).
- **Zero-downtime restarts:** `vibe dev` kills daemon → LaunchAgent restarts → persisted routes survive.
- **Input validation:** Route names must match `[a-z0-9-]`, "local" is reserved. All user-supplied strings HTML-escaped in dashboard output to prevent XSS.
- **Port auto-assignment:** Managed routes can omit `port` (or set to 0). `findFreePort(table)` scans 3000-3999, skipping ports claimed by existing routes, then falls back to OS assignment. The assigned port is injected as a `PORT` env var when spawning the child process and returned in the register API response. Persisted in `routes.json` so it survives daemon restarts.
- **Port conflict detection:** Before starting a managed process, verifies the port is free. If `killPort` fails to clear it, returns 409 with a clear error. Registering a route name that's already running returns 409 immediately. The `/ready` endpoint returns both `ready` and `running` so poll loops detect post-start crashes immediately.
- **Route icons:** Two fields — `Icon` (user-chosen emoji) and `AutoIcon` (auto-detected favicon as data URI). Display priority: Icon > AutoIcon > deterministic hash-based pool pick. Dashboard modal shows preview + emoji picker, never raw data URIs.
- **Dashboard view persistence:** List/grid toggle saved server-side via `PUT /_api/preferences` into `config.json`. Rendered server-side on page load — no flash of wrong view.
- **Embedded UI:** All HTML/CSS/JS is inline Go strings — no external assets, no build step. Dashboard includes a modal for CRUD operations on routes. Toast notifications surface errors from async actions.
- **TLS hot-reload:** Daemon holds a `sync.RWMutex`-guarded `*tls.Certificate` served via `GetCertificate`. When routes change (`saveStickyRoutes`), the leaf cert is regenerated with updated SANs and atomically swapped — no restart needed. The CA (10-year, trusted in Keychain) stays fixed; only the leaf (825-day) rotates.
- **Managed-route self-healing:** When a route's registered port stops answering, `routeRequest` serves the repair page (or the start page when the child is gone). `port_discover.go` tries `lsof` on the process group and a log-tail regex to find the real listening port; `RouteTable.UpdatePort` atomically rewrites the registration so subsequent requests proxy correctly. Start failures attach a tailed log + recovery hint (orphan PID, EADDRINUSE) via `StartError` → `Failure`; the start page surfaces a one-click "Kill PID X and Retry" button, and `safeKillPID` refuses to signal the daemon itself or other managed routes.
- **Browser-form vs JSON requests:** Some `/_api/routes/*` handlers (`handleStart`, `handleStop`, etc.) 303-redirect back to the dashboard when the request looks like a browser HTML form post — detected via `isBrowserFormRequest` (Content-Type `application/x-www-form-urlencoded`). The CLI client sends `Accept: application/json` and gets the JSON response directly. Don't gate the redirect on the `Accept` header alone: a missing Accept used to be misclassified as a browser request, and the resulting 303 to `https://local.vibe/` killed the CLI when followed over the Unix-socket transport (TLS over unix conn fails, surfaced as a spurious "daemon not running").
- **Bookmark proxy mode:** When a bookmark has `proxy=true`, `proxyBookmark` reverse-proxies to the upstream instead of 307-redirecting. The upstream `Host` header is forced to the external URL's host (SNI/vhost correctness). Same-origin 3xx `Location` headers are rewritten to the `.vibe` host, `Domain=` is stripped from `Set-Cookie` so browsers actually store cookies, and `X-Forwarded-For` is suppressed (strict upstreams like Home Assistant return 400 when they see it). The bookmark URL's path is treated as a landing destination: requests to `name.vibe/` 302 once to the path, but everything else passes through at the origin so SPA assets loaded from `/` resolve correctly. Optional per-route `insecure_skip_verify` disables upstream TLS verification for self-signed targets (Tailscale MagicDNS, etc.).

## System setup (macOS)

`sudo vibe setup` installs: dnsmasq, `/etc/resolver/vibe`, pf LaunchDaemon (port 80→7999 and 443→7443 at boot), TLS certificates (local CA + leaf, trusted in macOS Keychain), enables TLS in config, user LaunchAgent (daemon at login). De-escalates brew/launchctl ops via `SUDO_USER`.

## Files at runtime

- `~/.vibe/config.json` — optional config (daemon port, TLS settings, TLD, dashboard view preference)
- `~/.vibe/routes.json` — persisted routes (sticky, managed, bookmark)
- `~/.vibe/daemon.pid` — daemon PID
- `~/.vibe/vibe.sock` — Unix socket
- `~/.vibe/daemon.log` — daemon log
- `~/.vibe/{name}.log` — per-route process logs
- `~/.vibe/certs/` — TLS certificates (ca.pem, ca-key.pem, vibe.pem, vibe-key.pem)
