// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkevent "github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// maxProjectedAgents bounds how many agent→source PERMITTED edges one binding write
// projects, so a binding to a huge workspace cannot flood the bus. When the scope has
// more agents the projection is truncated and the truncation is LOGGED (never a silent
// cap): the access-map drift view is best-effort observability of the binding, while
// the AUTHORITATIVE control is the resolver's deny-closed enforcement.
const maxProjectedAgents = listCap

// publishBindingEdges projects a source→scope binding onto the access map's PERMITTED
// side (observe→enforce): it emits, for each agent currently in the bound scope,
// a declared edge agent→source with Source=SignalScopedGrant (which access-map maps to
// permitted=true). It runs AFTER the write commits and is BEST-EFFORT: a projection
// failure is logged but never fails the binding write (the durable binding and its
// enforcement stand regardless; the drift view self-heals on the next write/reconcile).
// Retraction of stale permitted edges on unbind is a documented follow-up (access-map
// OR-merges the permitted flag across signals and has no per-signal retract today).
func (m *Module) publishBindingEdges(ctx context.Context, tenant model.TenantID, b bindingDTO) {
	if !b.Enabled {
		return
	}
	// a FORBID subtracts access — it is not a PERMITTED edge, so it projects nothing.
	// (The access-map OR-merges the permitted flag across signals and has no per-signal
	// retract today; enforcement is the resolver's live decision regardless.)
	if normalizeEffect(b.Effect) == effectForbid {
		return
	}
	host := m.host0()
	data := m.moduleData()
	if host == nil || data == nil {
		return
	}

	// Subject axes. The three identity-of-one trees project ONE permitted edge with the
	// matching origin kind (a per-session binding thus appears as an edge of THAT session,
	// brief §4). The GROUP subject trees (user_group, role) would need to enumerate their
	// members — auth-scope entities (users, directory groups) not reachable from this
	// module's tenant store.Scope — so, exactly like a folder binding's reverse-grant
	// projection, they DEFER: log and project nothing. Enforcement is unaffected (the
	// resolver decides per call against the live principal); only the drift view lags.
	switch b.ScopeTree {
	case scopeSession:
		m.publishSubjectEdge(ctx, tenant, b, "session", b.ScopeRef)
		return
	case scopeAgent:
		m.publishSubjectEdge(ctx, tenant, b, "agent", b.ScopeRef)
		return
	case scopeUser:
		m.publishSubjectEdge(ctx, tenant, b, "identity", b.ScopeRef)
		return
	case scopeUserGroup, scopeRole:
		if m.log != nil {
			m.log.Info("sourcescope: group/role-binding access-map projection deferred (member enumeration needs the auth scope, out of module reach); enforcement unaffected",
				"tenant", tenant.String(), "source_ref", b.SourceRef, "scope_tree", b.ScopeTree, "scope_ref", b.ScopeRef)
		}
		return
	case scopeFolder:
		// A folder binding's PERMITTED edges are the REVERSE of its grant — which
		// principals/agents hold a grant over this subtree — a Cedar reverse query that
		// does not exist in-tree yet (the access-map drift view; reverse-grant queries are
		//). We deliberately project NOTHING rather than an empty set that would read
		// as "no agent may use this source": enforcement is unaffected (the resolver
		// decides per call against the live grant), only the best-effort drift view lags.
		if m.log != nil {
			m.log.Info("sourcescope: folder-binding access-map projection deferred (reverse grant query); enforcement unaffected",
				"tenant", tenant.String(), "source_ref", b.SourceRef, "folder", b.ScopeRef)
		}
		return
	}

	var (
		agentRefs []string
		truncated bool
	)
	if err := data.View(ctx, tenant, func(sc store.Scope) error {
		refs, more, err := agentRefsInScope(ctx, sc, b)
		agentRefs, truncated = refs, more
		return err
	}); err != nil {
		if m.log != nil {
			m.log.Warn("sourcescope: access-map projection skipped (scope enumeration failed)",
				"tenant", tenant.String(), "source_ref", b.SourceRef, "err", err)
		}
		return
	}
	if truncated && m.log != nil {
		m.log.Warn("sourcescope: access-map projection truncated (scope larger than cap); drift view is partial for this binding",
			"tenant", tenant.String(), "source_ref", b.SourceRef, "cap", maxProjectedAgents)
	}

	kind := scopeableKindFor(b.SourceType)
	now := m.clock.Now().Time()
	for _, ref := range agentRefs {
		obs := sdkmodel.EdgeObservation{
			OriginKind:   "agent",
			OriginRef:    ref,
			ResourceKind: kind,
			ResourceRef:  b.SourceRef,
			Mode:         sdkmodel.ModeRead,
			Source:       sdkmodel.SignalScopedGrant,
			Confidence:   sdkmodel.ConfidenceAttributed, // the binding is an authoritative control-plane fact
			ObservedAt:   now,
		}
		if err := host.Publish(ctx, sdkevent.FromObservation(tenant.String(), Name, obs)); err != nil && m.log != nil {
			m.log.Warn("sourcescope: access-map edge publish failed (best-effort)",
				"tenant", tenant.String(), "agent_ref", ref, "err", err)
		}
	}
}

// publishSubjectEdge emits ONE permitted edge for a single-subject binding (session /
// agent / user). originKind is the EdgeObservation origin class ("session", "agent" or
// "identity"); originRef is the subject's external id / user id (the binding's scope_ref).
// Best-effort, like the enumerated agent path.
func (m *Module) publishSubjectEdge(ctx context.Context, tenant model.TenantID, b bindingDTO, originKind, originRef string) {
	if originRef == "" {
		return
	}
	obs := sdkmodel.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: scopeableKindFor(b.SourceType),
		ResourceRef:  b.SourceRef,
		Mode:         sdkmodel.ModeRead,
		Source:       sdkmodel.SignalScopedGrant,
		Confidence:   sdkmodel.ConfidenceAttributed, // the binding is an authoritative control-plane fact
		ObservedAt:   m.clock.Now().Time(),
	}
	if err := m.host0().Publish(ctx, sdkevent.FromObservation(tenant.String(), Name, obs)); err != nil && m.log != nil {
		m.log.Warn("sourcescope: access-map subject edge publish failed (best-effort)",
			"tenant", tenant.String(), "origin_kind", originKind, "origin_ref", originRef, "err", err)
	}
}

// agentRefsInScope returns the external ids of the agents in a binding's scope, and
// whether the set was truncated at maxProjectedAgents. For a workspace binding it is
// the agents pinned to that workspace (plus the zero-workspace agents when the binding
// targets the default workspace, since a NULL workspace_id resolves to the default —
// §3.2). For an agent-group binding it is the group's members.
func agentRefsInScope(ctx context.Context, sc store.Scope, b bindingDTO) ([]string, bool, error) {
	switch b.ScopeTree {
	case scopeWorkspace:
		ws, ok, err := findWorkspaceBySlug(ctx, sc, b.ScopeRef)
		if err != nil || !ok {
			return nil, false, err
		}
		refs, trunc, err := agentRefsByWorkspace(ctx, sc, ws.ID, b.ScopeRef == model.DefaultWorkspaceSlug)
		return refs, trunc, err
	case scopeAgentGroup:
		g, ok, err := findAgentGroupBySlug(ctx, sc, b.ScopeRef)
		if err != nil || !ok {
			return nil, false, err
		}
		return agentRefsByGroup(ctx, sc, g.ID)
	default:
		// scopeFolder is short-circuited in publishBindingEdges (its projection needs a
		// reverse grant query); any other tree projects nothing.
		return nil, false, nil
	}
}

// agentRefsByWorkspace lists external ids of agents pinned to workspaceID, plus the
// implicit-default (zero workspace_id) agents when includeDefault is set.
func agentRefsByWorkspace(ctx context.Context, sc store.Scope, workspaceID model.ID, includeDefault bool) ([]string, bool, error) {
	out := make([]string, 0, 16)
	add := func(filters ...model.Filter) (bool, error) {
		q := model.Query{Filters: filters, Limit: listCap}
		for {
			agents, page, err := sc.Agents().List(ctx, q)
			if err != nil {
				return false, err
			}
			for _, a := range agents {
				if a.ExternalID == "" {
					continue
				}
				out = append(out, a.ExternalID)
				if len(out) >= maxProjectedAgents {
					return true, nil
				}
			}
			if !page.HasMore || page.Cursor == "" {
				return false, nil
			}
			q.Cursor = page.Cursor
		}
	}
	trunc, err := add(eq("workspace_id", workspaceID.String()))
	if err != nil || trunc {
		return out, trunc, err
	}
	if includeDefault {
		trunc, err = add(eq("workspace_id", ""))
		if err != nil {
			return out, false, err
		}
	}
	return out, trunc, nil
}

// agentRefsByGroup lists external ids of the agents in an agent-group, resolving member
// agent ids to their external ids.
func agentRefsByGroup(ctx context.Context, sc store.Scope, groupID model.ID) ([]string, bool, error) {
	out := make([]string, 0, 16)
	q := model.Query{Filters: []model.Filter{eq("group_id", groupID.String())}, Limit: listCap}
	for {
		members, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, false, err
		}
		for _, mem := range members {
			a, err := sc.Agents().Get(ctx, mem.AgentID)
			if err != nil {
				continue // an orphan membership (agent gone) projects nothing
			}
			if a.ExternalID == "" {
				continue
			}
			out = append(out, a.ExternalID)
			if len(out) >= maxProjectedAgents {
				return out, true, nil
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return out, false, nil
		}
		q.Cursor = page.Cursor
	}
}
