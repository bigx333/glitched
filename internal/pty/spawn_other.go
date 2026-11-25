//go:build !unix

package pty

import "glitched/internal/provider"

type proc interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait() (int, error)
	Close() error
}

func startProc(string, []string, int, int) (proc, error) {
	return nil, provider.ErrUnsupported
}
