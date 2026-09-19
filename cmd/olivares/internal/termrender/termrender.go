// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package termrender is the CLI's presentation layer: tables, key/value blocks,
// status lines, one-line summaries, progress and errors.
//
// WHY A PACKAGE AND NOT A HOUSE STYLE. Measured 2026-09-18 by walking the whole
// first hour on a clean data directory: `agent tool detect` printed two rows with
// DIFFERENT field counts under no header; `agent session ls` printed one bare
// tab-separated row; `doctor` printed a 200-column table that no ordinary terminal
// can show; `agent tool install` downloaded 163 MB with nothing on screen while it
// ran; and four different commands reported a failure by pasting the transport's
// own JSON envelope at the operator. Each of those is somebody hand-formatting,
// and hand-formatting is why they disagree.
//
// THE ONE PROPERTY EVERYTHING ELSE FOLLOWS FROM: the PLAIN form is the contract.
// This binary's output is parsed by scripts, by CI and by the repository's own
// gates, so the bytes a pipe receives are an interface. Colour is therefore purely
// ADDITIVE — every primitive builds its plain string first and wraps whole cells
// in SGR afterwards — and the property the tests assert is that stripping SGR from
// the rich form yields the plain form byte for byte. A renderer that decided to
// shorten a value when it thought nobody was watching would break callers it
// cannot see.
//
// NO PRIMITIVE RETURNS AN ERROR, and none truncates. A row wider than the terminal
// becomes a record (one key/value block per row); a value wider than the terminal
// is written whole and the terminal wraps it. Presentation is never the reason a
// command fails, and an identifier an operator has to copy is never cut in half.
package termrender

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
)

// Role is the closed set of semantic colours. It is four values and not a theme
// file ON PURPOSE, a decision and not an omission: a CLI that needs a theme to
// be readable has moved its accessibility into user configuration. The console
// is not bound by this decision; the terminal is.
type Role int

const (
	// RoleNone is unstyled text.
	RoleNone Role = iota
	// RoleOK is a measured, healthy fact.
	RoleOK
	// RoleWarn is a measured fact that needs attention but is not a failure.
	RoleWarn
	// RoleFail is a measured failure.
	RoleFail
	// RoleMuted is structural text: headers, separators, units.
	RoleMuted
)

// sgr returns the escape sequence for a role, or "" when it has none.
func (r Role) sgr() string {
	switch r {
	case RoleOK:
		return "\x1b[32m"
	case RoleWarn:
		return "\x1b[33m"
	case RoleFail:
		return "\x1b[31m"
	case RoleMuted:
		return "\x1b[2m"
	default:
		return ""
	}
}

// reset ends every styled span. One constant so no caller invents a second one.
const reset = "\x1b[0m"

// defaultWidth is what a terminal is assumed to be when COLUMNS says nothing.
//
// The real size is NOT queried. TIOCGWINSZ needs either a new direct dependency
// that must pass the export closure check, or `unsafe` with a #nosec in
// production CLI code; neither is proportionate to choosing between two layouts.
// 80 is the width every terminal meets and COLUMNS is the operator's override.
// The consequence is bounded and one-directional: a wide terminal may get the
// record fallback where a table would also have fitted. The opposite failure —
// cutting an identifier because the guess was too small — cannot happen here,
// because nothing in this package truncates.
const defaultWidth = 80

// Renderer writes one command's human-facing output.
type Renderer struct {
	w     io.Writer
	color bool
	width int // 0 means "not a terminal": do not wrap, do not truncate
}

// Options configures New. Every field has a working zero value; the seams exist
// so the tests can drive the colour and width matrix without a pty.
type Options struct {
	// Color forces colour on or off. Nil asks the environment and the stream.
	Color *bool
	// Width forces the usable column count. Zero asks COLUMNS, then the stream.
	Width int
	// LookupEnv reads the environment, reporting presence separately from value.
	// Presence matters: NO_COLOR is set to ANY value, including the empty string.
	// Nil means os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// IsTerminal reports whether a writer is a terminal. Nil means isatty.
	IsTerminal func(io.Writer) bool
}

// New builds a renderer for w.
func New(w io.Writer, opts Options) *Renderer {
	getenv := opts.LookupEnv
	if getenv == nil {
		getenv = os.LookupEnv
	}
	isTerm := opts.IsTerminal
	if isTerm == nil {
		isTerm = terminalWriter
	}
	term := isTerm(w)

	color := term && !noColorRequested(getenv)
	if opts.Color != nil {
		color = *opts.Color
	}

	width := opts.Width
	if width <= 0 {
		width = resolveWidth(getenv, term)
	}
	return &Renderer{w: w, color: color, width: width}
}

// noColorRequested honours NO_COLOR, which is set to ANY value — including the
// empty string — to mean "no colour". Testing for a NON-EMPTY value is the common
// misreading, and it defeats the documented `NO_COLOR= olivares …` form. That is
// why this package reads the environment through LookupEnv and not Getenv.
func noColorRequested(lookup func(string) (string, bool)) bool {
	_, ok := lookup("NO_COLOR")
	return ok
}

// resolveWidth reads COLUMNS, then falls back. A non-terminal gets 0, which every
// primitive reads as "unbounded".
func resolveWidth(lookup func(string) (string, bool), term bool) int {
	value, _ := lookup("COLUMNS")
	if raw := strings.TrimSpace(value); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	if term {
		return defaultWidth
	}
	return 0
}

// terminalWriter is the production terminal test. It asks isatty rather than
// os.ModeCharDevice for the reason confirm.go already records: /dev/null is a
// character device.
func terminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// Color reports whether this renderer emits SGR sequences.
func (r *Renderer) Color() bool { return r.color }

// Width reports the usable column count, or 0 when the stream is not a terminal.
func (r *Renderer) Width() int { return r.width }

// Writer exposes the underlying writer for callers that still format their own
// bytes. It exists so a partially converted command has one writer, not two.
func (r *Renderer) Writer() io.Writer { return r.w }

// paint wraps s in a role's SGR when colour is on. It never changes s otherwise,
// which is the property the golden tests depend on.
func (r *Renderer) paint(s string, role Role) string {
	if !r.color || role == RoleNone || s == "" {
		return s
	}
	seq := role.sgr()
	if seq == "" {
		return s
	}
	return seq + s + reset
}

// visibleWidth is the ONE width model of this package, by design. Every
// primitive measures through it, so alignment agrees across primitives.
//
// It counts terminal columns: SGR sequences are zero-width, combining marks are
// zero-width, and everything else is one column. Double-width East Asian text is
// counted as one column — stated rather than hidden, because the alternative is
// a UAX#11 table this package would then have to keep current. The failure mode
// is a column that is too narrow by a few cells, never a truncated value.
func visibleWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if j := skipANSI(s, i); j > i {
				i = j
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			n++
			continue
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			continue
		}
		n++
	}
	return n
}

// skipANSI returns the index just past a CSI/OSC sequence starting at i, or i
// when there is no complete sequence there.
func skipANSI(s string, i int) int {
	if i+1 >= len(s) {
		return i
	}
	switch s[i+1] {
	case '[': // CSI: parameters, then a final byte in 0x40..0x7e
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
	case ']': // OSC: terminated by BEL or ST
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
	}
	return i
}

// pad right-pads s to n columns using the visible width. It never shortens s: a
// cell wider than its column pushes the row out, and the row is what decides
// whether the table becomes a record (Table).
func pad(s string, n int) string {
	if d := n - visibleWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// line writes one already-assembled line, trailing spaces removed. Trailing
// whitespace is invisible on screen and a diff hazard in a golden file, so it is
// removed once, here, instead of in every caller.
func (r *Renderer) line(s string) {
	fmt.Fprintln(r.w, strings.TrimRight(s, " \t"))
}

// Line writes one plain line.
func (r *Renderer) Line(s string) { r.line(s) }

// Blank writes an empty line.
func (r *Renderer) Blank() { fmt.Fprintln(r.w) }

// Next writes the next command an operator runs. Every first-hour command ends in
// one, by design: the first-hour walk's ranked defect list is mostly commands that
// end correctly and leave the operator with nowhere to go.
func (r *Renderer) Next(command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	r.line(r.paint("next:", RoleMuted) + " " + command)
}

// Field is one key/value pair of a Fields block.
type Field struct {
	Key   string
	Value string
	Role  Role
}

// Fields writes an aligned key/value block in the order given. Insertion order is
// kept because these blocks are read top to bottom, not looked up.
//
// A field with an empty value prints "-". Measured in the walk: `olivares status`
// ends a line with `guard_warning=` and an operator cannot tell an empty value
// from a truncated line.
func (r *Renderer) Fields(fields []Field) {
	if len(fields) == 0 {
		return
	}
	w := 0
	for _, f := range fields {
		if n := visibleWidth(f.Key); n > w {
			w = n
		}
	}
	for _, f := range fields {
		value := f.Value
		if strings.TrimSpace(value) == "" {
			value = "-"
		}
		key := pad(r.paint(strings.ToUpper(f.Key), RoleMuted), w)
		r.line(key + "  " + r.paint(value, f.Role))
	}
}

// minWrapWidth is the narrowest ribbon a folded sentence is given.
//
// Below it the hanging indent eats most of the line — a COLUMNS=30 terminal with a
// 17-column key leaves thirteen characters — and a fold that narrow is harder to
// read than a line the terminal wraps by itself. The floor means a very narrow
// terminal soft-wraps, which is what it would have done anyway.
const minWrapWidth = 60

// wrapWidth is the column count the SENTENCE primitives fold at.
//
// It is deliberately NOT the same rule as Table's. Table reads width 0 as
// "unbounded" and is right to: a row of identifiers must not be cut, and a pipe has
// no width. A SENTENCE is the other case, and doctor measured it on 2026-09-19 —
// its DETAIL block was 158 columns wide at EVERY width, so it overflowed an
// 80-column terminal and wasted a 120-column one, which is the same output failing
// in both directions at once. 80 is the width every terminal meets and the width a
// pipe is given, so the plain form stays one deterministic set of bytes.
func (r *Renderer) wrapWidth() int {
	w := r.width
	if w <= 0 {
		w = defaultWidth
	}
	if w < minWrapWidth {
		w = minWrapWidth
	}
	return w
}

// wrapWords folds s at width columns WITHOUT EVER CUTTING A WORD. A word wider than
// the ribbon is written whole and overflows it, because this package truncates
// nothing: an operator has to be able to copy a path or an identifier.
//
// Runs of whitespace inside s collapse to one space, which is the one byte-level
// change folding makes and is why this is a separate primitive rather than Fields'
// behaviour: a caller whose value is an identifier must keep its bytes.
func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	if width < 1 {
		width = 1
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if visibleWidth(line)+1+visibleWidth(w) <= width {
			line += " " + w
			continue
		}
		out = append(out, line)
		line = w
	}
	return append(out, line)
}

// WrappedFields is Fields for values that are SENTENCES: it folds each value at the
// terminal width and indents the folds under the value column, so a paragraph reads
// as one block and a narrow terminal does not interleave two fields.
//
// It is a SECOND primitive and not an option on Fields on purpose. Most key/value
// blocks in this binary carry identifiers — origins, paths, digests, versions — and
// folding those would put a line break inside something an operator copies. The
// caller decides which of the two it has, and doctor's DETAIL block is the case
// that named this one.
func (r *Renderer) WrappedFields(fields []Field) {
	if len(fields) == 0 {
		return
	}
	keyWidth := 0
	for _, f := range fields {
		if n := visibleWidth(f.Key); n > keyWidth {
			keyWidth = n
		}
	}
	indent := keyWidth + 2
	ribbon := r.wrapWidth() - indent
	for _, f := range fields {
		value := f.Value
		if strings.TrimSpace(value) == "" {
			value = "-"
		}
		folds := wrapWords(value, ribbon)
		// Painted per fold, never across the break: line() trims trailing
		// whitespace, and a reset sequence that spanned a newline would leave the
		// rich form carrying bytes the plain form does not.
		r.line(pad(r.paint(strings.ToUpper(f.Key), RoleMuted), keyWidth) + "  " + r.paint(folds[0], f.Role))
		for _, fold := range folds[1:] {
			r.line(strings.Repeat(" ", indent) + r.paint(fold, f.Role))
		}
	}
}

// Table is a header plus rows of equal length.
type Table struct {
	Header []string
	Rows   [][]string
	// Roles optionally colours one cell per row: Roles[row][col]. Short rows and
	// a nil slice are fine; a missing entry is RoleNone.
	Roles [][]Role
	// Empty is the sentence printed when there are no rows. A list that prints
	// nothing is indistinguishable from a command that did nothing.
	Empty string
}

// Table writes t, choosing between the column form and the record form by width.
//
// THE DECISION THIS PRIMITIVE EXISTS FOR. `olivares doctor` prints a 200-column
// table; on an 80-column terminal every row wraps into the next one and the
// alignment that made it a table is what destroys it. The alternatives were
// truncating cells (an operator cannot copy a truncated identifier) and dropping
// columns (the operator cannot tell which). Both hide a measured fact to keep a
// shape. The record form keeps every byte and drops the shape.
func (r *Renderer) Table(t Table) {
	if len(t.Rows) == 0 {
		if t.Empty != "" {
			r.line(t.Empty)
		}
		return
	}
	cols := len(t.Header)
	for _, row := range t.Rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	widths := make([]int, cols)
	for c := 0; c < cols; c++ {
		if c < len(t.Header) {
			widths[c] = visibleWidth(t.Header[c])
		}
		for _, row := range t.Rows {
			if c < len(row) {
				if n := visibleWidth(row[c]); n > widths[c] {
					widths[c] = n
				}
			}
		}
	}
	total := 0
	for _, w := range widths {
		total += w + 2
	}
	total -= 2
	if r.width > 0 && total > r.width {
		r.records(t, cols)
		return
	}

	if len(t.Header) > 0 {
		cells := make([]string, 0, cols)
		for c := 0; c < cols; c++ {
			h := ""
			if c < len(t.Header) {
				h = strings.ToUpper(t.Header[c])
			}
			cells = append(cells, pad(h, widths[c]))
		}
		r.line(r.paint(strings.TrimRight(strings.Join(cells, "  "), " "), RoleMuted))
	}
	for i, row := range t.Rows {
		cells := make([]string, 0, cols)
		for c := 0; c < cols; c++ {
			v := ""
			if c < len(row) {
				v = row[c]
			}
			// Pad OUTSIDE the paint. Padding a cell and THEN wrapping it in SGR
			// puts the trailing spaces before the reset sequence, where line()'s
			// TrimRight cannot reach them: the coloured form would then carry
			// trailing whitespace the plain form does not, and rich-minus-colour
			// would stop equalling plain. pad() measures visible width, so the
			// escape bytes cost it nothing.
			cells = append(cells, pad(r.paint(v, t.roleAt(i, c)), widths[c]))
		}
		r.line(strings.Join(cells, "  "))
	}
}

// roleAt reads Roles defensively: a caller that colours only some cells, or none,
// must not have to build a full matrix.
func (t Table) roleAt(row, col int) Role {
	if row >= len(t.Roles) {
		return RoleNone
	}
	if col >= len(t.Roles[row]) {
		return RoleNone
	}
	return t.Roles[row][col]
}

// records is the fallback form: one key/value block per row, blank-line separated.
func (r *Renderer) records(t Table, cols int) {
	for i, row := range t.Rows {
		if i > 0 {
			r.Blank()
		}
		fields := make([]Field, 0, cols)
		for c := 0; c < cols; c++ {
			key := ""
			if c < len(t.Header) {
				key = t.Header[c]
			}
			if key == "" {
				key = "field " + strconv.Itoa(c+1)
			}
			v := ""
			if c < len(row) {
				v = row[c]
			}
			fields = append(fields, Field{Key: key, Value: v, Role: t.roleAt(i, c)})
		}
		r.Fields(fields)
	}
}

// Status is one measured check.
type Status struct {
	Role   Role
	Label  string
	Detail string
}

// StatusLine writes one check as `[ok] label - detail`.
//
// ASCII ON PURPOSE. The reference CLIs use U+2713 and U+2717. Those are
// a replacement glyph on a host without the font, a mojibake pair in a non-UTF-8
// locale, and unsearchable in a support ticket. `[ok]` is four columns everywhere.
func (r *Renderer) StatusLine(s Status) {
	// Padded outside the paint, for the reason Table records above.
	r.line(pad(r.paint(statusToken(s.Role), s.Role), statusTokenWidth) + " " + r.statusTail(s))
}

// statusTokenWidth is the column count every token is padded to, so the labels
// of a run of status lines start at one offset.
const statusTokenWidth = 6

func (r *Renderer) statusTail(s Status) string {
	if strings.TrimSpace(s.Detail) == "" {
		return s.Label
	}
	return s.Label + " " + r.paint("-", RoleMuted) + " " + s.Detail
}

// StatusToken is the token StatusLine writes for a role, for a caller that has
// to place a verdict somewhere StatusLine cannot reach — a table cell, for one.
//
// IT IS EXPORTED SO THAT NOBODY TYPES "[ok]". The token set is closed here, and a
// command that spelled one out by hand would both drift from it and trip the
// render gate's `status` rule, which counts exactly that literal in the command
// layer. One function is cheaper than either.
func StatusToken(role Role) string { return statusToken(role) }

// statusToken is the closed token set. RoleNone is "not measured", and it is
// deliberately NOT "[ok]": the walk found several places where an unmeasured
// check and a passing one looked the same.
func statusToken(role Role) string {
	switch role {
	case RoleOK:
		return "[ok]"
	case RoleWarn:
		return "[warn]"
	case RoleFail:
		return "[fail]"
	default:
		return "[--]"
	}
}

// Summary writes the one line a batch command ends with: the counts, then the
// caller's own clause. It replaces the reference CLI's live status footer, which
// has no plain form.
func (r *Renderer) Summary(counts map[Role]int, clause string) {
	parts := make([]string, 0, 3)
	for _, role := range []Role{RoleOK, RoleWarn, RoleFail} {
		if n := counts[role]; n > 0 {
			parts = append(parts, r.paint(strconv.Itoa(n)+" "+roleWord(role), role))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing to report")
	}
	out := strings.Join(parts, ", ")
	if clause = strings.TrimSpace(clause); clause != "" {
		out += " " + r.paint("-", RoleMuted) + " " + clause
	}
	r.line(out)
}

func roleWord(role Role) string {
	switch role {
	case RoleOK:
		return "ok"
	case RoleWarn:
		return "warn"
	case RoleFail:
		return "fail"
	default:
		return "unmeasured"
	}
}

// Problem is a failure as an operator reads it.
type Problem struct {
	// What happened, as a fact, lower case, no trailing period.
	What string
	// Cause is the underlying error when it adds something What does not say.
	Cause string
	// Next is the exact command to run. Every error that can name one, does.
	Next string
}

// Error writes a problem in the shape adopted from the reference CLI and
// adapted: `error: <fact>`, then the cause, then the NEXT COMMAND. The reference
// ends with "Run --help for details", which is what a CLI says when it does not
// know the answer. The walk showed we usually do know it.
func (r *Renderer) Error(p Problem) {
	r.line(r.paint("error:", RoleFail) + " " + p.What)
	if c := strings.TrimSpace(p.Cause); c != "" {
		r.line(r.paint("cause:", RoleMuted) + " " + c)
	}
	r.Next(p.Next)
}

// Progress reports a long operation.
//
// TO A TERMINAL it rewrites one line in place. TO A PIPE IT WRITES NOTHING: a
// carriage return in a log file is a line a reader never sees the start of, and a
// spinner in CI output is thousands of frames. The caller's own start and end
// lines carry the fact in both cases, which is why Done() writes nothing either.
type Progress struct {
	r     *Renderer
	label string
	live  bool
	last  int
}

// Progress starts a progress reporter for a labelled operation.
func (r *Renderer) Progress(label string) *Progress {
	return &Progress{r: r, label: label, live: r.width > 0}
}

// Set reports the current state, e.g. "121 MB of 163 MB".
func (p *Progress) Set(state string) {
	if p == nil || !p.live {
		return
	}
	text := p.label
	if state = strings.TrimSpace(state); state != "" {
		text += " " + state
	}
	if n := visibleWidth(text); n < p.last {
		text += strings.Repeat(" ", p.last-n)
	} else {
		p.last = n
	}
	fmt.Fprint(p.r.w, "\r"+text)
}

// Done clears the live line. It writes nothing to a pipe, and it writes no final
// state: the caller already prints the outcome, and a progress reporter that also
// printed one would say it twice.
func (p *Progress) Done() {
	if p == nil || !p.live {
		return
	}
	fmt.Fprint(p.r.w, "\r"+strings.Repeat(" ", p.last)+"\r")
	p.last = 0
}

// SortedKeys is the deterministic key order every map-backed block uses. Two runs
// of one command must produce identical bytes; a Go map range does not.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// StripANSI removes every SGR/OSC sequence. The tests use it to assert the one
// property this package is built on: rich minus colour equals plain.
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if j := skipANSI(s, i); j > i {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
