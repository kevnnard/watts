package sensor

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/config"
)

func TestParseVCP(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want DisplayReading
		err  string
	}{
		{name: "on at 65 %", out: "VCP 10 C 65 100\nVCP D6 SNC x01\n", want: DisplayReading{Brightness: 65, Max: 100, On: true}},
		{name: "DPM off", out: "VCP 10 C 65 100\nVCP D6 SNC x04\n", want: DisplayReading{Brightness: 65, Max: 100}},
		{name: "standby, lower case, other max", out: "VCP 10 C 0 255\nVCP d6 SNC x02\n", want: DisplayReading{Brightness: 0, Max: 255}},
		{name: "extra lines are ignored", out: "busno=4\nVCP 10 C 100 100\n\nVCP D6 SNC x01\n", want: DisplayReading{Brightness: 100, Max: 100, On: true}},
		{name: "brightness error", out: "VCP 10 ERR\nVCP D6 SNC x01\n", err: `brightness: unexpected "VCP 10 ERR"`},
		{name: "power mode error", out: "VCP 10 C 65 100\nVCP D6 ERR\n", err: `power mode: unexpected "VCP D6 ERR"`},
		{name: "zero max", out: "VCP 10 C 0 0\nVCP D6 SNC x01\n", err: `brightness: bad value "VCP 10 C 0 0"`},
		{name: "current above max", out: "VCP 10 C 120 100\nVCP D6 SNC x01\n", err: `brightness: bad value "VCP 10 C 120 100"`},
		{name: "garbage number", out: "VCP 10 C abc 100\nVCP D6 SNC x01\n", err: `brightness: bad value "VCP 10 C abc 100"`},
		{name: "garbage power value", out: "VCP 10 C 65 100\nVCP D6 SNC xZZ\n", err: `power mode: bad value "VCP D6 SNC xZZ"`},
		{name: "no brightness", out: "VCP D6 SNC x01\n", err: "no brightness (VCP 10)"},
		{name: "no power mode", out: "VCP 10 C 65 100\n", err: "no power mode (VCP D6)"},
		{name: "not ddcutil output", out: "Display not found\n", err: `no VCP values in "Display not found"`},
		{name: "empty", out: "", err: `no VCP values in ""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVCP([]byte(tt.out))
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("ParseVCP = %+v, %v, want error %q", got, err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseVCP = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

// fakeRun answers one command and records the argv.
type fakeRun struct {
	out  string
	err  error
	argv [][]string
	ctx  context.Context
}

func (f *fakeRun) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.argv = append(f.argv, append([]string{name}, args...))
	f.ctx = ctx
	return []byte(f.out), f.err
}

func TestDDCRead(t *testing.T) {
	at := time.Date(2026, 11, 8, 20, 15, 0, 0, time.UTC)
	f := &fakeRun{out: "VCP 10 C 65 100\nVCP D6 SNC x01\n"}
	d := &DDC{Bus: 4, Run: f.run, Now: func() time.Time { return at }}
	r, err := d.Read(context.Background())
	if err != nil || r != (DisplayReading{At: at, Brightness: 65, Max: 100, On: true}) {
		t.Fatalf("Read = %+v, %v", r, err)
	}
	want := [][]string{{"ddcutil", "--bus", "4", "--noverify", "--brief", "getvcp", "10", "d6"}}
	if !reflect.DeepEqual(f.argv, want) {
		t.Errorf("argv = %q", f.argv)
	}
	if dl, ok := f.ctx.Deadline(); !ok || time.Until(dl) > DDCTimeout {
		t.Errorf("ddcutil runs without the %v timeout", DDCTimeout)
	}

	f = &fakeRun{out: "Display not found\n", err: errors.New("exit status 1")}
	d = &DDC{Bus: 4, Run: f.run}
	if _, err := d.Read(context.Background()); err == nil || err.Error() != "ddcutil bus 4: exit status 1: Display not found" {
		t.Errorf("failed run = %v", err)
	}
	f = &fakeRun{out: "garbage"}
	d = &DDC{Bus: 4, Run: f.run}
	if _, err := d.Read(context.Background()); err == nil || err.Error() != `ddcutil bus 4: no VCP values in "garbage"` {
		t.Errorf("garbage = %v", err)
	}
	slow := func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	d = &DDC{Bus: 4, Run: slow, Timeout: time.Millisecond}
	if _, err := d.Read(context.Background()); err == nil || err.Error() != "ddcutil bus 4: timed out after 1ms" {
		t.Errorf("timeout = %v", err)
	}
}

const detectTerse = `Invalid display
   I2C bus:  /dev/i2c-3
   DRM connector:    card1-HDMI-A-1
   EDID synopsis:

Display 1
   I2C bus:          /dev/i2c-4
   DRM connector:    card1-DP-1
   Monitor:          GSM:LG ULTRAWIDE:

Display 2
   I2C bus:          /dev/i2c-7
`

func TestParseDetect(t *testing.T) {
	if bus, err := ParseDetect([]byte(detectTerse)); err != nil || bus != 4 {
		t.Errorf("ParseDetect = %d, %v, want 4 (the first valid display)", bus, err)
	}
	for _, out := range []string{"", "No displays found.\n", "Invalid display\n   I2C bus:  /dev/i2c-3\n", "Display 1\n   I2C bus: /dev/i2c-x\n"} {
		if bus, err := ParseDetect([]byte(out)); err == nil || err.Error() != "no display found" {
			t.Errorf("ParseDetect(%q) = %d, %v", out, bus, err)
		}
	}
}

func TestLocalDisplay(t *testing.T) {
	ddcCfg := func(bus int) *config.Config {
		c := config.Default()
		c.Items[2].DDC = config.SourceLinux
		c.Display.Bus = bus
		return c
	}
	found := func(string) (string, error) { return "/usr/bin/ddcutil", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	tests := []struct {
		name string
		goos string
		cfg  *config.Config
		look func(string) (string, error)
		run  *fakeRun
		bus  int // 0: no reader
		off  string
		ran  int
	}{
		{name: "not linux", goos: "darwin", cfg: ddcCfg(0), look: found, run: &fakeRun{}},
		{name: "no ddc item", goos: "linux", cfg: config.Default(), look: found, run: &fakeRun{}},
		{name: "ddcutil missing", goos: "linux", cfg: ddcCfg(0), look: missing, run: &fakeRun{},
			off: "DDC off: ddcutil not found (install ddcutil, or remove ddc from the config)"},
		{name: "configured bus skips detection", goos: "linux", cfg: ddcCfg(6), look: found, run: &fakeRun{}, bus: 6},
		{name: "detected bus", goos: "linux", cfg: ddcCfg(0), look: found, run: &fakeRun{out: detectTerse}, bus: 4, ran: 1},
		{name: "detection finds nothing", goos: "linux", cfg: ddcCfg(0), look: found, run: &fakeRun{out: "No displays found.\n"}, ran: 1,
			off: "DDC off: ddcutil detect: no display found (set [display] bus = N in the config)"},
		{name: "detection fails", goos: "linux", cfg: ddcCfg(0), look: found, run: &fakeRun{err: errors.New("exit status 1")}, ran: 1,
			off: "DDC off: ddcutil detect: exit status 1 (set [display] bus = N in the config)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, off := displayFor(context.Background(), tt.goos, tt.cfg, tt.run.run, tt.look)
			if off != tt.off {
				t.Errorf("off = %q, want %q", off, tt.off)
			}
			if (d == nil) != (tt.bus == 0) || d != nil && d.Bus != tt.bus {
				t.Errorf("reader = %+v, want bus %d", d, tt.bus)
			}
			if len(tt.run.argv) != tt.ran {
				t.Errorf("ran %q", tt.run.argv)
			}
			if tt.ran == 1 && strings.Join(tt.run.argv[0], " ") != "ddcutil detect --terse" {
				t.Errorf("detect argv = %q", tt.run.argv[0])
			}
		})
	}
}
