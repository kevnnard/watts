package discover

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// A minimal DNS message codec (RFC 1035) for mDNS: questions and A, AAAA,
// PTR, SRV and TXT records, with name compression both ways. Other record
// types keep their raw data.

const (
	dnsTypeA    = 1
	dnsTypePTR  = 12
	dnsTypeTXT  = 16
	dnsTypeAAAA = 28
	dnsTypeSRV  = 33
	dnsClassIN  = 1
	// mDNS reuses the class's top bit: "unicast response" in a question,
	// "cache flush" in a record (RFC 6762 §5.4, §10.2).
	dnsClassTopBit = 0x8000

	dnsFlagResponse = 0x8000
)

type dnsQuestion struct {
	Name  string
	Type  uint16
	Class uint16
}

type dnsRR struct {
	Name  string
	Type  uint16
	Class uint16
	TTL   uint32

	Target   string   // PTR, SRV
	Priority uint16   // SRV
	Weight   uint16   // SRV
	Port     uint16   // SRV
	Text     []string // TXT
	IP       net.IP   // A, AAAA
	Data     []byte   // other types
}

type dnsMsg struct {
	ID         uint16
	Flags      uint16
	Questions  []dnsQuestion
	Answers    []dnsRR
	Authority  []dnsRR
	Additional []dnsRR
}

// Records returns every answer, authority and additional record.
func (m dnsMsg) Records() []dnsRR {
	out := append([]dnsRR(nil), m.Answers...)
	out = append(out, m.Authority...)
	return append(out, m.Additional...)
}

var errDNSShort = errors.New("dns: message too short")

// splitName splits a presentation name into labels; "\." and "\\" escape
// a dot or backslash inside a label, as in mDNS instance names.
func splitName(name string) []string {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return nil
	}
	var labels []string
	var cur strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '\\' && i+1 < len(name):
			i++
			cur.WriteByte(name[i])
		case c == '.':
			labels = append(labels, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(labels, cur.String())
}

func escapeLabel(l string) string {
	if !strings.ContainsAny(l, `.\`) {
		return l
	}
	r := strings.NewReplacer(`\`, `\\`, `.`, `\.`)
	return r.Replace(l)
}

type dnsEncoder struct {
	buf  []byte
	comp map[string]int // lower-cased name suffix → offset
}

func (e *dnsEncoder) u16(v uint16) { e.buf = binary.BigEndian.AppendUint16(e.buf, v) }
func (e *dnsEncoder) u32(v uint32) { e.buf = binary.BigEndian.AppendUint32(e.buf, v) }

func (e *dnsEncoder) name(name string) error {
	labels := splitName(name)
	for i := range labels {
		key := strings.ToLower(strings.Join(labels[i:], "."))
		if off, ok := e.comp[key]; ok {
			e.u16(0xC000 | uint16(off))
			return nil
		}
		if len(e.buf) < 0x3FFF {
			e.comp[key] = len(e.buf)
		}
		l := labels[i]
		if len(l) == 0 || len(l) > 63 {
			return fmt.Errorf("dns: bad label %q in %q", l, name)
		}
		e.buf = append(e.buf, byte(len(l)))
		e.buf = append(e.buf, l...)
	}
	e.buf = append(e.buf, 0)
	return nil
}

func (e *dnsEncoder) rr(r dnsRR) error {
	if err := e.name(r.Name); err != nil {
		return err
	}
	e.u16(r.Type)
	e.u16(r.Class)
	e.u32(r.TTL)
	lenAt := len(e.buf)
	e.u16(0)
	switch r.Type {
	case dnsTypePTR:
		if err := e.name(r.Target); err != nil {
			return err
		}
	case dnsTypeSRV:
		e.u16(r.Priority)
		e.u16(r.Weight)
		e.u16(r.Port)
		if err := e.name(r.Target); err != nil {
			return err
		}
	case dnsTypeTXT:
		if len(r.Text) == 0 {
			e.buf = append(e.buf, 0)
		}
		for _, t := range r.Text {
			if len(t) > 255 {
				return fmt.Errorf("dns: TXT string longer than 255 bytes")
			}
			e.buf = append(e.buf, byte(len(t)))
			e.buf = append(e.buf, t...)
		}
	case dnsTypeA:
		ip := r.IP.To4()
		if ip == nil {
			return fmt.Errorf("dns: A record without IPv4 %v", r.IP)
		}
		e.buf = append(e.buf, ip...)
	case dnsTypeAAAA:
		ip := r.IP.To16()
		if ip == nil {
			return fmt.Errorf("dns: AAAA record without IP %v", r.IP)
		}
		e.buf = append(e.buf, ip...)
	default:
		e.buf = append(e.buf, r.Data...)
	}
	binary.BigEndian.PutUint16(e.buf[lenAt:], uint16(len(e.buf)-lenAt-2))
	return nil
}

func (m dnsMsg) encode() ([]byte, error) {
	e := &dnsEncoder{buf: make([]byte, 0, 512), comp: map[string]int{}}
	e.u16(m.ID)
	e.u16(m.Flags)
	e.u16(uint16(len(m.Questions)))
	e.u16(uint16(len(m.Answers)))
	e.u16(uint16(len(m.Authority)))
	e.u16(uint16(len(m.Additional)))
	for _, q := range m.Questions {
		if err := e.name(q.Name); err != nil {
			return nil, err
		}
		e.u16(q.Type)
		e.u16(q.Class)
	}
	for _, sec := range [][]dnsRR{m.Answers, m.Authority, m.Additional} {
		for _, r := range sec {
			if err := e.rr(r); err != nil {
				return nil, err
			}
		}
	}
	return e.buf, nil
}

// readName reads a possibly compressed name at off, returning it and the
// offset just past it in the original (uncompressed) position.
func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(msg) {
			return "", 0, errDNSShort
		}
		c := int(msg[off])
		switch c & 0xC0 {
		case 0x00:
			if c == 0 {
				if end < 0 {
					end = off + 1
				}
				return strings.Join(labels, "."), end, nil
			}
			if off+1+c > len(msg) {
				return "", 0, errDNSShort
			}
			labels = append(labels, escapeLabel(string(msg[off+1:off+1+c])))
			off += 1 + c
		case 0xC0:
			if off+1 >= len(msg) {
				return "", 0, errDNSShort
			}
			if jumps++; jumps > 32 {
				return "", 0, errors.New("dns: compression loop")
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
		default:
			return "", 0, fmt.Errorf("dns: bad label byte %#x", c)
		}
	}
}

func decodeDNS(msg []byte) (dnsMsg, error) {
	var m dnsMsg
	if len(msg) < 12 {
		return m, errDNSShort
	}
	be := binary.BigEndian
	m.ID, m.Flags = be.Uint16(msg[0:]), be.Uint16(msg[2:])
	qd, an, ns, ar := int(be.Uint16(msg[4:])), int(be.Uint16(msg[6:])), int(be.Uint16(msg[8:])), int(be.Uint16(msg[10:]))
	off := 12
	for range qd {
		name, n, err := readName(msg, off)
		if err != nil {
			return m, err
		}
		if n+4 > len(msg) {
			return m, errDNSShort
		}
		m.Questions = append(m.Questions, dnsQuestion{Name: name, Type: be.Uint16(msg[n:]), Class: be.Uint16(msg[n+2:])})
		off = n + 4
	}
	read := func(count int) ([]dnsRR, error) {
		var out []dnsRR
		for range count {
			r, n, err := readRR(msg, off)
			if err != nil {
				return out, err
			}
			out = append(out, r)
			off = n
		}
		return out, nil
	}
	var err error
	if m.Answers, err = read(an); err != nil {
		return m, err
	}
	if m.Authority, err = read(ns); err != nil {
		return m, err
	}
	m.Additional, err = read(ar)
	return m, err
}

func readRR(msg []byte, off int) (dnsRR, int, error) {
	be := binary.BigEndian
	var r dnsRR
	name, n, err := readName(msg, off)
	if err != nil {
		return r, 0, err
	}
	if n+10 > len(msg) {
		return r, 0, errDNSShort
	}
	r.Name = name
	r.Type, r.Class, r.TTL = be.Uint16(msg[n:]), be.Uint16(msg[n+2:]), be.Uint32(msg[n+4:])
	rdlen := int(be.Uint16(msg[n+8:]))
	start, end := n+10, n+10+rdlen
	if end > len(msg) {
		return r, 0, errDNSShort
	}
	data := msg[start:end]
	switch r.Type {
	case dnsTypePTR:
		if r.Target, _, err = readName(msg, start); err != nil {
			return r, 0, err
		}
	case dnsTypeSRV:
		if rdlen < 7 {
			return r, 0, errDNSShort
		}
		r.Priority, r.Weight, r.Port = be.Uint16(data), be.Uint16(data[2:]), be.Uint16(data[4:])
		if r.Target, _, err = readName(msg, start+6); err != nil {
			return r, 0, err
		}
	case dnsTypeTXT:
		for i := 0; i < len(data); {
			l := int(data[i])
			if i+1+l > len(data) {
				return r, 0, errDNSShort
			}
			if l > 0 {
				r.Text = append(r.Text, string(data[i+1:i+1+l]))
			}
			i += 1 + l
		}
	case dnsTypeA:
		if rdlen != 4 {
			return r, 0, fmt.Errorf("dns: A record of %d bytes", rdlen)
		}
		r.IP = net.IP(append([]byte(nil), data...))
	case dnsTypeAAAA:
		if rdlen != 16 {
			return r, 0, fmt.Errorf("dns: AAAA record of %d bytes", rdlen)
		}
		r.IP = net.IP(append([]byte(nil), data...))
	default:
		r.Data = append([]byte(nil), data...)
	}
	return r, end, nil
}
