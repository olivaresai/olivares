// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A budget's standing exceptions name people, so the spec declares them as counted person
// references: an account named there is found by the retirement step and the declaration
// census, and removing it is blocked until the tenant edits the budget.
func TestBudgetExemptActorsAreCountedPersonReferences(t *testing.T) {
	user := model.NewID()
	other := model.NewID()
	spec := budgetSpec{
		Dimension: "user_group", Key: "group", Period: "monthly", LimitMicroUSD: 1,
		ExemptActors: []string{"user:" + user.String()},
	}.toSpecMap()
	// A stored spec is JSON: read it back as the store returns it.
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	ids, err := policySpecDecl.CountedUserIDs(model.Record{"kind": policyKindBudget, "spec": stored}, "spec")
	if err != nil {
		t.Fatal(err)
	}
	found := map[model.ID]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found[user] || found[other] || len(ids) != 1 {
		t.Fatalf("counted accounts of the budget spec = %v, want only the exempt actor %s", ids, user)
	}
	// The declaration classifies every leaf of the stored spec.
	policies := model.EntityDescriptor{Kind: "fixture.policies", Fields: []model.FieldSpec{
		{Name: "kind", Kind: model.KindText, Principal: model.None("the fixture's discriminator: policykinds_test.go:1")},
		{Name: "spec", Kind: model.KindJSON, Principal: policySpecDecl},
	}}
	for _, defect := range policies.PrincipalDefects() {
		t.Error(defect)
	}
}
