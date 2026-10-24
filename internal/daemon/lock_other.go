//go:build !unix

package daemon

import "os"

func lock(string) (*os.File, error) { return nil, nil }

func syncDir(string) error { return nil }
