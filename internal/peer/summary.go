package peer

// RouteSummary is the wire shape of one route in GET /peer/routes — the
// read-only subset a paired machine is allowed to see. No dirs or commands
// cross the wire: a peer needs the name (to resolve), liveness (to display),
// and icon (to render). The one port that crosses is OAuthCallbackPort: an
// OAuth provider redirects the *viewer's* browser to http://localhost:<N>, so
// the viewing machine must run its own bridge on N (gated there by a
// confirmation page). The app's listening port never leaves the machine.
type RouteSummary struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	Running           bool   `json:"running"`
	Ready             bool   `json:"ready"`
	Icon              string `json:"icon,omitempty"`
	OAuthCallbackPort int    `json:"oauth_callback_port,omitempty"`
}

// Status words for a peer route, shared by the dashboard and `vibe list` so
// both surfaces say the same thing about the same route.
const (
	StatusReady       = "ready"
	StatusStarting    = "starting"
	StatusStopped     = "stopped"
	StatusUnreachable = "unreachable"
	// StatusShadowed is a peer route whose name a local route (or an
	// earlier-paired peer) already serves. Its own run state on the peer is
	// irrelevant here — the name never reaches it — and showing it ("stopped"
	// beside a local route that is running) read as the route being down.
	StatusShadowed = "shadowed"
)

// Status reports a peer route's liveness as one word. reachable is whether
// the peer's last sync is recent: when it isn't, the cached flags are stale
// and claiming "running" (or "stopped") would be a guess.
func Status(running, ready, reachable bool) string {
	switch {
	case !reachable:
		return StatusUnreachable
	case ready:
		return StatusReady
	case running:
		return StatusStarting
	default:
		return StatusStopped
	}
}
