// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/modules/governance"
)

// THE HALF OF THE CONTRACT THIS FILE OWNS, AND WHY IT IS NOT THE WHOLE CONTRACT.
//
// (#690) landed a console that posted a body the engine rejected with 400,
// and its test was GREEN because the test double accepted what production
// refuses. The defense against that class is a payload that is not written twice:
// the four testdata/agentcore_console_*.json files are the EXACT bytes the console
// posts, and they are read — never re-typed — by both halves:
//
//   - this file POSTs them at the real route, through the real decodeJSON, and
//     asserts the engine accepts them and honors the plan_hash inside;
//   - web/src/features/governance/agentcore-export.test.tsx reads the SAME files
//     and asserts the body the console actually hands the HTTP client matches.
//
// Neither half alone is a contract test. A fixture checked only here moves when
// the ENGINE moves and says nothing about console drift (the console could stop
// sending plan_hash tomorrow and this file would still pass); a fixture checked
// only in vitest pins the console against bytes no engine ever parsed. Together
// they fail from either side.
//
// decodeJSON calls DisallowUnknownFields (helpers.go:88-93), so a console that
// starts posting one extra field turns these into 400s rather than into a silently
// ignored key — which is precisely how #690 stayed invisible.

func consolePayload(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read console payload fixture: %v", err)
	}
	if !json.Valid(b) {
		t.Fatalf("console payload fixture %s is not valid JSON: %s", name, b)
	}
	return json.RawMessage(b)
}

// The plan hash the apply fixtures carry. The console sends the hash of the plan
// ON SCREEN, so the fake exporter must plan to exactly this value: if it planned
// to anything else the engine would answer 409, which is the correct behavior
// and the subject of TestAgentCoreExportApplyHashMismatch, not of this file.
const consoleFixturePlanHash = "plan-hash-the-operator-reviewed"

func TestAgentCoreExportAcceptsConsolePlanPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
	}{
		{"tenant default mode (no override field)", "agentcore_console_plan_default.json"},
		{"explicit enforcement mode", "agentcore_console_plan_explicit_mode.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			exp := &fakeAgentCoreExporter{}
			wireAgentCoreExport(h, tenant, exp)

			r := h.do("POST", "/v1/m/governance/agentcore-export/plan", admin,
				consolePayload(t, tc.fixture), tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("engine rejected the console's plan payload: %d %s", r.code, r.raw)
			}
			// The console reads PascalCase off this response (ExportPlan carries no
			// json tags). If the engine ever grows tags, this assert is what tells
			// the console its DTO just became wrong.
			if _, ok := r.body["PlanHash"]; !ok {
				t.Fatalf("plan response must carry PlanHash (PascalCase, untagged struct): %s", r.raw)
			}
		})
	}
}

func TestAgentCoreExportAcceptsConsoleApplyPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
	}{
		{"tenant default mode (no override field)", "agentcore_console_apply_default.json"},
		{"explicit enforcement mode", "agentcore_console_apply_explicit_mode.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			exp := &fakeAgentCoreExporter{
				plan: agentcore.ExportPlan{
					EngineID: "pe-123",
					Tenant:   tenant.String(),
					PlanHash: consoleFixturePlanHash,
				},
				applyResults: []agentcore.ExportResult{{
					Name: "olv_acme_g_1", Op: "create", Status: "CREATING",
				}},
			}
			wireAgentCoreExport(h, tenant, exp)

			r := h.do("POST", "/v1/m/governance/agentcore-export/apply", admin,
				consolePayload(t, tc.fixture), tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("engine rejected the console's apply payload: %d %s", r.code, r.raw)
			}
			// Not just "not rejected": the hash inside the console's bytes is the one
			// the engine bound the write to. A payload whose plan_hash the decoder
			// dropped would 400 ("plan_hash is required"), and one it misread would
			// 409 — so a 200 here means the field arrived under the name the console
			// spells it with.
			if exp.applyCalls != 1 {
				t.Fatalf("apply calls = %d, want 1 (the console payload must reach the exporter)", exp.applyCalls)
			}
			if r.body["plan_hash"] != consoleFixturePlanHash {
				t.Fatalf("apply echoed plan_hash %v, want %q: %s",
					r.body["plan_hash"], consoleFixturePlanHash, r.raw)
			}
		})
	}
}

// The control that proves the two asserts above can fail: the same route, the
// same fixture shape, ONE extra field. DisallowUnknownFields turns it into a 400,
// so "the engine accepts the console's payload" is a claim with a known negative
// rather than a route that accepts anything.
func TestAgentCoreExportRejectsUnknownFieldInConsolePayload(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	exp := &fakeAgentCoreExporter{plan: agentcore.ExportPlan{
		EngineID: "pe-123", Tenant: tenant.String(), PlanHash: consoleFixturePlanHash,
	}}
	wireAgentCoreExport(h, tenant, exp)

	r := h.do("POST", "/v1/m/governance/agentcore-export/apply", admin,
		json.RawMessage(`{"plan_hash":"`+consoleFixturePlanHash+`","enforcement_mode":"LOG_ONLY","dry_run":true}`),
		tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400: %s", r.code, r.raw)
	}
	if exp.applyCalls != 0 {
		t.Fatalf("a rejected body must not reach the exporter, got %d apply call(s)", exp.applyCalls)
	}
}

// 501 is the frontier the console paints as "this deployment has not wired the
// capability", and it detects it BY STATUS. This pins the status for both routes
// against an unwired tenant: if the engine ever answered 500 or 403 here, the
// console's panel would silently become an error or a permission wall.
func TestAgentCoreExportUnwiredTenantIsNotImplemented(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Deliberately NOT wired: no UseAgentCoreExport call, which is the shape a
	// deployment without OLIVARES_AGENTCORE_EXPORT_CONFIG produces
	// (agentcoreexportwiring.go:117-119 returns ok=false and nothing is bound).
	var _ governance.AgentCoreExporter

	plan := h.do("POST", "/v1/m/governance/agentcore-export/plan", admin,
		consolePayload(t, "agentcore_console_plan_default.json"), tenantHdr(tenant))
	if plan.code != http.StatusNotImplemented {
		t.Fatalf("unwired plan = %d, want 501: %s", plan.code, plan.raw)
	}
	apply := h.do("POST", "/v1/m/governance/agentcore-export/apply", admin,
		consolePayload(t, "agentcore_console_apply_default.json"), tenantHdr(tenant))
	if apply.code != http.StatusNotImplemented {
		t.Fatalf("unwired apply = %d, want 501: %s", apply.code, apply.raw)
	}
}
