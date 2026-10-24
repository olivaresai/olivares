// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// Host is the stack of screens and the one way back into the loop from another
// goroutine. Everything that touches a screen runs on the loop: key handlers,
// openers, and the functions given to Post. A screen is never changed from a
// goroutine of its own.
type Host struct {
	width, height int
	renderer      *termrender.Renderer
	stack         []page
	posted        chan func()
	done          chan struct{}
	closeOnce     sync.Once
	// stopped is set and read on the loop only: a signal is ending the process,
	// so nothing is drawn over the terminal the signal's own exit restores.
	stopped bool
}

// page is one screen of the stack. render returns entries whose terminal rows
// add up to the height; leave stops whatever work the screen started.
type page interface {
	render() []string
	handle(k key) (quit bool, code int)
	leave()
}

const postedBacklog = 32

func newHost(width, height int, r *termrender.Renderer) *Host {
	return &Host{
		width: width, height: height, renderer: r,
		posted: make(chan func(), postedBacklog),
		done:   make(chan struct{}),
	}
}

// PushMenu opens a list screen on top of the stack.
func (h *Host) PushMenu(f Frame) *Menu {
	m := newModel(f, h.width, h.height, h.renderer)
	m.host = h
	h.stack = append(h.stack, m)
	return &Menu{m: m}
}

// Root is the first screen of the stack, the main menu, so a screen above it can
// keep the row that names the next step true. It is nil when that screen is not a list.
func (h *Host) Root() *Menu {
	if m, ok := h.stack[0].(*model); ok {
		return &Menu{m: m}
	}
	return nil
}

// PushPanel opens a panel on top of the stack.
func (h *Host) PushPanel(p *Panel) {
	p.host = h
	h.stack = append(h.stack, p)
}

// Pop removes the top screen without stopping its work: the caller has finished
// it. The first screen stays.
func (h *Host) Pop() {
	if len(h.stack) > 1 {
		h.stack = h.stack[:len(h.stack)-1]
	}
}

// Depth is the number of screens on the stack.
func (h *Host) Depth() int { return len(h.stack) }

// OnTop reports whether p is the screen the person is on. An answer that comes
// back from the engine asks this on the loop before it changes or closes p: the
// person may have left while it was on its way.
func (h *Host) OnTop(p *Panel) bool { return h.top() == page(p) }

// Holds reports whether m is still on the stack. An answer that comes back for a
// list asks this on the loop: a list the person left has no screen to show it on.
func (h *Host) Holds(m *Menu) bool { return slices.Contains(h.stack, page(m.m)) }

// Close removes p when it is the screen the person is on, and says whether it
// did. A panel the person already left is not there to close.
func (h *Host) Close(p *Panel) bool {
	if !h.OnTop(p) {
		return false
	}
	h.Pop()
	return true
}

// Post runs fn on the loop. It is how a goroutine that waited on the engine
// shows the answer. fn runs after the key being handled, never in the middle of
// a draw. After the frame has closed Post drops fn and returns.
func (h *Host) Post(fn func()) {
	select {
	case h.posted <- fn:
	case <-h.done:
	}
}

// close ends the host: nothing posted after this blocks.
func (h *Host) close() { h.closeOnce.Do(func() { close(h.done) }) }

func (h *Host) top() page { return h.stack[len(h.stack)-1] }

// leaveTop is the person leaving the top screen: its work stops, then it goes.
func (h *Host) leaveTop() {
	if len(h.stack) <= 1 {
		return
	}
	h.top().leave()
	h.Pop()
}

// handle gives the key to the top screen. On quit it stops the work of every
// open screen first, so a sign-in the engine started ends with the frame.
func (h *Host) handle(k key) (quit bool, code int) {
	quit, code = h.top().handle(k)
	if quit {
		h.leaveAll()
	}
	return quit, code
}

// endWork has the loop stop the work of every open screen, for a signal that
// ends the process: the loop owns the stack, so the signal goroutine asks it and
// waits, but not longer than wait, because a loop stuck on a hung terminal would
// otherwise keep the terminal raw.
func (h *Host) endWork(wait time.Duration) {
	done := make(chan struct{})
	go h.Post(func() {
		h.leaveAll()
		h.stopped = true
		close(done)
	})
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// leaveAll stops the work of every open screen, top first.
func (h *Host) leaveAll() {
	for i := len(h.stack) - 1; i >= 0; i-- {
		h.stack[i].leave()
	}
}

func (h *Host) render() []string { return h.top().render() }

// draw repaints the whole screen in place. The cursor returns to the top first,
// which is also what makes a redraw on every keypress cheap. An entry wider
// than the terminal (a sign-in link) is written whole and wraps by itself, so
// the row count follows the entries and not the line count.
func (h *Host) draw(w io.Writer) error {
	if h.stopped {
		return nil
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	used := 0
	for _, entry := range h.render() {
		rows := entryRows(entry, h.width)
		if used+rows > h.height {
			break
		}
		b.WriteString(entry)
		if rows == 1 {
			b.WriteString("\x1b[K")
		}
		used += rows
		if used < h.height {
			b.WriteString("\r\n")
		}
	}
	for ; used < h.height; used++ {
		b.WriteString("\x1b[K")
		if used < h.height-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("could not draw the menu: %w", err)
	}
	return nil
}

func headingRow(r *termrender.Renderer, width int, left, right string) string {
	left = "  " + left
	if right != "" {
		if gap := width - runeLen(left) - runeLen(right); gap >= 1 {
			left += strings.Repeat(" ", gap) + right
		}
	}
	return r.Paint(clip(left, width), termrender.RoleMuted)
}

func separatorRow(r *termrender.Renderer, width int) string {
	return r.Paint(strings.Repeat("─", width), termrender.RoleMuted)
}

// Menu is a list screen on the stack: the screen that opened it changes its
// rows when the engine has answered.
type Menu struct {
	m *model
}

// Frame is the heading, rows and footer on the screen now.
func (mn *Menu) Frame() Frame { return mn.m.frame }

// SetFrame replaces the heading, rows and footer. The cursor stays on its row
// number, and the search filter keeps applying.
func (mn *Menu) SetFrame(f Frame) {
	mn.m.frame = f
	mn.m.applyFilter(mn.m.query)
}

// Select puts the cursor on row i of the unfiltered rows. While a search applies,
// the cursor is the person's: the screen's answer does not undo it.
func (mn *Menu) Select(i int) {
	if mn.m.query != "" {
		return
	}
	mn.m.list.cursor = i
	mn.m.list.clamp()
}

// SetNotice shows text on the status line until the next key.
func (mn *Menu) SetNotice(text string) { mn.m.notice = text }
