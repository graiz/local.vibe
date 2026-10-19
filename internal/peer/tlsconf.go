package peer

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
)

// ServerTLSConfig builds the peer listener's TLS config: present the local
// identity, demand SOME client cert, and reject the handshake unless the
// authorize callback accepts its fingerprint. authorize is consulted per
// handshake so pins added or removed at runtime (pairing, vibe peer remove)
// take effect without a listener restart — and during an open invite it can
// admit a not-yet-pinned cert whose pairing proof the HTTP layer then checks.
func ServerTLSConfig(id tls.Certificate, authorize func(fp string) bool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("peer: no client certificate")
			}
			if !authorize(Fingerprint(rawCerts[0])) {
				return errors.New("peer: client certificate not pinned")
			}
			return nil
		},
	}
}

// ClientTLSConfig builds the dialing side: present the local identity and
// replace chain+hostname verification with the verify callback on the server
// leaf's fingerprint. InsecureSkipVerify is deliberate — trust here is the
// pin established at pairing, not a CA chain; a self-signed peer cert can
// never chain anyway.
func ClientTLSConfig(id tls.Certificate, verify func(fp string) error) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{id},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("peer: server sent no certificate")
			}
			return verify(Fingerprint(rawCerts[0]))
		},
	}
}
