// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares recording`.

func TestRecordingVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"recording", "notice"}, "GET", "/v1/m/recording/notice"},
		{[]string{"recording", "ack"}, "POST", "/v1/m/recording/ack"},
		{[]string{"recording", "sessions", "ls"}, "GET", "/v1/m/recording/sessions"},
		{[]string{"recording", "sessions", "get", "rs-1"}, "GET", "/v1/m/recording/sessions/rs-1"},
		{[]string{"recording", "sessions", "replay", "rs-1"}, "GET", "/v1/m/recording/sessions/rs-1/replay"},
		{[]string{"recording", "sessions", "unified", "rs-1"}, "GET", "/v1/m/recording/sessions/rs-1/unified"},
		{[]string{"recording", "sessions", "export", "rs-1"}, "GET", "/v1/m/recording/sessions/rs-1/export"},
		{[]string{"recording", "sessions", "seal", "rs-1"}, "POST", "/v1/m/recording/sessions/rs-1/seal"},
		{[]string{"recording", "sessions", "summarize", "rs-1"}, "POST", "/v1/m/recording/sessions/rs-1/summarize"},
		{[]string{"recording", "sweep"}, "POST", "/v1/m/recording/sweep"},
		{[]string{"recording", "config", "get"}, "GET", "/v1/m/recording/config"},
	} {
		t.Run(strings.Join(tc.argv, "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false,"ok":true,"sealed":0}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestRecordingVerifyExitsDegradedOnABrokenChain is this family's exit-code
// decision, and the reason it exists: the route answers 200 EITHER WAY, so a
// client that read only the status would exit 0 on a tampered trail and a nightly
// integrity job would be green forever.
func TestRecordingVerifyExitsDegradedOnABrokenChain(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":false,"gap":true,"detail":"frame 42 does not chain to 41"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "recording", "sessions", "verify", "rs-1")...)
	if err == nil {
		t.Fatal("a chain that did not verify must not exit 0: the HTTP status was 200 either way")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(err.Error(), "frame 42") {
		t.Errorf("the failure must carry the engine's reason, got: %v", err)
	}
}

// TestRecordingVerifyExitsZeroOnAnIntactChain is the counterweight. Without it,
// "always return Degraded" would satisfy the test above and no chain would ever
// verify.
func TestRecordingVerifyExitsZeroOnAnIntactChain(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true,"gap":false,"frames":120}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "recording", "sessions", "verify", "rs-1")...)
	if err != nil {
		t.Fatalf("an intact chain must exit 0, got %v (code %d)", err, exitcode.From(err))
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("the verdict must be printed, got:\n%s", out)
	}
}

// TestRecordingVerifyRefusesToConcludeFromAnUnreadableVerdict: "I could not read
// the verdict" must never render as "the chain is intact".
func TestRecordingVerifyRefusesToConcludeFromAnUnreadableVerdict(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`this is not json`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "recording", "sessions", "verify", "rs-1")...)
	if err == nil {
		t.Fatal("an unparseable verdict must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(err.Error(), "NOT a passing chain") {
		t.Errorf("the refusal must say it is not a pass, got: %v", err)
	}
}

// TestRecordingExportRejectsAnUnsupportedFormatBeforeAnyRequest. --format here is
// the REAL export-format flag (like `audit export`), not an alias of -o/--output,
// and the engine accepts exactly two values.
func TestRecordingExportRejectsAnUnsupportedFormatBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"rs-1"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL,
		"recording", "sessions", "export", "rs-1", "--format", "pdf")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("an unsupported --format must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with an unsupported format", n)
	}

	// THE CONTROL: both supported formats reach the engine as a parameter, and
	// -o json still selects how the answer is RENDERED — the two flags do not
	// collide.
	for _, format := range []string{"json", "summary"} {
		if _, _, err := execRoot(t, lot3Args(srv.URL,
			"recording", "sessions", "export", "rs-1", "--format", format, "-o", "json")...); err != nil {
			t.Fatalf("--format %s must be accepted, got %v", format, err)
		}
		if q := srv.lastQuery(); !strings.Contains(q, "format="+format) {
			t.Fatalf("--format %s did not reach the engine, query was %q", format, q)
		}
	}
}

// TestRecordingConfigSetRefusesAnEmptyPolicyBeforeAnyRequest. `config set` is a
// PUT: an omitted namespace list would REPLACE the policy with one that records
// nothing, which is the one mistake this verb can make that nobody notices until
// an incident.
func TestRecordingConfigSetRefusesAnEmptyPolicyBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"namespaces":["agents"]}`))
	for _, bad := range [][]string{
		{"recording", "config", "set"},
		{"recording", "config", "set", "--namespace", "agents", "--consent", "maybe"},
		{"recording", "config", "set", "--namespace", "agents", "--idle-seconds", "0"},
		{"recording", "config", "set", "--namespace", "agents", "--retention-days", "-1"},
	} {
		_, _, err := execRoot(t, lot3Args(srv.URL, bad...)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%v must exit %d, got %v", bad, exitcode.Usage, err)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d invalid policy replacement(s) were sent", n)
	}

	// THE CONTROL: a complete policy is accepted, is a PUT, and carries every
	// field — including ai_summaries=false, because a PUT that omitted it would
	// leave the engine to guess about an egress opt-in.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "recording", "config", "set",
		"--namespace", "agents", "--namespace", "tools",
		"--consent", "required", "--idle-seconds", "600", "--retention-days", "365")...); err != nil {
		t.Fatalf("a complete policy must be accepted, got %v", err)
	}
	if m, _ := srv.method.Load().(string); m != http.MethodPut {
		t.Errorf("config set used %s, want PUT", m)
	}
	body := srv.lastBody()
	for _, want := range []string{`"agents"`, `"tools"`, `"consent":"required"`, `"idle_seconds":600`,
		`"retention_days":365`, `"ai_summaries":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("the policy body is missing %s: %s", want, body)
		}
	}
}

// TestRecordingAISummariesIsOffUnlessTyped: the transcript leaves the trust
// boundary, so the opt-in has to be an act, never a default.
func TestRecordingAISummariesIsOffUnlessTyped(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ai_summaries":true}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL, "recording", "config", "set",
		"--namespace", "agents", "--ai-summaries")...); err != nil {
		t.Fatalf("the policy must be accepted, got %v", err)
	}
	if !strings.Contains(srv.lastBody(), `"ai_summaries":true`) {
		t.Fatalf("the explicit opt-in did not travel: %s", srv.lastBody())
	}
}

// TestRecordingReplayNamesATruncatedLedgerOnStderr. A truncated ledger window is
// NOT the whole audit trail, and an incident reviewer who does not know that
// draws conclusions from a fragment.
func TestRecordingReplayNamesATruncatedLedgerOnStderr(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"session":{"id":"rs-1"},
		"frames":{"items":[],"cursor":"f-200","has_more":true},"ledger":[],"ledger_truncated":true}`))
	out, errOut, err := execRoot(t, lot3Args(srv.URL, "recording", "sessions", "replay", "rs-1")...)
	if err != nil {
		t.Fatalf("the replay must succeed, got %v", err)
	}
	if !strings.Contains(errOut, "f-200") {
		t.Errorf("stderr must name the frame cursor, got:\n%s", errOut)
	}
	if !strings.Contains(errOut, "TRUNCATED") {
		t.Errorf("stderr must say the ledger window is incomplete, got:\n%s", errOut)
	}
	if strings.Contains(out, "TRUNCATED") {
		t.Errorf("the warning leaked into stdout:\n%s", out)
	}
}

// TestRecordingSweepReportsWhatItSealed: "sealed 0" and "sealed 12" are different
// facts, and a sweep that printed nothing would be indistinguishable from one
// that did not run.
func TestRecordingSweepReportsWhatItSealed(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"sealed":12}`)
	})
	out, _, err := execRoot(t, lot3Args(srv.URL, "recording", "sweep")...)
	if err != nil {
		t.Fatalf("the sweep must succeed, got %v", err)
	}
	if !strings.Contains(out, "12") {
		t.Fatalf("the sweep must report how many sessions it sealed, got: %q", out)
	}
}

// TestRecordingVerifyNamesReservedFramesWithoutFailing pins the one verdict field
// this command reports WITHOUT turning it into an exit code.
//
// `gap` is not `ok`. The engine sets it from reserved > written (handlers.go:712)
// and does NOT fail the chain for it, because Reserve bumps `reserved` when a
// recorded request STARTS (recorder.go:252) and the frame lands when it finishes
// — so a live session with work in flight reports gap=true for that window.
// Exiting non-zero on it would redden every nightly job that ran while the estate
// was busy. Dropping it silently, which is what the first draft did, hides a lost
// frame on a sealed session. It is therefore named on stderr, and nowhere else.
func TestRecordingVerifyNamesReservedFramesWithoutFailing(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true,"gap":true,"written":41,"reserved":42,"tip_match":true}`))
	out, errOut, err := execRoot(t, lot3Args(srv.URL, "recording", "sessions", "verify", "rs-1")...)
	if err != nil {
		t.Fatalf("a chain that verified must exit 0 even with a reserved slot, got %v (code %d)",
			err, exitcode.From(err))
	}
	if !strings.Contains(errOut, "RESERVED FRAME SLOTS THAT WERE NEVER") {
		t.Errorf("stderr must name the reserved-but-unwritten slots, got:\n%s", errOut)
	}
	if strings.Contains(out, "RESERVED FRAME SLOTS") {
		t.Errorf("the warning leaked into the stdout a script parses:\n%s", out)
	}

	// CONTROL 1: a clean verdict says nothing. Without it, "always warn" would
	// satisfy the assertion above and the warning would carry no information.
	clean := newLot3Server(t, lot3OK(`{"ok":true,"gap":false,"written":42,"reserved":42}`))
	_, cleanErr, cerr := execRoot(t, lot3Args(clean.URL, "recording", "sessions", "verify", "rs-1")...)
	if cerr != nil {
		t.Fatalf("a clean chain must exit 0, got %v", cerr)
	}
	if strings.Contains(cleanErr, "RESERVED FRAME SLOTS") {
		t.Errorf("a session with no reserved slots must not be warned about:\n%s", cleanErr)
	}

	// CONTROL 2: the gap note must not have become a substitute for the exit
	// code. A chain that did NOT verify still exits 7, gap or no gap.
	broken := newLot3Server(t, lot3OK(`{"ok":false,"gap":true,"reason":"idx-gap","break_at":42}`))
	_, _, berr := execRoot(t, lot3Args(broken.URL, "recording", "sessions", "verify", "rs-1")...)
	if berr == nil || exitcode.From(berr) != exitcode.Degraded {
		t.Fatalf("a broken chain must still exit %d, got %v", exitcode.Degraded, berr)
	}
}

// TestRecordingSweepRefusesToPrintACountItCouldNotRead is the witness for the
// sweep's decode guard, and it exists because that guard was MEASURED to have
// none: with only TestRecordingSweepReportsWhatItSealed (which sends a readable
// `{"sealed":12}`) the guard could be deleted whole and the suite stayed green.
//
// What the command did before the guard is the reason it matters: a dropped
// decode error left Sealed at its zero value and printed "sealed 0 idle
// session(s)" on exit 0 — a number nobody measured, reported as fact, by the
// verb an operator runs to find out what a sweep did.
func TestRecordingSweepRefusesToPrintACountItCouldNotRead(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`this is not json`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "recording", "sweep")...)
	if err == nil {
		t.Fatal("a sweep whose answer cannot be read must not exit 0")
	}
	// Degraded, not Server: the 2xx already materialized the seals, so this is
	// "it ran and I cannot report what it did", not "it failed".
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(err.Error(), "UNKNOWN") {
		t.Errorf("the failure must say the count is unknown, got: %v", err)
	}
	// THE POINT: no invented count reaches stdout.
	if strings.Contains(out, "sealed 0") {
		t.Errorf("an unreadable answer printed a count nobody measured:\n%s", out)
	}

	// THE CONTROL, the half that is usually left out: a readable sweep still
	// reports its real count and exits 0. Without it, "always fail" would
	// satisfy every assertion above.
	ok := newLot3Server(t, lot3OK(`{"sealed":12}`))
	okOut, _, okErr := execRoot(t, lot3Args(ok.URL, "recording", "sweep")...)
	if okErr != nil {
		t.Fatalf("a readable sweep must exit 0, got %v (code %d)", okErr, exitcode.From(okErr))
	}
	if !strings.Contains(okOut, "12") {
		t.Errorf("the readable sweep did not report its count:\n%s", okOut)
	}
}
