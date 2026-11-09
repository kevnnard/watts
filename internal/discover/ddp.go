package discover

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
)

// PlayStation Device Discovery Protocol (S20): PS5 on UDP 9302, PS4 on 987.
// The console answers "HTTP/1.1 200 Ok" when on and "HTTP/1.1 620 Server
// Standby" in rest mode; an off console does not answer.
const (
	ddpPortPS5 = 9302
	ddpPortPS4 = 987
	ddpSearch  = "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:%s\n"
)

// ddpVersions is the protocol version each console's port expects.
var ddpVersions = map[int]string{ddpPortPS5: "00030010", ddpPortPS4: "00020020"}

func ddpRequest(port int) []byte {
	return []byte(fmt.Sprintf(ddpSearch, ddpVersions[port]))
}

func parseDDP(b []byte, host string) (found, error) {
	first, h, err := parseHTTPish(b)
	if err != nil {
		return found{}, err
	}
	code, err := statusCode(first)
	if err != nil {
		return found{}, err
	}
	state := StateUnknown
	switch code {
	case 200:
		state = StateOn
	case 620:
		state = StateStandby
	}
	typ := h["host-type"]
	f := found{prio: prioDDP, Device: Device{
		Name: h["host-name"], Brand: "Sony", Model: typ, Kind: "console",
		Host: host, Protocol: "ddp", State: state, Raw: map[string]string{},
	}}
	if id := strings.ToLower(h["host-id"]); id != "" {
		f.ID = "ps:" + id
	}
	if strings.EqualFold(typ, "PS5") {
		f.Probe = ProbePS5
	}
	for _, k := range []string{"host-id", "host-type", "host-request-port", "system-version", "device-discovery-protocol-version", "running-app-name", "running-app-titleid"} {
		if v := h[k]; v != "" {
			f.Raw["ddp."+k] = v
		}
	}
	return f, nil
}

// ddp broadcasts the PlayStation search on one interface.
func (s *scanner) ddp(ctx context.Context, ifc Interface, conn PacketConn, _ *sync.WaitGroup) {
	bcast := ifc.Broadcast()
	if bcast == nil {
		conn.Close()
		return
	}
	for _, port := range []int{ddpPortPS5, ddpPortPS4} {
		s.resend(ctx, conn, ddpRequest(port), &net.UDPAddr{IP: bcast, Port: port})
	}
	s.listen(ctx, conn, func(b []byte, from net.IP) {
		f, err := parseDDP(b, from.String())
		if err != nil || (f.ID == "" && !strings.HasPrefix(strings.ToUpper(f.Model), "PS")) {
			return
		}
		s.add(f)
	})
}
