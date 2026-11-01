// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import "github.com/olivaresai/olivares/core/model"

// The policy kinds this module writes, registered with the composition's
// policy-kind registry: the core policy writer refuses any other kind, and the
// declaration of a stored policy spec reads the same registry. An ABAC policy
// holds deny rules only, so it restricts; an approval policy names actions and
// subject kinds, never an account.
func init() {
	model.MustRegisterPolicyKind(policyKindABAC, model.Nested(abacSpec{}, model.ClassRestrict,
		model.Leaf("rules[].permission", model.None("a permission a deny rule matches: policy.go:161")),
		model.Leaf("rules[].verb", model.None("a closed verb set: policy.go:155")),
		model.Leaf("rules[].resource", model.None("a resource a deny rule matches: policy.go:161")),
		model.Leaf("rules[].principal_kind", model.None("a closed principal-kind set: policy.go:158")),
	))
	model.MustRegisterPolicyKind(policyKindApproval, model.Nested(approvalSpec{}, model.ClassEvidence,
		model.Leaf("risk_tier", model.None("a closed risk-tier set: policy.go:197, risktier.go:55")),
		model.Leaf("match.action", model.None("an action name an approval matches: policy.go:193")),
		model.Leaf("match.subject_kind", model.None("a subject kind an approval matches: policy.go:193")),
		model.Leaf("match.change_class", model.None("a closed change-class set an approval matches: policy.go:225, changeclass.go:57")),
		model.Leaf("sensitive_paths[]", model.None("a repository path glob that makes a change sensitive, never an account: policy.go:233, changeclass.go:62")),
		model.Leaf("change_record.system", model.None("a closed change-system name the reader is called with, never an account: approval_changerecord.go:69, approval_changerecord.go:214")),
		model.Leaf("change_record.key", model.None("a record number the change system is asked for, never an account: approval_changerecord.go:73, approval_changerecord.go:214")),
		model.Leaf("change_record.destination", model.None("the name of the destination holding the read-only credential by reference, never a principal: approval_changerecord.go:76, approval_changerecord.go:95, approval_changerecord.go:121-124, approval_changerecord.go:214")),
		model.Leaf("change_record.states[]", model.None("a bounded set of states compared with the change system's record, never an account: approval_changerecord.go:80, approval_changerecord.go:226")),
	))
	model.MustRegisterPolicyKind(policyKindGrantExpiry, model.Nested(grantExpirySpec{}, model.ClassEvidence))
}
