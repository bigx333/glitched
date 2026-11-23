package cliupdate

import "testing"

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		installed, latest string
		newer, ok         bool
	}{
		{"2.1.323", "2.1.330", true, true},
		{"2.1.330", "2.1.330", false, true},
		{"2.1.330", "2.1.323", false, true},
		{"1.9.9", "1.10.0", true, true},
		{"v0.170.0", "0.171.0", true, true},
		{"1.0.0+build.5", "1.0.0+build.6", false, true},
		{"1.0.0-alpha", "1.0.0-alpha.1", true, true},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", true, true},
		{"1.0.0-alpha.beta", "1.0.0-beta", true, true},
		{"1.0.0-beta", "1.0.0-beta.2", true, true},
		{"1.0.0-beta.2", "1.0.0-beta.11", true, true},
		{"1.0.0-beta.11", "1.0.0-rc.1", true, true},
		{"1.0.0-rc.1", "1.0.0", true, true},
		{"1.0.0", "1.0.0-rc.1", false, true},
		{"1.0.0", "1.0.1-0", true, true},
		{"1.0.0", "1.0.1+build.007", true, true},
		{"1.0.0", "02.0.0", false, false},
		{"1.0.0", "2.0.0+", false, false},
		{"1.0.0", "2.0.0+???", false, false},
		{"1.0.0", "2.0.0+a..b", false, false},
		{"1.0.0", "2.0.0-01", false, false},
		{"1.0.0", "18446744073709551616.0.0", false, false},
		{"1.0.0", "2.0.0-018446744073709551616", false, false},
		{"2.0.0-99999999999999999999", "2.0.0-100000000000000000000", false, false},
		{"2.0.0-18446744073709551614", "2.0.0-18446744073709551615", true, true},
		{"2.1.330 (Claude Code)", "2.1.331", false, false},
		{"1.2", "1.3.0", false, false},
		{"1.2.3", "latest", false, false},
		{"1.2.3", "1.2.4-", false, false},
		{"", "1.2.3", false, false},
		{"1.2.3", "", false, false},
	} {
		newer, ok := Newer(c.installed, c.latest)
		if newer != c.newer || ok != c.ok {
			t.Errorf("Newer(%q, %q) = %v, %v; want %v, %v", c.installed, c.latest, newer, ok, c.newer, c.ok)
		}
	}
}
