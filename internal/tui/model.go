// Package tui is the Bubble Tea dashboard (S10, S15): the 01 cockpit from
// design/usage-dashboard/01-cockpit.html, its add-item modal and narrow
// layout, plus the bill modal from 02-bill-first.html on `b`.
//
// The model does no I/O: data arrives through Data on a refresh tick and
// edits leave through Editor, both run as commands. View only lays out a
// board derived when the data changes, and caches the frame by state, width
// and height.
package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/energy"
	"github.com/kevnnard/watts/internal/tariff"
)

// RefreshInterval is how often the dashboard reloads its data.
const RefreshInterval = 5 * time.Second

// MinWidth and MinHeight are the smallest supported terminal; below it the
// dashboard shows a "terminal too small" message.
const (
	MinWidth   = 80
	MinHeight  = 24
	FullWidth  = 140 // the full cockpit from here up
	FullHeight = 40
)

// Option configures a Model.
type Option func(*Model)

// WithClock replaces time.Now, for tests and the preview harness.
func WithClock(now func() time.Time) Option { return func(m *Model) { m.now = now } }

// WithTimeout bounds each refresh (default 20 s).
func WithTimeout(d time.Duration) Option { return func(m *Model) { m.timeout = d } }

// Model is the dashboard.
type Model struct {
	data    Data
	edit    Editor
	cfg     *config.Config
	hist    tariff.History
	now     func() time.Time
	timeout time.Duration

	w, h int

	loaded  bool
	report  energy.Report
	sources []Source
	trend   Trend
	trendOK bool
	loadErr string
	b       *board

	sel   int
	modal modal

	flash    string
	flashErr bool

	seq      int // refresh sequence; stale results are dropped
	inFlight bool

	ver      int // bumped on every state change
	cacheKey [3]int
	cache    string
}

// New returns the dashboard over data and edit, starting from cfg and the
// tariff history h (both already loaded by the caller).
func New(data Data, edit Editor, cfg *config.Config, h tariff.History, opts ...Option) *Model {
	m := &Model{data: data, edit: edit, cfg: cloneConfig(cfg), hist: cloneHistory(h), now: time.Now, timeout: 20 * time.Second, cacheKey: [3]int{-1, -1, -1}}
	for _, o := range opts {
		o(m)
	}
	m.rebuild()
	return m
}

// Config is the model's current config (after edits).
func (m *Model) Config() *config.Config { return cloneConfig(m.cfg) }

// Tariffs is the model's current tariff history (after edits).
func (m *Model) Tariffs() tariff.History { return cloneHistory(m.hist) }

type tickMsg struct{}

type refreshMsg struct {
	seq     int
	report  energy.Report
	sources []Source
	trend   Trend
	trendOK bool
	err     error
}

// savedMsg reports an Editor call.
type savedMsg struct {
	kind  string // "config", "tariffs", "toggle"
	cfg   *config.Config
	hist  tariff.History
	flash string
	err   error
}

// Init starts the first refresh.
func (m *Model) Init() tea.Cmd { return m.refresh() }

func (m *Model) refresh() tea.Cmd {
	m.seq++
	m.inFlight = true
	seq, data, cfg, h, now, timeout := m.seq, m.data, cloneConfig(m.cfg), cloneHistory(m.hist), m.now(), m.timeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		msg := refreshMsg{seq: seq}
		if td, ok := data.(TrendData); ok {
			msg.report, msg.sources, msg.trend, msg.err = td.RefreshTrend(ctx, cfg, h, now)
			msg.trendOK = msg.err == nil
		} else {
			msg.report, msg.sources, msg.err = data.Refresh(ctx, cfg, h, now)
		}
		return msg
	}
}

func tick() tea.Cmd {
	return tea.Tick(RefreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.ver++
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, m.refresh()
	case refreshMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.inFlight = false
		if msg.err != nil {
			m.loadErr = "refresh failed: " + msg.err.Error()
			return m, tick()
		}
		m.loadErr = ""
		m.loaded = true
		m.report, m.sources, m.trend, m.trendOK = msg.report, msg.sources, msg.trend, msg.trendOK
		m.rebuild()
		return m, tick()
	case savedMsg:
		return m.saved(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	m.ver--
	return m, nil
}

func (m *Model) rebuild() {
	m.b = newBoard(m.cfg, m.report, m.sources, m.trend, m.trendOK)
	if m.sel >= len(m.cfg.Items) {
		m.sel = len(m.cfg.Items) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

func (m *Model) saved(msg savedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.modal != nil {
			m.modal.setErr(msg.err.Error())
		} else {
			m.setFlash(msg.err.Error(), true)
		}
		return m, nil
	}
	switch msg.kind {
	case "config":
		m.cfg = msg.cfg
	case "tariffs":
		m.hist = msg.hist
	}
	if _, keep := m.modal.(*tariffModal); keep && msg.kind == "tariffs" {
		m.modal.(*tariffModal).saved(m.hist)
	} else {
		m.modal = nil
	}
	m.setFlash(msg.flash, false)
	m.rebuild()
	return m, m.refresh()
}

func (m *Model) setFlash(s string, isErr bool) { m.flash, m.flashErr = s, isErr }

func (m *Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.modal != nil {
		return m, m.modal.key(m, k)
	}
	m.flash = ""
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(m.cfg.Items)-1 {
			m.sel++
		}
	case "a":
		m.modal = newAddForm(m)
	case "e":
		if it, ok := m.selected(); ok {
			m.modal = newEditForm(m, it)
		}
	case "d":
		if it, ok := m.selected(); ok {
			m.modal = &confirmDelete{name: it.Name}
		}
	case "space":
		return m, m.toggle()
	case "t":
		m.modal = newTariffModal(m)
	case "c":
		m.modal = newCurrencyModal(m)
	case "b":
		m.modal = &billModal{}
	}
	return m, nil
}

func (m *Model) selected() (config.Item, bool) {
	if m.sel < 0 || m.sel >= len(m.cfg.Items) {
		return config.Item{}, false
	}
	return m.cfg.Items[m.sel], true
}

func (m *Model) toggle() tea.Cmd {
	it, ok := m.selected()
	if !ok {
		return nil
	}
	if it.Mode != config.ModeManual {
		m.setFlash("space toggles manual items only; "+it.Name+" is "+string(it.Mode), true)
		return nil
	}
	on := true
	for _, rw := range m.b.rows {
		if rw.it.Name == it.Name && rw.state == stateOn {
			on = false
		}
	}
	edit, at, name := m.edit, m.now(), it.Name
	return func() tea.Msg {
		state := "off"
		if on {
			state = "on"
		}
		return savedMsg{kind: "toggle", flash: name + " turned " + state, err: edit.SetManual(name, on, at)}
	}
}

// saveConfig validates next and persists it through the Editor. A validation
// error is returned as the exact config.Validate message and nothing is
// saved.
func (m *Model) saveConfig(next *config.Config, flash string) (tea.Cmd, string) {
	if err := next.Validate(); err != nil {
		return nil, err.Error()
	}
	edit := m.edit
	return func() tea.Msg {
		return savedMsg{kind: "config", cfg: next, flash: flash, err: edit.SaveConfig(next)}
	}, ""
}

func (m *Model) saveTariffs(next tariff.History, flash string) (tea.Cmd, string) {
	if err := next.Validate(); err != nil {
		return nil, err.Error()
	}
	edit := m.edit
	return func() tea.Msg {
		return savedMsg{kind: "tariffs", hist: next, flash: flash, err: edit.SaveTariffs(next)}
	}, ""
}

// View renders the frame, cached by state version and size.
func (m *Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	return v
}

// Render is the styled frame as a string.
func (m *Model) Render() string {
	key := [3]int{m.ver, m.w, m.h}
	if key != m.cacheKey {
		m.cache = m.draw().render()
		m.cacheKey = key
	}
	return m.cache
}

func cloneConfig(c *config.Config) *config.Config {
	if c == nil {
		return config.Default()
	}
	out := *c
	out.Items = make([]config.Item, len(c.Items))
	for i, it := range c.Items {
		it.Follows = append([]string(nil), it.Follows...)
		it.Schedule = append([]string(nil), it.Schedule...)
		out.Items[i] = it
	}
	return &out
}

func cloneHistory(h tariff.History) tariff.History {
	h.Tariffs = append([]tariff.Tariff(nil), h.Tariffs...)
	return h
}
