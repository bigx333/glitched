//go:build !darwin

package main

// systemHasLiquidGlass is false: Liquid Glass is macOS's.
func systemHasLiquidGlass() bool { return false }
