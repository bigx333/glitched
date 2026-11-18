//go:build !unix

package daemon

func lock(string) (func() error, error) { return nil, nil }
