package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Client talks to the vibe daemon via Unix socket (preferred) or TCP fallback.
type Client struct {
	http    *http.Client
	baseURL string
}

// New creates a Client, preferring the Unix socket for communication.
// Falls back to TCP (127.0.0.1:7999) if the socket is unavailable.
func New() *Client {
	home, _ := os.UserHomeDir()
	sock := filepath.Join(home, ".vibe", "vibe.sock")

	sockClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
		Timeout: 5 * time.Second,
	}

	c := &Client{http: sockClient, baseURL: "http://local"}
	if _, err := c.Health(); err == nil {
		return c
	}

	return &Client{
		http:    &http.Client{Timeout: 5 * time.Second},
		baseURL: "http://127.0.0.1:7999",
	}
}

func (c *Client) do(method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("daemon not running — start it with: vibe daemon start")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// RegisterRequest mirrors the daemon API body.
type RegisterRequest struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	PID  *int   `json:"pid,omitempty"`
	TTL  *int   `json:"ttl,omitempty"`
	Cmd  string `json:"cmd,omitempty"`
	Dir  string `json:"dir,omitempty"`
}

type RegisterResponse struct {
	OK   bool   `json:"ok"`
	URL  string `json:"url"`
	Port int    `json:"port,omitempty"`
}

type RouteInfo struct {
	Name         string     `json:"name"`
	Port         int        `json:"port"`
	PID          *int       `json:"pid,omitempty"`
	RegisteredAt time.Time  `json:"registered_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Type         string     `json:"type"`
	Running      bool       `json:"running"`
	Ready        bool       `json:"ready"`
	URL          string     `json:"url"`
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
		return nil, fmt.Errorf("%s", data)
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
		return fmt.Errorf("%s", data)
	}
	return nil
}

func (c *Client) List() ([]RouteInfo, error) {
	data, status, err := c.do("GET", "/_api/routes", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", data)
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
		return fmt.Errorf("%s", data)
	}
	return nil
}

func (c *Client) Stop(name string) error {
	data, status, err := c.do("DELETE", "/_api/routes/"+name+"/stop", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", data)
	}
	return nil
}

func (c *Client) Health() (*HealthResponse, error) {
	data, status, err := c.do("GET", "/_api/health", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", data)
	}
	var h HealthResponse
	return &h, json.Unmarshal(data, &h)
}
