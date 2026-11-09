package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// defaultTransport is the transport of the default scan client.
func defaultTransport(t *testing.T) (*http.Client, *http.Transport) {
	t.Helper()
	c, ok := Options{}.withDefaults().HTTP.(*http.Client)
	if !ok {
		t.Fatalf("default HTTP is %T, want *http.Client", Options{}.withDefaults().HTTP)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport is %T, want its own *http.Transport (not the proxy-reading default)", c.Transport)
	}
	return c, tr
}

func testScanner(timeout time.Duration) *scanner {
	return newScanner(Options{Timeout: timeout}.withDefaults())
}

func TestDefaultClientNeverUsesAProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		w.Write([]byte(samsungAPI))
	}))
	defer proxy.Close()
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(samsungAPI))
	}))
	defer device.Close()
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY"} {
		t.Setenv(k, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	c, tr := defaultTransport(t)
	if tr.Proxy != nil {
		t.Fatal("transport has a Proxy func; LAN fetches must go direct")
	}
	// Ask for a LAN address (not loopback, which Go never proxies) and send
	// every dial to the fake device, recording where the client dialed.
	var mu sync.Mutex
	var dialed []string
	tr = tr.Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, device.Listener.Addr().String())
	}
	c.Transport = tr
	s := testScanner(time.Second)
	s.opts.HTTP = c
	body, err := s.get(context.Background(), "http://192.0.2.16:8001/api/v2/", time.Second, samsungLimit)
	if err != nil || !strings.Contains(string(body), "QN55Q60DAKXZL") {
		t.Fatalf("get: %q, %v", body, err)
	}
	if proxyHits.Load() != 0 || len(dialed) != 1 || dialed[0] != "192.0.2.16:8001" {
		t.Fatalf("proxy hits %d, dialed %q", proxyHits.Load(), dialed)
	}
}

func TestDefaultClientLimits(t *testing.T) {
	c, tr := defaultTransport(t)
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification is off")
	}
	if tr.MaxResponseHeaderBytes != httpMaxHeaderBytes || httpMaxHeaderBytes <= 0 || httpMaxHeaderBytes > 64<<10 {
		t.Fatalf("header limit %d (const %d)", tr.MaxResponseHeaderBytes, httpMaxHeaderBytes)
	}
	if !tr.DisableKeepAlives {
		t.Fatal("keep-alives on: idle connections would outlive the scan")
	}
	if c.CheckRedirect == nil {
		t.Fatal("redirects are followed")
	}
}

func TestDefaultClientRejectsUnverifiedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("{}")) }))
	defer srv.Close()
	_, err := testScanner(time.Second).get(context.Background(), srv.URL+"/", time.Second, samsungLimit)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("self-signed TLS: %v, want a certificate error", err)
	}
}

func TestGetDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Add(1)
			w.Write([]byte(samsungAPI))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	_, err := testScanner(time.Second).get(context.Background(), srv.URL+"/dmr", time.Second, descLimit)
	if err == nil || !strings.Contains(err.Error(), "302") || followed.Load() != 0 {
		t.Fatalf("redirect: err %v, followed %d", err, followed.Load())
	}
}

func TestGetRejectsOversizeResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exact":
			w.Write([]byte(strings.Repeat("a", 100)))
		case "/body":
			w.Write([]byte(strings.Repeat("a", 101)))
		case "/endless":
			b := []byte(strings.Repeat("a", 4096))
			for r.Context().Err() == nil {
				if _, err := w.Write(b); err != nil {
					return
				}
			}
		case "/header":
			w.Header().Set("X-Big", strings.Repeat("h", 2*httpMaxHeaderBytes))
			w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()
	s := testScanner(time.Second)
	if b, err := s.get(context.Background(), srv.URL+"/exact", time.Second, 100); err != nil || len(b) != 100 {
		t.Fatalf("at the limit: %d bytes, %v", len(b), err)
	}
	for _, path := range []string{"/body", "/endless", "/header"} {
		start := time.Now()
		b, err := s.get(context.Background(), srv.URL+path, time.Second, 100)
		if err == nil || b != nil || time.Since(start) > 900*time.Millisecond {
			t.Errorf("%s: %d bytes, err %v after %v; want an error, no body", path, len(b), err, time.Since(start))
		}
	}
}

// TestScanFloodIsBounded floods every protocol on both interfaces: each
// SSDP answer comes from a new host with a new LOCATION whose fetch hangs,
// plus thousands of PlayStation answers and mDNS instances.
func TestScanFloodIsBounded(t *testing.T) {
	const flood = 3000
	var ssdp, ddp []reply
	for i := range flood {
		host := fmt.Sprintf("10.%d.%d.%d", i/65536, i/256%256, i%256+1)
		ssdp = append(ssdp, reply{host, ssdpAnswer("http://"+host+":1400/d"+fmt.Sprint(i), fmt.Sprintf("uuid:flood-%d", i))})
		ddp = append(ddp, reply{host, []byte(fmt.Sprintf("HTTP/1.1 200 Ok\nhost-id:%012x\nhost-type:PS5\nhost-name:ps-%d\n", i, i))})
	}
	var mdns []reply
	for i := range flood / 50 {
		m := dnsMsg{Flags: dnsFlagResponse}
		for j := range 50 {
			m.Answers = append(m.Answers, dnsRR{Name: "_http._tcp.local", Type: dnsTypePTR, Class: dnsClassIN,
				Target: fmt.Sprintf("dev-%d-%d._http._tcp.local", i, j)})
		}
		mdns = append(mdns, reply{fmt.Sprintf("10.9.0.%d", i%250+1), mustEncode(t, m)})
	}
	n := &fakeNet{respond: func(_ Interface, to *net.UDPAddr, _ []byte) []reply {
		switch to.Port {
		case 1900:
			return ssdp
		case ddpPortPS5:
			return ddp
		case 5353:
			return mdns
		}
		return nil
	}}
	h := &fakeHTTP{hangAll: true}
	var notes atomic.Int32
	opts := fakeOpts(n, h, 600*time.Millisecond)
	opts.OnError = func(error) { notes.Add(1) }

	before := runtime.NumGoroutine()
	start := time.Now()
	devs, err := Scan(context.Background(), opts)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took > 1500*time.Millisecond {
		t.Errorf("flooded scan took %v for a 600ms timeout", took)
	}
	if len(devs) == 0 || len(devs) > maxFound {
		t.Errorf("%d devices, want 1..%d", len(devs), maxFound)
	}
	h.mu.Lock()
	fetches, peak := len(h.urls), h.maxInFlight
	h.mu.Unlock()
	if fetches == 0 || fetches > maxFetches {
		t.Errorf("%d HTTP fetches, want 1..%d", fetches, maxFetches)
	}
	if peak > maxHTTPInFlight {
		t.Errorf("%d fetches at once, want at most %d", peak, maxHTTPInFlight)
	}
	if notes.Load() == 0 {
		t.Error("hitting a limit left no note")
	}
	// Every goroutine the scan started ends soon after it returns.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > before+5 {
		t.Errorf("%d goroutines left after the scan, %d before", g, before)
	}
}

func TestScanFloodCancel(t *testing.T) {
	var ssdp []reply
	for i := range 2000 {
		host := fmt.Sprintf("10.1.%d.%d", i/256, i%256+1)
		ssdp = append(ssdp, reply{host, ssdpAnswer("http://"+host+"/d", fmt.Sprintf("uuid:c-%d", i))})
	}
	n := &fakeNet{respond: func(_ Interface, to *net.UDPAddr, _ []byte) []reply {
		if to.Port == 1900 {
			return ssdp
		}
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	devs, err := Scan(ctx, fakeOpts(n, &fakeHTTP{hangAll: true}, 5*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || len(devs) > maxFound {
		t.Fatalf("err %v after %v with %d devices", err, time.Since(start), len(devs))
	}
}

func TestMDNSCacheIsBounded(t *testing.T) {
	c := newMDNSCache()
	for i := range 40 {
		m := dnsMsg{Flags: dnsFlagResponse}
		for j := range 100 {
			k := i*100 + j
			inst := fmt.Sprintf("d%d._http._tcp.local", k)
			m.Answers = append(m.Answers,
				dnsRR{Name: mdnsServices, Type: dnsTypePTR, Target: fmt.Sprintf("_t%d._tcp.local", k)},
				dnsRR{Name: "_http._tcp.local", Type: dnsTypePTR, Target: inst},
				dnsRR{Name: inst, Type: dnsTypeSRV, Target: fmt.Sprintf("h%d.local", k)},
				dnsRR{Name: inst, Type: dnsTypeTXT, Text: []string{"a=b"}},
				dnsRR{Name: fmt.Sprintf("h%d.local", k), Type: dnsTypeA, IP: net.IPv4(10, 0, byte(k/256), byte(k%256)).To4()},
			)
		}
		c.addMsg(m, net.IPv4(10, 0, 0, 1))
	}
	instances := 0
	for _, insts := range c.ptr {
		instances += len(insts)
	}
	for name, n := range map[string]int{"types": len(c.types), "service types": len(c.ptr), "instances": instances,
		"srv": len(c.srv), "txt": len(c.txt), "addr": len(c.addr), "from": len(c.from)} {
		if n == 0 || n > mdnsMaxEntries {
			t.Errorf("%s: %d entries, want 1..%d", name, n, mdnsMaxEntries)
		}
	}
	qs := c.followUps()
	if len(c.asked) > 3*mdnsMaxEntries+mdnsMaxTypes || len(qs) > len(c.asked) {
		t.Errorf("%d questions asked", len(c.asked))
	}
	if len(c.devices()) > mdnsMaxEntries {
		t.Errorf("%d devices", len(c.devices()))
	}
}

// A normal home network stays far below every limit: the same result as
// before the limits, in the same order.
func TestLimitsLeaveANormalScanAlone(t *testing.T) {
	n, h := homeNet(t)
	var notes []string
	var mu sync.Mutex
	opts := fakeOpts(n, h, 600*time.Millisecond)
	opts.OnError = func(err error) { mu.Lock(); notes = append(notes, err.Error()); mu.Unlock() }
	devs, err := Scan(context.Background(), opts)
	if err != nil || len(devs) != 4 {
		t.Fatalf("%d devices, %v", len(devs), err)
	}
	for _, note := range notes {
		if strings.Contains(note, "limit") {
			t.Errorf("limit note on a normal network: %s", note)
		}
	}
}
