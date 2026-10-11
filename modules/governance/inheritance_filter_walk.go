// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"iter"

	cedar "github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// The authorizer's side of the inheritance filter (inheritance_filter.go stores the row).
//
// The lineage walk (readScope) already gives the Cedar graph of the target: its workspace,
// agent groups and folder chain. A filter row names one container node of that graph and a
// resource class. When the node is on the target's lineage and the class is the target's
// kind, the walk MARKS the node, and the rest follows from two facts about the algebra
// Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay:
//
//   - the RBAC term and the owner's implicit grant are rights from above the node, so the
//     Authorizer drops them (ScopedDecision.InheritanceFiltered), as it does for
//     RequireScopedGrant;
//   - a Cedar permit is a right from above the node unless its anchor is at or below it. A
//     permit is classified by evaluating it on the graph cut above the marked nodes: it
//     counts only if it requires a positive resource anchor, still matches there and does
//     not match a resource with no lineage at all (a tenant-wide permit matches that too). A forbid is decided on the full
//     graph, before any of this, so a filter never narrows a forbid.
//
// The row is read live inside the resolver's own View, so the answer is as fresh as the
// lineage it sits next to, and a write advances the policy authorization epoch like a grant
// write does (inheritance_filter.go), which is what invalidates a witness minted before it.
//
// ponytail: one indexed read of the filter table per scoped resolution of an entity or
// declared-workspace request, no cache. Cache it by authorization epoch if a profile shows it.

// inheritanceFilterNode is the Cedar entity of the container node a filter row sits on. A
// row whose tree is none of the three container trees, or with no ref, names no node: the
// column reads empty when it is missing, and the walk must not take that for a filter.
func inheritanceFilterNode(f inheritanceFilterDTO) (cedar.EntityUID, bool) {
	if f.ScopeRef == "" || f.ScopeClass == "" {
		return cedar.EntityUID{}, false
	}
	switch f.ScopeTree {
	case scopeWorkspace:
		return cedar.NewEntityUID(cedarTypeWorkspace, cedar.String(f.ScopeRef)), true
	case scopeAgentGroup:
		return cedar.NewEntityUID(cedarTypeAgentGroup, cedar.String(f.ScopeRef)), true
	case scopeFolder:
		return cedar.NewEntityUID(cedarTypeResource, cedar.String(f.ScopeRef)), true
	default:
		return cedar.EntityUID{}, false
	}
}

// inheritanceFiltersOf reads the filter rows for one resource class.
func inheritanceFiltersOf(ctx context.Context, sc store.Scope, class string) ([]inheritanceFilterDTO, error) {
	if class == "" {
		return nil, nil
	}
	repo, err := sc.Ext(inheritanceFilterKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colSGScopeClass, class))
	if err != nil {
		return nil, err
	}
	out := make([]inheritanceFilterDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, recToInheritanceFilter(rec))
	}
	return out, nil
}

// hasInheritanceFilter reports whether any filter names the request's class, without
// reading the lineage. It is the cheap question a tenant with no grant policy asks before
// it pays for a lineage read.
func (r *scopeResolver) hasInheritanceFilter(ctx context.Context, req auth.Request) (bool, error) {
	if !requestHasScope(req) {
		return false, nil
	}
	var found bool
	err := r.data.View(ctx, req.Tenant, func(sc store.Scope) error {
		rows, e := inheritanceFiltersOf(ctx, sc, req.Resource.Kind)
		found = len(rows) > 0
		return e
	})
	return found, err
}

// requestHasScope is the resolver's own fast-path test: a collection request that declares
// no workspace has no lineage, so no node can be above it.
func requestHasScope(req auth.Request) bool {
	return req.Resource.ID != "" || !req.Resource.WorkspaceID.IsZero()
}

// markFilteredNodes is the lineage walk's mark: the filter nodes for the target's class that
// are the target's own container (or the target itself, for a folder or an agent group) in
// the graph em holds for res. Nil when nothing is filtered.
func markFilteredNodes(ctx context.Context, sc store.Scope, req auth.Request, em cedar.EntityMap, res cedar.EntityUID) ([]cedar.EntityUID, error) {
	rows, err := inheritanceFiltersOf(ctx, sc, req.Resource.Kind)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	reach := ancestorsOrSelf(em, res)
	var marks []cedar.EntityUID
	seen := map[cedar.EntityUID]bool{}
	for _, f := range rows {
		node, ok := inheritanceFilterNode(f)
		if ok && reach[node] && !seen[node] {
			seen[node] = true
			marks = append(marks, node)
		}
	}
	return marks, nil
}

// ancestorsOrSelf is every entity the graph reaches from start by parent edges, start
// included. The graph is the resolver's own, a few levels deep and acyclic.
func ancestorsOrSelf(em cedar.EntityMap, start cedar.EntityUID) map[cedar.EntityUID]bool {
	reach := map[cedar.EntityUID]bool{start: true}
	queue := []cedar.EntityUID{start}
	for len(queue) > 0 {
		uid := queue[0]
		queue = queue[1:]
		ent, ok := em[uid]
		if !ok {
			continue
		}
		for p := range ent.Parents.All() {
			if !reach[p] {
				reach[p] = true
				queue = append(queue, p)
			}
		}
	}
	return reach
}

// cutAbove returns the graph with every marked node made a root and every edge to a proper
// ancestor of a marked node removed, wherever it starts. What is left reaches the target only
// through nodes at, below or beside the marked ones. The input is not modified.
func cutAbove(em cedar.EntityMap, marks []cedar.EntityUID) cedar.EntityMap {
	above := map[cedar.EntityUID]bool{}
	root := map[cedar.EntityUID]bool{}
	for _, m := range marks {
		root[m] = true
		for a := range ancestorsOrSelf(em, m) {
			if a != m {
				above[a] = true
			}
		}
	}
	out := make(cedar.EntityMap, len(em))
	for uid, ent := range em {
		var keep []cedar.EntityUID
		if !root[uid] {
			for p := range ent.Parents.All() {
				if !above[p] {
					keep = append(keep, p)
				}
			}
		}
		ent.Parents = cedar.NewEntityUIDSet(keep...)
		out[uid] = ent
	}
	return out
}

// permitsOf iterates the permit policies of a set only, so a forbid that would match the cut
// graph (a `forbid ... unless` does) cannot hide the permits being classified.
type permitsOf struct{ set cedar.PolicyIterator }

func (p permitsOf) All() iter.Seq2[cedar.PolicyID, *cedar.Policy] {
	return func(yield func(cedar.PolicyID, *cedar.Policy) bool) {
		for id, pol := range p.set.All() {
			if pol.Effect() == cedar.Permit && !yield(id, pol) {
				return
			}
		}
	}
}

// noLineageProbeID is the id of the stand-in resource grantAtOrBelow probes with. A real
// resource id is a ULID, so it cannot collide.
const noLineageProbeID = "inheritance-filter:no-lineage"

// grantAtOrBelow reports whether a permit that carries the request is anchored at or below a
// marked node. A permit must require a positive resource anchor; failing a synthetic probe
// alone is not proof of one. It counts when it matches on the full graph (it is one of
// the permits that allowed the request), still matches on the graph cut above the marks, and does not match a
// stand-in resource with no lineage at all. A permit that needs no lineage (tenant-wide, or
// attribute-only) is a right from above and does not count. Every doubt reads as "from above":
// a permit that errors on the probe is lineage-free, one that errors on the cut graph does not
// match there. A permit written with a negated hierarchy (`unless { resource in X }`) can match
// the cut graph without carrying the request, which is why the full graph is asked too.
func grantAtOrBelow(policies cedar.PolicyIterator, em cedar.EntityMap, creq cedar.Request, marks []cedar.EntityUID) bool {
	if len(marks) == 0 {
		return true
	}
	permits := permitsOf{policies}
	_, full := cedar.Authorize(permits, em, creq)
	cutEntities := cutAbove(em, marks)
	_, cut := cedar.Authorize(permits, cutEntities, creq)
	if len(full.Reasons) == 0 || len(cut.Reasons) == 0 {
		return false
	}
	// The lineage-free probe is a stand-in resource: same attributes, no parents, and an id
	// no row can have. Stripping the real resource's parents would not do, because `in` is
	// reflexive and a permit anchored on the target itself would still match it.
	probe := cedar.NewEntityUID(cedarTypeResource, cedar.String(noLineageProbeID))
	bare := make(cedar.EntityMap, len(em)+1)
	for uid, ent := range em {
		bare[uid] = ent
	}
	bare[probe] = cedar.Entity{UID: probe, Attributes: em[creq.Resource].Attributes}
	probeReq := creq
	probeReq.Resource = probe
	_, none := cedar.Authorize(permits, bare, probeReq)
	lineageFree := map[cedar.PolicyID]bool{}
	for _, r := range none.Reasons {
		lineageFree[r.PolicyID] = true
	}
	for _, e := range none.Errors {
		lineageFree[e.PolicyID] = true
	}
	reachable := ancestorsOrSelf(cutEntities, creq.Resource)
	// Collection requests share this synthetic UID across every workspace. It
	// selects a collection, but cannot prove an anchor at a real lineage node.
	delete(reachable, cedar.NewEntityUID(cedarTypeResource, cedar.String("*")))
	anchored := map[cedar.PolicyID]bool{}
	for id, pol := range permits.All() {
		anchored[id] = requiresResourceAnchor(pol, reachable)
	}
	carried := map[cedar.PolicyID]bool{}
	for _, r := range full.Reasons {
		carried[r.PolicyID] = true
	}
	for _, r := range cut.Reasons {
		if carried[r.PolicyID] && !lineageFree[r.PolicyID] && anchored[r.PolicyID] {
			return true
		}
	}
	return false
}

// requiresResourceAnchor proves a necessary positive resource constraint from Cedar's
// typed AST, to an entity still reachable after the cut. A higher anchor on an alternate
// branch must not let a policy switch branches between the full and cut graphs. Cedar
// still decides whether the whole policy matches on both graphs.
// A negative constraint, attribute test or exclusion of the probe cannot establish an
// anchor. For a disjunction both alternatives must require one; merely mentioning a
// scoped alternative beside a tenant-wide alternative is not enough. Unsupported shapes
// conservatively remain inherited.
func requiresResourceAnchor(policy *cedar.Policy, reachable map[cedar.EntityUID]bool) bool {
	p := policy.AST()
	var headAnchor cedar.EntityUID
	switch head := p.Resource.(type) {
	case cedarast.ScopeTypeEq:
		headAnchor = head.Entity
	case cedarast.ScopeTypeIn:
		headAnchor = head.Entity
	case cedarast.ScopeTypeIsIn:
		headAnchor = head.Entity
	}
	if reachable[headAnchor] {
		return true
	}
	// A collection-only head can still require a real anchor in WHEN.
	for _, c := range p.Conditions {
		if c.Condition == cedarast.ConditionWhen && conditionRequiresResourceAnchor(c.Body, reachable) {
			return true
		}
	}
	return false
}

func conditionRequiresResourceAnchor(node cedarast.IsNode, reachable map[cedar.EntityUID]bool) bool {
	switch n := node.(type) {
	case cedarast.NodeTypeAnd:
		return conditionRequiresResourceAnchor(n.Left, reachable) || conditionRequiresResourceAnchor(n.Right, reachable)
	case cedarast.NodeTypeOr:
		return conditionRequiresResourceAnchor(n.Left, reachable) && conditionRequiresResourceAnchor(n.Right, reachable)
	case cedarast.NodeTypeIn:
		return resourceAndEntity(n.Left, n.Right, reachable)
	case cedarast.NodeTypeEquals:
		return resourceAndEntity(n.Left, n.Right, reachable) || resourceAndEntity(n.Right, n.Left, reachable)
	case cedarast.NodeTypeIsIn:
		return resourceAndEntity(n.Left, n.Entity, reachable)
	default:
		return false
	}
}

func resourceAndEntity(variable, literal cedarast.IsNode, reachable map[cedar.EntityUID]bool) bool {
	name, ok := scopeVariable(variable)
	if !ok || name != cedarVariableResource {
		return false
	}
	value, ok := literal.(cedarast.NodeValue)
	if !ok {
		return false
	}
	uid, ok := value.Value.(cedar.EntityUID)
	return ok && reachable[uid]
}
