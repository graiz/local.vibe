package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/graiz/local.vibe/internal/config"
)

// Client talks to the vibe daemon via Unix socket (preferred) or TCP fallback.
type Client struct {
	http    *http.Client
	baseURL string
}

// Per-request deadlines. Reads are quick; a register or start legitimately
// takes seconds (the daemon watches the child for a second, preflights each
// port with a 500ms settle, and may run git/lsof), so mutations get more.
// The socket probe in New is the one that must fail fast.
const (
	readTimeout   = 5 * time.Second
	mutateTimeout = 30 * time.Second
	probeTimeout  = 2 * time.Second
)

// New creates a Client, preferring the Unix socket for communication.
// Falls back to TCP (127.0.0.1:<default port>) if the socket is unavailable.
func New() *Client {
	home, _ := os.UserHomeDir()
	sock := filepath.Join(home, ".vibe", "vibe.sock")

	sockClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
		// Redirects are never followed: a 303 to https://local.vibe/ cannot be
		// chased over the socket transport, and no daemon response the CLI
		// wants is behind one.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	c := &Client{http: sockClient, baseURL: "http://local"}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if _, _, err := c.doCtx(ctx, "GET", "/_api/health", nil); err == nil {
		return c
	}

	return &Client{
		http: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", tcpPort()),
	}
}

// tcpPort is the daemon's HTTP port as configured in ~/.vibe/config.json,
// falling back to the default. TCP is the only transport on Windows, so a
// hardcoded default would cut the CLI off from a daemon on a custom port.
func tcpPort() int {
	if cfg, err := config.Load(); err == nil && cfg.Daemon.Port > 0 {
		return cfg.Daemon.Port
	}
	return config.DefaultPort
}

func (c *Client) do(method, path string, body any) ([]byte, int, error) {
	timeout := readTimeout
	if method != http.MethodGet {
		timeout = mutateTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.doCtx(ctx, method, path, body)
}

func (c *Client) doCtx(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Daemon's start/stop handlers 303-redirect to the dashboard when this
	// header is missing — that redirect points at https://local.vibe/, which
	// the Unix-socket transport cannot follow (TLS over unix conn fails),
	// surfacing as a spurious "daemon not running" error.
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A deadline is the daemon being slow (or wedged), not absent — the
		// route it was starting may well be up. Say so instead of sending
		// the user to start a daemon that is running.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, fmt.Errorf("the daemon did not answer within %s — check `vibe status` and ~/.vibe/daemon.log", timeoutOf(ctx))
		}
		return nil, 0, fmt.Errorf("daemon not running — start it with: vibe daemon start")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// RegisterRequest mirrors the daemon API body.
type RegisterRequest struct {
	Name              string         `json:"name"`
	Port              int            `json:"port"`
	PID               *int           `json:"pid,omitempty"`
	TTL               *int           `json:"ttl,omitempty"`
	Cmd               string         `json:"cmd,omitempty"`
	Dir               string         `json:"dir,omitempty"`
	OAuthCallbackPort *int           `json:"oauth_callback_port,omitempty"`
	ReservePorts      map[string]int `json:"reserve_ports,omitempty"`
}

type RegisterResponse struct {
	OK   bool   `json:"ok"`
	URL  string `json:"url"`
	Port int    `json:"port,omitempty"`
}

type RouteInfo struct {
	Name              string     `json:"name"`
	Port              int        `json:"port"`
	PID               *int       `json:"pid,omitempty"`
	RegisteredAt      time.Time  `json:"registered_at"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	Type              string     `json:"type"`
	Running           bool       `json:"running"`
	Ready             bool       `json:"ready"`
	URL               string     `json:"url"`
	OAuthCallbackPort int        `json:"oauth_callback_port,omitempty"`
	Cmd               string     `json:"cmd,omitempty"`
	Dir               string     `json:"dir,omitempty"`
}

// WorktreeInfo is an on-disk git worktree of a managed app that has no route
// yet. Surfaced so `vibe list` can show worktrees you have created but never
// started — previously reachable only by stopping the parent app and visiting
// its picker.
type WorktreeInfo struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
	URL    string `json:"url"`
}

// Worktrees lists discovered-but-unregistered worktrees across all apps.
// Best-effort: an older daemon has no such endpoint, so a non-200 yields an
// empty list rather than an error — this decorates `vibe list`, it should
// never break it.
func (c *Client) Worktrees() []WorktreeInfo {
	data, status, err := c.do("GET", "/_api/worktrees", nil)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var wts []WorktreeInfo
	if json.Unmarshal(data, &wts) != nil {
		return nil
	}
	return wts
}

type HealthResponse struct {
	Status string `json:"status"`
	Routes int    `json:"routes"`
	Uptime int    `json:"uptime"`
}

func (c *Client) Register(req RegisterRequest) (*RegisterResponse, error) {
	data, status, err := c.do("POST", "/_api/routes", req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(data, status)
	}
	var resp RegisterResponse
	return &resp, json.Unmarshal(data, &resp)
}

func (c *Client) Deregister(name string) error {
	data, status, err := c.do("DELETE", "/_api/routes/"+name, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("no route named %q", name)
	}
	if status != http.StatusOK {
		return apiError(data, status)
	}
	return nil
}

func (c *Client) List() ([]RouteInfo, error) {
	data, status, err := c.do("GET", "/_api/routes", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(data, status)
	}
	var routes []RouteInfo
	return routes, json.Unmarshal(data, &routes)
}

func (c *Client) Start(name string) error {
	data, status, err := c.do("POST", "/_api/routes/"+name+"/start", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(data, status)
	}
	return nil
}

func (c *Client) Stop(name string) error {
	data, status, err := c.do("DELETE", "/_api/routes/"+name+"/stop", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(data, status)
	}
	return nil
}

func (c *Client) Health() (*HealthResponse, error) {
	data, status, err := c.do("GET", "/_api/health", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(data, status)
	}
	var h HealthResponse
	return &h, json.Unmarshal(data, &h)
}

// PeerRouteInfo is one route in a paired peer's cached route list.
type PeerRouteInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
}

// PeerInfo is one paired peer daemon with its sync health and routes.
type PeerInfo struct {
	Name        string          `json:"name"`
	Host        string          `json:"host"`
	Port        int             `json:"port"`
	Fingerprint string          `json:"fingerprint"`
	AddedAt     time.Time       `json:"added_at"`
	Reachable   bool            `json:"reachable"`
	LastError   string          `json:"last_error,omitempty"`
	Routes      []PeerRouteInfo `json:"routes"`
	// OAuthBridges are localhost ports this machine holds for the peer's
	// OAuth callbacks.
	OAuthBridges []PeerBridgeInfo `json:"oauth_bridges,omitempty"`
}

type PeerBridgeInfo struct {
	Port  int    `json:"port"`
	Route string `json:"route"`
	Bound bool   `json:"bound"`
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

// Peers lists paired peers and their cached routes. Callers that only
// decorate output (vibe list) should treat an error as "no peers" — like
// Worktrees, this must never break the primary command.
func (c *Client) Peers() (*PeersResponse, error) {
	data, status, err := c.do("GET", "/_api/peers", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(data, status)
	}
	var resp PeersResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PeerInvite opens a one-time pairing window on this machine's daemon.
func (c *Client) PeerInvite() (*PeerInviteResponse, error) {
	data, status, err := c.do("POST", "/_api/peers/invite", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(data, status)
	}
	var resp PeerInviteResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PeerAdd pairs this machine's daemon with host using an invite code shown
// by `vibe peer invite` on the other machine.
func (c *Client) PeerAdd(host string, port int, code string) (*PeerInfo, error) {
	body := map[string]any{"host": host, "port": port, "code": code}
	data, status, err := c.do("POST", "/_api/peers", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(data, status)
	}
	var resp PeerInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PeerRemove unpairs a peer by name.
func (c *Client) PeerRemove(name string) error {
	data, status, err := c.do("DELETE", "/_api/peers/"+name, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(data, status)
	}
	return nil
}

// timeoutOf renders a context's remaining-or-original deadline for messages.
func timeoutOf(ctx context.Context) string {
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			return d.Round(time.Second).String()
		}
	}
	return "the timeout"
}

// apiError surfaces the daemon's JSON error message when present.
func apiError(data []byte, status int) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	return fmt.Errorf("daemon returned %d", status)
}
