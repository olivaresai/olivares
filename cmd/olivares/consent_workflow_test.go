// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// createRetirementWorkflow uses the product writer when linked, or seeds a
// retained workflow through the existing guarded fixture seam otherwise.
func (e *consentEstate) createRetirementWorkflow(tenant model.TenantID, name string, steps []map[string]any, users ...model.ID) model.ID {
	e.t.Helper()
	if !editionOrchestrationAvailable {
		stored, err := json.Marshal(steps)
		if err != nil {
			e.t.Fatal(err)
		}
		return e.seedFenced(tenant, "orchestration.workflow", model.Record{
			"name": name, "enabled": true, "steps": string(stored),
			"owner_actor": "system:retained", "owner_actor_kind": "system",
		}, users...)
	}
	return e.created(e.do("POST", "/v1/m/orchestration/workflows", e.admin, tenant, map[string]any{
		"name": name, "steps": steps,
	}), "the workflow")
}

func TestWorkflowObligationInAnotherTenantDoesNotBlockRetirement(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		subject := e.retirementSubjectIn(e.tT, "workflow-other-tenant")
		e.seedMembership(subject.id, e.tB, "viewer")
		steps := participantSteps(t, "work owner", subject.id, subject.id)
		id := e.createRetirementWorkflow(e.tB, "other-tenant", steps, subject.id)
		stored := e.rowColumn(e.tB, "orchestration.workflow", id, "steps")
		if !strings.Contains(stored, subject.id.String()) {
			t.Fatalf("the other tenant's workflow does not name the account: %q", stored)
		}
		e.scimDelete(e.tT, subject.id)
		e.runPump()
		e.wantRetired(t, subject.id, e.tT)
		if got := e.rowColumn(e.tB, "orchestration.workflow", id, "steps"); got != stored {
			t.Errorf("the other tenant's workflow steps = %q, want %q", got, stored)
		}
		if !e.memberOf(subject.id, e.tB) {
			t.Error("retirement removed the other tenant's membership")
		}
	})
}
