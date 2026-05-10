package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureCA loads or generates a local CA certificate and key in certsDir.
// The CA is self-signed with a 10-year validity and is used to sign leaf certs.
func EnsureCA(certsDir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPath := filepath.Join(certsDir, "ca.pem")
	keyPath := filepath.Join(certsDir, "ca-key.pem")

	// Try to load existing CA
	if cert, key, err := loadCA(certPath, keyPath); err == nil {
		return cert, key, nil
	}

	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("create certs dir: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "local.vibe CA",
			Organization: []string{"local.vibe"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA cert: %w", err)
	}

	if err := writePEM(certPath, "CERTIFICATE", certDER, 0644); err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal CA key: %w", err)
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0600); err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}

	return cert, key, nil
}

// EnsureLeaf loads or generates a leaf certificate for the given hostnames,
// signed by the given CA. If the cert exists, is not expiring, and covers all
// the requested hostnames, it is reused. Otherwise it is regenerated.
// Returns paths to the cert and key files.
func EnsureLeaf(certsDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, hostnames []string) (certFile, keyFile string, err error) {
	certFile = filepath.Join(certsDir, "vibe.pem")
	keyFile = filepath.Join(certsDir, "vibe-key.pem")

	// Check if existing leaf is still valid and covers all hostnames
	if existing, parseErr := loadCert(certFile); parseErr == nil {
		if time.Until(existing.NotAfter) > 30*24*time.Hour && coversAll(existing, hostnames) {
			return certFile, keyFile, nil
		}
	}

	return generateLeaf(certFile, keyFile, caCert, caKey, hostnames)
}

// GenerateLeaf unconditionally generates a new leaf certificate for the given
// hostnames, regardless of whether a valid one already exists. Used when routes
// change and the cert needs new SANs.
func GenerateLeaf(certsDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, hostnames []string) (certFile, keyFile string, err error) {
	certFile = filepath.Join(certsDir, "vibe.pem")
	keyFile = filepath.Join(certsDir, "vibe-key.pem")
	return generateLeaf(certFile, keyFile, caCert, caKey, hostnames)
}

func generateLeaf(certFile, keyFile string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, hostnames []string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate leaf key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}

	cn := "vibe"
	if len(hostnames) > 0 {
		cn = hostnames[0]
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{"local.vibe"},
		},
		DNSNames:  hostnames,
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(825 * 24 * time.Hour), // Apple's max
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("create leaf cert: %w", err)
	}

	if err := writePEM(certFile, "CERTIFICATE", certDER, 0644); err != nil {
		return "", "", err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal leaf key: %w", err)
	}
	if err := writePEM(keyFile, "EC PRIVATE KEY", keyDER, 0600); err != nil {
		return "", "", err
	}

	return certFile, keyFile, nil
}

// coversAll returns true if the certificate's DNSNames contain all requested hostnames.
func coversAll(cert *x509.Certificate, hostnames []string) bool {
	have := make(map[string]bool, len(cert.DNSNames))
	for _, name := range cert.DNSNames {
		have[name] = true
	}
	for _, h := range hostnames {
		if !have[h] {
			return false
		}
	}
	return true
}

// CAThumbprint returns the SHA1 thumbprint of the CA cert at certsDir/ca.pem
// formatted as uppercase hex with no separators — the form `certutil -delstore`
// accepts as a unique identifier on Windows. Used at uninstall time to delete
// the exact cert we installed, instead of matching by Subject CN (which can
// collide with unrelated certs sharing "local.vibe CA" as their name).
//
// Cross-platform helper — the SHA1 calculation is the same everywhere; only
// the consumer (Windows certutil) is OS-specific. SHA1 is the established
// thumbprint algorithm in every cert store API; collision resistance isn't
// the property being relied on here, identity-as-a-key is.
func CAThumbprint(certsDir string) (string, error) {
	cert, err := loadCert(filepath.Join(certsDir, "ca.pem"))
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

// LoadTLSConfig returns a tls.Config loaded from the given cert and key files.
func LoadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
	}, nil
}

// TrustCA installs the CA certificate into the OS-level trust store.
// Implementation lives in cert_<goos>.go. Requires elevated privileges.
//
// Note: Linux and Windows implementations are stubs in Phase 1 of Windows
// support; only macOS is fully wired. See FUTURE_PLAN.md for status.

// --- helpers ---

func loadCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := loadCert(certPath)
	if err != nil {
		return nil, nil, err
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("no PEM block in %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}

	return cert, key, nil
}

func loadCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func writePEM(path, blockType string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: data})
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}
