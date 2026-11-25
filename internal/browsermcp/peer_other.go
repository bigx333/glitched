//go:build !darwin && !linux

package browsermcp

import "net"

func verifyPeer(net.Conn) error { return nil }
