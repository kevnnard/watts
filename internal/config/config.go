// Package config loads, validates and saves the user configuration at
// $XDG_CONFIG_HOME/watts/config.toml (default ~/.config/watts/config.toml).
//
// The config holds the consumption items (S2), the currency choice (S5), the
// remote host for the Linux PC (S9) and the daemon sample interval (S7).
// Tariff history is data, not configuration; it lives in the data dir (see
// TariffPath and package tariff).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kevnnard/watts/internal/catalog"
)

// Mode says how an item's energy is obtained.
type Mode string

const (
	ModeMeasured Mode = "measured" // real watts from a sensor source
	ModeFollows  Mode = "follows"  // fixed watts while any followed measured item is on
	ModeSchedule Mode = "schedule" // fixed watts during daily time ranges
	ModeManual   Mode = "manual"   // fixed watts while toggled on in the TUI
)

// Sensor sources for measured items.
const (
	SourceMac   = "mac"
	SourceLinux = "linux"
)

// Defaults.
const (
	DefaultRemoteHost = "kevnnard-station"
	// DefaultRemoteCommand is the watts binary on the remote host. It is a
	// full path because a non-interactive SSH shell does not put ~/.local/bin
	// on PATH, and on Arch /usr/bin/usage is an unrelated package (jdx/usage).
	DefaultRemoteCommand  = "~/.local/bin/watts"
	DefaultInterval       = 60 // seconds
	DefaultCurrency       = "COP"
	DefaultRestW          = 35.0
	DefaultPSUEfficiency  = 0.85
	defaultMonitorWattsLG = 22.0 // fallback if the catalog entry ever disappears
)

// Item is one consumption item.
type Item struct {
	Name     string   `toml:"name"`
	Mode     Mode     `toml:"mode"`
	Source   string   `toml:"source,omitempty"`   // measured: "mac" or "linux"
	Watts    float64  `toml:"watts,omitempty"`    // follows, schedule, manual: watts per unit
	Quantity int      `toml:"quantity,omitempty"` // units, default 1 (ignored for measured)
	Brand    string   `toml:"brand,omitempty"`
	Model    string   `toml:"model,omitempty"`
	Follows  []string `toml:"follows,omitempty"`  // follows: names of measured items
	Schedule []string `toml:"schedule,omitempty"` // schedule: "HH:MM-HH:MM" ranges, may cross midnight
	// Measured linux only: whole PC = (GPU + CPU + rest_w) / psu_efficiency.
	// Zero or omitted means the default (35 W and 0.85).
	RestW         float64 `toml:"rest_w,omitempty"`
	PSUEfficiency float64 `toml:"psu_efficiency,omitempty"`
	// Follows only (S19): ddc names the source whose DDC brightness and power
	// mode readings drive this item ("linux"). While a reading covers the
	// time, watts per unit are min_w + (max_w − min_w) × brightness while the
	// monitor is on, and standby_w while it sleeps. Zero means the default
	// (see DDCWatts).
	DDC      string  `toml:"ddc,omitempty"`
	MinW     float64 `toml:"min_w,omitzero"`
	MaxW     float64 `toml:"max_w,omitzero"`
	StandbyW float64 `toml:"standby_w,omitzero"`
}

// DDC defaults when neither the item nor its catalog entry sets them:
// min_w = DefaultDDCMinShare × watts, max_w = watts, standby_w = DefaultStandbyW.
const (
	DefaultDDCMinShare = 0.3
	DefaultStandbyW    = 0.3
)

// DDCWatts is the per-unit DDC estimate range of a ddc item: each zero field
// takes the catalog entry's value when brand and model match one that has
// it, else the default (0.3 × watts, watts, 0.3 W).
func (it Item) DDCWatts() (minW, maxW, standbyW float64) {
	minW, maxW, standbyW = it.MinW, it.MaxW, it.StandbyW
	var e catalog.Entry
	if it.Brand != "" || it.Model != "" {
		e, _ = catalog.Find(it.Brand, it.Model)
	}
	if minW == 0 {
		minW = e.MinW
	}
	if maxW == 0 {
		maxW = e.MaxW
	}
	if standbyW == 0 {
		standbyW = e.StandbyW
	}
	if minW == 0 {
		minW = DefaultDDCMinShare * it.Watts
	}
	if maxW == 0 {
		maxW = it.Watts
	}
	if standbyW == 0 {
		standbyW = DefaultStandbyW
	}
	return minW, maxW, standbyW
}

// TotalWatts is watts × quantity for fixed-watt items.
func (it Item) TotalWatts() float64 {
	q := it.Quantity
	if q < 1 {
		q = 1
	}
	return it.Watts * float64(q)
}

// WholePCWatts applies the S8 whole-PC estimate for a measured linux item.
func (it Item) WholePCWatts(gpuW, cpuW float64) float64 {
	rest, eff := it.RestW, it.PSUEfficiency
	if rest == 0 {
		rest = DefaultRestW
	}
	if eff == 0 {
		eff = DefaultPSUEfficiency
	}
	return (gpuW + cpuW + rest) / eff
}

// Ranges parses the item's schedule. It is valid after Validate succeeds.
func (it Item) Ranges() ([]Range, error) {
	out := make([]Range, 0, len(it.Schedule))
	for _, s := range it.Schedule {
		r, err := ParseRange(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Currency is the main currency and an optional secondary one.
// Rate is how many main-currency units buy one secondary unit
// (e.g. 4000 when main is COP and secondary is USD).
type Currency struct {
	Main      string  `toml:"main"`
	Secondary string  `toml:"secondary,omitempty"`
	Rate      float64 `toml:"rate,omitempty"`
}

// Remote is the SSH host that runs the Linux daemon, and the watts binary
// to run there.
type Remote struct {
	Host    string `toml:"host"`
	Command string `toml:"command,omitempty"` // "" means DefaultRemoteCommand
}

// Validate checks the remote command: one absolute or ~/ path, or a bare
// name, with no spaces or shell syntax, because SSH hands it to a shell.
func (r Remote) Validate() error {
	if r.Command == "" {
		return nil
	}
	for _, c := range r.Command {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/~._+-", c)) {
			return fmt.Errorf("remote: command %q must be one path without spaces or shell characters", r.Command)
		}
	}
	if strings.HasPrefix(r.Command, "~") && !strings.HasPrefix(r.Command, "~/") {
		return fmt.Errorf("remote: command %q must start with ~/, /, or a name", r.Command)
	}
	return nil
}

// Display is the Linux daemon's monitor over DDC (S19). It only matters on
// the machine that reads DDC, and only when an item sets ddc.
type Display struct {
	// Bus is the I2C bus of the monitor (/dev/i2c-N). 0 means detect it at
	// daemon start with `ddcutil detect --terse`.
	Bus int `toml:"bus,omitzero"`
}

// Config is the whole config file.
type Config struct {
	SampleIntervalSeconds int      `toml:"sample_interval_seconds"`
	Currency              Currency `toml:"currency"`
	Remote                Remote   `toml:"remote"`
	Display               Display  `toml:"display,omitempty"`
	Items                 []Item   `toml:"items"`
}

// UsesDDC reports whether an item is driven by DDC readings of source.
func (c *Config) UsesDDC(source string) bool {
	for _, it := range c.Items {
		if it.Mode == ModeFollows && it.DDC == source {
			return true
		}
	}
	return false
}

// Interval is the daemon sample interval.
func (c *Config) Interval() time.Duration {
	return time.Duration(c.SampleIntervalSeconds) * time.Second
}

// Item returns the item with the given name.
func (c *Config) Item(name string) (Item, bool) {
	for _, it := range c.Items {
		if it.Name == name {
			return it, true
		}
	}
	return Item{}, false
}

// Default is the first-run config: Mac and PC Linux measured, and the
// LG 29WQ600 following both.
func Default() *Config {
	lgW := defaultMonitorWattsLG
	if e, ok := catalog.Find("LG", "29WQ600"); ok {
		lgW = e.Watts
	}
	return &Config{
		SampleIntervalSeconds: DefaultInterval,
		Currency:              Currency{Main: DefaultCurrency},
		Remote:                Remote{Host: DefaultRemoteHost, Command: DefaultRemoteCommand},
		Items: []Item{
			{Name: "Mac", Mode: ModeMeasured, Source: SourceMac, Quantity: 1},
			{Name: "PC Linux", Mode: ModeMeasured, Source: SourceLinux, Quantity: 1, RestW: DefaultRestW, PSUEfficiency: DefaultPSUEfficiency},
			{Name: "LG 29WQ600", Mode: ModeFollows, Watts: lgW, Quantity: 1, Brand: "LG", Model: "29WQ600", Follows: []string{"Mac", "PC Linux"}},
		},
	}
}

// applyDefaults fills omitted fields.
func (c *Config) applyDefaults() {
	if c.SampleIntervalSeconds == 0 {
		c.SampleIntervalSeconds = DefaultInterval
	}
	if c.Currency.Main == "" {
		c.Currency.Main = DefaultCurrency
	}
	if c.Remote.Host == "" {
		c.Remote.Host = DefaultRemoteHost
	}
	if c.Remote.Command == "" {
		c.Remote.Command = DefaultRemoteCommand
	}
	for i := range c.Items {
		it := &c.Items[i]
		if it.Quantity == 0 {
			it.Quantity = 1
		}
		if it.Mode == ModeMeasured && it.Source == SourceLinux {
			if it.RestW == 0 {
				it.RestW = DefaultRestW
			}
			if it.PSUEfficiency == 0 {
				it.PSUEfficiency = DefaultPSUEfficiency
			}
		}
	}
}

// Validate returns the first problem found, or nil.
func (c *Config) Validate() error {
	if c.SampleIntervalSeconds <= 0 {
		return fmt.Errorf("sample_interval_seconds must be greater than 0, got %d", c.SampleIntervalSeconds)
	}
	if err := c.Currency.Validate(); err != nil {
		return err
	}
	if err := c.Remote.Validate(); err != nil {
		return err
	}
	if c.Display.Bus < 0 {
		return fmt.Errorf("display: bus must not be negative, got %d", c.Display.Bus)
	}
	byName := map[string]Item{}
	for _, it := range c.Items {
		byName[it.Name] = it
	}
	seen := map[string]bool{}
	for i, it := range c.Items {
		if strings.TrimSpace(it.Name) == "" {
			return fmt.Errorf("item %d: name is required", i+1)
		}
		key := strings.ToLower(strings.TrimSpace(it.Name))
		if seen[key] {
			return fmt.Errorf("item %q: duplicate name", it.Name)
		}
		seen[key] = true
		if err := it.validate(byName); err != nil {
			return fmt.Errorf("item %q: %w", it.Name, err)
		}
	}
	return nil
}

func (it Item) validate(byName map[string]Item) error {
	if it.Quantity < 0 {
		return fmt.Errorf("quantity must be at least 1, got %d", it.Quantity)
	}
	if err := it.validateDDC(); err != nil {
		return err
	}
	switch it.Mode {
	case ModeMeasured:
		if it.Source != SourceMac && it.Source != SourceLinux {
			return fmt.Errorf("source must be %q or %q, got %q", SourceMac, SourceLinux, it.Source)
		}
		if it.RestW < 0 {
			return fmt.Errorf("rest_w must not be negative, got %g", it.RestW)
		}
		if it.PSUEfficiency < 0 || it.PSUEfficiency > 1 {
			return fmt.Errorf("psu_efficiency must be greater than 0 and at most 1, got %g", it.PSUEfficiency)
		}
		return nil
	case ModeFollows, ModeSchedule, ModeManual:
	default:
		return fmt.Errorf("unknown mode %q (want measured, follows, schedule or manual)", it.Mode)
	}
	if it.Watts <= 0 {
		return errors.New("watts must be greater than 0")
	}
	if err := it.validateDDCWatts(); err != nil {
		return err
	}
	switch it.Mode {
	case ModeFollows:
		if len(it.Follows) == 0 {
			return errors.New("follows must list at least one measured item")
		}
		for _, name := range it.Follows {
			f, ok := byName[name]
			if !ok {
				return fmt.Errorf("follows unknown item %q", name)
			}
			if f.Mode != ModeMeasured {
				return fmt.Errorf("follows %q, which is not a measured item", name)
			}
		}
	case ModeSchedule:
		if len(it.Schedule) == 0 {
			return errors.New("schedule must have at least one range")
		}
		if _, err := it.Ranges(); err != nil {
			return err
		}
	}
	return nil
}

// validateDDC checks that ddc, min_w, max_w and standby_w go with a
// follows item; validateDDCWatts checks their values.
func (it Item) validateDDC() error {
	if it.DDC == "" {
		if it.MinW != 0 || it.MaxW != 0 || it.StandbyW != 0 {
			return fmt.Errorf("min_w, max_w and standby_w need ddc = %q", SourceLinux)
		}
		return nil
	}
	if it.Mode != ModeFollows {
		return fmt.Errorf("ddc is only for follows items, got mode %q", it.Mode)
	}
	if it.DDC != SourceLinux {
		return fmt.Errorf("ddc must be %q (the Linux daemon reads DDC), got %q", SourceLinux, it.DDC)
	}
	return nil
}

func (it Item) validateDDCWatts() error {
	if it.DDC == "" {
		return nil
	}
	for _, f := range []struct {
		key string
		v   float64
	}{{"min_w", it.MinW}, {"max_w", it.MaxW}, {"standby_w", it.StandbyW}} {
		if f.v < 0 {
			return fmt.Errorf("%s must not be negative, got %g", f.key, f.v)
		}
	}
	lo, hi, sb := it.DDCWatts()
	if !(sb <= lo && lo <= hi) {
		return fmt.Errorf("ddc watts must be 0 ≤ standby_w ≤ min_w ≤ max_w, got standby_w %g, min_w %g, max_w %g", sb, lo, hi)
	}
	return nil
}

// Validate checks the currency codes and rate.
func (cu Currency) Validate() error {
	if cu.Main == "" {
		return errors.New("currency: main code is required")
	}
	if !isCode(cu.Main) {
		return fmt.Errorf("currency: main code must be 3 uppercase letters, got %q", cu.Main)
	}
	if cu.Secondary == "" {
		return nil
	}
	if !isCode(cu.Secondary) {
		return fmt.Errorf("currency: secondary code must be 3 uppercase letters, got %q", cu.Secondary)
	}
	if cu.Secondary == cu.Main {
		return errors.New("currency: secondary code must differ from main")
	}
	if cu.Rate <= 0 {
		return errors.New("currency: rate must be greater than 0 when secondary is set")
	}
	return nil
}

func isCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// Load reads, defaults and validates the config at path. Unknown keys are
// errors so typos do not silently drop settings.
func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("config %s: unknown key %s", path, und[0].String())
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

// LoadOrCreate loads path, writing Default() there first if it does not exist.
// created reports whether the default was written.
func LoadOrCreate(path string) (c *Config, created bool, err error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := Save(path, Default()); err != nil {
			return nil, false, err
		}
		created = true
	} else if err != nil {
		return nil, false, fmt.Errorf("config %s: %w", path, err)
	}
	c, err = Load(path)
	return c, created, err
}

const header = `# watts config. Docs: watts --help.
# Item modes: measured (source = "mac" | "linux"), follows (watts while any
# listed measured item is on), schedule (watts during "HH:MM-HH:MM" ranges,
# which may cross midnight) and manual (watts while toggled on in the TUI).

`

// Save validates c and writes it to path atomically, creating the directory.
func Save(path string, c *Config) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	var buf bytes.Buffer
	buf.WriteString(header)
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return WriteFileAtomic(path, buf.Bytes())
}

// WriteFileAtomic writes data to a temp file next to path and renames it.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
