// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// terminal.go is the shell a person opens in a registered folder from the console.
//
// It is a session's launch with the person at the keyboard instead of a tool: the same
// folder policy (sessionConfinement), the same confinement helper and session guard, the
// same private TMPDIR and HOME, so the shell reaches what a session in the folder reaches
// and nothing more. Its network is the host's: a shell with no network cannot clone, push
// or install, and no provider is bound to it to limit it to (sessionNetworkPolicy).
//
// The output is a byte stream that keeps its tail, so a browser that reconnects within
// terminalReconnectWindow finds the same shell and reads from where it stopped. A shell
// nobody is attached to for that long, or that has seen no input and no output for
// terminalIdleTimeout, ends with everything it started.
//
// Every start, every command line and every close is sealed in the audit ledger. A line is
// sealed BEFORE the shell receives its newline (deny-closed: no evidence, no command), and
// a line typed at a hidden prompt (a password) is withheld and said to be withheld.
const (
	// terminalMaxPerPerson bounds the shells one person keeps open at once.
	terminalMaxPerPerson = 4
	// terminalReplayBytes is the output tail kept for a reconnect.
	terminalReplayBytes = 256 << 10
	// terminalChunkBytes is the most one read returns.
	terminalChunkBytes = 16 << 10
	// terminalMaxInputBytes bounds one input request (a large paste).
	terminalMaxInputBytes = 64 << 10
	// terminalMaxLineBytes bounds the excerpt; the payload hash seals the full line.
	terminalMaxLineBytes = 4 << 10
	// terminalDrainWait is how long the output pump may outlive the shell.
	terminalDrainWait = 2 * time.Second
	// terminalMaxRows and terminalMaxCols bound the window a client may ask for.
	terminalMaxRows, terminalMaxCols = 500, 1000
)

// ponytail: the limits below are fixed, not settings; upgrade trigger: an administrator
// who needs a different idle time or reconnect window than 15 minutes and 60 seconds.
var (
	terminalIdleTimeout     = 15 * time.Minute
	terminalReconnectWindow = 60 * time.Second
)

// terminalWho is the signed-in person a terminal belongs to.
type terminalWho struct{ Actor, Kind string }

// terminalHub holds the open terminals of the module.
type terminalHub struct {
	mu      sync.Mutex
	byID    map[string]*terminal
	stopped bool
}

// terminal is one shell on one pty.
type terminal struct {
	m         *Module
	id        string
	tenant    model.TenantID
	folderID  model.ID
	folderRef string
	who       terminalWho

	confinement confine.State
	master      *os.File
	cmd         *exec.Cmd
	cancel      context.CancelFunc
	release     func()
	doneCh      chan struct{}
	sealed      bool // the start is in the ledger, so the close is too

	sendMu sync.Mutex // one input at a time, in order
	line   lineAssembler

	mu       sync.Mutex
	buf      []byte // the output tail
	base     uint64 // the offset of buf[0] in the whole output
	changed  chan struct{}
	exited   bool
	exitCode int
	reason   string
	attached int
	detach   *time.Timer
	idle     *time.Timer
}

// terminalChunk is output from Offset on. Offset can be later than asked: the tail
// kept for a reconnect is bounded.
type terminalChunk struct {
	Offset   uint64
	Data     []byte
	Exited   bool
	ExitCode int
}

func (h *terminalHub) get(id string) *terminal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byID[id]
}

func (h *terminalHub) remove(t *terminal) {
	h.mu.Lock()
	delete(h.byID, t.id)
	h.mu.Unlock()
}

// add registers t, or refuses it when the module is stopping or the person has too many.
func (h *terminalHub) add(t *terminal) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return &runErr{http.StatusServiceUnavailable, "the server is stopping; the terminal was not started"}
	}
	open := 0
	for _, o := range h.byID {
		if o.tenant == t.tenant && o.who == t.who {
			open++
		}
	}
	if open >= terminalMaxPerPerson {
		return &runErr{http.StatusTooManyRequests, "you have " + strconv.Itoa(terminalMaxPerPerson) + " terminals open; close one first"}
	}
	if h.byID == nil {
		h.byID = map[string]*terminal{}
	}
	h.byID[t.id] = t
	return nil
}

// closeAll ends every terminal and refuses new ones (module Stop).
func (h *terminalHub) closeAll(reason string) {
	h.mu.Lock()
	h.stopped = true
	all := make([]*terminal, 0, len(h.byID))
	for _, t := range h.byID {
		all = append(all, t)
	}
	h.mu.Unlock()
	for _, t := range all {
		t.close(reason)
	}
	for _, t := range all {
		<-t.doneCh
	}
}

// closeFolder ends the terminals open in one folder (the folder was deregistered): a
// shell must not outlive the registration that governs what it may reach.
func (h *terminalHub) closeFolder(tenant model.TenantID, ref, reason string) {
	h.mu.Lock()
	var open []*terminal
	for _, t := range h.byID {
		if t.tenant == tenant && t.folderRef == ref {
			open = append(open, t)
		}
	}
	h.mu.Unlock()
	for _, t := range open {
		t.close(reason)
	}
}

// reopen lets a module that was stopped start terminals again.
func (h *terminalHub) reopen() {
	h.mu.Lock()
	h.stopped = false
	h.mu.Unlock()
}

// startTerminal opens a shell in the folder ref. The caller has already checked the
// person's rights on the folder and the deployment's step-up policy.
func (m *Module) startTerminal(ctx context.Context, tenant model.TenantID, ref string, who terminalWho, rows, cols uint16) (*terminal, error) {
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, ref)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, badRequest("a terminal needs a folder")
	}
	spec := m.terminalSpec(ws)
	pr := &procRunner{lineCap: maxOutputLine}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd, env, state, release, err := pr.command(runCtx, spec)
	if err != nil {
		cancel()
		ws.closeReadOnlyHandles()
		return nil, err
	}
	cmd.Dir = spec.Dir
	cmd.Env = sanitizedEnv(spec.EnvAllow, env)
	id, err := newTerminalID()
	if err != nil {
		cancel()
		release()
		ws.closeReadOnlyHandles()
		return nil, err
	}
	master, err := startTerminalShell(cmd)
	if err != nil {
		cancel()
		release()
		ws.closeReadOnlyHandles()
		return nil, &runErr{http.StatusBadGateway, "the terminal could not start: " + redactErr(err)}
	}
	t := &terminal{
		m: m, id: id, tenant: tenant, folderID: ws.id, folderRef: ref, who: who,
		confinement: state, master: master, cmd: cmd, cancel: cancel,
		release: func() { release(); ws.closeReadOnlyHandles() },
		doneCh:  make(chan struct{}), changed: make(chan struct{}),
	}
	if err := m.terminals.add(t); err != nil {
		t.discard()
		return nil, err
	}
	rows, cols = clampTerminalSize(rows, cols)
	_ = setTerminalSize(master, rows, cols)
	if err := m.sealTerminal(ctx, t, "start", terminalLineInput{}, map[string]any{"rows": rows, "cols": cols, "confinement": string(state.Mode)}); err != nil {
		m.terminals.remove(t)
		t.discard()
		return nil, &runErr{http.StatusServiceUnavailable, "the terminal was not started because the audit ledger could not record it"}
	}
	t.sealed = true
	t.mu.Lock()
	t.idle = time.AfterFunc(terminalIdleTimeout, func() { t.close("idle") })
	t.detach = time.AfterFunc(terminalReconnectWindow, func() { t.close("detached") })
	t.mu.Unlock()
	go t.run()
	return t, nil
}

// terminalSpec is the launch of the shell: the folder's confinement as a session in it
// gets it, with the person's shell where a tool would be.
func (m *Module) terminalSpec(ws *resolvedWorkspace) LaunchSpec {
	// A folder mounted read-only is read-only to the shell too, as for a read-only session.
	preset := PresetNone
	if ws.mountMode == mountRO {
		preset = PresetReadOnly
	}
	policy := m.sessionConfinement(ws.rootReal, nil, preset)
	var noDelete []string
	if policy != nil && ws.mountMode == mountNoDelete && preset != PresetReadOnly {
		noDelete = withoutDelete(policy, ws.rootReal)
	}
	// The Git hook and configuration mounts that keep an agent from planting what the
	// person's next git command would run are not applied: the person is typing here.
	return LaunchSpec{
		Program: terminalProgram(), Dir: ws.rootReal,
		Env: []EnvVar{
			{Name: "TERM", Value: "xterm-256color"},
			{Name: "COLORTERM", Value: "truecolor"},
		},
		WaitDelay: m.rt.waitDelay, KillTreeOnExit: true,
		Confinement: policy, ConfinementFiles: ws.readOnlyHandles, ConfinementNoDelete: noDelete,
		// A shell that cannot be confined does not start, whatever the node's own setting.
		ConfinementRequired: true,
	}
}

func terminalProgram() string {
	for _, shell := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if info, err := os.Stat(shell); err == nil && !info.IsDir() {
			return shell
		}
	}
	return "/bin/sh"
}

func newTerminalID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("sessions: terminal id: " + err.Error())
	}
	return hex.EncodeToString(b[:]), nil
}

func clampTerminalSize(rows, cols uint16) (uint16, uint16) {
	clamp := func(v, def, max uint16) uint16 {
		if v == 0 {
			return def
		}
		return min(v, max)
	}
	return clamp(rows, 24, terminalMaxRows), clamp(cols, 80, terminalMaxCols)
}

// terminalOf is the terminal id of this person in this folder, or not found: another
// person's terminal, another folder's and an ended one answer the same.
func (m *Module) terminalOf(tenant model.TenantID, ref, id string, who terminalWho) (*terminal, error) {
	t := m.terminals.get(id)
	if t == nil || t.tenant != tenant || t.folderRef != ref || t.who != who {
		return nil, &runErr{http.StatusNotFound, "not found"}
	}
	select {
	case <-t.doneCh:
		return nil, &runErr{http.StatusNotFound, "not found"}
	default:
	}
	return t, nil
}

// run pumps the shell's output until it ends, then settles the close.
func (t *terminal) run() {
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		buf := make([]byte, terminalChunkBytes)
		for {
			n, err := t.master.Read(buf)
			if n > 0 {
				t.append(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := t.cmd.Wait()
	code := 0
	if waitErr != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code = ee.ExitCode()
		}
	}
	select {
	case <-pumped:
	case <-time.After(terminalDrainWait):
	}
	_ = t.master.Close()
	t.release()
	t.mu.Lock()
	t.exited, t.exitCode = true, code
	if t.reason == "" {
		t.reason = "exit"
	}
	reason := t.reason
	t.stopTimers()
	t.wake()
	t.mu.Unlock()
	t.cancel()
	t.m.terminals.remove(t)
	if t.sealed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := t.m.sealTerminal(ctx, t, "close", terminalLineInput{}, map[string]any{"reason": reason, "exit_code": code}); err != nil {
			t.m.warnf("sessions: a terminal close could not be recorded", "terminal", t.id, "err", redactErr(err))
		}
	}
	close(t.doneCh)
}

// discard ends a shell that never became a terminal (start refused after the spawn). It
// closes done too: a module that is stopping may already be waiting for this terminal.
func (t *terminal) discard() {
	t.cancel()
	_ = t.cmd.Wait()
	_ = t.master.Close()
	t.release()
	close(t.doneCh)
}

func (t *terminal) append(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - terminalReplayBytes; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.base += uint64(over)
	}
	t.touchLocked()
	t.wake()
}

// wake releases every reader waiting for output. t.mu is held.
func (t *terminal) wake() {
	close(t.changed)
	t.changed = make(chan struct{})
}

// read returns the output from offset from on, waiting for some. An offset before the
// kept tail starts at the tail; the chunk's Offset says where. After the shell has
// ended and everything was read, the chunk is empty with Exited set.
func (t *terminal) read(ctx context.Context, from uint64) (terminalChunk, error) {
	for {
		t.mu.Lock()
		end := t.base + uint64(len(t.buf))
		if from < t.base {
			from = t.base
		}
		if from < end {
			n := min(int(end-from), terminalChunkBytes)
			start := int(from - t.base)
			chunk := terminalChunk{Offset: from, Data: bytes.Clone(t.buf[start : start+n])}
			t.mu.Unlock()
			return chunk, nil
		}
		if t.exited {
			chunk := terminalChunk{Offset: end, Exited: true, ExitCode: t.exitCode}
			t.mu.Unlock()
			return chunk, nil
		}
		wait := t.changed
		t.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return terminalChunk{}, ctx.Err()
		}
	}
}

// attach says a browser is reading the terminal; the returned func says it stopped.
// While nobody is attached the reconnect window runs.
func (t *terminal) attach() func() {
	t.mu.Lock()
	t.attached++
	if t.detach != nil {
		t.detach.Stop()
	}
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.attached--
			if t.attached == 0 && !t.exited && t.detach != nil {
				t.detach.Reset(terminalReconnectWindow)
			}
		})
	}
}

// touchLocked restarts the idle limit. t.mu is held.
func (t *terminal) touchLocked() {
	if t.idle != nil && !t.exited {
		t.idle.Reset(terminalIdleTimeout)
	}
}

// stopTimers stops both limits. t.mu is held.
func (t *terminal) stopTimers() {
	if t.idle != nil {
		t.idle.Stop()
	}
	if t.detach != nil {
		t.detach.Stop()
	}
}

// close ends the shell and everything it started. The first reason wins; the close is
// complete when done() is closed.
func (t *terminal) close(reason string) {
	t.mu.Lock()
	if t.reason == "" {
		t.reason = reason
	}
	t.mu.Unlock()
	t.cancel()
}

func (t *terminal) done() <-chan struct{} { return t.doneCh }

func (t *terminal) closeReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

func (t *terminal) inputHidden() bool { return terminalInputHidden(t.master) }

// resize sets the window size the shell sees.
func (t *terminal) resize(rows, cols uint16) error {
	rows, cols = clampTerminalSize(rows, cols)
	t.mu.Lock()
	t.touchLocked()
	t.mu.Unlock()
	return setTerminalSize(t.master, rows, cols)
}

// send types data into the shell. Each line is sealed in the ledger before its newline
// reaches the shell; when the ledger cannot record it the shell is closed before
// receiving the accept key, so unsealed text cannot run later.
func (t *terminal) send(ctx context.Context, data string) error {
	if len(data) > terminalMaxInputBytes {
		return &runErr{http.StatusRequestEntityTooLarge, "that input is too large for one request"}
	}
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	t.mu.Lock()
	gone := t.exited || t.reason != ""
	t.touchLocked()
	t.mu.Unlock()
	if gone {
		return &runErr{http.StatusGone, "the terminal has ended"}
	}
	start := 0
	flush := func(end int) error {
		if end > start {
			if _, err := io.WriteString(t.master, data[start:end]); err != nil {
				return &runErr{http.StatusGone, "the terminal has ended"}
			}
		}
		start = end
		return nil
	}
	for i, r := range data {
		if !t.line.feed(r) {
			continue
		}
		if err := flush(i); err != nil {
			return err
		}
		in, ok := t.line.commit(t.inputHidden())
		if ok {
			if err := t.m.sealTerminal(ctx, t, "command", in, nil); err != nil {
				// Editor bindings are controlled by the shell. End its lifetime rather
				// than trust an erase sequence to discard all unsealed input.
				t.close("audit_unavailable")
				return &runErr{http.StatusServiceUnavailable, "the terminal was closed because the audit ledger could not record the command"}
			}
		}
	}
	return flush(len(data))
}

// lineAssembler follows what is typed to know what the shell will be asked to run: it
// applies the editing keys a person uses on one line and notes the ones that change the
// line behind its back (history, completion), which it cannot follow.
//
// ponytail: this records the line as typed, not the process the shell then starts; a
// history recall or a tab completion is marked edited, not expanded. Upgrade trigger: a
// customer who needs the executed command, which takes the shell's own hook
// (PROMPT_COMMAND) or the kernel's exec audit.
type lineAssembler struct {
	line   []rune
	edited bool
	esc    int // 0 none, 1 after ESC, 2 inside a control sequence
}

// feed applies one typed character. It reports true for a line-accept key, the
// moment to commit the line.
func (a *lineAssembler) feed(r rune) bool {
	// Accept keys still accept a line in vi mode or canonical input even
	// after ESC. Never let an incomplete escape sequence swallow one.
	if r == '\r' || r == '\n' || r == 0x0f || (r == 0x04 && (len(a.line) > 0 || a.edited)) {
		a.esc = 0
		return true
	}
	switch a.esc {
	case 1:
		if r == '[' || r == 'O' {
			a.esc = 2
		} else {
			a.esc = 0
		}
		return false
	case 2:
		if r >= 0x40 && r <= 0x7e {
			a.esc = 0
		}
		return false
	}
	switch r {
	case 0x1b:
		a.esc, a.edited = 1, true
	case 0x7f, 0x08:
		if n := len(a.line); n > 0 {
			a.line = a.line[:n-1]
		}
	case 0x15, 0x03: // ^U, ^C
		a.reset()
	case 0x17: // ^W
		for len(a.line) > 0 && a.line[len(a.line)-1] == ' ' {
			a.line = a.line[:len(a.line)-1]
		}
		for len(a.line) > 0 && a.line[len(a.line)-1] != ' ' {
			a.line = a.line[:len(a.line)-1]
		}
	case '\t':
		a.edited = true
	default:
		if r >= 0x20 || r == utf8.RuneError {
			a.line = append(a.line, r)
		} else {
			a.edited = true // another control key: ^R, ^P, ^A ...
		}
	}
	return false
}

func (a *lineAssembler) reset() { *a = lineAssembler{} }

// commit settles the line at a newline and starts the next one. ok is false for an
// empty line nobody edited: pressing Enter alone runs nothing.
func (a *lineAssembler) commit(hidden bool) (in terminalLineInput, ok bool) {
	in = terminalLineInput{hidden: hidden, edited: a.edited}
	if !hidden {
		in.line = string(a.line)
	}
	ok = hidden || len(a.line) > 0 || a.edited
	a.reset()
	return in, ok
}

// terminalLineInput is one ledger entry of a terminal: a typed line, or the fact that a
// hidden entry was typed.
type terminalLineInput struct {
	ref, terminal string
	line          string
	hidden        bool
	edited        bool
}

// terminalPayloadHash commits an entry to its terminal, its folder and its line.
func terminalPayloadHash(in terminalLineInput) [32]byte {
	h := sha256.New()
	flags := ""
	if in.hidden {
		flags += "h"
	}
	if in.edited {
		flags += "e"
	}
	for _, part := range []string{in.ref, in.terminal, in.line, flags} {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// sealTerminal appends one terminal event to the audit ledger in its own transaction.
//
// ponytail: a command line is recorded exactly as typed, because the record is the point;
// a secret written into a command (export TOKEN=...) is in the ledger, which only people
// who may read the audit read. A prompt that hides what is typed is withheld instead.
// Upgrade trigger: a secret-pattern scrubber shared by every audit writer.
func (m *Module) sealTerminal(ctx context.Context, t *terminal, op string, in terminalLineInput, extra map[string]any) error {
	if m.Data == nil {
		return errors.New("sessions: no data handle for the audit ledger")
	}
	in.ref, in.terminal = t.folderRef, t.id
	hash := terminalPayloadHash(in)
	meta := map[string]any{"workspace_ref": t.folderRef, "terminal": t.id, "op": op}
	if op == "command" {
		meta["hidden"] = in.hidden
		meta["edited"] = in.edited
		if !in.hidden {
			// Bound the displayed excerpt, never the payload commitment above.
			meta["line"] = in.line
			if len(in.line) > terminalMaxLineBytes {
				meta["line"] = string([]rune(in.line[:terminalMaxLineBytes]))
				meta["truncated"] = true
			}
		}
	}
	for k, v := range extra {
		meta[k] = v
	}
	return m.Data.Mutate(ctx, t.tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:       orSystem(t.who.Actor),
			ActorKind:   orSystemKind(t.who.Kind),
			Action:      "sessions.terminal." + op,
			TargetKind:  workspaceKind,
			TargetID:    t.folderID,
			PayloadHash: hash[:],
			Meta:        meta,
		})
		return err
	})
}
