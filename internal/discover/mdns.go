package discover

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var mdnsAddr = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

const mdnsServices = "_services._dns-sd._udp.local"

// mdnsTypes are browsed from the start; the rest come from the
// _services._dns-sd._udp enumeration, browsed in a second round.
var mdnsTypes = []string{
	"_googlecast._tcp.local",
	"_airplay._tcp.local",
	"_shelly._tcp.local",
	"_http._tcp.local",
}

// mdnsKnown are the types a device is well described by.
var mdnsKnown = map[string]bool{
	"_googlecast._tcp.local": true,
	"_airplay._tcp.local":    true,
	"_shelly._tcp.local":     true,
}

const (
	mdnsMaxTypes     = 32  // service types browsed per interface
	mdnsQuestionsPer = 24  // questions per query message
	mdnsMaxEntries   = 512 // entries per cache map, and instances in all
)

// mdnsQuery encodes one query for the given questions, asking for unicast
// replies (our source port is not 5353, so replies come back to it).
func mdnsQuery(qs []dnsQuestion) ([]byte, error) {
	return dnsMsg{Questions: qs}.encode()
}

// mdnsCache collects records from every reply on one interface. Keys are
// lower-cased names.
type mdnsCache struct {
	types   map[string]string            // service type → as received
	browsed map[string]bool              // service types already queried
	ptr     map[string]map[string]string // service type → instance key → instance name
	srv     map[string]dnsRR
	txt     map[string][]string
	addr    map[string]net.IP
	from    map[string]net.IP // instance key → reply sender
	asked   map[string]bool   // questions already sent

	instances int  // entries across ptr
	full      bool // a record was dropped at mdnsMaxEntries
}

// room reports whether a map holding n entries may take key; a known key
// always may. It marks the cache full otherwise.
func room[V any](c *mdnsCache, m map[string]V, key string, n int) bool {
	if _, ok := m[key]; ok || n < mdnsMaxEntries {
		return true
	}
	c.full = true
	return false
}

func newMDNSCache() *mdnsCache {
	return &mdnsCache{
		types: map[string]string{}, browsed: map[string]bool{}, ptr: map[string]map[string]string{},
		srv: map[string]dnsRR{}, txt: map[string][]string{}, addr: map[string]net.IP{},
		from: map[string]net.IP{}, asked: map[string]bool{},
	}
}

func (c *mdnsCache) addMsg(m dnsMsg, from net.IP) {
	if m.Flags&dnsFlagResponse == 0 {
		return
	}
	for _, r := range m.Records() {
		name := strings.ToLower(r.Name)
		switch r.Type {
		case dnsTypePTR:
			target := strings.ToLower(r.Target)
			if name == mdnsServices {
				if room(c, c.types, target, len(c.types)) {
					c.types[target] = r.Target
				}
				continue
			}
			if !strings.HasSuffix(target, "."+name) || strings.Contains(name, "._sub.") {
				continue
			}
			if !room(c, c.ptr, name, len(c.ptr)) || !room(c, c.ptr[name], target, c.instances) ||
				!room(c, c.from, target, len(c.from)) {
				continue
			}
			if c.ptr[name] == nil {
				c.ptr[name] = map[string]string{}
			}
			if _, ok := c.ptr[name][target]; !ok {
				c.instances++
			}
			c.ptr[name][target] = r.Target
			c.from[target] = from
		case dnsTypeSRV:
			if room(c, c.srv, name, len(c.srv)) {
				c.srv[name] = r
			}
		case dnsTypeTXT:
			if room(c, c.txt, name, len(c.txt)) {
				c.txt[name] = r.Text
			}
		case dnsTypeA:
			if ip := r.IP.To4(); ip != nil && room(c, c.addr, name, len(c.addr)) {
				c.addr[name] = ip
			}
		}
	}
}

// followUps are the questions still worth asking: browse enumerated types,
// and resolve instances without SRV/TXT and targets without an address.
func (c *mdnsCache) followUps() []dnsQuestion {
	var qs []dnsQuestion
	ask := func(name string, t uint16) {
		key := strings.ToLower(name) + "/" + strconv.Itoa(int(t))
		if c.asked[key] {
			return
		}
		c.asked[key] = true
		qs = append(qs, dnsQuestion{Name: name, Type: t, Class: dnsClassIN | dnsClassTopBit})
	}
	types := make([]string, 0, len(c.types))
	for k := range c.types {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, k := range types {
		if len(c.browsed) >= mdnsMaxTypes {
			break
		}
		if !c.browsed[k] {
			c.browsed[k] = true
			ask(c.types[k], dnsTypePTR)
		}
	}
	for _, insts := range c.ptr {
		for key, inst := range insts {
			if _, ok := c.srv[key]; !ok {
				ask(inst, dnsTypeSRV)
			}
			if _, ok := c.txt[key]; !ok {
				ask(inst, dnsTypeTXT)
			}
		}
	}
	for _, r := range c.srv {
		if _, ok := c.addr[strings.ToLower(r.Target)]; !ok && r.Target != "" {
			ask(r.Target, dnsTypeA)
		}
	}
	sort.Slice(qs, func(i, j int) bool {
		if qs[i].Name != qs[j].Name {
			return qs[i].Name < qs[j].Name
		}
		return qs[i].Type < qs[j].Type
	})
	return qs
}

func txtMap(txt []string) map[string]string {
	m := map[string]string{}
	for _, t := range txt {
		k, v, _ := strings.Cut(t, "=")
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			if _, ok := m[k]; !ok {
				m[k] = v
			}
		}
	}
	return m
}

// devices turns every browsed instance into a found device.
func (c *mdnsCache) devices() []found {
	var out []found
	for stype, insts := range c.ptr {
		service := strings.TrimSuffix(stype, ".local")
		for key, inst := range insts {
			txt := txtMap(c.txt[key])
			host := ""
			srv, hasSRV := c.srv[key]
			if hasSRV {
				if ip := c.addr[strings.ToLower(srv.Target)]; ip != nil {
					host = ip.String()
				}
			}
			if host == "" && c.from[key] != nil {
				host = c.from[key].String()
			}
			if host == "" {
				continue
			}
			labels := splitName(inst)
			f := found{prio: prioMDNSOther, Device: Device{
				Name: firstNonEmpty(txt["fn"], labels[0]), Host: host, Protocol: "mdns", State: StateUnknown,
				Raw: map[string]string{"mdns.services": service, "mdns.instance": inst},
			}}
			if mdnsKnown[stype] {
				f.prio = prioMDNSKnown
			}
			if hasSRV {
				f.Raw["mdns.host"] = strings.TrimSuffix(srv.Target, ".")
			}
			for k, v := range txt {
				f.Raw["txt."+k] = v
			}
			f.Model = firstNonEmpty(txt["md"], txt["model"], txt["app"], txt["ty"])
			switch stype {
			case "_googlecast._tcp.local":
				if m := strings.ToLower(f.Model); strings.Contains(m, "chromecast") || strings.Contains(m, "google") || strings.Contains(m, "nest") {
					f.Brand = "Google"
				}
			case "_shelly._tcp.local":
				f.Brand, f.Kind = "Shelly", "plug"
			}
			if f.Brand == "" && isAppleModel(f.Model) {
				f.Brand = "Apple"
			}
			if mac := normalizeMAC(txt["deviceid"]); mac != "" {
				f.Raw["mac"] = mac
			}
			switch {
			case txt["id"] != "":
				f.ID = "mdns:" + strings.ToLower(txt["id"])
			case f.Raw["mac"] != "":
				f.ID = "mac:" + f.Raw["mac"]
			default:
				f.ID = "mdns:" + strings.ToLower(inst)
			}
			out = append(out, f)
		}
	}
	return out
}

func isAppleModel(m string) bool {
	for _, p := range []string{"AppleTV", "AudioAccessory", "MacBook", "iMac", "Macmini", "MacPro", "Mac1", "iPhone", "iPad"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// mdns browses the common service types and the _services enumeration on
// one interface, resolves what it can in a second round, then adds every
// instance it heard about.
func (s *scanner) mdns(ctx context.Context, ifc Interface, conn PacketConn, _ *sync.WaitGroup) {
	c := newMDNSCache()
	var mu sync.Mutex
	qs := []dnsQuestion{{Name: mdnsServices, Type: dnsTypePTR, Class: dnsClassIN | dnsClassTopBit}}
	for _, t := range mdnsTypes {
		c.browsed[t] = true
		qs = append(qs, dnsQuestion{Name: t, Type: dnsTypePTR, Class: dnsClassIN | dnsClassTopBit})
	}
	send := func(qs []dnsQuestion) {
		for len(qs) > 0 {
			n := min(len(qs), mdnsQuestionsPer)
			msg, err := mdnsQuery(qs[:n])
			qs = qs[n:]
			if err != nil {
				s.errorf("mdns %s: %w", ifc.Name, err)
				continue
			}
			if _, err := conn.WriteTo(msg, mdnsAddr); err != nil {
				s.errorf("mdns %s: send: %w", ifc.Name, err)
			}
		}
	}
	send(qs)
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(s.window * 2 / 5):
			mu.Lock()
			more := c.followUps()
			mu.Unlock()
			send(more)
		}
	}()
	s.listen(ctx, conn, func(b []byte, from net.IP) {
		m, err := decodeDNS(b)
		if err != nil {
			return
		}
		mu.Lock()
		c.addMsg(m, from)
		mu.Unlock()
	})
	mu.Lock()
	defer mu.Unlock()
	if c.full {
		s.limit("mDNS records per interface", mdnsMaxEntries)
	}
	for _, f := range c.devices() {
		s.add(f)
	}
}
