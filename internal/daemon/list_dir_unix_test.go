//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// localDetailsAllowed recognizes the CLI's unix-socket transport by its
// RemoteAddr not being ip:port. Pin that against a real socket rather than a
// faked RemoteAddr, since it is what the CLI actually exercises.
func TestListRoutesDirOverRealUnixSocket(t *testing.T) {
	s := listTestServer()
	s.table.Add(&Route{Name: "app", Port: 3000, Type: RouteManaged, Cmd: "node server.js", Dir: "/work/app"})

	// Short path: macOS caps sun_path at 104 bytes and t.TempDir() is long.
	dir, err := os.MkdirTemp("/tmp", "vls")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.apiHandler)}
	go srv.Serve(ln)
	defer srv.Close()

	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := c.Get("http://local/_api/routes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var routes []routeResponse
	if err := json.NewDecoder(resp.Body).Decode(&routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Dir != "/work/app" || routes[0].Cmd != "node server.js" {
		t.Errorf("unix socket listing = %+v; want dir and cmd included", routes)
	}
}
