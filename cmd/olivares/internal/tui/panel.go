// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// maxFieldLen bounds one field: a pasted blob cannot grow a value without end.
const maxFieldLen = 4096

// Panel is a screen of text with optional fields, for the work that is not a
// list: an install that runs, a sign-in that waits for a person, a key to type.
// The screen that opened it owns the work and changes the panel through Host.Post.
type Panel struct {
	Heading string
	Right   string
	// Lines are the text above the fields. A line wider than the terminal wraps
	// at its spaces; a word wider than the terminal (a link) is left whole.
	Lines  []string
	Footer string
	// FooterRole colors the footer; its words say the same.
	FooterRole termrender.Role
	// Command is the command line that does the same work.
	Command string
	// Busy is true while work runs: Enter and typing are ignored, and Esc or
	// Ctrl-C ask Cancel to stop it.
	Busy bool
	// Submit runs on Enter at the last field (or on Enter when there are no
	// fields). It must not block: start a goroutine and Post the answer.
	Submit func(values []string)
	// Cancel stops the work when the person leaves or quits. It may talk to the
	// engine, so it should be quick.
	Cancel func()
	// OnClose runs after the person leaves the panel (not when they quit).
	OnClose func()

	host *Host
	form Form
}

// SetFields replaces the fields and focuses the first one.
func (p *Panel) SetFields(fields []Field) { p.form.SetFields(fields) }

// Focus puts the cursor on field i, so a refusal can send the person to the field
// that needs another answer.
func (p *Panel) Focus(i int) { p.form.SetFocus(i) }

// Values are the fields' texts in order.
func (p *Panel) Values() []string { return p.form.Values() }

// ClearFields drops every field value, so a key sent to the engine is no longer held by the panel.
func (p *Panel) ClearFields() {
	for i := range p.form.fields {
		p.form.fields[i].Value = ""
	}
}

func (p *Panel) leave() {
	if p.Cancel != nil {
		p.Cancel()
	}
}

func (p *Panel) handle(k key) (quit bool, code int) {
	switch k.kind {
	case keyCtrlD:
		return true, 0
	case keyCtrlC, keyEsc:
		p.host.leaveTop()
		if p.OnClose != nil {
			p.OnClose()
		}
	case keyTab, keyDown:
		p.form.Next()
	case keyShiftTab, keyUp:
		p.form.Prev()
	case keyLeft:
		p.form.Cycle(-1)
	case keyRight:
		p.form.Cycle(1)
	case keyEnter:
		p.enter()
	case keyBackspace:
		if !p.Busy {
			p.form.Backspace()
		}
	case keyRune:
		if !p.Busy && isText(k.rune) && runeLen(p.form.Value()) < maxFieldLen {
			p.form.Insert(k.rune)
		}
	}
	return false, 0
}

func (p *Panel) enter() {
	if p.Busy {
		return
	}
	switch {
	case p.form.Len() == 0 && p.Submit == nil:
		p.host.Pop()
		if p.OnClose != nil {
			p.OnClose()
		}
	case p.form.Len() == 0:
		p.Submit(nil)
	case p.form.Focus() < p.form.Len()-1:
		p.form.Next()
	case p.Submit != nil:
		p.Submit(p.form.Values())
	}
}

func (p *Panel) render() []string {
	h := p.host
	width, height := h.width, h.height
	entries := []string{headingRow(h.renderer, width, p.Heading, p.Right), separatorRow(h.renderer, width)}
	if height < 6 || width < 1 {
		return entries
	}
	room := height - 5
	var body []string
	for _, line := range p.Lines {
		body = append(body, wrapWords(line, width)...)
	}
	// The fields are what the page is for: the body takes the rows they leave.
	var form []string
	if p.form.Len() > 0 {
		form = append(form, "")
		for i, field := range p.form.fields {
			form = append(form, clip(p.fieldRow(field, i == p.form.Focus()), width))
		}
	}
	formRows := 0
	for _, entry := range form {
		formRows += entryRows(entry, width)
	}
	used := 0
	var shown []string
	for _, entry := range body {
		rows := entryRows(entry, width)
		if used+rows+formRows > room {
			// A page that is cut says so, and the marker takes the room of the
			// rows it hides.
			for used+1+formRows > room && len(shown) > 0 {
				used -= entryRows(shown[len(shown)-1], width)
				shown = shown[:len(shown)-1]
			}
			if used+1+formRows <= room {
				shown = append(shown, fmt.Sprintf("  … %d more lines", len(body)-len(shown)))
				used++
			}
			break
		}
		shown = append(shown, entry)
		used += rows
	}
	entries = append(entries, shown...)
	entries = append(entries, form...)
	used += formRows
	for ; used < room; used++ {
		entries = append(entries, "")
	}
	entries = append(entries,
		h.renderer.Paint(clip("  "+p.Footer, width), p.FooterRole),
		separatorRow(h.renderer, width),
		p.statusLine().Render(width))
	return entries
}

func (p *Panel) fieldRow(field Field, focused bool) string {
	marker := "    "
	if focused {
		marker = "  ▸ "
	}
	value := field.Value
	switch {
	case field.Secret && value != "":
		value = fmt.Sprintf("hidden · %d characters", runeLen(value))
	case len(field.Choices) > 0 && focused:
		value = "‹ " + value + " ›"
	case focused && !field.Secret:
		value += "_"
	}
	return marker + padRight(field.Label, 10) + value
}

func (p *Panel) statusLine() StatusLine {
	line := StatusLine{Left: "Esc back"}
	if p.Command != "" {
		line.Left = "= " + p.Command
	}
	switch {
	case p.Busy && p.Cancel != nil:
		line.Right = "Ctrl-C cancel"
	case p.Busy:
		line.Right = "Esc leave"
	case p.form.Len() > 0 && len(p.form.fields[p.form.Focus()].Choices) > 0:
		line.Right = "←→ change  Enter next  Esc cancel"
	case p.form.Len() > 0:
		line.Right = "Enter next  Esc cancel"
	default:
		line.Right = "Enter back"
	}
	return line
}

// wrapWords breaks text at spaces into rows of at most width columns, keeping the
// text's own indent on every row. A word wider than the row stays whole on a row
// of its own, so the terminal can wrap it and a person can still copy it.
func wrapWords(text string, width int) []string {
	if runeLen(text) <= width {
		return []string{text}
	}
	indent := text[:len(text)-len(strings.TrimLeft(text, " "))]
	var rows []string
	row := indent
	for _, word := range strings.Fields(text) {
		switch {
		case runeLen(word)+len(indent) > width:
			if strings.TrimSpace(row) != "" {
				rows = append(rows, row)
			}
			rows, row = append(rows, indent+word), indent
		case row == indent:
			row += word
		case runeLen(row)+1+runeLen(word) <= width:
			row += " " + word
		default:
			rows, row = append(rows, row), indent+word
		}
	}
	if strings.TrimSpace(row) != "" {
		rows = append(rows, row)
	}
	return rows
}
