package tui

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/tariff"
)

// The fixtures carry the artboard data of design/usage-dashboard: Sunday
// Nov 8 2026 at 20:15 in Bogotá, Enel at $990,53 per kWh.

var bogota = time.FixedZone("COT", -5*3600)

var fixNow = time.Date(2026, 11, 8, 20, 15, 5, 0, bogota)

func fixConfig() *config.Config {
	return &config.Config{
		SampleIntervalSeconds: 60,
		Currency:              config.Currency{Main: "COP", Secondary: "USD", Rate: 4000},
		Remote:                config.Remote{Host: "kevnnard-station"},
		Items: []config.Item{
			{Name: "PC Linux", Mode: config.ModeMeasured, Source: config.SourceLinux, Quantity: 1, RestW: 35, PSUEfficiency: 0.85},
			{Name: "Mac", Mode: config.ModeMeasured, Source: config.SourceMac, Quantity: 1},
			{Name: "LG 29WQ600", Mode: config.ModeFollows, Watts: 25, Quantity: 1, Brand: "LG", Model: "29WQ600", Follows: []string{"Mac", "PC Linux"}},
			{Name: "Room lights", Mode: config.ModeSchedule, Watts: 9, Quantity: 3, Brand: "LED", Model: "bulb", Schedule: []string{"18:00-24:00"}},
			{Name: "Desk fan", Mode: config.ModeManual, Watts: 45, Quantity: 1, Brand: "Samurai"},
		},
	}
}

func fixHistory() tariff.History { return tariff.Seed() }

const fixPrice = 990.53

func totals(from, to time.Time, wh map[string]float64, order []string) energy.Totals {
	t := energy.Totals{From: from, To: to}
	for _, n := range order {
		c := energy.Cost(wh[n], fixPrice)
		t.Items = append(t.Items, energy.ItemTotal{Name: n, Wh: wh[n], Cost: c})
		t.Wh += wh[n]
		t.Cost += c
	}
	return t
}

var fixOrder = []string{"PC Linux", "Mac", "LG 29WQ600", "Room lights", "Desk fan"}

// fixReport is the 01 dashboard; offline makes PC Linux unreachable since
// 19:05 (the 01 offline state).
func fixReport(offline bool) energy.Report {
	h := fixHistory()
	p := h.Period(fixNow)
	tr, _ := h.ForPeriod(p)
	r := energy.Report{At: fixNow, Tariff: tr, HasTariff: true, Period: p}
	r.Items = []energy.ItemNow{
		{Name: "PC Linux", Mode: config.ModeMeasured, Watts: 70.9, On: true, LastTS: fixNow.Add(-7 * time.Second)},
		{Name: "Mac", Mode: config.ModeMeasured, Watts: 9.9, On: true, LastTS: fixNow.Add(-3 * time.Second)},
		{Name: "LG 29WQ600", Mode: config.ModeFollows, Watts: 25, On: true},
		{Name: "Room lights", Mode: config.ModeSchedule, Watts: 27, On: true},
		{Name: "Desk fan", Mode: config.ModeManual},
	}
	today := map[string]float64{"PC Linux": 779.4, "Mac": 140.3, "LG 29WQ600": 309.9, "Room lights": 59.6, "Desk fan": 130.2}
	bill := map[string]float64{"PC Linux": 5400, "Mac": 1000, "LG 29WQ600": 2100, "Room lights": 600, "Desk fan": 700}
	if offline {
		r.Items[0] = energy.ItemNow{Name: "PC Linux", Mode: config.ModeMeasured, Offline: true, LastTS: time.Date(2026, 11, 8, 19, 5, 11, 0, bogota)}
		today["PC Linux"] = 696.6
		bill["PC Linux"] = 5317
	}
	for _, n := range r.Items {
		if n.On && !n.Offline {
			r.TotalWatts += n.Watts
		}
	}
	midnight := time.Date(2026, 11, 8, 0, 0, 0, 0, bogota)
	r.Today = totals(midnight, fixNow, today, fixOrder)
	r.Bill = totals(p.Start, fixNow, bill, fixOrder)
	r.Projected = energy.Projection{Wh: 29378.6, Cost: energy.Cost(29378.6, fixPrice)}
	full := []float64{1100, 1280, 980, 1250, 1310, 1120, 0}
	sum := 0.0
	for _, v := range full[:6] {
		sum += v
	}
	full[6] = r.Bill.Wh - r.Today.Wh - sum
	for i, wh := range append(full, r.Today.Wh) {
		r.Days = append(r.Days, energy.DayTotal{Day: p.Start.AddDate(0, 0, i), Wh: wh, Cost: energy.Cost(wh, fixPrice)})
	}
	return r
}

func fixSources(offline bool) []Source {
	linux := Source{Name: "linux", Online: true, LastTS: time.Date(2026, 11, 8, 20, 14, 58, 0, bogota), Via: "ssh kevnnard-station", Reads: "RAPL + GPU hwmon",
		Parts: []Part{{Name: "CPU", Watts: 13.8, Reads: "RAPL energy counter"}, {Name: "GPU", Watts: 11.5, Reads: "hwmon power1_average"}}}
	if offline {
		linux = Source{Name: "linux", LastTS: time.Date(2026, 11, 8, 19, 5, 11, 0, bogota), Via: "ssh kevnnard-station", Reads: "RAPL + GPU hwmon", Detail: "connection timed out"}
	}
	return []Source{
		{Name: "mac", Online: true, LastTS: time.Date(2026, 11, 8, 20, 15, 2, 0, bogota), Via: "this machine", Reads: "ioreg SystemPowerIn (charger)"},
		linux,
	}
}

// fixTrend is a deterministic 3-hour history shaped like the artboards.
func fixTrend(offline bool) Trend {
	start := time.Date(2026, 11, 8, 17, 15, 0, 0, bogota)
	t := Trend{Start: start, Items: map[string][]float64{}, Total: make([]float64, TrendMinutes), Partial: make([]bool, TrendMinutes)}
	cut := int(time.Date(2026, 11, 8, 19, 5, 0, 0, bogota).Sub(start).Minutes())
	lights := int(time.Date(2026, 11, 8, 18, 0, 0, 0, bogota).Sub(start).Minutes())
	for _, n := range fixOrder {
		t.Items[n] = make([]float64, TrendMinutes)
	}
	for i := 0; i < TrendMinutes; i++ {
		x := float64(i)
		pc := 62 + 6*math.Sin(x/9) + 4*math.Sin(x/23) + x/30
		if offline && i >= cut {
			pc = math.NaN()
			t.Partial[i] = true
		}
		mac := 9.9 + 0.6*math.Sin(x/5)
		lg := 25.0
		lt := 0.0
		if i >= lights {
			lt = 27
		}
		t.Items["PC Linux"][i] = pc
		t.Items["Mac"][i] = mac
		t.Items["LG 29WQ600"][i] = lg
		t.Items["Room lights"][i] = lt
		t.Items["Desk fan"][i] = 0
		if !math.IsNaN(pc) {
			cpu := 13 + 2*math.Sin(x/7)
			t.Items[PartKey("PC Linux", "CPU")] = append(t.Items[PartKey("PC Linux", "CPU")], cpu)
			t.Items[PartKey("PC Linux", "GPU")] = append(t.Items[PartKey("PC Linux", "GPU")], 11+1.5*math.Sin(x/13))
		}
		for _, v := range []float64{pc, mac, lg, lt} {
			if !math.IsNaN(v) {
				t.Total[i] += v
			}
		}
	}
	t.Hours = []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0.08, 0.13, 0.10, 0.10, 0.03, 0.13, 0.13, 0.10, 0.10, 0.13, 0.13, 0.04}
	if offline {
		t.Hours[19], t.Hours[20] = 0.10, 0.02
	}
	t.Since = map[string]time.Time{"Desk fan": time.Date(2026, 11, 8, 15, 50, 0, 0, bogota)}
	return t
}

// fixDDCConfig is fixConfig with the LG driven by DDC readings of linux.
func fixDDCConfig() *config.Config {
	c := fixConfig()
	c.Items[2].DDC = config.SourceLinux
	return c
}

// fixDDCReport is fixReport with a fresh DDC reading of the LG: 65 % on
// (7 + 15 × 0.65 = 16,75 W) or, with on false, asleep at 0,3 W.
func fixDDCReport(on bool) energy.Report {
	r := fixReport(false)
	lg := &r.Items[2]
	r.TotalWatts -= lg.Watts
	lg.Watts = 0.3
	if on {
		lg.Watts = 16.75
	}
	lg.Display = &energy.DisplayState{Source: config.SourceLinux, Brightness: 65, Max: 100, On: on, TS: fixNow.Add(-7 * time.Second)}
	r.TotalWatts += lg.Watts
	return r
}

// fakeData is a TrendData with a fixed answer.
type fakeData struct {
	offline bool
	ddc     bool // the LG has a fresh DDC reading (fixDDCReport)
	err     error
	calls   int
}

func (f *fakeData) Refresh(ctx context.Context, cfg *config.Config, h tariff.History, now time.Time) (energy.Report, []Source, error) {
	r, s, _, err := f.RefreshTrend(ctx, cfg, h, now)
	return r, s, err
}

func (f *fakeData) RefreshTrend(_ context.Context, cfg *config.Config, _ tariff.History, _ time.Time) (energy.Report, []Source, Trend, error) {
	f.calls++
	if f.err != nil {
		return energy.Report{}, nil, Trend{}, f.err
	}
	r := fixReport(f.offline)
	if f.ddc {
		r = fixDDCReport(true)
	}
	return r, fixSources(f.offline), fixTrend(f.offline), nil
}

// fakeEditor records the edits.
type fakeEditor struct {
	cfgs    []*config.Config
	hists   []tariff.History
	toggles []string
	err     error
}

func (e *fakeEditor) SaveConfig(c *config.Config) error {
	if e.err != nil {
		return e.err
	}
	e.cfgs = append(e.cfgs, c)
	return nil
}

func (e *fakeEditor) SaveTariffs(h tariff.History) error {
	if e.err != nil {
		return e.err
	}
	e.hists = append(e.hists, h)
	return nil
}

func (e *fakeEditor) SetManual(item string, on bool, _ time.Time) error {
	if e.err != nil {
		return e.err
	}
	st := "off"
	if on {
		st = "on"
	}
	e.toggles = append(e.toggles, item+" "+st)
	return nil
}

var errDisk = errors.New("disk full")
