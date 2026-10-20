package main

import "testing"

// The AppKit call resolves and runs; without a finger on a Force Touch
// trackpad, it plays nothing.
func TestHaptic(t *testing.T) {
	if hapticManager == 0 {
		t.Fatal("NSHapticFeedbackManager not found")
	}
	haptic()
}
