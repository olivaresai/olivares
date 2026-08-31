// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// This is the AGPL composition-root bridge that makes module VIII's governed
// retrieval REAL: it implements knowledge.RetrievalGuard by resolving a
// requesting agent's retrieval authority from (governance) and (the identity
// roster). It lives here, in /cmd (AGPL), because it bridges the AGPL module port
// (core/model.TenantID, the knowledge.Grants type) to the governance/identity data —
// the same reason claudeEmbedderAdapter lives here, not in a connector.
//
// It READS grants, it does not DECIDE policy (the contract's fence): the ABAC engine
// already gated the route (boot.go wires auth.NewAuthorizer(gov.Evaluator())); this
// guard adds the per-IDENTITY chunk governance the route cannot express — the agent's
// group memberships (for chunk ACL), its classification clearance, and its data-
// residency region. Every failure or unresolvable subject is DENY-CLOSED: a store
// error denies the whole retrieval (fail closed, like ABAC); an un-bound or
// un-agented request grants only PUBLIC, unrestricted content (never a classified or
// ACL'd over-grant) — strictly an improvement on the denyGuard default it replaces,
// which grants NO groups at all.
//
// Sourcing (verified against the code, not assumed): the agent→identity link is
// Agent.IdentityID (binding); group membership is the governance.collection_member
// edge set, walked transitively UP the nesting; clearance and region are the
// non-PII roster attributes attr_clearance / attr_region that added to the roster
// allow-list (governance/roster.go) — absent, the guard falls closed to public / no
// region. See the knowledge/RAG contract.

// governanceRetrievalGuard resolves knowledge.Grants from the governed identity plane.
// Its store handle is LATE-BOUND (useData) because the composition root builds the
// module set before the store-backed ModuleData exists (the approval-bridge pattern).
type governanceRetrievalGuard struct {
	log *slog.Logger

	mu           sync.RWMutex
	data         api.ModuleData
	guardPosture *sourcescope.Resolver
}

var _ knowledge.RetrievalGuard = (*governanceRetrievalGuard)(nil)

func newGovernanceRetrievalGuard(log *slog.Logger) *governanceRetrievalGuard {
	return &governanceRetrievalGuard{log: log}
}

// useGuardPostureResolver binds the-B guard-posture reader. It is separate
// from knowledgeScopeGate: source-scope decides WHICH actor can reach a KB, while
// this posture only decides whether the retrieval guard stays ACL-aware or is
// deliberately downgraded to public-only.
func (g *governanceRetrievalGuard) useGuardPostureResolver(r *sourcescope.Resolver) {
	g.mu.Lock()
	g.guardPosture = r
	g.mu.Unlock()
}

// useData binds the tenant-scoped store handle after api.NewModuleData (boot.go).
func (g *governanceRetrievalGuard) useData(d api.ModuleData) {
	g.mu.Lock()
	g.data = d
	g.mu.Unlock()
}

// govMemberKind is the governance.collection_member entity kind — read by
// ref, never imported, so this bridge stays decoupled from governance's Go internals.
const govMemberKind model.Kind = "governance.collection_member"

// The collection_member columns and the bounded transitive-walk depth.
const (
	govColCollectionRef   = "collection_ref"
	govColMemberRef       = "member_ref"
	govMaxMembershipDepth = 32 // mirrors governance's bounded transitive walk
)

// Resolve returns the agent's retrieval grants. See the type doc for the deny-closed
// policy; an error here means DENY (the module records a denied lineage row).
func (g *governanceRetrievalGuard) Resolve(ctx context.Context, tenant model.TenantID, _, agentRef, kbName string) (knowledge.Grants, error) {
	g.mu.RLock()
	data := g.data
	guardPosture := g.guardPosture
	g.mu.RUnlock()
	if data == nil {
		// Wired but never bound (a boot-ordering defect) — public only, never an open
		// allow, and visible (the caller sees the reason on every result).
		return publicOnly("retrieval guard not bound to the store; only public, unrestricted content"), nil
	}

	kbName = strings.TrimSpace(kbName)
	if kbName != "" && guardPosture != nil {
		posture, err := guardPosture.GuardPosture(ctx, tenant, sourcescope.SourceKnowledge, kbName)
		if err != nil {
			if g.log != nil {
				g.log.Warn("knowledge retrieval guard: guard posture read failed; denying (fail closed)", "tenant", tenant.String(), "kb", kbName, "err", err)
			}
			return knowledge.Grants{}, err
		}
		if posture.Profile == sourcescope.GuardProfilePublicOnly {
			return publicOnly("knowledge base guard posture is public_only; ACL-aware retrieval is disabled by an approved posture request"), nil
		}
	}

	agentRef = strings.TrimSpace(agentRef)
	if agentRef == "" {
		// Governed retrieval is agent-centric: the chunk ACL/clearance is an NHI's
		// authority. With no agent subject there is no identity to authorize, so only
		// public, unrestricted content is retrievable (deny-closed for classified/ACL'd).
		return publicOnly("no agent_ref on the retrieval; only public, unrestricted content is retrievable"), nil
	}

	var grants knowledge.Grants
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(ctx, eqQuery("external_id", agentRef))
		if err != nil {
			return err
		}
		if len(agents) == 0 || agents[0].IdentityID.IsZero() {
			grants = publicOnly("agent has no bound identity (binding); only public, unrestricted content")
			return nil
		}
		ident, err := sc.Identities().Get(ctx, agents[0].IdentityID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				grants = publicOnly("bound identity not found; only public, unrestricted content")
				return nil
			}
			return err
		}
		// A disabled identity is denied outright at the KB level (deny-closed).
		if metaBool(ident.Metadata, "disabled") {
			grants = knowledge.Grants{Allowed: false, Reason: "identity is disabled"}
			return nil
		}
		groups, err := resolveIdentityGroups(ctx, sc, ident.ExternalID)
		if err != nil {
			return err
		}
		grants = knowledge.Grants{
			Allowed:   true,
			Groups:    groups,
			Clearance: metaString(ident.Metadata, "attr_clearance"), // "" ⇒ public (module normalizes)
			Region:    metaString(ident.Metadata, "attr_region"),    // "" ⇒ denied for a region-locked KB
			Reason:    "resolved from (agent → identity → groups/clearance/region)",
		}
		return nil
	})
	if err != nil {
		// Fail CLOSED on a store/resolution error — never a degraded allow (parallels
		// ABAC and what retrieval.go already expects on a guard error).
		g.log.Warn("knowledge retrieval guard: grant resolution failed; denying (fail closed)", "tenant", tenant.String(), "err", err)
		return knowledge.Grants{}, err
	}
	return grants, nil
}

// resolveIdentityGroups returns the collection (group/role) refs the identity belongs
// to, transitively UP the nesting (a member of a sub-group inherits its parent group's
// ACL grants). It walks the governance.collection_member edges (member_ref → the
// parent collection_ref), bounded in depth and cycle-safe. A member belonging to no
// collection — or a deployment without the governance module — yields no groups, so
// only unrestricted (empty-ACL / "anyone") chunks are retrievable.
func resolveIdentityGroups(ctx context.Context, sc store.Scope, identityExternalID string) ([]string, error) {
	if strings.TrimSpace(identityExternalID) == "" {
		return nil, nil
	}
	repo, err := sc.Ext(govMemberKind)
	if err != nil {
		return nil, nil // governance entity not registered → no group ACL (public only)
	}
	var out []string
	seen := map[string]bool{}
	frontier := []string{identityExternalID}
	for depth := 0; depth < govMaxMembershipDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, member := range frontier {
			edges, err := drainEdges(ctx, repo, govColMemberRef, member)
			if err != nil {
				return nil, err
			}
			for _, e := range edges {
				col := strings.TrimSpace(e.String(govColCollectionRef))
				if col == "" || seen[col] {
					continue
				}
				seen[col] = true
				out = append(out, col)
				next = append(next, col) // the collection may itself nest in a parent
			}
		}
		frontier = next
	}
	return out, nil
}

// drainEdges reads every membership edge whose member_ref column equals val (paging
// through the store so a member in many groups is fully resolved).
func drainEdges(ctx context.Context, repo store.GenericRepo, col, val string) ([]model.Record, error) {
	var out []model.Record
	q := model.Query{Filters: []model.Filter{{Column: col, Op: model.OpEq, Value: val}}, Limit: 1000}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// publicOnly is the deny-closed grant: the KB read is permitted but only public,
// unrestricted (empty-ACL) content survives the module's chunk filter.
func publicOnly(reason string) knowledge.Grants {
	return knowledge.Grants{Allowed: true, Clearance: "public", Reason: reason}
}

// eqQuery is a single-row equality query on a typed repository column.
func eqQuery(col, val string) model.Query {
	return model.Query{Filters: []model.Filter{{Column: col, Op: model.OpEq, Value: val}}, Limit: 1}
}

func metaBool(meta map[string]any, key string) bool {
	if v, ok := meta[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func metaString(meta map[string]any, key string) string {
	if v, ok := meta[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
