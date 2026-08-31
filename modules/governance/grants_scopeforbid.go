// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	cedar "github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/x/exp/ast"
)

const (
	cedarVariablePrincipal = "principal"
	cedarVariableResource  = "resource"
)

// scopeParentProbe identifies one hierarchy edge used by a policy but absent from
// evalGrantBasic's store-free entity graph.
type scopeParentProbe struct {
	variable string
	target   cedar.EntityUID
}

// hasTargetedUnresolvableScopeForbid reports whether a forbid's resolvable head
// constraints target req and the whole policy matches once its missing head/body
// scope-membership edges are made evaluable. It is intentionally called only by
// evalGrantBasic; Scoped already resolves the authoritative hierarchy and must
// retain its normal Cedar semantics.
func hasTargetedUnresolvableScopeForbid(
	policies *cedar.PolicySet,
	entities cedar.EntityMap,
	req cedar.Request,
) bool {
	if policies == nil {
		return false
	}
	for id, policy := range policies.All() {
		if policy == nil || policy.Effect() != cedar.Forbid || !basicPolicyHeadMatches(policy, entities, req) {
			continue
		}

		missing := unresolvableScopeParents(policy, entities, req)
		if len(missing) == 0 {
			continue
		}

		// Use cedar-go's typed AST rather than Cedar text/JSON substring matching:
		// typed variables, entity UIDs and scope nodes distinguish hierarchy `in`
		// from set membership and let us validate the policy head without heuristics.
		probeEntities := addScopeProbeParents(entities, req, missing)
		probeSet := cedar.NewPolicySet()
		probeSet.Add(id, policy)
		_, diag := cedar.Authorize(probeSet, probeEntities, req)
		if len(diag.Reasons) > 0 {
			return true
		}
	}
	return false
}

// basicPolicyHeadMatches evaluates the principal/action/resource constraints against
// the original BASIC graph. An unresolvable principal/resource hierarchy membership
// provisionally matches so the isolated probe can decide it, while every resolvable
// part remains a strict targeting guard.
func basicPolicyHeadMatches(policy *cedar.Policy, entities cedar.EntityMap, req cedar.Request) bool {
	p := policy.AST()
	return basicScopeMatches(p.Principal, cedarVariablePrincipal, req.Principal, entities, req) &&
		basicScopeMatches(p.Action, "", req.Action, entities, req) &&
		basicScopeMatches(p.Resource, cedarVariableResource, req.Resource, entities, req)
}

func basicScopeMatches(
	scope cedarast.IsScopeNode,
	variable string,
	uid cedar.EntityUID,
	entities cedar.EntityMap,
	req cedar.Request,
) bool {
	membershipMatches := func(target cedar.EntityUID) bool {
		if basicEntityIn(uid, target, entities) {
			return true
		}
		if variable == "" {
			return false
		}
		return !scopeMembershipResolvable(scopeParentProbe{variable: variable, target: target}, entities, req)
	}

	switch s := scope.(type) {
	case cedarast.ScopeTypeAll:
		return true
	case cedarast.ScopeTypeEq:
		return uid == s.Entity
	case cedarast.ScopeTypeIn:
		return membershipMatches(s.Entity)
	case cedarast.ScopeTypeInSet:
		for _, parent := range s.Entities {
			if membershipMatches(parent) {
				return true
			}
		}
		return false
	case cedarast.ScopeTypeIs:
		return uid.Type == s.Type
	case cedarast.ScopeTypeIsIn:
		return uid.Type == s.Type && membershipMatches(s.Entity)
	default:
		return false
	}
}

func basicEntityIn(child, ancestor cedar.EntityUID, entities cedar.EntityMap) bool {
	if child == ancestor {
		return true
	}
	seen := map[cedar.EntityUID]struct{}{child: {}}
	pending := []cedar.EntityUID{child}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entity, ok := entities[current]
		if !ok {
			continue
		}
		for parent := range entity.Parents.All() {
			if parent == ancestor {
				return true
			}
			if _, visited := seen[parent]; !visited {
				seen[parent] = struct{}{}
				pending = append(pending, parent)
			}
		}
	}
	return false
}

func unresolvableScopeParents(
	policy *cedar.Policy,
	entities cedar.EntityMap,
	req cedar.Request,
) []scopeParentProbe {
	seen := map[scopeParentProbe]struct{}{}
	var missing []scopeParentProbe
	add := func(variable string, targets []cedar.EntityUID) {
		for _, target := range targets {
			probe := scopeParentProbe{variable: variable, target: target}
			if scopeMembershipResolvable(probe, entities, req) {
				continue
			}
			if _, ok := seen[probe]; ok {
				continue
			}
			seen[probe] = struct{}{}
			missing = append(missing, probe)
		}
	}

	p := policy.AST()
	add(cedarVariablePrincipal, headScopeTargets(p.Principal))
	add(cedarVariableResource, headScopeTargets(p.Resource))
	for _, condition := range p.Conditions {
		cedarast.Inspect(cedarast.NewNode(condition.Body), func(node cedarast.IsNode) bool {
			switch n := node.(type) {
			case cedarast.NodeTypeIn:
				if variable, ok := scopeVariable(n.Left); ok {
					add(variable, entityTargets(n.Right))
				}
			case cedarast.NodeTypeIsIn:
				if variable, ok := scopeVariable(n.Left); ok {
					add(variable, entityTargets(n.Entity))
				}
			}
			return true
		})
	}
	return missing
}

func headScopeTargets(scope cedarast.IsScopeNode) []cedar.EntityUID {
	switch s := scope.(type) {
	case cedarast.ScopeTypeIn:
		return []cedar.EntityUID{s.Entity}
	case cedarast.ScopeTypeInSet:
		return s.Entities
	case cedarast.ScopeTypeIsIn:
		return []cedar.EntityUID{s.Entity}
	default:
		return nil
	}
}

func scopeVariable(node cedarast.IsNode) (string, bool) {
	variable, ok := node.(cedarast.NodeTypeVariable)
	if !ok {
		return "", false
	}
	switch string(variable.Name) {
	case cedarVariablePrincipal, cedarVariableResource:
		return string(variable.Name), true
	default:
		return "", false
	}
}

func entityTargets(node cedarast.IsNode) []cedar.EntityUID {
	switch n := node.(type) {
	case cedarast.NodeValue:
		if uid, ok := n.Value.(cedar.EntityUID); ok {
			return []cedar.EntityUID{uid}
		}
	case cedarast.NodeTypeSet:
		var targets []cedar.EntityUID
		for _, element := range n.Elements {
			targets = append(targets, entityTargets(element)...)
		}
		return targets
	}
	return nil
}

// scopeMembershipResolvable distinguishes a known-false BASIC membership from an
// unknown one. Principal Role/User/Group closure is fully materialized by
// buildPrincipalEntity; resource scope parents are never materialized by the BASIC
// path. Equality (an entity is in itself) is also fully resolvable.
func scopeMembershipResolvable(probe scopeParentProbe, entities cedar.EntityMap, req cedar.Request) bool {
	var child cedar.EntityUID
	switch probe.variable {
	case cedarVariablePrincipal:
		child = req.Principal
	case cedarVariableResource:
		child = req.Resource
	default:
		return true
	}
	if basicEntityIn(child, probe.target, entities) {
		return true
	}
	if probe.variable == cedarVariablePrincipal {
		switch string(probe.target.Type) {
		case cedarTypeRole, cedarTypeUser, cedarTypeGroup:
			return true
		}
	}
	return false
}

func addScopeProbeParents(
	entities cedar.EntityMap,
	req cedar.Request,
	missing []scopeParentProbe,
) cedar.EntityMap {
	probeEntities := make(cedar.EntityMap, len(entities)+len(missing))
	for uid, entity := range entities {
		probeEntities[uid] = entity
	}

	parents := map[cedar.EntityUID][]cedar.EntityUID{}
	for _, probe := range missing {
		var child cedar.EntityUID
		switch probe.variable {
		case cedarVariablePrincipal:
			child = req.Principal
		case cedarVariableResource:
			child = req.Resource
		default:
			continue
		}
		parents[child] = append(parents[child], probe.target)
		if _, exists := probeEntities[probe.target]; !exists {
			probeEntities[probe.target] = cedar.Entity{UID: probe.target}
		}
	}

	for child, extra := range parents {
		entity := probeEntities[child]
		allParents := entity.Parents.Slice()
		allParents = append(allParents, extra...)
		entity.Parents = cedar.NewEntityUIDSet(allParents...)
		probeEntities[child] = entity
	}
	return probeEntities
}
