package energy

import (
	"sort"
	"time"
)

// span is a half-open time interval [a, b).
type span struct{ a, b time.Time }

func (s span) dur() time.Duration {
	if !s.b.After(s.a) {
		return 0
	}
	return s.b.Sub(s.a)
}

func intersect(x, y span) span {
	a, b := x.a, x.b
	if y.a.After(a) {
		a = y.a
	}
	if y.b.Before(b) {
		b = y.b
	}
	if !b.After(a) {
		return span{a, a}
	}
	return span{a, b}
}

// merge sorts spans and joins overlapping or touching ones.
func merge(ss []span) []span {
	var in []span
	for _, s := range ss {
		if s.dur() > 0 {
			in = append(in, s)
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].a.Before(in[j].a) })
	var out []span
	for _, s := range in {
		if n := len(out); n > 0 && !s.a.After(out[n-1].b) {
			if s.b.After(out[n-1].b) {
				out[n-1].b = s.b
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// day is one local calendar day of a window: full is the whole day, part is
// the piece inside the window.
type day struct {
	key  string // YYYY-MM-DD
	full span
	part span
}

// splitDays lists the local days touching w, in order.
func splitDays(w span, loc *time.Location) []day {
	var out []day
	if w.dur() <= 0 {
		return out
	}
	t := w.a.In(loc)
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	for d.Before(w.b) {
		next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc)
		full := span{d, next}
		out = append(out, day{key: d.Format("2006-01-02"), full: full, part: intersect(full, w)})
		d = next
	}
	return out
}

// dayIndex returns the index of the day containing t, or len(days).
func dayIndex(days []day, t time.Time) int {
	return sort.Search(len(days), func(i int) bool { return days[i].full.b.After(t) })
}

// eachDay calls fn with every non-empty piece of s inside a day of days.
func eachDay(days []day, s span, fn func(i int, piece span)) {
	for i := dayIndex(days, s.a); i < len(days) && days[i].part.a.Before(s.b); i++ {
		if p := intersect(s, days[i].part); p.dur() > 0 {
			fn(i, p)
		}
	}
}
