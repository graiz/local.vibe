# Peer Discovery & Cross-Machine Routes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Paired vibe daemons on a LAN can browse each other's routes (`face.vibe` on machine A reaches the app on machine B), behind an experimental `daemon.peers.enabled` flag that defaults off.

**Architecture:** Daemon-to-daemon relay over mTLS with SSH-style fingerprint pinning. A new, dedicated LAN listener on each daemon (default :7444) serves exactly: an invite-gated pairing endpoint, a read-only route list, and Host-based reverse proxy to local routes. The existing loopback API, dashboard, and TLS listeners are untouched. Browsing machine resolves unknown `.vibe` hosts against an in-memory peer-route cache (30s ETag poll + throttled on-demand refresh) and proxies through its own daemon, so browser TLS stays local.

**Tech Stack:** Go stdlib only (`crypto/tls`, `crypto/ecdsa`, `crypto/hmac`, `net/http`, `httputil`). No new module dependencies — this is a hard project rule.

**Spec:** `docs/superpowers/specs/2026-08-22-peer-discovery-design.md` (approved). Phase 2 (mDNS discovery) is explicitly OUT of this plan.

## Global Constraints

- **No new Go module dependencies.** Only Cobra + `golang.org/x/sys` exist today; everything here is stdlib.
- **Loopback invariants:** the existing HTTP (7999), TLS (7443), and unix-socket listeners keep binding `127.0.0.1` / the socket. Only the new peer listener faces the LAN.
- **`/_api/*` never served on the peer listener** — 404 before any other dispatch.
- **Flag off ⇒ zero behavior change.** `daemon.peers` absent or `enabled:false` means no listener, no sync goroutine, no peer step in `routeRequest`. The full existing test suite passing unchanged is an acceptance criterion of every task.
- **Peer-route hosts must NOT become trusted API origins** (`origin.go` `originTrusted`). Peer routes live in a separate cache, never in `s.table`, so this holds by construction — Task 10 locks it in with a test.
- **Verification per task:** `go build ./... && go vet ./... && go test ./...` must pass before a task is complete. Run from the repo root.
- **Line endings:** LF only (enforced by `.gitattributes` + tests). Don't fight it.
- **Commits:** each task ends with a commit step, but Greg's standing rule is **no commits without his explicit approval**. If commit approval was not explicitly given for this run, skip the commit steps and leave the work uncommitted; everything else in the task still applies.
- Commit messages end with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`
- The daemon runs a compiled binary: manual verification on the live machine needs `vibe dev`, but no task below requires the live daemon — all tests are hermetic (`Server.ConfigDir` override + loopback listeners).

## Verified codebase facts (read these, they are load-bearing)

- `internal/config/config.go`: `Config{Daemon DaemonConfig...}`; `DaemonConfig` has `Port, Socket, TLD, TLS TLSConfig{Enabled,Port,CertsDir}, DNS, AutoStart *bool`. `DefaultConfig()` sets defaults; `Load()` overlays `~/.vibe/config.json`.
- `internal/daemon/daemon.go`: `Server` struct fields include `cfg *config.Config`, `table *RouteTable`, `procs *ProcessManager`, `quit chan struct{}`, `ConfigDir string` (test override, `s.configDir()` respects it), `tlsMu`/`tlsCert`/`caCert`/`caKey`. `Start()` builds `mux` with `/_api/`→`apiHandler`, `/`→`routeRequest`, binds `127.0.0.1:<port>`. `tlsHostnames()` = `local.<tld>` + all `table.Names()`. `reloadTLSCert()` regenerates leaf + hot-swaps. `routeRequest` order: strip port from `r.Host` → HTTPS redirect → `local` dashboard → `s.table.Get(name)` hit (bookmark/managed/proxy logic) → `worktreeParent` redirect fallback → `serveDashboard`.
- `internal/daemon/routes.go`: `Route` has `Name, Parent, Port, Type RouteType, Icon`, runtime `Running/Ready atomic.Bool`, `PIDValue()`, `TouchActivity()`, `LoadFailure()`. `RouteTable.Get/Add/Names/List`. `RouteBookmark`, `RouteManaged` consts.
- `internal/daemon/autostart.go`: `recoverManagedRoute(w, r, route) (served bool)` — writes pages itself; returns false only after successful adoption (caller proxies). `startManagedNow(route) error`, `beginAutoStart/endAutoStart/isAutoStarting`, `hasWorktrees(route)`, `adoptOrphan(route)` (unix). `cfg.Daemon.AutoStartEnabled()`.
- `internal/daemon/api.go`: `apiHandler` trims `/_api`, passthrough for unknown paths on non-local vibe hosts (`isDaemonAPIPath`), then `apiStateChanging(...) && s.apiRequestCrossSite(r)` guard, then a `switch` on method+path. `writeJSONError(w, code, msg)` exists.
- `internal/daemon/origin.go`: `originTrusted` trusts `local.<tld>` and hosts whose name is in `s.table` with `Type != RouteBookmark` (after `daemonPortTrusted`).
- `internal/cert/cert.go`: `EnsureCA`, `GenerateLeaf(certsDir, caCert, caKey, hostnames)`, `writePEM(path, blockType, data, perm)`, `randomSerial()` — copy the ECDSA/PEM idioms from here for the peer identity, do not import this package's CA into peer trust.
- `internal/client/client.go`: `Client.do(method, path, body) ([]byte, int, error)`; unix-socket-first transport; always sets `Accept: application/json`.
- `internal/daemon/theme.go`: `themeHead(title string) string` for styled pages.
- `internal/daemon/test_helpers_test.go` exists — check it before writing new daemon test scaffolding.
- CLI pattern: `cmd/list.go` (tabwriter), `cmd/root.go` `init()` does `rootCmd.AddCommand(...)`.

## File structure (locked in)

```
internal/peer/                    NEW package — pure primitives, no daemon imports
  identity.go   identity_test.go  self-signed ECDSA peer cert, SHA-256 fingerprints
  pairing.go    pairing_test.go   invite codes, HMAC pairing proofs, name sanitize
  store.go      store_test.go     Peer record + peers.json load/save
  tlsconf.go    tlsconf_test.go   mTLS server/client tls.Config builders (pinning)
  summary.go                      RouteSummary — the /peer/routes wire type

internal/daemon/
  peer_listener.go  NEW  LAN listener, per-request authz, /peer/routes, host-proxy, pages
  peer_pair.go      NEW  B-side /peer/pair handler + A-side outbound pairWithPeer
  peer_sync.go      NEW  peer-route cache, poll loop, throttled refresh, lookup
  peer_proxy.go     NEW  A-side proxyToPeer + unreachable page
  peer_api.go       NEW  loopback /_api/peers endpoints
  daemon.go         MOD  Server fields, Start() wiring, tlsHostnames, routeRequest hook
  api.go            MOD  dispatch entries + isDaemonAPIPath
  dashboard.go      MOD  peer group rendering (Task 12)

internal/config/config.go  MOD  PeersConfig
internal/client/client.go  MOD  peer API methods
cmd/peer.go                NEW  vibe peers / vibe peer invite|add|remove
cmd/list.go                MOD  peer-routes section
cmd/doctor.go              MOD  peer status lines
CLAUDE.md                  MOD  document the subsystem (Task 13)
```

---

### Task 1: Config gate — `daemon.peers`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.PeersConfig{Enabled bool, Port int}` at `cfg.Daemon.Peers`; default `{Enabled: false, Port: 7444}`.

- [ ] **Step 1: Write the failing test** (append to `internal/config/config_test.go`, following the file's existing style):

```go
func TestPeersConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Daemon.Peers.Enabled {
		t.Fatal("peers must default to disabled")
	}
	if cfg.Daemon.Peers.Port != 7444 {
		t.Fatalf("peers port default = %d, want 7444", cfg.Daemon.Peers.Port)
	}
}

func TestPeersConfigAbsentKeyDisabled(t *testing.T) {
	// A config.json written before this feature existed must not enable it.
	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(`{"daemon":{"port":7999}}`), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.Peers.Enabled {
		t.Fatal("absent daemon.peers key must mean disabled")
	}
	if cfg.Daemon.Peers.Port != 7444 {
		t.Fatalf("absent key must keep default port, got %d", cfg.Daemon.Peers.Port)
	}
}
```

- [ ] **Step 2: Run to verify failure:** `go test ./internal/config/` — expect compile error (`Peers` undefined).

- [ ] **Step 3: Implement.** In `config.go`, add to `DaemonConfig` (after the `DNS DNSConfig` field, before `AutoStart`):

```go
	// Peers configures the EXPERIMENTAL cross-machine peer feature: paired
	// vibe daemons on a LAN can browse each other's routes through an mTLS
	// relay listener on Port. Off by default; absent key means disabled — a
	// plain bool is safe here because (unlike autostart) the zero value and
	// the safe default agree.
	Peers PeersConfig `json:"peers"`
```

New type next to `DNSConfig`:

```go
// PeersConfig gates the experimental peer subsystem. When Enabled, the daemon
// may start a LAN-facing mTLS listener on Port for paired-peer traffic only —
// the loopback API/dashboard listeners are unaffected either way.
type PeersConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}
```

In `DefaultConfig()`, inside the `DaemonConfig` literal:

```go
			Peers: PeersConfig{Enabled: false, Port: 7444},
```

- [ ] **Step 4: Verify:** `go build ./... && go vet ./... && go test ./internal/config/` — PASS; then full `go test ./...`.

- [ ] **Step 5: Commit** (only if commit approval given): `feat(config): add experimental daemon.peers gate (default off)`

---

### Task 2: Peer identity — self-signed cert + fingerprint

**Files:**
- Create: `internal/peer/identity.go`
- Test: `internal/peer/identity_test.go`

**Interfaces:**
- Produces:
  - `peer.EnsureIdentity(certsDir string) (tls.Certificate, error)` — loads `peer.pem`/`peer-key.pem` from certsDir, creating them (0600 key) if absent. Idempotent: second call returns the same cert.
  - `peer.Fingerprint(der []byte) string` — lowercase SHA-256 hex of a certificate's DER bytes.
  - `peer.IdentityFingerprint(c tls.Certificate) string` — fingerprint of `c.Certificate[0]`.

- [ ] **Step 1: Write the failing test** `internal/peer/identity_test.go`:

```go
package peer

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureIdentityCreatesAndReloads(t *testing.T) {
	dir := t.TempDir()
	id1, err := EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp1 := IdentityFingerprint(id1)
	if len(fp1) != 64 {
		t.Fatalf("fingerprint = %q, want 64 hex chars", fp1)
	}
	// Key must not be world-readable.
	info, err := os.Stat(filepath.Join(dir, "peer-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("peer-key.pem mode = %v, want 0600", info.Mode().Perm())
	}
	// Second call reloads the same identity, never regenerates.
	id2, err := EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if IdentityFingerprint(id2) != fp1 {
		t.Fatal("EnsureIdentity regenerated the identity on reload")
	}
	// The cert must be usable for both TLS client and server auth.
	leaf, err := x509.ParseCertificate(id1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	var hasServer, hasClient bool
	for _, ku := range leaf.ExtKeyUsage {
		if ku == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
		if ku == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasServer || !hasClient {
		t.Fatal("identity cert must carry both server- and client-auth EKUs")
	}
}
```

- [ ] **Step 2: Run to verify failure:** `go test ./internal/peer/` — compile error.

- [ ] **Step 3: Implement** `internal/peer/identity.go`:

```go
// Package peer holds the primitives for vibe's experimental cross-machine
// peer feature: a per-daemon identity certificate, SSH-style fingerprint
// pinning, invite-code pairing proofs, and the peers.json store. It is
// deliberately independent of the daemon package.
package peer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// EnsureIdentity loads or creates the daemon's peer identity: a self-signed
// ECDSA certificate used ONLY for daemon-to-daemon mTLS. It is deliberately
// separate from the browser CA (internal/cert) — that CA is trusted by the OS
// keychain and must never authenticate network peers. Trust in this cert is
// established by fingerprint pinning at pairing time, never by chain
// verification, so self-signed is exactly right.
func EnsureIdentity(certsDir string) (tls.Certificate, error) {
	certPath := filepath.Join(certsDir, "peer.pem")
	keyPath := filepath.Join(certsDir, "peer-key.pem")
	if id, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return id, nil
	}
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "vibe-peer " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0644); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// Fingerprint returns the lowercase SHA-256 hex digest of a certificate's
// DER bytes — the identity that pairing pins and every later handshake
// re-verifies.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// IdentityFingerprint is Fingerprint of a loaded identity's leaf.
func IdentityFingerprint(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return ""
	}
	return Fingerprint(c.Certificate[0])
}

func writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Verify:** `go test ./internal/peer/` PASS, then `go build ./... && go vet ./...`.

- [ ] **Step 5: Commit** (if approved): `feat(peer): self-signed peer identity certs with sha256 fingerprints`

---

### Task 3: Pairing primitives — invite codes, proofs, name sanitize

**Files:**
- Create: `internal/peer/pairing.go`
- Test: `internal/peer/pairing_test.go`

**Interfaces:**
- Produces:
  - `peer.NewInviteCode() (string, error)` — 6 crypto/rand digits, e.g. `"482913"`.
  - `peer.Proof(code, senderFP, receiverFP string) string` — hex HMAC-SHA256, key=code, message=`senderFP+"|"+receiverFP`.
  - `peer.VerifyProof(proof, code, senderFP, receiverFP string) bool` — constant-time compare.
  - `peer.SanitizeName(host string) string` — lowercase, strip a trailing `.local`, map every char outside `[a-z0-9-]` to `-`, collapse/trim `-`; returns `"peer"` if empty.
- Protocol contract consumed by Task 7: initiator (A) sends `Proof(code, fpA, fpB)` where fpA is its own fingerprint and fpB the server cert fingerprint it observed; responder (B) verifies using the TLS client cert fingerprint it observed and its own identity, then replies with `Proof(code, fpB, fpA)`. Both directions bind the code to the exact certs on the wire, so a MITM without the code can neither forge nor splice.

- [ ] **Step 1: Write the failing test** `internal/peer/pairing_test.go`:

```go
package peer

import "testing"

func TestInviteCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := NewInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 {
			t.Fatalf("code %q: want 6 digits", code)
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("code %q contains non-digit", code)
			}
		}
		seen[code] = true
	}
	if len(seen) < 2 {
		t.Fatal("codes are not random")
	}
}

func TestProofRoundTripAndTamper(t *testing.T) {
	const code, fpA, fpB = "123456", "aaaa", "bbbb"
	p := Proof(code, fpA, fpB)
	if !VerifyProof(p, code, fpA, fpB) {
		t.Fatal("valid proof rejected")
	}
	if VerifyProof(p, "654321", fpA, fpB) {
		t.Fatal("wrong code accepted")
	}
	if VerifyProof(p, code, "cccc", fpB) {
		t.Fatal("swapped sender fingerprint accepted — MITM splice")
	}
	if VerifyProof(p, code, fpB, fpA) {
		t.Fatal("reversed roles accepted — reply must differ from request")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Gregs-iMac.local": "gregs-imac",
		"MY_HOST":          "my-host",
		"..weird..":        "weird",
		"":                 "peer",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure:** `go test ./internal/peer/` — compile error.

- [ ] **Step 3: Implement** `internal/peer/pairing.go`:

```go
package peer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// NewInviteCode returns a 6-digit one-time pairing code from crypto/rand.
// Six digits is enough because the code guards a single online exchange with
// a 5-minute TTL, not an offline-attackable secret.
func NewInviteCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n), nil
}

// Proof binds an invite code to the exact certificate fingerprints seen on
// the wire: HMAC-SHA256 keyed by the code over "senderFP|receiverFP". The
// initiator proves (its own fp, the server fp it observed); the responder
// replies with the roles swapped. A MITM that substitutes either cert
// changes the message, and without the code it cannot recompute the MAC.
func Proof(code, senderFP, receiverFP string) string {
	mac := hmac.New(sha256.New, []byte(code))
	mac.Write([]byte(senderFP + "|" + receiverFP))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyProof checks a proof in constant time.
func VerifyProof(proof, code, senderFP, receiverFP string) bool {
	return hmac.Equal([]byte(proof), []byte(Proof(code, senderFP, receiverFP)))
}

// SanitizeName converts a hostname into a peer name safe for display and for
// use as a map key: lowercase, trailing ".local" dropped, anything outside
// [a-z0-9-] mapped to "-", runs collapsed, edges trimmed.
func SanitizeName(host string) string {
	h := strings.ToLower(strings.TrimSuffix(strings.ToLower(host), ".local"))
	var b strings.Builder
	prevDash := true // suppress leading dashes
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "peer"
	}
	return out
}
```

- [ ] **Step 4: Verify:** `go test ./internal/peer/` PASS; `go build ./... && go vet ./...`.

- [ ] **Step 5: Commit** (if approved): `feat(peer): invite codes and HMAC pairing proofs`

---

### Task 4: Peer store — `peers.json`

**Files:**
- Create: `internal/peer/store.go`
- Test: `internal/peer/store_test.go`

**Interfaces:**
- Produces:
  - `peer.Peer{Name, Host string; Port int; Fingerprint string; AddedAt time.Time}` (json tags `name, host, port, fingerprint, added_at`).
  - `peer.LoadPeers(dir string) ([]Peer, error)` — reads `<dir>/peers.json`; missing file → `(nil, nil)`.
  - `peer.SavePeers(dir string, peers []Peer) error` — writes `<dir>/peers.json` 0600, temp-file + rename.
- Order of the slice is significant: it is the collision tie-break order (first-paired wins). Load/Save must preserve it.

- [ ] **Step 1: Write the failing test** `internal/peer/store_test.go`:

```go
package peer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPeersRoundTripPreservesOrder(t *testing.T) {
	dir := t.TempDir()
	in := []Peer{
		{Name: "imac", Host: "imac.local", Port: 7444, Fingerprint: "aa", AddedAt: time.Now().UTC().Truncate(time.Second)},
		{Name: "studio", Host: "192.168.1.20", Port: 7444, Fingerprint: "bb", AddedAt: time.Now().UTC().Truncate(time.Second)},
	}
	if err := SavePeers(dir, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("peers.json mode = %v, want 0600", info.Mode().Perm())
	}
	out, err := LoadPeers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "imac" || out[1].Name != "studio" {
		t.Fatalf("order not preserved: %+v", out)
	}
	if out[0].Fingerprint != "aa" || out[1].Host != "192.168.1.20" {
		t.Fatalf("fields lost: %+v", out)
	}
}

func TestLoadPeersMissingFile(t *testing.T) {
	out, err := LoadPeers(t.TempDir())
	if err != nil || out != nil {
		t.Fatalf("missing file: got (%v, %v), want (nil, nil)", out, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**, then **Step 3: Implement** `internal/peer/store.go`:

```go
package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Peer is one paired daemon. Slice order in peers.json is the collision
// tie-break order (first-paired wins), so Load/Save preserve it.
type Peer struct {
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Fingerprint string    `json:"fingerprint"`
	AddedAt     time.Time `json:"added_at"`
}

// LoadPeers reads <dir>/peers.json. A missing file is (nil, nil): no peers.
func LoadPeers(dir string) ([]Peer, error) {
	data, err := os.ReadFile(filepath.Join(dir, "peers.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var peers []Peer
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

// SavePeers writes <dir>/peers.json (0600 — it holds the trust roots for the
// peer channel) via temp file + rename so a crash never truncates it.
func SavePeers(dir string, peers []Peer) error {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "peers.json.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "peers.json"))
}
```

- [ ] **Step 4: Verify:** `go test ./internal/peer/` PASS; build + vet.

- [ ] **Step 5: Commit** (if approved): `feat(peer): peers.json store with order-preserving round trip`

---

### Task 5: mTLS config builders + RouteSummary wire type

**Files:**
- Create: `internal/peer/tlsconf.go`, `internal/peer/summary.go`
- Test: `internal/peer/tlsconf_test.go`

**Interfaces:**
- Produces:
  - `peer.ServerTLSConfig(id tls.Certificate, authorize func(fp string) bool) *tls.Config` — requires a client cert (`RequireAnyClientCert`); `VerifyPeerCertificate` computes the leaf fingerprint and rejects the handshake unless `authorize(fp)` returns true. TLS 1.3 minimum.
  - `peer.ClientTLSConfig(id tls.Certificate, verify func(fp string) error) *tls.Config` — `InsecureSkipVerify: true` (chain/hostname checks replaced by pinning on purpose) with `VerifyPeerCertificate` calling `verify(fp)` on the server leaf. TLS 1.3 minimum.
  - `peer.RouteSummary{Name, Type string; Running, Ready bool; Icon string}` (json tags `name, type, running, ready, icon` — icon `omitempty`).
- Consumed by: Tasks 6–9 build every peer-channel connection from exactly these two constructors — no other `tls.Config` is permitted on the peer channel.

- [ ] **Step 1: Write the failing test** `internal/peer/tlsconf_test.go` (real handshake over loopback):

```go
package peer

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"testing"
)

// handshake dials the listener with cc, writes one byte, and returns the
// error from the round trip. The server echoes a byte on success. TLS 1.3
// reports client-cert rejection on first read/write, not Dial, so exercise
// both directions.
func handshake(t *testing.T, ln net.Listener, cc *tls.Config) error {
	t.Helper()
	conn, err := tls.Dial("tcp", ln.Addr().String(), cc)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}
	buf := make([]byte, 1)
	_, err = io.ReadFull(conn, buf)
	return err
}

func TestPinnedHandshake(t *testing.T) {
	serverID, err := EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	strangerID, err := EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverFP := IdentityFingerprint(serverID)
	clientFP := IdentityFingerprint(clientID)

	sc := ServerTLSConfig(serverID, func(fp string) bool { return fp == clientFP })
	ln, err := tls.Listen("tcp", "127.0.0.1:0", sc)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1)
				if _, err := io.ReadFull(c, buf); err == nil {
					c.Write(buf)
				}
			}(c)
		}
	}()

	pin := func(want string) func(string) error {
		return func(fp string) error {
			if fp != want {
				return fmt.Errorf("fingerprint mismatch")
			}
			return nil
		}
	}

	if err := handshake(t, ln, ClientTLSConfig(clientID, pin(serverFP))); err != nil {
		t.Fatalf("pinned peer rejected: %v", err)
	}
	if err := handshake(t, ln, ClientTLSConfig(strangerID, pin(serverFP))); err == nil {
		t.Fatal("server accepted an unpinned client cert")
	}
	if err := handshake(t, ln, ClientTLSConfig(clientID, pin("deadbeef"))); err == nil {
		t.Fatal("client accepted a server cert that fails the pin")
	}
	bare := &tls.Config{InsecureSkipVerify: true} // no client cert at all
	if err := handshake(t, ln, bare); err == nil {
		t.Fatal("server accepted a connection with no client cert")
	}
}
```

- [ ] **Step 2: Run to verify failure**, then **Step 3: Implement**.

`internal/peer/tlsconf.go`:

```go
package peer

import (
	"crypto/tls"
	"errors"
)

// ServerTLSConfig builds the peer listener's TLS config: present the local
// identity, demand SOME client cert, and reject the handshake unless the
// authorize callback accepts its fingerprint. authorize is consulted per
// handshake so pins added or removed at runtime (pairing, vibe peer remove)
// take effect without a listener restart — and during an open invite it can
// admit a not-yet-pinned cert whose pairing proof the HTTP layer then checks.
func ServerTLSConfig(id tls.Certificate, authorize func(fp string) bool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509Chain) error {
			if len(rawCerts) == 0 {
				return errors.New("peer: no client certificate")
			}
			if !authorize(Fingerprint(rawCerts[0])) {
				return errors.New("peer: client certificate not pinned")
			}
			return nil
		},
	}
}

// ClientTLSConfig builds the dialing side: present the local identity and
// replace chain+hostname verification with the verify callback on the server
// leaf's fingerprint. InsecureSkipVerify is deliberate — trust here is the
// pin established at pairing, not a CA chain; a self-signed peer cert can
// never chain anyway.
func ClientTLSConfig(id tls.Certificate, verify func(fp string) error) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{id},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509Chain) error {
			if len(rawCerts) == 0 {
				return errors.New("peer: server sent no certificate")
			}
			return verify(Fingerprint(rawCerts[0]))
		},
	}
}
```

**Note for the implementer:** the real signature of `VerifyPeerCertificate` is `func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error` — import `crypto/x509` and use that type (the `x509Chain` above is a stand-in to keep this plan honest about the one thing you must fix; the compiler will hold you to it).

`internal/peer/summary.go`:

```go
package peer

// RouteSummary is the wire shape of one route in GET /peer/routes — the
// read-only subset a paired machine is allowed to see. No ports, dirs, or
// commands cross the wire: a peer needs the name (to resolve), liveness (to
// display), and icon (to render), nothing else.
type RouteSummary struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
	Icon    string `json:"icon,omitempty"`
}
```

- [ ] **Step 4: Verify:** `go test ./internal/peer/` PASS; build + vet + full test run.

- [ ] **Step 5: Commit** (if approved): `feat(peer): pinned mTLS config builders and route summary wire type`

---

### Task 6: Daemon peer state + LAN listener with authz, `/peer/routes`, `/_api` blackhole

**Files:**
- Create: `internal/daemon/peer_listener.go`
- Modify: `internal/daemon/daemon.go` (Server fields, `NewServer`, `Start`, `Stop`)
- Test: `internal/daemon/peer_listener_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–5.
- Produces (on `*Server`, all in `peer_listener.go` unless noted):
  - Fields added to the `Server` struct in `daemon.go`:

```go
	// Experimental peer subsystem (cfg.Daemon.Peers). All fields below are
	// guarded by peerMu unless noted. peerStates is keyed by peer name.
	peerMu            sync.Mutex
	peerIdentity      *tls.Certificate
	peerFP            string
	peerList          []peer.Peer
	peerInviteCode    string
	peerInviteExpires time.Time
	peerSrv           *http.Server
	peerLn            net.Listener
	peerStates        map[string]*peerState
```

  - `s.peersEnabled() bool` — `s.cfg.Daemon.Peers.Enabled`.
  - `s.ensurePeerIdentity() error` — lazy `peer.EnsureIdentity(s.tlsCertsDir())` into `peerIdentity`/`peerFP`.
  - `s.ensurePeerListener() error` — idempotent; starts the TLS listener on `fmt.Sprintf(":%d", s.cfg.Daemon.Peers.Port)` with `peer.ServerTLSConfig(*s.peerIdentity, s.peerCertAuthorized)` and handler `s.peerHandler`.
  - `s.peerCertAuthorized(fp string) bool` — true if fp matches a stored peer OR an invite is currently open (unexpired).
  - `s.peerByFingerprint(fp string) *peer.Peer` — nil if not pinned.
  - `s.peerHandler(w, r)` — dispatch: `/_api/` prefix → 404 **first**; `POST /peer/pair` → `s.handlePeerPair` (Task 7 — until then, respond 503); any other request from a *non-pinned* cert → 403; `GET /peer/routes` → `s.handlePeerRoutes`; everything else → `s.peerServeRoute` (Task 8 — until then, 404).
  - `s.handlePeerRoutes(w, r)` — JSON `[]peer.RouteSummary` of non-bookmark routes sorted by name, with `ETag` header = sha256 hex of the body; `If-None-Match` match → 304.
  - `s.loadPeerSubsystem()` — called from `Start()` when `peersEnabled()`: loads `peer.LoadPeers(s.configDir())` into `peerList`/`peerStates`; if any peers exist, `ensurePeerIdentity` + `ensurePeerListener` + `go s.peerSyncLoop()` (stub the loop as a no-op goroutine until Task 8 — a method that just returns).
  - `Stop()` closes `peerLn`/`peerSrv` if set.

**Design notes for the implementer:**
- `tls.Listen` + `http.Server.Serve(ln)` populates `r.TLS`, so `r.TLS.PeerCertificates[0].Raw` → `peer.Fingerprint(...)` is the per-request identity. Guard `len(r.TLS.PeerCertificates) == 0` defensively (shouldn't happen under RequireAnyClientCert) with 403.
- Import the peer package as `"github.com/graiz/local.vibe/internal/peer"`.
- Do NOT touch the existing mux or listeners. The peer listener gets its own `http.Server{Handler: http.HandlerFunc(s.peerHandler)}`.
- 403/404 bodies: plain `http.Error` is fine on this channel; no HTML, no details that leak state to an unpaired caller.

- [ ] **Step 1: Write the failing test** `internal/daemon/peer_listener_test.go`. Build a Server the way `test_helpers_test.go` does elsewhere (check that file first and reuse its constructor if one exists); the essentials:

```go
package daemon

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

// newPeerTestServer returns a Server with peers enabled on an OS-assigned
// port, its peer listener running, and one pinned test client identity.
func newPeerTestServer(t *testing.T) (*Server, tls.Certificate, string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Daemon.Peers.Enabled = true
	cfg.Daemon.Peers.Port = 0 // OS-assigned; read back from s.peerLn.Addr()
	cfg.Daemon.TLS.CertsDir = t.TempDir()
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()

	clientID, err := peer.EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.peerList = []peer.Peer{{Name: "testpeer", Host: "127.0.0.1", Port: 0,
		Fingerprint: peer.IdentityFingerprint(clientID), AddedAt: time.Now()}}
	if err := s.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if err := s.ensurePeerListener(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.peerLn.Close() })
	return s, clientID, s.peerLn.Addr().String()
}

func peerHTTPClient(id tls.Certificate, serverFP string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: peer.ClientTLSConfig(id, func(fp string) error {
				if fp != serverFP {
					return fmt.Errorf("bad server fp")
				}
				return nil
			}),
		},
	}
}

func TestPeerListenerRoutesAndBlackholes(t *testing.T) {
	s, clientID, addr := newPeerTestServer(t)
	s.table.Add(&Route{Name: "face", Type: RouteSticky, Port: 12345, RegisteredAt: time.Now()})
	s.table.Add(&Route{Name: "bm", Type: RouteBookmark, ExternalURL: "https://example.com", RegisteredAt: time.Now()})
	c := peerHTTPClient(clientID, s.peerFP)

	// /_api/ is a blackhole even for a pinned peer.
	resp, err := c.Get("https://" + addr + "/_api/routes")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/_api on peer listener: got %d, want 404", resp.StatusCode)
	}

	// Route list excludes bookmarks and carries an ETag honored by If-None-Match.
	resp, err = c.Get("https://" + addr + "/peer/routes")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/peer/routes: got %d", resp.StatusCode)
	}
	var routes []peer.RouteSummary
	if err := json.Unmarshal(body, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Name != "face" {
		t.Fatalf("want exactly [face] (bookmark excluded), got %+v", routes)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	req, _ := http.NewRequest("GET", "https://"+addr+"/peer/routes", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: got %d, want 304", resp.StatusCode)
	}
}

func TestPeerListenerRejectsUnpinned(t *testing.T) {
	_, _, addr := newPeerTestServer(t)
	strangerID, err := peer.EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: peer.ClientTLSConfig(strangerID, func(string) error { return nil }),
	}}
	if _, err := c.Get("https://" + addr + "/peer/routes"); err == nil {
		t.Fatal("unpinned client cert survived the handshake with no invite open")
	}
}

func TestPeerListenerOffByDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()
	s.loadPeerSubsystem()
	if s.peerLn != nil {
		t.Fatal("peer listener started with the flag off")
	}
}
```

(For `Peers.Port = 0`: make `ensurePeerListener` accept port 0 and record the real address via `s.peerLn.Addr()` — tests depend on it, production always sets a real port.)

- [ ] **Step 2: Run to verify failure:** `go test ./internal/daemon/ -run TestPeerListener` — compile errors.

- [ ] **Step 3: Implement** `peer_listener.go` + the `daemon.go` edits per the Produces list. Core of the handler:

```go
// peerHandler dispatches requests on the LAN peer listener. Order is
// security-load-bearing: the /_api blackhole comes first so the daemon API
// can never be reached through this listener, pairing is the only endpoint
// open to a not-yet-pinned cert (and only while an invite is open — the TLS
// authorize callback already enforced that), and everything else requires a
// pinned peer.
func (s *Server) peerHandler(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_api/") || r.URL.Path == "/_api" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	fp := peer.Fingerprint(r.TLS.PeerCertificates[0].Raw)
	if r.Method == http.MethodPost && r.URL.Path == "/peer/pair" {
		s.handlePeerPair(w, r, fp) // Task 7; stub 503 until then
		return
	}
	if s.peerByFingerprint(fp) == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/peer/routes" {
		s.handlePeerRoutes(w, r)
		return
	}
	s.peerServeRoute(w, r) // Task 8; stub 404 until then
}
```

`handlePeerRoutes`: build `[]peer.RouteSummary` from `s.table.List()` skipping `RouteBookmark`, `sort.Slice` by name, `json.Marshal`, `etag := fmt.Sprintf("%q", ...sha256 hex...)` — use a quoted strong ETag, compare against `If-None-Match` with exact string match, set `ETag` always, 304 with no body on match.

`ensurePeerListener` sketch:

```go
func (s *Server) ensurePeerListener() error {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if s.peerLn != nil {
		return nil
	}
	sc := peer.ServerTLSConfig(*s.peerIdentity, s.peerCertAuthorized)
	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Daemon.Peers.Port), sc)
	if err != nil {
		return fmt.Errorf("peer listener: %w", err)
	}
	s.peerLn = ln
	s.peerSrv = &http.Server{Handler: http.HandlerFunc(s.peerHandler)}
	go s.peerSrv.Serve(ln)
	fmt.Printf("vibe peer listener on %s (mTLS, paired peers only)\n", ln.Addr())
	return nil
}
```

`peerCertAuthorized` takes `peerMu`, checks `peerList` fingerprints, then `peerInviteCode != "" && time.Now().Before(peerInviteExpires)`.

In `daemon.go` `Start()`, after the DNS block and before the final `Serve`:

```go
	if s.peersEnabled() {
		s.loadPeerSubsystem()
	}
```

In `Stop()`: close `peerSrv` (`Close()`) and `peerLn` if non-nil.

- [ ] **Step 4: Verify:** `go test ./internal/daemon/ -run TestPeerListener -v` PASS, then the FULL suite `go build ./... && go vet ./... && go test ./...` (flag-off regression check is the whole existing suite).

- [ ] **Step 5: Commit** (if approved): `feat(daemon): mTLS peer listener with pinned authz and /_api blackhole`

---### Task 7: Pairing — B-side `/peer/pair`, A-side `pairWithPeer`, loopback `/_api/peers` API

**Files:**
- Create: `internal/daemon/peer_pair.go`, `internal/daemon/peer_api.go`
- Modify: `internal/daemon/api.go` (dispatch + `isDaemonAPIPath`)
- Test: `internal/daemon/peer_pair_test.go`

**Interfaces:**
- Consumes: Task 6's listener/state; `peer.Proof/VerifyProof/NewInviteCode/SanitizeName/SavePeers`.
- Produces:
  - Wire types (in `peer_pair.go`): `pairRequest{Name string `json:"name"`; Host string `json:"host"`; Port int `json:"port"`; Proof string `json:"proof"`}`, `pairResponse{Name string `json:"name"`; Port int `json:"port"`; Proof string `json:"proof"`}`.
  - `s.handlePeerPair(w, r, clientFP string)` — B side. All failures are `http.Error(w, "pairing failed", http.StatusForbidden)` — one generic message, no oracle. Success: verify invite open + `peer.VerifyProof(req.Proof, code, clientFP, s.peerFP)`; determine host (`req.Host` if non-empty else IP from `r.RemoteAddr`); append `peer.Peer{Name: peer.SanitizeName(req.Name), Host: host, Port: req.Port, Fingerprint: clientFP, AddedAt: time.Now()}` (de-dupe by fingerprint: re-pairing updates host/port in place); `peer.SavePeers(s.configDir(), s.peerList)`; close the invite (`peerInviteCode = ""`); reply `pairResponse{Name: <sanitized local hostname>, Port: s.cfg.Daemon.Peers.Port, Proof: peer.Proof(code, s.peerFP, clientFP)}`.
  - `s.pairWithPeer(host string, port int, code string) (peer.Peer, error)` — A side, three steps: (1) probe `tls.Dial` with `peer.ClientTLSConfig(id, capture-fp-and-accept)` to learn fpB, close; (2) POST `https://host:port/peer/pair` with a client pinned to fpB, body `pairRequest{Name: sanitized os.Hostname(), Host: sanitized-or-raw os.Hostname() (advertise name), Port: s.cfg.Daemon.Peers.Port, Proof: peer.Proof(code, s.peerFP, fpB)}`; (3) verify `peer.VerifyProof(resp.Proof, code, fpB, s.peerFP)` — reject on mismatch (a relay that swapped certs fails here). Persist `peer.Peer{Name: resp.Name, Host: host, Port: resp.Port (fallback: port arg if 0), Fingerprint: fpB, AddedAt: now}` de-duped by fingerprint, save, then `ensurePeerListener()` + start the sync loop if not running.
  - `s.openPeerInvite() (code string, expires time.Time, err error)` — `ensurePeerIdentity` + `ensurePeerListener` + set `peerInviteCode`/`peerInviteExpires = now+5m`.
  - Loopback API in `peer_api.go`, wired into `apiHandler`'s switch **and** `isDaemonAPIPath`:
    - `GET /_api/peers` → `s.handlePeersList` (Task 8 fills in route/status data; for now: `{"enabled": bool, "peers": [{name,host,port,fingerprint,added_at}]}`).
    - `POST /_api/peers/invite` → `s.handlePeerInvite` → `{"code": "...", "expires_at": ..., "port": 7444}`; 409 `writeJSONError` if `!s.peersEnabled()`.
    - `POST /_api/peers` body `{"host": "...", "port": 7444, "code": "123456"}` → runs `pairWithPeer` → 200 with the stored peer, or 502 `writeJSONError` with the error; 409 if disabled.
    - `DELETE /_api/peers/{name}` → remove from `peerList` + save; 404 if unknown.
  - These are POST/DELETE, so the existing `apiStateChanging` + `apiRequestCrossSite` guard covers them with zero new code — note this in a comment.

**api.go dispatch entries** (add to the switch, before the `default`):

```go
	case r.Method == http.MethodGet && path == "/peers":
		s.handlePeersList(w, r)
	case r.Method == http.MethodPost && path == "/peers/invite":
		s.handlePeerInvite(w, r)
	case r.Method == http.MethodPost && path == "/peers":
		s.handlePeerAdd(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/peers/"):
		s.handlePeerRemove(w, r, strings.TrimPrefix(path, "/peers/"))
```

And mirror all four in `isDaemonAPIPath` so a proxied app's `/_api/peers` (unlikely but possible) still passes through on non-local hosts only when methods differ — follow the exact pattern of the existing entries.

- [ ] **Step 1: Write the failing test** `internal/daemon/peer_pair_test.go`:

```go
package daemon

import (
	"strings"
	"testing"

	"github.com/graiz/local.vibe/internal/config"
	"github.com/graiz/local.vibe/internal/peer"
)

func newBareServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Daemon.Peers.Enabled = true
	cfg.Daemon.Peers.Port = 0
	cfg.Daemon.TLS.CertsDir = t.TempDir()
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()
	return s
}

func TestPairingHappyPathIsMutual(t *testing.T) {
	b := newBareServer(t)
	code, _, err := b.openPeerInvite()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())

	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	got, err := a.pairWithPeer(host, port, code)
	if err != nil {
		t.Fatalf("pairWithPeer: %v", err)
	}
	if got.Fingerprint != b.peerFP {
		t.Fatalf("A pinned %q, want B's fp %q", got.Fingerprint, b.peerFP)
	}
	// Mutual: B stored A too, pinned to A's real fingerprint.
	if p := b.peerByFingerprint(a.peerFP); p == nil {
		t.Fatal("B did not store A after pairing")
	}
	// Invite is one-time.
	if _, err := a.pairWithPeer(host, port, code); err == nil {
		t.Fatal("second pairing with a used invite succeeded")
	}
	// Both sides persisted.
	if ps, _ := peer.LoadPeers(a.configDir()); len(ps) != 1 {
		t.Fatalf("A peers.json: %+v", ps)
	}
	if ps, _ := peer.LoadPeers(b.configDir()); len(ps) != 1 {
		t.Fatalf("B peers.json: %+v", ps)
	}
}

func TestPairingWrongCodeRejected(t *testing.T) {
	b := newBareServer(t)
	if _, _, err := b.openPeerInvite(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())

	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pairWithPeer(host, port, "000000"); err == nil {
		t.Fatal("wrong invite code accepted")
	}
	if len(b.peerList) != 0 {
		t.Fatal("B stored a peer despite a bad proof")
	}
}

func TestPairingRequiresOpenInvite(t *testing.T) {
	b := newBareServer(t)
	if err := b.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if err := b.ensurePeerListener(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.peerLn.Close() })
	host, port := splitAddr(t, b.peerLn.Addr().String())
	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pairWithPeer(host, port, "123456"); err == nil {
		t.Fatal("pairing succeeded with no invite open")
	}
}
```

Add a `splitAddr(t, "host:port") (string, int)` helper (strings.LastIndex + strconv). Note `TestPairingRequiresOpenInvite` exercises the TLS-layer rejection: with no invite open, A's unpinned cert dies in the handshake — assert only that an error surfaced.

- [ ] **Step 2: Run to verify failure**, **Step 3: Implement** per the Produces contracts. Implementation notes:
  - The probe dial in `pairWithPeer` step (1) happens while B's invite is open, so B's authorize admits the unknown cert; close the conn immediately after handshake (`conn.Handshake()` then read `conn.ConnectionState().PeerCertificates[0].Raw`).
  - Use one `http.Client{Timeout: 5s}` per call, never a shared one (per-peer pinning).
  - Keep `peerMu` held only around state mutation, never across network I/O.
  - `handlePeerInvite`/`handlePeerAdd`/`handlePeerRemove` return `writeJSONError(w, http.StatusConflict, "peers are disabled — set daemon.peers.enabled=true in ~/.vibe/config.json and restart")` when `!s.peersEnabled()`.

- [ ] **Step 4: Verify:** `go test ./internal/daemon/ -run TestPairing -v` PASS; full suite green.

- [ ] **Step 5: Commit** (if approved): `feat(daemon): invite-code pairing with mutual fingerprint pinning`

---

### Task 8: Peer-route sync cache + host proxying on the serving side

**Files:**
- Create: `internal/daemon/peer_sync.go`
- Modify: `internal/daemon/peer_listener.go` (real `peerServeRoute`)
- Test: `internal/daemon/peer_sync_test.go`

**Interfaces:**
- Consumes: Tasks 5–7.
- Produces:
  - `type peerState struct { routes []peer.RouteSummary; etag string; lastOK time.Time; lastErr string; lastRefresh time.Time; refreshing atomic.Bool }`
  - `s.peerSyncLoop()` — replaces Task 6's stub: every 30s (`time.Ticker`), refresh all peers; exits on `<-s.quit`.
  - `s.refreshPeerRoutes(p peer.Peer, force bool)` — throttled (skip if `lastRefresh` < 3s ago unless force), single-flighted (`refreshing` CAS), GET `https://host:port/peer/routes` with `If-None-Match`, 3s timeout, pinned client config. On 200: decode, store, and if the *name set* changed call `s.reloadTLSCert()` (peer names must land in SANs). On 304: bump `lastOK`. On error: record `lastErr` (keep stale routes — spec: cache survives outages).
  - `s.findPeerRoute(name string) (peer.Peer, peer.RouteSummary, bool)` — iterate `peerList` in order (first-paired wins), return first cache hit. On total miss AND `peersEnabled()`, do one synchronous `refreshPeerRoutes(p, false)` sweep (throttle makes this cheap) and re-check — this is the "unknown-host miss triggers refresh" path.
  - `s.peerRouteNames() []string` — deduped names across all peer caches (for `tlsHostnames`).
  - In `daemon.go` `tlsHostnames()`: append `s.peerRouteNames()` entries not already present (local names first — order irrelevant to SANs, dedupe required).
  - Real `s.peerServeRoute(w, r)` in `peer_listener.go` (B side): strip port from `r.Host`; require suffix `"."+tld` and a table hit; bookmark or miss → 404. Managed route not running/ready → run recovery with a discarded response (`s.recoverManagedRoute(discardWriter{}, r, route)`); if it returns true (page would have been served) → `s.servePeerStartingPage(w, route)` (503, `themeHead` + `<meta http-equiv="refresh" content="2">`, text "starting <name> on this machine — retrying"); if false (adopted) → fall through. Then `!s.isPortReady(route.Port)` → same starting page. Else `route.TouchActivity()` and reverse-proxy to `http://localhost:<port>` with the same upgrade-only Origin rewrite used in `routeRequest` (copy the `isWebSocketUpgrade`/`rewriteOriginHeader` director wrap verbatim — same reasoning, this is now the hop that talks to the dev server).
  - `type discardWriter struct{ h http.Header }` implementing `http.ResponseWriter` (lazily-allocated Header map, Write→len(b), WriteHeader→no-op) — lives in `peer_listener.go` with a comment: recovery's page rendering is discarded; only its adopt/spawn side effects are wanted on the peer path.

- [ ] **Step 1: Write the failing test** `internal/daemon/peer_sync_test.go`. Wire two real servers: B from `newPeerTestServer` (Task 6) plus a real backend:

```go
func TestPeerSyncAndServeRoute(t *testing.T) {
	// Backend app on B's machine.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from face"))
	}))
	t.Cleanup(backend.Close)
	backendPort := portOf(t, backend.URL)

	b := newBareServer(t)
	code, _, err := b.openPeerInvite()
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { b.peerLn.Close() })
	bHost, bPort := splitAddr(t, b.peerLn.Addr().String())
	b.table.Add(&Route{Name: "face", Type: RouteSticky, Port: backendPort, RegisteredAt: time.Now()})

	a := newBareServer(t)
	if err := a.ensurePeerIdentity(); err != nil { t.Fatal(err) }
	if _, err := a.pairWithPeer(bHost, bPort, code); err != nil { t.Fatal(err) }

	// Sync populates the cache; findPeerRoute's miss path forces the refresh.
	p, sum, ok := a.findPeerRoute("face")
	if !ok {
		t.Fatal("face not found via peer cache after refresh")
	}
	if p.Fingerprint != b.peerFP || sum.Name != "face" {
		t.Fatalf("wrong resolution: peer=%+v summary=%+v", p, sum)
	}
	// SANs must now include the peer route.
	found := false
	for _, h := range a.tlsHostnames() {
		if h == "face.vibe" { found = true }
	}
	if !found {
		t.Fatalf("face.vibe missing from tlsHostnames: %v", a.tlsHostnames())
	}
	// B's peer listener serves the route by Host to a pinned client.
	c := peerHTTPClient(mustClientIdentity(t, a), b.peerFP) // reuse a's identity
	req, _ := http.NewRequest("GET", "https://"+b.peerLn.Addr().String()+"/anything", nil)
	req.Host = "face.vibe"
	resp, err := c.Do(req)
	if err != nil { t.Fatal(err) }
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from face" {
		t.Fatalf("proxied body = %q", body)
	}
}

func TestPeerSyncKeepsStaleCacheOnFailure(t *testing.T) {
	// Pair A↔B, populate cache, kill B's listener, force refresh: routes stay.
	// Assert findPeerRoute still returns face and lastErr is non-empty.
}
```

Write `TestPeerSyncKeepsStaleCacheOnFailure` in full (the comment above states the required behavior — implement the test body, no placeholder left behind), plus helpers `portOf` and `mustClientIdentity` (load A's identity from its certs dir via `peer.EnsureIdentity` — same files, same cert).

- [ ] **Step 2: Run to verify failure**, **Step 3: Implement**. Concurrency notes: `peerStates` map guarded by `peerMu`; copy the routes slice out under lock for readers; never hold `peerMu` during HTTP calls — take a snapshot of `peerList`, do I/O, re-take lock to store.

- [ ] **Step 4: Verify:** targeted tests PASS, full suite green (`go build ./... && go vet ./... && go test ./...`).

- [ ] **Step 5: Commit** (if approved): `feat(daemon): peer route sync cache and serving-side host proxy`

---

### Task 9: Browsing-side resolution — `routeRequest` hook + `proxyToPeer`

**Files:**
- Create: `internal/daemon/peer_proxy.go`
- Modify: `internal/daemon/daemon.go` (`routeRequest`)
- Test: `internal/daemon/peer_proxy_test.go`

**Interfaces:**
- Consumes: `findPeerRoute` (Task 8), `peer.ClientTLSConfig` (Task 5).
- Produces:
  - `s.proxyToPeer(w, r, p peer.Peer, name string)` — `httputil.NewSingleHostReverseProxy` to `https://<p.Host>:<p.Port>`; `Transport: &http.Transport{TLSClientConfig: peer.ClientTLSConfig(*s.peerIdentity, pin(p.Fingerprint))}`; `req.Host` left as the browser sent it (SingleHostReverseProxy doesn't touch `req.Host` — that is what makes B's Host-based dispatch work); `ErrorHandler` ignores `context.Canceled` (copy the guard from `routeRequest`'s ErrorHandler verbatim, same reasoning) and otherwise records `lastErr` on the peer state and serves `s.servePeerUnreachablePage(w, p, name)` — themed 502: "face lives on <peer> but <peer> isn't answering — check `vibe peers` on both machines".
  - `routeRequest` hook — in `daemon.go`, insert between the closing brace of the `if route, ok := s.table.Get(name); ok { ... }` block and the `worktreeParent` fallback:

```go
		// Peer routes: a name we don't serve locally may live on a paired
		// machine. Local routes always win (checked above); among peers,
		// peers.json order breaks ties inside findPeerRoute. Placed before
		// the worktree-parent redirect because an exact peer-route match
		// beats a heuristic parent fallback.
		if s.peersEnabled() {
			if p, _, ok := s.findPeerRoute(name); ok {
				s.proxyToPeer(w, r, p, name)
				return
			}
		}
```

- [ ] **Step 1: Write the failing test** `internal/daemon/peer_proxy_test.go` — full A→B→backend traversal through `a.routeRequest`:

```go
func TestRouteRequestResolvesPeerRoute(t *testing.T) {
	// backend + B + pairing exactly as in TestPeerSyncAndServeRoute.
	// Then drive A's routeRequest directly:
	req := httptest.NewRequest("GET", "http://face.vibe/", nil)
	req.Host = "face.vibe"
	rec := httptest.NewRecorder()
	a.routeRequest(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello from face" {
		t.Fatalf("A->B relay: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestLocalRouteShadowsPeerRoute(t *testing.T) {
	// Same setup, plus a LOCAL sticky route named "face" on A pointing at a
	// second backend that returns "local face". routeRequest must serve the
	// local one — the peer branch is never reached.
}

func TestPeerUnreachableServesErrorPageNotBare502(t *testing.T) {
	// Pair, populate cache, close B's peer listener, request face.vibe via
	// A's routeRequest: expect 502, body contains the peer's name and
	// "vibe peers" (the fix hint), not an empty body.
}

func TestUnknownHostStillDashboardWhenPeersDisabled(t *testing.T) {
	// Flag off on A (default config), same request: serveDashboard path —
	// assert the peer branch didn't fire (response is the dashboard HTML,
	// and no peer state was created).
}
```

Write all four bodies in full, reusing Task 8's helpers. (A's TLS is disabled in these tests, so `routeRequest`'s HTTPS redirect doesn't fire — `cfg.Daemon.TLS.Enabled` stays false in `newBareServer`.)

- [ ] **Step 2: Run to verify failure**, **Step 3: Implement** (`peer_proxy.go` + the ~8-line `routeRequest` insertion).

- [ ] **Step 4: Verify:** targeted PASS + full suite green — the full suite matters most here, `routeRequest` is the hottest path in the daemon.

- [ ] **Step 5: Commit** (if approved): `feat(daemon): resolve and relay peer routes from routeRequest`

---

### Task 10: Security lock-in — origin exclusion + listener-surface tests

**Files:**
- Test: `internal/daemon/peer_security_test.go` (new; pure tests, no production code expected — if a test fails, the fix goes in the file that broke the invariant)

**Interfaces:** consumes everything prior; produces regression armor.

- [ ] **Step 1: Write the tests** (these should PASS immediately if Tasks 6–9 are correct; a failure here is a real bug — fix the production code, never relax the test):

```go
// A peer route's host must NOT be a trusted origin for A's API: pages under
// face.vibe are authored by another machine, and a compromised peer must not
// drive the register-executes-shell API. This holds because peer routes never
// enter s.table — this test pins that consequence down so a future refactor
// (e.g. materializing peer routes as table entries) trips it loudly.
func TestPeerRouteHostNotTrustedOrigin(t *testing.T) {
	// A paired + cache populated with "face" (Task 8 helpers, no local route).
	if a.originTrusted("http://face.vibe") {
		t.Fatal("peer-route origin trusted by the API — cross-machine escalation")
	}
	// Sanity: a local sticky route IS trusted (existing behavior unchanged).
	a.table.Add(&Route{Name: "mine", Type: RouteSticky, Port: 1234, RegisteredAt: time.Now()})
	if !a.originTrusted("http://mine.vibe") {
		t.Fatal("local sticky route origin no longer trusted — regression")
	}
}

// The peer listener must never expose daemon state to an unpaired caller,
// even one that completes a handshake during an open invite window.
func TestInviteWindowExposesOnlyPairing(t *testing.T) {
	// B with an open invite; stranger identity (never pairs).
	// GET /peer/routes  -> 403 (handshake passes via invite, HTTP layer denies)
	// GET /_api/routes  -> 404 (blackhole)
	// GET / with Host face.vibe -> 403 (peerServeRoute requires pinned)
}

// State-changing peer API endpoints on the LOOPBACK listener are covered by
// the cross-site guard: a browser POST with a foreign Origin is rejected.
func TestPeerAPICrossSiteBlocked(t *testing.T) {
	// Build request POST /_api/peers/invite with Origin: https://evil.example
	// through s.apiHandler; expect 403 (pattern: see origin_test.go).
}
```

Write all bodies in full, modeling `TestPeerAPICrossSiteBlocked` on the existing `origin_test.go` style.

- [ ] **Step 2: Run:** `go test ./internal/daemon/ -run 'TestPeerRouteHostNotTrusted|TestInviteWindow|TestPeerAPICrossSite' -v` — expect PASS; investigate and fix production code on any failure.

- [ ] **Step 3: Full suite** green.

- [ ] **Step 4: Commit** (if approved): `test(daemon): lock in peer security invariants (origin exclusion, invite surface, CSRF guard)`

---

### Task 11: CLI — client methods, `vibe peers` / `vibe peer *`, list + doctor surfacing

**Files:**
- Modify: `internal/client/client.go`
- Create: `cmd/peer.go`
- Modify: `cmd/list.go`, `cmd/doctor.go`, `cmd/root.go`
- Test: none new beyond compile + `go vet` (CLI files here are thin client-printing wrappers, matching the repo's existing pattern of untested cobra glue; daemon behavior is already covered).

**Interfaces:**
- Consumes: `/_api/peers*` endpoints (Task 7) + the status fields Task 8 stores (extend `handlePeersList` NOW to include them — this task owns that edit in `peer_api.go`).
- Produces in `client.go` (mirror `RouteInfo`/`Worktrees` style — best-effort `Peers()` never breaks `vibe list`):

```go
type PeerRouteInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
}

type PeerInfo struct {
	Name        string          `json:"name"`
	Host        string          `json:"host"`
	Port        int             `json:"port"`
	Fingerprint string          `json:"fingerprint"`
	AddedAt     time.Time       `json:"added_at"`
	Reachable   bool            `json:"reachable"`
	LastError   string          `json:"last_error,omitempty"`
	Routes      []PeerRouteInfo `json:"routes"`
}

type PeersResponse struct {
	Enabled bool       `json:"enabled"`
	Peers   []PeerInfo `json:"peers"`
}

type PeerInviteResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	Port      int       `json:"port"`
}

func (c *Client) Peers() (*PeersResponse, error)
func (c *Client) PeerInvite() (*PeerInviteResponse, error)
func (c *Client) PeerAdd(host string, port int, code string) (*PeerInfo, error)
func (c *Client) PeerRemove(name string) error
```

  (`Reachable` = daemon-side `lastOK` within the last 2 poll intervals; computed in `handlePeersList`.)
- Produces in `cmd/peer.go`: `peersCmd` (`Use: "peers"`, prints a table: NAME / HOST / STATUS / ROUTES); `peerCmd` (`Use: "peer"`) with subcommands:
  - `invite` — calls `PeerInvite`, prints: code, expiry, and the exact command to run on the other machine: `vibe peer add <this-hostname> --code <code>` (get hostname via `os.Hostname()`).
  - `add <host>` — flags `--code` (required), `--port` (default 7444); calls `PeerAdd`; prints the paired name + fingerprint.
  - `remove <name>` — calls `PeerRemove`.
  - When the daemon returns the 409 disabled error, surface its message verbatim (it contains the enable instructions).
  - `init()`: `rootCmd.AddCommand(peersCmd)`, `rootCmd.AddCommand(peerCmd)` — but match the repo convention: route both through `cmd/root.go`'s init like the existing commands (look at how `listCmd` is added and do the same there instead of a local init if that's the pattern).
- `cmd/list.go`: after the worktrees loop, best-effort `c.Peers()`; when enabled and non-empty, one row per peer route: name, URL (`https://<name>.vibe`), port `—`, type `peer`, status: `ready`/`stopped`/**`shadowed`** (shadowed when a local route or an earlier peer claims the same name — compute against the already-fetched `routes` slice and earlier peers' routes), pid `—`, last column `on <peername>`.
- `cmd/doctor.go`: when `Peers()` reports enabled, print one line per peer: `peer <name>: ok` or `peer <name>: unreachable — <lastError>` following doctor's existing output style.

- [ ] **Step 1: Implement** all of the above (no new tests; this is presentation glue over tested endpoints).
- [ ] **Step 2: Verify:** `go build ./... && go vet ./... && go test ./...` green; then manual smoke: `go build -o /tmp/vibe-smoke . && /tmp/vibe-smoke peers` against a daemonless environment must print the "daemon not running" error, not panic.
- [ ] **Step 3: Commit** (if approved): `feat(cli): vibe peers, peer invite/add/remove, list+doctor surfacing`

---

### Task 12: Dashboard — peer groups with shadow badges

**Files:**
- Modify: `internal/daemon/dashboard.go` (+ its template), `internal/daemon/peer_api.go` if data shaping is needed
- Test: `internal/daemon/dashboard_test.go` (append)

**Interfaces:**
- Consumes: `s.peerStates` snapshot (Task 8).
- Produces: dashboard sections, one per paired peer with cached routes, following the existing worktree-grouping pattern in `dashboard.go` (read how worktree routes are grouped under their parent and mirror the structure). Each peer route renders: icon (or pool fallback, reuse the existing icon-priority helper), name linking to `https://<name>.<tld>`, peer machine name, ready/stopped dot — and **no** start/stop/edit/delete controls. A route shadowed by a local name or an earlier peer gets a visible `shadowed by <winner>` badge and its link is rendered inert (plain text, not an anchor — clicking it would reach the winner, which is misleading). All strings HTML-escaped like every other user string in this file.

- [ ] **Step 1: Write the failing test** (append to `dashboard_test.go`, following its existing serve-and-assert style):

```go
func TestDashboardShowsPeerRoutesReadOnly(t *testing.T) {
	// Server with peers enabled and a peerStates entry for peer "imac"
	// containing routes face (ready) and clash (ready), plus a LOCAL sticky
	// route also named "clash".
	// Render the dashboard (serveDashboard with Host local.vibe).
	// Assert: body contains "imac" and "face"; the face entry links to
	// https://face.vibe; the peer "clash" entry carries "shadowed by" and is
	// not an anchor; no start/stop form targets a peer route name.
}
```

Write the body in full against the actual dashboard markup (inspect what `serveDashboard` emits for worktree groups and assert on comparable anchors/classes).

- [ ] **Step 2: Run to verify failure**, **Step 3: Implement**, **Step 4: Full suite green.**
- [ ] **Step 5: Commit** (if approved): `feat(dashboard): read-only peer route groups with shadow badges`

---

### Task 13: End-to-end integration test + docs

**Files:**
- Test: `internal/daemon/peer_e2e_test.go`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Write the end-to-end test** — the full user story in one process, no network beyond loopback:

```go
// TestPeerEndToEnd walks the whole feature: enable → invite → pair → sync →
// browse A's routeRequest through B's peer listener to B's app → B goes
// away → A serves the unreachable page → peer removed → the name 404s to
// dashboard. One test, the spec's happy path and its two failure edges.
func TestPeerEndToEnd(t *testing.T) { ... }
```

Write it in full from Task 8/9 helpers. Also assert the WebSocket upgrade path: send a request with `Connection: Upgrade`/`Upgrade: websocket` headers through the relay to a backend that echoes the `Origin` header it received, and assert the backend saw its own origin (the upgrade rewrite ran on B), while a plain GET's Origin passed through untouched.

- [ ] **Step 2: Full suite + race:** `go test ./... && go test -race ./internal/daemon/ ./internal/peer/` — the race run is required for this task; the peer cache and listener state are the newest concurrent code in the daemon.

- [ ] **Step 3: Update `CLAUDE.md`:** add a "Peers (experimental)" subsection under Architecture — flag path (`daemon.peers.enabled`, default off), the relay design in ~6 lines (identity certs, pinning, invite pairing, listener surface, `/_api` blackhole, local-wins collisions), file map (`internal/peer/`, `peer_*.go`, `~/.vibe/peers.json`, `~/.vibe/certs/peer.pem`), and the security invariants (loopback listeners untouched; peer hosts excluded from `originTrusted`). Also add `~/.vibe/peers.json` to the "Files at runtime" list.

- [ ] **Step 4: Commit** (if approved): `feat(peer): end-to-end coverage and docs for experimental peer routes`

---

## Plan self-review (done at write time)

- **Spec coverage:** flag (T1), identity+pairing+pinning (T2,3,5,7), listener surface incl. `/_api` blackhole + bookmark exclusion (T6), sync+ETag+SAN reload+stale-cache (T8), resolution order+local-wins+relay+no-bare-502 (T9), origin exclusion + invite-surface + CSRF (T10), CLI+doctor (T11), dashboard+shadow badges (T12), e2e+upgrade-Origin+docs (T13). Stopped-route static page: T8 (`servePeerStartingPage` + discardWriter). Fingerprint-mismatch hard failure: pinned client config fails the dial (T5/T9 unreachable page); the "re-pair" hint lives in the unreachable page + doctor output. Out of spec scope here, matching the spec: mDNS (phase 2), remote control, revocation UX, SSE.
- **Known deviation from spec:** during an open invite window, an unknown cert completes the TLS handshake and is confined to `/peer/pair` by the HTTP layer (spec said handshake-level rejection always). Outside an invite window the handshake rejection holds. T10's `TestInviteWindowExposesOnlyPairing` pins the confinement.
- **Type consistency pass:** `peer.Peer`/`peerState`/`RouteSummary`/proof role order (`Proof(code, senderFP, receiverFP)`, reply swaps roles) used consistently across T3, T6–T9 tests. `VerifyPeerCertificate` signature flagged explicitly in T5.
- **Placeholder scan:** T8's stale-cache test, T9's three sibling tests, T10's bodies, T12's body, T13's e2e are specified by contract with required assertions enumerated — the implementer writes the bodies against real markup/helpers, with pass/fail criteria stated. No TBDs remain.
