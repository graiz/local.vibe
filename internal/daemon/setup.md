# local.vibe — Project Setup

Configure a project to work with local.vibe. For full CLI, API, and dashboard
docs, see the [README](https://github.com/graiz/local.vibe#readme).

## 1. Add a vibe.json to your project root

```json
{
  "name": "myapp",
  "port": 3000,
  "cmd": "npm run dev"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Subdomain: `name.{{TLD}}` |
| `port` | no | Port your app listens on (omit or `0` for auto-assign) |
| `cmd` | yes | Shell command to start the app |
| `icon` | no | Emoji for the dashboard (e.g. `"🚀"`) |
| `idle_timeout` | no | Auto-stop after N minutes idle (0 = never) |

### Automatic port assignment

Omit `port` and vibe picks a free port (starting from 3000) and injects it as the
`PORT` environment variable. Most frameworks respect this automatically:

```json
{
  "name": "myapp",
  "cmd": "npm run dev"
}
```

Your app reads `process.env.PORT` (Node), `os.environ["PORT"]` (Python), etc.
The assigned port is shown in CLI output and persisted across restarts.

## 2. Start it

```bash
vibe start
```

Or without a vibe.json:

```bash
vibe start myapp 3000 -- npm run dev
```

Your app is now at **https://myapp.{{TLD}}**

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
{"name": "myapi", "port": 8000, "cmd": "python3 manage.py runserver --noreload 0.0.0.0:8000"}
```

## Command Tips

- Use `python3` not `python` (macOS has no `python` by default)
- For virtualenvs: `".venv/bin/python app.py"` or `"source .venv/bin/activate && python app.py"`
- Node.js with nvm/fnm just works — vibe sources your .zshrc
