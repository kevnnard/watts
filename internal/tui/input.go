package tui

import (
	tea "charm.land/bubbletea/v2"
)

// modal is an overlay that owns the keyboard while open.
type modal interface {
	key(m *Model, k tea.KeyPressMsg) tea.Cmd
	draw(m *Model, s *screen)
	keys(m *Model) []key
	setErr(msg string)
}

// input is a one-line text field.
type input struct {
	label       string
	value       string
	placeholder string
	note        string
	choices     []string // a choice field: value is one of these
	hidden      bool     // not shown and skipped by focus
}

// cycle moves a choice field to the next or previous choice.
func (in *input) cycle(d int) {
	if len(in.choices) == 0 {
		return
	}
	i := 0
	for j, c := range in.choices {
		if c == in.value {
			i = j
		}
	}
	in.value = in.choices[focusNext(i, d, len(in.choices))]
}

// edit applies a key to the field and reports whether it used it.
func (in *input) edit(k tea.KeyPressMsg) bool {
	if len(in.choices) > 0 {
		switch k.String() {
		case "left":
			in.cycle(-1)
			return true
		case "right", "space":
			in.cycle(1)
			return true
		}
		return false
	}
	switch k.String() {
	case "backspace":
		if r := []rune(in.value); len(r) > 0 {
			in.value = string(r[:len(r)-1])
		}
		return true
	case "ctrl+u":
		in.value = ""
		return true
	}
	if k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 || k.Text == "" {
		return false
	}
	in.value += k.Text
	return true
}

// field draws an input box w cells wide with a cursor when focused.
func (s *screen) field(x, y, w int, text string, focused bool, placeholder string) {
	s.fill(x, y, w, 1, b2)
	room := w - 2
	if text == "" {
		if focused {
			s.put(x+1, y, "▏", sAc.bold(), b2)
			if placeholder != "" {
				s.put(x+2, y, trunc(placeholder, room-1), sMu.italic(), b2)
			}
			return
		}
		s.put(x+1, y, trunc(placeholder, room), sMu.italic(), b2)
		return
	}
	// keep the end of long text, where the cursor is, in view
	r := []rune(text)
	for textWidth(string(r)) > room-1 && len(r) > 0 {
		r = r[1:]
	}
	end := s.put(x+1, y, string(r), sTx, b2)
	if focused {
		s.put(end, y, "▏", sAc.bold(), b2)
	}
}

// modalRect centers a w × h modal, shrunk to fit above the key bar.
func modalRect(s *screen, w, h int) (int, int, int, int) {
	w = min(w, s.w-4)
	h = min(h, s.h-2)
	x := (s.w - w) / 2
	y := max(1, (s.h-1-h)/2)
	return x, y, w, h
}

// frame draws the modal surface and border.
func frame(s *screen, x, y, w, h int, title, right string) {
	s.fill(x, y, w, h, b3)
	s.box(x, y, w, h, title, right, b3)
}

// labeled draws "▸ Label" and its field, plus a muted note.
func labeled(s *screen, x, y, labelW, fieldW, noteW int, in *input, focused bool) {
	lst := sT2
	mark := "  "
	if focused {
		lst, mark = sAc.bold(), "▸ "
	}
	s.put(x, y, mark+trunc(in.label, labelW-2), lst, b3)
	if len(in.choices) > 0 {
		s.choice(x+labelW, y, fieldW, in.value, focused)
	} else {
		s.field(x+labelW, y, fieldW, in.value, focused, in.placeholder)
	}
	if in.note != "" && noteW > 0 {
		s.put(x+labelW+fieldW+1, y, trunc(in.note, noteW), sMu, b3)
	}
}

// errLine shows a validation error in the modal, truncated to fit.
func errLine(s *screen, x, y, w int, msg string) {
	if msg != "" {
		s.put(x, y, trunc(msg, w), sEr, b3)
	}
}

// focusNext moves focus by d over n fields.
func focusNext(f, d, n int) int {
	if n == 0 {
		return 0
	}
	return ((f+d)%n + n) % n
}

// choice draws a choice field: "‹ value ›" when focused.
func (s *screen) choice(x, y, w int, value string, focused bool) {
	s.fill(x, y, w, 1, b2)
	if focused {
		s.put(x+1, y, "‹", sAc.bold(), b2)
		s.put(x+3, y, trunc(value, w-6), sTx.bold(), b2)
		s.put(x+w-2, y, "›", sAc.bold(), b2)
		return
	}
	s.put(x+3, y, trunc(value, w-6), sTx, b2)
}

// form is a list of fields with one focused.
type form struct {
	fields []*input
	focus  int
	err    string
}

func (f *form) cur() *input {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return nil
	}
	return f.fields[f.focus]
}

// move focuses the next visible field d steps away.
func (f *form) move(d int) {
	n := len(f.fields)
	for i := 0; i < n; i++ {
		f.focus = focusNext(f.focus, d, n)
		if !f.fields[f.focus].hidden {
			return
		}
	}
}

// key handles focus movement and field edits; it reports use.
func (f *form) key(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "tab", "down":
		f.move(1)
		return true
	case "shift+tab", "up":
		f.move(-1)
		return true
	}
	if in := f.cur(); in != nil && in.edit(k) {
		f.err = ""
		return true
	}
	return false
}

// visible is the fields shown, in order.
func (f *form) visible() []*input {
	var out []*input
	for _, in := range f.fields {
		if !in.hidden {
			out = append(out, in)
		}
	}
	return out
}

// draw lays out the visible fields every gap rows from y and returns the row
// after the last one.
func (f *form) draw(s *screen, x, y, gap, labelW, fieldW, noteW int) int {
	for _, in := range f.visible() {
		labeled(s, x, y, labelW, fieldW, noteW, in, in == f.cur())
		y += gap
	}
	return y
}
