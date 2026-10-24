// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testShell(width, height int) (*Shell, chan string) {
	sh := NewShell()
	sh.SetTitle("Refactor the cart totals · Claude Code", "Running")
	sh.resize(width, height)
	submitted := make(chan string, 8)
	sh.Submit = func(text string) { submitted <- text }
	return sh, submitted
}

func typeText(sh *Shell, text string) {
	for _, r := range text {
		sh.handle(key{kind: keyRune, rune: r})
	}
}

func screen(sh *Shell) string { return strings.Join(sh.render(), "\n") }

func nextSubmit(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case text := <-ch:
		return text
	case <-time.After(2 * time.Second):
		t.Fatal("Submit was not called")
		return ""
	}
}

func TestShellDrawsHeadingTranscriptComposerAndHintsAt80x24(t *testing.T) {
	sh, _ := testShell(80, 24)
	sh.Say("you  Refactor the cart totals.")
	sh.Say("I read the cart module.")
	rows := sh.render()
	if len(rows) != 24 {
		t.Fatalf("the screen has %d rows, want 24", len(rows))
	}
	if !strings.Contains(rows[0], "Refactor the cart totals · Claude Code") || !strings.HasSuffix(strings.TrimRight(rows[0], " "), "Running") {
		t.Errorf("heading row = %q, want the title and the state on the right", rows[0])
	}
	if !strings.Contains(rows[17], "I read the cart module.") && !strings.Contains(rows[18], "I read the cart module.") {
		t.Errorf("the newest line is not at the bottom of the transcript:\n%s", screen(sh))
	}
	if !strings.HasPrefix(rows[20], "> ") {
		t.Errorf("composer row = %q, want it to start with the prompt", rows[20])
	}
	for _, want := range []string{"Enter send", "Alt-Enter new line", "/help"} {
		if !strings.Contains(rows[23], want) {
			t.Errorf("hint row = %q, missing %q", rows[23], want)
		}
	}
	for i, row := range rows {
		if runeLen(row) > 80 {
			t.Errorf("row %d is %d columns wide: %q", i, runeLen(row), row)
		}
	}
}

func TestShellWrapsALongLineInsteadOfCuttingIt(t *testing.T) {
	sh, _ := testShell(80, 24)
	words := make([]string, 40)
	for i := range words {
		words[i] = fmt.Sprintf("word%02d", i)
	}
	long := strings.Join(words, " ")
	sh.Say(long)
	var got []string
	for _, row := range sh.render()[2:18] {
		if strings.TrimSpace(row) != "" {
			got = append(got, strings.TrimSpace(row))
		}
	}
	if joined := strings.Join(got, " "); joined != long {
		t.Fatalf("wrapping lost or changed text:\n got %q\nwant %q", joined, long)
	}
	if len(got) < 3 {
		t.Fatalf("a %d-column line fits in %d rows at width 80", len(long), len(got))
	}
}

func TestShellBreaksAWordLongerThanTheScreen(t *testing.T) {
	sh, _ := testShell(80, 24)
	token := strings.Repeat("x", 200)
	sh.Say(token)
	var total int
	for _, row := range sh.render()[2:18] {
		total += strings.Count(row, "x")
		if runeLen(row) > 80 {
			t.Fatalf("a row is %d columns wide", runeLen(row))
		}
	}
	if total != 200 {
		t.Fatalf("the screen holds %d of the 200 characters", total)
	}
}

func TestShellRemovesControlSequencesFromSessionText(t *testing.T) {
	sh, _ := testShell(80, 24)
	_, _ = sh.Write([]byte("\x1b]0;owned\x07before\x1b[31m red\x1b[0m\r\x00after\ttab\n"))
	text := screen(sh)
	for _, r := range text {
		if r != '\n' && (r < 0x20 || (r >= 0x7f && r <= 0x9f)) {
			t.Fatalf("a control character %q reached the screen", r)
		}
	}
	if !strings.Contains(text, "before") || !strings.Contains(text, "after") {
		t.Fatalf("the text around the control characters was lost:\n%s", text)
	}
}

func TestShellWriteJoinsPartialLines(t *testing.T) {
	sh, _ := testShell(80, 24)
	_, _ = sh.Write([]byte("ab"))
	_, _ = sh.Write([]byte("c\nd"))
	text := screen(sh)
	if !strings.Contains(text, "abc") || !strings.Contains(text, "d") {
		t.Fatalf("a line split across writes was not joined:\n%s", text)
	}
	if strings.Contains(text, "ab\n") {
		t.Fatalf("the first half of a line was shown as a line:\n%s", text)
	}
}

func TestShellEnterSendsTheMessageAndClearsTheComposer(t *testing.T) {
	sh, submitted := testShell(80, 24)
	typeText(sh, "run the tests")
	if !strings.Contains(sh.render()[20], "> run the tests") {
		t.Fatalf("the composer does not show what was typed: %q", sh.render()[20])
	}
	if quit := sh.handle(key{kind: keyEnter}); quit {
		t.Fatal("Enter quit the shell")
	}
	if got := nextSubmit(t, submitted); got != "run the tests" {
		t.Fatalf("Submit got %q, want the typed text", got)
	}
	if strings.Contains(sh.render()[20], "run the tests") {
		t.Fatalf("the composer kept the sent message: %q", sh.render()[20])
	}
}

func TestShellEnterOnAnEmptyComposerSendsNothing(t *testing.T) {
	sh, submitted := testShell(80, 24)
	typeText(sh, "   ")
	sh.handle(key{kind: keyEnter})
	select {
	case got := <-submitted:
		t.Fatalf("a blank message was sent: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestShellAltEnterAddsALineAndPasteDoesNotSend(t *testing.T) {
	sh, submitted := testShell(80, 24)
	typeText(sh, "first")
	sh.handle(key{kind: keyAltEnter})
	typeText(sh, "second")
	sh.handle(key{kind: keyPasteStart})
	typeText(sh, " pasted")
	sh.handle(key{kind: keyEnter})
	typeText(sh, "third")
	sh.handle(key{kind: keyPasteEnd})
	select {
	case got := <-submitted:
		t.Fatalf("a newline inside a paste sent %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	sh.handle(key{kind: keyEnter})
	want := "first\nsecond pasted\nthird"
	if got := nextSubmit(t, submitted); got != want {
		t.Fatalf("Submit got %q, want %q", got, want)
	}
}

func TestShellComposerShowsItsLastThreeRows(t *testing.T) {
	sh, _ := testShell(80, 24)
	for i := 1; i <= 6; i++ {
		typeText(sh, fmt.Sprintf("line%d", i))
		sh.handle(key{kind: keyAltEnter})
	}
	typeText(sh, "line7")
	text := screen(sh)
	if !strings.Contains(text, "line7") || !strings.Contains(text, "line5") {
		t.Fatalf("the composer does not show its last rows:\n%s", text)
	}
	if strings.Contains(text, "line4") {
		t.Fatalf("the composer shows more than three rows:\n%s", text)
	}
	if got := len(sh.render()); got != 24 {
		t.Fatalf("a taller composer changed the screen to %d rows", got)
	}
}

func TestShellBackspaceRemovesOneCharacter(t *testing.T) {
	sh, submitted := testShell(80, 24)
	typeText(sh, "a\u00f1\u20ac")
	sh.handle(key{kind: keyBackspace})
	sh.handle(key{kind: keyEnter})
	if got := nextSubmit(t, submitted); got != "a\u00f1" {
		t.Fatalf("Submit got %q, want %q", got, "a\u00f1")
	}
}

func TestShellHistoryWalksBackAndRestoresTheDraft(t *testing.T) {
	sh, submitted := testShell(80, 24)
	for _, text := range []string{"one", "two"} {
		typeText(sh, text)
		sh.handle(key{kind: keyEnter})
		nextSubmit(t, submitted)
	}
	typeText(sh, "draft")
	steps := []struct {
		key  keyKind
		want string
	}{
		{keyUp, "> two"}, {keyUp, "> one"}, {keyUp, "> one"}, {keyDown, "> two"}, {keyDown, "> draft"},
	}
	for i, step := range steps {
		sh.handle(key{kind: step.key})
		if row := strings.TrimRight(sh.render()[20], " "); row != step.want {
			t.Fatalf("step %d: composer = %q, want %q", i, row, step.want)
		}
	}
}

func TestShellQIsTextInTheComposerAndCtrlDLeaves(t *testing.T) {
	sh, submitted := testShell(80, 24)
	if quit := sh.handle(key{kind: keyRune, rune: 'q'}); quit {
		t.Fatal("q quit while the composer had focus")
	}
	sh.handle(key{kind: keyEnter})
	if got := nextSubmit(t, submitted); got != "q" {
		t.Fatalf("Submit got %q, want q", got)
	}
	if quit := sh.handle(key{kind: keyCtrlD}); !quit {
		t.Fatal("Ctrl-D did not leave the shell")
	}
}

func TestShellCtrlCInterruptsAndDoesNotLeave(t *testing.T) {
	sh, _ := testShell(80, 24)
	called := make(chan struct{}, 1)
	sh.Interrupt = func() { called <- struct{}{} }
	if quit := sh.handle(key{kind: keyCtrlC}); quit {
		t.Fatal("Ctrl-C left the shell; it must interrupt the turn")
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl-C did not call Interrupt")
	}
}

func TestShellEscMovesFocusToTheTranscriptAndQLeaves(t *testing.T) {
	sh, submitted := testShell(80, 24)
	sh.handle(key{kind: keyEsc})
	if strings.HasPrefix(sh.render()[20], "> ") {
		t.Fatalf("the composer still shows the prompt with the transcript focused: %q", sh.render()[20])
	}
	typeText(sh, "x")
	select {
	case got := <-submitted:
		t.Fatalf("typing in the transcript sent %q", got)
	default:
	}
	if quit := sh.handle(key{kind: keyRune, rune: 'q'}); !quit {
		t.Fatal("q did not leave from the transcript")
	}
	sh2, _ := testShell(80, 24)
	sh2.handle(key{kind: keyTab})
	sh2.handle(key{kind: keyEnter})
	if !strings.HasPrefix(sh2.render()[20], "> ") {
		t.Fatalf("Enter did not return focus to the composer: %q", sh2.render()[20])
	}
}

func TestShellScrollsBackAndKeepsItsPlaceWhileOutputArrives(t *testing.T) {
	sh, _ := testShell(80, 24)
	for i := 1; i <= 100; i++ {
		sh.Say(fmt.Sprintf("row %03d", i))
	}
	sh.handle(key{kind: keyEsc})
	for range 30 {
		sh.handle(key{kind: keyUp})
	}
	before := screen(sh)
	if strings.Contains(before, "row 100") {
		t.Fatalf("scrolling up still shows the newest row:\n%s", before)
	}
	if !strings.Contains(before, "row 07") && !strings.Contains(before, "row 08") {
		t.Fatalf("30 rows up does not show rows 70..:\n%s", before)
	}
	first := lineContaining(before, "row 0")
	sh.Say("row 101")
	sh.Say("row 102")
	if after := screen(sh); lineContaining(after, "row 0") != first {
		t.Fatalf("new output moved the place the person scrolled to:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for range 40 {
		sh.handle(key{kind: keyDown})
	}
	if !strings.Contains(screen(sh), "row 102") {
		t.Fatalf("scrolling down to the end does not show the newest output:\n%s", screen(sh))
	}
	sh.Say("row 103")
	if !strings.Contains(screen(sh), "row 103") {
		t.Fatalf("a shell at the bottom stopped following output:\n%s", screen(sh))
	}
}

func TestShellScrollStopsAtTheFirstRow(t *testing.T) {
	sh, _ := testShell(80, 24)
	for i := 1; i <= 40; i++ {
		sh.Say(fmt.Sprintf("row %03d", i))
	}
	sh.handle(key{kind: keyEsc})
	for range 500 {
		sh.handle(key{kind: keyUp})
	}
	if text := screen(sh); !strings.Contains(text, "row 001") {
		t.Fatalf("scrolling to the top does not show the first row:\n%s", text)
	}
}

func TestShellAskWaitsForYBeforeItActs(t *testing.T) {
	sh, submitted := testShell(80, 24)
	acted := make(chan struct{}, 2)
	sh.Ask("Stop cart? It ends the tool's process. [y/N]", func() { acted <- struct{}{} })
	if !strings.Contains(screen(sh), "Stop cart? It ends the tool's process. [y/N]") {
		t.Fatalf("the question is not on screen:\n%s", screen(sh))
	}
	typeText(sh, "x")
	sh.handle(key{kind: keyEnter})
	select {
	case <-acted:
		t.Fatal("an unrelated key and Enter confirmed the question")
	case got := <-submitted:
		t.Fatalf("the composer took input during a question: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	if strings.Contains(screen(sh), "[y/N]") {
		t.Fatal("Enter left the question on screen: it should answer no")
	}
	sh.Ask("Stop cart? [y/N]", func() { acted <- struct{}{} })
	sh.handle(key{kind: keyRune, rune: 'y'})
	select {
	case <-acted:
	case <-time.After(2 * time.Second):
		t.Fatal("y did not confirm")
	}
	if strings.Contains(screen(sh), "[y/N]") {
		t.Fatal("the question stayed on screen after the answer")
	}
}

func TestShellLockedComposerIsNotOffered(t *testing.T) {
	sh, submitted := testShell(80, 24)
	sh.Lock("Steering a session another person started requires Olivares Business.")
	text := screen(sh)
	if !strings.Contains(text, "requires Olivares Business") {
		t.Fatalf("the reason is not on screen:\n%s", text)
	}
	if strings.HasPrefix(sh.render()[20], "> ") {
		t.Fatalf("a locked composer still shows a prompt: %q", sh.render()[20])
	}
	typeText(sh, "hello")
	sh.handle(key{kind: keyEnter})
	select {
	case got := <-submitted:
		t.Fatalf("a locked shell sent %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	if quit := sh.handle(key{kind: keyCtrlD}); !quit {
		t.Fatal("a locked shell cannot be left")
	}
}

func TestShellComposerAndTranscriptAreBounded(t *testing.T) {
	sh, submitted := testShell(80, 24)
	sh.handle(key{kind: keyPasteStart})
	for range maxComposerRunes/1000 + 50 {
		typeText(sh, strings.Repeat("a", 1000))
	}
	sh.handle(key{kind: keyPasteEnd})
	sh.handle(key{kind: keyEnter})
	if got := nextSubmit(t, submitted); utf8.RuneCountInString(got) > maxComposerRunes {
		t.Fatalf("the composer took %d characters, the cap is %d", utf8.RuneCountInString(got), maxComposerRunes)
	}
	for i := 0; i < maxTranscriptLines+500; i++ {
		sh.Say("line")
	}
	sh.mu.Lock()
	n := len(sh.lines)
	sh.mu.Unlock()
	if n > maxTranscriptLines {
		t.Fatalf("the transcript holds %d lines, the cap is %d", n, maxTranscriptLines)
	}
}

func TestShellWakesTheLoopWhenOutputArrives(t *testing.T) {
	sh, _ := testShell(80, 24)
	sh.Say("hello")
	select {
	case <-sh.wake:
	default:
		t.Fatal("Say did not wake the draw loop")
	}
}

func TestReadKeyDecodesBracketedPaste(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\x1b[200~\x1b[201~"))
	for i, want := range []keyKind{keyPasteStart, keyPasteEnd} {
		k, err := readKey(r, -1)
		if err != nil || k.kind != want {
			t.Fatalf("key %d = %+v, %v; want kind %d", i, k, err, want)
		}
	}
}

func TestShellBelowTheMinimumSizeSaysSoAndIgnoresKeys(t *testing.T) {
	sh, submitted := testShell(80, 24)
	sh.resize(60, 20)
	if rows := sh.render(); len(rows) != 20 || !strings.Contains(rows[0], "Make the terminal at least 80 x 24") {
		t.Fatalf("a 60x20 terminal shows %q, want the size sentence on row 0", rows[0])
	}
	typeText(sh, "hidden")
	sh.handle(key{kind: keyEnter})
	select {
	case got := <-submitted:
		t.Fatalf("a key acted on a screen the person could not see: sent %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	sh.resize(80, 24)
	if row := sh.render()[20]; strings.TrimRight(row, " ") != "> " && strings.TrimRight(row, " ") != ">" {
		t.Fatalf("the composer kept what was typed while the screen was hidden: %q", row)
	}
	if quit := sh.handle(key{kind: keyCtrlD}); !quit {
		t.Fatal("Ctrl-D did not leave")
	}
}

func TestShellWrapsWideCharactersByColumns(t *testing.T) {
	sh, _ := testShell(80, 24)
	sh.Say(strings.Repeat("漢", 60))
	var total int
	for i, row := range sh.render() {
		if cols := textCols(row); cols > 80 {
			t.Errorf("row %d takes %d columns: %q", i, cols, row)
		}
		total += strings.Count(row, "漢")
	}
	if total != 60 {
		t.Fatalf("the screen holds %d of the 60 characters", total)
	}
}

func TestShellSplitsAHugeLineAndKeepsEverything(t *testing.T) {
	sh, _ := testShell(80, 24)
	_, _ = sh.Write([]byte(strings.Repeat("a", 3*maxLineRunes+10)))
	sh.mu.Lock()
	held := runeLen(sh.partial)
	for _, line := range sh.lines {
		if runeLen(line) > maxLineRunes {
			t.Errorf("a transcript line holds %d runes, the cap is %d", runeLen(line), maxLineRunes)
		}
		held += runeLen(line)
	}
	if runeLen(sh.partial) > maxLineRunes {
		t.Errorf("the unfinished line holds %d runes", runeLen(sh.partial))
	}
	sh.mu.Unlock()
	if held != 3*maxLineRunes+10 {
		t.Fatalf("the transcript holds %d of %d characters", held, 3*maxLineRunes+10)
	}
	for i, row := range sh.render() {
		if textCols(row) > 80 {
			t.Errorf("row %d is %d columns wide", i, textCols(row))
		}
	}
}

func TestShellKeepsTheViewWhenAnUnfinishedLineCompletesWhileScrolledBack(t *testing.T) {
	sh, _ := testShell(80, 24)
	for i := 1; i <= 100; i++ {
		sh.Say(fmt.Sprintf("row %03d", i))
	}
	sh.handle(key{kind: keyEsc})
	for range 10 {
		sh.handle(key{kind: keyUp})
	}
	first := lineContaining(screen(sh), "row 0")
	long := strings.Repeat("word ", 40)
	_, _ = sh.Write([]byte(long[:100]))
	if got := lineContaining(screen(sh), "row 0"); got != first {
		t.Fatalf("an unfinished line moved the view: %q -> %q", first, got)
	}
	_, _ = sh.Write([]byte(long[100:] + "\n"))
	if got := lineContaining(screen(sh), "row 0"); got != first {
		t.Fatalf("finishing the line moved the view: %q -> %q", first, got)
	}
}

func TestShellComposerDropsC1ControlCharacters(t *testing.T) {
	sh, submitted := testShell(80, 24)
	typeText(sh, "a\u009bb\u007fc")
	sh.handle(key{kind: keyEnter})
	if got := nextSubmit(t, submitted); got != "abc" {
		t.Fatalf("Submit got %q, want abc", got)
	}
}

func TestShellEscEndsAPasteThatNeverClosed(t *testing.T) {
	sh, submitted := testShell(80, 24)
	sh.handle(key{kind: keyPasteStart})
	sh.handle(key{kind: keyEsc})
	sh.handle(key{kind: keyEnter})
	typeText(sh, "x")
	sh.handle(key{kind: keyEnter})
	if got := nextSubmit(t, submitted); got != "x" {
		t.Fatalf("Submit got %q, want x: Enter still inserted newlines", got)
	}
}

func TestShellPasteMarkerDoesNotAnswerAQuestion(t *testing.T) {
	sh, _ := testShell(80, 24)
	sh.Ask("Stop cart? [y/N]", func() {})
	sh.handle(key{kind: keyPasteStart})
	if !strings.Contains(screen(sh), "Stop cart? [y/N]") {
		t.Fatalf("a paste marker answered the question:\n%s", screen(sh))
	}
}

func TestShellPastedYDoesNotAnswerAQuestion(t *testing.T) {
	sh, _ := testShell(80, 24)
	acted := make(chan struct{}, 1)
	sh.Ask("Stop cart? [y/N]", func() { acted <- struct{}{} })
	sh.handle(key{kind: keyPasteStart})
	sh.handle(key{kind: keyRune, rune: 'y'})
	sh.handle(key{kind: keyPasteEnd})
	select {
	case <-acted:
		t.Fatal("a pasted y confirmed the question")
	case <-time.After(50 * time.Millisecond):
	}
	if strings.Contains(screen(sh), "[y/N]") {
		t.Fatal("the paste left the question open: any key but a typed y answers no")
	}
}

func TestShellPastedLettersAreNotTranscriptCommands(t *testing.T) {
	sh, _ := testShell(80, 24)
	for i := 1; i <= 40; i++ {
		sh.Say(fmt.Sprintf("row %03d", i))
	}
	sh.handle(key{kind: keyEsc}) // focus the transcript, where q leaves and j, k scroll
	sh.handle(key{kind: keyPasteStart})
	for _, r := range "kkkq" {
		if sh.handle(key{kind: keyRune, rune: r}) {
			t.Fatalf("a pasted %q left the shell", r)
		}
	}
	sh.handle(key{kind: keyPasteEnd})
	if text := screen(sh); !strings.Contains(text, "row 040") {
		t.Fatalf("pasted letters scrolled the transcript:\n%s", text)
	}
}

func TestShellReplacesInvalidUTF8(t *testing.T) {
	sh, _ := testShell(80, 24)
	_, _ = sh.Write([]byte("a\x9bb\n"))
	text := screen(sh)
	if strings.ContainsRune(text, 0x9b) || strings.Contains(text, "\x9b") || !strings.Contains(text, "a�b") {
		t.Fatalf("invalid UTF-8 reached the screen:\n%q", text)
	}
}
