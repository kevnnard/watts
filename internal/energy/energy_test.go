package energy

import (
	"math"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
)

var bog = time.FixedZone("COT", -5*3600)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, bog) }

var (
	mac    = config.Item{Name: "Mac", Mode: config.ModeMeasured, Source: "mac", Quantity: 1}
	pc     = config.Item{Name: "PC Linux", Mode: config.ModeMeasured, Source: "linux", Quantity: 1}
	lg     = config.Item{Name: "LG", Mode: config.ModeFollows, Watts: 22, Quantity: 1, Follows: []string{"Mac", "PC Linux"}}
	lights = config.Item{Name: "Lights", Mode: config.ModeSchedule, Watts: 9, Quantity: 3, Schedule: []string{"18:00-24:00"}}
	night  = config.Item{Name: "Night", Mode: config.ModeSchedule, Watts: 10, Quantity: 1, Schedule: []string{"22:00-02:00"}}
	fan    = config.Item{Name: "Fan", Mode: config.ModeManual, Watts: 50, Quantity: 1}
)

// run returns samples every minute over [from, to) at watts.
func run(source string, from, to time.Time, watts float64) []store.Sample {
	var out []store.Sample
	for t := from.Add(time.Minute); !t.After(to); t = t.Add(time.Minute) {
		out = append(out, store.Sample{TS: t, Source: source, Watts: watts, Seconds: 60})
	}
	return out
}

func input(data store.Data, items ...config.Item) Input {
	return Input{Items: items, Data: data, Interval: time.Minute, Loc: bog}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestItemEnergy(t *testing.T) {
	gapSamples := []store.Sample{
		{TS: at(8, 10, 1), Source: "mac", Watts: 60, Seconds: 60},
		{TS: at(8, 10, 4), Source: "mac", Watts: 60, Seconds: 180},  // 3 intervals: kept
		{TS: at(8, 12, 0), Source: "mac", Watts: 60, Seconds: 6960}, // slept: counts as 60 s
		{TS: at(8, 12, 1), Source: "mac", Watts: 0, Seconds: 60},    // on battery: 0 W
	}
	overlapData := store.Data{Samples: map[string][]store.Sample{
		"mac":   run("mac", at(8, 9, 0), at(8, 11, 0), 30),
		"linux": run("linux", at(8, 10, 0), at(8, 12, 0), 200),
	}}
	aggData := store.Data{
		Samples: map[string][]store.Sample{"mac": append(run("mac", at(7, 9, 0), at(7, 10, 0), 40), run("mac", at(8, 9, 0), at(8, 10, 0), 40)...)},
		Daily:   map[string][]store.DailyAggregate{"mac": {{Day: "2026-11-07", Source: "mac", Wh: 480, OnSeconds: 8 * 3600}}},
	}
	toggles := store.Data{Toggles: map[string][]store.Toggle{"Fan": {
		{Item: "Fan", TS: at(8, 8, 0), On: true},
		{Item: "Fan", TS: at(8, 8, 30), On: true}, // repeated on is ignored
		{Item: "Fan", TS: at(8, 9, 0), On: false},
		{Item: "Fan", TS: at(8, 11, 0), On: true}, // still on at window end
	}}}

	tests := []struct {
		name     string
		in       Input
		item     config.Item
		from, to time.Time
		wh       float64
		onHours  float64
	}{
		{"empty store measured", input(store.Data{}, mac), mac, at(8, 0, 0), at(9, 0, 0), 0, 0},
		{"empty store follows", input(store.Data{}, mac, pc, lg), lg, at(8, 0, 0), at(9, 0, 0), 0, 0},
		{"empty store manual", input(store.Data{}, fan), fan, at(8, 0, 0), at(9, 0, 0), 0, 0},
		{"measured steady", input(store.Data{Samples: map[string][]store.Sample{"mac": run("mac", at(8, 9, 0), at(8, 11, 0), 30)}}, mac),
			mac, at(8, 0, 0), at(9, 0, 0), 60, 2},
		{"measured clipped to window", input(store.Data{Samples: map[string][]store.Sample{"mac": run("mac", at(8, 9, 0), at(8, 11, 0), 30)}}, mac),
			mac, at(8, 9, 30), at(8, 10, 0), 15, 0.5},
		{"measured gap rule", input(store.Data{Samples: map[string][]store.Sample{"mac": gapSamples}}, mac),
			mac, at(8, 0, 0), at(9, 0, 0), 60 * 5 / 60.0, 6 / 60.0},
		{"measured uses aggregate on its day only", input(aggData, mac), mac, at(7, 0, 0), at(9, 0, 0), 480 + 40, 9},
		{"measured prorates aggregate", input(aggData, mac), mac, at(7, 0, 0), at(7, 12, 0), 240, 4},
		{"follows union of overlapping machines", input(overlapData, mac, pc, lg), lg, at(8, 0, 0), at(9, 0, 0), 22 * 3, 3},
		{"follows on battery still on", input(store.Data{Samples: map[string][]store.Sample{"mac": run("mac", at(8, 9, 0), at(8, 10, 0), 0)}}, mac, pc, lg),
			lg, at(8, 0, 0), at(9, 0, 0), 22, 1},
		{"follows gap is off", input(store.Data{Samples: map[string][]store.Sample{"mac": gapSamples}}, mac, pc, lg),
			lg, at(8, 0, 0), at(9, 0, 0), 22 * 6 / 60.0, 6 / 60.0},
		{"follows aggregate day max of on_seconds and samples", input(store.Data{
			Samples: map[string][]store.Sample{"linux": run("linux", at(7, 20, 0), at(7, 22, 0), 150)},
			Daily:   map[string][]store.DailyAggregate{"mac": {{Day: "2026-11-07", Source: "mac", Wh: 300, OnSeconds: 5 * 3600}}},
		}, mac, pc, lg), lg, at(7, 0, 0), at(8, 0, 0), 22 * 5, 5},
		{"schedule full day", input(store.Data{}, lights), lights, at(8, 0, 0), at(9, 0, 0), 27 * 6, 6},
		{"schedule partial", input(store.Data{}, lights), lights, at(8, 0, 0), at(8, 19, 30), 27 * 1.5, 1.5},
		{"schedule across days", input(store.Data{}, lights), lights, at(1, 0, 0), at(8, 0, 0), 27 * 6 * 7, 42},
		{"schedule crossing midnight, one day", input(store.Data{}, night), night, at(8, 0, 0), at(9, 0, 0), 10 * 4, 4},
		{"schedule crossing midnight, after midnight", input(store.Data{}, night), night, at(9, 0, 0), at(9, 1, 0), 10, 1},
		{"schedule crossing midnight, span", input(store.Data{}, night), night, at(8, 23, 0), at(9, 3, 0), 30, 3},
		{"schedule overlapping ranges not double counted", input(store.Data{}, config.Item{Name: "X", Mode: config.ModeSchedule, Watts: 10, Quantity: 1, Schedule: []string{"18:00-22:00", "20:00-23:00"}}),
			config.Item{Name: "X", Mode: config.ModeSchedule, Watts: 10, Quantity: 1, Schedule: []string{"18:00-22:00", "20:00-23:00"}}, at(8, 0, 0), at(9, 0, 0), 50, 5},
		{"manual intervals", input(toggles, fan), fan, at(8, 0, 0), at(8, 12, 0), 50 * 2, 2},
		{"manual window starts while on", input(toggles, fan), fan, at(8, 8, 45), at(8, 10, 0), 50 * 0.25, 0.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ItemEnergy(tt.in, tt.item, tt.from, tt.to)
			if !near(got.Wh, tt.wh) || !near(got.OnSeconds, tt.onHours*3600) {
				t.Fatalf("ItemEnergy = %.4f Wh, %.1f s; want %.4f Wh, %.1f s", got.Wh, got.OnSeconds, tt.wh, tt.onHours*3600)
			}
		})
	}
}

func TestSampleSplitAcrossMidnight(t *testing.T) {
	// A 3-minute sample ending at 00:01 is 2 minutes yesterday, 1 today.
	in := input(store.Data{Samples: map[string][]store.Sample{"mac": {{TS: at(9, 0, 1), Source: "mac", Watts: 60, Seconds: 180}}}}, mac)
	days := Days(in, at(8, 0, 0), at(10, 0, 0), 1000)
	if len(days) != 2 || !near(days[0].Wh, 2) || !near(days[1].Wh, 1) || !near(days[1].Cost, 1) {
		t.Fatalf("Days = %+v", days)
	}
}

func TestNow(t *testing.T) {
	now := at(8, 20, 0)
	data := store.Data{
		Samples: map[string][]store.Sample{
			"mac":   run("mac", at(8, 19, 0), at(8, 19, 58), 35),
			"linux": run("linux", at(8, 10, 0), at(8, 11, 0), 200), // stale
		},
		Toggles: map[string][]store.Toggle{"Fan": {{Item: "Fan", TS: at(8, 19, 0), On: true}}},
	}
	in := input(data, mac, pc, lg, lights, night, fan)
	tests := []struct {
		item    config.Item
		watts   float64
		on      bool
		offline bool
	}{
		{mac, 35, true, false},
		{pc, 0, false, true},
		{lg, 22, true, false},
		{lights, 27, true, false},
		{night, 0, false, false},
		{fan, 50, true, false},
	}
	for _, tt := range tests {
		got := Now(in, tt.item, now)
		if got.Watts != tt.watts || got.On != tt.on || got.Offline != tt.offline {
			t.Errorf("Now(%s) = %+v, want watts %v on %v offline %v", tt.item.Name, got, tt.watts, tt.on, tt.offline)
		}
	}
	if got := Now(in, night, at(9, 1, 0)); !got.On {
		t.Errorf("night at 01:00 = %+v, want on (range crosses midnight)", got)
	}
	empty := input(store.Data{}, mac, lg)
	if got := Now(empty, mac, now); !got.Offline || got.Watts != 0 {
		t.Errorf("empty store mac = %+v, want offline", got)
	}
	if got := Now(empty, lg, now); got.On {
		t.Errorf("empty store lg = %+v, want off", got)
	}
}

func TestProject(t *testing.T) {
	p := tariff.PeriodFor(at(8, 0, 0), 1) // Nov 1 – Dec 1, 30 days
	tests := []struct {
		name string
		now  time.Time
		so   float64
		want float64
	}{
		{"before start", at(1, 0, 0), 0, 0},
		{"after 10 days", at(11, 0, 0), 10000, 30000},
		{"after the end", time.Date(2026, 12, 3, 0, 0, 0, 0, bog), 31000, 31000},
	}
	for _, tt := range tests {
		got := Project(tt.so, p, tt.now, 1000)
		if !near(got.Wh, tt.want) || !near(got.Cost, tt.want) {
			t.Errorf("%s: Project = %+v, want %v Wh", tt.name, got, tt.want)
		}
	}
}

func TestBuild(t *testing.T) {
	now := at(8, 12, 0)
	data := store.Data{Samples: map[string][]store.Sample{
		"mac": append(run("mac", at(7, 10, 0), at(7, 12, 0), 50), run("mac", at(8, 10, 0), at(8, 12, 0), 50)...),
	}}
	in := input(data, mac, pc, lg)
	r := Build(in, tariff.Seed(), now)
	if !r.HasTariff || r.Tariff.PricePerKWh != 990.53 {
		t.Fatalf("tariff = %+v %v", r.Tariff, r.HasTariff)
	}
	if !r.Period.Start.Equal(at(1, 0, 0)) || !r.Period.End.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, bog)) {
		t.Errorf("period = %+v", r.Period)
	}
	// Today: Mac 2 h × 50 W = 100 Wh, LG 2 h × 22 W = 44 Wh.
	if !near(r.Today.Wh, 144) || !near(r.Today.Cost, 0.144*990.53) {
		t.Errorf("today = %+v", r.Today)
	}
	if !near(r.Bill.Wh, 288) || len(r.Bill.Items) != 3 || !near(r.Bill.Items[2].Wh, 88) {
		t.Errorf("bill = %+v", r.Bill)
	}
	if len(r.Days) != 8 || !near(r.Days[6].Wh, 144) || !near(r.Days[7].Wh, 144) || r.Days[0].Wh != 0 {
		t.Errorf("days = %+v", r.Days)
	}
	elapsed := now.Sub(r.Period.Start).Seconds()
	if !near(r.Projected.Wh, 288*r.Period.Duration().Seconds()/elapsed) {
		t.Errorf("projected = %+v", r.Projected)
	}
	if !near(r.TotalWatts, 72) || !r.Items[1].Offline {
		t.Errorf("now = %v %+v", r.TotalWatts, r.Items)
	}
}
