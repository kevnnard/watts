package energy

import (
	"sort"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
)

// DDC estimate for a follows item with ddc set (S19). A display reading of
// the item's ddc source covers [TS, TS + d), where d is the time to the
// source's next reading when that is at most GapIntervals × interval, else
// one interval (the sample gap rule, looking forward). In covered time the
// item draws, per unit,
//
//	on:      min_w + (max_w − min_w) × brightness / max_brightness
//	standby: standby_w
//
// and the fixed-watts follows rule only fills the time no reading covers.
// A reading with max_brightness ≤ 0 is unusable and covers nothing.

// DisplayState is the monitor as last read over DDC, for ItemNow.
type DisplayState struct {
	Source     string    // the item's ddc source
	Brightness int       // VCP 0x10 current value
	Max        int       // VCP 0x10 max value
	On         bool      // DPM on; false is standby
	TS         time.Time // when it was read
}

// ddcSpan is the time one reading covers and the item's watts in it.
type ddcSpan struct {
	span
	watts float64 // whole item (× quantity)
	on    bool
}

func usable(x store.Display) bool { return x.MaxBrightness > 0 && x.Brightness >= 0 }

// ddcWatts is the item's estimate for one reading, × quantity.
func ddcWatts(it config.Item, x store.Display) float64 {
	q := it.Quantity
	if q < 1 {
		q = 1
	}
	lo, hi, sleep := it.DDCWatts()
	if !x.On {
		return sleep * float64(q)
	}
	b := min(x.Brightness, x.MaxBrightness)
	return (lo + (hi-lo)*float64(b)/float64(x.MaxBrightness)) * float64(q)
}

// displays returns the readings of source, oldest first.
func displays(in Input, source string) []store.Display {
	var out []store.Display
	for _, x := range in.Data.Displays {
		if x.Source == source {
			out = append(out, x)
		}
	}
	if !sort.SliceIsSorted(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) }) {
		sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	}
	return out
}

// ddcSpans lists the time the item's DDC readings cover, oldest first. The
// spans do not overlap.
func ddcSpans(in Input, it config.Item) []ddcSpan {
	if it.DDC == "" {
		return nil
	}
	iv := in.interval()
	xs := displays(in, it.DDC)
	var out []ddcSpan
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
		if !usable(x) {
			continue
		}
		out = append(out, ddcSpan{span: span{x.TS, x.TS.Add(d)}, watts: ddcWatts(it, x), on: x.On})
	}
	return out
}

// latestDisplay is the newest usable reading of the item's ddc source at
// or before now that is at most GapIntervals × interval old.
func latestDisplay(in Input, it config.Item, now time.Time) (store.Display, bool) {
	var best store.Display
	ok := false
	if it.DDC == "" {
		return best, false
	}
	for _, x := range in.Data.Displays {
		if x.Source != it.DDC || x.TS.After(now) || !usable(x) {
			continue
		}
		if !ok || x.TS.After(best.TS) {
			best, ok = x, true
		}
	}
	if !ok || now.Sub(best.TS) > GapIntervals*in.interval() {
		return store.Display{}, false
	}
	return best, true
}

// subtract returns the parts of a not covered by b. Both must be merged
// (sorted, not overlapping).
func subtract(a, b []span) []span {
	var out []span
	j := 0
	for _, s := range a {
		cur := s.a
		for j < len(b) && !b[j].b.After(cur) {
			j++
		}
		for k := j; k < len(b) && b[k].a.Before(s.b); k++ {
			if b[k].a.After(cur) {
				out = append(out, span{cur, b[k].a})
			}
			if b[k].b.After(cur) {
				cur = b[k].b
			}
		}
		if s.b.After(cur) {
			out = append(out, span{cur, s.b})
		}
	}
	return out
}
