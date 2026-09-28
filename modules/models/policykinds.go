// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import "github.com/olivaresai/olivares/core/model"

// routingSpecLeaf is a routing knob the router reads: a strategy, capability,
// provider, model, endpoint, execution profile or access tier, never an account.
var routingSpecLeaf = model.None("a routing knob read by the router: routing.go:60-70,111-135")

// The routing policy kind this module writes, registered with the
// composition's policy-kind registry: the core policy writer refuses any other
// kind, and the declaration of a stored policy spec reads the same registry.
func init() {
	model.MustRegisterPolicyKind(policyKindRouting, model.Nested(routingSpec{}, model.ClassEvidence,
		model.Leaf("strategy", routingSpecLeaf),
		model.Leaf("required_capabilities[]", routingSpecLeaf),
		model.Leaf("preferred_providers[]", routingSpecLeaf),
		model.Leaf("pinned_model", routingSpecLeaf),
		model.Leaf("gateway_endpoint", routingSpecLeaf),
		model.Leaf("execution_profile_ref", routingSpecLeaf),
		model.Leaf("execution_profile_revision", routingSpecLeaf),
		model.Leaf("access_tiers[]", routingSpecLeaf),
	))
}
