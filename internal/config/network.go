package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Network probes (S20): the protocols that ask a device for its power
// state. The values are discover.ProbeSamsung and discover.ProbePS5.
const (
	ProbeSamsung = "samsung" // Samsung TV: GET http://host:8001/api/v2/
	ProbePS5     = "ps5"     // PlayStation 5: DDP over UDP 9302
)

// networkFamilies are the stable ID families of each probe's devices, as
// sensor.QueryNetwork checks them.
var networkFamilies = map[string][]string{
	ProbeSamsung: {"uuid", "mac"},
	ProbePS5:     {"ps"},
}

// NetworkID returns id normalized ("uuid:…", "mac:…" in colon form or
// "ps:…", lower case) when it is a stable identity of probe's devices. A
// host or mDNS ID is not: it can name another device tomorrow.
func NetworkID(probe, id string) (string, bool) {
	family, v, _ := strings.Cut(strings.TrimSpace(id), ":")
	family, v = strings.ToLower(family), strings.ToLower(strings.TrimSpace(v))
	ok := false
	for _, f := range networkFamilies[probe] {
		ok = ok || f == family
	}
	// printable ASCII only: device ids are hex, dashes and colons, and an
	// id is shown on screen and written to the config
	if !ok || v == "" || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", false
	}
	if family == "mac" {
		hw, err := net.ParseMAC(v)
		if err != nil || len(hw) != 6 {
			return "", false
		}
		v = hw.String()
	}
	return family + ":" + v, true
}

// validHost reports whether h is one IPv4 unicast address in dotted form, so
// a query can never become a broadcast, a multicast or a name lookup.
func validHost(h string) bool {
	ip := net.ParseIP(h)
	return ip != nil && ip.To4() != nil && ip.String() == h && (ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast()) &&
		!ip.Equal(net.IPv4bcast)
}

// validateNetwork checks the fields of a network item (S20).
func (it Item) validateNetwork() error {
	if it.Source != SourceMac && it.Source != SourceLinux {
		return fmt.Errorf("source must be %q or %q (the machine whose daemon asks the device), got %q", SourceMac, SourceLinux, it.Source)
	}
	if _, ok := networkFamilies[it.Probe]; !ok {
		return fmt.Errorf("probe must be %q or %q, got %q", ProbeSamsung, ProbePS5, it.Probe)
	}
	if !validHost(it.Host) {
		return fmt.Errorf("host %q must be one IPv4 address, such as 192.0.2.16", it.Host)
	}
	if it.ID != "" {
		if _, ok := NetworkID(it.Probe, it.ID); !ok {
			want := "uuid:… or mac:…"
			if it.Probe == ProbePS5 {
				want = "ps:…"
			}
			return fmt.Errorf("id %q is not a stable %s identity (want %s)", it.ID, it.Probe, want)
		}
	}
	if it.Watts <= 0 {
		return errors.New("watts must be greater than 0")
	}
	if it.StandbyW < 0 {
		return fmt.Errorf("standby_w must not be negative, got %g", it.StandbyW)
	}
	if it.StandbyW > it.Watts {
		return fmt.Errorf("standby_w must not exceed watts, got standby_w %g, watts %g", it.StandbyW, it.Watts)
	}
	return nil
}

// validateNetworkIDs checks that no two network items name one device.
func (c *Config) validateNetworkIDs() error {
	owner := map[string]string{}
	for _, it := range c.Items {
		if it.Mode != ModeNetwork {
			continue
		}
		id, ok := NetworkID(it.Probe, it.ID)
		if !ok {
			continue
		}
		if prev, dup := owner[id]; dup {
			return fmt.Errorf("item %q: id %s is also item %q's", it.Name, id, prev)
		}
		owner[id] = it.Name
	}
	return nil
}
