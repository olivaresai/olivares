// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// announceFixture builds the smallest moduleSet that REACHES the announcement.
// wireSessionGovernance returns early on a nil sessions module, and the first version of
// this fixture passed moduleSet{} — every assertion below would have been vacuous, and the
// vacuity guard at the bottom is what caught it rather than a green.
func announceFixture() moduleSet { return moduleSet{sessions: sessions.New()} }

// A FAIL-OPEN POSTURE IS NOT PART OF "WIRED" (2026-08-06).
//
// wireSessionGovernance announced its result as one INFO line — "session governance wired"
// — carrying nine key/value pairs, two of which were budget_posture and context_posture.
// A posture of fail-open means that when the process cannot READ the budget ledger or the
// context policy, the launch is ALLOWED. On the community edition that is the DEFAULT, so
// the operator most likely to be running it is the one who configured nothing.
//
// Announcing "I will decline to enforce when I cannot see" as a pair inside a message that
// reads like good news is the fourth answer wearing the first one's clothes. This is the
// same log-level rule the repository already applies in modules/models — a 503 is WARN, a
// 200 with an honest body is INFO — applied where it had not been.
//
// Two directions, and the second is what makes the first mean something: fail-closed must
// stay quiet, or a warning that is always printed is wallpaper.
func TestFailOpenPosturesAreAnnouncedAtWarnOnTheirOwnLine(t *testing.T) {
	for _, tc := range []struct {
		name       string
		budgetEnv  string
		contextEnv string
		wantWarns  int
		wantNames  []string
	}{
		{
			name:       "both fail-open: one WARN each, naming the control and the switch",
			budgetEnv:  "fail-open",
			contextEnv: "fail-open",
			wantWarns:  2,
			wantNames:  []string{"budget", "context policy", "OLIVARES_SESSION_BUDGET_AVAILABILITY", "OLIVARES_SESSION_CONTEXT_AVAILABILITY"},
		},
		{
			name:       "one of each: exactly one WARN, and it is the fail-open one",
			budgetEnv:  "fail-open",
			contextEnv: "fail-closed",
			wantWarns:  1,
			wantNames:  []string{"budget"},
		},
		{
			name:       "both fail-closed: nothing to warn about",
			budgetEnv:  "fail-closed",
			contextEnv: "fail-closed",
			wantWarns:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			env := map[string]string{
				envSessionBudgetAvailability:  tc.budgetEnv,
				envSessionContextAvailability: tc.contextEnv,
			}
			wireSessionGovernance(announceFixture(), nil, nil, nil, func(k string) string { return env[k] }, log)

			out := buf.String()
			warns := strings.Count(out, "session launch gate is FAIL-OPEN")
			if warns != tc.wantWarns {
				t.Errorf("FAIL-OPEN warnings = %d, want %d\n%s", warns, tc.wantWarns, out)
			}
			for _, n := range tc.wantNames {
				if !strings.Contains(out, n) {
					t.Errorf("the warning does not name %q — an operator cannot act on it:\n%s", n, out)
				}
			}
			for _, line := range strings.Split(out, "\n") {
				if !strings.Contains(line, "session launch gate is FAIL-OPEN") {
					continue
				}
				// The LEVEL is the whole point: this used to travel inside an INFO.
				if !strings.Contains(line, "level=WARN") {
					t.Errorf("the fail-open notice is not WARN:\n%s", line)
				}
			}
		})
	}
}

// wireSessionGovernance returns early on a nil sessions module, so the fixture above must
// actually reach the announcement — otherwise every assertion is vacuous and the whole test
// is a green that measured nothing.
func TestFailOpenAnnouncementFixtureIsNotVacuous(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	wireSessionGovernance(announceFixture(), nil, nil, nil, func(string) string { return "fail-open" }, log)
	if !strings.Contains(buf.String(), "session governance wired") {
		t.Fatalf("the fixture never reached the announcement, so the sibling test asserts nothing:\n%s", buf.String())
	}
}
