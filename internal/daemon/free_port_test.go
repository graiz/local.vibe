package daemon

import (
	"fmt"
	"net"
	"testing"
)

// findFreePort must not hand out a port another server already listens on
// via a wildcard or IPv6 address. Its 127.0.0.1 bind probe succeeds beside
// those (SO_REUSEADDR), and the start preflight then killed the holder — in
// this test suite's case, the developer's real dev server.
func TestFindFreePortSkipsIPv6AndWildcardListeners(t *testing.T) {
	for _, family := range []string{"[::1]", "[::]", ""} {
		table := NewRouteTable()
		p, err := findFreePort(table)
		if err != nil {
			t.Fatal(err)
		}
		holder, err := net.Listen("tcp", fmt.Sprintf("%s:%d", family, p))
		if err != nil {
			t.Skipf("cannot listen on %s:%d: %v", family, p, err)
		}
		got, err := findFreePort(table)
		holder.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got == p {
			t.Errorf("findFreePort returned %d while a server held %s:%d", p, family, p)
		}
	}
}
