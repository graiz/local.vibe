package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func testPortFromURL(t *testing.T, raw string) int {
	t.Helper()
	u, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	port, err := strconv.Atoi(u.URL.Port())
	if err != nil {
		t.Fatalf("parse port from %q: %v", raw, err)
	}
	return port
}

func TestRouteRequestNoOAuthFallbackRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("proxied"))
	}))
	defer upstream.Close()

	s := testServer()
	port := testPortFromURL(t, upstream.URL)
	s.table.Add(&Route{
		Name:         "screener",
		Port:         port,
		Type:         RouteSticky,
		RegisteredAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/auth/google", nil)
	req.Host = "screener.test"
	w := httptest.NewRecorder()
	s.routeRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if got := w.Body.String(); got != "proxied" {
		t.Errorf("body = %q; want proxied", got)
	}
}

func TestOAuthCallbackBridgeListenerRedirectsToVibeHost(t *testing.T) {
	s := testServer()
	defer s.stopOAuthBridgeListeners()

	appPort := pickFreePort(t)
	callbackPort := pickFreePort(t)

	body, _ := json.Marshal(map[string]any{
		"name":                "screener",
		"port":                appPort,
		"oauth_callback_port": callbackPort,
	})
	req := httptest.NewRequest(http.MethodPost, "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status = %d; body: %s", w.Code, w.Body.String())
	}

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get("http://localhost:" + strconv.Itoa(callbackPort) + "/auth/google/callback?code=abc&state=xyz")
	if err != nil {
		t.Fatalf("GET localhost callback bridge: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d; want 307", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "http://screener.test/auth/google/callback?code=abc&state=xyz" {
		t.Errorf("Location = %q; want redirect to screener.test callback", got)
	}
}

func TestAPIRejectsOAuthCallbackPortMatchingRoutePort(t *testing.T) {
	s := testServer()

	body, _ := json.Marshal(map[string]any{
		"name":                "screener",
		"port":                8787,
		"oauth_callback_port": 8787,
	})
	req := httptest.NewRequest(http.MethodPost, "/_api/routes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body: %s", w.Code, w.Body.String())
	}
}
