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
| `port` | yes | Port your app listens on |
| `cmd` | yes | Shell command to start the app |
| `icon` | no | Emoji for the dashboard (e.g. `"🚀"`) |
| `idle_timeout` | no | Auto-stop after N minutes idle (0 = never) |

## 2. Start it

```bash
vibe start
```

Or without a vibe.json:

```bash
vibe start myapp 3000 -- npm run dev
```

Your app is now at **http://myapp.{{TLD}}**

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
app.run(debug=True, use_reloader=False, host='0.0.0.0', port=5000)
```

Or via CLI:

```json
{"name": "myapi", "port": 5000, "cmd": "flask run --debug --no-reload --port 5000"}
```

### Django

```json
{"name": "myapi", "port": 8000, "cmd": "python3 manage.py runserver --noreload 0.0.0.0:8000"}
```

## Command Tips

- Use `python3` not `python` (macOS has no `python` by default)
- For virtualenvs: `".venv/bin/python app.py"` or `"source .venv/bin/activate && python app.py"`
- Node.js with nvm/fnm just works — vibe sources your .zshrc
