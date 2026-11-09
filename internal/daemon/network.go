package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/discover"
	"github.com/kevnnard/watts/internal/sensor"
	"github.com/kevnnard/watts/internal/store"
)

// Network items (S20, network panel S2–S3). Every tick the daemon asks each
// network item it owns for its power state and stores one reading per
// item: on, standby or unknown. Unknown is stored too, so a gap in the
// history means nobody asked, and it is never stored as off.
//
// An item with an id is only counted when the device at its address
// reports that id. When it does not answer there, or another device does,
// the daemon looks for it again: a bounded network scan, at most once per
// ScanEvery for all items, accepted only when exactly one other address
// reports the id (host and mDNS ids prove nothing) and that address then
// confirms it in a query. The new address is kept in memory only; the
// config file is never rewritten. An item without an id never moves.

const (
	// DefaultScanEvery is the shortest time between two network scans.
	DefaultScanEvery = 10 * time.Minute
	// DefaultParallel bounds the queries of one poll at once.
	DefaultParallel = 4
)

// Network polls network items. Poll must not be called concurrently.
type Network struct {
	Items []config.Item // network items, in config order
	// Query asks one device (sensor.QueryNetwork with the real network).
	Query func(context.Context, sensor.NetworkTarget) (sensor.NetworkObservation, error)
	// Scan lists the devices on the network (discover.Scan); nil never
	// looks for a lost item.
	Scan      func(context.Context) ([]discover.Device, error)
	Now       func() time.Time // time.Now when nil
	ScanEvery time.Duration    // DefaultScanEvery when zero
	Parallel  int              // DefaultParallel when zero

	moved    map[string]string // item name → address it answers at now, when not its configured one
	logged   map[string]string // item name → the state line last logged
	nextScan time.Time
}

// result is one item's query.
type result struct {
	obs sensor.NetworkObservation
	err error
}

// Poll asks every item once and returns one reading per item, in config
// order. A cancelled poll returns nil: its readings are not known.
func (n *Network) Poll(ctx context.Context, log io.Writer) []store.NetworkReading {
	if n.moved == nil {
		n.moved, n.logged = map[string]string{}, map[string]string{}
	}
	res := make([]result, len(n.Items))
	par := n.Parallel
	if par <= 0 {
		par = DefaultParallel
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i, it := range n.Items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			res[i].obs, res[i].err = n.Query(ctx, sensor.NetworkTarget{Probe: it.Probe, Host: n.host(it), ID: it.ID})
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}

	before := make([]string, len(n.Items)) // notes logged before an item's state
	after := make([]string, len(n.Items))  // and after it
	var lost []int
	for i, it := range n.Items {
		if it.ID != "" && n.Scan != nil && lostAt(res[i].err) {
			lost = append(lost, i)
		}
	}
	if devs, ok := n.scan(ctx, log, len(lost) > 0); ok {
		for _, i := range lost {
			r, moved, note := n.find(ctx, n.Items[i], res[i].obs.Host, devs)
			if moved {
				res[i], before[i] = r, note
			} else {
				after[i] = note
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}

	out := make([]store.NetworkReading, len(n.Items))
	now := n.now()
	for i, it := range n.Items {
		obs, err := res[i].obs, res[i].err
		ts := obs.At
		if ts.IsZero() {
			ts = now
		}
		x := store.NetworkReading{TS: ts.Truncate(time.Second), Item: it.Name, State: store.NetworkUnknown,
			Host: obs.Host, ID: obs.ID, Verified: obs.Verified}
		if x.Host == "" {
			x.Host = n.host(it)
		}
		if err == nil && (obs.State == store.NetworkOn || obs.State == store.NetworkStandby) {
			x.State = obs.State
		} else {
			x.Verified = false
		}
		out[i] = x
		fmt.Fprint(log, before[i])
		n.logState(log, x, err)
		fmt.Fprint(log, after[i])
	}
	return out
}

// lostAt reports whether a query error means the device may have moved:
// no answer there, or another device answers.
func lostAt(err error) bool {
	return errors.Is(err, sensor.ErrNoAnswer) || errors.Is(err, sensor.ErrIdentityMismatch) ||
		errors.Is(err, sensor.ErrIdentityUnknown) || errors.Is(err, sensor.ErrMalformed)
}

// scan runs one network scan when wanted and ScanEvery has passed since
// the last one.
func (n *Network) scan(ctx context.Context, log io.Writer, want bool) ([]discover.Device, bool) {
	now := n.now()
	if !want || now.Before(n.nextScan) {
		return nil, false
	}
	n.nextScan = now.Add(n.scanEvery())
	devs, err := n.Scan(ctx)
	if ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		fmt.Fprintf(log, "watts daemon: network scan failed: %v\n", err)
		return nil, false
	}
	return devs, true
}

// find looks for it in a scan, away from the address that just failed. It
// returns the confirming query and true when the item moved, else a note
// on why not. Every address listing the id counts, the failed one too: an
// id seen at two addresses names no single device, whichever answers.
func (n *Network) find(ctx context.Context, it config.Item, failed string, devs []discover.Device) (result, bool, string) {
	want, _ := config.NetworkID(it.Probe, it.ID)
	seen := map[string]bool{}
	for _, d := range devs {
		if hasStableID(it.Probe, d, want) {
			seen[d.Host] = true
		}
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	switch {
	case len(hosts) > 1:
		return result{}, false, fmt.Sprintf("watts daemon: %s: id %s answers at %d addresses (%s); not moved\n",
			it.Name, want, len(hosts), strings.Join(hosts, ", "))
	case len(hosts) == 1 && hosts[0] == failed:
		return result{}, false, "" // still listed where it fails: nothing new
	case len(hosts) == 0:
		return result{}, false, fmt.Sprintf("watts daemon: %s: id %s not found by a network scan; next scan in %v\n",
			it.Name, want, n.scanEvery())
	}
	host := hosts[0]
	obs, err := n.Query(ctx, sensor.NetworkTarget{Probe: it.Probe, Host: host, ID: it.ID})
	if err == nil && !obs.Verified {
		err = sensor.ErrIdentityUnknown
	}
	if err != nil {
		return result{}, false, fmt.Sprintf("watts daemon: %s: %s did not confirm id %s: %v; not moved\n", it.Name, host, want, err)
	}
	note := fmt.Sprintf("watts daemon: %s answers at %s with its id; asking there (the config still says %s)\n", it.Name, host, it.Host)
	if host == it.Host {
		delete(n.moved, it.Name)
		note = fmt.Sprintf("watts daemon: %s answers at its configured address %s again\n", it.Name, host)
	} else {
		n.moved[it.Name] = host
	}
	return result{obs: obs}, true, note
}

// hasStableID reports whether d lists want among its stable ids. Host and
// mDNS ids never match: config.NetworkID rejects them.
func hasStableID(probe string, d discover.Device, want string) bool {
	if want == "" {
		return false
	}
	ids := append(strings.Split(d.Raw["ids"], ","), d.ID)
	for _, id := range ids {
		if got, ok := config.NetworkID(probe, id); ok && got == want {
			return true
		}
	}
	return false
}

// logState logs an item's state when it changed since the last poll. An
// unknown state says why, once until it changes.
func (n *Network) logState(log io.Writer, x store.NetworkReading, err error) {
	line := fmt.Sprintf("watts daemon: %s %s at %s\n", x.Item, x.State, x.Host)
	key := line
	if x.State == store.NetworkUnknown {
		if err == nil {
			err = errors.New("no usable state")
		}
		line = fmt.Sprintf("watts daemon: %s unknown at %s (not off; logged once until it changes): %v\n", x.Item, x.Host, err)
	}
	if n.logged[x.Item] == key {
		return
	}
	n.logged[x.Item] = key
	fmt.Fprint(log, line)
}

func (n *Network) host(it config.Item) string {
	if h, ok := n.moved[it.Name]; ok {
		return h
	}
	return it.Host
}

func (n *Network) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Network) scanEvery() time.Duration {
	if n.ScanEvery > 0 {
		return n.ScanEvery
	}
	return DefaultScanEvery
}

// poller runs polls in the background, one at a time, so a slow network
// never delays the power sample. A tick while a poll runs is skipped.
type poller struct {
	n       *Network
	st      Store
	log     io.Writer
	wg      sync.WaitGroup
	running chan struct{}
}

func newPoller(n *Network, st Store, log io.Writer) *poller {
	return &poller{n: n, st: st, log: log, running: make(chan struct{}, 1)}
}

func (p *poller) start(ctx context.Context) {
	select {
	case p.running <- struct{}{}:
	default:
		return
	}
	p.wg.Add(1)
	go func() {
		defer func() { <-p.running; p.wg.Done() }()
		xs := p.n.Poll(ctx, p.log)
		if len(xs) == 0 {
			return
		}
		if err := p.st.AddNetwork(xs); err != nil {
			fmt.Fprintf(p.log, "watts daemon: store: %v\n", err)
		}
	}()
}

func (p *poller) wait() { p.wg.Wait() }
