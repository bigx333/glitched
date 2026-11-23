//go:build !darwin

package guard

// confine leaves the program as it is: only macOS has a sandbox for a
// program this guard can start, so elsewhere a denied folder is kept only
// from the paths a run names.
func confine(program string, args, _ []string) (string, []string) { return program, args }
