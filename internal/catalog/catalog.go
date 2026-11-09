// Package catalog embeds a small table of common household devices with
// their typical power draw. The values are typical "on" figures, not maximums;
// the device label is always a better source when the user has it (S3).
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Device kinds.
const (
	KindMonitor = "monitor"
	KindBulb    = "bulb"
	KindStrip   = "strip"
	KindFan     = "fan"
	KindRouter  = "router"
	KindSpeaker = "speaker"
	KindCharger = "charger"
	KindTV      = "tv"
)

// Entry is one catalog device.
type Entry struct {
	Kind  string  `json:"kind"`
	Brand string  `json:"brand"`
	Model string  `json:"model"`
	Watts float64 `json:"watts"`
	Note  string  `json:"note,omitempty"`
	// Optional DDC estimate range for monitors (S19): watts at brightness 0
	// and 100 while on, and in standby. Zero means unknown.
	MinW     float64 `json:"min_w,omitempty"`
	MaxW     float64 `json:"max_w,omitempty"`
	StandbyW float64 `json:"standby_w,omitempty"`
}

// Label is "Brand Model".
func (e Entry) Label() string { return e.Brand + " " + e.Model }

//go:embed devices.json
var devicesJSON []byte

var entries = mustLoad(devicesJSON)

func mustLoad(b []byte) []Entry {
	var es []Entry
	if err := json.Unmarshal(b, &es); err != nil {
		panic(fmt.Sprintf("catalog: invalid embedded devices.json: %v", err))
	}
	return es
}

// All returns a copy of every entry in catalog order.
func All() []Entry {
	return append([]Entry(nil), entries...)
}

// Find returns the entry whose brand and model match case-insensitively.
func Find(brand, model string) (Entry, bool) {
	for _, e := range entries {
		if strings.EqualFold(e.Brand, brand) && strings.EqualFold(e.Model, model) {
			return e, true
		}
	}
	return Entry{}, false
}

// Search returns entries matching every whitespace-separated term of query.
// A term matches when it is a case-insensitive substring of the brand, model
// or kind. Better matches (exact model, then exact brand or kind) come first.
// An empty query returns every entry.
func Search(query string) []Entry {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		e     Entry
		score int
	}
	var hits []scored
	for _, e := range entries {
		brand, model, kind := strings.ToLower(e.Brand), strings.ToLower(e.Model), e.Kind
		score, ok := 0, true
		for _, t := range terms {
			switch {
			case model == t:
				score += 100
			case brand == t || kind == t:
				score += 10
			case strings.Contains(model, t) || strings.Contains(brand, t) || strings.Contains(kind, t):
				score++
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			hits = append(hits, scored{e, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}
