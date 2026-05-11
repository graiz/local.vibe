// Package dns implements a tiny UDP DNS resolver used on platforms where
// vibe can't lean on the OS resolver (currently Windows). It answers A and
// AAAA queries for names ending in the configured TLD with 127.0.0.1 (or an
// empty NOERROR for AAAA so clients fall back to A); every other query is
// forwarded verbatim to an upstream resolver.
//
// The implementation is deliberately minimal — only the question section is
// parsed, only one question per query is honored, and EDNS/DNSSEC bits are
// passed through without inspection. That's enough for browsers and dev
// tooling on a single dev machine, and avoids pulling in a heavyweight DNS
// library.
package dns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// DNS message format constants (RFC 1035).
	flagResponse       = 0x8000
	flagRecursionAvail = 0x0080
	rcodeServFail      = 2

	typeA    uint16 = 1
	typeAAAA uint16 = 28
	classIN  uint16 = 1

	defaultUpstreamTimeout = time.Second
	maxMessageSize         = 4096 // generous; standard says 512 without EDNS, but EDNS extends.
)

// Config controls server behavior. Zero values pick sensible defaults.
type Config struct {
	// TLD is the suffix vibe owns, without leading dot — e.g. "vibe".
	// Queries for *.<TLD> get synthesized A=127.0.0.1 / AAAA=empty answers.
	TLD string

	// Listen is the address the UDP server binds. Defaults to 127.0.0.1:53.
	Listen string

	// Upstream is the address of the resolver we forward non-vibe queries to.
	// Defaults to 8.8.8.8:53.
	Upstream string

	// UpstreamTimeout is the per-query upstream deadline. Defaults to 1s.
	UpstreamTimeout time.Duration
}

// Server is a running DNS resolver. Use Start / Stop to control its
// lifecycle. Safe to call Stop multiple times.
type Server struct {
	cfg  Config
	conn *net.UDPConn

	stopOnce sync.Once
	done     chan struct{}
}

// New constructs a Server with the given config and reasonable defaults.
func New(cfg Config) *Server {
	if cfg.TLD == "" {
		cfg.TLD = "vibe"
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:53"
	}
	if cfg.Upstream == "" {
		cfg.Upstream = "8.8.8.8:53"
	}
	if cfg.UpstreamTimeout == 0 {
		cfg.UpstreamTimeout = defaultUpstreamTimeout
	}
	return &Server{cfg: cfg, done: make(chan struct{})}
}

// Start binds the UDP socket and runs the read loop in a goroutine.
// Returns immediately; the goroutine exits when Stop is called.
func (s *Server) Start() error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", s.cfg.Listen, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	s.conn = conn
	go s.serve()
	return nil
}

// Stop closes the UDP socket and waits for the read loop to exit.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		<-s.done
	})
}

// Listen returns the actual local address bound by the server. Useful for
// tests that ask for port 0 and want to discover what was assigned.
func (s *Server) Listen() net.Addr {
	if s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

func (s *Server) serve() {
	defer close(s.done)
	buf := make([]byte, maxMessageSize)
	for {
		n, src, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			// net.ErrClosed is the graceful-Stop signal — exit the loop.
			// Anything else is logged and we keep serving: a single transient
			// read error (ICMP-port-unreachable feedback, Windows WSAECONNRESET
			// from a vanished client) shouldn't kill DNS for the whole
			// machine. We don't have the daemon's logger here so stderr is
			// the lowest-friction sink.
			if errors.Is(err, net.ErrClosed) {
				return
			}
			fmt.Fprintf(os.Stderr, "dns: read error: %v\n", err)
			continue
		}
		go s.handle(append([]byte(nil), buf[:n]...), src)
	}
}

// handle decides whether to answer locally or forward upstream. The query
// runs in its own goroutine so a slow upstream doesn't block other clients.
func (s *Server) handle(query []byte, src *net.UDPAddr) {
	if len(query) < 12 {
		return // not even a valid header — drop silently
	}
	name, qtype, qclass, ok := parseQuestion(query)
	if !ok {
		return
	}
	// Only intercept IN-class A/AAAA queries for our TLD; everything else
	// (TXT, MX, the rare ANY, weird classes) goes upstream untouched.
	if qclass == classIN && (qtype == typeA || qtype == typeAAAA) && nameEndsInTLD(name, s.cfg.TLD) {
		resp := buildLocalResponse(query, qtype)
		_, _ = s.conn.WriteToUDP(resp, src)
		return
	}
	// Fall through: forward verbatim to upstream and relay the response.
	resp, err := s.forward(query)
	if err != nil {
		_, _ = s.conn.WriteToUDP(servfail(query), src)
		return
	}
	_, _ = s.conn.WriteToUDP(resp, src)
}

func (s *Server) forward(query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("udp", s.cfg.Upstream, s.cfg.UpstreamTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.cfg.UpstreamTimeout))
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, maxMessageSize)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// nameEndsInTLD reports whether name's last label equals tld
// (case-insensitive). Both arguments lack leading/trailing dots.
func nameEndsInTLD(name, tld string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	tld = strings.ToLower(tld)
	if name == tld {
		return true
	}
	return strings.HasSuffix(name, "."+tld)
}

// parseQuestion extracts the first question from a DNS query: the QNAME as
// a dot-joined string, QTYPE, and QCLASS. Returns ok=false if the message
// is malformed before the question section ends.
func parseQuestion(msg []byte) (name string, qtype, qclass uint16, ok bool) {
	if len(msg) < 12 {
		return "", 0, 0, false
	}
	qdcount := binary.BigEndian.Uint16(msg[4:6])
	if qdcount == 0 {
		return "", 0, 0, false
	}
	pos := 12
	var labels []string
	for {
		if pos >= len(msg) {
			return "", 0, 0, false
		}
		l := int(msg[pos])
		pos++
		if l == 0 {
			break
		}
		// Compression pointers (top 2 bits set) are not expected in question
		// names, but if we see one, abort — we don't follow them here.
		if l&0xC0 != 0 {
			return "", 0, 0, false
		}
		if pos+l > len(msg) {
			return "", 0, 0, false
		}
		labels = append(labels, string(msg[pos:pos+l]))
		pos += l
	}
	if pos+4 > len(msg) {
		return "", 0, 0, false
	}
	qtype = binary.BigEndian.Uint16(msg[pos : pos+2])
	qclass = binary.BigEndian.Uint16(msg[pos+2 : pos+4])
	return strings.Join(labels, "."), qtype, qclass, true
}

// buildLocalResponse synthesizes an A response with 127.0.0.1 (or an empty
// NOERROR for AAAA) reusing the query's header and question section. We
// flip QR, set RA, and append a single answer for typeA queries.
//
// Only the header + question section is echoed back — any additional records
// in the original query (notably EDNS0 OPT pseudo-RRs that Windows DnsClient,
// Chrome, and dig add by default) are dropped. Keeping them would leave the
// response with ARCOUNT=0 in the header but OPT bytes still in the body,
// which strict parsers consume as the "answer" record and miss our A record
// at the end.
func buildLocalResponse(query []byte, qtype uint16) []byte {
	end := questionEnd(query)
	if end < 0 {
		// Malformed query — handle() already gates on parseQuestion, so this
		// path is unreachable in practice. Return SERVFAIL defensively.
		return servfail(query)
	}
	resp := make([]byte, end)
	copy(resp, query[:end])
	flags := binary.BigEndian.Uint16(resp[2:4])
	flags |= flagResponse | flagRecursionAvail
	flags &^= 0x000F // clear RCODE
	binary.BigEndian.PutUint16(resp[2:4], flags)
	// QDCOUNT stays as-is; ANCOUNT/NSCOUNT/ARCOUNT cleared.
	binary.BigEndian.PutUint16(resp[6:8], 0)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)

	if qtype != typeA {
		// AAAA / others: NOERROR with zero answers — clients then ask for A.
		return resp
	}

	// Append: NAME (compression pointer to offset 12, where the question's
	// QNAME starts), TYPE=A, CLASS=IN, TTL=60, RDLENGTH=4, RDATA=127.0.0.1.
	answer := []byte{
		0xC0, 0x0C, // pointer to QNAME at offset 12
		0x00, byte(typeA),
		0x00, byte(classIN),
		0x00, 0x00, 0x00, 0x3C, // TTL = 60s
		0x00, 0x04, // RDLENGTH
		127, 0, 0, 1,
	}
	resp = append(resp, answer...)
	binary.BigEndian.PutUint16(resp[6:8], 1) // ANCOUNT = 1
	return resp
}

// servfail returns a server-failure response for query (RCODE=2) so the
// client gets a real error rather than a silent drop on upstream failure.
//
// Echoes the question section so the response is well-formed (QDCOUNT in
// the header agrees with the body). If the query is malformed past the
// header we still return a 12-byte header-only response, but with QDCOUNT
// cleared so strict clients don't try to parse a question that isn't there.
func servfail(query []byte) []byte {
	if len(query) < 12 {
		// Truly malformed input — can't even echo a header. Return as-is
		// so the client at least gets something back rather than timing out.
		return query
	}
	end := questionEnd(query)
	includeQuestion := end > 0 && end <= len(query)
	size := 12
	if includeQuestion {
		size = end
	}
	resp := make([]byte, size)
	copy(resp, query[:size])
	flags := binary.BigEndian.Uint16(resp[2:4])
	flags |= flagResponse | flagRecursionAvail
	flags &^= 0x000F
	flags |= rcodeServFail
	binary.BigEndian.PutUint16(resp[2:4], flags)
	if !includeQuestion {
		// No question echoed; QDCOUNT must be zero so the response parses.
		binary.BigEndian.PutUint16(resp[4:6], 0)
	}
	binary.BigEndian.PutUint16(resp[6:8], 0)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)
	return resp
}

// questionEnd returns the byte offset right after the question section,
// or -1 if the message is malformed before that point. Mirrors the
// label-walk in parseQuestion but skips string assembly — useful for
// callers (servfail) that only need to know where the question ends.
func questionEnd(msg []byte) int {
	if len(msg) < 12 {
		return -1
	}
	qdcount := binary.BigEndian.Uint16(msg[4:6])
	if qdcount == 0 {
		return 12
	}
	pos := 12
	for {
		if pos >= len(msg) {
			return -1
		}
		l := int(msg[pos])
		pos++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 {
			return -1
		}
		if pos+l > len(msg) {
			return -1
		}
		pos += l
	}
	if pos+4 > len(msg) {
		return -1
	}
	return pos + 4
}
