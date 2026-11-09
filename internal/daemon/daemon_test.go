package daemon

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kevnnard/watts/internal/sensor"
	"github.com/kevnnard/watts/internal/store"
)

type step struct {
	at    time.Time
	watts float64
	err   error
}

// fakeSensor returns steps in order; the first is the start-up read.
type fakeSensor struct {
	steps []step
	n     int
}

func (f *fakeSensor) Source() string { return "mac" }

func (f *fakeSensor) Read(context.Context) (sensor.Reading, error) {
	s := f.steps[f.n]
	f.n++
	if s.err != nil {
		return sensor.Reading{}, s.err
	}
	return sensor.Reading{Source: "mac", At: s.at, Watts: s.watts}, nil
}

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func TestRun(t *testing.T) {
	tests := []struct {
		name   string
		prior  []store.Sample
		steps  []step
		want   []store.Sample
		stderr string
	}{
		{
			name:  "first sample covers one interval, then elapsed time; gaps are kept raw",
			steps: []step{{at: at(0), watts: 20}, {at: at(60), watts: 24.5}, {at: at(120), watts: 30}, {at: at(400), watts: 0}},
			want: []store.Sample{
				{TS: at(60), Source: "mac", Watts: 24.5, Seconds: 60},
				{TS: at(120), Source: "mac", Watts: 30, Seconds: 60},
				{TS: at(400), Source: "mac", Watts: 0, Seconds: 280},
			},
			stderr: "watts daemon: mac 20.0 W now, sampling every 1m0s\nwatts daemon: stopped\n",
		},
		{
			name:  "resumes from the newest stored sample",
			prior: []store.Sample{{TS: at(-30), Source: "mac", Watts: 10, Seconds: 60}},
			steps: []step{{at: at(0), watts: 20}, {at: at(60), watts: 22}},
			want: []store.Sample{
				{TS: at(-30), Source: "mac", Watts: 10, Seconds: 60},
				{TS: at(60), Source: "mac", Watts: 22, Seconds: 90},
			},
			stderr: "watts daemon: mac 20.0 W now, sampling every 1m0s\nwatts daemon: stopped\n",
		},
		{
			name:  "a failed read writes nothing; the next sample covers the gap",
			steps: []step{{err: errors.New("ioreg: boom")}, {at: at(60), watts: 5}, {err: errors.New("ioreg: boom")}, {at: at(180), watts: 7}},
			want: []store.Sample{
				{TS: at(60), Source: "mac", Watts: 5, Seconds: 60},
				{TS: at(180), Source: "mac", Watts: 7, Seconds: 120},
			},
			stderr: "watts daemon: mac read failed: ioreg: boom\n" +
				"watts daemon: sampling mac every 1m0s\n" +
				"watts daemon: mac read failed, no sample: ioreg: boom\n" +
				"watts daemon: stopped\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "watts.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.AddSamples(tt.prior); err != nil {
				t.Fatal(err)
			}
			ticks := make(chan time.Time)
			var log bytes.Buffer
			d := &Daemon{
				Sensor:   &fakeSensor{steps: tt.steps},
				Store:    st,
				Interval: time.Minute,
				Log:      &log,
				Tick: func(iv time.Duration) (<-chan time.Time, func()) {
					if iv != time.Minute {
						t.Errorf("tick interval %v", iv)
					}
					return ticks, func() {}
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error)
			go func() { done <- d.Run(ctx) }()
			for range tt.steps[1:] {
				ticks <- time.Time{}
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run = %v", err)
			}
			got, err := st.Samples("", at(-3600), at(3600))
			if err != nil {
				t.Fatal(err)
			}
			for i := range got {
				got[i].TS = got[i].TS.UTC()
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("samples =\n %+v\nwant\n %+v", got, tt.want)
			}
			if log.String() != tt.stderr {
				t.Errorf("log =\n%q\nwant\n%q", log.String(), tt.stderr)
			}
		})
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		d    Daemon
		err  string
	}{
		{"no sensor", Daemon{Store: nopStore{}, Interval: time.Minute}, "daemon: sensor and store are required"},
		{"no store", Daemon{Sensor: &fakeSensor{}, Interval: time.Minute}, "daemon: sensor and store are required"},
		{"zero interval", Daemon{Sensor: &fakeSensor{}, Store: nopStore{}}, "daemon: interval must be greater than 0"},
	}
	for _, tt := range tests {
		if err := tt.d.Run(context.Background()); err == nil || err.Error() != tt.err {
			t.Errorf("%s: Run = %v, want %q", tt.name, err, tt.err)
		}
	}
}

type nopStore struct{}

func (nopStore) AddSample(store.Sample) error            { return nil }
func (nopStore) AddDisplay(store.Display) error          { return nil }
func (nopStore) AddNetwork([]store.NetworkReading) error { return nil }
func (nopStore) LatestSample(string) (store.Sample, bool, error) {
	return store.Sample{}, false, nil
}

// fakeDisplay returns its steps in order, one per tick.
type fakeDisplay struct {
	steps []ddcStep
	n     int
}

type ddcStep struct {
	r   sensor.DisplayReading
	err error
}

func (f *fakeDisplay) Read(context.Context) (sensor.DisplayReading, error) {
	s := f.steps[f.n]
	f.n++
	return s.r, s.err
}

func TestRunDisplay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "watts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ddcErr := errors.New("ddcutil bus 4: timed out after 5s")
	sens := &fakeSensor{steps: []step{
		{at: at(0), watts: 60},
		{at: at(60), watts: 61},
		{at: at(120), watts: 62}, // DDC fails: the sample is still written
		{err: errors.New("rapl: boom")},
		{at: at(240), watts: 64},
		{at: at(300), watts: 65},
	}}
	disp := &fakeDisplay{steps: []ddcStep{
		{r: sensor.DisplayReading{At: at(61), Brightness: 65, Max: 100, On: true}},
		{err: ddcErr},
		{r: sensor.DisplayReading{At: at(181), Brightness: 65, Max: 100, On: true}}, // power failed: the row is still written
		{err: ddcErr},
		{r: sensor.DisplayReading{At: at(301), Brightness: 0, Max: 100, On: false}},
	}}
	ticks := make(chan time.Time)
	var log bytes.Buffer
	d := &Daemon{Sensor: sens, Store: st, Display: disp, Interval: time.Minute, Log: &log,
		Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	for range disp.steps {
		ticks <- time.Time{}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	samples, _ := st.Samples("", at(-1), at(3600))
	var sts []int64
	for _, x := range samples {
		sts = append(sts, x.TS.Unix()-t0.Unix())
	}
	if !reflect.DeepEqual(sts, []int64{60, 120, 240, 300}) {
		t.Errorf("sample times = %v, want 60 120 240 300", sts)
	}
	rows, _ := st.Displays("", at(-1), at(3600))
	for i := range rows {
		rows[i].TS = rows[i].TS.UTC()
	}
	want := []store.Display{
		{TS: at(61), Source: "mac", Brightness: 65, MaxBrightness: 100, On: true},
		{TS: at(181), Source: "mac", Brightness: 65, MaxBrightness: 100, On: true},
		{TS: at(301), Source: "mac", Brightness: 0, MaxBrightness: 100, On: false},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("display rows =\n %+v\nwant\n %+v", rows, want)
	}
	wantLog := "watts daemon: mac 60.0 W now, sampling every 1m0s\n" +
		"watts daemon: DDC read failed, no display row (logged once until it works): ddcutil bus 4: timed out after 5s\n" +
		"watts daemon: mac read failed, no sample: rapl: boom\n" +
		"watts daemon: DDC read works again\n" +
		"watts daemon: DDC read failed, no display row (logged once until it works): ddcutil bus 4: timed out after 5s\n" +
		"watts daemon: DDC read works again\n" +
		"watts daemon: stopped\n"
	if log.String() != wantLog {
		t.Errorf("log =\n%s\nwant\n%s", log.String(), wantLog)
	}
}
