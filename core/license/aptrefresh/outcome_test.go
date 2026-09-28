// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package aptrefresh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

// testRefusal is a connect-v1 refusal as the client's transport reads it, with the body's reason.
type testRefusal struct {
	status int
	code   connectv1.ErrorCode
	reason string
}

func (r *testRefusal) Error() string                    { return fmt.Sprintf("refused %d %s", r.status, r.code) }
func (r *testRefusal) RefusalStatus() int               { return r.status }
func (r *testRefusal) RefusalCode() connectv1.ErrorCode { return r.code }
func (r *testRefusal) RefusalReason() string            { return r.reason }

// noAnswer wraps a transport failure the way net/http reports it.
func noAnswer(err error) error {
	return fmt.Errorf("the connect outcome is unknown: %w", &url.Error{Op: "Post", URL: "https://licenses.olivares.ai/connect/apt-refresh", Err: err})
}

// TestOutcomeOfEveryRefreshPath: rows R7-R11 of Interface Q3 r2 §3.1.4.1, R7 as r3 amends it, map to the
// handoff's outcome and code. A 4xx other than 401 is a definitive refusal; a 401, a lost answer and an
// answer that fails its checks are unknown; a 5xx, a timeout and a failure to reach the service are
// unavailable. Rows R1, R6 and R12 are decided before anything is sent (TestLocalOutcomesAreTheInterfaceNames).
func TestOutcomeOfEveryRefreshPath(t *testing.T) {
	cases := []struct {
		row     string
		err     error
		outcome Outcome
		code    string
	}{
		{"R7 revoked or deleted binding", &testRefusal{status: 403, code: "binding_denied"}, OutcomeRefused, "binding_denied"},
		{"R7 revoked authority", &testRefusal{status: 403, code: "authority_denied", reason: "revoked"}, OutcomeRefused, "authority_denied"},
		{"R8 stale lineage, 403", &testRefusal{status: 403, code: "credential_reissue_required"}, OutcomeRefused, "credential_reissue_required"},
		{"R8 expired stored result, 409", &testRefusal{status: 409, code: "credential_reissue_required"}, OutcomeRefused, "credential_reissue_required"},
		{"R8 generation stale", &testRefusal{status: 409, code: "generation_stale"}, OutcomeRefused, "generation_stale"},
		{"R8 operation conflict", &testRefusal{status: 409, code: "operation_conflict"}, OutcomeRefused, "operation_conflict"},
		{"R9 lost response", noAnswer(io.EOF), OutcomeUnknown, ""},
		{"R9 connection reset after sending", noAnswer(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), OutcomeUnknown, ""},
		{"R10 ledger outage", &testRefusal{status: 503, code: "authority_unavailable"}, OutcomeUnavailable, "authority_unavailable"},
		{"R10 provider adapter missing", &testRefusal{status: 503, code: "provider_adapter_missing"}, OutcomeUnavailable, "provider_adapter_missing"},
		{"R10 timeout", noAnswer(&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}), OutcomeUnavailable, ""},
		{"R10 overall deadline", fmt.Errorf("send: %w", context.DeadlineExceeded), OutcomeUnavailable, ""},
		{"R10 DNS failure", noAnswer(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "licenses.olivares.ai"}}), OutcomeUnavailable, ""},
		{"R10 connection refused", noAnswer(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), OutcomeUnavailable, ""},
		{"R11 set unresolved", &testRefusal{status: 403, code: "security_set_unresolved"}, OutcomeRefused, "security_set_unresolved"},
		{"401 is not definitive", &testRefusal{status: 401, code: "proof_invalid"}, OutcomeUnknown, "proof_invalid"},
		{"a refusal wrapped by the caller", fmt.Errorf("apt-refresh: %w", &testRefusal{status: 403, code: "binding_denied"}), OutcomeRefused, "binding_denied"},
		{"an answer that failed its checks", fmt.Errorf("check: %w", ErrAnswer), OutcomeUnknown, ""},
	}
	for _, tc := range cases {
		o, code := OutcomeOf(tc.err)
		if o != tc.outcome || code != tc.code {
			t.Errorf("%s: OutcomeOf = %q/%q, want %q/%q", tc.row, o, code, tc.outcome, tc.code)
		}
	}
}

// TestOutcomeOfRefusedIsExactlyTheDefinitiveRule: the handoff says refused exactly when the client's
// pending-operation rule ends the step (a 4xx other than 401), so the document never calls a kept step
// refused or a cleared step unknown.
func TestOutcomeOfRefusedIsExactlyTheDefinitiveRule(t *testing.T) {
	for status := 400; status < 600; status++ {
		o, _ := OutcomeOf(&testRefusal{status: status, code: "body_invalid"})
		definitive := status < 500 && status != 401
		if (o == OutcomeRefused) != definitive {
			t.Errorf("HTTP %d: outcome %q, definitive %v", status, o, definitive)
		}
		if o == "" {
			t.Errorf("HTTP %d: no outcome", status)
		}
	}
}

// TestOutcomeOfNeverEchoesAnUnknownCode: a code outside the connect-v1 vocabulary is written as unknown.
func TestOutcomeOfNeverEchoesAnUnknownCode(t *testing.T) {
	_, code := OutcomeOf(&testRefusal{status: 403, code: connectv1.ErrorCode("<script>")})
	if code != string(connectv1.ErrorCodeUnknown) {
		t.Fatalf("code %q, want %q", code, connectv1.ErrorCodeUnknown)
	}
}

// TestLocalOutcomesAreTheInterfaceNames: rows R1, R6 and R12 are decided before anything is sent; their
// outcome names are the Interface's.
func TestLocalOutcomesAreTheInterfaceNames(t *testing.T) {
	for got, want := range map[Outcome]string{
		OutcomeIssued: "issued", OutcomeNotBound: "not_bound", OutcomePendingOtherOperation: "pending_other_operation",
		OutcomeRefused: "refused", OutcomeUnknown: "unknown", OutcomeUnavailable: "unavailable",
		OutcomeIdentityMismatch: "identity_mismatch", OutcomeBusy: "busy",
	} {
		if string(got) != want {
			t.Errorf("outcome %q, want %q", got, want)
		}
	}
	if !errors.Is(fmt.Errorf("x: %w", ErrAnswer), ErrAnswer) {
		t.Fatal("ErrAnswer does not wrap")
	}
}

// TestOutcomeOfAuthorityDeniedNeedsAnR7Reason (F3): authority_denied is refused, the handoff code R7 acts on,
// only with reason revoked, refunded or absent (r3 §3.1.5.4). With another reason or none it is unknown with
// no code: the operation is kept and the helper removes nothing. binding_denied needs no reason.
func TestOutcomeOfAuthorityDeniedNeedsAnR7Reason(t *testing.T) {
	for _, reason := range []string{"revoked", "refunded", "absent"} {
		if o, code := OutcomeOf(&testRefusal{status: 403, code: "authority_denied", reason: reason}); o != OutcomeRefused || code != "authority_denied" {
			t.Errorf("authority_denied with reason %q: %q/%q, want refused/authority_denied", reason, o, code)
		}
	}
	for _, reason := range []string{"", "provenance_unproven", "security_set_unresolved", "binding_denied", "Revoked", "revoked refunded", " revoked"} {
		o, code := OutcomeOf(fmt.Errorf("apt-refresh: %w", &testRefusal{status: 403, code: "authority_denied", reason: reason}))
		if o != OutcomeUnknown || code != "" {
			t.Errorf("authority_denied with reason %q: %q/%q, want unknown with no code", reason, o, code)
		}
	}
	for _, reason := range []string{"", "binding_denied"} {
		if o, code := OutcomeOf(&testRefusal{status: 403, code: "binding_denied", reason: reason}); o != OutcomeRefused || code != "binding_denied" {
			t.Errorf("binding_denied with reason %q: %q/%q, want refused/binding_denied", reason, o, code)
		}
	}
}
