// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// The widget set every screen shares: a selectable list, a form, a scrolling
// pane and a status line. Each one keeps its own scroll or focus state so a
// screen composes them instead of reimplementing cursor maths. They draw
// nothing: the frame renders them, which is what makes them testable without a
// terminal.

// MenuItem is one row of a list. Command is the command line that does the same
// work. Open, when set, does the work on Enter: it may push a screen and returns
// a notice for the status line ("" for none).
type MenuItem struct {
	Label   string
	Detail  string
	Command string
	Open    func(h *Host) string
}

// List is a selectable list that scrolls one row at a time.
type List struct {
	items  []MenuItem
	cursor int
	offset int
}

// SetItems replaces the rows and keeps the cursor inside them.
func (l *List) SetItems(items []MenuItem) {
	l.items = items
	l.clamp()
}

// Move shifts the cursor by delta, stopping at either end.
func (l *List) Move(delta int) {
	if len(l.items) == 0 {
		l.cursor = 0
		return
	}
	l.cursor += delta
	l.clamp()
}

// Selected returns the row under the cursor.
func (l *List) Selected() (MenuItem, bool) {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		return MenuItem{}, false
	}
	return l.items[l.cursor], true
}

// Cursor is the index of the selected row in the current rows.
func (l *List) Cursor() int { return l.cursor }

// Len is the number of rows after the last filter.
func (l *List) Len() int { return len(l.items) }

// Window returns the first visible row index and the rows that fit in height,
// moving the scroll offset just enough to keep the cursor visible.
func (l *List) Window(height int) (start int, rows []MenuItem) {
	if height <= 0 || len(l.items) == 0 {
		return 0, nil
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+height {
		l.offset = l.cursor - height + 1
	}
	if maxOffset := len(l.items) - height; l.offset > maxOffset {
		l.offset = maxOffset
	}
	if l.offset < 0 {
		l.offset = 0
	}
	end := l.offset + height
	if end > len(l.items) {
		end = len(l.items)
	}
	return l.offset, l.items[l.offset:end]
}

func (l *List) clamp() {
	if l.cursor >= len(l.items) {
		l.cursor = len(l.items) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.offset > l.cursor {
		l.offset = l.cursor
	}
}

// isText is whether a typed rune is text: not DEL and not a C0 or C1 control, which
// a terminal would act on when the field is drawn.
func isText(r rune) bool { return r >= ' ' && r != 0x7f && (r < 0x80 || r > 0x9f) }

// Field is one labeled entry of a Form. A Secret field is typed but never
// drawn; a field with Choices holds one of them and is changed with the arrows.
type Field struct {
	Label   string
	Value   string
	Secret  bool
	Choices []string
}

// Form holds the fields of the current screen and which one has focus.
type Form struct {
	fields []Field
	focus  int
}

// SetFields replaces the fields and focuses the first one.
func (f *Form) SetFields(fields []Field) {
	f.fields = fields
	f.focus = 0
}

// Next moves focus to the following field, wrapping around.
func (f *Form) Next() {
	if len(f.fields) == 0 {
		return
	}
	f.focus = (f.focus + 1) % len(f.fields)
}

// Prev moves focus to the previous field, wrapping around.
func (f *Form) Prev() {
	if len(f.fields) == 0 {
		return
	}
	f.focus = (f.focus + len(f.fields) - 1) % len(f.fields)
}

// Len is the number of fields.
func (f *Form) Len() int { return len(f.fields) }

// Focus is the index of the focused field.
func (f *Form) Focus() int { return f.focus }

// Values are the fields' texts in order.
func (f *Form) Values() []string {
	values := make([]string, len(f.fields))
	for i, field := range f.fields {
		values[i] = field.Value
	}
	return values
}

// Cycle moves the focused field to its next (delta 1) or previous (delta -1)
// choice, wrapping around. A field without choices is left alone.
func (f *Form) Cycle(delta int) {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return
	}
	field := &f.fields[f.focus]
	n := len(field.Choices)
	if n == 0 {
		return
	}
	at := 0
	for i, choice := range field.Choices {
		if choice == field.Value {
			at = i
		}
	}
	field.Value = field.Choices[(at+delta%n+n)%n]
}

// editable reports whether typing changes the focused field.
func (f *Form) editable() bool {
	return f.focus >= 0 && f.focus < len(f.fields) && len(f.fields[f.focus].Choices) == 0
}

// Value is the focused field's text, or "" when the form has no fields.
func (f *Form) Value() string {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return ""
	}
	return f.fields[f.focus].Value
}

// SetValue replaces the focused field's text.
func (f *Form) SetValue(value string) {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return
	}
	f.fields[f.focus].Value = value
}

// Insert appends r to the focused field.
func (f *Form) Insert(r rune) {
	if !f.editable() {
		return
	}
	f.fields[f.focus].Value += string(r)
}

// Backspace removes the last rune of the focused field.
func (f *Form) Backspace() {
	if !f.editable() {
		return
	}
	runes := []rune(f.fields[f.focus].Value)
	if len(runes) == 0 {
		return
	}
	f.fields[f.focus].Value = string(runes[:len(runes)-1])
}

// Pane is a scrolling block of text.
type Pane struct {
	lines  []string
	offset int
}

// SetLines replaces the content and returns to the top.
func (p *Pane) SetLines(lines []string) {
	p.lines = lines
	p.offset = 0
}

// Scroll moves the view by delta rows, stopping at either end.
func (p *Pane) Scroll(delta, height int) {
	p.offset += delta
	p.clamp(height)
}

// Visible returns the rows that fit in height, starting at the current offset.
func (p *Pane) Visible(height int) []string {
	if height <= 0 || len(p.lines) == 0 {
		return nil
	}
	p.clamp(height)
	end := p.offset + height
	if end > len(p.lines) {
		end = len(p.lines)
	}
	return p.lines[p.offset:end]
}

// Offset is the index of the first visible row.
func (p *Pane) Offset() int { return p.offset }

func (p *Pane) clamp(height int) {
	maxOffset := len(p.lines) - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// StatusLine is the bottom row: what the keys do on the left, the state of the
// current step on the right.
type StatusLine struct {
	Left  string
	Right string
}

// Render lays the status line out in width columns of plain text.
func (s StatusLine) Render(width int) string {
	if width <= 0 {
		return ""
	}
	switch {
	case s.Right == "":
		return clip(s.Left, width)
	case runeLen(s.Left)+1+runeLen(s.Right) <= width:
		gap := width - runeLen(s.Left) - runeLen(s.Right)
		return s.Left + strings.Repeat(" ", gap) + s.Right
	default:
		return clip(s.Left+"  "+s.Right, width)
	}
}

// clip cuts plain text to width runes. The frame clips before it paints, so an
// ANSI sequence can never be cut in half.
func clip(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runeLen(text) <= width {
		return text
	}
	return string([]rune(text)[:width])
}

// runeLen counts the columns of the plain text the frame builds.
func runeLen(text string) int { return utf8.RuneCountInString(text) }

func entryRows(entry string, width int) int {
	if width <= 0 {
		return 1
	}
	n := runeLen(termrender.StripANSI(entry))
	if n <= width {
		return 1
	}
	return (n + width - 1) / width
}

func padRight(text string, width int) string {
	if n := runeLen(text); n < width {
		return text + strings.Repeat(" ", width-n)
	}
	return text
}
