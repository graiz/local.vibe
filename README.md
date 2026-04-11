# local.vibe

Friendly `.vibe` names for your local dev servers. No more remembering port numbers.

## Setup

```bash
git clone https://github.com/graiz/Vibe.local.git
cd Vibe.local
./setup.sh
```

This installs everything (Homebrew, Go, dnsmasq, port forwarding) and opens the dashboard.

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
vibe register myapp 3000      # Map myapp.vibe → localhost:3000
vibe deregister myapp          # Remove a route
vibe launch                    # Launch managed app (reads vibe.json)
vibe stop myapp                # Stop a managed app
vibe run myapp 3000 -- npm start  # Run command, auto-deregister on exit
vibe list                      # List all routes
vibe open myapp                # Open in browser
vibe dev                       # Rebuild + restart daemon (for development)
```

### vibe.json

Drop this in a project root, then run `vibe launch`:

```json
{"name": "myapp", "port": 3000, "cmd": "npm run dev"}
```

### Route Types

| Type | Created by | Lifecycle |
|------|-----------|-----------|
| **managed** | `vibe launch` / dashboard | Daemon manages the process; start/stop from dashboard |
| **sticky** | `vibe register` | Persists across daemon restarts |
| **pid** | `vibe run` | Auto-removed when tracked PID dies |
| **ttl** | `--ttl` flag | Auto-expires after N seconds |
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
