// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

type fakeAgentCoreProvider struct {
	items []agentcore.ExportItem
	err   error
}

func (p fakeAgentCoreProvider) AgentCoreExportItems(context.Context, model.TenantID) ([]agentcore.ExportItem, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]agentcore.ExportItem(nil), p.items...), nil
}

type fakeAgentCoreExporter struct {
	plan         agentcore.ExportPlan
	planErr      error
	applyResults []agentcore.ExportResult
	applyErr     error
	planCalls    int
	applyCalls   int
}

func (e *fakeAgentCoreExporter) Plan(_ context.Context, engineID, tenant string, desired []agentcore.RenderedPolicy) (agentcore.ExportPlan, error) {
	e.planCalls++
	if e.planErr != nil {
		return agentcore.ExportPlan{}, e.planErr
	}
	if e.plan.PlanHash != "" {
		return e.plan, nil
	}
	plan := agentcore.ExportPlan{EngineID: engineID, Tenant: tenant, PlanHash: "plan-1"}
	for _, p := range desired {
		plan.Creates = append(plan.Creates, agentcore.PlannedChange{
			Op:              "create",
			Name:            p.Name,
			Statement:       p.Statement,
			Description:     p.Description,
			EnforcementMode: p.EnforcementMode,
		})
	}
	return plan, nil
}

func (e *fakeAgentCoreExporter) Apply(context.Context, agentcore.ExportPlan, agentcore.ExportSpec) ([]agentcore.ExportResult, error) {
	e.applyCalls++
	return e.applyResults, e.applyErr
}

func wireAgentCoreExport(h *harness, tenant model.TenantID, exporter governance.AgentCoreExporter, providers ...governance.AgentCoreExportProvider) {
	h.gov.UseAgentCoreExport(governance.AgentCoreExportBinding{
		Tenants: []governance.AgentCoreExportTenantBinding{{
			TenantRef: tenant.String(),
			Exporter:  exporter,
			Mapping: agentcore.ExportMapping{
				WorkspaceGateways: map[string][]string{"payments": {"arn:aws:bedrock-agentcore:us-east-1:123:gateway/payments"}},
				SubjectClaims: map[string]agentcore.ClaimBinding{
					"role:viewer": {Tag: "role", Value: "viewer"},
				},
				PermActions: map[string][]string{"agent:read": {"Target___read"}},
			},
			PolicyEngineID:  "pe-123",
			EnforcementMode: "LOG_ONLY",
		}},
		Providers: providers,
	})
}

func TestAgentCoreExportPlanIncludesUnsupported(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	exp := &fakeAgentCoreExporter{}
	wireAgentCoreExport(h, tenant, exp, fakeAgentCoreProvider{items: []agentcore.ExportItem{
		{Kind: "grant", Tenant: tenant.String(), SubjectKind: "role", SubjectRef: "viewer", ScopeKind: "workspace", Workspace: "payments", Effect: "permit", Perms: []string{"agent:read"}},
		{Kind: "grant", Tenant: tenant.String(), SubjectKind: "role", SubjectRef: "viewer", ScopeKind: "workspace", Workspace: "unmapped", Effect: "permit", Perms: []string{"agent:read"}},
	}})

	r := h.do("POST", "/v1/m/governance/agentcore-export/plan", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("plan = %d %s", r.code, r.raw)
	}
	creates := r.body["Creates"].([]any)
	if len(creates) != 1 {
		t.Fatalf("creates = %d, want 1: %s", len(creates), r.raw)
	}
	if creates[0].(map[string]any)["Statement"] == "" {
		t.Fatalf("desired statement must be included: %s", r.raw)
	}
	unsupported := r.body["Unsupported"].([]any)
	if len(unsupported) != 1 || unsupported[0].(map[string]any)["Reason"] != "no_gateway_mapping" {
		t.Fatalf("unsupported passthrough mismatch: %s", r.raw)
	}
	if exp.planCalls != 1 {
		t.Fatalf("plan calls = %d, want 1", exp.planCalls)
	}
}

func TestAgentCoreExportProviderErrorStopsPlan(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	exp := &fakeAgentCoreExporter{}
	wireAgentCoreExport(h, tenant, exp, fakeAgentCoreProvider{err: errors.New("provider down")})

	r := h.do("POST", "/v1/m/governance/agentcore-export/plan", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("provider error = %d, want 502: %s", r.code, r.raw)
	}
	if exp.planCalls != 0 {
		t.Fatalf("provider error must not build partial plan, got %d plan call(s)", exp.planCalls)
	}
}

func TestAgentCoreExportApplyHashMismatch(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	exp := &fakeAgentCoreExporter{plan: agentcore.ExportPlan{EngineID: "pe-123", Tenant: tenant.String(), PlanHash: "fresh"}}
	wireAgentCoreExport(h, tenant, exp)

	r := h.do("POST", "/v1/m/governance/agentcore-export/apply", admin, map[string]any{"plan_hash": "stale"}, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("hash mismatch = %d, want 409: %s", r.code, r.raw)
	}
	if exp.applyCalls != 0 {
		t.Fatalf("hash mismatch must not apply, got %d apply call(s)", exp.applyCalls)
	}
}

func TestAgentCoreExportApplyPending(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	exp := &fakeAgentCoreExporter{
		plan: agentcore.ExportPlan{EngineID: "pe-123", Tenant: tenant.String(), PlanHash: "p1"},
		applyErr: &governance.AgentCoreExportPendingError{
			Err:         &agentcore.ExportDenyError{Reason: "export not approved by governance (pending)", PlanHash: "p1"},
			ApprovalRef: "approval-1",
		},
	}
	wireAgentCoreExport(h, tenant, exp)

	r := h.do("POST", "/v1/m/governance/agentcore-export/apply", admin, map[string]any{"plan_hash": "p1"}, tenantHdr(tenant))
	if r.code != http.StatusAccepted || r.body["status"] != "pending" || r.body["approval_ref"] != "approval-1" {
		t.Fatalf("pending apply mismatch: %d %s", r.code, r.raw)
	}
}

func TestAgentCoreExportApplyApproved(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	exp := &fakeAgentCoreExporter{
		plan: agentcore.ExportPlan{EngineID: "pe-123", Tenant: tenant.String(), PlanHash: "p1"},
		applyResults: []agentcore.ExportResult{{
			Name: "olv_acme_g_123", Op: "create", Status: "CREATING", StatusReasons: []string{"queued"},
		}},
	}
	wireAgentCoreExport(h, tenant, exp)

	r := h.do("POST", "/v1/m/governance/agentcore-export/apply", admin, map[string]any{"plan_hash": "p1"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("approved apply = %d %s", r.code, r.raw)
	}
	results := r.body["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["status"] != "CREATING" {
		t.Fatalf("apply results mismatch: %s", r.raw)
	}
}
