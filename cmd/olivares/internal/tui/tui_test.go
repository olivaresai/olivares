// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

func testFrame() Frame {
	return Frame{
		Heading: "Olivares AI · Demo Estate",
		Right:   "Community",
		Items: []MenuItem{
			{Label: "Get started", Detail: "Next: olivares quickstart", Command: "olivares quickstart"},
			{Label: "AI tools", Detail: "install, sign in, API keys", Command: "olivares tool ls"},
			{Label: "Sessions", Detail: "start, attach, stop", Command: "olivares session ls"},
			{Label: "Security", Detail: "approvals and policies", Command: "olivares governance approvals ls"},
			{Label: "Settings", Detail: "this installation", Command: "olivares config"},
			{Label: "All commands", Detail: "the full reference", Command: "olivares --help"},
		},
		Footer:     "No engine found on this machine · version test",
		FooterRole: termrender.RoleWarn,
		Hints:      "↑↓ move  Enter show  / search  ? help  q quit",
	}
}

func testModel(width, height int, color bool) *model {
	r := termrender.New(io.Discard, termrender.Options{Color: &color, Width: width})
	return newModel(testFrame(), width, height, r)
}

func rendered(m *model) string { return strings.Join(m.render(), "\n") }

func lineContaining(text, want string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	return ""
}

func TestListMovesAndStops(t *testing.T) {
	var list List
	list.SetItems([]MenuItem{{Label: "a"}, {Label: "b"}, {Label: "c"}})
	if item, ok := list.Selected(); !ok || item.Label != "a" {
		t.Fatalf("initial selection = %v, %v; want a", item, ok)
	}
	list.Move(1)
	if item, _ := list.Selected(); item.Label != "b" {
		t.Fatalf("after one move selection = %q, want b", item.Label)
	}
	list.Move(-9)
	if item, _ := list.Selected(); item.Label != "a" {
		t.Fatalf("moving above the first row selected %q, want a", item.Label)
	}
	list.Move(9)
	if item, _ := list.Selected(); item.Label != "c" {
		t.Fatalf("moving below the last row selected %q, want c", item.Label)
	}
	list.SetItems(nil)
	if _, ok := list.Selected(); ok {
		t.Fatal("an empty list reported a selection")
	}
}

func TestListWindowKeepsCursorVisible(t *testing.T) {
	items := make([]MenuItem, 20)
	for i := range items {
		items[i] = MenuItem{Label: string(rune('a' + i))}
	}
	var list List
	list.SetItems(items)
	for range 9 {
		list.Move(1)
	}
	start, rows := list.Window(5)
	if start != 5 || len(rows) != 5 || rows[4].Label != items[9].Label {
		t.Fatalf("window at cursor 9 = start %d rows %v; want start 5 ending at j", start, rows)
	}
	list.Move(-20)
	if start, _ := list.Window(5); start != 0 {
		t.Fatalf("window after returning to the top started at %d, want 0", start)
	}
	list.Move(20)
	if start, rows := list.Window(5); start != 15 || rows[4].Label != items[19].Label {
		t.Fatalf("window at the last row = start %d; want 15 ending at the last item", start)
	}
}

func TestFormEditsTheFocusedField(t *testing.T) {
	var form Form
	form.SetFields([]Field{{Label: "one"}, {Label: "two"}})
	form.Insert('a')
	form.Insert('b')
	form.Backspace()
	if got := form.Value(); got != "a" {
		t.Fatalf("focused value = %q, want a", got)
	}
	form.Next()
	form.Insert('z')
	if got := form.Value(); got != "z" {
		t.Fatalf("after Tab the value = %q, want z", got)
	}
	form.Prev()
	if got := form.Value(); got != "a" {
		t.Fatalf("after Shift-Tab the value = %q, want a", got)
	}
	form.Backspace()
	form.Backspace()
	if got := form.Value(); got != "" {
		t.Fatalf("backspace past the start = %q, want empty", got)
	}
	var empty Form
	empty.Insert('x')
	if got := empty.Value(); got != "" {
		t.Fatalf("a form with no fields returned %q", got)
	}
}

func TestPaneScrollClamps(t *testing.T) {
	var pane Pane
	pane.SetLines([]string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"})
	pane.Scroll(-1, 4)
	if pane.Offset() != 0 {
		t.Fatalf("scroll above the top = %d, want 0", pane.Offset())
	}
	pane.Scroll(100, 4)
	if pane.Offset() != 6 {
		t.Fatalf("scroll below the end = %d, want 6", pane.Offset())
	}
	if rows := pane.Visible(4); len(rows) != 4 || rows[3] != "9" {
		t.Fatalf("last window = %v, want rows ending at 9", rows)
	}
}

func TestStatusLineFitsWidth(t *testing.T) {
	if got := (StatusLine{Left: "left", Right: "right"}).Render(20); len(got) != 20 || !strings.HasPrefix(got, "left") || !strings.HasSuffix(got, "right") {
		t.Fatalf("status line = %q, want left/right in 20 columns", got)
	}
	long := StatusLine{Left: strings.Repeat("x", 30), Right: "right"}.Render(20)
	if runeLen(long) != 20 {
		t.Fatalf("overlong status line = %q (%d runes), want 20", long, runeLen(long))
	}
}

func TestKeyDecoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want key
	}{
		{"plain rune", "j", key{kind: keyRune, rune: 'j'}},
		{"utf8 rune", "\u00e9", key{kind: keyRune, rune: '\u00e9'}},
		{"enter", "\r", key{kind: keyEnter}},
		{"ctrl-c", "\x03", key{kind: keyCtrlC}},
		{"ctrl-d", "\x04", key{kind: keyCtrlD}},
		{"ctrl-o", "\x0f", key{kind: keyCtrlO}},
		{"ctrl-t", "\x14", key{kind: keyCtrlT}},
		{"tab", "\x09", key{kind: keyTab}},
		{"backspace", "\x7f", key{kind: keyBackspace}},
		{"up", "\x1b[A", key{kind: keyUp}},
		{"down", "\x1b[B", key{kind: keyDown}},
		{"right", "\x1b[C", key{kind: keyRight}},
		{"left", "\x1b[D", key{kind: keyLeft}},
		{"shift-tab", "\x1b[Z", key{kind: keyShiftTab}},
		{"modified up", "\x1b[1;2A", key{kind: keyUp}},
		{"ss3 up", "\x1bOA", key{kind: keyUp}},
		{"lone esc", "\x1b", key{kind: keyEsc}},
		{"alt-enter", "\x1b\r", key{kind: keyAltEnter}},
		{"bracketed paste start", "\x1b[200~", key{kind: keyPasteStart}},
		{"bracketed paste end", "\x1b[201~", key{kind: keyPasteEnd}},
		{"delete key is not a paste marker", "\x1b[3~", key{kind: keyNone}},
		{"paste marker with a parameter is not one", "\x1b[200;1~", key{kind: keyNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readKey(bufio.NewReader(strings.NewReader(tc.in)), -1)
			if err != nil {
				t.Fatalf("readKey(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("readKey(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestReadKeyWaitsForASplitEscapeSequence exercises the descriptor path the
// table above never reaches: ESC arrives alone, the rest of the arrow follows
// a moment later, so the decoder must poll instead of calling it a lone Esc.
func TestReadKeyWaitsForASplitEscapeSequence(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("make a pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	go func() {
		_, _ = w.Write([]byte{0x1b})
		time.Sleep(time.Millisecond)
		_, _ = w.Write([]byte("[A"))
	}()
	got, err := readKey(bufio.NewReader(r), int(r.Fd()))
	if err != nil {
		t.Fatalf("readKey on a split arrow: %v", err)
	}
	if got.kind != keyUp {
		t.Fatalf("split arrow decoded as %+v, want keyUp", got)
	}
}

func TestSearchFieldIsBounded(t *testing.T) {
	m := testModel(80, 24, false)
	m.handle(key{kind: keyRune, rune: '/'})
	for i := 0; i < maxSearchLen+10; i++ {
		m.handle(key{kind: keyRune, rune: 'x'})
	}
	if got := runeLen(m.form.Value()); got != maxSearchLen {
		t.Fatalf("search value grew to %d runes, want the %d cap", got, maxSearchLen)
	}
}

func TestReadKeyReportsEOF(t *testing.T) {
	if _, err := readKey(bufio.NewReader(strings.NewReader("")), -1); err != io.EOF {
		t.Fatalf("readKey on an empty stream = %v, want io.EOF", err)
	}
}

func TestMenuShowsEveryItemAt80x24(t *testing.T) {
	m := testModel(80, 24, false)
	lines := m.render()
	if len(lines) != 24 {
		t.Fatalf("rendered %d rows, want 24", len(lines))
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"Olivares AI · Demo Estate", "Community",
		"Get started", "Next: olivares quickstart",
		"AI tools", "Sessions", "Security", "Settings", "All commands",
		"No engine found on this machine · version test",
		"↑↓ move  Enter show  / search  ? help  q quit",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("menu is missing %q:\n%s", want, text)
		}
	}
	for i, line := range lines {
		if runeLen(line) > 80 {
			t.Fatalf("row %d is %d columns wide, want at most 80: %q", i, runeLen(line), line)
		}
	}
	selected := lineContaining(text, "Get started")
	if !strings.Contains(selected, "▸") {
		t.Fatalf("the first row is not marked as selected: %q", selected)
	}
}

func TestSelectionMovesWithJAndK(t *testing.T) {
	m := testModel(80, 24, false)
	if quit, _ := m.handle(key{kind: keyRune, rune: 'j'}); quit {
		t.Fatal("j quit the menu")
	}
	text := rendered(m)
	if !strings.Contains(lineContaining(text, "AI tools"), "▸") {
		t.Fatalf("j did not select the second row:\n%s", text)
	}
	if strings.Contains(lineContaining(text, "Get started"), "▸") {
		t.Fatalf("j left the first row selected:\n%s", text)
	}
	m.handle(key{kind: keyRune, rune: 'k'})
	if !strings.Contains(lineContaining(rendered(m), "Get started"), "▸") {
		t.Fatal("k did not return to the first row")
	}
}

// Every row shows the command that does its action before Enter is pressed, so
// the menu teaches the scriptable CLI as the person moves. The command has a row
// of its own and the key hints stay whole on the bottom line.
func TestEverySelectedRowShowsItsEquivalentCommand(t *testing.T) {
	m := testModel(80, 24, false)
	for range m.frame.Items {
		item, ok := m.list.Selected()
		if !ok {
			t.Fatal("no row is selected")
		}
		assertCommandAndHints(t, m, "= "+item.Command)
		m.handle(key{kind: keyDown})
	}
}

// A command that fills the row (MinWidth less its 2-column indent and "= ") is
// shown whole, never cut, and takes no room from the key hints. The Enter notice
// is "Use: " plus the command, 3 columns longer, so it is checked on the longest
// value rootNextFromTools returns (49 columns), which the cmd/olivares tests pin
// against MinWidth.
func TestLongCommandIsShownWholeAndKeepsTheHints(t *testing.T) {
	for _, tc := range []struct {
		command string
		notice  bool
	}{
		{"olivares session start <folder> --tool gemini-cli", true},
		{strings.Repeat("x", MinWidth-len("  = ")), false},
	} {
		frame := testFrame()
		frame.Items = []MenuItem{{Label: "Get started", Detail: "Next", Command: tc.command}}
		color := false
		m := newModel(frame, MinWidth, 24, termrender.New(io.Discard, termrender.Options{Color: &color, Width: MinWidth}))
		assertCommandAndHints(t, m, "= "+tc.command)
		if tc.notice {
			m.handle(key{kind: keyEnter})
			assertCommandAndHints(t, m, "Use: "+tc.command)
		}
	}
}

// assertCommandAndHints wants wantRow whole on a row of its own and the frame's
// hints whole on the bottom line, every row within the terminal width.
func assertCommandAndHints(t *testing.T, m *model, wantRow string) {
	t.Helper()
	lines := m.render()
	if got := lines[len(lines)-1]; !strings.Contains(got, m.frame.Hints) {
		t.Fatalf("bottom line = %q, want the whole hints %q", got, m.frame.Hints)
	}
	found := false
	for _, line := range lines {
		if runeLen(line) > m.width {
			t.Fatalf("row is %d columns, want at most %d: %q", runeLen(line), m.width, line)
		}
		if strings.TrimSpace(line) == wantRow {
			found = true
		}
	}
	if !found {
		t.Fatalf("no row is %q:\n%s", wantRow, strings.Join(lines, "\n"))
	}
}

func TestEnterNamesTheEquivalentCommand(t *testing.T) {
	m := testModel(80, 24, false)
	m.handle(key{kind: keyEnter})
	assertCommandAndHints(t, m, "Use: olivares quickstart")
}

// The notice names the row Enter was pressed on. Searching changes which row is
// selected, so the notice must not outlive it and name a row that is not selected.
func TestEnterNoticeDoesNotSurviveASearch(t *testing.T) {
	m := testModel(80, 24, false)
	m.handle(key{kind: keyEnter})
	m.handle(key{kind: keyRune, rune: '/'})
	for _, r := range "tool" {
		m.handle(key{kind: keyRune, rune: r})
	}
	if text := rendered(m); strings.Contains(text, "Use: ") {
		t.Fatalf("a search left the Enter notice on screen:\n%s", text)
	}
	if text := rendered(m); !strings.Contains(text, "= olivares tool ls") {
		t.Fatalf("the searched row shows no equivalent command:\n%s", text)
	}
}

// Moving the selection after Enter returns the command row to the new row's
// command; a filter keeps the hints whole beside "filter: ..."; no match leaves
// no command to show.
func TestCommandRowFollowsMovementAndFilter(t *testing.T) {
	m := testModel(80, 24, false)
	m.handle(key{kind: keyEnter})
	m.handle(key{kind: keyDown})
	assertCommandAndHints(t, m, "= olivares tool ls")

	m.handle(key{kind: keyRune, rune: '/'})
	for _, r := range "tool" {
		m.handle(key{kind: keyRune, rune: r})
	}
	m.handle(key{kind: keyEnter})
	assertCommandAndHints(t, m, "= olivares tool ls")
	if got := m.render()[m.height-1]; !strings.Contains(got, "filter: tool") {
		t.Fatalf("bottom line = %q, want the filter beside the hints", got)
	}

	m.handle(key{kind: keyRune, rune: '/'})
	m.handle(key{kind: keyRune, rune: 'z'})
	m.handle(key{kind: keyEnter})
	if text := rendered(m); strings.Contains(text, "= ") {
		t.Fatalf("no row matches, yet a command is shown:\n%s", text)
	}
}

func TestSearchFiltersAndEscRestores(t *testing.T) {
	m := testModel(80, 24, false)
	m.handle(key{kind: keyRune, rune: '/'})
	if m.mode != modeSearch {
		t.Fatal("/ did not open the search field")
	}
	for _, r := range "sess" {
		m.handle(key{kind: keyRune, rune: r})
	}
	if m.list.Len() != 1 {
		t.Fatalf("search sess kept %d rows, want 1:\n%s", m.list.Len(), rendered(m))
	}
	if item, _ := m.list.Selected(); item.Label != "Sessions" {
		t.Fatalf("search selected %q, want Sessions", item.Label)
	}
	m.handle(key{kind: keyEnter})
	if m.mode != modeMenu || m.query != "sess" {
		t.Fatalf("Enter left mode %v query %q, want the filtered menu", m.mode, m.query)
	}
	m.handle(key{kind: keyRune, rune: '/'})
	m.handle(key{kind: keyEsc})
	if m.mode != modeMenu || m.query != "" || m.list.Len() != len(testFrame().Items) {
		t.Fatalf("Esc left mode %v query %q with %d rows, want the whole menu", m.mode, m.query, m.list.Len())
	}
}

func TestHelpOpensAndEscCloses(t *testing.T) {
	m := testModel(80, 24, false)
	m.handle(key{kind: keyRune, rune: '?'})
	if m.mode != modeHelp {
		t.Fatal("? did not open the help pane")
	}
	if !strings.Contains(rendered(m), "Keys on this screen") {
		t.Fatalf("help pane has no title:\n%s", rendered(m))
	}
	m.handle(key{kind: keyEsc})
	if m.mode != modeMenu {
		t.Fatal("Esc did not close the help pane")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode mode
		key  key
		code int
	}{
		{"menu q", modeMenu, key{kind: keyRune, rune: 'q'}, 0},
		{"menu ctrl-d", modeMenu, key{kind: keyCtrlD}, 0},
		{"menu ctrl-c", modeMenu, key{kind: keyCtrlC}, 130},
		{"help ctrl-d", modeHelp, key{kind: keyCtrlD}, 0},
		{"help ctrl-c", modeHelp, key{kind: keyCtrlC}, 130},
		{"search ctrl-d", modeSearch, key{kind: keyCtrlD}, 0},
		{"search ctrl-c", modeSearch, key{kind: keyCtrlC}, 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(80, 24, false)
			if tc.mode == modeSearch {
				m.handle(key{kind: keyRune, rune: '/'})
			} else {
				m.mode = tc.mode
			}
			quit, code := m.handle(tc.key)
			if !quit || code != tc.code {
				t.Fatalf("%s = quit %v code %d, want quit with %d", tc.name, quit, code, tc.code)
			}
		})
	}
	if quit, _ := testModel(80, 24, false).handle(key{kind: keyEsc}); quit {
		t.Fatal("Esc at the main menu quit the frame")
	}
	help := testModel(80, 24, false)
	help.mode = modeHelp
	if quit, _ := help.handle(key{kind: keyRune, rune: 'q'}); quit || help.mode != modeMenu {
		t.Fatal("q in the help pane should close the help, not quit the frame")
	}
	search := testModel(80, 24, false)
	search.handle(key{kind: keyRune, rune: '/'})
	if quit, _ := search.handle(key{kind: keyRune, rune: 'q'}); quit || search.form.Value() != "q" {
		t.Fatalf("q in the search field = quit %v, value %q; want the letter typed", quit, search.form.Value())
	}
}

func TestColorNeverCarriesMeaning(t *testing.T) {
	plain := rendered(testModel(80, 24, false))
	color := rendered(testModel(80, 24, true))
	if plain == color {
		t.Fatal("the colour renderer emitted no SGR, so the comparison below proves nothing")
	}
	if got := termrender.StripANSI(color); got != plain {
		t.Fatalf("stripping colour changed the screen:\ncolour:\n%s\nplain:\n%s", got, plain)
	}
	if !strings.Contains(color, "\x1b[32m") {
		t.Fatalf("the selected row carried no colour:\n%q", color)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("a renderer with colour off emitted an escape sequence: %q", plain)
	}
}

func TestWindowScrollsToTheSelection(t *testing.T) {
	frame := testFrame()
	frame.Items = nil
	for i := 0; i < 30; i++ {
		frame.Items = append(frame.Items, MenuItem{
			Label:   "Item " + string(rune('A'+i/10)) + string(rune('0'+i%10)),
			Detail:  "row",
			Command: "olivares --help",
		})
	}
	off := false
	r := termrender.New(io.Discard, termrender.Options{Color: &off, Width: 80})
	m := newModel(frame, 80, 24, r)
	for range 25 {
		m.handle(key{kind: keyDown})
	}
	text := rendered(m)
	if !strings.Contains(text, "Item C5") {
		t.Fatalf("the selected row is off screen:\n%s", text)
	}
	if strings.Contains(text, "Item A0") {
		t.Fatalf("the window kept the first row after scrolling:\n%s", text)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// drawAlone repaints a list on its own, as a host with one screen does.
func drawAlone(m *model, w io.Writer) error {
	h := &Host{width: m.width, height: m.height, renderer: m.renderer, stack: []page{m}}
	return h.draw(w)
}

// A terminal that stops accepting output must give the user a sentence, not a
// bare "write /dev/pts/3: input/output error" with no hint of what failed.
func TestDrawFailureIsExplained(t *testing.T) {
	err := drawAlone(testModel(80, 24, false), failingWriter{})
	if err == nil {
		t.Fatal("draw on a closed terminal returned no error")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("draw error %q lost its cause", err)
	}
	if !strings.HasPrefix(err.Error(), "could not draw the menu: ") {
		t.Errorf("draw error = %q, want it to start with \"could not draw the menu: \"", err)
	}
}
