// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package tui is the frame behind `olivares ui`: the terminal checks, raw mode
// and its restoration, key decoding, and the widgets every screen shares. A
// screen supplies a Frame; the package never chooses what the menu says.
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// The smallest terminal the menus are drawn for (CLI-CONCEPT §3).
const (
	MinWidth  = 80
	MinHeight = 24
)

// The alternate screen keeps the menu out of the operator's scrollback; the
// cursor hides while the frame owns the screen and comes back on every exit.
const (
	enterScreen = "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H"
	leaveScreen = "\x1b[?2004l\x1b[?25h\x1b[?1049l"
)

var (
	// ErrNotTerminal is the one sentence a pipe or a script gets instead of a
	// menu. It names the commands that do the same work without a terminal.
	ErrNotTerminal = errors.New("olivares ui needs a terminal; use olivares status, " +
		"olivares tool ls, or olivares session start <folder> instead")
	// ErrTooSmall is the exact sentence of CLI-CONCEPT §3.
	ErrTooSmall = errors.New("Make the terminal at least 80 x 24")
)

// Frame is one screen: a heading, a right-aligned label, the menu rows, a
// footer that reports state, and the key hints for the bottom row.
// AfterSignal runs when a signal ends the process, once the terminal is back, to
// say what the screens could not finish; a quit says it after Run returns.
type Frame struct {
	Heading     string
	Right       string
	Items       []MenuItem
	Footer      string
	FooterRole  termrender.Role
	Hints       string
	AfterSignal func()
}

// CheckTerminal reports ErrNotTerminal when either stream is not a terminal.
// Run calls it; a caller that must do work before drawing checks first, so a
// pipe is refused without doing that work.
func CheckTerminal(in, out *os.File) error {
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return ErrNotTerminal
	}
	return nil
}

// Run takes the terminal, draws frame, and returns the exit code the caller
// must use: 0 for q or Ctrl-D, 130 for Ctrl-C. It returns ErrNotTerminal or
// ErrTooSmall before touching the terminal mode, and restores the terminal on
// every exit path, including SIGINT, SIGTERM and SIGHUP.
func Run(in, out *os.File, frame Frame) (int, error) {
	width, height, restore, release, err := takeTerminalRestorer(in, out)
	if err != nil {
		return 0, err
	}
	defer release()
	if frame.Hints == "" {
		frame.Hints = "↑↓ move  Enter show  / search  ? help  q quit"
	}
	// Bracketed paste: a newline inside a paste reaches the screens as text, not
	// as an Enter. leaveScreen switches it off again.
	if _, err := io.WriteString(out, "\x1b[?2004h"); err != nil {
		return 0, fmt.Errorf("could not set up the terminal: %w", err)
	}
	h := newHost(width, height, termrender.New(out, termrender.Options{Width: width}))
	// A signal ends the work the screens started (a sign-in the engine is
	// waiting on) before the process exits, as a quit does.
	restore.setExitHooks(func() { h.endWork(signalWorkWait) }, frame.AfterSignal)
	defer h.close()
	// Whatever way Run returns, the work the screens started ends with it.
	defer h.leaveAll()
	h.PushMenu(frame)
	// Keys are read on their own goroutine, so work that finishes while the
	// person is thinking (an install, a sign-in) can redraw the screen. The
	// reader ends with the process: it blocks in a read that only a key or the
	// end of the terminal wakes.
	keys := make(chan keyEvent)
	go readKeys(bufio.NewReader(in), int(in.Fd()), keys, h.done)
	for {
		if err := h.draw(out); err != nil {
			return 0, err
		}
		select {
		case fn := <-h.posted:
			fn()
		case ev := <-keys:
			if ev.err != nil {
				h.leaveAll()
				if errors.Is(ev.err, io.EOF) {
					return 0, nil
				}
				return 0, fmt.Errorf("could not read a key from the terminal: %w", ev.err)
			}
			if quit, code := h.handle(ev.key); quit {
				return code, nil
			}
		}
	}
}

type keyEvent struct {
	key key
	err error
}

// readKeys decodes keys until the terminal ends or the frame closes.
func readKeys(r *bufio.Reader, fd int, out chan<- keyEvent, done <-chan struct{}) {
	for {
		k, err := readKey(r, fd)
		select {
		case out <- keyEvent{key: k, err: err}:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

// takeTerminal is the start every screen shares: the terminal and size checks
// before the mode changes, raw mode, the alternate screen, and the restoration
// of all three on release and on SIGINT, SIGTERM and SIGHUP.
func takeTerminal(in, out *os.File) (width, height int, release func(), err error) {
	width, height, _, release, err = takeTerminalRestorer(in, out)
	return width, height, release, err
}

// takeTerminalRestorer is takeTerminal that also returns the restorer, for a
// screen that has work to end when a signal ends the process.
func takeTerminalRestorer(in, out *os.File) (width, height int, restore *restorer, release func(), err error) {
	if err := CheckTerminal(in, out); err != nil {
		return 0, 0, nil, nil, err
	}
	width, height, err = frameSize(in, out)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	// Register for the signals before raw mode: a signal that lands while the
	// terminal is being taken over is then queued, and the watcher restores
	// the returned state on the way out instead of dying with the tty raw.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		signal.Stop(sig)
		return 0, 0, nil, nil, fmt.Errorf("could not put the terminal in raw mode: %w", err)
	}
	restore = newRestorer(in, out, state)
	restore.watchSignals(sig)
	release = func() {
		restore.restore()
		signal.Stop(sig)
	}
	if _, err := io.WriteString(out, enterScreen); err != nil {
		release()
		return 0, 0, nil, nil, fmt.Errorf("could not open the screen on the terminal: %w", err)
	}
	return width, height, restore, release, nil
}

// CheckSize reports ErrTooSmall below the frame's minimum without changing the
// terminal mode, so a caller can refuse a small terminal before doing work
// whose result the frame could not show. Run checks again before drawing.
func CheckSize(in, out *os.File) error {
	_, _, err := frameSize(in, out)
	return err
}

// frameSize is the check Run and CheckSize share.
func frameSize(in, out *os.File) (width, height int, err error) {
	width, height, err = terminalSize(out, in)
	if err != nil {
		return 0, 0, err
	}
	if width < MinWidth || height < MinHeight {
		return 0, 0, ErrTooSmall
	}
	return width, height, nil
}

// terminalSize asks the output stream first, then the input stream: a terminal
// multiplexer sometimes reports the window on only one of them. A size of zero
// is a size, not an error, and is refused by frameSize as below 80 x 24.
func terminalSize(out, in *os.File) (width, height int, err error) {
	if w, h, sizeErr := term.GetSize(int(out.Fd())); sizeErr == nil {
		return w, h, nil
	}
	if w, h, sizeErr := term.GetSize(int(in.Fd())); sizeErr == nil {
		return w, h, nil
	}
	return 0, 0, errors.New("could not read the terminal size")
}

// restorer puts the terminal back exactly once, whichever path leaves the
// frame, and tells the signal watcher to stop.
type restorer struct {
	in    *os.File
	out   *os.File
	state *term.State
	once  sync.Once
	done  chan struct{}
	// beforeExit runs when a signal ends the process, before the terminal is
	// restored, and afterExit once it is restored (what it writes is left on the
	// person's own screen). The watcher reads them on its own goroutine: mu
	// guards them.
	mu                    sync.Mutex
	beforeExit, afterExit func()
}

// setExitHooks sets what runs before and after the terminal is restored when a
// signal ends the process. Either may be nil.
func (r *restorer) setExitHooks(before, after func()) {
	r.mu.Lock()
	r.beforeExit, r.afterExit = before, after
	r.mu.Unlock()
}

// signalWorkWait is how long a signal waits for the screens to end their work.
const signalWorkWait = 6 * time.Second

func newRestorer(in, out *os.File, state *term.State) *restorer {
	return &restorer{in: in, out: out, state: state, done: make(chan struct{})}
}

func (r *restorer) restore() {
	r.once.Do(func() {
		_ = term.Restore(int(r.in.Fd()), r.state)
		_, _ = io.WriteString(r.out, leaveScreen)
		close(r.done)
	})
}

// watchSignals restores the terminal before exiting on a signal. os.Exit skips
// defers, so the restore happens here and not in Run's stack. The channel is
// registered before raw mode, so this goroutine starts with the state to put
// back already known.
func (r *restorer) watchSignals(sig <-chan os.Signal) {
	go func() {
		select {
		case got := <-sig:
			r.mu.Lock()
			before, after := r.beforeExit, r.afterExit
			r.mu.Unlock()
			if before != nil {
				before()
			}
			r.restore()
			if after != nil {
				after()
			}
			os.Exit(signalExitCode(got))
		case <-r.done:
		}
	}()
}

func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

type mode int

const (
	modeMenu mode = iota
	modeSearch
	modeHelp
)

type model struct {
	host     *Host
	frame    Frame
	width    int
	height   int
	renderer *termrender.Renderer
	list     *List
	form     *Form
	pane     *Pane
	mode     mode
	query    string
	notice   string
}

func newModel(frame Frame, width, height int, r *termrender.Renderer) *model {
	m := &model{
		frame:    frame,
		width:    width,
		height:   height,
		renderer: r,
		list:     &List{},
		form:     &Form{},
		pane:     &Pane{},
	}
	m.list.SetItems(frame.Items)
	m.pane.SetLines(helpLines)
	return m
}

// maxSearchLen bounds the search field: a pasted blob cannot grow the value
// and make every following keystroke re-filter an unbounded string.
const maxSearchLen = 128

var helpLines = []string{
	"Keys on this screen",
	"",
	"  ↑ ↓ or j k    move in the list",
	"  Enter         show the command that does it",
	"  /             search the list",
	"  ?             open this help",
	"  Esc           close this help or cancel a search",
	"  q             close this help",
	"  Ctrl-D        quit",
	"  Ctrl-C        quit",
}

func (m *model) handle(k key) (quit bool, code int) {
	// Ctrl-C and Ctrl-D quit from every mode; a q is text in the search field
	// and closes this help, so only the menu treats it as quit.
	switch k.kind {
	case keyCtrlC:
		return true, 130
	case keyCtrlD:
		return true, 0
	}
	switch m.mode {
	case modeSearch:
		m.handleSearch(k)
	case modeHelp:
		m.handleHelp(k)
	default:
		return m.handleMenu(k)
	}
	return false, 0
}

func (m *model) handleMenu(k key) (quit bool, code int) {
	switch k.kind {
	case keyUp:
		m.list.Move(-1)
		m.notice = ""
	case keyDown:
		m.list.Move(1)
		m.notice = ""
	case keyEnter:
		if item, ok := m.list.Selected(); ok {
			m.notice = "Use: " + item.Command
			if item.Open != nil && m.host != nil {
				m.notice = item.Open(m.host)
			}
		}
	case keyEsc:
		m.notice = ""
		m.goBack()
	case keyRune:
		switch k.rune {
		case 'k':
			m.list.Move(-1)
			m.notice = ""
		case 'j':
			m.list.Move(1)
			m.notice = ""
		case 'q':
			if m.goBack() {
				return false, 0
			}
			return true, 0
		case '/':
			m.notice = ""
			m.form.SetFields([]Field{{Label: "Search", Value: m.query}})
			m.mode = modeSearch
		case '?':
			m.mode = modeHelp
		}
	}
	return false, 0
}

// goBack leaves a sub-menu for the screen below it. The first screen has no
// "below": there Esc only clears the notice and q quits.
func (m *model) goBack() bool {
	if m.host == nil || m.host.Depth() < 2 {
		return false
	}
	m.host.leaveTop()
	return true
}

// leave has nothing to stop: a list starts no work of its own.
func (m *model) leave() {}

func (m *model) handleSearch(k key) {
	switch k.kind {
	case keyEsc:
		m.form.SetValue("")
		m.applyFilter("")
		m.mode = modeMenu
	case keyEnter:
		m.applyFilter(m.form.Value())
		m.mode = modeMenu
	case keyBackspace:
		m.form.Backspace()
		m.applyFilter(m.form.Value())
	case keyUp:
		m.list.Move(-1)
	case keyDown:
		m.list.Move(1)
	case keyRune:
		if isText(k.rune) && runeLen(m.form.Value()) < maxSearchLen {
			m.form.Insert(k.rune)
			m.applyFilter(m.form.Value())
		}
	}
}

func (m *model) handleHelp(k key) {
	switch k.kind {
	case keyEsc:
		m.mode = modeMenu
	case keyUp:
		m.pane.Scroll(-1, m.height-3)
	case keyDown:
		m.pane.Scroll(1, m.height-3)
	case keyRune:
		switch k.rune {
		case 'q', '?':
			m.mode = modeMenu
		case 'k':
			m.pane.Scroll(-1, m.height-3)
		case 'j':
			m.pane.Scroll(1, m.height-3)
		}
	}
}

// applyFilter keeps the rows whose label, detail or command contains query,
// ignoring case. An empty query is the whole menu.
func (m *model) applyFilter(query string) {
	m.query = strings.TrimSpace(query)
	if m.query == "" {
		m.list.SetItems(m.frame.Items)
		return
	}
	needle := strings.ToLower(m.query)
	rows := make([]MenuItem, 0, len(m.frame.Items))
	for _, item := range m.frame.Items {
		haystack := strings.ToLower(item.Label + " " + item.Detail + " " + item.Command)
		if strings.Contains(haystack, needle) {
			rows = append(rows, item)
		}
	}
	m.list.SetItems(rows)
}

// paintRows writes height rows from the top of the screen, each cleared to the
// end of its line, and clears whatever is left below.
func paintRows(b *strings.Builder, lines []string, height int) {
	b.WriteString("\x1b[H")
	for i := 0; i < height; i++ {
		if i < len(lines) {
			b.WriteString(lines[i])
		}
		b.WriteString("\x1b[K")
		if i < height-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J")
}

func (m *model) render() []string {
	lines := make([]string, m.height)
	if m.height < 4 || m.width < 1 {
		return lines
	}
	if m.mode == modeHelp {
		lines[0] = m.heading("Help")
		lines[1] = m.separator()
		for i, row := range m.pane.Visible(m.height - 3) {
			lines[2+i] = clip(row, m.width)
		}
		lines[m.height-1] = m.statusLine()
		return lines
	}
	lines[0] = m.heading(m.frame.Heading)
	lines[1] = m.separator()
	start, rows := m.list.Window(m.height - 6)
	labelWidth := m.labelWidth()
	for i, item := range rows {
		lines[2+i] = m.item(item, start+i == m.list.Cursor(), labelWidth)
	}
	lines[m.height-4] = m.commandRow()
	lines[m.height-3] = m.renderer.Paint(clip("  "+m.frame.Footer, m.width), m.frame.FooterRole)
	lines[m.height-2] = m.separator()
	lines[m.height-1] = m.statusLine()
	return lines
}

func (m *model) heading(left string) string {
	return headingRow(m.renderer, m.width, left, m.frame.Right)
}

// commandRow is the row above the footer: the command that does the selected
// row's action, so it shows on every selection, before Enter. Enter swaps it for
// the notice. It has a row of its own because a command is not runnable once
// cut, and the key hints on the bottom line must never lose room to it.
func (m *model) commandRow() string {
	text := m.notice
	if item, ok := m.list.Selected(); ok && text == "" {
		text = "= " + item.Command
	}
	return m.renderer.Paint(clip("  "+text, m.width), termrender.RoleMuted)
}

func (m *model) separator() string { return separatorRow(m.renderer, m.width) }

func (m *model) item(item MenuItem, selected bool, labelWidth int) string {
	marker := "    "
	if selected {
		marker = "  ▸ "
	}
	row := strings.TrimRight(clip(marker+padRight(item.Label, labelWidth)+item.Detail, m.width), " ")
	if selected {
		return m.renderer.Paint(row, termrender.RoleOK)
	}
	return row
}

func (m *model) labelWidth() int {
	width := 0
	for _, item := range m.frame.Items {
		if n := runeLen(item.Label); n > width {
			width = n
		}
	}
	width += 2
	if width > 24 {
		width = 24
	}
	return width
}

func (m *model) statusLine() string {
	var line StatusLine
	switch m.mode {
	case modeSearch:
		left := "Search: " + m.form.Value()
		if m.list.Len() == 0 {
			left += "  no matches"
		}
		line = StatusLine{Left: left, Right: "Enter apply  Esc cancel"}
	case modeHelp:
		line = StatusLine{Left: "↑↓ scroll", Right: "Esc close"}
	default:
		line = StatusLine{Left: m.frame.Hints}
		if m.query != "" {
			line.Right = "filter: " + m.query
		}
	}
	return line.Render(m.width)
}
