# local.vibe

Friendly `.vibe` names for your local dev servers. No more remembering port numbers.

## Setup

```bash
git clone https://github.com/graiz/local.vibe.git
cd local.vibe
./setup.sh
```

This installs everything (Homebrew, Go, dnsmasq, port forwarding) and opens the dashboard. Currently macOS only — see [future plan](FUTURE_PLAN.md) for Windows/Linux.

**That's it.** Everything else happens at [http://local.vibe](http://local.vibe).

---

## For Agents & Automation

The sections below are reference documentation for CLI tools, AI agents, and automation scripts.

### How it works

```
Browser → dnsmasq (*.vibe → 127.0.0.1) → pf (port 80 → 7999) → vibe daemon → reverse proxy → your app
```

### CLI Reference

```bash
vibe start                           # Start app from vibe.json in current dir
vibe start myapp                     # Start an already-registered route
vibe start myapp 3000 -- npm run dev # Register + start a new managed app
vibe stop myapp                      # Stop a managed app
vibe register myapp 3000             # Static port mapping (no process management)
vibe deregister myapp                # Remove a route
vibe list                            # List all routes
vibe open myapp                      # Open in browser
vibe dev                             # Rebuild + restart daemon (for development)
```

### vibe.json

Drop this in a project root, then run `vibe start`:

```json
{"name": "myapp", "port": 3000, "cmd": "npm run dev"}
```

### Route Types

| Type | Created by | Lifecycle |
|------|-----------|-----------|
| **managed** | `vibe start` / dashboard | Daemon manages the process; start/stop from dashboard |
| **sticky** | `vibe register` | Persists across daemon restarts |
| **bookmark** | Dashboard | Redirects (307) to an external URL |

### API

```bash
curl http://local.vibe/_api/health          # Health check
curl http://local.vibe/_api/routes          # List routes (JSON)
curl -X POST http://local.vibe/_api/routes \
  -H 'Content-Type: application/json' \
  -d '{"name":"myapp","port":3000,"cmd":"npm run dev","dir":"/path/to/project"}'
```

### Runtime Files

| Path | Purpose |
|------|---------|
| `~/.vibe/routes.json` | Persisted routes |
| `~/.vibe/config.json` | Optional config |
| `~/.vibe/daemon.log` | Daemon log |
| `~/.vibe/{name}.log` | Per-route logs |

### Development

```bash
vibe dev           # rebuild + restart daemon
go test ./...      # run tests
```

### License

[MIT](LICENSE)
