package peer

import "testing"

func TestInviteCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := NewInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 {
			t.Fatalf("code %q: want 6 digits", code)
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("code %q contains non-digit", code)
			}
		}
		seen[code] = true
	}
	if len(seen) < 2 {
		t.Fatal("codes are not random")
	}
}

func TestProofRoundTripAndTamper(t *testing.T) {
	const code, fpA, fpB = "123456", "aaaa", "bbbb"
	p := Proof(code, fpA, fpB)
	if !VerifyProof(p, code, fpA, fpB) {
		t.Fatal("valid proof rejected")
	}
	if VerifyProof(p, "654321", fpA, fpB) {
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
		"Gregs-iMac.local": "gregs-imac",
		"MY_HOST":          "my-host",
		"..weird..":        "weird",
		"":                 "peer",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
