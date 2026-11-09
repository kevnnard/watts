package sensor

import (
	"testing"

	"github.com/kevnnard/watts/internal/config"
)

func TestForOS(t *testing.T) {
	cfg := config.Default()
	cfg.Items[1].RestW = 50
	tests := []struct {
		goos, source string
		err          string
	}{
		{"darwin", "mac", ""},
		{"linux", "linux", ""},
		{"windows", "", "sensor: no power sensor for windows (macOS and Linux only)"},
	}
	for _, tt := range tests {
		s, err := forOS(tt.goos, cfg)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err || s != nil {
				t.Errorf("forOS(%s) = %v, %v", tt.goos, s, err)
			}
			continue
		}
		if err != nil || s.Source() != tt.source {
			t.Fatalf("forOS(%s) = %v, %v", tt.goos, s, err)
		}
		if l, ok := s.(*Linux); ok && l.Item.RestW != 50 {
			t.Errorf("linux sensor item = %+v, want the config's PC Linux", l.Item)
		}
	}
	if it := linuxItem(nil); it.Source != "linux" || it.RestW != 0 {
		t.Errorf("linuxItem(nil) = %+v", it)
	}
}
