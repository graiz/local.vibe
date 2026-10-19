# Peer discovery & cross-machine routes — design

**Date:** 2026-08-22
**Status:** Approved (design), not yet planned/implemented
**Feature flag:** `daemon.peers.enabled` — experimental, default **off**

## Problem

Two machines on the same home network each run a vibe daemon. A route (e.g.
`face`) registered on machine B is invisible from machine A: typing
`face.vibe` on A hits A's daemon, which has no such route. We want peer
daemons to discover each other's routes and make them browsable — without
weakening vibe's security model, and with collisions handled visibly rather
than avoided by namespacing.

## Decisions made during design

- **Flat namespace:** `face.vibe` just works from any paired machine. No
  `face.b.vibe` scoping. Collisions are handled by precedence + visible
  shadowing, not by construction.
- **Trust model:** explicit pairing. Discovery (mDNS) makes peers *visible*;
  nothing is *trusted* until a one-time invite-code pairing. No ambient LAN
  trust.
- **Peer scope:** browse + read-only route visibility. A paired peer may read
  your route list and send HTTP to your routes. It may never start, stop,
  register, or otherwise operate your daemon.
- **Transport:** daemon-to-daemon relay over mTLS (Approach 1). DNS-based
  direct connection was rejected (breaks the loopback-only assumption across
  the codebase, requires cross-installing browser CAs). Tailscale-as-transport
  was folded in rather than chosen: peer addresses are plain hostnames, so a
  MagicDNS name works as a manually added peer with no special support.

## Why the relay, in security terms

Every existing listener binds `127.0.0.1` (`internal/daemon/daemon.go`), and
the whole API security posture depends on it: the API is unauthenticated,
`origin.go` treats a *missing* Origin header as a trusted non-browser client,
and `handleRegister` executes `cmd` through a login shell. Any design that
rebinds those listeners to the LAN hands drive-by RCE to anything on the
network (`curl` from another machine sends no Origin). The relay adds one
**new, separate, narrow** LAN listener and leaves every existing surface
untouched at loopback.

## 1. Feature gate

`config.json` gains:

```json
"daemon": { "peers": { "enabled": false, "port": 7444 } }
```

- Absent key ⇒ disabled. A plain bool is fine (unlike `daemon.autostart`,
  the safe default and the zero value agree, so no pointer needed).
- Disabled ⇒ zero behavior change: no listener, no mDNS, no peer step in
  `routeRequest`. `vibe peer *` commands print how to enable.
- Enabled ⇒ the peer listener starts only when at least one peer is paired
  **or** an invite is open. Enabled-but-unpaired exposes nothing to the LAN.

## 2. Identity, pairing, peer listener

### Identity

Each daemon generates a self-signed ECDSA **peer identity cert** on first
enable: `~/.vibe/certs/peer.pem` + `peer-key.pem`. Deliberately separate from
the browser CA (`ca.pem`), which never leaves the machine and never signs
anything network-facing.

### Pairing (SSH-style pinning, invite-gated)

- On B: `vibe peer invite` → one-time 6-digit code, 5-minute TTL. Opens the
  pairing endpoint.
- On A: `vibe peer add <host> [--port N] --code 123456`. Host comes from mDNS
  discovery (phase 2) or is typed manually (phase 1); Tailscale MagicDNS
  names work here unmodified.
- Exchange runs over TLS to B's peer port. Each side proves knowledge of the
  code by HMAC-ing both peer-cert fingerprints with it — a LAN MITM without
  the code cannot splice itself into the exchange.
- Both sides persist the other in `~/.vibe/peers.json`: `{name, host, port,
  fingerprint, added_at}`. Pairing is mutual in one step.
- `vibe peer remove <name>` deletes the record (one side removing is enough
  to break the pair — the other side's connections stop verifying is false;
  the other side's *inbound* trust of us remains until it removes too, which
  is acceptable for v1; see out-of-scope re: revocation).

### Peer listener

TLS listener on `daemon.peers.port` (default 7444), LAN-facing, with
`ClientAuth: RequireAndVerifyClientCert` and a custom `VerifyPeerCertificate`
that accepts **only pinned fingerprints** from `peers.json`. Unpaired
connections die in the TLS handshake.

Its mux shares nothing with `/_api/`:

1. `POST /peer/pair` — live only while an invite is open; 404 otherwise.
2. `GET /peer/routes` — route list: name, type, ready, icon. Includes
   sticky/managed/pid/ttl routes. **Bookmarks are excluded** (third-party
   content; also consistent with bookmarks being excluded from
   `originTrusted`).
3. Host-based reverse proxy: requests whose `Host` matches a local route go
   straight to the route's port via the existing proxy machinery.

Hard rule: `/_api/*` on this listener is 404 **unconditionally, before proxy
resolution** — the daemon API stays loopback-only. (A proxied app's *own*
`/_api/` paths are unaffected: the proxy branch routes Host→app-port directly
and never enters the daemon's API mux, mirroring the passthrough note in
`origin.go`.)

## 3. Resolution & proxying on the browsing machine

### Resolution order in `routeRequest`

local route map hit → worktree logic → **peer route cache** → dashboard/404.

- **Local always wins.** Among peers, `peers.json` order breaks ties
  (stable, deterministic).
- Shadowed entries (peer route hidden behind a local name or an earlier
  peer) are marked, never silently dropped — see §4.

### Cross-machine proxy

Go `ReverseProxy` to `https://<peer-host>:<peer-port>` presenting A's peer
client cert, verifying B by pinned fingerprint (not by CA/hostname).
Original `Host: face.vibe` is preserved so B's peer mux resolves the route.
WebSocket/HMR upgrades tunnel natively through both hops;
`proxy_upgrade.go`'s upgrade-only Origin rewrite runs on **B**, the hop that
talks to the dev server — no changes needed there.

### Route sync

In-memory cache on A (never `routes.json` — those are B's routes):

- Background poll ~30s per peer while any peer is configured, with ETag so
  an unchanged list is a header exchange.
- On-demand refresh on dashboard render, `vibe list`, and unknown-host
  misses — throttled + single-flighted, same pattern as
  `managedOwnerCheckInterval` / `ownerChecking`.
- Deliberate deviation from the no-sweep creed, acknowledged: cross-machine
  push (SSE) is the pure form and is deferred; a 30s ETag poll against a LAN
  peer is negligible and the on-demand paths make misses self-correcting.

A change in the merged (local + peer) name set triggers the existing
leaf-cert hot-reload so peer names land in A's TLS SANs. Chrome rejects
`*.vibe` wildcards, so this regen is load-bearing for `https://face.vibe`
from A.

### Security exclusion on A

Peer-route hosts are **excluded from `originTrusted`** — same treatment and
same reason as bookmarks: pages under `face.vibe` are authored by another
machine, and a compromised peer must not drive A's register-executes-shell
API. Pairing grants "serve me content," never "operate my daemon."

## 4. UX: dashboard, CLI, collisions, stopped routes

- Dashboard + `vibe list`: peer routes grouped under the peer's name (same
  visual pattern as worktree grouping), read-only — no start/stop controls.
- Collisions: shadowed routes render with a "shadowed by <winner>" badge in
  both dashboard and CLI. No silent flapping, no cross-machine name
  enforcement at register time.
- New commands: `vibe peers` (discovered + paired + reachability),
  `vibe peer invite`, `vibe peer add`, `vibe peer remove`.
- `vibe doctor`: when enabled, checks the peer listener is up and each
  paired peer is reachable/verifies.
- Stopped managed route requested via a peer: B runs its normal recovery
  (adopt / auto-spawn — it is B's route and B's autostart setting), but the
  remote viewer gets a **static meta-refresh "starting on <peer>…" page**,
  not the interactive reconnecting/start pages, whose buttons POST to
  `/_api/` and would 404 across the peer channel.
- Idle timeout: peer-proxied requests hit B's proxy path and therefore count
  as activity, keeping idle-timeout semantics correct while A browses.

## 5. Discovery, dependencies, phasing

mDNS is the one component in tension with the minimal-deps rule (Cobra +
x/sys only). No stdlib mDNS exists; importing `grandcat/zeroconf` is
rejected. `internal/dns` already parses DNS wire format, so a tiny
`internal/mdns` (advertise + browse exactly one service type,
`_vibe-peer._tcp.local`) is in-house-able and consistent with house style.

- **Phase 1:** pairing by explicit host (`vibe peer add imac.local`, or a
  MagicDNS name). Complete security model, zero discovery code.
- **Phase 2:** `internal/mdns` browse/advertise so `vibe peers` discovers
  machines unaided. Same flag gates both.

## Error handling

- Peer unreachable / TLS verify fails on proxy: serve a clear "peer <name>
  unreachable" error page (styled via `theme.go`), never a bare 502 —
  consistent with the no-bare-502 stance. The route stays in cache until the
  next successful sync replaces it; repeated failures mark the peer
  unreachable in `vibe peers` / dashboard.
- Invite code wrong or expired: pairing endpoint returns a generic failure
  (no oracle distinguishing wrong vs expired), invite stays open until TTL.
- Fingerprint mismatch on an established peer (host reinstalled, key
  changed): hard failure with an explicit message naming `vibe peer remove`
  + re-pair as the fix — never trust-on-first-use silently for a *known*
  peer.

## Testing

- Unit: fingerprint pinning verification (accept pinned, reject unknown,
  reject no-cert), pairing HMAC (tampered fingerprints fail, wrong code
  fails), resolution precedence + shadowing marks, `originTrusted` exclusion
  of peer hosts, bookmark exclusion from `/peer/routes`.
- Integration: two daemons in one test process, peer listeners on loopback
  (transport doesn't care), full pair → sync → browse-through-both-hops →
  upgrade tunnel; `/_api/` 404 on the peer listener both for daemon API
  paths and pre-pairing.
- Existing suites must stay green: no behavior change with the flag off is
  itself a test target.

## Out of scope

- Remote start/stop or any state-changing control of a peer's routes.
- Bookmark sharing.
- Revocation beyond `vibe peer remove`; cert rotation.
- Mesh semantics beyond pairwise: three machines = three pairings; no
  transitivity; A never relays B's routes to C.
- SSE/push route sync (deferred; poll is the v1 mechanism).
- Windows peer listener firewall automation (the flag being experimental,
  documenting the prompt is enough for v1).
