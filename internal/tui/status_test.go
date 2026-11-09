package tui

import (
	"strings"
	"testing"
)

func TestStatusTextPlain(t *testing.T) {
	got := StatusText(fixReport(false), fixSources(false), fixConfig(), false)
	want := `watts  Sun Nov 8 20:15  Enel · $990,53 per kWh

Now     132,8 W  80,8 W measured + ~52,0 W estimated
  ● PC Linux     measured     70,9 W    0,78 kWh today
  ● Mac          measured      9,9 W    0,14 kWh today
  ● LG 29WQ600   follows     ~25,0 W    0,31 kWh today
  ● Room lights  schedule    ~27,0 W    0,06 kWh today
  ○ Desk fan     manual          0 W    0,13 kWh today

Today   1,42 kWh  $1.406  ≈ US$0.35
Bill    $9.707 so far ≈ US$2.43 · projected $29.100 ≈ US$7.28
        Nov 1 → Dec 1 · day 8 of 30 · 9,8 kWh

Source  mac    ● online · last sample 20:15:02
Source  linux  ● online · last sample 20:14:58
`
	if got != want {
		t.Fatalf("StatusText:\n%s\nwant:\n%s", got, want)
	}
}

func TestStatusTextOffline(t *testing.T) {
	got := StatusText(fixReport(true), fixSources(true), fixConfig(), false)
	for _, want := range []string{
		"Now     61,9 W  9,9 W measured + ~52,0 W estimated  partial · PC Linux offline\n",
		"  ✗ PC Linux     measured    offline    0,70 kWh today\n",
		"· may be low: PC Linux missing\n",
		"Source  linux  ✗ offline · connection timed out · last sample 19:05:11\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("StatusText lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Error("plain StatusText has escape codes")
	}
}

func TestStatusTextANSI(t *testing.T) {
	got := StatusText(fixReport(false), fixSources(false), fixConfig(), true)
	if !strings.Contains(got, "\x1b[") {
		t.Fatal("tty StatusText has no styling")
	}
	plain := StatusText(fixReport(false), fixSources(false), fixConfig(), false)
	if stripANSI(got) != plain {
		t.Fatalf("styled text differs from plain once stripped:\n%s", stripANSI(got))
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestStatusTextDDC(t *testing.T) {
	got := StatusText(fixDDCReport(true), fixSources(false), fixDDCConfig(), false)
	for _, want := range []string{
		"Now     124,6 W  80,8 W measured + ~43,8 W estimated\n",
		"  ● LG 29WQ600   follows     ~16,8 W    0,31 kWh today · brightness 65 % · DDC via linux\n",
		"  ● Room lights  schedule    ~27,0 W    0,06 kWh today\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("StatusText lacks %q:\n%s", want, got)
		}
	}
	asleep := StatusText(fixDDCReport(false), fixSources(false), fixDDCConfig(), false)
	if want := "  ● LG 29WQ600   follows      ~0,3 W    0,31 kWh today · sleeping · DDC via linux\n"; !strings.Contains(asleep, want) {
		t.Errorf("StatusText lacks %q:\n%s", want, asleep)
	}
	// Without a fresh reading the row is today's.
	if plain := StatusText(fixReport(false), fixSources(false), fixDDCConfig(), false); strings.Contains(plain, "DDC") {
		t.Errorf("no reading, but a DDC note:\n%s", plain)
	}
}

func TestRuleTextDDC(t *testing.T) {
	cfg := fixDDCConfig()
	for _, tt := range []struct {
		on   bool
		want string
	}{{true, "brightness 65 % · DDC via linux"}, {false, "sleeping · DDC via linux"}} {
		b := newBoard(cfg, fixDDCReport(tt.on), fixSources(false), Trend{}, false)
		if got := b.rule(b.rows[2]); got != tt.want {
			t.Errorf("rule = %q, want %q", got, tt.want)
		}
	}
	b := newBoard(cfg, fixReport(false), fixSources(false), Trend{}, false)
	if got := b.rule(b.rows[2]); got != "on while Mac or PC Linux is on" {
		t.Errorf("rule without a reading = %q", got)
	}
}
