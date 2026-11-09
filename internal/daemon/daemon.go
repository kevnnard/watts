// Package daemon samples the local sensor every interval into the store
// (S7). Each sample's Seconds is the wall-clock time since the previous
// stored sample of the source (one interval for the very first). The S7 gap
// rule (more than 3 intervals counts as one) is applied when reading, in
// package energy, so the raw elapsed time is kept. A failed read writes
// nothing: an unknown reading is never stored as 0 W.
//
// With a Display reader (the Linux monitor over DDC, S19), each tick also
// stores one display row. A failed DDC read stores no row for that tick and
// does not affect the power sample.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/kevnnard/watts/internal/sensor"
	"github.com/kevnnard/watts/internal/store"
)

// Store is the part of store.Store the daemon writes to.
type Store interface {
	AddSample(store.Sample) error
	AddDisplay(store.Display) error
	LatestSample(source string) (store.Sample, bool, error)
}

// Display reads the monitor (sensor.DDC).
type Display interface {
	Read(ctx context.Context) (sensor.DisplayReading, error)
}

// Daemon is one sampling loop.
type Daemon struct {
	Sensor   sensor.Sensor
	Store    Store
	Interval time.Duration
	Log      io.Writer // brief start, error and stop lines; nil discards
	// Display, when set, is read on every tick into a display row of the
	// sensor's source. nil means no DDC.
	Display Display
	// Tick returns a channel that fires every interval and a stop func;
	// nil means a time.Ticker.
	Tick func(time.Duration) (<-chan time.Time, func())
}

// Run reads the sensor once at start (it also primes delta-based sensors),
// then samples on every tick until ctx is done. It returns nil when ctx is
// cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if d.Sensor == nil || d.Store == nil {
		return errors.New("daemon: sensor and store are required")
	}
	if d.Interval <= 0 {
		return errors.New("daemon: interval must be greater than 0")
	}
	log := d.Log
	if log == nil {
		log = io.Discard
	}
	src := d.Sensor.Source()
	if r, err := d.Sensor.Read(ctx); err != nil {
		fmt.Fprintf(log, "watts daemon: %s read failed: %v\n", src, err)
		fmt.Fprintf(log, "watts daemon: sampling %s every %v\n", src, d.Interval)
	} else {
		fmt.Fprintf(log, "watts daemon: %s %.1f W now, sampling every %v\n", src, r.Watts, d.Interval)
	}
	var last time.Time
	if s, ok, err := d.Store.LatestSample(src); err != nil {
		fmt.Fprintf(log, "watts daemon: store: %v\n", err)
	} else if ok {
		last = s.TS
	}
	ddcFailing := false
	tick, stop := d.ticker()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(log, "watts daemon: stopped")
			return nil
		case <-tick:
		}
		if ts, ok := d.sample(ctx, log, src, last); ok {
			last = ts
		}
		if d.Display != nil {
			ddcFailing = d.display(ctx, log, src, ddcFailing)
		}
	}
}

// sample reads the sensor and stores one sample. It returns the sample's
// time when one was stored.
func (d *Daemon) sample(ctx context.Context, log io.Writer, src string, last time.Time) (time.Time, bool) {
	r, err := d.Sensor.Read(ctx)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(log, "watts daemon: %s read failed, no sample: %v\n", src, err)
		}
		return time.Time{}, false
	}
	ts := time.Unix(r.At.Unix(), 0) // the store keeps whole seconds
	secs := d.Interval.Seconds()
	if !last.IsZero() {
		// Wall clock (Unix), not the monotonic clock, so time asleep counts
		// and the gap rule sees it.
		if e := ts.Unix() - last.Unix(); e > 0 {
			secs = float64(e)
		}
	}
	if err := d.Store.AddSample(store.Sample{TS: ts, Source: src, Watts: r.Watts, Seconds: secs}); err != nil {
		fmt.Fprintf(log, "watts daemon: store: %v\n", err)
		return time.Time{}, false
	}
	return ts, true
}

// display reads the monitor and stores one display row. A failure is
// logged once until a read works again (an unplugged monitor fails every
// tick). It returns whether DDC is failing now.
func (d *Daemon) display(ctx context.Context, log io.Writer, src string, failing bool) bool {
	r, err := d.Display.Read(ctx)
	if err != nil {
		if ctx.Err() == nil && !failing {
			fmt.Fprintf(log, "watts daemon: DDC read failed, no display row (logged once until it works): %v\n", err)
		}
		return true
	}
	if failing {
		fmt.Fprintln(log, "watts daemon: DDC read works again")
	}
	x := store.Display{TS: time.Unix(r.At.Unix(), 0), Source: src, Brightness: r.Brightness, MaxBrightness: r.Max, On: r.On}
	if err := d.Store.AddDisplay(x); err != nil {
		fmt.Fprintf(log, "watts daemon: store: %v\n", err)
	}
	return false
}

func (d *Daemon) ticker() (<-chan time.Time, func()) {
	if d.Tick != nil {
		return d.Tick(d.Interval)
	}
	t := time.NewTicker(d.Interval)
	return t.C, t.Stop
}
