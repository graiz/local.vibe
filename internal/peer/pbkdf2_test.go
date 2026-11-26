package peer

import (
	"encoding/hex"
	"testing"
)

// RFC 7914 §11, PBKDF2-HMAC-SHA256 test vector.
func TestPBKDF2Vector(t *testing.T) {
	got := hex.EncodeToString(pbkdf2SHA256([]byte("passwd"), []byte("salt"), 1, 64))
	want := "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc49ca9cccf179b645991664b39d77ef317c71b845b1e30bd509112041d3a19783"
	if got != want {
		t.Fatalf("pbkdf2 = %s\nwant      %s", got, want)
	}
}
