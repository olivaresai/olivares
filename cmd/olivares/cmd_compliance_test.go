// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// These tests pin the two exit-code decisions of E5 and the destructive
// confirmation. Each is written so a MUTATION kills it: swap Degraded for OK,
// Conflict for Err, or drop the confirmDestructive call, and one fails.

// complianceTestArgs appends the client flags every compliance verb needs.
func complianceTestArgs(server string, args ...string) []string {
	return append(args, "--server", server, "--token", "test-token", "--tenant", "tenant-a")
}

// TestErasureExecutePendingApprovalExitsDegraded is the central claim of E5:
// a 202 has erased NOTHING, so the command must not exit 0.
func TestErasureExecutePendingApprovalExitsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/compliance/erasure/er-1/execute" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"pending_approval","approval_ref":"ap-9",
			"detail":"erasure awaiting dual-control approval (2 distinct humans; no break-glass)"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if err == nil {
		t.Fatal("a pending approval must not exit 0: nothing was erased")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	// The operator must be told, in words, that nothing happened.
	if !strings.Contains(out, "NOT DONE") {
		t.Errorf("stdout must say the erasure did not happen, got:\n%s", out)
	}
	if !strings.Contains(out, "ap-9") {
		t.Errorf("stdout must carry the approval ref, got:\n%s", out)
	}
}

// TestHoldReleasePendingApprovalExitsDegraded: same rule for the other
// dual-control verb. A release that returns 202 leaves the hold ACTIVE.
func TestHoldReleasePendingApprovalExitsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"pending_approval","approval_ref":"ap-3"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "holds", "release", "lh-1", "--yes")...)
	if err == nil {
		t.Fatal("a pending release must not exit 0: the hold is still active")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(out, "NOT DONE") {
		t.Errorf("stdout must say the release did not happen, got:\n%s", out)
	}
}

// TestErasureExecuteCompletedExitsOK is the counterweight: a genuinely clean
// erasure MUST exit 0. Without it, "always return Degraded" would satisfy every
// other test here.
//
// Like the gaps test, it models the REAL contract — the 200 body is the receipt
// (no status field) and the terminal status is read back from the persisted
// request. An earlier version invented `status` on the receipt, which is a body
// this endpoint cannot produce.
func TestErasureExecuteCompletedExitsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/m/compliance/erasure/er-1" {
			_, _ = io.WriteString(w, `{"id":"er-1","status":"completed","subject_kind":"user",
				"subject_token":"tok","data_classes":[],"case_ref":"DSAR-9","reason":"",
				"requested_by":"dpo","created_at":"2026-08-05"}`)
			return
		}
		_, _ = io.WriteString(w, `{"erasure_id":"er-1","key_shredded":true,"verify_ok":true,
			"account_outcome":"erased","provider_outcome":"erased","approval_ref":"ap-1",
			"targets":[],"retained":[],"manifest_hash":"abc","ledger_seq":1,"case_ref":"DSAR-9",
			"subject_kind":"user","subject_token":"tok","occurred_at":"2026-08-05"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if err != nil {
		t.Fatalf("a completed erasure must exit 0, got %v (code %d)", err, exitcode.From(err))
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("stdout should report the outcome, got:\n%s", out)
	}
}

// TestErasureBlockedByLegalHoldExitsConflict pins the 423 decision AND the
// requirement that the command names the holds. "Blocked" without "by what" is
// what sends an operator back to curl.
func TestErasureBlockedByLegalHoldExitsConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"error":{"code":"legal_hold","message":"blocked by an active legal hold",
			"holds":[{"id":"lh-7","matter_ref":"CASE-42","scope_kind":"subject"}]}}`)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if err == nil {
		t.Fatal("a hold-blocked erasure must be an error")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %d, want %d (conflict); a hold veto is not a generic failure", got, exitcode.Conflict)
	}
	msg := err.Error()
	for _, want := range []string{"lh-7", "CASE-42", "subject"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must name the covering hold (%q missing) — got:\n%s", want, msg)
		}
	}
}

// TestComplianceSeamAnswerIsNotReportedAsFailure: a 501 on this module is a
// product boundary (the generators live in the add-on), not a fault.
func TestComplianceSeamAnswerIsNotReportedAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"error":{"code":"not_implemented",
			"message":"DORA Register of Information generation requires the Olivares enterprise add-on"}}`)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL, "compliance", "dora", "registers")...)
	if err == nil {
		t.Fatal("a 501 must still be an error")
	}
	if !strings.Contains(err.Error(), "add-on") {
		t.Errorf("a 501 must be explained as an add-on boundary, got: %v", err)
	}
	// It must NOT be classified as a server failure: the plane is healthy.
	if got := exitcode.From(err); got == exitcode.Server {
		t.Errorf("a 501 add-on boundary must not be exit %d (server failure)", exitcode.Server)
	}
}

// TestDestructiveVerbsRefuseWithoutConsent: a pipe is not consent.
func TestComplianceDestructiveVerbsRefuseWithoutConsent(t *testing.T) {
	// The server must never be reached; if it is, the guard did not run.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("destructive verb reached the network without confirmation")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"erasure execute", []string{"compliance", "erasure", "execute", "er-1"}},
		{"hold release", []string{"compliance", "holds", "release", "lh-1"}},
		{"subject erase", []string{"compliance", "subject", "erase", "u-7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := execRoot(t, complianceTestArgs(srv.URL, tc.args...)...)
			if err == nil {
				t.Fatal("a non-interactive session must not be treated as consent")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit code = %d, want %d (usage)", got, exitcode.Usage)
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Errorf("the refusal must say how to state the intent, got: %v", err)
			}
		})
	}
}

// TestHoldsCheckRejectsHalfASubject: the engine requires subject_kind and
// subject_ref together (holds.go:480). Catching it client-side keeps a
// half-specified query from reading as "nothing is held".
func TestHoldsCheckRejectsHalfASubject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a malformed check must not reach the network")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "holds", "check", "--subject-kind", "user")...)
	if err == nil {
		t.Fatal("subject-kind without subject-ref must be rejected")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d (usage)", got, exitcode.Usage)
	}
}

// TestHoldsCheckReportsHeld proves the preview verb reports the engine's answer
// rather than a generic success.
func TestHoldsCheckReportsHeld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("subject_ref"); got != "u-7" {
			t.Errorf("subject_ref = %q, want u-7", got)
		}
		_, _ = io.WriteString(w, `{"held":true,"holds":[{"id":"lh-1","matter_ref":"CASE-1","scope_kind":"subject"}]}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "holds", "check", "--subject-kind", "user", "--subject-ref", "u-7")...)
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !strings.Contains(out, "HELD") || !strings.Contains(out, "lh-1") {
		t.Errorf("a held subject must be reported with its hold, got:\n%s", out)
	}
}

// TestHoldsCheckReportsNotHeld is the other polarity — the one that matters
// most, because reading "not held" when something IS held authorizes a deletion.
func TestHoldsCheckReportsNotHeld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"held":false}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "holds", "check", "--data-class", "session_transcript")...)
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !strings.Contains(out, "not held") {
		t.Errorf("an unheld subject must be reported as such, got:\n%s", out)
	}
	if strings.Contains(out, "HELD by") {
		t.Errorf("an unheld subject must never render as held, got:\n%s", out)
	}
}

// TestErasureReceiptPrintsRetainedAndProviderFloor: the receipt's honesty is the
// reason it is defensible. A receipt that printed only what was destroyed would
// overstate the erasure.
func TestErasureReceiptPrintsRetainedAndProviderFloor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"erasure_id":"er-1","case_ref":"DSAR-9","key_shredded":true,
			"verify_ok":true,"verify_checked":42,"manifest_hash":"abc","ledger_seq":7,
			"targets":[{"label":"sessions","rows":3}],
			"retained":[{"records":"audit ledger events","basis":"GDPR Art. 17(3)(b)"}],
			"provider_floor_days":30,"provider_floor_known":true,"provider_floor_source":"covered-models"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL, "compliance", "erasure", "receipt", "er-1")...)
	if err != nil {
		t.Fatalf("receipt must succeed, got %v", err)
	}
	for _, want := range []string{"audit ledger events", "Art. 17(3)(b)", "provider floor", "30 days"} {
		if !strings.Contains(out, want) {
			t.Errorf("receipt must disclose %q, got:\n%s", want, out)
		}
	}
}

// TestComplianceHonoursJSONOutput: -o json must be the server's data, not the
// text form. renderOut is the single switch, so one representative check per
// shape keeps a hand-rolled Fprintf from creeping in.
func TestComplianceHonoursJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"id":"lh-1","matter_ref":"CASE-1","scope_kind":"tenant",
			"status":"active","reason":"litigation","created_by":"dpo","created_at":"2026-08-05"}]}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL, "compliance", "holds", "ls", "-o", "json")...)
	if err != nil {
		t.Fatalf("ls must succeed, got %v", err)
	}
	var decoded holdListOut
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("-o json must emit parseable JSON: %v\ngot:\n%s", jerr, out)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].ID != "lh-1" {
		t.Fatalf("JSON output lost the payload: %+v", decoded)
	}
}

// TestComplianceMapsStandardStatuses keeps the generic classification working
// through the module's own error wrapper.
func TestComplianceMapsStandardStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
	}{
		{http.StatusNotFound, exitcode.NotFound},
		{http.StatusConflict, exitcode.Conflict},
		{http.StatusForbidden, exitcode.Auth},
		{http.StatusInternalServerError, exitcode.Server},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":{"code":"x","message":"y"}}`)
		}))
		_, _, err := execRoot(t, complianceTestArgs(srv.URL, "compliance", "holds", "get", "lh-1")...)
		if err == nil {
			t.Fatalf("HTTP %d must be an error", tc.status)
		}
		if got := exitcode.From(err); got != tc.want {
			t.Errorf("HTTP %d → exit %d, want %d", tc.status, got, tc.want)
		}
		srv.Close()
	}
}

// TestCalendarRendersSourceAndStatus: the calendar's whole value is that every
// date carries a citation and a status. A view that dropped either would turn a
// provisional agreement into a deadline.
func TestCalendarRendersSourceAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/m/compliance/calendar" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"milestones":[{"id":"m1","regime":"eu_ai_act","date":"2026-08-02",
			"title":"GPAI obligations apply","effect":"e","status":"provisional_agreement",
			"verified_on":"2026-06-01","source":{"url":"https://x","title":"t","publisher":"p"}}],
			"watchlist":[{"id":"w1","name":"OWASP MCP Top 10","status":"beta","verified_on":"2026-06-10"}],
			"disclaimer":"provisional_agreement entries are NOT in-force law"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL, "compliance", "calendar")...)
	if err != nil {
		t.Fatalf("calendar must succeed, got %v", err)
	}
	for _, want := range []string{"provisional_agreement", "2026-08-02", "NOT in-force law", "OWASP MCP Top 10"} {
		if !strings.Contains(out, want) {
			t.Errorf("calendar output must carry %q, got:\n%s", want, out)
		}
	}
}

// TestComplianceUnresolvedServerIsUsageNotServerFailure: "you have not configured
// a server" is an invocation error, and the message must name the client
// contexts (E7) rather than only the flag.
func TestComplianceUnresolvedServerIsUsageNotServerFailure(t *testing.T) {
	// Clearing the env is NOT enough to make the server unresolved: a named client
	// CONTEXT on disk is precisely the fallback for when the env is empty
	// (cliconfig.go), so on any machine where an operator has run `olivares auth
	// login` this test resolved a real server, tried to reach it, and failed with
	// exit 6 instead of 2. Measured 2026-08-10: red with ~/.config/olivares/config.yaml
	// present, green with the same file moved aside and no other change. The test was
	// passing for a reason that is not in this repository — an empty home directory.
	// Six other test files already pin the config path this way; this one missed it.
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	_, _, err := execRoot(t, "compliance", "holds", "ls")
	if err == nil {
		t.Fatal("an unresolved server must be an error")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d (usage): a missing server is a bad invocation, not a dead plane", got, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "use-context") {
		t.Errorf("the message must mention client contexts, got: %v", err)
	}
}

// ---- cases the sol-max contrast found (-02, -04) --------------------

// TestErasureProviderPendingIsNotReportedAsNothingErased: the engine has a SECOND
// 202. It comes AFTER local targets were erased and the account leg ran, so
// telling the operator "NOTHING was erased" is false in the dangerous direction.
func TestErasureProviderPendingIsNotReportedAsNothingErased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"provider_pending",
			"detail":"provider: 1 erased, 2 pending; re-execute once the provider approvals are granted"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(out, "PARTIALLY DONE") {
		t.Errorf("a provider_pending must be reported as partial, got:\n%s", out)
	}
	if strings.Contains(out, "NOT DONE — awaiting dual-control") {
		t.Errorf("provider_pending is NOT the pending-approval case, got:\n%s", out)
	}
	if !strings.Contains(out, "provider_pending") {
		t.Errorf("the engine status must be surfaced, got:\n%s", out)
	}
}

// TestErasure202WithUnparseableBodyStillExitsDegraded: a malformed 202 must not
// silently become exit 1 — the status is the contract, not the body.
func TestErasure202WithUnparseableBodyStillExitsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(out, "DID NOT COMPLETE") {
		t.Errorf("an unparseable 202 must not read as success, got:\n%s", out)
	}
}

// TestErasureCompletedWithGapsExitsDegraded: a 200 is not always a clean erasure.
// The engine persists completed_with_gaps when the provider is not wired or the
// verification failed; exiting 0 lets an automation close an unfinished DSAR.
func TestErasureCompletedWithGapsExitsDegraded(t *testing.T) {
	// The server models the REAL contract: the 200 body is the receipt, which has
	// NO status field (erasure.go:163-181), and the terminal status lives on the
	// request the engine persisted. The first version of this test invented a
	// status field on the receipt, so it passed while the CLI still exited 0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/m/compliance/erasure/er-1" {
			_, _ = io.WriteString(w, `{"id":"er-1","status":"completed_with_gaps",
				"subject_kind":"user","subject_token":"tok","data_classes":[],"case_ref":"DSAR-9",
				"reason":"","requested_by":"dpo","created_at":"2026-08-05"}`)
			return
		}
		_, _ = io.WriteString(w, `{"erasure_id":"er-1","key_shredded":true,"verify_ok":true,
			"account_outcome":"erased","provider_outcome":"not_wired: no provider eraser configured",
			"targets":[],"retained":[],"manifest_hash":"abc","ledger_seq":1,"case_ref":"DSAR-9",
			"subject_kind":"user","subject_token":"tok","occurred_at":"2026-08-05"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d (degraded); a gapped erasure is not a clean success", got, exitcode.Degraded)
	}
	if !strings.Contains(out, "COMPLETED WITH GAPS") {
		t.Errorf("the gaps must be stated, got:\n%s", out)
	}
	// The leg that explains WHY it has gaps must be printed.
	if !strings.Contains(out, "not_wired") {
		t.Errorf("the provider leg must be shown, got:\n%s", out)
	}
}

// TestRTBFCoordinatorBlockIsNotCalledALegalHold: both vetoes are 423 with
// different remedies. Naming the wrong one sends an operator to release a hold
// that does not exist.
func TestRTBFCoordinatorBlockIsNotCalledALegalHold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"error":{"code":"rtbf_coordinator",
			"message":"blocked by enterprise RTBF coordinator",
			"blockers":["worm_lock active on archive tier"],"warnings":["replica lag 4m"]}}`)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %d, want %d (conflict)", got, exitcode.Conflict)
	}
	msg := err.Error()
	if !strings.Contains(msg, "worm_lock active on archive tier") {
		t.Errorf("the real blockers must survive, got:\n%s", msg)
	}
	if strings.Contains(msg, "preservation wins over erasure") {
		t.Errorf("a coordinator veto must not be reported as a legal hold, got:\n%s", msg)
	}
}

// TestLegalHoldWithNoHoldsListedSaysSo: an empty or malformed holds array must
// not read as "blocked, and here is nothing".
func TestLegalHoldWithNoHoldsListedSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"error":{"code":"legal_hold","message":"blocked"}}`)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if !strings.Contains(err.Error(), "named no holds") {
		t.Errorf("an empty hold list must be called out, got:\n%v", err)
	}
}

// TestErasureReceiptPrintsBothLegs: the receipt must disclose the account and
// provider legs, because a not-wired provider is the usual reason for gaps.
func TestErasureReceiptPrintsBothLegs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"erasure_id":"er-1","case_ref":"DSAR-9","key_shredded":true,
			"verify_ok":true,"verify_checked":1,"manifest_hash":"abc","ledger_seq":7,
			"account_outcome":"erased","provider_outcome":"not_wired: no provider eraser configured",
			"retained":[{"records":"ledger","basis":"Art. 17(3)(b)"}]}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL, "compliance", "erasure", "receipt", "er-1")...)
	if err != nil {
		t.Fatalf("receipt must succeed, got %v", err)
	}
	for _, want := range []string{"account leg", "erased", "provider leg", "not_wired"} {
		if !strings.Contains(out, want) {
			t.Errorf("receipt must disclose %q, got:\n%s", want, out)
		}
	}
}

// TestErasureUnconfirmedTerminalStatusDoesNotExitZero: if the status cannot be
// read back, the command must not claim the erasure finished cleanly. "I could
// not check" is not "it is clean".
func TestErasureUnconfirmedTerminalStatusDoesNotExitZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":"x","message":"y"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"erasure_id":"er-1","key_shredded":true,"verify_ok":true,
			"account_outcome":"erased","provider_outcome":"erased","targets":[],"retained":[],
			"manifest_hash":"abc","ledger_seq":1,"case_ref":"DSAR-9","subject_kind":"user",
			"subject_token":"tok","occurred_at":"2026-08-05"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d: an unconfirmed status must not read as clean", got, exitcode.Degraded)
	}
	if !strings.Contains(out, "could NOT be confirmed") {
		t.Errorf("the operator must be told the status is unconfirmed, got:\n%s", out)
	}
}

// TestA202WithoutAReasonDoesNotInventOne: a 202 whose body does not state a
// reason this CLI knows must not be reported as one that did.
//
// ALLOWLIST, same rule as the exit codes: the specific wording is claimed only
// for a status the CLI recognizes. The default used to be "awaiting dual-control
// approval", so an empty body, a `{}`, a `null` and a body carrying only an
// unrelated field all announced a reason the engine had never given. The
// follow-up contrast measured all four.
func TestA202WithoutAReasonDoesNotInventOne(t *testing.T) {
	for _, body := range []string{
		``,                          // no bytes at all
		`{}`,                        // valid JSON, no discriminant
		`null`,                      // valid JSON null
		`{"detail":"future queue"}`, // valid JSON, unrelated field only
		`{"status":"future_state"}`, // a status from a newer engine
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, body)
			}))
			t.Cleanup(srv.Close)

			out, _, err := execRoot(t, complianceTestArgs(srv.URL,
				"compliance", "erasure", "execute", "er-1", "--yes")...)
			if got := exitcode.From(err); got != exitcode.Degraded {
				t.Fatalf("exit code = %d, want %d", got, exitcode.Degraded)
			}
			if strings.Contains(out, "awaiting dual-control approval") {
				t.Errorf("body %q does not accredit that reason, got:\n%s", body, out)
			}
			if !strings.Contains(out, "DID NOT COMPLETE") {
				t.Errorf("body %q must be reported as not completed, got:\n%s", body, out)
			}
		})
	}
}

// TestA202ThatDoesStateItsReasonKeepsIt is the counterweight: when the engine
// DOES say pending_approval, that wording must survive.
func TestA202ThatDoesStateItsReasonKeepsIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"pending_approval","approval_ref":"ap-1"}`)
	}))
	t.Cleanup(srv.Close)

	out, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Degraded)
	}
	if !strings.Contains(out, "awaiting dual-control approval") {
		t.Errorf("a stated pending_approval must keep its wording, got:\n%s", out)
	}
}

// TestUnknown423IsNotNamedALegalHold: the CLI fallback must not assert a cause
// the response did not give.
func TestUnknown423IsNotNamedALegalHold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"error":{"code":"future_veto","message":"a future policy veto"}}`)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Conflict)
	}
	if strings.Contains(err.Error(), "legal hold") {
		t.Errorf("an unknown veto code must not be named a legal hold, got:\n%v", err)
	}
	if !strings.Contains(err.Error(), "a future policy veto") {
		t.Errorf("the engine message must survive, got:\n%v", err)
	}
}

// ---- the allowlist guard: this is what dies if someone writes a denylist ----

// erasureReceiptBody is the REAL 200 body of an execute: the receipt, which has
// no status field (erasure.go:163-181). Written once so no test in this file can
// drift back into inventing one.
const erasureReceiptBody = `{"erasure_id":"er-1","key_shredded":true,"verify_ok":true,
	"account_outcome":"erased","provider_outcome":"erased","targets":[],"retained":[],
	"manifest_hash":"abc","ledger_seq":1,"case_ref":"DSAR-9","subject_kind":"user",
	"subject_token":"tok","occurred_at":"2026-08-05"}`

// erasureTerminalVocabulary is the SINGLE source both allowlist guards read.
//
// It used to be two lists — the table's inline slice and the sync test's map —
// which could drift apart silently: the map could claim coverage the table did
// not exercise. The fourth-round contrast measured exactly that false green.
var erasureTerminalVocabulary = []string{
	"received", "pending_approval", "executing", "blocked_hold",
	"denied", "failed", "completed", "completed_with_gaps",
}

// erasureStatusCleanValue is the ONE value that may exit 0.
const erasureStatusCleanValue = "completed"

// TestOnlyCompletedExitsZero is the ALLOWLIST GUARD.
//
// It walks every terminal status the engine vocabulary defines, plus one this
// build has never seen, and asserts that EXACTLY ONE of them — `completed` —
// exits 0. A denylist (`if status == "completed_with_gaps" { degrade }`) passes
// the gaps case and fails every other row here, which is the point: this test
// exists so the class cannot be reopened one sibling at a time.
//
// Three rounds of contrast in this session each found the same mistake in a new
// place. The rule is now stated once, in a test, over the whole vocabulary.
func TestOnlyCompletedExitsZero(t *testing.T) {
	// Both governed verbs go through the same renderer, but "goes through the
	// same function" is an implementation claim; the contrast flagged that only
	// one was exercised. Assert it on both.
	verbs := map[string][]string{
		"execute":       {"compliance", "erasure", "execute", "er-1", "--yes"},
		"subject-erase": {"compliance", "subject", "erase", "u-7", "--yes"},
	}
	statuses := append(append([]string{}, erasureTerminalVocabulary...),
		"some_future_state") // a status a newer engine could introduce
	for _, status := range statuses {
		for verb, args := range verbs {
			t.Run(status+"/"+verb, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/m/compliance/erasure/") {
						_, _ = io.WriteString(w, `{"id":"er-1","status":"`+status+`","subject_kind":"user",
							"subject_token":"tok","data_classes":[],"case_ref":"DSAR-9","reason":"",
							"requested_by":"dpo","created_at":"2026-08-05"}`)
						return
					}
					_, _ = io.WriteString(w, erasureReceiptBody)
				}))
				t.Cleanup(srv.Close)

				_, _, err := execRoot(t, complianceTestArgs(srv.URL, args...)...)
				code := exitcode.From(err)
				if status == erasureStatusCleanValue {
					if err != nil {
						t.Fatalf("%s: `completed` is the ONLY clean outcome and must exit 0, got %d (%v)", verb, code, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("%s: status %q exited 0; only `completed` may. This is the denylist bug: "+
						"every other terminal state, known or not, must exit non-zero", verb, status)
				}
				if code != exitcode.Degraded {
					t.Errorf("%s: status %q exited %d, want %d (degraded)", verb, status, code, exitcode.Degraded)
				}
			})
		}
	}
}

// TestReadBackWithoutStatusDoesNotExitZero: a read-back body carrying no status
// at all is not a clean completion either.
func TestReadBackWithoutStatusDoesNotExitZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"id":"er-1","subject_kind":"user"}`)
			return
		}
		_, _ = io.WriteString(w, erasureReceiptBody)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t, complianceTestArgs(srv.URL,
		"compliance", "erasure", "execute", "er-1", "--yes")...)
	if got := exitcode.From(err); err == nil || got != exitcode.Degraded {
		t.Fatalf("a read-back with no status must not exit 0, got %d (%v)", got, err)
	}
}

// TestOnlyReleasedHoldExitsZero applies the same allowlist to the other governed
// verb: a 200 whose DTO does not say `released` is not a release.
func TestOnlyReleasedHoldExitsZero(t *testing.T) {
	for _, status := range []string{"active", "released", "some_future_state"} {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"id":"lh-1","matter_ref":"CASE-1","scope_kind":"tenant",
					"reason":"r","status":"`+status+`","created_by":"dpo","created_at":"2026-08-05"}`)
			}))
			t.Cleanup(srv.Close)

			_, _, err := execRoot(t, complianceTestArgs(srv.URL,
				"compliance", "holds", "release", "lh-1", "--yes")...)
			if status == "released" {
				if err != nil {
					t.Fatalf("a released hold must exit 0, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a hold still reporting %q must not exit 0: preservation may be in force", status)
			}
		})
	}
}

// TestAllowlistCoversTheEngineVocabulary is the SYNC GUARD.
//
// The table above is hand-written, so it can go stale the moment the engine adds
// a terminal state — and a stale allowlist test is exactly the failure that let
// a fabricated fixture pass earlier in this session. This reads the engine's own
// status constants and fails if the table does not cover them.
func TestAllowlistCoversTheEngineVocabulary(t *testing.T) {
	// FAIL-CLOSED. This used to Skip when the source was unreadable, which turns
	// "I could not check" into a green run — the same fail-open shape this whole
	// session has been closing. If the guard cannot read the vocabulary it is
	// guarding, that is a failure, not an absence.
	raw, err := os.ReadFile("../../modules/compliance/erasure.go")
	if err != nil {
		t.Fatalf("cannot read the engine status vocabulary this guard exists to track: %v", err)
	}
	// Tolerate every shape a Go declaration of these constants can take: gofmt
	// alignment or none, a `:=` a refactor might introduce, an EXPLICIT TYPE
	// (`erasureStatusFoo string = "foo"`), and BOTH string literal forms —
	// interpreted ("…") and raw (backticks).
	//
	// Each of those was found by a contrast, in that order, and the lesson is the
	// same every time: a guard whose pattern is narrower than the language it
	// reads stops guarding without saying so. The raw-literal case is the one the
	// seventh round caught — a perfectly valid Go constant that left both guards
	// green.
	re := regexp.MustCompile("erasureStatus[A-Za-z]+(?:\\s+[A-Za-z0-9_.\\[\\]]+)?\\s*:?=\\s*(?:\"([a-z_]+)\"|`([a-z_]+)`)")
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		// Group 1 is the interpreted literal, group 2 the raw one; exactly one
		// matches per declaration.
		if m[1] != "" {
			found[m[1]] = true
		} else if m[2] != "" {
			found[m[2]] = true
		}
	}
	if len(found) == 0 {
		t.Fatal("could not read any erasure status constant from the engine; this guard is not guarding")
	}
	covered := map[string]bool{}
	for _, st := range erasureTerminalVocabulary {
		covered[st] = true
	}
	for status := range found {
		if !covered[status] {
			t.Errorf("the engine defines status %q and TestOnlyCompletedExitsZero does not exercise it; "+
				"add it to that table (only `completed` may exit 0)", status)
		}
	}
	// And the other direction: a status the table carries but the engine no
	// longer defines is a vocabulary that has drifted, not extra safety. Testing
	// only engine ⊆ table lets the table rot silently.
	for _, status := range erasureTerminalVocabulary {
		if !found[status] {
			t.Errorf("the table exercises status %q which the engine no longer defines; "+
				"the vocabulary has drifted", status)
		}
	}
}

// TestCreatesDoNotAnnounceAnUnconfirmed2xx: a 2xx is not evidence a record
// exists. Measured by the fourth-round contrast: a server answering
// `202 {"status":"pending_approval"}` made both creates print success and exit 0
// with an empty id, because complianceCall only separates >=300.
func TestCreatesDoNotAnnounceAnUnconfirmed2xx(t *testing.T) {
	for _, tc := range []struct {
		name, wantNot, okStatus string
		args                    []string
	}{
		{"holds place", "placed", "active", []string{"compliance", "holds", "place",
			"--matter", "CASE-1", "--scope", "tenant", "--reason", "litigation"}},
		{"erasure request", "registered", "received", []string{"compliance", "erasure", "request",
			"--subject-ref", "u-7", "--case-ref", "DSAR-9"}},
	} {
		// One row per CLAUSE of confirmedCreate, each isolated so that removing
		// ONE clause fails at least one row. The fifth-round contrast measured
		// the gap: every row here used to violate several clauses at once, so
		// dropping only the HTTP-status check left the suite green.
		for _, body := range []struct{ label, status, payload string }{
			// HTTP status alone: id and status are both valid.
			{"202 with a fully valid body", "202", `{"id":"x-1","status":"` + tc.okStatus + `"}`},
			// id alone: 201 and the right status.
			{"201 without an id", "201", `{"status":"` + tc.okStatus + `"}`},
			{"201 with a blank id", "201", `{"id":"   ","status":"` + tc.okStatus + `"}`},
			// status alone: 201 and a valid id.
			{"201 with the wrong status", "201", `{"id":"x-1","status":"some_other_state"}`},
			// and the degenerate shapes.
			{"200 with an empty body", "200", ``},
		} {
			t.Run(tc.name+"/"+body.label, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if body.status == "202" {
						w.WriteHeader(http.StatusAccepted)
					} else if body.status == "201" {
						w.WriteHeader(http.StatusCreated)
					}
					_, _ = io.WriteString(w, body.payload)
				}))
				t.Cleanup(srv.Close)

				out, _, err := execRoot(t, complianceTestArgs(srv.URL, tc.args...)...)
				if err == nil {
					t.Fatalf("an unconfirmed create must not exit 0; stdout was:\n%s", out)
				}
				if strings.Contains(out, tc.wantNot) && !strings.Contains(out, "NOT confirmed") {
					t.Errorf("must not announce %q for an unconfirmed create, got:\n%s", tc.wantNot, out)
				}
			})
		}
	}
}

// TestCreatesDoAnnounceAConfirmedOne is the counterweight: the real engine answer
// must still be reported as success, or the guard above would be satisfied by
// refusing everything.
func TestCreatesDoAnnounceAConfirmedOne(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
		args                []string
	}{
		{"holds place", `{"id":"lh-1","matter_ref":"CASE-1","scope_kind":"tenant","status":"active",
			"reason":"r","created_by":"dpo","created_at":"2026-08-05"}`, "placed",
			[]string{"compliance", "holds", "place", "--matter", "CASE-1", "--scope", "tenant", "--reason", "r"}},
		{"erasure request", `{"id":"er-1","status":"received","subject_kind":"user","subject_token":"tok",
			"data_classes":[],"case_ref":"DSAR-9","reason":"","requested_by":"dpo","created_at":"2026-08-05"}`,
			"registered",
			[]string{"compliance", "erasure", "request", "--subject-ref", "u-7", "--case-ref", "DSAR-9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, tc.payload)
			}))
			t.Cleanup(srv.Close)

			out, _, err := execRoot(t, complianceTestArgs(srv.URL, tc.args...)...)
			if err != nil {
				t.Fatalf("a confirmed create must exit 0, got %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in stdout, got:\n%s", tc.want, out)
			}
		})
	}
}
