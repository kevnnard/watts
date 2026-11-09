package tui

import (
	"strings"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
)

// StatusText is the one-shot summary `watts status` prints: total watts,
// each item, today, the bill so far and its projection, and the sources.
// With ansi false (not a TTY) it is plain text with no escape codes.
func StatusText(r energy.Report, sources []Source, cfg *config.Config, ansi bool) string {
	b := newBoard(cfg, r, sources, Trend{}, false)
	nf := b.nf
	var out strings.Builder
	paint := func(text string, sty st) string {
		if !ansi || text == "" {
			return text
		}
		return styleFor(styleKey{fg: sty.fg, bg: b0, at: sty.at}).UnsetBackground().Render(text)
	}
	line := func(parts ...seg) {
		for _, p := range parts {
			out.WriteString(paint(p.t, p.s))
		}
		out.WriteByte('\n')
	}
	pad := func(s string, w int) string { return padRight(s, w) }
	lpad := func(s string, w int) string {
		if d := w - textWidth(s); d > 0 {
			return strings.Repeat(" ", d) + s
		}
		return s
	}

	head := []seg{{"watts", sAc.bold()}, {"  " + r.At.Format("Mon Jan 2 15:04"), sMu}}
	if r.HasTariff {
		head = append(head, seg{"  " + r.Tariff.Provider + " · " + nf.price(r.Tariff.PricePerKWh) + " per kWh", sMu})
	} else {
		head = append(head, seg{"  no tariff for this period, costs are $0", sWa})
	}
	line(head...)
	out.WriteByte('\n')

	total := []seg{{"Now     ", sMu}, {nf.watts(r.TotalWatts), sTx.bold()}, {"  " + nf.watts(b.measuredW) + " measured + ~" + nf.watts(b.estimatedW) + " estimated", sMu}}
	if b.partial() {
		total = append(total, seg{"  partial · " + b.offlineNames() + " offline", sWa})
	}
	line(total...)

	nameW := 4
	for _, rw := range b.rows {
		nameW = max(nameW, textWidth(rw.it.Name))
	}
	for _, rw := range b.rows {
		g, gs := glyph(rw)
		wt, ws := b.wattsText(rw)
		parts := []seg{{"  ", sMu}, {g, gs}, {" " + pad(rw.it.Name, nameW) + "  ", sTx}, {pad(string(rw.it.Mode), 8), st{fg: modeRole(rw.it.Mode)}}, {"  ", sMu}, {lpad(wt, 9), ws},
			{"  " + lpad(nf.kwh2(rw.todayWh), 6) + " kWh today", sMu}}
		if note := ddcNote(rw); note != "" {
			parts = append(parts, seg{" · " + note, sMu})
		}
		line(parts...)
	}
	out.WriteByte('\n')

	today := []seg{{"Today   ", sMu}, {nf.kwh2(r.Today.Wh) + " kWh  " + nf.money(r.Today.Cost), sTx.bold()}}
	if s := nf.second(r.Today.Cost); s != "" {
		today = append(today, seg{"  " + s, sMu})
	}
	line(today...)
	bill := []seg{{"Bill    ", sMu}, {nf.money(r.Bill.Cost), sTx.bold()}, {" so far", sMu}}
	if s := nf.second(r.Bill.Cost); s != "" {
		bill = append(bill, seg{" " + s, sMu})
	}
	bill = append(bill, seg{" · " + energy.ProjectionLabel + " ", sMu}, seg{nf.money(r.Projected.Cost), sTx.bold()})
	if s := nf.second(r.Projected.Cost); s != "" {
		bill = append(bill, seg{" " + s, sMu})
	}
	line(bill...)
	if !r.Period.Start.IsZero() {
		period := []seg{{"        " + r.Period.Start.Format("Jan 2") + " → " + r.Period.End.Format("Jan 2") + " · day " + itoa(b.dayIdx) + " of " + itoa(b.days) + " · " + nf.kwh1(r.Bill.Wh) + " kWh", sMu}}
		if b.partial() {
			period = append(period, seg{" · may be low: " + b.offlineNames() + " missing", sWa})
		}
		line(period...)
	}
	if len(sources) > 0 {
		out.WriteByte('\n')
		for _, src := range sources {
			parts := []seg{{"Source  " + pad(src.Name, 6) + " ", sMu}}
			if src.Online {
				parts = append(parts, seg{"● online", st{fg: ms}})
			} else {
				parts = append(parts, seg{"✗ offline", sEr})
				if src.Detail != "" {
					parts = append(parts, seg{" · " + src.Detail, sMu})
				}
			}
			if !src.LastTS.IsZero() {
				parts = append(parts, seg{" · last sample " + src.LastTS.Format("15:04:05"), sMu})
			}
			line(parts...)
		}
	}
	return out.String()
}
