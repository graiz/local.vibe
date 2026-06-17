package cmd

import "testing"

func TestDoctorHasLoopback(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  bool
	}{
		{"loopback present", []string{"::1", "127.0.0.1"}, true},
		{"only loopback", []string{"127.0.0.1"}, true},
		{"no loopback", []string{"192.168.1.10", "::1"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		if got := hasLoopback(tc.addrs); got != tc.want {
			t.Errorf("%s: hasLoopback(%v) = %v, want %v", tc.name, tc.addrs, got, tc.want)
		}
	}
}
