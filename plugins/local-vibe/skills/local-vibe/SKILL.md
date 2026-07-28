---
name: local-vibe
description: >-
  Use whenever starting, running, restarting, or wiring up a local dev server,
  web app, or API on this machine — or whenever you need one's URL. This machine
  runs local.vibe: dev servers get a stable https://<name>.vibe address via the
  `vibe` CLI. Do NOT guess, hardcode, or assume a port (3000, 8080, 5173, ...) or
  a http://localhost:PORT URL — ports are assigned and tracked by vibe, so a
  guessed port collides with another project or points at the wrong app. Register
  the server with vibe and read the real URL/port back instead.
---

# local.vibe

`local.vibe` (CLI: `vibe`) is a local DNS daemon, already installed and running
on this machine, that gives every dev server a friendly, collision-free
`https://<name>.vibe` address.

## The rule: never guess a port or a localhost URL

When you start or reference a local server, **do not** pick a port yourself,
write `http://localhost:3000`, or assume the framework's default. Two projects
that both grab `3000` clobber each other. Let vibe assign and track the port,
then use the `https://<name>.vibe` URL it hands back.

## Starting a server (the two commands you need)

- **In a project that has a `vibe.json`:** `vibe start`
- **Ad hoc, no config file:** `vibe start <name> <port> -- <command>`
  Pass `0` for the port to let vibe auto-assign one and inject it as `$PORT`.
  Do **not** use this form inside a git worktree — see below.

Minimal `vibe.json` (omit `port` so vibe assigns one and sets `$PORT`):

```json
{ "name": "myapp", "cmd": "npm run dev" }
```

The command must bind `$PORT` (read `process.env.PORT` / `os.environ["PORT"]`,
or pass `--port $PORT`). The app is then reachable at `https://myapp.vibe`.

## Git worktrees — run `vibe start`, change nothing else

Inside a linked worktree, the whole procedure is:

```bash
vibe start            # from the worktree directory. That's it.
```

You get `https://<branch-slug>.<app>.vibe` on its own auto-assigned port. The
main checkout's `<app>.vibe` and every other worktree keep running untouched —
**vibe already guarantees that isolation, so you never need to arrange it
yourself.**

**Do not "avoid a collision" — there isn't one.** Specifically, in a worktree:

- **Do not change `name` in the worktree's `vibe.json`.** It is a copy, and
  renaming it (`myapp` → `myapp-wt`) is the single most common way to break
  this. The worktree stops being a worktree *of* the app: it disappears from
  the app's picker and dashboard group, and it no longer inherits the app's
  `oauth_callback_port`, so OAuth logins break. vibe reads the app name from
  the main checkout and will tell you when the copy disagrees — but leave the
  copy alone.
- **Do not register a second app** with `vibe start <name> <port> -- <cmd>`.
  That creates an unrelated route, not a worktree of the app.
- **Do not pick a port**, and do not copy the parent's `port` — vibe assigns a
  free one and injects `$PORT`.

Only the subdomain is yours to choose, via `vibe start --as <name>` if the
branch slug is unwieldy.

A worktree inherits the parent app's OAuth callback bridge, and vibe copies the
parent's untracked `.env*` files in if the worktree has none. Deleting the
worktree removes the route automatically.

## Finding what's already running (don't guess the URL)

- `vibe list` — every route and its `https://<name>.vibe` URL
- `vibe status` — whether the daemon is up
- `vibe doctor` — diagnose and repair the request path

## Full guide

For framework-specific config (Vite/Next `allowedHosts`, Flask, Django, Rails,
Jekyll), auto-port details, `reserve_ports` for multi-port apps, worktree
routes, and OAuth localhost callbacks, read the authoritative guide from the
running daemon — don't reproduce it from memory:

```bash
curl -sS --max-time 5 http://localhost:7999/setup.md || curl -sS --max-time 5 http://local.vibe/setup.md
```

The daemon's fixed port is tried first so the guide is reachable even when DNS
or the privileged-port redirect is down. The `local.vibe` fallback covers the
opposite case: a VPN kill-switch or firewall that filters direct connections to
loopback high ports while the redirect still works. If both fail, run
`vibe doctor`.
