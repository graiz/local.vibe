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
	cfg.Daemon.Port = 0
	cfg.Daemon.TLS.Enabled = false
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
	cfg.Daemon.Port = 0
	s := NewServer(cfg)
	s.ConfigDir = t.TempDir()
	s.loadPeerSubsystem()
	if s.peerLn != nil {
		t.Fatal("peer listener started with the flag off")
	}
}
