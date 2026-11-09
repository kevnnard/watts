package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/tariff"
)

func lastConfig(t *testing.T, e *fakeEditor) *config.Config {
	t.Helper()
	if len(e.cfgs) == 0 {
		t.Fatal("no config was saved")
	}
	return e.cfgs[len(e.cfgs)-1]
}

func formErr(m *Model) string {
	switch f := m.modal.(type) {
	case *itemForm:
		switch f.step {
		case stepLabel:
			return f.label.err
		case stepMode:
			return f.mode.err
		}
		return f.name.err
	case *tariffModal:
		if f.adding {
			return f.form.err
		}
		return f.err
	case *currencyModal:
		return f.form.err
	case *confirmDelete:
		return f.err
	}
	return ""
}

func selectItem(t *testing.T, m *Model, name string) {
	t.Helper()
	for i, it := range m.cfg.Items {
		if it.Name == name {
			m.sel = i
			return
		}
	}
	t.Fatalf("no item %q", name)
}

func TestAddFromCatalog(t *testing.T) {
	m, d, e := newFix(t, false, 140, 40)
	calls := d.calls
	press(t, m, "a")
	typeText(t, m, "29wq600")
	f := m.modal.(*itemForm)
	if len(f.results) != 1 || f.results[0].Model != "29WQ600" {
		t.Fatalf("search results = %+v", f.results)
	}
	press(t, m, "enter") // step 2: a monitor follows the machines
	if f.step != stepMode || f.mode.fields[fMode].value != "follows" {
		t.Fatalf("step %v mode %q, want mode step with follows", f.step, f.mode.fields[fMode].value)
	}
	press(t, m, "enter", "enter") // step 3, then save
	if m.modal != nil {
		t.Fatalf("modal still open: %q", formErr(m))
	}
	got := lastConfig(t, e).Items[5]
	want := config.Item{Name: "LG 29WQ600 2", Mode: config.ModeFollows, Watts: 22, Quantity: 1, Brand: "LG", Model: "29WQ600", Follows: []string{"PC Linux", "Mac"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v\nwant  %+v", got, want)
	}
	if len(m.Config().Items) != 6 || m.sel != 5 || m.flash != "LG 29WQ600 2 added" {
		t.Fatalf("items %d sel %d flash %q", len(m.Config().Items), m.sel, m.flash)
	}
	if d.calls != calls+1 {
		t.Fatalf("no refresh after the edit: %d calls", d.calls-calls)
	}
}

func TestAddCatalogNoResultsGoesToLabel(t *testing.T) {
	m, _, _ := newFix(t, false, 140, 40)
	press(t, m, "a")
	typeText(t, m, "lgx")
	if out := screenText(m); !strings.Contains(out, `No catalog devices match "lgx"`) || !strings.Contains(out, "0 matches") {
		t.Fatalf("no-results state missing:\n%s", out)
	}
	press(t, m, "enter")
	if f := m.modal.(*itemForm); f.step != stepLabel {
		t.Fatalf("step = %v, want label", f.step)
	}
	press(t, m, "esc")
	if f := m.modal.(*itemForm); f.step != stepCatalog || f.query.value != "lgx" {
		t.Fatalf("back to catalog lost the search: %+v", f.query)
	}
}

func TestAddManualWatts(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	press(t, m, "a", "tab")
	typeText(t, m, "45")
	press(t, m, "tab", "backspace")
	typeText(t, m, "2")
	press(t, m, "tab")
	typeText(t, m, "fan")
	press(t, m, "tab")
	typeText(t, m, "Samurai")
	press(t, m, "enter") // step 2: a fan is manual
	f := m.modal.(*itemForm)
	if f.step != stepMode || f.mode.fields[fMode].value != "manual" {
		t.Fatalf("step %v mode %q", f.step, f.mode.fields[fMode].value)
	}
	press(t, m, "enter", "ctrl+u")
	typeText(t, m, "Ceiling fan")
	press(t, m, "enter")
	got := lastConfig(t, e).Items[5]
	want := config.Item{Name: "Ceiling fan", Mode: config.ModeManual, Watts: 45, Quantity: 2, Brand: "Samurai"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v\nwant  %+v", got, want)
	}
}

func TestAddScheduleMode(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	press(t, m, "a", "tab")
	typeText(t, m, "9,5")
	press(t, m, "enter", "right") // manual → measured
	press(t, m, "left", "left")   // → schedule
	f := m.modal.(*itemForm)
	if f.mode.fields[fMode].value != "schedule" || f.mode.fields[fHours].hidden {
		t.Fatalf("mode %q, hours hidden %v", f.mode.fields[fMode].value, f.mode.fields[fHours].hidden)
	}
	press(t, m, "tab", "ctrl+u")
	typeText(t, m, "22:00-02:00")
	press(t, m, "enter", "ctrl+u")
	typeText(t, m, "Porch")
	press(t, m, "enter")
	got := lastConfig(t, e).Items[5]
	if got.Mode != config.ModeSchedule || got.Watts != 9.5 || !reflect.DeepEqual(got.Schedule, []string{"22:00-02:00"}) {
		t.Fatalf("added %+v", got)
	}
}

func TestAddValidationErrors(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	press(t, m, "a", "tab")
	typeText(t, m, "0")
	press(t, m, "enter")
	if got := formErr(m); got != "watts must be greater than 0" {
		t.Fatalf("label error = %q", got)
	}
	press(t, m, "backspace")
	typeText(t, m, "20")
	press(t, m, "enter", "right", "right") // manual → measured → follows
	press(t, m, "tab", "ctrl+u")
	typeText(t, m, "Nope")
	press(t, m, "enter", "ctrl+u")
	typeText(t, m, "Lamp")
	press(t, m, "enter")
	if got, want := formErr(m), `item "Lamp": follows unknown item "Nope"`; got != want {
		t.Fatalf("error = %q, want the config.Validate message %q", got, want)
	}
	if len(e.cfgs) != 0 || len(m.Config().Items) != 5 {
		t.Fatalf("an invalid item was saved")
	}
	if !strings.Contains(screenText(m), `follows unknown item "Nope"`) {
		t.Fatal("error not shown inline")
	}
}

func TestEditRenamesFollowers(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	selectItem(t, m, "Mac")
	press(t, m, "e")
	f := m.modal.(*itemForm)
	if f.step != stepMode || f.mode.fields[fMode].value != "measured" || f.mode.fields[fSource].value != "mac" {
		t.Fatalf("edit form: step %v mode %q source %q", f.step, f.mode.fields[fMode].value, f.mode.fields[fSource].value)
	}
	press(t, m, "enter", "ctrl+u")
	typeText(t, m, "MacBook")
	press(t, m, "enter")
	c := lastConfig(t, e)
	if c.Items[1].Name != "MacBook" || !reflect.DeepEqual(c.Items[2].Follows, []string{"MacBook", "PC Linux"}) {
		t.Fatalf("items after rename: %+v", c.Items)
	}
	if m.flash != "MacBook saved" {
		t.Fatalf("flash %q", m.flash)
	}
}

// The form has no DDC fields, so editing a ddc item keeps them.
func TestEditKeepsDDC(t *testing.T) {
	cfg := fixDDCConfig()
	cfg.Items[2].MaxW = 24
	m, _, e := newFixData(t, &fakeData{ddc: true}, cfg, 140, 40)
	selectItem(t, m, "LG 29WQ600")
	press(t, m, "e", "enter", "ctrl+u")
	typeText(t, m, "LG")
	press(t, m, "enter")
	c := lastConfig(t, e)
	if lg := c.Items[2]; lg.Name != "LG" || lg.DDC != "linux" || lg.MaxW != 24 {
		t.Fatalf("LG after edit = %+v", lg)
	}
}

func TestDeleteConfirm(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	press(t, m, "down", "down", "down", "down") // Desk fan
	press(t, m, "d")
	if _, ok := m.modal.(*confirmDelete); !ok {
		t.Fatal("d did not ask for confirmation")
	}
	if !strings.Contains(screenText(m), "Delete Desk fan?") {
		t.Fatal("confirm text missing")
	}
	press(t, m, "n")
	if m.modal != nil || len(e.cfgs) != 0 {
		t.Fatal("n must keep the item")
	}
	press(t, m, "d", "y")
	c := lastConfig(t, e)
	if len(c.Items) != 4 {
		t.Fatalf("items after delete: %d", len(c.Items))
	}
	if _, ok := c.Item("Desk fan"); ok {
		t.Fatal("Desk fan still present")
	}
	if m.sel != 3 {
		t.Fatalf("selection = %d, want the new last item", m.sel)
	}
}

func TestDeleteFollowedItemRefused(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	selectItem(t, m, "Mac")
	press(t, m, "d", "y")
	if got, want := formErr(m), `item "LG 29WQ600": follows unknown item "Mac"`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if len(e.cfgs) != 0 || len(m.Config().Items) != 5 {
		t.Fatal("data changed")
	}
}

func TestToggleManual(t *testing.T) {
	m, d, e := newFix(t, false, 140, 40)
	calls := d.calls
	selectItem(t, m, "Desk fan")
	press(t, m, "space")
	if !reflect.DeepEqual(e.toggles, []string{"Desk fan on"}) {
		t.Fatalf("toggles = %v", e.toggles)
	}
	if d.calls != calls+1 || m.flash != "Desk fan turned on" {
		t.Fatalf("calls %d flash %q", d.calls-calls, m.flash)
	}
	selectItem(t, m, "Mac")
	press(t, m, "space")
	if len(e.toggles) != 1 || !m.flashErr || !strings.Contains(m.flash, "manual items only") {
		t.Fatalf("space on a measured item: toggles %v flash %q", e.toggles, m.flash)
	}
}

func TestTariffAddInvalidPrice(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	before := m.Tariffs()
	press(t, m, "t")
	if !strings.Contains(screenText(m), "$990,53") {
		t.Fatal("tariff history not listed")
	}
	press(t, m, "a")
	typeText(t, m, "abc")
	press(t, m, "enter")
	if got, want := formErr(m), "tariff Enel 2026-11-08: price_per_kwh must be greater than 0"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(m.Tariffs(), before) || len(e.hists) != 0 {
		t.Fatal("an invalid tariff changed the data")
	}
	if !strings.Contains(screenText(m), "price_per_kwh must be greater than 0") {
		t.Fatal("error not shown inline")
	}
	// a bad cut-off day reports the history validator's message
	press(t, m, "ctrl+u")
	typeText(t, m, "1020,5")
	press(t, m, "tab", "tab", "ctrl+u")
	typeText(t, m, "40")
	press(t, m, "enter")
	if got, want := formErr(m), "tariffs: cutoff_day must be between 1 and 31, got 40"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	press(t, m, "backspace", "backspace")
	typeText(t, m, "5")
	press(t, m, "enter")
	if len(e.hists) != 1 {
		t.Fatal("valid tariff not saved")
	}
	want := tariff.History{CutoffDay: 5, Tariffs: append(append([]tariff.Tariff(nil), before.Tariffs...), tariff.Tariff{Provider: "Enel", PricePerKWh: 1020.5, Start: "2026-11-08"})}
	if !reflect.DeepEqual(m.Tariffs(), want) {
		t.Fatalf("history = %+v\nwant      %+v", m.Tariffs(), want)
	}
	if tm, ok := m.modal.(*tariffModal); !ok || tm.adding {
		t.Fatal("after saving, the tariff list should stay open")
	}
	press(t, m, "esc")
	if m.modal != nil {
		t.Fatal("esc did not close the tariffs")
	}
}

func TestCurrencyModal(t *testing.T) {
	m, _, e := newFix(t, false, 80, 24)
	press(t, m, "c")
	if _, ok := m.modal.(*currencyModal); !ok {
		t.Fatal("c did not open the currency modal")
	}
	press(t, m, "esc")
	if m.modal != nil || len(e.cfgs) != 0 {
		t.Fatal("esc must close without saving")
	}
	press(t, m, "c", "tab", "tab", "ctrl+u", "enter")
	if got, want := formErr(m), "currency: rate must be greater than 0 when secondary is set"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	typeText(t, m, "4100")
	press(t, m, "enter")
	if c := lastConfig(t, e); c.Currency != (config.Currency{Main: "COP", Secondary: "USD", Rate: 4100}) || m.modal != nil {
		t.Fatalf("currency = %+v, modal %T", c.Currency, m.modal)
	}
}

func TestBillModal(t *testing.T) {
	for _, sz := range [][2]int{{140, 40}, {80, 24}} {
		m, _, _ := newFix(t, false, sz[0], sz[1])
		press(t, m, "b")
		if _, ok := m.modal.(*billModal); !ok {
			t.Fatal("b did not open the bill")
		}
		out := screenText(m)
		for _, want := range []string{"projected $29.100 by Dec 1", "≈ US$2.43 so far · US$7.28 projected", "▼ today, day 8", "55 %", "█▀█"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: bill lacks %q:\n%s", sz, want, out)
			}
		}
		press(t, m, "esc")
		if m.modal != nil {
			t.Fatal("esc did not close the bill")
		}
	}
}

func TestOfflineRendering(t *testing.T) {
	for _, sz := range [][2]int{{140, 40}, {100, 30}, {80, 24}} {
		m, _, _ := newFix(t, true, sz[0], sz[1])
		out := screenText(m)
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "PC Linux") && strings.Contains(l, "measured") {
				if !strings.Contains(l, "offline") || strings.Contains(l, " 0 W") {
					t.Errorf("%v: offline row must say offline, never 0 W: %q", sz, l)
				}
			}
		}
		if !strings.Contains(out, "✗ offline") || !strings.Contains(out, "partial") {
			t.Errorf("%v: offline state not shown:\n%s", sz, out)
		}
	}
}

func TestEstimatedStyle(t *testing.T) {
	m, _, _ := newFix(t, false, 140, 40)
	for _, rw := range m.b.rows {
		text, sty := m.b.wattsText(rw)
		switch rw.it.Name {
		case "LG 29WQ600":
			if text != "~25,0 W" || sty.at&italic == 0 {
				t.Errorf("estimated = %q %+v, want ~ and italic", text, sty)
			}
		case "PC Linux":
			if text != "70,9 W" || sty.at&italic != 0 {
				t.Errorf("measured = %q %+v, want no ~ and upright", text, sty)
			}
		}
	}
}

func TestRefreshErrorAndStale(t *testing.T) {
	m, d, _ := newFix(t, false, 140, 40)
	d.err = errDisk
	_, cmd := m.Update(tickMsg{})
	stale := cmd()
	_, cmd = m.Update(tickMsg{})
	run(t, m, cmd)
	if m.loadErr != "refresh failed: disk full" || !m.loaded {
		t.Fatalf("loadErr %q loaded %v", m.loadErr, m.loaded)
	}
	if !strings.Contains(screenText(m), "refresh failed: disk full") {
		t.Fatal("refresh error not shown")
	}
	d.err = nil
	m.Update(stale) // an older refresh must not overwrite a newer one
	if m.loadErr == "" {
		t.Fatal("stale refresh result was applied")
	}
}

func TestSaveErrorKeepsModal(t *testing.T) {
	m, _, e := newFix(t, false, 140, 40)
	e.err = errDisk
	press(t, m, "c", "enter")
	if _, ok := m.modal.(*currencyModal); !ok || formErr(m) != "disk full" {
		t.Fatalf("modal %T err %q", m.modal, formErr(m))
	}
}

func TestQuitAndSelection(t *testing.T) {
	m, _, _ := newFix(t, false, 140, 40)
	press(t, m, "j", "j", "k")
	if m.sel != 1 {
		t.Fatalf("sel = %d", m.sel)
	}
	press(t, m, "up", "up", "up")
	if m.sel != 0 {
		t.Fatalf("sel = %d", m.sel)
	}
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
	// q inside a text field types, esc closes
	press(t, m, "a", "q")
	if f := m.modal.(*itemForm); f.query.value != "q" {
		t.Fatalf("query = %q", f.query.value)
	}
}
