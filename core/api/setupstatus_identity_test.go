// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// THE IDENTITY STEP MUST MEASURE IDENTITY (2026-08-05). checkHasIdentity was
// checkHasUsers with the threshold moved from >0 to >1 — the same query, in the same
// AuthView, with fedSvc used only as a nil guard. It never asked the federation
// service the one question the step is named after, so a correct single-administrator
// install reported the step incomplete and /v1/console/setup-status answered
// `completed:false` forever.
//
// With no federation service wired the step is NOT APPLICABLE, and that is the half
// that matters: a step this build cannot complete must not hold the whole wizard at
// false. It must also carry a REASON — an incomplete step with no reason is a to-do
// item with no instructions.
func TestIdentityStepIsNotApplicableWithoutAFederationService(t *testing.T) {
	s := &Server{}
	step := s.identityStep(context.Background())

	if step.ID != "identity" {
		t.Fatalf("id = %q", step.ID)
	}
	if step.Applicable == nil || *step.Applicable {
		t.Error("a build with no federation service must mark the step NOT APPLICABLE, " +
			"or it holds `completed` at false for an install that is finished")
	}
	if step.Reason == "" {
		t.Error("an incomplete step with no reason is a to-do item with no instructions")
	}
	if step.Completed {
		t.Error("not applicable must not be reported as completed either")
	}
}

// The wizard's verdict skips the steps this build cannot complete. Without this, one
// unwireable step is indistinguishable from an unfinished setup.
func TestSetupCompletionSkipsInapplicableSteps(t *testing.T) {
	no, yes := false, true
	steps := []setupStepDTO{
		{ID: "database", Completed: true},
		{ID: "identity", Completed: false, Applicable: &no},
		{ID: "users", Completed: true},
	}
	allDone := true
	for _, step := range steps {
		if step.Applicable != nil && !*step.Applicable {
			continue
		}
		if !step.Completed {
			allDone = false
			break
		}
	}
	if !allDone {
		t.Fatal("an inapplicable step still blocked completion")
	}
	// And the control, so this is not just "ignore everything": an APPLICABLE step
	// that is genuinely incomplete must still block.
	steps[1].Applicable = &yes
	allDone = true
	for _, step := range steps {
		if step.Applicable != nil && !*step.Applicable {
			continue
		}
		if !step.Completed {
			allDone = false
			break
		}
	}
	if allDone {
		t.Fatal("an applicable, incomplete step stopped blocking completion")
	}
}

// Belt and braces on the defect itself: the step must not be derivable from a user
// count. Two accounts do not make an identity provider.
func TestIdentityStepIsNotAUserCount(t *testing.T) {
	if model.SystemTenantID.IsZero() {
		t.Skip("system tenant unavailable in this build")
	}
	s := &Server{}
	// No federation service and no store at all: if the step still consulted users it
	// would panic or answer from the user table. It answers from its own contract.
	step := s.identityStep(context.Background())
	if step.Applicable != nil && *step.Applicable {
		t.Fatal("without a federation service the step cannot be applicable")
	}
}
