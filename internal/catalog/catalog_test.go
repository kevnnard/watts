package catalog

import (
	"testing"
)

func TestAllHasEnoughEntries(t *testing.T) {
	all := All()
	if len(all) < 40 {
		t.Fatalf("catalog has %d entries, want at least 40", len(all))
	}
	kinds := map[string]bool{}
	for _, e := range all {
		if e.Brand == "" || e.Model == "" || e.Kind == "" {
			t.Errorf("entry %+v has an empty brand, model or kind", e)
		}
		if e.Watts <= 0 {
			t.Errorf("entry %s %s has watts %v, want > 0", e.Brand, e.Model, e.Watts)
		}
		kinds[e.Kind] = true
	}
	for _, k := range []string{KindMonitor, KindBulb, KindStrip, KindFan, KindRouter, KindSpeaker, KindCharger, KindTV} {
		if !kinds[k] {
			t.Errorf("no entries of kind %q", k)
		}
	}
}

func TestSearch(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantFirst string // "Brand Model" of the first result, "" for no results
		wantMin   int
		wantKind  string // every result must have this kind, if set
	}{
		{name: "exact model", query: "29WQ600", wantFirst: "LG 29WQ600", wantMin: 1},
		{name: "case insensitive", query: "lg 29wq600", wantFirst: "LG 29WQ600", wantMin: 1},
		{name: "brand only", query: "lg", wantMin: 3},
		{name: "kind", query: "bulb", wantMin: 4, wantKind: KindBulb},
		{name: "kind and brand", query: "philips bulb", wantMin: 2, wantKind: KindBulb},
		{name: "kind plural-ish prefix", query: "rout", wantMin: 3, wantKind: KindRouter},
		{name: "no match", query: "zzz-nothing", wantFirst: "", wantMin: 0},
		{name: "empty returns all", query: "   ", wantMin: 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Search(tt.query)
			if len(got) < tt.wantMin {
				t.Fatalf("Search(%q) returned %d results, want at least %d", tt.query, len(got), tt.wantMin)
			}
			if tt.wantFirst == "" && tt.wantMin == 0 && len(got) != 0 {
				t.Fatalf("Search(%q) returned %d results, want none", tt.query, len(got))
			}
			if tt.wantFirst != "" && got[0].Brand+" "+got[0].Model != tt.wantFirst {
				t.Errorf("Search(%q)[0] = %s %s, want %s", tt.query, got[0].Brand, got[0].Model, tt.wantFirst)
			}
			if tt.wantKind != "" {
				for _, e := range got {
					if e.Kind != tt.wantKind {
						t.Errorf("Search(%q) returned %s %s of kind %q, want %q", tt.query, e.Brand, e.Model, e.Kind, tt.wantKind)
					}
				}
			}
		})
	}
}

func TestFind(t *testing.T) {
	e, ok := Find("lg", "29wq600")
	if !ok {
		t.Fatal("Find(lg, 29wq600) not found")
	}
	if e.Kind != KindMonitor || e.Watts <= 0 {
		t.Errorf("Find returned %+v", e)
	}
	if _, ok := Find("LG", "nope"); ok {
		t.Error("Find(LG, nope) found an entry")
	}
}

func TestAllReturnsCopy(t *testing.T) {
	a := All()
	a[0].Watts = -1
	if All()[0].Watts == -1 {
		t.Error("All() exposes the internal slice")
	}
}

func TestDDCRange(t *testing.T) {
	e, ok := Find("LG", "29WQ600")
	if !ok || e.MinW != 7 || e.MaxW != 22 || e.StandbyW != 0.3 {
		t.Fatalf("LG 29WQ600 = %+v, want min 7, max 22, standby 0.3", e)
	}
	for _, e := range All() {
		if e.Model == "29WQ600" {
			continue
		}
		if e.MinW != 0 || e.MaxW != 0 || (e.StandbyW != 0 && e.Probe == "") {
			t.Errorf("%s has a DDC range %+v, want none", e.Label(), e)
		}
	}
}

func TestNetworkDevices(t *testing.T) {
	for _, tt := range []struct {
		brand, model, kind, probe string
		watts, standby            float64
	}{
		{"Samsung", `55" QLED Q60D (QN55Q60D)`, KindTV, "samsung", 63, 0.5},
		{"Sony", "PlayStation 5 Slim (disc)", KindConsole, "ps5", 217, 1.2},
	} {
		e, ok := Find(tt.brand, tt.model)
		if !ok || e.Kind != tt.kind || e.Probe != tt.probe || e.Watts != tt.watts || e.StandbyW != tt.standby || e.Note == "" {
			t.Errorf("%s %s = %+v, %v", tt.brand, tt.model, e, ok)
		}
	}
	if got := Search("playstation"); len(got) != 1 || got[0].Probe != "ps5" {
		t.Errorf(`Search("playstation") = %+v`, got)
	}
	for _, e := range All() {
		if e.Probe != "" && e.Probe != "samsung" && e.Probe != "ps5" {
			t.Errorf("%s has probe %q", e.Label(), e.Probe)
		}
		if e.Probe != "" && e.StandbyW <= 0 {
			t.Errorf("%s has a probe but no standby watts", e.Label())
		}
	}
}
