# local.vibe

> `myapp.vibe` instead of `localhost:3000` — for every project on your Mac.

<p align="center">
  <img src="docs/dashboard-grid.jpg" alt="local.vibe dashboard" width="820">
</p>

Dev work drifts into a mess of `localhost:3000`, `localhost:5173`, `localhost:8080` tabs. Which port was the blog on again? **local.vibe** gives every local project a friendly `.vibe` hostname and puts start/stop controls in one dashboard at [https://local.vibe](https://local.vibe).

macOS only. Single Go binary. No external services.

## What you get

- **Friendly hostnames** — `myapp.vibe` resolves to your local app over HTTP *and* HTTPS
- **Auto-assigned ports** — drop `port` from `vibe.json`, vibe picks a free one and exposes it as `$PORT`
- **Dashboard** — start/stop managed apps, add bookmarks, pick icons, switch list/grid
- **HTTPS built-in** — a local CA is trusted in your Keychain; per-route SANs hot-reload without restart
- **Agent-friendly** — paste `curl http://localhost:7999/setup.md` into Claude Code / Cursor and it understands the setup
- **Bookmark anything** — route `tailscale.vibe` → your Tailscale machine, `office.vibe` → Home Assistant
- **Zero hidden deps** — single binary, Cobra + Go stdlib, no Node, no Docker

## Install

```bash
git clone https://github.com/graiz/local.vibe.git
cd local.vibe
./setup.sh
```

Installs Homebrew (if missing), Go, dnsmasq, `/etc/resolver/vibe`, pf rules for port forwarding, and a local TLS CA trusted in your Keychain — then opens the dashboard.

## Your first app

Drop this in any project root:

```json
{
  "name": "myapp",
  "cmd": "npm run dev"
}
```

```bash
vibe start
# started: myapp → https://myapp.vibe (port 3000)
```

vibe picks a free port starting at 3000 and passes it to your app via `$PORT`. The daemon keeps it running; `vibe stop myapp` shuts it down. Visit `https://myapp.vibe` in any browser.

## Dashboard

<p align="center">
  <img src="docs/dashboard-list.jpg" alt="local.vibe list view" width="820">
</p>

Switch between grid and list views (preference persists across restarts). Each row shows route type, port, uptime, and start/stop/edit controls. The modal editor supports custom emoji or auto-detected favicons; bookmark routes either redirect or reverse-proxy to any external URL — handy for Tailscale hosts or Home Assistant dashboards. Proxy mode keeps the `.vibe` name in the browser's URL bar, with an optional opt-in for self-signed upstream certs.

When a managed route's process rebinds to a different port or exits, the daemon serves a "Reconnecting…" or "Not running" page instead of a dead proxy. It auto-discovers the new port via `lsof` and log-tail regex, and surfaces recovery hints (orphan PID, port-in-use) as one-click "Kill PID X and Retry" buttons.

---

## Reference

### CLI

```bash
vibe --help                          # Show all commands
vibe start                           # Start from vibe.json in the current dir
vibe start myapp                     # Start an already-registered route
vibe start myapp 3000 -- npm run dev # Register + start inline
vibe stop myapp                      # Stop a managed app
vibe restart myapp                   # Stop + start (picks up vibe.json edits)
vibe register myapp 3000             # Static mapping (no process management)
vibe deregister myapp                # Remove a route
vibe list                            # List all routes
vibe status                          # Show daemon health
vibe open myapp                      # Open in browser
vibe dev                             # Rebuild + restart daemon (for contributors)
```

### `vibe.json`

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Subdomain: `name.vibe` |
| `port` | no | Port your app listens on (omit or `0` for auto-assign) |
| `cmd` | yes | Shell command to start the app |
| `icon` | no | Emoji or image URL for the dashboard |
| `idle_timeout` | no | Auto-stop after N minutes of no traffic (0 = never) |
| `oauth_callback_port` | no | Fixed localhost port for OAuth callbacks |
| `reserve_ports` | no | `{"name": port}` map of auxiliary ports the cmd binds; injected as `$PORT_<UPPER_NAME>` (see [setup.md](internal/daemon/setup.md#apps-that-bind-more-than-one-port)) |

**Tip:** use `python3` (not `python`) on macOS. For Python apps, prefer a venv: `".venv/bin/python app.py"`.

### Framework notes

**Vite** (React, Vue, Svelte) — add `.vibe` to `vite.config.js`:
```js
export default defineConfig({ server: { allowedHosts: ['.vibe'] } })
```

**Next.js** — in `next.config.js`:
```js
module.exports = { allowedDevOrigins: ['*.vibe'] }
```

**Flask** — disable the reloader (debug error pages still work):
```python
app.run(debug=True, use_reloader=False, host='0.0.0.0',
        port=int(os.environ.get("PORT", 5000)))
```
Or via CLI: `flask run --debug --no-reload --port $PORT`

**Django**:
```bash
python3 manage.py runserver --noreload 0.0.0.0:$PORT
```

### Route types

| Type | Created by | Lifecycle |
|------|-----------|-----------|
| **managed** | `vibe start` / dashboard | Daemon manages the process; start/stop controls |
| **sticky** | `vibe register` | Persists across daemon restarts |
| **bookmark** | Dashboard | Redirects (307) or reverse-proxies to an external URL |

### HTTP API

All endpoints live under `https://local.vibe/_api/` (or `http://localhost:7999/_api/`).

```bash
curl /_api/health                                # {"status":"ok","routes":3,"uptime":120}
curl /_api/routes                                # List all routes
curl -X POST /_api/routes \
  -H 'Content-Type: application/json' \
  -d '{"name":"app","cmd":"npm run dev","dir":"/path/to/project"}'
curl -X PUT    /_api/routes/app -d '{"port":3001,"icon":"🚀"}'
curl -X POST   /_api/routes/app/start            # 409 if port occupied
curl -X POST   /_api/routes/app/stop
curl           /_api/routes/app/ready            # {"ready":true,"running":true}
curl -X DELETE /_api/routes/app
curl -X PUT    /_api/preferences -d '{"view":"grid"}'
```

Port conflicts return `409` with the occupied port. Immediate process crashes include the last few lines of `~/.vibe/{name}.log` in the error response. Auto-assigned ports come back as `"port": <number>`.

### Runtime files

| Path | Purpose |
|------|---------|
| `~/.vibe/routes.json` | Persisted routes (sticky, managed, bookmark) |
| `~/.vibe/config.json` | Daemon config (port, TLD, dashboard view) |
| `~/.vibe/certs/` | Local CA + leaf cert (trusted in Keychain) |
| `~/.vibe/daemon.log` | Daemon log |
| `~/.vibe/{name}.log` | Per-route process logs (tailed on crash) |
| `~/.vibe/daemon.pid` | Daemon PID |
| `~/.vibe/vibe.sock` | Unix socket for CLI ↔ daemon |

### Agents & automation

A per-project setup guide is served by the daemon — paste this into any agentic IDE (Claude Code, Cursor, etc.) and the agent will know how to register a `.vibe` name for the project:

```bash
curl http://localhost:7999/setup.md
```

### Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). PRs welcome.

### License

[MIT](LICENSE)
