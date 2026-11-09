package discover

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestParseSSDP(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ssdpReply
		err  bool
	}{
		{
			name: "samsung CRLF",
			in: "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nDATE: Sun, 08 Nov 2026 21:00:00 GMT\r\nEXT:\r\n" +
				"LOCATION: http://192.0.2.16:9197/dmr\r\nSERVER: SHP, UPnP/1.0, Samsung UPnP SDK/1.0\r\n" +
				"ST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
				"USN: uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345::urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n",
			want: ssdpReply{
				Location: "http://192.0.2.16:9197/dmr", Server: "SHP, UPnP/1.0, Samsung UPnP SDK/1.0",
				ST: "urn:schemas-upnp-org:device:MediaRenderer:1", USN: "uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345::urn:schemas-upnp-org:device:MediaRenderer:1",
			},
		},
		{
			name: "LF only, odd case",
			in:   "HTTP/1.1 200 OK\nCache-Control: max-age=3600\nst: roku:ecp\nUsn: uuid:roku:ecp:X00000000001\nlocation: http://192.0.2.20:8060/\n\n",
			want: ssdpReply{Location: "http://192.0.2.20:8060/", ST: "roku:ecp", USN: "uuid:roku:ecp:X00000000001"},
		},
		{name: "a NOTIFY is not an answer", in: "NOTIFY * HTTP/1.1\r\nNT: upnp:rootdevice\r\n\r\n", err: true},
		{name: "not 200", in: "HTTP/1.1 500 Error\r\n\r\n", err: true},
		{name: "empty", in: "", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSSDP([]byte(tt.in))
			if tt.err {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestUSNUUID(t *testing.T) {
	for in, want := range map[string]string{
		"uuid:0AB1C2D3-0000::urn:schemas-upnp-org:device:MediaRenderer:1": "uuid:0ab1c2d3-0000",
		"uuid:abc":         "uuid:abc",
		"UUID:ABC::upnp:x": "uuid:abc",
		"urn:x::y":         "",
		"":                 "",
	} {
		if got := usnUUID(in); got != want {
			t.Errorf("usnUUID(%q) = %q, want %q", in, got, want)
		}
	}
}

const samsungDescription = `<?xml version="1.0" encoding="UTF-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0" xmlns:sec="http://www.sec.co.kr/dlna" xmlns:dlna="urn:schemas-dlna-org:device-1-0">
 <specVersion><major>1</major><minor>0</minor></specVersion>
 <device>
  <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
  <pnpx:X_compatibleId xmlns:pnpx="http://schemas.microsoft.com/windows/pnpx/2005/11">MS_DigitalMediaDeviceClass_DMR_V001</pnpx:X_compatibleId>
  <dlna:X_DLNADOC>DMR-1.50</dlna:X_DLNADOC>
  <friendlyName>[TV] Bedroom TV</friendlyName>
  <manufacturer>Samsung Electronics</manufacturer>
  <manufacturerURL>http://www.samsung.com/sec</manufacturerURL>
  <modelDescription>Samsung TV DMR</modelDescription>
  <modelName> QN55Q60DAKXZL </modelName>
  <modelNumber>AllShare1.0</modelNumber>
  <sec:deviceID>P0ABCDEFGH</sec:deviceID>
  <UDN>uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345</UDN>
  <serviceList><service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType></service></serviceList>
  <deviceList><device><friendlyName>not the root</friendlyName><UDN>uuid:inner</UDN></device></deviceList>
 </device>
</root>`

func TestParseDescription(t *testing.T) {
	got, err := parseDescription([]byte(samsungDescription))
	if err != nil {
		t.Fatal(err)
	}
	want := upnpDescription{
		DeviceType: "urn:schemas-upnp-org:device:MediaRenderer:1", FriendlyName: "[TV] Bedroom TV",
		Manufacturer: "Samsung Electronics", ModelName: "QN55Q60DAKXZL", ModelNumber: "AllShare1.0",
		UDN: "uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345",
	}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	// A charset Go does not know is read as is.
	odd := strings.Replace(samsungDescription, "UTF-8", "ISO-8859-1", 1)
	if got, err := parseDescription([]byte(odd)); err != nil || got.FriendlyName != want.FriendlyName {
		t.Fatalf("ISO-8859-1: %+v, %v", got, err)
	}
	if _, err := parseDescription([]byte("<root><device>")); err == nil {
		t.Fatal("truncated XML: want error")
	}
}

// samsungAPI has the shape of a Samsung TV's http://host:8001/api/v2/ answer.
const samsungAPI = `{"device":{"FrameTVSupport":"false","GamePadSupport":"true","ImeSyncedSupport":"true","Language":"en_US","OS":"Tizen","PowerState":"on","TokenAuthSupport":"true","VoiceSupport":"true","WallScreenRatio":"0","WallService":"false","countryCode":"US","description":"Samsung DTV RCR","developerIP":"0.0.0.0","developerMode":"0","duid":"uuid:0ab1c2d3-0000-1000-8000-abcdef012345","firmwareVersion":"Unknown","id":"uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345","ip":"192.0.2.16","model":"24_PONTUSM_QTV","modelName":"QN55Q60DAKXZL","name":"Bedroom TV","networkType":"wireless","resolution":"3840x2160","smartHubAgreement":"true","type":"Samsung SmartTV","udn":"uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345","wifiMac":"A0:B1:C2:D3:E4:F5"},"id":"uuid:0AB1C2D3-0000-1000-8000-ABCDEF012345","isSupport":"{}","name":"Bedroom TV","remote":"1.0","type":"Samsung SmartTV","uri":"http://192.0.2.16:8001/api/v2/","version":"2.0.25"}`

func TestParseSamsung(t *testing.T) {
	f, err := parseSamsung([]byte(samsungAPI), "192.0.2.16")
	if err != nil {
		t.Fatal(err)
	}
	want := Device{
		ID: "uuid:0ab1c2d3-0000-1000-8000-abcdef012345", Name: "Bedroom TV", Brand: "Samsung",
		Model: "QN55Q60DAKXZL", Kind: "tv", Host: "192.0.2.16", Protocol: "samsung",
		Probe: ProbeSamsung, State: StateOn,
		Raw: map[string]string{"samsung.type": "Samsung SmartTV", "samsung.os": "Tizen", "mac": "a0:b1:c2:d3:e4:f5"},
	}
	if !reflect.DeepEqual(f.Device, want) {
		t.Fatalf("got %+v\nwant %+v", f.Device, want)
	}

	tests := []struct {
		name, in, state, id string
		err                 bool
	}{
		{name: "minimal on", in: `{"device":{"id":"uuid:A","modelName":"QN55Q60DAKXZL","name":"Bedroom TV","PowerState":"on","wifiMac":"a0:b1:c2:d3:e4:f5"},"type":"Samsung SmartTV"}`, state: StateOn, id: "uuid:a"},
		{name: "standby", in: `{"device":{"id":"uuid:A","PowerState":"standby"},"type":"Samsung SmartTV"}`, state: StateStandby, id: "uuid:a"},
		{name: "no PowerState is unknown, not on", in: `{"device":{"id":"uuid:A"},"type":"Samsung SmartTV"}`, state: StateUnknown, id: "uuid:a"},
		{name: "odd PowerState", in: `{"device":{"id":"uuid:A","PowerState":"booting"}}`, state: StateUnknown, id: "uuid:a"},
		{name: "MAC when there is no id", in: `{"device":{"wifiMac":"A0-B1-C2-D3-E4-F5"},"type":"Samsung SmartTV"}`, state: StateUnknown, id: "mac:a0:b1:c2:d3:e4:f5"},
		{name: "some other JSON", in: `{"status":"ok"}`, err: true},
		{name: "not JSON", in: `<html>`, err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseSamsung([]byte(tt.in), "192.0.2.16")
			if tt.err {
				if err == nil {
					t.Fatalf("want error, got %+v", f)
				}
				return
			}
			if err != nil || f.State != tt.state || f.ID != tt.id || f.Probe != ProbeSamsung {
				t.Fatalf("got %+v, %v; want state %s id %s", f.Device, err, tt.state, tt.id)
			}
		})
	}
}

func TestParseDDP(t *testing.T) {
	on := "HTTP/1.1 200 Ok\nhost-id:A1B2C3D4E5F6\nhost-type:PS5\nhost-name:PS5-001\nhost-request-port:997\ndevice-discovery-protocol-version:00030010\nsystem-version:09060000\n"
	standby := "HTTP/1.1 620 Server Standby\nhost-id:A1B2C3D4E5F6\nhost-type:PS5\nhost-name:PS5-001\nhost-request-port:997\n"
	ps4 := "HTTP/1.1 620 Server Standby\r\nhost-id:001122334455\r\nhost-type:PS4\r\nhost-name:PS4-living\r\n\r\n"
	tests := []struct {
		name, in, state, probe, id, model string
	}{
		{"PS5 on", on, StateOn, ProbePS5, "ps:a1b2c3d4e5f6", "PS5"},
		{"PS5 rest mode", standby, StateStandby, ProbePS5, "ps:a1b2c3d4e5f6", "PS5"},
		{"PS4 is found but not polled", ps4, StateStandby, "", "ps:001122334455", "PS4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseDDP([]byte(tt.in), "198.51.100.40")
			if err != nil {
				t.Fatal(err)
			}
			d := f.Device
			if d.State != tt.state || d.Probe != tt.probe || d.ID != tt.id || d.Model != tt.model ||
				d.Name == "" || d.Brand != "Sony" || d.Kind != "console" || d.Host != "198.51.100.40" {
				t.Fatalf("got %+v", d)
			}
		})
	}
	if _, err := parseDDP([]byte("SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n"), "x"); err == nil {
		t.Fatal("our own request echoed back: want error")
	}
}

func TestDDPRequest(t *testing.T) {
	if got := string(ddpRequest(ddpPortPS5)); got != "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n" {
		t.Fatalf("PS5 request %q", got)
	}
	if got := string(ddpRequest(ddpPortPS4)); got != "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00020020\n" {
		t.Fatalf("PS4 request %q", got)
	}
}

func TestBroadcast(t *testing.T) {
	tests := []struct {
		ifc  Interface
		want string
	}{
		{Interface{IP: net.IPv4(192, 0, 2, 15), Mask: net.CIDRMask(24, 32)}, "192.0.2.255"},
		{Interface{IP: net.IPv4(10, 1, 2, 3), Mask: net.CIDRMask(20, 32)}, "10.1.15.255"},
		{Interface{IP: net.IPv4(198, 51, 100, 3), Mask: net.CIDRMask(120, 128)}, "198.51.100.255"},
	}
	for _, tt := range tests {
		if got := tt.ifc.Broadcast().String(); got != tt.want {
			t.Errorf("%v/%v: got %s, want %s", tt.ifc.IP, tt.ifc.Mask, got, tt.want)
		}
	}
	if b := (Interface{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}).Broadcast(); b != nil {
		t.Errorf("IPv6: got %v", b)
	}
}

func TestParseHTTPishSkipsLeadingBlankLines(t *testing.T) {
	first, h, err := parseHTTPish(bytes.NewBufferString("\r\nHTTP/1.1 200 OK\r\nA: b:c\r\nbad line\r\n").Bytes())
	if err != nil || first != "HTTP/1.1 200 OK" || h["a"] != "b:c" || len(h) != 1 {
		t.Fatalf("%q %v %v", first, h, err)
	}
}

func TestFilterInterfaces(t *testing.T) {
	ipnet := func(cidr string) *net.IPNet {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast
	ifs := []net.Interface{
		{Index: 1, Name: "lo0", Flags: up | net.FlagLoopback},
		{Index: 2, Name: "en0", Flags: up},
		{Index: 3, Name: "en1", Flags: up},
		{Index: 4, Name: "en2", Flags: net.FlagBroadcast}, // down
		{Index: 5, Name: "utun0", Flags: up | net.FlagPointToPoint},
		{Index: 6, Name: "en3", Flags: up},
	}
	addrs := map[string][]net.Addr{
		"lo0":   {ipnet("127.0.0.1/8")},
		"en0":   {ipnet("192.0.2.15/24"), ipnet("fe80::1/64"), ipnet("2001:db8::1/64")},
		"en1":   {ipnet("169.254.10.20/16")}, // link-local only (no DHCP): still a local network
		"en2":   {ipnet("198.51.100.2/24")},
		"utun0": {ipnet("10.8.0.2/32")},
		"en3":   {&net.IPAddr{IP: net.ParseIP("203.0.113.5")}}, // not an *IPNet
	}
	got := filterInterfaces(ifs, func(ifc *net.Interface) ([]net.Addr, error) { return addrs[ifc.Name], nil })
	var desc []string
	for _, i := range got {
		desc = append(desc, fmt.Sprintf("%s %s %s", i.Name, i.IP, i.Broadcast()))
	}
	want := []string{"en0 192.0.2.15 192.0.2.255", "en1 169.254.10.20 169.254.255.255"}
	if !reflect.DeepEqual(desc, want) {
		t.Fatalf("interfaces %q, want %q", desc, want)
	}

	failing := filterInterfaces(ifs[1:3], func(ifc *net.Interface) ([]net.Addr, error) {
		if ifc.Name == "en0" {
			return nil, errors.New("addrs failed")
		}
		return addrs[ifc.Name], nil
	})
	if len(failing) != 1 || failing[0].Name != "en1" {
		t.Fatalf("an interface whose addresses fail is skipped, the rest kept: %+v", failing)
	}
}
