package cert

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testHostnames = []string{"local.vibe", "desk.vibe", "test.vibe"}

func TestEnsureCA(t *testing.T) {
	dir := t.TempDir()

	ca, key, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if !ca.IsCA {
		t.Error("expected IsCA=true")
	}
	if ca.Subject.CommonName != "local.vibe CA" {
		t.Errorf("expected CN=local.vibe CA, got %s", ca.Subject.CommonName)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}

	// Verify files exist with correct permissions
	info, err := os.Stat(filepath.Join(dir, "ca-key.pem"))
	if err != nil {
		t.Fatalf("ca-key.pem not found: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("ca-key.pem permissions: got %o, want 0600", perm)
	}

	// Calling again should return the same CA (idempotent)
	ca2, _, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA (second call): %v", err)
	}
	if ca.SerialNumber.Cmp(ca2.SerialNumber) != 0 {
		t.Error("expected same serial on second call (loaded from disk)")
	}
}

func TestEnsureLeaf(t *testing.T) {
	dir := t.TempDir()

	ca, caKey, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	certFile, keyFile, err := EnsureLeaf(dir, ca, caKey, testHostnames)
	if err != nil {
		t.Fatalf("EnsureLeaf: %v", err)
	}

	// Verify leaf cert properties
	leaf, err := loadCert(certFile)
	if err != nil {
		t.Fatalf("load leaf: %v", err)
	}
	if leaf.IsCA {
		t.Error("leaf should not be CA")
	}

	// Check DNSNames include all requested hostnames
	wantDNS := make(map[string]bool)
	for _, h := range testHostnames {
		wantDNS[h] = true
	}
	for _, name := range leaf.DNSNames {
		delete(wantDNS, name)
	}
	if len(wantDNS) > 0 {
		t.Errorf("missing DNSNames: %v", wantDNS)
	}

	// Verify chain: leaf → CA for each hostname
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, hostname := range testHostnames {
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:     roots,
			DNSName:   hostname,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("chain verification failed for %s: %v", hostname, err)
		}
	}

	// Verify key file permissions
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("vibe-key.pem not found: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("vibe-key.pem permissions: got %o, want 0600", perm)
	}

	// Validity should be ~825 days
	duration := leaf.NotAfter.Sub(leaf.NotBefore)
	if duration < 824*24*time.Hour || duration > 827*24*time.Hour {
		t.Errorf("leaf validity: got %v, want ~825 days", duration)
	}
}

func TestEnsureLeafIdempotent(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)

	cert1, _, _ := EnsureLeaf(dir, ca, caKey, testHostnames)
	leaf1, _ := loadCert(cert1)

	cert2, _, _ := EnsureLeaf(dir, ca, caKey, testHostnames)
	leaf2, _ := loadCert(cert2)

	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) != 0 {
		t.Error("expected same cert on second call (not expired, same hostnames)")
	}
}

func TestEnsureLeafRegeneratesOnNewHostnames(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)

	cert1, _, _ := EnsureLeaf(dir, ca, caKey, []string{"local.vibe"})
	leaf1, _ := loadCert(cert1)

	// Adding a new hostname should regenerate
	cert2, _, _ := EnsureLeaf(dir, ca, caKey, []string{"local.vibe", "new.vibe"})
	leaf2, _ := loadCert(cert2)

	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) == 0 {
		t.Error("expected new cert when hostnames change")
	}

	// Verify the new cert covers both hostnames
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, h := range []string{"local.vibe", "new.vibe"} {
		if _, err := leaf2.Verify(x509.VerifyOptions{
			Roots:     roots,
			DNSName:   h,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("new cert doesn't cover %s: %v", h, err)
		}
	}
}

func TestEnsureLeafRenewal(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)

	// Generate initial leaf
	certFile, _, _ := EnsureLeaf(dir, ca, caKey, testHostnames)

	// Overwrite with a cert expiring in 10 days to trigger renewal
	leaf, _ := loadCert(certFile)
	oldSerial := leaf.SerialNumber

	// Write an about-to-expire cert
	writeExpiringCert(t, dir, ca, caKey, 10*24*time.Hour)

	// EnsureLeaf should regenerate
	certFile2, _, err := EnsureLeaf(dir, ca, caKey, testHostnames)
	if err != nil {
		t.Fatalf("EnsureLeaf renewal: %v", err)
	}
	leaf2, _ := loadCert(certFile2)
	if leaf2.SerialNumber.Cmp(oldSerial) == 0 {
		t.Error("expected new serial after renewal")
	}
}

func TestLoadTLSConfig(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)
	certFile, keyFile, _ := EnsureLeaf(dir, ca, caKey, testHostnames)

	tlsCfg, err := LoadTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatalf("LoadTLSConfig: %v", err)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("expected 1 cert, got %d", len(tlsCfg.Certificates))
	}
}

func TestTLSHandshake(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)
	certFile, keyFile, _ := EnsureLeaf(dir, ca, caKey, testHostnames)

	tlsCfg, _ := LoadTLSConfig(certFile, keyFile)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("TLS listen: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		tlsConn := conn.(*tls.Conn)
		err = tlsConn.Handshake()
		conn.Close()
		done <- err
	}()

	roots := x509.NewCertPool()
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	roots.AppendCertsFromPEM(caPEM)

	clientCfg := &tls.Config{
		RootCAs:    roots,
		ServerName: "test.vibe",
	}

	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("TLS handshake failed: %v", err)
	}
	conn.Close()

	if srvErr := <-done; srvErr != nil {
		t.Fatalf("server handshake failed: %v", srvErr)
	}
}

func TestGenerateLeaf(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)

	// Generate once
	cert1, _, _ := GenerateLeaf(dir, ca, caKey, testHostnames)
	leaf1, _ := loadCert(cert1)

	// GenerateLeaf always regenerates (unlike EnsureLeaf)
	cert2, _, _ := GenerateLeaf(dir, ca, caKey, testHostnames)
	leaf2, _ := loadCert(cert2)

	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) == 0 {
		t.Error("GenerateLeaf should always create a new cert")
	}
}

func TestCoversAll(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := EnsureCA(dir)

	certFile, _, _ := EnsureLeaf(dir, ca, caKey, []string{"a.vibe", "b.vibe"})
	leaf, _ := loadCert(certFile)

	if !coversAll(leaf, []string{"a.vibe", "b.vibe"}) {
		t.Error("should cover same hostnames")
	}
	if !coversAll(leaf, []string{"a.vibe"}) {
		t.Error("should cover subset")
	}
	if coversAll(leaf, []string{"a.vibe", "c.vibe"}) {
		t.Error("should not cover c.vibe")
	}
}

// writeExpiringCert creates a leaf cert that expires in the given duration.
func writeExpiringCert(t *testing.T, dir string, ca *x509.Certificate, caKey interface{}, expiresIn time.Duration) {
	t.Helper()
	certPath := filepath.Join(dir, "vibe.pem")

	data, _ := os.ReadFile(certPath)
	block, _ := pem.Decode(data)
	existing, _ := x509.ParseCertificate(block.Bytes)

	template := &x509.Certificate{
		SerialNumber: existing.SerialNumber,
		Subject:      existing.Subject,
		DNSNames:     existing.DNSNames,
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(expiresIn),
		KeyUsage:     existing.KeyUsage,
		ExtKeyUsage:  existing.ExtKeyUsage,
	}

	keyData, _ := os.ReadFile(filepath.Join(dir, "vibe-key.pem"))
	keyBlock, _ := pem.Decode(keyData)
	leafKey, _ := x509.ParseECPrivateKey(keyBlock.Bytes)

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create expiring cert: %v", err)
	}
	_ = writePEM(certPath, "CERTIFICATE", certDER, 0644)
}
