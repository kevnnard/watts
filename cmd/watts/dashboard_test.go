package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/remote"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
	"github.com/kevnnard/watts/internal/tui"
)

// statusNow is Sunday Nov 8 2026 20:15:05 local time, inside the seed
// tariff's Nov 1 → Dec 1 period.
var statusNow = time.Date(2026, 11, 8, 20, 15, 5, 0, time.Local)

// samples is one sample a minute for the last n minutes up to at.
func samples(source string, watts float64, at time.Time, n int) []store.Sample {
	var out []store.Sample
	for k := n - 1; k >= 0; k-- {
		out = append(out, store.Sample{Source: source, TS: at.Add(-time.Duration(k) * time.Minute), Watts: watts, Seconds: 60})
	}
	return out
}

func seedStore(t *testing.T, path string, xs ...[]store.Sample) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, x := range xs {
		if err := st.AddSamples(x); err != nil {
			t.Fatal(err)
		}
	}
}

func useStatusEdges(t *testing.T, client func(*config.Config) *remote.Client) {
	t.Helper()
	oldNow, oldClient := now, remoteClient
	now = func() time.Time { return statusNow }
	remoteClient = client
	t.Cleanup(func() { now, remoteClient = oldNow, oldClient })
}

// exitErr is an *exec.ExitError stand-in for the fake runner.
type exitErr int

func (e exitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

// fakeRemote answers `ssh … export --json` with the export it holds, or
// with err and stderr, and counts the calls.
type fakeRemote struct {
	mu     sync.Mutex
	calls  int
	export remote.Export
	err    error
	stderr string
}

func (f *fakeRemote) set(err error, stderr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err, f.stderr = err, stderr
}

func (f *fakeRemote) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRemote) run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if name != "ssh" || !strings.Contains(strings.Join(args, " "), "~/.local/bin/watts export --json") {
		return nil, []byte("unexpected command"), exitErr(1)
	}
	if f.err != nil {
		return nil, []byte(f.stderr), f.err
	}
	b, _ := json.Marshal(f.export)
	return b, nil, nil
}

func (f *fakeRemote) client(cfg *config.Config) *remote.Client {
	c := remote.FromConfig(cfg)
	c.Run = f.run
	return c
}

func linuxExport(at time.Time) remote.Export {
	return remote.Export{Version: remote.Version, From: at.Add(-time.Hour), To: at,
		Samples: samples(config.SourceLinux, 70, at, 60), Daily: []store.DailyAggregate{}, Toggles: []store.Toggle{}}
}

func TestStatus(t *testing.T) {
	db := isolate(t)
	useStatusEdges(t, func(*config.Config) *remote.Client { return nil })
	seedStore(t, db, samples(config.SourceMac, 20, statusNow, 120), samples(config.SourceLinux, 70, statusNow.Add(-2*time.Second), 60))

	code, out, errOut := runArgs("status")
	want := `watts  Sun Nov 8 20:15  Enel · $990,53 per kWh

Now     112,0 W  90,0 W measured + ~22,0 W estimated
  ● Mac         measured     20,0 W    0,04 kWh today
  ● PC Linux    measured     70,0 W    0,07 kWh today
  ● LG 29WQ600  follows     ~22,0 W    0,04 kWh today

Today   0,15 kWh  $153
Bill    $153 so far · projected $583
        Nov 1 → Dec 1 · day 8 of 30 · 0,2 kWh

Source  mac    ● online · last sample 20:15:05
Source  linux  ● online · last sample 20:15:03
`
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("status = %d, stderr %q, stdout:\n%s\nwant:\n%s", code, errOut, out, want)
	}
	if strings.Contains(out, "\x1b") {
		t.Error("status to a non-TTY has escape codes")
	}
	if _, err := os.Stat(config.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("status wrote a config: %v", err)
	}
}

func TestStatusANSIOnlyOnTTY(t *testing.T) {
	db := isolate(t)
	useStatusEdges(t, func(*config.Config) *remote.Client { return nil })
	seedStore(t, db, samples(config.SourceMac, 20, statusNow, 10))
	old := isTTY
	t.Cleanup(func() { isTTY = old })

	isTTY = func(io.Writer) bool { return true }
	code, out, _ := runArgs("status")
	if code != 0 || !strings.Contains(out, "\x1b[") {
		t.Errorf("status on a TTY = %d, want styled:\n%s", code, out)
	}
	isTTY = fileIsTTY
	if _, plain, _ := runArgs("status"); strings.Contains(plain, "\x1b") {
		t.Errorf("status off a TTY has escape codes:\n%s", plain)
	}
}

func TestStatusRejectsArguments(t *testing.T) {
	isolate(t)
	code, _, errOut := runArgs("status", "--json")
	if code != 2 || !strings.Contains(errOut, "watts status: flag provided but not defined: -json") {
		t.Errorf("status --json = %d, %q", code, errOut)
	}
}

func TestStatusRemoteOffline(t *testing.T) {
	db := isolate(t)
	f := &fakeRemote{err: exitErr(255), stderr: "ssh: connect to host kevnnard-station port 22: Operation timed out\n"}
	useStatusEdges(t, f.client)
	seedStore(t, db, samples(config.SourceMac, 20, statusNow, 120))

	code, out, errOut := runArgs("status")
	if code != 0 || errOut != "" {
		t.Fatalf("status = %d, %q", code, errOut)
	}
	for _, want := range []string{
		"Now     42,0 W  20,0 W measured + ~22,0 W estimated  partial · PC Linux offline\n",
		"  ● Mac         measured     20,0 W    0,04 kWh today\n",
		"  ✗ PC Linux    measured    offline    0,00 kWh today\n",
		"Today   0,08 kWh  $83\n",
		"· 0,1 kWh · may be low: PC Linux missing\n",
		"Source  linux  ✗ offline · ssh: connect to host kevnnard-station port 22: Operation timed out\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PC Linux    measured      0 W") {
		t.Errorf("offline PC shown as 0 W:\n%s", out)
	}
	if f.count() != 1 {
		t.Errorf("ssh calls = %d, want 1", f.count())
	}
}

func TestRemoteLoader(t *testing.T) {
	db := isolate(t)
	seedStore(t, db, samples(config.SourceMac, 20, statusNow, 30))
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	from := statusNow.Add(-2 * time.Hour)
	source := func(src []tui.Source, name string) tui.Source {
		for _, s := range src {
			if s.Name == name {
				return s
			}
		}
		t.Fatalf("no %s source in %+v", name, src)
		return tui.Source{}
	}

	t.Run("online then cached", func(t *testing.T) {
		f := &fakeRemote{export: linuxExport(statusNow)}
		useStatusEdges(t, f.client)
		l := newRemoteLoader(st)

		in, src, err := l.Load(context.Background(), cfg, from, statusNow)
		if err != nil {
			t.Fatal(err)
		}
		if f.count() != 1 || len(in.Data.Samples[config.SourceLinux]) != 60 || len(in.Data.Samples[config.SourceMac]) != 30 {
			t.Fatalf("first load: calls %d, linux %d, mac %d", f.count(), len(in.Data.Samples[config.SourceLinux]), len(in.Data.Samples[config.SourceMac]))
		}
		if s := source(src, config.SourceLinux); !s.Online || !s.LastTS.Equal(statusNow) || s.Via != "ssh kevnnard-station" {
			t.Errorf("linux source = %+v", s)
		}

		// The 5 s refresh reuses the cached export.
		for _, d := range []time.Duration{5 * time.Second, 59 * time.Second} {
			in, src, _ = l.Load(context.Background(), cfg, from, statusNow.Add(d))
			if f.count() != 1 || len(in.Data.Samples[config.SourceLinux]) != 60 || !source(src, config.SourceLinux).Online {
				t.Fatalf("load at +%v: calls %d, linux %d, %+v", d, f.count(), len(in.Data.Samples[config.SourceLinux]), src)
			}
		}

		// After 60 s it fetches again in the background; the PC is now off
		// the network, so the last good export stays merged.
		f.set(exitErr(255), "ssh: connect to host kevnnard-station port 22: No route to host")
		in, src, _ = l.Load(context.Background(), cfg, from, statusNow.Add(60*time.Second))
		if len(in.Data.Samples[config.SourceLinux]) != 60 {
			t.Errorf("background fetch dropped the cache: linux %d", len(in.Data.Samples[config.SourceLinux]))
		}
		l.bg.Wait()
		if f.count() != 2 {
			t.Fatalf("calls after 60 s = %d, want 2", f.count())
		}
		in, src, _ = l.Load(context.Background(), cfg, from, statusNow.Add(65*time.Second))
		s := source(src, config.SourceLinux)
		if f.count() != 2 || s.Online || s.Detail != "ssh: connect to host kevnnard-station port 22: No route to host" || !s.LastTS.Equal(statusNow) {
			t.Errorf("after the failed fetch: calls %d, linux %+v", f.count(), s)
		}
		if len(in.Data.Samples[config.SourceLinux]) != 60 || len(in.Data.Samples[config.SourceMac]) != 30 {
			t.Errorf("offline lost data: linux %d, mac %d", len(in.Data.Samples[config.SourceLinux]), len(in.Data.Samples[config.SourceMac]))
		}
	})

	t.Run("offline keeps local data", func(t *testing.T) {
		f := &fakeRemote{err: exitErr(255), stderr: "ssh: Could not resolve hostname kevnnard-station"}
		useStatusEdges(t, f.client)
		l := newRemoteLoader(st)
		in, src, err := l.Load(context.Background(), cfg, from, statusNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(in.Data.Samples[config.SourceMac]) != 30 || len(in.Data.Samples[config.SourceLinux]) != 0 {
			t.Errorf("offline data: mac %d, linux %d", len(in.Data.Samples[config.SourceMac]), len(in.Data.Samples[config.SourceLinux]))
		}
		if s := source(src, config.SourceLinux); s.Online || s.Detail != "ssh: Could not resolve hostname kevnnard-station" {
			t.Errorf("linux source = %+v", s)
		}
		if s := source(src, config.SourceMac); !s.Online {
			t.Errorf("mac source = %+v", s)
		}
		local := tui.NewLocal(st, config.Path(), config.TariffPath())
		local.Load = l.Load
		r, _, err := local.Refresh(context.Background(), cfg, loadTariffsT(t), statusNow.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range r.Items {
			if n.Name == "PC Linux" && (!n.Offline || n.On || n.Watts != 0) {
				t.Errorf("PC Linux now = %+v, want offline", n)
			}
		}
	})

	t.Run("remote error", func(t *testing.T) {
		f := &fakeRemote{err: exitErr(127), stderr: "bash: /home/k/.local/bin/watts: No such file or directory"}
		useStatusEdges(t, f.client)
		_, src, _ := newRemoteLoader(st).Load(context.Background(), cfg, from, statusNow)
		want := "error: remote command failed (exit 127): bash: /home/k/.local/bin/watts: No such file or directory"
		if s := source(src, config.SourceLinux); s.Online || s.Detail != want {
			t.Errorf("linux source = %+v, want detail %q", s, want)
		}
	})

	t.Run("no remote off macOS", func(t *testing.T) {
		old := goos
		goos = "linux"
		t.Cleanup(func() { goos = old })
		if c := defaultRemoteClient(cfg); c != nil {
			t.Errorf("client on linux = %+v", c)
		}
		goos = "darwin"
		if c := defaultRemoteClient(cfg); c == nil || c.Host != "kevnnard-station" {
			t.Errorf("client on darwin = %+v", c)
		}
		noPC := config.Default()
		noPC.Items = noPC.Items[:1]
		if c := defaultRemoteClient(noPC); c != nil {
			t.Errorf("client without a linux item = %+v", c)
		}
	})
}

func loadTariffsT(t *testing.T) tariff.History {
	t.Helper()
	h, err := loadTariffs()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDashboardFirstRun(t *testing.T) {
	isolate(t)
	useStatusEdges(t, func(*config.Config) *remote.Client { return nil })
	var got tea.Model
	old := runProgram
	runProgram = func(m tea.Model) error { got = m; return nil }
	t.Cleanup(func() { runProgram = old })

	code, out, errOut := runArgs()
	want := "watts: created " + config.Path() + " with the default items; edit them in the dashboard\n"
	if code != 0 || out != "" || errOut != want || got == nil {
		t.Fatalf("watts = %d, stdout %q, stderr %q, model %v", code, out, errOut, got)
	}
	for _, p := range []string{config.Path(), config.TariffPath(), config.StorePath()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("first run did not create %s: %v", p, err)
		}
	}
	if code, _, errOut := runArgs(); code != 0 || errOut != "" {
		t.Errorf("second run = %d, stderr %q", code, errOut)
	}
}
