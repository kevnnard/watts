package tui

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/discover"
)

// The Network devices view (n), driven through Update and View with an
// injected scanner: no real network, config or store.

// rowRe finds a table row by its address column.
var rowRe = regexp.MustCompile(`\s{2}192\.0\.2\.\d+\s{2}`)

func newScanFix(t *testing.T, sc *fakeScan, cfg *config.Config, w, h int) (*Model, *fakeEditor) {
	t.Helper()
	e := &fakeEditor{}
	m := New(&fakeData{net: true}, e, cfg, fixHistory(), WithClock(func() time.Time { return fixNow }),
		WithScanner(sc.scan), WithLocalSource(config.SourceLinux))
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	run(t, m, m.Init())
	return m, e
}

// send presses k and returns its command without running it.
func send(m *Model, k string) tea.Cmd {
	_, cmd := m.Update(keyMsg(k))
	return cmd
}

// scanOf runs a scan command (the scan and its elapsed-time tick) in the
// background and returns where the scan's result arrives; the tick is
// dropped.
func scanOf(t *testing.T, cmd tea.Cmd) <-chan netScanMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no scan was started")
	}
	b, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("scan command is %T, want a batch", b)
	}
	out := make(chan netScanMsg, 1)
	for _, c := range b {
		go func() {
			if msg, ok := c().(netScanMsg); ok {
				out <- msg
			}
		}()
	}
	return out
}

func recv(t *testing.T, ch <-chan netScanMsg) netScanMsg {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("the scan did not return")
	}
	return netScanMsg{}
}

// openScanned opens the view and applies its first scan.
func openScanned(t *testing.T, sc *fakeScan, cfg *config.Config, w, h int) (*Model, *fakeEditor) {
	t.Helper()
	m, e := newScanFix(t, sc, cfg, w, h)
	m.Update(recv(t, scanOf(t, send(m, "n"))))
	return m, e
}

func selectDev(t *testing.T, m *Model, name string) {
	t.Helper()
	for i, d := range m.net.devs {
		if d.d.Name == name {
			m.net.sel = i
			return
		}
	}
	t.Fatalf("no device %q", name)
}

func TestNetOpenScansOnceAndCloses(t *testing.T) {
	sc := &fakeScan{res: fixScan()}
	m, _ := newScanFix(t, sc, fixConfig(), 140, 40)
	cmd := send(m, "n")
	if _, ok := m.modal.(*netPanel); !ok {
		t.Fatalf("n opened %T", m.modal)
	}
	if out := screenText(m); !strings.Contains(out, "Scanning this machine's local networks…") || !strings.Contains(out, "scanning") {
		t.Fatalf("loading state missing:\n%s", out)
	}
	m.Update(recv(t, scanOf(t, cmd)))
	out := screenText(m)
	for _, want := range []string{"5 found · scanned 20:15", "en0 192.0.2.10/24 · samsung, ddp, ssdp, mdns · multicast may not reach other subnets", "Samsung TV", "◐ standby", "? unknown", "unnamed device"} {
		if !strings.Contains(out, want) {
			t.Errorf("results lack %q:\n%s", want, out)
		}
	}
	rows := 0
	for _, l := range strings.Split(out, "\n") {
		if rowRe.MatchString(l) {
			rows++
		}
		if rowRe.MatchString(l) && !strings.Contains(l, "● on") && !strings.Contains(l, "◐ standby") && !strings.Contains(l, "? unknown") {
			t.Errorf("a row without on, standby or unknown: %q", l)
		}
	}
	if rows != 5 {
		t.Errorf("%d device rows, want 5", rows)
	}
	press(t, m, "esc")
	if m.modal != nil {
		t.Fatal("esc did not close the view")
	}
	if cmd := send(m, "n"); cmd != nil || sc.count() != 1 {
		t.Fatalf("reopening scanned again: %d scans", sc.count())
	}
	if !strings.Contains(screenText(m), "Samsung TV") {
		t.Fatal("reopening lost the list")
	}
	press(t, m, "n")
	if m.modal != nil {
		t.Fatal("n did not close the view")
	}
}

// r rescans; while a scan runs r is ignored, the old list stays and the
// arrows still move.
func TestNetRescanKeepsListAndArrows(t *testing.T) {
	sc := &fakeScan{res: fixScan()}
	m, _ := openScanned(t, sc, fixConfig(), 140, 40)
	sc.block = make(chan struct{})
	ch := scanOf(t, send(m, "r"))
	if cmd := send(m, "r"); cmd != nil {
		t.Fatal("r started a second scan while one runs")
	}
	press(t, m, "down", "down", "up")
	if m.net.sel != 1 {
		t.Fatalf("sel = %d during the scan", m.net.sel)
	}
	out := screenText(m)
	if !strings.Contains(out, "Samsung TV") || !strings.Contains(out, "r scanning…") {
		t.Fatalf("rescan lost the list or the key state:\n%s", out)
	}
	res := fixScan()
	res.Devices = res.Devices[1:] // the TV left; the PS5 is now first
	sc.mu.Lock()
	sc.res = res
	sc.mu.Unlock()
	close(sc.block)
	m.Update(recv(t, ch))
	if sc.count() != 2 || len(m.net.devs) != 4 {
		t.Fatalf("scans %d devices %d", sc.count(), len(m.net.devs))
	}
	if m.net.devs[m.net.sel].d.Name != "PS5" {
		t.Fatalf("selection did not follow the PS5: %q", m.net.devs[m.net.sel].d.Name)
	}
}

// Closing cancels the scan's context, and its late result is dropped.
func TestNetCloseCancelsAndDropsLateResult(t *testing.T) {
	sc := &fakeScan{res: fixScan(), block: make(chan struct{})}
	m, _ := newScanFix(t, sc, fixConfig(), 140, 40)
	ch := scanOf(t, send(m, "n"))
	press(t, m, "esc")
	late := recv(t, ch)
	if !errors.Is(late.err, context.Canceled) {
		t.Fatalf("scan not cancelled: %v", late.err)
	}
	late.err, late.res = nil, fixScan() // even a full late answer is dropped
	m.Update(late)
	if len(m.net.devs) != 0 || m.net.done {
		t.Fatalf("late result applied: %d devices", len(m.net.devs))
	}
	// the next open scans again, and an older result cannot overwrite it
	cmd := send(m, "n")
	m.Update(late)
	if !m.net.pending || len(m.net.devs) != 0 {
		t.Fatal("a stale result ended the new scan")
	}
	close(sc.block)
	m.Update(recv(t, scanOf(t, cmd)))
	if len(m.net.devs) != 5 {
		t.Fatalf("devices %d", len(m.net.devs))
	}
}

// Many keys while a scan runs never block Update.
func TestNetRapidKeysDoNotBlock(t *testing.T) {
	sc := &fakeScan{res: fixScan(), block: make(chan struct{})}
	defer close(sc.block)
	m, _ := newScanFix(t, sc, fixConfig(), 80, 24)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			for _, k := range []string{"n", "r", "down", "up", "enter", "esc", "n", "r"} {
				m.Update(keyMsg(k))
				m.View()
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Update blocked on a pending scan")
	}
}

func TestNetElapsedTick(t *testing.T) {
	sc := &fakeScan{res: fixScan()}
	now := fixNow
	m := New(&fakeData{}, &fakeEditor{}, fixConfig(), fixHistory(), WithClock(func() time.Time { return now }), WithScanner(sc.scan))
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	send(m, "n")
	now = now.Add(3 * time.Second)
	if _, cmd := m.Update(netTickMsg{seq: m.net.seq}); cmd == nil {
		t.Fatal("the tick stopped while the scan runs")
	}
	if out := screenText(m); !strings.Contains(out, "scanning · 3 s") {
		t.Fatalf("no elapsed time:\n%s", out)
	}
	if _, cmd := m.Update(netTickMsg{seq: m.net.seq - 1}); cmd != nil {
		t.Fatal("a stale tick kept ticking")
	}
}

func TestNetErrorAndEmpty(t *testing.T) {
	sc := &fakeScan{err: errors.New("discover: no IPv4 network interface is up")}
	m, _ := openScanned(t, sc, fixConfig(), 140, 40)
	out := screenText(m)
	for _, want := range []string{"scan failed 20:15", "Couldn't scan: no IPv4 network interface is up.", "esc, then a"} {
		if !strings.Contains(out, want) {
			t.Errorf("error state lacks %q:\n%s", want, out)
		}
	}
	// a failed rescan keeps the last list and says so
	sc.err, sc.res = nil, fixScan()
	m.Update(recv(t, scanOf(t, send(m, "r"))))
	sc.err = errors.New("discover: interfaces: denied")
	m.Update(recv(t, scanOf(t, send(m, "r"))))
	if out := screenText(m); !strings.Contains(out, "The last scan failed: interfaces: denied") || !strings.Contains(out, "Samsung TV") {
		t.Errorf("failed rescan:\n%s", out)
	}
	sc.err, sc.res = nil, NetScan{Interfaces: fixScan().Interfaces}
	m.Update(recv(t, scanOf(t, send(m, "r"))))
	if out := screenText(m); !strings.Contains(out, "nothing found · 20:15") || !strings.Contains(out, "No device answered on en0 192.0.2.10/24.") {
		t.Errorf("empty state:\n%s", out)
	}
}

// A failed rescan says when it failed, and that the list kept on screen is
// from the scan before it.
func TestNetFailedRescanListTime(t *testing.T) {
	sc := &fakeScan{res: fixScan()}
	now := fixNow
	m := New(&fakeData{}, &fakeEditor{}, fixConfig(), fixHistory(), WithClock(func() time.Time { return now }), WithScanner(sc.scan))
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(recv(t, scanOf(t, send(m, "n"))))
	now = now.Add(10 * time.Minute)
	sc.err = errors.New("discover: interfaces: denied")
	m.Update(recv(t, scanOf(t, send(m, "r"))))
	out := screenText(m)
	for _, want := range []string{"scan failed 20:25", "This list is from 20:15.", "Samsung TV"} {
		if !strings.Contains(out, want) {
			t.Errorf("failed rescan lacks %q:\n%s", want, out)
		}
	}
}

// Strings from the network never reach the terminal as control codes.
func TestNetSanitized(t *testing.T) {
	res := fixScan()
	res.Devices[0].Name = "TV\x1b[31m\x07evil\nname"
	res.Devices[0].Model = "Q\x1b]0;x\x07"
	res.Devices[2].ID = "uuid:\x1b[2J"
	sc := &fakeScan{res: res, err: nil}
	m, _ := openScanned(t, sc, fixConfig(), 140, 40)
	out := screenText(m)
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "TV?[31m?evil?name") {
		t.Fatalf("unsanitized:\n%q", out)
	}
	for _, r := range stripANSI(m.Render()) {
		if r < ' ' && r != '\n' {
			t.Fatalf("control rune %q in the frame", r)
		}
	}
	press(t, m, "enter")
	if f := m.modal.(*itemForm); strings.ContainsAny(f.label.fields[lModel].value, "\x1b\x07") {
		t.Fatalf("form prefilled with a control code: %q", f.label.fields[lModel].value)
	}
}

// Added only by a stable ID, in any case or alias; enter does not add it
// twice.
func TestNetAddedByStableID(t *testing.T) {
	cfg := fixNetConfig()
	cfg.Items[5].ID = "mac:02:00:5e:10:00:23" // the TV's other ID
	cfg.Items[5].Host = "192.0.2.99"          // and an old address
	res := fixScan()
	res.Devices[0].Raw["ids"] = strings.ToUpper(tvID) + ",MAC:02:00:5E:10:00:23"
	m, e := openScanned(t, &fakeScan{res: res}, cfg, 140, 40)
	out := screenText(m)
	if !strings.Contains(out, "✓ TV") || !strings.Contains(out, "✓ PS5") {
		t.Fatalf("ID matches not shown:\n%s", out)
	}
	press(t, m, "enter")
	if _, ok := m.modal.(*netPanel); !ok || len(e.cfgs) != 0 {
		t.Fatalf("enter on an added device opened %T", m.modal)
	}
	if !strings.Contains(screenText(m), "Already in your items as TV; e edits it from the cockpit.") {
		t.Fatalf("no duplicate warning:\n%s", screenText(m))
	}
}

// An address, an mDNS ID or a name is weak: never "added", at most "?".
func TestNetWeakIdentity(t *testing.T) {
	cfg := fixNetConfig()
	cfg.Items[5].ID = ""
	res := fixScan()
	res.Devices[0].Name = "TV" // same name as the item, too
	m, _ := openScanned(t, &fakeScan{res: res}, cfg, 140, 40)
	if d := m.net.devs[0]; d.added != "" || d.maybe != "TV" {
		t.Fatalf("TV by address: added %q maybe %q", d.added, d.maybe)
	}
	out := screenText(m)
	if !strings.Contains(out, "? TV") || !strings.Contains(out, "uses this address, which alone does not prove it is this device") {
		t.Fatalf("uncertain match not shown:\n%s", out)
	}
	press(t, m, "enter") // not a hard block
	f, ok := m.modal.(*itemForm)
	if !ok || !strings.Contains(screenText(m), "TV uses this address;") {
		t.Fatalf("enter on a maybe: %T\n%s", m.modal, screenText(m))
	}
	if f.mode.fields[fID].value != tvID {
		t.Fatalf("ID = %q", f.mode.fields[fID].value)
	}
	// the speaker's mDNS ID is not a stable identity
	press(t, m, "esc")
	selectDev(t, m, "Living room speaker")
	if d := m.net.devs[m.net.sel]; len(d.ids) != 0 || !strings.Contains(screenText(m), "an address or a name can change") {
		t.Fatalf("mDNS ID treated as stable: %+v", d.ids)
	}
}

// Two devices reporting one ID: neither is matched, and no ID is saved.
func TestNetAmbiguousID(t *testing.T) {
	res := fixScan()
	twin := res.Devices[1]
	twin.Name, twin.Host = "PS5 twin", "192.0.2.32"
	res.Devices = append(res.Devices, twin)
	m, _ := openScanned(t, &fakeScan{res: res}, fixNetConfig(), 140, 40)
	selectDev(t, m, "PS5")
	if d := m.net.devs[m.net.sel]; d.added != "" || !d.ambiguous || d.maybe != "PS5" {
		t.Fatalf("ambiguous PS5: %+v", d)
	}
	if !strings.Contains(screenText(m), "another device in this scan reports it too") {
		t.Fatalf("ambiguity not explained:\n%s", screenText(m))
	}
	selectDev(t, m, "PS5 twin")
	press(t, m, "enter")
	f := m.modal.(*itemForm)
	if f.mode.fields[fID].value != "" {
		t.Fatalf("ambiguous ID prefilled: %q", f.mode.fields[fID].value)
	}
}

// Enter opens the Add item form filled in and editable; esc goes back to
// the same row; saving validates the whole config and returns to the view.
func TestNetAddPrefilledNetworkItem(t *testing.T) {
	m, e := openScanned(t, &fakeScan{res: fixScan()}, fixConfig(), 140, 40)
	press(t, m, "enter")
	f, ok := m.modal.(*itemForm)
	if !ok || f.step != stepLabel {
		t.Fatalf("enter opened %T", m.modal)
	}
	lf := f.label.fields
	if lf[lWatts].value != "" || lf[lKind].value != "tv" || lf[lBrand].value != "Samsung" || lf[lModel].value != "" {
		t.Fatalf("prefill: watts %q kind %q brand %q model %q", lf[lWatts].value, lf[lKind].value, lf[lBrand].value, lf[lModel].value)
	}
	out := screenText(m)
	for _, want := range []string{"step 1 of 3 · from network", "From the scan", "saved as its stable ID", "No model in the scan, so watts", "esc back to network devices"} {
		if !strings.Contains(out, want) {
			t.Errorf("form lacks %q:\n%s", want, out)
		}
	}
	press(t, m, "esc")
	if _, ok := m.modal.(*netPanel); !ok || m.net.sel != 0 {
		t.Fatalf("esc went to %T sel %d", m.modal, m.net.sel)
	}
	press(t, m, "enter", "enter")
	if formErr(m) != "watts must be greater than 0" {
		t.Fatalf("empty watts: %q", formErr(m))
	}
	typeText(t, m, "80")
	press(t, m, "enter")
	f = m.modal.(*itemForm)
	mf := f.mode.fields
	if f.step != stepMode || mf[fMode].value != "network" || !reflect.DeepEqual(mf[fMode].choices, []string{"network", "schedule", "manual"}) {
		t.Fatalf("mode step: %v %q %v", f.step, mf[fMode].value, mf[fMode].choices)
	}
	if mf[fSource].value != "linux" || mf[fProbe].value != "samsung" || mf[fHost].value != "192.0.2.23" || mf[fID].value != tvID {
		t.Fatalf("network fields: %q %q %q %q", mf[fSource].value, mf[fProbe].value, mf[fHost].value, mf[fID].value)
	}
	press(t, m, "tab", "left") // the source stays editable
	press(t, m, "enter", "enter")
	if !strings.Contains(formErr(m), "standby") || len(e.cfgs) != 0 {
		t.Fatalf("empty standby saved: %q", formErr(m))
	}
	press(t, m, "tab", "tab", "tab")
	typeText(t, m, "0")
	press(t, m, "enter")
	want := config.Item{Name: "Samsung TV", Mode: config.ModeNetwork, Source: config.SourceMac, Probe: "samsung", Host: "192.0.2.23", ID: tvID,
		Watts: 80, StandbyW: 0, Quantity: 1, Brand: "Samsung"}
	if got := lastConfig(t, e).Items[5]; !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v\nwant  %+v", got, want)
	}
	if _, ok := m.modal.(*netPanel); !ok {
		t.Fatalf("after adding: %T", m.modal)
	}
	if !strings.Contains(screenText(m), "✓ Samsung TV") {
		t.Fatalf("the new item is not marked:\n%s", screenText(m))
	}
}

// A device without a state query counts by hand or by schedule only.
func TestNetAddDiscoveryOnly(t *testing.T) {
	m, e := openScanned(t, &fakeScan{res: fixScan()}, fixConfig(), 140, 40)
	selectDev(t, m, "Router")
	press(t, m, "enter", "tab")
	f := m.modal.(*itemForm)
	if f.label.fields[lModel].placeholder != "not reported" {
		t.Fatalf("model placeholder %q", f.label.fields[lModel].placeholder)
	}
	typeText(t, m, "") // watts are typed in the first field
	f.label.focus = lWatts
	typeText(t, m, "9")
	press(t, m, "enter")
	if f.mode.fields[fMode].value != "manual" || !reflect.DeepEqual(f.mode.fields[fMode].choices, []string{"schedule", "manual"}) {
		t.Fatalf("modes %q %v", f.mode.fields[fMode].value, f.mode.fields[fMode].choices)
	}
	press(t, m, "enter", "enter")
	want := config.Item{Name: "Router", Mode: config.ModeManual, Watts: 9, Quantity: 1}
	if got := lastConfig(t, e).Items[5]; !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v\nwant  %+v", got, want)
	}
}

// Catalog watts only for the exact model; never a generic preset.
func TestNetCatalogExactModelOnly(t *testing.T) {
	res := fixScan()
	res.Devices[0].Model = `55" QLED Q60D (QN55Q60D)`
	m, _ := openScanned(t, &fakeScan{res: res}, fixConfig(), 140, 40)
	press(t, m, "enter")
	f := m.modal.(*itemForm)
	if f.label.fields[lWatts].value != "63" || f.name.fields[nStandby].value != "0,5" {
		t.Fatalf("exact model: watts %q standby %q", f.label.fields[lWatts].value, f.name.fields[nStandby].value)
	}
	press(t, m, "esc")
	selectDev(t, m, "PS5") // "PlayStation 5" is not the catalog's PS5 Slim
	press(t, m, "enter")
	f = m.modal.(*itemForm)
	if f.label.fields[lWatts].value != "" || f.name.fields[nStandby].value != "" {
		t.Fatalf("PS5 guessed: watts %q standby %q", f.label.fields[lWatts].value, f.name.fields[nStandby].value)
	}
}

// A saved ID cannot be given to a second item; nothing changes.
func TestNetDuplicateIDRejected(t *testing.T) {
	m, e := openScanned(t, &fakeScan{res: fixScan()}, fixNetConfig(), 140, 40)
	before := m.Config()
	selectDev(t, m, "Router")
	press(t, m, "enter")
	f := m.modal.(*itemForm)
	f.net.d.Probe = "ps5" // as if the router were a console: network mode
	f.mode.fields[fMode].choices = []string{"network", "manual"}
	typeText(t, m, "5")
	press(t, m, "enter")
	f.mode.fields[fMode].value, f.mode.fields[fProbe].value, f.mode.fields[fID].value = "network", "ps5", strings.ToUpper(ps5ID)
	f.syncMode()
	f.name.fields[nStandby].value = "1"
	press(t, m, "enter", "enter")
	if len(e.cfgs) != 0 || !strings.Contains(formErr(m), "is also item \"PS5\"'s") {
		t.Fatalf("duplicate saved: %d, err %q", len(e.cfgs), formErr(m))
	}
	if !reflect.DeepEqual(m.Config(), before) {
		t.Fatal("config changed")
	}
}

// The scan's state never becomes a guessed device: unknown probes and
// states are dropped to none and unknown.
func TestNetUnknownProbeAndState(t *testing.T) {
	devs := newNetDevs([]discover.Device{{ID: "ps:aa", Probe: "xbox", State: "off", Host: "192.0.2.5"}})
	if d := devs[0]; d.d.Probe != "" || d.d.State != discover.StateUnknown || len(d.ids) != 0 {
		t.Fatalf("got %+v", d)
	}
}

// At 80 × 24 the scope limitation, a failed rescan and an uncertain match
// all fit, and nothing is drawn past the frame.
func TestNetNarrowFits(t *testing.T) {
	sc := &fakeScan{res: fixScan()}
	m, _ := openScanned(t, sc, tvAtConfig(), 80, 24)
	sc.err = errors.New("discover: interfaces: denied")
	m.Update(recv(t, scanOf(t, send(m, "r"))))
	out := screenText(m)
	lines := strings.Split(out, "\n")
	for _, want := range []string{"multicast may not reach other subnets", "The last scan failed: interfaces: denied", "? TV", "uses this address"} {
		if !strings.Contains(out, want) {
			t.Errorf("80x24 lacks %q:\n%s", want, out)
		}
	}
	if len(lines) < 24 || !strings.HasPrefix(lines[22], "└") {
		t.Fatalf("frame broken:\n%s", out)
	}
}
