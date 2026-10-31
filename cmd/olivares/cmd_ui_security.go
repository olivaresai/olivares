// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui_security.go is CLI-CONCEPT §5.5: the Security menu, the approval queue and
// the kill switch as screens (the audit ledger is cmd_ui_security_ledger.go).
// Every read and every vote goes through the function the matching command uses
// (governance*, auditRecentEvents, and the audit verify command itself), so a
// screen sends the requests its command sends and the audit ledger records the
// same events. Like the tools screens, each one reads the engine on its own
// goroutine and shows the answer through Host.Post.

const (
	// stopWord is what a person types to stop everything.
	stopWord = "stop"
	// choosePrompt is the Decision field before a person picks: Enter on it votes
	// for nothing.
	choosePrompt = "(choose)"
	// ledgerRows is how many of the newest events the ledger screen shows.
	ledgerRows = 100
	// ledgerTimeout bounds one read of the ledger, which can follow many pages.
	ledgerTimeout = 30 * time.Second

	screenHints = "↑↓ move  Enter open  / search  ? help  Esc back"

	approvalsHeading = "Security › Approvals"
	ledgerHeading    = "Security › Audit ledger"
)

// voteOf maps the Decision field's words to the vote the engine takes;
// voteDone is what the outcome sentence calls it.
var (
	voteOf   = map[string]string{"Approve": "approve", "Reject": "reject"}
	voteDone = map[string]string{"approve": "Approved", "reject": "Rejected"}
)

type securityScreens struct {
	cmd   *cobra.Command
	flags *authClientFlags
	// notes keeps what a screen could not say before it closed (a vote or a stop
	// sent as the person quit), the way the tools screens do.
	notes *uiTools
}

// newSecurityScreens reads the engine, token and tenant the way the commands do:
// from the environment, then the saved sign-in.
func newSecurityScreens(cmd *cobra.Command, notes *uiTools) *securityScreens {
	return &securityScreens{cmd: cmd, flags: &authClientFlags{timeout: uiRequestTimeout}, notes: notes}
}

// wireSecurityScreens opens the Security row on its screens. They need what the
// tools screens need (an engine that answers and a saved sign-in), so they are
// wired with the same state; with none the row keeps naming its command.
func wireSecurityScreens(frame *tui.Frame, cmd *cobra.Command, tools *uiTools) {
	if tools == nil {
		return
	}
	s := newSecurityScreens(cmd, tools)
	for i := range frame.Items {
		if frame.Items[i].Label == "Security" {
			frame.Items[i].Open = s.securityMenu
		}
	}
}

func (s *securityScreens) menuFrame() tui.Frame {
	return tui.Frame{
		Heading: "Security",
		Items: []tui.MenuItem{
			{Label: "Approvals", Detail: "requests that wait for a person", Command: "olivares governance approvals ls", Open: s.approvals},
			{Label: "Kill switch", Detail: "stop all work, or see that it is stopped", Command: "olivares governance killswitch state", Open: s.killSwitch},
			{Label: "Audit ledger", Detail: "recent events and filters", Command: "olivares audit ls", Open: s.ledger},
		},
		Footer: "Everything done on these screens is recorded in the audit ledger",
		Hints:  screenHints,
	}
}

func (s *securityScreens) securityMenu(h *tui.Host) string {
	h.PushMenu(s.menuFrame())
	return ""
}

// errorLines is an error as the lines of a panel.
func errorLines(err error) []string {
	var lines []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(oneRow(line)); line != "" {
			lines = append(lines, "  "+line)
		}
	}
	return lines
}

// Approvals

// errNoAnswer is an engine that said 200 with no body. A command prints that as
// an empty list; a screen must not read it as "nothing is waiting" or "the kill
// switch is off".
var errNoAnswer = errors.New("the engine answered with nothing")

func answeredNothing(res observeResult) bool { return len(bytes.TrimSpace(res.raw)) == 0 }

func (s *securityScreens) readQueue() (cliApprovalList, error) {
	res, list, err := governanceApprovals(s.cmd, s.flags, url.Values{"status": {"pending"}, "limit": {"100"}})
	if err == nil && answeredNothing(res) {
		err = errNoAnswer
	}
	return list, err
}

// approvalsFrame is the queue. A queue that could not be read says why, in the
// engine's words, and has no rows.
func (s *securityScreens) approvalsFrame(list cliApprovalList, err error, reload func(string)) tui.Frame {
	if err != nil {
		return readFailedFrame(approvalsHeading, "", err)
	}
	waiting := fmt.Sprintf("%d waiting", len(list.Items))
	if list.HasMore {
		waiting = fmt.Sprintf("%d+ waiting", len(list.Items))
	}
	frame := tui.Frame{Heading: approvalsHeading, Right: waiting, Footer: "Enter reviews a request", Hints: screenHints}
	if len(list.Items) == 0 {
		frame.Footer = "Nothing is waiting for a person."
	}
	for _, a := range list.Items {
		id := a.ID
		command := "olivares governance approvals ls"
		if plainName.MatchString(id) {
			command = "olivares governance approvals get " + id
		}
		frame.Items = append(frame.Items, tui.MenuItem{
			Label:   shorten(requestTitle(a), menuLabelRunes),
			Detail:  approvalDetail(a, time.Now()),
			Command: command,
			Open:    func(h *tui.Host) string { return s.review(h, id, reload) },
		})
	}
	return frame
}

// approvals is Security › Approvals: it pushes the list, reads the queue without
// blocking the keys and shows the answer. reload re-reads it after a vote.
func (s *securityScreens) approvals(h *tui.Host) string {
	menu := h.PushMenu(tui.Frame{Heading: approvalsHeading, Footer: "Reading the engine…", Hints: screenHints})
	var reload func(notice string)
	latest := 0   // the newest read; set and read on the loop only
	carried := "" // the notice the screen shows once a read answers; also on the loop only
	reload = func(notice string) {
		if notice != "" {
			if !h.Holds(menu) {
				s.notes.note(notice) // the person left this list: the command says it when the screens close
			}
			carried = notice
		}
		latest++
		mine := latest
		go func() {
			list, err := s.readQueue()
			h.Post(func() {
				if mine != latest {
					return // a newer read answers for the screen
				}
				menu.SetFrame(s.approvalsFrame(list, err, reload))
				menu.SetNotice(carried)
				carried = ""
			})
		}()
	}
	reload("")
	return ""
}

// reviewLines is the request in full: who asked, the rule that asked, what it
// asks to do and reach, then the request's own words.
func reviewLines(a cliApproval, now time.Time) []string {
	var lines []string
	row := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			lines = append(lines, fmt.Sprintf("  %-10s %s", label, oneRow(value)))
		}
	}
	row("Asked by", a.RequestedBy)
	row("Rule", a.PolicyRef)
	row("Action", a.Action)
	row("Reaches", strings.TrimSpace(a.SubjectKind+" "+a.SubjectRef))
	row("Risk", a.RiskTier)
	row("Votes", fmt.Sprintf("%d of %d needed", a.ApproveCount, a.RequiredApprovals))
	row("Expires", expiresIn(a.ExpiresAt, now))
	// The request's own words are the last rows, in the column of the others, so
	// a request cut by a short terminal loses its end and not its start.
	for i, line := range strings.Split(termSafe(a.Reason), "\n") {
		label := ""
		if i == 0 {
			label = "Request"
		}
		lines = append(lines, fmt.Sprintf("  %-10s %s", label, oneRow(line)))
	}
	return lines
}

// voteFrom reads the review's two fields: the vote and the reason, which is
// optional. A refusal is the sentence for the footer; nothing is sent then.
func voteFrom(values []string) (decision, note, refusal string) {
	if len(values) != 2 || voteOf[values[0]] == "" {
		return "", "", "Choose Approve or Reject with ← →."
	}
	return voteOf[values[0]], strings.TrimSpace(values[1]), ""
}

func voteFields(values []string) []tui.Field {
	fields := []tui.Field{
		{Label: "Decision", Value: choosePrompt, Choices: []string{choosePrompt, "Approve", "Reject"}},
		{Label: "Reason"},
	}
	if len(values) == len(fields) {
		fields[0].Value, fields[1].Value = values[0], values[1]
	}
	return fields
}

// review reads the request again, so one that someone else decided meanwhile is
// refused here with the command's own sentence, then offers the vote.
func (s *securityScreens) review(h *tui.Host, id string, reload func(string)) string {
	p := &tui.Panel{Heading: approvalsHeading + " › Review", Lines: []string{"  Reading the request…"}, Busy: true}
	if plainName.MatchString(id) {
		p.Command = "olivares governance approvals get " + id
	}
	h.PushPanel(p)
	go func() {
		a, err := governancePendingApproval(s.cmd, s.flags, id)
		h.Post(func() {
			if !h.OnTop(p) {
				return
			}
			if err != nil {
				reload("") // the list behind may be stale: someone decided it
				showResult(p, termrender.RoleFail, "Enter goes back.", errorLines(err)...)
				return
			}
			s.offerVote(h, p, a, reload)
		})
	}()
	return ""
}

func (s *securityScreens) offerVote(h *tui.Host, p *tui.Panel, a cliApproval, reload func(string)) {
	p.Busy, p.Lines = false, reviewLines(a, time.Now())
	p.SetFields(voteFields(nil))
	p.Footer, p.FooterRole = "Choose with ← →, then Enter. The reason is optional; the ledger keeps it.", termrender.RoleNone
	p.Submit = func(values []string) {
		decision, note, refusal := voteFrom(values)
		if refusal != "" {
			p.SetFields(voteFields(values))
			p.Footer, p.FooterRole = refusal, termrender.RoleWarn
			return
		}
		answered := s.notes.pending("A decision sent as you left may have been recorded. Check olivares governance approvals ls.")
		p.SetFields(nil)
		p.Busy, p.Footer, p.FooterRole = true, "Sending your decision…", termrender.RoleNone
		go func() {
			res, after, err := governanceApprovalDecide(s.cmd, s.flags, a.ID, decision, note)
			h.Post(func() {
				answered()
				switch {
				case err == nil:
					settle(h, p, reload, false, oneRow(approvalOutcome(voteDone[decision], after)))
				case res.status >= 200 && res.status < 300:
					// The engine took the vote; only its answer could not be read.
					settle(h, p, reload, false, voteDone[decision]+" "+oneRow(a.ID)+".")
				default:
					if h.OnTop(p) {
						reload("") // someone may have decided it first
					}
					settle(h, p, reload, true, oneRow(err.Error()))
				}
			})
		}()
	}
}

// menuLabelRunes is the most a row's label may take: the list gives labels 24
// columns, two of them the gap before the detail.
const menuLabelRunes = 22

// shorten cuts s to n runes, with an ellipsis when it cut.
func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// requestTitle is the first line of what the request says, or the action it gates.
func requestTitle(a cliApproval) string {
	for _, line := range strings.Split(termSafe(a.Reason), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return oneRow(line)
		}
	}
	if a.Action != "" {
		return oneRow(a.Action)
	}
	return oneRow(a.ID)
}

func approvalDetail(a cliApproval, now time.Time) string {
	who := strings.TrimSpace(a.RequestedBy)
	if who == "" {
		who = "unknown"
	}
	// The votes and the time left come first: the requester can be a long machine
	// ID, and the row is cut at the edge of an 80-column terminal.
	detail := fmt.Sprintf("%d of %d", a.ApproveCount, a.RequiredApprovals)
	if left := expiresIn(a.ExpiresAt, now); left != "" {
		detail += " · " + left
	}
	return detail + " · " + oneRow(who)
}

// expiresIn says when a request lapses in a few words, or returns the engine's
// own text when it is not a time.
func expiresIn(at string, now time.Time) string {
	if at == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return oneRow(at)
	}
	switch left := t.Sub(now); {
	case left <= 0:
		return "expired"
	case left < time.Hour:
		return fmt.Sprintf("%d min left", int(left.Minutes())+1)
	case left < 48*time.Hour:
		return fmt.Sprintf("%d h left", int(left.Hours()))
	default:
		return fmt.Sprintf("%d days left", int(left.Hours()/24))
	}
}

// Kill switch

// killSwitchLines say what is stopped and whether Stop all is on offer. A state
// that could not be read does not take the stop away: during an incident the
// way to stop must not depend on being able to read.
func killSwitchLines(state cliKillSwitchState, err error) (lines []string, canStop bool) {
	switch {
	case err != nil:
		lines = []string{"  Could not read the kill switch: " + oneRow(err.Error()), "  You can still stop everything."}
	case state.EstateStopped:
		lines = []string{"  Everything is stopped. Every tool is denied.", "  Starting again needs two people."}
	default:
		lines = []string{"  The kill switch is off for the estate."}
	}
	for _, k := range state.Active {
		line := "  Stopped: " + oneRow(strings.TrimSpace(k.ScopeKind+" "+cmp.Or(k.AgentExternalID, k.ScopeRef)))
		if k.Reason != "" {
			line += ". Why: " + oneRow(k.Reason)
		}
		if k.EngagedBy != "" {
			line += ". By " + oneRow(k.EngagedBy)
		}
		lines = append(lines, line)
	}
	canStop = err != nil || !state.EstateStopped
	if canStop {
		lines = append(lines, "", "  Stop all denies every tool in this estate until two people start it again.")
	}
	return lines, canStop
}

// stopFrom reads Stop all's two fields. Without a reason and the exact word
// nothing is sent; the refusal is the sentence for the footer and focus is the
// field that needs another answer.
func stopFrom(values []string) (reason string, focus int, refusal string) {
	switch {
	case len(values) != 2:
		return "", 0, "Nothing was stopped: a reason and the word " + stopWord + " are needed."
	case strings.TrimSpace(values[0]) == "":
		return "", 0, "Nothing was stopped: say why. The ledger keeps your answer."
	case values[1] != stopWord:
		return "", 1, "Nothing was stopped: type the word " + stopWord + "."
	}
	return strings.TrimSpace(values[0]), 0, ""
}

// stopFields are the two fields. The word is never carried over from an earlier
// try: it is typed again each time, after the reason.
func stopFields(reason string) []tui.Field {
	return []tui.Field{{Label: "Reason", Value: reason}, {Label: "Type " + stopWord}}
}

// killSwitch is Security › Kill switch: the state, and Stop all behind a reason
// and the typed word, as confirmDestructive asks for a deed that cannot be undone.
func (s *securityScreens) killSwitch(h *tui.Host) string {
	p := &tui.Panel{Heading: "Security › Kill switch", Lines: []string{"  Reading the kill switch…"}, Busy: true,
		Command: "olivares governance killswitch state"}
	h.PushPanel(p)
	go func() {
		res, state, err := governanceKillSwitchStatus(s.cmd, s.flags)
		if err == nil && answeredNothing(res) {
			err = errNoAnswer
		}
		h.Post(func() {
			if h.OnTop(p) {
				s.offerStop(h, p, state, err)
			}
		})
	}()
	return ""
}

func (s *securityScreens) offerStop(h *tui.Host, p *tui.Panel, state cliKillSwitchState, readErr error) {
	lines, canStop := killSwitchLines(state, readErr)
	if !canStop {
		showResult(p, termrender.RoleWarn, "Kill switch: ON. Enter goes back.", lines...)
		return
	}
	p.Busy, p.Lines = false, lines
	// No command stops the estate, so the screen does not claim one.
	p.Command = ""
	p.SetFields(stopFields(""))
	p.Footer, p.FooterRole = "Say why, then type "+stopWord+". The ledger keeps your answer.", termrender.RoleNone
	p.Submit = func(values []string) {
		reason, focus, refusal := stopFrom(values)
		if refusal != "" {
			kept := ""
			if len(values) == 2 {
				kept = values[0]
			}
			p.SetFields(stopFields(kept))
			p.Focus(focus)
			p.Footer, p.FooterRole = refusal, termrender.RoleWarn
			return
		}
		answered := s.notes.pending("A stop sent as you left may have been engaged. Check olivares governance killswitch state.")
		p.SetFields(nil)
		p.Busy, p.Footer, p.FooterRole = true, "Stopping everything…", termrender.RoleNone
		go func() {
			err := governanceKillSwitchStopEstate(s.cmd, s.flags, reason)
			h.Post(func() {
				answered()
				s.stopped(h, p, err)
			})
		}()
	}
}

// stopped is the end of Stop all, decided on the loop, where the answer is
// checked against what the person did meanwhile: one who left is told by the
// command once the screens close.
func (s *securityScreens) stopped(h *tui.Host, p *tui.Panel, err error) {
	const done = "Stopped. Every tool is denied until two people start it again."
	switch {
	case err == nil && h.OnTop(p):
		showResult(p, termrender.RoleWarn, "Kill switch: ON. Enter goes back.", "  "+done)
	case err == nil:
		s.notes.note(done)
	case h.OnTop(p):
		showResult(p, termrender.RoleFail, "Enter goes back.",
			append(errorLines(err), "", "  Check with: olivares governance killswitch state")...)
	default:
		s.notes.note(oneRow(err.Error()) + " Check with: olivares governance killswitch state")
	}
}
