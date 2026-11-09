package sensor

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/discover"
)

const (
	ddpPort     = 9302
	ddpSearch   = "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n"
	ddpMaxReply = 2048
)

// queryPS5 sends the DDP search to ip, again every third of the timeout,
// until a PS5 answer from ip arrives. Datagrams from any other sender are
// ignored; a malformed one from ip is remembered and the wait goes on.
func queryPS5(ctx context.Context, ip net.IP, listen func() (discover.PacketConn, error)) (answer, error) {
	if listen == nil {
		listen = func() (discover.PacketConn, error) { return net.ListenPacket("udp4", ":0") }
	}
	conn, err := listen()
	if err != nil {
		return answer{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	every := time.Until(deadline) / 3
	to := &net.UDPAddr{IP: ip, Port: ddpPort}
	last := ErrNoAnswer
	buf := make([]byte, ddpMaxReply)
	for time.Now().Before(deadline) {
		if _, err := conn.WriteTo([]byte(ddpSearch), to); err != nil {
			if ctx.Err() != nil {
				return answer{}, last
			}
			return answer{}, noAnswer(err)
		}
		wait := time.Now().Add(every)
		if wait.After(deadline) {
			wait = deadline
		}
		_ = conn.SetReadDeadline(wait)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				if ctx.Err() != nil {
					return answer{}, last
				}
				if isTimeout(err) {
					break
				}
				return answer{}, noAnswer(err)
			}
			if u, ok := from.(*net.UDPAddr); !ok || !u.IP.Equal(ip) {
				continue
			}
			a, err := parseDDPState(buf[:n])
			if err != nil {
				last = err
				continue
			}
			return a, nil
		}
	}
	return answer{}, last
}

// parseDDPState reads a DDP answer: "HTTP/1.1 200 Ok" (on) or "HTTP/1.1 620
// Server Standby" (rest mode), then "key:value" lines with host-type PS5.
func parseDDPState(b []byte) (answer, error) {
	lines := strings.Split(string(b), "\n")
	f := strings.Fields(lines[0])
	if len(f) < 2 || !strings.HasPrefix(f[0], "HTTP/") {
		return answer{}, fmt.Errorf("%w: not a DDP answer", ErrMalformed)
	}
	code, err := strconv.Atoi(f[1])
	if err != nil {
		return answer{}, fmt.Errorf("%w: DDP status %q", ErrMalformed, f[1])
	}
	h := map[string]string{}
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ":"); ok {
			h[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if !strings.EqualFold(h["host-type"], "PS5") {
		return answer{}, fmt.Errorf("%w: host-type %q is not PS5", ErrMalformed, h["host-type"])
	}
	a := answer{name: h["host-name"], model: h["host-type"]}
	if id, ok := normalizeID("ps:" + h["host-id"]); ok {
		a.ids = append(a.ids, id)
	}
	switch code {
	case 200:
		a.state = discover.StateOn
	case 620:
		a.state = discover.StateStandby
	}
	return a, nil
}
