# local.vibe — Project Setup

Configure a project to work with local.vibe. For full CLI, API, and dashboard
docs, see the [README](https://github.com/graiz/local.vibe#readme).

## 1. Add a vibe.json to your project root

**Recommended** — omit `port` and let vibe pick one:

```json
{
  "name": "myapp",
  "cmd": "npm run dev"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Subdomain: `name.{{TLD}}` |
| `port` | no | **Leave this out.** Only set it when the app can't honor `$PORT` |
| `cmd` | yes | Shell command to start the app |
| `icon` | no | Emoji for the dashboard (e.g. `"🚀"`) |
| `idle_timeout` | no | Auto-stop after N minutes idle (0 = never) |
| `oauth_callback_port` | no | Fixed localhost port for OAuth callbacks (see below) |
| `reserve_ports` | no | Named auxiliary ports the cmd binds beyond `port` (see below) |

### Automatic port assignment (recommended)

Skip `port` and vibe picks a free one (starting from 3000) and injects it as
the `PORT` environment variable. Most frameworks respect this automatically —
no two projects will ever collide, and the assigned port persists across
restarts.

Your app reads `process.env.PORT` (Node), `os.environ["PORT"]` (Python), etc.
Pin a specific port only when the app hard-codes it or an external system
(e.g. a webhook) must reach a known number.

### Your cmd must bind `$PORT` (the #1 gotcha)

Vibe proxies `name.{{TLD}}` to the port it assigned, but it can't force your app
to listen there — your `cmd` has to bind it. Read it from the `$PORT` env var,
or pass `--port $PORT` on the command line. If the cmd instead lets the
framework fall back to its own default port, the app binds one port while vibe
proxies to another. Self-healing port-discovery papers over the mismatch for a
while, so it *looks* fine — until something else claims the registered port and
the route silently stops responding.

Most Node tooling (Vite, Next, CRA) reads `process.env.PORT` on its own. Many
others **don't** and need an explicit flag:

| Framework | cmd |
|-----------|-----|
| Jekyll | `bundle exec jekyll serve --port $PORT` |
| Rails | `bin/rails server -p $PORT` |
| Flask (CLI) | `flask run --no-reload --port $PORT` |
| Django | `python3 manage.py runserver --noreload 0.0.0.0:$PORT` |

When in doubt, pass the port explicitly — `--port $PORT` never hurts, even for
frameworks that would have read the env var anyway. And prefer auto-assign
(omit `port`) so a stale or system-claimed number — e.g. macOS AirPlay Receiver
holds **7000** — can never block your start.

### Apps that bind more than one port

Some dev setups run multiple processes that each bind their own port — a
backend on one port, a frontend dev server on another, maybe a metrics
endpoint on a third. Vibe routes traffic to a single `port` (the one
`name.{{TLD}}` resolves to), but the other ports still need to be reserved
so vibe doesn't auto-assign them to a different app and so collisions
surface as a clear error instead of silent traffic bleed-through.

Declare the auxiliary ports as a `reserve_ports` map keyed by a semantic
name. Each entry is exposed to your cmd as `PORT_<UPPER_NAME>`, so the
command never has to hardcode numeric values:

```json
{
  "name": "myapp",
  "port": 3012,
  "reserve_ports": { "server": 3001, "metrics": 9090 },
  "cmd": "concurrently \"cd server && PORT=$PORT_SERVER bun start\" \"cd client && bun dev -- --port $PORT\""
}
```

What vibe does with this:

- **Reserves** every value across the route table — `findFreePort` will not
  hand `3001` or `9090` to any other route, even after this app stops.
- **Pre-flights** each port on `vibe start`. If something stale holds one
  of them, the start page surfaces a recovery hint with the offending PID
  and a one-click "Kill PID X and retry" button — same UX as a primary-port
  collision.
- **Injects** `PORT_SERVER=3001` and `PORT_METRICS=9090` as env vars when
  spawning the cmd, alongside the usual `PORT=3012` for the routed port.

Naming rules:

- Keys must match `[a-zA-Z][a-zA-Z0-9_]*`. Case-insensitive — `Server` and
  `server` are the same key; the env var is always uppercase (`PORT_SERVER`).
- The literal name `port` is rejected (it would collide with the primary
  `$PORT`).
- Each port number must be unique across `port`, `oauth_callback_port`,
  and the rest of `reserve_ports`. Two names mapping to the same number is
  rejected — give it one canonical `$PORT_<NAME>`.

### OAuth with localhost callbacks

Some OAuth providers (Google, GitHub Apps, etc.) only allow redirect URIs on
`http://localhost:<port>`. Add `oauth_callback_port` and vibe opens a localhost
bridge on that port that 307-forwards **any** callback path to your `.{{TLD}}`
host — so `http://localhost:8787/auth/google/callback?...` and NextAuth's
`http://localhost:8787/api/auth/callback/google?...` both work unchanged:

```json
{
  "name": "myapp",
  "cmd": "npm run dev",
  "oauth_callback_port": 8787
}
```

The callback port must differ from the app's own port. For bookmark routes
pointing at an upstream that needs OAuth over `https://name.{{TLD}}`, enable
the per-route `proxy` option in the dashboard so cookies and redirects stay
on the `.{{TLD}}` origin.

## 2. Start it

```bash
vibe start
```

Or without a vibe.json:

```bash
vibe start myapp 3000 -- npm run dev
```

Your app is now at **https://myapp.{{TLD}}**

### Git worktrees

Run `vibe start` inside a linked git worktree and vibe registers
`<branch-slug>.<app>.{{TLD}}` (e.g. `feature-auth.myapp.{{TLD}}`) on its own
auto-assigned port instead of `<app>.{{TLD}}` — worktrees never clobber the
main checkout or each other. `--as <name>` overrides the subdomain. Worktree
routes auto-stop after 60 idle minutes (the next visit restarts them) and are
removed automatically once the worktree directory is deleted; visiting a
removed worktree's URL redirects to the parent app. When the main app is
stopped, its start page lists the running worktrees. Notes: the worktree's
`vibe.json` `port` and `oauth_callback_port` are ignored, and `reserve_ports`
get fresh values (still injected as `$PORT_<NAME>`), so always read ports from
the environment.

## Framework Configuration

### Vite (React, Vue, Svelte, etc.)

Add to `vite.config.js` / `vite.config.ts`:

```js
export default defineConfig({
  server: {
    allowedHosts: ['.{{TLD}}'],
  },
})
```

### Next.js

Add to `next.config.js`:

```js
module.exports = {
  allowedDevOrigins: ['*.{{TLD}}'],
}
```

### Flask

Disable the reloader (debug error pages still work):

```python
port = int(os.environ.get("PORT", 5000))
app.run(debug=True, use_reloader=False, host='0.0.0.0', port=port)
```

Or via CLI (with auto-assigned port):

```json
{"name": "myapi", "cmd": "flask run --debug --no-reload --port $PORT"}
```

### Django

```json
{"name": "myapi", "cmd": "python3 manage.py runserver --noreload 0.0.0.0:$PORT"}
```

### Jekyll

Jekyll ignores `$PORT` and defaults to 4000, so pass the port explicitly:

```json
{"name": "myblog", "cmd": "bundle exec jekyll serve --port $PORT"}
```

### Rails

```json
{"name": "myapp", "cmd": "bin/rails server -p $PORT"}
```

## Command Tips

### macOS

- Use `python3` not `python` (macOS has no `python` by default)
- For virtualenvs: `".venv/bin/python app.py"` or `"source .venv/bin/activate && python app.py"`
- Node.js with nvm/fnm just works — vibe spawns `$SHELL -lic` so your `.zshrc` is sourced

### Windows

- Vibe spawns commands via `cmd.exe /C`, not your interactive shell — your PATH must include the tools you reference (PowerShell `$PROFILE` and shell aliases are not loaded)
- For virtualenvs: `".venv\\Scripts\\python.exe app.py"` (note the `Scripts` directory, not `bin`)
- nvm-windows: the active Node version's directory must be on system PATH (it usually is after `nvm use`); `cmd /C` won't run your PowerShell init
- Use forward slashes or escaped backslashes in JSON paths — `"cmd": "C:\\path\\to\\app.exe"` or `"cmd": "C:/path/to/app.exe"`
