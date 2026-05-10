# Future: Linux Support

> Status (Windows): both the cross-platform refactor (Phase 1) and the
> Windows implementation (Phase 2) have landed on
> `feature/windows-implementation`. Process supervision uses Job Objects;
> DNS uses an embedded stub on 127.0.0.1:53 plus `netsh interface ipv4 set
> dnsservers static`; port forwarding uses `netsh interface portproxy`;
> autostart uses a Scheduled Task on logon at /rl HIGHEST; cert trust uses
> `certutil -addstore Root`. `vibe uninstall` reverses every step.
> Linux remains unimplemented — sections below describe its design.

local.vibe is currently macOS- and Windows-supported. This document captures what would need to change to support Linux, and the design constraints that came out of reviewing PR #4 (first Linux attempt).

---

## Guiding principles (from PR #4 review)

1. **Don't regress macOS.** Every cross-platform change must preserve current Mac behavior. In particular: managed routes spawn under `$SHELL -lic` so nvm/rbenv/pyenv-managed binaries are on PATH. Any shell-invocation change must be GOOS-gated, not collapsed into a shared codepath.
2. **Platform code lives behind build tags, not runtime `if runtime.GOOS == ...` branches.** Setup, process management, and lifecycle install/uninstall each get `_darwin.go` / `_linux.go` / `_windows.go` files. `setup.go` itself stays small and dispatches.
3. **`sudo vibe setup` must be self-sufficient on a fresh box** for whichever platform it's running on. No shell-script preinstall step. Each platform owns its own dependency install (dnsmasq on macOS, equivalents on Linux).
4. **Every install step must have a matching uninstall.** macOS pf via LaunchDaemon plist is reversible; Linux equivalents (iptables/nft rules, systemd units, CA trust) need `vibe uninstall` parity.
5. **Windows lands as build-tagged stubs first.** Even if non-functional, the file layout and command surface should exist so future contributors aren't blocked on a refactor.

---

## Recommended PR split

Land Linux support in two PRs to keep review tractable:

- **PR A — Cross-platform refactor, no behavior change.** Split `cmd/setup.go` into `setup_darwin.go` / `setup_linux.go` / `setup_windows.go` (stub) with build tags. Same for `internal/daemon/process_*.go`. GOOS-gate the shell flag (`-lic` on darwin, `-c` or `bash -l -c` on linux). Mac behavior unchanged. CI gains a Linux build target.
- **PR B — Actual Linux implementation.** DNS, port forwarding, systemd unit, CA trust, uninstall.

This was the recommendation given on PR #4 and is the path forward.

---

## Areas Requiring Platform-Specific Work

### 1. DNS Resolution

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **Mechanism** | `/etc/resolver/vibe` | systemd-resolved per-domain routing (preferred) or dnsmasq | Acrylic DNS Proxy, or embedded Go DNS stub on 127.0.0.1:53 |

**Linux design note:** dnsmasq on `127.0.0.1:53` collides with systemd-resolved on most modern distros (Debian 12+, Ubuntu, Fedora, Arch). Prefer configuring systemd-resolved with a per-domain server for `.vibe` — it's already installed everywhere we care about and avoids the port-53 fight. Fall back to dnsmasq only on systems that genuinely don't have systemd-resolved.

Windows has no `/etc/resolver/` equivalent. Options: third-party Acrylic DNS Proxy, or a lightweight Go DNS stub on 127.0.0.1:53 that handles `.vibe` queries locally.

### 2. Port 80/443 Forwarding

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **Mechanism** | pf via LaunchDaemon plist | nftables (preferred) | `netsh interface portproxy` |

**Linux design note:** prefer `nft` directly over `iptables`. Modern distros (Debian 11+, Fedora, Arch) default to nftables; iptables is a legacy compat shim and behaves inconsistently when both are present. Whatever we install must be removable by `vibe uninstall` — a one-shot systemd unit that loads/unloads an nft ruleset is the closest analogue to the macOS LaunchDaemon plist pattern.

### 3. Daemon Lifecycle (autostart + keep-alive)

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **Mechanism** | LaunchAgent + `launchctl` | systemd user service (`vibe.service`) | Windows Service (`golang.org/x/sys/windows/svc`) |

**Linux design note:** ship a `vibe.service` template and have setup install + enable it under `systemctl --user`. Don't claim autostart in the README until this exists.

### 4. Process Management

macOS/Linux use POSIX process groups (`Setpgid`, `Kill(-pgid, SIGTERM)`). Windows has no equivalent — would need Job Objects (`windows.CreateJobObject`) via build-tagged files:

```
internal/daemon/process_unix.go      // +build !windows
internal/daemon/process_windows.go   // +build windows
cmd/setup_darwin.go
cmd/setup_linux.go
cmd/setup_windows.go                 // stub initially
```

### 5. Shell Execution

Currently uses `$SHELL -lic`. This is a documented invariant on macOS — it pulls nvm/rbenv/pyenv shims onto PATH for managed routes. Cross-platform handling:

- **darwin:** keep `$SHELL -lic` exactly as-is.
- **linux:** `$SHELL -c` (or `bash -l -c`); `-i` interferes with non-interactive runners.
- **windows:** `cmd.exe /C` or `powershell.exe -Command`.

This must be GOOS-gated, not unified.

### 6. Unix Socket

Unix sockets don't exist on Windows. Recommend TCP-only on Windows (fallback already exists in `internal/client`).

### 7. TLS Cert Trust

| | macOS (done) | Linux | Windows |
|---|---|---|---|
| **System trust store** | Keychain (`security add-trusted-cert`) | `update-ca-certificates` (Debian/Ubuntu) / `update-ca-trust` (Fedora) / `trust anchor` (Arch) | Windows Cert Store (`certutil -addstore`) |
| **Browser trust** | Chrome/Safari read system store | **Chrome/Firefox use NSS, not the system store** — need `certutil -d sql:$HOME/.pki/nssdb -A` | Chrome reads system store; Firefox uses NSS |

**Linux gotcha:** updating the system CA bundle is not enough — Chrome and Firefox use NSS. First-time Linux users will hit cert warnings unless we either install via NSS too (`certutil` from `libnss3-tools`) or document the manual step prominently. This is a real UX cliff and worth budgeting time for, not a footnote.

### 8. Paths and Defaults

| | macOS | Linux | Windows |
|---|---|---|---|
| Binary | `/opt/homebrew/bin/vibe` | `/usr/local/bin/vibe` | `%LOCALAPPDATA%\vibe\vibe.exe` |
| Config | `~/.vibe/` | `~/.vibe/` or `$XDG_CONFIG_HOME/vibe/` | `%APPDATA%\vibe\` |

Use `os.UserConfigDir()` to abstract paths. Remove hardcoded Homebrew paths from non-darwin codepaths.

### 9. CI

Add build matrix: `macos-latest`, `ubuntu-latest`, `windows-latest`. PR A should turn this on at least for `go build` + `go vet` + `go test ./...` — full integration tests can stay macOS-only initially.

### 10. New Dependencies (Windows only)

- `golang.org/x/sys/windows/svc` — Windows Service management
- `golang.org/x/sys/windows` — Job Objects for process groups

---

## Recommended Implementation Order

1. **PR A: Refactor with build tags.** Split setup and process management into per-GOOS files. GOOS-gate shell invocation. Add Windows stubs (non-functional, documented). Turn on CI matrix. No behavior change on macOS.
2. **PR B: Linux implementation.** systemd-resolved DNS config, nftables forwarding, `vibe.service` user unit, CA trust install (system + NSS), full uninstall path.
3. **Path abstraction.** `os.UserConfigDir()`, drop hardcoded Homebrew assumptions everywhere except darwin-specific files.
4. **Windows daemon.** Service registration, TCP-only client transport, embedded DNS or Acrylic, `netsh portproxy`, Job Objects for process groups.

## Complexity

| Area | Effort |
|---|---|
| PR A refactor (build tags + CI) | Low–Medium |
| Linux DNS via systemd-resolved | Medium |
| Linux nftables + reversible uninstall | Medium |
| Linux CA trust (system + NSS) | Medium |
| Path abstraction | Low |
| Windows support (full) | High |

---

## Known limitations (Windows)

### Graceful child shutdown

Managed processes are stopped by `TerminateJobObject`, which is the moral equivalent of `kill(-pgid, SIGKILL)` — immediate, no clean-up window. Windows has no cross-process SIGTERM analogue we can deliver to an arbitrary console child from outside its console group. This is fine for dev servers (the use case `vibe` targets) but worth knowing if a managed route really needs to flush state on shutdown. A future implementation could attach to the child's console and send `CTRL_BREAK_EVENT` via `GenerateConsoleCtrlEvent`, but that's non-trivial and out of scope for the current Windows port.

### Firefox cert trust

In practice, recent Firefox on Windows (and macOS) honors `security.enterprise_roots.enabled`, which makes Firefox read the platform root store — so it picks up our locally-trusted CA without per-profile work. That preference is on by default for most users in modern Firefox builds. If a user hits a cert warning on `https://*.vibe`, the fix is `about:config` → `security.enterprise_roots.enabled` → `true`. Document this in the README rather than automating per-profile NSS imports.

The same mechanism applies on macOS (Firefox reads Keychain via the same pref) — no per-platform code needed. If we ever do need per-profile automation as a fallback, the right home for it is `internal/cert/firefox.go` (no build tag) with thin per-OS path-resolution helpers, since the certutil invocation and profile-iteration logic are platform-agnostic.
