package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Range is a daily time range in minutes since local midnight. EndMin may be
// 1440 ("24:00"). EndMin < StartMin means the range crosses midnight.
type Range struct {
	StartMin int
	EndMin   int
}

// CrossesMidnight reports whether the range ends on the next day.
func (r Range) CrossesMidnight() bool { return r.EndMin < r.StartMin }

// String formats the range as "HH:MM-HH:MM".
func (r Range) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", r.StartMin/60, r.StartMin%60, r.EndMin/60, r.EndMin%60)
}

// ParseRange parses "HH:MM-HH:MM". Start is 00:00–23:59, end 00:00–24:00.
func ParseRange(s string) (Range, error) {
	bad := fmt.Errorf("invalid schedule range %q (want HH:MM-HH:MM, 00:00 to 24:00)", s)
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return Range{}, bad
	}
	start, ok1 := parseClock(strings.TrimSpace(a))
	end, ok2 := parseClock(strings.TrimSpace(b))
	if !ok1 || !ok2 || start == 24*60 {
		return Range{}, bad
	}
	if start == end {
		return Range{}, fmt.Errorf("invalid schedule range %q (start and end must differ)", s)
	}
	return Range{StartMin: start, EndMin: end}, nil
}

func parseClock(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 || hh > 24 || (hh == 24 && mm != 0) {
		return 0, false
	}
	return hh*60 + mm, true
}
