# TODO: OAuth localhost callback bridge hardening (delete before merge)

This branch adds `oauth_callback_port` and a localhost callback bridge for OAuth flows that must return to `http://localhost:<port>/auth/google/callback`.

## Ship readiness checklist

### 1) Product / behavior
- [ ] Decide if callback path should stay hardcoded (`/auth/google/callback`) or become configurable per route.
- [ ] Decide if bridge should support providers beyond Google by allowing multiple callback paths.
- [ ] Document exact route semantics for managed apps using bridge mode.

### 2) API / config
- [ ] Add docs for `oauth_callback_port` in README and setup docs.
- [ ] Confirm dashboard route CRUD surfaces `oauth_callback_port` (or intentionally hide it as advanced-only).
- [ ] Ensure API update semantics can clear `oauth_callback_port` explicitly (nullable behavior).

### 3) Runtime correctness
- [ ] Add tests for daemon restart + persisted bridge listener recovery.
- [ ] Add tests for update flows (change callback port, remove callback port, route rename).
- [ ] Add tests for conflict handling when another process binds callback port after route registration.
- [ ] Verify clean listener teardown on route deregister and daemon shutdown.

### 4) Security review
- [ ] Validate that bridge only redirects to known route host derived from route table (no user-controlled host).
- [ ] Audit open-redirect risk in callback bridge (query passthrough is expected; host must remain fixed).
- [ ] Re-check interaction with TLS redirect behavior and non-TLS mode.

### 5) UX / operability
- [ ] Improve error messages when callback bridge is missing/unavailable (currently looks like localhost connection failure in browser).
- [ ] Add a health/debug endpoint for active oauth bridge listeners.
- [ ] Consider status output to display `oauth_callback_port` for managed routes.

### 6) Regression control
- [ ] Add integration-style tests that model Screener-like split frontend/backend dev server behavior.
- [ ] Verify no regressions for routes without `oauth_callback_port`.
- [ ] Verify bookmark/proxy route behavior remains unchanged.

### 6a) Gaps found during tasks.vibe setup (2026-04-23)
- [ ] `vibe start` on an already-registered route (`startExisting` path) silently ignores `oauth_callback_port` changes in `vibe.json`. Should re-sync `oauth_callback_port` (and likely `icon`, `idle_timeout`) from disk when re-starting. Currently the field only gets applied on first-time register — if the user adds it after registration, subsequent `vibe start` does nothing with it.
- [ ] `validateOAuthBridgeConfig` doesn't detect collisions between a callback port and *another route's app port* (e.g. callback 3000 while another managed route has `port: 3000`). Desired behavior:
    - Other route **not running** → warn (log + dashboard badge), allow.
    - Other route **running** → error (reject at register/update time; also refuse to start a colliding route later, surfaced on the start page).
- [x] Callback path was hardcoded to `/auth/google/callback`; NextAuth's `/api/auth/callback/google` 404'd. Fixed by forwarding any path to the route's `.{{TLD}}` host.

### 7) Cleanup before merge
- [ ] Remove this TODO file.
- [ ] Squash/fix commit messages as needed.
- [ ] Re-run: `go test ./...` and `vibe dev` on final branch state.
