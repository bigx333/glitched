//go:build !unix

package client

import "testing"

// holdEngineLock does nothing: an engine here takes no lock.
func holdEngineLock(*testing.T, string) {}
