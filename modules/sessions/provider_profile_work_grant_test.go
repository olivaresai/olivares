// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProviderProfileWorkGrantAPIRejectsInvalidOrUnauthorizedGrants(t *testing.T) {
	m := New()
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "grant-denials")
	viewer := h.viewerToken(admin, tenant, "viewer@grant.test")
	var workspace model.ID
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(context.Background())
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token string
		grant       map[string]any
		status      int
	}{
		{"viewer", viewer, map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": []string{"work.read"}}, http.StatusForbidden},
		{"unknown capability", admin, map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": []string{"token.write"}}, http.StatusBadRequest},
		{"no workspace", admin, map[string]any{"role": "orchestrator", "capabilities": []string{"work.create"}}, http.StatusBadRequest},
		{"foreign workspace", admin, map[string]any{"role": "orchestrator", "workspace_id": model.NewID(), "capabilities": []string{"work.read"}}, http.StatusBadRequest},
		{"caller grant identity", admin, map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": []string{"work.read"}, "grant_id": model.NewID()}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(),
				"session_work_grant": tc.grant,
			}
			r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", tc.token, body, tenantHdr(tenant))
			if r.code != tc.status {
				t.Fatalf("grant status = %d, want %d: %s", r.code, tc.status, r.raw)
			}
		})
	}
}

func TestProviderProfileWorkGrantAPIDefaultRevocationAndGeneration(t *testing.T) {
	m := New()
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "grant-lifecycle")
	var workspace model.ID
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(context.Background())
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{
		"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(),
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create undeclared profile = %d: %s", r.code, r.raw)
	}
	if grant := r.body["session_work_grant"]; grant != nil {
		t.Fatal("undeclared profile has an orchestration grant")
	}
	path := "/v1/m/sessions/provider-profiles/" + r.body["profile_ref"].(string)
	grant := map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": []string{"work.read", "work.create"}}
	r = h.doJSON("PATCH", path, admin, map[string]any{"session_work_grant": grant}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("grant profile = %d: %s", r.code, r.raw)
	}
	stored, ok := r.body["session_work_grant"].(map[string]any)
	if !ok || stored["role"] != "orchestrator" || stored["workspace_id"] != workspace.String() || stored["grant_id"] == "" {
		t.Fatalf("grant not durably exposed: %s", r.raw)
	}
	firstGeneration := stored["grant_id"]
	r = h.doJSON("PATCH", path, admin, map[string]any{"session_work_grant": nil}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["session_work_grant"] != nil {
		t.Fatalf("grant revocation = %d: %s", r.code, r.raw)
	}
	r = h.doJSON("PATCH", path, admin, map[string]any{"session_work_grant": grant}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("regrant = %d: %s", r.code, r.raw)
	}
	if r.body["session_work_grant"].(map[string]any)["grant_id"] == firstGeneration {
		t.Fatal("regrant revived the revoked grant generation")
	}
}
