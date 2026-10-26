package sysweb

import "testing"

// A page whose process crashed or was ended loads again; one only
// unresponsive keeps its state, as it may come back.
func TestReloads(t *testing.T) {
	for reason, want := range map[string]bool{"crashed": true, "browser-exited": true, "killed": true, "unresponsive": false} {
		if got := reloads(reason); got != want {
			t.Errorf("reloads(%q) = %v, want %v", reason, got, want)
		}
	}
}
