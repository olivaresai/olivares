// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
	"github.com/olivaresai/olivares/sdk/event"
)

func setBindingExpiry(t *testing.T, h *harness, tenant model.TenantID, id string, at time.Time) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sourcescope.binding")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		if err != nil {
			return err
		}
		rec["expires_at"] = model.NewTimestamp(at).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatalf("set binding expiry: %v", err)
	}
}

func accessAuditCount(t *testing.T, h *harness, tenant model.TenantID, action string) int {
	t.Helper()
	count := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			if ev.Action == action {
				count++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	return count
}

func accessFindingCount(h *harness, kind string) int {
	h.host.mu.Lock()
	defer h.host.mu.Unlock()
	count := 0
	for _, e := range h.host.events {
		if finding, ok := event.FindingOf(e); ok && finding.Kind == kind {
			count++
		}
	}
	return count
}

func accessAuditTarget(t *testing.T, h *harness, tenant model.TenantID, action string) string {
	t.Helper()
	var target string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			if ev.Action == action {
				target = ev.TargetID.String()
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	return target
}

func existingToken(h *harness, email string) string {
	h.t.Helper()
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if login.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, login.code, login.raw)
	}
	return login.body["token"].(string)
}

func TestBindingUsesTenantExpiryPolicy(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "Access expiry", Kind: "grant_expiry", Enabled: true,
			Spec: map[string]any{"default_seconds": 3600, "maximum_seconds": 7200},
		})
		return err
	}); err != nil {
		t.Fatalf("seed expiry policy: %v", err)
	}
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
	})
	if got, _ := r.body["expires_at"].(string); r.code != http.StatusCreated || got == "" {
		t.Fatalf("policy default was not applied: %d %s", r.code, r.raw)
	}
	forbid := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "gemini-cli", "scope_tree": "workspace", "scope_ref": "dept", "effect": "forbid", "enabled": true,
	})
	if got, _ := forbid.body["expires_at"].(string); forbid.code != http.StatusCreated || got != "" {
		t.Fatalf("a forbid must not inherit the tenant default expiry: %d %s", forbid.code, forbid.raw)
	}
	tooLong := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "codex", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
		"expires_at": time.Now().Add(3 * time.Hour).Format(time.RFC3339),
	})
	if tooLong.code != http.StatusBadRequest {
		t.Fatalf("expiry above tenant maximum = %d %s, want 400", tooLong.code, tooLong.raw)
	}
}

func TestBindingExpiryRoundTripAndLegacyDefault(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	expires := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": "person", "enabled": true,
		"expires_at": expires.Format(time.RFC3339),
	})
	if r.code != http.StatusCreated || r.body["expires_at"] == "" {
		t.Fatalf("create expiring binding = %d %s, want stored expiry", r.code, r.raw)
	}
	legacy := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "codex", "scope_tree": "user", "scope_ref": "person", "enabled": true,
	})
	if legacy.code != http.StatusCreated || legacy.body["expires_at"] != nil {
		t.Fatalf("legacy binding without expiry = %d %s, want no expiry", legacy.code, legacy.raw)
	}
	past := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "gemini", "scope_tree": "user", "scope_ref": "person", "enabled": true,
		"expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if past.code != http.StatusBadRequest {
		t.Fatalf("past expiry = %d %s, want 400", past.code, past.raw)
	}
}

func TestExpiredAllowDeniesWithoutUnconfiningSource(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	person := h.principalFor(admin, tenant, "person@acme.io", "")
	bot := h.createAgent(tenant, "bot", model.ID(""))
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": person.UserID.String(), "enabled": true,
		"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude"); !d.Allowed {
		t.Fatalf("active grant must allow, got %+v", d)
	}
	setBindingExpiry(t, h, tenant, r.body["id"].(string), time.Now().Add(-time.Minute))
	d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude")
	if d.Allowed || !d.Bound {
		t.Fatalf("expired last allow must deny while keeping the source confined, got %+v", d)
	}
}

func TestExpiredForbidLapses(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	person := h.principalFor(admin, tenant, "person@acme.io", "")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	bot := h.createAgent(tenant, "bot", model.ID(""))
	id := h.createBindingApproved(admin, approver, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": person.UserID.String(), "effect": "forbid", "enabled": true,
		"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude"); d.Allowed {
		t.Fatalf("active forbid must deny, got %+v", d)
	}
	setBindingExpiry(t, h, tenant, id, time.Now().Add(-time.Minute))
	if d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude"); !d.Allowed {
		t.Fatalf("expired forbid must lapse, got %+v", d)
	}
}

func TestBindingExpirySweepRemindsAndAuditsOnce(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	ids := []string{}
	for _, tc := range []struct {
		ref string
		at  time.Time
	}{
		{"claude", time.Now().Add(13 * 24 * time.Hour)},
		{"codex", time.Now().Add(23 * time.Hour)},
	} {
		r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "tool", "source_ref": tc.ref, "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
			"expires_at": tc.at.Format(time.RFC3339),
		})
		if r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", tc.ref, r.code, r.raw)
		}
		ids = append(ids, r.body["id"].(string))
	}
	r := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("reminder sweep = %d %s", r.code, r.raw)
	}
	for _, id := range ids {
		setBindingExpiry(t, h, tenant, id, time.Now().Add(-time.Minute))
	}
	r = h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("expiry sweep = %d %s", r.code, r.raw)
	}
	for kind, want := range map[string]int{"sourcescope_binding_expiring_14": 1, "sourcescope_binding_expiring_1": 1, "sourcescope_binding_expired": 2} {
		if got := accessFindingCount(h, kind); got != want {
			t.Errorf("%s findings = %d, want %d", kind, got, want)
		}
	}
	if got := accessAuditCount(t, h, tenant, "sourcescope.binding.expire"); got != 2 {
		t.Errorf("expiry audit events = %d, want 2", got)
	}
}

func TestAccessRequestApprovalCreatesRequestedBinding(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	person := h.principalFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	personToken := existingToken(h, "person@acme.io")
	bot := h.createAgent(tenant, "bot", model.ID(""))
	seed := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": "owner", "enabled": true,
	})
	if seed.code != http.StatusCreated {
		t.Fatalf("seed confined tool = %d %s", seed.code, seed.raw)
	}
	if d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude"); d.Allowed {
		t.Fatalf("precondition: requester must be refused, got %+v", d)
	}

	r := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}, tenantHdr(tenant))
	if r.code != http.StatusAccepted || r.body["op"] != "access_request" {
		t.Fatalf("request access = %d %s, want pending access_request", r.code, r.raw)
	}
	requestID := r.body["id"].(string)
	approved := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", admin, nil, tenantHdr(tenant))
	if approved.code != http.StatusOK {
		t.Fatalf("owner approval = %d %s", approved.code, approved.raw)
	}
	if d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude"); !d.Allowed {
		t.Fatalf("approved request must allow the launch, got %+v", d)
	}
	if got := accessAuditCount(t, h, tenant, "sourcescope.access.request"); got != 1 {
		t.Errorf("request audit events = %d, want 1", got)
	}
	if got := accessAuditCount(t, h, tenant, "sourcescope.posture.approve"); got != 1 {
		t.Errorf("approval audit events = %d, want 1", got)
	}
	if got := accessAuditTarget(t, h, tenant, "sourcescope.posture.approve"); got != requestID {
		t.Errorf("approval audit target = %q, want request %q", got, requestID)
	}
}

// confineTool makes a tool refused to everyone but one other person, the state an
// access request answers.
func confineTool(t *testing.T, h *harness, admin string, tenant model.TenantID, ref string) {
	t.Helper()
	seed := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": ref, "scope_tree": "user", "scope_ref": "owner", "enabled": true,
	})
	if seed.code != http.StatusCreated {
		t.Fatalf("confine %s = %d %s", ref, seed.code, seed.raw)
	}
}

func TestAccessRequestDepartmentAdminCeiling(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	h.createWorkspace(tenant, "other")
	h.principalFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	personToken := existingToken(h, "person@acme.io")
	departmentAdmin := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
	departmentToken := existingToken(h, "dept-admin@acme.io")
	otherAdmin := h.principalFor(admin, tenant, "other-admin@acme.io", auth.RoleViewer)
	otherToken := existingToken(h, "other-admin@acme.io")
	confineTool(t, h, admin, tenant, "claude")
	confineTool(t, h, admin, tenant, "codex")
	h.publishGrant(admin, tenant,
		`permit(principal in User::"`+departmentAdmin.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`+
			`permit(principal in User::"`+otherAdmin.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"other" };`)

	r := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("request access = %d %s", r.code, r.raw)
	}
	requestID := r.body["id"].(string)
	second := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "codex", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if second.code != http.StatusAccepted {
		t.Fatalf("second access request = %d %s", second.code, second.raw)
	}
	if queue := h.do("GET", "/v1/m/sourcescope/posture-requests?limit=1", otherToken, nil, tenantHdr(tenant)); queue.code != http.StatusOK || len(items(queue)) != 0 || (queue.body["cursor"] != nil && queue.body["cursor"] != "") {
		t.Fatalf("other department queue leaked a hidden cursor = %d %s", queue.code, queue.raw)
	}
	if direct := h.do("GET", "/v1/m/sourcescope/posture-requests/"+requestID, otherToken, nil, tenantHdr(tenant)); direct.code != http.StatusNotFound {
		t.Fatalf("other department direct read = %d %s, want 404", direct.code, direct.raw)
	}
	if queue := h.do("GET", "/v1/m/sourcescope/posture-requests", departmentToken, nil, tenantHdr(tenant)); queue.code != http.StatusOK || len(items(queue)) != 2 {
		t.Fatalf("department queue = %d %s, want both access requests", queue.code, queue.raw)
	}
	if queue := h.do("GET", "/v1/m/sourcescope/posture-requests", personToken, nil, tenantHdr(tenant)); queue.code != http.StatusOK || len(items(queue)) != 2 {
		t.Fatalf("requester queue = %d %s, want their own access requests", queue.code, queue.raw)
	}
	if got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", otherToken, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("other department admin = %d %s, want 403", got.code, got.raw)
	}
	if got := h.do("POST", "/v1/m/sourcescope/posture-requests/"+requestID+"/approve", otherToken, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("other department admin used the generic approval route = %d %s, want 403", got.code, got.raw)
	}
	if got := h.do("POST", "/v1/m/sourcescope/posture-requests/"+requestID+"/reject", otherToken, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("other department admin rejected the access request = %d %s, want 403", got.code, got.raw)
	}
	if got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", departmentToken, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("department admin must not approve a tenant-owned source = %d %s", got.code, got.raw)
	}
}

func TestAccessRequestSourceOwnerCeiling(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	dept := h.createWorkspace(tenant, "dept")
	h.createWorkspace(tenant, "other")
	h.confineTo(admin, tenant, "person@acme.io", dept)
	personToken := existingToken(h, "person@acme.io")
	departmentAdmin := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
	departmentToken := existingToken(h, "dept-admin@acme.io")
	h.publishGrant(admin, tenant,
		`permit(principal in User::"`+departmentAdmin.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`)
	seed := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "other", "enabled": true,
	})
	if seed.code != http.StatusCreated {
		t.Fatalf("seed other department source = %d %s", seed.code, seed.raw)
	}
	setBindingExpiry(t, h, tenant, seed.body["id"].(string), time.Now().Add(-time.Hour))
	r := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("request access = %d %s", r.code, r.raw)
	}
	requestID := r.body["id"].(string)
	if got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", departmentToken, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("department admin approved another department's source = %d %s, want 403", got.code, got.raw)
	}
	if got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("owner approval = %d %s", got.code, got.raw)
	}
}

func TestAccessRequestAgentGroupOwnerCeiling(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	other := h.createWorkspace(tenant, "other")
	personToken := h.tokenFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	departmentAdmin := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
	departmentToken := existingToken(h, "dept-admin@acme.io")
	h.publishGrant(admin, tenant,
		`permit(principal in User::"`+departmentAdmin.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`)
	bot := h.createAgent(tenant, "owner-bot", other)
	h.addAgentToGroup(tenant, bot.ID, "owners", other)
	seed := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "agent_group", "scope_ref": "owners", "enabled": true,
	})
	if seed.code != http.StatusCreated {
		t.Fatalf("seed the other department's agent-group source = %d %s", seed.code, seed.raw)
	}
	r := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("request access = %d %s", r.code, r.raw)
	}
	requestID := r.body["id"].(string)
	if got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", departmentToken, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("a department the requester picked approved a source owned through another department's agent group = %d %s, want 403", got.code, got.raw)
	}
	if got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("owner approval = %d %s", got.code, got.raw)
	}
}

func TestAccessRequestSelfApprovalIsRefused(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	confineTool(t, h, admin, tenant, "claude")
	r := h.do("POST", "/v1/m/sourcescope/access-requests", admin, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("request access = %d %s", r.code, r.raw)
	}
	requestID := r.body["id"].(string)
	approved := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", admin, nil, tenantHdr(tenant))
	if approved.code != http.StatusConflict {
		t.Fatalf("self approval = %d %s, want dual-control refusal", approved.code, approved.raw)
	}
}

func TestBindingReminderRetriesAfterPublishFailure(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
		"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create expiring binding = %d %s", r.code, r.raw)
	}
	bindingID := r.body["id"].(string)
	h.host.publishErr = errors.New("finding rail unavailable")
	if got := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant)); got.code != http.StatusInternalServerError {
		t.Fatalf("failed reminder publication = %d %s, want 500", got.code, got.raw)
	}
	assertReminderMarker := func(wantEmpty bool) {
		t.Helper()
		if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("sourcescope.binding")
			if err != nil {
				return err
			}
			rec, err := repo.Get(context.Background(), model.ID(bindingID))
			if err != nil {
				return err
			}
			if got := rec.String("remind_1_at"); (got == "") != wantEmpty {
				t.Errorf("remind_1_at = %q, wantEmpty %v", got, wantEmpty)
			}
			return nil
		}); err != nil {
			t.Fatalf("read reminder marker: %v", err)
		}
	}
	assertReminderMarker(true)
	h.host.publishErr = nil
	if got := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("reminder retry = %d %s", got.code, got.raw)
	}
	assertReminderMarker(false)
}

func TestGrantReminderRetriesAfterPublishFailure(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	expires := time.Now().Add(time.Hour)
	grantID := seedScopedGrant(t, h, tenant, "group", "expiring", &expires)
	h.host.publishErr = errors.New("finding rail unavailable")
	if got := h.do("POST", "/v1/m/governance/approvals/sweep", admin, nil, tenantHdr(tenant)); got.code != http.StatusInternalServerError {
		t.Fatalf("failed reminder publication = %d %s, want 500", got.code, got.raw)
	}
	assertReminderMarker := func(wantEmpty bool) {
		t.Helper()
		if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("governance.scoped_grant")
			if err != nil {
				return err
			}
			rec, err := repo.Get(context.Background(), model.ID(grantID))
			if err != nil {
				return err
			}
			if got := rec.String("remind_1_at"); (got == "") != wantEmpty {
				t.Errorf("remind_1_at = %q, wantEmpty %v", got, wantEmpty)
			}
			return nil
		}); err != nil {
			t.Fatalf("read reminder marker: %v", err)
		}
	}
	assertReminderMarker(true)
	h.host.publishErr = nil
	if got := h.do("POST", "/v1/m/governance/approvals/sweep", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("reminder retry = %d %s", got.code, got.raw)
	}
	assertReminderMarker(false)
}

func TestReminderOutageDoesNotBlockExpiry(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	expired := time.Now().Add(-time.Hour)
	expiring := time.Now().Add(time.Hour)
	expiredGrant := seedScopedGrant(t, h, tenant, "group", "expired", &expired)
	seedScopedGrant(t, h, tenant, "group", "expiring", &expiring)
	h.host.publishErr = errors.New("finding rail unavailable")
	if got := h.do("POST", "/v1/m/governance/approvals/sweep", admin, nil, tenantHdr(tenant)); got.code != http.StatusInternalServerError {
		t.Fatalf("sweep with notify down = %d %s, want 500 for the reminder", got.code, got.raw)
	}
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.scoped_grant")
		if err != nil {
			return err
		}
		if _, err := repo.Get(context.Background(), model.ID(expiredGrant)); err == nil {
			t.Error("expired grant is still stored while notify is down")
		}
		return nil
	}); err != nil {
		t.Fatalf("read grants: %v", err)
	}
	if got := accessAuditCount(t, h, tenant, "governance.scoped_grant.expire"); got != 1 {
		t.Errorf("grant expiry audit events with notify down = %d, want 1", got)
	}

	for _, ref := range []string{"codex", "grok"} {
		at := expired
		if ref == "grok" {
			at = expiring
		}
		r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "tool", "source_ref": ref, "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
		if r.code != http.StatusCreated {
			t.Fatalf("create binding %s = %d %s", ref, r.code, r.raw)
		}
		setBindingExpiry(t, h, tenant, r.body["id"].(string), at)
	}
	if got := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant)); got.code != http.StatusInternalServerError {
		t.Fatalf("binding sweep with notify down = %d %s, want 500 for the reminder", got.code, got.raw)
	}
	if got := accessAuditCount(t, h, tenant, "sourcescope.binding.expire"); got != 1 {
		t.Errorf("binding expiry audit events with notify down = %d, want 1", got)
	}
}

func seedScopedGrant(t *testing.T, h *harness, tenant model.TenantID, kind, ref string, expiresAt *time.Time) string {
	t.Helper()
	var id string
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.scoped_grant")
		if err != nil {
			return err
		}
		fields := model.Record{
			"subject_kind": kind, "subject_ref": ref, "grant_role": "viewer", "role_custom": false,
			"scope_tree": "workspace", "scope_ref": "dept", "scope_class": "model", "created_by": "user:test",
		}
		if expiresAt != nil {
			fields["expires_at"] = model.NewTimestamp(*expiresAt).String()
		}
		rec, err := repo.Create(context.Background(), fields)
		id = rec.String(model.ColID)
		return err
	}); err != nil {
		t.Fatalf("seed scoped grant: %v", err)
	}
	return id
}

func TestScopedGrantSweepExpiresOnceAndKeepsLegacy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	expired := time.Now().Add(-time.Hour)
	expiredID := seedScopedGrant(t, h, tenant, "group", "expired", &expired)
	legacyID := seedScopedGrant(t, h, tenant, "group", "legacy", nil)
	for i := 0; i < 2; i++ {
		r := h.do("POST", "/v1/m/governance/approvals/sweep", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("sweep %d = %d %s", i, r.code, r.raw)
		}
	}
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.scoped_grant")
		if err != nil {
			return err
		}
		if _, err := repo.Get(context.Background(), model.ID(expiredID)); err == nil {
			t.Error("expired grant remains stored")
		}
		legacy, err := repo.Get(context.Background(), model.ID(legacyID))
		if err != nil {
			t.Errorf("legacy grant was changed: %v", err)
		} else if legacy.String("remind_14_at") != "" || legacy.String("remind_1_at") != "" {
			t.Errorf("legacy never-expiring grant received reminders: %v", legacy)
		}
		return nil
	}); err != nil {
		t.Fatalf("read grants: %v", err)
	}
	if got := accessAuditCount(t, h, tenant, "governance.scoped_grant.expire"); got != 1 {
		t.Errorf("scoped grant expiry audit events = %d, want 1", got)
	}
	for _, kind := range []string{"governance_scoped_grant_expiring_14", "governance_scoped_grant_expiring_1"} {
		if got := accessFindingCount(h, kind); got != 0 {
			t.Errorf("legacy grant reminder %s = %d, want 0", kind, got)
		}
	}
}

func TestAccessRequestRouteRefusesOrdinaryPostureApproval(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	viewer := h.tokenFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	first := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
	})
	if first.code != http.StatusCreated {
		t.Fatalf("seed binding = %d %s", first.code, first.raw)
	}
	pending := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": "another-person", "enabled": true,
	})
	if pending.code != http.StatusAccepted || pending.body["op"] != "create" {
		t.Fatalf("ordinary pending create = %d %s", pending.code, pending.raw)
	}
	requestID := pending.body["id"].(string)
	approved := h.do("POST", "/v1/m/sourcescope/access-requests/"+requestID+"/approve", viewer, nil, tenantHdr(tenant))
	if approved.code != http.StatusForbidden {
		t.Fatalf("viewer approved an ordinary posture change through access-requests: %d %s", approved.code, approved.raw)
	}
	got := h.do("GET", "/v1/m/sourcescope/posture-requests/"+requestID, admin, nil, tenantHdr(tenant))
	if got.code != http.StatusOK || got.body["status"] != "pending" {
		t.Fatalf("ordinary request changed after refused approval: %d %s", got.code, got.raw)
	}
}

func TestExpiringForbidRequiresDualControl(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept",
		"effect": "forbid", "enabled": true, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusAccepted || r.body["op"] != "create" {
		t.Fatalf("expiring forbid applied without dual control: %d %s", r.code, r.raw)
	}
	approved := h.do("POST", "/v1/m/sourcescope/posture-requests/"+r.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant))
	if approved.code != http.StatusOK {
		t.Fatalf("second-person approval = %d %s", approved.code, approved.raw)
	}
}

func TestShorteningForbidExpiryRequiresDualControl(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept",
		"effect": "forbid", "enabled": true,
	})
	if r.code != http.StatusCreated {
		t.Fatalf("permanent forbid = %d %s", r.code, r.raw)
	}
	updated := h.do("PUT", "/v1/m/sourcescope/bindings/"+r.body["id"].(string), admin, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept",
		"effect": "forbid", "enabled": true, "expires_at": time.Now().Add(time.Minute).Format(time.RFC3339),
	}, tenantHdr(tenant))
	if updated.code != http.StatusAccepted || updated.body["op"] != "update" {
		t.Fatalf("short-lived forbid replaced the permanent one without dual control: %d %s", updated.code, updated.raw)
	}
}

func TestRenewedBindingExpiresWithFreshEvidence(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept",
		"enabled": true, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create expiring binding = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	setBindingExpiry(t, h, tenant, id, time.Now().Add(-time.Minute))
	if sweep := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant)); sweep.code != http.StatusOK {
		t.Fatalf("first expiry sweep = %d %s", sweep.code, sweep.raw)
	}
	updated := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept",
		"enabled": true, "expires_at": time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	}, tenantHdr(tenant))
	if updated.code != http.StatusAccepted {
		t.Fatalf("renewal must be dual-controlled = %d %s", updated.code, updated.raw)
	}
	approved := h.do("POST", "/v1/m/sourcescope/posture-requests/"+updated.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant))
	if approved.code != http.StatusOK {
		t.Fatalf("approve renewal = %d %s", approved.code, approved.raw)
	}
	setBindingExpiry(t, h, tenant, id, time.Now().Add(-time.Minute))
	if sweep := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant)); sweep.code != http.StatusOK {
		t.Fatalf("second expiry sweep = %d %s", sweep.code, sweep.raw)
	}
	if got := accessAuditCount(t, h, tenant, "sourcescope.binding.expire"); got != 2 {
		t.Errorf("renewed binding expiry audits = %d, want 2", got)
	}
	if got := accessFindingCount(h, "sourcescope_binding_expired"); got != 2 {
		t.Errorf("renewed binding expiry findings = %d, want 2", got)
	}
}

func TestAccessRequestRenewsExpiredBinding(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	person := h.principalFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	personToken := existingToken(h, "person@acme.io")
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": person.UserID.String(),
		"enabled": true, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create expiring binding = %d %s", r.code, r.raw)
	}
	setBindingExpiry(t, h, tenant, r.body["id"].(string), time.Now().Add(-time.Minute))
	requested := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}, tenantHdr(tenant))
	if requested.code != http.StatusAccepted {
		t.Fatalf("renewal request was treated as an active duplicate: %d %s", requested.code, requested.raw)
	}
	approved := h.do("POST", "/v1/m/sourcescope/access-requests/"+requested.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant))
	if approved.code != http.StatusOK {
		t.Fatalf("approve renewal request = %d %s", approved.code, approved.raw)
	}
	bot := h.createAgent(tenant, "bot", model.ID(""))
	if d := h.resolveAgent(tenant, person, bot.ExternalID, sourcescope.SourceTool, "claude"); !d.Allowed {
		t.Fatalf("renewed request did not restore access: %+v", d)
	}
}

func TestAccessRequestRefusedForUnconfinedSource(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	h.principalFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	personToken := existingToken(h, "person@acme.io")
	r := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("a source nothing confines is not refused to anyone; request = %d %s, want 409", r.code, r.raw)
	}
}

func TestAccessRequestDoesNotRestoreDisabledBinding(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	person := h.principalFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	personToken := existingToken(h, "person@acme.io")
	seed := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": "owner", "enabled": true,
	})
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": person.UserID.String(),
		"enabled": false, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if seed.code != http.StatusCreated || r.code != http.StatusCreated {
		t.Fatalf("seed bindings = %d %s / %d %s", seed.code, seed.raw, r.code, r.raw)
	}
	setBindingExpiry(t, h, tenant, r.body["id"].(string), time.Now().Add(-time.Minute))
	got := h.do("POST", "/v1/m/sourcescope/access-requests", personToken, map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept",
	}, tenantHdr(tenant))
	if got.code != http.StatusConflict {
		t.Fatalf("an access request must not renew a binding an admin disabled; request = %d %s, want 409", got.code, got.raw)
	}
}

func TestDisabledBindingGetsNoExpiryReminder(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": false,
		"expires_at": time.Now().Add(13 * 24 * time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create disabled expiring binding = %d %s", r.code, r.raw)
	}
	if sweep := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant)); sweep.code != http.StatusOK {
		t.Fatalf("reminder sweep = %d %s", sweep.code, sweep.raw)
	}
	if got := accessFindingCount(h, "sourcescope_binding_expiring_14"); got != 0 {
		t.Errorf("a disabled binding grants nothing, so its expiry is no news: got %d reminders", got)
	}
}

func TestAccessRequestRejectsNonMemberWorkspace(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	dept := h.createWorkspace(tenant, "dept")
	h.createWorkspace(tenant, "other")
	h.confineTo(admin, tenant, "person@acme.io", dept)
	confineTool(t, h, admin, tenant, "claude")
	got := h.do("POST", "/v1/m/sourcescope/access-requests", existingToken(h, "person@acme.io"), map[string]any{
		"source_type": "tool", "source_ref": "claude", "workspace_ref": "other",
	}, tenantHdr(tenant))
	if got.code != http.StatusForbidden {
		t.Fatalf("non-member workspace = %d %s, want 403", got.code, got.raw)
	}
}

func TestAccessRequestLateApprovalGetsFullLifetime(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			h.createWorkspace(tenant, "dept")
			person := h.tokenFor(admin, tenant, "person@acme.io", auth.RoleViewer)
			confineTool(t, h, admin, tenant, "claude")
			if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				_, err := sc.Policies().Create(t.Context(), model.Policy{Kind: auth.PolicyKindGrantExpiry, Enabled: true, Name: "expiry", Spec: map[string]any{"default_seconds": 1}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			in := map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}
			if explicit {
				in["expires_at"] = time.Now().Add(time.Second).Format(time.RFC3339Nano)
			}
			requested := h.do("POST", "/v1/m/sourcescope/access-requests", person, in, tenantHdr(tenant))
			if requested.code != http.StatusAccepted {
				t.Fatalf("request = %d %s", requested.code, requested.raw)
			}
			time.Sleep(1500 * time.Millisecond)
			before := time.Now()
			got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requested.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant))
			if got.code != http.StatusOK {
				t.Fatalf("late approval = %d %s", got.code, got.raw)
			}
			bindings := h.do("GET", "/v1/m/sourcescope/bindings", admin, nil, tenantHdr(tenant))
			found := false
			for _, item := range items(bindings) {
				b := item.(map[string]any)
				if b["scope_tree"] == "user" && b["scope_ref"] != "owner" {
					at, err := time.Parse(time.RFC3339Nano, b["expires_at"].(string))
					if err != nil || at.Before(before.Add(900*time.Millisecond)) {
						t.Errorf("approval did not start full lifetime: %v %v", at, err)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("approved binding missing")
			}
		})
	}
}

func TestAccessRequestDepartmentMembershipAtApproval(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprint(moved), func(t *testing.T) {
			h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			dept := h.createWorkspace(tenant, "dept")
			other := h.createWorkspace(tenant, "other")
			person := h.confineTo(admin, tenant, "person@acme.io", dept)
			approver := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
			h.publishGrant(admin, tenant, `permit(principal in User::"`+approver.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`)
			seed := h.createBinding(admin, tenant, map[string]any{"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true})
			if seed.code != http.StatusCreated {
				t.Fatal(seed.raw)
			}
			requested := h.do("POST", "/v1/m/sourcescope/access-requests", existingToken(h, "person@acme.io"), map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}, tenantHdr(tenant))
			if requested.code != http.StatusAccepted {
				t.Fatalf("request = %d %s", requested.code, requested.raw)
			}
			if moved {
				if err := h.st.AuthMutate(t.Context(), func(sc store.AuthScope) error {
					ms, _, err := sc.Memberships().List(t.Context(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: person.UserID.String()}}})
					if err != nil {
						return err
					}
					for _, m := range ms {
						if m.TargetTenantID == tenant {
							m.WorkspaceID = other
							_, err = sc.Memberships().Update(t.Context(), m)
							return err
						}
					}
					return errors.New("membership missing")
				}); err != nil {
					t.Fatal(err)
				}
			}
			got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requested.body["id"].(string)+"/approve", existingToken(h, "dept-admin@acme.io"), nil, tenantHdr(tenant))
			want := http.StatusOK
			if moved {
				want = http.StatusForbidden
			}
			if got.code != want {
				t.Fatalf("approve after transfer=%v: %d %s, want %d", moved, got.code, got.raw, want)
			}
			if moved {
				refused := h.do("POST", "/v1/m/sourcescope/posture-requests/"+requested.body["id"].(string)+"/reject", admin, nil, tenantHdr(tenant))
				if refused.code != http.StatusOK {
					t.Fatalf("owner must be able to reject a request after transfer: %d %s", refused.code, refused.raw)
				}
			}
		})
	}
}

func TestAccessRequestApprovalRefusesRemovedConfinement(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	person := h.tokenFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	seed := h.createBinding(admin, tenant, map[string]any{"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": "owner", "enabled": true})
	if seed.code != http.StatusCreated {
		t.Fatal(seed.raw)
	}
	requested := h.do("POST", "/v1/m/sourcescope/access-requests", person, map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}, tenantHdr(tenant))
	if requested.code != http.StatusAccepted {
		t.Fatal(requested.raw)
	}
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sourcescope.binding")
		if err != nil {
			return err
		}
		return repo.Delete(t.Context(), model.ID(seed.body["id"].(string)))
	}); err != nil {
		t.Fatal(err)
	}
	got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requested.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant))
	if got.code != http.StatusConflict {
		t.Fatalf("approval created the first allow after the premise disappeared: %d %s", got.code, got.raw)
	}
}

func TestAccessRequestTenantAdminStillHonorsSourceOwnerForbid(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "dept")
	h.createWorkspace(tenant, "other")
	person := h.tokenFor(admin, tenant, "person@acme.io", auth.RoleViewer)
	approver := h.principalFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	seed := h.createBinding(admin, tenant, map[string]any{"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "other", "enabled": true})
	if seed.code != http.StatusCreated {
		t.Fatal(seed.raw)
	}
	h.publishGrant(admin, tenant, `forbid(principal in User::"`+approver.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"other" };`)
	requested := h.do("POST", "/v1/m/sourcescope/access-requests", person, map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}, tenantHdr(tenant))
	if requested.code != http.StatusAccepted {
		t.Fatal(requested.raw)
	}
	got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requested.body["id"].(string)+"/approve", existingToken(h, "approver@acme.io"), nil, tenantHdr(tenant))
	if got.code != http.StatusForbidden {
		t.Fatalf("tenant authority bypassed source-owner forbid: %d %s", got.code, got.raw)
	}
}

func TestAccessRequestSecondDepartmentApproval(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	dept := h.createWorkspace(tenant, "dept")
	approver := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
	h.publishGrant(admin, tenant, `permit(principal in User::"`+approver.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`)
	seed := h.createBinding(admin, tenant, map[string]any{"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true})
	if seed.code != http.StatusCreated {
		t.Fatal(seed.raw)
	}
	for i := range 2 {
		email := fmt.Sprintf("person%d@acme.io", i)
		h.confineTo(admin, tenant, email, dept)
		requested := h.do("POST", "/v1/m/sourcescope/access-requests", existingToken(h, email), map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}, tenantHdr(tenant))
		if requested.code != http.StatusAccepted {
			t.Fatal(requested.raw)
		}
		got := h.do("POST", "/v1/m/sourcescope/access-requests/"+requested.body["id"].(string)+"/approve", existingToken(h, "dept-admin@acme.io"), nil, tenantHdr(tenant))
		if got.code != http.StatusOK {
			t.Fatalf("approval %d = %d %s, want 200", i, got.code, got.raw)
		}
	}
}

func TestBindingRejectsUnreadableCanonicalExpiry(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	invalid := h.createBinding(admin, tenant, map[string]any{"source_type": "tool", "source_ref": "claude", "scope_tree": "user", "scope_ref": "person", "enabled": true, "expires_at": "9999-12-31T23:59:59-23:00"})
	if invalid.code != http.StatusBadRequest {
		t.Fatalf("unreadable expiry = %d %s, want 400", invalid.code, invalid.raw)
	}
	good := h.createBinding(admin, tenant, map[string]any{"source_type": "tool", "source_ref": "codex", "scope_tree": "user", "scope_ref": "person", "enabled": true, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if good.code != http.StatusCreated {
		t.Fatal(good.raw)
	}
	setBindingExpiry(t, h, tenant, good.body["id"].(string), time.Now().Add(-3*time.Second))
	sweep := h.do("POST", "/v1/m/sourcescope/expiry/sweep", admin, nil, tenantHdr(tenant))
	if sweep.code != http.StatusOK {
		t.Fatalf("sweep = %d %s", sweep.code, sweep.raw)
	}
	if got := accessAuditCount(t, h, tenant, "sourcescope.binding.expire"); got != 1 {
		t.Fatalf("expiry audits = %d, want 1", got)
	}
}

// A forbid restricts and owns nothing: a restriction the tenant admin places inside a
// department must not let that department's admin alone approve access to a source the
// tenant confined to someone else.
func TestAccessRequestForbidDoesNotMakeSourceDepartmentOwned(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	dept := h.createWorkspace(tenant, "dept")
	h.confineTo(admin, tenant, "person@acme.io", dept)
	approver := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
	h.publishGrant(admin, tenant, `permit(principal in User::"`+approver.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`)
	for _, binding := range []map[string]any{
		{"scope_tree": "user", "scope_ref": "owner"},
		{"scope_tree": "workspace", "scope_ref": "dept", "effect": "forbid"},
	} {
		binding["source_type"], binding["source_ref"], binding["enabled"] = "tool", "claude", true
		if r := h.createBinding(admin, tenant, binding); r.code != http.StatusCreated {
			t.Fatalf("create %v binding = %d %s", binding["scope_tree"], r.code, r.raw)
		}
	}
	requested := h.do("POST", "/v1/m/sourcescope/access-requests", existingToken(h, "person@acme.io"), map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}, tenantHdr(tenant))
	if requested.code != http.StatusAccepted {
		t.Fatalf("request = %d %s", requested.code, requested.raw)
	}
	approve := "/v1/m/sourcescope/access-requests/" + requested.body["id"].(string) + "/approve"
	if got := h.do("POST", approve, existingToken(h, "dept-admin@acme.io"), nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("department admin approved a tenant-confined source after a forbid marker: %d %s, want 403", got.code, got.raw)
	}
	if got := h.do("POST", approve, admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("tenant admin approval = %d %s, want 200", got.code, got.raw)
	}
}

// An allow that has expired grants its department nothing, and one with an end date would
// let that department's admin approve grants that outlive it: neither makes the department
// the owner of the source, so the tenant admin decides.
func TestAccessRequestLapsedOrTimeBoxedDepartmentAllowNeedsTenantApproval(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lapse bool
	}{{"time-boxed", false}, {"expired", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			dept := h.createWorkspace(tenant, "dept")
			h.confineTo(admin, tenant, "person@acme.io", dept)
			approver := h.principalFor(admin, tenant, "dept-admin@acme.io", auth.RoleViewer)
			h.publishGrant(admin, tenant, `permit(principal in User::"`+approver.UserID.String()+`", action == Action::"sourcescope:posture:admin", resource) when { resource in Workspace::"dept" };`)
			seed := h.createBinding(admin, tenant, map[string]any{
				"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "dept", "enabled": true,
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
			if seed.code != http.StatusCreated {
				t.Fatalf("create department allow = %d %s", seed.code, seed.raw)
			}
			if tc.lapse {
				setBindingExpiry(t, h, tenant, seed.body["id"].(string), time.Now().Add(-time.Hour))
			}
			requested := h.do("POST", "/v1/m/sourcescope/access-requests", existingToken(h, "person@acme.io"), map[string]any{"source_type": "tool", "source_ref": "claude", "workspace_ref": "dept"}, tenantHdr(tenant))
			if requested.code != http.StatusAccepted {
				t.Fatalf("request = %d %s", requested.code, requested.raw)
			}
			approve := "/v1/m/sourcescope/access-requests/" + requested.body["id"].(string) + "/approve"
			if got := h.do("POST", approve, existingToken(h, "dept-admin@acme.io"), nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
				t.Fatalf("department admin approved on a %s department allow: %d %s, want 403", tc.name, got.code, got.raw)
			}
			if got := h.do("POST", approve, admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
				t.Fatalf("tenant admin approval = %d %s, want 200", got.code, got.raw)
			}
		})
	}
}
