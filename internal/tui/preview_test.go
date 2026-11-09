package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The preview harness renders every fixture screen for visual review:
//
//	go test ./internal/tui -run TestPreview -preview /tmp/watts-preview
//
// writes <name>.txt (plain cells) and <name>.html (colors, one cell per
// character) into the directory. Screenshot the HTML with a headless browser.
var previewDir = flag.String("preview", "", "write the fixture screens as .txt and .html into this directory")

const previewCSS = `body{margin:0;background:#0e1014}
pre{margin:0;padding:12px;font:15px/19px "JetBrains Mono","SF Mono",Menlo,monospace;color:#e6e8ee;background:#0e1014}
pre span{display:inline-block;height:19px;vertical-align:top}
i.g{font-style:inherit;display:inline-block;width:1ch;overflow:visible;text-align:left}`

func TestPreview(t *testing.T) {
	if *previewDir == "" {
		t.Skip("set -preview DIR to write the screens")
	}
	if err := os.MkdirAll(*previewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, sc := range screens {
		m := screenModel(t, i)
		s := m.draw()
		page := "<!doctype html><meta charset=utf-8><title>" + sc.name + "</title><style>" + previewCSS +
			"</style><pre data-w=" + strconv.Itoa(sc.w) + " data-h=" + strconv.Itoa(sc.h) + ">" + s.html() + "</pre>"
		for ext, body := range map[string]string{".txt": s.text(), ".html": page} {
			if err := os.WriteFile(filepath.Join(*previewDir, sc.name+ext), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
