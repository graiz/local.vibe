# Worktree Routes — Design Spec

**Date:** 2026-07-20
**Status:** Approved design, pre-implementation

## Problem

Agents (and humans) create git worktrees to develop features in isolation, then want to
preview the result in a browser. Today the worktree contains a copy of `vibe.json` with the
same `name` and `port` as the main checkout, so `vibe start` in the worktree either 409s or
fights the main route for the name/port — clobbering main or other worktrees.

## Solution overview

A **worktree route** is a managed route with a parent. `vibe start` inside a linked worktree
auto-detects the situation and registers the dev server at
`https://<worktree>.<app>.vibe` on its own auto-assigned port.

```
$ cd ~/dev/myapp-feature-auth        # git worktree of ~/dev/myapp
$ vibe start
→ worktree of myapp (branch feature/auth)
→ https://feature-auth.myapp.vibe   (port 3007, auto-assigned)
```

Core rules (decided during design):

1. **`app.vibe` is never hijacked.** Main running → proxy to main, always.
2. **Picker = smarter start page.** When main is stopped and worktree routes exist, the
   start page for `app.vibe` lists "Start main" plus each worktree with running/stopped
   status and a link. No separate picker URL.
3. **Nested naming.** `<worktree>.<app>.vibe`, one level deep only.
4. **Auto-detect registration.** No new flags required in the happy path; the previously
   broken flow becomes the correct flow.
5. **Event-based lifecycle.** Idle stop + dir-gone prune; no polling sweeps.
6. **Gone worktree → 307 to parent.** Stale worktree URLs never dead-end.

## 1. Data model & routing

- `Route` gains `Parent string` (empty for normal routes). A route with `Parent != ""` is a
  worktree route. `Dir` already exists and holds the worktree path.
- **Stored name** is the dotted form `<worktree>.<app>` (e.g. `feature-auth.myapp`).
  - `validName` stays per-label. A worktree route name is validated as exactly two labels,
    each individually `validName`-conforming. Deeper nesting is rejected.
  - Slugging for branch names: lowercase; `/` and any character outside `[a-z0-9-]` → `-`;
    collapse repeated `-`; trim leading/trailing `-`.
- **Host lookup** in `routeRequest` is unchanged on the fast path: worktree routes sit in
  the RouteTable under their dotted name, so `feature-auth.myapp.vibe` is an exact map hit
  after trimming `.vibe`.
- **Miss handling for two-label hosts:** if the (dotted) name is not in the table but the
  parent label *is* a known route → **307 redirect to `https://<app>.vibe`** (temporary —
  never 301, so a future worktree reusing the name isn't poisoned by browser redirect
  caching). If the parent is also unknown → existing "unknown host" dashboard banner.
- Everywhere else, worktree routes are ordinary managed routes: readiness probe,
  autostart/adopt, repair page, proxy-failure recovery, and `routes.json` persistence all
  apply unchanged.

## 2. `vibe start` in a worktree

1. **Detect:** `git rev-parse --git-dir` ≠ `git rev-parse --git-common-dir` → linked
   worktree. The main checkout dir is derivable from the common dir when needed.
2. **Parent name:** the `name` field of the worktree's own `vibe.json` (the copied file
   naturally identifies the *app*). Parent is **just a string** on the route — no phantom
   parent route is created if the app was never registered; dashboard/picker rendering
   handles a string-only parent gracefully.
3. **Worktree name:** slugified branch name; fallback to the worktree directory basename;
   `--as <name>` overrides. A collision with an existing sibling worktree route → 409 with
   a hint to use `--as`.
4. **Port:** always auto-assigned. A `port` value in the copied `vibe.json` is **ignored**
   for worktree routes (honoring it is precisely the clobbering bug). `PORT` env injected
   as today.
5. **`oauth_callback_port`:** ignored for worktree routes, with a printed warning — it is a
   fixed port that cannot be shared across worktrees, and OAuth redirect URIs are
   registered with external providers against specific URLs anyway.
6. **`reserve_ports`:** names kept, values **auto-reassigned** per worktree; injected as
   `PORT_<UPPER_NAME>` env vars as today, so multi-port apps work without collisions.

## 3. Picker & dashboard

- `startpage.go`: when serving the start page for a stopped route that has children, append
  a worktree section listing each child with status (running/stopped) and a link. This is
  the picker; it appears exactly where the start page already appears (`app.vibe` with main
  stopped), so no working URL is ever interrupted.
- `dashboard.go`: worktree routes render grouped under their parent app (nested/indented),
  not as top-level peers. Child rows get the same start/stop controls as other managed
  routes. A parent that exists only as a string (no registered main route) still gets a
  group header.

## 4. Lifecycle

- **Idle stop:** worktree routes default `idle_timeout` to 60 minutes (existing per-route
  timer machinery; overridable). The server stops; the route stays; the next visit
  auto-restarts it via the existing autostart path.
- **Dir-gone prune:** on daemon startup load, on `Start`, and on request-time recovery
  paths, `stat(route.Dir)` for worktree routes. Missing dir → deregister the route (killing
  any tracked process) — the branch was merged / `git worktree remove` happened. Event-
  based, touch-triggered; no sweep. The request that discovers the dead dir deregisters and
  answers with the 307-to-parent in the same response.
- Worktree routes persist across daemon restarts like other managed routes (with the dir
  check on load).

## 5. Certs, DNS, platforms

- **DNS: zero work.** dnsmasq `address=/.vibe/` (macOS) and the Windows resolver's
  last-label match already resolve hosts of any depth.
- **TLS:** each worktree route contributes `<wt>.<app>.vibe` to the SAN list via the
  existing `tlsHostnames` + leaf hot-reload. Explicit SANs only; no wildcards.
- **Origins:** each worktree is its own browser origin — isolated cookies/localStorage.
  Desirable for testing; documented since logins won't carry across worktrees.

## 6. Out of scope / known limits

- Shared backing state (databases, external services in `.env`) between worktrees — vibe
  cannot isolate what it doesn't manage.
- Apps that hardcode a port and ignore `$PORT` still collide (existing limitation).
- Sticky/bookmark routes get no worktree semantics; managed routes only.
- No hook into `git worktree remove`; pruning is touch-based. A dead route may linger
  invisibly until the next touch or daemon boot — harmless.
- One nesting level only; worktrees of worktrees are not modeled.

## 7. Testing

- **Unit:** branch-name slugging; two-label validation; host lookup incl. 307-to-parent
  and unknown-parent fallthrough; dir-gone prune on load/start/request; port /
  `oauth_callback_port` / `reserve_ports` override rules; picker rendering states (main
  running / main stopped + children running / mixed).
- **Integration** (existing daemon-test style): register parent + worktree → two servers on
  two ports, both proxied correctly; daemon restart → both recover; delete worktree dir →
  route pruned on load and stale URL 307s to parent.
- **Docs:** update `SKILL.md` and `setup.md` so agents learn the flow — headline: `vibe
  start` inside a worktree now just does the right thing.
