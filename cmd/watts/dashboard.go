package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/remote"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
	"github.com/kevnnard/watts/internal/tui"
)

// RemoteEvery is the slowest cadence of the SSH fetch. A full period export
// is about 4 MB, so the 5 s dashboard refresh re-merges the cached one.
const RemoteEvery = 60 * time.Second

// Edges replaced by tests.
var (
	remoteClient = defaultRemoteClient
	isTTY        = fileIsTTY
	runProgram   = func(m tea.Model) error { _, err := tea.NewProgram(m).Run(); return err }
)

// defaultRemoteClient reads the Linux PC over SSH from the Mac, when the
// config measures one or has network items the Linux daemon asks.
// Elsewhere the store is the only source.
func defaultRemoteClient(cfg *config.Config) *remote.Client {
	if goos != "darwin" {
		return nil
	}
	for _, it := range cfg.Items {
		if (it.Mode == config.ModeMeasured || it.Mode == config.ModeNetwork) && it.Source == config.SourceLinux {
			return remote.FromConfig(cfg)
		}
	}
	return nil
}

func fileIsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// remoteLoader is the tui.Loader that merges the Linux export into the local
// store data. The first load fetches in line, so `watts status` and the
// first frame have a real answer; later loads re-merge the last good export
// and refresh it in the background at most every RemoteEvery. A failed fetch
// keeps the cached export (its totals stay counted) and reports the source
// offline or in error, never as 0 W.
type remoteLoader struct {
	st    *store.Store
	every time.Duration

	mu       sync.Mutex
	key      string // host and command of the cached export
	tried    bool
	last     time.Time // start of the last fetch, in the loader's clock (to)
	fetching bool
	status   remote.Status
	cache    remote.Export
	cached   bool
	bg       sync.WaitGroup // background fetches, for tests
}

func newRemoteLoader(st *store.Store) *remoteLoader {
	return &remoteLoader{st: st, every: RemoteEvery}
}

// Load implements tui.Loader.
func (l *remoteLoader) Load(ctx context.Context, cfg *config.Config, from, to time.Time) (energy.Input, []tui.Source, error) {
	c := remoteClient(cfg)
	if c == nil {
		in, _, err := remote.LoadInput(ctx, l.st, cfg, nil, from, to)
		if err != nil {
			return energy.Input{}, nil, err
		}
		return in, tui.DefaultSources(cfg, in, to), nil
	}
	l.mu.Lock()
	if key := c.Host + " " + c.Command; key != l.key {
		l.key, l.tried, l.fetching, l.cached = key, false, false, false
		l.status, l.cache = remote.Status{}, remote.Export{}
	}
	first := !l.tried
	if first {
		l.tried, l.last = true, to
	}
	l.mu.Unlock()

	if first {
		in, s, err := remote.LoadInput(ctx, l.st, cfg, c, from, to)
		if err != nil {
			return energy.Input{}, nil, err
		}
		l.mu.Lock()
		l.record(s)
		l.mu.Unlock()
		return in, remoteSources(cfg, in, to, s), nil
	}

	in, _, err := remote.LoadInput(ctx, l.st, cfg, nil, from, to)
	if err != nil {
		return energy.Input{}, nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cached {
		remote.Merge(&in.Data, l.cache)
	}
	if !l.fetching && to.Sub(l.last) >= l.every {
		l.fetching, l.last = true, to
		key := l.key
		l.bg.Add(1)
		go func() {
			defer l.bg.Done()
			_, s, err := remote.LoadInput(context.Background(), l.st, cfg, c, from, to)
			l.mu.Lock()
			defer l.mu.Unlock()
			if key != l.key {
				return
			}
			l.fetching = false
			if err == nil {
				l.record(s)
			}
		}()
	}
	return in, remoteSources(cfg, in, to, l.status), nil
}

// record keeps s as the current status and, when online, its export as the
// cache. l.mu must be held.
func (l *remoteLoader) record(s remote.Status) {
	if s.State == remote.StateOnline {
		l.cache, l.cached = s.Export, true
	}
	s.Export = remote.Export{}
	l.status = s
}

// remoteSources reports the sources from the data, with the Linux source
// marked offline or in error when the last fetch failed.
func remoteSources(cfg *config.Config, in energy.Input, now time.Time, s remote.Status) []tui.Source {
	src := tui.DefaultSources(cfg, in, now)
	for i := range src {
		if src[i].Name != config.SourceLinux {
			continue
		}
		switch s.State {
		case remote.StateOffline:
			src[i].Online = false
			var oe *remote.OfflineError
			if errors.As(s.Err, &oe) && oe.Reason != "" {
				src[i].Detail = oe.Reason
			}
		case remote.StateFailed:
			src[i].Online = false
			src[i].Detail = "error: " + strings.TrimPrefix(s.Err.Error(), s.Host+": ")
		}
	}
	return src
}

// openLocal opens the store and the local Data over it with the remote merge.
func openLocal() (*store.Store, *tui.Local, error) {
	st, err := store.Open(config.StorePath())
	if err != nil {
		return nil, nil, err
	}
	l := tui.NewLocal(st, config.Path(), config.TariffPath())
	l.Load = newRemoteLoader(st).Load
	return st, l, nil
}

func runDashboard(_ []string, _, stderr io.Writer) int {
	cfg, created, err := config.LoadOrCreate(config.Path())
	if err != nil {
		fmt.Fprintf(stderr, "watts: %v\n", err)
		return 1
	}
	if created {
		fmt.Fprintf(stderr, "watts: created %s with the default items; edit them in the dashboard\n", config.Path())
	}
	h, _, err := tariff.LoadOrCreate(config.TariffPath())
	if err != nil {
		fmt.Fprintf(stderr, "watts: %v\n", err)
		return 1
	}
	st, l, err := openLocal()
	if err != nil {
		fmt.Fprintf(stderr, "watts: %v\n", err)
		return 1
	}
	defer st.Close()
	if err := runProgram(tui.New(l, l, cfg, h)); err != nil {
		fmt.Fprintf(stderr, "watts: %v\n", err)
		return 1
	}
	return 0
}

// loadTariffs reads the tariff history, or the seed when there is none. Like
// loadConfig it never writes.
func loadTariffs() (tariff.History, error) {
	path := config.TariffPath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return tariff.Seed(), nil
	}
	return tariff.Load(path)
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	if code, ok := parseFlags("status", args, stdout, stderr, noFlags); !ok {
		return code
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "watts status: %v\n", err)
		return 1
	}
	h, err := loadTariffs()
	if err != nil {
		fmt.Fprintf(stderr, "watts status: %v\n", err)
		return 1
	}
	st, l, err := openLocal()
	if err != nil {
		fmt.Fprintf(stderr, "watts status: %v\n", err)
		return 1
	}
	defer st.Close()
	r, src, err := l.Refresh(context.Background(), cfg, h, now())
	if err != nil {
		fmt.Fprintf(stderr, "watts status: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, tui.StatusText(r, src, cfg, isTTY(stdout)))
	return 0
}
