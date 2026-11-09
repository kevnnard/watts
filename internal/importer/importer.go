// Package importer imports the old consumo daily ledgers (S12) from
// ~/.local/share/consumo/YYYY-MM-DD as store daily aggregates. Each file
// holds one line; its field count says which machine wrote it:
//
//	mac:   mac_wh monitor_s
//	linux: gpu_wh cpu_wh on_s monitor_s
//
// The Mac ledger has no on time, so monitor_s becomes OnSeconds (it drives
// the follows items). Linux Wh is the whole PC: (gpu + cpu) / psu +
// rest × on_s / 3600 / psu. Source files are only read. Aggregates are
// upserted by (day, source), so re-running replaces instead of adding.
package importer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
)

// Origin marks imported aggregates.
const Origin = "consumo"

// DefaultDir is ~/.local/share/consumo, where the consumo loggers write.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "consumo")
}

// Options are the whole-PC estimates for Linux ledgers and today's date
// (YYYY-MM-DD) for the partial-day note.
type Options struct {
	RestW         float64
	PSUEfficiency float64
	Today         string
}

// Store is the part of store.Store the importer uses.
type Store interface {
	UpsertDaily(store.DailyAggregate) error
	Daily(fromDay, toDay string) ([]store.DailyAggregate, error)
}

// Parse reads one ledger file for day.
func Parse(day string, data []byte, restW, psu float64) (store.DailyAggregate, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return store.DailyAggregate{}, fmt.Errorf("%s: empty file", day)
	}
	if n := strings.Count(text, "\n") + 1; n != 1 {
		return store.DailyAggregate{}, fmt.Errorf("%s: %d lines, want 1", day, n)
	}
	fields := strings.Fields(text)
	if len(fields) != 2 && len(fields) != 4 {
		return store.DailyAggregate{}, fmt.Errorf("%s: %d fields, want 2 (mac_wh monitor_s) or 4 (gpu_wh cpu_wh on_s monitor_s)", day, len(fields))
	}
	v := make([]float64, len(fields))
	for i, f := range fields {
		x, err := strconv.ParseFloat(f, 64)
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
			return store.DailyAggregate{}, fmt.Errorf("%s: field %d %q is not a number", day, i+1, f)
		}
		if x < 0 {
			return store.DailyAggregate{}, fmt.Errorf("%s: field %d %q is negative", day, i+1, f)
		}
		v[i] = x
	}
	a := store.DailyAggregate{Day: day, Origin: Origin}
	if len(v) == 2 {
		a.Source, a.Wh, a.OnSeconds, a.MonitorSeconds = config.SourceMac, v[0], v[1], v[1]
		return a, nil
	}
	gpu, cpu, on, mon := v[0], v[1], v[2], v[3]
	a.Source = config.SourceLinux
	a.Wh = (gpu+cpu)/psu + restW*on/3600/psu
	a.OnSeconds, a.MonitorSeconds, a.GPUWh, a.CPUWh = on, mon, gpu, cpu
	return a, nil
}

// Scan parses every YYYY-MM-DD file in dir, in day order. skipped lists the
// other entries' names. It writes nothing.
func Scan(dir string, restW, psu float64) (days []store.DailyAggregate, skipped []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return nil, nil, fmt.Errorf("open %s: %w", dir, pe.Err)
		}
		return nil, nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if _, perr := time.Parse(store.DayLayout, name); perr != nil || !e.Type().IsRegular() {
			if !e.IsDir() {
				skipped = append(skipped, name)
			}
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		a, err := Parse(name, b, restW, psu)
		if err != nil {
			return nil, nil, err
		}
		days = append(days, a)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })
	return days, skipped, nil
}

// Run imports dir into st and prints a summary to w. On any bad file it
// writes nothing to st and prints nothing.
func Run(dir string, o Options, st Store, w io.Writer) error {
	if o.PSUEfficiency <= 0 || o.PSUEfficiency > 1 {
		return fmt.Errorf("import-consumo: psu_efficiency must be greater than 0 and at most 1, got %g", o.PSUEfficiency)
	}
	if o.RestW < 0 {
		return fmt.Errorf("import-consumo: rest_w must not be negative, got %g", o.RestW)
	}
	days, skipped, err := Scan(dir, o.RestW, o.PSUEfficiency)
	if err != nil {
		return fmt.Errorf("import-consumo: %w", err)
	}
	existing := map[string]bool{}
	if len(days) > 0 {
		old, err := st.Daily(days[0].Day, days[len(days)-1].Day)
		if err != nil {
			return fmt.Errorf("import-consumo: %w", err)
		}
		for _, a := range old {
			existing[a.Day+"/"+a.Source] = true
		}
	}
	var lines strings.Builder
	counts := map[string]int{}
	fresh, replaced := 0, 0
	for _, a := range days {
		state := "new"
		if existing[a.Day+"/"+a.Source] {
			state = "replaced"
			replaced++
		} else {
			fresh++
		}
		if err := st.UpsertDaily(a); err != nil {
			return fmt.Errorf("import-consumo: %s: %w", a.Day, err)
		}
		counts[a.Source]++
		on := time.Duration(a.OnSeconds) * time.Second
		fmt.Fprintf(&lines, "%s  %-5s  %9.3f Wh  on %dh%02dm  %s\n", a.Day, a.Source, a.Wh, int(on.Hours()), int(on.Minutes())%60, state)
	}
	io.WriteString(w, lines.String())
	unit := "days"
	if len(days) == 1 {
		unit = "day"
	}
	fmt.Fprintf(w, "imported %d %s from %s (mac %d, linux %d): %d new, %d replaced\n",
		len(days), unit, dir, counts[config.SourceMac], counts[config.SourceLinux], fresh, replaced)
	if len(skipped) > 0 {
		unit := "files"
		if len(skipped) == 1 {
			unit = "file"
		}
		fmt.Fprintf(w, "skipped %d %s not named YYYY-MM-DD: %s\n", len(skipped), unit, strings.Join(skipped, ", "))
	}
	for _, a := range days {
		if a.Day == o.Today {
			fmt.Fprintf(w, "note: %s is today; while it is imported, its aggregate replaces today's daemon samples. Re-run after midnight to get the full day.\n", a.Day)
			break
		}
	}
	return nil
}
