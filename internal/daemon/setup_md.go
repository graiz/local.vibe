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

## Quick Start

### 1. Add a vibe.json to your project

`+"`"+`json
{"name": "myapp", "port": 3000, "cmd": "npm run dev"}
`+"`"+`

### 2. Start it

`+"`"+`bash
cd /path/to/your/project
vibe start
`+"`"+`

Your app is now available at **http://myapp.%[1]s**

The daemon manages the process — if it stops, visiting myapp.%[1]s shows a
"Start" button to relaunch it.

### 3. Stop it

`+"`"+`bash
vibe stop myapp
`+"`"+`

## Other Ways to Start

`+"`"+`bash
# Start with inline args (no vibe.json needed)
vibe start myapp 3000 -- npm run dev

# Start an already-registered route
vibe start myapp
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

## Command Best Practices

The `+"`"+`cmd`+"`"+` field runs inside a login shell (`+"`"+`$SHELL -lic`+"`"+`), so your PATH is
available — but some common pitfalls can cause startup failures:

### Use explicit binary names

| Instead of | Use | Why |
|-----------|-----|-----|
| `+"`"+`python app.py`+"`"+` | `+"`"+`python3 app.py`+"`"+` | `+"`"+`python`+"`"+` doesn't exist on macOS by default |
| `+"`"+`pip install && ...`+"`"+` | `+"`"+`pip3 install && ...`+"`"+` | Same issue |
| `+"`"+`node server.js`+"`"+` | `+"`"+`node server.js`+"`"+` | Usually fine if nvm is in .zshrc |

### Python: use a virtualenv

Activate the venv in your command so the right Python and packages are used:

`+"`"+`json
{"name": "myapp", "port": 5000, "cmd": "source .venv/bin/activate && python app.py"}
`+"`"+`

Or use the venv's Python directly:

`+"`"+`json
{"name": "myapp", "port": 5000, "cmd": ".venv/bin/python app.py"}
`+"`"+`

### Node.js: nvm / fnm

If you use nvm or fnm, they're loaded via .zshrc which vibe sources
automatically. `+"`"+`npm run dev`+"`"+` and `+"`"+`node server.js`+"`"+` should just work.

### Avoid debug/reload modes that fork

Some frameworks (Flask, Django) fork a child process in debug mode.
This can cause port conflicts and unexpected crashes. For vibe-managed
processes, consider disabling the reloader:

`+"`"+`json
{"name": "myapi", "port": 5000, "cmd": "flask run --no-reload --port 5000"}
`+"`"+`

Or for raw Flask apps:

`+"`"+`python
app.run(debug=False, host='0.0.0.0', port=5000)
`+"`"+`

## Static Port Mapping

For services you manage yourself (no process management needed):

`+"`"+`bash
vibe register homeassistant 8123
vibe deregister myapp
`+"`"+`

## CLI Reference

`+"`"+`bash
vibe start                           # Start app from vibe.json
vibe start myapp                     # Start existing registered route
vibe start myapp 3000 -- npm run dev # Register + start inline
vibe stop myapp                      # Stop a managed app
vibe register myapp 3000             # Static port mapping
vibe deregister myapp                # Remove a route
vibe list                            # List all routes
vibe open myapp                      # Open in browser
`+"`"+`

## Route Types

| Type | Created by | Behavior |
|------|-----------|----------|
| **managed** | `+"`"+`vibe start`+"`"+` | Daemon manages lifecycle; start/stop from dashboard |
| **sticky** | `+"`"+`vibe register`+"`"+` | Persists across daemon restarts |
| **bookmark** | Dashboard | Redirects (307) to an external URL |

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
2. **pf** (macOS) forwards port 80 to the daemon on port 7999
3. The daemon reverse-proxies each request to the registered local port
`, tld)
}
