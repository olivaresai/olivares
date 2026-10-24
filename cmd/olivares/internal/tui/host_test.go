// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

func testHost(width, height int) *Host {
	off := false
	return newHost(width, height, termrender.New(io.Discard, termrender.Options{Color: &off, Width: width}))
}

func typeIntoHost(h *Host, text string) {
	for _, r := range text {
		h.handle(key{kind: keyRune, rune: r})
	}
}

// drain runs the functions posted so far.
func (h *Host) drain() {
	for {
		select {
		case fn := <-h.posted:
			fn()
		default:
			return
		}
	}
}

// visualRows is how many terminal rows the entries take once the terminal wraps
// the ones wider than width. The frame writes a long link as one entry, so the
// terminal's own wrap keeps it copyable.
func visualRows(entries []string, width int) int {
	total := 0
	for _, entry := range entries {
		total += entryRows(entry, width)
	}
	return total
}

func screenText(h *Host) string { return strings.Join(h.render(), "\n") }

func TestEnterOnARowWithAnOpenerPushesItsScreenAndEscReturns(t *testing.T) {
	h := testHost(80, 24)
	opened := false
	frame := testFrame()
	frame.Items[1].Open = func(host *Host) string {
		opened = true
		host.PushPanel(&Panel{Heading: "AI tools › Install", Lines: []string{"Installing."}})
		return ""
	}
	h.PushMenu(frame)
	h.handle(key{kind: keyDown})
	h.handle(key{kind: keyEnter})
	if !opened || h.Depth() != 2 {
		t.Fatalf("Enter on a row with an opener left depth %d, opened %v; want the screen pushed", h.Depth(), opened)
	}
	if !strings.Contains(screenText(h), "AI tools › Install") {
		t.Fatalf("the pushed screen is not drawn:\n%s", screenText(h))
	}
	if quit, _ := h.handle(key{kind: keyEsc}); quit || h.Depth() != 1 {
		t.Fatalf("Esc left depth %d (quit %v), want the main menu", h.Depth(), quit)
	}
	if quit, _ := h.handle(key{kind: keyEsc}); quit || h.Depth() != 1 {
		t.Fatal("Esc at the main menu must neither quit nor pop the root")
	}
}

func TestEnterOnARowWithoutAnOpenerStillNamesTheCommand(t *testing.T) {
	h := testHost(80, 24)
	h.PushMenu(testFrame())
	h.handle(key{kind: keyEnter})
	if h.Depth() != 1 || !strings.Contains(screenText(h), "Use: olivares quickstart") {
		t.Fatalf("a row with no opener changed the screen:\n%s", screenText(h))
	}
}

func TestQInASubMenuGoesBackAndQAtTheRootQuits(t *testing.T) {
	h := testHost(80, 24)
	h.PushMenu(testFrame())
	h.PushMenu(testFrame())
	if quit, _ := h.handle(key{kind: keyRune, rune: 'q'}); quit || h.Depth() != 1 {
		t.Fatalf("q in a sub-menu = quit %v depth %d, want back to the main menu", quit, h.Depth())
	}
	if quit, code := h.handle(key{kind: keyRune, rune: 'q'}); !quit || code != 0 {
		t.Fatalf("q at the main menu = quit %v code %d, want quit 0", quit, code)
	}
}

func TestMenuSetFrameReplacesRowsAndKeepsTheCursor(t *testing.T) {
	h := testHost(80, 24)
	menu := h.PushMenu(Frame{Heading: "AI tools", Footer: "Reading the tools from the engine…"})
	if !strings.Contains(screenText(h), "Reading the tools from the engine…") {
		t.Fatalf("the loading frame is not drawn:\n%s", screenText(h))
	}
	menu.SetFrame(testFrame())
	menu.Select(2)
	if !strings.Contains(lineContaining(screenText(h), "Sessions"), "▸") {
		t.Fatalf("Select(2) did not mark the third row:\n%s", screenText(h))
	}
	menu.SetNotice("Installed Claude Code.")
	if !strings.Contains(screenText(h), "Installed Claude Code.") {
		t.Fatalf("the notice is not drawn:\n%s", screenText(h))
	}
}

// An answer from the engine closes its panel only when the person is still on
// it: a panel they left is not there to close, and the screen below must stay.
func TestCloseRemovesAPanelOnlyWhileItIsOnTop(t *testing.T) {
	h := testHost(80, 24)
	h.PushMenu(testFrame())
	left := &Panel{Heading: "left"}
	h.PushPanel(left)
	h.leaveTop() // the person pressed Esc
	if h.OnTop(left) || h.Close(left) || h.Depth() != 1 {
		t.Fatalf("Close acted on a panel the person already left (depth %d)", h.Depth())
	}
	open := &Panel{Heading: "open"}
	h.PushPanel(open)
	if !h.OnTop(open) || !h.Close(open) || h.Depth() != 1 {
		t.Fatalf("Close did not remove the panel the person is on (depth %d)", h.Depth())
	}
	covered := &Panel{Heading: "covered"}
	h.PushPanel(covered)
	h.PushMenu(testFrame())
	if h.Close(covered) || h.Depth() != 3 {
		t.Fatal("Close removed a panel that another screen covers")
	}
}

// An answer that comes back for a list asks whether the person is still on the
// stack that holds it: a list they left cannot show a notice.
func TestHoldsSaysWhetherAMenuIsStillOnTheStack(t *testing.T) {
	h := testHost(80, 24)
	root := h.PushMenu(testFrame())
	sub := h.PushMenu(testFrame())
	if !h.Holds(sub) || !h.Holds(root) || !h.Holds(h.Root()) {
		t.Fatal("Holds missed a menu that is on the stack")
	}
	h.leaveTop()
	if h.Holds(sub) {
		t.Fatal("Holds found a menu the person left")
	}
	if !h.Holds(root) {
		t.Fatal("Holds lost the main menu when a screen above it was left")
	}
}

// A signal ends the work of the open screens before the process exits, and does
// not wait on a loop that never answers.
func TestEndWorkStopsEveryScreensWorkAndGivesUpOnAStuckLoop(t *testing.T) {
	h := testHost(80, 24)
	stopped := 0
	h.PushMenu(testFrame())
	h.PushPanel(&Panel{Heading: "waiting", Cancel: func() { stopped++ }})
	done := make(chan struct{})
	go func() { // the loop of Run, reduced to what endWork needs from it
		defer close(done)
		for {
			select {
			case fn := <-h.posted:
				fn()
			case <-h.done:
				return
			}
		}
	}()
	h.endWork(time.Second)
	if stopped != 1 {
		t.Fatalf("endWork stopped %d panels' work, want 1", stopped)
	}
	h.close()
	<-done

	stuck := testHost(80, 24)
	stuck.PushMenu(testFrame())
	start := time.Now()
	stuck.endWork(50 * time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatal("endWork waited on a loop that never ran")
	}
}

// Once a signal ended the work, the loop draws nothing more: the signal's exit
// restores the terminal and writes what the screens could not finish there, and a
// late frame would land on top of it.
func TestNothingIsDrawnAfterASignalEndedTheWork(t *testing.T) {
	h := testHost(80, 24)
	h.PushMenu(testFrame())
	var before strings.Builder
	if err := h.draw(&before); err != nil || before.Len() == 0 {
		t.Fatalf("a host that is running drew %d bytes (err %v), want a frame", before.Len(), err)
	}
	go func() { // the loop of Run, reduced to what endWork needs from it
		for {
			select {
			case fn := <-h.posted:
				fn()
			case <-h.done:
				return
			}
		}
	}()
	h.endWork(time.Second)
	var after strings.Builder
	if err := h.draw(&after); err != nil || after.Len() != 0 {
		t.Errorf("a host whose work a signal ended drew %q, want nothing", after.String())
	}
	h.close()
}

func TestPanelFormEditsMovesAndSubmitsOnTheLastField(t *testing.T) {
	h := testHost(80, 24)
	var got []string
	p := &Panel{Heading: "Add an API key", Submit: func(values []string) { got = values }}
	p.SetFields([]Field{
		{Label: "Provider", Value: "Anthropic", Choices: []string{"Anthropic", "OpenAI", "xAI"}},
		{Label: "Name"},
		{Label: "Key", Secret: true},
	})
	h.PushMenu(testFrame())
	h.PushPanel(p)
	h.handle(key{kind: keyRight})
	typeIntoHost(h, "q") // text in the choice field is ignored
	h.handle(key{kind: keyEnter})
	typeIntoHost(h, "team")
	h.handle(key{kind: keyEnter})
	typeIntoHost(h, "sk-ant-1234")
	if got != nil {
		t.Fatalf("submitted before Enter on the last field: %v", got)
	}
	h.handle(key{kind: keyEnter})
	want := []string{"OpenAI", "team", "sk-ant-1234"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("submitted %v, want %v", got, want)
	}
}

func TestPanelChoiceCyclesBothWaysAndWraps(t *testing.T) {
	var f Form
	f.SetFields([]Field{{Label: "Provider", Value: "Anthropic", Choices: []string{"Anthropic", "OpenAI", "xAI"}}})
	f.Cycle(-1)
	if f.Value() != "xAI" {
		t.Fatalf("Cycle(-1) from the first = %q, want the last", f.Value())
	}
	f.Cycle(1)
	if f.Value() != "Anthropic" {
		t.Fatalf("Cycle(1) from the last = %q, want the first", f.Value())
	}
}

// The key is typed in raw mode, which does not echo, and the frame must not draw
// it either: only that something was entered.
func TestPanelNeverDrawsASecretField(t *testing.T) {
	h := testHost(80, 24)
	p := &Panel{Heading: "Add an API key"}
	p.SetFields([]Field{{Label: "Key", Secret: true}})
	h.PushMenu(testFrame())
	h.PushPanel(p)
	const secret = "sk-ant-api03-SECRET-VALUE"
	typeIntoHost(h, secret)
	text := screenText(h)
	if strings.Contains(text, "SECRET") || strings.Contains(text, "sk-ant") {
		t.Fatalf("the screen shows the key:\n%s", text)
	}
	if !strings.Contains(text, "hidden · 25 characters") {
		t.Fatalf("the screen does not say a key was entered:\n%s", text)
	}
	h.handle(key{kind: keyBackspace})
	if !strings.Contains(screenText(h), "hidden · 24 characters") {
		t.Fatalf("Backspace did not shorten the hidden key:\n%s", screenText(h))
	}
}

// A key sent to the engine is no longer held by the panel.
func TestPanelClearFieldsEmptiesEveryValue(t *testing.T) {
	h := testHost(80, 24)
	p := &Panel{Heading: "Add an API key"}
	p.SetFields([]Field{{Label: "Name", Value: "work"}, {Label: "Key", Secret: true}})
	h.PushMenu(testFrame())
	h.PushPanel(p)
	h.handle(key{kind: keyTab})
	typeIntoHost(h, "sk-ant-api03-SECRET")
	p.ClearFields()
	for i, v := range p.Values() {
		if v != "" {
			t.Errorf("field %d still holds %q", i, v)
		}
	}
	if strings.Contains(screenText(h), "hidden") {
		t.Errorf("the screen still says a key is held:\n%s", screenText(h))
	}
}

// A field takes text. DEL and the C1 controls are not text: the Name field draws
// what is typed, and a terminal would act on them.
func TestPanelFieldRefusesControlRunes(t *testing.T) {
	h := testHost(80, 24)
	p := &Panel{Heading: "Add an API key"}
	p.SetFields([]Field{{Label: "Name"}})
	h.PushMenu(testFrame())
	h.PushPanel(p)
	for _, r := range []rune{0x7f, 0x80, 0x9b, 0x9f} {
		h.handle(key{kind: keyRune, rune: r})
	}
	typeIntoHost(h, "ok")
	if got := p.Values()[0]; got != "ok" {
		t.Errorf("the field holds %q, want only the text typed", got)
	}
}

// The first read of a screen puts the cursor on the next step. It must not undo a
// search the person is already typing.
func TestMenuSelectKeepsASearchInProgress(t *testing.T) {
	h := testHost(80, 24)
	mn := h.PushMenu(testFrame())
	mn.m.applyFilter("AI")
	mn.Select(0)
	if mn.m.query != "AI" {
		t.Errorf("the search reads %q after Select, want it kept", mn.m.query)
	}
}

func TestPanelFieldIsBounded(t *testing.T) {
	h := testHost(80, 24)
	p := &Panel{Heading: "Add an API key"}
	p.SetFields([]Field{{Label: "Key", Secret: true}})
	h.PushMenu(testFrame())
	h.PushPanel(p)
	typeIntoHost(h, strings.Repeat("k", maxFieldLen+500))
	if got := runeLen(p.Values()[0]); got != maxFieldLen {
		t.Fatalf("field holds %d runes, want %d", got, maxFieldLen)
	}
}

func TestPanelWrapsWordsAndKeepsALongLinkWhole(t *testing.T) {
	h := testHost(80, 24)
	link := "https://claude.ai/oauth/authorize?code=true&client_id=" + strings.Repeat("a1b2c3", 30)
	h.PushMenu(testFrame())
	h.PushPanel(&Panel{Heading: "Sign in", Lines: []string{
		"  Open this page and sign in, then paste the code the page shows. The code works for fifteen minutes only.",
		"  " + link,
		"  Enter this code there:",
	}})
	rows := h.render()
	text := strings.Join(rows, "\n")
	if !strings.Contains(text, link) {
		t.Fatalf("the link was clipped or split; it must reach the terminal whole so it can be copied:\n%s", text)
	}
	for _, row := range rows {
		if strings.Contains(row, link) {
			continue
		}
		if runeLen(row) > 80 {
			t.Fatalf("a text row is %d columns wide: %q", runeLen(row), row)
		}
	}
	if used := visualRows(rows, 80); used != 24 {
		t.Fatalf("the screen takes %d terminal rows, want exactly 24 (a wrapped link must not push the status line off)", used)
	}
	if last := rows[len(rows)-1]; !strings.Contains(last, "Enter") {
		t.Fatalf("the last entry is not the status line: %q", last)
	}
}

func TestPanelEscCtrlCAndQuitCancelTheWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      key
		wantQuit bool
		depth    int
	}{
		{"Esc", key{kind: keyEsc}, false, 1},
		{"Ctrl-C", key{kind: keyCtrlC}, false, 1},
		{"Ctrl-D", key{kind: keyCtrlD}, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testHost(80, 24)
			canceled, closed := 0, 0
			h.PushMenu(testFrame())
			h.PushPanel(&Panel{Heading: "Sign in", Busy: true,
				Cancel: func() { canceled++ }, OnClose: func() { closed++ }})
			quit, code := h.handle(tc.key)
			if quit != tc.wantQuit || code != 0 || h.Depth() != tc.depth {
				t.Fatalf("%s = quit %v code %d depth %d", tc.name, quit, code, h.Depth())
			}
			if canceled != 1 {
				t.Fatalf("%s canceled the work %d times, want once", tc.name, canceled)
			}
			if wantClosed := map[bool]int{true: 0, false: 1}[tc.wantQuit]; closed != wantClosed {
				t.Fatalf("%s ran OnClose %d times, want %d", tc.name, closed, wantClosed)
			}
		})
	}
}

func TestQuitCancelsEveryOpenPanel(t *testing.T) {
	h := testHost(80, 24)
	canceled := 0
	h.PushMenu(testFrame())
	h.PushPanel(&Panel{Heading: "one", Cancel: func() { canceled++ }})
	h.PushPanel(&Panel{Heading: "two", Cancel: func() { canceled++ }})
	h.handle(key{kind: keyCtrlD})
	if canceled != 2 {
		t.Fatalf("quit canceled %d panels, want 2", canceled)
	}
}

func TestBusyPanelIgnoresEnterAndTyping(t *testing.T) {
	h := testHost(80, 24)
	submitted := false
	p := &Panel{Heading: "Add an API key", Busy: true, Submit: func([]string) { submitted = true }}
	p.SetFields([]Field{{Label: "Key", Secret: true}})
	h.PushMenu(testFrame())
	h.PushPanel(p)
	typeIntoHost(h, "abc")
	h.handle(key{kind: keyEnter})
	if submitted || p.Values()[0] != "" || h.Depth() != 2 {
		t.Fatalf("a busy panel took input: submitted %v value %q depth %d", submitted, p.Values()[0], h.Depth())
	}
}

// Ctrl-C is only offered where there is work to stop: a busy panel with nothing
// to cancel says Esc leaves it.
func TestBusyPanelOffersCtrlCOnlyWhereThereIsWorkToStop(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cancel       func()
		want, absent string
	}{
		{"with work to stop", func() {}, "Ctrl-C cancel", "Esc leave"},
		{"without", nil, "Esc leave", "Ctrl-C cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testHost(80, 24)
			h.PushMenu(testFrame())
			h.PushPanel(&Panel{Heading: "Add an API key", Busy: true, Cancel: tc.cancel})
			if screen := screenText(h); !strings.Contains(screen, tc.want) || strings.Contains(screen, tc.absent) {
				t.Fatalf("busy panel status line should say %q and not %q:\n%s", tc.want, tc.absent, screen)
			}
		})
	}
}

func TestTextPanelEnterGoesBackWhenNothingIsRunning(t *testing.T) {
	h := testHost(80, 24)
	closed := 0
	h.PushMenu(testFrame())
	h.PushPanel(&Panel{Heading: "Installed", Lines: []string{"Installed Claude Code 2.1."}, OnClose: func() { closed++ }})
	h.handle(key{kind: keyEnter})
	if h.Depth() != 1 || closed != 1 {
		t.Fatalf("Enter on a finished panel left depth %d, OnClose %d; want back, once", h.Depth(), closed)
	}
}

func TestPostedWorkRunsOnTheLoopAndAfterCloseDoesNotBlock(t *testing.T) {
	h := testHost(80, 24)
	h.PushMenu(testFrame())
	ran := false
	h.Post(func() { ran = true })
	h.drain()
	if !ran {
		t.Fatal("a posted function did not run on the loop")
	}
	h.close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range cap(h.posted) + 5 {
			h.Post(func() {})
		}
	}()
	<-done
}

func TestDrawWritesEveryTerminalRowOnceWithAWrappedLink(t *testing.T) {
	h := testHost(80, 24)
	link := "https://example.com/" + strings.Repeat("x", 200)
	h.PushMenu(testFrame())
	h.PushPanel(&Panel{Heading: "Sign in", Lines: []string{link}})
	var out bytes.Buffer
	if err := h.draw(&out); err != nil {
		t.Fatalf("draw: %v", err)
	}
	// 220 columns wrap onto 3 rows of an 80-column terminal: the terminal moves
	// down twice by itself, so 22 entries and 21 line breaks fill 24 rows.
	if got := strings.Count(out.String(), "\r\n"); got != 21 {
		t.Fatalf("draw moved down %d times, want 21: the terminal's own wrap must not add rows", got)
	}
	if !strings.Contains(out.String(), link) {
		t.Fatal("draw did not write the link in one piece")
	}
}

// The fields are what a page is for: a body longer than the screen never pushes
// them off, whatever the engine put in it.
func TestPanelKeepsItsFieldsWhenTheBodyIsLong(t *testing.T) {
	h := testHost(80, 24)
	h.PushMenu(testFrame())
	var lines []string
	for range 40 {
		lines = append(lines, "  A long sentence from the engine that takes a whole row of the screen by itself.")
	}
	p := &Panel{Heading: "Sign in", Lines: lines, Footer: "Paste the code the page shows."}
	p.SetFields([]Field{{Label: "Code", Secret: true}})
	h.PushPanel(p)
	text := screenText(h)
	if !strings.Contains(text, "Code") {
		t.Fatalf("the Code field is not on the screen:\n%s", text)
	}
	if !strings.Contains(text, "Paste the code the page shows.") {
		t.Fatalf("the footer is not on the screen:\n%s", text)
	}
	if used := visualRows(h.render(), 80); used != 24 {
		t.Fatalf("the screen takes %d rows, want exactly 24", used)
	}
}
