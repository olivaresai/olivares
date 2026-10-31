// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"testing"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
)

func TestExplanationDoesNotInventAScopeFromOneAlternative(t *testing.T) {
	for _, tc := range []struct {
		name, condition string
		want            auth.DecisionNode
	}{
		{"one scope", `resource in Workspace::"payments"`, auth.DecisionNode{Kind: "workspace", Ref: "payments"}},
		{"alternative scope", `resource in Workspace::"payments" || resource in Workspace::"sales"`, auth.DecisionNode{}},
		{"unscoped alternative", `resource in Workspace::"payments" || true`, auth.DecisionNode{}},
		{"shared scope", `(resource in Workspace::"payments" && true) || (resource in Workspace::"payments" && false)`, auth.DecisionNode{Kind: "workspace", Ref: "payments"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies, err := cedar.NewPolicyListFromBytes("scope.cedar", []byte(`permit(principal, action, resource) when { `+tc.condition+` };`))
			if err != nil {
				t.Fatal(err)
			}
			if got := policyResourceNode(policies[0]); got != tc.want {
				t.Fatalf("scope = %+v, want %+v", got, tc.want)
			}
		})
	}
}
