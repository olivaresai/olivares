// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/store"
)

// runDBCheck drives `olivares db check` and returns stdout, the code the process
// would exit with, and the message the operator would read.
func runDBCheck(t *testing.T, args ...string) (string, int, string) {
	t.Helper()
	cmd := newDBCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"check"}, args...))
	err := cmd.Execute()
	if err == nil {
		return out.String(), exitcode.OK, ""
	}
	return out.String(), exitcode.From(err), err.Error()
}

// TestDBCheckStrictSeparatesRefusedFromUnresolved pins QA-04.
//
// `db check --strict` collapsed two different facts into one sentence and one
// code: a DSN the boot guard WOULD refuse (proven, from a successful probe) and
// a DSN nobody could connect to (not probed at all). Measured on the source this
// test was written against, a run whose only finding was an unopenable SQLite
// file exited 1 saying "at least one DSN would be refused at boot" — a refusal
// that never happened — and a mixed run said the same while one of its DSNs had
// been probed and ACCEPTED.
//
// The separation is asserted through the real command with real SQLite files:
// one path that opens, and one inside a directory that does not exist.
func TestDBCheckStrictSeparatesRefusedFromUnresolved(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.db")
	unopenable := filepath.Join(dir, "no-such-directory", "bad.db")

	t.Run("every DSN accepted exits 0", func(t *testing.T) {
		out, code, msg := runDBCheck(t, "--engine", "sqlite", "--dsn", good, "--strict")
		if code != exitcode.OK {
			t.Fatalf("exit = %d, want 0: %s\n%s", code, msg, out)
		}
	})

	t.Run("an unreachable DSN is not reported as a refusal", func(t *testing.T) {
		out, code, msg := runDBCheck(t, "--engine", "sqlite", "--dsn", unopenable, "--strict")
		if code != exitcode.Server {
			t.Errorf("exit = %d, want %d (Server): %s", code, exitcode.Server, msg)
		}
		if strings.Contains(msg, "refused at boot") {
			t.Errorf("a DSN that was never probed must not be described as refused at boot: %q", msg)
		}
		if !strings.Contains(msg, "--dsn") {
			t.Errorf("the message must name the flag that could not be reached: %q", msg)
		}
		// The DSN value carries a password on a real Postgres deployment. Today's
		// message names only the flag label, and that must not regress.
		if strings.Contains(msg, unopenable) {
			t.Errorf("the message leaked the DSN value: %q", msg)
		}
		assertDBCheckTableShape(t, out, "--dsn")
	})

	t.Run("an accepted DSN beside an unreachable one is not called refused", func(t *testing.T) {
		out, code, msg := runDBCheck(t,
			"--engine", "sqlite", "--dsn", good, "--admin-dsn", unopenable, "--strict")
		if code != exitcode.Server {
			t.Errorf("exit = %d, want %d (Server): %s", code, exitcode.Server, msg)
		}
		if strings.Contains(msg, "refused at boot") {
			t.Errorf("nothing was refused in this run: %q", msg)
		}
		if !strings.Contains(msg, "--admin-dsn") {
			t.Errorf("the message must name the flag that could not be reached: %q", msg)
		}
		// Remove the label that SHOULD be there before looking for the one that
		// should not: "--admin-dsn" contains "-dsn", and a bare substring search
		// would pass whatever the message said.
		if strings.Contains(strings.ReplaceAll(msg, "--admin-dsn", ""), "--dsn") {
			t.Errorf("the accepted --dsn must not appear in the failure clause: %q", msg)
		}
		assertDBCheckTableShape(t, out, "--dsn", "--admin-dsn")
	})
}

// assertDBCheckTableShape asserts the human pane survived the repair: the table
// is still written to stdout BEFORE the command errors, and it still carries one
// row per probed flag. The machine pane has its own test below.
func assertDBCheckTableShape(t *testing.T, textOut string, wantLabels ...string) {
	t.Helper()
	if !strings.HasPrefix(textOut, "DSN") {
		t.Errorf("the text table must still be printed before the error:\n%s", textOut)
	}
	for _, label := range wantLabels {
		if !strings.Contains(textOut, label) {
			t.Errorf("the table lost the %s row:\n%s", label, textOut)
		}
	}
}

// TestDBCheckStrictJSONPaneIsUnchanged is the preservation half: the wire
// contract carried the refused/unresolved distinction all along
// (`reachable:false` versus `reachable:true && accepted:false`), so repairing
// the exit code must not move a single JSON field.
func TestDBCheckStrictJSONPaneIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.db")
	unopenable := filepath.Join(dir, "no-such-directory", "bad.db")

	out, code, msg := runDBCheck(t,
		"--engine", "sqlite", "--dsn", good, "--admin-dsn", unopenable, "--strict", "--format", "json")
	if code != exitcode.Server {
		t.Fatalf("exit = %d, want %d (Server): %s", code, exitcode.Server, msg)
	}
	var results []dbCheckResult
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&results); err != nil {
		t.Fatalf("db check JSON is invalid: %v\n%s", err, out)
	}
	if len(results) != 2 {
		t.Fatalf("db check JSON result count = %d, want 2: %#v", len(results), results)
	}
	if results[0].DSN != "--dsn" || !results[0].Reachable || !results[0].Accepted {
		t.Errorf("the accepted probe changed shape: %#v", results[0])
	}
	if !strings.HasPrefix(results[0].Verdict, "OK — ") {
		t.Errorf("the accepted verdict string changed: %q", results[0].Verdict)
	}
	if results[1].DSN != "--admin-dsn" || results[1].Reachable || results[1].Accepted {
		t.Errorf("the unreachable probe changed shape: %#v", results[1])
	}
	if !strings.HasPrefix(results[1].Verdict, "UNREACHABLE — ") {
		t.Errorf("the unreachable verdict string changed: %q", results[1].Verdict)
	}
}

// TestCheckVerdictClass pins the three-way classification itself, on the same
// fabricated postures TestCheckVerdict already uses. It is the unit half of the
// row the process half cannot reach: a PROVEN refusal needs a live Postgres role
// with real attributes, and no mock of a probe would prove anything about the
// probe. What this asserts is the rule applied to a posture, which is exactly
// what checkVerdictClass owns.
func TestCheckVerdictClass(t *testing.T) {
	for _, c := range []struct {
		name    string
		posture store.RolePosture
		admin   bool
		want    dbVerdictClass
	}{
		{"app rls-safe", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app"}, false, dbVerdictOK},
		{"app superuser", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "postgres", Superuser: true}, false, dbVerdictRefused},
		{"app bypassrls", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, BypassRLS: true}, false, dbVerdictRefused},
		{"app replica", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app", ReplicationRole: "replica"}, false, dbVerdictRefused},
		{"admin bypassrls", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_admin", BypassRLS: true}, true, dbVerdictOK},
		{"admin not bypassrls", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app"}, true, dbVerdictRefused},
		{"admin superuser", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Superuser: true, BypassRLS: true}, true, dbVerdictRefused},
		{"sqlite reachable", store.RolePosture{Engine: store.EngineSQLite, Reachable: true}, false, dbVerdictOK},
		// The distinction the boolean could not carry: unreachable is NOT refused.
		{"postgres unreachable", store.RolePosture{Engine: store.EnginePostgres, Err: "dial tcp: connection refused"}, false, dbVerdictUnresolved},
		{"sqlite unreachable", store.RolePosture{Engine: store.EngineSQLite, Err: "unable to open database file"}, false, dbVerdictUnresolved},
		{"admin unreachable", store.RolePosture{Engine: store.EnginePostgres, Err: "boom"}, true, dbVerdictUnresolved},
	} {
		t.Run(c.name, func(t *testing.T) {
			verdict, got := checkVerdictClass(c.posture, c.admin)
			if got != c.want {
				t.Errorf("class = %v, want %v (verdict %q)", got, c.want, verdict)
			}
			// checkVerdict must stay a derivation, not a second copy of the rules.
			sameVerdict, accepted := checkVerdict(c.posture, c.admin)
			if sameVerdict != verdict || accepted != (c.want == dbVerdictOK) {
				t.Errorf("checkVerdict drifted from checkVerdictClass: %q/%v vs %q/%v",
					sameVerdict, accepted, verdict, c.want)
			}
		})
	}
}

// TestStrictVerdictAggregate pins the precedence and the sentence, including the
// mixed case that needs a proven refusal beside an unresolved probe — the one
// combination no SQLite fixture can produce, because a refusal is a statement
// about Postgres role attributes.
func TestStrictVerdictAggregate(t *testing.T) {
	for _, c := range []struct {
		name         string
		refused      []string
		unresolved   []string
		wantCode     int
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:     "nothing to report",
			wantCode: exitcode.OK,
		},
		{
			name: "one proven refusal", refused: []string{"--dsn"},
			wantCode:     exitcode.Err,
			wantContains: []string{"--dsn", "would be refused at boot"},
			wantAbsent:   []string{"could not be reached"},
		},
		{
			name: "only unresolved", unresolved: []string{"--admin-dsn"},
			wantCode:     exitcode.Server,
			wantContains: []string{"--admin-dsn", "could not be reached", "not a refusal"},
			wantAbsent:   []string{"would be refused at boot"},
		},
		{
			// Precedence, and the reason for it: a role that is SUPERUSER does not
			// become acceptable by retrying, so the run must not exit on the
			// retryable code just because something else was unreachable.
			name:    "a proven refusal outranks an unresolved probe",
			refused: []string{"--dsn"}, unresolved: []string{"--admin-dsn"},
			wantCode: exitcode.Err,
			// Both classes, in their own clauses, so a mixed run reads as one.
			wantContains: []string{"--dsn would be refused at boot", "--admin-dsn could not be reached"},
		},
		{
			name:    "every class can name more than one flag",
			refused: []string{"--dsn", "--owner-dsn"}, unresolved: []string{"--admin-dsn"},
			wantCode:     exitcode.Err,
			wantContains: []string{"--dsn, --owner-dsn would be refused at boot", "--admin-dsn could not be reached"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := strictVerdictError(c.refused, c.unresolved)
			if c.wantCode == exitcode.OK {
				if err != nil {
					t.Fatalf("a clean run must not error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected exit %d, got nil", c.wantCode)
			}
			if got := exitcode.From(err); got != c.wantCode {
				t.Errorf("exit = %d, want %d: %v", got, c.wantCode, err)
			}
			for _, want := range c.wantContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q does not contain %q", err.Error(), want)
				}
			}
			for _, absent := range c.wantAbsent {
				if strings.Contains(err.Error(), absent) {
					t.Errorf("message %q must not contain %q", err.Error(), absent)
				}
			}
		})
	}
}
