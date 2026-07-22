package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// vibeJSONFields mirrors the subset of vibe.json the CLI's register flow
// supplies. The daemon re-reads it from a managed route's Dir on every Start
// so that edits to oauth_callback_port, reserve_ports, or cmd take effect
// without requiring the user to deregister and re-register the route.
//
// Fields not in this struct (icon, idle_timeout) aren't touched here — they're
// owned by the dashboard CRUD flow today, not vibe.json.
type vibeJSONFields struct {
	Name              string         `json:"name"`
	Port              int            `json:"port"`
	Cmd               string         `json:"cmd"`
	OAuthCallbackPort *int           `json:"oauth_callback_port,omitempty"`
	ReservePorts      map[string]int `json:"reserve_ports,omitempty"`
}

// syncRouteFromVibeJSON re-reads vibe.json from the route's Dir (if any) and
// applies oauth_callback_port, reserve_ports, and cmd updates to the in-memory
// route. Validates collisions and reconciles the OAuth bridge listeners when
// the callback port changes. Persists routes.json on success.
//
// No-ops when Dir is empty, vibe.json is missing, or the file's `name` field
// disagrees with the route — that mismatch usually means the user is starting
// the wrong route from this directory and we'd rather not silently retag fields
// from a stranger's config onto this route.
//
// Returns an error only for actively invalid edits the daemon should refuse
// (collision, malformed reserve_ports). A missing or unreadable file is a
// silent no-op so a route that lost its source dir still starts from the
// last-known config.
func (s *Server) syncRouteFromVibeJSON(route *Route) error {
	if route.Type != RouteManaged || route.Dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(route.Dir, "vibe.json"))
	if err != nil {
		return nil
	}
	var cfg vibeJSONFields
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Malformed config shouldn't block start — the user can keep the
		// old in-memory copy running while they fix the file.
		fmt.Fprintf(os.Stderr, "vibe: %s vibe.json is invalid, using last-known config: %v\n", route.Name, err)
		return nil
	}
	if route.Parent != "" {
		// Worktree route: the file is a copy of the parent's vibe.json, so
		// its name identifies the app, and its port/oauth/reserve values are
		// the parent's — never re-import them over the worktree-local
		// assignments. Only cmd edits sync.
		if cfg.Name != "" && cfg.Name != route.Parent {
			return nil
		}
		if cfg.Cmd == "" || cfg.Cmd == route.Cmd {
			return nil
		}
		if !s.table.UpdateManagedConfig(route.Name, cfg.Cmd, route.OAuthCallbackPort, route.ReservePorts) {
			return nil
		}
		return s.saveStickyRoutes()
	}
	if cfg.Name != "" && cfg.Name != route.Name {
		// Don't cross-pollinate fields between routes. The user is probably
		// in the wrong directory.
		return nil
	}

	// Compute the candidate new state. Validate it before mutating the route
	// so a bad edit leaves the route bootable from its last-known config.
	newCmd := cfg.Cmd
	if newCmd == "" {
		newCmd = route.Cmd
	}
	newOAuth := route.OAuthCallbackPort
	if cfg.OAuthCallbackPort != nil {
		newOAuth = *cfg.OAuthCallbackPort
	}
	newReserve, err := validateReservePorts(cfg.ReservePorts, route.Port, newOAuth)
	if err != nil {
		return err
	}
	if newOAuth > 0 {
		if err := s.validateOAuthBridgeConfig(route.Name, route.Port, newOAuth); err != nil {
			return err
		}
	}
	if msg := s.reservePortsClaim(route.Name, newReserve); msg != "" {
		return fmt.Errorf("%s", msg)
	}

	changed := newCmd != route.Cmd ||
		newOAuth != route.OAuthCallbackPort ||
		!reflect.DeepEqual(newReserve, route.ReservePorts)
	if !changed {
		return nil
	}
	// Mutate under the table lock so concurrent readers (monitor, dashboard,
	// routeRequest) never see a torn write of these plain struct fields.
	if !s.table.UpdateManagedConfig(route.Name, newCmd, newOAuth, newReserve) {
		return nil
	}

	if err := s.reconcileOAuthBridgeListeners(); err != nil {
		return fmt.Errorf("oauth bridge reconcile: %w", err)
	}
	return s.saveStickyRoutes()
}
