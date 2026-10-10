// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestExecutorSavedConfiguration(t *testing.T) {
	h := newHarnessWith(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "executor-config")
	other := h.createOrg(admin, "executor-other")
	headers := tenantHdr(tenant)
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.SetOrgSettings(context.Background(), map[string]any{"existing_setting": "retained"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"credential_ref": "store:deploy-token"}
	r := h.do("PUT", "/v1/m/deploy/executor/config", admin, config, headers)
	if r.code != http.StatusOK {
		t.Fatalf("save executor = %d %s, want 200", r.code, r.raw)
	}
	if r.body["credential_ref"] != "store:deploy-token" || r.body["socket_path"] != "/var/run/docker.sock" {
		t.Fatalf("saved config = %s", r.raw)
	}
	r = h.do("GET", "/v1/m/deploy/executor/config", admin, nil, headers)
	if r.code != http.StatusOK || r.body["credential_ref"] != "store:deploy-token" {
		t.Fatalf("load saved config = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/deploy/executor/config", admin, nil, tenantHdr(other))
	if r.code != http.StatusOK || r.body["credential_ref"] != "" {
		t.Fatalf("another tenant saw configuration: %d %s", r.code, r.raw)
	}
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		org, err := sc.Org(context.Background())
		if err == nil && org.Settings["existing_setting"] != "retained" {
			t.Error("configuration replaced unrelated organization settings")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSavedExecutorPreservesConfinedReadiness(t *testing.T) {
	setup := &testExecutorSetup{exec: newMockExecutor()}
	h := newHarnessWith(t, WithExecutorSetup(setup, false))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "executor-confined")
	var workspace model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "workspace", Slug: "workspace", Status: model.StatusActive})
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "confined@executor.io", "password": "memberpass1", "tenant": tenant.String(), "role": "admin", "workspace_id": workspace.String()}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create confined user = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "confined@executor.io", "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login = %d %s", r.code, r.raw)
	}
	confined := r.body["token"].(string)
	r = h.do("PUT", "/v1/m/deploy/executor/config", admin, map[string]any{"credential_ref": "store:env/deploy"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("save = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/deploy/executor", confined, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["configured"] != false || setup.config.CredentialRef != "" {
		t.Fatalf("confined readiness changed or consumed tenant setup = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/deploy/executor/config", confined, nil, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("confined user accessed parent setup = %d %s", r.code, r.raw)
	}
}

type testExecutorSetup struct {
	exec    *mockExecutor
	tested  int
	testErr error
	tenant  model.TenantID
	config  ExecutorConfig
}

func (s *testExecutorSetup) Build(_ context.Context, tenant model.TenantID, cfg ExecutorConfig) (Executor, error) {
	s.tenant, s.config = tenant, cfg
	return s.exec, nil
}
func (s *testExecutorSetup) Test(_ context.Context, tenant model.TenantID, cfg ExecutorConfig) error {
	s.tested++
	s.tenant, s.config = tenant, cfg
	return s.testErr
}

func TestSavedExecutorSetupFlowsThroughGovernanceAndReconfiguration(t *testing.T) {
	setup := &testExecutorSetup{exec: newMockExecutor()}
	gate := newFakeGate()
	h := newHarnessWith(t, WithExecutorSetup(setup, false), WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "executor-flow")
	headers := tenantHdr(tenant)
	h.stepUp(admin)
	save := func(ref string) {
		t.Helper()
		r := h.do("PUT", "/v1/m/deploy/executor/config", admin, map[string]any{"credential_ref": ref}, headers)
		if r.code != http.StatusOK {
			t.Fatalf("save = %d %s", r.code, r.raw)
		}
	}
	save("store:env/deploy-first")
	r := h.do("GET", "/v1/m/deploy/executor", admin, nil, headers)
	if r.code != http.StatusOK || r.body["configured"] != true || setup.tenant != tenant {
		t.Fatalf("readiness = %d %s", r.code, r.raw)
	}
	setup.testErr = errors.New("executor: connection refused")
	r = h.do("POST", "/v1/m/deploy/executor/test", admin, nil, headers)
	if r.code != http.StatusBadGateway || setup.tested != 1 {
		t.Fatalf("failed connection test = %d %s", r.code, r.raw)
	}
	if setup.exec.applyCalls != 0 {
		t.Fatal("connection test applied infrastructure")
	}
	setup.testErr = nil
	r = h.do("POST", "/v1/m/deploy/executor/test", admin, nil, headers)
	if r.code != http.StatusOK || r.body["connected"] != true {
		t.Fatalf("connection test = %d %s", r.code, r.raw)
	}
	id := h.createDef(admin, tenant, "setup-agent", agentSpec("agent:1", "setup-identity"))
	path := "/v1/m/deploy/definitions/" + id + "/apply"
	r = h.do("POST", path, admin, nil, headers)
	if r.code != http.StatusAccepted || setup.exec.applyCalls != 0 {
		t.Fatalf("approval request = %d %s", r.code, r.raw)
	}
	ref := r.body["approval_ref"].(string)
	gate.set(ref, StatusApproved)
	save("store:env/deploy-second")
	r = h.do("POST", path, admin, map[string]any{"approval_ref": ref}, headers)
	if r.code != http.StatusForbidden || setup.exec.applyCalls != 0 {
		t.Fatalf("old approval = %d %s", r.code, r.raw)
	}
	r = h.do("POST", path, admin, nil, headers)
	ref = r.body["approval_ref"].(string)
	gate.set(ref, StatusApproved)
	r = h.do("POST", path, admin, map[string]any{"approval_ref": ref}, headers)
	if r.code != http.StatusOK || setup.exec.applyCalls != 1 || setup.config.CredentialRef != "store:env/deploy-second" {
		t.Fatalf("governed apply = %d %s", r.code, r.raw)
	}
}

func TestOperatorExecutorOverridePreservesLegacyConfiguration(t *testing.T) {
	setup := &testExecutorSetup{exec: newMockExecutor()}
	h := newHarnessWith(t, WithExecutor(newMockExecutor()), WithExecutorSetup(setup, true))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "executor-override")
	for _, path := range []string{"/executor/config", "/executor/test"} {
		method := "PUT"
		if path == "/executor/test" {
			method = "POST"
		}
		r := h.do(method, "/v1/m/deploy"+path, admin, map[string]any{"credential_ref": "store:env/deploy"}, tenantHdr(tenant))
		if r.code != http.StatusConflict {
			t.Fatalf("operator override %s = %d %s", path, r.code, r.raw)
		}
	}
	r := h.do("GET", "/v1/m/deploy/executor", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["configured"] != true || setup.tested != 0 || setup.config.CredentialRef != "" {
		t.Fatalf("override readiness = %d %s", r.code, r.raw)
	}
}

func TestEmptyOperatorExecutorOverrideRemainsAuthoritative(t *testing.T) {
	setup := &testExecutorSetup{exec: newMockExecutor()}
	h := newHarnessWith(t, WithExecutorSetup(setup, true))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "executor-empty-override")
	r := h.do("GET", "/v1/m/deploy/executor", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["configured"] != false {
		t.Fatalf("empty override readiness = %d %s", r.code, r.raw)
	}
	for _, method := range []string{"PUT", "POST"} {
		path := "/v1/m/deploy/executor/config"
		if method == "POST" {
			path = "/v1/m/deploy/executor/test"
		}
		r = h.do(method, path, admin, map[string]any{"credential_ref": "store:env/deploy"}, tenantHdr(tenant))
		if r.code != http.StatusConflict {
			t.Fatalf("empty override %s = %d %s", method, r.code, r.raw)
		}
	}
	if setup.tested != 0 || setup.config.CredentialRef != "" {
		t.Fatal("empty operator override fell back to saved setup")
	}
}

func TestExecutorSetupRefusesInvalidInputAndInsufficientAuthority(t *testing.T) {
	h := newHarnessWith(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "executor-refusal")
	editor := h.roleToken(admin, tenant, "executor-editor@x.io", "editor")
	for _, path := range []string{"/executor/config", "/executor/test"} {
		method := "PUT"
		if path == "/executor/test" {
			method = "POST"
		}
		r := h.do(method, "/v1/m/deploy"+path, editor, map[string]any{"credential_ref": "store:deploy-token"}, tenantHdr(tenant))
		if r.code != http.StatusForbidden {
			t.Fatalf("editor %s = %d %s, want 403", path, r.code, r.raw)
		}
	}
	for _, config := range []map[string]any{
		{}, {"credential_ref": "plain-secret"}, {"credential_ref": "file:/etc/shadow"},
		{"credential_ref": "store:deploy-token", "socket_path": "/run/unrelated.sock"},
		{"credential_ref": "store:deploy-token", "token": "inline-secret"},
	} {
		r := h.do("PUT", "/v1/m/deploy/executor/config", admin, config, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Fatalf("invalid config = %d %s, want 400", r.code, r.raw)
		}
	}
	r := h.do("POST", "/v1/m/deploy/executor/test", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("missing executor test = %d %s, want 503", r.code, r.raw)
	}
}
