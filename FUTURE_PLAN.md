# Future: Windows & Linux Support

local.vibe is currently macOS-only. This document captures what would need to change to support Windows and Linux.

---

## Areas Requiring Platform-Specific Work

### 1. DNS Resolution

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **Mechanism** | `/etc/resolver/vibe` | dnsmasq or systemd-resolved | Acrylic DNS Proxy or embedded Go DNS server |

Windows has no `/etc/resolver/` equivalent. Options: third-party Acrylic DNS Proxy, or a lightweight Go DNS stub on 127.0.0.1:53 that handles `.vibe` queries locally.

### 2. Port 80 Forwarding

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **Mechanism** | pf (`pfctl`) | iptables/nftables NAT REDIRECT | `netsh interface portproxy` |

### 3. Daemon Lifecycle (autostart + keep-alive)

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **Mechanism** | LaunchAgent + `launchctl` | systemd user service | Windows Service (`golang.org/x/sys/windows/svc`) |

### 4. Process Management

macOS/Linux use POSIX process groups (`Setpgid`, `Kill(-pgid, SIGTERM)`). Windows has no equivalent — would need Job Objects (`windows.CreateJobObject`) via build-tagged files:

```
internal/daemon/process_unix.go      // +build !windows
internal/daemon/process_windows.go   // +build windows
cmd/setup_darwin.go
cmd/setup_linux.go
cmd/setup_windows.go
```

### 5. Shell Execution

Currently uses `$SHELL -lic` (defaults to `/bin/zsh`). Windows would use `cmd.exe /C` or `powershell.exe -Command`.

### 6. Unix Socket

Unix sockets don't exist on Windows. Recommend TCP-only on Windows (fallback already exists).

### 7. Paths and Defaults

| | macOS | Linux | Windows |
|---|---|---|---|
| Binary | `/opt/homebrew/bin/vibe` | `/usr/local/bin/vibe` | `%LOCALAPPDATA%\vibe\vibe.exe` |
| Config | `~/.vibe/` | `~/.vibe/` or `$XDG_CONFIG_HOME/vibe/` | `%APPDATA%\vibe\` |

Use `os.UserConfigDir()` to abstract paths.

### 8. CI

Add build matrix: `macos-latest`, `ubuntu-latest`, `windows-latest`.

### 9. New Dependencies (Windows only)

- `golang.org/x/sys/windows/svc` — Windows Service management
- `golang.org/x/sys/windows` — Job Objects for process groups

---

## Recommended Implementation Order

1. **Refactor process management with build tags** — split into unix/windows variants
2. **Automate Linux setup** — stub already exists in `cmd/setup.go`
3. **CI matrix** — catch cross-platform issues early
4. **Abstract paths** — `os.UserConfigDir()`, remove hardcoded Homebrew paths
5. **Windows daemon** — service registration, TCP-only communication
6. **Windows DNS + port forwarding** — embedded DNS or Acrylic, netsh portproxy
7. **Windows process groups** — Job Objects

## Complexity

| Area | Effort |
|---|---|
| Linux automated setup | Low (stub exists) |
| Process management refactor | Medium |
| CI matrix | Low |
| Path abstraction | Low |
| Windows support (full) | High |
