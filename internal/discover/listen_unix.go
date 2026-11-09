//go:build unix

package discover

import (
	"net"
	"syscall"
)

func setMulticastInterface(c syscall.RawConn, ip net.IP) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, [4]byte(ip.To4()))
	})
	if err != nil {
		return err
	}
	return serr
}
