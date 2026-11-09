package sensor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/discover"
)

var queryAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// noAnswerCase is a query that gets no answer.
type noAnswerCase struct {
	name   string
	ctx    context.Context
	target NetworkTarget
	opts   NetworkOptions
	want   error
}

// checkNoAnswer wants each query unknown, not proof of off, and back
// within the timeout.
func checkNoAnswer(t *testing.T, tests []noAnswerCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			obs, err := QueryNetwork(tt.ctx, tt.target, tt.opts)
			if !errors.Is(err, tt.want) || obs.State != discover.StateUnknown {
				t.Errorf("got %+v, %v; want unknown, %v", obs, err, tt.want)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}

// checkInvalidTargets wants each target rejected as unknown with nothing
// sent for it.
func checkInvalidTargets(t *testing.T, targets []NetworkTarget) {
	t.Helper()
	sent := 0
	opts := NetworkOptions{
		HTTP:      doerFunc(func(*http.Request) (*http.Response, error) { sent++; return nil, errors.New("sent") }),
		ListenUDP: func() (discover.PacketConn, error) { sent++; return nil, errors.New("sent") },
	}
	for _, tt := range targets {
		obs, err := QueryNetwork(context.Background(), tt, opts)
		if !errors.Is(err, ErrInvalidTarget) || obs.State != discover.StateUnknown {
			t.Errorf("%+v: got %+v, %v; want unknown, invalid target", tt, obs, err)
		}
	}
	if sent != 0 {
		t.Errorf("%d queries sent for invalid targets", sent)
	}
}
