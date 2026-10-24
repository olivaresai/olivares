// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// policy_observed.scope is the host id the check-in self-asserts. A host id can carry a
// person's login or name, so it is declared scanned, as workstation_tool.scope is. A None
// declaration would say no principal is named while the value holds one.
func TestPolicyObservedScopeIsScannedLikeWorkstationScope(t *testing.T) {
	d, ok := registeredGovernanceSchema(t)["governance.policy_observed"]
	if !ok {
		t.Fatal("governance.policy_observed is not registered")
	}
	found := false
	for _, f := range d.Fields {
		if f.Name != "scope" {
			continue
		}
		found = true
		if f.Principal == nil || f.Principal.Form != model.FormScan || f.Principal.Class != model.ClassEvidence {
			t.Errorf("scope = %#v, want a scanned evidence column", f.Principal)
		}
	}
	if !found {
		t.Fatal("policy_observed has no scope column")
	}
	if defects := d.PrincipalDefects(); len(defects) != 0 {
		t.Fatalf("principal declarations: %v", defects)
	}
}
