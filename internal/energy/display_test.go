package energy

import (
	"reflect"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
)

// lgDDC is the LG with DDC: catalog range 7–22 W, standby 0.3 W.
var lgDDC = config.Item{Name: "LG", Mode: config.ModeFollows, Watts: 22, Quantity: 1, Brand: "LG", Model: "29WQ600",
	Follows: []string{"Mac", "PC Linux"}, DDC: config.SourceLinux}

// readings returns a DDC reading every minute at from, from+1m, … before to.
func readings(from, to time.Time, brightness int, on bool) []store.Display {
	var out []store.Display
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		out = append(out, store.Display{TS: t, Source: "linux", Brightness: brightness, MaxBrightness: 100, On: on})
	}
	return out
}

func TestDDCEnergy(t *testing.T) {
	macOn := run("mac", at(8, 9, 0), at(8, 12, 0), 30) // 9:00–12:00
	lg2 := lgDDC
	lg2.Quantity = 2
	tests := []struct {
		name     string
		data     store.Data
		item     config.Item
		from, to time.Time
		wh       float64
		onHours  float64
	}{
		{"on at 65 %: 7 + 15 × 0.65 = 16.75 W",
			store.Data{Displays: readings(at(8, 20, 0), at(8, 21, 0), 65, true)}, lgDDC, at(8, 20, 0), at(8, 21, 0), 16.75, 1},
		{"DPM off: standby 0.3 W, not on",
			store.Data{Displays: readings(at(8, 20, 0), at(8, 21, 0), 65, false)}, lgDDC, at(8, 20, 0), at(8, 21, 0), 0.3, 0},
		{"quantity multiplies",
			store.Data{Displays: readings(at(8, 20, 0), at(8, 21, 0), 65, true)}, lg2, at(8, 20, 0), at(8, 21, 0), 33.5, 1},
		{"mixed day: DDC covers 10–11 inside the Mac's 9–12, no double count",
			store.Data{Samples: map[string][]store.Sample{"mac": macOn}, Displays: readings(at(8, 10, 0), at(8, 11, 0), 65, true)},
			lgDDC, at(8, 0, 0), at(9, 0, 0), 2*22 + 16.75, 3},
		{"mixed day: DDC also counts while no followed sample covers it",
			store.Data{Samples: map[string][]store.Sample{"mac": macOn}, Displays: readings(at(8, 13, 0), at(8, 13, 30), 100, true)},
			lgDDC, at(8, 0, 0), at(9, 0, 0), 3*22 + 0.5*22, 3.5},
		{"a reading covers up to the next one within 3 intervals, else one interval",
			store.Data{Displays: []store.Display{
				{TS: at(8, 20, 0), Source: "linux", Brightness: 100, MaxBrightness: 100, On: true}, // to 20:03
				{TS: at(8, 20, 3), Source: "linux", Brightness: 100, MaxBrightness: 100, On: true}, // gap 7 min: 1 min
				{TS: at(8, 20, 10), Source: "linux", Brightness: 100, MaxBrightness: 100, On: true},
			}}, lgDDC, at(8, 0, 0), at(9, 0, 0), 22 * 5 / 60.0, 5 / 60.0},
		{"clipped to the window",
			store.Data{Displays: readings(at(8, 20, 0), at(8, 21, 0), 65, true)}, lgDDC, at(8, 20, 30), at(8, 21, 0), 16.75 / 2, 0.5},
		{"max_brightness 0 is unusable: the fixed rule applies",
			store.Data{Samples: map[string][]store.Sample{"mac": macOn}, Displays: []store.Display{
				{TS: at(8, 10, 0), Source: "linux", Brightness: 0, MaxBrightness: 0, On: true}}},
			lgDDC, at(8, 0, 0), at(9, 0, 0), 3 * 22, 3},
		{"another source's readings are ignored",
			store.Data{Samples: map[string][]store.Sample{"mac": macOn}, Displays: []store.Display{
				{TS: at(8, 10, 0), Source: "mac", Brightness: 0, MaxBrightness: 100, On: true}}},
			lgDDC, at(8, 0, 0), at(9, 0, 0), 3 * 22, 3},
		{"aggregate day with readings: on_seconds minus the DDC time",
			store.Data{Daily: map[string][]store.DailyAggregate{"mac": {{Day: "2026-11-07", Source: "mac", Wh: 300, OnSeconds: 5 * 3600}}},
				Displays: readings(at(7, 20, 0), at(7, 21, 0), 65, true)},
			lgDDC, at(7, 0, 0), at(8, 0, 0), 4*22 + 16.75, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ItemEnergy(input(tt.data, mac, pc, tt.item), tt.item, tt.from, tt.to)
			if !near(got.Wh, tt.wh) || !near(got.OnSeconds, tt.onHours*3600) {
				t.Fatalf("ItemEnergy = %.4f Wh, %.1f s; want %.4f Wh, %.1f s", got.Wh, got.OnSeconds, tt.wh, tt.onHours*3600)
			}
		})
	}
}

// With no display rows, a ddc item gives exactly the fixed-watts numbers,
// on sample days and on aggregate days.
func TestDDCWithoutReadingsIsUnchanged(t *testing.T) {
	plain := lgDDC
	plain.DDC = ""
	datas := map[string]store.Data{
		"samples": {Samples: map[string][]store.Sample{
			"mac":   run("mac", at(8, 9, 0), at(8, 11, 0), 30),
			"linux": run("linux", at(8, 10, 0), at(8, 12, 0), 200),
		}},
		"aggregate day": {
			Samples: map[string][]store.Sample{"linux": run("linux", at(7, 20, 0), at(7, 22, 0), 150)},
			Daily:   map[string][]store.DailyAggregate{"mac": {{Day: "2026-11-07", Source: "mac", Wh: 300, OnSeconds: 5 * 3600}}},
		},
		"empty": {},
	}
	for name, d := range datas {
		for _, w := range [][2]time.Time{{at(7, 0, 0), at(9, 0, 0)}, {at(7, 12, 30), at(8, 10, 15)}} {
			a := ItemEnergy(input(d, mac, pc, lgDDC), lgDDC, w[0], w[1])
			b := ItemEnergy(input(d, mac, pc, plain), plain, w[0], w[1])
			if a != b {
				t.Errorf("%s %v: ddc %+v != fixed %+v", name, w, a, b)
			}
			da := Days(input(d, mac, pc, lgDDC), w[0], w[1], 990.53)
			db := Days(input(d, mac, pc, plain), w[0], w[1], 990.53)
			if !reflect.DeepEqual(da, db) {
				t.Errorf("%s %v: days differ", name, w)
			}
		}
	}
}

func TestNowDDC(t *testing.T) {
	now := at(8, 20, 0)
	macFresh := map[string][]store.Sample{"mac": run("mac", at(8, 19, 0), at(8, 19, 58), 35)}
	tests := []struct {
		name    string
		data    store.Data
		watts   float64
		on      bool
		display *DisplayState
	}{
		{"fresh reading on", store.Data{Samples: macFresh, Displays: []store.Display{
			{TS: at(8, 19, 0), Source: "linux", Brightness: 10, MaxBrightness: 100, On: true},
			{TS: at(8, 19, 58), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true},
		}}, 16.75, true, &DisplayState{Source: "linux", Brightness: 65, Max: 100, On: true, TS: at(8, 19, 58)}},
		{"fresh reading asleep", store.Data{Displays: []store.Display{
			{TS: at(8, 19, 57), Source: "linux", Brightness: 65, MaxBrightness: 100, On: false},
		}}, 0.3, true, &DisplayState{Source: "linux", Brightness: 65, Max: 100, TS: at(8, 19, 57)}},
		{"stale reading: the fixed rule", store.Data{Samples: macFresh, Displays: []store.Display{
			{TS: at(8, 19, 56), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true},
		}}, 22, true, nil},
		{"stale reading, nothing on", store.Data{Displays: []store.Display{
			{TS: at(8, 19, 56), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true},
		}}, 0, false, nil},
		{"a reading after now is not used", store.Data{Samples: macFresh, Displays: []store.Display{
			{TS: at(8, 20, 1), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true},
		}}, 22, true, nil},
		{"unusable reading: the fixed rule", store.Data{Samples: macFresh, Displays: []store.Display{
			{TS: at(8, 19, 59), Source: "linux", Brightness: 65, MaxBrightness: 0, On: true},
		}}, 22, true, nil},
	}
	for _, tt := range tests {
		got := Now(input(tt.data, mac, pc, lgDDC), lgDDC, now)
		if !near(got.Watts, tt.watts) || got.On != tt.on || !reflect.DeepEqual(got.Display, tt.display) {
			t.Errorf("%s: Now = %+v (display %+v), want %v W on %v display %+v", tt.name, got, got.Display, tt.watts, tt.on, tt.display)
		}
	}
	r := Build(input(store.Data{Samples: macFresh, Displays: []store.Display{
		{TS: at(8, 19, 58), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true}}}, mac, pc, lgDDC), tariff.Seed(), now)
	if !near(r.TotalWatts, 35+16.75) {
		t.Errorf("TotalWatts = %v, want 51.75", r.TotalWatts)
	}
}
