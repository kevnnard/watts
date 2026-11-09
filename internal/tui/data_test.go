package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevnnard/watts/internal/config"
	"github.com/kevnnard/watts/internal/store"
	"github.com/kevnnard/watts/internal/tariff"
)

func openLocal(t *testing.T) *Local {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "watts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	l := NewLocal(st, filepath.Join(dir, "config.toml"), filepath.Join(dir, "tariffs.toml"))
	l.Loc = bogota
	return l
}

func TestLocalRefreshFromStore(t *testing.T) {
	l := openLocal(t)
	cfg := fixConfig()
	var xs []store.Sample
	for ts := fixNow.Add(-2 * time.Hour); !ts.After(fixNow); ts = ts.Add(time.Minute) {
		xs = append(xs, store.Sample{TS: ts, Source: "mac", Watts: 10, Seconds: 60})
	}
	if err := l.Store.AddSamples(xs); err != nil {
		t.Fatal(err)
	}
	r, src, tr, err := l.RefreshTrend(context.Background(), cfg, fixHistory(), fixNow)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasTariff || r.Tariff.Provider != "Enel" {
		t.Fatalf("tariff %+v", r.Tariff)
	}
	if r.Items[1].Name != "Mac" || !r.Items[1].On || r.Items[1].Watts != 10 {
		t.Fatalf("Mac now = %+v", r.Items[1])
	}
	if !r.Items[0].Offline {
		t.Fatal("PC Linux has no samples and must be offline")
	}
	if len(src) != 2 || src[0].Name != "linux" || src[0].Online || src[1].Name != "mac" || !src[1].Online {
		t.Fatalf("sources = %+v", src)
	}
	if len(tr.Total) != TrendMinutes || tr.Total[TrendMinutes-2] < 34.9 {
		t.Fatalf("trend total near now = %v (Mac 10 W + LG 25 W)", tr.Total[TrendMinutes-2])
	}
	if !tr.Partial[TrendMinutes-1] {
		t.Fatal("minutes without PC Linux samples must be partial")
	}
	if len(tr.Hours) != 21 {
		t.Fatalf("hours = %d, want 21 (00 to 20)", len(tr.Hours))
	}
}

func TestLocalEditorPersists(t *testing.T) {
	l := openLocal(t)
	cfg := fixConfig()
	if err := l.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	back, err := config.Load(l.ConfigPath)
	if err != nil || len(back.Items) != 5 || back.Currency.Rate != 4000 {
		t.Fatalf("config round trip: %v %+v", err, back)
	}
	h := fixHistory()
	if err := h.Add(tariff.Tariff{Provider: "Enel", PricePerKWh: 1000, Start: "2026-12-01"}); err != nil {
		t.Fatal(err)
	}
	if err := l.SaveTariffs(h); err != nil {
		t.Fatal(err)
	}
	hb, err := tariff.Load(l.TariffPath)
	if err != nil || len(hb.Tariffs) != 2 {
		t.Fatalf("tariff round trip: %v %+v", err, hb)
	}
	if err := l.SetManual("Desk fan", true, fixNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _, tr, err := l.RefreshTrend(context.Background(), cfg, h, fixNow)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Items[4].On || r.Items[4].Watts != 45 {
		t.Fatalf("Desk fan after toggle = %+v", r.Items[4])
	}
	if got := tr.Since["Desk fan"]; !got.Equal(fixNow.Add(-time.Hour)) {
		t.Fatalf("since = %v", got)
	}
}

// The model drives Local end to end: a toggle reaches the store and the
// next refresh shows it.
func TestModelOverLocal(t *testing.T) {
	l := openLocal(t)
	now := fixNow
	m := New(l, l, fixConfig(), fixHistory(), WithClock(func() time.Time { return now }))
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	run(t, m, m.Init())
	selectItem(t, m, "Desk fan")
	_, cmd := m.Update(keyMsg("space"))
	now = now.Add(time.Minute)
	run(t, m, cmd)
	if m.b.rows[4].state != stateOn {
		t.Fatalf("Desk fan state = %v after toggle", m.b.rows[4].state)
	}
	checkSize(t, m, 140, 40)
}
