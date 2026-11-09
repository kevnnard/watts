package energy

import (
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
)

// ItemNow is an item's state at an instant.
type ItemNow struct {
	Name  string
	Mode  config.Mode
	Watts float64 // 0 when off or offline
	On    bool
	// Offline is set for a measured item whose newest sample is older than
	// GapIntervals × interval (or that has none). Show it as offline, not 0 W.
	Offline bool
	LastTS  time.Time // newest sample time for measured items, zero if none
	// Display is set for a follows item with ddc while a fresh DDC reading
	// (at most GapIntervals × interval old) drives Watts: the DDC estimate,
	// on even while the monitor sleeps at standby watts.
	Display *DisplayState
}

// Now returns the state of it at now.
func Now(in Input, it config.Item, now time.Time) ItemNow {
	n := ItemNow{Name: it.Name, Mode: it.Mode}
	switch it.Mode {
	case config.ModeMeasured:
		s, ok := latest(in, it.Source, now)
		n.LastTS = s.TS
		if !ok || in.stale(s, now) {
			n.Offline = true
			return n
		}
		n.On, n.Watts = true, s.Watts
		return n
	case config.ModeFollows:
		if x, ok := latestDisplay(in, it, now); ok {
			n.On, n.Watts = true, ddcWatts(it, x)
			n.Display = &DisplayState{Source: x.Source, Brightness: x.Brightness, Max: x.MaxBrightness, On: x.On, TS: x.TS}
			return n
		}
		for _, src := range sourcesOf(in, it) {
			if s, ok := latest(in, src, now); ok && !in.stale(s, now) {
				n.On = true
				break
			}
		}
	case config.ModeSchedule:
		for _, s := range scheduleSpans(in, it, now, now.Add(time.Second)) {
			if !now.Before(s.a) && now.Before(s.b) {
				n.On = true
				break
			}
		}
	case config.ModeManual:
		n.On = ManualOn(in.Data.Toggles[it.Name], now)
	}
	if n.On {
		n.Watts = it.TotalWatts()
	}
	return n
}

// ManualOn reports a manual item's state at t from its toggles.
func ManualOn(toggles []store.Toggle, t time.Time) bool {
	on := false
	for _, tg := range sortedToggles(toggles) {
		if tg.TS.After(t) {
			break
		}
		on = tg.On
	}
	return on
}

func latest(in Input, source string, now time.Time) (store.Sample, bool) {
	var best store.Sample
	ok := false
	for _, s := range in.Data.Samples[source] {
		if s.TS.After(now) {
			continue
		}
		if !ok || s.TS.After(best.TS) {
			best, ok = s, true
		}
	}
	return best, ok
}

func (in Input) stale(s store.Sample, now time.Time) bool {
	return now.Sub(s.TS) > GapIntervals*in.interval()
}

// ItemTotal is one item's energy and cost over a window.
type ItemTotal struct {
	Name      string
	Wh        float64
	OnSeconds float64
	Cost      float64
}

// Totals is energy and cost over [From, To).
type Totals struct {
	From, To time.Time
	Wh       float64
	Cost     float64
	Items    []ItemTotal // in config order
}

// KWh is Wh / 1000.
func (t Totals) KWh() float64 { return t.Wh / 1000 }

// Cost prices wh at pricePerKWh.
func Cost(wh, pricePerKWh float64) float64 { return wh / 1000 * pricePerKWh }

// Window integrates every item over [from, to) at one price.
func Window(in Input, from, to time.Time, pricePerKWh float64) Totals {
	t := Totals{From: from, To: to}
	for _, it := range in.Items {
		r := ItemEnergy(in, it, from, to)
		c := Cost(r.Wh, pricePerKWh)
		t.Items = append(t.Items, ItemTotal{Name: it.Name, Wh: r.Wh, OnSeconds: r.OnSeconds, Cost: c})
		t.Wh += r.Wh
		t.Cost += c
	}
	return t
}

// DayTotal is one local day's energy and cost.
type DayTotal struct {
	Day  time.Time // local midnight
	Wh   float64
	Cost float64
}

// Days integrates every item over [from, to) split by local day.
func Days(in Input, from, to time.Time, pricePerKWh float64) []DayTotal {
	days := splitDays(span{from, to}, in.loc())
	out := make([]DayTotal, len(days))
	for i, d := range days {
		out[i].Day = d.full.a
	}
	for _, it := range in.Items {
		for i, r := range itemDays(in, it, from, to) {
			out[i].Wh += r.Wh
		}
	}
	for i := range out {
		out[i].Cost = Cost(out[i].Wh, pricePerKWh)
	}
	return out
}

// ProjectionLabel is the label the UI must show next to a Projection; it is
// the only "if it keeps going" figure (S6).
const ProjectionLabel = "projected"

// Projection extends the energy so far linearly to the end of the period.
type Projection struct {
	Wh      float64
	Cost    float64
	Elapsed time.Duration // part of the period the projection is based on
}

// Project scales soFarWh over the elapsed part of p to the whole period.
// Before the period starts it is zero; after it ends it equals soFarWh.
func Project(soFarWh float64, p tariff.Period, now time.Time, pricePerKWh float64) Projection {
	elapsed := now.Sub(p.Start)
	if elapsed > p.Duration() {
		elapsed = p.Duration()
	}
	if elapsed <= 0 {
		return Projection{}
	}
	wh := soFarWh * p.Duration().Seconds() / elapsed.Seconds()
	return Projection{Wh: wh, Cost: Cost(wh, pricePerKWh), Elapsed: elapsed}
}

// Report is everything the dashboard and `watts status` show.
type Report struct {
	At         time.Time
	Items      []ItemNow // in config order
	TotalWatts float64   // sum over items that are on and not offline
	Tariff     tariff.Tariff
	HasTariff  bool // false: no tariff covers the period, costs are 0
	Period     tariff.Period
	Today      Totals // [local midnight, now)
	Bill       Totals // [period start, now)
	Projected  Projection
	Days       []DayTotal // each day of the period up to today
}

// Build computes the report at now. in.Data must cover [period start, now]
// (store.Load(period.Start, now, in.Margin())).
func Build(in Input, h tariff.History, now time.Time) Report {
	now = now.In(in.loc())
	r := Report{At: now, Period: h.Period(now)}
	r.Tariff, r.HasTariff = h.ForPeriod(r.Period)
	price := r.Tariff.PricePerKWh
	for _, it := range in.Items {
		n := Now(in, it, now)
		r.Items = append(r.Items, n)
		if n.On && !n.Offline {
			r.TotalWatts += n.Watts
		}
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	r.Today = Window(in, midnight, now, price)
	r.Bill = Window(in, r.Period.Start, now, price)
	r.Projected = Project(r.Bill.Wh, r.Period, now, price)
	r.Days = Days(in, r.Period.Start, now, price)
	return r
}
