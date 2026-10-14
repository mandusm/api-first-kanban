package config

import "testing"

// TestIsLoopbackAddr pins the loopback-only bind policy: only 127.0.0.0/8, ::1
// and localhost are accepted (v1 has no auth).
func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:18080": true,
		"127.0.0.5:80":    true,
		"localhost:18080": true,
		"[::1]:18080":     true,
		"0.0.0.0:18080":   false,
		"[::]:18080":      false,
		":18080":          false,
		"192.0.2.10:80":   false,
		"example.com:80":  false,
		"127.0.0.1":       false, // no port
	}
	for addr, want := range cases {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
