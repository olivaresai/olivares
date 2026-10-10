// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workSessionEdgePrincipals authenticates an ordinary tenant token and a purpose-restricted
// work-session credential for the same tenant.
func workSessionEdgePrincipals(t *testing.T) (model.TenantID, auth.Principal, auth.Principal) {
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
	return tenant, ordinary, work
}

// The Claude hook's tenant edge is one of the membership edges a work-session credential must
// not pass (cmd/olivares pins the inference proxy, apps gateway and Codex hook edges).
func TestWorkSessionCredentialCannotBypassPurposeCeilingAtClaudeHookEdge(t *testing.T) {
	tenant, ordinary, work := workSessionEdgePrincipals(t)
	hook := &Decider{Tenants: map[model.TenantID]ResolvedTenant{
		tenant: {tenant: tenant},
	}}
	if got, resolution := hook.resolveTenant(tenant.String(), work, nil); resolution.found || !got.IsZero() {
		t.Fatalf("Claude hook accepted work-session membership: tenant=%s found=%v", got, resolution.found)
	}
	if got, resolution := hook.resolveTenant(tenant.String(), ordinary, nil); !resolution.found || got != tenant {
		t.Fatalf("ordinary Claude hook control: tenant=%s found=%v", got, resolution.found)
	}
}
