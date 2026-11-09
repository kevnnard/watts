package sensor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
)

// sysfs and procfs paths, relative to the Linux FS root.
const (
	gpuGlob     = "sys/class/drm/card*/device/hwmon/hwmon*/power1_%s"
	raplEnergy  = "sys/class/powercap/intel-rapl:0/energy_uj"
	raplMaxPath = "sys/class/powercap/intel-rapl:0/max_energy_range_uj"
	procStat    = "proc/stat"
)

// CPU fallback when RAPL is unreadable: idle watts + span × utilisation.
const (
	CPUIdleW = 15.0
	CPUSpanW = 90.0
)

// DefaultWindow is how long the first read waits to get CPU deltas.
const DefaultWindow = time.Second

// Linux reads the GPU (hwmon power1_average or power1_input, µW), the CPU
// (RAPL energy_uj deltas, or the /proc/stat estimate) and reports whole-PC
// watts as Item.WholePCWatts(gpu, cpu). The first Read waits Window to get
// a delta; later reads average over the time since the previous read, so
// the daemon's samples cover its whole interval. Not safe for concurrent use.
type Linux struct {
	FS     fs.FS       // filesystem root; nil means os.DirFS("/")
	Item   config.Item // the measured linux item (rest_w, psu_efficiency)
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error // nil means a timer
	Window time.Duration                              // 0 means DefaultWindow

	prev *counters
}

// Source is config.SourceLinux.
func (l *Linux) Source() string { return config.SourceLinux }

type counters struct {
	at       time.Time
	energyUJ uint64
	maxUJ    uint64
	raplOK   bool
	stat     cpuStat
	statOK   bool
}

type cpuStat struct{ busy, total uint64 }

func (l *Linux) fsys() fs.FS {
	if l.FS == nil {
		return os.DirFS("/")
	}
	return l.FS
}

// Read returns the current whole-PC reading.
func (l *Linux) Read(ctx context.Context) (Reading, error) {
	fsys := l.fsys()
	if l.prev == nil {
		first := l.counters(fsys)
		w := l.Window
		if w <= 0 {
			w = DefaultWindow
		}
		if err := l.sleep(ctx, w); err != nil {
			return Reading{}, err
		}
		l.prev = &first
	}
	cur := l.counters(fsys)
	prev := *l.prev
	l.prev = &cur

	var p Parts
	util, utilOK := utilisation(prev, cur)
	cpuW, raplOK := raplWatts(prev, cur)
	switch {
	case raplOK:
		p.Measured = append(p.Measured, "cpu")
	case utilOK:
		cpuW = CPUIdleW + CPUSpanW*util
		p.Estimated = append(p.Estimated, "cpu")
	default:
		return Reading{}, errors.New("linux sensor: cpu: RAPL and /proc/stat are unreadable")
	}
	gpuW, found, err := readGPU(fsys)
	if err != nil {
		return Reading{}, fmt.Errorf("linux sensor: %w", err)
	}
	if found {
		p.Measured = append([]string{"gpu"}, p.Measured...)
	} else {
		p.Missing = []string{"gpu"}
	}
	p.Estimated = append(p.Estimated, "rest", "psu")
	it := l.Item
	rest, eff := it.RestW, it.PSUEfficiency
	if rest == 0 {
		rest = config.DefaultRestW
	}
	if eff == 0 {
		eff = config.DefaultPSUEfficiency
	}
	p.GPUW, p.CPUW = ptr(gpuW), ptr(cpuW)
	if utilOK {
		p.CPUUtil = ptr(util)
	}
	p.RestW, p.PSUEfficiency = ptr(rest), ptr(eff)
	return Reading{Source: config.SourceLinux, At: cur.at, Watts: it.WholePCWatts(gpuW, cpuW), Parts: p}, nil
}

func (l *Linux) sleep(ctx context.Context, d time.Duration) error {
	if l.Sleep != nil {
		return l.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// counters reads RAPL and /proc/stat; unreadable ones are marked not OK.
func (l *Linux) counters(fsys fs.FS) counters {
	c := counters{at: now(l.Now)}
	e, err1 := readUint(fsys, raplEnergy)
	m, err2 := readUint(fsys, raplMaxPath)
	if err1 == nil && err2 == nil && m > 0 {
		c.energyUJ, c.maxUJ, c.raplOK = e, m, true
	}
	if b, err := fs.ReadFile(fsys, procStat); err == nil {
		if s, err := parseStat(b); err == nil {
			c.stat, c.statOK = s, true
		}
	}
	return c
}

// raplWatts is the CPU package power between two reads, handling one
// counter wrap at max_energy_range_uj.
func raplWatts(a, b counters) (float64, bool) {
	dt := b.at.Sub(a.at).Seconds()
	if !a.raplOK || !b.raplOK || dt <= 0 {
		return 0, false
	}
	d := b.energyUJ - a.energyUJ
	if b.energyUJ < a.energyUJ {
		d = b.maxUJ - a.energyUJ + b.energyUJ
	}
	return float64(d) / 1e6 / dt, true
}

// utilisation is the busy share of CPU time between two reads (0–1).
func utilisation(a, b counters) (float64, bool) {
	if !a.statOK || !b.statOK || b.stat.total <= a.stat.total || b.stat.busy < a.stat.busy {
		return 0, false
	}
	return float64(b.stat.busy-a.stat.busy) / float64(b.stat.total-a.stat.total), true
}

// parseStat reads the aggregate "cpu" line of /proc/stat. Busy is user,
// nice, system, irq, softirq and steal; idle and iowait are idle. guest
// time is already inside user.
func parseStat(b []byte) (cpuStat, error) {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return cpuStat{}, errors.New("proc/stat: no aggregate cpu line")
	}
	var v [8]uint64
	for i := 0; i < 8 && i+1 < len(f); i++ {
		n, err := strconv.ParseUint(f[i+1], 10, 64)
		if err != nil {
			return cpuStat{}, fmt.Errorf("proc/stat: %w", err)
		}
		v[i] = n
	}
	busy := v[0] + v[1] + v[2] + v[5] + v[6] + v[7]
	return cpuStat{busy: busy, total: busy + v[3] + v[4]}, nil
}

// readGPU sums the GPU power of every hwmon directory, preferring
// power1_average over power1_input in the same directory.
func readGPU(fsys fs.FS) (watts float64, found bool, err error) {
	files := map[string]string{} // hwmon dir → file
	for _, kind := range []string{"input", "average"} {
		ms, _ := fs.Glob(fsys, fmt.Sprintf(gpuGlob, kind))
		for _, m := range ms {
			files[path.Dir(m)] = m // average overwrites input
		}
	}
	dirs := make([]string, 0, len(files))
	for d := range files {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		uw, err := readUint(fsys, files[d])
		if err != nil {
			return 0, false, fmt.Errorf("gpu: %w", err)
		}
		watts += float64(uw) / 1e6
		found = true
	}
	return watts, found, nil
}

func readUint(fsys fs.FS, name string) (uint64, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}
