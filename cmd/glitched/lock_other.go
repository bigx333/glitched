//go:build !unix

package main

import "os"

func lock(string) (*os.File, error) { return nil, nil }
