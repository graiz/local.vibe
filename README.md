# local.vibe

Friendly `.vibe` names for your local dev servers. No more remembering port numbers.

## Setup

```bash
git clone https://github.com/graiz/local.vibe.git
cd local.vibe
./setup.sh
```

This installs everything (Homebrew, Go, dnsmasq, port forwarding, HTTPS certificates) and opens the dashboard. Currently macOS only.

**That's it.** Everything else happens at [https://local.vibe](https://local.vibe).

---

## For Agents & Automation

A quick-start guide for configuring individual projects is served by the daemon.
Fetch it with `curl http://localhost:7999/setup.md` (or visit
[https://local.vibe/setup.md](https://local.vibe/setup.md) in a browser).
The sections below are the full reference documentation.

### How it works

```
Browser → dnsmasq (*.vibe → 127.0.0.1) → pf (443 → 7443, 80 → 7999) → vibe daemon (HTTPS/HTTP) → reverse proxy → your app
```

### CLI Reference

```bash
vibe --help                          # Show all commands
vibe [command] --help                # Help for a specific command
vibe start                           # Start app from vibe.json in current dir
vibe start myapp                     # Start an already-registered route
vibe start myapp 3000 -- npm run dev # Register + start a new managed app
vibe stop myapp                      # Stop a managed app
vibe register myapp 3000             # Static port mapping (no process management)
vibe deregister myapp                # Remove a route
vibe list                            # List all routes
vibe status                          # Show daemon health
vibe open myapp                      # Open in browser
vibe dev                             # Rebuild + restart daemon (for development)
```

### vibe.json

Drop this in a project root, then run `vibe start`:

```json
{
  "name": "myapp",
  "port": 3000,
  "cmd": "npm run dev"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Subdomain: `name.vibe` |
| `port` | yes | Port your app listens on |
| `cmd` | yes | Shell command to start the app |
| `icon` | no | Emoji or image URL for the dashboard |
| `idle_timeout` | no | Auto-stop after N minutes of no traffic (0 = never) |

**Command tips:** Use `python3` not `python` on macOS. For Python apps, use a venv: `".venv/bin/python app.py"`. For Flask/Django, disable the reloader (not debug mode): `app.run(debug=True, use_reloader=False)` or `flask run --debug --no-reload`.

### Route Types

| Type | Created by | Lifecycle |
|------|-----------|-----------|
| **managed** | `vibe start` / dashboard | Daemon manages the process; start/stop from dashboard |
| **sticky** | `vibe register` | Persists across daemon restarts |
| **bookmark** | Dashboard | Redirects (307) to an external URL |

### Dashboard

The dashboard at [https://local.vibe](https://local.vibe) provides:
- List and grid views (preference persisted across restarts)
- Start/stop managed routes
- Add/edit/delete routes via modal UI
- Auto-detected favicons from running apps
- Custom emoji icons via picker or text input
- Bookmark routes that redirect to external URLs
- Toast notifications for errors (port conflicts, crash details)

### API

All endpoints are under `https://local.vibe/_api/` (or `http://localhost:7999/_api/`).

```bash
# Health & listing
curl /_api/health                            # {"status":"ok","routes":3,"uptime":120}
curl /_api/routes                            # List all routes (JSON array)

# Register a new route
curl -X POST /_api/routes \
  -H 'Content-Type: application/json' \
  -d '{"name":"myapp","port":3000,"cmd":"npm run dev","dir":"/path/to/project"}'

# Update an existing route
curl -X PUT /_api/routes/myapp \
  -H 'Content-Type: application/json' \
  -d '{"port":3001,"icon":"🚀"}'

# Start / stop managed routes
curl -X POST /_api/routes/myapp/start        # Returns 409 if port is occupied
curl -X POST /_api/routes/myapp/stop

# Check readiness
curl /_api/routes/myapp/ready                # {"ready":true,"running":true}

# Delete a route
curl -X DELETE /_api/routes/myapp

# Set dashboard preferences
curl -X PUT /_api/preferences \
  -H 'Content-Type: application/json' \
  -d '{"view":"grid"}'                       # "list" or "grid"
```

**Error handling:** Port conflicts return `409` with the occupied port number. Immediate process crashes include the last few lines of `~/.vibe/{name}.log` in the error response.

### Runtime Files

| Path | Purpose |
|------|---------|
| `~/.vibe/routes.json` | Persisted routes (sticky, managed, bookmark) |
| `~/.vibe/config.json` | Config (daemon port, TLD, dashboard view preference) |
| `~/.vibe/daemon.log` | Daemon log |
| `~/.vibe/{name}.log` | Per-route process logs (tailed on crash for diagnostics) |
| `~/.vibe/daemon.pid` | Daemon PID file |
| `~/.vibe/vibe.sock` | Unix socket for CLI-to-daemon communication |

### Framework Notes

**Vite** (React, Vue, Svelte): Add `.vibe` to allowed hosts in `vite.config.js`:
```js
export default defineConfig({ server: { allowedHosts: ['.vibe'] } })
```

**Next.js**: Add to `next.config.js`:
```js
module.exports = { allowedDevOrigins: ['*.vibe'] }
```

**Flask**: Disable the reloader (debug error pages still work):
```python
app.run(debug=True, use_reloader=False, host='0.0.0.0', port=5000)
```
Or via CLI: `flask run --debug --no-reload --port 5000`

**Django**: Disable the reloader:
```bash
python3 manage.py runserver --noreload 0.0.0.0:8000
```

### Development

```bash
vibe dev           # rebuild + restart daemon
go test ./...      # run tests
go vet ./...       # lint
```

### License

[MIT](LICENSE)
