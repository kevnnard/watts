package importer

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kevnnard/watts/internal/store"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		rest float64
		psu  float64
		want store.DailyAggregate
		err  string
	}{
		{"mac", "6.4708 1386\n", 35, 0.85, store.DailyAggregate{
			Day: "2026-11-07", Source: "mac", Wh: 6.4708, OnSeconds: 1386, MonitorSeconds: 1386, Origin: "consumo"}, ""},
		{"mac no monitor", "139.4696 0", 35, 0.85, store.DailyAggregate{
			Day: "2026-11-07", Source: "mac", Wh: 139.4696, Origin: "consumo"}, ""},
		{"linux", "120.5 80.25 36000 1800\n", 35, 0.85, store.DailyAggregate{
			Day: "2026-11-07", Source: "linux", Wh: (120.5+80.25)/0.85 + 35*36000/3600/0.85,
			OnSeconds: 36000, MonitorSeconds: 1800, GPUWh: 120.5, CPUWh: 80.25, Origin: "consumo"}, ""},
		{"linux other rest and psu", "10 20 7200 0", 20, 0.9, store.DailyAggregate{
			Day: "2026-11-07", Source: "linux", Wh: 30/0.9 + 20*2/0.9,
			OnSeconds: 7200, GPUWh: 10, CPUWh: 20, Origin: "consumo"}, ""},
		{"three fields", "1 2 3", 35, 0.85, store.DailyAggregate{}, "2026-11-07: 3 fields, want 2 (mac_wh monitor_s) or 4 (gpu_wh cpu_wh on_s monitor_s)"},
		{"empty", "  \n", 35, 0.85, store.DailyAggregate{}, "2026-11-07: empty file"},
		{"two lines", "1 2\n3 4\n", 35, 0.85, store.DailyAggregate{}, "2026-11-07: 2 lines, want 1"},
		{"not a number", "1.5 abc", 35, 0.85, store.DailyAggregate{}, `2026-11-07: field 2 "abc" is not a number`},
		{"negative", "-1 0", 35, 0.85, store.DailyAggregate{}, `2026-11-07: field 1 "-1" is negative`},
		{"NaN", "NaN 0", 35, 0.85, store.DailyAggregate{}, `2026-11-07: field 1 "NaN" is not a number`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse("2026-11-07", []byte(tt.in), tt.rest, tt.psu)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !near(got.Wh, tt.want.Wh) {
				t.Errorf("Wh = %v, want %v", got.Wh, tt.want.Wh)
			}
			got.Wh = tt.want.Wh
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func ledger(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if strings.HasSuffix(name, "/") {
			os.Mkdir(filepath.Join(dir, name), 0o755)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "watts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func allDaily(t *testing.T, st *store.Store) []store.DailyAggregate {
	t.Helper()
	as, err := st.Daily("0000-01-01", "9999-12-31")
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func TestRun(t *testing.T) {
	files := map[string]string{
		"2026-11-07":     "6.4708 1386\n",
		"2026-11-08":     "139.4696 0\n",
		"2026-11-08.tmp": "1 1\n",
		"notes/":         "",
	}
	dir := ledger(t, files)
	stat0, _ := os.Stat(filepath.Join(dir, "2026-11-07"))
	st := openStore(t)
	var out bytes.Buffer
	if err := Run(dir, Options{RestW: 35, PSUEfficiency: 0.85, Today: "2026-11-08"}, st, &out); err != nil {
		t.Fatal(err)
	}
	want := "2026-11-07  mac        6.471 Wh  on 0h23m  new\n" +
		"2026-11-08  mac      139.470 Wh  on 0h00m  new\n" +
		"imported 2 days from " + dir + " (mac 2, linux 0): 2 new, 0 replaced\n" +
		"skipped 1 file not named YYYY-MM-DD: 2026-11-08.tmp\n" +
		"note: 2026-11-08 is today; while it is imported, its aggregate replaces today's daemon samples. Re-run after midnight to get the full day.\n"
	if out.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out.String(), want)
	}
	first := allDaily(t, st)
	if len(first) != 2 || first[0].Day != "2026-11-07" || first[0].OnSeconds != 1386 || first[0].Origin != "consumo" || first[1].Wh != 139.4696 {
		t.Fatalf("store = %+v", first)
	}

	// Re-running replaces instead of adding.
	out.Reset()
	if err := Run(dir, Options{RestW: 35, PSUEfficiency: 0.85, Today: "2026-11-09"}, st, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "imported 2 days from "+dir+" (mac 2, linux 0): 0 new, 2 replaced\n") || strings.Contains(out.String(), "note:") {
		t.Errorf("second run stdout =\n%s", out.String())
	}
	if again := allDaily(t, st); !reflect.DeepEqual(again, first) {
		t.Errorf("second run changed the store:\n %+v\n %+v", again, first)
	}

	// Source files are never modified.
	for name, body := range files {
		if strings.HasSuffix(name, "/") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if string(b) != body {
			t.Errorf("%s changed to %q", name, b)
		}
	}
	if stat1, _ := os.Stat(filepath.Join(dir, "2026-11-07")); !stat1.ModTime().Equal(stat0.ModTime()) {
		t.Error("source mtime changed")
	}
}

func TestRunLinux(t *testing.T) {
	dir := ledger(t, map[string]string{"2026-11-07": "120.5 80.25 36000 1800\n"})
	st := openStore(t)
	var out bytes.Buffer
	if err := Run(dir, Options{RestW: 35, PSUEfficiency: 0.85, Today: "2026-11-08"}, st, &out); err != nil {
		t.Fatal(err)
	}
	wh := (120.5+80.25)/0.85 + 35*10/0.85
	want := fmt.Sprintf("2026-11-07  linux  %9.3f Wh  on 10h00m  new\n", wh) +
		"imported 1 day from " + dir + " (mac 0, linux 1): 1 new, 0 replaced\n"
	if out.String() != want {
		t.Errorf("stdout =\n%q\nwant\n%q", out.String(), want)
	}
	as := allDaily(t, st)
	if len(as) != 1 || as[0].Source != "linux" || !near(as[0].Wh, wh) || as[0].GPUWh != 120.5 || as[0].OnSeconds != 36000 {
		t.Errorf("store = %+v", as)
	}
}

func TestRunRejectsWithoutWriting(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		dir   string
		err   string
	}{
		{"bad line format", map[string]string{"2026-11-07": "1 2\n", "2026-11-08": "1 2 3\n"}, "",
			"import-consumo: 2026-11-08: 3 fields, want 2 (mac_wh monitor_s) or 4 (gpu_wh cpu_wh on_s monitor_s)"},
		{"missing dir", nil, "/nonexistent/consumo", "import-consumo: open /nonexistent/consumo: no such file or directory"},
		{"bad options", map[string]string{"2026-11-07": "1 2\n"}, "", "import-consumo: psu_efficiency must be greater than 0 and at most 1, got 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.dir
			if dir == "" {
				dir = ledger(t, tt.files)
			}
			st := openStore(t)
			st.UpsertDaily(store.DailyAggregate{Day: "2026-11-07", Source: "mac", Wh: 1, Origin: "consumo"})
			before := allDaily(t, st)
			opts := Options{RestW: 35, PSUEfficiency: 0.85}
			if tt.name == "bad options" {
				opts.PSUEfficiency = 0
			}
			var out bytes.Buffer
			err := Run(dir, opts, st, &out)
			if err == nil || err.Error() != tt.err {
				t.Fatalf("err = %v, want %q", err, tt.err)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if after := allDaily(t, st); !reflect.DeepEqual(after, before) {
				t.Errorf("store changed: %+v", after)
			}
		})
	}
}
