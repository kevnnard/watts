// Package tariff keeps the user's hand-entered tariff history (S4) and
// computes billing periods from the cut-off day.
//
// The history is data, not preferences, so it lives in the data dir:
// $XDG_DATA_HOME/watts/tariffs.toml (config.TariffPath). Each entry is the
// price in force from its start date until the next entry's start date, so
// past periods keep their own price.
package tariff

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kevnnard/watts/internal/config"
)

// DateLayout is the format of Tariff.Start.
const DateLayout = "2006-01-02"

// Tariff is one bill's price.
type Tariff struct {
	Provider    string  `toml:"provider"`
	PricePerKWh float64 `toml:"price_per_kwh"` // main currency per kWh
	Start       string  `toml:"start"`         // YYYY-MM-DD, local date the price takes effect
	Note        string  `toml:"note,omitempty"`
}

// StartIn returns Start as local midnight in loc.
func (t Tariff) StartIn(loc *time.Location) time.Time {
	d, _ := time.ParseInLocation(DateLayout, t.Start, loc)
	return d
}

// Validate checks one tariff.
func (t Tariff) Validate() error {
	if t.Provider == "" {
		return errors.New("tariff: provider is required")
	}
	if _, err := time.Parse(DateLayout, t.Start); err != nil {
		return fmt.Errorf("tariff %s: start %q must be a date like 2026-11-01", t.Provider, t.Start)
	}
	if t.PricePerKWh <= 0 {
		return fmt.Errorf("tariff %s %s: price_per_kwh must be greater than 0", t.Provider, t.Start)
	}
	return nil
}

// History is the tariff list, sorted by Start, plus the billing cut-off day.
type History struct {
	// CutoffDay is the day of month a billing period starts (1–31; months
	// shorter than the day use their last day).
	CutoffDay int      `toml:"cutoff_day"`
	Tariffs   []Tariff `toml:"tariffs"`
}

// Seed is the first-run history: Enel Bogotá estrato 2, above-subsistence
// price, billing day 1.
func Seed() History {
	return History{
		CutoffDay: 1,
		Tariffs: []Tariff{{
			Provider:    "Enel",
			PricePerKWh: 990.53,
			Start:       "2026-11-01",
			Note:        "Bogotá estrato 2, October 2026 bill, above the 130 kWh subsistence block (514.00 within it)",
		}},
	}
}

// Validate checks the cut-off day and every tariff.
func (h History) Validate() error {
	if h.CutoffDay < 1 || h.CutoffDay > 31 {
		return fmt.Errorf("tariffs: cutoff_day must be between 1 and 31, got %d", h.CutoffDay)
	}
	for _, t := range h.Tariffs {
		if err := t.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Add inserts t, replacing an entry with the same start date, keeping the
// history sorted.
func (h *History) Add(t Tariff) error {
	if err := t.Validate(); err != nil {
		return err
	}
	for i := range h.Tariffs {
		if h.Tariffs[i].Start == t.Start {
			h.Tariffs[i] = t
			return nil
		}
	}
	h.Tariffs = append(h.Tariffs, t)
	h.sort()
	return nil
}

// Remove deletes the entry starting on start (YYYY-MM-DD) and reports
// whether one existed.
func (h *History) Remove(start string) bool {
	for i := range h.Tariffs {
		if h.Tariffs[i].Start == start {
			h.Tariffs = append(h.Tariffs[:i], h.Tariffs[i+1:]...)
			return true
		}
	}
	return false
}

func (h *History) sort() {
	// YYYY-MM-DD sorts lexically.
	sort.SliceStable(h.Tariffs, func(a, b int) bool { return h.Tariffs[a].Start < h.Tariffs[b].Start })
}

// At returns the tariff in force at t (the latest one whose start is not
// after t). ok is false when t precedes every tariff.
func (h History) At(t time.Time) (Tariff, bool) {
	var cur Tariff
	ok := false
	for _, tr := range h.Tariffs {
		if tr.StartIn(t.Location()).After(t) {
			break
		}
		cur, ok = tr, true
	}
	return cur, ok
}

// ForPeriod returns the tariff that prices a billing period: the one in force
// at the period start, else the first one starting inside the period.
func (h History) ForPeriod(p Period) (Tariff, bool) {
	if t, ok := h.At(p.Start); ok {
		return t, true
	}
	for _, tr := range h.Tariffs {
		if p.Contains(tr.StartIn(p.Start.Location())) {
			return tr, true
		}
	}
	return Tariff{}, false
}

// Period returns the billing period containing t.
func (h History) Period(t time.Time) Period { return PeriodFor(t, h.CutoffDay) }

// Period is a billing period [Start, End) at local midnights.
type Period struct {
	Start, End time.Time
}

// Contains reports whether t is in [Start, End).
func (p Period) Contains(t time.Time) bool { return !t.Before(p.Start) && t.Before(p.End) }

// Days is the number of calendar days in the period.
func (p Period) Days() int {
	n := 0
	for d := p.Start; d.Before(p.End); d = d.AddDate(0, 0, 1) {
		n++
	}
	return n
}

// Duration is End − Start.
func (p Period) Duration() time.Duration { return p.End.Sub(p.Start) }

// PeriodFor returns the billing period containing t for a cut-off day,
// in t's location. Cut-off days past a month's end use its last day;
// values below 1 mean 1.
func PeriodFor(t time.Time, cutoffDay int) Period {
	if cutoffDay < 1 {
		cutoffDay = 1
	}
	loc := t.Location()
	start := cutDate(t.Year(), t.Month(), cutoffDay, loc)
	if t.Before(start) {
		start = cutDate(t.Year(), t.Month()-1, cutoffDay, loc)
	}
	return Period{Start: start, End: cutDate(start.Year(), start.Month()+1, cutoffDay, loc)}
}

func cutDate(y int, m time.Month, day int, loc *time.Location) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, loc) // normalizes month overflow
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, loc)
}

// Load reads and validates the history at path.
func Load(path string) (History, error) {
	var h History
	md, err := toml.DecodeFile(path, &h)
	if err != nil {
		return History{}, fmt.Errorf("tariffs %s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return History{}, fmt.Errorf("tariffs %s: unknown key %s", path, und[0].String())
	}
	if h.CutoffDay == 0 {
		h.CutoffDay = 1
	}
	if err := h.Validate(); err != nil {
		return History{}, fmt.Errorf("tariffs %s: %w", path, err)
	}
	h.sort()
	return h, nil
}

// LoadOrCreate loads path, writing Seed() there first if it does not exist.
func LoadOrCreate(path string) (h History, created bool, err error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := Save(path, Seed()); err != nil {
			return History{}, false, err
		}
		created = true
	} else if err != nil {
		return History{}, false, fmt.Errorf("tariffs %s: %w", path, err)
	}
	h, err = Load(path)
	return h, created, err
}

// Save validates h and writes it to path atomically.
func Save(path string, h History) error {
	if err := h.Validate(); err != nil {
		return fmt.Errorf("tariffs %s: %w", path, err)
	}
	h.sort()
	var buf bytes.Buffer
	buf.WriteString("# watts tariff history. Each entry applies from start until the next entry.\n\n")
	if err := toml.NewEncoder(&buf).Encode(h); err != nil {
		return fmt.Errorf("tariffs %s: %w", path, err)
	}
	return config.WriteFileAtomic(path, buf.Bytes())
}
