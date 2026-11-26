package peer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// Codes are two blocks of four Crockford base32 characters, each block
// holding at least one digit so a code can never spell a word.
func TestNewInviteCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[rune]bool{}
	for i := 0; i < 5000; i++ {
		code, err := NewInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(code) {
			t.Fatalf("code %q is not XXXX-XXXX in the Crockford alphabet", code)
		}
		for _, block := range strings.Split(code, "-") {
			if !strings.ContainsAny(block, "0123456789") {
				t.Fatalf("code %q has a block with no digit", code)
			}
		}
		for _, c := range code {
			seen[c] = true
		}
	}
	for _, c := range codeAlphabet {
		if !seen[c] {
			t.Errorf("alphabet char %q never generated in 5000 codes", c)
		}
	}
}

// Typed codes are forgiving: case, dash, spaces, and Crockford's O/I/L.
func TestNormalizeCode(t *testing.T) {
	ok := map[string]string{
		"k7qm-3xr9":   "K7QM-3XR9",
		"K7QM3XR9":    "K7QM-3XR9",
		" K7QM 3XR9 ": "K7QM-3XR9",
		"O0IL-1234":   "0011-1234",
	}
	for in, want := range ok {
		got, valid := NormalizeCode(in)
		if !valid || got != want {
			t.Errorf("NormalizeCode(%q) = %q, %v; want %q, true", in, got, valid, want)
		}
	}
	for _, bad := range []string{"K7QM-3XR", "ABCD-EFGH", "K7QU-3XR9", "123456", ""} {
		if got, valid := NormalizeCode(bad); valid {
			t.Errorf("NormalizeCode(%q) = %q, true; want rejected", bad, got)
		}
	}
}

// v2 proofs are stretched with PBKDF2; an old plain-HMAC proof must not verify.
func TestProofV2RejectsV1(t *testing.T) {
	const code, fpA, fpB = "K7QM-3XR9", "aaaa", "bbbb"
	mac := hmac.New(sha256.New, []byte(code))
	mac.Write([]byte(fpA + "|" + fpB))
	v1 := hex.EncodeToString(mac.Sum(nil))
	if VerifyProof(v1, code, fpA, fpB) {
		t.Fatal("unstretched v1 proof verified")
	}
	if !VerifyProof(Proof(code, fpA, fpB), code, fpA, fpB) {
		t.Fatal("v2 proof does not verify")
	}
}

func TestShortKey(t *testing.T) {
	if got := ShortKey("4f2a91c3deadbeef"); got != "4F2A·91C3" {
		t.Errorf("ShortKey = %q", got)
	}
	if got := ShortKey("ab12"); got != "AB12" {
		t.Errorf("ShortKey(short) = %q", got)
	}
}

func TestProofRoundTripAndTamper(t *testing.T) {
	const code, fpA, fpB = "K7QM-3XR9", "aaaa", "bbbb"
	p := Proof(code, fpA, fpB)
	if !VerifyProof(p, code, fpA, fpB) {
		t.Fatal("valid proof rejected")
	}
	if VerifyProof(p, "K7QM-3XR8", fpA, fpB) {
		t.Fatal("wrong code accepted")
	}
	if VerifyProof(p, code, "cccc", fpB) {
		t.Fatal("swapped sender fingerprint accepted — MITM splice")
	}
	if VerifyProof(p, code, fpB, fpA) {
		t.Fatal("reversed roles accepted — reply must differ from request")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Alices-iMac.local": "alices-imac",
		"MY_HOST":           "my-host",
		"..weird..":         "weird",
		"":                  "peer",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
