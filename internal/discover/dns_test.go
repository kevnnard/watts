package discover

import (
	"bytes"
	"net"
	"reflect"
	"sort"
	"testing"
)

// castReply is what a Chromecast sends back for a _googlecast._tcp browse.
func castReply() dnsMsg {
	inst := "Chromecast-0123abcd._googlecast._tcp.local"
	return dnsMsg{
		Flags: dnsFlagResponse | 0x0400,
		Answers: []dnsRR{
			{Name: "_googlecast._tcp.local", Type: dnsTypePTR, Class: dnsClassIN, TTL: 120, Target: inst},
		},
		Additional: []dnsRR{
			{Name: inst, Type: dnsTypeTXT, Class: dnsClassIN | dnsClassTopBit, TTL: 4500,
				Text: []string{"id=0123abcd4567ef", "md=Chromecast", "fn=Living room", "rs="}},
			{Name: inst, Type: dnsTypeSRV, Class: dnsClassIN | dnsClassTopBit, TTL: 120, Port: 8009, Target: "0123abcd-4567.local"},
			{Name: "0123abcd-4567.local", Type: dnsTypeA, Class: dnsClassIN | dnsClassTopBit, TTL: 120, IP: net.IPv4(192, 0, 2, 30).To4()},
		},
	}
}

func TestDNSRoundTrip(t *testing.T) {
	m := castReply()
	m.Questions = []dnsQuestion{{Name: "_googlecast._tcp.local", Type: dnsTypePTR, Class: dnsClassIN | dnsClassTopBit}}
	m.Authority = []dnsRR{{Name: "x.local", Type: dnsTypeAAAA, Class: dnsClassIN, TTL: 1, IP: net.ParseIP("fe80::1")},
		{Name: "x.local", Type: 47 /* NSEC */, Class: dnsClassIN, TTL: 1, Data: []byte{1, 2, 3}}}
	b, err := m.encode()
	if err != nil {
		t.Fatal(err)
	}
	// "_googlecast._tcp.local" appears 5 times but is spelled out once.
	if n := bytes.Count(b, []byte("_googlecast")); n != 1 {
		t.Fatalf("name compression: _googlecast spelled %d times", n)
	}
	got, err := decodeDNS(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, m)
	}
}

// TestDecodeDNSHandBuilt decodes bytes built by hand, so the decoder is not
// only checked against our own encoder.
func TestDecodeDNSHandBuilt(t *testing.T) {
	b := []byte{
		0x00, 0x00, 0x84, 0x00, // id, flags: response, authoritative
		0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, // 0 questions, 2 answers
		// @12 PTR _tv._tcp.local → My\.TV._tv._tcp.local
		0x03, '_', 't', 'v', 0x04, '_', 't', 'c', 'p', 0x05, 'l', 'o', 'c', 'a', 'l', 0x00,
		0x00, 0x0c, 0x00, 0x01, 0x00, 0x00, 0x00, 0x78, 0x00, 0x08,
		0x05, 'M', 'y', '.', 'T', 'V', 0xc0, 0x0c, // label with a dot, pointer to @12
		// SRV for the instance: name pointer to its label @38
		0xc0, 0x26,
		0x00, 0x21, 0x80, 0x01, 0x00, 0x00, 0x00, 0x78, 0x00, 0x0b,
		0x00, 0x00, 0x00, 0x00, 0x1f, 0x90, // priority, weight, port 8080
		0x02, 't', 'v', 0xc0, 0x15, // "tv" + pointer to "local" @21
	}
	m, err := decodeDNS(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Answers) != 2 {
		t.Fatalf("answers %+v", m.Answers)
	}
	if p := m.Answers[0]; p.Type != dnsTypePTR || p.Name != "_tv._tcp.local" || p.Target != `My\.TV._tv._tcp.local` {
		t.Fatalf("PTR %+v", p)
	}
	if s := m.Answers[1]; s.Type != dnsTypeSRV || s.Name != `My\.TV._tv._tcp.local` || s.Port != 8080 || s.Target != "tv.local" || s.Class&dnsClassTopBit == 0 {
		t.Fatalf("SRV %+v", s)
	}
	if got := splitName(m.Answers[0].Target); !reflect.DeepEqual(got, []string{"My.TV", "_tv", "_tcp", "local"}) {
		t.Fatalf("labels %q", got)
	}
	// The escaped dot survives a re-encode.
	re, err := dnsMsg{Answers: m.Answers}.encode()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := decodeDNS(re); err != nil || again.Answers[0].Target != m.Answers[0].Target {
		t.Fatalf("re-encode: %+v %v", again, err)
	}
}

func TestDecodeDNSBad(t *testing.T) {
	head := []byte{0, 0, 0x84, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	tests := map[string][]byte{
		"short header":     {0, 1, 2},
		"compression loop": append(append([]byte{}, head...), 0xc0, 0x0c, 0, 1, 0, 1),
		"label past end":   append(append([]byte{}, head...), 0x3f, 'a'),
		"bad label byte":   append(append([]byte{}, head...), 0x80, 0),
		"missing records":  {0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0},
	}
	for name, b := range tests {
		if _, err := decodeDNS(b); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMDNSCacheResolvesInstances(t *testing.T) {
	c := newMDNSCache()
	m := castReply()
	// The services enumeration and a bare airplay PTR, resolved later.
	m.Answers = append(m.Answers,
		dnsRR{Name: mdnsServices, Type: dnsTypePTR, Class: dnsClassIN, Target: "_airplay._tcp.local"},
		dnsRR{Name: mdnsServices, Type: dnsTypePTR, Class: dnsClassIN, Target: "_hap._tcp.local"},
		dnsRR{Name: "_airplay._tcp.local", Type: dnsTypePTR, Class: dnsClassIN, Target: "Bedroom._airplay._tcp.local"},
	)
	c.browsed["_airplay._tcp.local"] = true
	c.addMsg(m, net.IPv4(192, 0, 2, 30))
	c.addMsg(dnsMsg{Answers: []dnsRR{{Name: "_x._tcp.local", Type: dnsTypePTR, Target: "y._x._tcp.local"}}}, net.IPv4(1, 1, 1, 1)) // a query, ignored

	qs := c.followUps()
	var asked []string
	for _, q := range qs {
		asked = append(asked, q.Name+"/"+map[uint16]string{dnsTypePTR: "PTR", dnsTypeSRV: "SRV", dnsTypeTXT: "TXT", dnsTypeA: "A"}[q.Type])
	}
	want := []string{"Bedroom._airplay._tcp.local/TXT", "Bedroom._airplay._tcp.local/SRV", "_hap._tcp.local/PTR"}
	if !reflect.DeepEqual(asked, want) {
		t.Fatalf("follow-ups %q, want %q", asked, want)
	}
	if again := c.followUps(); len(again) != 0 {
		t.Fatalf("asked twice: %+v", again)
	}

	// The second round answers the airplay instance.
	c.addMsg(dnsMsg{Flags: dnsFlagResponse, Answers: []dnsRR{
		{Name: "Bedroom._airplay._tcp.local", Type: dnsTypeSRV, Port: 7000, Target: "Bedroom.local"},
		{Name: "Bedroom._airplay._tcp.local", Type: dnsTypeTXT, Text: []string{"deviceid=AA:BB:CC:DD:EE:FF", "model=AppleTV14,1"}},
		{Name: "Bedroom.local", Type: dnsTypeA, IP: net.IPv4(192, 0, 2, 31)},
	}}, net.IPv4(192, 0, 2, 31))

	devs := c.devices()
	sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
	if len(devs) != 2 {
		t.Fatalf("devices %+v", devs)
	}
	tv := devs[0].Device
	if tv.Name != "Bedroom" || tv.Brand != "Apple" || tv.Model != "AppleTV14,1" || tv.Host != "192.0.2.31" || tv.ID != "mac:aa:bb:cc:dd:ee:ff" {
		t.Fatalf("apple tv %+v", tv)
	}
	cast := devs[1]
	if cast.Name != "Living room" || cast.Brand != "Google" || cast.Model != "Chromecast" || cast.Host != "192.0.2.30" ||
		cast.ID != "mdns:0123abcd4567ef" || cast.prio != prioMDNSKnown || cast.Raw["mdns.services"] != "_googlecast._tcp" {
		t.Fatalf("chromecast %+v", cast)
	}
}
