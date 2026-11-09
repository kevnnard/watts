package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	c := Default()
	return c
}

func TestDefault(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	if len(c.Items) != 3 {
		t.Fatalf("default has %d items, want 3", len(c.Items))
	}
	mac, pc, lg := c.Items[0], c.Items[1], c.Items[2]
	if mac.Name != "Mac" || mac.Mode != ModeMeasured || mac.Source != SourceMac {
		t.Errorf("mac item = %+v", mac)
	}
	if pc.Name != "PC Linux" || pc.Mode != ModeMeasured || pc.Source != SourceLinux || pc.RestW != 35 || pc.PSUEfficiency != 0.85 {
		t.Errorf("pc item = %+v", pc)
	}
	if lg.Mode != ModeFollows || lg.Brand != "LG" || lg.Model != "29WQ600" || lg.Watts <= 0 ||
		strings.Join(lg.Follows, ",") != "Mac,PC Linux" {
		t.Errorf("lg item = %+v", lg)
	}
	if c.Currency.Main != "COP" || c.Remote.Host != "kevnnard-station" || c.Remote.Command != "~/.local/bin/watts" || c.Interval() != 60*time.Second {
		t.Errorf("defaults = %+v %+v %v", c.Currency, c.Remote, c.Interval())
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		want   string
	}{
		{"empty name", func(c *Config) { c.Items[0].Name = " " }, `item 1: name is required`},
		{"duplicate name", func(c *Config) { c.Items[1].Name = "mac" }, `item "mac": duplicate name`},
		{"unknown mode", func(c *Config) { c.Items[0].Mode = "solar" }, `item "Mac": unknown mode "solar" (want measured, follows, schedule or manual)`},
		{"bad source", func(c *Config) { c.Items[0].Source = "windows" }, `item "Mac": source must be "mac" or "linux", got "windows"`},
		{"psu efficiency", func(c *Config) { c.Items[1].PSUEfficiency = 1.5 }, `item "PC Linux": psu_efficiency must be greater than 0 and at most 1, got 1.5`},
		{"negative rest", func(c *Config) { c.Items[1].RestW = -1 }, `item "PC Linux": rest_w must not be negative, got -1`},
		{"zero watts", func(c *Config) { c.Items[2].Watts = 0 }, `item "LG 29WQ600": watts must be greater than 0`},
		{"negative quantity", func(c *Config) { c.Items[2].Quantity = -2 }, `item "LG 29WQ600": quantity must be at least 1, got -2`},
		{"follows empty", func(c *Config) { c.Items[2].Follows = nil }, `item "LG 29WQ600": follows must list at least one measured item`},
		{"follows unknown", func(c *Config) { c.Items[2].Follows = []string{"Tablet"} }, `item "LG 29WQ600": follows unknown item "Tablet"`},
		{"follows non measured", func(c *Config) {
			c.Items = append(c.Items, Item{Name: "Lamp", Mode: ModeManual, Watts: 9, Quantity: 1})
			c.Items[2].Follows = []string{"Lamp"}
		}, `item "LG 29WQ600": follows "Lamp", which is not a measured item`},
		{"schedule empty", func(c *Config) {
			c.Items = append(c.Items, Item{Name: "Lights", Mode: ModeSchedule, Watts: 9, Quantity: 3})
		}, `item "Lights": schedule must have at least one range`},
		{"schedule bad range", func(c *Config) {
			c.Items = append(c.Items, Item{Name: "Lights", Mode: ModeSchedule, Watts: 9, Quantity: 3, Schedule: []string{"18:00-25:00"}})
		}, `item "Lights": invalid schedule range "18:00-25:00" (want HH:MM-HH:MM, 00:00 to 24:00)`},
		{"schedule equal ends", func(c *Config) {
			c.Items = append(c.Items, Item{Name: "Lights", Mode: ModeSchedule, Watts: 9, Quantity: 3, Schedule: []string{"18:00-18:00"}})
		}, `item "Lights": invalid schedule range "18:00-18:00" (start and end must differ)`},
		{"main currency", func(c *Config) { c.Currency.Main = "" }, `currency: main code is required`},
		{"main currency shape", func(c *Config) { c.Currency.Main = "pesos" }, `currency: main code must be 3 uppercase letters, got "pesos"`},
		{"secondary same", func(c *Config) { c.Currency.Secondary = "COP"; c.Currency.Rate = 1 }, `currency: secondary code must differ from main`},
		{"secondary rate", func(c *Config) { c.Currency.Secondary = "USD" }, `currency: rate must be greater than 0 when secondary is set`},
		{"interval", func(c *Config) { c.SampleIntervalSeconds = -5 }, `sample_interval_seconds must be greater than 0, got -5`},
		{"remote command spaces", func(c *Config) { c.Remote.Command = "nice watts" }, `remote: command "nice watts" must be one path without spaces or shell characters`},
		{"remote command shell", func(c *Config) { c.Remote.Command = "~/bin/watts;rm" }, `remote: command "~/bin/watts;rm" must be one path without spaces or shell characters`},
		{"remote command tilde user", func(c *Config) { c.Remote.Command = "~root/watts" }, `remote: command "~root/watts" must start with ~/, /, or a name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", tt.want)
			}
			if err.Error() != tt.want {
				t.Fatalf("Validate() = %q, want %q", err.Error(), tt.want)
			}
		})
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		in         string
		start, end int
		crosses    bool
		wantErr    bool
	}{
		{"18:00-24:00", 18 * 60, 24 * 60, false, false},
		{"22:30-06:15", 22*60 + 30, 6*60 + 15, true, false},
		{" 07:00 - 08:00 ", 7 * 60, 8 * 60, false, false},
		{"7-8", 0, 0, false, true},
		{"24:00-01:00", 0, 0, false, true},
		{"10:60-11:00", 0, 0, false, true},
	}
	for _, tt := range tests {
		r, err := ParseRange(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRange(%q) = %+v, want error", tt.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRange(%q) error %v", tt.in, err)
			continue
		}
		if r.StartMin != tt.start || r.EndMin != tt.end || r.CrossesMidnight() != tt.crosses {
			t.Errorf("ParseRange(%q) = %+v crosses=%v", tt.in, r, r.CrossesMidnight())
		}
	}
}

func TestLoadOrCreateWritesDefaultAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watts", "config.toml")
	c, created, err := LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("LoadOrCreate = %v, created=%v", err, created)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	c.Items = append(c.Items, Item{Name: "Room lights", Mode: ModeSchedule, Watts: 9, Quantity: 3, Schedule: []string{"18:00-24:00"}})
	c.Currency.Secondary, c.Currency.Rate = "USD", 4000
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, created, err := LoadOrCreate(path)
	if err != nil || created {
		t.Fatalf("second LoadOrCreate = %v, created=%v", err, created)
	}
	if len(got.Items) != 4 || got.Items[3].Schedule[0] != "18:00-24:00" || got.Items[3].Quantity != 3 ||
		got.Currency.Rate != 4000 || got.Items[1].PSUEfficiency != 0.85 {
		t.Errorf("round trip = %+v", got)
	}
}

func TestLoadAppliesDefaultsAndRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	minimal := filepath.Join(dir, "min.toml")
	os.WriteFile(minimal, []byte(`
[[items]]
name = "PC"
mode = "measured"
source = "linux"

[[items]]
name = "Fan"
mode = "manual"
watts = 50
`), 0o644)
	c, err := Load(minimal)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Items[0].RestW != DefaultRestW || c.Items[0].PSUEfficiency != DefaultPSUEfficiency ||
		c.Items[1].Quantity != 1 || c.Currency.Main != "COP" || c.Remote.Host != DefaultRemoteHost ||
		c.Remote.Command != DefaultRemoteCommand || c.SampleIntervalSeconds != 60 {
		t.Errorf("defaults not applied: %+v", c)
	}

	typo := filepath.Join(dir, "typo.toml")
	os.WriteFile(typo, []byte("[[items]]\nname = \"Fan\"\nmode = \"manual\"\nwats = 50\n"), 0o644)
	_, err = Load(typo)
	want := "config " + typo + ": unknown key items.wats"
	if err == nil || err.Error() != want {
		t.Errorf("Load(typo) = %v, want %q", err, want)
	}

	invalid := filepath.Join(dir, "invalid.toml")
	os.WriteFile(invalid, []byte("[[items]]\nname = \"Fan\"\nmode = \"manual\"\n"), 0o644)
	_, err = Load(invalid)
	want = "config " + invalid + `: item "Fan": watts must be greater than 0`
	if err == nil || err.Error() != want {
		t.Errorf("Load(invalid) = %v, want %q", err, want)
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/conf")
	t.Setenv("XDG_DATA_HOME", "/x/data")
	if got := Path(); got != "/x/conf/watts/config.toml" {
		t.Errorf("Path() = %q", got)
	}
	if got := DataDir(); got != "/x/data/watts" {
		t.Errorf("DataDir() = %q", got)
	}
	if got := StorePath(); got != "/x/data/watts/watts.db" {
		t.Errorf("StorePath() = %q", got)
	}
	if got := TariffPath(); got != "/x/data/watts/tariffs.toml" {
		t.Errorf("TariffPath() = %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	if got := Path(); got != filepath.Join(home, ".config", "watts", "config.toml") {
		t.Errorf("Path() without XDG = %q", got)
	}
}

func TestWholePCWatts(t *testing.T) {
	it := Item{Mode: ModeMeasured, Source: SourceLinux, RestW: 35, PSUEfficiency: 0.85}
	if got := it.WholePCWatts(60, 75); got != 200 {
		t.Errorf("WholePCWatts = %v, want 200", got)
	}
}

func TestValidateDDCErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		want   string
	}{
		{"ddc on measured", func(c *Config) { c.Items[0].DDC = "linux" }, `item "Mac": ddc is only for follows items, got mode "measured"`},
		{"ddc on manual", func(c *Config) {
			c.Items = append(c.Items, Item{Name: "Lamp", Mode: ModeManual, Watts: 9, Quantity: 1, DDC: "linux"})
		}, `item "Lamp": ddc is only for follows items, got mode "manual"`},
		{"ddc source", func(c *Config) { c.Items[2].DDC = "mac" }, `item "LG 29WQ600": ddc must be "linux" (the Linux daemon reads DDC), got "mac"`},
		{"range without ddc", func(c *Config) { c.Items[2].MaxW = 30 }, `item "LG 29WQ600": min_w, max_w and standby_w need ddc = "linux"`},
		{"negative standby", func(c *Config) { c.Items[2].DDC, c.Items[2].StandbyW = "linux", -1 }, `item "LG 29WQ600": standby_w must not be negative, got -1`},
		{"max below min", func(c *Config) { c.Items[2].DDC, c.Items[2].MaxW = "linux", 5 },
			`item "LG 29WQ600": ddc watts must be 0 ≤ standby_w ≤ min_w ≤ max_w, got standby_w 0.3, min_w 7, max_w 5`},
		{"standby above min", func(c *Config) { c.Items[2].DDC, c.Items[2].StandbyW = "linux", 8 },
			`item "LG 29WQ600": ddc watts must be 0 ≤ standby_w ≤ min_w ≤ max_w, got standby_w 8, min_w 7, max_w 22`},
		{"negative bus", func(c *Config) { c.Display.Bus = -1 }, `display: bus must not be negative, got -1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			err := c.Validate()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Validate() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDDCWatts(t *testing.T) {
	tests := []struct {
		name          string
		it            Item
		lo, hi, sleep float64
	}{
		{"catalog LG", Item{Watts: 22, Brand: "lg", Model: "29wq600", DDC: "linux"}, 7, 22, 0.3},
		{"catalog LG with own watts keeps the catalog range", Item{Watts: 25, Brand: "LG", Model: "29WQ600", DDC: "linux"}, 7, 22, 0.3},
		{"item overrides one field", Item{Watts: 22, Brand: "LG", Model: "29WQ600", DDC: "linux", MaxW: 28}, 7, 28, 0.3},
		{"catalog entry without a range", Item{Watts: 30, Brand: "LG", Model: "27UL500", DDC: "linux"}, 9, 30, 0.3},
		{"no brand", Item{Watts: 20, DDC: "linux"}, 6, 20, 0.3},
		{"all set", Item{Watts: 20, DDC: "linux", MinW: 4, MaxW: 18, StandbyW: 0.5}, 4, 18, 0.5},
	}
	for _, tt := range tests {
		lo, hi, sleep := tt.it.DDCWatts()
		if math.Abs(lo-tt.lo) > 1e-9 || hi != tt.hi || sleep != tt.sleep {
			t.Errorf("%s: DDCWatts = %g, %g, %g, want %g, %g, %g", tt.name, lo, hi, sleep, tt.lo, tt.hi, tt.sleep)
		}
	}
}

func TestLoadDDCItemAndDisplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[display]
bus = 4

[[items]]
name = "Mac"
mode = "measured"
source = "mac"

[[items]]
name = "LG 29WQ600"
mode = "follows"
watts = 22
brand = "LG"
model = "29WQ600"
follows = ["Mac"]
ddc = "linux"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Bus != 4 || c.Items[1].DDC != "linux" || !c.UsesDDC("linux") || c.UsesDDC("mac") {
		t.Fatalf("loaded %+v %+v", c.Display, c.Items[1])
	}
	// The default config stays without ddc and without a [display] table.
	if Default().UsesDDC("linux") {
		t.Error("default config uses DDC")
	}
	def := filepath.Join(t.TempDir(), "default.toml")
	if err := Save(def, Default()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(def)
	for _, key := range []string{"display", "ddc", "min_w", "max_w", "standby_w"} {
		if strings.Contains(string(b), key) {
			t.Errorf("default config has %q:\n%s", key, b)
		}
	}
}
