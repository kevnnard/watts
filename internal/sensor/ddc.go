package sensor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kevnnard/watts/internal/config"
)

// DDCTimeout bounds one ddcutil call.
const DDCTimeout = 5 * time.Second

// DisplayReading is one DDC reading of the monitor (S19): brightness (VCP
// 0x10) out of Max, and whether the power mode (VCP 0xD6) is on.
type DisplayReading struct {
	At         time.Time
	Brightness int
	Max        int
	On         bool
}

// DDC reads the monitor on one I2C bus with `ddcutil --bus N --noverify
// --brief getvcp 10 d6`. Only the Linux daemon uses it.
type DDC struct {
	Bus     int
	Run     Runner        // nil means Exec
	Timeout time.Duration // 0 means DDCTimeout
	Now     func() time.Time
}

// Read runs ddcutil once and parses its brief output.
func (d *DDC) Read(ctx context.Context) (DisplayReading, error) {
	at := now(d.Now)
	out, err := runTimeout(ctx, d.Run, d.Timeout, "ddcutil", "--bus", strconv.Itoa(d.Bus), "--noverify", "--brief", "getvcp", "10", "d6")
	if err != nil {
		return DisplayReading{}, fmt.Errorf("ddcutil bus %d: %w", d.Bus, err)
	}
	r, err := ParseVCP(out)
	if err != nil {
		return DisplayReading{}, fmt.Errorf("ddcutil bus %d: %w", d.Bus, err)
	}
	r.At = at
	return r, nil
}

// ParseVCP parses `ddcutil --brief getvcp 10 d6`:
//
//	VCP 10 C 65 100
//	VCP D6 SNC x01
//
// Brightness is "C <current> <max>"; power mode x01 is on and any other
// value is standby or off. Both lines are required.
func ParseVCP(out []byte) (DisplayReading, error) {
	var r DisplayReading
	var gotB, gotP bool
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[0] != "VCP" {
			continue
		}
		switch strings.ToUpper(f[1]) {
		case "10":
			if len(f) != 5 || f[2] != "C" {
				return DisplayReading{}, fmt.Errorf("brightness: unexpected %q", sc.Text())
			}
			cur, err1 := strconv.Atoi(f[3])
			max, err2 := strconv.Atoi(f[4])
			if err1 != nil || err2 != nil || cur < 0 || max <= 0 || cur > max {
				return DisplayReading{}, fmt.Errorf("brightness: bad value %q", sc.Text())
			}
			r.Brightness, r.Max, gotB = cur, max, true
		case "D6":
			if len(f) != 4 || f[2] != "SNC" || !strings.HasPrefix(f[3], "x") {
				return DisplayReading{}, fmt.Errorf("power mode: unexpected %q", sc.Text())
			}
			v, err := strconv.ParseUint(f[3][1:], 16, 8)
			if err != nil {
				return DisplayReading{}, fmt.Errorf("power mode: bad value %q", sc.Text())
			}
			r.On, gotP = v == 1, true
		}
	}
	switch {
	case !gotB && !gotP:
		return DisplayReading{}, fmt.Errorf("no VCP values in %q", firstLine(out))
	case !gotB:
		return DisplayReading{}, errors.New("no brightness (VCP 10)")
	case !gotP:
		return DisplayReading{}, errors.New("no power mode (VCP D6)")
	}
	return r, nil
}

// DetectBus finds the monitor's I2C bus with `ddcutil detect --terse`: the
// first valid display's "I2C bus: /dev/i2c-N".
func DetectBus(ctx context.Context, run Runner) (int, error) {
	out, err := runTimeout(ctx, run, 0, "ddcutil", "detect", "--terse")
	if err != nil {
		return 0, fmt.Errorf("ddcutil detect: %w", err)
	}
	bus, err := ParseDetect(out)
	if err != nil {
		return 0, fmt.Errorf("ddcutil detect: %w", err)
	}
	return bus, nil
}

// ParseDetect returns the bus of the first "Display N" block of `ddcutil
// detect --terse`, skipping "Invalid display" blocks.
func ParseDetect(out []byte) (int, error) {
	valid := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			valid = strings.HasPrefix(t, "Display ")
			continue
		}
		dev, ok := strings.CutPrefix(t, "I2C bus:")
		if !ok || !valid {
			continue
		}
		n, ok := strings.CutPrefix(strings.TrimSpace(dev), "/dev/i2c-")
		if !ok {
			continue
		}
		if bus, err := strconv.Atoi(n); err == nil && bus >= 0 {
			return bus, nil
		}
	}
	return 0, errors.New("no display found")
}

func runTimeout(ctx context.Context, run Runner, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if run == nil {
		run = Exec
	}
	if timeout <= 0 {
		timeout = DDCTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(ctx, name, args...)
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("timed out after %v", timeout)
	}
	// ddcutil prints most failures ("Display not found") on stdout.
	msg := firstLine(out)
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		msg = firstLine(ee.Stderr)
	}
	if msg != "" {
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return nil, err
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// LocalDisplay is the DDC reader of the Linux daemon. It is nil on other
// systems and when no item sets ddc = "linux"; then off is "". When DDC is
// wanted but cannot work (ddcutil missing, no display detected), it is nil
// and off says why, for one log line. It never fails the daemon.
func LocalDisplay(ctx context.Context, cfg *config.Config) (d *DDC, off string) {
	return displayFor(ctx, runtime.GOOS, cfg, Exec, exec.LookPath)
}

func displayFor(ctx context.Context, goos string, cfg *config.Config, run Runner, look func(string) (string, error)) (*DDC, string) {
	if goos != "linux" || cfg == nil || !cfg.UsesDDC(config.SourceLinux) {
		return nil, ""
	}
	if _, err := look("ddcutil"); err != nil {
		return nil, "DDC off: ddcutil not found (install ddcutil, or remove ddc from the config)"
	}
	bus := cfg.Display.Bus
	if bus == 0 {
		var err error
		if bus, err = DetectBus(ctx, run); err != nil {
			return nil, "DDC off: " + err.Error() + " (set [display] bus = N in the config)"
		}
	}
	return &DDC{Bus: bus, Run: run}, ""
}
