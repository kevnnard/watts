//go:build !unix

package discover

import (
	"net"
	"syscall"
)

// setMulticastInterface leaves the system's choice: multicast goes out of
// the default route's interface.
func setMulticastInterface(syscall.RawConn, net.IP) error { return nil }
