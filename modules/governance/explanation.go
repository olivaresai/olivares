// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	cedar "github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/x/exp/ast"

	"github.com/olivaresai/olivares/core/auth"
)

const grantIDAnnotation = "olivares_grant_id"

// reasonPolicyIDs preserves Cedar's determining-policy order without duplicates.
func reasonPolicyIDs(reasons []cedar.DiagnosticReason) []cedar.PolicyID {
	out := make([]cedar.PolicyID, 0, len(reasons))
	seen := make(map[cedar.PolicyID]bool, len(reasons))
	for _, reason := range reasons {
		if !seen[reason.PolicyID] {
			seen[reason.PolicyID] = true
			out = append(out, reason.PolicyID)
		}
	}
	return out
}

// decisionGrants identifies the permits that determined an allow.
func decisionGrants(policies *cedar.PolicySet, ids []cedar.PolicyID) []auth.DecisionGrant {
	out := make([]auth.DecisionGrant, 0, len(ids))
	for _, id := range ids {
		policy := policies.Get(id)
		if policy == nil || policy.Effect() != cedar.Permit {
			continue
		}
		out = append(out, auth.DecisionGrant{
			GrantID:  policyAnnotation(policy, grantIDAnnotation),
			PolicyID: string(id),
			Node:     policyResourceNode(policy),
			Subject:  policySubject(policy),
		})
	}
	return out
}

// decisionForbids identifies the authored forbids that determined a deny.
func decisionForbids(policies *cedar.PolicySet, reasons []cedar.DiagnosticReason) []auth.DecisionRemoval {
	out := make([]auth.DecisionRemoval, 0, len(reasons))
	for _, id := range reasonPolicyIDs(reasons) {
		policy := policies.Get(id)
		if policy == nil || policy.Effect() != cedar.Forbid {
			continue
		}
		name := policyAnnotation(policy, "id")
		if name == "" {
			name = string(id)
		}
		out = append(out, auth.DecisionRemoval{Kind: "forbid", ID: name, Node: policyResourceNode(policy)})
	}
	return out
}

func policyAnnotation(policy *cedar.Policy, name string) string {
	for key, value := range policy.Annotations() {
		if string(key) == name {
			return string(value)
		}
	}
	return ""
}

func policySubject(policy *cedar.Policy) auth.DecisionSubject {
	var uid cedar.EntityUID
	switch scope := policy.AST().Principal.(type) {
	case cedarast.ScopeTypeEq:
		uid = scope.Entity
	case cedarast.ScopeTypeIn:
		uid = scope.Entity
	case cedarast.ScopeTypeIsIn:
		uid = scope.Entity
	}
	return auth.DecisionSubject{Kind: decisionEntityKind(uid), Ref: string(uid.ID)}
}

func policyResourceNode(policy *cedar.Policy) auth.DecisionNode {
	ast := policy.AST()
	switch scope := ast.Resource.(type) {
	case cedarast.ScopeTypeEq:
		return decisionNode(scope.Entity)
	case cedarast.ScopeTypeIn:
		return decisionNode(scope.Entity)
	case cedarast.ScopeTypeIsIn:
		return decisionNode(scope.Entity)
	}
	for _, condition := range ast.Conditions {
		if condition.Condition != cedarast.ConditionWhen {
			continue
		}
		if uid, ok := firstResourceAnchor(condition.Body); ok {
			return decisionNode(uid)
		}
	}
	return auth.DecisionNode{}
}

func firstResourceAnchor(node cedarast.IsNode) (cedar.EntityUID, bool) {
	switch n := node.(type) {
	case cedarast.NodeTypeAnd:
		if uid, ok := firstResourceAnchor(n.Left); ok {
			return uid, true
		}
		return firstResourceAnchor(n.Right)
	case cedarast.NodeTypeOr:
		left, leftOK := firstResourceAnchor(n.Left)
		right, rightOK := firstResourceAnchor(n.Right)
		return left, leftOK && rightOK && left == right
	case cedarast.NodeTypeIn:
		return resourceEntityLiteral(n.Left, n.Right)
	case cedarast.NodeTypeEquals:
		if uid, ok := resourceEntityLiteral(n.Left, n.Right); ok {
			return uid, true
		}
		return resourceEntityLiteral(n.Right, n.Left)
	case cedarast.NodeTypeIsIn:
		return resourceEntityLiteral(n.Left, n.Entity)
	default:
		return cedar.EntityUID{}, false
	}
}

func resourceEntityLiteral(variable, literal cedarast.IsNode) (cedar.EntityUID, bool) {
	name, ok := scopeVariable(variable)
	if !ok || name != cedarVariableResource {
		return cedar.EntityUID{}, false
	}
	value, ok := literal.(cedarast.NodeValue)
	if !ok {
		return cedar.EntityUID{}, false
	}
	uid, ok := value.Value.(cedar.EntityUID)
	return uid, ok
}

func decisionNode(uid cedar.EntityUID) auth.DecisionNode {
	kind := decisionEntityKind(uid)
	if uid.Type == cedarTypeResource {
		kind = scopeFolder
	}
	return auth.DecisionNode{Kind: kind, Ref: string(uid.ID)}
}

// Cedar uses Resource for leaves as well as folder ancestors. A direct anchor
// names the request's entity kind; only an ancestor Resource denotes a folder.
func decisionNodeForResource(node auth.DecisionNode, resource auth.ResourceAttrs) auth.DecisionNode {
	if node.Kind == scopeFolder && node.Ref == resource.ID && resource.Kind != "" {
		node.Kind = resource.Kind
	}
	return node
}

func decisionEntityKind(uid cedar.EntityUID) string {
	switch uid.Type {
	case cedarTypeWorkspace:
		return scopeWorkspace
	case cedarTypeAgentGroup:
		return scopeAgentGroup
	case cedarTypeUser:
		return subjectUser
	case cedarTypeRole:
		return subjectRole
	case cedarTypeGroup:
		return subjectGroup
	case cedarTypeResource:
		return "resource"
	default:
		return string(uid.Type)
	}
}
