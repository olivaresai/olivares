// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// The terminal tests start a REAL shell on a real pty under the folder's real
// confinement. Nothing in them stands in for the shell, the pty or the audit ledger.

var terminalWho1 = terminalWho{Actor: "user:u1", Kind: "user"}

// terminalHarness is a module with confinement wired and one registered folder.
func terminalHarness(t *testing.T) (m *Module, tenant model.TenantID, st store.Store, folder workspaceDTO) {
	t.Helper()
	m, st, tenant, _ = newRuntimeHarness(t, WithConfinement([]string{t.TempDir()}, false))
	folder = mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	return m, tenant, st, folder
}

func startTestTerminal(t *testing.T, m *Module, tenant model.TenantID, ref string) *terminal {
	t.Helper()
	term, err := m.startTerminal(context.Background(), tenant, ref, terminalWho1, 24, 80)
	if err != nil {
		t.Fatalf("startTerminal: %v", err)
	}
	t.Cleanup(func() { term.close("test") })
	return term
}

// readUntil reads the terminal's output until it holds want, or fails at the deadline.
func readUntil(t *testing.T, term *terminal, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var seen []byte
	var pos uint64
	for !bytes.Contains(seen, []byte(want)) {
		chunk, err := term.read(ctx, pos)
		if err != nil {
			t.Fatalf("waiting for %q, saw %q: %v", want, seen, err)
		}
		seen = append(seen, chunk.Data...)
		pos = chunk.Offset + uint64(len(chunk.Data))
		if chunk.Exited && !bytes.Contains(seen, []byte(want)) {
			t.Fatalf("the shell ended before %q, saw %q", want, seen)
		}
	}
	return string(seen)
}

func auditActions(t *testing.T, st store.Store, tenant model.TenantID) map[string][][]byte {
	t.Helper()
	out := map[string][][]byte{}
	ctx := context.Background()
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			out[ev.Action] = append(out[ev.Action], ev.PayloadHash)
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	return out
}

func hasHash(hashes [][]byte, want [32]byte) bool {
	for _, h := range hashes {
		if bytes.Equal(h, want[:]) {
			return true
		}
	}
	return false
}

func TestTerminalShellStartsInTheFolder(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.send(context.Background(), "pwd\r"); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(folder.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, root)
}

func TestTerminalIsConfinedToTheFolder(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if term.confinement.Mode == confine.ModeNone {
		t.Skipf("this host gives no confinement: %s", term.confinement.Reason)
	}
	outside := t.TempDir()
	if err := term.send(context.Background(), "echo x > "+outside+"/escaped; echo inside > escaped-not; echo rc=$?\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "rc=0")
	if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
		t.Fatal("the shell wrote outside its folder")
	}
	if _, err := os.Stat(filepath.Join(folder.RootPath, "escaped-not")); err != nil {
		t.Fatalf("the shell could not write inside its folder: %v", err)
	}
}

func TestTerminalRecordsStartAndEveryCommandLine(t *testing.T) {
	m, tenant, st, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	// "echoo" + backspace + " one" is what the shell runs as "echo one": the record is the edited line.
	if err := term.send(context.Background(), "echoo\x7f one-$((1+1))\recho two-$((1+2))\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "two-3")
	got := auditActions(t, st, tenant)
	if len(got["sessions.terminal.start"]) != 1 {
		t.Fatalf("start events = %d, want 1", len(got["sessions.terminal.start"]))
	}
	for _, line := range []string{"echo one-$((1+1))", "echo two-$((1+2))"} {
		want := terminalPayloadHash(terminalLineInput{ref: folder.WorkspaceRef, terminal: term.id, line: line, hidden: false})
		if !hasHash(got["sessions.terminal.command"], want) {
			t.Errorf("no command event for %q", line)
		}
	}
}

// failingLedger is the module's real data handle until fail is set, then a ledger that cannot be written.
type failingLedger struct {
	api.ModuleData
	fail atomic.Bool
}

func (f *failingLedger) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if f.fail.Load() {
		return errors.New("ledger unavailable")
	}
	return f.ModuleData.Mutate(ctx, tenant, fn)
}

func TestTerminalSealsTheLineBeforeTheShellRunsIt(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	ledger := &failingLedger{ModuleData: m.Data}
	m.UseData(ledger)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	marker := filepath.Join(folder.RootPath, "ran")
	ledger.fail.Store(true)
	if err := term.send(context.Background(), "touch "+marker+"\r"); err == nil {
		t.Fatal("a line the ledger could not record was sent to the shell")
	}
	ledger.fail.Store(false)
	select {
	case <-term.done():
	case <-time.After(5 * time.Second):
		t.Fatal("a shell with unsealed input stayed alive")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the shell ran a command that was never recorded")
	}
}

// A password prompt (sudo, ssh, read -s) turns echo off and leaves line editing to the
// kernel; the shell's own prompt turns both off. Only the first is a hidden entry.
func TestTerminalWithholdsAHiddenEntry(t *testing.T) {
	m, tenant, st, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.send(context.Background(), "read -s secret\r"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !term.inputHidden() {
		if time.Now().After(deadline) {
			t.Fatal("the shell never asked for a hidden entry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := term.send(context.Background(), "hunter2\r"); err != nil {
		t.Fatal(err)
	}
	got := auditActions(t, st, tenant)
	if hasHash(got["sessions.terminal.command"], terminalPayloadHash(terminalLineInput{ref: folder.WorkspaceRef, terminal: term.id, line: "hunter2"})) {
		t.Fatal("a hidden entry reached the ledger")
	}
	if !hasHash(got["sessions.terminal.command"], terminalPayloadHash(terminalLineInput{ref: folder.WorkspaceRef, terminal: term.id, hidden: true})) {
		t.Fatal("the ledger does not say that an entry was withheld")
	}
}

func TestTerminalResizeReachesTheShell(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.resize(41, 117); err != nil {
		t.Fatal(err)
	}
	if err := term.send(context.Background(), "stty size\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "41 117")
}

func TestTerminalReconnectKeepsTheSameShell(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.send(context.Background(), "echo first-$((6*7))\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "first-42")
	again, err := m.terminalOf(tenant, folder.WorkspaceRef, term.id, terminalWho1)
	if err != nil || again != term {
		t.Fatalf("reconnect = %v, %v; want the same terminal", again, err)
	}
	// The same shell: its earlier output replays from the start.
	readUntil(t, again, "first-42")
	if err := again.send(context.Background(), "echo second-$((6*7))\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, again, "second-42")
}

// A terminal is the person's until they close it: leaving it for longer than a reconnect
// window (the limit was 60 seconds) finds the same shell, its process and its output.
// Real time on purpose: no limit is named here, so the test holds for any value of one.
func TestTerminalSurvivesBeingLeft(t *testing.T) {
	if testing.Short() {
		t.Skip("waits 70 seconds")
	}
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.send(context.Background(), "sleep 300 & echo job-$((6*7))\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "job-42")
	time.Sleep(70 * time.Second) // nobody is reading
	select {
	case <-term.done():
		t.Fatalf("the terminal ended while it was left alone (reason %q)", term.closeReason())
	default:
	}
	again, err := m.terminalOf(tenant, folder.WorkspaceRef, term.id, terminalWho1)
	if err != nil || again != term {
		t.Fatalf("coming back = %v, %v; want the same terminal", again, err)
	}
	readUntil(t, again, "job-42") // its earlier output is still there
	if err := again.send(context.Background(), "jobs | grep -c sleep\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, again, "\r1\r\n") // the process it started is still running: one job named sleep
}

func TestTerminalBelongsToItsPerson(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	other := terminalWho{Actor: "user:u2", Kind: "user"}
	if _, err := m.terminalOf(tenant, folder.WorkspaceRef, term.id, other); err == nil {
		t.Fatal("another person reached the terminal")
	}
	if _, err := m.terminalOf(tenant, "another-folder", term.id, terminalWho1); err == nil {
		t.Fatal("the terminal answered under another folder")
	}
}

func TestTerminalLimitPerPerson(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	for range terminalMaxPerPerson {
		startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	}
	_, err := m.startTerminal(context.Background(), tenant, folder.WorkspaceRef, terminalWho1, 24, 80)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusTooManyRequests {
		t.Fatalf("one terminal too many = %v, want 429", err)
	}
}

// The refusal counts across folders while the list is per folder, so it says where they are.
func TestTerminalLimitNamesTheFoldersHoldingThem(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	other := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	startTestTerminal(t, m, tenant, other.WorkspaceRef)
	for range terminalMaxPerPerson - 1 {
		startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	}
	_, err := m.startTerminal(context.Background(), tenant, folder.WorkspaceRef, terminalWho1, 24, 80)
	names := []string{folder.WorkspaceRef, other.WorkspaceRef}
	slices.Sort(names)
	want := "you have 4 terminals open in " + names[0] + ", " + names[1] + "; close one first"
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusTooManyRequests || re.Error() != want {
		t.Fatalf("one terminal too many = %v, want 429 %q", err, want)
	}
}

// History is kept at the reference's limits, 8 MiB and 5,000 lines, whichever is smaller. The
// numbers are written out: a change to the constants must be a decision, seen here.
func TestTerminalHistoryLimits(t *testing.T) {
	t.Run("a fresh read from 0 returns line 1 of 5000", func(t *testing.T) {
		m, tenant, _, folder := terminalHarness(t)
		term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
		// Padded to 100 digits: the plain output of seq 1 5000 (24 KiB) fit the old tail too.
		if err := term.send(context.Background(), "seq -f '%0100g' 1 5000; echo done-$((6*7))\r"); err != nil {
			t.Fatal(err)
		}
		readUntil(t, term, "done-42")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		chunk, err := term.read(ctx, 0)
		lineOne := strings.Repeat("0", 99) + "1\r\n"
		if err != nil || chunk.Offset != 0 || !bytes.Contains(chunk.Data, []byte(lineOne)) {
			t.Fatalf("read from 0: offset %d, err %v, line 1 present %v", chunk.Offset, err, bytes.Contains(chunk.Data, []byte(lineOne)))
		}
	})

	t.Run("the line limit keeps the last 5000 whole lines", func(t *testing.T) {
		term := &terminal{changed: make(chan struct{})}
		for i := range 20000 {
			term.append([]byte("line-" + strconv.Itoa(i) + "\n"))
		}
		if n := bytes.Count(term.buf, []byte("\n")); n < 5000 || n > 6000 {
			t.Fatalf("kept %d lines, want 5000 plus at most the slack", n)
		}
		if !bytes.HasPrefix(term.buf, []byte("line-")) || !bytes.HasSuffix(term.buf, []byte("line-19999\n")) {
			t.Fatalf("kept output starts %q and ends %q", term.buf[:12], term.buf[len(term.buf)-12:])
		}
		chunk, err := term.read(context.Background(), 0)
		if err != nil || chunk.Offset != term.base || term.base == 0 {
			t.Fatalf("read from 0 = offset %d, err %v; base %d", chunk.Offset, err, term.base)
		}
	})

	t.Run("the byte limit trims past a slack and cuts after a newline", func(t *testing.T) {
		const lineLen = 4 << 10 // long lines, so 8 MiB is reached before 5,000 lines
		line := append(bytes.Repeat([]byte("x"), lineLen-1), '\n')
		term := &terminal{changed: make(chan struct{})}
		for range (8<<20)/lineLen + 25 { // 100 KiB over the limit
			term.append(line)
		}
		if term.base != 0 {
			t.Fatalf("trimmed at %d bytes, inside the slack: base %d", len(term.buf), term.base)
		}
		for range 1000 { // 4 MiB more, past any slack
			term.append(line)
		}
		if term.base == 0 || term.base%lineLen != 0 {
			t.Fatalf("base %d: not trimmed, or cut inside a line", term.base)
		}
		if n := len(term.buf); n < 8<<20-terminalChunkBytes || n > 10<<20 {
			t.Fatalf("kept %d bytes, want 8 MiB plus at most the slack", n)
		}
	})

	t.Run("output without a newline still honors the byte limit", func(t *testing.T) {
		term := &terminal{changed: make(chan struct{})}
		for range 11 {
			term.append(bytes.Repeat([]byte("x"), 1<<20))
		}
		if n := len(term.buf); n < 8<<20 || n > 10<<20 {
			t.Fatalf("kept %d bytes, want 8 MiB plus at most the slack", n)
		}
	})
}

func TestTerminalRefusesWithoutConfinement(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	folder := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	_, err := m.startTerminal(context.Background(), tenant, folder.WorkspaceRef, terminalWho1, 24, 80)
	if err == nil {
		t.Fatal("a shell started in a folder this node cannot confine")
	}
}

func TestStopEndsEveryTerminal(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.done():
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the module left a shell running")
	}
}

func TestTerminalEndsWhenItsFolderIsDeregistered(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := m.deleteWorkspace(context.Background(), tenant, folder.WorkspaceRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.done():
	case <-time.After(5 * time.Second):
		t.Fatal("a shell outlived the registration of its folder")
	}
	if got := term.closeReason(); got != "folder_removed" {
		t.Fatalf("close reason = %q, want folder_removed", got)
	}
}

func TestTerminalInAReadOnlyFolderCannotWriteIt(t *testing.T) {
	m, st, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{t.TempDir()}, false))
	_ = st
	folder := mkWorkspace(t, m, tenant, CreateWorkspaceParams{MountMode: mountRO})
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if term.confinement.Mode == confine.ModeNone {
		t.Skipf("this host gives no confinement: %s", term.confinement.Reason)
	}
	if err := term.send(context.Background(), "touch blocked; echo wrote-$?\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "wrote-1")
	if _, err := os.Stat(filepath.Join(folder.RootPath, "blocked")); err == nil {
		t.Fatal("the shell wrote a folder that is mounted read-only")
	}
}

func TestTerminalLedgerFailureAcceptKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []string
	}{
		{"vi-escape-return", []string{"\x1b\r"}},
		{"split-escape-return", []string{"\x1b", "\r"}},
		{"csi-return", []string{"\x1b[\r"}},
		{"operate-next", []string{"\x0f"}},
		{"canonical-eof", []string{"\x04"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, tenant, _, folder := terminalHarness(t)
			ledger := &failingLedger{ModuleData: m.Data}
			m.UseData(ledger)
			term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
			setup := ""
			switch tc.name {
			case "vi-escape-return", "split-escape-return":
				setup = "set -o vi; "
			case "canonical-eof":
				setup = "exec /bin/sh\r"
			}
			if err := term.send(context.Background(), setup+"echo ready-$((6*7))\r"); err != nil {
				t.Fatal(err)
			}
			readUntil(t, term, "ready-42")
			ledger.fail.Store(true)
			if err := term.send(context.Background(), "touch "+filepath.Join(folder.RootPath, "unsealed")); err != nil {
				t.Fatal(err)
			}
			for i, part := range tc.parts {
				err := term.send(context.Background(), part)
				if i < len(tc.parts)-1 && err != nil {
					t.Fatalf("before accept key: %v", err)
				}
				if i == len(tc.parts)-1 {
					var re *runErr
					if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
						t.Fatalf("accept key bypassed the failing ledger: %v", err)
					}
				}
			}
			ledger.fail.Store(false)
		})
	}
}

func TestTerminalLedgerCoversLongLine(t *testing.T) {
	m, tenant, st, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	line := "#" + strings.Repeat("x", 5000) + "-tail"
	if err := term.send(context.Background(), line+"\r"); err != nil {
		t.Fatal(err)
	}
	want := terminalPayloadHash(terminalLineInput{ref: folder.WorkspaceRef, terminal: term.id, line: line})
	if !hasHash(auditActions(t, st, tenant)["sessions.terminal.command"], want) {
		t.Fatal("ledger did not seal the full line")
	}
}

func TestTerminalLedgerFailureAfterCursorLeft(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	ledger := &failingLedger{ModuleData: m.Data}
	m.UseData(ledger)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.send(context.Background(), "echo ready-$((6*7))\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "ready-42")
	marker := filepath.Join(folder.RootPath, "unsealed")
	ledger.fail.Store(true)
	if err := term.send(context.Background(), "touch "+marker+"\x01\r"); err == nil {
		t.Fatal("ledger failure accepted")
	}
	ledger.fail.Store(false)
	select {
	case <-term.done():
	case <-time.After(5 * time.Second):
		t.Fatal("a shell with unsealed input stayed alive")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the remaining line ran without a seal")
	}
}

func TestTerminalCloseEndsBackgroundChild(t *testing.T) {
	m, tenant, _, folder := terminalHarness(t)
	term := startTestTerminal(t, m, tenant, folder.WorkspaceRef)
	if err := term.send(context.Background(), "sleep 300 & echo $! > child.pid; echo ready-$((6*7))\r"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, term, "ready-42")
	raw, err := os.ReadFile(filepath.Join(folder.RootPath, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("child was never alive: %v", err)
	}
	term.close("closed")
	select {
	case <-term.done():
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not close")
	}
	deadline := time.Now().Add(5 * time.Second)
	for unix.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("background child survived terminal close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
