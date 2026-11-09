package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/catalog"
	"github.com/kevnnard/watts/internal/config"
)

// The add-item modal of 01: step 1 picks a device from the catalog or types
// the label watts, step 2 picks the mode and its fields, step 3 the name and
// quantity. Edit opens the same modal at step 2 with the item's values.

type addStep int

const (
	stepCatalog addStep = iota
	stepLabel
	stepMode
	stepName
)

var modeHelp = []struct {
	mode config.Mode
	desc string
	long []string
}{
	{config.ModeMeasured, "sensor watts", []string{"Real watts from a sensor on", "this Mac or the Linux PC."}},
	{config.ModeFollows, "on with a machine", []string{"Fixed watts while any of the", "measured items it follows is on."}},
	{config.ModeSchedule, "on at set hours", []string{"Fixed watts during daily hours,", "e.g. 18:00-24:00. Ranges may", "cross midnight; separate several", "with commas."}},
	{config.ModeManual, "on when toggled", []string{"Fixed watts while you toggle it", "on with space in the items list."}},
}

type itemForm struct {
	editing string // name of the edited item, "" when adding
	step    addStep

	// step 1, catalog
	query   input
	results []catalog.Entry
	cur     int // len(results) is the "type watts" row
	top     int
	picked  *catalog.Entry
	brand   string // from the catalog entry or the label
	model   string

	// step 1, label
	label form

	// steps 2 and 3
	mode form
	name form
}

// label form fields
const (
	lWatts = iota
	lQty
	lKind
	lBrand
	lModel
)

// mode form fields
const (
	fMode = iota
	fSource
	fFollows
	fHours
)

// name form fields
const (
	nName = iota
	nWatts
	nQty
)

func newAddForm(m *Model) *itemForm {
	f := &itemForm{query: input{placeholder: "kind, brand or model"}}
	f.search()
	f.label = form{fields: []*input{
		{label: "Watts", note: " W  required"},
		{label: "Quantity", value: "1"},
		{label: "Kind", note: " optional"},
		{label: "Brand", note: " optional"},
		{label: "Model", placeholder: "optional"},
	}}
	f.initMode(m, config.Item{Mode: config.ModeManual, Quantity: 1})
	return f
}

func newEditForm(m *Model, it config.Item) *itemForm {
	f := &itemForm{editing: it.Name, step: stepMode}
	f.initMode(m, it)
	f.name.fields[nName].value = it.Name
	f.name.fields[nWatts].value = m.b.nf.short(it.Watts)
	q := it.Quantity
	if q < 1 {
		q = 1
	}
	f.name.fields[nQty].value = itoa(q)
	f.syncMode()
	return f
}

func (f *itemForm) initMode(m *Model, it config.Item) {
	var measured []string
	for _, x := range m.cfg.Items {
		if x.Mode == config.ModeMeasured && x.Name != f.editing {
			measured = append(measured, x.Name)
		}
	}
	follows := strings.Join(it.Follows, ", ")
	if follows == "" {
		follows = strings.Join(measured, ", ")
	}
	hours := strings.Join(it.Schedule, ", ")
	if hours == "" {
		hours = "18:00-24:00"
	}
	src := it.Source
	if src == "" {
		src = config.SourceMac
	}
	f.mode = form{fields: []*input{
		{label: "Mode", value: string(it.Mode), choices: []string{"measured", "follows", "schedule", "manual"}},
		{label: "Source", value: src, choices: []string{config.SourceMac, config.SourceLinux}},
		{label: "Follows", value: follows, note: " names, comma separated"},
		{label: "Hours", value: hours, note: " HH:MM-HH:MM, comma separated"},
	}}
	f.name = form{fields: []*input{
		{label: "Name"},
		{label: "Watts", note: " W per unit"},
		{label: "Quantity", value: "1"},
	}}
	f.syncMode()
}

// syncMode hides the fields the chosen mode does not use.
func (f *itemForm) syncMode() {
	md := config.Mode(f.mode.fields[fMode].value)
	f.mode.fields[fSource].hidden = md != config.ModeMeasured
	f.mode.fields[fFollows].hidden = md != config.ModeFollows
	f.mode.fields[fHours].hidden = md != config.ModeSchedule
	f.name.fields[nWatts].hidden = md == config.ModeMeasured
	f.name.fields[nQty].hidden = md == config.ModeMeasured
}

func (f *itemForm) search() {
	f.results = catalog.Search(f.query.value)
	f.cur, f.top = 0, 0
	if len(f.results) == 0 {
		f.cur = 0 // the "type watts" row
	}
}

func (f *itemForm) setErr(msg string) {
	switch f.step {
	case stepLabel:
		f.label.err = msg
	case stepMode:
		f.mode.err = msg
	default:
		f.name.err = msg
	}
}

func (f *itemForm) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	switch f.step {
	case stepCatalog:
		switch ks {
		case "esc":
			m.modal = nil
		case "up":
			if f.cur > 0 {
				f.cur--
			}
		case "down":
			if f.cur < len(f.results) {
				f.cur++
			}
		case "tab":
			f.step = stepLabel
		case "enter":
			if f.cur == len(f.results) {
				f.step = stepLabel
				return nil
			}
			e := f.results[f.cur]
			f.pick(m, &e)
		default:
			if f.query.edit(k) {
				f.search()
			}
		}
	case stepLabel:
		switch ks {
		case "esc":
			f.step = stepCatalog
		case "enter":
			w, ok := parseNumber(f.label.fields[lWatts].value)
			if !ok || w <= 0 {
				f.label.err = "watts must be greater than 0"
				f.label.focus = lWatts
				return nil
			}
			if _, ok := parseQty(f.label.fields[lQty].value); !ok {
				f.label.err = qtyErr
				f.label.focus = lQty
				return nil
			}
			f.pick(m, nil)
		default:
			f.label.key(k)
		}
	case stepMode:
		switch ks {
		case "esc":
			if f.editing != "" {
				m.modal = nil
			} else if f.picked != nil {
				f.step = stepCatalog
			} else {
				f.step = stepLabel
			}
		case "enter":
			f.mode.err = ""
			f.step = stepName
			f.name.focus = nName
		default:
			f.mode.key(k)
			f.syncMode()
		}
	case stepName:
		switch ks {
		case "esc":
			f.step = stepMode
		case "enter":
			return f.save(m)
		default:
			f.name.key(k)
		}
	}
	return nil
}

// pick moves to step 2 with the catalog entry or the typed label values.
func (f *itemForm) pick(m *Model, e *catalog.Entry) {
	f.picked = e
	var kind, brand, model string
	var watts float64
	qty := "1"
	if e != nil {
		kind, brand, model, watts = e.Kind, e.Brand, e.Model, e.Watts
	} else {
		lf := f.label.fields
		watts, _ = parseNumber(lf[lWatts].value)
		qty = strings.TrimSpace(lf[lQty].value)
		kind, brand, model = strings.TrimSpace(lf[lKind].value), strings.TrimSpace(lf[lBrand].value), strings.TrimSpace(lf[lModel].value)
	}
	md := config.ModeManual
	switch kind {
	case catalog.KindMonitor, catalog.KindSpeaker:
		md = config.ModeFollows
	case catalog.KindBulb:
		md = config.ModeSchedule
	}
	f.mode.fields[fMode].value = string(md)
	f.mode.focus = fMode
	name := strings.TrimSpace(brand + " " + model)
	if name == "" {
		name = strings.ToUpper(kind[:min(1, len(kind))]) + kind[min(1, len(kind)):]
	}
	f.name.fields[nName].value = uniqueName(m.cfg, name)
	f.name.fields[nWatts].value = m.b.nf.short(watts)
	f.name.fields[nQty].value = qty
	f.brand, f.model = brand, model
	f.syncMode()
	f.step = stepMode
}

const qtyErr = "quantity must be a whole number of at least 1"

func parseQty(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil && n >= 1
}

// uniqueName appends " 2", " 3"… when name is taken.
func uniqueName(cfg *config.Config, name string) string {
	if name == "" {
		return ""
	}
	taken := func(n string) bool {
		for _, it := range cfg.Items {
			if strings.EqualFold(it.Name, n) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		if n := name + " " + itoa(i); !taken(n) {
			return n
		}
	}
}

// item builds the item from the form.
func (f *itemForm) item(m *Model) (config.Item, string) {
	mf, nf := f.mode.fields, f.name.fields
	it := config.Item{Name: strings.TrimSpace(nf[nName].value), Mode: config.Mode(mf[fMode].value), Brand: f.brand, Model: f.model}
	if f.editing != "" {
		if old, ok := m.cfg.Item(f.editing); ok {
			it.Brand, it.Model, it.RestW, it.PSUEfficiency = old.Brand, old.Model, old.RestW, old.PSUEfficiency
		}
	}
	if it.Mode == config.ModeMeasured {
		it.Source, it.Quantity = mf[fSource].value, 1
		if it.Source == config.SourceLinux && it.RestW == 0 {
			it.RestW, it.PSUEfficiency = config.DefaultRestW, config.DefaultPSUEfficiency
		}
		if it.Source != config.SourceLinux {
			it.RestW, it.PSUEfficiency = 0, 0
		}
		return it, ""
	}
	it.RestW, it.PSUEfficiency = 0, 0
	if it.Mode == config.ModeFollows && f.editing != "" {
		// The form has no DDC fields; keep the ones the config file sets.
		if old, ok := m.cfg.Item(f.editing); ok && old.Mode == config.ModeFollows {
			it.DDC, it.MinW, it.MaxW, it.StandbyW = old.DDC, old.MinW, old.MaxW, old.StandbyW
		}
	}
	w, ok := parseNumber(nf[nWatts].value)
	if !ok {
		w = 0 // config.Validate reports it
	}
	it.Watts = w
	q, ok := parseQty(nf[nQty].value)
	if !ok {
		return it, qtyErr
	}
	it.Quantity = q
	switch it.Mode {
	case config.ModeFollows:
		it.Follows = splitList(mf[fFollows].value)
	case config.ModeSchedule:
		it.Schedule = splitList(mf[fHours].value)
	}
	return it, ""
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// save validates the new config with config.Validate and persists it.
func (f *itemForm) save(m *Model) tea.Cmd {
	it, msg := f.item(m)
	if msg != "" {
		f.name.err = msg
		return nil
	}
	next := cloneConfig(m.cfg)
	flash := it.Name + " added"
	if f.editing == "" {
		next.Items = append(next.Items, it)
	} else {
		flash = it.Name + " saved"
		for i := range next.Items {
			if next.Items[i].Name == f.editing {
				next.Items[i] = it
			}
			// a renamed measured item keeps its followers
			if it.Name != f.editing {
				for j, n := range next.Items[i].Follows {
					if n == f.editing {
						next.Items[i].Follows[j] = it.Name
					}
				}
			}
		}
	}
	cmd, verr := m.saveConfig(next, flash)
	if verr != "" {
		f.name.err = verr
		return nil
	}
	if f.editing == "" {
		m.sel = len(next.Items) - 1
	}
	return cmd
}

func (f *itemForm) keys(m *Model) []key {
	switch f.step {
	case stepCatalog:
		if len(f.results) == 0 {
			return []key{{"enter", "type watts"}, {"backspace", "edit search"}, {"esc", "cancel"}}
		}
		return []key{{"↑↓", "move"}, {"enter", "choose"}, {"tab", "type watts"}, {"esc", "cancel"}}
	case stepLabel:
		return []key{{"tab", "next field"}, {"enter", "continue to mode"}, {"esc", "back to catalog"}}
	case stepMode:
		back := "back"
		if f.editing != "" {
			back = "cancel"
		}
		return []key{{"←→", "change"}, {"tab", "next field"}, {"enter", "continue"}, {"esc", back}}
	}
	verb := "add item"
	if f.editing != "" {
		verb = "save"
	}
	return []key{{"tab", "next field"}, {"enter", verb}, {"esc", "back to mode"}}
}

func (f *itemForm) draw(m *Model, s *screen) {
	X, Y, W, H := modalRect(s, 100, 29)
	title, right := "Add item", ""
	switch f.step {
	case stepCatalog:
		right = "step 1 of 3 · device"
	case stepLabel:
		right = "step 1 of 3 · from label"
	case stepMode:
		right = "step 2 of 3 · mode"
	case stepName:
		right = "step 3 of 3 · name"
	}
	if f.editing != "" {
		title = "Edit " + f.editing
	}
	frame(s, X, Y, W, H, title, right)
	steps := []string{"1 Device", "2 Mode", "3 Name and quantity"}
	cur := map[addStep]int{stepCatalog: 0, stepLabel: 0, stepMode: 1, stepName: 2}[f.step]
	var parts []seg
	for i, t := range steps {
		if i > 0 {
			parts = append(parts, seg{"  ›  ", sMu})
		}
		sty := sMu
		if i == cur {
			sty = sAc.bold()
		}
		if f.editing != "" && i == 0 {
			sty = sBd
		}
		parts = append(parts, seg{t, sty})
	}
	s.seqMax(X+2, Y+1, W-4, parts, b3)
	// two panes when wide enough, else the left pane only
	L, lw := X+2, W-4
	R, rw := 0, 0
	if W >= 96 {
		lw = W - 38
		R, rw = X+lw+5, W-lw-7
		for r := Y + 3; r < Y+H-1; r++ {
			s.put(X+lw+3, r, "│", sBd, b3)
		}
	}
	switch f.step {
	case stepCatalog:
		f.drawCatalog(m, s, L, Y, lw, H, R, rw)
	case stepLabel:
		f.drawLabel(m, s, L, Y, lw, R, rw)
	case stepMode:
		f.drawMode(m, s, L, Y, lw, R, rw)
	case stepName:
		f.drawName(m, s, L, Y, lw, H, R, rw)
	}
}

func (f *itemForm) drawCatalog(m *Model, s *screen, L, Y, lw, H, R, rw int) {
	s.seqMax(L, Y+3, lw, []seg{{"Search catalog", sT2}, {"  kind, brand or model", sMu}}, b3)
	s.field(L, Y+4, lw, "› "+f.query.value, true, "")
	n := len(f.results)
	switch {
	case n == 0:
		s.put(L, Y+5, "0 matches", sMu, b3)
	case n == 1:
		s.put(L, Y+5, "1 match", sMu, b3)
	default:
		s.put(L, Y+5, itoa(n)+" matches · best first", sMu, b3)
	}
	ly := min(Y+16, Y+H-3)
	if n == 0 {
		q := trunc(f.query.value, lw-30)
		s.put(L+2, Y+8, trunc(`No catalog devices match "`+q+`"`, lw-2), sTx, b3)
		s.put(L+2, Y+9, trunc("Try a kind (monitor, bulb, fan) or just the brand.", lw-2), sMu, b3)
	} else {
		typR := L + lw - 7
		s.put(L+2, Y+7, "Kind", sMu, b3)
		s.put(L+13, Y+7, "Brand", sMu, b3)
		s.put(L+20, Y+7, "Model", sMu, b3)
		s.rput(typR, Y+7, "Typical", sMu, b3)
		rows := ly - 1 - (Y + 8)
		if f.cur < len(f.results) {
			if f.cur < f.top {
				f.top = f.cur
			}
			if f.cur >= f.top+rows {
				f.top = f.cur - rows + 1
			}
		}
		terms := strings.Fields(strings.ToLower(f.query.value))
		for i := f.top; i < n && i < f.top+rows; i++ {
			e := f.results[i]
			y := Y + 8 + i - f.top
			bg := b3
			if i == f.cur {
				bg = b2
				s.fill(L, y, lw, 1, b2)
				s.put(L, y, "▸", sAc.bold(), b2)
			}
			s.put(L+2, y, trunc(e.Kind, 10), sT2, bg)
			bst := sTx
			if matches(e.Brand, terms) {
				bst = sAc.bold().ul()
			}
			s.put(L+13, y, trunc(e.Brand, 6), bst, bg)
			w := m.b.nf.typical(e.Watts)
			model := e.Model
			if e.Note != "" {
				model += " · " + e.Note
			}
			s.put(L+20, y, trunc(model, typR-textWidth(w)-1-(L+20)), sTx, bg)
			s.rput(typR, y, w, sTx.bold(), bg)
			if countSame(m.cfg, e) > 0 {
				s.put(typR+2, y, "added", sMu, bg)
			}
		}
	}
	s.hrule(L, ly-1, lw, b3)
	bg := b3
	if f.cur == n {
		bg = b2
		s.fill(L, ly, lw, 1, b2)
		s.put(L, ly, "▸", sAc.bold(), b2)
	}
	s.seqMax(L+2, ly, lw-2, []seg{{"+ ", sAc.bold()}, {"Type watts from the device label", sTx}, {"  not in the catalog", sMu}}, bg)
	if rw <= 0 {
		return
	}
	if n == 0 || f.cur == n {
		s.put(R, Y+3, "Not in the catalog?", sTx.bold(), b3)
		for i, l := range []string{"Most devices print their watts", "on a label. Type that number", "and pick a mode in step 2.", "", "Your search stays here if you", "go back."} {
			s.put(R, Y+5+i, trunc(l, rw), sMu, b3)
		}
		return
	}
	e := f.results[f.cur]
	s.put(R, Y+3, trunc(e.Label(), rw), sTx.bold(), b3)
	sub := e.Kind
	if e.Note != "" {
		sub += " · " + e.Note
	}
	s.put(R, Y+4, trunc(sub, rw), sMu, b3)
	s.seq(R, Y+6, []seg{{"Typical  ", sMu}, {m.b.nf.typical(e.Watts), sTx.bold()}}, b3)
	if c := countSame(m.cfg, e); c > 0 {
		s.put(R, Y+7, trunc("Already in your items "+times(c), rw), sWa, b3)
	}
	s.put(R, Y+9, "Next, choose how it counts", sT2, b3)
	for i, h := range modeHelp {
		s.seqMax(R, Y+10+i, rw, []seg{{padRight(string(h.mode), 8), st{fg: modeRole(h.mode)}}, {"  " + h.desc, sMu}}, b3)
	}
	s.put(R, Y+15, trunc("Catalog watts are typical, not", rw), sMu, b3)
	s.put(R, Y+16, trunc("measured. Adjust them in step 3.", rw), sMu, b3)
}

func matches(s string, terms []string) bool {
	s = strings.ToLower(s)
	for _, t := range terms {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

func countSame(cfg *config.Config, e catalog.Entry) int {
	n := 0
	for _, it := range cfg.Items {
		if strings.EqualFold(it.Brand, e.Brand) && strings.EqualFold(it.Model, e.Model) {
			n++
		}
	}
	return n
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return itoa(n) + " times"
}

func padRight(s string, w int) string {
	if d := w - textWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func (f *itemForm) drawLabel(m *Model, s *screen, L, Y, lw, R, rw int) {
	s.put(L, Y+3, trunc("Type the watts printed on the device label", lw), sTx.bold(), b3)
	f.label.draw(s, L, Y+5, 2, 12, 24, lw-37)
	lf := f.label.fields
	if w, ok := parseNumber(lf[lWatts].value); ok && w > 0 {
		q, ok := parseQty(lf[lQty].value)
		if !ok {
			q = 1
		}
		nf := m.b.nf
		s.seqMax(L, Y+16, lw, []seg{{"Counts as ", sMu}, {nf.typical(w) + " × " + itoa(q) + " = " + nf.typical(w*float64(q)), sTx.bold()}, {" while on", sMu}}, b3)
	}
	errLine(s, L, Y+18, lw, f.label.err)
	if rw <= 0 {
		return
	}
	s.put(R, Y+3, "Where to find it", sTx.bold(), b3)
	for i, l := range []string{`Look for "Potencia" or a figure`, "ending in W, usually next to", `"120 V ~ 60 Hz".`, "", "Only amps? Watts ≈ 120 × A.", "", "Label watts are a maximum, so", "the estimate may run high."} {
		s.put(R, Y+5+i, trunc(l, rw), sMu, b3)
	}
}

func (f *itemForm) drawMode(m *Model, s *screen, L, Y, lw, R, rw int) {
	s.put(L, Y+3, trunc("How does it count?", lw), sTx.bold(), b3)
	f.mode.draw(s, L, Y+5, 2, 12, 24, lw-37)
	md := config.Mode(f.mode.fields[fMode].value)
	y := Y + 10
	for _, h := range modeHelp {
		if h.mode != md {
			continue
		}
		for i, l := range h.long {
			s.put(L, y+i, trunc(l, lw), sMu, b3)
		}
	}
	errLine(s, L, Y+16, lw, f.mode.err)
	if rw <= 0 {
		return
	}
	s.put(R, Y+3, "Modes", sTx.bold(), b3)
	for i, h := range modeHelp {
		sty := sMu
		mark := "  "
		if h.mode == md {
			sty, mark = sT2, "▸ "
		}
		s.seqMax(R, Y+5+i, rw, []seg{{mark, sAc.bold()}, {padRight(string(h.mode), 8), st{fg: modeRole(h.mode)}}, {"  " + h.desc, sty}}, b3)
	}
	s.put(R, Y+10, trunc("Measured, follows and schedule", rw), sMu, b3)
	s.put(R, Y+11, trunc("items count by themselves;", rw), sMu, b3)
	s.put(R, Y+12, trunc("manual ones need space.", rw), sMu, b3)
}

func (f *itemForm) drawName(m *Model, s *screen, L, Y, lw, H, R, rw int) {
	s.put(L, Y+3, trunc("Name and quantity", lw), sTx.bold(), b3)
	f.name.draw(s, L, Y+5, 2, 12, 24, lw-37)
	it, _ := f.item(m)
	nf := m.b.nf
	if it.Mode != config.ModeMeasured && it.Watts > 0 && it.Quantity >= 1 {
		s.seqMax(L, Y+12, lw, []seg{{"Counts as ", sMu}, {nf.typical(it.Watts) + " × " + itoa(it.Quantity) + " = " + nf.typical(it.TotalWatts()), sTx.bold()}, {" while on", sMu}}, b3)
	}
	if f.editing != "" && it.Name != f.editing {
		if old, ok := m.cfg.Item(f.editing); ok && old.Mode == config.ModeManual {
			s.put(L, Y+14, trunc("Renaming starts a new toggle history.", lw), sWa, b3)
		}
	}
	errLine(s, L, Y+16, lw, f.name.err)
	if rw <= 0 {
		return
	}
	s.put(R, Y+3, "Summary", sTx.bold(), b3)
	s.seqMax(R, Y+5, rw, []seg{{padRight(string(it.Mode), 8), st{fg: modeRole(it.Mode)}}}, b3)
	var rule string
	switch it.Mode {
	case config.ModeMeasured:
		rule = "source " + it.Source
	case config.ModeFollows:
		rule = "on while " + orList(it.Follows) + " is on"
	case config.ModeSchedule:
		rule = "on " + rangeText(it.Schedule)
	case config.ModeManual:
		rule = "on while toggled with space"
	}
	s.put(R, Y+6, trunc(rule, rw), sMu, b3)
	s.put(R, Y+8, trunc("Editing a rule recomputes past", rw), sMu, b3)
	s.put(R, Y+9, trunc("days too.", rw), sMu, b3)
}
