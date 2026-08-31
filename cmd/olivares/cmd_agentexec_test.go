// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// These tests pin the decisions the EIGHT families of the agent-execution lot
// share, so a change to cmd_agentexec.go cannot quietly break all of them at
// once. Each is written so a mutation kills it: swap Degraded for OK on a 202,
// drop the confirmDestructive call, stop escaping a positional id, or send an
// untyped flag in a PATCH, and one of these fails.
//
// EVERY REFUSAL TEST COUNTS REQUESTS. Without the counter, "the command failed"
// is satisfied just as well by a command that is simply broken, and each refusal
// is therefore paired with the control that proves the same path SUCCEEDS when
// the caller has what it needs.

// lot3Args appends the client flags every verb in this lot needs.
func lot3Args(server string, args ...string) []string {
	return append(args, "--server", server, "--token", "test-token", "--tenant", "tenant-a")
}

// lot3Server records how many requests reached it, and what the last one was.
type lot3Server struct {
	*httptest.Server
	calls  atomic.Int64
	method atomic.Value // string
	path   atomic.Value // string
	query  atomic.Value // string
	body   atomic.Value // string
}

func newLot3Server(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *lot3Server {
	t.Helper()
	cs := &lot3Server{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.calls.Add(1)
		cs.method.Store(r.Method)
		// EscapedPath, not Path. r.URL.Path is DECODED, so a %2F an id was escaped
		// into reappears there as a slash and an escaping test reads as a failure
		// while the escaping worked. What the router sees is the escaped form —
		// chi routes on RawPath when it is present — so that is what is recorded.
		cs.path.Store(r.URL.EscapedPath())
		cs.query.Store(r.URL.RawQuery)
		raw, _ := io.ReadAll(r.Body)
		cs.body.Store(string(raw))
		handler(w, r)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (c *lot3Server) lastPath() string  { s, _ := c.path.Load().(string); return s }
func (c *lot3Server) lastQuery() string { s, _ := c.query.Load().(string); return s }
func (c *lot3Server) lastBody() string  { s, _ := c.body.Load().(string); return s }

// lot3OK is the handler for the "with the right, it works" half of every pair.
func lot3OK(payload string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}
}

// lot3WriteTempJSON drops a document on disk for the --*-file flags. The file path,
// not stdin, is the default in these tests: execRoot leaves stdin as the process
// stdin, and a test that relied on it would be reading whatever the test binary
// was launched with.
func lot3WriteTempJSON(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp document: %v", err)
	}
	return path
}

// lot3ReadVerbs is one read verb per family. Refusal rules must hold for ALL EIGHT,
// not for whichever one happened to be tested.
var lot3ReadVerbs = map[string][]string{
	"orchestration": {"orchestration", "schedules", "ls"},
	"sandbox":       {"sandbox", "scenarios", "ls"},
	"recording":     {"recording", "sessions", "ls"},
	"deploy":        {"deploy", "definitions", "ls"},
	"redteam":       {"redteam", "targets", "ls"},
	"voice":         {"voice", "sessions", "ls"},
	"claude-policy": {"claude-policy", "versions", "ls", "managed-settings"},
	"claude-agents": {"claude-agents", "sessions", "events", "sess-1"},
}

// TestAgentExecFamiliesRefuseWithoutACredentialBeforeOpeningAConnection is D-1.
// A caller with no token must not even learn that the host answers.
func TestAgentExecFamiliesRefuseWithoutACredentialBeforeOpeningAConnection(t *testing.T) {
	for family, verb := range lot3ReadVerbs {
		t.Run(family, func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[]}`))
			t.Setenv("OLIVARES_TOKEN", "")
			t.Setenv("OLIVARES_TENANT", "")

			args := append(append([]string{}, verb...), "--server", srv.URL, "--tenant", "tenant-a")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("a verb with no credential must not succeed")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
			if n := srv.calls.Load(); n != 0 {
				t.Fatalf("%d request(s) reached the server; an unauthenticated caller must not open a connection", n)
			}

			// THE CONTROL: the same verb, with the credential, reaches the engine
			// exactly once and succeeds. Without this, "0 requests" would also be
			// satisfied by a command that is simply broken.
			if _, _, err := execRoot(t, lot3Args(srv.URL, verb...)...); err != nil {
				t.Fatalf("with a credential the same verb must succeed, got %v", err)
			}
			if n := srv.calls.Load(); n != 1 {
				t.Fatalf("the authenticated call made %d requests, want exactly 1", n)
			}
		})
	}
}

// TestAgentExecMapsServerRefusalsToTheExitContract is D-2: the generic statuses
// keep the codes the rest of the CLI gives them, through this lot's client.
func TestAgentExecMapsServerRefusalsToTheExitContract(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, exitcode.Auth},
		{http.StatusForbidden, exitcode.Auth},
		{http.StatusNotFound, exitcode.NotFound},
		{http.StatusConflict, exitcode.Conflict},
		{http.StatusInternalServerError, exitcode.Server},
		{http.StatusBadGateway, exitcode.Server},
		{http.StatusServiceUnavailable, exitcode.Server},
	} {
		t.Run(fmt.Sprintf("http-%d", tc.status), func(t *testing.T) {
			srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"message":"refused"}}`)
			})
			out, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "ls")...)
			if err == nil {
				t.Fatalf("HTTP %d must not exit 0", tc.status)
			}
			if got := exitcode.From(err); got != tc.want {
				t.Fatalf("exit = %d, want %d", got, tc.want)
			}
			if strings.TrimSpace(out) != "" {
				t.Fatalf("a refused request must print nothing on stdout, got:\n%s", out)
			}
		})
	}
}

// lot3TwoPhaseVerbs are the four governed actuations of the lot. All four answer 202
// when an approval is opened and NOTHING is actuated.
var lot3TwoPhaseVerbs = map[string][]string{
	"orchestration-fire":  {"orchestration", "schedules", "fire", "sc-1"},
	"orchestration-run":   {"orchestration", "workflows", "run", "wf-1"},
	"deploy-apply":        {"deploy", "apply", "dep-1"},
	"voice-sessions-open": {"voice", "sessions", "open", "--session-ref", "vs-1", "--agent-ref", "agent-a", "--model-ref", "m1", "--provider-ref", "p1"},
}

// TestAgentExecPendingApprovalExitsDegraded is D-3, the central exit-code
// decision of the lot: a 202 actuated nothing, so it must not exit 0.
func TestAgentExecPendingApprovalExitsDegraded(t *testing.T) {
	for name, verb := range lot3TwoPhaseVerbs {
		t.Run(name, func(t *testing.T) {
			srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"op":"apply","op_status":"requested","status":"requested",
					"requires_approval":true,"approval_ref":"ap-9","gate_status":"pending",
					"plan_hash":"ph-1","detail":"waiting on a human"}`)
			})
			out, _, err := execRoot(t, lot3Args(srv.URL, verb...)...)
			if err == nil {
				t.Fatal("a 202 actuated nothing and must not exit 0")
			}
			if got := exitcode.From(err); got != exitcode.Degraded {
				t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
			}
			if !strings.Contains(out, "NOT DONE") {
				t.Errorf("stdout must say the effect did not happen, got:\n%s", out)
			}
			if !strings.Contains(out, "ap-9") {
				t.Errorf("stdout must carry the approval ref a caller repeats with, got:\n%s", out)
			}
		})
	}
}

// TestAgentExecCompletedActuationExitsZero is the counterweight to D-3. Without
// it, "always return Degraded" would satisfy the test above.
func TestAgentExecCompletedActuationExitsZero(t *testing.T) {
	for name, verb := range lot3TwoPhaseVerbs {
		t.Run(name, func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"op":"apply","op_status":"applied","status":"applied",
				"gate_status":"approved","dispatch_ref":"d-1","plan_hash":"ph-1"}`))
			out, _, err := execRoot(t, lot3Args(srv.URL,
				append(append([]string{}, verb...), "--approval-ref", "ap-9")...)...)
			if err != nil {
				t.Fatalf("a completed actuation must exit 0, got %v (code %d)", err, exitcode.From(err))
			}
			if !strings.Contains(out, "applied") {
				t.Errorf("stdout should report the outcome, got:\n%s", out)
			}
			if !strings.Contains(srv.lastBody(), "ap-9") {
				t.Errorf("the approval ref must reach the engine, body was: %s", srv.lastBody())
			}
		})
	}
}

// TestAgentExecKillSwitchExitsConflict is D-4: a 423 is a state conflict, not a
// generic failure, and the operator must be told nothing was actuated.
func TestAgentExecKillSwitchExitsConflict(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"op":"fire","op_status":"blocked","detail":"estate stop sw-1 is active"}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "orchestration", "schedules", "fire", "sc-1")...)
	if err == nil {
		t.Fatal("a kill-switched actuation must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Fatalf("exit = %d, want %d (conflict)", got, exitcode.Conflict)
	}
	if !strings.Contains(err.Error(), "sw-1") {
		t.Errorf("the refusal must name what is stopping it, got: %v", err)
	}
	if !strings.Contains(err.Error(), "no effect was actuated") {
		t.Errorf("the refusal must say nothing happened, got: %v", err)
	}
}

// TestAgentExecUnprocessableArgumentExitsUsage is D-5: a 422 is the caller's
// argument, so a script must stop rather than retry.
func TestAgentExecUnprocessableArgumentExitsUsage(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"message":"agent_ref does not resolve to an agent in this tenant's inventory"}}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL,
		"redteam", "targets", "register", "--agent-ref", "ghost", "--name", "n")...)
	if err == nil {
		t.Fatal("a 422 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit = %d, want %d (usage)", got, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "inventory") {
		t.Errorf("the engine's own reason must survive, got: %v", err)
	}
}

// TestAgentExecUnimplementedNamesTheBoundary is D-6: a 501 is a wiring/edition
// boundary and must not read as "request failed".
func TestAgentExecUnimplementedNamesTheBoundary(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"error":{"message":"no summarizer configured"}}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL,
		"recording", "sessions", "summarize", "rs-1")...)
	if err == nil {
		t.Fatal("a 501 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Err {
		t.Fatalf("exit = %d, want %d", got, exitcode.Err)
	}
	if !strings.Contains(err.Error(), "not a fault") {
		t.Errorf("a 501 must be explained as a boundary, got: %v", err)
	}
	if !strings.Contains(err.Error(), "no summarizer configured") {
		t.Errorf("the engine's own reason must survive, got: %v", err)
	}
}

// TestAgentExecStepUpRefusalNamesTheCeremony is D-7. Reported as a bare 403, this
// sends an operator hunting a role nobody removed.
func TestAgentExecStepUpRefusalNamesTheCeremony(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"step_up_required","message":"hardware step-up required"}}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "apply", "dep-1")...)
	if err == nil {
		t.Fatal("a step-up refusal must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Fatalf("exit = %d, want %d (auth) — 403 keeps its code", got, exitcode.Auth)
	}
	if !strings.Contains(err.Error(), "step-up") {
		t.Errorf("the refusal must name the step-up, got: %v", err)
	}
	if !strings.Contains(err.Error(), "your role is not the problem") {
		t.Errorf("the refusal must rule out the wrong diagnosis, got: %v", err)
	}
}

// TestAgentExecGateDenialIsExplainedNotJustDenied is D-8: a governance denial and
// a missing permission are different problems with different remedies.
func TestAgentExecGateDenialIsExplainedNotJustDenied(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"op":"apply","status":"blocked","requires_approval":true,
			"approval_ref":"ap-9","gate_status":"denied","detail":"denied by default"}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "apply", "dep-1", "--approval-ref", "ap-9")...)
	if err == nil {
		t.Fatal("a denied gate must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Fatalf("exit = %d, want %d (auth)", got, exitcode.Auth)
	}
	for _, want := range []string{"refused by governance, not by your role", "denied", "ap-9", "nothing was actuated"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must contain %q, got: %v", want, err)
		}
	}

	// THE CONTROL: an ORDINARY 403 keeps httpErr's wording. Without it, this
	// classifier could rewrite every permission denial as a governance decision.
	plain := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"permission denied"}}`)
	})
	_, _, perr := execRoot(t, lot3Args(plain.URL, "deploy", "apply", "dep-1")...)
	if perr == nil || exitcode.From(perr) != exitcode.Auth {
		t.Fatalf("an ordinary 403 must still exit %d, got %v", exitcode.Auth, perr)
	}
	if strings.Contains(perr.Error(), "refused by governance") {
		t.Errorf("an ordinary permission denial must NOT be dressed up as a gate decision, got: %v", perr)
	}
}

// lot3DestructiveVerbs are the four irreversible verbs of the lot. THREE OF THEM ARE
// POSTs: a gate built by counting DELETEs would have caught only `definitions rm`.
var lot3DestructiveVerbs = map[string][]string{
	"deploy-definitions-rm":  {"deploy", "definitions", "rm", "dep-1"},
	"deploy-retire":          {"deploy", "retire", "dep-1"},
	"deploy-rollback":        {"deploy", "rollback", "dep-1", "--to-version", "4"},
	"sandbox-scenario-arch":  {"sandbox", "scenarios", "archive", "sc-1"},
	"redteam-target-authorz": {"redteam", "targets", "authorize", "rt-1"},
}

// TestAgentExecDestructiveVerbsRefuseUnattendedConsent is D-9. A pipe is answered by EOF,
// which is the absence of a human, not consent.
func TestAgentExecDestructiveVerbsRefuseUnattendedConsent(t *testing.T) {
	for name, verb := range lot3DestructiveVerbs {
		t.Run(name, func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"id":"x","status":"ok"}`))
			_, _, err := execRoot(t, lot3Args(srv.URL, verb...)...)
			if err == nil {
				t.Fatal("a destructive verb must not run unattended without --yes")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit = %d, want %d (usage)", got, exitcode.Usage)
			}
			if n := srv.calls.Load(); n != 0 {
				t.Fatalf("%d request(s) reached the server before consent was given", n)
			}

			// THE CONTROL: with --yes it goes through, exactly once.
			if _, _, err := execRoot(t, lot3Args(srv.URL,
				append(append([]string{}, verb...), "--yes")...)...); err != nil {
				t.Fatalf("with --yes the same verb must succeed, got %v", err)
			}
			if n := srv.calls.Load(); n != 1 {
				t.Fatalf("the consented call made %d requests, want exactly 1", n)
			}
		})
	}
}

// TestAgentExecNonDestructiveTwinsAreNotGated is the other half of D-9: the confirmation
// is on the dangerous DIRECTION only. `redteam targets revoke` withdraws consent
// and must never be harder to reach than granting it.
func TestAgentExecNonDestructiveTwinsAreNotGated(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"rt-1","authorized":false,"status":"registered"}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL, "redteam", "targets", "revoke", "rt-1")...); err != nil {
		t.Fatalf("revoking consent must not require a ceremony, got %v", err)
	}
	if n := srv.calls.Load(); n != 1 {
		t.Fatalf("revoke made %d requests, want 1", n)
	}
	if !strings.Contains(srv.lastBody(), `"authorized":false`) {
		t.Errorf("revoke must send authorized=false, body was: %s", srv.lastBody())
	}
}

// TestAgentExecPositionalIDsCannotRetargetTheRequest is D-10: an id is a path SEGMENT, and
// a caller must not be able to address a route they did not name.
//
// THE INSTRUMENT MATTERS AS MUCH AS THE CLAIM. The first version of this test
// read r.URL.Path and failed — because Path is decoded, so the %2F the escaping
// produced was shown back as a slash. It was measuring the wrong thing: chi
// routes on the ESCAPED path when one is present, so that is what decides which
// handler runs, and that is what this asserts. A test that reads the decoded
// path can only ever fail, which would have made this guard look absent.
func TestAgentExecPositionalIDsCannotRetargetTheRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"x"}`))
	_, _, _ = execRoot(t, lot3Args(srv.URL,
		"deploy", "definitions", "get", "../../../v1/system/orgs/t_victim")...)
	got := srv.lastPath()
	const prefix = "/v1/m/deploy/definitions/"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("the request landed on %q; a positional id must stay inside its own route", got)
	}
	// The whole id has to be ONE segment: no unescaped slash may survive it.
	if rest := strings.TrimPrefix(got, prefix); strings.Contains(rest, "/") {
		t.Fatalf("the request reached %q — the id broke out into %d extra path segment(s)",
			got, strings.Count(rest, "/"))
	}
	if !strings.Contains(got, "%2F") {
		t.Fatalf("the slashes in the id were not escaped at all: %q", got)
	}

	// THE CONTROL: an ordinary id is NOT mangled by the escaping. Percent-encoding
	// everything would satisfy the assertions above and break every real call.
	_, _, _ = execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "get", "dep-1")...)
	if want := prefix + "dep-1"; srv.lastPath() != want {
		t.Fatalf("an ordinary id landed on %q, want %q", srv.lastPath(), want)
	}
}

// TestAgentExecNegativeLimitIsAUsageErrorBeforeAnyRequest is D-11. An engine that silently
// ignores an unparseable limit would let a typo look like it worked.
func TestAgentExecNegativeLimitIsAUsageErrorBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[]}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "voice", "sessions", "ls", "--limit", "-3")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("a negative --limit must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with an invalid --limit", n)
	}

	// THE CONTROL: a valid limit is sent, and reaches the engine as a parameter.
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"voice", "sessions", "ls", "--limit", "25", "--cursor", "c-7")...); err != nil {
		t.Fatalf("a valid page request must succeed, got %v", err)
	}
	if q := srv.lastQuery(); !strings.Contains(q, "limit=25") || !strings.Contains(q, "cursor=c-7") {
		t.Fatalf("the page parameters did not reach the engine, query was %q", q)
	}
}

// TestAgentExecPatchSendsOnlyTheFlagsTheOperatorTyped is P-1, the rule this lot fixes for
// the whole census. An omitted flag must not appear in the body at all.
func TestAgentExecPatchSendsOnlyTheFlagsTheOperatorTyped(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"sc-1","desired_status":"paused"}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "schedules", "update", "sc-1", "--desired-status", "paused")...); err != nil {
		t.Fatalf("the patch must succeed, got %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(srv.lastBody()), &body); err != nil {
		t.Fatalf("the patch body is not JSON: %v (%s)", err, srv.lastBody())
	}
	if len(body) != 1 {
		t.Fatalf("the patch sent %d field(s): %s — only the typed flag may travel", len(body), srv.lastBody())
	}
	if body["desired_status"] != "paused" {
		t.Fatalf("the typed flag did not travel: %s", srv.lastBody())
	}
	for _, never := range []string{"cadence_spec", "grace_factor", "expected_interval_seconds", "subject_ref"} {
		if _, present := body[never]; present {
			t.Errorf("an untyped field %q reached the engine and would clobber the stored value", never)
		}
	}
}

// TestAgentExecPatchSendsAnExplicitZero is the counterweight to P-1: `--grace-factor 0` is
// a REQUEST, not an absence, and keying off the value instead of Changed would
// silently drop it.
func TestAgentExecPatchSendsAnExplicitZero(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"sc-1"}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "schedules", "update", "sc-1", "--expected-interval-seconds", "0")...); err != nil {
		t.Fatalf("the patch must succeed, got %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(srv.lastBody()), &body); err != nil {
		t.Fatalf("the patch body is not JSON: %v", err)
	}
	v, present := body["expected_interval_seconds"]
	if !present {
		t.Fatalf("an explicitly typed zero was dropped: %s", srv.lastBody())
	}
	if fmt.Sprintf("%v", v) != "0" {
		t.Fatalf("expected_interval_seconds = %v, want 0", v)
	}
}

// TestAgentExecEmptyPatchIsRefusedBeforeAnyRequest: a PATCH with nothing in it is a
// no-op request the operator did not mean to make.
func TestAgentExecEmptyPatchIsRefusedBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "orchestration", "schedules", "update", "sc-1")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("an empty patch must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d empty patch request(s) were sent", n)
	}
}

// TestAgentExecListTruncationNoteGoesToStderrNotStdout is P-2. `olivares deploy operations
// | wc -l` must count operations, not a sentence about paging.
func TestAgentExecListTruncationNoteGoesToStderrNotStdout(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[{"definition_id":"dep-1","op":"apply","status":"applied"}],
		"cursor":"c-9","has_more":true}`))
	out, errOut, err := execRoot(t, lot3Args(srv.URL, "deploy", "operations")...)
	if err != nil {
		t.Fatalf("the list must succeed, got %v", err)
	}
	if strings.Contains(out, "c-9") || strings.Contains(out, "more rows") {
		t.Errorf("the truncation note leaked into stdout:\n%s", out)
	}
	if !strings.Contains(errOut, "c-9") {
		t.Errorf("stderr must name the cursor to continue with, got:\n%s", errOut)
	}
	if !strings.Contains(out, "dep-1") {
		t.Errorf("stdout must carry the row, got:\n%s", out)
	}
}

// TestAgentExecJSONOutputPreservesFieldsTheCLIDoesNotModel is P-3. This plane adds fields
// faster than a client can track them, so -o json returns the engine's own bytes.
func TestAgentExecJSONOutputPreservesFieldsTheCLIDoesNotModel(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[{"id":"sc-1","name":"nightly",
		"a_field_this_cli_has_never_heard_of":"keep me"}],"has_more":false}`))
	out, _, err := execRoot(t, lot3Args(srv.URL,
		"orchestration", "schedules", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("the list must succeed, got %v", err)
	}
	if !strings.Contains(out, "a_field_this_cli_has_never_heard_of") {
		t.Errorf("-o json dropped a field the engine sent:\n%s", out)
	}
}

// TestAgentExecEmptyListSaysSoOnStdout is P-5. Zero bytes cannot be told apart from a
// swallowed command (the defect renderListOut exists to fix).
func TestAgentExecEmptyListSaysSoOnStdout(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "scenarios", "ls")...)
	if err != nil {
		t.Fatalf("an empty list must exit 0, got %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("an empty list printed nothing: an operator cannot tell that from a broken command")
	}
	if !strings.Contains(out, "no scenarios") {
		t.Errorf("the empty note must say what is empty, got: %q", out)
	}
}

// TestAgentExecStreamEmitsNDJSONOnStdoutAndNoiseOnStderr is P-4: the stream contract is
// one JSON object per line, so `| jq` works and nothing on stdout is a protocol
// artifact.
func TestAgentExecStreamEmitsNDJSONOnStdoutAndNoiseOnStderr(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": connected\n\n")
		_, _ = io.WriteString(w, "event: relation\ndata: {\"supervisor_ref\":\"a\",\"worker_ref\":\"b\"}\n\n")
		_, _ = io.WriteString(w, ": ping\n\n")
	})
	out, errOut, err := execRoot(t, lot3Args(srv.URL, "orchestration", "stream")...)
	if err != nil {
		t.Fatalf("the stream must end cleanly, got %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout carried %d line(s), want exactly the one data frame:\n%s", len(lines), out)
	}
	var frame struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	if uerr := json.Unmarshal([]byte(lines[0]), &frame); uerr != nil {
		t.Fatalf("stdout is not NDJSON: %v (%q)", uerr, lines[0])
	}
	if frame.Event != "relation" {
		t.Errorf("event = %q, want relation", frame.Event)
	}
	if !strings.Contains(string(frame.Data), "worker_ref") {
		t.Errorf("the frame lost its payload: %s", frame.Data)
	}
	if !strings.Contains(errOut, "connected") || !strings.Contains(errOut, "ping") {
		t.Errorf("keep-alives must reach stderr so an idle stream is visibly alive, got:\n%s", errOut)
	}
	if strings.Contains(out, "connected") || strings.Contains(out, "ping") {
		t.Errorf("a keep-alive comment leaked into the NDJSON pipe:\n%s", out)
	}
}

// TestAgentExecStreamRefusalKeepsTheExitContract: a stream that is refused before the
// first frame must classify like any other request, not exit 0 with no output.
func TestAgentExecStreamRefusalKeepsTheExitContract(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"nope"}}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "voice", "sessions", "stream", "vs-1")...)
	if err == nil {
		t.Fatal("a refused stream must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Fatalf("exit = %d, want %d", got, exitcode.Auth)
	}
}

// TestAgentExecTableValuesAreSanitizedBeforeReachingTheTerminal: values come off the
// network, and a control byte in one would otherwise be written verbatim.
func TestAgentExecTableValuesAreSanitizedBeforeReachingTheTerminal(t *testing.T) {
	srv := newLot3Server(t, lot3OK(
		"{\"items\":[{\"id\":\"sc-1\",\"name\":\"night\\u001b[31mly\"}],\"has_more\":false}"))
	out, _, err := execRoot(t, lot3Args(srv.URL, "orchestration", "schedules", "ls")...)
	if err != nil {
		t.Fatalf("the list must succeed, got %v", err)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("an escape byte from the server reached the terminal verbatim: %q", out)
	}
}

// TestAgentExecLargeIntegersAreNotReformattedThroughFloat: a version or sequence number
// must print as itself, not in scientific notation.
func TestAgentExecLargeIntegersAreNotReformattedThroughFloat(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[{"id":"dep-1","current_version":1099511627776}],"has_more":false}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "deploy", "definitions", "ls")...)
	if err != nil {
		t.Fatalf("the list must succeed, got %v", err)
	}
	if !strings.Contains(out, "1099511627776") {
		t.Fatalf("a large integer was reformatted: %q", out)
	}
}

// TestAgentExecTableIsOnlyReachedThroughRenderOut is the witness that keeps the
// `render-exempt:` on writeAgentExecTable honest.
//
// WHY THIS TEST EXISTS. writeAgentExecTable hand-builds a table with a tabwriter,
// which TestCommandFilesRenderThroughRenderOut flags on sight. It is exempt only
// because every call site is the TEXT CLOSURE renderOut invokes, so `-o json`
// takes the other branch and never reaches it. That is a property of the callers,
// not of this function — and an exemption comment cannot enforce a property. A
// fifth call site that formatted a table outside a renderOut closure would inherit
// the exemption and silently stop honoring the global -o/--output, which is the
// exact defect the E2 gate was built to catch.
//
// The first draft of that exemption claimed "the only caller is
// renderAgentExecList". There were FOUR. The claim was wrong the day it was
// written, which is the reason this is measured rather than asserted in prose.
func TestAgentExecTableIsOnlyReachedThroughRenderOut(t *testing.T) {
	files, err := filepath.Glob("cmd_*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var (
		sites    int
		offenses []string
	)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(name) //nolint:gosec // fixed package-local path
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			// The definition and its doc comment are not call sites.
			if !strings.Contains(line, "writeAgentExecTable(") ||
				strings.HasPrefix(strings.TrimSpace(line), "//") ||
				strings.HasPrefix(line, "func ") {
				continue
			}
			sites++
			// Walk back to the top of the enclosing function looking for the
			// renderOut that owns this closure. Stopping at `func ` is what makes
			// this a real check: a renderOut in some earlier function in the same
			// file must not absolve a table built in a later one.
			rendered := false
			for j := i; j >= 0; j-- {
				if strings.Contains(lines[j], "renderOut(") {
					rendered = true
					break
				}
				if strings.HasPrefix(lines[j], "func ") {
					break
				}
			}
			if !rendered {
				offenses = append(offenses,
					fmt.Sprintf("%s:%d: %s", name, i+1, strings.TrimSpace(line)))
			}
		}
	}
	// A scan that found nothing proves nothing: if a rename ever makes the needle
	// miss, this test would certify silence as compliance.
	if sites < 4 {
		t.Fatalf("found %d call site(s) of writeAgentExecTable; the scan is not seeing "+
			"the package (4 were measured when this was written)", sites)
	}
	if len(offenses) > 0 {
		t.Fatalf("%d table(s) are built outside a renderOut closure, so the global "+
			"-o/--output does not reach them and the render-exempt on "+
			"writeAgentExecTable no longer covers them:\n  %s",
			len(offenses), strings.Join(offenses, "\n  "))
	}
}

// TestAgentExecJSONNeverRendersATableForAnyEnvelopeShape is the behavioral half:
// the four envelope shapes this lot renders (items, edges, probes, scopes) each
// return the ENGINE'S OWN BYTES under -o json, with no column header anywhere.
//
// Asserting the unmodelled field survives is not enough on its own — a table that
// happened to print the field would satisfy it. So each case also asserts the
// header the text form WOULD have emitted is absent: that is what distinguishes
// "json branch taken" from "table that looks like json".
func TestAgentExecJSONNeverRendersATableForAnyEnvelopeShape(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		args    []string
		header  string // a column header the TEXT form emits for this shape
	}{
		{
			name: "items",
			payload: `{"items":[{"id":"sc-1","name":"nightly",
				"unmodelled_field":"keep me"}],"has_more":false}`,
			args:   []string{"orchestration", "schedules", "ls"},
			header: "NAME",
		},
		{
			name: "edges",
			payload: `{"edges":[{"supervisor_ref":"a","worker_ref":"b","link_kind":"spawn",
				"unmodelled_field":"keep me"}],"has_more":false}`,
			args:   []string{"orchestration", "graph"},
			header: "SUPERVISOR_REF",
		},
		{
			name: "probes",
			payload: `{"total":1,"probes":[{"id":"p-1","family":"exfil","severity":"high",
				"unmodelled_field":"keep me"}]}`,
			args:   []string{"redteam", "catalog"},
			header: "SEVERITY",
		},
		{
			name: "scopes",
			payload: `{"surface":"managed-settings","latest_revision":3,
				"scopes":[{"scope":"fleet","reporter":"r-1","verified":true,
				"unmodelled_field":"keep me"}]}`,
			args:   []string{"claude-policy", "distribution", "managed-settings"},
			header: "REPORTER",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// CONTROL FIRST: the text form really does build a table with that
			// header. Without it, "the header is absent" under -o json is satisfied
			// just as well by a header this lot never emits for this shape.
			text := newLot3Server(t, lot3OK(tc.payload))
			textOut, _, terr := execRoot(t, lot3Args(text.URL, tc.args...)...)
			if terr != nil {
				t.Fatalf("the text form must succeed, got %v", terr)
			}
			if !strings.Contains(textOut, tc.header) {
				t.Fatalf("control failed: the text form did not emit the %q header, "+
					"so its absence under -o json would prove nothing:\n%s", tc.header, textOut)
			}

			srv := newLot3Server(t, lot3OK(tc.payload))
			out, _, err := execRoot(t, lot3Args(srv.URL, append(tc.args, "-o", "json")...)...)
			if err != nil {
				t.Fatalf("the json form must succeed, got %v", err)
			}
			if !strings.Contains(out, "unmodelled_field") {
				t.Errorf("-o json dropped a field the engine sent:\n%s", out)
			}
			if strings.Contains(out, tc.header) {
				t.Errorf("-o json rendered the TEXT table (found the %q header), so the "+
					"table is reachable with the json output selected:\n%s", tc.header, out)
			}
			if !json.Valid([]byte(out)) {
				t.Errorf("-o json did not emit valid JSON:\n%s", out)
			}
		})
	}
}

// TestDenialMessageStripsControlBytesFromEveryEngineSuppliedField is the witness for a
// defect the internal adversarial panel found on 2026-08-18 and that no gate could see:
// agentExecDenialMessage printed SIX engine-supplied fields straight to the terminal
// while reportAgentExecPending -- its twin, thirty lines below in the same file --
// already ran the SAME fields through safeCLIValue.
//
// The asymmetry inside one file is what settles the question of intent: sanitizing is
// the behavior this file already chose, and the 403 leg was the omission. The values
// come from an HTTP body, so a control byte in one of them is written by whatever
// answered the request, not by the operator.
//
// Every field is asserted SEPARATELY and with its own control byte, because a single
// combined case goes green as soon as one field is fixed and would let the other five
// regress unnoticed.
func TestDenialMessageStripsControlBytesFromEveryEngineSuppliedField(t *testing.T) {
	for _, tc := range []struct {
		field string
		body  string
	}{
		{"op", "{\"op\":\"a\\u0001b\",\"gate_status\":\"pending\"}"},
		{"op_status", "{\"op_status\":\"a\\u0002b\",\"gate_status\":\"pending\"}"},
		{"gate_status", "{\"gate_status\":\"a\\u000db\"}"},
		{"policy_verdict", "{\"policy_verdict\":\"a\\u001bb\"}"},
		{"approval_ref", "{\"approval_ref\":\"a\\u0007b\",\"gate_status\":\"pending\"}"},
		{"detail", "{\"detail\":\"a\\u0004b\",\"gate_status\":\"pending\"}"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			msg, ok := agentExecDenialMessage([]byte(tc.body))
			if !ok {
				t.Fatalf("%s: no denial message was produced at all, so this witness is "+
					"measuring nothing -- fix the fixture before trusting a green", tc.field)
			}
			for _, r := range msg {
				if unicode.IsControl(r) && r != '\n' {
					t.Fatalf("%s: the denial message carries control byte %q, which whatever "+
						"answered the request put in the body and this leg wrote straight to "+
						"the terminal. reportAgentExecPending runs the same fields through "+
						"safeCLIValue; this leg must too.\nmessage: %q", tc.field, r, msg)
				}
			}
			if !strings.Contains(msg, "a b") {
				t.Fatalf("%s: safeCLIValue maps a control rune to a space, so %q should survive "+
					"in the message; got %q", tc.field, "a b", msg)
			}
		})
	}
}
