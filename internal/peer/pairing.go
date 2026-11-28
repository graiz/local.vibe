package peer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
)

// codeAlphabet is Crockford base32: digits plus A–Z without I, L, O, U
// (no lookalikes, fewer accidental words).
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ProofIterations is the PBKDF2 cost applied to a pairing code before it keys
// the proof. Measured 2026-11-26: ~960k iterations per 100 ms on an
// Apple-silicon Mac (SHA extensions), several times slower on an Intel Mac
// running an amd64 build — so 600k: ~60 ms per proof on the fast machine, well
// under a second on the slow one, and a pairing computes four. The code
// (~2^39 valid values) is short enough to type; stretching is what keeps a
// proof that reached the wrong machine from being brute-forced inside the
// 5-minute invite window: plain HMAC falls to one gaming GPU in about a minute,
// 600k iterations put the full space at years.
const ProofIterations = 600_000

// proofSaltPrefix versions the proof construction; a v1 (6-digit, plain
// HMAC) peer cannot produce a v2 proof, so mixed versions fail to pair.
const proofSaltPrefix = "vibe-peer-pair-v2|"

// NewInviteCode returns a one-time pairing code "XXXX-XXXX": two blocks of
// four Crockford base32 characters from crypto/rand, each block containing
// at least one digit so a code can never spell a word.
func NewInviteCode() (string, error) {
	a, err := codeBlock()
	if err != nil {
		return "", err
	}
	b, err := codeBlock()
	if err != nil {
		return "", err
	}
	return a + "-" + b, nil
}

// codeBlock draws 4 alphabet characters, redrawing until one is a digit.
func codeBlock() (string, error) {
	max := big.NewInt(int64(len(codeAlphabet)))
	for {
		var b strings.Builder
		for i := 0; i < 4; i++ {
			n, err := rand.Int(rand.Reader, max)
			if err != nil {
				return "", err
			}
			b.WriteByte(codeAlphabet[n.Int64()])
		}
		if strings.ContainsAny(b.String(), "0123456789") {
			return b.String(), nil
		}
	}
}

// NormalizeCode canonicalizes a typed code to "XXXX-XXXX": case-insensitive,
// dashes and spaces optional, and Crockford's O→0 and I/L→1. ok is false for
// anything that isn't 8 alphabet characters with a digit in each block.
func NormalizeCode(s string) (string, bool) {
	var b strings.Builder
	for _, c := range strings.ToUpper(s) {
		switch c {
		case '-', ' ':
			continue
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if !strings.ContainsRune(codeAlphabet, c) {
			return "", false
		}
		b.WriteRune(c)
	}
	raw := b.String()
	if len(raw) != 8 || !strings.ContainsAny(raw[:4], "0123456789") || !strings.ContainsAny(raw[4:], "0123456789") {
		return "", false
	}
	return raw[:4] + "-" + raw[4:], true
}

// Proof binds an invite code to the exact certificate fingerprints seen on
// the wire. The code is first stretched with PBKDF2 (salted with both
// fingerprints), then keys HMAC-SHA256 over "senderFP|receiverFP". The
// initiator proves (its own fp, the server fp it observed); the responder
// replies with the roles swapped. A MITM that substitutes either cert
// changes the message, and without the code it cannot recompute the MAC.
// Callers pass the normalized code.
func Proof(code, senderFP, receiverFP string) string {
	key := pbkdf2SHA256([]byte(code), []byte(proofSaltPrefix+senderFP+"|"+receiverFP), ProofIterations, 32)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(senderFP + "|" + receiverFP))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyProof checks a proof in constant time.
func VerifyProof(proof, code, senderFP, receiverFP string) bool {
	return hmac.Equal([]byte(proof), []byte(Proof(code, senderFP, receiverFP)))
}

// ShortKey is the human-comparable form of a key fingerprint: the first 8
// hex characters, uppercased, as "4F2A·91C3". Shown on both machines during
// pairing so a spoofed name can't pass for the real machine.
func ShortKey(fp string) string {
	u := strings.ToUpper(fp)
	if len(u) < 8 {
		return u
	}
	return u[:4] + "\u00b7" + u[4:8]
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
