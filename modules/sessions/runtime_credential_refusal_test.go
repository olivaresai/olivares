// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tokenFileMintFailure is the error the composition root's token-file adapter
// actually returns for a CONFIGURED but unreadable credential file: the module's
// classification wrapped around the executor's own deny-closed cause, exactly as
// cmd/olivares builds it (see the cmd-side battery, which proves this shape against
// a real file and the real executor source).
func tokenFileMintFailure() error {
	return fmt.Errorf("%w: %w", ErrCredentialUnavailable,
		errors.New("executor: no credential source wired; actuation denied (no default/long-lived key): "+
			"no short-lived token at the configured path for env=\"operate\" mode=read"))
}

// refusalBody drives the REAL API mapper and returns the status and message an
// operator would actually receive.
func refusalBody(t *testing.T, err error) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	writeRunErr(rec, err)
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatalf("the refusal body is not the module's error envelope: %v (%s)", e, rec.Body.String())
	}
	return rec.Code, body.Error.Message
}

// A launch whose CONFIGURED token file cannot be read is a deployment fault with a
// remedy, not an internal error. Before this classification existed the raw mint
// error travelled unclassified through denyClosedErr and writeRunErr answered
// 500 {"error":{"message":"internal error"}} — the same two-answer failure the
// unwired case already fixed, on the branch an operator is far more likely to hit
// (they DID configure it; the file is missing, empty or not readable).
func TestWiredTokenFileRefusalAnswers503AndNamesItsVariable(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr),
		WithCredentialSource(CredentialSourceFunc(
			func(context.Context, CredentialRequest) (Credential, error) {
				return Credential{}, tokenFileMintFailure()
			})))

	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatal("an unreadable token file LAUNCHED the session: the deny-closed credential seam did not refuse")
	}

	status, msg := refusalBody(t, err)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %q, want %d: a configured-but-unreadable credential source is an "+
			"unavailable dependency, which is what this module answers for its other issuers",
			status, msg, http.StatusServiceUnavailable)
	}
	if !strings.Contains(msg, envSessionRuntimeTokenFileName) {
		t.Fatalf("message %q does not name %s: an operator told only that it is unavailable "+
			"cannot tell an unreadable file from a source they never wired", msg, envSessionRuntimeTokenFileName)
	}

	// NO LAUNCH AND NO EFFECT. The refusal happens before the runner is reached, so
	// a failed mint cannot leave a process, and a 503 that still spawned would be
	// worse than the 500 it replaces.
	fr.mu.Lock()
	launched := len(fr.specs)
	fr.mu.Unlock()
	if launched != 0 {
		t.Fatalf("the refused launch still reached the runner %d time(s)", launched)
	}

	// REDACTION, on the response: the public text is fixed. None of the cause's
	// wrapped text, the token bytes, the expanded path, the run ref or the host may
	// be concatenated into what the client reads.
	for _, forbidden := range []string{"executor:", "no short-lived token", "env=", "mode=read", "/run/olivares"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("the public message %q carries %q from the wrapped cause; the API text must be fixed", msg, forbidden)
		}
	}
}

// The five refusal classes must stay distinguishable. A rule that collapsed them
// would still pass the assertions above and would destroy the only information the
// operator has about what to do next.
func TestCredentialRefusalsStayDistinct(t *testing.T) {
	t.Parallel()

	// 1. UNWIRED: no credential source configured at all. 503, and it says so.
	unwiredStatus, unwired := refusalBody(t, denyClosedErr("inference credential unavailable", errNoCredential))
	if unwiredStatus != http.StatusServiceUnavailable || !strings.Contains(unwired, "not wired") {
		t.Fatalf("unwired = %d %q, want 503 naming that nothing is wired", unwiredStatus, unwired)
	}

	// 2. WIRED BUT UNREADABLE: also 503, and NOT the same sentence.
	wiredStatus, wired := refusalBody(t, denyClosedErr("inference credential unavailable", tokenFileMintFailure()))
	if wiredStatus != http.StatusServiceUnavailable {
		t.Fatalf("wired-but-unreadable = %d %q, want 503", wiredStatus, wired)
	}
	if wired == unwired {
		t.Fatal("a configured-but-unreadable token file answers the SAME sentence as a source that was " +
			"never wired; the operator cannot tell 'fix the file' from 'choose a source'")
	}
	if !strings.Contains(wired, envSessionRuntimeTokenFileName) {
		t.Fatalf("wired-but-unreadable message %q does not name its variable", wired)
	}

	// 3. AUTHORIZATION DENIAL keeps its own verdict: a decision, not an outage.
	if s, msg := refusalBody(t, denyClosedErr("session identity unavailable", ErrLeaseLost)); s != http.StatusForbidden {
		t.Fatalf("an admission refusal = %d %q, want %d", s, msg, http.StatusForbidden)
	}

	// 4. CANCELED / DEADLINE work is not a verdict about the credential source, even
	//    when it surfaces through the same seam. It must NOT be classified here.
	for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
		both := fmt.Errorf("%w: %w", ErrCredentialUnavailable, ctxErr)
		var re *runErr
		if errors.As(denyClosedErr("inference credential unavailable", both), &re) {
			t.Fatalf("%v carried through the credential seam was classified as %d %q; "+
				"the caller going away is not the credential source refusing", ctxErr, re.status, re.msg)
		}
	}

	// 5. A GENUINE INTERNAL FAILURE (an unreachable mint backend) stays unclassified,
	//    which is the non-firing direction that keeps 503 meaningful.
	var re *runErr
	if errors.As(denyClosedErr("inference credential unavailable",
		errors.New("dial tcp 10.0.0.9:443: connect: connection refused")), &re) {
		t.Fatalf("an unreachable backend was classified as %d %q", re.status, re.msg)
	}
}
