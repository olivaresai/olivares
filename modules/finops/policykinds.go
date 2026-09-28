// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import "github.com/olivaresai/olivares/core/model"

// The policy kinds this module writes, registered with the composition's
// policy-kind registry: the core policy writer refuses any other kind, and the
// declaration of a stored policy spec reads the same registry. A budget keyed
// on an actor or identity only caps spend; a spend limit scoped to a user is
// that account's own allowance.
func init() {
	model.MustRegisterPolicyKind(policyKindBudget, model.Nested(budgetSpec{}, model.ClassRestrict,
		model.Leaf("dimension", model.None("a closed dimension set: budgets.go:111")),
		model.Leaf("key", model.Scan(model.ClassRestrict)),
		model.Leaf("period", model.None("a closed period set: budgets.go:117")),
		model.Leaf("currency", model.None("a currency code, defaulted and displayed: budgets.go:141-142")),
		model.Leaf("action", model.None("a closed enforcement-action set: budgets.go:123")),
	))
	model.MustRegisterPolicyKind(policyKindSpendLimit, model.Nested(storedSpendLimitSpec{}, model.ClassAuthority,
		model.Leaf("scope_type", model.None("a closed scope set: spendlimits.go:147-170")),
		model.Leaf("scope_key", model.Ref(model.EncodeUserRef, "")),
		model.Leaf("period", model.None("a closed period set: spendlimits.go:171")),
	))
}
