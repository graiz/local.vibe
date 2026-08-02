package daemon

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// upstreamWebSocketEcho stands in for a dev server's hot-reload endpoint: it
// completes an Upgrade handshake by hand (no external websocket dependency),
// then echoes one line back over the raw tunnel. Enough to prove the proxy
// establishes a bidirectional connection rather than just returning 101.
func upstreamWebSocketEcho(t *testing.T) (port int, stop func()) {
	t.Helper()
	mux := http.NewServeMux()
	// selfPort is filled in once the listener exists. The gate below compares
	// Origin against the dev server's OWN listening address, not against Host
	// — that is what Next.js actually does, and why a request can carry
	// Host: app.vibe with Origin: http://localhost:<port> and be accepted.
	selfPort := 0
	// Mirrors Next.js 15.2+ allowedDevOrigins: an upgrade whose Origin is not
	// the dev server's own is answered with HTML instead of 101, which is what
	// makes the browser's socket close 1006 and the page reload-loop.
	mux.HandleFunc("/hmr-origin-gated", func(w http.ResponseWriter, r *http.Request) {
		ownOrigins := map[string]bool{
			fmt.Sprintf("http://localhost:%d", selfPort): true,
			fmt.Sprintf("http://127.0.0.1:%d", selfPort): true,
		}
		if o := r.Header.Get("Origin"); o != "" && !ownOrigins[o] {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "<html>cross-origin dev request blocked (origin=%s)</html>", o)
			return
		}
		// Accepted: complete a real upgrade by hijacking, echoing back the
		// browser's original origin so the test can assert it survived.
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
			"X-Saw-Forwarded-Origin: %s\r\n\r\n", r.Header.Get("X-Forwarded-Origin"))
		buf.Flush()
		// Hold the tunnel open briefly so the client can read the handshake.
		_, _ = buf.ReadString('\n')
	})
	// Echoes back the Origin the upstream actually received, for a plain
	// (non-upgrade) request.
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Origin"))
	})
	mux.HandleFunc("/_next/webpack-hmr", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(buf, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n")
		buf.Flush()
		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		fmt.Fprintf(buf, "echo:%s", line)
		buf.Flush()
	})
	srv := httptest.NewServer(mux)
	addr := srv.Listener.Addr().(*net.TCPAddr)
	selfPort = addr.Port
	return addr.Port, srv.Close
}

// TestProxyTunnelsWebSocketUpgrade is the regression test for the Next.js dev
// reload loop: the HMR socket (wss://<route>.vibe/_next/webpack-hmr) must be
// tunneled through the reverse proxy. When the upgrade fails the dev client
// retries, then calls location.reload() after three failures — a full page
// reload every ~20s that silently destroys client state.
func TestProxyTunnelsWebSocketUpgrade(t *testing.T) {
	upstreamPort, stopUpstream := upstreamWebSocketEcho(t)
	defer stopUpstream()

	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name:         "app",
		Type:         RouteSticky,
		Port:         upstreamPort,
		RegisteredAt: time.Now(),
	})

	front := httptest.NewServer(http.HandlerFunc(s.routeRequest))
	defer front.Close()

	conn, err := net.Dial("tcp", front.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	fmt.Fprint(conn, "GET /_next/webpack-hmr?id=abc HTTP/1.1\r\n"+
		"Host: app.test\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade not tunneled: got status %q, want 101 Switching Protocols", strings.TrimSpace(status))
	}
	// Drain headers.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	// Prove the tunnel carries data in both directions after the handshake.
	fmt.Fprint(conn, "ping\n")
	reply, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("tunnel closed before echo (the 1006 symptom): %v", err)
	}
	if strings.TrimSpace(reply) != "echo:ping" {
		t.Fatalf("tunnel payload = %q, want %q", strings.TrimSpace(reply), "echo:ping")
	}
}

// TestProxyRewritesOriginOnUpgrade is the regression test for the Next.js dev
// reload loop's actual cause: the proxy tunnels the upgrade correctly, but the
// upstream rejects it because the browser's Origin is the .vibe host rather
// than the dev server's own. The proxy must present the upstream with its own
// origin on upgrade requests, preserving the browser's in X-Forwarded-Origin.
func TestProxyRewritesOriginOnUpgrade(t *testing.T) {
	upstreamPort, stopUpstream := upstreamWebSocketEcho(t)
	defer stopUpstream()

	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name: "app", Type: RouteSticky, Port: upstreamPort, RegisteredAt: time.Now(),
	})
	front := httptest.NewServer(http.HandlerFunc(s.routeRequest))
	defer front.Close()

	conn, err := net.Dial("tcp", front.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	fmt.Fprint(conn, "GET /hmr-origin-gated HTTP/1.1\r\n"+
		"Host: app.test\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: keep-alive, Upgrade\r\n"+ // multi-token, as browsers send
		"Origin: https://app.test\r\n"+
		"Sec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("upstream rejected the upgrade (%q) — the browser would see 1006 and "+
			"reload-loop; Origin was not rewritten to the upstream's own", strings.TrimSpace(status))
	}
	var forwarded string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "X-Saw-Forwarded-Origin") {
			forwarded = strings.TrimSpace(v)
		}
	}
	if forwarded != "https://app.test" {
		t.Errorf("X-Forwarded-Origin seen upstream = %q, want the browser's original %q",
			forwarded, "https://app.test")
	}
}

// TestProxyLeavesOriginAloneOnNormalRequests pins the scope of the rewrite.
// Ordinary requests must keep the browser's Origin: apps compare it against
// their configured URL for CSRF (Auth.js does this on every POST), so
// rewriting it there would break logins that work today.
func TestProxyLeavesOriginAloneOnNormalRequests(t *testing.T) {
	upstreamPort, stopUpstream := upstreamWebSocketEcho(t)
	defer stopUpstream()

	s := testServer()
	s.ConfigDir = t.TempDir()
	s.table.Add(&Route{
		Name: "app", Type: RouteSticky, Port: upstreamPort, RegisteredAt: time.Now(),
	})
	front := httptest.NewServer(http.HandlerFunc(s.routeRequest))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/plain", nil)
	req.Host = "app.test"
	req.Header.Set("Origin", "https://app.test")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body := make([]byte, 128)
	n, _ := resp.Body.Read(body)

	if got := string(body[:n]); got != "https://app.test" {
		t.Errorf("upstream saw Origin %q on a normal request, want the browser's %q unchanged",
			got, "https://app.test")
	}
}
