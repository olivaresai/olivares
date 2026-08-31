// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// agentcoreexport.go is the AGPL composition half of the AgentCore Cedar
// exporter: it assembles structured governance rows from module VI plus provider
// modules (models/sourcescope), renders them through the Apache connector translator,
// and exposes plan/apply endpoints. The write credential, allowlist, HITL gate and
// auditor are built only in cmd/olivares, mirroring adminactionwiring.go /
// adminactiongate.go. Missing wiring is honest 501 and the exporter stays inert.

// AgentCoreExportProvider lets another module contribute structured rules to the
// AgentCore Cedar export. Implemented by modules/models (model-access) and modules/sourcescope (assignments).
type AgentCoreExportProvider interface {
	AgentCoreExportItems(ctx context.Context, tenant model.TenantID) ([]agentcore.ExportItem, error)
}

// AgentCoreExporter is the narrow module-side seam over the governed connector
// exporter. Tests and cmd wrappers implement this without depending on AWS wire.
type AgentCoreExporter interface {
	Plan(ctx context.Context, engineID, tenant string, desired []agentcore.RenderedPolicy) (agentcore.ExportPlan, error)
	Apply(ctx context.Context, plan agentcore.ExportPlan, spec agentcore.ExportSpec) ([]agentcore.ExportResult, error)
}

var _ AgentCoreExporter = (*agentcore.Exporter)(nil)

// AgentCoreExportBinding is the composition-root AgentCore export provisioning.
// Each tenant carries its own exporter, mapping, policy engine and render mode;
// providers are process-local modules that contribute their tenant rows.
type AgentCoreExportBinding struct {
	Tenants   []AgentCoreExportTenantBinding
	Providers []AgentCoreExportProvider
}

// AgentCoreExportTenantBinding is one tenant's governed AgentCore export target.
type AgentCoreExportTenantBinding struct {
	TenantRef       string
	Exporter        AgentCoreExporter
	Mapping         agentcore.ExportMapping
	PolicyEngineID  string
	EnforcementMode string
}

type agentCoreExportTarget struct {
	exporter        AgentCoreExporter
	mapping         agentcore.ExportMapping
	policyEngineID  string
	enforcementMode string
}

// WithAgentCoreExport wires the optional governed AgentCore export surface. A nil
// or empty binding leaves the route handlers present but unbound (501), matching the
// module's other optional actuation seams.
func WithAgentCoreExport(b AgentCoreExportBinding) Option {
	return func(m *Module) { m.UseAgentCoreExport(b) }
}

// UseAgentCoreExport is the additive post-construction injection used by the
// composition root after models/sourcescope exist. Safe to call before Start.
func (m *Module) UseAgentCoreExport(b AgentCoreExportBinding) {
	targets := map[model.TenantID]agentCoreExportTarget{}
	for _, tb := range b.Tenants {
		tid, ok := tenantOf(tb.TenantRef)
		if !ok || tb.Exporter == nil || strings.TrimSpace(tb.PolicyEngineID) == "" {
			continue
		}
		targets[tid] = agentCoreExportTarget{
			exporter:        tb.Exporter,
			mapping:         tb.Mapping,
			policyEngineID:  strings.TrimSpace(tb.PolicyEngineID),
			enforcementMode: strings.TrimSpace(tb.EnforcementMode),
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agentCoreExports = targets
	m.agentCoreProviders = append([]AgentCoreExportProvider(nil), b.Providers...)
}

type agentCoreExportPlanBody struct {
	EnforcementMode string `json:"enforcement_mode,omitempty"`
}

type agentCoreExportApplyBody struct {
	PlanHash        string `json:"plan_hash"`
	EnforcementMode string `json:"enforcement_mode,omitempty"`
}

type agentCoreExportPendingDTO struct {
	Status      string `json:"status"`
	ApprovalRef string `json:"approval_ref"`
	PlanHash    string `json:"plan_hash"`
}

type agentCoreExportApplyDTO struct {
	PlanHash string                     `json:"plan_hash"`
	Results  []agentCoreExportResultDTO `json:"results"`
}

type agentCoreExportResultDTO struct {
	Name          string   `json:"name"`
	Op            string   `json:"op"`
	Status        string   `json:"status"`
	StatusReasons []string `json:"status_reasons,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// AgentCoreExportPendingError lets cmd's exporter wrapper attach the approval
// reference the connector gate saw to the connector's deny error, without moving
// Approval knowledge into the Apache connector package.
type AgentCoreExportPendingError struct {
	Err         *agentcore.ExportDenyError
	ApprovalRef string
}

func (e *AgentCoreExportPendingError) Error() string {
	if e == nil || e.Err == nil {
		return "agentcore-export: pending approval"
	}
	return e.Err.Error()
}

func (e *AgentCoreExportPendingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (m *Module) handleAgentCoreExportPlan(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in agentCoreExportPlanBody
	if !decodeJSON(w, r, &in) {
		return
	}
	plan, code, msg, err := m.agentCoreExportPlan(r.Context(), mc, in.EnforcementMode)
	if err != nil {
		if code == 0 {
			code = http.StatusBadGateway
		}
		writeJSON(w, code, errorBody(msg))
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (m *Module) handleAgentCoreExportApply(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in agentCoreExportApplyBody
	if !decodeJSON(w, r, &in) {
		return
	}
	in.PlanHash = strings.TrimSpace(in.PlanHash)
	if in.PlanHash == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("plan_hash is required"))
		return
	}
	target, ok := m.agentCoreExportTarget(mc.Tenant)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorBody("AgentCore export is not configured for this tenant"))
		return
	}
	plan, code, msg, err := m.agentCoreExportPlan(r.Context(), mc, in.EnforcementMode)
	if err != nil {
		if code == 0 {
			code = http.StatusBadGateway
		}
		writeJSON(w, code, errorBody(msg))
		return
	}
	if plan.PlanHash != in.PlanHash {
		writeJSON(w, http.StatusConflict, errorBody("plan changed; re-plan"))
		return
	}
	results, err := target.exporter.Apply(r.Context(), plan, agentcore.ExportSpec{
		Tenant:      mc.Tenant.String(),
		RequestedBy: mc.Principal.Actor(),
	})
	if err != nil {
		var pending *AgentCoreExportPendingError
		if errors.As(err, &pending) && pending.Err != nil {
			writeJSON(w, http.StatusAccepted, agentCoreExportPendingDTO{Status: "pending", ApprovalRef: pending.ApprovalRef, PlanHash: pending.Err.PlanHash})
			return
		}
		var deny *agentcore.ExportDenyError
		if errors.As(err, &deny) {
			writeJSON(w, http.StatusForbidden, errorBody(deny.Reason))
			return
		}
		var applyErr *agentcore.ExportApplyError
		if errors.As(err, &applyErr) {
			writeJSON(w, http.StatusOK, agentCoreExportApplyDTO{PlanHash: plan.PlanHash, Results: agentCoreResultDTOs(results)})
			return
		}
		writeJSON(w, http.StatusBadGateway, errorBody("AgentCore export apply failed"))
		return
	}
	writeJSON(w, http.StatusOK, agentCoreExportApplyDTO{PlanHash: plan.PlanHash, Results: agentCoreResultDTOs(results)})
}

func (m *Module) agentCoreExportPlan(ctx context.Context, mc api.ModuleContext, modeOverride string) (agentcore.ExportPlan, int, string, error) {
	target, ok := m.agentCoreExportTarget(mc.Tenant)
	if !ok {
		return agentcore.ExportPlan{}, http.StatusNotImplemented, "AgentCore export is not configured for this tenant", errors.New("agentcore export not configured")
	}
	items, err := m.agentCoreExportItems(ctx, mc)
	if err != nil {
		return agentcore.ExportPlan{}, http.StatusBadGateway, "AgentCore export provider failed", err
	}
	mode := target.enforcementMode
	if strings.TrimSpace(modeOverride) != "" {
		mode = modeOverride
	}
	desired, unsupported := agentcore.RenderExport(items, target.mapping, agentcore.RenderOptions{EnforcementMode: mode})
	plan, err := target.exporter.Plan(ctx, target.policyEngineID, mc.Tenant.String(), desired)
	if err != nil {
		return agentcore.ExportPlan{}, http.StatusBadGateway, "AgentCore export plan failed", err
	}
	plan.Unsupported = append([]agentcore.UnsupportedItem(nil), unsupported...)
	return plan, 0, "", nil
}

func (m *Module) agentCoreExportItems(ctx context.Context, mc api.ModuleContext) ([]agentcore.ExportItem, error) {
	var items []agentcore.ExportItem
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		grants, err := agentCoreItemsFromGrants(ctx, mc.Tenant, sc)
		if err != nil {
			return err
		}
		items = append(items, grants...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, p := range m.agentCoreExportProviders() {
		provided, err := p.AgentCoreExportItems(ctx, mc.Tenant)
		if err != nil {
			return nil, err
		}
		items = append(items, provided...)
	}
	return items, nil
}

func agentCoreItemsFromGrants(ctx context.Context, tenant model.TenantID, sc store.Scope) ([]agentcore.ExportItem, error) {
	grants, err := loadScopedGrants(ctx, sc)
	if err != nil {
		return nil, err
	}
	roles, err := loadCustomRoles(ctx, sc)
	if err != nil {
		return nil, err
	}
	groups, err := loadPermGroups(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := make([]agentcore.ExportItem, 0, len(grants))
	for _, g := range grants {
		perms := sortedPerms(effectivePermsOf(g.Role, g.RoleCustom, g.Scope.Class, roles, groups))
		item := agentcore.ExportItem{
			Kind:        "grant",
			Tenant:      tenant.String(),
			SubjectKind: g.SubjectKind,
			SubjectRef:  g.SubjectRef,
			ScopeKind:   g.Scope.Tree,
			Effect:      "permit",
			Perms:       perms,
		}
		switch g.Scope.Tree {
		case scopeWorkspace:
			item.Workspace = g.Scope.Ref
		case scopeAgentGroup:
			item.Workspace = g.Scope.Ref
		case scopeTenant:
			item.Workspace = ""
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return agentCoreItemSortKey(out[i]) < agentCoreItemSortKey(out[j]) })
	return out, nil
}

func (m *Module) agentCoreExportTarget(tenant model.TenantID) (agentCoreExportTarget, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.agentCoreExports[tenant]
	return t, ok
}

func (m *Module) agentCoreExportProviders() []AgentCoreExportProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AgentCoreExportProvider(nil), m.agentCoreProviders...)
}

func agentCoreResultDTOs(results []agentcore.ExportResult) []agentCoreExportResultDTO {
	out := make([]agentCoreExportResultDTO, 0, len(results))
	for _, r := range results {
		dto := agentCoreExportResultDTO{
			Name:          r.Name,
			Op:            r.Op,
			Status:        r.Status,
			StatusReasons: append([]string(nil), r.StatusReasons...),
		}
		if r.Err != nil {
			dto.Error = r.Err.Error()
		}
		out = append(out, dto)
	}
	return out
}

func agentCoreItemSortKey(item agentcore.ExportItem) string {
	return strings.Join([]string{
		item.Kind,
		item.Tenant,
		item.SubjectKind,
		item.SubjectRef,
		item.ScopeKind,
		item.Workspace,
		item.Effect,
		strings.Join(item.Perms, "\x00"),
		strings.Join(item.Models, "\x00"),
		strings.Join(item.Sources, "\x00"),
		strings.Join(item.Surfaces, "\x00"),
		item.Access,
	}, "\x01")
}
