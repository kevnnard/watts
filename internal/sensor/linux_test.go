package sensor

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
)

const (
	gpuDir  = "sys/class/drm/card1/device/hwmon/hwmon2"
	gpu2Dir = "sys/class/drm/card2/device/hwmon/hwmon5"
	raplDir = "sys/class/powercap/intel-rapl:0"
	raplMax = "262143328850"
)

// statLine builds a /proc/stat cpu line with the given busy and idle jiffies
// (user and idle carry them; iowait is idle time too).
func statLine(busy, idle int) string {
	return "cpu  " + strconv.Itoa(busy) + " 0 0 " + strconv.Itoa(idle) + " 0 0 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"
}

// tree writes files under a temp root and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		write(t, root, name, body)
	}
	return root
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeClock advances by the slept duration and applies the next file set.
type fakeClock struct {
	t     *testing.T
	root  string
	now   time.Time
	next  map[string]string
	slept []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	for name, body := range c.next {
		write(c.t, c.root, name, body)
	}
	return nil
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLinuxRead(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	pc := config.Item{Name: "PC Linux", Mode: config.ModeMeasured, Source: "linux"}
	tests := []struct {
		name     string
		item     config.Item
		before   map[string]string
		after    map[string]string // applied during the first-read window
		gpuW     float64
		cpuW     float64
		util     float64
		watts    float64
		measured []string
		est      []string
		missing  []string
		err      string
	}{
		{
			name: "RAPL measured",
			item: pc,
			before: map[string]string{
				gpuDir + "/power1_input":         "48000000\n",
				raplDir + "/energy_uj":           "1000000\n",
				raplDir + "/max_energy_range_uj": raplMax + "\n",
				"proc/stat":                      statLine(100, 900),
			},
			after: map[string]string{
				raplDir + "/energy_uj": "41000000\n",
				"proc/stat":            statLine(350, 1650),
			},
			gpuW: 48, cpuW: 40, util: 0.25, watts: (48 + 40 + 35) / 0.85,
			measured: []string{"gpu", "cpu"}, est: []string{"rest", "psu"},
		},
		{
			name: "RAPL wraps at max_energy_range_uj",
			item: pc,
			before: map[string]string{
				gpuDir + "/power1_input":         "48000000",
				raplDir + "/energy_uj":           "262133328850",
				raplDir + "/max_energy_range_uj": raplMax,
				"proc/stat":                      statLine(100, 900),
			},
			after: map[string]string{
				raplDir + "/energy_uj": "30000000",
				"proc/stat":            statLine(100, 1900),
			},
			gpuW: 48, cpuW: 40, util: 0, watts: (48 + 40 + 35) / 0.85,
			measured: []string{"gpu", "cpu"}, est: []string{"rest", "psu"},
		},
		{
			name: "RAPL missing falls back to /proc/stat",
			item: pc,
			before: map[string]string{
				gpuDir + "/power1_input": "48000000",
				"proc/stat":              statLine(100, 900),
			},
			after: map[string]string{"proc/stat": statLine(350, 1650)},
			gpuW:  48, cpuW: 15 + 90*0.25, util: 0.25, watts: (48 + 15 + 90*0.25 + 35) / 0.85,
			measured: []string{"gpu"}, est: []string{"cpu", "rest", "psu"},
		},
		{
			name: "power1_average wins over power1_input, cards add up",
			item: config.Item{Name: "PC", Mode: config.ModeMeasured, Source: "linux", RestW: 20, PSUEfficiency: 0.9},
			before: map[string]string{
				gpuDir + "/power1_average":       "30000000",
				gpuDir + "/power1_input":         "99000000",
				gpu2Dir + "/power1_input":        "5000000",
				raplDir + "/energy_uj":           "0",
				raplDir + "/max_energy_range_uj": raplMax,
				"proc/stat":                      statLine(0, 1000),
			},
			after: map[string]string{
				raplDir + "/energy_uj": "10000000",
				"proc/stat":            statLine(1000, 1000),
			},
			gpuW: 35, cpuW: 10, util: 1, watts: (35 + 10 + 20) / 0.9,
			measured: []string{"gpu", "cpu"}, est: []string{"rest", "psu"},
		},
		{
			name: "no GPU hwmon counts 0 W and says so",
			item: pc,
			before: map[string]string{
				raplDir + "/energy_uj":           "0",
				raplDir + "/max_energy_range_uj": raplMax,
				"proc/stat":                      statLine(0, 1000),
			},
			after: map[string]string{
				raplDir + "/energy_uj": "20000000",
				"proc/stat":            statLine(0, 2000),
			},
			gpuW: 0, cpuW: 20, util: 0, watts: (0 + 20 + 35) / 0.85,
			measured: []string{"cpu"}, est: []string{"rest", "psu"}, missing: []string{"gpu"},
		},
		{
			name: "RAPL without /proc/stat leaves util unknown",
			item: pc,
			before: map[string]string{
				gpuDir + "/power1_input":         "48000000",
				raplDir + "/energy_uj":           "0",
				raplDir + "/max_energy_range_uj": raplMax,
			},
			after: map[string]string{raplDir + "/energy_uj": "40000000"},
			gpuW:  48, cpuW: 40, util: -1, watts: (48 + 40 + 35) / 0.85,
			measured: []string{"gpu", "cpu"}, est: []string{"rest", "psu"},
		},
		{
			name:   "no CPU source at all",
			item:   pc,
			before: map[string]string{gpuDir + "/power1_input": "48000000"},
			err:    "cpu: RAPL and /proc/stat are unreadable",
		},
		{
			name: "garbage GPU value",
			item: pc,
			before: map[string]string{
				gpuDir + "/power1_input": "n/a",
				"proc/stat":              statLine(0, 1000),
			},
			after: map[string]string{"proc/stat": statLine(0, 2000)},
			err:   "power1_input",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tree(t, tt.before)
			clk := &fakeClock{t: t, root: root, now: t0, next: tt.after}
			l := &Linux{FS: os.DirFS(root), Item: tt.item, Now: clk.Now, Sleep: clk.Sleep}
			got, err := l.Read(context.Background())
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(clk.slept, []time.Duration{time.Second}) {
				t.Errorf("slept %v, want one 1s window", clk.slept)
			}
			p := got.Parts
			if tt.util < 0 {
				if p.CPUUtil != nil {
					t.Errorf("cpu_util = %v, want unknown (nil)", *p.CPUUtil)
				}
				p.CPUUtil = ptr(-1.0)
			}
			if got.Source != "linux" || !got.At.Equal(t0.Add(time.Second)) || !near(got.Watts, tt.watts) ||
				!near(*p.GPUW, tt.gpuW) || !near(*p.CPUW, tt.cpuW) || !near(*p.CPUUtil, tt.util) {
				t.Errorf("Read = %+v (gpu %v cpu %v util %v), want watts %v gpu %v cpu %v util %v",
					got, *p.GPUW, *p.CPUW, *p.CPUUtil, tt.watts, tt.gpuW, tt.cpuW, tt.util)
			}
			rest, eff := tt.item.RestW, tt.item.PSUEfficiency
			if rest == 0 {
				rest, eff = config.DefaultRestW, config.DefaultPSUEfficiency
			}
			if *p.RestW != rest || *p.PSUEfficiency != eff {
				t.Errorf("rest %v psu %v, want %v %v", *p.RestW, *p.PSUEfficiency, rest, eff)
			}
			if !reflect.DeepEqual(p.Measured, tt.measured) || !reflect.DeepEqual(p.Estimated, tt.est) || !reflect.DeepEqual(p.Missing, tt.missing) {
				t.Errorf("measured %q estimated %q missing %q, want %q %q %q", p.Measured, p.Estimated, p.Missing, tt.measured, tt.est, tt.missing)
			}
			if p.ExternalConnected != nil || p.ChargerW != nil {
				t.Errorf("linux reading has mac parts: %+v", p)
			}
		})
	}
}

func TestLinuxUnreadableRAPL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 files")
	}
	root := tree(t, map[string]string{
		raplDir + "/energy_uj":           "0",
		raplDir + "/max_energy_range_uj": raplMax,
		"proc/stat":                      statLine(0, 1000),
	})
	if err := os.Chmod(filepath.Join(root, raplDir, "energy_uj"), 0); err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: t, root: root, now: time.Unix(0, 0), next: map[string]string{"proc/stat": statLine(500, 1500)}}
	l := &Linux{FS: os.DirFS(root), Now: clk.Now, Sleep: clk.Sleep}
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !near(*got.Parts.CPUW, 15+90*0.5) || !reflect.DeepEqual(got.Parts.Estimated, []string{"cpu", "rest", "psu"}) {
		t.Fatalf("Read = cpu %v, estimated %q", *got.Parts.CPUW, got.Parts.Estimated)
	}
}

// After the first read, each read is the average since the previous one,
// with no extra sleep (the daemon's interval is the window).
func TestLinuxReadSincePrevious(t *testing.T) {
	root := tree(t, map[string]string{
		raplDir + "/energy_uj":           "0",
		raplDir + "/max_energy_range_uj": raplMax,
		"proc/stat":                      statLine(0, 1000),
	})
	clk := &fakeClock{t: t, root: root, now: time.Unix(0, 0), next: map[string]string{
		raplDir + "/energy_uj": "50000000",
		"proc/stat":            statLine(0, 2000),
	}}
	l := &Linux{FS: os.DirFS(root), Now: clk.Now, Sleep: clk.Sleep}
	if _, err := l.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.now = clk.now.Add(60 * time.Second)
	write(t, root, raplDir+"/energy_uj", "650000000") // +600 J over 60 s
	write(t, root, "proc/stat", statLine(3000, 5000))
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clk.slept) != 1 || !near(*got.Parts.CPUW, 10) || !near(*got.Parts.CPUUtil, 0.5) {
		t.Fatalf("second read cpu %v util %v, slept %v", *got.Parts.CPUW, *got.Parts.CPUUtil, clk.slept)
	}
	if (&Linux{}).Source() != "linux" {
		t.Error("Linux.Source != linux")
	}
}

func TestParseStat(t *testing.T) {
	tests := []struct {
		in          string
		busy, total uint64
		err         bool
	}{
		// user nice system idle iowait irq softirq steal (guest is inside user)
		{"cpu  10 1 4 80 5 0 0 0 3 0\n", 15, 100, false},
		{"cpu  1963431 5114 643859 42623086 3078464 57259 21690 0 0 0\n", 1963431 + 5114 + 643859 + 57259 + 21690, 1963431 + 5114 + 643859 + 42623086 + 3078464 + 57259 + 21690, false},
		{"cpu0 1 2 3 4\n", 0, 0, true},
		{"cpu  1 2 x 4\n", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, tt := range tests {
		s, err := parseStat([]byte(tt.in))
		if (err != nil) != tt.err || s.busy != tt.busy || s.total != tt.total {
			t.Errorf("parseStat(%q) = %+v, %v", tt.in, s, err)
		}
	}
}
