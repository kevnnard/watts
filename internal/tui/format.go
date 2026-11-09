package tui

import (
	"math"
	"strconv"
	"strings"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/currency"
)

// numFmt formats numbers with the main currency's decimal separator, so a
// COP user reads "70,9 W" and "1,42 kWh" (S5).
type numFmt struct {
	cu  config.Currency
	dec string
}

func newNumFmt(cu config.Currency) numFmt {
	main := cu.Main
	if main == "" {
		main = config.DefaultCurrency
		cu.Main = main
	}
	return numFmt{cu: cu, dec: currency.StyleFor(main).Decimal}
}

// fixed writes v with n decimals.
func (f numFmt) fixed(v float64, n int) string {
	if p := math.Pow10(n); math.Round(v*p) == 0 {
		v = 0 // no "-0,0"
	}
	return strings.Replace(strconv.FormatFloat(v, 'f', n, 64), ".", f.dec, 1)
}

// watts is "70,9 W"; zero is "0 W".
func (f numFmt) watts(v float64) string {
	if math.Abs(v) < 0.05 {
		return "0 W"
	}
	return f.fixed(v, 1) + " W"
}

// typical is a catalog figure: "22 W" or "9,5 W".
func (f numFmt) typical(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64) + " W"
	}
	return f.fixed(v, 1) + " W"
}

func (f numFmt) kwh2(wh float64) string { return f.fixed(wh/1000, 2) }
func (f numFmt) kwh1(wh float64) string { return f.fixed(wh/1000, 1) }

// money is a cost in the main currency. Currencies written without decimals
// when whole (COP) are rounded to whole units, so "$1.406", not "$1.406,07".
func (f numFmt) money(v float64) string {
	if currency.StyleFor(f.cu.Main).TrimWhole {
		return currency.FormatPrecision(math.Round(v), f.cu.Main, 0)
	}
	return currency.Format(v, f.cu.Main)
}

// price is a tariff price per kWh, with its decimals: "$990,53".
func (f numFmt) price(v float64) string { return currency.Format(v, f.cu.Main) }

// second is the secondary currency, dimmed next to a main amount:
// "≈ US$2.43". Empty without a secondary currency.
func (f numFmt) second(v float64) string {
	x, ok := currency.Convert(v, f.cu)
	if !ok {
		return ""
	}
	return "≈ " + currency.Format(x, f.cu.Secondary)
}

// pct is "55 %".
func pct(part, whole float64) string {
	if whole <= 0 {
		return "0 %"
	}
	return strconv.Itoa(int(math.Round(part/whole*100))) + " %"
}

// parseNumber accepts "990,53", "990.53" and "1.234,5" style input.
func parseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// niceMax is the smallest "round" number ≥ v, for chart scales.
func niceMax(v float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if c := m * p; c >= v*(1-1e-9) {
			return c
		}
	}
	return 10 * p
}

// short is a compact number for chart labels: "1,5", "0,15", "150".
func (f numFmt) short(v float64) string {
	s := strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
	return strings.Replace(s, ".", f.dec, 1)
}

// rangeText turns "18:00-24:00" into "18:00–24:00".
func rangeText(ranges []string) string {
	out := make([]string, len(ranges))
	for i, r := range ranges {
		out[i] = strings.Replace(strings.TrimSpace(r), "-", "–", 1)
	}
	return strings.Join(out, ", ")
}

// orList is "Mac or PC Linux", "A, B or C".
func orList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
