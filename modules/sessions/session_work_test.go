// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSessionWorkUsesSharedPrincipalAndConcealsForeignWorkspace(t *testing.T) {
	f := newOrchestrationFixtureInWorkspace(t, []string{"work.read"}, true)
	a := auth.NewAuthenticator(f.h.st, nil)
	launcher, err := a.Authenticate(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := f.h.m.rt.getLive(f.tenant, f.run)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
		TenantID: f.tenant, WorkspaceID: f.workspace, FolderRef: "test-folder", FolderPath: live.companion.Dir,
		SessionRef: f.sid, RunRef: f.run, AgentRef: live.agentRef, Fence: live.claim.Fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.Authenticate(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	if _, ordinary := p.Ref(); ordinary {
		t.Fatal("session retained an admin REST identity")
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		f.h.m.CallSessionWork(w, httptest.NewRequest(method, "/v1/m/sessions/"+path, strings.NewReader(body)), p, f.tenant)
		return w
	}
	if w := call("GET", "work-items", ""); w.Code != 200 {
		t.Fatalf("shared principal list: %d %s", w.Code, w.Body.String())
	}
	f.resolver.admit("user", launcher.UserID.String())
	foreign := workAPIWorkspace(t, f.h, f.tenant)
	created := f.h.doJSON("POST", "/v1/m/sessions/work-items?mode=apply", f.admin,
		workAPICreateBody(foreign, launcher.UserID.String(), "Foreign work must remain hidden"),
		workAPIHeaders(f.tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	if created.code != http.StatusOK {
		t.Fatalf("foreign fixture: %d %s", created.code, created.raw)
	}
	item, _ := created.body["result_id"].(string)
	if item == "" {
		t.Fatalf("foreign fixture has no item: %s", created.raw)
	}
	if w := call("GET", "work-items/"+item, ""); w.Code != http.StatusNotFound {
		t.Fatalf("superadmin parent escaped workspace: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "work-items", ""); w.Code != 200 || strings.Contains(w.Body.String(), "Foreign work") {
		t.Fatalf("foreign workspace listed: %d %s", w.Code, w.Body.String())
	}
	WithWorkAuthorizer(permissionSetWorkAuthorizer{permWorkRead: true})(f.h.m)
	if w := call("POST", "work-items?mode=validate", `{"command":"item.create"}`); w.Code != http.StatusForbidden {
		t.Fatalf("write ignored current authorization: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "inbox", ""); w.Code != http.StatusNotFound {
		t.Fatal("communication entered the work port")
	}
	if _, err := f.h.m.stopRun(t.Context(), f.tenant, f.run, "operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "work-items", ""); w.Code != http.StatusForbidden {
		t.Fatal("stopped session retained native work authority")
	}
}
