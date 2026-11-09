package sensor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseIoreg(t *testing.T) {
	tests := []struct {
		name      string
		in        []byte
		connected bool
		mw        float64
		err       string
	}{
		{"charging", fixture(t, "ioreg-charging.txt"), true, 24178, ""},
		{"battery", fixture(t, "ioreg-battery.txt"), false, 0, ""},
		// AccumulatedSystemPowerIn must not be mistaken for SystemPowerIn.
		{"connected without SystemPowerIn", fixture(t, "ioreg-no-power-in.txt"), false, 0, `no "SystemPowerIn"`},
		{"no battery", []byte("+-o Root\n"), false, 0, `no "ExternalConnected"`},
		{"empty", nil, false, 0, `no "ExternalConnected"`},
		{"spaced", []byte(`"ExternalConnected" = Yes` + "\n" + `"SystemPowerIn" = 61500`), true, 61500, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connected, mw, err := ParseIoreg(tt.in)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || connected != tt.connected || mw != tt.mw {
				t.Fatalf("ParseIoreg = %v, %v, %v; want %v, %v", connected, mw, err, tt.connected, tt.mw)
			}
		})
	}
}

func TestMacRead(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	yes, no := true, false
	tests := []struct {
		name string
		out  []byte
		err  error
		want Reading
		werr string
	}{
		{"charging", fixture(t, "ioreg-charging.txt"), nil, Reading{
			Source: "mac", At: at, Watts: 24.178,
			Parts: Parts{ExternalConnected: &yes, ChargerW: ptr(24.178), Measured: []string{"charger"}},
		}, ""},
		{"battery counts 0 W", fixture(t, "ioreg-battery.txt"), nil, Reading{
			Source: "mac", At: at, Watts: 0,
			Parts: Parts{ExternalConnected: &no, Measured: []string{"charger"}},
		}, ""},
		{"ioreg fails", nil, errors.New("exit status 1"), Reading{}, "ioreg: exit status 1"},
		{"bad output", []byte("garbage"), nil, Reading{}, `no "ExternalConnected"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotCmd []string
			m := &Mac{
				Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
					gotCmd = append([]string{name}, args...)
					return tt.out, tt.err
				},
				Now: func() time.Time { return at },
			}
			got, err := m.Read(context.Background())
			if want := []string{"ioreg", "-rn", "AppleSmartBattery"}; !reflect.DeepEqual(gotCmd, want) {
				t.Errorf("ran %q, want %q", gotCmd, want)
			}
			if tt.werr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.werr) {
					t.Fatalf("err = %v, want %q", err, tt.werr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Read =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
	if (&Mac{}).Source() != "mac" {
		t.Error("Mac.Source != mac")
	}
}
