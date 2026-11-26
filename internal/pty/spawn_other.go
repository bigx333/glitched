//go:build !unix && !windows

package pty

import "glitched/internal/provider"

func startProc(string, []string, int, int) (proc, error) {
	return nil, provider.ErrUnsupported
}
