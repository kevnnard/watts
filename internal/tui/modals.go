package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/currency"
	"github.com/kevnnard/watts/internal/tariff"
)

// ---- delete -----------------------------------------------------------

type confirmDelete struct {
	name string
	err  string
}

func (c *confirmDelete) setErr(msg string) { c.err = msg }

func (c *confirmDelete) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "n":
		m.modal = nil
	case "y", "enter":
		next := cloneConfig(m.cfg)
		next.Items = next.Items[:0]
		for _, it := range m.cfg.Items {
			if it.Name != c.name {
				next.Items = append(next.Items, it)
			}
		}
		cmd, verr := m.saveConfig(next, c.name+" deleted")
		if verr != "" {
			c.err = verr
		}
		return cmd
	}
	return nil
}

func (c *confirmDelete) keys(*Model) []key {
	return []key{{"y", "delete"}, {"n", "keep"}, {"esc", "cancel"}}
}

func (c *confirmDelete) draw(m *Model, s *screen) {
	X, Y, W, H := modalRect(s, 64, 9)
	frame(s, X, Y, W, H, "Delete item", "")
	L, lw := X+2, W-4
	s.seqMax(L, Y+2, lw, []seg{{"Delete ", sTx}, {c.name, sTx.bold()}, {"?", sTx}}, b3)
	s.put(L, Y+3, trunc("It leaves the config. Samples stay in the store, but", lw), sMu, b3)
	s.put(L, Y+4, trunc("today and the bill stop counting it.", lw), sMu, b3)
	errLine(s, L, Y+6, lw, c.err)
}

// ---- tariffs ----------------------------------------------------------

type tariffModal struct {
	adding bool
	form   form
	err    string
}

const (
	tProvider = iota
	tPrice
	tStart
	tCutoff
)

func newTariffModal(*Model) *tariffModal { return &tariffModal{} }

func (t *tariffModal) setErr(msg string) {
	if t.adding {
		t.form.err = msg
	} else {
		t.err = msg
	}
}

// saved returns to the list after a tariff was added.
func (t *tariffModal) saved(tariff.History) { t.adding, t.form, t.err = false, form{}, "" }

func (t *tariffModal) open(m *Model) {
	prov := ""
	if n := len(m.hist.Tariffs); n > 0 {
		prov = m.hist.Tariffs[n-1].Provider
	}
	t.adding = true
	t.form = form{fields: []*input{
		{label: "Provider", value: prov},
		{label: "Price", note: " per kWh, " + m.cfg.Currency.Main},
		{label: "Start", value: m.now().Format(tariff.DateLayout), note: " YYYY-MM-DD"},
		{label: "Cut-off day", value: itoa(m.hist.CutoffDay), note: " 1–31, the day a period starts"},
	}, focus: tPrice}
}

func (t *tariffModal) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if !t.adding {
		switch k.String() {
		case "esc", "t", "q":
			m.modal = nil
		case "a":
			t.open(m)
		}
		return nil
	}
	switch k.String() {
	case "esc":
		t.adding, t.form = false, form{}
		return nil
	case "enter":
		return t.save(m)
	}
	t.form.key(k)
	return nil
}

// save builds the history with the new tariff and validates it with
// tariff.History.Validate; an unparsable number is passed on as 0 so the
// message is the validator's.
func (t *tariffModal) save(m *Model) tea.Cmd {
	f := t.form.fields
	price, ok := parseNumber(f[tPrice].value)
	if !ok {
		price = 0
	}
	cut, err := strconv.Atoi(strings.TrimSpace(f[tCutoff].value))
	if err != nil {
		cut = 0
	}
	tr := tariff.Tariff{Provider: strings.TrimSpace(f[tProvider].value), PricePerKWh: price, Start: strings.TrimSpace(f[tStart].value)}
	next := cloneHistory(m.hist)
	next.CutoffDay = cut
	if err := next.Add(tr); err != nil {
		t.form.err = err.Error()
		return nil
	}
	cmd, verr := m.saveTariffs(next, "tariff "+tr.Provider+" from "+tr.Start+" saved")
	if verr != "" {
		t.form.err = verr
	}
	return cmd
}

func (t *tariffModal) keys(*Model) []key {
	if t.adding {
		return []key{{"tab", "next field"}, {"enter", "save tariff"}, {"esc", "back to list"}}
	}
	return []key{{"a", "add tariff"}, {"esc", "close"}}
}

func (t *tariffModal) draw(m *Model, s *screen) {
	X, Y, W, H := modalRect(s, 92, 22)
	right := "cut-off day " + itoa(m.hist.CutoffDay)
	if t.adding {
		right = "add"
	}
	frame(s, X, Y, W, H, "Tariffs", right)
	L, lw := X+2, W-4
	nf := m.b.nf
	if t.adding {
		s.put(L, Y+2, trunc("A new price takes effect on its start date. A start that", lw), sMu, b3)
		s.put(L, Y+3, trunc("matches an existing tariff replaces it.", lw), sMu, b3)
		t.form.draw(s, L, Y+5, 2, 14, 24, lw-39)
		errLine(s, L, Y+14, lw, t.form.err)
		return
	}
	p := m.hist.Period(m.now())
	s.seqMax(L, Y+2, lw, []seg{{"Billing periods start on day ", sMu}, {itoa(m.hist.CutoffDay), sTx.bold()}, {" · this one " + p.Start.Format("Jan 2") + " → " + p.End.Format("Jan 2"), sMu}}, b3)
	priceR := L + 30
	s.put(L+2, Y+4, "Provider", sMu, b3)
	s.rput(priceR, Y+4, "Per kWh", sMu, b3)
	s.put(priceR+3, Y+4, "From", sMu, b3)
	s.put(priceR+16, Y+4, "Note", sMu, b3)
	s.hrule(L, Y+5, lw, b3)
	cur, ok := m.hist.ForPeriod(p)
	rows := H - 10
	ts := m.hist.Tariffs
	if len(ts) > rows {
		ts = ts[len(ts)-rows:]
	}
	if len(ts) == 0 {
		s.put(L+2, Y+6, "No tariffs yet. Press a to add one; costs show $0 until then.", sMu, b3)
	}
	for i := len(ts) - 1; i >= 0; i-- {
		tr := ts[i]
		y := Y + 6 + (len(ts) - 1 - i)
		g, gs := "○", sMu
		if ok && tr == cur {
			g, gs = "●", sAc
		}
		s.put(L, y, g, gs, b3)
		s.put(L+2, y, trunc(tr.Provider, priceR-L-12), sTx.bold(), b3)
		s.rput(priceR, y, nf.price(tr.PricePerKWh), sTx, b3)
		s.put(priceR+3, y, tr.Start, sT2, b3)
		s.put(priceR+16, y, trunc(tr.Note, L+lw-(priceR+16)), sMu, b3)
	}
	s.seqMax(L, Y+H-3, lw, []seg{{"●", sAc}, {" prices this period", sMu}, {"   ○", sMu}, {" history; past days keep their price", sMu}}, b3)
	errLine(s, L, Y+H-2, lw, t.err)
}

// ---- currency ---------------------------------------------------------

type currencyModal struct{ form form }

const (
	cMain = iota
	cSecondary
	cRate
)

func newCurrencyModal(m *Model) *currencyModal {
	cu := m.cfg.Currency
	rate := ""
	if cu.Rate > 0 {
		rate = strconv.FormatFloat(cu.Rate, 'f', -1, 64)
	}
	return &currencyModal{form: form{fields: []*input{
		{label: "Main", value: cu.Main, note: " " + strings.Join(currency.Codes(), ", ") + " or any 3-letter code"},
		{label: "Secondary", value: cu.Secondary, placeholder: "optional", note: " shown dimmed next to main"},
		{label: "Rate", value: rate, placeholder: "e.g. 4000", note: " main units per 1 secondary"},
	}}}
}

func (c *currencyModal) setErr(msg string) { c.form.err = msg }

func (c *currencyModal) value() config.Currency {
	f := c.form.fields
	cu := config.Currency{Main: strings.ToUpper(strings.TrimSpace(f[cMain].value)), Secondary: strings.ToUpper(strings.TrimSpace(f[cSecondary].value))}
	if r, ok := parseNumber(f[cRate].value); ok {
		cu.Rate = r
	}
	return cu
}

func (c *currencyModal) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.modal = nil
		return nil
	case "enter":
		next := cloneConfig(m.cfg)
		next.Currency = c.value()
		cmd, verr := m.saveConfig(next, "currency saved")
		if verr != "" {
			c.form.err = verr
		}
		return cmd
	}
	c.form.key(k)
	return nil
}

func (c *currencyModal) keys(*Model) []key {
	return []key{{"tab", "next field"}, {"enter", "save"}, {"esc", "cancel"}}
}

func (c *currencyModal) draw(m *Model, s *screen) {
	X, Y, W, H := modalRect(s, 76, 14)
	frame(s, X, Y, W, H, "Currency", "")
	L, lw := X+2, W-4
	s.put(L, Y+2, trunc("Costs show in the main currency; the secondary one is a", lw), sMu, b3)
	s.put(L, Y+3, trunc("conversion at your rate, not a live exchange rate.", lw), sMu, b3)
	c.form.draw(s, L, Y+5, 2, 12, 14, lw-27)
	cu := c.value()
	if cu.Validate() == nil {
		s.seqMax(L, Y+11, lw, []seg{{"Preview  ", sMu}, {currency.Format(10000, cu.Main), sTx.bold()}, {"  " + newNumFmt(cu).second(10000), sMu}}, b3)
	}
	errLine(s, L, Y+12, lw, c.form.err)
}
