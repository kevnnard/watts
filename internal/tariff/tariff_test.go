package tariff

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var bog = time.FixedZone("COT", -5*3600)

func d(y int, m time.Month, day, h, min int) time.Time {
	return time.Date(y, m, day, h, min, 0, 0, bog)
}

func TestPeriodFor(t *testing.T) {
	tests := []struct {
		name       string
		cutoff     int
		at         time.Time
		start, end time.Time
	}{
		{"day 1 mid month", 1, d(2026, 11, 8, 12, 0), d(2026, 11, 1, 0, 0), d(2026, 12, 1, 0, 0)},
		{"day 1 first instant", 1, d(2026, 11, 1, 0, 0), d(2026, 11, 1, 0, 0), d(2026, 12, 1, 0, 0)},
		{"day 1 last instant", 1, d(2026, 10, 31, 23, 59), d(2026, 10, 1, 0, 0), d(2026, 11, 1, 0, 0)},
		{"cutoff 15 day before", 15, d(2026, 11, 14, 23, 59), d(2026, 10, 15, 0, 0), d(2026, 11, 15, 0, 0)},
		{"cutoff 15 on the day", 15, d(2026, 11, 15, 0, 0), d(2026, 11, 15, 0, 0), d(2026, 12, 15, 0, 0)},
		{"cutoff 15 across year", 15, d(2027, 1, 3, 9, 0), d(2026, 12, 15, 0, 0), d(2027, 1, 15, 0, 0)},
		{"cutoff 31 clamps in feb", 31, d(2027, 2, 28, 10, 0), d(2027, 2, 28, 0, 0), d(2027, 3, 31, 0, 0)},
		{"cutoff 31 before feb clamp", 31, d(2027, 2, 27, 10, 0), d(2027, 1, 31, 0, 0), d(2027, 2, 28, 0, 0)},
		{"cutoff 0 means 1", 0, d(2026, 11, 8, 0, 0), d(2026, 11, 1, 0, 0), d(2026, 12, 1, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PeriodFor(tt.at, tt.cutoff)
			if !p.Start.Equal(tt.start) || !p.End.Equal(tt.end) {
				t.Fatalf("PeriodFor(%v, %d) = [%v, %v), want [%v, %v)", tt.at, tt.cutoff, p.Start, p.End, tt.start, tt.end)
			}
			if !p.Contains(tt.at) {
				t.Errorf("period does not contain %v", tt.at)
			}
			if p.Contains(p.End) {
				t.Errorf("period contains its end")
			}
		})
	}
}

func TestPeriodDays(t *testing.T) {
	p := PeriodFor(d(2026, 11, 8, 0, 0), 1)
	if p.Days() != 30 {
		t.Errorf("Days() = %d, want 30", p.Days())
	}
}

func TestLookupAcrossPriceChange(t *testing.T) {
	h := Seed()
	h.CutoffDay = 15
	if err := h.Add(Tariff{Provider: "Enel", PricePerKWh: 1012.40, Start: "2026-12-15"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		at    time.Time
		price float64
		ok    bool
	}{
		{"before any tariff", d(2026, 10, 31, 23, 59), 0, false},
		{"first day of seed", d(2026, 11, 1, 0, 0), 990.53, true},
		{"last instant before change", d(2026, 12, 14, 23, 59), 990.53, true},
		{"first instant of change", d(2026, 12, 15, 0, 0), 1012.40, true},
		{"later", d(2027, 6, 1, 0, 0), 1012.40, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := h.At(tt.at)
			if ok != tt.ok || got.PricePerKWh != tt.price {
				t.Fatalf("At(%v) = %v, %v; want %v, %v", tt.at, got.PricePerKWh, ok, tt.price, tt.ok)
			}
		})
	}

	// The bill uses the tariff in force at the period start; a period that
	// starts before any tariff falls back to the first one starting inside it.
	for _, tt := range []struct {
		at    time.Time
		price float64
		ok    bool
	}{
		{d(2026, 11, 20, 0, 0), 990.53, true}, // Nov 15 – Dec 15
		{d(2026, 12, 20, 0, 0), 1012.40, true},
		{d(2026, 10, 20, 0, 0), 990.53, true}, // Oct 15 – Nov 15, seed starts Nov 1
		{d(2026, 9, 20, 0, 0), 0, false},
	} {
		got, ok := h.ForPeriod(h.Period(tt.at))
		if ok != tt.ok || got.PricePerKWh != tt.price {
			t.Errorf("ForPeriod(%v) = %v, %v; want %v, %v", tt.at, got.PricePerKWh, ok, tt.price, tt.ok)
		}
	}
}

func TestAddReplacesSameStartAndValidates(t *testing.T) {
	h := Seed()
	if err := h.Add(Tariff{Provider: "Enel", PricePerKWh: 1000, Start: "2026-11-01"}); err != nil {
		t.Fatal(err)
	}
	if len(h.Tariffs) != 1 || h.Tariffs[0].PricePerKWh != 1000 {
		t.Errorf("Add did not replace: %+v", h.Tariffs)
	}
	for _, tt := range []struct {
		t    Tariff
		want string
	}{
		{Tariff{Provider: "", PricePerKWh: 1, Start: "2026-11-01"}, "tariff: provider is required"},
		{Tariff{Provider: "Enel", PricePerKWh: 0, Start: "2026-11-01"}, "tariff Enel 2026-11-01: price_per_kwh must be greater than 0"},
		{Tariff{Provider: "Enel", PricePerKWh: 1, Start: "11/01/2026"}, `tariff Enel: start "11/01/2026" must be a date like 2026-11-01`},
	} {
		if err := h.Add(tt.t); err == nil || err.Error() != tt.want {
			t.Errorf("Add(%+v) = %v, want %q", tt.t, err, tt.want)
		}
	}
	h.CutoffDay = 32
	if err := h.Validate(); err == nil || err.Error() != "tariffs: cutoff_day must be between 1 and 31, got 32" {
		t.Errorf("Validate cutoff = %v", err)
	}
}

func TestLoadOrCreateSeedsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watts", "tariffs.toml")
	h, created, err := LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("LoadOrCreate = %v created=%v", err, created)
	}
	if len(h.Tariffs) != 1 || h.Tariffs[0].Provider != "Enel" || h.Tariffs[0].PricePerKWh != 990.53 ||
		h.Tariffs[0].Start != "2026-11-01" || h.CutoffDay != 1 {
		t.Fatalf("seed = %+v", h)
	}
	h.Add(Tariff{Provider: "Enel", PricePerKWh: 1012.4, Start: "2026-12-01"})
	if err := Save(path, h); err != nil {
		t.Fatal(err)
	}
	got, created, err := LoadOrCreate(path)
	if err != nil || created || len(got.Tariffs) != 2 || got.Tariffs[1].PricePerKWh != 1012.4 {
		t.Fatalf("reload = %+v %v %v", got, created, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.toml")
	os.WriteFile(bad, []byte("cutoff_day = 1\n[[tariffs]]\nprovider = \"Enel\"\nprice_per_kwh = -1\nstart = \"2026-11-01\"\n"), 0o644)
	if _, err := Load(bad); err == nil || err.Error() != "tariffs "+bad+": tariff Enel 2026-11-01: price_per_kwh must be greater than 0" {
		t.Errorf("Load(bad) = %v", err)
	}
}
