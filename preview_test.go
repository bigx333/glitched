package main

import "testing"

// Addresses on this machine and the local network load over http; names
// that only look like addresses don't.
func TestIsLocal(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:5173":        true,
		"http://app.localhost":         true,
		"http://printer.local/":        true,
		"http://127.0.0.1:3000/app":    true,
		"http://0.0.0.0:8080":          true,
		"http://[::1]:5173":            true,
		"http://10.0.0.2":              true,
		"http://172.16.4.1:8000":       true,
		"http://192.168.1.20:8080":     true,
		"http://169.254.10.1":          true,
		"http://10.com":                false,
		"http://192.168.com/x":         false,
		"http://172.32.0.1":            false,
		"https://example.com":          false,
		"http://8.8.8.8":               false,
		"http://Localhost:3000":        true,
		"http://LOCALHOST":             true,
		"http://localhost.:5173":       true,
		"http://printer.local./":       true,
		"http://[fe80::1%25eth0]:3000": true,
		"http://user@localhost:3000":   true,
		"http://user@example.com":      false,
	} {
		if got := isLocal(url); got != want {
			t.Errorf("isLocal(%q) = %v, want %v", url, got, want)
		}
	}
}
