package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevnnard/watts/internal/discover"
)

// useScan replaces the network scan, so no test touches the real network.
// opts receives the options the command passed.
func useScan(t *testing.T, devs []discover.Device, err error, notes ...error) *discover.Options {
	t.Helper()
	got := &discover.Options{}
	old := scanNetwork
	scanNetwork = func(_ context.Context, o discover.Options) ([]discover.Device, error) {
		*got = o
		for _, n := range notes {
			o.OnError(n)
		}
		return devs, err
	}
	t.Cleanup(func() { scanNetwork = old })
	return got
}

// scanHome is a scan result as discover.Scan returns it: sorted by name.
func scanHome() []discover.Device {
	return []discover.Device{
		{ID: "uuid:0ab1c2d3-0000-1000-8000-abcdef012345", Name: "Bedroom TV", Brand: "Samsung", Model: "QN55Q60DAKXZL",
			Host: "192.0.2.16", Protocol: "samsung,ssdp", Probe: discover.ProbeSamsung, State: discover.StateOn},
		{ID: "mdns:0123abcd", Name: "Kitchen", Brand: "Google", Model: "Chromecast",
			Host: "192.0.2.30", Protocol: "mdns", State: discover.StateUnknown},
		{ID: "uuid:t4300", Name: "Office TV", Brand: "samsung", Model: "32\" T4300",
			Host: "192.0.2.17", Protocol: "ssdp", State: discover.StateUnknown},
		{ID: "ps:a1b2c3d4e5f6", Name: "PS5-001", Brand: "Sony", Model: "PS5",
			Host: "192.0.2.40", Protocol: "ddp", Probe: discover.ProbePS5, State: discover.StateStandby},
		{ID: "host:192.0.2.50", Name: "192.0.2.50", Host: "192.0.2.50", Protocol: "ssdp", State: discover.StateUnknown},
	}
}

const scanLimits = "Probe none: watts can't read its power state; add it as a manual or schedule item.\n" +
	"State unknown: no power-state answer. No answer never means off.\n" +
	"Only this machine's local networks are searched: multicast and broadcast don't cross\n" +
	"routers, guest or isolated Wi-Fi hides devices, and devices that are fully off can't answer.\n"

// isolateHome also moves HOME, and returns every dir the command could write.
func isolateHome(t *testing.T) []string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	return []string{os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_DATA_HOME"), home}
}

func assertEmpty(t *testing.T, dirs []string) {
	t.Helper()
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, _ os.DirEntry, _ error) error {
			if p != d {
				t.Errorf("scan wrote %s", p)
			}
			return nil
		})
	}
}

func TestScan(t *testing.T) {
	dirs := isolateHome(t)
	opts := useScan(t, scanHome(), nil)
	code, out, errOut := runArgs("scan")
	want := "" +
		"NAME        BRAND/MODEL            STATE    PROBE    WATTS    HOST        ID\n" +
		"Bedroom TV  Samsung QN55Q60DAKXZL  on       samsung  unknown  192.0.2.16  uuid:0ab1c2d3-0000-1000-8000-abcdef012345\n" +
		"Kitchen     Google Chromecast      unknown  none     unknown  192.0.2.30  mdns:0123abcd\n" +
		"Office TV   samsung 32\" T4300      unknown  none     ~25 W    192.0.2.17  uuid:t4300\n" +
		"PS5-001     Sony PS5               standby  ps5      unknown  192.0.2.40  ps:a1b2c3d4e5f6\n" +
		"192.0.2.50  -                      unknown  none     unknown  192.0.2.50  host:192.0.2.50\n" +
		"\n5 devices answered within 3s.\n" + scanLimits
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("scan = %d\n%s\nwant\n%s\nstderr %q", code, out, want, errOut)
	}
	if opts.Timeout != 0 || opts.OnError == nil {
		t.Errorf("options %+v: want the default timeout and an OnError", opts)
	}
	assertEmpty(t, dirs)
}

func TestScanNothingFound(t *testing.T) {
	dirs := isolateHome(t)
	useScan(t, nil, nil)
	code, out, errOut := runArgs("scan")
	if want := "No devices answered within 3s.\n" + scanLimits; code != 0 || out != want || errOut != "" {
		t.Fatalf("scan = %d\n%s\nstderr %q", code, out, errOut)
	}
	assertEmpty(t, dirs)
}

func TestScanNotesGoToStderrSorted(t *testing.T) {
	isolateHome(t)
	useScan(t, scanHome()[:1], nil, errors.New("ssdp b: refused"), errors.New("ssdp a: refused"), errors.New("ssdp b: refused"))
	code, out, errOut := runArgs("scan")
	if code != 0 || !strings.HasPrefix(out, "NAME") || errOut != "watts scan: note: ssdp a: refused\nwatts scan: note: ssdp b: refused\n" {
		t.Fatalf("scan = %d\n%s\nstderr %q", code, out, errOut)
	}
}

func TestScanFailures(t *testing.T) {
	tests := []struct {
		name   string
		devs   []discover.Device
		err    error
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{name: "no network", err: errors.New("discover: no IPv4 network interface is up"), code: 1,
			stderr: "watts scan: discover: no IPv4 network interface is up\n"},
		{name: "interrupted keeps what was found", devs: scanHome()[3:4], err: context.Canceled, code: 1,
			stdout: "NAME     BRAND/MODEL  STATE    PROBE  WATTS    HOST        ID\n" +
				"PS5-001  Sony PS5     standby  ps5    unknown  192.0.2.40  ps:a1b2c3d4e5f6\n" +
				"\n1 device answered before the scan was interrupted.\n" + scanLimits,
			stderr: "watts scan: interrupted\n"},
		{name: "bad flag", args: []string{"--json"}, code: 2,
			stderr: "watts scan: flag provided but not defined: -json\nRun 'watts scan -h' for its flags.\n"},
		{name: "stray arg", args: []string{"wifi"}, code: 2,
			stderr: "watts scan: unexpected argument \"wifi\"\nRun 'watts scan -h' for its flags.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirs := isolateHome(t)
			useScan(t, tt.devs, tt.err)
			code, out, errOut := runArgs(append([]string{"scan"}, tt.args...)...)
			if code != tt.code || out != tt.stdout || errOut != tt.stderr {
				t.Errorf("scan %q = %d\nstdout %s\nstderr %q", tt.args, code, out, errOut)
			}
			assertEmpty(t, dirs)
		})
	}
}

// Untrusted names from the network cannot inject terminal escapes or break
// the table.
func TestScanSanitizesDeviceText(t *testing.T) {
	isolateHome(t)
	useScan(t, []discover.Device{{ID: "mdns:x\x1b[2J", Name: "evil\x1b]0;pwned\x07\tname\nline",
		Brand: "B", Model: strings.Repeat("m", 50), Host: "192.0.2.9", State: discover.StateUnknown}}, nil)
	_, out, _ := runArgs("scan")
	if strings.ContainsAny(out, "\x1b\x07\t") || strings.Count(out, "\n") != 8 {
		t.Fatalf("unsafe output %q", out)
	}
	if !strings.Contains(out, "evil?]0;pwned??name?line") || !strings.Contains(out, "B "+strings.Repeat("m", 29)+"…") {
		t.Fatalf("output %q", out)
	}
}

func TestScanHelp(t *testing.T) {
	isolateHome(t)
	called := false
	old := scanNetwork
	scanNetwork = func(context.Context, discover.Options) ([]discover.Device, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { scanNetwork = old })
	want := "Usage: watts scan\n\nList the devices found on the local networks (SSDP, mDNS, PS DDP); writes nothing.\n"
	for _, args := range [][]string{{"scan", "-h"}, {"scan", "--help"}, {"help", "scan"}} {
		if code, out, errOut := runArgs(args...); code != 0 || out != want || errOut != "" {
			t.Errorf("%q = %d\n%s\nstderr %q", args, code, out, errOut)
		}
	}
	if called {
		t.Error("help ran a scan")
	}
	_, out, _ := runArgs("--help")
	for _, want := range []string{"  scan            list the devices found", "Network scan (watts scan):", "No answer never means off"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help lacks %q", want)
		}
	}
}
