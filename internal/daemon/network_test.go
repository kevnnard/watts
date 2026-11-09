package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/discover"
	"github.com/kevnnard/watts/internal/sensor"
	"github.com/kevnnard/watts/internal/store"
)

// RFC 5737 addresses and made-up identities.
const (
	hostA = "192.0.2.16"
	hostB = "192.0.2.17"
	hostC = "192.0.2.18"
	tvID  = "uuid:0f1e2d3c-aaaa-bbbb-cccc-000000000016"
	psA   = "ps:00aabbccdd01"
	psB   = "ps:00aabbccdd02"
)

var (
	tvItem = config.Item{Name: "TV", Mode: config.ModeNetwork, Source: config.SourceLinux, Probe: config.ProbeSamsung,
		Host: hostA, ID: tvID, Watts: 63, StandbyW: 0.5, Quantity: 1}
	ps5Item = config.Item{Name: "PS5", Mode: config.ModeNetwork, Source: config.SourceLinux, Probe: config.ProbePS5,
		Host: hostA, ID: psA, Watts: 217, StandbyW: 1.2, Quantity: 1}
)

// device is a fake device on the fake network.
type device struct {
	ids   []string // what it reports, primary first
	state string   // "" gives no state
}

// fakeNet answers queries like sensor.QueryNetwork from a host → device
// table, and scans from a list. Every call is counted.
type fakeNet struct {
	mu      sync.Mutex
	hosts   map[string]device
	scan    []discover.Device
	scanErr error
	queries []string // "host id" of each query
	scans   int
	block   chan struct{} // when set, a query waits for it or ctx
	now     time.Time
}

func (f *fakeNet) query(ctx context.Context, t sensor.NetworkTarget) (sensor.NetworkObservation, error) {
	f.mu.Lock()
	f.queries = append(f.queries, t.Host+" "+t.ID)
	d, ok := f.hosts[t.Host]
	block, at := f.block, f.now
	f.mu.Unlock()
	obs := sensor.NetworkObservation{At: at, State: discover.StateUnknown, Host: t.Host}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return obs, ctx.Err()
		}
	}
	if !ok {
		return obs, fmt.Errorf("%s: %w", t.Host, sensor.ErrNoAnswer)
	}
	if len(d.ids) > 0 {
		obs.ID = d.ids[0]
	}
	if t.ID != "" {
		if len(d.ids) == 0 {
			return obs, sensor.ErrIdentityUnknown
		}
		if !contains(d.ids, t.ID) {
			return obs, sensor.ErrIdentityMismatch
		}
		obs.Verified = true
	}
	if d.state == "" {
		obs.Verified = false
		return obs, sensor.ErrNoState
	}
	obs.State = d.state
	return obs, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (f *fakeNet) scanNet(ctx context.Context) ([]discover.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.scan, f.scanErr
}

func (f *fakeNet) network(items ...config.Item) *Network {
	return &Network{Items: items, Query: f.query, Scan: f.scanNet, Now: func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}}
}

func (f *fakeNet) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fakeNet) calls() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.queries
	f.queries = nil
	return q, f.scans
}

// found is a scanned device at host with the given IDs (primary first).
func found(host string, ids ...string) discover.Device {
	return discover.Device{ID: ids[0], Host: host, Raw: map[string]string{"ids": strings.Join(ids, ",")}}
}

func reading(item, state, host, id string, verified bool) store.NetworkReading {
	return store.NetworkReading{TS: t0, Item: item, State: state, Host: host, ID: id, Verified: verified}
}

func TestStatesMatchDiscover(t *testing.T) {
	if store.NetworkOn != discover.StateOn || store.NetworkStandby != discover.StateStandby || store.NetworkUnknown != discover.StateUnknown {
		t.Fatal("store network states differ from discover's")
	}
}

// config.NetworkID accepts exactly the IDs sensor.QueryNetwork accepts, so
// a config that validates never fails as an invalid target.
func TestConfigIDsMatchSensor(t *testing.T) {
	noNet := sensor.NetworkOptions{
		Timeout:   50 * time.Millisecond,
		HTTP:      failDoer{},
		ListenUDP: func() (discover.PacketConn, error) { return nil, errors.New("no network in tests") },
	}
	for _, probe := range []string{config.ProbeSamsung, config.ProbePS5} {
		for _, id := range []string{"uuid:abc", "UUID:ABC", "mac:aa:bb:cc:00:00:16", "mac:aa-bb-cc-00-00-16", "mac:aabb.cc00.0016",
			"mac:aa:bb:cc:00:00:00:00:16", "ps:00AA", "ps:", "host:192.0.2.16", "mdns:tv", "uuid:a b", "x"} {
			_, ok := config.NetworkID(probe, id)
			_, err := sensor.QueryNetwork(context.Background(), sensor.NetworkTarget{Probe: probe, Host: hostA, ID: id}, noNet)
			if invalid := errors.Is(err, sensor.ErrInvalidTarget); invalid == ok {
				t.Errorf("%s %q: config ok %v, sensor %v", probe, id, ok, err)
			}
		}
	}
}

type failDoer struct{}

func (failDoer) Do(*http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Err: errors.New("no network in tests")}
}

func TestPollStates(t *testing.T) {
	noID := ps5Item
	noID.Name, noID.ID, noID.Host = "PS5 no id", "", hostB
	f := &fakeNet{now: t0, hosts: map[string]device{
		hostA: {ids: []string{tvID, "mac:aa:bb:cc:00:00:16"}, state: discover.StateOn},
		hostB: {ids: []string{psB}, state: discover.StateStandby},
	}}
	var log bytes.Buffer
	n := f.network(tvItem, noID)
	got := n.Poll(context.Background(), &log)
	want := []store.NetworkReading{
		reading("TV", store.NetworkOn, hostA, tvID, true),
		// Without an id the answer counts but proves no identity: the
		// reported id is kept as reported, never promoted.
		reading("PS5 no id", store.NetworkStandby, hostB, psB, false),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Poll =\n %+v\nwant\n %+v", got, want)
	}
	if _, scans := f.calls(); scans != 0 {
		t.Errorf("%d scans, want none", scans)
	}
	wantLog := "watts daemon: TV on at 192.0.2.16\nwatts daemon: PS5 no id standby at 192.0.2.17\n"
	if log.String() != wantLog {
		t.Errorf("log =\n%q\nwant\n%q", log.String(), wantLog)
	}
	// The same states again log nothing.
	log.Reset()
	n.Poll(context.Background(), &log)
	if log.String() != "" {
		t.Errorf("unchanged states logged %q", log.String())
	}
}

func TestPollUnknownIsStoredNotOff(t *testing.T) {
	noID := ps5Item
	noID.ID = ""
	f := &fakeNet{now: t0, hosts: map[string]device{
		hostB: {ids: []string{tvID}}, // answers without a state
	}}
	tvNoState := tvItem
	tvNoState.Host = hostB
	var log bytes.Buffer
	n := f.network(noID, tvNoState)
	for i := 0; i < 3; i++ {
		got := n.Poll(context.Background(), &log)
		want := []store.NetworkReading{
			reading("PS5", store.NetworkUnknown, hostA, "", false),
			reading("TV", store.NetworkUnknown, hostB, tvID, false),
		}
		for j := range want {
			want[j].TS = t0.Add(time.Duration(i) * time.Hour)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Poll %d =\n %+v\nwant\n %+v", i, got, want)
		}
		f.advance(time.Hour)
	}
	// No id, or an answer from the right device without a state: nothing
	// to find again.
	if _, scans := f.calls(); scans != 0 {
		t.Errorf("%d scans, want none", scans)
	}
	wantLog := "watts daemon: PS5 unknown at 192.0.2.16 (not off; logged once until it changes): 192.0.2.16: no answer\n" +
		"watts daemon: TV unknown at 192.0.2.17 (not off; logged once until it changes): no power state in the answer\n"
	if log.String() != wantLog {
		t.Errorf("log =\n%q\nwant\n%q", log.String(), wantLog)
	}
}

// The router gave the TV's old address to another TV, and the TV a new one.
func TestPollFindsMovedDeviceByID(t *testing.T) {
	f := &fakeNet{now: t0,
		hosts: map[string]device{
			hostA: {ids: []string{"uuid:another-tv"}, state: discover.StateOn},
			hostB: {ids: []string{tvID, "mac:aa:bb:cc:00:00:16"}, state: discover.StateStandby},
		},
		scan: []discover.Device{
			found(hostA, "uuid:another-tv"),
			// The scan reports the TV by its MAC first; the uuid is an alias.
			found(hostB, "mac:AA:BB:CC:00:00:16", strings.ToUpper(tvID)),
		},
	}
	var log bytes.Buffer
	n := f.network(tvItem)
	got := n.Poll(context.Background(), &log)
	if want := []store.NetworkReading{reading("TV", store.NetworkStandby, hostB, tvID, true)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Poll =\n %+v\nwant\n %+v", got, want)
	}
	if q, scans := f.calls(); scans != 1 || !reflect.DeepEqual(q, []string{hostA + " " + tvID, hostB + " " + tvID}) {
		t.Errorf("queries %q, %d scans", q, scans)
	}
	wantLog := "watts daemon: TV answers at 192.0.2.17 with its id; asking there (the config still says 192.0.2.16)\n" +
		"watts daemon: TV standby at 192.0.2.17\n"
	if log.String() != wantLog {
		t.Errorf("log =\n%q\nwant\n%q", log.String(), wantLog)
	}
	// Later polls ask the new address directly, without scanning.
	got = n.Poll(context.Background(), &log)
	if want := []store.NetworkReading{reading("TV", store.NetworkStandby, hostB, tvID, true)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second Poll = %+v", got)
	}
	if q, scans := f.calls(); scans != 1 || !reflect.DeepEqual(q, []string{hostB + " " + tvID}) {
		t.Errorf("second poll: queries %q, %d scans", q, scans)
	}
	// It moves back: found again at the configured address.
	f.mu.Lock()
	f.hosts = map[string]device{hostA: {ids: []string{tvID}, state: discover.StateOn}}
	f.scan = []discover.Device{found(hostA, tvID)}
	f.mu.Unlock()
	f.advance(DefaultScanEvery)
	got = n.Poll(context.Background(), &log)
	back := reading("TV", store.NetworkOn, hostA, tvID, true)
	back.TS = t0.Add(DefaultScanEvery)
	if want := []store.NetworkReading{back}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after moving back: Poll = %+v", got)
	}
}

func TestPollRejectsUnprovenMoves(t *testing.T) {
	tests := []struct {
		name  string
		hosts map[string]device
		scan  []discover.Device
		log   string
	}{
		{"the id at two addresses",
			map[string]device{hostB: {ids: []string{tvID}, state: discover.StateOn}, hostC: {ids: []string{tvID}, state: discover.StateOn}},
			[]discover.Device{found(hostB, tvID), found(hostC, "mac:aa:bb:cc:00:00:99", tvID)},
			"watts daemon: TV: id " + tvID + " answers at 2 addresses (192.0.2.17, 192.0.2.18); not moved\n"},
		{"the id at the failed address and one other",
			map[string]device{hostB: {ids: []string{tvID}, state: discover.StateOn}},
			[]discover.Device{found(hostA, tvID), found(hostB, tvID)},
			"watts daemon: TV: id " + tvID + " answers at 2 addresses (192.0.2.16, 192.0.2.17); not moved\n"},
		{"only weak identities match",
			map[string]device{hostB: {ids: []string{tvID}, state: discover.StateOn}},
			[]discover.Device{found(hostB, "host:192.0.2.17", "mdns:"+strings.TrimPrefix(tvID, "uuid:")), found(hostC, "mdns:TV")},
			"watts daemon: TV: id " + tvID + " not found by a network scan; next scan in 10m0s\n"},
		{"the new address does not confirm the id",
			map[string]device{hostB: {state: discover.StateOn}},
			[]discover.Device{found(hostB, tvID)},
			"watts daemon: TV: 192.0.2.17 did not confirm id " + tvID + ": device identity not reported; not moved\n"},
		{"the new address confirms the id but has no state",
			map[string]device{hostB: {ids: []string{tvID}}},
			[]discover.Device{found(hostB, tvID)},
			"watts daemon: TV: 192.0.2.17 did not confirm id " + tvID + ": no power state in the answer; not moved\n"},
		{"the scan only finds the address that just failed",
			map[string]device{},
			[]discover.Device{found(hostA, tvID)},
			""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeNet{now: t0, hosts: tt.hosts, scan: tt.scan}
			var log bytes.Buffer
			n := f.network(tvItem)
			got := n.Poll(context.Background(), &log)
			if want := []store.NetworkReading{reading("TV", store.NetworkUnknown, hostA, "", false)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Poll = %+v, want the unknown reading at the configured host", got)
			}
			want := "watts daemon: TV unknown at 192.0.2.16 (not off; logged once until it changes): 192.0.2.16: no answer\n" + tt.log
			if log.String() != want {
				t.Errorf("log =\n%q\nwant\n%q", log.String(), want)
			}
			// The next poll still asks the configured host.
			n.Poll(context.Background(), io_discard{})
			if q, _ := f.calls(); q[len(q)-1] != hostA+" "+tvID {
				t.Errorf("queries %q: the host changed without proof", q)
			}
		})
	}
}

type io_discard struct{}

func (io_discard) Write(p []byte) (int, error) { return len(p), nil }

// Two consoles answer; each item only ever moves to its own id, and an
// item without an id never moves.
func TestPollTwoConsoles(t *testing.T) {
	a, b := ps5Item, ps5Item
	b.Name, b.ID, b.Host = "PS5 bedroom", "", hostA
	f := &fakeNet{now: t0,
		hosts: map[string]device{hostB: {ids: []string{psB}, state: discover.StateOn}, hostC: {ids: []string{psA}, state: discover.StateStandby}},
		scan:  []discover.Device{found(hostB, psB), found(hostC, psA)},
	}
	got := f.network(a, b).Poll(context.Background(), io_discard{})
	want := []store.NetworkReading{
		reading("PS5", store.NetworkStandby, hostC, psA, true),
		reading("PS5 bedroom", store.NetworkUnknown, hostA, "", false),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Poll =\n %+v\nwant\n %+v", got, want)
	}
}

func TestPollScanCadence(t *testing.T) {
	f := &fakeNet{now: t0, hosts: map[string]device{}, scanErr: errors.New("discover: no IPv4 network interface is up")}
	var log bytes.Buffer
	n := f.network(tvItem, ps5Item)
	n.ScanEvery = 5 * time.Minute
	for i := 0; i < 10; i++ { // every minute for 10 minutes
		if got := n.Poll(context.Background(), &log); len(got) != 2 || got[0].State != store.NetworkUnknown || got[1].State != store.NetworkUnknown {
			t.Fatalf("Poll %d = %+v", i, got)
		}
		f.advance(time.Minute)
	}
	// One scan serves both lost items; a failed scan waits like any other.
	if _, scans := f.calls(); scans != 2 {
		t.Errorf("%d scans in 10 minutes every 5, want 2", scans)
	}
	if c := strings.Count(log.String(), "network scan failed: discover: no IPv4 network interface is up"); c != 2 {
		t.Errorf("log:\n%s", log.String())
	}
}

func TestPollBoundsFanout(t *testing.T) {
	var items []config.Item
	hosts := map[string]device{}
	for i := 0; i < 9; i++ {
		it := tvItem
		it.Name, it.ID, it.Host = fmt.Sprintf("TV %d", i), "", fmt.Sprintf("192.0.2.%d", 100+i)
		items = append(items, it)
		hosts[it.Host] = device{state: discover.StateOn}
	}
	var cur, peak atomic.Int32
	f := &fakeNet{now: t0, hosts: hosts}
	n := f.network(items...)
	n.Parallel = 2
	n.Query = func(ctx context.Context, t sensor.NetworkTarget) (sensor.NetworkObservation, error) {
		c := cur.Add(1)
		for p := peak.Load(); c > p && !peak.CompareAndSwap(p, c); p = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		defer cur.Add(-1)
		return f.query(ctx, t)
	}
	got := n.Poll(context.Background(), io_discard{})
	if len(got) != 9 || peak.Load() > 2 || peak.Load() < 1 {
		t.Fatalf("%d readings, peak %d queries at once, want 9 and at most 2", len(got), peak.Load())
	}
	for i, r := range got {
		if r.Item != items[i].Name || r.State != store.NetworkOn {
			t.Errorf("reading %d = %+v: not in config order", i, r)
		}
	}
}

func TestPollCancelledStoresNothing(t *testing.T) {
	f := &fakeNet{now: t0, hosts: map[string]device{}, block: make(chan struct{})}
	n := f.network(tvItem, ps5Item)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []store.NetworkReading)
	go func() { done <- n.Poll(ctx, io_discard{}) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got != nil {
			t.Errorf("a cancelled poll returned %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not stop on cancel")
	}
	if _, scans := f.calls(); scans != 0 {
		t.Errorf("a cancelled poll scanned %d times", scans)
	}
}

// Run polls at start and on ticks, in the background, stores every
// reading, and waits for a running poll before it returns.
func TestRunNetwork(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "watts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := &fakeNet{now: t0, hosts: map[string]device{hostA: {ids: []string{tvID}, state: discover.StateOn}}}
	polled := make(chan struct{}, 10)
	n := f.network(tvItem)
	q := n.Query
	n.Query = func(ctx context.Context, t sensor.NetworkTarget) (sensor.NetworkObservation, error) {
		f.advance(time.Minute)
		defer func() { polled <- struct{}{} }()
		return q(ctx, t)
	}
	sens := &fakeSensor{steps: []step{{at: at(0), watts: 60}, {at: at(60), watts: 61}, {at: at(120), watts: 62}}}
	ticks := make(chan time.Time)
	var log syncBuffer
	d := &Daemon{Sensor: sens, Store: st, Network: n, Interval: time.Minute, Log: &log,
		Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	<-polled
	for range 2 {
		ticks <- time.Time{}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rows, _ := st.NetworkReadings("", t0.Add(-time.Hour), t0.Add(time.Hour))
	if len(rows) < 1 || len(rows) > 3 {
		t.Fatalf("rows = %+v, want one per poll that ran", rows)
	}
	for _, r := range rows {
		if r.Item != "TV" || r.State != store.NetworkOn || r.Host != hostA || !r.Verified {
			t.Errorf("row = %+v", r)
		}
	}
	if samples, _ := st.Samples("", at(-1), at(3600)); len(samples) != 2 {
		t.Errorf("samples = %+v: network polls must not affect them", samples)
	}
	if s := log.String(); !strings.HasSuffix(s, "watts daemon: stopped\n") || !strings.Contains(s, "watts daemon: TV on at 192.0.2.16\n") {
		t.Errorf("log =\n%s", s)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
