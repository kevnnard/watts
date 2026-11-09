package tui

import (
	"math"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/energy"
)

// billModal is the bill view of 02-bill-first: the bill so far in large
// digits next to the projection, the billing period bar over per-day bars,
// and each item's share of the period.
type billModal struct{}

func (*billModal) setErr(string) {}

func (*billModal) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "b", "q", "enter":
		m.modal = nil
	}
	return nil
}

func (*billModal) keys(*Model) []key { return []key{{"esc", "close"}, {"b", "back to the cockpit"}} }

var bigDigits = map[rune][3]string{
	'0': {"█▀█", "█ █", "▀▀▀"}, '1': {"▀█ ", " █ ", "▀▀▀"}, '2': {"▀▀█", "█▀▀", "▀▀▀"}, '3': {"▀▀█", " ▀█", "▀▀▀"},
	'4': {"█ █", "▀▀█", "  ▀"}, '5': {"█▀▀", "▀▀█", "▀▀▀"}, '6': {"█▀▀", "█▀█", "▀▀▀"}, '7': {"▀▀█", "  █", "  ▀"},
	'8': {"█▀█", "█▀█", "▀▀▀"}, '9': {"█▀█", "▀▀█", "▀▀▀"}, '.': {" ", " ", "▀"}, ',': {" ", " ", "▄"},
}

// bigWidth is the width bigNum draws for text.
func bigWidth(text string) int {
	pre, digits := splitPrefix(text)
	w := textWidth(pre) + 1
	for _, r := range digits {
		g, ok := bigDigits[r]
		if !ok {
			w += 2
			continue
		}
		w += textWidth(g[0]) + 1
	}
	return w - 1
}

func splitPrefix(text string) (string, string) {
	i := strings.IndexFunc(text, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return text, ""
	}
	return text[:i], text[i:]
}

// bigNum draws an amount three rows tall: the currency prefix as text, the
// digits in block glyphs. It returns the column after the number.
func (s *screen) bigNum(x, y int, text string, sty st, bg ...role) int {
	pre, digits := splitPrefix(text)
	x = s.put(x, y, pre, sty, bg...) + 1
	for _, r := range digits {
		g, ok := bigDigits[r]
		if !ok {
			s.put(x, y+2, string(r), sty, bg...)
			x += 2
			continue
		}
		for i, row := range g {
			s.put(x, y+i, row, sty, bg...)
		}
		x += textWidth(g[0]) + 1
	}
	return x - 1
}

func (*billModal) draw(m *Model, s *screen) {
	b := m.b
	// the bill takes the whole screen between the header and the key bar
	X, Y, W, H := 0, 1, s.w, s.h-2
	right := ""
	if m.loaded && b.days > 0 {
		right = "day " + itoa(b.dayIdx) + " of " + itoa(b.days) + " · " + daysLeft(b)
	}
	frame(s, X, Y, W, H, "Bill", right)
	L, iw := X+3, W-6
	R := L + iw - 1
	if !m.loaded {
		s.put(L, Y+2, "reading the store…", sMu, b3)
		return
	}
	nf := b.nf
	p := b.r.Period
	end := p.End.Format("Jan 2")
	s.seqMax(L, Y+1, iw, []seg{{"Bill so far", sT2}, {"   " + p.Start.Format("Jan 2") + " → " + end, sMu}}, b3)
	so := nf.money(b.r.Bill.Cost)
	bx := s.bigNum(L, Y+2, so, sTx.bold(), b3)
	tx := max(L+26, bx+4)
	sideX := R - 39
	wide := sideX-tx >= 44
	tw := R - tx + 1
	if wide {
		tw = sideX - 3 - tx
	}
	s.seqMax(tx, Y+2, tw, []seg{{energy.ProjectionLabel + " ", sMu}, {nf.money(b.r.Projected.Cost), sTx.bold()}, {" by " + end, sMu}}, b3)
	sec, psec := nf.second(b.r.Bill.Cost), nf.second(b.r.Projected.Cost)
	if sec != "" {
		s.put(tx, Y+3, trunc(sec+" so far · "+strings.TrimPrefix(psec, "≈ ")+" "+energy.ProjectionLabel, tw), sMu, b3)
	}
	avg := "first day of the period"
	if b.fullDays > 0 {
		avg = "avg " + nf.kwh2(b.fullAvgWh) + " kWh per full day"
	}
	s.seqMax(tx, Y+4, tw, []seg{{nf.kwh1(b.r.Bill.Wh) + " kWh", sT2}, {" · " + avg, sMu}}, b3)
	if b.billLow() {
		var parts []string
		if len(b.offline) > 0 {
			msg := b.offlineNames() + " offline"
			if !b.offSince.IsZero() {
				msg += " since " + clock(b.offSince)
			}
			parts = append(parts, msg)
		}
		if u := unknownTime(b.r.Bill, " state unknown for "); u != "" {
			parts = append(parts, u)
		}
		low := strings.Join(parts, ", ") + ", so these totals may be low"
		if textWidth(low) > R-tx+1 {
			low = "Totals may be low: " + b.billLowNote()
		}
		s.put(tx, Y+5, trunc(low, R-tx+1), sWa, b3)
	}
	if wide {
		s.put(sideX, Y+2, "Now", sMu, b3)
		s.rput(R, Y+2, nf.watts(b.r.TotalWatts), sTx.bold(), b3)
		s.put(sideX, Y+3, "Today", sMu, b3)
		s.rput(R, Y+3, nf.kwh2(b.r.Today.Wh)+" kWh · "+nf.money(b.r.Today.Cost), sTx, b3)
		t := nf.second(b.r.Today.Cost)
		room := R - sideX - textWidth(t) - 1
		if b.partial() {
			s.put(sideX, Y+4, trunc("partial, "+b.nowNote(), room), sWa, b3)
		} else {
			s.put(sideX, Y+4, trunc(itoa(b.onCount)+" of "+itoa(len(b.rows))+" items on", room), sMu, b3)
		}
		if t != "" {
			s.rput(R, Y+4, t, sMu, b3)
		}
	}

	// period bar over per-day bars
	days := max(1, b.days)
	col := max(1, min(4, (iw-9)/days))
	bw := col * days
	elapsed := b.r.At.Sub(p.Start).Hours() / 24
	today := min(bw, int(math.Round(elapsed*float64(col))))
	s.put(L, Y+7, p.Start.Format("Jan 2"), sMu, b3)
	s.rput(L+bw-1, Y+7, end, sMu, b3)
	mark := "▼ today, day " + itoa(b.dayIdx)
	if mx := max(L+6, L+today-1); mx+textWidth(mark) < L+bw-textWidth(end)-1 {
		s.put(mx, Y+7, mark, sAc, b3)
	}
	s.put(L, Y+8, strings.Repeat("━", today), sAc, b3)
	s.put(L+today, Y+8, strings.Repeat("─", bw-today), sBd, b3)
	if dl := daysLeft(b); L+bw+2+textWidth(dl) <= R {
		s.put(L+bw+2, Y+8, dl, sT2, b3)
	}

	n := len(b.rows)
	bh := max(2, min(12, H-2-12-(n+2)-3))
	top := Y + 9
	vals := make([]float64, len(b.r.Days))
	for i, d := range b.r.Days {
		vals[i] = d.Wh / 1000
	}
	scale := niceMax(peak(vals))
	bar := max(1, col-1)
	cur := len(vals) - 1
	for d, v := range vals {
		sty := sT2
		if d == cur {
			sty = sAc
		}
		lv := level(v, scale, bh*8)
		for r := 0; r < bh; r++ {
			if c := min(8, lv-r*8); c > 0 {
				s.put(L+d*col, top+bh-1-r, strings.Repeat(string(blocks[c]), bar), sty, b3)
			}
		}
		if col >= 4 {
			lst := sMu
			if d == cur {
				lst = sAc
			}
			s.put(L+d*col, top+bh, nf.fixed(v, 1), lst, b3)
		}
	}
	for d := len(vals); d < days; d++ {
		s.put(L+d*col+(bar-1)/2, top+bh-1, "·", sMu, b3)
	}
	lx := L + bw + 2
	s.put(lx, top, trunc(nf.short(scale)+" kWh", R-lx+1), sMu, b3)
	s.put(lx, top+bh-1, "0", sMu, b3)
	s.put(lx, top+bh, trunc("per day", R-lx+1), sMu, b3)

	// per item share of the period
	y := top + bh + 1
	if H-2-(y-Y) >= n+3 {
		y++
	}
	shareR := R
	costR := shareR - 12
	kwhR := costR - 12
	barX := L + 30
	barW := kwhR - 10 - barX
	s.put(L, y, "Items", sTx.bold(), b3)
	if barW >= 8 {
		head := "Share of the period"
		if barW < 20 {
			head = "Share"
		}
		s.put(barX, y, head, sMu, b3)
	}
	s.rput(kwhR, y, "kWh", sMu, b3)
	s.rput(costR, y, "Cost", sMu, b3)
	s.rput(shareR, y, "Of bill", sMu, b3)
	s.hrule(L, y+1, iw, b3)
	rows := append([]row(nil), b.rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].billWh > rows[j].billWh })
	room := Y + H - 1 - (y + 2)
	shown := rows
	if len(rows) > room {
		shown = rows[:max(0, room-1)]
	}
	for i, rw := range shown {
		ry := y + 2 + i
		g, gs := glyph(rw)
		s.put(L, ry, g, gs, b3)
		s.put(L+2, ry, trunc(rw.it.Name, 16), sTx.bold(), b3)
		s.put(L+20, ry, string(rw.it.Mode), st{fg: modeRole(rw.it.Mode)}, b3)
		if barW >= 8 {
			ch := "━"
			if rw.est {
				ch = "┅"
			}
			if rw.billWh > 0 && b.r.Bill.Wh > 0 {
				s.put(barX, ry, strings.Repeat(ch, max(1, int(math.Round(rw.billWh/b.r.Bill.Wh*float64(barW))))), st{fg: modeRole(rw.it.Mode)}, b3)
			} else {
				s.put(barX, ry, "·", sMu, b3)
			}
		}
		kst := sTx
		if rw.est {
			kst = sT2.italic()
		}
		s.rput(kwhR, ry, nf.kwh1(rw.billWh), kst, b3)
		s.rput(costR, ry, nf.money(rw.billCost), sTx, b3)
		s.rput(shareR, ry, pct(rw.billWh, b.r.Bill.Wh), sMu, b3)
	}
	if len(shown) < len(rows) {
		s.put(L+2, y+2+len(shown), "+ "+itoa(len(rows)-len(shown))+" more", sMu, b3)
	}
	if ly := y + 2 + len(rows) + 1; ly <= Y+H-2 {
		s.seqMax(L, ly, iw, []seg{
			{"━", st{fg: ms}}, {" measured   ", sMu}, {"┅", st{fg: fl}}, {" ~ estimated   ", sMu},
			{"Measured ", sMu}, {nf.kwh1(b.billMeasWh) + " kWh", sTx.bold()}, {"   Estimated ", sMu}, {"~" + nf.kwh1(b.billEstWh) + " kWh", sT2.italic()},
		}, b3)
	}
}
