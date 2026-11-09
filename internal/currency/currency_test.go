package currency

import (
	"testing"

	"github.com/kevnnard/watts/internal/config"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		amount float64
		code   string
		want   string
	}{
		{1234567, "COP", "$1.234.567"},
		{0, "COP", "$0"},
		{999, "COP", "$999"},
		{1000, "COP", "$1.000"},
		{990.53, "COP", "$990,53"},
		{1234.5, "COP", "$1.234,50"},
		{1234.999, "COP", "$1.235"}, // rounds to a whole amount
		{-45200, "COP", "-$45.200"},
		{1234.5, "USD", "US$1,234.50"},
		{12, "USD", "US$12.00"},
		{1234.5, "EUR", "€1.234,50"},
		{1234.5, "MXN", "MXN 1,234.50"},
	}
	for _, tt := range tests {
		if got := Format(tt.amount, tt.code); got != tt.want {
			t.Errorf("Format(%v, %s) = %q, want %q", tt.amount, tt.code, got, tt.want)
		}
	}
}

func TestFormatPrecision(t *testing.T) {
	tests := []struct {
		amount   float64
		code     string
		decimals int
		want     string
	}{
		{1234.56, "COP", 0, "$1.235"},
		{1234.5, "COP", 2, "$1.234,50"},
		{0.004, "USD", 2, "US$0.00"},
		{1234567.891, "USD", 3, "US$1,234,567.891"},
	}
	for _, tt := range tests {
		if got := FormatPrecision(tt.amount, tt.code, tt.decimals); got != tt.want {
			t.Errorf("FormatPrecision(%v, %s, %d) = %q, want %q", tt.amount, tt.code, tt.decimals, got, tt.want)
		}
	}
}

func TestConvertAndPair(t *testing.T) {
	cu := config.Currency{Main: "COP", Secondary: "USD", Rate: 4000}
	if got, ok := Convert(10000, cu); !ok || got != 2.5 {
		t.Errorf("Convert = %v, %v; want 2.5, true", got, ok)
	}
	if got := Pair(10000, cu); got != "$10.000 (US$2.50)" {
		t.Errorf("Pair = %q", got)
	}
	none := config.Currency{Main: "COP"}
	if _, ok := Convert(10000, none); ok {
		t.Error("Convert without secondary returned ok")
	}
	if got := Pair(10000, none); got != "$10.000" {
		t.Errorf("Pair without secondary = %q", got)
	}
}
