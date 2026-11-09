package tui

import (
	"reflect"
	"strings"
	"testing"
)

// Network items in the cockpit, the bill, status and the edit form (S20).

func newNetFix(t *testing.T, w, h int) (*Model, *fakeData, *fakeEditor) {
	t.Helper()
	return newFixData(t, &fakeData{net: true}, fixNetConfig(), w, h)
}

// Editing a network item keeps every field the form does not change.
func TestEditNetworkItemKeepsFields(t *testing.T) {
	m, _, e := newNetFix(t, 140, 40)
	selectItem(t, m, "TV")
	press(t, m, "e")
	f := m.modal.(*itemForm)
	if f.step != stepMode || f.mode.fields[fMode].value != "network" {
		t.Fatalf("edit form: step %v mode %q", f.step, f.mode.fields[fMode].value)
	}
	press(t, m, "enter", "enter")
	if m.modal != nil {
		t.Fatalf("modal still open: %q", formErr(m))
	}
	if got := lastConfig(t, e).Items[5]; !reflect.DeepEqual(got, tvItem()) {
		t.Fatalf("TV after edit = %+v\nwant %+v", got, tvItem())
	}
}

// Standby watts and host are editable; 0 W standby is kept as 0.
func TestEditNetworkHostAndZeroStandby(t *testing.T) {
	m, _, e := newNetFix(t, 140, 40)
	selectItem(t, m, "TV")
	press(t, m, "e", "tab", "tab", "tab", "ctrl+u")
	typeText(t, m, "192.0.2.40")
	press(t, m, "enter", "tab", "tab", "tab", "ctrl+u")
	typeText(t, m, "0")
	press(t, m, "enter")
	want := tvItem()
	want.Host, want.StandbyW = "192.0.2.40", 0
	if got := lastConfig(t, e).Items[5]; !reflect.DeepEqual(got, want) {
		t.Fatalf("TV after edit = %+v\nwant %+v", got, want)
	}
}

// An invalid host is the validator's message and nothing is saved.
func TestEditNetworkInvalidHostSavesNothing(t *testing.T) {
	m, _, e := newNetFix(t, 140, 40)
	before := m.Config()
	selectItem(t, m, "TV")
	press(t, m, "e", "tab", "tab", "tab", "ctrl+u")
	typeText(t, m, "tv.local")
	press(t, m, "enter", "enter")
	if len(e.cfgs) != 0 || !strings.Contains(formErr(m), `host "tv.local" must be one IPv4 address`) {
		t.Fatalf("saved %d, err %q", len(e.cfgs), formErr(m))
	}
	if !reflect.DeepEqual(m.Config(), before) {
		t.Fatal("config changed after a failed validation")
	}
}

// Empty standby is an error, never a silent 0.
func TestEditNetworkStandbyRequired(t *testing.T) {
	m, _, e := newNetFix(t, 140, 40)
	selectItem(t, m, "TV")
	press(t, m, "e", "enter", "tab", "tab", "tab", "ctrl+u", "enter")
	if len(e.cfgs) != 0 || !strings.Contains(formErr(m), "standby") {
		t.Fatalf("saved %d, err %q", len(e.cfgs), formErr(m))
	}
}

// a and e on other items keep the four existing modes.
func TestModeChoicesUnchanged(t *testing.T) {
	m, _, _ := newNetFix(t, 140, 40)
	press(t, m, "a", "tab")
	want := []string{"measured", "follows", "schedule", "manual"}
	if got := m.modal.(*itemForm).mode.fields[fMode].choices; !reflect.DeepEqual(got, want) {
		t.Fatalf("add choices %v", got)
	}
	press(t, m, "esc", "esc", "esc")
	selectItem(t, m, "Desk fan")
	press(t, m, "e")
	if got := m.modal.(*itemForm).mode.fields[fMode].choices; !reflect.DeepEqual(got, want) {
		t.Fatalf("edit choices %v", got)
	}
	// cycling still visits every mode and the hidden fields follow it
	f := m.modal.(*itemForm)
	seen := map[string]bool{}
	for range want {
		press(t, m, "right")
		seen[f.mode.fields[fMode].value] = true
		if f.mode.fields[fProbe].hidden == false || f.name.fields[nStandby].hidden == false {
			t.Fatalf("network fields shown for %q", f.mode.fields[fMode].value)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("cycled %v", seen)
	}
}

func rowLine(t *testing.T, out, name string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, " "+name+" ") && strings.Contains(l, "network") {
			return l
		}
	}
	t.Fatalf("no row %q in:\n%s", name, out)
	return ""
}

// A standby TV counts its standby watts; a PS5 without a fresh reading is
// unknown, never 0 W, and names the machine that asks it.
func TestNetworkRowsInCockpit(t *testing.T) {
	for _, sz := range [][2]int{{140, 40}, {100, 30}, {80, 24}} {
		m, _, _ := newNetFix(t, sz[0], sz[1])
		press(t, m, "j", "j", "j", "j", "j", "j") // scroll the PS5 into view
		out := screenText(m)
		tv, ps := rowLine(t, out, "TV"), rowLine(t, out, "PS5")
		if !strings.Contains(tv, "◐") || !strings.Contains(tv, "~0,5 W") || !strings.Contains(tv, "standby") {
			t.Errorf("%v: TV row %q", sz, tv)
		}
		if !strings.Contains(ps, "unknown") || strings.Contains(ps, " 0 W") || !strings.Contains(ps, "?") {
			t.Errorf("%v: PS5 row %q", sz, ps)
		}
		if sz[0] >= 100 && !strings.Contains(ps, "linux") {
			t.Errorf("%v: PS5 row does not name its source: %q", sz, ps)
		}
		if !strings.Contains(out, "partial") {
			t.Errorf("%v: no partial note:\n%s", sz, out)
		}
	}
}

// Unknown time in the period is shown next to the totals, which stay.
func TestIncompleteTotalsShown(t *testing.T) {
	m, _, _ := newNetFix(t, 140, 40)
	out := screenText(m)
	for _, want := range []string{"partial · PS5 unknown", "may be low: PS5 unknown 3 h 10 min", "$11.876 so far"} {
		if !strings.Contains(out, want) {
			t.Errorf("cockpit lacks %q:\n%s", want, out)
		}
	}
	press(t, m, "b")
	out = screenText(m)
	for _, want := range []string{"PS5 state unknown for 3 h 10 min, so these totals may be low", "partial, PS5 unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("bill lacks %q:\n%s", want, out)
		}
	}
	m, _, _ = newNetFix(t, 80, 24)
	if out := screenText(m); !strings.Contains(out, "projection may be low") {
		t.Errorf("narrow bill lacks the note:\n%s", out)
	}
}

func TestStatusTextNetwork(t *testing.T) {
	got := StatusText(fixNetReport(), fixSources(false), fixNetConfig(), false)
	for _, want := range []string{
		"  partial · PS5 unknown\n",
		"  ◐ TV           network      ~0,5 W    0,01 kWh today · standby\n",
		"  ? PS5          network     unknown    0,40 kWh today · no reading since 18:40\n",
		"  may be low: PS5 unknown 1 h 35 min\n",
		" · may be low: PS5 unknown 3 h 10 min\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("StatusText lacks %q:\n%s", want, got)
		}
	}
}
