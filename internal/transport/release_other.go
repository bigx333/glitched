//go:build !unix

package transport

import "net"

func release(ln net.Listener) net.Listener { return ln }
