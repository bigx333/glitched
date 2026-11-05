package claude

import "testing"

// WithoutScopes has runs started for the length of t find their processes
// by their marker alone, as where there is no user systemd manager.
func WithoutScopes(t *testing.T) {
	old := scopes
	scopes = func() slice { return slice{} }
	t.Cleanup(func() { scopes = old })
}
