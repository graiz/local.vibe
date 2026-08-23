// Package peer holds the primitives for vibe's experimental cross-machine
// peer feature: a per-daemon identity certificate, SSH-style fingerprint
// pinning, invite-code pairing proofs, and the peers.json store. It is
// deliberately independent of the daemon package.
package peer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// EnsureIdentity loads or creates the daemon's peer identity: a self-signed
// ECDSA certificate used ONLY for daemon-to-daemon mTLS. It is deliberately
// separate from the browser CA (internal/cert) — that CA is trusted by the OS
// keychain and must never authenticate network peers. Trust in this cert is
// established by fingerprint pinning at pairing time, never by chain
// verification, so self-signed is exactly right.
func EnsureIdentity(certsDir string) (tls.Certificate, error) {
	certPath := filepath.Join(certsDir, "peer.pem")
	keyPath := filepath.Join(certsDir, "peer-key.pem")
	if id, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return id, nil
	}
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "vibe-peer " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0644); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// Fingerprint returns the lowercase SHA-256 hex digest of a certificate's
// DER bytes — the identity that pairing pins and every later handshake
// re-verifies.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// IdentityFingerprint is Fingerprint of a loaded identity's leaf.
func IdentityFingerprint(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return ""
	}
	return Fingerprint(c.Certificate[0])
}

func writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return nil
}
