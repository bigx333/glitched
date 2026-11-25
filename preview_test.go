package main

import "testing"

func TestIsLocal(t *testing.T) {
	local := []string{
		"localhost",
		"localhost:5173",
		"LOCALHOST.:3000",
		"app.localhost",
		"mybox.local",
		"mybox.local.",
		"127.0.0.1",
		"127.1.2.3:8080",
		"0.0.0.0:3000",
		"10.0.0.1",
		"172.16.0.1:3000",
		"172.31.255.254",
		"192.168.1.50:8080",
		"169.254.1.1",
		"[::1]:8080",
		"[fe80::1]:8080",
		"[fc00::1]:8080",
		"[::]:8080",
		"[::ffff:127.0.0.1]:8080",
	}
	for _, raw := range local {
		if !isLocal(raw) {
			t.Errorf("isLocal(%q) = false, want true", raw)
		}
	}

	public := []string{
		"10.com",
		"192.168.com",
		"127.evil.example",
		"127.0.0.1@example.com",
		"localhost@example.com",
		"http://127.0.0.1:80@example.com/a",
		"172.32.0.1",
		"172.15.255.255",
		"example.com",
	}
	for _, raw := range public {
		if isLocal(raw) {
			t.Errorf("isLocal(%q) = true, want false", raw)
		}
	}
}

func TestNormalizeURLLocalHardening(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"localhost:5173", "http://localhost:5173"},
		{"127.0.0.1:3000/app", "http://127.0.0.1:3000/app"},
		{"172.16.0.5:8080", "http://172.16.0.5:8080"},
		{"[::1]:8080", "http://[::1]:8080"},
		{"10.com", "https://10.com"},
		{"192.168.com", "https://192.168.com"},
		{"127.evil.example", "https://127.evil.example"},
		{"127.0.0.1@example.com", "https://127.0.0.1@example.com"},
		{"https://localhost:5173", "https://localhost:5173"},
		{"  example.com  ", "https://example.com"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeURL(tc.in); got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
