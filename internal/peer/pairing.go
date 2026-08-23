package peer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// NewInviteCode returns a 6-digit one-time pairing code from crypto/rand.
// Six digits is enough because the code guards a single online exchange with
// a 5-minute TTL, not an offline-attackable secret.
func NewInviteCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n), nil
}

// Proof binds an invite code to the exact certificate fingerprints seen on
// the wire: HMAC-SHA256 keyed by the code over "senderFP|receiverFP". The
// initiator proves (its own fp, the server fp it observed); the responder
// replies with the roles swapped. A MITM that substitutes either cert
// changes the message, and without the code it cannot recompute the MAC.
func Proof(code, senderFP, receiverFP string) string {
	mac := hmac.New(sha256.New, []byte(code))
	mac.Write([]byte(senderFP + "|" + receiverFP))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyProof checks a proof in constant time.
func VerifyProof(proof, code, senderFP, receiverFP string) bool {
	return hmac.Equal([]byte(proof), []byte(Proof(code, senderFP, receiverFP)))
}

// SanitizeName converts a hostname into a peer name safe for display and for
// use as a map key: lowercase, trailing ".local" dropped, anything outside
// [a-z0-9-] mapped to "-", runs collapsed, edges trimmed.
func SanitizeName(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".local")
	var b strings.Builder
	prevDash := true // suppress leading dashes
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "peer"
	}
	return out
}
