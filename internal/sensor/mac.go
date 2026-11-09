package sensor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/kevnnard/watts/internal/config"
)

// Mac reads SystemPowerIn (mW at the charger input) from
// `ioreg -rn AppleSmartBattery`. On battery it reads 0 W, because that
// energy was counted while charging (S7).
type Mac struct {
	Run Runner           // nil means Exec
	Now func() time.Time // nil means time.Now
}

// Source is config.SourceMac.
func (m *Mac) Source() string { return config.SourceMac }

// Read runs ioreg once.
func (m *Mac) Read(ctx context.Context) (Reading, error) {
	run := m.Run
	if run == nil {
		run = Exec
	}
	out, err := run(ctx, "ioreg", "-rn", "AppleSmartBattery")
	if err != nil {
		return Reading{}, fmt.Errorf("mac sensor: ioreg: %w", err)
	}
	connected, mw, err := ParseIoreg(out)
	if err != nil {
		return Reading{}, fmt.Errorf("mac sensor: %w", err)
	}
	r := Reading{Source: config.SourceMac, At: now(m.Now), Parts: Parts{
		ExternalConnected: ptr(connected),
		Measured:          []string{"charger"},
	}}
	if connected {
		r.Watts = mw / 1000
		r.Parts.ChargerW = ptr(r.Watts)
	}
	return r, nil
}

var (
	reExternal = regexp.MustCompile(`"ExternalConnected"\s*=\s*(Yes|No)`)
	// The leading quote keeps "AccumulatedSystemPowerIn" from matching.
	rePowerIn = regexp.MustCompile(`"SystemPowerIn"\s*=\s*(\d+)`)
)

// ParseIoreg returns whether the charger is connected and, if so,
// SystemPowerIn in mW. SystemPowerIn sits inside "PowerTelemetryData".
func ParseIoreg(out []byte) (connected bool, mw float64, err error) {
	m := reExternal.FindSubmatch(out)
	if m == nil {
		return false, 0, errors.New(`ioreg: no "ExternalConnected" key (no AppleSmartBattery?)`)
	}
	if string(m[1]) != "Yes" {
		return false, 0, nil
	}
	p := rePowerIn.FindSubmatch(out)
	if p == nil {
		return false, 0, errors.New(`ioreg: charger connected but no "SystemPowerIn" key`)
	}
	v, err := strconv.ParseFloat(string(p[1]), 64)
	if err != nil {
		return false, 0, fmt.Errorf("ioreg: SystemPowerIn: %w", err)
	}
	return true, v, nil
}
