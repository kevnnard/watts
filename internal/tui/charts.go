package tui

import (
	"math"
	"sort"
)

var blocks = []rune(" ▁▂▃▄▅▆▇█")

// level maps v onto 0..steps; any positive value shows at least 1.
func level(v, max float64, steps int) int {
	if math.IsNaN(v) || v <= 0 || max <= 0 {
		return 0
	}
	l := int(math.Round(v / max * float64(steps)))
	return min(steps, max1(l))
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// buckets averages the known values of vals into n buckets. A bucket with
// no known value is NaN.
func buckets(vals []float64, n int) []float64 {
	out := make([]float64, n)
	if len(vals) == 0 || n <= 0 {
		for i := range out {
			out[i] = math.NaN()
		}
		return out
	}
	span := float64(len(vals)) / float64(n)
	for i := range out {
		a := int(math.Floor(float64(i) * span))
		b := int(math.Floor(float64(i+1)*span)) - 1
		if b < a {
			b = a
		}
		sum, cnt := 0.0, 0
		for t := a; t <= b && t < len(vals); t++ {
			if !math.IsNaN(vals[t]) {
				sum += vals[t]
				cnt++
			}
		}
		if cnt == 0 {
			out[i] = math.NaN()
		} else {
			out[i] = sum / float64(cnt)
		}
	}
	return out
}

// anyTrue is buckets for flags: a bucket is set when any of its minutes is.
func anyTrue(flags []bool, n int) []bool {
	out := make([]bool, n)
	if len(flags) == 0 || n <= 0 {
		return out
	}
	span := float64(len(flags)) / float64(n)
	for i := range out {
		a := int(math.Floor(float64(i) * span))
		b := max(a, int(math.Floor(float64(i+1)*span))-1)
		for t := a; t <= b && t < len(flags); t++ {
			if flags[t] {
				out[i] = true
			}
		}
	}
	return out
}

// spark is a one-row sparkline.
func spark(vals []float64, max float64) string {
	out := make([]rune, len(vals))
	for i, v := range vals {
		out[i] = blocks[level(v, max, 8)]
	}
	return string(out)
}

// vbars draws vertical bars h rows tall with the bottom row at y+h−1.
func (s *screen) vbars(x, y, h int, vals []float64, max float64, sty func(i int) st) {
	for i, v := range vals {
		lv := level(v, max, h*8)
		for r := 0; r < h; r++ {
			if c := min(8, lv-r*8); c > 0 {
				s.put(x+i, y+h-1-r, string(blocks[c]), sty(i))
			}
		}
	}
}

// peak is the largest known value, or 0.
func peak(vals []float64) float64 {
	p := 0.0
	for _, v := range vals {
		if !math.IsNaN(v) && v > p {
			p = v
		}
	}
	return p
}

// split divides total cells by weights with the largest remainder method.
func split(total int, weights []float64) []int {
	sum := 0.0
	for _, w := range weights {
		sum += w
	}
	out := make([]int, len(weights))
	if sum <= 0 || total <= 0 {
		return out
	}
	type rem struct {
		r float64
		i int
	}
	rs := make([]rem, len(weights))
	left := total
	for i, w := range weights {
		raw := w / sum * float64(total)
		out[i] = int(math.Floor(raw))
		left -= out[i]
		rs[i] = rem{raw - math.Floor(raw), i}
	}
	sort.SliceStable(rs, func(a, b int) bool { return rs[a].r > rs[b].r })
	for _, r := range rs {
		if left <= 0 {
			break
		}
		out[r.i]++
		left--
	}
	return out
}
