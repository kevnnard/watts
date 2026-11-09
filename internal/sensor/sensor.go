// Package sensor reads the power of the local machine (S8) without root:
// the Mac's charger input from ioreg, and the Linux PC's GPU and CPU from
// sysfs, turned into whole-PC watts with the measured item's rest and PSU
// estimates. Sources sit behind Sensor so the daemon and tests use fakes.
package sensor

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/kevnnard/watts/internal/config"
)

// Sensor reads the whole-device watts of one source.
type Sensor interface {
	Source() string // config.SourceMac or config.SourceLinux
	Read(ctx context.Context) (Reading, error)
}

// Reading is one sensor reading. Watts is the whole device: what the daemon
// stores. Parts say where it came from and which parts are estimates.
type Reading struct {
	Source string    `json:"source"`
	At     time.Time `json:"at"`
	Watts  float64   `json:"watts"`
	Parts  Parts     `json:"parts"`
}

// Parts break a reading down. Mac readings set ExternalConnected and
// ChargerW; Linux readings set the GPU, CPU and estimate fields. Measured,
// Estimated and Missing name the parts ("charger", "gpu", "cpu", "rest",
// "psu").
type Parts struct {
	ExternalConnected *bool    `json:"external_connected,omitempty"`
	ChargerW          *float64 `json:"charger_w,omitempty"` // SystemPowerIn; absent on battery
	GPUW              *float64 `json:"gpu_w,omitempty"`
	CPUW              *float64 `json:"cpu_w,omitempty"`
	CPUUtil           *float64 `json:"cpu_util,omitempty"` // 0–1 over the read window; absent if /proc/stat is unreadable
	RestW             *float64 `json:"rest_w,omitempty"`
	PSUEfficiency     *float64 `json:"psu_efficiency,omitempty"`
	Measured          []string `json:"measured"`
	Estimated         []string `json:"estimated,omitempty"`
	Missing           []string `json:"missing,omitempty"`
}

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs the command for real.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Local returns the sensor for this machine: the Mac sensor on macOS, the
// Linux sensor (using the first measured linux item of cfg for the rest and
// PSU estimates) on Linux.
func Local(cfg *config.Config) (Sensor, error) {
	return forOS(runtime.GOOS, cfg)
}

func forOS(goos string, cfg *config.Config) (Sensor, error) {
	switch goos {
	case "darwin":
		return &Mac{}, nil
	case "linux":
		return &Linux{Item: linuxItem(cfg)}, nil
	}
	return nil, fmt.Errorf("sensor: no power sensor for %s (macOS and Linux only)", goos)
}

// linuxItem is the first measured linux item, or a default one.
func linuxItem(cfg *config.Config) config.Item {
	if cfg != nil {
		for _, it := range cfg.Items {
			if it.Mode == config.ModeMeasured && it.Source == config.SourceLinux {
				return it
			}
		}
	}
	return config.Item{Mode: config.ModeMeasured, Source: config.SourceLinux}
}

func ptr[T any](v T) *T { return &v }

func now(f func() time.Time) time.Time {
	if f == nil {
		return time.Now()
	}
	return f()
}
