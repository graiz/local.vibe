# local.vibe

Give your local dev servers friendly names.

Instead of remembering `localhost:3000`, `localhost:5678`, `localhost:8123`...
use `myapp.vibe`, `n8n.vibe`, `desk.vibe`.

## How it works

```
Browser → dnsmasq (*.vibe → 127.0.0.1) → pf (port 80 → 7999) → vibe daemon → reverse proxy → your app
```

A lightweight daemon intercepts `.vibe` DNS requests and reverse-proxies them to the right local port. Bookmark routes redirect (307) to external URLs instead of proxying.

## Requirements

- **macOS** (uses dnsmasq, pf, LaunchAgent)
- **Go 1.22+** (to build from source)
- **Homebrew** (for dnsmasq)

## Install

```bash
go build -o vibe .
sudo vibe setup    # installs dnsmasq, pf rules, LaunchAgent
vibe install       # copies binary to /opt/homebrew/bin/vibe
```

## Quick start

```bash
# Register a running dev server
vibe register myapp 3000
# → http://myapp.vibe

# Run a command and auto-register it
vibe run --name api --port 8080 -- npm start

# Launch a managed service (reads vibe.json in current dir)
vibe launch

# List all routes
vibe list

# Open a route in your browser
vibe open myapp

# Remove a route
vibe deregister myapp
```

## Route types

| Type | Created by | Lifecycle |
|------|-----------|-----------|
| **sticky** | `vibe register` | Persists across daemon restarts |
| **pid** | `vibe run` | Auto-removed when tracked PID dies |
| **ttl** | `--ttl` flag | Auto-expires after N seconds |
| **managed** | `vibe launch` | Daemon manages the process; start/stop from dashboard |
| **bookmark** | Dashboard UI | Redirects (307) to an external URL |

## Dashboard

Visit [http://local.vibe](http://local.vibe) for a web dashboard with route management, including adding bookmarks and starting/stopping managed services.

## Configuration

Optional config at `~/.vibe/config.json`. All fields have sensible defaults:

```json
{
  "daemon": {
    "port": 7999,
    "tld": "vibe",
    "pid_check_interval": 5
  },
  "dashboard": {
    "enabled": true,
    "theme": "dark"
  },
  "logging": {
    "level": "warn",
    "max_size_mb": 10
  }
}
```

## Runtime files

| Path | Purpose |
|------|---------|
| `~/.vibe/config.json` | Optional configuration |
| `~/.vibe/routes.json` | Persisted routes (sticky, managed, bookmark) |
| `~/.vibe/daemon.pid` | Daemon PID |
| `~/.vibe/vibe.sock` | Unix socket |
| `~/.vibe/daemon.log` | Daemon log |
| `~/.vibe/{name}.log` | Per-route process logs |

## Development

```bash
vibe dev           # rebuild binary + restart daemon
go test ./...      # run tests
go vet ./...       # static analysis
```

The daemon runs from a compiled binary at `/opt/homebrew/bin/vibe` — changes aren't picked up until rebuilt. `vibe dev` handles the full cycle: build, install, kill old daemon, LaunchAgent auto-restarts with the new binary.

## License

[MIT](LICENSE)
