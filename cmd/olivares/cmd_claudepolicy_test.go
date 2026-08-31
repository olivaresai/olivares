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

// Family tests for `olivares claude-policy`. Every exit-code test here exists for
// the same reason: these routes answer 200 whether or not the answer is good, so
// the verdict is in the BODY and a client that read the status would call every
// broken policy valid.

func TestClaudePolicyVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	doc := lot3WriteTempJSON(t, `{"permissions":{}}`)
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"claude-policy", "validate", "managed-settings", "--content-file", doc},
			"POST", "/v1/m/claude-policy/managed-settings/validate"},
		{[]string{"claude-policy", "dry-run", "hooks", "--content-file", doc},
			"POST", "/v1/m/claude-policy/hooks/dry-run"},
		{[]string{"claude-policy", "publish", "managed-mcp", "--content-file", doc},
			"POST", "/v1/m/claude-policy/managed-mcp/publish"},
		{[]string{"claude-policy", "versions", "ls", "sandbox"},
			"GET", "/v1/m/claude-policy/sandbox/versions"},
		{[]string{"claude-policy", "versions", "get", "managed-settings", "3"},
			"GET", "/v1/m/claude-policy/managed-settings/versions/3"},
		{[]string{"claude-policy", "artifact", "managed-settings"},
			"GET", "/v1/m/claude-policy/managed-settings/artifact"},
		{[]string{"claude-policy", "checkin", "managed-settings", "--scope", "host-7"},
			"POST", "/v1/m/claude-policy/managed-settings/checkin"},
		{[]string{"claude-policy", "distribution", "managed-settings"},
			"GET", "/v1/m/claude-policy/managed-settings/distribution"},
	} {
		t.Run(strings.Join(tc.argv[:3], "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"ok":true,"verified":true,"items":[],"scopes":[],
				"diagnostics":[],"drift_computed":true,"distribution":"distributed"}`))
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

// TestClaudePolicyRejectsAnUnknownSurfaceBeforeAnyRequest. The engine is
// authoritative, but a typo should cost exit 2 and no request — and the message
// must list the four, because "unknown surface" without them sends the operator
// to the source.
func TestClaudePolicyRejectsAnUnknownSurfaceBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "versions", "ls", "managed-setings")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("an unknown surface must exit %d, got %v", exitcode.Usage, err)
	}
	if !strings.Contains(err.Error(), "managed-settings") {
		t.Errorf("the refusal must name the surfaces that exist, got: %v", err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent for an unknown surface", n)
	}

	// THE CONTROL: all four real surfaces are accepted.
	for _, surface := range []string{"managed-settings", "hooks", "managed-mcp", "sandbox"} {
		if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "versions", "ls", surface)...); err != nil {
			t.Fatalf("surface %s must be accepted, got %v", surface, err)
		}
	}
	if n := srv.calls.Load(); n != 4 {
		t.Fatalf("the four surfaces made %d requests, want 4", n)
	}
}

// TestClaudePolicyValidateExitsDegradedOnErrorDiagnostics is the family's central
// exit-code decision.
func TestClaudePolicyValidateExitsDegradedOnErrorDiagnostics(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":false,"surface":"managed-settings",
		"diagnostics":[{"message":"permissions.deny must be a list","severity":"error"}]}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "validate", "managed-settings",
		"--content-file", lot3WriteTempJSON(t, `{"permissions":{"deny":"nope"}}`))...)
	if err == nil {
		t.Fatal("a document with error diagnostics must not exit 0: the HTTP status was 200")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(err.Error(), "NOT publishable") {
		t.Errorf("the failure must say the document cannot ship, got: %v", err)
	}
}

// TestClaudePolicyValidateExitsZeroOnACleanDocument is the counterweight, and it
// also pins that a WARNING does not fail: failing on warnings would make the
// distinction between the two severities meaningless.
func TestClaudePolicyValidateExitsZeroOnACleanDocument(t *testing.T) {
	for name, payload := range map[string]string{
		"clean":        `{"ok":true,"surface":"managed-settings","diagnostics":[]}`,
		"warning-only": `{"ok":true,"surface":"managed-settings","diagnostics":[{"message":"deprecated key","severity":"warning"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(payload))
			if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "validate", "managed-settings",
				"--content-file", lot3WriteTempJSON(t, `{"permissions":{}}`))...); err != nil {
				t.Fatalf("a %s document must exit 0, got %v (code %d)", name, err, exitcode.From(err))
			}
		})
	}
}

// TestClaudePolicyCheckinExitsDegradedWhenTheArtifactDoesNotVerify: an echoed
// hash that does not match what was signed is a tampered or stale artifact, and
// the route still answers 200.
func TestClaudePolicyCheckinExitsDegradedWhenTheArtifactDoesNotVerify(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"surface":"managed-settings","scope":"host-7","verified":false,
		"drift":[{"severity":"high"}]}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings",
		"--scope", "host-7", "--revision", "3", "--artifact-sha256", "wrong")...)
	if err == nil {
		t.Fatal("an unverified check-in must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(err.Error(), "NOT VERIFIED") {
		t.Errorf("the failure must say the artifact did not verify, got: %v", err)
	}

	// THE CONTROL: a verified check-in exits 0. Without it, "always degraded"
	// would satisfy the assertion above and no agent could ever report success.
	ok := newLot3Server(t, lot3OK(`{"surface":"managed-settings","scope":"host-7","verified":true,"drift":[]}`))
	if _, _, err := execRoot(t, lot3Args(ok.URL, "claude-policy", "checkin", "managed-settings",
		"--scope", "host-7", "--revision", "3", "--artifact-sha256", "right")...); err != nil {
		t.Fatalf("a verified check-in must exit 0, got %v (code %d)", err, exitcode.From(err))
	}
	body := ok.lastBody()
	for _, want := range []string{`"scope":"host-7"`, `"revision":3`, `"artifact_sha256":"right"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the check-in body is missing %s: %s", want, body)
		}
	}
}

// TestClaudePolicyPublishNamesAnUndistributedRevisionOnStderr. `seam-pending`
// means the revision was published and NO HOST WILL EVER PULL IT — an operator
// who reads that as success has shipped nothing.
func TestClaudePolicyPublishNamesAnUndistributedRevisionOnStderr(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"surface":"hooks","revision":4,"distribution":"seam-pending",
		"drift":[],"drift_computed":true}`))
	_, errOut, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "publish", "hooks",
		"--content-file", lot3WriteTempJSON(t, `{"hooks":[]}`))...)
	if err != nil {
		t.Fatalf("publishing is still an authoring success and must exit 0, got %v", err)
	}
	if !strings.Contains(errOut, "NOT distributed") {
		t.Errorf("stderr must say no host will pull it, got:\n%s", errOut)
	}

	// THE CONTROL: a distributed publish does NOT print the warning.
	ok := newLot3Server(t, lot3OK(`{"surface":"hooks","revision":4,"distribution":"distributed",
		"drift":[],"drift_computed":true,"artifact":{"artifact_sha256":"abc","key_fingerprint":"SHA256:xyz"}}`))
	out, okErr, err := execRoot(t, lot3Args(ok.URL, "claude-policy", "publish", "hooks",
		"--content-file", lot3WriteTempJSON(t, `{"hooks":[]}`))...)
	if err != nil {
		t.Fatalf("a distributed publish must exit 0, got %v", err)
	}
	if strings.Contains(okErr, "NOT distributed") {
		t.Errorf("a distributed publish must not warn, got:\n%s", okErr)
	}
	if !strings.Contains(out, "SHA256:xyz") {
		t.Errorf("the key fingerprint an operator pins must be printed, got:\n%s", out)
	}
}

// TestClaudePolicyPublishSaysWhenDriftIsUnknownRatherThanClean. An empty drift
// list with drift_computed=false means NOTHING WAS OBSERVED, and rendering that
// as "no drift" is the exact confusion the engine added the field to prevent.
func TestClaudePolicyPublishSaysWhenDriftIsUnknownRatherThanClean(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"surface":"hooks","revision":1,"distribution":"distributed",
		"drift":[],"drift_computed":false}`))
	_, errOut, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "publish", "hooks",
		"--content-file", lot3WriteTempJSON(t, `{"hooks":[]}`))...)
	if err != nil {
		t.Fatalf("publishing must exit 0, got %v", err)
	}
	if !strings.Contains(errOut, "UNKNOWN, not clean") {
		t.Errorf("stderr must distinguish unknown drift from no drift, got:\n%s", errOut)
	}
}

// TestClaudePolicyVersionsGetRejectsANonNumericRevisionBeforeAnyRequest.
func TestClaudePolicyVersionsGetRejectsANonNumericRevisionBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"revision":1}`))
	for _, bad := range []string{"latest", "0", "-1"} {
		_, _, err := execRoot(t, lot3Args(srv.URL,
			"claude-policy", "versions", "get", "managed-settings", bad)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("revision %q must exit %d, got %v", bad, exitcode.Usage, err)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with an invalid revision", n)
	}
}

// TestClaudePolicyDistributionRendersScopesAndNamesAnEmptyTruth: "no scope has
// ever checked in" and "every scope is current" are opposite facts and an empty
// table would look like the second.
func TestClaudePolicyDistributionRendersScopesAndNamesAnEmptyTruth(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"surface":"managed-settings","latest_revision":4,
		"scopes":[{"scope":"host-7","reporter":"agent","reported_revision":4,"verified":true,
		"current":true,"content_reported":true}]}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "distribution", "managed-settings")...)
	if err != nil {
		t.Fatalf("the truth view must succeed, got %v", err)
	}
	for _, want := range []string{"latest_revision", "4", "host-7", "true"} {
		if !strings.Contains(out, want) {
			t.Errorf("the truth view is missing %q:\n%s", want, out)
		}
	}

	empty := newLot3Server(t, lot3OK(`{"surface":"hooks","latest_revision":0,"scopes":[]}`))
	eout, _, eerr := execRoot(t, lot3Args(empty.URL, "claude-policy", "distribution", "hooks")...)
	if eerr != nil {
		t.Fatalf("an empty truth view must exit 0, got %v", eerr)
	}
	if !strings.Contains(eout, "no scope has ever checked in") {
		t.Errorf("an empty truth view must say so, got: %q", eout)
	}
}

// TestClaudePolicyDryRunRefusesThroughTheEnginesStatusNotAVerdict pins what
// `dry-run` actually does, because the first draft of this family documented it
// as validate's twin and it is not.
//
// Measured in modules/governance/claudepolicy.go: handleDryRun answers HTTP 400
// for an invalid document (:300) and its 200 body (dryRunResult, :197) carries
// neither `ok` nor `diagnostics`. So the "200 with a verdict" reading applies to
// `validate` alone, and a caller must not be told to expect exit 7 here.
func TestClaudePolicyDryRunRefusesThroughTheEnginesStatusNotAVerdict(t *testing.T) {
	// The engine's REAL refusal shape for an invalid document.
	bad := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"document is invalid: permissions.deny must be a list"}}`)
	})
	out, _, err := execRoot(t, lot3Args(bad.URL, "claude-policy", "dry-run", "managed-settings",
		"--content-file", lot3WriteTempJSON(t, `{"permissions":{"deny":"nope"}}`))...)
	if err == nil {
		t.Fatal("an invalid dry-run must not exit 0")
	}
	if !strings.Contains(err.Error(), "permissions.deny") {
		t.Errorf("the engine's own reason must survive, got: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a refused dry-run must print nothing on stdout, got:\n%s", out)
	}

	// THE CONTROL: the engine's REAL success shape — no `ok`, no `diagnostics` —
	// exits 0 and renders. A reader that treated a missing `ok` as false would
	// fail every valid dry-run there is.
	ok := newLot3Server(t, lot3OK(`{"surface":"managed-settings","changes":[{"path":"permissions"}],
		"notes":["no observed host config available — precedence resolved, effect not diffed"]}`))
	okOut, _, okErr := execRoot(t, lot3Args(ok.URL, "claude-policy", "dry-run", "managed-settings",
		"--content-file", lot3WriteTempJSON(t, `{"permissions":{}}`))...)
	if okErr != nil {
		t.Fatalf("a valid dry-run must exit 0, got %v (code %d)", okErr, exitcode.From(okErr))
	}
	if !strings.Contains(okOut, "notes") {
		t.Errorf("the engine's notes must reach the operator, got:\n%s", okOut)
	}
}

// TestClaudePolicyValidateTrustsTheEnginesVerdictOverTheDiagnosticCount is the
// witness for the `ok` half of claudePolicyReportIssues, and it exists because
// that half was MEASURED to have none: the degraded test above sends ok=false
// ALONGSIDE an error diagnostic, so `errors > 0` satisfies it on its own and the
// ok check could be deleted whole without reddening anything.
//
// The case that separates the two is a false verdict with NOTHING the counter can
// see. Re-deriving the answer from a severity STRING calls those documents
// publishable — which is the engine's own verdict, inverted.
func TestClaudePolicyValidateTrustsTheEnginesVerdictOverTheDiagnosticCount(t *testing.T) {
	for name, payload := range map[string]string{
		"no diagnostics at all": `{"ok":false,"surface":"managed-settings","diagnostics":[]}`,
		"warnings only":         `{"ok":false,"surface":"managed-settings","diagnostics":[{"message":"deprecated key","severity":"warning"}]}`,
		"a severity we do not know": `{"ok":false,"surface":"managed-settings",
			"diagnostics":[{"message":"permissions.deny must be a list","severity":"fatal"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(payload))
			_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "validate", "managed-settings",
				"--content-file", lot3WriteTempJSON(t, `{"permissions":{}}`))...)
			if err == nil {
				t.Fatal("ok=false is the engine's own verdict and must not exit 0")
			}
			if got := exitcode.From(err); got != exitcode.Degraded {
				t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
			}
			if !strings.Contains(err.Error(), "NOT publishable") {
				t.Errorf("the failure must say the document cannot ship, got: %v", err)
			}
		})
	}

	// THE CONTROL: ok=true with an empty list still exits 0, so "always fail"
	// cannot satisfy the loop above. The OTHER control — a body with no `ok` key
	// at all, which is dry-run's real success shape and must NOT decode as false
	// — is pinned by TestClaudePolicyDryRunRefusesThroughTheEnginesStatusNotAVerdict.
	srv := newLot3Server(t, lot3OK(`{"ok":true,"surface":"managed-settings","diagnostics":[]}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "validate", "managed-settings",
		"--content-file", lot3WriteTempJSON(t, `{"permissions":{}}`))...); err != nil {
		t.Fatalf("a clean document must still exit 0, got %v (code %d)", err, exitcode.From(err))
	}
}

// TestClaudePolicyPublishRefusesToCallAnUnreadableAnswerACleanPublish is the
// witness for publish's decode guard, MEASURED to have none: every publish test
// sends a well-formed body, so dropping the decode error changed nothing any test
// observed.
//
// Dropping it left Distribution == "" and skipped the "NOT distributed" warning
// entirely, so an unreadable answer exited 0 having printed nothing at all — the
// one outcome an operator must never get from the verb that ships a policy to
// every host in the estate.
func TestClaudePolicyPublishRefusesToCallAnUnreadableAnswerACleanPublish(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"surface":"hooks","revision":`)) // truncated mid-object
	_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "publish", "hooks",
		"--content-file", lot3WriteTempJSON(t, `{"hooks":[]}`))...)
	if err == nil {
		t.Fatal("a publish answer that cannot be parsed must not exit 0")
	}
	// Degraded, not Server: the engine answered 2xx, so the revision may well
	// exist. Telling a pipeline "server failure" invites the retry that publishes
	// it a second time.
	if got := exitcode.From(err); got != exitcode.Degraded {
		t.Fatalf("exit = %d, want %d (degraded)", got, exitcode.Degraded)
	}
	if !strings.Contains(err.Error(), "DISTRIBUTED is unknown") {
		t.Errorf("the failure must say distribution is unknown, got: %v", err)
	}

	// THE CONTROL: a readable publish still exits 0 and still warns about the
	// undistributed revision, so "always fail" cannot satisfy the above.
	ok := newLot3Server(t, lot3OK(`{"surface":"hooks","revision":4,"distribution":"seam-pending",
		"drift":[],"drift_computed":true}`))
	_, okErr, err := execRoot(t, lot3Args(ok.URL, "claude-policy", "publish", "hooks",
		"--content-file", lot3WriteTempJSON(t, `{"hooks":[]}`))...)
	if err != nil {
		t.Fatalf("a readable publish must exit 0, got %v (code %d)", err, exitcode.From(err))
	}
	if !strings.Contains(okErr, "NOT distributed") {
		t.Errorf("the readable publish lost its warning, got:\n%s", okErr)
	}
}
