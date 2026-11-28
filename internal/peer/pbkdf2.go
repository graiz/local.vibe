package peer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// pbkdf2SHA256 is PBKDF2 (RFC 8018 §5.2) with HMAC-SHA256. In-house because
// crypto/pbkdf2 is stdlib only from Go 1.24 and the module's floor is 1.22;
// pbkdf2_go124_test.go cross-checks it against the stdlib where available.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	var idx [4]byte
	u := make([]byte, hLen)
	t := make([]byte, hLen)
	for b := 1; b <= blocks; b++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(idx[:], uint32(b))
		prf.Write(idx[:])
		u = prf.Sum(u[:0])
		copy(t, u)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
