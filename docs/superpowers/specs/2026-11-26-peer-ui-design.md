# Peer management in the dashboard — design

Date: 2026-11-26. Status: written from the 2026-11-26 conversation; builder-checked; awaiting the maintainer's approval.
Builds on: `2026-08-22-peer-discovery-design.md` (trust model, pairing ceremony,
relay) and the branch `hardening-and-peer-oauth` (basic "Add peer" modal,
`PeerAdmin`, peer rename pickup, OAuth bridges).

## Intent

Peers (paired vibe machines browsing each other's `.vibe` routes) are managed
today almost entirely from the CLI: `vibe peer invite` on one machine, `vibe
peer add <host> --code N` on the other, hand-editing `config.json` to enable
the feature, and no way to fix a peer's host or name short of re-pairing. The maintainer
wants the whole lifecycle — turn the feature on, find the other machine, pair,
fix, remove — in the dashboard's experimental Peers section, and a clear
answer when the other machine can't pair yet ("vibe isn't running there —
here's how to enable it").

## Goals

- Enable and disable peers from the dashboard, at runtime, no daemon restart.
- Pair in either direction from the UI: show this machine's code, or pick /
  type the other machine and enter its code.
- Find candidate machines without typing addresses: machines on the local
  subnet and Tailscale nodes that run vibe.
- Edit a paired peer's host, port, and local name; remove a peer.
- For a machine that can't pair yet, say exactly which step is missing.

## Non-goals

- mDNS/Bonjour (the 2026-08-22 spec's phase 2). Replaced by the subnet sweep
  below; may return later.
- An approve-on-target pairing flow. The one-time code stays the ceremony.
- Revocation that propagates (removing a peer here doesn't tell the other
  side), IPv6 sweeps, sweeping networks larger than the cap.

## Anti-goals (would be a failure even if it works)

- A machine announcing itself on networks it joins. Nothing broadcasts, ever;
  discovery only probes, and only when the user opens the picker.
- Weakening the trust model: discovery and probing grant nothing. Trust still
  comes only from the code + fingerprint pin.
- Peer pages or route pages (bookmarks, peer routes, managed-route origins)
  gaining any of these controls.

## Decisions (the maintainer's)

1. **Disabled state:** an **Enable peers** button that opens a short dialog
   explaining the basics; confirming turns peers on immediately.
2. **Discovery:** local network + Tailscale nodes in one picker, typed address
   always allowed.
3. **Visibility:** a machine is findable only while it is showing a pairing
   code — like Bluetooth pairing mode.
4. **Pairing:** the existing invite-code HMAC ceremony, exposed in both
   directions.
5. **Edit:** host, port, and name (a local label). Fingerprint read-only, shown
   for comparison.
6. **Mechanism:** subnet sweep + Tailscale node probe; no mDNS ("keep simple
   for now").
7. When the target can't pair, explain how to enable it there.
8. **Pairing code:** 8 characters as two blocks of four (`K7QM-3XR9`), with at
   least one digit in every block so a code can never spell a word ("meet in
   the middle" between today's 6 digits and a 12-character code).

## Approaches considered

**Disabled behavior.** (A) runtime Enable switch — chosen: "how to enable it
there" becomes one click in the other machine's dashboard. (B) a notice with
the config line and a restart button — fewer moving parts, more steps for the
user. (C) hide the section as today — discovery can't help a machine that's off.

**Local-network discovery.** (A) in-house `internal/mdns` (advertise while a
code is open, browse on picker open) — the 2026-08-22 plan; ~400–600 lines,
shares UDP 5353 with macOS's mDNSResponder. (B) shell out to `dns-sd` —
little code on macOS, but screen-scrapes CLI output (the codebase avoids that)
and no Windows support without Bonjour. (C) defer LAN discovery. **Chosen
instead: a bounded subnet sweep of the peer port**, raised by the maintainer. It finds
exactly the machines decision 3 wants findable (their listener is up while
showing a code), broadcasts nothing, is ~100–150 lines, and is identical on
macOS/Windows/Linux. It gives up multi-subnet LANs, which Tailscale covers.

## Design

### 0. Pairing hardening (prerequisite; also fixes the existing CLI flow)

**The hole.** Today's code is 6 digits (`peer.NewInviteCode`, ~2^20) and the
initiator sends `Proof = HMAC(code, …)` to whatever answered at the address
*before* that server has proven anything (`pairWithPeer` step 2). A fake
listener — reached by a mistyped host, a drifted MagicDNS name, or a picker
entry whose self-reported name it spoofed — recovers the code from that one
proof offline (measured: 0.23 s on one core), then pairs with the real target
inside its 5-minute window (relay access to every route there) and completes
the pairing with the victim too (pinned under a trusted name). The 5-attempt
cap doesn't apply: the guessing is offline. Discovery with self-reported names
(§4–§7) makes the mistake a click away, so this lands first.

**Fix, three parts:**
1. **Code format (decision 8):** 8 characters, two blocks of four, Crockford
   base32 alphabet (`0-9` + `A-Z` minus `I L O U`), every block containing at
   least one digit; generated with `crypto/rand` by rejection sampling. ~2^39
   valid codes. Displayed as `XXXX-XXXX`. Input is normalized: case-insensitive,
   dash/space optional, `O→0`, `I/L→1` (Crockford decoding), then validated.
2. **Key stretching:** the proof key is `PBKDF2-HMAC-SHA256(code, salt =
   "vibe-peer-pair-v2|" + senderFP + "|" + receiverFP, iterations tuned to
   ~100 ms on a current Mac)`, then `HMAC(key, senderFP|receiverFP)` as today.
   Measured search cost for the full code space: plain HMAC on one gaming GPU
   ~66 s (inside the window); stretched ~150 days. Honest cost: ~0.1 s per
   side, once per pairing. The repo's Go floor is 1.22 and `crypto/pbkdf2` is
   stdlib only from 1.24, so implement PBKDF2 (~20 lines) in `internal/peer`
   with RFC test vectors rather than raising the floor *(my call)*.
3. **Key comparison:** the Show-a-code dialog shows this machine's key
   fingerprint in short form (`key 4F2A·91C3` — first 8 hex of the SHA-256
   fingerprint); the Add-peer picker and code field show the key of the
   machine about to be paired, next to its *claimed* name ("says it's
   box-c"). A spoofed name shows a different key. `vibe peer invite` and
   `vibe peer add` print the same short key.

**Compatibility:** the proof format changes (`v2` in the salt), so an old and a
new build cannot pair; the responder's 403 stays generic, and the initiator's
error says "pairing failed — make sure both machines run the same vibe
version". Existing pins are unaffected (pairing only). Peers is experimental;
no migration path.

### 1. Runtime enable / disable

- `peersEnabled()` reads `s.cfg.Daemon.Peers.Enabled` in ~10 places without a
  lock. Replace with an `atomic.Bool` on `Server`, initialized from config.
- `POST /_api/peers/enabled {"enabled": bool}`: writes `daemon.peers.enabled`
  to `config.json` via the existing `saveConfig` path (as `handleSetPreferences`
  does), then:
  - **on:** set the flag, run `loadPeerSubsystem()` (loads peers.json; brings up
    identity + listener + sync loop only if peers exist — unchanged lazy rule).
    Enabling does **not** start the listener on an unpaired machine: per
    decision 3 a machine is findable only while it shows a code, so an enabled
    machine with no peers and no code answers nothing on 7444.
  - **off:** clear the flag first (so `routeRequest` stops consulting peers),
    close any open invite, shut down the peer listener and server, stop the
    sync loop, clear the in-memory peer caches, reconcile OAuth bridges (closes
    the peer bridges), and reload the TLS leaf (drops peer SANs). `peers.json`
    and the identity cert are kept — re-enabling restores the pairings. Live
    relay connections are cut immediately (`http.Server.Close`, not a graceful
    `Shutdown` that would let an in-flight relay finish): off means off.
- **Listener lifetime (my call, from decision 3):** today an invite starts the
  listener and nothing ever stops it, so a machine that once showed a code
  stays findable. Make it symmetric: when an invite expires or is consumed and
  no peers are paired, stop the listener (and on removing the last peer with no
  invite open). The listener runs only while peers exist or a code is open.
- `peerSyncLoop` currently exits only on daemon shutdown. Give it a stop channel
  per enable cycle so off→on never runs two loops. Enabling twice is a no-op.
- Gate: a dashboard-only operation. It is not `ownRoute`, so route origins are
  already refused; it is state-changing, so the cross-site gate applies.
- CLI parity *(my call)*: `vibe peers enable` / `vibe peers disable` call the
  same endpoint; the "peers are disabled" message points at them and the
  dashboard switch instead of "edit config.json and restart".

### 2. The Peers section

Rendered on the dashboard proper only (`PeerAdmin` = `localDetailsAllowed`),
whether or not peers are enabled — today it is hidden when disabled.

**Disabled:**
```
Peers  experimental                                   [Enable peers]
 Browse other machines' .vibe routes from here.
```
**Enable dialog** (dashboard modal style) — the basics, then Cancel / Enable:
- Peers let you open another machine's `.vibe` routes from this one, over an
  encrypted connection between the two vibe daemons.
- Nothing is reachable until you pair: a machine must show a one-time code and
  you must enter it. Pairing pins both machines' keys.
- vibe only listens on the network (port 7444) while you have paired machines
  or a code is showing — and only paired machines, or one holding the current
  code, get through. Turning peers off stops it and keeps your pairings for
  next time.

**Enabled:**
```
Peers  experimental                [Add peer]  [Show a code]  [⏻ Turn off]
 ON BOX-C       100.64.0.3:7444        ● connected            [Edit] [Remove]
   cell2jack  …route rows as today…
 ON BOX-B       100.64.0.2:7444        ● unreachable (2m)     [Edit] [Remove]
```
Group header: name, `host:port`, sync status from `GET /_api/peers`
(reachable / last error), Edit, Remove. Route rows unchanged (including
shadowed rows).

### 3. Show a code (this machine becomes findable)

Uses the existing `POST /_api/peers/invite` (which already starts the
listener). The dialog shows the 8-character code (§0), this machine's short key
fingerprint, this machine's name and
addresses (LAN IPs and Tailscale name if any, to type on the other side), and a
live countdown to the 5-minute expiry; when it expires it offers "Show a new
code". Closing the dialog does not cancel the code *(my call — the CLI behaves
the same)*.

### 4. Add peer: picker + code

Opening **Add peer** calls `POST /_api/peers/discover` (new; dashboard-only) and
shows a list that fills in as results arrive, plus a typed-address field:

```
Add peer
 Found on this network
   box-c        192.168.1.23        ● showing a code      [Pair…]
   192.168.1.40                     ○ vibe here, no code  ⓘ
 Tailscale
   box-b        box-b.tailnet       ● showing a code      [Pair…]
   nas          100.64.0.9          — no vibe found        ⓘ
 Or enter an address: [ host[:port]        ] [Check]
```
`[Pair…]` reveals the code field for that row and calls the existing `POST
/_api/peers` (host, port, code). Already-paired machines are listed as "paired"
(no action) so the list never offers to re-pair a pinned machine.

### 5. Discovery (`POST /_api/peers/discover`)

A **POST**, not a GET: it has side effects (a network sweep, Tailscale probes,
creating the peer identity), and a GET can be triggered cross-site by an
`<img src>` with no Origin to gate — the same reason `repair` is a POST. It is
also `localOnly` so the response (addresses, names) is dashboard/CLI-only.
Runs on demand only; hard overall deadline ~3 s; returns what it has.

- **Local sweep:** for each up, non-loopback IPv4 interface address in a
  private range (RFC 1918; excludes Tailscale's 100.64/10 and link-local),
  enumerate the subnet's hosts. Skip a subnet with more than 1024 addresses and
  return a note ("network too large to scan — enter an address"). Skip this
  machine's own addresses. Probe each with bounded concurrency (~128) and a
  ~300 ms TCP dial timeout.
- **Tailscale:** locate the CLI — `tailscale` on PATH, else the app-bundled
  binary at `/Applications/Tailscale.app/Contents/MacOS/Tailscale` (macOS; not
  on PATH on box-a), else `%ProgramFiles%\Tailscale\tailscale.exe` (Windows).
  The daemon runs unprivileged, so a PATH lookup is acceptable here. Run
  `status --json` through the bounded `runTool`; take online peers' first IPv4
  and MagicDNS name; probe each. Absent CLI or error → a note in the response,
  not a failure.
- Hosts found by both sources are merged (by IP).

### 6. Probe and classification (shared by discovery and "Check")

`probePeerHost(host, port)` dials TCP, then a TLS handshake presenting this
daemon's peer identity (`peer.ClientTLSConfig`, accepting any server cert —
nothing is trusted from a probe), then `GET /peer/hello`. The request is
needed, not just the handshake: under TLS 1.3 a rejected client cert surfaces
only on the first read, so "handshake returned" doesn't mean "admitted".
"Pinned here" below means the server's fingerprint is in this machine's
peers.json.

| Result | Pinned here? | State | What the UI says |
|---|---|---|---|
| TCP refused / timeout | — | `none` | "No vibe peer service answered at host:port. On that machine: install local.vibe (clone github.com/graiz/local.vibe and run `./setup.sh`, or `setup.ps1` on Windows — per the README), open its dashboard, click **Enable peers**, then **Show a code**." (Covers not installed / not enabled / enabled with no code — indistinguishable by design.) |
| no route to host / network unreachable on a private LAN address | — | `blocked` | "This Mac couldn't reach the local network — macOS may be blocking vibe. Check System Settings → Privacy & Security → Local Network." (macOS 15+ Local Network privacy; see §11.) |
| admitted, `/peer/hello` says code open | no | `pairing` | Name from `/peer/hello`; offers the code field. |
| admitted, code not open | no | `trusts-us` | "<name> still trusts this machine (you removed it here). Show a code on <name> to pair again." |
| admitted | yes | `paired` | "Already paired as <name>." |
| rejected (TLS alert / reset on first request) | no | `vibe` | "vibe is running there but isn't showing a code. Click **Show a code** in its dashboard (or run `vibe peer invite`)." |
| rejected | yes | `removed-us` | "Paired here, but <host> no longer trusts this machine. Remove it here, then pair again with a new code." |

Admission follows the existing `peerCertAuthorized` rule (pinned, or any cert
while an invite is open); no server-side change beyond `/peer/hello`'s
`code_open` field is needed for classification.

`POST /_api/peers/probe {"host", "port"}` (dashboard-only) exposes the same
classification for the typed-address **Check** button.

### 7. `/peer/hello` (peer listener)

New: `GET /peer/hello` → `{"name": "<localPeerName()>", "code_open": bool}`.
Reachable by pinned peers and, during an open invite, by unpinned certs (the
HTTP layer's unpinned-cert allowlist grows from `/peer/pair` to `/peer/pair` +
`/peer/hello`). Exposes only the name and whether a code is open, only to
machines this one already trusts or during the 5-minute window the user opened
— the exposure mDNS would have had. `code_open` is what lets a one-sided
pairing (the other side removed us, or we removed them) be told apart from
"showing a code".

### 8. Edit

`PUT /_api/peers/{name}` with any of `{"name", "host", "port"}`
(dashboard-only):
- `name`: `peer.SanitizeName`, non-empty, not used by another peer; re-keys the
  in-memory sync state; sets a new `peers.json` field `name_set_by_user: true`.
  `adoptPeerName` skips peers with that flag, so a name the user typed sticks;
  the flag is false for paired/auto names, so rename pickup keeps working for
  them.
- `host` non-empty; `port` 1–65535. Fingerprint is never editable; the edit
  dialog shows it read-only, full length, for comparison with the other
  machine's `vibe peers` output.
- After saving: force a sync of that peer (the pin is re-verified on the next
  connection, so a wrong host simply shows "unreachable" — it can never
  connect to a different machine).
- Edit dialog also runs `probe` against the new host:port and shows the state
  inline before Save *(my call)*.

### 9. Remove

Existing `DELETE /_api/peers/{name}`. Dashboard confirms inline in the row
("Remove box-c? It will need a new code to pair again. [Remove] [Cancel]")
— never a browser `confirm()`. After removal: reconcile OAuth bridges, reload the
TLS leaf (drop that peer's SANs). Local-only removal, as the CLI does today;
the other machine's view goes "unreachable"/rejected and the user removes it
there too.

### 10. Security

- Every new `/_api` endpoint is dashboard/CLI-only and state-changing ones are
  POST/PUT (so the cross-site gate applies — `discover` included, see §5);
  `discover` and `probe` responses are also `localOnly`; none are
  `ownRoute`, so no route origin can reach them; peer-route hosts are already
  excluded from `originTrusted` (`peer_security_test.go` stays authoritative —
  extend it to cover the new endpoints).
- Discovery grants nothing; pairing still requires the code; pins are still
  verified on every connection.
- The sweep runs only on picker open, is capped, private-range only, and never
  touches Tailscale's 100.64/10 by sweeping (Tailscale peers come from its own
  list).
- `/peer/hello` returns the name and `code_open` only (no routes, ports, or
  versions). Names it returns are claims, shown as such next to the key (§0).
- Pairing codes resist offline brute force (§0): a proof sent to the wrong
  machine no longer leaks the code within any useful time.
- Not changed here, noted for later: a paired peer can reach every route on
  this machine (no per-route sharing). Easier pairing makes that matter more.

### 11. macOS Local Network privacy

macOS 15+ can block a launchd-started, unsigned binary from connecting to
private LAN addresses while leaving Tailscale (100.64/10) traffic alone. Every
peer used so far is on Tailscale, so LAN reachability from the daemon is
unverified. First implementation step: confirm on a real Mac whether the
LaunchAgent daemon can reach a LAN host on 7444. Either way, the probe maps
`EHOSTUNREACH`/`ENETUNREACH` on a private LAN address to the `blocked` state
(§6) rather than `none`, and the sweep reports "local network access may be
blocked" when every LAN probe fails that way. Signing / entitlements are out
of scope unless the check shows the daemon is blocked with no user-facing
remedy.

## Testing (expected)

- Runtime toggle: on → listener + sync up (with peers present), off → listener
  closed, loop stopped, bridges closed, SANs dropped, peers.json kept; off→on
  twice → one sync loop. Race detector.
- Pairing hardening (§0): code generator always yields two blocks with a
  digit each and only Crockford characters (property test over many draws);
  normalization accepts `k7qm3xr9`, `K7QM-3XR9`, `K7QM 3XR9`, O/I/L variants;
  PBKDF2 against RFC 7914 §11 PBKDF2-HMAC-SHA256 vectors; v1 (6-digit,
  unstretched) proofs are rejected; existing pairing/cap tests updated to the
  new format.
- Probe classification against a real in-process peer listener in each state
  of the §6 table (no listener; no invite; invite open; pinned both ways;
  pinned only here; pinned only there).
- Listener lifetime: invite expiry / consumption with no peers stops the
  listener; removing the last peer with no invite stops it.
- Sweep: subnet enumeration, the 1024 cap, private-range filter, own-address
  exclusion (pure functions); Tailscale JSON parsing from a fixture.
- Edit: rename collision, `name_set_by_user` suppresses `adoptPeerName`, host
  change keeps the pin.
- Dashboard: disabled/enabled section rendering, controls only on the dashboard
  proper, escaping of peer-provided names in the picker (they come from
  `/peer/hello` and Tailscale — untrusted, render with `textContent`).
- `peer_security_test.go` extended: new endpoints refused from route, bookmark,
  and peer origins.

## Left to the builder

- Exact copy, layout, and polling cadence of the picker and dialogs (match the
  existing modal style; the 2026-11-24 "Add peer" modal is the base).
- Whether discovery streams results (SSE) or returns once at the deadline
  (returning once is fine for v1).
- Install instructions' wording per OS in the `none` message.
- Build order *(my call)*: pairing hardening (§0) → LAN-reachability check
  (§11) → runtime toggle +
  listener lifetime → Peers section states + Show a code → probe +
  `/peer/hello` + typed Check → Tailscale discovery → subnet sweep → edit →
  remove polish.
