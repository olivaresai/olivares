// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Context-policy scope kinds (the colScopeKind column).
const (
	scopeAgent      = "agent"
	scopeKB         = "kb"
	scopeTenant     = "tenant"
	scopeSession    = "session"
	scopeUser       = "user"
	scopeUserGroup  = "user_group"
	scopeRole       = "role"
	scopeAgentGroup = "agent_group"
	scopeWorkspace  = "workspace"
)

// Context-compaction strategies (the colStrategy column).
const (
	strategyTruncate  = "truncate"
	strategySummarize = "summarize"
	strategyWindow    = "window"
)

// Context-policy effects. Empty stored effect is allow (legacy rows).
const (
	effectAllow  = "allow"
	effectForbid = "forbid"
)

var (
	errContextPolicyNotReady = errors.New("knowledge: context policy resolver not bound to the store")

	validContextPolicyScopes = map[string]bool{
		scopeSession: true, scopeAgent: true, scopeUser: true, scopeUserGroup: true, scopeRole: true,
		scopeAgentGroup: true, scopeKB: true, scopeWorkspace: true, scopeTenant: true,
	}
)

// contextPolicyRequest declares how an agent's/KB's/tenant's context is composed
// and compacted. This module owns it as GOVERNED DATA (what enters, how it is
// compacted, whether redaction is required); the orchestration that APPLIES it at
// model-call time is — Stores and governs the policy, observable and
// auditable (the brief's "contexto/compaction como dato gobernado").
type contextPolicyRequest struct {
	ScopeKind         string         `json:"scope_kind"`
	ScopeRef          string         `json:"scope_ref"`
	MaxTokens         int64          `json:"max_tokens,omitempty"`
	Strategy          string         `json:"strategy,omitempty"`
	RedactionRequired bool           `json:"redaction_required,omitempty"`
	Spec              map[string]any `json:"spec,omitempty"`
	Effect            string         `json:"effect,omitempty"`
}

type contextPolicyDTO struct {
	ID                string         `json:"id"`
	ScopeKind         string         `json:"scope_kind"`
	ScopeRef          string         `json:"scope_ref"`
	MaxTokens         int64          `json:"max_tokens"`
	Strategy          string         `json:"strategy"`
	RedactionRequired bool           `json:"redaction_required"`
	Spec              map[string]any `json:"spec,omitempty"`
	Effect            string         `json:"effect"`
}

func toContextPolicyDTO(rec model.Record) contextPolicyDTO {
	dto := contextPolicyDTO{
		ID: rec.String(model.ColID), ScopeKind: rec.String(colScopeKind), ScopeRef: rec.String(colScopeRef),
		MaxTokens: rec.Int(colMaxTokens), Strategy: rec.String(colStrategy), RedactionRequired: rec.Bool(colRedactReq),
		Effect: normalizeContextPolicyEffect(rec.String(colEffect)),
	}
	if s := rec.String(colSpec); strings.TrimSpace(s) != "" {
		_ = unmarshalInto(s, &dto.Spec)
	}
	return dto
}

// handlePutContextPolicy upserts a context/compaction policy by (scope_kind,
// scope_ref).
func (m *Module) handlePutContextPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req contextPolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.ScopeKind = strings.TrimSpace(req.ScopeKind)
	req.ScopeRef = strings.TrimSpace(req.ScopeRef)
	req.Effect = strings.TrimSpace(req.Effect)
	if !validContextPolicyScopes[req.ScopeKind] {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_kind must be session, agent, user, user_group, role, agent_group, kb, workspace or tenant"))
		return
	}
	if req.ScopeRef == "" || len(req.ScopeRef) > maxRefLen {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref is required and must be short"))
		return
	}
	if req.Effect == "" {
		req.Effect = effectAllow
	}
	if req.Effect != effectAllow && req.Effect != effectForbid {
		writeJSON(w, http.StatusBadRequest, errorBody("effect must be allow or forbid"))
		return
	}
	if req.Strategy == "" {
		req.Strategy = strategyTruncate
	}
	if req.Strategy != strategyTruncate && req.Strategy != strategySummarize && req.Strategy != strategyWindow {
		writeJSON(w, http.StatusBadRequest, errorBody("strategy must be truncate, summarize or window"))
		return
	}
	if req.MaxTokens < 0 || req.MaxTokens > 10_000_000 {
		writeJSON(w, http.StatusBadRequest, errorBody("max_tokens out of range"))
		return
	}
	specJSON := "null"
	if req.Spec != nil {
		specJSON = marshalJSON(req.Spec)
		if len(specJSON) > maxContentLen {
			writeJSON(w, http.StatusBadRequest, errorBody("spec too large"))
			return
		}
	}

	var out contextPolicyDTO
	created := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(ctxPolicyKind)
		if err != nil {
			return err
		}
		existing, ok, err := findOne(r.Context(), repo, eq(colScopeKind, req.ScopeKind), eq(colScopeRef, req.ScopeRef))
		if err != nil {
			return err
		}
		fields := model.Record{
			colScopeKind: req.ScopeKind, colScopeRef: req.ScopeRef, colMaxTokens: req.MaxTokens,
			colStrategy: req.Strategy, colRedactReq: req.RedactionRequired, colSpec: specJSON, colEffect: req.Effect,
		}
		if ok {
			for k, v := range fields {
				existing[k] = v
			}
			updated, err := repo.Update(r.Context(), existing)
			if err != nil {
				return err
			}
			out = toContextPolicyDTO(updated)
			return auditEvent(r.Context(), sc, mc, "knowledge.context.put", ctxPolicyKind, model.ID(out.ID), map[string]any{"scope": req.ScopeKind + ":" + req.ScopeRef})
		}
		rec, err := repo.Create(r.Context(), fields)
		if err != nil {
			return err
		}
		created = true
		out = toContextPolicyDTO(rec)
		return auditEvent(r.Context(), sc, mc, "knowledge.context.put", ctxPolicyKind, model.ID(out.ID), map[string]any{"scope": req.ScopeKind + ":" + req.ScopeRef})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

// handleListContextPolicies lists context policies, optionally filtered by scope.
func (m *Module) handleListContextPolicies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("scope_kind")); v != "" {
		q.Filters = append(q.Filters, eq(colScopeKind, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("scope_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colScopeRef, v))
	}
	out := listResponse[contextPolicyDTO]{Items: []contextPolicyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(ctxPolicyKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toContextPolicyDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ContextPolicyQuery is the caller-provided context for resolving an effective
// context/compaction policy. It is read-only input: unknown refs simply match no row.
type ContextPolicyQuery struct {
	Principal    auth.Principal
	SessionRef   string
	AgentRef     string
	KBRef        string
	WorkspaceRef string
	Model        string
}

// EffectivePolicy is the deterministic composition of the tenant's matching
// context-policy rows for one model call.
type EffectivePolicy struct {
	Deny              bool
	DenyReason        string
	MaxContextTokens  int64
	Strategy          string
	RedactionRequired bool
	ExcludedSources   []string
	WinningScope      string
}

type contextPolicyIdentity struct {
	userID       string
	userGroups   []string
	role         string
	sessionRef   string
	agentRef     string
	agentGroups  []string
	kbRef        string
	workspaceRef string
}

type contextPolicyRow struct {
	id                model.ID
	scopeKind         string
	scopeRef          string
	effect            string
	maxTokens         int64
	strategy          string
	redactionRequired bool
	spec              map[string]any
}

// Apply resolves the effective context policy for one tenant-scoped model call.
// It mirrors source-scope's subject-axis precedence and forbid-absolute algebra,
// but only returns policy metadata; enforcement is intentionally owned by later
// consumers.
func (m *Module) Apply(ctx context.Context, tenant model.TenantID, q ContextPolicyQuery) (EffectivePolicy, error) {
	if m.data == nil {
		return EffectivePolicy{}, errContextPolicyNotReady
	}

	role, _ := q.Principal.RoleIn(tenant)
	id := contextPolicyIdentity{
		userID:       q.Principal.UserID.String(),
		userGroups:   q.Principal.GroupsIn(tenant),
		role:         role,
		sessionRef:   q.SessionRef,
		agentRef:     q.AgentRef,
		kbRef:        q.KBRef,
		workspaceRef: q.WorkspaceRef,
	}
	if q.Principal.AgentIdentity != "" {
		id.agentRef = q.Principal.AgentIdentity
	}

	var matches []contextPolicyRow
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		groups, err := contextPolicyAgentGroupSlugs(ctx, sc, id.agentRef)
		if err != nil {
			return err
		}
		id.agentGroups = groups

		repo, err := sc.Ext(ctxPolicyKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			row := contextPolicyRow{
				id:                model.ID(rec.String(model.ColID)),
				scopeKind:         rec.String(colScopeKind),
				scopeRef:          rec.String(colScopeRef),
				effect:            normalizeContextPolicyEffect(rec.String(colEffect)),
				maxTokens:         rec.Int(colMaxTokens),
				strategy:          rec.String(colStrategy),
				redactionRequired: rec.Bool(colRedactReq),
			}
			if s := rec.String(colSpec); strings.TrimSpace(s) != "" {
				_ = unmarshalInto(s, &row.spec)
			}
			if contextPolicyMatches(id, row) {
				matches = append(matches, row)
			}
		}
		return nil
	})
	if err != nil {
		return EffectivePolicy{}, err
	}

	for _, row := range matches {
		if row.effect == effectForbid {
			return EffectivePolicy{Deny: true, DenyReason: "context denied by a forbid context-policy (deny-closed)"}, nil
		}
	}

	allows := make([]contextPolicyRow, 0, len(matches))
	for _, row := range matches {
		if row.effect == effectAllow {
			allows = append(allows, row)
		}
	}
	sort.Slice(allows, func(i, j int) bool {
		if ri, rj := contextPolicySpecificityRank(allows[i].scopeKind), contextPolicySpecificityRank(allows[j].scopeKind); ri != rj {
			return ri < rj
		}
		if allows[i].scopeRef != allows[j].scopeRef {
			return allows[i].scopeRef < allows[j].scopeRef
		}
		return allows[i].id < allows[j].id
	})

	out := EffectivePolicy{Strategy: strategyTruncate}
	var maxWinner, strategyWinner string
	excludedSeen := map[string]struct{}{}
	// allows is sorted most-specific first. MaxContextTokens is a CEILING, not a
	// tunable value: the MOST-RESTRICTIVE (minimum) applicable cap binds, so a
	// broader scope's ceiling (a tenant/user_group cap) can never be loosened by a
	// more-specific one — that is what "techo enforced" means, and it is the only
	// coherent composition for a resource limit (a loosenable ceiling is no ceiling).
	// Qualitative fields are NOT limits, so they keep semantics: Strategy is
	// most-specific-wins (Q1), RedactionRequired composes by OR and ExcludedSources by
	// union (security floors that a sub-scope cannot switch off). On a tie in the
	// minimum, the most-specific row (first in sort order) owns WinningScope.
	for _, row := range allows {
		if row.maxTokens > 0 && (out.MaxContextTokens == 0 || row.maxTokens < out.MaxContextTokens) {
			out.MaxContextTokens = row.maxTokens
			maxWinner = contextPolicyScopeLabel(row)
		}
		if row.strategy != "" && strategyWinner == "" {
			out.Strategy = row.strategy
			strategyWinner = contextPolicyScopeLabel(row)
		}
		out.RedactionRequired = out.RedactionRequired || row.redactionRequired
		appendExcludedSources(&out.ExcludedSources, excludedSeen, row.spec)
	}
	if maxWinner != "" {
		out.WinningScope = maxWinner
	} else {
		out.WinningScope = strategyWinner
	}
	return out, nil
}

func normalizeContextPolicyEffect(stored string) string {
	if stored == effectForbid {
		return effectForbid
	}
	return effectAllow
}

func contextPolicySpecificityRank(kind string) int {
	switch kind {
	case scopeSession:
		return 0
	case scopeAgent:
		return 1
	case scopeUser:
		return 2
	case scopeUserGroup:
		return 3
	case scopeRole:
		return 4
	case scopeAgentGroup:
		return 5
	case scopeKB:
		return 6
	case scopeWorkspace:
		return 7
	default:
		return 8
	}
}

func contextPolicyMatches(id contextPolicyIdentity, row contextPolicyRow) bool {
	switch row.scopeKind {
	case scopeUser:
		return id.userID != "" && id.userID == row.scopeRef
	case scopeUserGroup:
		return containsString(id.userGroups, row.scopeRef)
	case scopeRole:
		return id.role != "" && id.role == row.scopeRef
	case scopeSession:
		return id.sessionRef != "" && id.sessionRef == row.scopeRef
	case scopeAgent:
		return id.agentRef != "" && id.agentRef == row.scopeRef
	case scopeAgentGroup:
		return containsString(id.agentGroups, row.scopeRef)
	case scopeKB:
		return id.kbRef != "" && id.kbRef == row.scopeRef
	case scopeWorkspace:
		return id.workspaceRef != "" && id.workspaceRef == row.scopeRef
	case scopeTenant:
		return true
	default:
		return false
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func contextPolicyScopeLabel(row contextPolicyRow) string {
	return row.scopeKind + ":" + row.scopeRef
}

func appendExcludedSources(out *[]string, seen map[string]struct{}, spec map[string]any) {
	if spec == nil {
		return
	}
	switch xs := spec["excluded_sources"].(type) {
	case []string:
		for _, s := range xs {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			*out = append(*out, s)
		}
	case []any:
		for _, v := range xs {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			*out = append(*out, s)
		}
	}
}

func contextPolicyAgentGroupSlugs(ctx context.Context, sc store.Scope, agentRef string) ([]string, error) {
	if agentRef == "" {
		return nil, nil
	}
	agents, _, err := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", agentRef)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, nil
	}
	return contextPolicyAgentGroupSlugsByID(ctx, sc, agents[0].ID)
}

func contextPolicyAgentGroupSlugsByID(ctx context.Context, sc store.Scope, agentID model.ID) ([]string, error) {
	if agentID.IsZero() {
		return nil, nil
	}
	members, err := contextPolicyAgentGroupMembers(ctx, sc, agentID)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}
	groups, err := contextPolicyAgentGroups(ctx, sc)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[model.ID]string, len(groups))
	for _, g := range groups {
		bySlug[g.ID] = g.Slug
	}
	var out []string
	for _, mem := range members {
		if slug, ok := bySlug[mem.GroupID]; ok {
			out = append(out, slug)
		}
	}
	return out, nil
}

func contextPolicyAgentGroupMembers(ctx context.Context, sc store.Scope, agentID model.ID) ([]model.AgentGroupMember, error) {
	var out []model.AgentGroupMember
	q := model.Query{Filters: []model.Filter{eq("agent_id", agentID.String())}, Limit: listCap}
	for {
		recs, page, err := sc.AgentGroupMembers().List(ctx, q)
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

func contextPolicyAgentGroups(ctx context.Context, sc store.Scope) ([]model.AgentGroup, error) {
	var out []model.AgentGroup
	q := model.Query{Limit: listCap}
	for {
		recs, page, err := sc.AgentGroups().List(ctx, q)
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
