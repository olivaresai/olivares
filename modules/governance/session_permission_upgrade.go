// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	cedar "github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/x/exp/ast"
)

var legacySessionPermissions = map[cedar.String]cedar.String{
	"sessions:output:read": "sessions:run:read",
	"sessions:stop:write":  "sessions:run:write",
}

// legacySessionRequest preserves the policy inputs of the operations split from
// run read/write. Resource identity and its stored workspace lineage never change.
func legacySessionRequest(req cedar.Request) (cedar.Request, bool) {
	permission := sessionRequestPermission(req)
	old, split := legacySessionPermissions[permission]
	if !split {
		return req, false
	}
	if req.Action == cedar.NewEntityUID(cedarTypeAction, permission) {
		req.Action = cedar.NewEntityUID(cedarTypeAction, old)
	}
	attrs := req.Context.Map()
	if attrs == nil {
		attrs = cedar.RecordMap{}
	}
	attrs["permission"] = old
	req.Context = cedar.NewRecord(attrs)
	return req, true
}

// authorizeCedar keeps stored policies effective across the split. Managed
// projections use exact permission lists. Authored policies receive the legacy
// permission and permission-derived action by default,
// including wildcard and negative conditions. Only an explicit comparison with
// the new permission opts a policy into the narrow request. Evaluate each policy
// exactly once: evaluating both and OR-ing permits would widen a policy that excludes the old permission with `unless` or `!=`.
func authorizeCedar(policies cedar.PolicyIterator, entities cedar.EntityGetter, req cedar.Request) (cedar.Decision, cedar.Diagnostic) {
	legacy, split := legacySessionRequest(req)
	if !split {
		return cedar.Authorize(policies, entities, req)
	}
	current, previous := cedar.NewPolicySet(), cedar.NewPolicySet()
	for id, policy := range policies.All() {
		if policyNamesSessionPermission(policy, sessionRequestPermission(req)) {
			current.Add(id, policy)
		} else {
			previous.Add(id, policy)
		}
	}
	decision, diag := cedar.Authorize(current, entities, req)
	oldDecision, oldDiag := cedar.Authorize(previous, entities, legacy)
	diag.Errors = append(diag.Errors, oldDiag.Errors...)
	// Only determining forbids are reasons for Deny; unrelated permit reasons must
	// not become restriction evidence. Both evaluations retain their error diagnostics.
	if decision == cedar.Deny && len(diag.Reasons) > 0 {
		if oldDecision == cedar.Deny {
			diag.Reasons = append(diag.Reasons, oldDiag.Reasons...)
		}
		return cedar.Deny, diag
	}
	if oldDecision == cedar.Deny && len(oldDiag.Reasons) > 0 {
		diag.Reasons = oldDiag.Reasons
		return cedar.Deny, diag
	}
	diag.Reasons = append(diag.Reasons, oldDiag.Reasons...)
	return decision || oldDecision, diag
}

func policyNamesSessionPermission(policy *cedar.Policy, permission cedar.String) bool {
	if policy.Annotations()["olivares_session_permissions"] == "exact" {
		return true
	}
	ast := policy.AST()
	switch scope := ast.Action.(type) {
	case cedarast.ScopeTypeEq:
		if scope.Entity == cedar.NewEntityUID(cedarTypeAction, permission) {
			return true
		}
	case cedarast.ScopeTypeInSet:
		for _, entity := range scope.Entities {
			if entity == cedar.NewEntityUID(cedarTypeAction, permission) {
				return true
			}
		}
	case cedarast.ScopeTypeIn:
		if scope.Entity == cedar.NewEntityUID(cedarTypeAction, permission) {
			return true
		}
	}
	found := false
	for _, condition := range ast.Conditions {
		cedarast.Inspect(cedarast.NewNode(condition.Body), func(node cedarast.IsNode) bool {
			var left, right cedarast.IsNode
			switch n := node.(type) {
			case cedarast.NodeTypeNotEquals:
				left, right = n.Left, n.Right
			case cedarast.NodeTypeLike:
				if n.Value.Match(permission) {
					found = found || sessionPermissionComparison(n.Arg, cedarast.NodeValue{Value: permission}, permission)
				}
			case cedarast.NodeTypeEquals:
				left, right = n.Left, n.Right
			case cedarast.NodeTypeIn:
				left, right = n.Left, n.Right
			case cedarast.NodeTypeContains:
				left, right = n.Left, n.Right
			case cedarast.NodeTypeContainsAll:
				left, right = n.Left, n.Right
			case cedarast.NodeTypeContainsAny:
				left, right = n.Left, n.Right
			}
			if left != nil && right != nil {
				found = found || sessionPermissionComparison(left, right, permission) || sessionPermissionComparison(right, left, permission)
			}
			return !found
		})
	}

	return found
}

// Resource names and other unrelated literals cannot opt a stored rule out of
// compatibility. The literal must be compared with action or context.permission.
func sessionPermissionComparison(question, values cedarast.IsNode, permission cedar.String) bool {
	permissionValues, actionValues := false, false
	cedarast.Inspect(cedarast.NewNode(question), func(node cedarast.IsNode) bool {
		switch n := node.(type) {
		case cedarast.NodeTypeVariable:
			actionValues = actionValues || n.Name == "action"
		case cedarast.NodeTypeAccess:
			if variable, ok := n.Arg.(cedarast.NodeTypeVariable); ok {
				permissionValues = permissionValues || variable.Name == "context" && n.Value == "permission"
			}
		}
		return true
	})
	if !permissionValues && !actionValues {
		return false
	}
	found := false
	cedarast.Inspect(cedarast.NewNode(values), func(node cedarast.IsNode) bool {
		if value, ok := node.(cedarast.NodeValue); ok {
			found = found || permissionValues && value.Value == permission || actionValues && value.Value == cedar.NewEntityUID(cedarTypeAction, permission)
		}
		return !found
	})
	return found
}

func sessionRequestPermission(req cedar.Request) cedar.String {
	value, _ := req.Context.Get("permission")
	permission, _ := value.(cedar.String)
	return permission
}
