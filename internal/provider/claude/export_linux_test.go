package claude

import (
	"sync"
	"testing"
)

// WithoutScopes has runs started for the length of t find their processes
// by their marker alone, as where there is no user systemd manager.
func WithoutScopes(t *testing.T) {
	old := scopes
	scopes = func() slice { return slice{} }
	t.Cleanup(func() { scopes = old })
}

// RedetectScopes has the next run started look for a user systemd manager
// anew, under t's environment, and the first run after t look again.
func RedetectScopes(t *testing.T) {
	old := scopes
	scopes = sync.OnceValue(func() slice { s, _ := userSlice(); return s })
	t.Cleanup(func() { scopes = old })
}
