package agentversion

import "testing"

func TestOlder(t *testing.T) {
	for _, tc := range []struct {
		host, latest string
		want         bool
	}{
		{"0.2.9", "0.2.10", true},
		{"0.2.10", "0.2.10", false},
		{"0.2.11", "0.2.10", false},
		{"0.1.99", "0.2.0", true},
		{"0.10.0", "0.9.9", false},
		{"1.0.0", "0.99.99", false},
		{"0.99.99", "1.0.0", true},
		{"00.2.9", "0.2.10", true},
		{"", "0.2.10", false},
		{"v0.2.9", "0.2.10", false},
		{"0.2.9-rc1", "0.2.10", false},
		{"0.2.3.2", "0.2.10", false},
		{"0.2", "0.2.10", false},
		{"dev", "0.2.10", false},
		{"0.2.9", "", false},
		{"0.2.9", "latest", false},
		{"0.2.9", "0.2.x", false},
		{" 0.2.9", "0.2.10", false},
		{"0.2.9", "0.2.9999999999", false},
		{"0.2.９", "0.2.10", false},
	} {
		if got := Older(tc.host, tc.latest); got != tc.want {
			t.Errorf("Older(%q, %q) = %v, want %v", tc.host, tc.latest, got, tc.want)
		}
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"0.2.10": true, "10.20.30": true, "0.0.0": true,
		"": false, "v0.2.10": false, "0.2": false, "0.2.10.1": false, "0.2.10 ": false,
		"0..1": false, "0.2.-1": false, "0.2.+1": false, "a.b.c": false, "1234567890.0.0": false,
	} {
		if got := Valid(s); got != want {
			t.Errorf("Valid(%q) = %v, want %v", s, got, want)
		}
	}
}
