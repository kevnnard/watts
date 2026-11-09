package tui

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
)

// itemState is how an item reads right now.
type itemState int

const (
	stateOff itemState = iota
	stateOn
	stateOffline
	statePending // in the config but not in the report yet (just added)
)

// row is one item, joined from the config, the report and the sources.
type row struct {
	it        config.Item
	state     itemState
	watts     float64
	est       bool                 // estimated (follows, schedule, manual), not measured
	disp      *energy.DisplayState // fresh DDC reading driving the watts (S19)
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
		switch {
		case !ok:
			rw.state = statePending
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
			b.onCount++
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

// partial reports whether a measured item is offline, so totals are low.
func (b *board) partial() bool { return len(b.offline) > 0 }

func (b *board) offlineNames() string { return orList(b.offline) }

// wattsText is the Now column: "70,9 W", "~25,0 W", "0 W" or "offline".
func (b *board) wattsText(rw row) (string, st) {
	switch rw.state {
	case stateOffline:
		return "offline", sEr
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

// glyph is ● on (mode color), ○ off, ✗ offline.
func glyph(rw row) (string, st) {
	switch rw.state {
	case stateOn:
		return "●", st{fg: modeRole(rw.it.Mode)}
	case stateOffline:
		return "✗", sEr
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

func clock(t time.Time) string { return t.Format("15:04") }

func itoa(n int) string { return strconv.Itoa(n) }
