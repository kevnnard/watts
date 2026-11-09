package tui

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
)

// Source is the state of one sensor source ("mac" or "linux") at refresh.
type Source struct {
	Name   string    // config.SourceMac or config.SourceLinux
	Online bool      // false: show its items as offline, never as 0 W
	LastTS time.Time // newest sample, zero when none
	Via    string    // where it is read: "this machine", "ssh kevnnard-station"
	Reads  string    // what it reads: "ioreg SystemPowerIn (charger)"
	Detail string    // why it is offline: "connection timed out"
	// Parts is an optional live breakdown of the whole-device watts (for the
	// Linux PC: CPU and GPU). Without it the dashboard derives CPU + GPU from
	// the whole-PC watts and the item's rest and PSU settings.
	Parts []Part
}

// Part is one measured component of a source.
type Part struct {
	Name  string // "CPU", "GPU"
	Watts float64
	Reads string // "RAPL energy counter"
}

// Trend is the recent history the sparklines draw. Values are average watts
// per minute, oldest first; NaN means unknown (source offline).
type Trend struct {
	Start time.Time            // first minute of Total and Items
	Total []float64            // whole house, known sources only
	Items map[string][]float64 // by item name, and PartKey for a Source part
	// Partial marks minutes in which a measured source was unknown, so the
	// total there is low.
	Partial []bool
	// Hours is kWh per local hour today, from midnight to the current
	// (partial) hour.
	Hours []float64
	// Since is the last toggle of each manual item.
	Since map[string]time.Time
}

// PartKey is the Trend.Items key of one part of a measured item, e.g.
// "PC Linux/CPU".
func PartKey(item, part string) string { return item + "/" + part }

// TrendMinutes is the length of the "Last 3 h" sparklines.
const TrendMinutes = 180

// Data feeds the dashboard. Refresh is called from a command every
// RefreshInterval and after every edit, never from View. cfg and h are the
// model's current config and tariff history.
type Data interface {
	Refresh(ctx context.Context, cfg *config.Config, h tariff.History, now time.Time) (energy.Report, []Source, error)
}

// TrendData is a Data that also returns the sparkline history from the same
// read. The model uses it when the Data implements it; otherwise the
// sparklines show "no history".
type TrendData interface {
	Data
	RefreshTrend(ctx context.Context, cfg *config.Config, h tariff.History, now time.Time) (energy.Report, []Source, Trend, error)
}

// Editor persists the edits made in the dashboard. The model validates
// first (config.Config.Validate, tariff.History.Validate), so a returned
// error is an I/O failure.
type Editor interface {
	SaveConfig(cfg *config.Config) error
	SaveTariffs(h tariff.History) error
	SetManual(item string, on bool, at time.Time) error
}

// Loader loads the energy input for [from, to] plus the energy margin and
// reports the sources. The local one reads the store; a remote-aware one
// merges the Linux export (remote.LoadInput) and marks it offline on failure.
type Loader func(ctx context.Context, cfg *config.Config, from, to time.Time) (energy.Input, []Source, error)

// Local is the Data and Editor over the local files: the config file, the
// tariff file and the SQLite store.
type Local struct {
	ConfigPath string
	TariffPath string
	Store      *store.Store
	// Load replaces the store-only loader, e.g. to merge the remote export.
	Load Loader
	// Loc is the local time zone; nil means time.Local.
	Loc *time.Location
}

// NewLocal returns the local Data and Editor.
func NewLocal(st *store.Store, configPath, tariffPath string) *Local {
	return &Local{ConfigPath: configPath, TariffPath: tariffPath, Store: st}
}

// StoreLoader is the store-only Loader with sources from DefaultSources.
func StoreLoader(st *store.Store, loc *time.Location) Loader {
	return func(_ context.Context, cfg *config.Config, from, to time.Time) (energy.Input, []Source, error) {
		in := energy.Input{Items: cfg.Items, Interval: cfg.Interval(), Loc: loc}
		d, err := st.Load(from, to, in.Margin())
		if err != nil {
			return energy.Input{}, nil, err
		}
		in.Data = d
		return in, DefaultSources(cfg, in, to), nil
	}
}

// Refresh implements Data.
func (l *Local) Refresh(ctx context.Context, cfg *config.Config, h tariff.History, now time.Time) (energy.Report, []Source, error) {
	r, src, _, err := l.RefreshTrend(ctx, cfg, h, now)
	return r, src, err
}

// RefreshTrend implements TrendData.
func (l *Local) RefreshTrend(ctx context.Context, cfg *config.Config, h tariff.History, now time.Time) (energy.Report, []Source, Trend, error) {
	load := l.Load
	if load == nil {
		load = StoreLoader(l.Store, l.Loc)
	}
	in, src, err := load(ctx, cfg, LoadFrom(h, now, l.Loc), now)
	if err != nil {
		return energy.Report{}, nil, Trend{}, err
	}
	if in.Loc == nil {
		in.Loc = l.Loc
	}
	return energy.Build(in, h, now), src, BuildTrend(in, now), nil
}

// LoadFrom is the earliest time a refresh needs: the billing period start,
// local midnight or the start of the trend window, whichever is first.
func LoadFrom(h tariff.History, now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	from := h.Period(now).Start
	if m := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc); m.Before(from) {
		from = m
	}
	if t := now.Add(-TrendMinutes * time.Minute); t.Before(from) {
		from = t
	}
	return from
}

// SaveConfig implements Editor.
func (l *Local) SaveConfig(cfg *config.Config) error { return config.Save(l.ConfigPath, cfg) }

// SaveTariffs implements Editor.
func (l *Local) SaveTariffs(h tariff.History) error { return tariff.Save(l.TariffPath, h) }

// SetManual implements Editor.
func (l *Local) SetManual(item string, on bool, at time.Time) error {
	return l.Store.AddToggle(store.Toggle{Item: item, TS: at, On: on})
}

// DefaultSources reports each measured source in cfg from its newest sample:
// online while that sample is fresh (energy's offline rule).
func DefaultSources(cfg *config.Config, in energy.Input, now time.Time) []Source {
	var out []Source
	seen := map[string]bool{}
	for _, it := range cfg.Items {
		if it.Mode != config.ModeMeasured || seen[it.Source] {
			continue
		}
		seen[it.Source] = true
		n := energy.Now(in, it, now)
		s := Source{Name: it.Source, Online: !n.Offline, LastTS: n.LastTS, Reads: defaultReads(it.Source)}
		if it.Source == config.SourceLinux {
			s.Via = "ssh " + cfg.Remote.Host
		} else {
			s.Via = "this machine"
		}
		if n.Offline {
			s.Detail = "no recent sample"
		}
		out = append(out, s)
	}
	return out
}

func defaultReads(source string) string {
	switch source {
	case config.SourceMac:
		return "ioreg SystemPowerIn (charger)"
	case config.SourceLinux:
		return "RAPL + GPU hwmon"
	}
	return ""
}

// BuildTrend computes the sparkline history from in at now.
func BuildTrend(in energy.Input, now time.Time) Trend {
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	end := now.Truncate(time.Minute)
	start := end.Add(-(TrendMinutes - 1) * time.Minute)
	recent := clip(in, start.Add(-in.Margin()-time.Hour), now)
	t := Trend{Start: start, Items: map[string][]float64{}, Since: map[string]time.Time{}}
	t.Total = make([]float64, TrendMinutes)
	t.Partial = make([]bool, TrendMinutes)
	iv := in.Interval
	if iv <= 0 {
		iv = config.DefaultInterval * time.Second
	}
	last := map[string]time.Time{}
	for src, ss := range in.Data.Samples {
		for _, s := range ss {
			if !s.TS.After(now) && s.TS.After(last[src]) {
				last[src] = s.TS
			}
		}
	}
	for _, it := range in.Items {
		vals := make([]float64, TrendMinutes)
		for i := range vals {
			a := start.Add(time.Duration(i) * time.Minute)
			b := a.Add(time.Minute)
			if b.After(now) {
				b = now
			}
			if it.Mode == config.ModeMeasured && (last[it.Source].IsZero() || a.After(last[it.Source].Add(iv))) {
				vals[i] = math.NaN()
				t.Partial[i] = true
				continue
			}
			if !b.After(a) {
				vals[i] = math.NaN()
				continue
			}
			r := energy.ItemEnergy(recent, it, a, b)
			vals[i] = r.Wh * 3600 / b.Sub(a).Seconds()
			t.Total[i] += vals[i]
		}
		t.Items[it.Name] = vals
		if it.Mode == config.ModeManual {
			if tg := in.Data.Toggles[it.Name]; len(tg) > 0 {
				ts := append([]store.Toggle(nil), tg...)
				sort.Slice(ts, func(a, b int) bool { return ts[a].TS.Before(ts[b].TS) })
				t.Since[it.Name] = ts[len(ts)-1].TS
			}
		}
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	today := clip(in, midnight.Add(-in.Margin()), now)
	for h := midnight; h.Before(now); h = h.Add(time.Hour) {
		b := h.Add(time.Hour)
		if b.After(now) {
			b = now
		}
		t.Hours = append(t.Hours, energy.Window(today, h, b, 0).KWh())
	}
	return t
}

// clip keeps the samples and display readings near [from, to] so the
// per-minute integration stays cheap. Aggregates and toggles are kept whole.
func clip(in energy.Input, from, to time.Time) energy.Input {
	out := in
	out.Data.Samples = map[string][]store.Sample{}
	limit := to.Add(in.Margin())
	for src, ss := range in.Data.Samples {
		var keep []store.Sample
		for _, s := range ss {
			if s.TS.After(from) && !s.TS.After(limit) {
				keep = append(keep, s)
			}
		}
		out.Data.Samples[src] = keep
	}
	out.Data.Displays = nil
	for _, x := range in.Data.Displays {
		if x.TS.After(from.Add(-in.Margin())) && !x.TS.After(to) {
			out.Data.Displays = append(out.Data.Displays, x)
		}
	}
	return out
}
