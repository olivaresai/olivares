// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func workSessionEdgePrincipals(
	t *testing.T,
) (*auth.Authenticator, model.TenantID, string, auth.Principal, string, auth.Principal) {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "work edge", Slug: "work-edge", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthenticator(st, nil)
	admin := auth.ScopedPrincipal(model.NewID(), "edge admin", tenant, auth.RoleAdmin)
	ordinaryToken, _, err := a.IssueToken(ctx, admin, auth.TokenSpec{
		Name: "edge ordinary", BoundTenant: tenant, Role: auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := a.Authenticate(ctx, ordinaryToken)
	if err != nil {
		t.Fatal(err)
	}
	system, err := auth.NewSystemOperator("test:sessions-runtime", "exercise purpose-restricted protocol edges")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := a.IssueWorkSessionCredential(ctx, system, auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	work, err := a.Authenticate(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	return a, tenant, ordinaryToken, ordinary, issued.Token, work
}

func TestWorkSessionCredentialCannotBypassPurposeCeilingAtMembershipEdges(t *testing.T) {
	a, tenant, ordinaryToken, ordinary, workToken, work := workSessionEdgePrincipals(t)

	proxy := &inferenceProxyDecider{tenantHint: tenant}
	if _, ok := proxy.resolveTenant(work); ok {
		t.Fatal("inference proxy accepted work-session membership as gateway authority")
	}
	if got, ok := proxy.resolveTenant(ordinary); !ok || got != tenant {
		t.Fatalf("ordinary inference control = tenant %s ok=%v", got, ok)
	}

	apps := &appsGatewayHandler{tenantHint: tenant, authr: a}
	if _, ok := apps.resolveTenant(work); ok {
		t.Fatal("apps gateway accepted work-session membership")
	}
	if got, ok := apps.resolveTenant(ordinary); !ok || got != tenant {
		t.Fatalf("ordinary apps control = tenant %s ok=%v", got, ok)
	}
	requestFor := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	if _, ok := apps.authenticateBearer(context.Background(), requestFor(workToken)); ok {
		t.Fatal("apps bearer authentication admitted purpose-restricted token")
	}
	if _, ok := apps.authenticateBearer(context.Background(), requestFor(ordinaryToken)); !ok {
		t.Fatal("apps bearer authentication rejected ordinary control")
	}

	hook := &claudeHookDecider{tenants: map[model.TenantID]resolvedTenant{
		tenant: {tenant: tenant},
	}}
	if got, resolution := hook.resolveTenant(tenant.String(), work, nil); resolution.found || !got.IsZero() {
		t.Fatalf("Claude hook accepted work-session membership: tenant=%s found=%v", got, resolution.found)
	}
	if got, resolution := hook.resolveTenant(tenant.String(), ordinary, nil); !resolution.found || got != tenant {
		t.Fatalf("ordinary Claude hook control: tenant=%s found=%v", got, resolution.found)
	}

	codex := &codexHookDecider{authr: a}
	if _, _, tier := codex.principalOf(context.Background(), workToken); tier != tierUnknown {
		t.Fatalf("Codex hook work-session tier = %q, want unknown", tier)
	}
	if _, _, tier := codex.principalOf(context.Background(), ordinaryToken); tier != tierFirm {
		t.Fatalf("Codex hook ordinary tier = %q, want firm", tier)
	}
}
