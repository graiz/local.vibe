//go:build windows

package cmd

import (
	"net"
	"strings"
	"time"
)

// publicResolverFallbacks is the ordered list of public DNS resolvers we try
// when the user's adapter DNS isn't reachable. Order matters: 1.1.1.1 first
// because Cloudflare is fastest in the most regions and tends to be less
// often blocked than 8.8.8.8 in corporate networks.
var publicResolverFallbacks = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
	"9.9.9.9:53",
}

// pickUpstreamResolver returns the first reachable resolver from the
// snapshotted adapter DNS, falling back to the public list. Reachability is
// tested by a short UDP query for "dns.google." — any well-formed reply
// counts as a working resolver.
//
// Returned in netstat-style host:port form so it can be written straight
// into config.json's daemon.dns.upstream field.
func pickUpstreamResolver(snap map[string]adapterDNS) string {
	candidates := candidateUpstreams(snap)
	for _, c := range candidates {
		if probeUDPResolver(c, 600*time.Millisecond) {
			return c
		}
	}
	// Last resort: hand back the first public fallback so the daemon at least
	// has a syntactically-valid upstream. If that's also unreachable the user
	// will see resolution failures, but we've left them no worse off than the
	// pre-fix hardcoded default.
	return publicResolverFallbacks[0]
}

// candidateUpstreams orders snapshot servers ahead of public fallbacks,
// dedupes, and skips loopback (127.0.0.1 was almost certainly us from a
// prior failed setup).
func candidateUpstreams(snap map[string]adapterDNS) []string {
	seen := map[string]bool{}
	var out []string
	add := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" || host == "127.0.0.1" {
			return
		}
		hp := host
		if !strings.Contains(hp, ":") {
			hp = host + ":53"
		}
		if seen[hp] {
			return
		}
		seen[hp] = true
		out = append(out, hp)
	}
	for _, entry := range snap {
		for _, srv := range entry.Servers {
			add(srv)
		}
	}
	for _, p := range publicResolverFallbacks {
		add(p)
	}
	return out
}

// probeUDPResolver sends a small DNS query for "dns.google." (A record) to
// the given host:port and returns true if any response arrives within the
// timeout. We don't validate the answer — we just want to know the resolver
// is alive and routes back to us.
func probeUDPResolver(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// Hand-rolled query for "dns.google." A record. Easier than pulling in
	// miekg/dns just for a liveness probe.
	query := []byte{
		0xab, 0xcd, // ID
		0x01, 0x00, // flags: standard recursive query
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// QNAME: dns.google
		3, 'd', 'n', 's',
		6, 'g', 'o', 'o', 'g', 'l', 'e',
		0,          // root label terminator
		0x00, 0x01, // QTYPE=A
		0x00, 0x01, // QCLASS=IN
	}
	if _, err := conn.Write(query); err != nil {
		return false
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	return err == nil && n > 0
}
