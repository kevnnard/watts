package energy

import (
	"sort"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
)

// Network items (S20). A network reading of the item covers [TS, TS + d),
// where d is the time to the item's next reading when that is at most
// GapIntervals × interval, else one interval (the sample gap rule, looking
// forward, as for DDC readings). In covered time the item draws, × quantity,
//
//	on:      watts
//	standby: standby_w
//
// An unknown reading, any other state, and time no reading covers count no
// energy and are unknown time (Result.UnknownSeconds): never off, never on.
// The estimate is a state-based estimate, not a measurement.

// NetworkState is a network item's newest fresh reading, for ItemNow.
type NetworkState struct {
	State    string // store.NetworkOn, NetworkStandby or NetworkUnknown
	TS       time.Time
	Host     string // the address the daemon asked
	Verified bool   // the device reported the item's id
}

// netSpan is the time one reading covers and the item's watts in it.
type netSpan struct {
	span
	watts float64 // whole item (× quantity)
	on    bool
}

func knownNetwork(s string) bool { return s == store.NetworkOn || s == store.NetworkStandby }

// networkWatts is the item's draw in a known state, × quantity.
func networkWatts(it config.Item, state string) float64 {
	q := it.Quantity
	if q < 1 {
		q = 1
	}
	if state == store.NetworkOn {
		return it.Watts * float64(q)
	}
	return it.StandbyW * float64(q)
}

// netSpans lists the time the item's known readings cover, oldest first.
// The spans do not overlap.
func netSpans(in Input, it config.Item) []netSpan {
	xs := in.Data.Network[it.Name]
	if !sort.SliceIsSorted(xs, func(i, j int) bool { return xs[i].TS.Before(xs[j].TS) }) {
		xs = append([]store.NetworkReading(nil), xs...)
		sort.SliceStable(xs, func(i, j int) bool { return xs[i].TS.Before(xs[j].TS) })
	}
	iv := in.interval()
	var out []netSpan
	for i, x := range xs {
		d := iv
		if i+1 < len(xs) {
			gap := xs[i+1].TS.Sub(x.TS)
			if gap <= 0 {
				continue // a duplicate time: the later row stands
			}
			if gap <= GapIntervals*iv {
				d = gap
			}
		}
		if !knownNetwork(x.State) {
			continue
		}
		out = append(out, netSpan{span: span{x.TS, x.TS.Add(d)}, watts: networkWatts(it, x.State), on: x.State == store.NetworkOn})
	}
	return out
}

// network integrates a network item. OnSeconds counts the time it is on,
// not in standby; UnknownSeconds the time of the window no known reading
// covers.
func network(in Input, it config.Item, days []day, out []Result) {
	known := make([]float64, len(days))
	for _, d := range netSpans(in, it) {
		eachDay(days, d.span, func(i int, p span) {
			sec := p.dur().Seconds()
			known[i] += sec
			out[i].Wh += d.watts * sec / 3600
			if d.on {
				out[i].OnSeconds += sec
			}
		})
	}
	for i := range days {
		out[i].UnknownSeconds = max(0, days[i].part.dur().Seconds()-known[i])
	}
}

// nowNetwork fills n for a network item at now. Only a fresh reading (at
// most GapIntervals × interval old) gives a current state; an unknown one
// is shown with its state but, like a stale or missing one, the item is
// offline: its watts now are not known.
func nowNetwork(in Input, it config.Item, now time.Time, n *ItemNow) {
	var best store.NetworkReading
	ok := false
	for _, x := range in.Data.Network[it.Name] {
		if x.TS.After(now) {
			continue
		}
		if !ok || !x.TS.Before(best.TS) {
			best, ok = x, true
		}
	}
	n.LastTS = best.TS
	if !ok || now.Sub(best.TS) > GapIntervals*in.interval() {
		n.Offline = true
		return
	}
	state := best.State
	if !knownNetwork(state) {
		state = store.NetworkUnknown
	}
	n.Network = &NetworkState{State: state, TS: best.TS, Host: best.Host, Verified: best.Verified}
	if state == store.NetworkUnknown {
		n.Offline = true
		return
	}
	n.On, n.Watts = true, networkWatts(it, state)
}
