//go:build windows

package winutil

import "testing"

func TestParseTasklistCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"chrome", `"chrome.exe","12345","Console","1","123,456 K"`, "chrome.exe"},
		{"node", `"node.exe","9876","Console","1","45,678 K"`, "node.exe"},
		{"empty", ``, ""},
		{"info-no-tasks", `INFO: No tasks are running which match the specified criteria.`, ""},
		// Image names with spaces are quoted; csv reader handles it.
		{"spaces", `"My App.exe","555","Console","1","8,000 K"`, "My App.exe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTasklistCSV(tc.in)
			if got != tc.want {
				t.Errorf("ParseTasklistCSV(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
