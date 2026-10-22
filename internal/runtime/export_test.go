package runtime

import "time"

// SetCallTimeout shortens the deadline on provider calls and returns what
// puts it back.
func SetCallTimeout(d time.Duration) func() {
	old := callTimeout
	callTimeout = d
	return func() { callTimeout = old }
}
