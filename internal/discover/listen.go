package discover

import (
	"context"
	"net"
	"syscall"
)

// listenUDP opens a UDP socket bound to the interface address, with
// multicast sent out of that interface rather than the default route's.
func listenUDP(ifc Interface) (PacketConn, error) {
	ip := ifc.IP.To4()
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return setMulticastInterface(c, ip)
	}}
	return lc.ListenPacket(context.Background(), "udp4", net.JoinHostPort(ip.String(), "0"))
}
