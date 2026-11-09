package tui

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/store"
)

// itemState is how an item reads right now.
type itemState int

const (
	stateOff itemState = iota
	stateOn
	stateOffline
	statePending // in the config but not in the report yet (just added)
	stateUnknown // a network item without a fresh known state: not off, not 0 W
)

// row is one item, joined from the config, the report and the sources.
type row struct {
	it        config.Item
	state     itemState
	watts     float64
	est       bool                 // estimated (follows, schedule, manual), not measured
	disp      *energy.DisplayState // fresh DDC reading driving the watts (S19)
	net       *energy.NetworkState // fresh network reading (S20)
	lastTS    time.Time            // network: newest reading, zero if none
	src       *Source
	todayWh   float64
	todayCost float64
	billWh    float64
	billCost  float64
	trend     []float64
}

// board is everything the views draw, derived once per data change so View
// only lays it out.
type board struct {
	nf       numFmt
	cfg      *config.Config
	r        energy.Report
	sources  []Source
	trend    Trend
	hasTrend bool
	rows     []row

	onCount    int
	measuredW  float64
	estimatedW float64
	offline    []string  // names of offline items
	unknown    []string  // names of network items without a known state now
	offSince   time.Time // newest sample of the offline sources
	dayIdx     int       // 1-based day of the billing period
	days       int
	fullDays   int
	fullAvgWh  float64
	billPct    float64 // so far ÷ projected, 0–1
	billMeasWh float64
	billEstWh  float64
}

func newBoard(cfg *config.Config, r energy.Report, sources []Source, tr Trend, hasTrend bool) *board {
	b := &board{nf: newNumFmt(cfg.Currency), cfg: cfg, r: r, sources: sources, trend: tr, hasTrend: hasTrend}
	now := map[string]energy.ItemNow{}
	for _, n := range r.Items {
		now[n.Name] = n
	}
	today := map[string]energy.ItemTotal{}
	for _, t := range r.Today.Items {
		today[t.Name] = t
	}
	bill := map[string]energy.ItemTotal{}
	for _, t := range r.Bill.Items {
		bill[t.Name] = t
	}
	for _, it := range cfg.Items {
		rw := row{it: it, est: it.Mode != config.ModeMeasured}
		if it.Mode == config.ModeMeasured {
			rw.src = b.source(it.Source)
		}
		n, ok := now[it.Name]
		rw.net, rw.lastTS = n.Network, n.LastTS
		switch {
		case !ok:
			rw.state = statePending
		case it.Mode == config.ModeNetwork && n.Offline:
			rw.state = stateUnknown
			b.unknown = append(b.unknown, it.Name)
		case n.Offline || (rw.src != nil && !rw.src.Online):
			rw.state = stateOffline
			b.offline = append(b.offline, it.Name)
			if rw.src != nil && rw.src.LastTS.After(b.offSince) {
				b.offSince = rw.src.LastTS
			} else if rw.src == nil && n.LastTS.After(b.offSince) {
				b.offSince = n.LastTS
			}
		case n.On:
			rw.state, rw.watts, rw.disp = stateOn, n.Watts, n.Display
			if !rw.standby() {
				b.onCount++
			}
			if rw.est {
				b.estimatedW += n.Watts
			} else {
				b.measuredW += n.Watts
			}
		}
		rw.todayWh, rw.todayCost = today[it.Name].Wh, today[it.Name].Cost
		rw.billWh, rw.billCost = bill[it.Name].Wh, bill[it.Name].Cost
		if rw.est {
			b.billEstWh += rw.billWh
		} else {
			b.billMeasWh += rw.billWh
		}
		if hasTrend {
			rw.trend = tr.Items[it.Name]
		}
		b.rows = append(b.rows, rw)
	}
	p := r.Period
	b.days = p.Days()
	if !p.Start.IsZero() {
		at := r.At.In(p.Start.Location())
		mid := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
		for d := p.Start; d.Before(mid); d = d.AddDate(0, 0, 1) {
			b.dayIdx++
		}
		b.dayIdx++
	}
	var full float64
	for i, d := range r.Days {
		if i < len(r.Days)-1 {
			full += d.Wh
			b.fullDays++
		}
	}
	if b.fullDays > 0 {
		b.fullAvgWh = full / float64(b.fullDays)
	}
	if r.Projected.Wh > 0 {
		b.billPct = math.Min(1, r.Bill.Wh/r.Projected.Wh)
	}
	return b
}

func (b *board) source(name string) *Source {
	for i := range b.sources {
		if b.sources[i].Name == name {
			return &b.sources[i]
		}
	}
	return nil
}

// partial reports whether a measured item is offline or a network item's
// state is unknown, so the watts now are low.
func (b *board) partial() bool { return len(b.offline)+len(b.unknown) > 0 }

func (b *board) offlineNames() string { return orList(b.offline) }

// nowNote names what partial leaves out: "PC Linux offline, PS5 unknown".
func (b *board) nowNote() string {
	var parts []string
	if len(b.offline) > 0 {
		parts = append(parts, orList(b.offline)+" offline")
	}
	if len(b.unknown) > 0 {
		parts = append(parts, orList(b.unknown)+" unknown")
	}
	return strings.Join(parts, ", ")
}

// unknownTime lists the items with unknown time in t: "PS5 unknown 3 h
// 10 min". Their totals count none of it, so t is a lower bound.
func unknownTime(t energy.Totals, sep string) string {
	var parts []string
	for _, x := range t.Items {
		if x.UnknownSeconds > 0 {
			parts = append(parts, x.Name+sep+duration(x.UnknownSeconds))
		}
	}
	return strings.Join(parts, ", ")
}

// billLow reports whether the bill so far, and its projection, may be low:
// a measured item is offline or a network item has unknown time.
func (b *board) billLow() bool { return len(b.offline) > 0 || b.r.Bill.Incomplete }

// billLowNote is why billLow: "PC Linux missing, PS5 unknown 3 h 10 min".
func (b *board) billLowNote() string {
	var parts []string
	if len(b.offline) > 0 {
		parts = append(parts, orList(b.offline)+" missing")
	}
	if u := unknownTime(b.r.Bill, " unknown "); u != "" {
		parts = append(parts, u)
	}
	return strings.Join(parts, ", ")
}

// duration is "45 min", "3 h 10 min" or "2 d 4 h", rounded to the minute.
func duration(sec float64) string {
	mins := int(math.Round(sec / 60))
	switch {
	case mins < 1:
		return "< 1 min"
	case mins < 60:
		return itoa(mins) + " min"
	case mins < 24*60:
		if mins%60 == 0 {
			return itoa(mins/60) + " h"
		}
		return itoa(mins/60) + " h " + itoa(mins%60) + " min"
	}
	h := int(math.Round(float64(mins) / 60))
	if h%24 == 0 {
		return itoa(h/24) + " d"
	}
	return itoa(h/24) + " d " + itoa(h%24) + " h"
}

// standby reports a network item whose fresh reading says standby.
func (rw row) standby() bool { return rw.net != nil && rw.net.State == store.NetworkStandby }

// wattsText is the Now column: "70,9 W", "~25,0 W", "0 W", "offline" or
// "unknown".
func (b *board) wattsText(rw row) (string, st) {
	switch rw.state {
	case stateOffline:
		return "offline", sEr
	case stateUnknown:
		return "unknown", sWa
	case statePending:
		return "…", sMu
	case stateOff:
		return "0 W", sMu
	}
	if rw.est {
		return "~" + b.nf.watts(rw.watts), sT2.italic()
	}
	return b.nf.watts(rw.watts), sTx.bold()
}

// glyph is ● on (mode color), ◐ standby, ○ off, ✗ offline, ? unknown.
func glyph(rw row) (string, st) {
	switch {
	case rw.state == stateOn && rw.standby():
		return "◐", sT2
	case rw.state == stateOn:
		return "●", st{fg: modeRole(rw.it.Mode)}
	case rw.state == stateOffline:
		return "✗", sEr
	case rw.state == stateUnknown:
		return "?", sWa
	}
	return "○", sMu
}

func modeRole(m config.Mode) role {
	switch m {
	case config.ModeMeasured:
		return ms
	case config.ModeFollows:
		return fl
	case config.ModeSchedule:
		return sc
	case config.ModeManual:
		return mn
	case config.ModeNetwork:
		return t2
	}
	return mu
}

// rule is the "Source or rule" column.
func (b *board) rule(rw row) string {
	it := rw.it
	switch it.Mode {
	case config.ModeMeasured:
		if rw.state == stateOffline {
			if rw.src != nil && !rw.src.LastTS.IsZero() {
				return it.Source + " · last seen " + clock(rw.src.LastTS)
			}
			return it.Source + " · no samples yet"
		}
		if rw.src != nil && rw.src.Via != "" {
			return it.Source + " · " + rw.src.Via
		}
		return it.Source
	case config.ModeFollows:
		if note := ddcNote(rw); note != "" {
			return note
		}
		return "on while " + orList(it.Follows) + " is on"
	case config.ModeSchedule:
		var parts []string
		if q := it.Quantity; q > 1 {
			parts = append(parts, itoa(q)+" ×")
		}
		if l := strings.TrimSpace(it.Brand + " " + it.Model); l != "" {
			parts = append(parts, l)
		}
		parts = append(parts, b.nf.typical(it.Watts))
		return strings.Join(parts, " ") + " · " + rangeText(it.Schedule)
	case config.ModeNetwork:
		host := it.Host
		if rw.net != nil && rw.net.Host != "" {
			host = rw.net.Host
		}
		return it.Source + " · " + netNote(rw) + " · " + host
	case config.ModeManual:
		verb := "off"
		next := "space turns on"
		if rw.state == stateOn {
			verb, next = "on", "space turns off"
		}
		if t, ok := b.trend.Since[it.Name]; ok && b.hasTrend {
			return verb + " since " + clock(t) + " · " + next
		}
		return verb + " · " + next
	}
	return ""
}

// ddcNote is "brightness 65 % · DDC via linux", or "sleeping · DDC via
// linux", while a fresh DDC reading drives the item; else "".
func ddcNote(rw row) string {
	d := rw.disp
	if d == nil {
		return ""
	}
	if !d.On {
		return "sleeping · DDC via " + d.Source
	}
	pct := 0
	if d.Max > 0 {
		pct = int(math.Round(100 * float64(d.Brightness) / float64(d.Max)))
	}
	return "brightness " + itoa(pct) + " % · DDC via " + d.Source
}

// netNote is a network item's state now: "on", "standby", "state unknown
// at 20:14", "no reading since 18:40" or "no readings yet". On is the
// device's power state, not proof it is in use.
func netNote(rw row) string {
	switch {
	case rw.state == stateOn && rw.net != nil:
		return rw.net.State
	case rw.net != nil:
		return "state unknown at " + clock(rw.net.TS)
	case !rw.lastTS.IsZero():
		return "no reading since " + clock(rw.lastTS)
	}
	return "no readings yet"
}

func clock(t time.Time) string { return t.Format("15:04") }

func itoa(n int) string { return strconv.Itoa(n) }
