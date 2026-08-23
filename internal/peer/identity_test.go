package peer

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureIdentityCreatesAndReloads(t *testing.T) {
	dir := t.TempDir()
	id1, err := EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp1 := IdentityFingerprint(id1)
	if len(fp1) != 64 {
		t.Fatalf("fingerprint = %q, want 64 hex chars", fp1)
	}
	// Key must not be world-readable.
	info, err := os.Stat(filepath.Join(dir, "peer-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("peer-key.pem mode = %v, want 0600", info.Mode().Perm())
	}
	// Second call reloads the same identity, never regenerates.
	id2, err := EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if IdentityFingerprint(id2) != fp1 {
		t.Fatal("EnsureIdentity regenerated the identity on reload")
	}
	// The cert must be usable for both TLS client and server auth.
	leaf, err := x509.ParseCertificate(id1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	var hasServer, hasClient bool
	for _, ku := range leaf.ExtKeyUsage {
		if ku == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
		if ku == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasServer || !hasClient {
		t.Fatal("identity cert must carry both server- and client-auth EKUs")
	}
}
