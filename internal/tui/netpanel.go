package tui

import (
	"context"
	"errors"
	"net"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/catalog"
	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/discover"
)

// The Network devices view of design/network-devices/01-panel.html (S20):
// n opens it and scans this machine's local networks once, r scans again
// in the background, enter opens the Add item form filled in with what the
// scan found. The scan runs as a command; the list stays usable while it
// runs, and a result that is no longer awaited is dropped.

// Scanner finds devices on this machine's local networks. It must return
// soon after ctx is done.
type Scanner func(ctx context.Context) (NetScan, error)

// NetScan is what one scan found, and on which networks.
type NetScan struct {
	Interfaces []discover.Interface
	Devices    []discover.Device
}

// ScanTimeout bounds one scan from the view; discover.Scan itself stops
// after discover.DefaultTimeout.
const ScanTimeout = 10 * time.Second

// netProtocols are the discovery protocols a scan asks with.
const netProtocols = "samsung, ddp, ssdp, mdns"

// WithScanner replaces the network scan, for tests and the preview.
func WithScanner(sc Scanner) Option { return func(m *Model) { m.net.scan = sc } }

// WithLocalSource sets the machine new network items are asked from
// (default: this one, mac on macOS, else linux).
func WithLocalSource(src string) Option { return func(m *Model) { m.local = src } }

func localSource() string {
	if runtime.GOOS == "darwin" {
		return config.SourceMac
	}
	return config.SourceLinux
}

// systemScan is the real scan: discover.Scan over the system interfaces.
func systemScan(ctx context.Context) (NetScan, error) {
	ifs, err := discover.SystemInterfaces()
	if err != nil {
		return NetScan{}, err
	}
	devs, err := discover.Scan(ctx, discover.Options{Interfaces: func() ([]discover.Interface, error) { return ifs, nil }})
	return NetScan{Interfaces: ifs, Devices: devs}, err
}

// netState is the view's scan and its last result. It lives on the Model,
// so closing and opening the view keeps the list.
type netState struct {
	scan    Scanner
	seq     int // bumped per scan and on close; other results are dropped
	pending bool
	cancel  context.CancelFunc
	started time.Time
	elapsed int // whole seconds of the pending scan

	done  bool      // a scan has finished
	at    time.Time // when the last scan finished
	since time.Time // when the scan that found devs finished
	err   string    // the last scan failed
	cut   bool      // the last scan stopped at ScanTimeout
	scope string    // "en0 192.0.2.10/24"
	devs  []netDev
	sel   int
	note  string // a warning for the selected row, until it moves
}

// netDev is one found device, its strings made printable, matched against
// the config.
type netDev struct {
	d         discover.Device
	ids       []string // stable IDs of d's probe, normalized, d.ID first
	ambiguous bool     // another found device reports one of ids
	added     string   // the network item with one of ids
	maybe     string   // an item at the same address without an ID match
	entry     *catalog.Entry
}

type netScanMsg struct {
	seq int
	res NetScan
	err error
}

type netTickMsg struct{ seq int }

// openNet opens the view and starts the first scan.
func (m *Model) openNet() tea.Cmd {
	m.modal = &netPanel{}
	if m.net.done || m.net.pending {
		return nil
	}
	return m.startScan()
}

func (m *Model) startScan() tea.Cmd {
	n := &m.net
	n.seq++
	n.pending, n.started, n.elapsed, n.note = true, m.now(), 0, ""
	ctx, cancel := context.WithTimeout(context.Background(), ScanTimeout)
	n.cancel = cancel
	seq, scan := n.seq, n.scan
	return tea.Batch(func() tea.Msg {
		defer cancel()
		res, err := scan(ctx)
		return netScanMsg{seq: seq, res: res, err: err}
	}, scanTick(seq))
}

func scanTick(seq int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return netTickMsg{seq: seq} })
}

// closeNet closes the view and stops a pending scan; its result is dropped.
func (m *Model) closeNet() {
	m.modal = nil
	m.net.note = ""
	if m.net.pending {
		m.net.cancel()
		m.net.pending = false
		m.net.seq++
	}
}

func (m *Model) scanned(msg netScanMsg) {
	n := &m.net
	if msg.seq != n.seq || !n.pending {
		return
	}
	n.pending, n.done, n.at = false, true, m.now()
	n.err, n.cut = "", false
	if msg.err != nil {
		if !errors.Is(msg.err, context.DeadlineExceeded) || len(msg.res.Devices) == 0 {
			// keep the previous list; only the summary says the scan failed
			n.err = printable(strings.TrimPrefix(msg.err.Error(), "discover: "), 120)
			return
		}
		n.cut = true
	}
	var key string
	if n.sel < len(n.devs) {
		key = devKey(n.devs[n.sel])
	}
	n.devs, n.since = newNetDevs(msg.res.Devices), n.at
	n.scope = scopeText(msg.res.Interfaces)
	n.sel = min(n.sel, max(0, len(n.devs)-1))
	// the selection follows its device, when exactly one row is it
	var at []int
	for i, d := range n.devs {
		if key != "" && devKey(d) == key {
			at = append(at, i)
		}
	}
	if len(at) == 1 {
		n.sel = at[0]
	}
	n.match(m.cfg)
}

func devKey(d netDev) string {
	if len(d.ids) > 0 && !d.ambiguous {
		return d.ids[0]
	}
	return "host:" + d.d.Host + "/" + d.d.Name
}

// scopeText is "en0 192.0.2.10/24, en1 198.51.100.4/24".
func scopeText(ifs []discover.Interface) string {
	var parts []string
	for _, i := range ifs {
		ones, _ := net.IPMask(i.Mask).Size()
		if len(i.Mask) == net.IPv6len {
			ones -= 96
		}
		parts = append(parts, printable(i.Name, 16)+" "+printable(i.IP.String(), 0)+"/"+itoa(ones))
	}
	return strings.Join(parts, ", ")
}

// newNetDevs makes every string from the network printable, as watts scan
// does, keeps only known probes and states, and finds each device's stable
// IDs. An ID more than one device reports is not used.
func newNetDevs(devs []discover.Device) []netDev {
	out := make([]netDev, 0, len(devs))
	owners := map[string]int{}
	for _, d := range devs {
		c := discover.Device{
			ID: printable(d.ID, 0), Name: printable(d.Name, scanCellMax), Brand: printable(d.Brand, scanCellMax),
			Model: printable(d.Model, scanCellMax), Kind: printable(d.Kind, 16), Host: printable(d.Host, 0),
			Protocol: printable(d.Protocol, 0), State: discover.StateUnknown,
		}
		if d.State == discover.StateOn || d.State == discover.StateStandby {
			c.State = d.State
		}
		if d.Probe == config.ProbeSamsung || d.Probe == config.ProbePS5 {
			c.Probe = d.Probe
		}
		nd := netDev{d: c}
		if c.Probe != "" {
			for _, id := range append([]string{d.ID}, strings.Split(d.Raw["ids"], ",")...) {
				if n, ok := config.NetworkID(c.Probe, id); ok && !slices.Contains(nd.ids, n) {
					nd.ids = append(nd.ids, n)
					owners[n]++
				}
			}
		}
		if e, ok := catalog.Find(c.Brand, c.Model); ok && e.Watts > 0 && c.Brand != "" && c.Model != "" {
			nd.entry = &e
		}
		out = append(out, nd)
	}
	for i := range out {
		for _, id := range out[i].ids {
			out[i].ambiguous = out[i].ambiguous || owners[id] > 1
		}
	}
	return out
}

// match marks the devices already in cfg: added only by a stable ID that
// no other found device reports; an item at the same address is a maybe.
func (n *netState) match(cfg *config.Config) {
	for i := range n.devs {
		d := &n.devs[i]
		d.added, d.maybe = "", ""
		for _, it := range cfg.Items {
			if it.Mode != config.ModeNetwork {
				continue
			}
			id, ok := config.NetworkID(it.Probe, it.ID)
			if ok && it.Probe == d.d.Probe && slices.Contains(d.ids, id) {
				if d.ambiguous {
					d.maybe = it.Name
				} else {
					d.added = it.Name
				}
				break
			}
			if it.Host == d.d.Host && d.maybe == "" {
				d.maybe = it.Name
			}
		}
	}
}

// scanCellMax caps a name, brand or model from the network, as watts scan.
const scanCellMax = 32

// printable replaces control and other non-printing runes (terminal
// escapes, tabs, newlines) with '?', and cuts s to max runes when max > 0;
// the policy of watts scan.
func printable(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// ---- the view ---------------------------------------------------------

type netPanel struct{}

func (*netPanel) setErr(string) {}

func (p *netPanel) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	n := &m.net
	switch k.String() {
	case "esc", "n", "q":
		m.closeNet()
	case "up", "k":
		if n.sel > 0 {
			n.sel--
			n.note = ""
		}
	case "down", "j":
		if n.sel < len(n.devs)-1 {
			n.sel++
			n.note = ""
		}
	case "r":
		if !n.pending {
			return m.startScan()
		}
	case "enter":
		if n.sel >= len(n.devs) {
			return nil
		}
		d := n.devs[n.sel]
		if d.added != "" {
			n.note = "Already in your items as " + d.added + "; e edits it from the cockpit."
			return nil
		}
		m.modal = newNetForm(m, d, p)
	}
	return nil
}

func (*netPanel) keys(m *Model) []key {
	n := &m.net
	w, _ := m.size()
	wide := w >= FullWidth
	back := "back to the cockpit"
	scan := "scan again"
	if !wide {
		back, scan = "cockpit", "scan"
	}
	if n.pending {
		scan = "scanning…"
	}
	if len(n.devs) == 0 {
		return []key{{"r", scan}, {"esc", "close"}, {"n", back}}
	}
	return []key{{"↑↓", "select"}, {"enter", "add"}, {"r", scan}, {"esc", "close"}, {"n", back}}
}

// summary is the title's right side: "6 found · scanned 20:14".
func (n *netState) summary(wide bool) string {
	at := clock(n.at)
	switch {
	case n.pending && n.elapsed > 0:
		return "scanning · " + itoa(n.elapsed) + " s"
	case n.pending:
		return "scanning"
	case !n.done:
		return ""
	case n.err != "" && wide:
		return "scan failed " + at
	case n.err != "":
		return "scan failed"
	case len(n.devs) == 0 && wide:
		return "nothing found · " + at
	case len(n.devs) == 0:
		return "nothing found"
	}
	found := itoa(len(n.devs)) + " found · "
	if n.cut {
		return found + "cut short " + at
	}
	if wide {
		return found + "scanned " + at
	}
	return found + at
}

func (*netPanel) draw(m *Model, s *screen) {
	n := &m.net
	W, H := s.w, s.h
	wide := W >= FullWidth && H >= FullHeight
	frame(s, 0, 1, W, H-2, "Network devices", n.summary(wide))
	L, R := 2, W-3
	scope := n.scope
	if scope == "" {
		scope = "this machine's networks"
	}
	scope += " · " + netProtocols
	const limit = "multicast may not reach other subnets"
	head := 3 // the first row after the header lines
	if textWidth(scope+" · "+limit) <= R-L+1 {
		s.put(L, 2, scope+" · "+limit, sMu, b3)
	} else {
		s.put(L, 2, trunc(scope, R-L+1), sMu, b3)
		s.put(L, 3, trunc(limit, R-L+1), sMu, b3)
		head = 4
	}

	switch {
	case len(n.devs) == 0 && n.pending:
		s.put(L, head+1, trunc("Scanning this machine's local networks…", R-L+1), sTx, b3)
		s.put(L, head+2, trunc("The list appears when the scan ends, in about "+itoa(int(discover.DefaultTimeout.Seconds()))+" s.", R-L+1), sMu, b3)
		return
	case len(n.devs) == 0 && n.err != "":
		s.put(L, head+1, trunc("Couldn't scan: "+n.err+".", R-L+1), sEr, b3)
		s.put(L, head+2, trunc("Check Wi-Fi or Ethernet, then press r. Nothing on the network or in your items changed.", R-L+1), sMu, b3)
		s.put(L, head+4, trunc("You can still add any device by hand: esc, then a.", R-L+1), sMu, b3)
		return
	case len(n.devs) == 0 && n.done:
		on := n.scope
		if on == "" {
			on = "this machine's networks"
		}
		s.put(L, head+1, trunc("No device answered on "+on+".", R-L+1), sTx, b3)
		s.put(L, head+2, trunc("Devices that are off, on another subnet or with discovery turned off do not show up.", R-L+1), sMu, b3)
		s.put(L, head+3, trunc("Routers often do not forward multicast between networks.", R-L+1), sMu, b3)
		s.put(L, head+5, trunc("You can still add any device by hand: esc, then a.", R-L+1), sMu, b3)
		return
	case len(n.devs) == 0:
		return
	}
	if n.err != "" {
		// a failed rescan keeps the last list
		s.put(L, head, trunc("The last scan failed: "+n.err+". This list is from "+clock(n.since)+".", R-L+1), sEr, b3)
		head++
	}
	if wide {
		drawNetWide(m, s, L, R, head)
	} else {
		drawNetNarrow(m, s, L, R, head)
	}
}

func nameOf(d discover.Device) (string, st) {
	if d.Name == "" {
		return "unnamed device", sMu.italic()
	}
	return d.Name, sTx.bold()
}

func brandModel(d discover.Device) (string, st) {
	switch {
	case d.Brand != "" || d.Model != "":
		var parts []string
		for _, x := range []string{d.Brand, d.Model} {
			if x != "" {
				parts = append(parts, x)
			}
		}
		return strings.Join(parts, " · "), sTx
	case d.Kind == "other" || d.Kind == "":
		return "not identified", sMu.italic()
	}
	return "brand not reported", sMu.italic()
}

// stateText is "● on", "◐ standby" or "? unknown"; never off.
func stateText(d discover.Device) (string, st) {
	switch d.State {
	case discover.StateOn:
		return "● on", st{fg: ms}
	case discover.StateStandby:
		return "◐ standby", sT2
	}
	return "? unknown", sMu
}

func countsText(d netDev) (string, st) {
	if d.d.Probe != "" {
		return "~ network estimate", sT2.italic()
	}
	return "manual or schedule", sMu
}

// window is the first row to draw so sel shows in rows lines.
func window(sel, total, rows int) int {
	if rows <= 0 || total <= rows {
		return 0
	}
	return min(max(0, sel-rows/2), total-rows)
}

func drawNetWide(m *Model, s *screen, L, R, head int) {
	n := &m.net
	C := struct{ name, kind, bm, host, state, by, counts, added int }{4, 27, 37, 63, 77, 89, 103, 125}
	hy := max(4, head)
	for _, h := range []struct {
		x int
		t string
	}{{C.name, "Device"}, {C.kind, "Kind"}, {C.bm, "Brand · model"}, {C.host, "Address"}, {C.state, "State"}, {C.by, "Found by"}, {C.counts, "Counts as"}, {C.added, "In items"}} {
		s.put(h.x, hy, h.t, sMu, b3)
	}
	s.hrule(L, hy+1, R-L+1, b3)
	detailH := 8
	rows := s.h - 3 - detailH - (hy + 2) + 1
	top := window(n.sel, len(n.devs), rows)
	y := hy + 2
	for i := top; i < len(n.devs) && i < top+rows; i++ {
		d := n.devs[i]
		bg := b3
		if i == n.sel {
			bg = b2
			s.fill(1, y, s.w-2, 1, b2)
			s.put(L, y, "▸", sAc.bold(), b2)
		}
		name, nst := nameOf(d.d)
		s.put(C.name, y, trunc(name, 22), nst, bg)
		s.put(C.kind, y, trunc(d.d.Kind, 9), sT2, bg)
		bm, bst := brandModel(d.d)
		s.put(C.bm, y, trunc(bm, 25), bst, bg)
		s.put(C.host, y, trunc(d.d.Host, 13), sT2, bg)
		stt, sst := stateText(d.d)
		s.put(C.state, y, stt, sst, bg)
		s.put(C.by, y, trunc(strings.ReplaceAll(d.d.Protocol, ",", ""), 13), sMu, bg)
		ct, cst := countsText(d)
		s.put(C.counts, y, ct, cst, bg)
		inItems(s, C.added, y, R-C.added+1, d, bg)
		y++
	}
	drawNetDetail(m, s, L, R, y+1, true)
}

func drawNetNarrow(m *Model, s *screen, L, R, head int) {
	n := &m.net
	extra := (s.w - MinWidth) / 2
	C := struct{ name, host, state, counts, added int }{4, 21 + extra, 34 + extra, 45 + extra, 67 + extra}
	hy := max(4, head)
	for _, h := range []struct {
		x int
		t string
	}{{C.name, "Device"}, {C.host, "Address"}, {C.state, "State"}, {C.counts, "Counts as"}, {C.added, "In items"}} {
		s.put(h.x, hy, h.t, sMu, b3)
	}
	detailH := 11
	rows := s.h - 3 - detailH - (hy + 1) + 1
	top := window(n.sel, len(n.devs), rows)
	y := hy + 1
	for i := top; i < len(n.devs) && i < top+rows; i++ {
		d := n.devs[i]
		bg := b3
		if i == n.sel {
			bg = b2
			s.fill(1, y, s.w-2, 1, b2)
			s.put(L, y, "▸", sAc.bold(), b2)
		}
		name, nst := nameOf(d.d)
		s.put(C.name, y, trunc(name, C.host-C.name-1), nst, bg)
		s.put(C.host, y, trunc(d.d.Host, 12), sT2, bg)
		stt, sst := stateText(d.d)
		s.put(C.state, y, stt, sst, bg)
		ct, cst := countsText(d)
		s.put(C.counts, y, trunc(ct, C.added-C.counts-1), cst, bg)
		inItems(s, C.added, y, R-C.added+1, d, bg)
		y++
	}
	drawNetDetail(m, s, L, R, y+1, false)
}

// inItems is "✓ PS5" for a device matched by ID, "? TV" for an item that
// only shares its address.
func inItems(s *screen, x, y, w int, d netDev, bg role) {
	switch {
	case d.added != "":
		s.seq(x, y, []seg{{"✓ ", st{fg: ms}}, {trunc(d.added, w-2), sT2}}, bg)
	case d.maybe != "":
		s.seq(x, y, []seg{{"? ", sWa}, {trunc(d.maybe, w-2), sMu}}, bg)
	}
}

// drawNetDetail explains the selected device: its identity, why its state
// is what it is, how it can count, and its watts.
func drawNetDetail(m *Model, s *screen, L, R, y int, wide bool) {
	n := &m.net
	d := n.devs[n.sel]
	s.hrule(L, y-1, R-L+1, b3)
	name, nst := nameOf(d.d)
	bm, _ := brandModel(d.d)
	sub := "   " + d.d.Kind + " · " + bm
	if wide {
		sub += " · " + d.d.Host
	}
	s.seqMax(L, y, R-L+1, []seg{{name, nst}, {sub, sMu}}, b3)
	y++
	V := L + 11
	if !wide {
		V = L + 10
	}
	last := s.h - 3 // the last row inside the frame
	line := func(k string, parts ...seg) {
		if y+boolInt(!wide) > last {
			return
		}
		s.put(L, y, k, sMu, b3)
		if !wide && len(parts) > 1 {
			// narrow: the reason goes under its value
			s.seqMax(V, y, R-V+1, parts[:1], b3)
			y++
			parts = []seg{{strings.TrimSpace(parts[1].t), sMu}}
		}
		s.seqMax(V, y, R-V+1, parts, b3)
		y++
	}
	id, idWhy := idText(d)
	line("ID", seg{id, sT2}, seg{"   " + idWhy, sMu})
	stt, sst := stateText(d.d)
	line("State", seg{stt, sst}, seg{"   " + stateWhy(d), sMu})
	ct, cst := countsText(d)
	if d.d.Probe != "" {
		line("Counts as", seg{ct, cst}, seg{"   watts you enter × time it reports on; no sensor, so never measured", sMu})
	} else {
		line("Counts as", seg{ct, cst}, seg{"   no supported state query, so you enter watts and when it is on", sMu})
	}
	switch {
	case d.added != "":
		line("In items", seg{"✓ " + d.added, st{fg: ms}}, seg{"   matched by ID; enter does not add it twice", sMu})
	case d.maybe != "":
		line("In items", seg{"? " + d.maybe, sWa}, seg{"   uses this address, which alone does not prove it is this device", sMu})
	}
	if d.added == "" {
		if d.entry != nil {
			w := "~" + m.b.nf.watts(d.entry.Watts)
			line("Watts", seg{w, sT2.italic()}, seg{"   typical for " + d.entry.Label() + " in the catalog; you can change it", sMu})
		} else {
			why := "   not in the catalog; check the label, enter fills in the rest"
			if d.d.Model == "" {
				why = "   model not reported; check the label, enter fills in the rest"
			}
			line("Watts", seg{"not known", sT2}, seg{why, sMu})
		}
	}
	if n.note != "" && y <= last {
		s.put(L, y, trunc(n.note, R-L+1), sWa, b3)
	}
}

// idText is the ID shown and what it is worth.
func idText(d netDev) (string, string) {
	switch {
	case len(d.ids) > 0 && d.ambiguous:
		return d.ids[0], "another device in this scan reports it too, so it is not used"
	case len(d.ids) > 0:
		return d.ids[0], "stable; the device is found again if its address changes"
	case d.d.Probe != "":
		return orNone(d.d.ID), "not a stable ID; the item would be asked at this address only"
	}
	return orNone(d.d.ID), "an address or a name can change, so watts does not use it"
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// stateWhy says what the state rests on: only a power query answers on or
// standby, and no answer is never off.
func stateWhy(d netDev) string {
	switch {
	case d.d.State == discover.StateOn && d.d.Probe == config.ProbePS5:
		return "reported on over DDP; on would not prove it is gaming"
	case d.d.State == discover.StateOn:
		return "reported on to the power query"
	case d.d.State == discover.StateStandby:
		return "reported standby to the power query"
	case d.d.Probe != "":
		return "answered discovery but not the power query; not proof it is off"
	}
	by := strings.TrimSpace(strings.SplitN(d.d.Protocol, ",", 2)[0])
	if by == "" {
		by = "discovery"
	}
	return "reachable over " + by + "; it has no power query"
}

// ---- add from the view ------------------------------------------------

// newNetForm is the Add item form at its label step, filled in with what
// the scan found. Watts stay empty unless the catalog has the exact model.
func newNetForm(m *Model, d netDev, back modal) *itemForm {
	f := newAddForm(m)
	f.net, f.back, f.step = &d, back, stepLabel
	lf := f.label.fields
	if d.entry != nil {
		lf[lWatts].value = m.b.nf.short(d.entry.Watts)
	}
	lf[lKind].value, lf[lBrand].value, lf[lModel].value = d.d.Kind, d.d.Brand, d.d.Model
	for _, i := range []int{lKind, lBrand, lModel} {
		lf[i].note = ""
		if lf[i].value != "" {
			lf[i].note = " from network"
		}
	}
	lf[lModel].placeholder = "not reported"
	mf := f.mode.fields
	modes := []string{string(config.ModeSchedule), string(config.ModeManual)}
	if d.d.Probe != "" {
		modes = append([]string{string(config.ModeNetwork)}, modes...)
		mf[fProbe].value = d.d.Probe
	}
	mf[fMode].choices = modes
	mf[fSource].value = m.local
	mf[fHost].value = d.d.Host
	if len(d.ids) > 0 && !d.ambiguous {
		mf[fID].value = d.ids[0]
	}
	if d.entry != nil && d.d.Probe != "" {
		f.name.fields[nStandby].value = m.b.nf.short(d.entry.StandbyW)
	}
	return f
}
