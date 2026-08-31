// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// onEdge materializes the estate from one access-edge observation: it resolves
// the edge's natural origin and resource references to core entities (creating
// them on first sight) and records a catalog entry for each. It is the
// passive-discovery heart of module I.
//
// It does NOT write the AccessEdge: the R/RW graph is owned by module III (the
// access map), the SOLE writer of AccessEdge, which reconciles identity
// across signals onto a canonical origin — something inventory's naive
// per-signal write could not do (decision A, 2026-06-03). Inventory discovers the
// entities an edge names; the access map records the edges.
func (m *Module) onEdge(ctx context.Context, tenantRef string, edge sdkmodel.EdgeObservation) error {
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		m.debugf("inventory: edge for non-tenant ref; skipped", "tenant", tenantRef)
		return nil
	}
	m.noteTenant(tenant)
	at := edge.ObservedAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}
	source := string(edge.Source)

	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := m.materializeOrigin(ctx, sc, edge, at, source); err != nil {
			return err
		}
		return m.materializeResource(ctx, sc, edge, at, source)
	})
}

// materializeOrigin resolves the edge's origin to a core entity and records its
// catalog entry (an honest skip when the origin kind is unknown or the reference
// empty). The AccessEdge itself is ; inventory only catalogs the entity.
func (m *Module) materializeOrigin(ctx context.Context, sc store.Scope, edge sdkmodel.EdgeObservation, at time.Time, source string) error {
	ref := edge.OriginRef
	if ref == "" {
		return nil
	}
	var (
		kind string
		id   model.ID
		err  error
	)
	switch edge.OriginKind {
	case "session":
		kind = kindSession
		id, err = foSession(ctx, sc, ref, at)
	case "agent":
		kind = kindAgent
		id, err = foAgent(ctx, sc, ref)
	case "identity":
		kind = kindIdentity
		id, err = foIdentity(ctx, sc, ref)
	case "mcp_server":
		kind = kindMCPServer
		id, err = foMCPServer(ctx, sc, ref)
	default:
		return nil
	}
	if err != nil || id.IsZero() {
		return err
	}
	return m.cat(ctx, sc, kind, id, originName(edge.OriginKind, ref), ref, source, edge, at)
}

// materializeResource resolves the edge's resource to a core entity (and, when
// the edge names a tool, a Tool) and records their catalog entries. The
// AccessEdge that ties origin and resource is ; inventory only catalogs.
func (m *Module) materializeResource(ctx context.Context, sc store.Scope, edge sdkmodel.EdgeObservation, at time.Time, source string) error {
	rk := edge.ResourceKind
	ref := edge.ResourceRef

	switch rk {
	case rkMCPTool:
		server, tool := splitServerTool(ref)
		if tool == "" {
			tool = edge.ToolRef
		}
		var serverID model.ID
		if server != "" {
			id, err := foMCPServer(ctx, sc, server)
			if err != nil {
				return err
			}
			serverID = id
			if err := m.cat(ctx, sc, kindMCPServer, serverID, server, server, source, edge, at); err != nil {
				return err
			}
		}
		if tool == "" {
			return nil // nothing nameable to materialize
		}
		hint := mcpReadOnlyHint(edge.Mode)
		toolID, err := foTool(ctx, sc, tool, serverID, &hint)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindTool, toolID, tool, ref, source, edge, at)

	case rkMCPServer:
		if ref == "" {
			return nil
		}
		serverID, err := foMCPServer(ctx, sc, ref)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindMCPServer, serverID, ref, ref, source, edge, at)

	case rkMCPResource, rkMCPResourceTemplate:
		resID, err := foResource(ctx, sc, rk, ref, resourceName(rk, ref))
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindResource, resID, resourceName(rk, ref), ref, source, edge, at)

	case rkMCPPrompt:
		server, name := splitServerTool(ref)
		if name == "" {
			name = ref
		}
		var serverID model.ID
		if server != "" {
			id, err := foMCPServer(ctx, sc, server)
			if err != nil {
				return err
			}
			serverID = id
			if err := m.cat(ctx, sc, kindMCPServer, serverID, server, server, source, edge, at); err != nil {
				return err
			}
		}
		if name == "" {
			return nil
		}
		skillID, err := foSkill(ctx, sc, name, server, serverID)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindSkill, skillID, name, ref, source, edge, at)

	case rkClaudeTool:
		name := edge.ToolRef
		if name == "" {
			name = ref
		}
		if name == "" {
			return nil
		}
		toolID, err := foTool(ctx, sc, name, model.ID(""), nil)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindTool, toolID, name, name, source, edge, at)

	case rkA2AAgent:
		// AIP-05: the remote/peer agent in an observed A2A edge is itself an Agent,
		// not a generic resource — materialize it as one so the estate catalogs
		// non-Claude agents the orchestration graph spans.
		if ref == "" {
			return nil
		}
		agentID, err := foAgent(ctx, sc, ref)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindAgent, agentID, ref, ref, source, edge, at)

	case rkCMAManagedAgent:
		// a CMA session / managed-agent run (e.g. the session a work item wraps) is a
		// Session entity, not a generic resource — and its ToolRef (a work id) is NOT a tool,
		// so this case must not fall through to the default's Tool materialization.
		if ref == "" {
			return nil
		}
		sid, err := foSession(ctx, sc, ref, at)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindSession, sid, ref, ref, source, edge, at)

	case rkCMASkill:
		// a CMA skill attached to an agent is a Skill entity (no MCP server).
		if ref == "" {
			return nil
		}
		skID, err := foSkill(ctx, sc, ref, source, model.ID(""))
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindSkill, skID, ref, ref, source, edge, at)

	case rkCMAAgentDef:
		// a multi-agent roster grant names an agent DEFINITION — an Agent entity
		// (mirrors rkA2AAgent), so the estate catalogs the sub-agents a coordinator may
		// spawn even before any thread runs them.
		if ref == "" {
			return nil
		}
		agentID, err := foAgent(ctx, sc, ref)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindAgent, agentID, ref, ref, source, edge, at)

	case rkCMAAgentTool:
		// an agent's declared built-in/custom tool (the PERMITTED tools[]
		// expansion) is a Tool entity with no MCP server behind it.
		if ref == "" {
			return nil
		}
		toolID, err := foTool(ctx, sc, ref, model.ID(""), nil)
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindTool, toolID, ref, ref, source, edge, at)

	case rkCMAVault, rkCMAVaultCred, rkCMAMemoryStore, rkCMAEnvironment, rkCMAPermPolicy, rkCMADream:
		// the CMA control-plane resources (incl. a Dreams job) are inventoried
		// as Resources under their CMA kind so module I lists them first-class rather than
		// as bare unknowns. The edge's ToolRef (a credential / work / dream id) is NOT a
		// tool, so — unlike the default branch — no Tool is materialized from it.
		if ref == "" {
			return nil
		}
		resID, err := foResource(ctx, sc, rk, ref, resourceName(rk, ref))
		if err != nil {
			return err
		}
		return m.cat(ctx, sc, kindResource, resID, resourceName(rk, ref), ref, source, edge, at)

	default:
		// file / http.url / shell / web.search / agent.task / unknown → a Resource,
		// plus a Tool when the edge names the tool that performed the access.
		name := resourceName(rk, ref)
		resID, err := foResource(ctx, sc, rk, ref, name)
		if err != nil {
			return err
		}
		if err := m.cat(ctx, sc, kindResource, resID, name, ref, source, edge, at); err != nil {
			return err
		}
		if edge.ToolRef != "" {
			toolID, err := foTool(ctx, sc, edge.ToolRef, model.ID(""), nil)
			if err != nil {
				return err
			}
			return m.cat(ctx, sc, kindTool, toolID, edge.ToolRef, edge.ToolRef, source, edge, at)
		}
		return nil
	}
}

// onCost materializes the provider and model an observed cost names, so the
// catalog lists every provider/model actually in use. It does NOT persist a
// CostRecord: cost/FinOps accounting is module XI; inventory only
// discovers the entities. The live token/cost view is module II (sessions).
func (m *Module) onCost(ctx context.Context, tenantRef string, cost sdkmodel.CostSample) error {
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	if cost.ProviderRef == "" && cost.ModelRef == "" {
		return nil
	}
	m.noteTenant(tenant)
	at := cost.OccurredAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}
	const source = "cost"
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		var providerID model.ID
		if cost.ProviderRef != "" {
			var err error
			if providerID, err = foProvider(ctx, sc, cost.ProviderRef); err != nil {
				return err
			}
			if err := m.catRef(ctx, sc, kindProvider, providerID, cost.ProviderRef, cost.ProviderRef, source, at); err != nil {
				return err
			}
		}
		if cost.ModelRef != "" {
			modelID, err := foModel(ctx, sc, cost.ModelRef, providerID)
			if err != nil {
				return err
			}
			if err := m.catRef(ctx, sc, kindModel, modelID, cost.ModelRef, cost.ModelRef, source, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// cat records a catalog entry for an entity discovered through an edge,
// attributing the host the edge was seen on (unknown for cooperative edges).
func (m *Module) cat(ctx context.Context, sc store.Scope, kind string, id model.ID, name, ref, source string, edge sdkmodel.EdgeObservation, at time.Time) error {
	return m.upsertCatalogEntry(ctx, sc, kind, id, name, ref, source, hostOf(edge), at)
}

// catRef records a catalog entry for an entity discovered outside an edge (a
// cost sample), with no host.
func (m *Module) catRef(ctx context.Context, sc store.Scope, kind string, id model.ID, name, ref, source string, at time.Time) error {
	return m.upsertCatalogEntry(ctx, sc, kind, id, name, ref, source, "", at)
}

// originName derives a display name for an origin entity from its kind and ref.
func originName(originKind, ref string) string { return ref }
