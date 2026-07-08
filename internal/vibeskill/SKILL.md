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

Minimal `vibe.json` (omit `port` so vibe assigns one and sets `$PORT`):

```json
{ "name": "myapp", "cmd": "npm run dev" }
```

The command must bind `$PORT` (read `process.env.PORT` / `os.environ["PORT"]`,
or pass `--port $PORT`). The app is then reachable at `https://myapp.vibe`.

## Finding what's already running (don't guess the URL)

- `vibe list` — every route and its `https://<name>.vibe` URL
- `vibe status` — whether the daemon is up
- `vibe doctor` — diagnose and repair the request path

## Full guide

For framework-specific config (Vite/Next `allowedHosts`, Flask, Django, Rails,
Jekyll), auto-port details, `reserve_ports` for multi-port apps, and OAuth
localhost callbacks, read the authoritative guide from the running daemon —
don't reproduce it from memory:

```bash
curl http://localhost:7999/setup.md
```
