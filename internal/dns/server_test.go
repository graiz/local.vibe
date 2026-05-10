package dns

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pickFreeUDPPort asks the OS for an unused UDP port.
func pickFreeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}

// startTestServer runs a Server on 127.0.0.1:<random> with no upstream
// (forwards will fail) and returns its address. Tests that exercise the
// upstream path supply their own server via a custom Config.
func startTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.Listen == "" {
		port := pickFreeUDPPort(t)
		cfg.Listen = "127.0.0.1:" + strconv.Itoa(port)
	}
	if cfg.TLD == "" {
		cfg.TLD = "vibe"
	}
	if cfg.UpstreamTimeout == 0 {
		cfg.UpstreamTimeout = 200 * time.Millisecond
	}
	srv := New(cfg)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

// askUDP sends a single DNS query and returns the raw response bytes.
func askUDP(t *testing.T, addr net.Addr, query []byte) []byte {
	t.Helper()
	conn, err := net.Dial("udp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(query); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf[:n]
}

// buildQuery builds a minimal DNS query for (name, qtype) with id=0x1234,
// recursion-desired flag set.
func buildQuery(name string, qtype uint16) []byte {
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q[0:2], 0x1234) // ID
	binary.BigEndian.PutUint16(q[2:4], 0x0100) // Flags: standard query, RD
	binary.BigEndian.PutUint16(q[4:6], 1)      // QDCOUNT
	for _, label := range strings.Split(name, ".") {
		q = append(q, byte(len(label)))
		q = append(q, []byte(label)...)
	}
	q = append(q, 0)                                                              // null terminator
	q = append(q, byte(qtype>>8), byte(qtype&0xff), byte(classIN>>8), byte(classIN&0xff)) // QTYPE, QCLASS
	return q
}

func TestNameEndsInTLD(t *testing.T) {
	cases := []struct {
		name string
		tld  string
		want bool
	}{
		{"foo.vibe", "vibe", true},
		{"FOO.VIBE", "vibe", true},
		{"vibe", "vibe", true},
		{"foo.bar.vibe", "vibe", true},
		{"foo.viber", "vibe", false},
		{"vibevibe", "vibe", false},
		{"google.com", "vibe", false},
	}
	for _, c := range cases {
		got := nameEndsInTLD(c.name, c.tld)
		if got != c.want {
			t.Errorf("nameEndsInTLD(%q, %q) = %v; want %v", c.name, c.tld, got, c.want)
		}
	}
}

func TestServerAnswersVibeAQuery(t *testing.T) {
	srv := startTestServer(t, Config{TLD: "vibe", Upstream: "127.0.0.1:1"}) // upstream points nowhere
	resp := askUDP(t, srv.Listen(), buildQuery("foo.vibe", typeA))
	if len(resp) < 12 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	flags := binary.BigEndian.Uint16(resp[2:4])
	if flags&flagResponse == 0 {
		t.Errorf("QR bit not set")
	}
	if rcode := flags & 0x000F; rcode != 0 {
		t.Errorf("rcode = %d; want 0 (NOERROR)", rcode)
	}
	ancount := binary.BigEndian.Uint16(resp[6:8])
	if ancount != 1 {
		t.Fatalf("ANCOUNT = %d; want 1", ancount)
	}
	// Last 4 bytes of the response should be the A record's RDATA: 127.0.0.1.
	if got := resp[len(resp)-4:]; got[0] != 127 || got[1] != 0 || got[2] != 0 || got[3] != 1 {
		t.Errorf("RDATA = %v; want 127.0.0.1", got)
	}
}

func TestServerAAAAQueryReturnsEmpty(t *testing.T) {
	srv := startTestServer(t, Config{TLD: "vibe", Upstream: "127.0.0.1:1"})
	resp := askUDP(t, srv.Listen(), buildQuery("foo.vibe", typeAAAA))
	if len(resp) < 12 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	flags := binary.BigEndian.Uint16(resp[2:4])
	if flags&flagResponse == 0 {
		t.Errorf("QR bit not set")
	}
	if rcode := flags & 0x000F; rcode != 0 {
		t.Errorf("rcode = %d; want 0 (NOERROR — empty answer for AAAA)", rcode)
	}
	if ancount := binary.BigEndian.Uint16(resp[6:8]); ancount != 0 {
		t.Errorf("ANCOUNT = %d; want 0 (no AAAA answers)", ancount)
	}
}

func TestServerForwardsNonVibeQueriesToUpstream(t *testing.T) {
	// Stand up a fake upstream that always answers "8.8.4.4" to any A query.
	upConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream listen: %v", err)
	}
	defer upConn.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			_ = upConn.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, src, err := upConn.ReadFrom(buf)
			if err != nil {
				return
			}
			// Build a response: copy the question, set QR + RA, append one A
			// answer with 8.8.4.4.
			resp := make([]byte, n)
			copy(resp, buf[:n])
			flags := binary.BigEndian.Uint16(resp[2:4])
			flags |= flagResponse | flagRecursionAvail
			binary.BigEndian.PutUint16(resp[2:4], flags)
			binary.BigEndian.PutUint16(resp[6:8], 1) // ANCOUNT
			ans := []byte{
				0xC0, 0x0C,
				0x00, byte(typeA),
				0x00, byte(classIN),
				0x00, 0x00, 0x00, 0x3C,
				0x00, 0x04,
				8, 8, 4, 4,
			}
			resp = append(resp, ans...)
			_, _ = upConn.WriteTo(resp, src)
		}
	}()

	srv := startTestServer(t, Config{
		TLD:      "vibe",
		Upstream: upConn.LocalAddr().String(),
	})
	resp := askUDP(t, srv.Listen(), buildQuery("example.com", typeA))
	if len(resp) < 16 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	got := resp[len(resp)-4:]
	if got[0] != 8 || got[1] != 8 || got[2] != 4 || got[3] != 4 {
		t.Errorf("RDATA = %v; want 8.8.4.4 (forwarded answer)", got)
	}
}

func TestServerServfailWhenUpstreamUnreachable(t *testing.T) {
	srv := startTestServer(t, Config{
		TLD: "vibe",
		// Reserved test/discard address; should never answer.
		Upstream:        "127.0.0.1:1",
		UpstreamTimeout: 50 * time.Millisecond,
	})
	resp := askUDP(t, srv.Listen(), buildQuery("example.com", typeA))
	if len(resp) < 12 {
		t.Fatalf("response too short")
	}
	rcode := binary.BigEndian.Uint16(resp[2:4]) & 0x000F
	if rcode != rcodeServFail {
		t.Errorf("rcode = %d; want %d (SERVFAIL)", rcode, rcodeServFail)
	}
}

// TestServfailIsWellFormed locks down the response shape — the header's
// QDCOUNT must agree with the body. Either the question section is echoed
// (length matches header offset + question bytes), or QDCOUNT is zero.
// Strict resolvers reject responses where QDCOUNT claims a question but
// the body is too short to contain it.
func TestServfailIsWellFormed(t *testing.T) {
	query := buildQuery("example.com", typeA)
	resp := servfail(query)

	if len(resp) < 12 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	qdcount := binary.BigEndian.Uint16(resp[4:6])
	flags := binary.BigEndian.Uint16(resp[2:4])
	if flags&flagResponse == 0 {
		t.Errorf("QR bit not set")
	}
	if rcode := flags & 0x000F; rcode != rcodeServFail {
		t.Errorf("rcode = %d; want %d (SERVFAIL)", rcode, rcodeServFail)
	}
	if binary.BigEndian.Uint16(resp[6:8]) != 0 {
		t.Errorf("ANCOUNT must be zero on SERVFAIL")
	}
	switch qdcount {
	case 0:
		// Header-only response — body must be exactly 12 bytes.
		if len(resp) != 12 {
			t.Errorf("QDCOUNT=0 but response is %d bytes; want 12", len(resp))
		}
	case 1:
		// Question echoed — must equal the byte offset right after the question.
		end := questionEnd(query)
		if end < 0 {
			t.Fatalf("test bug: questionEnd of valid query returned -1")
		}
		if len(resp) != end {
			t.Errorf("QDCOUNT=1 but response is %d bytes; want %d (12 + question)", len(resp), end)
		}
		if string(resp[12:end]) != string(query[12:end]) {
			t.Errorf("question section mismatch: got %x want %x", resp[12:end], query[12:end])
		}
	default:
		t.Errorf("unexpected QDCOUNT=%d", qdcount)
	}
}

// TestServfailMalformedQuery checks the failure-mode path: a query whose
// question section can't be parsed should still produce a 12-byte response
// with QDCOUNT=0, not a malformed body that lies about its contents.
func TestServfailMalformedQuery(t *testing.T) {
	// Header claims 1 question but body is just the header.
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q[4:6], 1) // QDCOUNT=1, but no question follows
	resp := servfail(q)
	if len(resp) != 12 {
		t.Fatalf("response = %d bytes; want 12 for header-only fallback", len(resp))
	}
	if binary.BigEndian.Uint16(resp[4:6]) != 0 {
		t.Errorf("QDCOUNT must be zeroed when question is unavailable")
	}
}
