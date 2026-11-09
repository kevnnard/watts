package tui

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/discover"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/store"
)

// Network fixtures (S20): generic names and RFC 5737 addresses only.

const (
	tvID  = "uuid:6f1d2c40-8b3e-4a57-9c12-0e4b7a9d3f21"
	ps5ID = "ps:a1b2c3d4e5f6"
)

func tvItem() config.Item {
	return config.Item{Name: "TV", Mode: config.ModeNetwork, Source: config.SourceMac, Probe: config.ProbeSamsung,
		Host: "192.0.2.23", ID: tvID, Watts: 63, StandbyW: 0.5, Quantity: 1, Brand: "Samsung"}
}

func ps5Item() config.Item {
	return config.Item{Name: "PS5", Mode: config.ModeNetwork, Source: config.SourceLinux, Probe: config.ProbePS5,
		Host: "192.0.2.31", ID: ps5ID, Watts: 217, Quantity: 1}
}

// fixNetConfig is fixConfig plus a Samsung TV asked by the Mac and a PS5
// asked by the Linux PC.
func fixNetConfig() *config.Config {
	c := fixConfig()
	c.Items = append(c.Items, tvItem(), ps5Item())
	return c
}

// fixNetReport is fixReport with the TV in standby (fresh reading) and the
// PS5 without a fresh reading since 18:40, so today and the bill have
// unknown PS5 time.
func fixNetReport() energy.Report {
	r := fixReport(false)
	r.Items = append(r.Items,
		energy.ItemNow{Name: "TV", Mode: config.ModeNetwork, Watts: 0.5, On: true, LastTS: fixNow.Add(-20 * time.Second),
			Network: &energy.NetworkState{State: store.NetworkStandby, TS: fixNow.Add(-20 * time.Second), Host: "192.0.2.23", Verified: true}},
		energy.ItemNow{Name: "PS5", Mode: config.ModeNetwork, Offline: true, LastTS: time.Date(2026, 11, 8, 18, 40, 0, 0, bogota)},
	)
	r.TotalWatts += 0.5
	add := func(t *energy.Totals, tv, ps5, unknown float64) {
		for _, x := range []energy.ItemTotal{{Name: "TV", Wh: tv}, {Name: "PS5", Wh: ps5, UnknownSeconds: unknown}} {
			x.Cost = energy.Cost(x.Wh, fixPrice)
			t.Items = append(t.Items, x)
			t.Wh += x.Wh
			t.Cost += x.Cost
		}
		t.Incomplete = true
	}
	add(&r.Today, 12, 400, 5700)  // 1 h 35 min unknown
	add(&r.Bill, 90, 2100, 11400) // 3 h 10 min unknown
	return r
}

// ps5Config is fixConfig plus the PS5, so the scan finds it added.
func ps5Config() *config.Config {
	c := fixConfig()
	c.Items = append(c.Items, ps5Item())
	return c
}

// tvAtConfig has the TV saved without an ID: the scan's TV is only a maybe.
func tvAtConfig() *config.Config {
	c := fixNetConfig()
	c.Items[5].ID = ""
	return c
}

var errNoIface = errors.New("discover: no IPv4 network interface is up")

// fixScan is the 01-panel artboard's scan: generic devices on en0.
func fixScan() NetScan {
	return NetScan{
		Interfaces: []discover.Interface{{Name: "en0", IP: net.IPv4(192, 0, 2, 10), Mask: net.CIDRMask(24, 32)}},
		Devices: []discover.Device{
			{ID: tvID, Name: "Samsung TV", Brand: "Samsung", Kind: "tv", Host: "192.0.2.23", Protocol: "samsung, ssdp", Probe: "samsung", State: "unknown",
				Raw: map[string]string{"ids": tvID + ",mac:02:00:5e:10:00:23"}},
			{ID: ps5ID, Name: "PS5", Brand: "Sony", Model: "PlayStation 5", Kind: "console", Host: "192.0.2.31", Protocol: "ddp", Probe: "ps5", State: "standby",
				Raw: map[string]string{"ids": ps5ID}},
			{ID: "uuid:0b7e9a14-2c6f-4d83-a5e1-93f0c4d2b876", Name: "Router", Kind: "router", Host: "192.0.2.1", Protocol: "ssdp", State: "unknown"},
			{ID: "mdns:9c41e07a5b2f", Name: "Living room speaker", Brand: "Google", Model: "Nest Mini", Kind: "speaker", Host: "192.0.2.57", Protocol: "mdns", State: "unknown"},
			{ID: "mac:02:00:5e:10:00:47", Kind: "other", Host: "192.0.2.47", Protocol: "mdns", State: "unknown"},
		},
	}
}

// fakeScan is an injectable Scanner. With block set, each scan waits for
// it or for its context.
type fakeScan struct {
	mu    sync.Mutex
	calls int
	res   NetScan
	err   error
	block chan struct{}
	ctxs  []context.Context
}

func (f *fakeScan) scan(ctx context.Context) (NetScan, error) {
	f.mu.Lock()
	f.calls++
	f.ctxs = append(f.ctxs, ctx)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return NetScan{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res, f.err
}

func (f *fakeScan) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
