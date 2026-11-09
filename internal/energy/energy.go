// Package energy integrates power into energy per item. Every function here
// is pure: callers load config, tariffs and store data and pass them in.
//
// Rules (S6, S7):
//   - measured: each sample covers [TS − seconds, TS) at its watts. A sample
//     whose seconds exceed GapIntervals × interval (sleep, suspend, daemon
//     down) counts as one interval. A daily aggregate for (day, source)
//     replaces that source's samples for the whole local day; a window that
//     covers part of that day gets the same fraction of its Wh.
//   - follows: watts × quantity while any followed measured item has samples
//     covering that time. On aggregate days the followed item's on_seconds
//     (prorated) is used; the item is on for the larger of that and the
//     sample coverage, so overlapping machines are not double counted.
//     With ddc set, the monitor's DDC readings decide the watts in the time
//     they cover, and the rule above only fills the rest (display.go).
//   - schedule: watts × quantity during the daily ranges; a range whose end
//     is before its start runs to that time on the next day.
//   - manual: watts × quantity between an "on" toggle and the next "off".
//   - network: watts × quantity while the daemon's readings of the item
//     say on, standby_w × quantity in standby; unknown and missing time
//     count nothing and are reported as unknown time (network.go).
//
// Estimated items are derived at read time, so config edits apply
// retroactively.
package energy

import (
	"sort"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
)

// GapIntervals is the S7 gap rule threshold: a sample covering more than
// this many intervals counts as one interval.
const GapIntervals = 3

// Input is everything the energy math reads.
type Input struct {
	Items    []config.Item
	Data     store.Data
	Interval time.Duration  // sample interval; 0 means 60 s
	Loc      *time.Location // local days; nil means time.Local
}

func (in Input) interval() time.Duration {
	if in.Interval <= 0 {
		return config.DefaultInterval * time.Second
	}
	return in.Interval
}

func (in Input) loc() *time.Location {
	if in.Loc == nil {
		return time.Local
	}
	return in.Loc
}

// Coverage is the time a sample stands for after the S7 gap rule.
func (in Input) Coverage(s store.Sample) time.Duration {
	iv := in.interval()
	d := time.Duration(s.Seconds * float64(time.Second))
	if d <= 0 || d > GapIntervals*iv {
		return iv
	}
	return d
}

// Margin is how far past a window end samples must be loaded so that
// samples covering the window edge are included (store.Load margin).
func (in Input) Margin() time.Duration { return GapIntervals * in.interval() }

func (in Input) item(name string) (config.Item, bool) {
	for _, it := range in.Items {
		if it.Name == name {
			return it, true
		}
	}
	return config.Item{}, false
}

// Result is an item's energy over a window.
type Result struct {
	Wh        float64
	OnSeconds float64
	// UnknownSeconds is, for a network item, the time of the window whose
	// state is not known (an unknown reading or none). Wh counts nothing
	// for it, so Wh is then a lower bound. Always 0 for other modes.
	UnknownSeconds float64
}

// ItemEnergy integrates one item over [from, to).
func ItemEnergy(in Input, it config.Item, from, to time.Time) Result {
	var r Result
	for _, d := range itemDays(in, it, from, to) {
		r.Wh += d.Wh
		r.OnSeconds += d.OnSeconds
		r.UnknownSeconds += d.UnknownSeconds
	}
	return r
}

// itemDays integrates one item over [from, to), split by local day.
func itemDays(in Input, it config.Item, from, to time.Time) []Result {
	days := splitDays(span{from, to}, in.loc())
	out := make([]Result, len(days))
	switch it.Mode {
	case config.ModeMeasured:
		measured(in, it.Source, days, out)
	case config.ModeFollows:
		follows(in, it, days, out)
	case config.ModeSchedule:
		fixed(scheduleSpans(in, it, from, to), it.TotalWatts(), days, out)
	case config.ModeManual:
		fixed(manualSpans(in.Data.Toggles[it.Name], to), it.TotalWatts(), days, out)
	case config.ModeNetwork:
		network(in, it, days, out)
	}
	return out
}

// aggregates maps day index to the source's aggregate on that day.
func aggregates(in Input, source string, days []day) map[int]store.DailyAggregate {
	idx := make(map[string]int, len(days))
	for i, d := range days {
		idx[d.key] = i
	}
	out := map[int]store.DailyAggregate{}
	for _, a := range in.Data.Daily[source] {
		if i, ok := idx[a.Day]; ok {
			out[i] = a
		}
	}
	return out
}

func fraction(d day) float64 {
	if d.full.dur() == 0 {
		return 0
	}
	return d.part.dur().Seconds() / d.full.dur().Seconds()
}

func measured(in Input, source string, days []day, out []Result) {
	aggs := aggregates(in, source, days)
	for _, s := range in.Data.Samples[source] {
		cov := span{s.TS.Add(-in.Coverage(s)), s.TS}
		eachDay(days, cov, func(i int, p span) {
			if _, ok := aggs[i]; ok {
				return
			}
			sec := p.dur().Seconds()
			out[i].Wh += s.Watts * sec / 3600
			out[i].OnSeconds += sec
		})
	}
	for i, a := range aggs {
		f := fraction(days[i])
		out[i].Wh += a.Wh * f
		out[i].OnSeconds += a.OnSeconds * f
	}
}

// sourcesOf returns the sensor sources of the measured items it follows.
func sourcesOf(in Input, it config.Item) []string {
	var srcs []string
	seen := map[string]bool{}
	for _, name := range it.Follows {
		f, ok := in.item(name)
		if !ok || f.Mode != config.ModeMeasured || seen[f.Source] {
			continue
		}
		seen[f.Source] = true
		srcs = append(srcs, f.Source)
	}
	return srcs
}

func follows(in Input, it config.Item, days []day, out []Result) {
	var cover []span
	aggOn := make([]float64, len(days))
	for _, src := range sourcesOf(in, it) {
		aggs := aggregates(in, src, days)
		for _, s := range in.Data.Samples[src] {
			eachDay(days, span{s.TS.Add(-in.Coverage(s)), s.TS}, func(i int, p span) {
				if _, ok := aggs[i]; !ok {
					cover = append(cover, p)
				}
			})
		}
		for i, a := range aggs {
			if on := a.OnSeconds * fraction(days[i]); on > aggOn[i] {
				aggOn[i] = on
			}
		}
	}
	// DDC readings cover their own time; the fixed rule gets the rest.
	ddcSec, ddcOn := make([]float64, len(days)), make([]float64, len(days))
	var ddc []span
	for _, d := range ddcSpans(in, it) {
		ddc = append(ddc, d.span)
		eachDay(days, d.span, func(i int, p span) {
			sec := p.dur().Seconds()
			ddcSec[i] += sec
			out[i].Wh += d.watts * sec / 3600
			if d.on {
				ddcOn[i] += sec
			}
		})
	}
	on := make([]float64, len(days))
	for _, s := range subtract(merge(cover), merge(ddc)) {
		eachDay(days, s, func(i int, p span) { on[i] += p.dur().Seconds() })
	}
	w := it.TotalWatts()
	for i := range days {
		sec := max(on[i], aggOn[i]-ddcSec[i])
		sec = max(0, min(sec, days[i].part.dur().Seconds()-ddcSec[i]))
		out[i].OnSeconds = sec + ddcOn[i]
		out[i].Wh += w * sec / 3600
	}
}

func fixed(spans []span, watts float64, days []day, out []Result) {
	for _, s := range merge(spans) {
		eachDay(days, s, func(i int, p span) {
			sec := p.dur().Seconds()
			out[i].OnSeconds += sec
			out[i].Wh += watts * sec / 3600
		})
	}
}

// scheduleSpans lists the item's daily ranges for every local day that can
// overlap [from, to), including the day before from for ranges crossing
// midnight.
func scheduleSpans(in Input, it config.Item, from, to time.Time) []span {
	ranges, err := it.Ranges()
	if err != nil || !to.After(from) {
		return nil
	}
	loc := in.loc()
	f := from.In(loc)
	var out []span
	for d := time.Date(f.Year(), f.Month(), f.Day()-1, 0, 0, 0, 0, loc); d.Before(to); d = time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc) {
		for _, r := range ranges {
			out = append(out, rangeSpan(d, r, loc))
		}
	}
	return out
}

// rangeSpan places r on the local day starting at midnight d. Clock times
// are wall-clock, so DST days keep "18:00" at 18:00.
func rangeSpan(d time.Time, r config.Range, loc *time.Location) span {
	at := func(dayOffset, min int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day()+dayOffset, min/60, min%60, 0, 0, loc)
	}
	if r.CrossesMidnight() {
		return span{at(0, r.StartMin), at(1, r.EndMin)}
	}
	return span{at(0, r.StartMin), at(0, r.EndMin)}
}

func sortedToggles(ts []store.Toggle) []store.Toggle {
	if sort.SliceIsSorted(ts, func(i, j int) bool { return ts[i].TS.Before(ts[j].TS) }) {
		return ts
	}
	out := append([]store.Toggle(nil), ts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

// manualSpans turns toggles (oldest first) into on intervals. An item still
// on stays on until end.
func manualSpans(toggles []store.Toggle, end time.Time) []span {
	toggles = sortedToggles(toggles)
	var out []span
	var onAt time.Time
	on := false
	for _, t := range toggles {
		switch {
		case t.On && !on:
			on, onAt = true, t.TS
		case !t.On && on:
			on = false
			out = append(out, span{onAt, t.TS})
		}
	}
	if on && end.After(onAt) {
		out = append(out, span{onAt, end})
	}
	return out
}
