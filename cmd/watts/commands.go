package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/daemon"
	"github.com/kevnnard/watts/internal/discover"
	"github.com/kevnnard/watts/internal/importer"
	"github.com/kevnnard/watts/internal/install"
	"github.com/kevnnard/watts/internal/remote"
	"github.com/kevnnard/watts/internal/sensor"
	"github.com/kevnnard/watts/internal/store"
)

// Edges replaced by tests.
var (
	localSensor  = sensor.Local
	localDisplay = ddcDisplay
	localNetwork = netNetwork
	now          = time.Now
	goos         = runtime.GOOS
	executable   = stableExecutable
	installRun   = install.Exec
	installWrite = config.WriteFileAtomic
)

// Flag definitions, shared by the commands and by help.

type sensorsOpts struct{ json bool }

func sensorsFlags(fs *flag.FlagSet) *sensorsOpts {
	o := &sensorsOpts{}
	fs.BoolVar(&o.json, "json", false, "print the reading as JSON")
	return o
}

type exportOpts struct {
	json     bool
	from, to string
}

func exportFlags(fs *flag.FlagSet) *exportOpts {
	o := &exportOpts{}
	fs.BoolVar(&o.json, "json", false, "print JSON (required; the only format)")
	fs.StringVar(&o.from, "from", "", "window start: `TIME` in RFC3339 or YYYY-MM-DD (local midnight); default 24 h before --to")
	fs.StringVar(&o.to, "to", "", "window end: `TIME` in RFC3339 or YYYY-MM-DD (local midnight); default now")
	return o
}

type installOpts struct {
	dryRun bool
	binary string
}

func installFlags(fs *flag.FlagSet) *installOpts {
	o := &installOpts{}
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the service file and the commands without writing or running anything")
	fs.StringVar(&o.binary, "binary", "", "absolute `PATH` of the watts binary the service runs (default this binary)")
	return o
}

type importOpts struct{ dir string }

func importFlags(fs *flag.FlagSet) *importOpts {
	o := &importOpts{}
	fs.StringVar(&o.dir, "dir", "", "`DIR` with the consumo YYYY-MM-DD ledgers, read only; default ~/.local/share/consumo")
	return o
}

func noFlags(*flag.FlagSet) {}

// showHelp is commandHelp, set in init to avoid an initialization cycle
// (commands → run funcs → help → commands).
var showHelp func(io.Writer, string)

// parseFlags parses args for the command name. ok is false when the caller
// must return code: 0 after -h, 2 on a bad flag or a stray argument.
func parseFlags(name string, args []string, stdout, stderr io.Writer, bind func(*flag.FlagSet)) (code int, ok bool) {
	fs := flag.NewFlagSet("watts "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bind(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			showHelp(stdout, name)
			return 0, false
		}
		fmt.Fprintf(stderr, "watts %s: %v\nRun 'watts %s -h' for its flags.\n", name, err, name)
		return 2, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "watts %s: unexpected argument %q\nRun 'watts %s -h' for its flags.\n", name, fs.Arg(0), name)
		return 2, false
	}
	return 0, true
}

// loadConfig reads the config, or uses the default when there is none. It
// never writes: creating the config is the dashboard's job.
func loadConfig() (*config.Config, error) {
	path := config.Path()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return config.Default(), nil
	}
	return config.Load(path)
}

func linuxItem(cfg *config.Config) config.Item {
	for _, it := range cfg.Items {
		if it.Mode == config.ModeMeasured && it.Source == config.SourceLinux {
			return it
		}
	}
	return config.Item{RestW: config.DefaultRestW, PSUEfficiency: config.DefaultPSUEfficiency}
}

func runSensors(args []string, stdout, stderr io.Writer) int {
	var o *sensorsOpts
	if code, ok := parseFlags("sensors", args, stdout, stderr, func(fs *flag.FlagSet) { o = sensorsFlags(fs) }); !ok {
		return code
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "watts sensors: %v\n", err)
		return 1
	}
	s, err := localSensor(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "watts sensors: %v\n", err)
		return 1
	}
	r, err := s.Read(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "watts sensors: %v\n", err)
		return 1
	}
	if o.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
		return 0
	}
	fmt.Fprintln(stdout, sensorText(r))
	return 0
}

func sensorText(r sensor.Reading) string {
	p := r.Parts
	kind := func(part string) string {
		for _, m := range p.Measured {
			if m == part {
				return "measured"
			}
		}
		return "estimated"
	}
	switch {
	case p.ExternalConnected != nil && !*p.ExternalConnected:
		return fmt.Sprintf("%s  %.1f W  on battery (counted while charging)", r.Source, r.Watts)
	case p.ExternalConnected != nil:
		return fmt.Sprintf("%s  %.1f W  charger input (measured)", r.Source, r.Watts)
	case p.GPUW != nil:
		gpu := fmt.Sprintf("gpu %.1f W (%s)", *p.GPUW, kind("gpu"))
		if len(p.Missing) > 0 {
			gpu = "gpu not found"
		}
		util := "util unknown"
		if p.CPUUtil != nil {
			util = fmt.Sprintf("util %.1f%%", *p.CPUUtil*100)
		}
		return fmt.Sprintf("%s  %.1f W  %s, cpu %.1f W (%s), %s, rest %.0f W, psu %.2f (estimated)",
			r.Source, r.Watts, gpu, *p.CPUW, kind("cpu"), util, *p.RestW, *p.PSUEfficiency)
	}
	return fmt.Sprintf("%s  %.1f W", r.Source, r.Watts)
}

func runDaemon(args []string, stdout, stderr io.Writer) int {
	if code, ok := parseFlags("daemon", args, stdout, stderr, noFlags); !ok {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "watts daemon: %v\n", err)
		return 1
	}
	s, err := localSensor(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "watts daemon: %v\n", err)
		return 1
	}
	st, err := store.Open(config.StorePath())
	if err != nil {
		fmt.Fprintf(stderr, "watts daemon: %v\n", err)
		return 1
	}
	defer st.Close()
	fmt.Fprintf(stderr, "watts daemon: store %s\n", config.StorePath())
	d := &daemon.Daemon{Sensor: s, Store: st, Interval: cfg.Interval(), Log: stderr}
	disp, note := localDisplay(ctx, cfg)
	if note != "" {
		fmt.Fprintf(stderr, "watts daemon: %s\n", note)
	}
	d.Display = disp
	if items := networkItems(cfg, s.Source()); len(items) > 0 {
		d.Network = localNetwork(items)
	}
	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "watts daemon: %v\n", err)
		return 1
	}
	return 0
}

// ddcDisplay is the monitor reader of the Linux daemon (S19), or nil. note
// is one log line: the bus in use, why DDC is off, or "" when no item
// uses DDC.
func ddcDisplay(ctx context.Context, cfg *config.Config) (daemon.Display, string) {
	d, off := sensor.LocalDisplay(ctx, cfg)
	if d == nil { // a nil *DDC must not become a non-nil daemon.Display
		return nil, off
	}
	return d, fmt.Sprintf("DDC on /dev/i2c-%d", d.Bus)
}

// networkItems are the network items the daemon of source asks: each
// item's source names the one machine that polls it.
func networkItems(cfg *config.Config, source string) []config.Item {
	var out []config.Item
	for _, it := range cfg.Items {
		if it.Mode == config.ModeNetwork && it.Source == source {
			out = append(out, it)
		}
	}
	return out
}

// netNetwork asks the items over the real network (S20).
func netNetwork(items []config.Item) *daemon.Network {
	return &daemon.Network{
		Items: items,
		Query: func(ctx context.Context, t sensor.NetworkTarget) (sensor.NetworkObservation, error) {
			return sensor.QueryNetwork(ctx, t, sensor.NetworkOptions{})
		},
		Scan: func(ctx context.Context) ([]discover.Device, error) { return discover.Scan(ctx, discover.Options{}) },
	}
}

// parseTime accepts RFC3339 or YYYY-MM-DD (local midnight).
func parseTime(flagName, s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(store.DayLayout, s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--%s %q: want RFC3339 (2026-11-08T10:00:00-05:00) or YYYY-MM-DD", flagName, s)
}

func runExport(args []string, stdout, stderr io.Writer) int {
	var o *exportOpts
	if code, ok := parseFlags("export", args, stdout, stderr, func(fs *flag.FlagSet) { o = exportFlags(fs) }); !ok {
		return code
	}
	if !o.json {
		fmt.Fprintln(stderr, "watts export: --json is required")
		return 2
	}
	to := now()
	if o.to != "" {
		t, err := parseTime("to", o.to)
		if err != nil {
			fmt.Fprintf(stderr, "watts export: %v\n", err)
			return 2
		}
		to = t
	}
	from := to.Add(-24 * time.Hour)
	if o.from != "" {
		t, err := parseTime("from", o.from)
		if err != nil {
			fmt.Fprintf(stderr, "watts export: %v\n", err)
			return 2
		}
		from = t
	}
	if !from.Before(to) {
		fmt.Fprintln(stderr, "watts export: --from must be before --to")
		return 2
	}
	st, err := store.Open(config.StorePath())
	if err != nil {
		fmt.Fprintf(stderr, "watts export: %v\n", err)
		return 1
	}
	defer st.Close()
	e, err := remote.Build(st, from, to)
	if err != nil {
		fmt.Fprintf(stderr, "watts %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(e); err != nil {
		fmt.Fprintf(stderr, "watts export: %v\n", err)
		return 1
	}
	return 0
}

// stableExecutable is the path the service should run: this binary as it was
// invoked, not resolved through symlinks, so a Homebrew install keeps
// working after `brew upgrade` replaces the versioned Cellar dir.
func stableExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return stablePath(p, brewPrefix, fileExists), nil
}

// stablePath maps a binary inside a Homebrew Cellar
// (<prefix>/Cellar/watts/0.1.0/bin/watts) to <brew --prefix>/bin/watts when
// that exists. Other paths are returned as they are.
func stablePath(exe string, prefix func() (string, error), exists func(string) bool) string {
	if !strings.Contains(filepath.ToSlash(exe), "/Cellar/") {
		return exe
	}
	pre, err := prefix()
	if err != nil || pre == "" {
		return exe
	}
	if bin := filepath.Join(pre, "bin", filepath.Base(exe)); exists(bin) {
		return bin
	}
	return exe
}

func brewPrefix() (string, error) {
	out, err := exec.Command("brew", "--prefix").Output()
	return strings.TrimSpace(string(out)), err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func runInstall(args []string, stdout, stderr io.Writer) int {
	var o *installOpts
	if code, ok := parseFlags("install", args, stdout, stderr, func(fs *flag.FlagSet) { o = installFlags(fs) }); !ok {
		return code
	}
	bin := o.binary
	if bin == "" {
		p, err := executable()
		if err != nil {
			fmt.Fprintf(stderr, "watts install: %v\n", err)
			return 1
		}
		bin = p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "watts install: %v\n", err)
		return 1
	}
	p, err := install.PlanFor(install.Options{GOOS: goos, Binary: bin, Home: home, UID: os.Getuid()})
	if err != nil {
		fmt.Fprintf(stderr, "watts %v\n", err)
		return 2
	}
	temp := install.TemporaryBinary(bin)
	if o.dryRun {
		if temp {
			fmt.Fprintf(stderr, "watts install: warning: %s is a temporary go build; install a built binary or pass --binary\n", bin)
		}
		p.DryRun(stdout)
		return 0
	}
	if temp {
		fmt.Fprintf(stderr, "watts install: %s is a temporary go build; install a built binary (e.g. go build -o ~/.local/bin/watts ./cmd/watts) or pass --binary\n", bin)
		return 2
	}
	if err := p.Apply(context.Background(), installWrite, installRun, stdout); err != nil {
		fmt.Fprintf(stderr, "watts %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "installed: %s daemon runs at login; logs: %s\n", bin, p.Log)
	return 0
}

func runImport(args []string, stdout, stderr io.Writer) int {
	var o *importOpts
	if code, ok := parseFlags("import-consumo", args, stdout, stderr, func(fs *flag.FlagSet) { o = importFlags(fs) }); !ok {
		return code
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "watts import-consumo: %v\n", err)
		return 1
	}
	dir := o.dir
	if dir == "" {
		dir = importer.DefaultDir()
	}
	it := linuxItem(cfg)
	// Check the ledgers before opening (and maybe creating) the store, so
	// rejected input leaves no trace.
	if _, _, err := importer.Scan(dir, it.RestW, it.PSUEfficiency); err != nil {
		fmt.Fprintf(stderr, "watts import-consumo: %v\n", err)
		return 1
	}
	st, err := store.Open(config.StorePath())
	if err != nil {
		fmt.Fprintf(stderr, "watts import-consumo: %v\n", err)
		return 1
	}
	defer st.Close()
	opts := importer.Options{RestW: it.RestW, PSUEfficiency: it.PSUEfficiency, Today: now().Format(store.DayLayout)}
	if err := importer.Run(dir, opts, st, stdout); err != nil {
		fmt.Fprintf(stderr, "watts %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "store: %s\n", config.StorePath())
	return 0
}

// commandHelp prints one command's synopsis and flags.
func commandHelp(w io.Writer, name string) {
	for _, c := range commands {
		if c.name != name {
			continue
		}
		fmt.Fprintf(w, "Usage: %s\n\n%s.\n", c.synopsis(), upperFirst(c.summary))
		if flags := flagLines(c); flags != "" {
			fmt.Fprintf(w, "\nFlags:\n%s", flags)
		}
		return
	}
}

func (c command) synopsis() string {
	if c.usage != "" {
		return "watts " + c.usage
	}
	return strings.TrimSpace("watts " + c.name)
}

// flagLines lists the command's flags, one per line.
func flagLines(c command) string {
	if c.flags == nil {
		return ""
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	c.flags(fs)
	var b strings.Builder
	fs.VisitAll(func(f *flag.Flag) {
		arg, usage := flag.UnquoteUsage(f)
		name := "--" + f.Name
		if arg != "" {
			name += " " + arg
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "false" {
			def = " (default " + f.DefValue + ")"
		}
		fmt.Fprintf(&b, "  %-16s %s%s\n", name, usage, def)
	})
	return b.String()
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
