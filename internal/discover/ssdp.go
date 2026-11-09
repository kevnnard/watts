package discover

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ssdpAddr = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

const mSearch = "M-SEARCH * HTTP/1.1\r\n" +
	"HOST: 239.255.255.250:1900\r\n" +
	"MAN: \"ssdp:discover\"\r\n" +
	"MX: 1\r\n" +
	"ST: ssdp:all\r\n" +
	"USER-AGENT: watts UPnP/1.1\r\n\r\n"

// Per-request limits for the HTTP fetches a scan makes.
const (
	descTimeout    = 1500 * time.Millisecond
	samsungTimeout = 1500 * time.Millisecond
	descLimit      = 256 << 10
	samsungLimit   = 64 << 10
	samsungPort    = 8001

	ssdpMaxHosts     = 256 // answering hosts per interface (a /24 has 254)
	ssdpMaxLocations = 512 // distinct LOCATIONs per interface
)

// parseHTTPish parses an SSDP or DDP datagram: an HTTP-style status or
// request line, then "Key: value" lines (CRLF or LF). Keys are lower-cased.
func parseHTTPish(b []byte) (first string, headers map[string]string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	headers = map[string]string{}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if first == "" {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			first = line
			continue
		}
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if first == "" {
		return "", nil, errors.New("empty message")
	}
	return first, headers, sc.Err()
}

// statusCode returns the code of an "HTTP/1.1 200 OK" line.
func statusCode(first string) (int, error) {
	f := strings.Fields(first)
	if len(f) < 2 || !strings.HasPrefix(f[0], "HTTP/") {
		return 0, fmt.Errorf("not a status line: %q", first)
	}
	return strconv.Atoi(f[1])
}

// ssdpReply is one M-SEARCH answer.
type ssdpReply struct {
	Location, USN, Server, ST string
}

func parseSSDP(b []byte) (ssdpReply, error) {
	first, h, err := parseHTTPish(b)
	if err != nil {
		return ssdpReply{}, err
	}
	code, err := statusCode(first)
	if err != nil {
		return ssdpReply{}, err
	}
	if code != 200 {
		return ssdpReply{}, fmt.Errorf("ssdp status %d", code)
	}
	return ssdpReply{Location: h["location"], USN: h["usn"], Server: h["server"], ST: h["st"]}, nil
}

// usnUUID returns the "uuid:…" part of a USN such as
// "uuid:abc::urn:schemas-upnp-org:device:MediaRenderer:1".
func usnUUID(usn string) string {
	u, _, _ := strings.Cut(usn, "::")
	if !strings.HasPrefix(strings.ToLower(u), "uuid:") {
		return ""
	}
	return normalizeUUID(u)
}

func normalizeUUID(u string) string {
	u = strings.TrimSpace(u)
	if len(u) >= 5 && strings.EqualFold(u[:5], "uuid:") {
		u = u[5:]
	}
	if u == "" {
		return ""
	}
	return "uuid:" + strings.ToLower(u)
}

// upnpDescription is the root device of a UPnP device description.
type upnpDescription struct {
	DeviceType   string `xml:"device>deviceType"`
	FriendlyName string `xml:"device>friendlyName"`
	Manufacturer string `xml:"device>manufacturer"`
	ModelName    string `xml:"device>modelName"`
	ModelNumber  string `xml:"device>modelNumber"`
	UDN          string `xml:"device>UDN"`
}

func parseDescription(b []byte) (upnpDescription, error) {
	var d upnpDescription
	dec := xml.NewDecoder(bytes.NewReader(b))
	// Devices declare odd charsets; the fields we read are ASCII in practice.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	trim := func(s *string) { *s = strings.TrimSpace(*s) }
	trim(&d.DeviceType)
	trim(&d.FriendlyName)
	trim(&d.Manufacturer)
	trim(&d.ModelName)
	trim(&d.ModelNumber)
	trim(&d.UDN)
	return d, nil
}

// samsungInfo is the answer of a Samsung TV's http://host:8001/api/v2/.
type samsungInfo struct {
	Device struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		ModelName  string `json:"modelName"`
		PowerState string `json:"PowerState"`
		WifiMac    string `json:"wifiMac"`
		Type       string `json:"type"`
		OS         string `json:"OS"`
	} `json:"device"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func parseSamsung(b []byte, host string) (found, error) {
	var s samsungInfo
	if err := json.Unmarshal(b, &s); err != nil {
		return found{}, err
	}
	d := s.Device
	if d.ID == "" && !strings.Contains(strings.ToLower(s.Type+" "+d.Type), "samsung") {
		return found{}, errors.New("not a Samsung api/v2 answer")
	}
	// Only an explicit PowerState counts: answering the API is not proof
	// of on, and a missing or odd value is unknown, never on or off.
	state := StateUnknown
	switch strings.ToLower(d.PowerState) {
	case "on":
		state = StateOn
	case "standby":
		state = StateStandby
	}
	name := d.Name
	if name == "" {
		name = s.Name
	}
	f := found{prio: prioSamsung, Device: Device{
		ID: normalizeUUID(d.ID), Name: name, Brand: "Samsung", Model: d.ModelName,
		Kind: "tv", Host: host, Protocol: "samsung", Probe: ProbeSamsung, State: state,
		Raw: map[string]string{"samsung.type": firstNonEmpty(d.Type, s.Type)},
	}}
	if d.OS != "" {
		f.Raw["samsung.os"] = d.OS
	}
	if mac := normalizeMAC(d.WifiMac); mac != "" {
		f.Raw["mac"] = mac
		if f.ID == "" {
			f.ID = "mac:" + mac
		}
	}
	return f, nil
}

// ssdp runs the SSDP/UPnP search on one interface, then fetches each
// device description and asks every answering host for Samsung's api/v2.
func (s *scanner) ssdp(ctx context.Context, ifc Interface, conn PacketConn, wg *sync.WaitGroup) {
	var mu sync.Mutex
	seenLoc := map[string]bool{}
	seenHost := map[string]bool{}
	s.resend(ctx, conn, []byte(mSearch), ssdpAddr)
	s.listen(ctx, conn, func(b []byte, from net.IP) {
		r, err := parseSSDP(b)
		if err != nil {
			return
		}
		host := from.String()
		mu.Lock()
		newHost, newLoc := !seenHost[host], r.Location != "" && !seenLoc[r.Location]
		hostsFull, locsFull := newHost && len(seenHost) >= ssdpMaxHosts, newLoc && len(seenLoc) >= ssdpMaxLocations
		if !hostsFull && !locsFull {
			seenHost[host] = true
			if r.Location != "" {
				seenLoc[r.Location] = true
			}
		}
		mu.Unlock()
		switch {
		case hostsFull:
			s.limit("SSDP hosts per interface", ssdpMaxHosts)
			return
		case locsFull:
			s.limit("SSDP locations per interface", ssdpMaxLocations)
			return
		}
		if newHost && s.reserveFetch() {
			wg.Add(1)
			go func() { defer wg.Done(); s.samsung(ctx, host) }()
		}
		if !newLoc && r.Location != "" {
			return
		}
		f := found{prio: prioSSDPHeaders, Device: Device{
			ID: usnUUID(r.USN), Host: host, Protocol: "ssdp", State: StateUnknown,
			Raw: map[string]string{"ssdp.server": r.Server, "ssdp.location": r.Location},
		}}
		// The headers alone already make a device; the description, when
		// it arrives in time, describes it better.
		s.add(f)
		if r.Location == "" || !sameHost(r.Location, host) {
			return // never follow a LOCATION to a host other than the sender
		}
		if !s.reserveFetch() {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := s.get(ctx, r.Location, descTimeout, descLimit)
			if err != nil {
				s.errorf("ssdp %s: %w", r.Location, err)
				return
			}
			d, err := parseDescription(body)
			if err != nil {
				s.errorf("ssdp %s: %w", r.Location, err)
				return
			}
			s.add(found{prio: prioSSDP, Device: Device{
				ID: firstNonEmpty(normalizeUUID(d.UDN), f.ID), Name: d.FriendlyName, Brand: d.Manufacturer,
				Model: firstNonEmpty(d.ModelName, d.ModelNumber), Host: host, Protocol: "ssdp", State: StateUnknown,
				Raw: map[string]string{"upnp.deviceType": d.DeviceType},
			}})
		}()
	})
}

// samsung asks host for Samsung's api/v2 and adds the TV when it answers.
func (s *scanner) samsung(ctx context.Context, host string) {
	u := "http://" + net.JoinHostPort(host, strconv.Itoa(samsungPort)) + "/api/v2/"
	body, err := s.get(ctx, u, samsungTimeout, samsungLimit)
	if err != nil {
		return // most hosts are not Samsung TVs
	}
	f, err := parseSamsung(body, host)
	if err != nil {
		return
	}
	s.add(f)
}

func sameHost(rawURL, host string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() == host
}

func normalizeMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return ""
	}
	return hw.String()
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
