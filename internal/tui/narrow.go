package tui

import (
	"math"
	"sort"
	"strings"
)

// ---- narrow cockpit, 80 × 24 up to the full layout ------------------------
//
// At 80 × 24 this is the 01 narrow artboard. Wider or taller terminals that
// do not fit the full cockpit get the same layout with the extra columns
// given to the gauges, the source column and the sparklines, and the extra
// rows to the items table.

func (m *Model) drawNarrow(s *screen) {
	W, H := s.w, s.h
	m.header(s, false)
	extra := W - MinWidth
	nowW := 28 + extra/2
	todayW := 24 + extra/4
	billW := W - nowW - todayW
	m.nowSmall(s, 0, 1, nowW)
	m.todaySmall(s, nowW, 1, todayW)
	m.billSmall(s, nowW+todayW, 1, billW)
	m.itemsNarrow(s, 9, H-14)
	m.periodLine(s, H-5)
	m.sourcesLine(s, H-4)
	legend(s, 2, H-3, W-4, m.b.nf)
	msg, isErr := m.status()
	if msg != "" {
		sty := sMu
		if isErr {
			sty = sEr
		}
		s.put(2, H-2, trunc(msg, W-4), sty)
	}
	keybar(s, H-1, dashKeys, "", false)
}

func (m *Model) nowSmall(s *screen, x0, y0, w int) {
	b := m.b
	s.box(x0, y0, w, 8, "Now", "")
	if !m.loaded {
		return
	}
	L, R := x0+2, x0+w-3
	s.put(L, y0+1, b.nf.watts(b.r.TotalWatts), sTx.bold())
	if b.partial() {
		s.rput(R, y0+1, "partial", sWa)
	} else {
		s.rput(R, y0+1, itoa(b.onCount)+" of "+itoa(len(b.rows))+" on", sMu)
	}
	s.seqMax(L, y0+2, w-4, []seg{{b.nf.watts(b.measuredW), sTx.bold()}, {" + ", sMu}, {"~" + b.nf.watts(b.estimatedW), sT2.italic()}})
	if b.hasTrend {
		inner := w - 4
		part := anyTrue(b.trend.Partial, inner)
		s.vbars(L, y0+3, 3, buckets(b.trend.Total, inner), niceMax(peak(b.trend.Total)), func(i int) st {
			if part[i] {
				return sWa
			}
			return sAc
		})
	} else {
		s.put(L, y0+4, "no history yet", sMu)
	}
	s.put(L, y0+6, "last 3 h", sMu)
	s.rput(R, y0+6, "now", sMu)
}

func (m *Model) todaySmall(s *screen, x0, y0, w int) {
	b := m.b
	s.box(x0, y0, w, 8, "Today", "")
	if !m.loaded {
		return
	}
	L := x0 + 2
	s.put(L, y0+1, b.nf.kwh2(b.r.Today.Wh)+" kWh", sTx.bold())
	s.put(L, y0+2, b.nf.money(b.r.Today.Cost), sTx.bold())
	if sec := b.nf.second(b.r.Today.Cost); sec != "" {
		s.put(L, y0+3, trunc(sec, w-4), sMu)
	}
	if b.r.Today.Incomplete {
		s.put(L, y0+4, trunc("may be low", w-4), sWa)
	}
	var top *row
	for i := range b.rows {
		if top == nil || b.rows[i].todayWh > top.todayWh {
			top = &b.rows[i]
		}
	}
	if top != nil && top.todayWh > 0 {
		s.seqMax(L, y0+5, w-4, []seg{{"most ", sMu}, {top.it.Name + " " + pct(top.todayWh, b.r.Today.Wh), sT2}})
	}
	s.put(L, y0+6, "00:00 → "+clock(b.r.At), sMu)
}

func (m *Model) billSmall(s *screen, x0, y0, w int) {
	b := m.b
	right := ""
	if m.loaded && b.days > 0 {
		right = "day " + itoa(b.dayIdx) + "/" + itoa(b.days)
	}
	s.box(x0, y0, w, 8, "Bill", right)
	if !m.loaded {
		return
	}
	L, R := x0+2, x0+w-3
	s.seqMax(L, y0+1, w-4, []seg{{b.nf.money(b.r.Bill.Cost), sTx.bold()}, {" of ", sMu}, {b.nf.money(b.r.Projected.Cost), sTx.bold()}})
	barW := w - 9
	f := int(math.Round(b.billPct * float64(barW)))
	s.put(L, y0+2, strings.Repeat("█", f), sAc)
	s.put(L+f, y0+2, strings.Repeat("░", barW-f), sBd)
	s.rput(R, y0+2, pct(b.billPct, 1), sT2)
	if sec := b.nf.second(b.r.Bill.Cost); sec != "" {
		s.put(L, y0+3, trunc(sec+" so far", w-4), sMu)
	}
	s.put(L, y0+4, trunc(b.nf.kwh1(b.r.Bill.Wh)+" kWh · "+daysLeft(b), w-4), sMu)
	if b.billLow() {
		s.put(L, y0+5, trunc("projection may be low", w-4), sWa)
	} else {
		s.put(L, y0+5, trunc("projected, not billed", w-4), sMu)
	}
	s.put(L, y0+6, trunc(b.r.Period.Start.Format("Jan 2")+" → "+b.r.Period.End.Format("Jan 2"), w-4), sMu)
}

func (m *Model) itemsNarrow(s *screen, y0, h int) {
	b := m.b
	W := s.w
	s.box(0, y0, W, h, "Items", itoa(len(b.rows)))
	extra := W - MinWidth
	spW := 12 + extra/2
	costR := W - 3
	kwhR := costR - 7
	sparkX := kwhR - 5 - spW
	nowR := sparkX - 3
	srcX := 26
	srcW := nowR - 8 - srcX
	y := y0 + 1
	s.put(5, y, "Item", sMu)
	s.put(17, y, "Mode", sMu)
	s.rput(nowR, y, "Now", sMu)
	s.put(sparkX, y, "last 3 h", sMu)
	s.rput(kwhR, y, "kWh", sMu)
	s.rput(costR, y, "Cost", sMu)
	if len(b.rows) == 0 {
		s.put(5, y+2, "No items yet. Press a to add one.", sMu)
		return
	}
	for _, l := range m.window(m.narrowLines(), h-3) {
		y++
		rw := b.rows[l.item]
		if l.detail > 0 {
			m.narrowDetail(s, y, rw, l.detail)
			continue
		}
		if l.item == m.sel {
			s.tint(1, y, W-2, 1, b2)
			s.put(1, y, "▸", sAc.bold())
		}
		g, gs := glyph(rw)
		s.put(3, y, g, gs)
		s.put(5, y, trunc(rw.it.Name, 11), sTx.bold())
		s.put(17, y, string(rw.it.Mode), st{fg: modeRole(rw.it.Mode)})
		rst := sMu
		if rw.state == stateOffline || rw.state == stateUnknown {
			rst = sWa
		}
		s.put(srcX, y, trunc(b.rule(rw), srcW), rst)
		wt, ws := b.wattsText(rw)
		s.rput(nowR, y, wt, ws)
		m.itemSparkNarrow(s, sparkX, y, spW, rw)
		s.rput(kwhR, y, b.nf.kwh2(rw.todayWh), sTx)
		s.rput(costR, y, b.nf.money(rw.todayCost), sTx)
	}
}

func (m *Model) itemSparkNarrow(s *screen, x, y, w int, rw row) {
	switch {
	case rw.state == stateOff:
		s.put(x, y, "off", sMu)
	case rw.trend == nil:
	default:
		sty := st{fg: modeRole(rw.it.Mode)}
		if rw.state == stateOffline {
			sty = st{fg: ms}
		}
		s.put(x, y, spark(buckets(rw.trend, w), niceMax(peak(rw.trend)*1.15)), sty)
	}
}

// narrowDetail is the compact form of the expanded Linux PC rows.
func (m *Model) narrowDetail(s *screen, y int, rw row, n int) {
	ds := m.details(rw)
	d := ds[n-1]
	W := s.w
	if d.note != "" {
		s.seqMax(5, y, W-8, []seg{{d.tree, sBd}, {d.note, sMu}})
		return
	}
	if d.kind == "measured" {
		// all measured parts on one row
		if n > 1 && ds[n-2].kind == "measured" {
			return
		}
		parts := []seg{{"├ ", sBd}}
		for i, p := range ds {
			if p.kind != "measured" {
				break
			}
			if i > 0 {
				parts = append(parts, seg{"  ", sMu})
			}
			parts = append(parts, seg{p.name + " ", sT2}, seg{p.watts, sTx})
		}
		parts = append(parts, seg{"  measured", st{fg: ms}})
		s.seqMax(5, y, W-8, parts)
		return
	}
	rest := strings.TrimPrefix(d.reads, "fixed ")
	if i := strings.Index(rest, ", "); i >= 0 {
		rest = strings.Replace(rest[i+2:], "at ", "", 1)
	}
	s.seqMax(5, y, W-8, []seg{{d.tree, sBd}, {"rest ", sT2}, {d.watts, sT2.italic()}, {"  " + rest, sMu}})
}

// shortName is the word that tells an item apart: the last word when it is
// a plain lowercase noun ("Desk fan" → "fan"), else the first ("PC Linux" →
// "PC").
func shortName(name string) string {
	f := strings.Fields(name)
	if len(f) == 0 {
		return name
	}
	if last := f[len(f)-1]; len(f) > 1 && strings.ToLower(last) == last && strings.IndexFunc(last, func(r rune) bool { return r < 'a' || r > 'z' }) < 0 {
		return last
	}
	return f[0]
}

// narrowLines is itemLines with the measured parts on one row.
func (m *Model) narrowLines() []line {
	var out []line
	for _, l := range m.itemLines() {
		if l.detail > 1 {
			ds := m.details(m.b.rows[l.item])
			if ds[l.detail-1].kind == "measured" && ds[l.detail-2].kind == "measured" {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// periodLine is "Period  PC Linux 5,4   LG 29WQ600 2,1 … kWh".
func (m *Model) periodLine(s *screen, y int) {
	b := m.b
	if !m.loaded {
		return
	}
	rows := append([]row(nil), b.rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].billWh > rows[j].billWh })
	build := func(short bool) []seg {
		parts := []seg{{"Period  ", sMu}}
		for i, rw := range rows {
			name := rw.it.Name
			if short {
				name = shortName(name)
			}
			if i > 0 {
				parts = append(parts, seg{"   ", sMu})
			}
			parts = append(parts, seg{name, st{fg: modeRole(rw.it.Mode)}}, seg{" " + b.nf.kwh1(rw.billWh), sT2})
		}
		return append(parts, seg{" kWh", sMu})
	}
	parts := build(false)
	if segsWidth(parts) > s.w-4 {
		parts = build(true)
	}
	s.seqMax(2, y, s.w-4, parts)
}

func (m *Model) sourcesLine(s *screen, y int) {
	b := m.b
	var parts []seg
	for i, src := range b.sources {
		if i > 0 {
			parts = append(parts, seg{"   ", sMu})
		}
		parts = append(parts, seg{src.Name + " ", sT2})
		if src.Online {
			parts = append(parts, seg{"● online", st{fg: ms}})
			if !src.LastTS.IsZero() {
				parts = append(parts, seg{" " + clock(src.LastTS), sMu})
			}
		} else {
			parts = append(parts, seg{"✗ offline", sEr})
			if !src.LastTS.IsZero() {
				parts = append(parts, seg{" since " + clock(src.LastTS), sMu})
			}
		}
	}
	end := s.seqMax(2, y, s.w-4, parts)
	iv := m.cfg.SampleIntervalSeconds
	if iv <= 0 {
		iv = 60
	}
	note := "samples every " + itoa(iv) + " s"
	if end+2+textWidth(note) <= s.w-3 {
		s.rput(s.w-3, y, note, sMu)
	}
}
