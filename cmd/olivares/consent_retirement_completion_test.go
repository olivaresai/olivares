// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"
)

// A clean account is the prerequisite for cases that need retirement to finish,
// publish a blocked verdict, or lift its fence through readmission.
func TestRetirementCompletionIsEditionScoped(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		e.retirementCompletionRefused(t)
	})
}

// requireRetirementCompletion is called only by cases whose blocked/retired
// verdict or later readmission cannot be reached on a refusing composition.
// Each caller states its own prerequisite. Prove the known refusal before
// skipping, so an unrelated failure never counts as non-applicability.
func (e *consentEstate) requireRetirementCompletion(t *testing.T, prerequisite string) {
	t.Helper()
	if !businessErasureRefusedOn(e.engine) {
		return
	}
	if !e.retirementCompletionRefused(t) || t.Failed() {
		t.Fatal("the composition did not prove its declared retirement refusal")
	}
	t.Skipf("%s: this composition refuses account retirement on %s because it cannot prove all session history absent", prerequisite, e.engine)
}

// retirementCompletionRefused drives the real composition and checks the
// edition's record evidence, readmission barrier, and retained account state.
func (e *consentEstate) retirementCompletionRefused(t *testing.T) bool {
	t.Helper()
	if e.eng.census == nil {
		t.Fatal("the composed store exposes no census")
	}
	if r := e.eng.census.CompositionReadiness(); !r.Ready() {
		assertEditionAbsenceProofReadiness(t, e, r)
		if t.Failed() {
			t.Fatal("the composition did not prove its declared absence-proof refusal")
		}
		t.Skipf("account retirement cannot complete with an incomplete reader census: %s", r.Cause())
	}
	const email = "completion@consent.test"
	user := e.onboard(e.tT, email, "viewer")
	e.seedMembership(user, e.tB, "viewer")
	e.scimDelete(e.tT, user)
	e.runPump()
	refused := e.wantEditionRetirementRefusal(t, user, e.tT, retirementSeeder{}, "")
	if refused != businessErasureRefusedOn(e.engine) {
		t.Fatal("retirement completion and the edition's applicability predicate disagree")
	}
	if e.memberOf(user, e.tT) {
		t.Fatal("offboarding retained the target tenant's membership")
	}
	if !refused {
		e.wantRetired(t, user, e.tT)
	}
	r := e.readmit(e.tT, email, "viewer")
	if refused {
		if r.code != http.StatusConflict || r.errorCode() != "retirement_pending" {
			t.Fatalf("readmission during retirement refusal = %d %s, want 409 retirement_pending", r.code, r.raw)
		}
		if e.memberOf(user, e.tT) {
			t.Fatal("refused readmission restored the target tenant's membership")
		}
	} else if r.code != http.StatusCreated {
		t.Fatalf("readmission after retirement = %d %s, want 201", r.code, r.raw)
	}
	if stored, found := e.userByEmail(email); !found || stored.ID != user {
		t.Fatal("retirement removed the deployment account")
	}
	if !e.memberOf(user, e.tB) {
		t.Fatal("retirement removed the other tenant's membership")
	}
	return refused
}
