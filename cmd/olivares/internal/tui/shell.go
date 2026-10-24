// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// The session shell of CLI-CONCEPT §5.4: a heading, a transcript that follows
// the session's output, one question row, a composer and a row of key hints.
// It knows nothing about sessions. The caller writes the conversation into it
// (it is an io.Writer, and Say adds one line) and hears about the person's
// messages and interrupts through Submit and Interrupt.

const (
	// maxComposerRunes bounds one message: a paste cannot grow the composer
	// until every keystroke re-wraps a huge string.
	maxComposerRunes = 32768
	// maxTranscriptLines bounds the scrollback; the oldest lines go first.
	maxTranscriptLines = 5000
	// maxLineRunes bounds one transcript line. A longer one becomes several, so
	// wrapping a single huge line never stalls the screen.
	maxLineRunes = 4096
	// maxHistory bounds the messages Up walks back through.
	maxHistory = 100
	// composerRows is how many rows of the message the composer shows.
	composerRows = 3
	// shellChrome is every row that is not transcript: heading, separator, the
	// question row, a separator, the composer and the hints.
	shellChrome = 5 + composerRows
	// tabWidth is what a tab in session text becomes: a terminal would jump to a
	// stop the wrapping does not know about.
	tabWidth = 4
)

type focus int

const (
	focusComposer focus = iota
	focusTranscript
)

// Shell is the state of the session screen. Say, Write, SetTitle, Ask and Lock
// are safe to call from any goroutine; the callbacks run on the key loop and
// must return quickly, so a slow one starts its work in a goroutine.
type Shell struct {
	// Submit receives a message when Enter sends it.
	Submit func(text string)
	// Interrupt is called on Ctrl-C.
	Interrupt func()

	mu         sync.Mutex
	wake       chan struct{}
	width      int
	height     int
	title      string
	state      string
	lines      []string
	partial    string
	scrollBack int
	composer   []rune
	history    []string
	histPos    int
	draft      []rune
	pasting    bool
	focus      focus
	question   string
	onYes      func()
	lockReason string
}

// NewShell returns a shell for a terminal of MinWidth x MinHeight until the
// caller resizes it.
func NewShell() *Shell {
	return &Shell{wake: make(chan struct{}, 1), width: MinWidth, height: MinHeight}
}

// SetTitle sets the heading: what the session is called and, on the right, its
// state.
func (s *Shell) SetTitle(title, state string) {
	s.mu.Lock()
	s.title, s.state = oneLine(title), oneLine(state)
	s.mu.Unlock()
	s.signal()
}

// Say adds one line to the transcript. A line still being written ends first.
func (s *Shell) Say(line string) {
	s.mu.Lock()
	s.endPartialLocked()
	s.appendLocked(line + "\n")
	s.mu.Unlock()
	s.signal()
}

// Write adds session output. Control sequences are removed first, so what an
// agent prints can never drive the terminal; a line split across writes is
// joined, and the unfinished part shows at once.
func (s *Shell) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.appendLocked(string(p))
	s.mu.Unlock()
	s.signal()
	return len(p), nil
}

// Ask shows question and runs yes only if the next key is y. Any other key
// answers no and the question goes away.
func (s *Shell) Ask(question string, yes func()) {
	s.mu.Lock()
	s.question, s.onYes = oneLine(question), yes
	s.mu.Unlock()
	s.signal()
}

// Lock turns the composer off for good and shows why. The transcript still
// follows the session, scrolls, and can be left.
func (s *Shell) Lock(reason string) {
	s.mu.Lock()
	s.lockReason = oneLine(reason)
	s.focus = focusTranscript
	s.mu.Unlock()
	s.signal()
}

func (s *Shell) resize(width, height int) {
	s.mu.Lock()
	s.width, s.height = width, height
	s.mu.Unlock()
	s.signal()
}

// signal wakes the draw loop; a wake already waiting is enough.
func (s *Shell) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// clean is session text a terminal can print: invalid UTF-8 is replaced and the
// control sequences go (termrender.Safe), tabs become spaces.
func clean(text string) string {
	text = termrender.Safe(strings.ToValidUTF8(text, "\uFFFD"))
	return strings.ReplaceAll(text, "\t", strings.Repeat(" ", tabWidth))
}

// oneLine is session text for a cell that is one row.
func oneLine(text string) string {
	return strings.ReplaceAll(clean(text), "\n", " ")
}

func (s *Shell) endPartialLocked() {
	if s.partial != "" {
		s.pushLocked(s.partial)
		s.partial = ""
	}
}

// appendLocked adds text to the transcript. While the person is scrolled back
// the view stays on the same rows: scrollBack grows by the rows the text adds
// below the view, counting the part of the line that was already drawn once.
func (s *Shell) appendLocked(text string) {
	before := s.partialRowsLocked()
	added := 0
	parts := strings.Split(clean(text), "\n")
	for i, part := range parts {
		s.partial += part
		if len(s.partial) > maxLineRunes {
			runes := []rune(s.partial)
			for len(runes) > maxLineRunes {
				added += s.pushLocked(string(runes[:maxLineRunes]))
				runes = runes[maxLineRunes:]
			}
			s.partial = string(runes)
		}
		if i < len(parts)-1 {
			added += s.pushLocked(s.partial)
			s.partial = ""
		}
	}
	added += s.partialRowsLocked()
	if s.scrollBack > 0 {
		s.scrollBack += added - before
	}
}

func (s *Shell) partialRowsLocked() int {
	if s.partial == "" {
		return 0
	}
	return len(wrapRows(s.partial, s.width))
}

// pushLocked adds a finished line and returns the rows it takes.
func (s *Shell) pushLocked(line string) int {
	s.lines = append(s.lines, line)
	if over := len(s.lines) - maxTranscriptLines; over > 0 {
		s.lines = s.lines[over:]
	}
	return len(wrapRows(line, s.width))
}

// tooSmallLocked reports a terminal below the size the screen is laid out for.
func (s *Shell) tooSmallLocked() bool { return s.height < MinHeight || s.width < MinWidth }

// handle applies one key and reports whether the person left the shell.
func (s *Shell) handle(k key) (quit bool) {
	s.mu.Lock()
	quit, after := s.handleLocked(k)
	s.mu.Unlock()
	s.signal()
	if after != nil {
		after()
	}
	return quit
}

func (s *Shell) handleLocked(k key) (quit bool, after func()) {
	switch {
	case k.kind == keyCtrlD:
		return true, nil
	case k.kind == keyNone:
		return false, nil
	case k.kind == keyPasteStart:
		s.pasting = true
		return false, nil
	case k.kind == keyPasteEnd:
		s.pasting = false
		return false, nil
	case s.tooSmallLocked():
		// The screen shows only the size notice: a key must not act on rows
		// the person cannot see.
		return false, nil
	case s.question != "":
		yes := s.onYes
		s.question, s.onYes = "", nil
		// Only a typed y confirms: a paste that starts with y must not.
		if !s.pasting && k.kind == keyRune && (k.rune == 'y' || k.rune == 'Y') {
			return false, yes
		}
		return false, nil
	case k.kind == keyCtrlC:
		// A paste whose end marker never came must not keep Enter from sending.
		s.pasting = false
		return false, s.Interrupt
	}
	if s.focus == focusTranscript || s.lockReason != "" {
		return s.handleTranscriptLocked(k), nil
	}
	return false, s.handleComposerLocked(k)
}

func (s *Shell) handleTranscriptLocked(k key) (quit bool) {
	switch k.kind {
	case keyUp:
		s.scrollLocked(1)
	case keyDown:
		s.scrollLocked(-1)
	case keyEnter, keyTab:
		if s.lockReason == "" {
			s.focus = focusComposer
		}
	case keyRune:
		if s.pasting {
			break // pasted text is never a command
		}
		switch k.rune {
		case 'k':
			s.scrollLocked(1)
		case 'j':
			s.scrollLocked(-1)
		case 'q':
			return true
		}
	}
	return false
}

func (s *Shell) handleComposerLocked(k key) (after func()) {
	switch k.kind {
	case keyEsc:
		s.pasting = false
		s.focus = focusTranscript
	case keyTab:
		if s.pasting {
			s.insertLocked([]rune(strings.Repeat(" ", tabWidth))...)
		} else {
			s.focus = focusTranscript
		}
	case keyEnter:
		if s.pasting {
			s.insertLocked('\n')
			return nil
		}
		return s.sendLocked()
	case keyAltEnter:
		s.insertLocked('\n')
	case keyBackspace:
		// ponytail: editing is append-only, so Backspace removes the last rune.
		// Add cursor movement when a message is long enough that people fix its middle.
		if n := len(s.composer); n > 0 {
			s.composer = s.composer[:n-1]
		}
	case keyUp:
		s.historyLocked(-1)
	case keyDown:
		s.historyLocked(1)
	case keyRune:
		if k.rune >= ' ' && (k.rune < 0x7f || k.rune > 0x9f) {
			s.insertLocked(k.rune)
		}
	}
	return nil
}

func (s *Shell) insertLocked(runes ...rune) {
	if room := maxComposerRunes - len(s.composer); room < len(runes) {
		runes = runes[:max(room, 0)]
	}
	s.composer = append(s.composer, runes...)
}

// sendLocked takes the composer's message. A blank one is dropped, not sent.
func (s *Shell) sendLocked() (after func()) {
	text := strings.TrimSpace(string(s.composer))
	s.composer, s.draft = nil, nil
	if text == "" {
		return nil
	}
	if n := len(s.history); n == 0 || s.history[n-1] != text {
		s.history = append(s.history, text)
		if len(s.history) > maxHistory {
			s.history = s.history[len(s.history)-maxHistory:]
		}
	}
	s.histPos = len(s.history)
	s.scrollBack = 0
	if s.Submit == nil {
		return nil
	}
	submit := s.Submit
	return func() { submit(text) }
}

// historyLocked walks the sent messages: -1 goes back, 1 forward. Going forward
// past the newest one restores the draft the person had typed.
func (s *Shell) historyLocked(delta int) {
	if s.histPos == len(s.history) {
		s.draft = append(s.draft[:0], s.composer...)
	}
	next := s.histPos + delta
	if next < 0 || next > len(s.history) {
		return
	}
	s.histPos = next
	if next == len(s.history) {
		s.composer = append([]rune(nil), s.draft...)
		return
	}
	s.composer = []rune(s.history[next])
}

func (s *Shell) scrollLocked(delta int) {
	s.scrollBack = max(0, min(s.scrollBack+delta, len(s.rowsLocked())-s.viewRows()))
}

// viewRows is how many transcript rows the screen has.
func (s *Shell) viewRows() int { return max(s.height-shellChrome, 0) }

// rowsLocked is the transcript as screen rows: every line wrapped to the width,
// then the line still being written.
func (s *Shell) rowsLocked() []string {
	rows := make([]string, 0, len(s.lines))
	for _, line := range s.lines {
		rows = append(rows, wrapRows(line, s.width)...)
	}
	if s.partial != "" {
		rows = append(rows, wrapRows(s.partial, s.width)...)
	}
	return rows
}

// render returns the screen, one string per row.
func (s *Shell) render() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, _, _ := s.renderLocked()
	return rows
}

// renderLocked also returns where the text cursor goes, or row -1 when the
// composer is not taking input.
func (s *Shell) renderLocked() (rows []string, cursorRow, cursorCol int) {
	height, width := s.height, s.width
	rows = make([]string, max(height, 0))
	cursorRow = -1
	if s.tooSmallLocked() {
		if height > 0 {
			rows[0] = clipCols(ErrTooSmall.Error()+". Ctrl-D leaves.", max(width, 0))
		}
		return rows, cursorRow, 0
	}
	rows[0] = headRow("  "+s.title, s.state, width)
	rows[1] = strings.Repeat("─", width)

	view := s.viewRows()
	all := s.rowsLocked()
	s.scrollBack = max(0, min(s.scrollBack, len(all)-view))
	end := len(all) - s.scrollBack
	shown := all[max(end-view, 0):end]
	for i, row := range shown {
		rows[2+view-len(shown)+i] = row
	}

	rows[height-6] = clipCols(s.noticeLocked(), width)
	rows[height-5] = strings.Repeat("─", width)
	composer, row, col := s.composerRowsLocked(width)
	for i, text := range composer {
		rows[height-4+i] = text
	}
	if row >= 0 {
		cursorRow, cursorCol = height-4+row, col
	}
	rows[height-1] = clipCols(s.hintsLocked(), width)
	return rows, cursorRow, cursorCol
}

func headRow(left, right string, width int) string {
	if gap := width - textCols(left) - textCols(right); right != "" && gap >= 1 {
		return left + strings.Repeat(" ", gap) + right
	}
	return clipCols(left, width)
}

func (s *Shell) noticeLocked() string {
	switch {
	case s.question != "":
		return "  " + s.question
	case s.lockReason != "":
		return "  " + s.lockReason
	}
	return ""
}

// composerRowsLocked draws the last composerRows rows of the message, and the
// cursor's place in them when the composer has the keys.
func (s *Shell) composerRowsLocked(width int) (rows []string, cursorRow, cursorCol int) {
	switch {
	case s.question != "":
		return []string{"  y confirms. Any other key cancels."}, -1, 0
	case s.lockReason != "":
		return []string{"  This session is read-only here."}, -1, 0
	case s.focus == focusTranscript:
		return []string{"  Press Enter to write a message."}, -1, 0
	}
	var visual []string
	for i, line := range strings.Split(string(s.composer), "\n") {
		for j, row := range wrapRows(line, width-2) {
			prefix := "  "
			if i == 0 && j == 0 {
				prefix = "> "
			}
			visual = append(visual, prefix+row)
		}
	}
	visual = visual[max(len(visual)-composerRows, 0):]
	last := len(visual) - 1
	return visual, last, textCols(visual[last])
}

func (s *Shell) hintsLocked() string {
	switch {
	case s.lockReason != "":
		return "↑↓ scroll · q leave"
	case s.focus == focusTranscript:
		return "↑↓ scroll · Enter write · q leave"
	}
	return "Enter send · Alt-Enter new line · Ctrl-C interrupt · /help · Ctrl-D leave"
}

// wrapRows cuts one transcript line into rows of at most width columns. It
// breaks at the last space that fits, keeps the line's indent on the rows it
// adds, and breaks a word longer than the row where it stands.
func wrapRows(line string, width int) []string {
	runes := []rune(line)
	if width < 1 || textCols(line) <= width {
		return []string{line}
	}
	indent := 0
	for indent < len(runes) && runes[indent] == ' ' {
		indent++
	}
	if indent > width/2 {
		indent = 0
	}
	pad := strings.Repeat(" ", indent)
	var rows []string
	start := 0
	for start < len(runes) {
		prefix, avail, floor := "", width, indent
		if len(rows) > 0 {
			prefix, avail, floor = pad, width-indent, 0
		}
		end, lastSpace, used := start, -1, 0
		for end < len(runes) {
			w := runeCols(runes[end])
			if used+w > avail {
				break
			}
			if runes[end] == ' ' && end > start+floor {
				lastSpace = end
			}
			used += w
			end++
		}
		if end == len(runes) {
			rows = append(rows, prefix+string(runes[start:end]))
			break
		}
		cut := end
		if runes[end] != ' ' && lastSpace > start {
			cut = lastSpace
		}
		cut = max(cut, start+1)
		rows = append(rows, prefix+strings.TrimRight(string(runes[start:cut]), " "))
		start = cut
		for start < len(runes) && runes[start] == ' ' {
			start++
		}
	}
	return rows
}

// textCols is how many terminal columns text takes.
func textCols(text string) int {
	n := 0
	for _, r := range text {
		n += runeCols(r)
	}
	return n
}

// clipCols cuts text to width columns.
func clipCols(text string, width int) string {
	used := 0
	for i, r := range text {
		w := runeCols(r)
		if used+w > width {
			return text[:i]
		}
		used += w
	}
	return text
}

// runeCols is the columns one rune takes: 2 for East Asian wide characters and
// emoji, 0 for combining marks and joiners, 1 otherwise. It is the common
// wcwidth table, enough to keep a row from wrapping on the terminal's side.
func runeCols(r rune) int {
	switch {
	case r >= 0x0300 && r <= 0x036f, r >= 0x200b && r <= 0x200f, r == 0xfe0f, r >= 0x20d0 && r <= 0x20ff:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe6f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// RunShell takes the terminal and runs the shell until the person leaves it. It
// returns 0 for Ctrl-D and q, after restoring the terminal on every path, and
// the same errors as Run before it changes the terminal mode.
func RunShell(in, out *os.File, sh *Shell) (int, error) {
	width, height, release, err := takeTerminal(in, out)
	if err != nil {
		return 0, err
	}
	defer release()
	sh.resize(width, height)
	// Bracketed paste: leaveScreen switches it off again.
	if _, err := io.WriteString(out, "\x1b[?2004h"); err != nil {
		return 0, fmt.Errorf("could not set up the terminal: %w", err)
	}

	type read struct {
		key key
		err error
	}
	keys := make(chan read)
	done := make(chan struct{})
	defer close(done)
	go func() {
		reader := bufio.NewReader(in)
		for {
			k, err := readKey(reader, int(in.Fd()))
			select {
			case keys <- read{k, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	resized, stopResize := resizeSignals()
	defer stopResize()

	for {
		if err := sh.draw(out); err != nil {
			return 0, err
		}
		select {
		case r := <-keys:
			if r.err != nil {
				if r.err == io.EOF {
					return 0, nil
				}
				return 0, fmt.Errorf("could not read a key from the terminal: %w", r.err)
			}
			if sh.handle(r.key) {
				return 0, nil
			}
		case <-sh.wake:
		case <-resized:
			if w, h, err := terminalSize(out, in); err == nil {
				sh.resize(w, h)
			}
		}
	}
}

// draw repaints the screen and puts the text cursor where typing lands.
func (s *Shell) draw(w io.Writer) error {
	s.mu.Lock()
	rows, cursorRow, cursorCol := s.renderLocked()
	height := s.height
	s.mu.Unlock()
	var b strings.Builder
	b.WriteString("\x1b[?25l")
	paintRows(&b, rows, height)
	if cursorRow >= 0 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", cursorRow+1, cursorCol+1)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("could not draw the session: %w", err)
	}
	return nil
}
