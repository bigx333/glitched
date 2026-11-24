//go:build !windows && !darwin

package runtime

import "errors"

// cloneTree is only for APFS: elsewhere copyTree copies each path.
func cloneTree(from, to string) error { return errors.ErrUnsupported }
