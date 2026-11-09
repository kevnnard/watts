package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/daemon"
	"github.com/kevnnard/watts/internal/remote"
	"github.com/kevnnard/watts/internal/sensor"
	"github.com/kevnnard/watts/internal/store"
)

// isolate points the config and data dirs at temp dirs and returns the
// store path.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	return filepath.Join(data, "watts", "watts.db")
}

func runArgs(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

type fakeSensor struct {
	r      sensor.Reading
	err    error
	onRead func()
}

func (f *fakeSensor) Source() string { return f.r.Source }
func (f *fakeSensor) Read(context.Context) (sensor.Reading, error) {
	if f.onRead != nil {
		f.onRead()
	}
	return f.r, f.err
}

func useSensor(t *testing.T, s sensor.Sensor, err error) {
	t.Helper()
	old := localSensor
	localSensor = func(*config.Config) (sensor.Sensor, error) { return s, err }
	t.Cleanup(func() { localSensor = old })
}

func f64(v float64) *float64 { return &v }

func TestHelpDocumentsEveryCommandAndFlag(t *testing.T) {
	code, out, errOut := runArgs("--help")
	if code != 0 || errOut != "" {
		t.Fatalf("--help = %d, %q", code, errOut)
	}
	for _, want := range []string{
		"daemon ", "export ", "sensors ", "install ", "import-consumo ", "status ",
		"watts export --json [--from TIME] [--to TIME]", "--from TIME", "--to TIME", "--json",
		"watts install [--dry-run] [--binary PATH]", "--dry-run", "--binary PATH",
		"watts import-consumo [--dir DIR]", "--dir DIR", "watts sensors [--json]",
		"WATTS_REMOTE_COMMAND", "dev.kevnnard.watts.plist", "watts.service", "SIGINT/SIGTERM",
		`ddc = "linux"`, "min_w, max_w, standby_w", "[display] bus = N",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--help lacks %q", want)
		}
	}
	wantExport := "Usage: watts export --json [--from TIME] [--to TIME]\n\n" +
		"Print stored samples, daily aggregates and toggles as JSON (read by the Mac over SSH).\n\n" +
		"Flags:\n" +
		"  --from TIME      window start: TIME in RFC3339 or YYYY-MM-DD (local midnight); default 24 h before --to\n" +
		"  --json           print JSON (required; the only format)\n" +
		"  --to TIME        window end: TIME in RFC3339 or YYYY-MM-DD (local midnight); default now\n"
	for _, args := range [][]string{{"export", "-h"}, {"export", "--help"}, {"help", "export"}} {
		if code, out, errOut := runArgs(args...); code != 0 || out != wantExport || errOut != "" {
			t.Errorf("%q = %d\n%s\nstderr %q", args, code, out, errOut)
		}
	}
	if code, out, errOut := runArgs("help", "nope"); code != 2 || out != "" || errOut != "watts help: unknown command \"nope\"\n" {
		t.Errorf("help nope = %d, %q, %q", code, out, errOut)
	}
}

func TestSensors(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	yes := true
	mac := sensor.Reading{Source: "mac", At: at, Watts: 24.178, Parts: sensor.Parts{ExternalConnected: &yes, ChargerW: f64(24.178), Measured: []string{"charger"}}}
	linux := sensor.Reading{Source: "linux", At: at, Watts: 150, Parts: sensor.Parts{
		GPUW: f64(48), CPUW: f64(37.5), CPUUtil: f64(0.25), RestW: f64(35), PSUEfficiency: f64(0.85),
		Measured: []string{"gpu"}, Estimated: []string{"cpu", "rest", "psu"}}}
	tests := []struct {
		name      string
		args      []string
		r         sensor.Reading
		readErr   error
		sensorErr error
		code      int
		stdout    string
		stderr    string
	}{
		{"mac json", []string{"--json"}, mac, nil, nil, 0, `{
  "source": "mac",
  "at": "2026-11-08T12:00:00Z",
  "watts": 24.178,
  "parts": {
    "external_connected": true,
    "charger_w": 24.178,
    "measured": [
      "charger"
    ]
  }
}
`, ""},
		{"linux json", []string{"--json"}, linux, nil, nil, 0, `{
  "source": "linux",
  "at": "2026-11-08T12:00:00Z",
  "watts": 150,
  "parts": {
    "gpu_w": 48,
    "cpu_w": 37.5,
    "cpu_util": 0.25,
    "rest_w": 35,
    "psu_efficiency": 0.85,
    "measured": [
      "gpu"
    ],
    "estimated": [
      "cpu",
      "rest",
      "psu"
    ]
  }
}
`, ""},
		{"mac text", nil, mac, nil, nil, 0, "mac  24.2 W  charger input (measured)\n", ""},
		{"linux text", nil, linux, nil, nil, 0, "linux  150.0 W  gpu 48.0 W (measured), cpu 37.5 W (estimated), util 25.0%, rest 35 W, psu 0.85 (estimated)\n", ""},
		{"read fails", []string{"--json"}, mac, errors.New("mac sensor: ioreg: exit status 1"), nil, 1, "", "watts sensors: mac sensor: ioreg: exit status 1\n"},
		{"no sensor", []string{"--json"}, mac, nil, errors.New("sensor: no power sensor for windows (macOS and Linux only)"), 1, "",
			"watts sensors: sensor: no power sensor for windows (macOS and Linux only)\n"},
		{"bad flag", []string{"--yaml"}, mac, nil, nil, 2, "", "watts sensors: flag provided but not defined: -yaml\nRun 'watts sensors -h' for its flags.\n"},
		{"stray arg", []string{"now"}, mac, nil, nil, 2, "", "watts sensors: unexpected argument \"now\"\nRun 'watts sensors -h' for its flags.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			useSensor(t, &fakeSensor{r: tt.r, err: tt.readErr}, tt.sensorErr)
			code, out, errOut := runArgs(append([]string{"sensors"}, tt.args...)...)
			if code != tt.code || out != tt.stdout || errOut != tt.stderr {
				t.Errorf("sensors %q = %d\nstdout %s\nstderr %q", tt.args, code, out, errOut)
			}
		})
	}
}

func TestExport(t *testing.T) {
	dbPath := isolate(t)
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)
	for i := 1; i <= 3; i++ {
		st.AddSample(store.Sample{TS: base.Add(time.Duration(i) * time.Minute), Source: "linux", Watts: float64(100 + i), Seconds: 60})
	}
	st.UpsertDaily(store.DailyAggregate{Day: "2026-11-07", Source: "linux", Wh: 900, OnSeconds: 3600, Origin: "consumo"})
	st.Close()
	before, _ := os.ReadFile(dbPath)

	code, out, errOut := runArgs("export", "--json", "--from", "2026-11-07", "--to", base.Add(2*time.Minute).Format(time.RFC3339))
	if code != 0 || errOut != "" {
		t.Fatalf("export = %d, %q", code, errOut)
	}
	if !strings.HasSuffix(out, "}\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("export is not one JSON line: %q", out)
	}
	var e remote.Export
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatal(err)
	}
	if e.Version != remote.Version || len(e.Samples) != 2 || e.Samples[1].Watts != 102 || len(e.Daily) != 1 || e.Daily[0].Wh != 900 || len(e.Toggles) != 0 {
		t.Errorf("export = %+v", e)
	}

	rejects := []struct {
		args   []string
		stderr string
	}{
		{[]string{"export"}, "watts export: --json is required\n"},
		{[]string{"export", "--json", "--from", "yesterday"}, "watts export: --from \"yesterday\": want RFC3339 (2026-11-08T10:00:00-05:00) or YYYY-MM-DD\n"},
		{[]string{"export", "--json", "--to", "2026-13-01"}, "watts export: --to \"2026-13-01\": want RFC3339 (2026-11-08T10:00:00-05:00) or YYYY-MM-DD\n"},
		{[]string{"export", "--json", "--from", "2026-11-08", "--to", "2026-11-07"}, "watts export: --from must be before --to\n"},
		{[]string{"export", "--json", "extra"}, "watts export: unexpected argument \"extra\"\nRun 'watts export -h' for its flags.\n"},
	}
	for _, r := range rejects {
		code, out, errOut := runArgs(r.args...)
		if code != 2 || out != "" || errOut != r.stderr {
			t.Errorf("%q = %d, stdout %q, stderr %q", r.args, code, out, errOut)
		}
	}
	if after, _ := os.ReadFile(dbPath); !bytes.Equal(after, before) {
		t.Error("export changed the store file")
	}
}

func TestExportDoesNotNeedOtherFlags(t *testing.T) {
	isolate(t)
	oldNow := now
	now = func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) }
	defer func() { now = oldNow }()
	code, out, errOut := runArgs("export", "--json")
	if code != 0 || errOut != "" ||
		out != `{"version":1,"from":"2026-11-07T12:00:00Z","to":"2026-11-08T12:00:00Z","samples":[],"daily":[],"toggles":[]}`+"\n" {
		t.Errorf("export --json = %d, %q, %q", code, out, errOut)
	}
}

func writeLedger(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestImportConsumo(t *testing.T) {
	dbPath := isolate(t)
	oldNow := now
	now = func() time.Time { return time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local) }
	defer func() { now = oldNow }()

	bad := writeLedger(t, map[string]string{"2026-11-07": "1 2 3\n"})
	code, out, errOut := runArgs("import-consumo", "--dir", bad)
	if code != 1 || out != "" || errOut != "watts import-consumo: 2026-11-07: 3 fields, want 2 (mac_wh monitor_s) or 4 (gpu_wh cpu_wh on_s monitor_s)\n" {
		t.Errorf("bad ledger = %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rejected import created the store: %v", err)
	}
	code, out, errOut = runArgs("import-consumo", "--dir", "/nonexistent/consumo")
	if code != 1 || out != "" || errOut != "watts import-consumo: open /nonexistent/consumo: no such file or directory\n" {
		t.Errorf("missing dir = %d, %q, %q", code, out, errOut)
	}

	mac := writeLedger(t, map[string]string{"2026-11-07": "6.4708 1386\n", "2026-11-08": "139.4696 0\n"})
	linux := writeLedger(t, map[string]string{"2026-11-07": "120 80 36000 0\n"})
	want := "2026-11-07  mac        6.471 Wh  on 0h23m  new\n" +
		"2026-11-08  mac      139.470 Wh  on 0h00m  new\n" +
		"imported 2 days from " + mac + " (mac 2, linux 0): 2 new, 0 replaced\n" +
		"store: " + dbPath + "\n"
	if code, out, errOut := runArgs("import-consumo", "--dir", mac); code != 0 || out != want || errOut != "" {
		t.Errorf("import mac = %d\n%s\nwant\n%s\nstderr %q", code, out, want, errOut)
	}
	if code, out, _ := runArgs("import-consumo", "--dir", linux); code != 0 || !strings.Contains(out, "2026-11-07  linux    647.059 Wh  on 10h00m  new\n") {
		t.Errorf("import linux = %d\n%s", code, out)
	}
	// Idempotent: a second run replaces.
	if code, out, _ := runArgs("import-consumo", "--dir", mac); code != 0 || !strings.Contains(out, ": 0 new, 2 replaced\n") {
		t.Errorf("re-import = %d\n%s", code, out)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	as, _ := st.Daily("2026-01-01", "2026-12-31")
	if len(as) != 3 || as[0].Source != "linux" || as[1].Source != "mac" || as[1].OnSeconds != 1386 || as[2].Wh != 139.4696 {
		t.Errorf("store = %+v", as)
	}
}

func TestInstall(t *testing.T) {
	oldGOOS, oldExe, oldRun, oldWrite := goos, executable, installRun, installWrite
	t.Cleanup(func() { goos, executable, installRun, installWrite = oldGOOS, oldExe, oldRun, oldWrite })
	home, _ := os.UserHomeDir()
	unit := filepath.Join(home, ".config", "systemd", "user", "watts.service")
	tests := []struct {
		name    string
		goos    string
		exe     string
		args    []string
		code    int
		stdout  string
		stderr  string
		written []string
		ran     [][]string
	}{
		{name: "dry run writes and runs nothing", goos: "linux", exe: "/home/k/.local/bin/watts", args: []string{"--dry-run"}, code: 0,
			stdout: "Would write " + unit + ":\n[Unit]\nDescription=watts energy sampler (watts daemon)\n\n[Service]\nExecStart=/home/k/.local/bin/watts daemon\nRestart=always\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n\nWould run:\n  systemctl --user daemon-reload\n  systemctl --user enable --now watts.service\n  systemctl --user restart watts.service\n"},
		{name: "dry run warns about go run builds", goos: "linux", exe: "/tmp/go-build123/b001/exe/watts", args: []string{"--dry-run"}, code: 0,
			stderr: "watts install: warning: /tmp/go-build123/b001/exe/watts is a temporary go build; install a built binary or pass --binary\n"},
		{name: "install", goos: "linux", exe: "/home/k/.local/bin/watts", code: 0,
			stdout: "wrote " + unit + "\nran systemctl --user daemon-reload\nran systemctl --user enable --now watts.service\nran systemctl --user restart watts.service\n" +
				"installed: /home/k/.local/bin/watts daemon runs at login; logs: journalctl --user -u watts.service\n",
			written: []string{unit},
			ran:     [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "watts.service"}, {"systemctl", "--user", "restart", "watts.service"}}},
		{name: "--binary overrides", goos: "linux", exe: "/tmp/go-build1/exe/watts", args: []string{"--binary", "/opt/watts"}, code: 0,
			written: []string{unit}, ran: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "watts.service"}, {"systemctl", "--user", "restart", "watts.service"}}},
		{name: "refuses a go run build", goos: "linux", exe: "/tmp/go-build123/b001/exe/watts", code: 2,
			stderr: "watts install: /tmp/go-build123/b001/exe/watts is a temporary go build; install a built binary (e.g. go build -o ~/.local/bin/watts ./cmd/watts) or pass --binary\n"},
		{name: "relative --binary", goos: "linux", exe: "/x", args: []string{"--binary", "watts"}, code: 2, stderr: "watts install: binary path \"watts\" must be absolute\n"},
		{name: "unsupported OS", goos: "windows", exe: "/x/watts", code: 2, stderr: "watts install: unsupported OS windows (macOS and Linux only)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var written []string
			var ran [][]string
			goos = tt.goos
			executable = func() (string, error) { return tt.exe, nil }
			installWrite = func(path string, _ []byte) error { written = append(written, path); return nil }
			installRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
				ran = append(ran, append([]string{name}, args...))
				return nil, nil
			}
			code, out, errOut := runArgs(append([]string{"install"}, tt.args...)...)
			if code != tt.code || errOut != tt.stderr || (tt.stdout != "" && out != tt.stdout) {
				t.Errorf("install %q = %d\nstdout %s\nstderr %q", tt.args, code, out, errOut)
			}
			if !reflect.DeepEqual(written, tt.written) || !reflect.DeepEqual(ran, tt.ran) {
				t.Errorf("wrote %q ran %q, want %q %q", written, ran, tt.written, tt.ran)
			}
		})
	}
}

func TestDaemonStopsOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			dbPath := isolate(t)
			started := make(chan struct{}, 1)
			useSensor(t, &fakeSensor{r: sensor.Reading{Source: "mac", Watts: 24.2, At: time.Now()}, onRead: func() {
				select {
				case started <- struct{}{}:
				default:
				}
			}}, nil)
			type result struct {
				code        int
				out, errOut string
			}
			done := make(chan result)
			go func() {
				c, o, e := runArgs("daemon")
				done <- result{c, o, e}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("daemon did not start")
			}
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			var r result
			select {
			case r = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("daemon did not stop")
			}
			want := "watts daemon: store " + dbPath + "\nwatts daemon: mac 24.2 W now, sampling every 1m0s\nwatts daemon: stopped\n"
			if r.code != 0 || r.out != "" || r.errOut != want {
				t.Errorf("daemon = %d, stdout %q, stderr\n%q\nwant\n%q", r.code, r.out, r.errOut, want)
			}
		})
	}
}

type fakeDisplay struct{ reads int }

func (f *fakeDisplay) Read(context.Context) (sensor.DisplayReading, error) {
	f.reads++
	return sensor.DisplayReading{}, errors.New("unused")
}

// The daemon logs the DDC state once and keeps sampling when DDC is off.
func TestDaemonDDCNote(t *testing.T) {
	for _, tt := range []struct {
		name string
		disp daemon.Display
		note string
	}{
		{"off", nil, "DDC off: ddcutil not found (install ddcutil, or remove ddc from the config)"},
		{"on", &fakeDisplay{}, "DDC on /dev/i2c-4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := isolate(t)
			old := localDisplay
			localDisplay = func(context.Context, *config.Config) (daemon.Display, string) { return tt.disp, tt.note }
			t.Cleanup(func() { localDisplay = old })
			started := make(chan struct{}, 1)
			useSensor(t, &fakeSensor{r: sensor.Reading{Source: "linux", Watts: 70, At: time.Now()}, onRead: func() {
				select {
				case started <- struct{}{}:
				default:
				}
			}}, nil)
			done := make(chan string)
			go func() {
				_, _, e := runArgs("daemon")
				done <- e
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("daemon did not start")
			}
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			want := "watts daemon: store " + dbPath + "\nwatts daemon: " + tt.note + "\nwatts daemon: linux 70.0 W now, sampling every 1m0s\nwatts daemon: stopped\n"
			select {
			case got := <-done:
				if got != want {
					t.Errorf("stderr =\n%q\nwant\n%q", got, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("daemon did not stop")
			}
		})
	}
}

func TestDaemonRejects(t *testing.T) {
	isolate(t)
	useSensor(t, nil, errors.New("sensor: no power sensor for windows (macOS and Linux only)"))
	if code, out, errOut := runArgs("daemon"); code != 1 || out != "" || errOut != "watts daemon: sensor: no power sensor for windows (macOS and Linux only)\n" {
		t.Errorf("daemon without sensor = %d, %q, %q", code, out, errOut)
	}
	if code, _, errOut := runArgs("daemon", "--interval", "5"); code != 2 || errOut != "watts daemon: flag provided but not defined: -interval\nRun 'watts daemon -h' for its flags.\n" {
		t.Errorf("daemon --interval = %d, %q", code, errOut)
	}
}

func TestStablePath(t *testing.T) {
	prefix := func() (string, error) { return "/opt/homebrew", nil }
	noBrew := func() (string, error) { return "", errors.New(`exec: "brew": executable file not found in $PATH`) }
	have := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range paths {
				if p == x {
					return true
				}
			}
			return false
		}
	}
	tests := []struct {
		name   string
		exe    string
		prefix func() (string, error)
		exists func(string) bool
		want   string
	}{
		{"brew symlink as invoked", "/opt/homebrew/bin/watts", prefix, have(), "/opt/homebrew/bin/watts"},
		{"Cellar path to the prefix bin", "/opt/homebrew/Cellar/watts/0.1.0/bin/watts", prefix, have("/opt/homebrew/bin/watts"), "/opt/homebrew/bin/watts"},
		{"Linuxbrew Cellar", "/home/linuxbrew/.linuxbrew/Cellar/watts/0.1.0/bin/watts", func() (string, error) { return "/home/linuxbrew/.linuxbrew", nil },
			have("/home/linuxbrew/.linuxbrew/bin/watts"), "/home/linuxbrew/.linuxbrew/bin/watts"},
		{"Cellar without the prefix link", "/opt/homebrew/Cellar/watts/0.1.0/bin/watts", prefix, have(), "/opt/homebrew/Cellar/watts/0.1.0/bin/watts"},
		{"Cellar without brew", "/opt/homebrew/Cellar/watts/0.1.0/bin/watts", noBrew, have("/opt/homebrew/bin/watts"), "/opt/homebrew/Cellar/watts/0.1.0/bin/watts"},
		{"plain install", "/home/k/.local/bin/watts", prefix, have("/opt/homebrew/bin/watts"), "/home/k/.local/bin/watts"},
	}
	for _, tt := range tests {
		if got := stablePath(tt.exe, tt.prefix, tt.exists); got != tt.want {
			t.Errorf("%s: stablePath(%q) = %q, want %q", tt.name, tt.exe, got, tt.want)
		}
	}
}

// The daemon asks only the network items of its own source, and stores
// their readings; the items of the other machine are left to its daemon.
func TestDaemonNetworkItems(t *testing.T) {
	dbPath := isolate(t)
	cfg := config.Default()
	cfg.Items = append(cfg.Items,
		config.Item{Name: "TV", Mode: config.ModeNetwork, Source: config.SourceMac, Probe: config.ProbeSamsung,
			Host: "192.0.2.16", ID: "uuid:abc", Watts: 63, StandbyW: 0.5, Quantity: 1},
		config.Item{Name: "PS5", Mode: config.ModeNetwork, Source: config.SourceLinux, Probe: config.ProbePS5,
			Host: "192.0.2.40", Watts: 217, StandbyW: 1.2, Quantity: 1})
	if err := config.Save(config.Path(), cfg); err != nil {
		t.Fatal(err)
	}
	polled := make(chan struct{}, 10)
	var asked []string
	old := localNetwork
	localNetwork = func(items []config.Item) *daemon.Network {
		n := old(items)
		n.Scan = nil
		n.Query = func(_ context.Context, tg sensor.NetworkTarget) (sensor.NetworkObservation, error) {
			defer func() { polled <- struct{}{} }()
			asked = append(asked, tg.Host+" "+tg.ID)
			return sensor.NetworkObservation{At: now(), State: "on", Host: tg.Host, ID: tg.ID, Verified: true}, nil
		}
		return n
	}
	t.Cleanup(func() { localNetwork = old })
	useSensor(t, &fakeSensor{r: sensor.Reading{Source: "mac", Watts: 24.2, At: time.Now()}}, nil)
	done := make(chan string)
	go func() {
		_, _, e := runArgs("daemon")
		done <- e
	}()
	select {
	case <-polled:
	case <-time.After(5 * time.Second):
		t.Fatal("no network poll")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var errOut string
	select {
	case errOut = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	want := "watts daemon: store " + dbPath + "\nwatts daemon: mac 24.2 W now, sampling every 1m0s\n" +
		"watts daemon: asking TV for its power state every 1m0s (estimated, not measured)\n" +
		"watts daemon: TV on at 192.0.2.16\nwatts daemon: stopped\n"
	if errOut != want {
		t.Errorf("stderr =\n%q\nwant\n%q", errOut, want)
	}
	if !reflect.DeepEqual(asked, []string{"192.0.2.16 uuid:abc"}) {
		t.Errorf("asked %q, want only the mac's TV", asked)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, _ := st.NetworkReadings("", time.Time{}, time.Now().Add(time.Hour))
	if len(rows) != 1 || rows[0].Item != "TV" || rows[0].State != store.NetworkOn || !rows[0].Verified {
		t.Errorf("rows = %+v", rows)
	}
}
