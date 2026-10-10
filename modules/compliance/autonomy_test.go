// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestRiskDeclaredIntentEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "autonomy")
	tok := h.roleToken(admin, tenant, "editor@autonomy.test", "editor")
	agent := h.seedAgent(tenant, "agent")
	r := h.do("POST", "/v1/m/compliance/risk/classify", tok, map[string]any{"subject_ref": agent.String()}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("classify=%d %s", r.code, r.raw)
	}
	signals := r.body["signals"].(map[string]any)
	evidence, ok := signals["declared_autonomy"].(map[string]any)
	if !ok || evidence["state"] != "unknown" {
		t.Fatalf("unwired declaration evidence is not unknown: %s", r.raw)
	}
	if r.body["suggested_tier"] != string(TierMinimal) {
		t.Fatalf("existing observed heuristic changed: %s", r.raw)
	}

	for _, tc := range []struct {
		state                 string
		scheduled, autonomous bool
		err                   error
		want                  string
		tier                  RiskTier
	}{
		{"declared", true, true, nil, "declared", TierLimited},
		{"declared", true, false, nil, "declared", TierMinimal},
		{"none_declared", false, false, nil, "none_declared", TierMinimal},
		{"partial", true, false, nil, "partial", TierMinimal},
		{"arbitrary-canary", false, false, nil, "unknown", TierMinimal},
		{"", false, false, errors.New("c52-private-error-canary"), "unavailable", TierMinimal},
	} {
		WithAutonomySource(autonomyFixture{tenant: tenant, ref: agent.String(), signal: AutonomySignal{State: tc.state, Scheduled: tc.scheduled, Autonomous: tc.autonomous, Detail: "c52-private-detail-canary"}, err: tc.err})(h.mod)
		got := h.do("POST", "/v1/m/compliance/risk/classify", tok, map[string]any{"subject_ref": agent.String()}, tenantHdr(tenant))
		if got.code != http.StatusCreated || got.body["suggested_tier"] != string(tc.tier) {
			t.Fatalf("declaration changed risk rules: %d %s", got.code, got.raw)
		}
		evidence := got.body["signals"].(map[string]any)["declared_autonomy"].(map[string]any)
		if evidence["state"] != tc.want {
			t.Fatalf("evidence state=%v want=%s", evidence, tc.want)
		}
		if strings.Contains(got.raw, "canary") {
			t.Fatalf("non-enumerated source detail leaked: %s", got.raw)
		}
		read := h.do("GET", "/v1/m/compliance/risk", tok, nil, tenantHdr(tenant))
		if read.code != http.StatusOK || !strings.Contains(read.raw, `"state":"`+tc.want+`"`) {
			t.Fatalf("retained declaration read-back=%d %s", read.code, read.raw)
		}
	}
	// An observed high finding wins even when no active intent is declared.
	h.seedFinding(tenant, agent, "guardrail", model.SeverityHigh)
	WithAutonomySource(autonomyFixture{tenant: tenant, ref: agent.String(), signal: AutonomySignal{State: "none_declared"}})(h.mod)
	conflict := h.do("POST", "/v1/m/compliance/risk/classify", tok, map[string]any{"subject_ref": agent.String()}, tenantHdr(tenant))
	if conflict.body["suggested_tier"] != string(TierHigh) {
		t.Fatalf("declaration absence lowered observed risk: %s", conflict.raw)
	}
	other := h.createOrg(admin, "autonomy-other")
	otherTok := h.roleToken(admin, other, "other@autonomy.test", "editor")
	cross := h.do("GET", "/v1/m/compliance/risk", otherTok, nil, tenantHdr(other))
	if cross.code != http.StatusOK || strings.Contains(cross.raw, agent.String()) {
		t.Fatalf("cross-tenant risk leakage: %d %s", cross.code, cross.raw)
	}
}

type autonomyFixture struct {
	tenant model.TenantID
	ref    string
	signal AutonomySignal
	err    error
}

func (f autonomyFixture) Autonomy(_ context.Context, tenant model.TenantID, ref string) (AutonomySignal, error) {
	if tenant != f.tenant || ref != f.ref {
		return AutonomySignal{}, errors.New("scope mismatch")
	}
	return f.signal, f.err
}
