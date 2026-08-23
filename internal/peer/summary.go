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
