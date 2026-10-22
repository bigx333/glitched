//go:build !unix

package daemon

import "os"

func lock(string) (*os.File, error) { return nil, nil }
