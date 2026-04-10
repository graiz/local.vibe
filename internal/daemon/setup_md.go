package daemon

import (
	"fmt"
	"net/http"
)

// serveSetupMD serves a Markdown guide at /setup.md explaining how to
// configure projects to work with vibe.
func (s *Server) serveSetupMD(w http.ResponseWriter, r *http.Request) {
	tld := s.cfg.Daemon.TLD

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	fmt.Fprintf(w, `# local.vibe Setup

local.vibe gives your local dev servers friendly names.
Instead of remembering localhost:3000, use **myapp.%[1]s**.

## Quick Start (Recommended)

### 1. Add a vibe.json to your project

`+"`"+`json
{"name": "myapp", "port": 3000, "cmd": "npm run dev"}
`+"`"+`

### 2. Launch it

`+"`"+`bash
cd /path/to/your/project
vibe launch
`+"`"+`

Your app is now available at **http://myapp.%[1]s**

The daemon manages the process — if it stops, visiting myapp.%[1]s shows a
"Start" button to relaunch it. No need to go back to the terminal.

### 3. Stop it

`+"`"+`bash
vibe stop myapp
`+"`"+`

## vibe.json Reference

`+"`"+`json
{
  "name": "myapp",
  "port": 3000,
  "cmd": "npm run dev"
}
`+"`"+`

| Field | Required | Description |
|-------|----------|-------------|
| name | yes | The subdomain: name.%[1]s |
| port | yes | The port your app listens on |
| cmd | yes | Shell command to start the app |

## Framework-Specific Configuration

### Vite (React, Vue, Svelte, etc.)

Vite blocks requests from unrecognized hostnames by default. Add your
.%[1]s domain to the allowed hosts in `+"`"+`vite.config.js`+"`"+` / `+"`"+`vite.config.ts`+"`"+`:

`+"`"+`js
export default defineConfig({
  server: {
    allowedHosts: ['.%[1]s'],
  },
})
`+"`"+`

The `+"`"+`.%[1]s`+"`"+` pattern (with leading dot) allows all subdomains — so `+"`"+`myapp.%[1]s`+"`"+`,
`+"`"+`email.%[1]s`+"`"+`, etc. all work without listing each one.

### Next.js

Next.js may also need hostname configuration in `+"`"+`next.config.js`+"`"+`:

`+"`"+`js
module.exports = {
  allowedDevOrigins: ['*.%[1]s'],
}
`+"`"+`

## Other Registration Methods

### Wrap a command (auto-deregisters on exit)

`+"`"+`bash
vibe run myapp 3000 -- npm run dev
`+"`"+`

### Sticky register (always-on services)

`+"`"+`bash
vibe register homeassistant 8123
`+"`"+`

### Remove a route

`+"`"+`bash
vibe deregister myapp
`+"`"+`

## Route Types

| Type | Command | Behavior |
|------|---------|----------|
| **managed** | `+"`"+`vibe launch`+"`"+` (via vibe.json) | Daemon manages lifecycle; restart from browser |
| **sticky** | `+"`"+`vibe register name port`+"`"+` | Persists across daemon restarts |
| **pid-tracked** | `+"`"+`vibe run name port -- cmd`+"`"+` | Auto-removed when process exits |

## Managing Routes

`+"`"+`bash
# List active routes
vibe list

# Open a route in the browser
vibe open myapp

# Stop a managed app
vibe stop myapp

# Remove a route entirely
vibe deregister myapp
`+"`"+`

## API

The daemon exposes a JSON API at http://localhost:7999:

`+"`"+`bash
# Health check
curl http://local.%[1]s/_api/health

# List all routes
curl http://local.%[1]s/_api/routes

# Register via API (managed)
curl -X POST http://local.%[1]s/_api/routes \
  -H 'Content-Type: application/json' \
  -d '{"name": "myapp", "port": 3000, "cmd": "npm run dev", "dir": "/path/to/project"}'

# Start a stopped managed app
curl -X POST http://local.%[1]s/_api/routes/myapp/start

# Stop a managed app
curl -X DELETE http://local.%[1]s/_api/routes/myapp/stop

# Deregister via API
curl -X DELETE http://local.%[1]s/_api/routes/myapp
`+"`"+`

## How It Works

1. **dnsmasq** resolves *.%[1]s to 127.0.0.1
2. **pf** (macOS) or **iptables** (Linux) forwards port 80 to the daemon on port 7999
3. The daemon reverse-proxies each request to the registered local port

## Install

`+"`"+`bash
# macOS (one-time, requires sudo)
sudo vibe setup
`+"`"+`

This installs dnsmasq, configures DNS for *.%[1]s, sets up port forwarding
(80 → 7999), and registers a LaunchAgent so the daemon starts at login.
`, tld)
}
