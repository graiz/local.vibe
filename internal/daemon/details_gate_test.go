package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A route's failure record carries its log tail and recovery hints that name
// its command line and working directory. The start/repair pages on the
// route's own host need it; a proxy bookmark's third-party JS on another .vibe
// host (same-origin to the API, since every host answers /_api/) must not get
// it — nor may a DNS-rebound name.
func TestFailureDetailsOnlyForOwnHostOrLocal(t *testing.T) {
	s := testServer(t)
	s.ConfigDir = t.TempDir()
	route := &Route{Name: "dead", Port: 9, Type: RouteManaged, Cmd: "secret-token-cmd", RegisteredAt: time.Now()}
	route.SetFailure(&Failure{Message: "boom", Log: "TOKEN=abc"})
	s.table.Add(route)

	get := func(method, path, host string) map[string]any {
		req := httptest.NewRequest(method, path, nil)
		req.Host = host
		req.RemoteAddr = "127.0.0.1:50000"
		w := httptest.NewRecorder()
		s.apiHandler(w, req)
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp
	}

	for _, path := range []string{"/_api/routes/dead/ready", "/_api/routes/dead/repair"} {
		method := http.MethodGet
		if path == "/_api/routes/dead/repair" {
			method = http.MethodPost
		}
		if _, has := get(method, path, "dead.test")["failure"]; !has {
			t.Errorf("%s on the route's own host omits the failure", path)
		}
		if _, has := get(method, path, "local.test")["failure"]; !has {
			t.Errorf("%s on the dashboard host omits the failure", path)
		}
		if _, has := get(method, path, "other.test")["failure"]; has {
			t.Errorf("%s on another route's host leaks the failure", path)
		}
		if _, has := get(method, path, "rebound.example.com")["failure"]; has {
			t.Errorf("%s on a foreign host leaks the failure", path)
		}
	}
}
