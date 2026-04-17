# Contributing to local.vibe

## Requirements

- **macOS** — this project uses macOS-specific features (dnsmasq, pf, LaunchAgent)
- **Go 1.22+**
- **Homebrew**

## Getting started

```bash
git clone https://github.com/graiz/local.vibe.git
cd local.vibe
go build -o vibe .
sudo ./vibe setup    # one-time: installs dnsmasq, pf rules, LaunchAgent
./vibe install       # copies binary to /opt/homebrew/bin/vibe
```

## Development workflow

```bash
vibe dev           # rebuild + restart daemon (do this after every change)
go test ./...      # run tests
go vet ./...       # static analysis
```

The daemon runs from a compiled binary — source changes aren't picked up until rebuilt. `vibe dev` handles the full cycle.

## Code style

- Run `gofmt` before committing
- No external dependencies beyond Cobra — use the standard library
- All HTML output must escape user-supplied strings to prevent XSS

## Project structure

- `cmd/` — CLI commands (Cobra). No daemon internals.
- `internal/client/` — HTTP client for daemon communication.
- `internal/config/` — Configuration loader.
- `internal/daemon/` — Core daemon: HTTP server, routing, process management, dashboard.
