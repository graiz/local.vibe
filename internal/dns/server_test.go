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

// TestServerAnswersVibeAQueryWithEDNS locks down the EDNS-handling fix in
// buildLocalResponse: a query that includes an OPT pseudo-RR in its
// additional section (which Windows DnsClient, Chrome, and dig all do by
// default) must still produce a clean response where the A record is
// reachable as the first and only answer.
//
// The original implementation copied the full query, cleared ARCOUNT, and
// appended the A record at the end. That left the OPT bytes in the body,
// so a strict parser walking ANCOUNT=1 would consume the OPT record as the
// "answer" and miss our A record entirely.
func TestServerAnswersVibeAQueryWithEDNS(t *testing.T) {
	srv := startTestServer(t, Config{TLD: "vibe", Upstream: "127.0.0.1:1"})
	resp := askUDP(t, srv.Listen(), buildQueryWithEDNS("foo.vibe", typeA))
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
	arcount := binary.BigEndian.Uint16(resp[10:12])
	if arcount != 0 {
		t.Errorf("ARCOUNT = %d; want 0 (no OPT echoed)", arcount)
	}
	// Walk the message: skip header + question, then verify the next bytes
	// are our A answer (compression pointer 0xC00C + TYPE=A) — not an OPT
	// record leaked from the query's additional section.
	end := questionEnd(buildQueryWithEDNS("foo.vibe", typeA))
	if end < 0 {
		t.Fatalf("test bug: questionEnd returned -1")
	}
	if len(resp) < end+12 {
		t.Fatalf("response missing answer section: len=%d, expected at least %d", len(resp), end+12)
	}
	// Answer should start with 0xC0 0x0C (pointer to QNAME at offset 12).
	if resp[end] != 0xC0 || resp[end+1] != 0x0C {
		t.Errorf("answer NAME = %x %x; want C0 0C (compression pointer)", resp[end], resp[end+1])
	}
	// TYPE=A.
	if rtype := binary.BigEndian.Uint16(resp[end+2 : end+4]); rtype != typeA {
		t.Errorf("answer TYPE = %d; want %d (A)", rtype, typeA)
	}
	// Last 4 bytes are RDATA: 127.0.0.1.
	if got := resp[len(resp)-4:]; got[0] != 127 || got[1] != 0 || got[2] != 0 || got[3] != 1 {
		t.Errorf("RDATA = %v; want 127.0.0.1", got)
	}
}

// buildQueryWithEDNS builds a DNS query with an EDNS0 OPT pseudo-RR in the
// additional section, mimicking what real-world resolvers send.
func buildQueryWithEDNS(name string, qtype uint16) []byte {
	q := buildQuery(name, qtype)
	// Bump ARCOUNT in the header to claim one additional record.
	binary.BigEndian.PutUint16(q[10:12], 1)
	// Append an OPT pseudo-RR: NAME=. (root, 1 byte), TYPE=41 (OPT),
	// CLASS=4096 (UDP payload size), TTL=0 (extended RCODE+version+flags),
	// RDLENGTH=0 (no options).
	opt := []byte{
		0,          // root NAME
		0x00, 0x29, // TYPE=41 (OPT)
		0x10, 0x00, // CLASS = 4096 (UDP payload size)
		0x00, 0x00, 0x00, 0x00, // TTL = extended-RCODE/version/flags
		0x00, 0x00, // RDLENGTH = 0
	}
	return append(q, opt...)
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

// FuzzParseQuestion exercises the question-section parser against arbitrary
// byte inputs. The parser MUST never panic — malformed inputs return
// ok=false. Since the daemon's resolver listens on loopback only, attacker
// reach is limited, but a panic in the read loop would still kill DNS for
// the whole machine. Bounds-check regressions are exactly what fuzzing is
// good at catching.
//
// Runs as a deterministic seed-corpus replay during `go test ./...` (no
// random mutation). Run with `go test -fuzz=FuzzParseQuestion` for actual
// fuzzing during development.
func FuzzParseQuestion(f *testing.F) {
	// Seed with a few well-formed and pathological inputs.
	f.Add(buildQuery("foo.vibe", typeA))
	f.Add(buildQuery("a.b.c.d.e.f.g.vibe", typeAAAA))
	f.Add([]byte{}) // empty
	f.Add(make([]byte, 12)) // header only, all zero
	// Header claims 1 question but body is the header alone.
	hdrOnly := make([]byte, 12)
	binary.BigEndian.PutUint16(hdrOnly[4:6], 1)
	f.Add(hdrOnly)
	// Compression-pointer label inside QNAME — parser must reject.
	withPointer := []byte{
		0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, // header, QDCOUNT=1
		0xC0, 0x0C, // pointer where a label length should be
		0, 1, 0, 1, // QTYPE=A, QCLASS=IN
	}
	f.Add(withPointer)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on any input. Don't assert a specific outcome —
		// either ok=true with valid fields, or ok=false. Anything that
		// avoids panic is correct behavior at this layer.
		_, _, _, _ = parseQuestion(data)
		// questionEnd uses the same label-walk; cover it too.
		_ = questionEnd(data)
		// servfail must also tolerate arbitrary input (used as a
		// last-ditch error response).
		_ = servfail(data)
	})
}
