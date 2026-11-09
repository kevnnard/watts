// Package discover finds power-relevant devices on the local IPv4 networks
// (S21): SSDP/UPnP (smart TVs, Roku, Xbox), mDNS/Bonjour (Chromecast,
// Apple TV, Shelly) and PlayStation DDP. It only reads: it sends discovery
// queries and plain HTTP GETs to the devices that answered them.
package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds one Scan when Options.Timeout is zero.
const DefaultTimeout = 3 * time.Second

// Device states.
const (
	StateOn      = "on"
	StateStandby = "standby"
	StateUnknown = "unknown"
)

// Probes that watts can poll (S20).
const (
	ProbeSamsung = "samsung"
	ProbePS5     = "ps5"
)

// Device is one discovered device, merged across protocols.
type Device struct {
	// ID is stable across DHCP changes: Samsung device id or UPnP UDN
	// ("uuid:…"), PlayStation host-id ("ps:…"), mDNS TXT id or instance
	// ("mdns:…"), else the MAC ("mac:…"). Only when none is known is it
	// the host ("host:…"). Raw["ids"] lists every ID seen for the device.
	ID       string
	Name     string
	Brand    string
	Model    string
	Kind     string // tv, media, console, speaker, plug, light, printer, router, computer, other
	Host     string // IPv4 address
	Protocol string // discovery protocols, best first: samsung, ddp, ssdp, mdns
	Probe    string // ProbeSamsung, ProbePS5 or empty
	// State comes only from a power-state answer (Samsung api/v2, DDP).
	// Answering SSDP or mDNS proves the device is reachable, not that it
	// is on, so those devices stay StateUnknown. watts never reports off:
	// no answer is not proof of it.
	State string // StateOn, StateStandby or StateUnknown
	Raw   map[string]string
}

// Interface is one local IPv4 network to scan.
type Interface struct {
	Name string
	IP   net.IP
	Mask net.IPMask
}

// Broadcast is the interface's directed broadcast address.
func (i Interface) Broadcast() net.IP {
	ip, mask := i.IP.To4(), i.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if ip == nil || len(mask) != net.IPv4len {
		return nil
	}
	b := make(net.IP, net.IPv4len)
	for k := range b {
		b[k] = ip[k] | ^mask[k]
	}
	return b
}

// PacketConn is the part of net.PacketConn a scan uses.
type PacketConn interface {
	ReadFrom(p []byte) (int, net.Addr, error)
	WriteTo(p []byte, addr net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// HTTPDoer is the part of *http.Client a scan uses.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Options configures a scan. Every network edge is injectable; zero
// values use the real network.
type Options struct {
	Timeout time.Duration // whole scan; DefaultTimeout when zero

	// Interfaces lists the networks to scan; default: every up,
	// non-loopback, non-point-to-point interface with an IPv4 address.
	Interfaces func() ([]Interface, error)
	// ListenUDP opens an unconnected UDP socket on the interface (bound to
	// its address, multicast sent out of it).
	ListenUDP func(Interface) (PacketConn, error)
	HTTP      HTTPDoer
	Now       func() time.Time
	// OnError, when set, receives non-fatal errors (a failed send or
	// fetch). They never fail the scan. It may be called concurrently.
	OnError func(error)
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Interfaces == nil {
		o.Interfaces = SystemInterfaces
	}
	if o.ListenUDP == nil {
		o.ListenUDP = listenUDP
	}
	if o.HTTP == nil {
		o.HTTP = newHTTPClient()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OnError == nil {
		o.OnError = func(error) {}
	}
	return o
}

// newHTTPClient is the client for device fetches. It always goes straight
// to the device (never through a proxy from the environment), verifies TLS,
// never follows a redirect and keeps no idle connection. Together with
// fetching only from the host that sent the answer (sameHost), that bounds
// where a scan connects; it cannot rule out a spoofed UDP sender.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{}).DialContext,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: httpMaxHeaderBytes,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// SystemInterfaces returns every up, non-loopback, non-point-to-point
// interface with an IPv4 address, one entry per address.
func SystemInterfaces() ([]Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	return filterInterfaces(ifs, (*net.Interface).Addrs), nil
}

// filterInterfaces keeps the IPv4 addresses of the up, non-loopback,
// non-point-to-point interfaces in ifs, as listed by addrs.
func filterInterfaces(ifs []net.Interface, addrs func(*net.Interface) ([]net.Addr, error)) []Interface {
	var out []Interface
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		list, err := addrs(&ifc)
		if err != nil {
			continue
		}
		for _, a := range list {
			n, ok := a.(*net.IPNet)
			// IPv4 link-local (169.254/16, no DHCP) is a local network too.
			if !ok || n.IP.To4() == nil {
				continue
			}
			out = append(out, Interface{Name: ifc.Name, IP: n.IP.To4(), Mask: n.Mask})
		}
	}
	return out
}

// listenWindow is the share of the timeout spent listening for UDP
// replies; the rest lets the HTTP fetches they started finish.
const listenWindow = 0.7

// found is one protocol's view of a device before merging.
type found struct {
	Device
	prio int
}

// Source priorities: when merged, a higher one wins each field.
const (
	prioSSDPHeaders = 20
	prioMDNSOther   = 30
	prioMDNSKnown   = 50
	prioSSDP        = 60
	prioDDP         = 90
	prioSamsung     = 100
)

// Per-scan limits, so a flood of answers cannot grow memory or goroutines
// without bound. A home network stays far below them.
const (
	maxFound           = 1024     // device views kept (one device has a few)
	maxFetches         = 128      // HTTP fetches started
	maxHTTPInFlight    = 8        // HTTP fetches at once
	httpMaxHeaderBytes = 32 << 10 // response header bytes per fetch
)

type scanner struct {
	opts     Options
	deadline time.Time // end of the listen window, from opts.Now
	window   time.Duration

	slots   chan struct{} // HTTP fetches in flight
	fetches atomic.Int32  // HTTP fetches started

	mu      sync.Mutex
	found   []found
	limited map[string]bool // limits already noted
}

// newScanner starts the scan clock for opts, which has its defaults.
func newScanner(opts Options) *scanner {
	window := time.Duration(float64(opts.Timeout) * listenWindow)
	return &scanner{opts: opts, window: window, deadline: opts.Now().Add(window),
		slots: make(chan struct{}, maxHTTPInFlight), limited: map[string]bool{}}
}

func (s *scanner) add(f found) {
	s.mu.Lock()
	full := len(s.found) >= maxFound
	if !full {
		s.found = append(s.found, f)
	}
	s.mu.Unlock()
	if full {
		s.limit("answers", maxFound)
	}
}

// limit notes, once per scan, that a limit dropped the rest of what.
func (s *scanner) limit(what string, n int) {
	s.mu.Lock()
	first := !s.limited[what]
	s.limited[what] = true
	s.mu.Unlock()
	if first {
		s.errorf("discover: limit of %d %s reached; ignoring the rest", n, what)
	}
}

// reserveFetch takes one of the scan's HTTP fetches; false when none is left.
func (s *scanner) reserveFetch() bool {
	if s.fetches.Add(1) > maxFetches {
		s.limit("HTTP fetches", maxFetches)
		return false
	}
	return true
}

func (s *scanner) errorf(format string, args ...any) {
	s.opts.OnError(fmt.Errorf(format, args...))
}

// Scan discovers devices on every local IPv4 network within opts.Timeout,
// merges duplicates by ID, then by host, and sorts them by name. It
// returns an error only when no network could be scanned at all, or with
// the partial result when ctx itself is cancelled.
func Scan(ctx context.Context, opts Options) ([]Device, error) {
	opts = opts.withDefaults()
	ifs, err := opts.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("discover: interfaces: %w", err)
	}
	if len(ifs) == 0 {
		return nil, errors.New("discover: no IPv4 network interface is up")
	}
	sctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	s := newScanner(opts)

	var wg sync.WaitGroup
	var openMu sync.Mutex
	var openErrs []error
	opened := 0
	for _, ifc := range ifs {
		for _, run := range []func(context.Context, Interface, PacketConn, *sync.WaitGroup){s.ssdp, s.mdns, s.ddp} {
			conn, err := opts.ListenUDP(ifc)
			openMu.Lock()
			if err != nil {
				openErrs = append(openErrs, fmt.Errorf("%s %s: %w", ifc.Name, ifc.IP, err))
			} else {
				opened++
			}
			openMu.Unlock()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				run(sctx, ifc, conn, &wg)
			}()
		}
	}
	if opened == 0 {
		return nil, fmt.Errorf("discover: no UDP socket: %w", errors.Join(openErrs...))
	}
	for _, e := range openErrs {
		opts.OnError(e)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-sctx.Done():
		// Every goroutine watches sctx; give them a moment to hand in
		// what they have.
		select {
		case <-done:
		case <-time.After(50 * time.Millisecond):
		}
	}
	s.mu.Lock()
	all := append([]found(nil), s.found...)
	s.mu.Unlock()
	devs := merge(all)
	if err := ctx.Err(); err != nil {
		return devs, err
	}
	return devs, nil
}

// Find re-discovers one device by ID (any ID in Raw["ids"] matches), so an
// item can follow its device across DHCP changes.
func Find(ctx context.Context, opts Options, id string) (Device, bool, error) {
	devs, err := Scan(ctx, opts)
	for _, d := range devs {
		if d.ID == id || hasID(d, id) {
			return d, true, nil
		}
	}
	return Device{}, false, err
}

func hasID(d Device, id string) bool {
	for _, x := range strings.Split(d.Raw["ids"], ",") {
		if x != "" && x == id {
			return true
		}
	}
	return false
}

// listen reads datagrams until the listen window ends or ctx is done,
// handing each one with its sender to handle.
func (s *scanner) listen(ctx context.Context, conn PacketConn, handle func([]byte, net.IP)) {
	lctx, cancel := context.WithTimeout(ctx, s.window)
	defer cancel()
	go func() { <-lctx.Done(); conn.Close() }()
	_ = conn.SetReadDeadline(s.deadline)
	buf := make([]byte, 9000)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var ip net.IP
		if u, ok := addr.(*net.UDPAddr); ok {
			ip = u.IP.To4()
		}
		if ip == nil {
			continue
		}
		handle(append([]byte(nil), buf[:n]...), ip)
	}
}

// resend sends msg now and once more a third into the listen window, as
// UDP discovery is lossy.
func (s *scanner) resend(ctx context.Context, conn PacketConn, msg []byte, to ...*net.UDPAddr) {
	send := func() {
		for _, a := range to {
			if _, err := conn.WriteTo(msg, a); err != nil {
				s.errorf("send to %s: %w", a, err)
			}
		}
	}
	send()
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(s.window / 3):
			send()
		}
	}()
}

// get fetches url with a per-request timeout, waiting for one of the
// maxHTTPInFlight slots first. A body over limit bytes is an error.
func (s *scanner) get(ctx context.Context, url string, timeout time.Duration, limit int64) ([]byte, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.opts.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s: response over %d bytes", url, limit)
	}
	return body, nil
}

// merge joins the views that share an ID or a host, then sorts.
func merge(all []found) []Device {
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	join := func(key string, i int) {
		if j, ok := owner[key]; ok {
			parent[root(i)] = root(j)
		} else {
			owner[key] = i
		}
	}
	// By ID first, then by host.
	for i, f := range all {
		if f.ID != "" {
			join("id:"+f.ID, i)
		}
	}
	for i, f := range all {
		if f.Host != "" {
			join("host:"+f.Host, i)
		}
	}
	groups := map[int][]found{}
	for i, f := range all {
		r := root(i)
		groups[r] = append(groups[r], f)
	}
	out := make([]Device, 0, len(groups))
	for _, g := range groups {
		out = append(out, combine(g))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// combine folds one group: each field comes from the highest-priority view
// that has it.
func combine(g []found) Device {
	sort.SliceStable(g, func(i, j int) bool { return g[i].prio > g[j].prio })
	d := Device{State: StateUnknown, Raw: map[string]string{}}
	first := func(dst *string, v string) {
		if *dst == "" && v != "" {
			*dst = v
		}
	}
	var ids, protos []string
	seen := map[string]bool{}
	for _, f := range g {
		first(&d.ID, f.ID)
		first(&d.Name, f.Name)
		first(&d.Brand, f.Brand)
		first(&d.Model, f.Model)
		first(&d.Kind, f.Kind)
		first(&d.Host, f.Host)
		first(&d.Probe, f.Probe)
		if d.State == StateUnknown && (f.State == StateOn || f.State == StateStandby) {
			d.State = f.State
		}
		if f.ID != "" && !seen["id:"+f.ID] {
			seen["id:"+f.ID] = true
			ids = append(ids, f.ID)
		}
		if f.Protocol != "" && !seen["p:"+f.Protocol] {
			seen["p:"+f.Protocol] = true
			protos = append(protos, f.Protocol)
		}
		for k, v := range f.Raw {
			if k == "mdns.services" && d.Raw[k] != "" {
				d.Raw[k] = joinUnique(d.Raw[k], v)
				continue
			}
			if _, ok := d.Raw[k]; !ok {
				d.Raw[k] = v
			}
		}
	}
	if d.ID == "" {
		if mac := d.Raw["mac"]; mac != "" {
			d.ID = "mac:" + mac
		} else if d.Host != "" {
			d.ID = "host:" + d.Host
		}
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	sort.Strings(ids)
	d.Raw["ids"] = strings.Join(ids, ",")
	d.Protocol = strings.Join(protos, ",")
	if d.Kind == "" {
		d.Kind = guessKind(d.Name, d.Brand, d.Model, d.Raw["upnp.deviceType"], d.Raw["mdns.services"])
	}
	if d.Name == "" {
		d.Name = strings.TrimSpace(d.Brand + " " + d.Model)
		if d.Name == "" {
			d.Name = d.Host
		}
	}
	return d
}

// joinUnique appends the comma-separated items of b missing from a.
func joinUnique(a, b string) string {
	items := strings.Split(a, ",")
	for _, x := range strings.Split(b, ",") {
		if x != "" && !slices.Contains(items, x) {
			items = append(items, x)
		}
	}
	return strings.Join(items, ",")
}

// kindRules map words (in name, brand, model, UPnP type or mDNS services)
// to a Kind; the first match wins.
var kindRules = []struct {
	kind  string
	words []string
}{
	{"console", []string{"playstation", "ps5", "ps4", "xbox", "nintendo"}},
	{"media", []string{"chromecast", "google tv streamer", "apple tv", "appletv", "roku", "fire tv", "firetv", "shield"}},
	{"tv", []string{"smarttv", "smart tv", "television", "bravia", "webos", "tizen", "aquos", "vizio", " tv "}},
	{"speaker", []string{"sonos", "speaker", "homepod", "audioaccessory", "echo", "soundbar", "_spotify-connect"}},
	{"plug", []string{"shelly", "plug", "tasmota", "kasa", "outlet"}},
	{"light", []string{"hue", "bulb", "lifx", "wiz", "light"}},
	{"printer", []string{"printer", "_ipp", "_pdl-datastream", "_scanner"}},
	{"router", []string{"internetgatewaydevice", "router", "gateway", "wfadevice"}},
	{"computer", []string{"macbook", "imac", "macmini", "mac mini", "macpro", "_ssh", "_sftp-ssh", "_smb", "_workstation", "_rfb"}},
	{"media", []string{"mediarenderer", "_googlecast", "_airplay", "_raop", "dial"}},
}

func guessKind(parts ...string) string {
	text := " " + strings.ToLower(strings.Join(parts, " ")) + " "
	for _, r := range kindRules {
		for _, w := range r.words {
			if strings.Contains(text, w) {
				return r.kind
			}
		}
	}
	return "other"
}
