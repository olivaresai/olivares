// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

func TestSecurityMenuOffersApprovalsTheKillSwitchAndTheLedger(t *testing.T) {
	root := newRootCmd()
	frame := newTestScreens(t).menuFrame()
	var labels []string
	for _, item := range frame.Items {
		labels = append(labels, item.Label)
		if item.Open == nil {
			t.Errorf("row %q opens nothing", item.Label)
		}
		if err := commandResolves(root, item.Command); err != nil {
			t.Errorf("row %q names %q, which is no command: %v", item.Label, item.Command, err)
		}
	}
	if want := []string{"Approvals", "Kill switch", "Audit ledger"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("rows = %v, want %v", labels, want)
	}
}

// The Security row opens its screens only for an engine that is reachable and a
// person signed in to it, as the tools rows do; with either missing it keeps
// naming its command. Whatever the wiring, no other row gains an opener and the
// row's command does not change.
func TestWireSecurityScreensNeedsAnEngineAndASignIn(t *testing.T) {
	cmd := &cobra.Command{Use: "ui"}
	for name, tools := range map[string]*uiTools{"no screens wired": nil, "screens wired": {}} {
		frame := mainMenuFrame(rootStatusReport{})
		wireSecurityScreens(&frame, cmd, tools)
		for _, item := range frame.Items {
			opens := item.Open != nil
			switch {
			case item.Label == "Security" && opens != (tools != nil):
				t.Errorf("%s: the Security row opens %v", name, opens)
			case item.Label != "Security" && opens:
				t.Errorf("%s: %q gained an opener", name, item.Label)
			case item.Label == "Security" && item.Command != "olivares governance approvals ls":
				t.Errorf("%s: the Security row's command is %q", name, item.Command)
			}
		}
	}
}

func readQueue(t *testing.T, s *securityScreens) tui.Frame {
	t.Helper()
	list, err := s.readQueue()
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	return s.approvalsFrame(list, nil, nil)
}

func TestApprovalsFrameListsWaitingRequests(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{
		secApproval("apr-1", "pending", "Launch claude in /srv/payroll (read-write)"),
		secApproval("apr-2", "pending", "Turn on the issue tracker MCP server"),
		secApproval("apr-3", "approved", "Already decided"),
	}
	f := readQueue(t, newTestScreens(t))
	if f.Right != "2 waiting" {
		t.Errorf("Right = %q, want \"2 waiting\"", f.Right)
	}
	if len(f.Items) != 2 || !strings.HasPrefix(f.Items[0].Label, "Launch claude in /srv") ||
		!strings.Contains(f.Items[0].Detail, "rehema@acme.example") || !strings.Contains(f.Items[0].Detail, "0 of 1") {
		t.Fatalf("rows = %+v", f.Items)
	}
	// The list gives a label 24 columns; a longer one would run into the detail.
	if n := len([]rune(f.Items[0].Label)); n > menuLabelRunes {
		t.Errorf("the label %q takes %d columns, want at most %d", f.Items[0].Label, n, menuLabelRunes)
	}
	if f.Items[0].Command != "olivares governance approvals get apr-1" || f.Items[0].Open == nil {
		t.Errorf("the row's command is %q, opener %v", f.Items[0].Command, f.Items[0].Open != nil)
	}
	if got := e.requests(http.MethodGet, "/v1/m/governance/approvals"); len(got) != 1 || got[0].Query.Get("status") != "pending" {
		t.Errorf("the queue was read with %v, want one read of status=pending", got)
	}
}

func TestApprovalsFrameSaysWhenNothingWaits(t *testing.T) {
	newSecEngine(t)
	f := readQueue(t, newTestScreens(t))
	if len(f.Items) != 0 || !strings.Contains(f.Footer, "Nothing is waiting") {
		t.Fatalf("an empty queue shows rows %v and footer %q", f.Items, f.Footer)
	}
}

func TestApprovalsFrameStripsTerminalControlsFromEngineText(t *testing.T) {
	e := newSecEngine(t)
	evil := "Launch \x1b[2J\x1b]0;owned\x07 here"
	a := secApproval("apr-1", "pending", evil)
	a["requested_by"] = "\x1b[31mjohan\x1b[0m"
	e.approvals = []map[string]any{a}
	f := readQueue(t, newTestScreens(t))
	review := strings.Join(reviewLines(cliApproval{Reason: evil, RequestedBy: "\x1b[31mjohan\x1b[0m"}, time.Now()), "\n")
	if all := f.Items[0].Label + f.Items[0].Detail + review; strings.ContainsAny(all, "\x1b\x07") {
		t.Fatalf("a control character from the engine reached the screen: %q", all)
	}
}

func TestApprovalsFrameSaysWhenThereAreMoreWaitingThanItShows(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude")}
	e.approvalsHasMore = true
	if f := readQueue(t, newTestScreens(t)); f.Right != "1+ waiting" {
		t.Fatalf("Right = %q, want \"1+ waiting\"", f.Right)
	}
}

// A queue that cannot be read says why, in the engine's words, and offers no rows.
func TestApprovalsFrameSaysWhyTheQueueCouldNotBeRead(t *testing.T) {
	e := newSecEngine(t)
	e.queueStatus = http.StatusInternalServerError
	s := newTestScreens(t)
	list, err := s.readQueue()
	if err == nil {
		t.Fatal("a queue the engine refused was read")
	}
	f := s.approvalsFrame(list, err, nil)
	if len(f.Items) != 0 || !strings.Contains(f.Footer, "approval queue is unavailable") {
		t.Fatalf("rows %v, footer %q; want no rows and the engine's words", f.Items, f.Footer)
	}
}

func TestReadQueueNeedsASignedInPersonAndSaysWhatToDo(t *testing.T) {
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := newTestScreens(t).readQueue(); err == nil || !strings.Contains(err.Error(), "server") {
		t.Fatalf("err = %v, want the command's sentence about the missing server", err)
	}
}

// An engine that answers 200 with nothing is not an engine with nothing waiting
// and a kill switch that is off: the screens say they could not read it.
func TestAnEmptyAnswerIsNotAnEmptyQueueOrASwitchThatIsOff(t *testing.T) {
	e := newSecEngine(t)
	e.emptyAnswers = true
	s := newTestScreens(t)
	if _, err := s.readQueue(); !errors.Is(err, errNoAnswer) {
		t.Errorf("readQueue on an empty answer = %v, want errNoAnswer", err)
	}
	res, _, err := governanceKillSwitchStatus(s.cmd, s.flags)
	if err != nil || !answeredNothing(res) {
		t.Errorf("the kill switch state: err %v, empty %v; want an empty answer the screen can tell", err, answeredNothing(res))
	}
	// The command keeps printing what it always did.
	out, errb, err := execRoot(t, observeArgs(e.URL, "governance", "approvals", "ls")...)
	if err != nil || !strings.Contains(out, "no approvals match") {
		t.Errorf("the command printed %q, %q, %v; want it unchanged", out, errb, err)
	}
}

// The requester can be a long machine ID, and a row is cut at 80 columns: the
// votes and the time left must come before it.
func TestApprovalRowShowsVotesAndTimeBeforeALongRequester(t *testing.T) {
	e := newSecEngine(t)
	a := secApproval("apr-1", "pending", "Push cart-totals from storefront-web to origin")
	a["requested_by"] = "token:" + strings.Repeat("machine-id-", 4)
	e.approvals = []map[string]any{a}
	f := readQueue(t, newTestScreens(t))
	head := []rune(f.Items[0].Detail)
	if len(head) > 20 {
		head = head[:20]
	}
	if got := string(head); !strings.Contains(got, "0 of 1") || !strings.Contains(got, "h left") {
		t.Fatalf("the first 20 columns of the detail are %q; want the votes and the time left", got)
	}
}

// An ID that is not a plain name is the engine's text, not a command to paste.
func TestApprovalRowNamesNoCommandForAnIDThatIsNotAPlainName(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr 1; rm", "pending", "Launch claude")}
	f := readQueue(t, newTestScreens(t))
	if f.Items[0].Command != "olivares governance approvals ls" {
		t.Fatalf("the row's command is %q", f.Items[0].Command)
	}
}

func TestReviewShowsTheRequestWhatItReachesAndTheRuleThatAsked(t *testing.T) {
	a := cliApproval{
		ID: "apr-1", Action: "sessions.run.launch", PolicyRef: "dlp.require-approval", SubjectKind: "workspace", SubjectRef: "workspace:ws-1",
		RequestedBy: "rehema@acme.example", RiskTier: "high", RequiredApprovals: 1,
		ExpiresAt: time.Now().Add(23 * time.Hour).UTC().Format(time.RFC3339),
		Reason:    "Launch claude in /srv/payroll (DLP label, read-write)\nSecond line of the request",
	}
	text := strings.Join(reviewLines(a, time.Now()), "\n")
	for _, want := range []string{
		"Request    Launch claude in /srv/payroll (DLP label, read-write)", // the exact request
		"           Second line of the request",
		"workspace:ws-1",                  // what it reaches
		"Rule       dlp.require-approval", // the rule that asked: the engine's policy_ref
		"Action     sessions.run.launch",  // what it asked to do
		"rehema@acme.example", "0 of 1", "h left",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the review does not show %q:\n%s", want, text)
		}
	}
}

func TestVoteFromNeedsAChoiceAndReadsTheReason(t *testing.T) {
	for _, tc := range []struct {
		name           string
		values         []string
		decision, note string
		refused        bool
	}{
		{"approve with a reason", []string{"Approve", " checked the folder "}, "approve", "checked the folder", false},
		{"reject without a reason", []string{"Reject", ""}, "reject", "", false},
		{"no choice yet", []string{choosePrompt, "because"}, "", "", true},
		{"too few answers", []string{"Approve"}, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, note, refusal := voteFrom(tc.values)
			if decision != tc.decision || note != tc.note || (refusal != "") != tc.refused {
				t.Fatalf("voteFrom(%q) = %q, %q, %q", tc.values, decision, note, refusal)
			}
		})
	}
}

// What the person does on the screen and what the command does must be the
// same requests, so the audit ledger records the same events.
func TestApproveAndRejectSendWhatTheCommandsSend(t *testing.T) {
	for _, tc := range []struct{ choice, decision, note string }{
		{"Approve", "approve", "checked the folder"}, {"Reject", "reject", "not this week"}, {"Approve", "approve", ""}, {"Reject", "reject", ""},
	} {
		t.Run(tc.decision+"/"+tc.note, func(t *testing.T) {
			viaCommand := newSecEngine(t)
			viaCommand.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude")}
			args := observeArgs(viaCommand.URL, "governance", "approvals", tc.decision, "apr-1", "--yes", "-o", "text")
			if tc.note != "" {
				args = append(args, "--note", tc.note)
			}
			out, errb, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("command: %v\n%s", err, errb)
			}

			viaScreen := newSecEngine(t)
			viaScreen.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude")}
			s := newTestScreens(t)
			a, err := governancePendingApproval(s.cmd, s.flags, "apr-1")
			if err != nil {
				t.Fatal(err)
			}
			decision, note, refusal := voteFrom([]string{tc.choice, tc.note})
			if refusal != "" {
				t.Fatal(refusal)
			}
			_, after, err := governanceApprovalDecide(s.cmd, s.flags, a.ID, decision, note)
			if err != nil {
				t.Fatalf("screen: %v", err)
			}
			if got, want := viaScreen.writes(), viaCommand.writes(); len(got) != 1 || !reflect.DeepEqual(got, want) {
				t.Fatalf("the screen sent %+v, the command sent %+v", got, want)
			}
			if notice := approvalOutcome(voteDone[decision], after); !strings.Contains(out, notice) {
				t.Fatalf("the screen says %q, the command says %q", notice, out)
			}
		})
	}
}

func TestReviewRefusesARequestThatIsNoLongerWaiting(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "approved", "Launch claude")}
	s := newTestScreens(t)
	_, err := governancePendingApproval(s.cmd, s.flags, "apr-1")
	if err == nil || err.Error() != "Approval apr-1 is already approved; there is nothing to decide." {
		t.Fatalf("err = %v", err)
	}
	if exitcode.From(err) != exitcode.Conflict {
		t.Errorf("exit code = %d, want the command's %d", exitcode.From(err), exitcode.Conflict)
	}
	if len(e.writes()) != 0 {
		t.Fatalf("a vote was sent for a decided request: %v", e.writes())
	}
}

func TestApproveSaysWhatTheEngineAnswersWhenSomeoneDecidedFirst(t *testing.T) {
	e := newSecEngine(t)
	e.approvals = []map[string]any{secApproval("apr-1", "pending", "Launch claude")}
	s := newTestScreens(t)
	e.approvals[0]["status"] = "approved" // another person voted while the review was open
	_, _, err := governanceApprovalDecide(s.cmd, s.flags, "apr-1", "approve", "")
	if err == nil || !strings.Contains(err.Error(), "no decision can be recorded") {
		t.Fatalf("err = %v, want the engine's words", err)
	}
}

// Stop all sends nothing without the exact word and a reason; the engine is
// never asked.
func TestStopFromNeedsTheExactWordAndAReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		reason string
		ok     bool
		focus  int // the field that needs another answer
	}{
		{"the word and a reason", []string{" database failover ", "stop"}, "database failover", true, 0},
		{"no word", []string{"database failover", ""}, "", false, 1},
		{"a near word", []string{"database failover", "stp"}, "", false, 1},
		{"the word in capitals", []string{"database failover", "STOP"}, "", false, 1},
		{"the word with a space", []string{"database failover", "stop "}, "", false, 1},
		{"no reason", []string{"   ", "stop"}, "", false, 0},
		{"no reason and no word", []string{"", ""}, "", false, 0},
		{"too few answers", []string{"why"}, "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, focus, refusal := stopFrom(tc.values)
			if reason != tc.reason || (refusal == "") != tc.ok || (!tc.ok && focus != tc.focus) {
				t.Fatalf("stopFrom(%q) = %q, focus %d, %q; want focus %d", tc.values, reason, focus, refusal, tc.focus)
			}
		})
	}
}

func TestKillSwitchStopsTheEstateOnceAndTheEngineKeepsTheReason(t *testing.T) {
	e := newSecEngine(t)
	s := newTestScreens(t)
	if err := governanceKillSwitchStopEstate(s.cmd, s.flags, "database failover"); err != nil {
		t.Fatal(err)
	}
	got := e.writes()
	wantBody := map[string]any{"scope_kind": "estate", "reason": "database failover"}
	if len(got) != 1 || got[0].Path != "/v1/m/governance/killswitch" || !reflect.DeepEqual(got[0].Body, wantBody) {
		t.Fatalf("writes = %+v, want one POST of %v", got, wantBody)
	}
	// A stop that is already on is the engine's own conflict.
	if err := governanceKillSwitchStopEstate(s.cmd, s.flags, "again"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want the engine's words", err)
	}
}

func TestKillSwitchLinesSayWhatIsStoppedAndWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     cliKillSwitchState
		err       error
		want      []string
		canStop   bool
		forbidden string
	}{
		{"running", cliKillSwitchState{}, nil, []string{"The kill switch is off"}, true, ""},
		{"estate stopped", cliKillSwitchState{EstateStopped: true}, nil, []string{"Everything is stopped", "two people"}, false, ""},
		{"an agent stop", cliKillSwitchState{Active: []cliKillSwitch{{ScopeKind: "agent", AgentExternalID: "builder-7", Reason: "loops", EngagedBy: "rehema@acme.example"}}}, nil,
			[]string{"agent builder-7", "loops", "rehema@acme.example"}, true, ""},
		{"unreadable", cliKillSwitchState{}, fmt.Errorf("you need the kill switch read right"), []string{"Could not read", "kill switch read right", "still stop"}, true, ""},
		{"engine text with controls", cliKillSwitchState{Active: []cliKillSwitch{{ScopeKind: "agent", AgentExternalID: "x\x1b[2J", Reason: "y\x07"}}}, nil, nil, true, "\x1b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, canStop := killSwitchLines(tc.state, tc.err)
			text := strings.Join(lines, "\n")
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("the screen does not say %q:\n%s", want, text)
				}
			}
			if canStop != tc.canStop {
				t.Errorf("Stop all offered = %v, want %v", canStop, tc.canStop)
			}
			if tc.forbidden != "" && strings.ContainsAny(text, tc.forbidden+"\x07") {
				t.Errorf("a control character reached the screen: %q", text)
			}
		})
	}
}

func readLedgerFrame(t *testing.T, s *securityScreens, f ledgerFilter, verified string) (tui.Frame, []auditRow) {
	t.Helper()
	rows, complete, err := s.readLedger(f)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	l := &ledgerScreen{s: s, filter: f, verified: verified}
	return l.frame(rows, complete), rows
}

// eventRows are the rows that are events: the actions of the screen open a panel.
func eventRows(f tui.Frame) []tui.MenuItem {
	var out []tui.MenuItem
	for _, it := range f.Items {
		if it.Open == nil {
			out = append(out, it)
		}
	}
	return out
}

func TestLedgerListsTheNewestEventsFirstWithoutItsOwnReads(t *testing.T) {
	ledgerEngine(t)
	f, _ := readLedgerFrame(t, newTestScreens(t), ledgerFilter{}, "")
	var actions []string
	for _, it := range eventRows(f) {
		actions = append(actions, strings.Fields(it.Detail)[0])
	}
	if want := []string{"tool.login", "checkpoint.signed", "approval.requested", "session.started"}; !reflect.DeepEqual(actions, want) {
		t.Fatalf("events = %v, want %v (newest first, no audit.read)", actions, want)
	}
	var acts []string
	for _, it := range f.Items {
		if it.Open != nil {
			acts = append(acts, it.Label)
		}
	}
	want := []string{"Filter"}
	if auditVerificationAvailable {
		want = append(want, "Verify now")
	}
	if !reflect.DeepEqual(acts, want) {
		t.Errorf("action rows = %v, want %v and no Clear filter without a filter", acts, want)
	}
}

func TestLedgerFilterReadsPersonTextActionAndSince(t *testing.T) {
	e := ledgerEngine(t)
	s := newTestScreens(t)
	var filter ledgerFilter
	if err := filter.fromAnswers([]string{"rehema@acme.example", "", "", "2h"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f, rows := readLedgerFrame(t, s, filter, "")
	if len(rows) != 1 || !strings.HasPrefix(eventRows(f)[0].Detail, "tool.login") {
		t.Fatalf("rows = %+v, want only Rehema's event from the last two hours", rows)
	}
	last := e.requests(http.MethodGet, "/v1/audit")
	q := last[len(last)-1].Query
	if q.Get("actor") != "rehema@acme.example" || q.Get("since") == "" || q.Get("q") != "" || q.Get("action") != "" {
		t.Fatalf("the engine was asked %v", q)
	}
	var clear bool
	for _, it := range f.Items {
		clear = clear || it.Label == "Clear filter"
	}
	if !clear {
		t.Error("an active filter has no Clear filter row")
	}
	if !strings.Contains(f.Footer, "filter: person rehema@acme.example, since 2h") {
		t.Errorf("footer = %q, want the filter in the words the person typed", f.Footer)
	}

	filter = ledgerFilter{}
	if err := filter.fromAnswers([]string{"", "codex", "tool.", ""}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f, _ = readLedgerFrame(t, s, filter, "")
	last = e.requests(http.MethodGet, "/v1/audit")
	if q = last[len(last)-1].Query; q.Get("q") != "codex" || q.Get("action") != "tool." || q.Get("actor") != "" {
		t.Fatalf("the engine was asked %v", q)
	}
	if len(eventRows(f)) != 1 {
		t.Fatalf("rows = %+v", f.Items)
	}
}

func TestLedgerFilterRefusesASinceItCannotRead(t *testing.T) {
	filter := ledgerFilter{person: "rehema@acme.example"}
	err := filter.fromAnswers([]string{"", "", "", "last tuesday"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "2h") {
		t.Fatalf("err = %v, want a sentence that shows the accepted forms", err)
	}
	if filter != (ledgerFilter{person: "rehema@acme.example"}) {
		t.Fatalf("a bad since changed the filter: %+v", filter)
	}
}

func TestFilterWithTooFewAnswersIsAnErrorNotAPanic(t *testing.T) {
	var filter ledgerFilter
	if err := filter.fromAnswers([]string{"only one"}, time.Now()); err == nil {
		t.Fatal("a short answer list was accepted")
	}
}

// The list shows times in this computer's zone, and the screen passes time.Now(),
// so a typed date is that day in the zone of now: east of UTC, midnight UTC would
// leave out the first hours of the day.
func TestParseSinceReadsADateInTheZoneOfNow(t *testing.T) {
	got, err := parseSince("2026-10-01", time.Date(2026, 10, 20, 12, 0, 0, 0, time.FixedZone("UTC+9", 9*3600)))
	if err != nil || got != "2026-09-30T15:00:00Z" {
		t.Fatalf("parseSince(date) = %q, %v; want local midnight 2026-09-30T15:00:00Z", got, err)
	}
}

func TestParseSinceAcceptsDurationsDaysDatesAndRFC3339(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ in, want string }{
		{"2h", "2026-10-20T10:00:00Z"},
		{"90m", "2026-10-20T10:30:00Z"},
		{"3d", "2026-10-17T12:00:00Z"},
		{"2026-10-01", "2026-10-01T00:00:00Z"},
		{"2026-10-01T08:30:00Z", "2026-10-01T08:30:00Z"},
		{"", ""},
	} {
		got, err := parseSince(tc.in, now)
		if err != nil || got != tc.want {
			t.Errorf("parseSince(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"last tuesday", "-2h", "0d", "2x", "99999999d"} {
		if got, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) = %q, want an error", bad, got)
		}
	}
}

func TestLedgerFollowsPagesAndKeepsTheNewestEvents(t *testing.T) {
	e := newSecEngine(t)
	e.pageCap = 100
	base := time.Now().Add(-5 * time.Hour)
	for i := 1; i <= 250; i++ {
		e.events = append(e.events, secEvent(i, base.Add(time.Duration(i)*time.Minute), "rehema@acme.example", fmt.Sprintf("tool.login.%03d", i), "tool", "codex"))
	}
	f, _ := readLedgerFrame(t, newTestScreens(t), ledgerFilter{person: "rehema@acme.example"}, "")
	rows := eventRows(f)
	if len(rows) != ledgerRows || !strings.HasPrefix(rows[0].Detail, "tool.login.250") || !strings.HasPrefix(rows[len(rows)-1].Detail, "tool.login.151") {
		t.Fatalf("rows = %d, first %q last %q; want the newest %d (250 down to 151)", len(rows), rows[0].Detail, rows[len(rows)-1].Detail, ledgerRows)
	}
	if strings.Contains(f.Footer, "Scan stopped early") {
		t.Errorf("a complete scan says it stopped early: %q", f.Footer)
	}
	if f.Right != "newest 100" {
		t.Errorf("Right = %q, want \"newest 100\" when the list is cut at the row limit", f.Right)
	}
}

func TestLedgerSaysWhenTheScanStoppedBeforeTheNewestEvents(t *testing.T) {
	e := newSecEngine(t)
	e.pageCap = 1
	base := time.Now().Add(-5 * time.Hour)
	for i := 1; i <= auditFilterPages+10; i++ {
		e.events = append(e.events, secEvent(i, base.Add(time.Duration(i)*time.Minute), "rehema@acme.example", "tool.login", "tool", "codex"))
	}
	f, _ := readLedgerFrame(t, newTestScreens(t), ledgerFilter{person: "rehema@acme.example"}, "")
	if !strings.Contains(f.Footer, "Scan stopped early") {
		t.Fatalf("footer = %q; a list that may miss the newest events must say so", f.Footer)
	}
}

func TestLedgerKeepsTheLastVerifyInItsFooter(t *testing.T) {
	ledgerEngine(t)
	f, _ := readLedgerFrame(t, newTestScreens(t), ledgerFilter{}, "last verify 09:41: unattested")
	if !strings.Contains(f.Footer, "last verify 09:41: unattested") {
		t.Fatalf("footer = %q, want the last verify result", f.Footer)
	}
}

func TestLedgerEmptyOfEventsSaysSo(t *testing.T) {
	newSecEngine(t)
	f, _ := readLedgerFrame(t, newTestScreens(t), ledgerFilter{}, "")
	if f.Right != "no events" || len(eventRows(f)) != 0 {
		t.Fatalf("Right %q, rows %v", f.Right, eventRows(f))
	}
}

// signedLedger is a data directory whose demo tenant has a signed checkpoint, the
// ledger `olivares audit verify` reads on this computer.
func signedLedger(t *testing.T) (dir, tenant string) { return ledgerDir(t, true) }

// ledgerDir is a data directory with the demo tenant, and a signed checkpoint
// when signed.
func ledgerDir(t *testing.T, signed bool) (dir, tenant string) {
	t.Helper()
	ctx := context.Background()
	dir = t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if eng.demoTenant.IsZero() {
		t.Fatal("no demo tenant seeded")
	}
	if signed {
		if _, ok, err := eng.signer.Checkpoint(ctx, eng.store, eng.demoTenant); err != nil || !ok {
			t.Fatalf("checkpoint demo tenant: ok=%v err=%v", ok, err)
		}
	}
	tenant = eng.demoTenant.String()
	_ = eng.Close()
	return dir, tenant
}

// Verify now runs the audit verify command itself, so it can only show what
// that command shows.
func TestVerifyNowShowsTheFailureOfTheCommand(t *testing.T) {
	ledgerEngine(t) // its tenant, tenant-a, is not an ID audit verify accepts
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	_, _, err := newTestScreens(t).verify()
	if err == nil {
		t.Fatal("Verify now reported success without a ledger to verify")
	}
	_, _, want := execRoot(t, "audit", "verify")
	if want == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("screen error %q does not carry the command's %v", err, want)
	}
}

func TestVerifyReportSaysWhatItReadsAndWhatTheStatusMeans(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"ok", "Ledger status: ok"},
		{"unattested", "no signed checkpoint"},
		{"corrupt", "failed a check"},
	} {
		got := verifyReport(tc.status, "{ }")
		if !strings.Contains(got, tc.want) || !strings.Contains(got, "on this computer") || !strings.HasSuffix(got, "{ }") {
			t.Errorf("verifyReport(%q) = %q; want %q, the scope, and the command's report", tc.status, got, tc.want)
		}
	}
}

func TestAuditLsPrintsAnEmptyLedgerAsAnArrayInJSON(t *testing.T) {
	e := newSecEngine(t)
	out, errb, err := execRoot(t, observeArgs(e.URL, "audit", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("audit ls: %v\n%s", err, errb)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("audit ls -o json on an empty ledger printed %q, want []", out)
	}
}

// The read behind the screens must leave the command's answer as it was.
func TestAuditLsKeepsItsListAfterTheRefactor(t *testing.T) {
	e := ledgerEngine(t)
	read := func(args ...string) []map[string]any {
		out, errb, err := execRoot(t, observeArgs(e.URL, append([]string{"audit", "ls", "-o", "json"}, args...)...)...)
		if err != nil {
			t.Fatalf("audit ls %v: %v\n%s", args, err, errb)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("audit ls %v printed %q: %v", args, out, err)
		}
		return rows
	}
	seqs := func(rows []map[string]any) []int {
		var got []int
		for _, r := range rows {
			got = append(got, int(r["seq"].(float64)))
		}
		return got
	}
	if got := seqs(read("--limit", "2")); !reflect.DeepEqual(got, []int{4, 3}) {
		t.Errorf("--limit 2 listed seq %v, want the two newest without audit.read: [4 3]", got)
	}
	if got := seqs(read("--all", "--limit", "2")); !reflect.DeepEqual(got, []int{5, 4}) {
		t.Errorf("--all --limit 2 listed seq %v, want [5 4]", got)
	}
	read("--system")
	if len(e.requests(http.MethodGet, "/v1/audit/system")) == 0 {
		t.Error("--system did not read the system ledger")
	}
	row := read("--limit", "1")[0]
	for _, key := range []string{"seq", "occurred_at", "actor", "action", "target_kind", "target_id"} {
		if _, ok := row[key]; !ok {
			t.Errorf("a listed event lost its %q field: %v", key, row)
		}
	}
}
