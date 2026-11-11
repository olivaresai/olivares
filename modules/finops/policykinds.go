// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"encoding/json"

	"github.com/olivaresai/olivares/core/model"
)

// storedBudgetSpec is a stored budget spec as its declaration reads it. The money and
// flag cells stay raw: the write seam and the retirement step decode the spec for its
// exempt actors, so a malformed cell there must not refuse the write or every
// account's removal; the evaluation refuses it (alert_evaluation.go:152, :224).
type storedBudgetSpec struct {
	Dimension        string          `json:"dimension"`
	Key              string          `json:"key,omitempty"`
	LimitMicroUSD    json.RawMessage `json:"limit_micro_usd"`
	Period           string          `json:"period"`
	Thresholds       json.RawMessage `json:"thresholds,omitempty"`
	Currency         string          `json:"currency,omitempty"`
	Action           string          `json:"action,omitempty"`
	ReservedMicroUSD json.RawMessage `json:"reserved_micro_usd,omitempty"`
	FailClosed       json.RawMessage `json:"fail_closed,omitempty"`
	ExemptActors     []string        `json:"exempt_actors,omitempty"`
}

// The policy kinds this module writes, registered with the composition's
// policy-kind registry: the core policy writer refuses any other kind, and the
// declaration of a stored policy spec reads the same registry. A budget keyed
// on an actor or identity only caps spend, so its key restricts; the people a
// group budget lets continue, and a spend limit scoped to a user, are authority.
func init() {
	model.MustRegisterPolicyKind(policyKindBudget, model.Nested(storedBudgetSpec{}, model.ClassAuthority,
		model.Leaf("dimension", model.None("a closed dimension set: budgets.go:111")),
		model.Leaf("key", model.Scan(model.ClassRestrict)),
		model.Leaf("limit_micro_usd", model.None("a money cell the evaluation reads: alert_evaluation.go:152")),
		model.Leaf("thresholds", model.None("alert fractions: budgets.go:221")),
		model.Leaf("reserved_micro_usd", model.None("a money cell the evaluation reads: alert_evaluation.go:224")),
		model.Leaf("fail_closed", model.None("an enforcement flag: budgets.go:225")),
		model.Leaf("period", model.None("a closed period set: budgets.go:117")),
		model.Leaf("currency", model.None("a currency code, defaulted and displayed: budgets.go:141-142")),
		model.Leaf("action", model.None("a closed enforcement-action set: budgets.go:123")),
		// A user_group budget's standing exceptions: the people it lets continue. The tenant's
		// own decision about an account, so removing that account is blocked until the budget
		// is edited, as it is for a spend limit scoped to the account.
		model.Leaf("exempt_actors[]", model.Ref(model.EncodeUserRef, model.ClassAuthority)),
	))
	model.MustRegisterPolicyKind(policyKindSpendLimit, model.Nested(storedSpendLimitSpec{}, model.ClassAuthority,
		model.Leaf("scope_type", model.None("a closed scope set: spendlimits.go:147-170")),
		model.Leaf("scope_key", model.Ref(model.EncodeUserRef, "")),
		model.Leaf("period", model.None("a closed period set: spendlimits.go:171")),
	))
}
