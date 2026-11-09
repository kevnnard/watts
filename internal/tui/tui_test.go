package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// newFix returns a model loaded with the artboard fixture at w × h.
func newFix(t *testing.T, offline bool, w, h int) (*Model, *fakeData, *fakeEditor) {
	t.Helper()
	return newFixData(t, &fakeData{offline: offline}, fixConfig(), w, h)
}

// newFixData is newFix with a given fake and config.
func newFixData(t *testing.T, d *fakeData, cfg *config.Config, w, h int) (*Model, *fakeData, *fakeEditor) {
	t.Helper()
	// never the real network: n scans the artboard fixture
	return newFixScan(t, d, cfg, &fakeScan{res: fixScan()}, w, h)
}

// newFixScan is newFixData with a given scanner.
func newFixScan(t *testing.T, d *fakeData, cfg *config.Config, sc *fakeScan, w, h int) (*Model, *fakeData, *fakeEditor) {
	t.Helper()
	e := &fakeEditor{}
	m := New(d, e, cfg, fixHistory(), WithClock(func() time.Time { return fixNow }), WithScanner(sc.scan), WithLocalSource(config.SourceMac))
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	run(t, m, m.Init())
	if !m.loaded {
		t.Fatalf("fixture did not load: %s", m.loadErr)
	}
	return m, d, e
}

// run executes cmd and feeds save and refresh results back into m. It never
// runs the refresh tick, which would sleep.
func run(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for cmd != nil {
		switch msg := cmd().(type) {
		case savedMsg:
			_, cmd = m.Update(msg)
		case refreshMsg:
			m.Update(msg)
			return
		case tea.BatchMsg:
			// a network scan and its tick: apply the scan, drop the tick
			m.Update(<-scanOf(t, func() tea.Msg { return msg }))
			return
		default:
			m.Update(msg)
			return
		}
	}
}

// press sends keys: single characters, or names such as "enter", "esc",
// "up", "down", "tab", "space", "backspace".
func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, cmd := m.Update(keyMsg(k))
		run(t, m, cmd)
	}
}

// typeText sends each rune of s as a key press.
func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		run(t, m, cmd)
	}
}

func keyMsg(k string) tea.KeyPressMsg {
	named := map[string]rune{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "up": tea.KeyUp, "down": tea.KeyDown,
		"left": tea.KeyLeft, "right": tea.KeyRight, "tab": tea.KeyTab, "backspace": tea.KeyBackspace, "space": tea.KeySpace,
	}
	if k == "ctrl+u" {
		return tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl})
	}
	if k == "shift+tab" {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
	}
	if c, ok := named[k]; ok {
		key := tea.Key{Code: c}
		if k == "space" {
			key.Text = " "
		}
		return tea.KeyPressMsg(key)
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg(tea.Key{Code: r, Text: k})
}

// screenText is the plain frame.
func screenText(m *Model) string { return m.draw().text() }

// checkSize asserts the styled frame is exactly w × h cells.
func checkSize(t *testing.T, m *Model, w, h int) {
	t.Helper()
	for name, out := range map[string]string{"styled": m.Render(), "plain": screenText(m)} {
		lines, widest := lineWidths(out)
		if lines != h {
			t.Errorf("%dx%d %s: %d lines, want %d", w, h, name, lines, h)
		}
		if widest > w {
			t.Errorf("%dx%d %s: a line is %d cells wide, want ≤ %d", w, h, name, widest, w)
		}
		for i, l := range strings.Split(out, "\n") {
			if name == "plain" && textWidth(l) != w {
				t.Errorf("%dx%d plain line %d is %d cells, want %d", w, h, i, textWidth(l), w)
				break
			}
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run go test -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s; run go test ./internal/tui -update and review the diff\n--- got\n%s", name, path, got)
	}
}

// screens are the fixture states the golden tests and the preview render.
var screens = []struct {
	name    string
	offline bool
	ddc     bool // the LG is driven by a fresh DDC reading
	w, h    int
	keys    []string
	text    string // typed after keys
	more    []string
	net     bool                  // the network fixture: TV in standby, PS5 unknown
	cfg     func() *config.Config // instead of the fixture config
	scan    *fakeScan             // instead of the artboard scan
	pending string                // a key whose scan is left running: "n" or "r"
}{
	{name: "dash-140x40", w: 140, h: 40},
	{name: "dash-100x30", w: 100, h: 30},
	{name: "dash-80x24", w: 80, h: 24},
	{name: "offline-140x40", offline: true, w: 140, h: 40},
	{name: "offline-80x24", offline: true, w: 80, h: 24},
	{name: "ddc-140x40", ddc: true, w: 140, h: 40},
	{name: "ddc-80x24", ddc: true, w: 80, h: 24},
	{name: "add-results-140x40", w: 140, h: 40, keys: []string{"a"}, text: "lg"},
	{name: "add-empty-140x40", w: 140, h: 40, keys: []string{"a"}, text: "lgx"},
	{name: "add-label-140x40", w: 140, h: 40, keys: []string{"a", "tab"}, text: "45"},
	{name: "add-mode-140x40", w: 140, h: 40, keys: []string{"a"}, text: "29wq600", more: []string{"enter"}},
	{name: "add-results-80x24", w: 80, h: 24, keys: []string{"a"}, text: "lg"},
	{name: "bill-140x40", w: 140, h: 40, keys: []string{"b"}},
	{name: "bill-100x30", w: 100, h: 30, keys: []string{"b"}},
	{name: "bill-80x24", w: 80, h: 24, keys: []string{"b"}},
	{name: "bill-offline-140x40", offline: true, w: 140, h: 40, keys: []string{"b"}},
	{name: "tariffs-140x40", w: 140, h: 40, keys: []string{"t"}},
	{name: "currency-80x24", w: 80, h: 24, keys: []string{"c"}},
	{name: "delete-80x24", w: 80, h: 24, keys: []string{"d"}},
	{name: "small-79x24", w: 79, h: 24},
	{name: "net-dash-140x40", net: true, w: 140, h: 40},
	{name: "net-dash-100x30", net: true, w: 100, h: 30, keys: []string{"j", "j", "j", "j", "j", "j"}},
	{name: "net-dash-80x24", net: true, w: 80, h: 24, keys: []string{"j", "j", "j", "j", "j", "j"}},
	{name: "net-bill-140x40", net: true, w: 140, h: 40, keys: []string{"b"}},
	{name: "net-bill-80x24", net: true, w: 80, h: 24, keys: []string{"b"}},
	{name: "net-edit-140x40", net: true, w: 140, h: 40, keys: []string{"j", "j", "j", "j", "j", "e"}},
	{name: "netpanel-loading-140x40", w: 140, h: 40, pending: "n"},
	{name: "netpanel-loading-100x30", w: 100, h: 30, pending: "n"},
	{name: "netpanel-loading-80x24", w: 80, h: 24, pending: "n"},
	{name: "netpanel-results-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n"}},
	{name: "netpanel-results-100x30", cfg: ps5Config, w: 100, h: 30, keys: []string{"n"}},
	{name: "netpanel-results-80x24", cfg: ps5Config, w: 80, h: 24, keys: []string{"n"}},
	{name: "netpanel-added-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n", "down", "enter"}},
	{name: "netpanel-rescan-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n", "down"}, pending: "r"},
	{name: "netpanel-rescan-80x24", cfg: ps5Config, w: 80, h: 24, keys: []string{"n", "down"}, pending: "r"},
	{name: "netpanel-maybe-140x40", cfg: tvAtConfig, w: 140, h: 40, keys: []string{"n"}},
	{name: "netpanel-maybe-80x24", cfg: tvAtConfig, w: 80, h: 24, keys: []string{"n"}},
	{name: "netpanel-error-140x40", scan: &fakeScan{err: errNoIface}, w: 140, h: 40, keys: []string{"n"}},
	{name: "netpanel-error-100x30", scan: &fakeScan{err: errNoIface}, w: 100, h: 30, keys: []string{"n"}},
	{name: "netpanel-error-80x24", scan: &fakeScan{err: errNoIface}, w: 80, h: 24, keys: []string{"n"}},
	{name: "netpanel-empty-140x40", scan: &fakeScan{res: NetScan{Interfaces: fixScan().Interfaces}}, w: 140, h: 40, keys: []string{"n"}},
	{name: "netpanel-empty-80x24", scan: &fakeScan{res: NetScan{Interfaces: fixScan().Interfaces}}, w: 80, h: 24, keys: []string{"n"}},
	{name: "netpanel-add-label-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n", "enter"}},
	{name: "netpanel-add-label-80x24", cfg: ps5Config, w: 80, h: 24, keys: []string{"n", "enter"}},
	{name: "netpanel-add-mode-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n", "enter"}, text: "80", more: []string{"enter"}},
	{name: "netpanel-add-name-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n", "enter"}, text: "80", more: []string{"enter", "enter", "tab", "tab", "tab"}},
	{name: "netpanel-add-router-140x40", cfg: ps5Config, w: 140, h: 40, keys: []string{"n", "down", "down", "enter"}, text: "9", more: []string{"enter"}},
}

func screenModel(t *testing.T, i int) *Model {
	sc := screens[i]
	cfg := fixConfig()
	switch {
	case sc.ddc:
		cfg = fixDDCConfig()
	case sc.net:
		cfg = fixNetConfig()
	case sc.cfg != nil:
		cfg = sc.cfg()
	}
	scan := &fakeScan{res: fixScan()}
	if sc.scan != nil {
		scan = &fakeScan{res: sc.scan.res, err: sc.scan.err}
	}
	m, _, _ := newFixScan(t, &fakeData{offline: sc.offline, ddc: sc.ddc, net: sc.net}, cfg, scan, sc.w, sc.h)
	press(t, m, sc.keys...)
	typeText(t, m, sc.text)
	press(t, m, sc.more...)
	if sc.pending != "" {
		m.Update(keyMsg(sc.pending)) // the scan command is never run
	}
	return m
}

func TestGoldenScreens(t *testing.T) {
	for i, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			m := screenModel(t, i)
			checkSize(t, m, sc.w, sc.h)
			golden(t, sc.name, screenText(m))
		})
	}
}

// Nothing overflows or wraps at any width ≥ 80, with or without a modal.
func TestNoOverflowAnySize(t *testing.T) {
	for _, keys := range [][]string{nil, {"a"}, {"a", "tab"}, {"b"}, {"t"}, {"t", "a"}, {"c"}, {"d"}, {"e"}, {"e", "enter"}, {"n"}, {"n", "enter"}, {"n", "down", "enter", "tab", "tab"}, {"n", "enter", "tab", "enter"}} {
		for _, off := range []bool{false, true} {
			for w := 80; w <= 170; w += 3 {
				for _, h := range []int{24, 27, 30, 39, 40, 44} {
					m, _, _ := newFix(t, off, w, h)
					press(t, m, keys...)
					checkSize(t, m, w, h)
				}
			}
		}
	}
}

func TestTooSmall(t *testing.T) {
	for _, sz := range [][2]int{{79, 24}, {80, 23}, {40, 10}} {
		m, _, _ := newFix(t, false, sz[0], sz[1])
		out := screenText(m)
		if !strings.Contains(out, "Terminal too small") {
			t.Errorf("%v: no too-small message:\n%s", sz, out)
		}
		checkSize(t, m, sz[0], sz[1])
	}
}

// View must not touch the data source and must reuse its frame.
func TestViewCachedAndNoIO(t *testing.T) {
	m, d, _ := newFix(t, false, 140, 40)
	calls := d.calls
	a := m.View().Content
	b := m.View().Content
	if a != b || d.calls != calls {
		t.Fatalf("View recomputed or refreshed: calls %d → %d", calls, d.calls)
	}
	key := m.cacheKey
	m.View()
	if m.cacheKey != key {
		t.Fatal("cache key changed without a state change")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if c := m.View().Content; c == a {
		t.Fatal("resize did not re-render")
	}
}
