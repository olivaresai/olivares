// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

// Approval by what a change touches is a Business policy: Community answers
// 501 for every spec that uses it and leaves publication approval as it is.
func TestCommunityRejectsChangeClassPolicies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for name, spec := range map[string]map[string]any{
		"match by class":        {"required_approvals": 2, "match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"}},
		"tenant sensitive set":  {"sensitive_paths": []string{"infra/**"}},
		"other than the launch": {"require_other_than_launcher": true, "match": map[string]any{"action": "gitpublish.push"}},
		"bound to a record":     {"required_approvals": 1, "match": map[string]any{"action": "gitpublish.push"}, "change_record": map[string]any{"system": "servicenow", "key": "CHG0030001"}},
	} {
		r := h.do("POST", govPath+"/policies", admin, map[string]any{"name": "p", "kind": "approval", "enabled": true, "spec": spec}, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Errorf("%s = %d %s, want 501", name, r.code, r.raw)
		}
	}
	// A plain review of the action stays available.
	r := h.do("POST", govPath+"/policies", admin, map[string]any{"name": "q", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": "gitpublish.push"}}}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Errorf("a plain review policy = %d %s, want 201", r.code, r.raw)
	}
}

// A policy stored with a class and a lowered tier cannot lower the action's
// tier in Community: the edition floor applies to it as to any stored policy.
func TestCommunityStoredChangeClassPolicyCannotLowerTheTier(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	storePolicy(t, h, tenant, "stored lowering by class", map[string]any{
		"risk_tier": "low", "required_approvals": 1, "match": map[string]any{"action": "deploy.apply", "change_class": "docs_only"}})
	principal := cancellationSession(t, h, tenant, admin, "floor")
	got, err := h.gov.EngineApprovals().Request(t.Context(), tenant, principal, governance.ApprovalRequest{
		Action: "deploy.apply", SubjectKind: "deployment", SubjectRef: "prod", SessionRef: principal.SessionIdentity,
		ChangeClass: "docs_only", ChangePaths: []string{"README.md"},
	})
	if err != nil || got.RiskTier != "critical" || got.RequiredApprovals != 2 {
		t.Fatalf("request = %+v %v, want the default critical tier with two approvers", got, err)
	}
}

// An existing policy turned into one that depends on the class is refused on
// update as it is on create.
func TestCommunityRejectsUpdatingAPolicyIntoAChangeClassPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	created := h.do("POST", govPath+"/policies", admin, map[string]any{"name": "q", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": "gitpublish.push"}}}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id, _ := created.body["id"].(string)
	updated := h.do("PUT", govPath+"/policies/"+id, admin, map[string]any{"name": "q", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 2, "match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"}}}, tenantHdr(tenant))
	if updated.code != http.StatusNotImplemented {
		t.Fatalf("update into a class policy = %d %s, want 501", updated.code, updated.raw)
	}
}
