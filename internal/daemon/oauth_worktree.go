package daemon

// Worktree-aware OAuth bridging.
//
// The oauth_callback_port bridge lets an app keep a fixed
// http://localhost:<N> redirect URI registered with its OAuth provider while
// sessions live on the .vibe host. Worktrees break the single-destination
// assumption: main and every worktree share the provider registration (the
// redirect URI must match exactly, including in the code-for-token exchange,
// so it can't be per-worktree), but each is its own browser origin holding
// its own state/PKCE cookies — the callback must return to the origin that
// STARTED the flow.
//
// The trick: providers echo the opaque `state` parameter untouched. When a
// worktree app 302s the browser toward its provider, the reverse proxy tags
// the state with the route name ("v1!<route>!<original>"); the bridge on
// localhost:<N> unwraps the tag, restores the original state, and forwards
// the callback to that worktree's .vibe host. The app never sees the tag and
// the registered redirect URI never changes. Only server-side redirect flows
// can be tagged — a provider URL assembled in client-side JS never passes
// through the proxy.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/graiz/local.vibe/internal/gitwt"
)

// oauthStateTagPrefix marks a state value vibe wrapped. It is deliberately
// distinctive: an app whose own state happened to start with the prefix AND
// whose next "!"-delimited token matched a live sibling worktree name would be
// misrouted, so the sentinel is made long enough that the collision is not a
// practical concern.
const oauthStateTagPrefix = "vibewt1!"

// wrapOAuthState tags an OAuth state value with the originating route name.
func wrapOAuthState(routeName, state string) string {
	return oauthStateTagPrefix + routeName + "!" + state
}

// parseWrappedOAuthState splits a tagged state back into route name and the
// original state. ok=false when the value isn't one of ours.
func parseWrappedOAuthState(state string) (routeName, original string, ok bool) {
	if !strings.HasPrefix(state, oauthStateTagPrefix) {
		return "", "", false
	}
	rest := state[len(oauthStateTagPrefix):]
	i := strings.Index(rest, "!")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// parentBridgePort returns the parent route's oauth_callback_port for a
// worktree route, or 0 when there is none to inherit.
func (s *Server) parentBridgePort(route *Route) int {
	if route.Parent == "" {
		return 0
	}
	parent, ok := s.table.Get(route.Parent)
	if !ok {
		return 0
	}
	return parent.OAuthCallbackPort
}

// tagOAuthAuthorizeRedirect rewrites the state parameter of an outbound OAuth
// authorize redirect so the callback bridge can route the eventual callback
// back to the originating worktree. It only touches absolute 3xx Locations
// whose redirect_uri points at the bridge port — anything else passes through
// byte-identical.
func tagOAuthAuthorizeRedirect(resp *http.Response, routeName string, bridgePort int) {
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return
	}
	u, err := url.Parse(loc)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	if tagged, ok := tagAuthorizeURL(u, routeName, bridgePort); ok {
		resp.Header.Set("Location", tagged)
	}
}

// isBridgeHost reports whether host is the loopback bridge listener.
func isBridgeHost(host string, bridgePort int) bool {
	return host == fmt.Sprintf("localhost:%d", bridgePort) || host == fmt.Sprintf("127.0.0.1:%d", bridgePort)
}

// tagAuthorizeURL tags the state parameter of an OAuth authorize URL whose
// redirect_uri points at the bridge port. Returns the rewritten URL and
// whether a rewrite happened. PKCE-only flows (e.g. Auth.js v5's Google
// provider) send no state at all — providers echo state whenever present, so
// a synthetic one carrying just the route tag is injected; the bridge strips
// it before forwarding, and the app never sees a state it didn't send.
func tagAuthorizeURL(u *url.URL, routeName string, bridgePort int) (string, bool) {
	q := u.Query()
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" || strings.HasPrefix(state, oauthStateTagPrefix) {
		return "", false
	}
	ru, err := url.Parse(redirectURI)
	if err != nil || !isBridgeHost(ru.Host, bridgePort) {
		return "", false
	}
	q.Set("state", wrapOAuthState(routeName, state))
	u.RawQuery = q.Encode()
	return u.String(), true
}

// rewriteBridgeHostRedirect points a redirect that targets the bridge
// listener (Location: http://localhost:<N>/...) back at the route's own .vibe
// origin. Apps configured with AUTH_URL=http://localhost:<N> build their
// post-login redirects from it; without this rewrite the browser detours
// through the bridge, which can only guess a destination for tag-less
// requests. Applies to the parent and its worktrees alike.
func rewriteBridgeHostRedirect(resp *http.Response, scheme, vibeHost string, bridgePort int) {
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return
	}
	u, err := url.Parse(loc)
	if err != nil || !isBridgeHost(u.Host, bridgePort) {
		return
	}
	u.Scheme = scheme
	u.Host = vibeHost
	resp.Header.Set("Location", u.String())
}

// maxTaggableJSONBody bounds how much of a JSON response the tagger will
// buffer. Auth.js signin responses are tens of bytes; anything big is not an
// auth payload.
const maxTaggableJSONBody = 64 << 10

// tagAuthorizeJSONBody rewrites OAuth authorize URLs inside small JSON
// response bodies. Client-side auth flows (Auth.js signIn()) don't navigate
// via a 302 — they POST, receive {"url": "https://provider/...?..."} and
// assign window.location themselves, so the Location-header tagger never
// sees the URL. Every string value in the JSON that qualifies as a
// bridge-bound authorize URL is tagged via literal substring replacement (the
// body bytes are otherwise untouched). Returns the new body and whether
// anything changed.
func tagAuthorizeJSONBody(body []byte, routeName string, bridgePort int) ([]byte, bool) {
	if !bytes.Contains(body, []byte("redirect_uri")) {
		return body, false
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, false
	}
	tagged, changed := tagJSONValue(doc, routeName, bridgePort)
	if !changed {
		return body, false
	}
	// Re-encode from the decoded document rather than splicing text into the
	// raw bytes. Substring splicing missed any body that escaped the URL
	// (\/ or \uXXXX — the decoded value then matches nothing) and could
	// double-tag when the same URL appeared twice. Re-encoding is exact.
	// Only reached when something actually changed, so an untagged body is
	// still forwarded byte-identical.
	out, err := json.Marshal(tagged)
	if err != nil {
		return body, false
	}
	return out, true
}

// tagJSONValue walks a decoded JSON document and tags every string value that
// is a bridge-bound OAuth authorize URL, returning the (possibly mutated)
// value and whether anything changed. Maps and slices are mutated in place.
func tagJSONValue(v any, routeName string, bridgePort int) (any, bool) {
	switch t := v.(type) {
	case string:
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return v, false
		}
		if tagged, ok := tagAuthorizeURL(u, routeName, bridgePort); ok {
			return tagged, true
		}
		return v, false
	case map[string]any:
		changed := false
		for k, vv := range t {
			if nv, c := tagJSONValue(vv, routeName, bridgePort); c {
				t[k] = nv
				changed = true
			}
		}
		return t, changed
	case []any:
		changed := false
		for i, vv := range t {
			if nv, c := tagJSONValue(vv, routeName, bridgePort); c {
				t[i] = nv
				changed = true
			}
		}
		return t, changed
	}
	return v, false
}

// tagOAuthJSONResponse applies tagAuthorizeJSONBody to a proxied response
// when it plausibly carries an auth payload: 200, JSON, uncompressed, small.
// The body is fully buffered (bounded by maxTaggableJSONBody) and swapped
// back regardless of whether tagging changed it.
func tagOAuthJSONResponse(resp *http.Response, routeName string, bridgePort int) error {
	if resp.StatusCode != http.StatusOK || resp.Body == nil {
		return nil
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return nil // compressed despite the stripped Accept-Encoding — leave it
	}
	if resp.ContentLength > maxTaggableJSONBody {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTaggableJSONBody+1))
	if err != nil {
		_ = resp.Body.Close()
		return err
	}
	if len(body) > maxTaggableJSONBody {
		// Bigger than the bound (e.g. a chunked data response with unknown
		// length): not an auth payload — stitch the read prefix back onto
		// the unread remainder and pass it through byte-identical.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	if err := resp.Body.Close(); err != nil {
		return err
	}
	tagged, changed := tagAuthorizeJSONBody(body, routeName, bridgePort)
	if changed {
		body = tagged
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	return nil
}

// isLinkedWorktreeOf reports whether dir is one of the linked worktrees git
// lists for the repository at repoDir. Paths are compared resolved, since git
// reports realpaths.
func isLinkedWorktreeOf(repoDir, dir string) bool {
	wts, err := gitwt.ListLinked(repoDir)
	if err != nil {
		return false
	}
	target := realpath(dir)
	for _, wt := range wts {
		if realpath(wt.Path) == target {
			return true
		}
	}
	return false
}

// oauthEnvForRoute returns the environment bindings that pin an app's OAuth
// base URL to its callback bridge. `oauth_callback_port: N` is a declaration
// that the app's registered redirect URI is http://localhost:N, but frameworks
// that derive their base URL from the listening port (Auth.js v5 with no
// AUTH_URL set) emit the app's own auto-assigned port instead and the provider
// rejects the mismatch. Injecting the value as a real env var is what makes
// this durable: Next.js's loader does not overwrite variables already present
// in the environment, so this survives a regenerated .env (e.g. `vercel env
// pull`), which a hand-edited config file does not. Worktrees inherit the
// parent's bridge, so they get the same base URL.
func (s *Server) oauthEnvForRoute(route *Route) []string {
	port := route.OAuthCallbackPort
	if port == 0 {
		port = s.parentBridgePort(route)
	}
	if port == 0 {
		return nil
	}
	base := fmt.Sprintf("http://localhost:%d", port)
	return []string{"AUTH_URL=" + base, "NEXTAUTH_URL=" + base}
}

// prepareWorktreeEnv copies the parent checkout's untracked .env* files into
// a worktree that lacks them. `git worktree add` only carries tracked files,
// and env files are almost always gitignored — so a fresh worktree silently
// runs without credentials/config and its auth flows derive wrong URLs. Copy
// (not symlink) so a worktree can still diverge its env locally; existing
// files are never overwritten. Best-effort: failures only log.
func (s *Server) prepareWorktreeEnv(route *Route) {
	if route.Parent == "" || route.Dir == "" {
		return
	}
	parent, ok := s.table.Get(route.Parent)
	if !ok || parent.Dir == "" || realpath(parent.Dir) == realpath(route.Dir) {
		return
	}
	// Only copy into a directory git itself reports as a linked worktree of
	// the parent checkout. `Parent` is derived syntactically from the dotted
	// route name and `Dir` is caller-supplied, so without this check a route
	// named "<anything>.<app>" with an arbitrary dir would relocate the app's
	// real credentials somewhere they don't belong — including a directory
	// the app serves statically.
	if !isLinkedWorktreeOf(parent.Dir, route.Dir) {
		return
	}
	matches, err := filepath.Glob(filepath.Join(parent.Dir, ".env*"))
	if err != nil {
		return
	}
	for _, src := range matches {
		fi, err := os.Stat(src)
		if err != nil || fi.IsDir() {
			continue
		}
		dst := filepath.Join(route.Dir, filepath.Base(src))
		if _, err := os.Stat(dst); err == nil {
			continue // the worktree already has its own copy
		}
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "vibe: could not copy %s into worktree %s: %v\n", filepath.Base(src), route.Name, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "vibe: copied %s from %s into worktree %s\n", filepath.Base(src), route.Parent, route.Name)
	}
}
