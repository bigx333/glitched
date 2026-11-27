package main

import "golang.org/x/sys/unix"

// systemHasLiquidGlass is whether this Mac's macOS has Liquid Glass.
func systemHasLiquidGlass() bool {
	v, err := unix.Sysctl("kern.osproductversion")
	return err == nil && liquidGlassVersion(v)
}
