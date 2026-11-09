package discover

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// reply is one datagram a fake device sends back.
type reply struct {
	from string
	data []byte
}

// fakeNet answers datagrams with respond, never touching the network.
type fakeNet struct {
	respond func(ifc Interface, to *net.UDPAddr, data []byte) []reply

	mu     sync.Mutex
	writes []*net.UDPAddr
}

func (n *fakeNet) listen(ifc Interface) (PacketConn, error) {
	return &fakeConn{net: n, ifc: ifc, in: make(chan reply, 256), closed: make(chan struct{})}, nil
}

func (n *fakeNet) sentTo() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, a := range n.writes {
		out = append(out, a.String())
	}
	return out
}

type fakeConn struct {
	net    *fakeNet
	ifc    Interface
	in     chan reply
	closed chan struct{}
	once   sync.Once
}

func (c *fakeConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	to := addr.(*net.UDPAddr)
	c.net.mu.Lock()
	c.net.writes = append(c.net.writes, to)
	c.net.mu.Unlock()
	if c.net.respond != nil {
		rs := c.net.respond(c.ifc, to, append([]byte(nil), p...))
		// Deliver in the background, in order, so a flood larger than the
		// buffer cannot block the sender.
		go func() {
			for _, r := range rs {
				select {
				case c.in <- r:
				case <-c.closed:
					return
				}
			}
		}()
	}
	return len(p), nil
}

func (c *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case r := <-c.in:
		return copy(p, r.data), &net.UDPAddr{IP: net.ParseIP(r.from), Port: 40000}, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakeConn) SetReadDeadline(time.Time) error { return nil }

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// fakeHTTP serves fixed bodies by URL; "hang" blocks until the request is
// cancelled; anything else is refused.
type fakeHTTP struct {
	bodies  map[string]string
	hangAll bool // every request hangs until cancelled

	mu                    sync.Mutex
	urls                  []string
	inFlight, maxInFlight int
}

func (h *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	h.mu.Lock()
	h.urls = append(h.urls, u)
	h.inFlight++
	h.maxInFlight = max(h.maxInFlight, h.inFlight)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.inFlight--
		h.mu.Unlock()
	}()
	body, ok := h.bodies[u]
	if h.hangAll {
		body, ok = "hang", true
	}
	if !ok {
		return nil, errors.New("connection refused")
	}
	if body == "hang" {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(bytes.NewBufferString(body))}, nil
}

func testIfaces() ([]Interface, error) {
	return []Interface{
		{Name: "en0", IP: net.IPv4(192, 0, 2, 15).To4(), Mask: net.CIDRMask(24, 32)},
		{Name: "en1", IP: net.IPv4(198, 51, 100, 5).To4(), Mask: net.CIDRMask(24, 32)},
	}, nil
}

func fakeOpts(n *fakeNet, h *fakeHTTP, timeout time.Duration) Options {
	return Options{Timeout: timeout, Interfaces: testIfaces, ListenUDP: n.listen, HTTP: h, Now: time.Now}
}
