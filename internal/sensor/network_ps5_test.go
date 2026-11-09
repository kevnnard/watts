package sensor

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/discover"
)

const (
	psHost = "192.0.2.20"
	psID   = "ps:a1b2c3d4e5f6"
)

// datagram is one fake console answer.
type datagram struct{ from, data string }

// fakeUDP answers each sent datagram with respond, never touching the network.
type fakeUDP struct {
	respond func(to string) []datagram

	mu       sync.Mutex
	sent     []string
	deadline time.Time
	in       chan datagram
	closed   chan struct{}
	once     sync.Once
}

func newFakeUDP(respond func(to string) []datagram) *fakeUDP {
	return &fakeUDP{respond: respond, in: make(chan datagram, 64), closed: make(chan struct{})}
}

func (c *fakeUDP) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	c.mu.Lock()
	c.sent = append(c.sent, addr.String()+" "+string(p))
	c.mu.Unlock()
	if c.respond != nil {
		for _, d := range c.respond(addr.String()) {
			c.in <- d
		}
	}
	return len(p), nil
}

func (c *fakeUDP) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	wait := time.Until(c.deadline)
	c.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case d := <-c.in:
		return copy(p, d.data), &net.UDPAddr{IP: net.ParseIP(d.from), Port: 9302}, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-timer.C:
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (c *fakeUDP) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

func (c *fakeUDP) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func ps5At(conn *fakeUDP, timeout time.Duration) NetworkOptions {
	return NetworkOptions{Timeout: timeout, Now: func() time.Time { return queryAt },
		ListenUDP: func() (discover.PacketConn, error) { return conn, nil }}
}

func ddpReply(status, typ, id string) string {
	s := "HTTP/1.1 " + status + "\r\nhost-type:" + typ + "\r\nhost-name:Console\r\n"
	if id != "" {
		s += "host-id:" + id + "\r\n"
	}
	return s
}

// answers sends each reply once, to the first search only.
func answers(replies ...datagram) func(string) []datagram {
	var once sync.Once
	return func(string) (out []datagram) {
		once.Do(func() { out = replies })
		return out
	}
}

func TestQueryNetworkPS5State(t *testing.T) {
	tests := []struct {
		name    string
		replies []datagram
		id      string
		state   string
	}{
		{"on", []datagram{{psHost, ddpReply("200 Ok", "PS5", "A1B2C3D4E5F6")}}, "PS:A1B2C3D4E5F6", discover.StateOn},
		{"standby", []datagram{{psHost, ddpReply("620 Server Standby", "PS5", "a1b2c3d4e5f6")}}, psID, discover.StateStandby},
		{"spoofed sender ignored", []datagram{
			{"192.0.2.99", ddpReply("200 Ok", "PS5", "a1b2c3d4e5f6")},
			{psHost, ddpReply("620 Server Standby", "PS5", "a1b2c3d4e5f6")},
		}, psID, discover.StateStandby},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newFakeUDP(answers(tt.replies...))
			obs, err := QueryNetwork(context.Background(), NetworkTarget{Probe: discover.ProbePS5, Host: psHost, ID: tt.id}, ps5At(conn, time.Second))
			if err != nil {
				t.Fatal(err)
			}
			want := NetworkObservation{At: queryAt, State: tt.state, Host: psHost, ID: psID, Name: "Console", Model: "PS5", Verified: true}
			if obs != want {
				t.Errorf("got %+v\nwant %+v", obs, want)
			}
			if want := psHost + ":9302 SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n"; len(conn.sent) == 0 || conn.sent[0] != want {
				t.Errorf("sent %q, want %q first", conn.sent, want)
			}
		})
	}
}

func TestQueryNetworkPS5Unknown(t *testing.T) {
	tests := []struct {
		name  string
		reply datagram
		id    string
		want  error
	}{
		{"other status", datagram{psHost, ddpReply("500 Error", "PS5", "a1b2c3d4e5f6")}, psID, ErrNoState},
		{"not a ps5", datagram{psHost, ddpReply("200 Ok", "PS4", "a1b2c3d4e5f6")}, psID, ErrMalformed},
		{"garbage", datagram{psHost, "hello"}, psID, ErrMalformed},
		{"other console", datagram{psHost, ddpReply("200 Ok", "PS5", "ffffffffffff")}, psID, ErrIdentityMismatch},
		{"identity not reported", datagram{psHost, ddpReply("200 Ok", "PS5", "")}, psID, ErrIdentityUnknown},
		{"only a spoofed sender", datagram{"192.0.2.99", ddpReply("200 Ok", "PS5", "a1b2c3d4e5f6")}, psID, ErrNoAnswer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newFakeUDP(answers(tt.reply))
			obs, err := QueryNetwork(context.Background(), NetworkTarget{Probe: discover.ProbePS5, Host: psHost, ID: tt.id}, ps5At(conn, 100*time.Millisecond))
			if !errors.Is(err, tt.want) || obs.State != discover.StateUnknown || obs.Verified {
				t.Errorf("got %+v, %v; want unknown, %v", obs, err, tt.want)
			}
		})
	}
}

// No answer is unknown, not proof of off, and comes back within the timeout.
func TestQueryNetworkPS5NoAnswer(t *testing.T) {
	udp := func() (discover.PacketConn, error) { return newFakeUDP(nil), nil }
	ps := NetworkTarget{Probe: discover.ProbePS5, Host: psHost, ID: psID}
	checkNoAnswer(t, []noAnswerCase{
		{"ps5 timeout", context.Background(), ps, NetworkOptions{ListenUDP: udp, Timeout: 50 * time.Millisecond}, ErrNoAnswer},
		{"ps5 canceled", canceled(), ps, NetworkOptions{ListenUDP: udp}, context.Canceled},
	})
}

// A target must name one console: no hostless or broadcast query, no ID
// from another device family.
func TestQueryNetworkPS5InvalidTarget(t *testing.T) {
	checkInvalidTargets(t, []NetworkTarget{
		{Probe: discover.ProbePS5, ID: psID},
		{Probe: discover.ProbePS5, Host: "255.255.255.255", ID: psID},
		{Probe: discover.ProbePS5, Host: "239.255.255.250"},
		{Probe: discover.ProbePS5, Host: psHost, ID: "uuid:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"},
	})
}
