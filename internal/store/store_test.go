package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data", "watts.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)

func TestSamples(t *testing.T) {
	s := open(t)
	for i := 1; i <= 5; i++ {
		if err := s.AddSample(Sample{TS: t0.Add(time.Duration(i) * time.Minute), Source: "mac", Watts: float64(10 * i), Seconds: 60}); err != nil {
			t.Fatal(err)
		}
	}
	s.AddSample(Sample{TS: t0.Add(2 * time.Minute), Source: "linux", Watts: 200, Seconds: 60})
	// Re-adding the same (source, ts) replaces, so remote merges are idempotent.
	s.AddSamples([]Sample{{TS: t0.Add(5 * time.Minute), Source: "mac", Watts: 55, Seconds: 60}})

	got, err := s.Samples("mac", t0.Add(2*time.Minute), t0.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Watts != 30 || got[2].Watts != 55 {
		t.Fatalf("Samples (from, to] = %+v", got)
	}
	all, _ := s.Samples("", t0, t0.Add(time.Hour))
	if len(all) != 6 {
		t.Errorf("all sources = %d samples, want 6", len(all))
	}
	last, ok, err := s.LatestSample("mac")
	if err != nil || !ok || last.Watts != 55 || !last.TS.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("LatestSample = %+v %v %v", last, ok, err)
	}
	if _, ok, _ := s.LatestSample("none"); ok {
		t.Error("LatestSample(none) ok")
	}
	srcs, _ := s.Sources()
	if len(srcs) != 2 || srcs[0] != "linux" || srcs[1] != "mac" {
		t.Errorf("Sources = %v", srcs)
	}
}

func TestDailyUpsert(t *testing.T) {
	s := open(t)
	a := DailyAggregate{Day: "2026-11-01", Source: "linux", Wh: 1500, OnSeconds: 36000, GPUWh: 400, CPUWh: 500, MonitorSeconds: 20000, Origin: "consumo"}
	if err := s.UpsertDaily(a); err != nil {
		t.Fatal(err)
	}
	a.Wh = 1600
	s.UpsertDaily(a)
	s.UpsertDaily(DailyAggregate{Day: "2026-11-02", Source: "mac", Wh: 300, OnSeconds: 30000, Origin: "consumo"})
	s.UpsertDaily(DailyAggregate{Day: "2026-11-09", Source: "mac", Wh: 1})
	got, err := s.Daily("2026-11-01", "2026-11-08")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Wh != 1600 || got[0].GPUWh != 400 || got[1].Source != "mac" {
		t.Fatalf("Daily = %+v", got)
	}
	if err := s.UpsertDaily(DailyAggregate{Day: "Nov 1", Source: "mac"}); err == nil {
		t.Error("UpsertDaily accepted a bad day")
	}
}

func TestTogglesAndLoad(t *testing.T) {
	s := open(t)
	s.AddToggle(Toggle{Item: "Fan", TS: t0, On: true})
	s.AddToggle(Toggle{Item: "Fan", TS: t0.Add(time.Hour), On: false})
	s.AddToggle(Toggle{Item: "Lamp", TS: t0.Add(30 * time.Minute), On: true})
	s.AddToggle(Toggle{Item: "Fan", TS: t0.Add(3 * time.Hour), On: true})
	s.AddSample(Sample{TS: t0.Add(time.Minute), Source: "mac", Watts: 20, Seconds: 60})
	s.AddSample(Sample{TS: t0.Add(2*time.Hour + 2*time.Minute), Source: "mac", Watts: 20, Seconds: 60})
	s.UpsertDaily(DailyAggregate{Day: "2026-11-07", Source: "mac", Wh: 100})
	s.UpsertDaily(DailyAggregate{Day: "2026-10-01", Source: "mac", Wh: 100})

	on, ok, err := s.ToggleState("Fan")
	if err != nil || !ok || !on {
		t.Errorf("ToggleState(Fan) = %v %v %v", on, ok, err)
	}

	from, to := t0.Add(-24*time.Hour), t0.Add(2*time.Hour)
	d, err := s.Load(from, to, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Toggles["Fan"]) != 2 || len(d.Toggles["Lamp"]) != 1 {
		t.Errorf("toggles = %+v", d.Toggles)
	}
	if len(d.Samples["mac"]) != 2 { // the 2h02m sample is within the margin
		t.Errorf("samples = %+v", d.Samples)
	}
	if len(d.Daily["mac"]) != 1 || d.Daily["mac"][0].Day != "2026-11-07" {
		t.Errorf("daily = %+v", d.Daily)
	}
}

func TestDisplays(t *testing.T) {
	s := open(t)
	xs := []Display{
		{TS: t0.Add(time.Minute), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true},
		{TS: t0.Add(2 * time.Minute), Source: "linux", Brightness: 0, MaxBrightness: 100, On: false},
		{TS: t0.Add(3 * time.Minute), Source: "linux", Brightness: 40, MaxBrightness: 100, On: true},
		{TS: t0.Add(time.Hour), Source: "linux", Brightness: 50, MaxBrightness: 100, On: true},
	}
	if err := s.AddDisplays(xs); err != nil {
		t.Fatal(err)
	}
	// The same (source, ts) replaces.
	if err := s.AddDisplay(Display{TS: t0.Add(3 * time.Minute), Source: "linux", Brightness: 45, MaxBrightness: 100, On: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Displays("linux", t0.Add(time.Minute), t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := []Display{
		{TS: t0.Add(2 * time.Minute), Source: "linux", Brightness: 0, MaxBrightness: 100, On: false},
		{TS: t0.Add(3 * time.Minute), Source: "linux", Brightness: 45, MaxBrightness: 100, On: true},
	}
	for i := range got {
		got[i].TS = got[i].TS.In(time.Local)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Displays (from, to] = %+v", got)
	}
	// Load reaches back by the margin, because a reading covers the time after it.
	d, err := s.Load(t0.Add(3*time.Minute), t0.Add(30*time.Minute), 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Displays) != 2 || !d.Displays[0].TS.Equal(t0.Add(2*time.Minute)) || !d.Displays[1].TS.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("Load displays = %+v", d.Displays)
	}
}

// A store created before the displays table existed opens, keeps its rows
// and takes display readings.
func TestOpenOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watts.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	const old = `
CREATE TABLE samples (ts INTEGER NOT NULL, source TEXT NOT NULL, watts REAL NOT NULL, seconds REAL NOT NULL, PRIMARY KEY (source, ts)) WITHOUT ROWID;
CREATE INDEX samples_ts ON samples (ts);
CREATE TABLE daily (day TEXT NOT NULL, source TEXT NOT NULL, wh REAL NOT NULL DEFAULT 0, on_seconds REAL NOT NULL DEFAULT 0,
	monitor_seconds REAL NOT NULL DEFAULT 0, gpu_wh REAL NOT NULL DEFAULT 0, cpu_wh REAL NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT '', PRIMARY KEY (day, source));
CREATE TABLE toggles (item TEXT NOT NULL, ts INTEGER NOT NULL, on_ INTEGER NOT NULL, PRIMARY KEY (item, ts));
INSERT INTO samples VALUES (1794150060, 'linux', 70.5, 60);
INSERT INTO daily (day, source, wh) VALUES ('2026-11-01', 'linux', 1500);`
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for i := 0; i < 2; i++ { // and again, now with the new table in place
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open old db: %v", err)
		}
		x, ok, err := s.LatestSample("linux")
		if err != nil || !ok || x.Watts != 70.5 || x.TS.Unix() != 1794150060 {
			t.Errorf("old sample = %+v %v %v", x, ok, err)
		}
		if as, _ := s.Daily("2026-11-01", "2026-11-01"); len(as) != 1 || as[0].Wh != 1500 {
			t.Errorf("old daily = %+v", as)
		}
		if err := s.AddDisplay(Display{TS: time.Unix(1794150060, 0), Source: "linux", Brightness: 65, MaxBrightness: 100, On: true}); err != nil {
			t.Errorf("AddDisplay on old db: %v", err)
		}
		if ds, _ := s.Displays("", time.Unix(0, 0), time.Unix(1794150060, 0)); len(ds) != 1 {
			t.Errorf("displays = %+v", ds)
		}
		s.Close()
	}
}
