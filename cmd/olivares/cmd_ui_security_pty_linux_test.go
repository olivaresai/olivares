// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// The Security screens on the real binary and a real terminal, against the fake
// engine of the screen tests: the requests the terminal sends are the ones read
// back from the engine.

const (
	keyDown  = "\x1b[B"
	keyRight = "\x1b[C"
)

func sendKeys(t *testing.T, master *os.File, keys string) {
	t.Helper()
	if _, err := master.Write([]byte(keys)); err != nil {
		t.Fatalf("send %q: %v", keys, err)
	}
}

func killSwitchWrites(e *secEngine) []secRequest {
	return e.requests(http.MethodPost, "/v1/m/governance/killswitch")
}

// startSecurity opens `olivares ui` on the fake engine and goes to the Security
// menu. extra are more environment entries. The first screen is returned, which
// carries the bytes that set the terminal up.
func startSecurity(t *testing.T, e *secEngine, extra ...string) (*os.File, *exec.Cmd, string) {
	t.Helper()
	master, slave := openPTY(t, tui.MinWidth, tui.MinHeight)
	env := cliProcessEnv(t, append([]string{"OLIVARES_SERVER_URL=" + e.URL, "OLIVARES_TOKEN=test-token", "OLIVARES_TENANT=tenant-a"}, extra...)...)
	cmd := startCLI(t, env, slave, slave, slave, "ui")
	first := readScreen(t, master, "Get started")
	sendKeys(t, master, strings.Repeat(keyDown, 3))
	readScreen(t, master, "▸ Security")
	sendKeys(t, master, "\r")
	readScreen(t, master, "Kill switch")
	return master, cmd, first
}

func plainScreen(raw string) string { return termrender.StripANSI(raw) }

func wantOnScreen(t *testing.T, screen string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen is missing %q:\n%s", want, screen)
		}
	}
}

// quitUI leaves with Ctrl-D, which ends the screens from a list and from a panel
// alike (Ctrl-C only leaves a panel).
func quitUI(t *testing.T, master *os.File, cmd *exec.Cmd) {
	t.Helper()
	sendKeys(t, master, "\x04")
	if code := waitCLI(t, cmd); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
}

// Review a request, vote with a reason, and see the list read again with what
// the engine now holds.
func TestUISecurityApprovesWithAReasonFromATerminal(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude in /srv/payroll (read-write)")}
	master, cmd, _ := startSecurity(t, e)

	sendKeys(t, master, "\r") // Approvals
	screen := plainScreen(readScreen(t, master, "1 waiting"))
	wantOnScreen(t, screen, "Security › Approvals", "Launch claude in /srv", "= olivares governance approvals get apr-1")

	sendKeys(t, master, "\r") // the request
	screen = plainScreen(readScreen(t, master, "Decision"))
	wantOnScreen(t, screen, "Review", "dlp.require-approval", "sessions.run.launch", "workspace:ws-1", "(choose)")

	// Enter without choosing sends nothing and says what to do.
	sendKeys(t, master, "\r\r")
	readScreen(t, master, "Choose Approve or Reject")
	if got := e.writes(); len(got) != 0 {
		t.Fatalf("Enter without a choice reached the engine: %+v", got)
	}

	sendKeys(t, master, keyRight+"\r") // Approve, then the reason
	sendKeys(t, master, "ok to push\r")
	readScreen(t, master, "Approved apr-1. The request is approved.")
	decisions := e.requests(http.MethodPost, "/v1/m/governance/approvals/apr-1/decisions")
	if len(decisions) != 1 || !reflect.DeepEqual(decisions[0].Body, map[string]any{"decision": "approve", "note": "ok to push"}) {
		t.Fatalf("decisions sent = %+v", decisions)
	}
	quitUI(t, master, cmd)
}

func TestUISecurityRejectsFromATerminal(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "pending", "Turn on the issue tracker MCP server")}
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, "\r")
	readScreen(t, master, "1 waiting")
	sendKeys(t, master, "\r")
	readScreen(t, master, "Decision")
	sendKeys(t, master, "\x1b[D\r") // ← wraps to Reject, then the reason
	sendKeys(t, master, "not this week\r")
	readScreen(t, master, "Rejected apr-1. The request is rejected.")
	decisions := e.requests(http.MethodPost, "/v1/m/governance/approvals/apr-1/decisions")
	if len(decisions) != 1 || !reflect.DeepEqual(decisions[0].Body, map[string]any{"decision": "reject", "note": "not this week"}) {
		t.Fatalf("decisions sent = %+v", decisions)
	}
	quitUI(t, master, cmd)
}

// Someone decides the request while the review is open: the engine's own words
// come back, and the list behind the review is read again.
func TestUISecuritySaysWhenSomeoneDecidedFirst(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude")}
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, "\r")
	readScreen(t, master, "1 waiting")
	sendKeys(t, master, "\r")
	readScreen(t, master, "Decision")
	e.mu.Lock()
	e.approvals[0]["status"] = "approved"
	e.mu.Unlock()
	sendKeys(t, master, keyRight+"\r\r")
	readScreen(t, master, "no decision can be recorded")
	sendKeys(t, master, "\r") // back to the list, read again
	readScreen(t, master, "Nothing is waiting")
	quitUI(t, master, cmd)
}

// Stop all: a wrong word sends nothing; the right one, with a reason, stops the
// estate once.
func TestUISecurityStopAllNeedsTheWordFromATerminal(t *testing.T) {
	e := newSecEngine(t)
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, keyDown+"\r") // Kill switch
	screen := plainScreen(readScreen(t, master, "Type stop"))
	wantOnScreen(t, screen, "The kill switch is off", "Reason")

	sendKeys(t, master, "database drill\rstp\r")
	readScreen(t, master, "Nothing was stopped: type the word stop.")
	if got := killSwitchWrites(e); len(got) != 0 {
		t.Fatalf("Stop all with the wrong word reached the engine: %+v", got)
	}

	// The reason is kept and the focus is on the word, which is typed again.
	sendKeys(t, master, "stop\r")
	screen = plainScreen(readScreen(t, master, "Stopped. Every tool is denied"))
	wantOnScreen(t, screen, "two people", "Kill switch: ON")
	got := killSwitchWrites(e)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Body, map[string]any{"scope_kind": "estate", "reason": "database drill"}) {
		t.Fatalf("stop requests = %+v", got)
	}
	quitUI(t, master, cmd)
}

// A reason pasted over several lines, with the word in it, is text: it does not
// send anything. The terminal was told to bracket pastes when the screens opened.
func TestUISecurityPastedNewlinesDoNotStopAnything(t *testing.T) {
	e := newSecEngine(t)
	master, cmd, first := startSecurity(t, e)
	if !strings.Contains(first, "\x1b[?2004h") {
		t.Fatalf("the screens did not turn on bracketed paste; the first output was %q", first)
	}
	sendKeys(t, master, keyDown+"\r")
	readScreen(t, master, "Type stop")
	sendKeys(t, master, "\x1b[200~drill\rstop\r\x1b[201~")
	readScreen(t, master, "drillstop")
	if got := killSwitchWrites(e); len(got) != 0 {
		t.Fatalf("a paste stopped the estate: %+v", got)
	}
	quitUI(t, master, cmd)
}

// Stop all is on offer when the state cannot be read, and the estate that is
// already stopped says so without offering it again.
func TestUISecurityKillSwitchStates(t *testing.T) {
	e := newSecEngine(t)
	e.stateStatus = http.StatusForbidden
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, keyDown+"\r")
	screen := plainScreen(readScreen(t, master, "Type stop"))
	wantOnScreen(t, screen, "Could not read the kill switch", "kill switch read right", "You can still stop everything.")
	quitUI(t, master, cmd)

	// An engine that says 200 with nothing is not a switch that is off.
	silent := newSecEngine(t)
	silent.emptyAnswers = true
	master, cmd, _ = startSecurity(t, silent)
	sendKeys(t, master, keyDown+"\r")
	screen = plainScreen(readScreen(t, master, "Type stop"))
	wantOnScreen(t, screen, "Could not read the kill switch", "the engine answered with nothing")
	if strings.Contains(screen, "The kill switch is off") {
		t.Errorf("an empty answer was read as a switch that is off:\n%s", screen)
	}
	quitUI(t, master, cmd)

	stopped := newSecEngine(t)
	stopped.estateStopped = true
	master, cmd, _ = startSecurity(t, stopped)
	sendKeys(t, master, keyDown+"\r")
	screen = plainScreen(readScreen(t, master, "Everything is stopped"))
	if strings.Contains(screen, "Type stop") {
		t.Errorf("a stopped estate offers Stop all:\n%s", screen)
	}
	quitUI(t, master, cmd)
}

// The ledger: events newest first, a filter typed in, Verify now on a signed
// ledger; Esc goes back one level at a time.
func TestUISecurityLedgerFiltersAndVerifiesFromATerminal(t *testing.T) {
	e := ledgerEngine(t)
	dir, tenant := signedLedger(t)
	master, cmd, _ := startSecurity(t, e, "OLIVARES_DATA_DIR="+dir, "OLIVARES_TENANT="+tenant)
	sendKeys(t, master, keyDown+keyDown+"\r")
	screen := plainScreen(readScreen(t, master, "4 shown"))
	wantOnScreen(t, screen, "Security › Audit ledger", "tool.login", "session.started", "Filter")
	if auditVerificationAvailable {
		wantOnScreen(t, screen, "Verify now")
	} else if strings.Contains(screen, "Verify now") {
		t.Fatal("Community offers Business verification")
	}
	if strings.Contains(screen, "audit.read") {
		t.Errorf("the ledger lists its own reads:\n%s", screen)
	}

	sendKeys(t, master, "\r") // Filter
	readScreen(t, master, "Person")
	sendKeys(t, master, "johan@acme.example\r\r\r\r")
	screen = plainScreen(readScreen(t, master, "Filter set."))
	screen = screen[strings.LastIndex(screen, "Security › Audit ledger"):]
	wantOnScreen(t, screen, "1 shown", "approval.requested", "Clear filter")
	if strings.Contains(screen, "tool.login") {
		t.Errorf("the filtered ledger still lists other people's events:\n%s", screen)
	}

	if auditVerificationAvailable {
		sendKeys(t, master, keyDown+"\r") // Verify now (the second row)
		screen = plainScreen(readScreen(t, master, "Ledger status: ok"))
		wantOnScreen(t, screen, "olivares audit verify")
		sendKeys(t, master, "\r") // back to the list, which keeps the result
		readScreen(t, master, "last verify")

	}
	sendKeys(t, master, keyDown+"\r") // Clear filter (the third row)
	screen = plainScreen(readScreen(t, master, "Filter cleared."))
	screen = screen[strings.LastIndex(screen, "Security › Audit ledger"):]
	wantOnScreen(t, screen, "4 shown", "tool.login")
	last := e.requests(http.MethodGet, "/v1/audit")
	if q := last[len(last)-1].Query; q.Get("actor") != "" {
		t.Errorf("the ledger was read with the filter after Clear filter: %v", q)
	}

	sendKeys(t, master, "\x1b")
	readScreen(t, master, "Approvals") // the Security menu
	sendKeys(t, master, "\x1b")
	readScreen(t, master, "All commands") // the main menu
	sendKeys(t, master, "q")
	if code := waitCLI(t, cmd); code != 0 {
		t.Fatalf("q exited %d, want 0", code)
	}
}

// A request longer than the screen says it was cut and still offers the vote:
// the command in the bottom row reads all of it.
func TestUISecurityReviewSaysWhenTheRequestIsCut(t *testing.T) {
	e := newSecEngine(t)
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("Line %d of a very long request", i))
	}
	e.approvals = []map[string]any{secApproval("apr-1", "pending", strings.Join(lines, "\n"))}
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, "\r")
	readScreen(t, master, "1 waiting")
	sendKeys(t, master, "\r")
	screen := plainScreen(readScreen(t, master, "Decision"))
	wantOnScreen(t, screen, "more lines", "Line 1 of a very long request", "= olivares governance approvals get apr-1")
	if strings.Contains(screen, "Line 40 of") {
		t.Errorf("the end of a 40-line request fits at 80 x 24?\n%s", screen)
	}
	sendKeys(t, master, keyRight+"\r\r")
	readScreen(t, master, "Approved apr-1.")
	quitUI(t, master, cmd)
}

// Leaving while a stop or a vote is on its way: the engine may have taken it, so
// the command says so once the terminal is back.
func TestUISecurityLeavingWhileAStopIsOnItsWaySaysSo(t *testing.T) {
	e := newSecEngine(t)
	gate := make(chan struct{})
	e.mu.Lock()
	e.gate = gate
	e.mu.Unlock()
	t.Cleanup(func() { close(gate) })
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, keyDown+"\r")
	readScreen(t, master, "Type stop")
	sendKeys(t, master, "database drill\rstop\r")
	readScreen(t, master, "Stopping everything")
	quitUI(t, master, cmd)
	if said := plainScreen(readAvailable(t, master)); !strings.Contains(said, "A stop sent as you left may have been engaged") {
		t.Fatalf("leaving mid-stop said nothing about it:\n%s", said)
	}
}

func TestUISecurityLeavingWhileAVoteIsOnItsWaySaysSo(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude")}
	gate := make(chan struct{})
	e.mu.Lock()
	e.gate = gate
	e.mu.Unlock()
	t.Cleanup(func() { close(gate) })
	master, cmd, _ := startSecurity(t, e)
	sendKeys(t, master, "\r")
	readScreen(t, master, "1 waiting")
	sendKeys(t, master, "\r")
	readScreen(t, master, "Decision")
	sendKeys(t, master, keyRight+"\r\r")
	readScreen(t, master, "Sending your decision")
	quitUI(t, master, cmd)
	if said := plainScreen(readAvailable(t, master)); !strings.Contains(said, "A decision sent as you left may have been recorded") {
		t.Fatalf("leaving mid-vote said nothing about it:\n%s", said)
	}
}
