package tui

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
)

// key is one key hint in the key bar.
type key struct{ k, label string }

var dashKeys = []key{{"a", "add"}, {"e", "edit"}, {"d", "delete"}, {"space", "toggle"}, {"t", "tariffs"}, {"c", "currency"}, {"b", "bill"}, {"q", "quit"}, {"n", "network"}}

func (m *Model) size() (int, int) {
	w, h := m.w, m.h
	if w == 0 && h == 0 {
		w, h = FullWidth, FullHeight
	}
	return w, h
}

// draw lays out one frame.
func (m *Model) draw() *screen {
	w, h := m.size()
	s := newScreen(max(w, 0), max(h, 0))
	if w < MinWidth || h < MinHeight {
		tooSmall(s)
		return s
	}
	full := w >= FullWidth && h >= FullHeight
	if full {
		m.drawFull(s)
	} else {
		m.drawNarrow(s)
	}
	if m.modal != nil {
		s.shade()
		if f, ok := m.modal.(*itemForm); ok && f.back != nil {
			// adding from the network view: the form sits on the view
			f.back.draw(m, s)
			s.shade()
		}
		m.modal.draw(m, s)
		keybar(s, s.h-1, m.modal.keys(m), "", false)
	}
	return s
}

func tooSmall(s *screen) {
	lines := []struct {
		t  string
		st st
	}{
		{"Terminal too small", sTx.bold()},
		{itoa(s.w) + " × " + itoa(s.h) + ", needs " + itoa(MinWidth) + " × " + itoa(MinHeight), sMu},
		{"Resize the window or press q to quit.", sMu},
	}
	y := (s.h - len(lines)) / 2
	for i, l := range lines {
		t := trunc(l.t, s.w)
		s.put(max(0, (s.w-textWidth(t))/2), y+i, t, l.st)
	}
}

// keybar draws the key hints and, on the right, a status or error message.
func keybar(s *screen, y int, keys []key, msg string, msgErr bool) {
	s.fill(0, y, s.w, 1, b1)
	x := 1
	for i, k := range keys {
		w := textWidth(k.k) + 1 + textWidth(k.label)
		if x+w > s.w-1 {
			break
		}
		x = s.seq(x, y, []seg{{k.k, sAc.bold()}, {" " + k.label, sMu}}, b1)
		if i < len(keys)-1 {
			x += 2
		}
	}
	if msg != "" {
		room := s.w - 2 - x - 2
		if room >= 12 {
			sty := sMu
			if msgErr {
				sty = sEr
			}
			s.rput(s.w-2, y, trunc(msg, room), sty, b1)
		}
	}
}

func (m *Model) status() (string, bool) {
	switch {
	case m.flash != "":
		return m.flash, m.flashErr
	case m.loadErr != "":
		return m.loadErr, true
	case !m.loaded:
		return "reading the store…", false
	}
	return "updated " + m.report.At.Format("15:04:05"), false
}

// header is the top bar: app, provider and price, date and time.
func (m *Model) header(s *screen, long bool) {
	b := m.b
	s.fill(0, 0, s.w, 1, b1)
	at := m.report.At
	if !m.loaded {
		at = m.now()
	}
	right := at.Format("Jan 2  15:04")
	if long {
		right = at.Format("Mon Jan 2  15:04")
	}
	gap := "  "
	if long {
		gap = "   "
	}
	room := s.w - 3 - textWidth(right) - 2
	var parts []seg
	switch {
	case m.loaded && b.r.HasTariff:
		parts = []seg{{"watts", sAc.bold()}, {gap + b.r.Tariff.Provider, sTx}, {" · ", sMu}, {b.nf.price(b.r.Tariff.PricePerKWh) + " per kWh", sTx}}
	case m.loaded:
		parts = []seg{{"watts", sAc.bold()}, {gap + "no tariff for this period", sWa}, {" · t adds one", sMu}}
	default:
		parts = []seg{{"watts", sAc.bold()}}
	}
	s.seqMax(1, 0, room, parts, b1)
	s.rput(s.w-2, 0, right, sTx.bold(), b1)
}

// ---- full cockpit, ≥ 140 × 40 ------------------------------------------

func (m *Model) drawFull(s *screen) {
	W, H := s.w, s.h
	m.header(s, true)
	nowW := W - 76
	m.nowPanel(s, 0, 1, nowW)
	m.todayPanel(s, nowW, 1)
	m.billPanel(s, nowW+36, 1)
	itemsH := H - 1 - 14 - 13 - 1
	m.itemsFull(s, 15, itemsH)
	by := 15 + itemsH
	m.periodPanel(s, 0, by, W/2)
	m.sourcesPanel(s, W/2, by, W-W/2)
	msg, isErr := m.status()
	keybar(s, H-1, dashKeys, msg, isErr)
}

func (m *Model) nowPanel(s *screen, x0, y0, w int) {
	b := m.b
	s.box(x0, y0, w, 14, "Now", "")
	L, R := x0+2, x0+w-3
	if !m.loaded {
		s.put(L, y0+1, "reading the store…", sMu)
		return
	}
	countText := itoa(b.onCount) + " of " + itoa(len(b.rows)) + " items on"
	x := s.seq(L, y0+1, []seg{{"Total  ", sMu}, {b.nf.watts(b.r.TotalWatts), sTx.bold()}})
	if b.partial() {
		s.seqMax(x+2, y0+1, R-textWidth(countText)-2-(x+2), []seg{{"partial · " + b.nowNote(), sWa}})
	}
	s.rput(R, y0+1, countText, sMu)
	s.seqMax(L, y0+2, w-4, []seg{{b.nf.watts(b.measuredW), sTx.bold()}, {" measured", sMu}, {"  +  ", sMu}, {"~" + b.nf.watts(b.estimatedW), sT2.italic()}, {" estimated", sMu}})

	// stacked bar of what draws power now
	var live []row
	var weights []float64
	for _, rw := range b.rows {
		if rw.state == stateOn && rw.watts > 0 {
			live = append(live, rw)
			weights = append(weights, rw.watts)
		}
	}
	inner := w - 4
	if len(live) > 0 {
		widths := split(inner-(len(live)-1), weights)
		x := L
		for i, rw := range live {
			if widths[i] == 0 {
				continue
			}
			ch := "█"
			if rw.est {
				ch = "▓"
			}
			s.put(x, y0+4, strings.Repeat(ch, widths[i]), st{fg: modeRole(rw.it.Mode)})
			s.put(x, y0+5, trunc(rw.it.Name, widths[i]), sT2)
			x += widths[i] + 1
		}
	} else {
		s.put(L, y0+4, "nothing is drawing power", sMu)
	}

	s.put(L, y0+7, "Last 3 h", sMu)
	if !b.hasTrend {
		s.put(L, y0+10, "no history yet", sMu)
		return
	}
	tot := b.trend.Total
	pk, at := 0.0, 0
	sum, n := 0.0, 0
	for i, v := range tot {
		if math.IsNaN(v) {
			continue
		}
		sum += v
		n++
		if v > pk {
			pk, at = v, i
		}
	}
	if len(b.offline) > 0 && !b.offSince.IsZero() {
		s.rput(R, y0+7, trunc("amber: after "+clock(b.offSince)+", without "+b.offlineNames(), inner-10), sWa)
	} else if n > 0 {
		s.rput(R, y0+7, "avg "+b.nf.watts(sum/float64(n))+" · peak "+b.nf.watts(pk)+" at "+clock(b.trend.Start.Add(time.Duration(at)*time.Minute)), sMu)
	}
	vals := buckets(tot, inner)
	part := anyTrue(b.trend.Partial, inner)
	s.vbars(L, y0+8, 4, vals, niceMax(pk), func(i int) st {
		if part[i] {
			return sWa
		}
		return sAc
	})
	m.timeAxis(s, L, y0+12, inner)
}

// timeAxis labels the 3-hour sparkline: start, each full hour, "now".
func (m *Model) timeAxis(s *screen, x, y, w int) {
	start := m.b.trend.Start
	span := float64(TrendMinutes) / float64(w)
	s.put(x, y, clock(start), sMu)
	s.rput(x+w-1, y, "now", sMu)
	next := x + 6
	for t := start.Truncate(time.Hour).Add(time.Hour); t.Before(start.Add(TrendMinutes * time.Minute)); t = t.Add(time.Hour) {
		col := x + int(t.Sub(start).Minutes()/span)
		if col >= next && col+5 < x+w-4 {
			s.put(col, y, clock(t), sMu)
			next = col + 6
		}
	}
}

func (m *Model) todayPanel(s *screen, x0, y0 int) {
	b := m.b
	s.box(x0, y0, 36, 14, "Today", "")
	if !m.loaded {
		return
	}
	L, R := x0+2, x0+33
	s.put(L, y0+1, b.nf.kwh2(b.r.Today.Wh)+" kWh", sTx.bold())
	s.rput(R, y0+1, b.nf.money(b.r.Today.Cost), sTx.bold())
	s.put(L, y0+2, "00:00 → "+clock(b.r.At), sMu)
	if sec := b.nf.second(b.r.Today.Cost); sec != "" {
		s.rput(R, y0+2, sec, sMu)
	}
	if u := unknownTime(b.r.Today, " unknown "); u != "" {
		low := "may be low: " + u
		if textWidth(low) > 32 {
			low = "may be low: " + orList(b.unknown) + " unknown"
		}
		s.put(L, y0+3, trunc(low, 32), sWa)
	}
	s.put(L, y0+4, "By hour", sMu)
	s.rput(R, y0+4, "kWh", sMu)
	if !b.hasTrend {
		s.put(L, y0+8, "no history yet", sMu)
		return
	}
	hours := b.trend.Hours
	top := niceMax(peak(hours))
	cur := len(hours) - 1
	s.vbars(L, y0+5, 7, hours, top, func(i int) st {
		if i == cur {
			return sAc
		}
		return sT2
	})
	for h := len(hours); h < 24; h++ {
		s.put(L+h, y0+11, "·", sMu)
	}
	s.put(L+25, y0+5, b.nf.short(top), sMu)
	s.put(L+25, y0+11, "0", sMu)
	for _, h := range []int{0, 6, 12, 18} {
		s.put(L+h, y0+12, twoDigits(h), sMu)
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func (m *Model) billPanel(s *screen, x0, y0 int) {
	b := m.b
	right := ""
	if m.loaded && b.days > 0 {
		right = "day " + itoa(b.dayIdx) + " of " + itoa(b.days)
	}
	s.box(x0, y0, 40, 14, "Bill", right)
	if !m.loaded {
		return
	}
	L, R := x0+2, x0+37
	s.seq(L, y0+1, []seg{{b.nf.money(b.r.Bill.Cost), sTx.bold()}, {" so far", sMu}})
	if sec := b.nf.second(b.r.Bill.Cost); sec != "" {
		s.rput(R, y0+1, sec, sMu)
	}
	s.seq(L, y0+2, []seg{{energy.ProjectionLabel + " ", sMu}, {b.nf.money(b.r.Projected.Cost), sTx.bold()}})
	if sec := b.nf.second(b.r.Projected.Cost); sec != "" {
		s.rput(R, y0+2, sec, sMu)
	}
	if b.billLow() {
		s.put(L, y0+3, trunc("may be low: "+b.billLowNote(), 36), sWa)
	}
	filled := int(math.Round(b.billPct * 30))
	s.put(L, y0+4, strings.Repeat("█", filled), sAc)
	s.put(L+filled, y0+4, strings.Repeat("░", 30-filled), sBd)
	s.rput(R, y0+4, pct(b.billPct, 1), sT2)
	s.seq(L, y0+5, []seg{{b.nf.kwh1(b.r.Bill.Wh) + " kWh", sT2}, {" used · " + daysLeft(b), sMu}})

	s.put(L, y0+7, "Per day", sMu)
	s.rput(R, y0+7, "kWh", sMu)
	days := min(b.days, 31)
	vals := make([]float64, len(b.r.Days))
	for i, d := range b.r.Days {
		vals[i] = d.Wh / 1000
	}
	top := niceMax(peak(vals))
	cur := len(vals) - 1
	s.vbars(L, y0+8, 3, vals, top, func(i int) st {
		if i == cur {
			return sAc
		}
		return sT2
	})
	for d := len(vals); d < days; d++ {
		s.put(L+d, y0+10, "·", sMu)
	}
	lx := L + days + 1
	s.put(lx, y0+8, b.nf.short(top), sMu)
	s.put(lx, y0+10, "0", sMu)
	s.put(L, y0+11, b.r.Period.Start.Format("Jan 2"), sMu)
	end := b.r.Period.End.Format("Jan 2")
	if t := L + cur; cur >= 6 && t+7 < L+days-textWidth(end) {
		s.put(t, y0+11, "▲ today", sAc)
	}
	s.rput(L+days-1, y0+11, end, sMu)
	if b.fullDays > 0 {
		s.put(L, y0+12, "avg "+b.nf.kwh2(b.fullAvgWh)+" kWh per full day", sMu)
	} else {
		s.put(L, y0+12, "first day of the period", sMu)
	}
}

func daysLeft(b *board) string {
	n := b.days - b.dayIdx
	if n == 1 {
		return "1 day left"
	}
	return itoa(n) + " days left"
}

// line is one row of the items table: an item or one of its detail rows.
type line struct {
	item   int // index into board rows
	detail int // 0 for the item row, 1.. for detail rows
}

// itemLines lists the table rows, expanding the selected Linux PC.
func (m *Model) itemLines() []line {
	var out []line
	for i, rw := range m.b.rows {
		out = append(out, line{item: i})
		if i == m.sel {
			for d := range m.details(rw) {
				out = append(out, line{item: i, detail: d + 1})
			}
		}
	}
	return out
}

// detail is an expanded row under the selected measured Linux item.
type detail struct {
	tree, name, kind, reads, watts string
	wst                            st
	trend                          []float64 // measured parts only
	note                           string    // offline: a single explanatory line
}

func (m *Model) details(rw row) []detail {
	if rw.it.Mode != config.ModeMeasured || rw.it.Source != config.SourceLinux || rw.state == statePending {
		return nil
	}
	b := m.b
	if rw.state == stateOffline {
		why := "no recent sample"
		if rw.src != nil && rw.src.Detail != "" {
			why = rw.src.Detail
		}
		via := ""
		if rw.src != nil && rw.src.Via != "" {
			via = rw.src.Via + ": "
		}
		return []detail{{tree: "└ ", note: via + why + " · retrying every " + itoa(int(RefreshInterval.Seconds())) + " s"}}
	}
	if rw.state != stateOn {
		return nil
	}
	rest, psu := rw.it.RestW, rw.it.PSUEfficiency
	if rest == 0 {
		rest = config.DefaultRestW
	}
	if psu == 0 {
		psu = config.DefaultPSUEfficiency
	}
	var out []detail
	if rw.src != nil && len(rw.src.Parts) > 0 {
		for _, p := range rw.src.Parts {
			d := detail{tree: "├ ", name: p.Name, kind: "measured", reads: p.Reads, watts: b.nf.watts(p.Watts), wst: sTx}
			if b.hasTrend {
				d.trend = b.trend.Items[PartKey(rw.it.Name, p.Name)]
			}
			out = append(out, d)
		}
	} else {
		cg := math.Max(0, rw.watts*psu-rest)
		out = append(out, detail{tree: "├ ", name: "CPU + GPU", kind: "measured", reads: "RAPL + GPU hwmon", watts: b.nf.watts(cg), wst: sTx})
	}
	loss := rw.watts * (1 - psu)
	out = append(out, detail{tree: "└ ", name: "rest", kind: "estimate",
		reads: "fixed " + b.nf.typical(rest) + ", PSU at " + pct(psu, 1) + " adds " + b.nf.watts(loss),
		watts: "~" + b.nf.watts(rest), wst: sT2.italic()})
	return out
}

// window returns the visible slice of lines for a table of n rows, keeping
// the selected item and its details in view.
func (m *Model) window(lines []line, n int) []line {
	if len(lines) <= n {
		return lines
	}
	first, last := 0, 0
	for i, l := range lines {
		if l.item == m.sel {
			if l.detail == 0 {
				first = i
			}
			last = i
		}
	}
	off := 0
	if last >= n {
		off = last - n + 1
	}
	if first < off {
		off = first
	}
	return lines[off:min(len(lines), off+n)]
}

func (m *Model) itemsFull(s *screen, y0, h int) {
	b := m.b
	W := s.w
	s.box(0, y0, W, h, "Items", itoa(len(b.rows)))
	shareR := W - 3
	costR := shareR - 9
	kwhR := costR - 10
	sparkX := kwhR - 39
	nowR := sparkX - 3
	srcX := 33
	srcW := nowR - 8 - srcX + 1
	y := y0 + 1
	s.put(6, y, "Item", sMu)
	s.put(23, y, "Mode", sMu)
	s.put(srcX, y, "Source or rule", sMu)
	s.rput(nowR, y, "Now", sMu)
	s.put(sparkX, y, "Last 3 h", sMu)
	s.rput(kwhR, y, "Today kWh", sMu)
	s.rput(costR, y, "Cost", sMu)
	s.rput(shareR, y, "Share", sMu)
	if len(b.rows) == 0 {
		s.put(6, y+2, "No items yet. Press a to add one.", sMu)
		return
	}
	for _, l := range m.window(m.itemLines(), h-3) {
		y++
		rw := b.rows[l.item]
		if l.detail > 0 {
			d := m.details(rw)[l.detail-1]
			if d.note != "" {
				s.seqMax(6, y, W-9, []seg{{d.tree, sBd}, {d.note, sMu}})
				continue
			}
			s.seq(6, y, []seg{{d.tree, sBd}, {trunc(d.name, 14), sT2}})
			kst := sMu.italic()
			if d.kind == "measured" {
				kst = st{fg: ms}
			}
			s.put(23, y, d.kind, kst)
			s.put(srcX, y, trunc(d.reads, srcW), sMu)
			s.rput(nowR, y, d.watts, d.wst)
			if d.trend != nil {
				top := 0.0
				for _, o := range m.details(rw) {
					top = math.Max(top, peak(o.trend))
				}
				s.put(sparkX, y, spark(buckets(d.trend, 30), niceMax(top*1.15)), st{fg: ms})
			}
			continue
		}
		if l.item == m.sel {
			s.tint(1, y, W-2, 1, b2)
			s.put(2, y, "▸", sAc.bold())
		}
		g, gs := glyph(rw)
		s.put(4, y, g, gs)
		s.put(6, y, trunc(rw.it.Name, 16), sTx.bold())
		s.put(23, y, string(rw.it.Mode), st{fg: modeRole(rw.it.Mode)})
		rst := sMu
		if rw.state == stateOffline || rw.state == stateUnknown {
			rst = sWa
		}
		s.put(srcX, y, trunc(b.rule(rw), srcW), rst)
		wt, ws := b.wattsText(rw)
		s.rput(nowR, y, wt, ws)
		m.itemSpark(s, sparkX, y, 30, rw)
		s.rput(kwhR, y, b.nf.kwh2(rw.todayWh), sTx)
		s.rput(costR, y, b.nf.money(rw.todayCost), sTx)
		s.rput(shareR, y, pct(rw.todayWh, b.r.Today.Wh), sMu)
	}
}

// itemSpark is an item's 3-hour sparkline, "off", or the known part
// followed by "offline".
func (m *Model) itemSpark(s *screen, x, y, w int, rw row) {
	switch {
	case rw.state == stateOff:
		s.put(x, y, "off", sMu)
		return
	case rw.trend == nil:
		return
	}
	vals := buckets(rw.trend, w)
	top := niceMax(peak(rw.trend) * 1.15)
	sty := st{fg: modeRole(rw.it.Mode)}
	if !rw.est {
		sty = sty.bold()
	}
	if rw.state == stateOffline {
		sty = st{fg: ms}
	}
	s.put(x, y, spark(vals, top), sty)
	if rw.state == stateOffline {
		lastKnown := -1
		for i, v := range vals {
			if !math.IsNaN(v) {
				lastKnown = i
			}
		}
		if at := x + lastKnown + 2; at+7 <= x+w {
			s.put(at, y, "offline", sEr.italic())
		}
	}
}

func (m *Model) periodPanel(s *screen, x0, y0, w int) {
	b := m.b
	right := ""
	if m.loaded {
		right = b.r.Period.Start.Format("Jan 2") + " → today"
	}
	s.box(x0, y0, w, 13, "Period by item", right)
	if !m.loaded {
		return
	}
	shareR := x0 + w - 4
	costR := shareR - 7
	kwhR := costR - 9
	barX := x0 + 15
	barW := kwhR - 6 - barX + 1
	s.put(x0+2, y0+1, "Item", sMu)
	s.rput(kwhR, y0+1, "kWh", sMu)
	s.rput(costR, y0+1, "Cost", sMu)
	s.rput(shareR, y0+1, "Share", sMu)
	rows := append([]row(nil), b.rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].billWh > rows[j].billWh })
	top := 0.0
	if len(rows) > 0 {
		top = rows[0].billWh
	}
	shown := rows
	if len(rows) > 5 {
		shown = rows[:4]
	}
	for i, rw := range shown {
		y := y0 + 2 + i
		s.put(x0+2, y, trunc(rw.it.Name, 12), sTx)
		ch := "█"
		if rw.est {
			ch = "▓"
		}
		if rw.billWh > 0 && top > 0 {
			s.put(barX, y, strings.Repeat(ch, max(1, int(math.Round(rw.billWh/top*float64(barW))))), st{fg: modeRole(rw.it.Mode)})
		} else {
			s.put(barX, y, "·", sMu)
		}
		s.rput(kwhR, y, b.nf.kwh1(rw.billWh), sTx)
		s.rput(costR, y, b.nf.money(rw.billCost), sTx)
		s.rput(shareR, y, pct(rw.billWh, b.r.Bill.Wh), sMu)
	}
	if len(rows) > 5 {
		s.put(x0+2, y0+6, "+ "+itoa(len(rows)-4)+" more · b shows the whole bill", sMu)
	}
	s.seq(x0+2, y0+8, []seg{{"Measured ", sMu}, {b.nf.kwh1(b.billMeasWh) + " kWh", sTx.bold()}, {"   Estimated ", sMu}, {"~" + b.nf.kwh1(b.billEstWh) + " kWh", sT2.italic()}})
	s.put(x0+2, y0+10, trunc("Estimated = watts × time on by its rule. Editing a rule", w-4), sMu)
	s.put(x0+2, y0+11, "recomputes past days too.", sMu)
}

func (m *Model) sourcesPanel(s *screen, x0, y0, w int) {
	b := m.b
	s.box(x0, y0, w, 13, "Sources", "")
	R := x0 + w - 3
	s.put(x0+2, y0+1, "Machine", sMu)
	s.put(x0+11, y0+1, "Status", sMu)
	s.put(x0+23, y0+1, "Reads", sMu)
	s.rput(R, y0+1, "Last sample", sMu)
	for i, src := range b.sources {
		if i > 2 {
			break
		}
		y := y0 + 2 + i
		s.put(x0+2, y, trunc(src.Name, 8), sTx)
		last, lst := "never", sWa
		if !src.LastTS.IsZero() {
			last = src.LastTS.Format("15:04:05")
			lst = sT2
		}
		var reads string
		if src.Online {
			s.put(x0+11, y, "● online", st{fg: ms})
			reads = src.Reads
			if src.Via != "" && src.Via != "this machine" {
				reads = src.Via + ", " + src.Reads
			}
		} else {
			s.put(x0+11, y, "✗ offline", sEr)
			lst = sWa
			reads = src.Detail
			if reads == "" {
				reads = "no recent sample"
			}
			if !src.LastTS.IsZero() {
				reads += ", totals to " + clock(src.LastTS)
			}
		}
		s.put(x0+23, y, trunc(reads, R-textWidth(last)-2-(x0+23)), sMu)
		s.rput(R, y, last, lst)
	}
	if len(b.sources) == 0 {
		s.put(x0+2, y0+2, "no measured items", sMu)
	}
	iv := m.cfg.SampleIntervalSeconds
	if iv <= 0 {
		iv = config.DefaultInterval
	}
	s.put(x0+2, y0+5, trunc("watts daemon samples every "+itoa(iv)+" s on each machine.", w-4), sMu)
	s.put(x0+2, y0+6, trunc("A Mac on battery records 0 W; that energy counts", w-4), sMu)
	s.put(x0+2, y0+7, "when it charges.", sMu)
	legend(s, x0+2, y0+10, w-4, m.b.nf)
}

// legend explains the styles. The artboard used sample numbers here; plain
// labels keep them from being read as live values.
func legend(s *screen, x, y, w int, nf numFmt) {
	s.seqMax(x, y, w, []seg{
		{"W", sTx.bold()}, {" measured   ", sMu},
		{"~W", sT2.italic()}, {" estimated   ", sMu},
		{"●", st{fg: ms}}, {" on  ", sMu}, {"○", sMu}, {" off  ", sMu}, {"✗", sEr}, {" offline", sMu},
	})
}
