//go:build go1.24

package peer

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// Cross-check the in-house PBKDF2 against the stdlib's (available from Go
// 1.24; the module's floor is 1.22, so production code can't import it).
func TestPBKDF2MatchesStdlib(t *testing.T) {
	for i := 0; i < 20; i++ {
		pw, salt := make([]byte, 1+i), make([]byte, 16)
		rand.Read(pw)
		rand.Read(salt)
		want, err := pbkdf2.Key(sha256.New, string(pw), salt, 1000, 32+i)
		if err != nil {
			t.Fatal(err)
		}
		if got := pbkdf2SHA256(pw, salt, 1000, 32+i); !bytes.Equal(got, want) {
			t.Fatalf("mismatch at len %d", 32+i)
		}
	}
}
