package main

import "testing"

func TestLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"localhost", true},
		{"LocalHost", true},
		{"app.localhost", true},
		{"", false},
		{"0.0.0.0", false},
		{"::", false},
		{"192.168.1.5", false},
		{"10.0.0.1", false},
		{"example.com", false},
		{"lvh.me", false},
	}
	for _, tc := range tests {
		if got := loopbackHost(tc.host); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
