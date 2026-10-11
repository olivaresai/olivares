// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// scopedGrantsOf is a scoped engine that grants exactly the listed permissions, as a scoped
// admin grant of them would, and abstains for everything else.
type scopedGrantsOf map[auth.Permission]bool

func (g scopedGrantsOf) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if g[req.Permission] {
		return auth.ScopedDecision{Effect: auth.EffectGrant}, nil
	}
	return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
}

// An issued session credential lists the work write tool when the admission seam admits
// its launcher to write work, whatever the rank of the launcher's role: a viewer holding
// a tenant-scope grant gets it, an editor by role keeps it, and a viewer holding nothing
// does not. A handler with no admission door lists no write tool, and the call is refused
// exactly where the listing hides the tool.
func TestSessionMCPWriteToolFollowsTheAdmissionSeam(t *testing.T) {
	a, tenant, _, viewerLauncher, _, _ := workSessionEdgePrincipals(t)
	ctx := context.Background()
	admin := auth.ScopedPrincipal(model.NewID(), "edge admin", tenant, auth.RoleAdmin)
	editorToken, _, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "edge editor", BoundTenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	editorLauncher, err := a.Authenticate(ctx, editorToken)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	bearerFor := func(launcher auth.Principal) string {
		t.Helper()
		bearer, err := issuer.Mint(ctx, launcher, auth.SessionScope{
			TenantID: tenant, WorkspaceID: model.NewID(), FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(),
			RunRef: model.NewID().String(), Fence: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return bearer
	}
	rbacOnly := auth.NewAuthorizer(nil)
	withGrant := auth.NewAuthorizer(nil, auth.WithScopedGrants(scopedGrantsOf{auth.WorkSessionWorkWrite: true}))

	for _, tc := range []struct {
		name     string
		launcher auth.Principal
		admits   func(context.Context, auth.Request) bool
		want     bool
	}{
		{"viewer with a tenant-scope grant", viewerLauncher, admitsThrough(withGrant), true},
		{"editor by role", editorLauncher, admitsThrough(rbacOnly), true},
		{"viewer with nothing", viewerLauncher, admitsThrough(rbacOnly), false},
		{"editor without an admission door", editorLauncher, nil, false},
	} {
		calls := 0
		h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, admits: tc.admits,
			work: func(w http.ResponseWriter, _ *http.Request, _ auth.Principal, _ model.TenantID) {
				calls++
				_, _ = w.Write([]byte(`{}`))
			}}
		bearer := bearerFor(tc.launcher)
		rpc := func(body string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+bearer)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w
		}
		w := rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: tools/list = %d %s", tc.name, w.Code, w.Body.String())
		}
		if got := strings.Contains(w.Body.String(), "olivares_work_command"); got != tc.want {
			t.Errorf("%s: olivares_work_command listed = %v, want %v", tc.name, got, tc.want)
		}
		rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"olivares_work_command","arguments":{"mode":"validate","command":{"command":"item.create"}}}}`)
		if reached := calls == 1; reached != tc.want {
			t.Errorf("%s: olivares_work_command reached the work port = %v, want %v", tc.name, reached, tc.want)
		}
	}
}
