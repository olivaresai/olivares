// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package governance_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCommunityRejectsTierLoweringPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, tc := range []struct{ action, tier string }{
		{"deploy.apply", "high"}, {"security.killswitch.reenable", "high"}, {"", "high"},
		{"claude.tool.use", "low"}, {"claude.tool.use", "medium"},
	} {
		r := h.do("POST", govPath+"/policies", admin, map[string]any{
			"name": "lower-" + tc.action + "-" + tc.tier, "kind": "approval", "enabled": true,
			"spec": map[string]any{"risk_tier": tc.tier, "match": map[string]any{"action": tc.action}},
		}, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Errorf("lowering policy for %q to %s = %d %s, want 501", tc.action, tc.tier, r.code, r.raw)
		}
	}
}

func TestCommunityStoredLoweringPolicyKeepsDefaultAndReview(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	var stored model.Policy
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		var err error
		stored, err = sc.Policies().Create(t.Context(), model.Policy{
			Name: "legacy downgrade", Kind: "approval", Enabled: true,
			Spec: map[string]any{"risk_tier": "high", "required_approvals": 1, "match": map[string]any{"action": "deploy.apply"}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.createApproval(admin, tenant, map[string]any{"action": "deploy.apply", "subject_kind": "deployment", "subject_ref": "prod"})
	if r.code != http.StatusCreated || r.body["risk_tier"] != "critical" || r.body["required_approvals"] != float64(2) {
		t.Fatalf("stored lowering policy = %d %s, want critical with two approvers", r.code, r.raw)
	}
	ref, err := h.gov.EngineApprovals().ReviewPolicy(t.Context(), tenant, "deploy.apply", "deployment")
	if err != nil || ref != stored.ID.String() {
		t.Fatalf("stored review = %q %v, want %s", ref, err, stored.ID)
	}
	read := h.do("GET", govPath+"/policies/"+stored.ID.String(), admin, nil, tenantHdr(tenant))
	if read.code != http.StatusOK || read.body["spec"].(map[string]any)["risk_tier"] != "high" || read.body["spec"].(map[string]any)["required_approvals"] != float64(1) {
		t.Fatalf("stored policy export = %d %s", read.code, read.raw)
	}
}

func TestCommunityStoredApprovedLoweringPolicyCannotBeSpent(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, first := h.roleUser(admin, tenant, "legacy-first@x.io", "admin")
	_, second := h.roleUser(admin, tenant, "legacy-second@x.io", "admin")
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(t.Context(), model.Policy{Name: "legacy downgrade", Kind: "approval", Enabled: true,
			Spec: map[string]any{"risk_tier": "high", "required_approvals": 1, "match": map[string]any{"action": "deploy.apply"}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.createApproval(admin, tenant, map[string]any{"action": "deploy.apply", "subject_kind": "deployment", "subject_ref": "prod"})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if vote := h.decide(first, tenant, id, "approve"); vote.code != http.StatusOK {
		t.Fatalf("first human vote = %d %s", vote.code, vote.raw)
	}
	// A one-person approval issued by Business before the edition swap must
	// not authorize a new critical action in Community either.
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.approval"))
		if err != nil {
			return err
		}
		rec, err := repo.Get(t.Context(), model.ID(r.body["id"].(string)))
		if err != nil {
			return err
		}
		rec["status"], rec["required_approvals"], rec["approve_count"] = "approved", int64(1), int64(1)
		_, err = repo.Update(t.Context(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	spent, err := h.gov.EngineApprovals().Consume(t.Context(), tenant, r.body["id"].(string), "new-deploy", "")
	if err != nil || spent.Granted {
		t.Errorf("one-person legacy critical approval spent: %+v %v", spent, err)
	}
	read := h.do("GET", govPath+"/approvals/"+id, admin, nil, tenantHdr(tenant))
	if read.code != http.StatusOK || read.body["status"] != "expired" || read.body["required_approvals"] != float64(2) {
		t.Errorf("legacy critical grant read = %d %s, want unavailable at two humans", read.code, read.raw)
	}

	// The REST and in-process lists must classify the same grant as the detail
	// read, including when a one-item page is requested.
	for _, status := range []string{"approved", "expired"} {
		list := h.do("GET", govPath+"/approvals?status="+status+"&limit=1", admin, nil, tenantHdr(tenant))
		if list.code != http.StatusOK {
			t.Fatalf("%s list = %d %s", status, list.code, list.raw)
		}
		found := false
		for _, item := range list.body["items"].([]any) {
			if item.(map[string]any)["id"] == id {
				found = true
			}
		}
		if found != (status == "expired") {
			t.Errorf("%s list classified legacy grant incorrectly: %s", status, list.raw)
		}
		items, _, err := h.gov.EngineApprovals().List(t.Context(), tenant, "deploy.apply", status, "")
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, item := range items {
			if item.ID == id {
				found = true
			}
		}
		if found != (status == "expired") {
			t.Errorf("engine %s list classified legacy grant incorrectly: %+v", status, items)
		}
	}
	// The historical terminal decision remains intact. A new request can collect
	// the Community quorum through the existing workflow.
	fresh := h.createApproval(admin, tenant, map[string]any{"action": "deploy.apply", "subject_kind": "deployment", "subject_ref": "prod-retry"})
	if fresh.code != http.StatusCreated || fresh.body["required_approvals"] != float64(2) {
		t.Fatalf("fresh critical request = %d %s", fresh.code, fresh.raw)
	}
	freshID := fresh.body["id"].(string)
	if vote := h.decide(first, tenant, freshID, "approve"); vote.code != http.StatusOK || vote.body["status"] != "pending" {
		t.Fatalf("first human on fresh request = %d %s, want pending", vote.code, vote.raw)
	}
	if vote := h.decide(first, tenant, freshID, "approve"); vote.code != http.StatusConflict {
		t.Fatalf("duplicate human on fresh request = %d %s, want 409", vote.code, vote.raw)
	}
	if vote := h.decide(second, tenant, freshID, "approve"); vote.code != http.StatusOK || vote.body["status"] != "approved" {
		t.Fatalf("second distinct human = %d %s, want approved", vote.code, vote.raw)
	}
	out, err := h.gov.EngineApprovals().Consume(t.Context(), tenant, freshID, "fresh-deploy", "")
	if err != nil || !out.Granted {
		t.Fatalf("two-person fresh approval was not spendable: %+v %v", out, err)
	}
}

func TestCommunityStoredLoweringKillSwitchKeepsTwoPeople(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, first := h.roleUser(admin, tenant, "first@x.io", "admin")
	_, second := h.roleUser(admin, tenant, "second@x.io", "admin")
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(t.Context(), model.Policy{Name: "legacy reenable", Kind: "approval", Enabled: true,
			Spec: map[string]any{"risk_tier": "high", "required_approvals": 1, "match": map[string]any{"action": "security.killswitch.reenable"}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stop := engage(h, admin, tenant, "estate", "", "incident")
	id := stop.body["id"].(string)
	opened := reenable(h, admin, tenant, id)
	if opened.code != http.StatusAccepted {
		t.Fatalf("open reenable = %d %s", opened.code, opened.raw)
	}
	approval := opened.body["approval"].(map[string]any)
	if approval["risk_tier"] != "critical" || approval["required_approvals"] != float64(2) {
		t.Fatalf("legacy reenable floor = %v", approval)
	}
	ref := approval["id"].(string)
	if r := decide(h, first, tenant, ref, "approve"); r.code != http.StatusOK {
		t.Fatalf("first decision = %d %s", r.code, r.raw)
	}
	if r := reenable(h, admin, tenant, id); r.code != http.StatusAccepted {
		t.Fatalf("one-person reenable = %d %s", r.code, r.raw)
	}
	if r := decide(h, first, tenant, ref, "approve"); r.code != http.StatusConflict {
		t.Fatalf("duplicate person = %d %s", r.code, r.raw)
	}
	if r := decide(h, second, tenant, ref, "approve"); r.code != http.StatusOK {
		t.Fatalf("second decision = %d %s", r.code, r.raw)
	}
	if r := reenable(h, admin, tenant, id); r.code != http.StatusOK {
		t.Fatalf("two-person reenable = %d %s", r.code, r.raw)
	}
}

func TestCommunityBreakGlassRoutesUnavailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, route := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/breakglass", nil}, {"POST", "/breakglass", map[string]any{"reason": "incident"}}, {"POST", "/breakglass/consume", map[string]any{"action": "deploy.apply"}},
		{"GET", "/breakglass/legacy", nil}, {"GET", "/breakglass/legacy/uses", nil},
		{"POST", "/breakglass/legacy/revoke", map[string]any{}}, {"POST", "/breakglass/legacy/review", map[string]any{"note": "reviewed incident"}},
	} {
		r := h.do(route.method, govPath+route.path, admin, route.body, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d %s, want 501", route.method, route.path, r.code, r.raw)
		}
	}
}

func TestCommunityStoredBreakGlassGrantIsInert(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	var grant model.Record
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.breakglass"))
		if err != nil {
			return err
		}
		grant, err = repo.Create(t.Context(), model.Record{"status": "active", "reason": "legacy", "activated_by": "legacy", "activated_by_user": "legacy", "activated_at": h.clk.Now().String(), "expires_at": "2099-01-01T00:00:00Z", "use_count": int64(0), "reviewed": false})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := h.gov.EngineApprovals().ConsumeEmergency(t.Context(), tenant, "deploy.apply", "deployment", "prod")
	if err != nil || out.Granted {
		t.Errorf("legacy emergency grant applied: %+v %v", out, err)
	}
	// Read through the export scope: Community still exports the Business record.
	if err := h.st.Export(t.Context(), tenant, func(sc store.ExportScope) error {
		repo, err := sc.Ext(model.Kind("governance.breakglass"))
		if err != nil {
			return err
		}
		stored, err := repo.Get(t.Context(), model.ID(grant.String(model.ColID)))
		if err != nil {
			return err
		}
		if stored.String("status") != "active" || stored.Int("use_count") != 0 || stored.String("expires_at") != "2099-01-01T00:00:00Z" {
			t.Errorf("Community changed the stored grant: status=%s use_count=%d expires_at=%s", stored.String("status"), stored.Int("use_count"), stored.String("expires_at"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommunityRestrictivePoliciesRemainAvailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	reviewers := make([]string, 3)
	for i := range reviewers {
		_, reviewers[i] = h.roleUser(admin, tenant, fmt.Sprintf("restrictive-reviewer-%d@x.io", i), "admin")
	}
	for _, spec := range []map[string]any{
		{"match": map[string]any{"action": "claude.tool.use"}},
		{"match": map[string]any{"action": "sessions.provider.approval"}, "required_approvals": 3},
		{"match": map[string]any{"action": "mcp.tool.call"}, "risk_tier": "critical", "required_approvals": 2},
	} {
		action := spec["match"].(map[string]any)["action"].(string)
		id := h.createApprovalPolicy(admin, tenant, action, spec)
		ref, err := h.gov.EngineApprovals().ReviewPolicy(t.Context(), tenant, action, "tool")
		if err != nil || ref != id {
			t.Fatalf("review for %s = %q %v, want %s", action, ref, err, id)
		}
		required := 1
		if n, ok := spec["required_approvals"].(int); ok {
			required = n
		}
		request := h.createApproval(admin, tenant, map[string]any{"action": action, "subject_kind": "tool", "subject_ref": "restrictive-policy-journey"})
		if request.code != http.StatusCreated || request.body["required_approvals"] != float64(required) {
			t.Fatalf("restrictive policy for %s = %d %s, want quorum %d", action, request.code, request.raw, required)
		}
		for i := range required {
			want := "pending"
			if i == required-1 {
				want = "approved"
			}
			decision := h.decide(reviewers[i], tenant, request.body["id"].(string), "approve")
			if decision.code != http.StatusOK || decision.body["status"] != want {
				t.Fatalf("restrictive policy for %s decision %d = %d %s, want %s", action, i+1, decision.code, decision.raw, want)
			}
		}
	}
}

func TestCommunityRejectsTierLoweringPolicyUpdate(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	id := h.createApprovalPolicy(admin, tenant, "review", map[string]any{"match": map[string]any{"action": "deploy.apply"}})
	r := h.do("PUT", govPath+"/policies/"+id, admin, map[string]any{
		"name": "review", "kind": "approval", "enabled": true,
		"spec": map[string]any{"risk_tier": "high", "match": map[string]any{"action": "deploy.apply"}},
	}, tenantHdr(tenant))
	if r.code != http.StatusNotImplemented {
		t.Fatalf("lowering update = %d %s, want 501", r.code, r.raw)
	}
	read := h.do("GET", govPath+"/policies/"+id, admin, nil, tenantHdr(tenant))
	if read.code != http.StatusOK || read.body["spec"].(map[string]any)["risk_tier"] == "high" {
		t.Fatalf("rejected update changed stored policy: %d %s", read.code, read.raw)
	}
}

// A tenant back on Community can still switch a stored lowering policy off, or
// delete it, without Business: a disabled policy is never applied.
func TestCommunityDisablesStoredLoweringPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	spec := map[string]any{"risk_tier": "high", "required_approvals": 1, "match": map[string]any{"action": "deploy.apply"}}
	var stored model.Policy
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		var err error
		stored, err = sc.Policies().Create(t.Context(), model.Policy{Name: "legacy downgrade", Kind: "approval", Enabled: true, Spec: spec})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := govPath + "/policies/" + stored.ID.String()
	r := h.do("PUT", path, admin, map[string]any{"name": "legacy downgrade", "kind": "approval", "enabled": false, "spec": spec}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["enabled"] != false {
		t.Fatalf("disable stored lowering policy = %d %s, want 200", r.code, r.raw)
	}
	if r := h.do("DELETE", path, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete stored lowering policy = %d %s, want 204", r.code, r.raw)
	}
}

// An explicit tier equal to the default lowers nothing: Community accepts it.
func TestCommunityAcceptsDefaultEqualTierPolicies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, tc := range []struct{ action, tier string }{
		{"deploy.apply", "critical"}, {"claude.tool.use", "high"}, {"", "critical"},
	} {
		r := h.do("POST", govPath+"/policies", admin, map[string]any{
			"name": "equal-" + tc.action + "-" + tc.tier, "kind": "approval", "enabled": true,
			"spec": map[string]any{"risk_tier": tc.tier, "match": map[string]any{"action": tc.action}},
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Errorf("equal-tier policy for %q at %s = %d %s, want 201", tc.action, tc.tier, r.code, r.raw)
		}
	}
}
