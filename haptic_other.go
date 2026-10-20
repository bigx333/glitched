//go:build !darwin

package main

// haptic does nothing where there is no trackpad haptics.
func haptic() {}

// hapticBump does nothing where there is no trackpad haptics.
func hapticBump() {}
