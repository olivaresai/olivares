// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// cmd_audit_recover_persons_test.go covers the two-human bar of the ledger
// recovery ceremony — the most consequential quorum in the estate, because the marker it
// gates is what seals a corrupt audit tail.
//
// It exists because the ordinary fixture COULD NOT express the failing case. The gate
// dependency hands the command an already-resolved approverEvidence, and every fixture
// filled it with one credential per person, so the case production can actually produce
// — one human holding a session AND a token, two credentials, ONE person — never reached
// the command's quorum branch. A double that cannot reproduce what production can is not
// a weaker test, it is a test of the double. The fix needed no seam redesign: the gate is
// a per-test function, so a test can simply state the shape.

// evidenceRecoveryDeps is approvedRecoveryDeps with the gate's evidence stated by the
// caller, so a test can drive the ceremony's own person-quorum re-check directly.
func evidenceRecoveryDeps(f recoveryCLIFixture, ev approverEvidence) auditRecoverDeps {
	d := approvedRecoveryDeps(f, true)
	d.gate = func(_ context.Context, _ *engine, _ model.TenantID, planHash, _, _ string) (string, string, string, approverEvidence, error) {
		return "apr_recovery", nbApproved, planHash, ev, nil
	}
	return d
}

// TestAuditRecoverRefusesOneHumanBehindTwoCredentials is the case the resolved stub used
// to hide: two credentials, one person. Counting credentials would seal a corrupt audit
// tail on one human's say-so.
func TestAuditRecoverRefusesOneAccountBehindTwoCredentials(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	out, err := runRecoveryCLI(t, f, evidenceRecoveryDeps(f, approverEvidence{
		Actors:  []string{"user:alice", "token:cred-7"}, // two credentials…
		Persons: []string{"alice"},                      // …one account
	}), "--dry-run=false")

	if err == nil {
		t.Fatalf("one human behind two credentials must NOT seal a recovery marker:\n%s", out)
	}
	if !strings.Contains(out, "fewer than two distinct approver accounts") {
		t.Fatalf("the refusal must name the two-ACCOUNT bar, got:\n%s", out)
	}
	// The refusal must promise what this binary can VERIFY. It counts decider_user,
	// i.e. accounts; a message promising humans would claim more than the evidence it
	// signs can support (core/auth/person.go). A comment cannot go red when it
	// lies — this assertion is what keeps the sealed wording honest.
	//
	// NOTE (after the contrast): there is deliberately NO banned-word guard here,
	// and the absence is measured, not an oversight. The assert above pins the EXACT
	// phrase, which is strictly stronger for a fixed message: mutating the refusal to
	// "fewer than two distinct HUMANS" or "...approver people" dies on :51 before any
	// word list could look at it. A banned-word loop scoped to that same line is
	// unreachable — it would read as protection and measure nothing.
	//
	// The DR reproduction (core/api/dr_dual_control_identity_test.go) DOES carry one,
	// because there the subject is a JSON body whose wording is not pinned exactly.
	// The credentials must still be reported — they are the provenance an operator needs
	// to see WHICH credentials approved, even (especially) when the answer is a refusal.
	if !strings.Contains(out, "token:cred-7") {
		t.Fatalf("the report must still show the credentials it saw:\n%s", out)
	}
	// And nothing may have been appended: a refused ceremony leaves no marker.
	if strings.Contains(out, `"mutated": true`) {
		t.Fatalf("a refused recovery must not mutate the ledger:\n%s", out)
	}
}

// TestAuditRecoverAcceptsTwoDistinctHumans is the control. A fix that only ever denies is
// not a fix, and without this the test above would pass against a hardcoded refusal.
func TestAuditRecoverAcceptsTwoDistinctAccounts(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	out, err := runRecoveryCLI(t, f, evidenceRecoveryDeps(f, approverEvidence{
		Actors:  []string{"user:alice", "token:cred-7", "user:bob"},
		Persons: []string{"alice", "bob"},
	}), "--dry-run=false")

	if err != nil {
		t.Fatalf("two distinct accounts must seal the marker: err=%v\n%s", err, out)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Fatalf("the ceremony should have succeeded:\n%s", out)
	}
	// The SIGNED evidence carries the credentials, not the people: Approvers is inside
	// recoverPreimage, and the person quorum is deliberately enforced outside it.
	if !strings.Contains(out, "token:cred-7") {
		t.Fatalf("the signed evidence must keep the credential provenance:\n%s", out)
	}
}

// TestAuditRecoverReportsAnApprovalWithNoPersonBehindIt: a personless approval is not one
// of the two humans, and the report must SAY so — "one human short" and "an approval I
// cannot attribute to a human" are different facts for whoever runs this ceremony at 3am.
func TestAuditRecoverReportsAnApprovalWithNoAccountBehindIt(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	out, err := runRecoveryCLI(t, f, evidenceRecoveryDeps(f, approverEvidence{
		Actors:       []string{"user:alice", "token:system-9"},
		Persons:      []string{"alice"},
		Unattributed: 1,
	}), "--dry-run=false")

	if err == nil {
		t.Fatalf("one human plus a personless credential is not two humans:\n%s", out)
	}
	if !strings.Contains(out, `"unattributed_approvals": 1`) {
		t.Fatalf("the report must name the approval it could not attribute:\n%s", out)
	}
}
