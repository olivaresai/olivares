// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui_security_ledger.go is Security › Audit ledger (CLI-CONCEPT §5.6): the
// newest events with a filter, and Verify now. cmd_ui_security.go holds the
// Security menu, the approvals and the kill switch.

// ledgerFilter is what the person typed for the ledger list. since holds the
// time it resolved to, so the list stays the same while the person reads it;
// sinceText is what was typed.
type ledgerFilter struct {
	person    string
	tool      string
	action    string
	since     string
	sinceText string
}

// query turns the filter into the API's own filters. The ledger has no tool
// column, so the tool is searched as text across actor, action and target.
func (f ledgerFilter) query() url.Values {
	q := url.Values{}
	for name, value := range map[string]string{"actor": f.person, "q": f.tool, "action": f.action, "since": f.since} {
		if value != "" {
			q.Set(name, value)
		}
	}
	return q
}

func (f ledgerFilter) summary() string {
	var parts []string
	for _, p := range []struct{ name, value string }{{"person", f.person}, {"text", f.tool}, {"action", f.action}, {"since", f.sinceText}} {
		if p.value != "" {
			parts = append(parts, p.name+" "+oneRow(p.value))
		}
	}
	return strings.Join(parts, ", ")
}

// fromAnswers sets the filter from the Filter panel's four fields (person, text,
// action, since). A bad answer leaves the filter as it was.
func (f *ledgerFilter) fromAnswers(values []string, now time.Time) error {
	if len(values) != 4 {
		return sentence(exitcode.Usage, "The filter needs four answers; it got %d.", len(values))
	}
	since, err := parseSince(values[3], now)
	if err != nil {
		return err
	}
	*f = ledgerFilter{
		person: strings.TrimSpace(values[0]), tool: strings.TrimSpace(values[1]), action: strings.TrimSpace(values[2]),
		since: since, sinceText: strings.TrimSpace(values[3]),
	}
	return nil
}

func (f ledgerFilter) fields() []tui.Field {
	return []tui.Field{{Label: "Person", Value: f.person}, {Label: "Text", Value: f.tool}, {Label: "Action", Value: f.action}, {Label: "Since", Value: f.sinceText}}
}

// parseSince reads the "since" a person types: a length of time back from now (2h,
// 90m, 3d), a date (midnight in the zone of now, which is this computer's zone
// where the list shows its times), or a full RFC 3339 time. It returns the RFC 3339
// time the ledger API wants, or "" for no limit.
func parseSince(in string, now time.Time) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, in); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", in, now.Location()); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	var back time.Duration
	if days, ok := strings.CutSuffix(in, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 && n <= 3650 {
			back = time.Duration(n) * 24 * time.Hour
		}
	} else if d, err := time.ParseDuration(in); err == nil && d > 0 {
		back = d
	}
	if back == 0 {
		return "", sentence(exitcode.Usage, "Since %q is not a time. Use 2h, 90m, 3d, a date like 2026-10-01 or a time like 2026-10-01T08:30:00Z.", oneRow(in))
	}
	return now.Add(-back).UTC().Format(time.RFC3339), nil
}

// auditConfig is the client `audit ls` reads the ledger with, resolved from the
// same environment and saved sign-in as every other command.
func (s *securityScreens) auditConfig() (agentClientConfig, error) {
	opts, err := s.flags.resolutionOptions(s.cmd)
	if err != nil {
		return agentClientConfig{}, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return agentClientConfig{}, err
	}
	cfg := agentClientConfig{server: resolved.Server, token: resolved.Token, tenant: resolved.Tenant,
		caCert: resolved.CACert, pins: resolved.PinSHA256, timeout: s.flags.timeout}
	if err := cfg.resolve(); err != nil {
		return agentClientConfig{}, err
	}
	return cfg, nil
}

// readLedger reads the newest events that match the filter, as `audit ls` does.
func (s *securityScreens) readLedger(f ledgerFilter) ([]auditRow, bool, error) {
	cfg, err := s.auditConfig()
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(s.cmd.Context(), ledgerTimeout)
	defer cancel()
	return auditRecentEvents(ctx, &cfg, false, false, ledgerRows, f.query())
}

// verify runs `olivares audit verify` itself and returns what it printed with
// the status on top. Like the command, it reads the ledger kept on this computer.
func (s *securityScreens) verify() (report, status string, err error) {
	cmd := newAuditCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(s.cmd.Context())
	cmd.SetArgs([]string{"verify"})
	runErr := cmd.Execute()
	// The report is what the command prints on stdout; a warning on stderr must
	// not keep the status from being read.
	printed := strings.TrimSpace(out.String())
	if runErr != nil {
		if said := strings.TrimSpace(printed + "\n" + errOut.String()); said != "" {
			return "", "", fmt.Errorf("%w\n%s", runErr, said)
		}
		return "", "", runErr
	}
	var parsed struct {
		Status string `json:"status"`
	}
	status = "not reported"
	if json.Unmarshal([]byte(printed), &parsed) == nil && parsed.Status != "" {
		status = oneRow(parsed.Status)
	}
	return verifyReport(status, printed), status, nil
}

// verifyReport is the screen for a finished verify: the status, what it means
// when it is not a clean ok, which ledger it read, and the command's own report.
func verifyReport(status, printed string) string {
	lines := []string{"Ledger status: " + status}
	switch status {
	case "unattested":
		lines = append(lines, "The chain and its signatures checked out, but no signed checkpoint covers the ledger yet.")
	case "corrupt":
		lines = append(lines, "The ledger failed a check. Do not rely on it until the report below is explained.")
	}
	lines = append(lines,
		"This is the ledger kept on this computer, checked with the keys of this engine (advisory).",
		"", "The rest is what olivares audit verify prints:", "", printed)
	return strings.Join(lines, "\n")
}

// auditStamp shortens an event time to the month, day and minute.
func auditStamp(s string) string {
	if t := auditTime(s); len(t) == len("2006-01-02 15:04:05") {
		return t[5:16]
	}
	return s
}

// ledgerScreen is the state of one open Audit ledger: what the person filtered
// and the last Verify now, which stay when the list is read again.
type ledgerScreen struct {
	s        *securityScreens
	h        *tui.Host
	menu     *tui.Menu
	filter   ledgerFilter
	verified string
	latest   int
	carried  string // the notice shown once a read answers; set and read on the loop only
}

// ledger is Security › Audit ledger: the newest events, a filter and Verify now.
func (s *securityScreens) ledger(h *tui.Host) string {
	l := &ledgerScreen{s: s, h: h}
	l.menu = h.PushMenu(tui.Frame{Heading: ledgerHeading, Footer: "Reading the ledger…", Hints: screenHints})
	l.reload("")
	return ""
}

// reload reads the ledger again with the filter as it is now.
func (l *ledgerScreen) reload(notice string) {
	if notice != "" {
		if !l.h.Holds(l.menu) {
			l.s.notes.note(notice)
		}
		l.carried = notice
	}
	l.latest++
	mine, filter := l.latest, l.filter
	go func() {
		rows, complete, err := l.s.readLedger(filter)
		l.h.Post(func() {
			if mine != l.latest {
				return
			}
			if err != nil {
				// The actions stay: a filter that cannot be read can be cleared.
				failed := readFailedFrame(ledgerHeading, "", err)
				failed.Items, failed.Hints = l.actionRows(), screenHints
				l.menu.SetFrame(failed)
			} else {
				l.menu.SetFrame(l.frame(rows, complete))
			}
			l.menu.SetNotice(l.carried)
			l.carried = ""
		})
	}()
}

// actionRows are Filter and Verify now, and Clear filter while a filter applies.
func (l *ledgerScreen) actionRows() []tui.MenuItem {
	rows := []tui.MenuItem{
		{Label: "Filter", Detail: "by person, text, action or time", Command: "olivares audit ls", Open: l.openFilter},
	}
	if auditVerificationAvailable {
		rows = append(rows, tui.MenuItem{Label: "Verify now", Detail: "check the ledger with olivares audit verify", Command: "olivares audit verify", Open: l.openVerify})
	}
	if l.filter != (ledgerFilter{}) {
		rows = append(rows, tui.MenuItem{
			Label: "Clear filter", Detail: l.filter.summary(), Command: "olivares audit ls",
			Open: func(*tui.Host) string {
				l.filter = ledgerFilter{}
				l.reload("Filter cleared.")
				return ""
			},
		})
	}
	return rows
}

// frame is the list: Filter and Verify now first, because the events are many,
// then the events, newest first.
func (l *ledgerScreen) frame(rows []auditRow, complete bool) tui.Frame {
	frame := tui.Frame{Heading: ledgerHeading, Right: fmt.Sprintf("%d shown", len(rows)), Hints: screenHints}
	switch {
	case len(rows) == 0:
		frame.Right = "no events"
	case len(rows) == ledgerRows:
		frame.Right = fmt.Sprintf("newest %d", ledgerRows)
	}
	frame.Items = l.actionRows()
	for _, r := range rows {
		// The label column is short: the time is the label, and the rest of the
		// event reads across the detail, action first.
		target := strings.TrimSpace(r.TargetKind + " " + r.TargetID)
		var parts []string
		for _, part := range []string{r.Action, r.Actor, target} {
			if part = strings.TrimSpace(oneRow(part)); part != "" {
				parts = append(parts, part)
			}
		}
		frame.Items = append(frame.Items, tui.MenuItem{
			Label:   oneRow(auditStamp(r.OccurredAt)),
			Detail:  strings.Join(parts, "  "),
			Command: "olivares audit ls",
		})
	}
	frame.Footer = "Newest first"
	if !complete {
		frame.Footer, frame.FooterRole = "Scan stopped early; the newest events may be missing", termrender.RoleWarn
	}
	if sum := l.filter.summary(); sum != "" {
		frame.Footer += " · filter: " + sum
	}
	if l.verified != "" {
		frame.Footer += " · " + l.verified
	}
	return frame
}

func (l *ledgerScreen) openFilter(h *tui.Host) string {
	p := &tui.Panel{
		Heading: ledgerHeading + " › Filter",
		Lines: []string{"  Leave a field empty for any. Text is searched in the person, the action and the target.",
			"  Since takes 2h, 3d, a date like 2026-10-01 or a time like 2026-10-01T08:30:00Z."},
		Footer:  "Enter moves on; Enter on the last field applies the filter.",
		Command: "olivares audit ls",
	}
	p.SetFields(l.filter.fields())
	p.Submit = func(values []string) {
		next := l.filter
		if err := next.fromAnswers(values, time.Now()); err != nil {
			p.SetFields(fieldsWith(l.filter.fields(), values))
			p.Focus(3) // the only answer that can be refused is the time
			p.Footer, p.FooterRole = oneRow(err.Error()), termrender.RoleWarn
			return
		}
		l.filter = next
		text := "Filter set."
		if next == (ledgerFilter{}) {
			text = "Filter cleared."
		}
		settle(h, p, l.reload, false, text)
	}
	h.PushPanel(p)
	return ""
}

// fieldsWith is fields holding what the person typed.
func fieldsWith(fields []tui.Field, values []string) []tui.Field {
	for i := range fields {
		if i < len(values) {
			fields[i].Value = values[i]
		}
	}
	return fields
}

// openVerify runs `olivares audit verify` itself, so it can only show what that
// command shows, and keeps the result in the list's footer.
func (l *ledgerScreen) openVerify(h *tui.Host) string {
	p := &tui.Panel{
		Heading: ledgerHeading + " › Verify", Lines: []string{"  Checking the ledger…"}, Busy: true,
		Command: "olivares audit verify",
		OnClose: func() { l.reload("") },
	}
	h.PushPanel(p)
	go func() {
		report, status, err := l.s.verify()
		h.Post(func() {
			if err == nil {
				l.verified = "last verify " + time.Now().Format("15:04") + ": " + status
			}
			switch {
			case !h.OnTop(p):
				// The person left while it ran: the result is the list's notice, or the
				// command's sentence once the list is gone.
				result := "Ledger status: " + status + "."
				if err != nil {
					result = "Verify did not finish: " + oneRow(err.Error())
				}
				l.reload(result)
			case err != nil:
				showResult(p, termrender.RoleFail, "Enter goes back.", errorLines(err)...)
			default:
				role := termrender.RoleWarn
				if status == "ok" {
					role = termrender.RoleOK
				}
				var lines []string
				for _, line := range strings.Split(report, "\n") {
					lines = append(lines, "  "+oneRow(line))
				}
				showResult(p, role, "Ledger status: "+status+". Enter goes back.", lines...)
			}
		})
	}()
	return ""
}
