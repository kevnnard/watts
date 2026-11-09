package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// role is a color role from the design (design/usage-dashboard, palette in
// the artboard CSS). Foreground and background roles share the type.
type role uint8

const (
	// backgrounds
	b0 role = iota // canvas
	b1             // header and key bar
	b2             // selection and input fields
	b3             // modal surface
	// foregrounds
	tx // primary text
	t2 // secondary text
	mu // muted labels
	bd // borders
	sh // shaded text under a modal
	ac // accent: action, selection, live
	wa // warning
	er // error, offline
	ms // mode measured
	fl // mode follows
	sc // mode schedule
	mn // mode manual
)

var palette = map[role]string{
	b0: "#0e1014", b1: "#171a22", b2: "#232a3a", b3: "#141823",
	tx: "#e6e8ee", t2: "#b4bccb", mu: "#8f97a8", bd: "#454d60", sh: "#3d4351",
	ac: "#7dd3fc", wa: "#fdba74", er: "#fca5a5",
	ms: "#6ee7b7", fl: "#a5b4fc", sc: "#fcd34d", mn: "#f0abfc",
}

// attr is a text attribute set.
type attr uint8

const (
	bold attr = 1 << iota
	italic
	underline
)

// st is a cell style: foreground role plus attributes.
type st struct {
	fg role
	at attr
}

func (s st) bold() st   { s.at |= bold; return s }
func (s st) italic() st { s.at |= italic; return s }
func (s st) ul() st     { s.at |= underline; return s }

// fgs are the plain foreground styles.
var (
	sTx = st{fg: tx}
	sT2 = st{fg: t2}
	sMu = st{fg: mu}
	sBd = st{fg: bd}
	sAc = st{fg: ac}
	sWa = st{fg: wa}
	sEr = st{fg: er}
)

type cell struct {
	r    rune // 0 for the right half of a wide rune
	fg   role
	bg   role
	at   attr
	cont bool
}

// screen is a fixed terminal cell grid, the same model the artboards use.
// Everything is clipped to the grid, so nothing can overflow or wrap.
type screen struct {
	w, h int
	rows [][]cell
}

func newScreen(w, h int) *screen {
	s := &screen{w: w, h: h, rows: make([][]cell, h)}
	for y := range s.rows {
		s.rows[y] = make([]cell, w)
		for x := range s.rows[y] {
			s.rows[y][x] = cell{r: ' ', fg: tx, bg: b0}
		}
	}
	return s
}

// runeWidth is the terminal width of r: 0, 1 or 2.
func runeWidth(r rune) int {
	if r < 0x1100 {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return 0
		}
		return 1
	}
	return lipgloss.Width(string(r))
}

// textWidth is the terminal width of plain text.
func textWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// trunc cuts s to at most w cells, ending in … when cut.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if textWidth(s) <= w {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		rw := runeWidth(r)
		if n+rw > w-1 {
			break
		}
		b.WriteRune(r)
		n += rw
	}
	b.WriteRune('…')
	return b.String()
}

// put writes text at (x, y) in style s on background bg (bg < 0 keeps the
// cell's). It returns the column after the text.
func (s *screen) put(x, y int, text string, sty st, bg ...role) int {
	for _, r := range text {
		rw := runeWidth(r)
		if rw == 0 {
			continue
		}
		if y >= 0 && y < s.h && x >= 0 && x+rw <= s.w {
			s.set(x, y, r, sty, bg)
			if rw == 2 {
				c := &s.rows[y][x+1]
				*c = s.rows[y][x]
				c.r, c.cont = 0, true
			}
		} else if y >= 0 && y < s.h && x >= 0 && x < s.w {
			s.set(x, y, ' ', sty, bg) // a wide rune cut by the edge
		}
		x += rw
	}
	return x
}

func (s *screen) set(x, y int, r rune, sty st, bg []role) {
	c := &s.rows[y][x]
	if c.cont && x > 0 {
		s.rows[y][x-1].r = ' ' // overwriting half of a wide rune
	}
	if x+1 < s.w && s.rows[y][x+1].cont {
		s.rows[y][x+1] = cell{r: ' ', fg: c.fg, bg: c.bg}
	}
	c.r, c.fg, c.at, c.cont = r, sty.fg, sty.at, false
	if len(bg) > 0 {
		c.bg = bg[0]
	}
}

// rput writes text so that it ends at column xEnd (inclusive).
func (s *screen) rput(xEnd, y int, text string, sty st, bg ...role) int {
	return s.put(xEnd-textWidth(text)+1, y, text, sty, bg...)
}

// seg is one styled piece of a line.
type seg struct {
	t string
	s st
}

func (s *screen) seq(x, y int, parts []seg, bg ...role) int {
	for _, p := range parts {
		x = s.put(x, y, p.t, p.s, bg...)
	}
	return x
}

// seqMax writes parts but never past column x+max−1, ending in … when cut.
func (s *screen) seqMax(x, y, max int, parts []seg, bg ...role) int {
	if segsWidth(parts) <= max {
		return s.seq(x, y, parts, bg...)
	}
	used := 0
	for _, p := range parts {
		w, left := textWidth(p.t), max-used
		if w < left {
			s.put(x+used, y, p.t, p.s, bg...)
			used += w
			continue
		}
		return s.put(x+used, y, trunc(p.t+"…", left), p.s, bg...)
	}
	return x + used
}

func segsWidth(parts []seg) int {
	n := 0
	for _, p := range parts {
		n += textWidth(p.t)
	}
	return n
}

func (s *screen) fill(x, y, w, h int, bg role) {
	for r := y; r < y+h; r++ {
		for c := x; c < x+w; c++ {
			if r >= 0 && r < s.h && c >= 0 && c < s.w {
				s.rows[r][c] = cell{r: ' ', fg: tx, bg: bg}
			}
		}
	}
}

func (s *screen) tint(x, y, w, h int, bg role) {
	for r := y; r < y+h; r++ {
		for c := x; c < x+w; c++ {
			if r >= 0 && r < s.h && c >= 0 && c < s.w {
				s.rows[r][c].bg = bg
			}
		}
	}
}

// box draws a thin frame with an optional bold title and a muted right label.
func (s *screen) box(x, y, w, h int, title, right string, bg ...role) {
	if w < 2 || h < 2 {
		return
	}
	s.put(x, y, "┌"+strings.Repeat("─", w-2)+"┐", sBd, bg...)
	for r := y + 1; r < y+h-1; r++ {
		s.put(x, r, "│", sBd, bg...)
		s.put(x+w-1, r, "│", sBd, bg...)
	}
	s.put(x, y+h-1, "└"+strings.Repeat("─", w-2)+"┘", sBd, bg...)
	if title != "" {
		end := s.put(x+2, y, " "+trunc(title, w-6)+" ", sTx.bold(), bg...)
		if right != "" {
			room := x + w - 3 - end - 1
			if r := trunc(right, room-2); r != "" {
				s.rput(x+w-3, y, " "+r+" ", sMu, bg...)
			}
		}
	}
}

func (s *screen) hrule(x, y, w int, bg ...role) {
	if w > 0 {
		s.put(x, y, strings.Repeat("─", w), sBd, bg...)
	}
}

// shade dims everything, for the backdrop of a modal.
func (s *screen) shade() {
	for y := range s.rows {
		for x := range s.rows[y] {
			c := &s.rows[y][x]
			c.fg, c.bg, c.at = sh, b0, 0
		}
	}
}

// text is the grid without styles, one line per row.
func (s *screen) text() string {
	var b strings.Builder
	for y, row := range s.rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		for _, c := range row {
			if !c.cont {
				b.WriteRune(c.r)
			}
		}
	}
	return b.String()
}

type styleKey struct {
	fg, bg role
	at     attr
}

var styleCache = map[styleKey]lipgloss.Style{}

func styleFor(k styleKey) lipgloss.Style {
	if s, ok := styleCache[k]; ok {
		return s
	}
	s := lipgloss.NewStyle().
		Foreground(lipgloss.Color(palette[k.fg])).
		Background(lipgloss.Color(palette[k.bg])).
		Bold(k.at&bold != 0).
		Italic(k.at&italic != 0).
		Underline(k.at&underline != 0)
	styleCache[k] = s
	return s
}

// render is the grid as styled lines, one Lip Gloss style per run of equal
// cells.
func (s *screen) render() string {
	var b strings.Builder
	var run strings.Builder
	for y, row := range s.rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		var key styleKey
		started := false
		flush := func() {
			if run.Len() > 0 {
				b.WriteString(styleFor(key).Render(run.String()))
				run.Reset()
			}
		}
		for _, c := range row {
			if c.cont {
				continue
			}
			k := styleKey{c.fg, c.bg, c.at}
			if !started || k != key {
				flush()
				key, started = k, true
			}
			if c.r == 0 {
				run.WriteByte(' ')
			} else {
				run.WriteRune(c.r)
			}
		}
		flush()
	}
	return b.String()
}

// html is the grid as spans with the artboard's class names, for the preview
// harness screenshots.
func (s *screen) html() string {
	var b strings.Builder
	for y, row := range s.rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		var key styleKey
		var buf strings.Builder
		started := false
		flush := func() {
			if buf.Len() == 0 {
				return
			}
			fmtSpan(&b, key, buf.String())
			buf.Reset()
		}
		for _, c := range row {
			if c.cont {
				continue
			}
			k := styleKey{c.fg, c.bg, c.at}
			if !started || k != key {
				flush()
				key, started = k, true
			}
			r := c.r
			if r == 0 {
				r = ' '
			}
			switch {
			case r == '&':
				buf.WriteString("&amp;")
			case r == '<':
				buf.WriteString("&lt;")
			case r == '>':
				buf.WriteString("&gt;")
			case r > 126:
				buf.WriteString(`<i class="g">`)
				buf.WriteRune(r)
				buf.WriteString(`</i>`)
			default:
				buf.WriteRune(r)
			}
		}
		flush()
	}
	return b.String()
}

func fmtSpan(b *strings.Builder, k styleKey, inner string) {
	b.WriteString(`<span style="color:`)
	b.WriteString(palette[k.fg])
	b.WriteString(`;background:`)
	b.WriteString(palette[k.bg])
	if k.at&bold != 0 {
		b.WriteString(`;font-weight:700`)
	}
	if k.at&italic != 0 {
		b.WriteString(`;font-style:italic`)
	}
	if k.at&underline != 0 {
		b.WriteString(`;text-decoration:underline`)
	}
	b.WriteString(`">`)
	b.WriteString(inner)
	b.WriteString(`</span>`)
}

// lineWidths reports the widest rendered line, for tests.
func lineWidths(text string) (lines, widest int) {
	for _, l := range strings.Split(text, "\n") {
		lines++
		if w := lipgloss.Width(l); w > widest {
			widest = w
		}
	}
	return lines, widest
}
