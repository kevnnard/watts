package discover

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

var live = flag.Bool("live", false, "TestLiveScan: scan the real local network")

const dialDescription = `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<deviceType>urn:dial-multiscreen-org:device:dialreceiver:1</deviceType><friendlyName>Bedroom TV</friendlyName>
<manufacturer>Samsung Electronics</manufacturer><modelName>QN55Q60DAKXZL</modelName><UDN>uuid:dial-xyz</UDN></device></root>`

func ssdpAnswer(location, usn string) []byte {
	return []byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: " + location +
		"\r\nSERVER: test UPnP/1.0\r\nST: upnp:rootdevice\r\nUSN: " + usn + "::upnp:rootdevice\r\n\r\n")
}

func mustEncode(t *testing.T, m dnsMsg) []byte {
	t.Helper()
	b, err := m.encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// homeNet is a sample home: the Samsung TV and a Chromecast on WiFi
// (192.0.2.0/24), a PS5 in rest mode on the wired net (198.51.100.0/24),
// plus a device whose LOCATION points off the LAN.
func homeNet(t *testing.T) (*fakeNet, *fakeHTTP) {
	tvAirplay := dnsMsg{Flags: dnsFlagResponse, Answers: []dnsRR{
		{Name: "_airplay._tcp.local", Type: dnsTypePTR, Class: dnsClassIN, Target: "Bedroom TV._airplay._tcp.local"},
		{Name: "Bedroom TV._airplay._tcp.local", Type: dnsTypeTXT, Class: dnsClassIN, Text: []string{"deviceid=A0:B1:C2:D3:E4:F5", "manufacturer=Samsung", "model=QN55Q60DAKXZL"}},
		{Name: "Bedroom TV._airplay._tcp.local", Type: dnsTypeSRV, Class: dnsClassIN, Port: 7000, Target: "Samsung-TV.local"},
		{Name: "Samsung-TV.local", Type: dnsTypeA, Class: dnsClassIN, IP: net.IPv4(192, 0, 2, 16).To4()},
	}}
	cast, airplay := mustEncode(t, castReply()), mustEncode(t, tvAirplay)
	n := &fakeNet{respond: func(ifc Interface, to *net.UDPAddr, data []byte) []reply {
		wifi := ifc.IP.Equal(net.IPv4(192, 0, 2, 15))
		switch {
		case to.Port == 1900 && wifi:
			return []reply{
				{"192.0.2.16", ssdpAnswer("http://192.0.2.16:9197/dmr", "uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345")},
				{"192.0.2.16", ssdpAnswer("http://192.0.2.16:7678/nservice/", "uuid:dial-xyz")},
				{"192.0.2.16", ssdpAnswer("http://192.0.2.16:9197/dmr", "uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345")}, // repeat
				{"192.0.2.50", ssdpAnswer("http://203.0.113.9/desc.xml", "uuid:elsewhere")},
				{"192.0.2.51", []byte("garbage")},
			}
		case to.Port == 5353 && wifi:
			if _, err := decodeDNS(data); err != nil {
				t.Errorf("bad mDNS query: %v", err)
			}
			return []reply{{"192.0.2.30", cast}, {"192.0.2.16", airplay}}
		case to.Port == ddpPortPS5 && to.IP.Equal(net.IPv4(198, 51, 100, 255)):
			if !strings.HasPrefix(string(data), "SRCH * HTTP/1.1\n") {
				t.Errorf("bad DDP request %q", data)
			}
			return []reply{{"198.51.100.40", []byte("HTTP/1.1 620 Server Standby\nhost-id:A1B2C3D4E5F6\nhost-type:PS5\nhost-name:PS5-001\nhost-request-port:997\n")}}
		}
		return nil
	}}
	h := &fakeHTTP{bodies: map[string]string{
		"http://192.0.2.16:9197/dmr":       samsungDescription,
		"http://192.0.2.16:7678/nservice/": dialDescription,
		"http://192.0.2.16:8001/api/v2/":   samsungAPI,
	}}
	return n, h
}

func TestScanHomeNetwork(t *testing.T) {
	n, h := homeNet(t)
	devs, err := Scan(context.Background(), fakeOpts(n, h, 600*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range devs {
		names = append(names, d.Name)
	}
	if want := []string{"192.0.2.50", "Bedroom TV", "Living room", "PS5-001"}; !slices.Equal(names, want) {
		t.Fatalf("names %q, want %q\n%+v", names, want, devs)
	}

	tv := devs[1]
	if tv.ID != "uuid:0ab1c2d3-0000-1000-8000-abcdef012345" || tv.Brand != "Samsung" || tv.Model != "QN55Q60DAKXZL" ||
		tv.Kind != "tv" || tv.Host != "192.0.2.16" || tv.Probe != ProbeSamsung || tv.State != StateOn ||
		tv.Protocol != "samsung,ssdp,mdns" {
		t.Fatalf("tv %+v", tv)
	}
	if want := "mac:a0:b1:c2:d3:e4:f5,uuid:0ab1c2d3-0000-1000-8000-abcdef012345,uuid:dial-xyz"; tv.Raw["ids"] != want {
		t.Fatalf("tv ids %q, want %q", tv.Raw["ids"], want)
	}

	cast := devs[2]
	// Answering mDNS or SSDP is not a power state: only a probe sets one.
	if cast.ID != "mdns:0123abcd4567ef" || cast.Kind != "media" || cast.Brand != "Google" || cast.Probe != "" || cast.Host != "192.0.2.30" ||
		cast.State != StateUnknown {
		t.Fatalf("chromecast %+v", cast)
	}
	ps := devs[3]
	if ps.ID != "ps:a1b2c3d4e5f6" || ps.Probe != ProbePS5 || ps.State != StateStandby || ps.Host != "198.51.100.40" || ps.Protocol != "ddp" {
		t.Fatalf("ps5 %+v", ps)
	}
	// The off-LAN LOCATION is kept as a bare device and never fetched.
	if other := devs[0]; other.ID != "uuid:elsewhere" || other.Protocol != "ssdp" || other.Kind != "other" || other.State != StateUnknown {
		t.Fatalf("other %+v", other)
	}
	for _, u := range h.urls {
		if strings.Contains(u, "203.0.113.9") {
			t.Fatalf("followed an off-LAN LOCATION: %s", u)
		}
	}

	sent := n.sentTo()
	for _, want := range []string{"239.255.255.250:1900", "224.0.0.251:5353", "192.0.2.255:9302", "192.0.2.255:987", "198.51.100.255:9302", "198.51.100.255:987"} {
		if !slices.Contains(sent, want) {
			t.Errorf("never sent to %s: %q", want, sent)
		}
	}
	// Each M-SEARCH is sent twice per interface (UDP is lossy).
	ssdp := 0
	for _, s := range sent {
		if s == "239.255.255.250:1900" {
			ssdp++
		}
	}
	if ssdp != 4 {
		t.Errorf("M-SEARCH sent %d times, want 4", ssdp)
	}
}

func TestFind(t *testing.T) {
	n, h := homeNet(t)
	opts := fakeOpts(n, h, 400*time.Millisecond)
	// The DIAL UDN is an alias of the TV.
	d, ok, err := Find(context.Background(), opts, "uuid:dial-xyz")
	if err != nil || !ok || d.Name != "Bedroom TV" {
		t.Fatalf("by alias: %+v %v %v", d, ok, err)
	}
	d, ok, err = Find(context.Background(), opts, "ps:a1b2c3d4e5f6")
	if err != nil || !ok || d.Host != "198.51.100.40" {
		t.Fatalf("ps5: %+v %v %v", d, ok, err)
	}
	if _, ok, err := Find(context.Background(), opts, "ps:gone"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
}

func TestScanTimeout(t *testing.T) {
	// The TV answers SSDP, then every HTTP fetch hangs; nothing else answers.
	n := &fakeNet{respond: func(ifc Interface, to *net.UDPAddr, _ []byte) []reply {
		if to.Port == 1900 && ifc.Name == "en0" {
			return []reply{{"192.0.2.16", ssdpAnswer("http://192.0.2.16:9197/dmr", "uuid:ABC")}}
		}
		return nil
	}}
	h := &fakeHTTP{bodies: map[string]string{
		"http://192.0.2.16:9197/dmr":     "hang",
		"http://192.0.2.16:8001/api/v2/": "hang",
	}}
	start := time.Now()
	devs, err := Scan(context.Background(), fakeOpts(n, h, 300*time.Millisecond))
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took < 250*time.Millisecond || took > 600*time.Millisecond {
		t.Fatalf("took %v for a 300ms timeout", took)
	}
	if len(devs) != 1 || devs[0].ID != "uuid:abc" || devs[0].Name != "192.0.2.16" || devs[0].State != StateUnknown {
		t.Fatalf("devices %+v", devs)
	}
}

func TestScanParentCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Scan(ctx, fakeOpts(&fakeNet{}, &fakeHTTP{}, 5*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestScanDefaultTimeout(t *testing.T) {
	if got := (Options{}).withDefaults().Timeout; got != DefaultTimeout || DefaultTimeout != 3*time.Second {
		t.Fatalf("default timeout %v", got)
	}
}

func TestScanErrors(t *testing.T) {
	refuse := Options{Interfaces: testIfaces, ListenUDP: func(Interface) (PacketConn, error) {
		return nil, errors.New("sendto: no route to host")
	}}
	if _, err := Scan(context.Background(), refuse); err == nil || !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("no socket: %v", err)
	}
	none := Options{Interfaces: func() ([]Interface, error) { return nil, nil }}
	if _, err := Scan(context.Background(), none); err == nil {
		t.Fatal("no interface: want error")
	}
	broken := Options{Interfaces: func() ([]Interface, error) { return nil, errors.New("boom") }}
	if _, err := Scan(context.Background(), broken); err == nil {
		t.Fatal("interfaces error: want error")
	}
}

func TestMerge(t *testing.T) {
	v := func(prio int, id, host, name, proto, state string) found {
		return found{prio: prio, Device: Device{ID: id, Host: host, Name: name, Protocol: proto, State: state}}
	}
	got := merge([]found{
		v(prioSSDPHeaders, "uuid:b", "10.0.0.2", "", "ssdp", StateOn),
		v(prioSSDP, "uuid:a", "10.0.0.2", "TV (upnp)", "ssdp", StateOn),
		v(prioSamsung, "uuid:a", "10.0.0.9", "TV", "samsung", StateStandby), // same ID, new host: joins through uuid:a
		v(prioMDNSOther, "mdns:z", "10.0.0.3", "alpha", "mdns", StateOn),
		v(prioMDNSOther, "", "10.0.0.4", "", "mdns", ""),
	})
	if len(got) != 3 {
		t.Fatalf("merged %+v", got)
	}
	want := []string{"10.0.0.4", "alpha", "TV"}
	for i, d := range got {
		if d.Name != want[i] {
			t.Fatalf("order %d: %q, want %q", i, d.Name, want[i])
		}
	}
	tv := got[2]
	if tv.ID != "uuid:a" || tv.Host != "10.0.0.9" || tv.State != StateStandby || tv.Protocol != "samsung,ssdp" || tv.Raw["ids"] != "uuid:a,uuid:b" {
		t.Fatalf("tv %+v", tv)
	}
	if bare := got[0]; bare.ID != "host:10.0.0.4" || bare.State != StateUnknown || bare.Kind != "other" {
		t.Fatalf("bare %+v", bare)
	}
}

func TestGuessKind(t *testing.T) {
	for want, parts := range map[string][]string{
		"tv":      {"Bedroom TV", "Samsung"},
		"media":   {"Living room", "Google", "Chromecast"},
		"console": {"Xbox", "Microsoft"},
		"plug":    {"shellyplus1pm-a8032ab12345"},
		"printer": {"HP", "", "", "", "_ipp._tcp"},
		"other":   {"thing"},
	} {
		if got := guessKind(parts...); got != want {
			t.Errorf("guessKind(%q) = %s, want %s", parts, got, want)
		}
	}
}

// TestLiveScan prints a real scan: go test ./internal/discover -run TestLiveScan -live
func TestLiveScan(t *testing.T) {
	if !*live {
		t.Skip("pass -live to scan the real network")
	}
	ifs, err := SystemInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, ifc := range ifs {
		fmt.Printf("interface %s %s/%d broadcast %s\n", ifc.Name, ifc.IP, maskBits(ifc.Mask), ifc.Broadcast())
	}
	start := time.Now()
	devs, err := Scan(context.Background(), Options{OnError: func(err error) { fmt.Println("  note:", err) }})
	fmt.Printf("scan took %v, %d devices, err %v\n", time.Since(start).Round(time.Millisecond), len(devs), err)
	for _, d := range devs {
		fmt.Printf("- %-28q %-15s %-8s %-7s probe=%-7s brand=%q model=%q kind=%s via %s\n    id=%s ids=%s\n",
			d.Name, d.Host, d.Kind, d.State, d.Probe, d.Brand, d.Model, d.Kind, d.Protocol, d.ID, d.Raw["ids"])
	}
}

func maskBits(m net.IPMask) int {
	ones, _ := m.Size()
	return ones
}
