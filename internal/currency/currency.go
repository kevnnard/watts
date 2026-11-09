// Package currency formats money in the user's main currency and converts it
// to the optional secondary currency at the user's rate (S5).
package currency

import (
	"math"
	"strconv"
	"strings"

	"github.com/kevnnard/watts/internal/config"
)

// Style is how a currency is written.
type Style struct {
	Prefix    string // e.g. "$" or "MXN "
	Thousands string
	Decimal   string
	Decimals  int  // decimals when the amount is not whole
	TrimWhole bool // drop decimals when the rounded amount is whole (COP)
}

var styles = map[string]Style{
	"COP": {Prefix: "$", Thousands: ".", Decimal: ",", Decimals: 2, TrimWhole: true},
	"USD": {Prefix: "US$", Thousands: ",", Decimal: ".", Decimals: 2},
	"EUR": {Prefix: "€", Thousands: ".", Decimal: ",", Decimals: 2},
}

// Codes lists the currencies with a known style; any other 3-letter code is
// formatted as "XYZ 1,234.56".
func Codes() []string { return []string{"COP", "USD", "EUR"} }

// StyleFor returns the style of code.
func StyleFor(code string) Style {
	if s, ok := styles[code]; ok {
		return s
	}
	return Style{Prefix: code + " ", Thousands: ",", Decimal: ".", Decimals: 2}
}

// Format writes amount in code's style. COP is "$1.234.567", with a decimal
// comma only when the amount is not whole ("$990,53").
func Format(amount float64, code string) string {
	s := StyleFor(code)
	dec := s.Decimals
	if s.TrimWhole {
		scale := math.Pow10(dec)
		if r := math.Round(amount*scale) / scale; r == math.Trunc(r) {
			dec = 0
		}
	}
	return format(amount, s, dec)
}

// FormatPrecision writes amount in code's style with exactly decimals digits.
func FormatPrecision(amount float64, code string, decimals int) string {
	return format(amount, StyleFor(code), decimals)
}

func format(amount float64, s Style, decimals int) string {
	neg := amount < 0
	num := strconv.FormatFloat(math.Abs(amount), 'f', decimals, 64)
	if neg && strings.Trim(num, "0.") == "" {
		neg = false // no "-$0"
	}
	whole, frac, _ := strings.Cut(num, ".")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(s.Prefix)
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(s.Thousands)
		}
		b.WriteRune(r)
	}
	if frac != "" {
		b.WriteString(s.Decimal)
		b.WriteString(frac)
	}
	return b.String()
}

// Convert turns a main-currency amount into the secondary currency.
// cu.Rate is main units per secondary unit. ok is false without a secondary.
func Convert(amountMain float64, cu config.Currency) (float64, bool) {
	if cu.Secondary == "" || cu.Rate <= 0 {
		return 0, false
	}
	return amountMain / cu.Rate, true
}

// Pair formats amount in the main currency, followed by the secondary
// currency in parentheses when one is set: "$10.000 (US$2.50)".
func Pair(amountMain float64, cu config.Currency) string {
	out := Format(amountMain, cu.Main)
	if v, ok := Convert(amountMain, cu); ok {
		out += " (" + Format(v, cu.Secondary) + ")"
	}
	return out
}
