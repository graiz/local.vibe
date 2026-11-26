package cmd

import "testing"

const twoIdentities = `  1) 9BEB1EC9A86FBC3CB29E47C1746D310FBF9967D1 "Apple Development: Someone (ABCDE12345)"
  2) 1111111111111111111111111111111111111111 "Developer ID Application: Someone (ABCDE12345)"
     2 valid identities found
`

func TestPickSigningIdentity(t *testing.T) {
	cases := []struct {
		name, out, env, wantID, wantKind string
	}{
		{"prefers Developer ID", twoIdentities, "", "1111111111111111111111111111111111111111", "Developer ID Application"},
		{"Apple Development only", `  1) 9BEB1EC9A86FBC3CB29E47C1746D310FBF9967D1 "Apple Development: Someone (ABCDE12345)"
     1 valid identities found
`, "", "9BEB1EC9A86FBC3CB29E47C1746D310FBF9967D1", "Apple Development"},
		{"none found", "     0 valid identities found\n", "", "", ""},
		{"other kinds ignored", `  1) 2222222222222222222222222222222222222222 "Mac Developer Installer: Someone"
     1 valid identities found
`, "", "", ""},
		{"env override", twoIdentities, "ABCD", "ABCD", "VIBE_CODESIGN_IDENTITY"},
		{"env disables", twoIdentities, "-", "", ""},
	}
	for _, c := range cases {
		id, kind := pickSigningIdentity(c.out, c.env)
		if id != c.wantID || kind != c.wantKind {
			t.Errorf("%s: got (%q, %q); want (%q, %q)", c.name, id, kind, c.wantID, c.wantKind)
		}
	}
}
