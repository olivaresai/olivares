// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// grantsOnly is a scoped engine that grants one permission to one user, as a scoped admin
// grant of that permission would, and abstains for everything else.
type grantsOnly struct {
	user model.ID
	perm auth.Permission
}

func (g grantsOnly) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if req.Principal.UserID == g.user && req.Permission == g.perm {
		return auth.ScopedDecision{Effect: auth.EffectGrant}, nil
	}
	return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
}

// The work admin bit a REST work command carries is the admission seam's answer for
// sessions:work:admin. A principal admitted only by a scoped grant (it holds no tenant
// role at all here) is a work admin; one the engine does not admit is not; and a context
// with no admission door refuses.
func TestWorkAdminIsTheAdmissionSeamsAnswer(t *testing.T) {
	tenant := model.NewTenantID()
	scoped := auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}
	stranger := auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(grantsOnly{user: scoped.UserID, perm: permWorkAdmin}))
	door := func(ctx context.Context, req auth.Request) bool { return az.Authorize(ctx, req).Allow }

	for _, tc := range []struct {
		name string
		mc   api.ModuleContext
		want bool
	}{
		{"scoped grant admits", api.ModuleContext{Principal: scoped, Tenant: tenant, Admission: door}, true},
		{"not admitted by the engine", api.ModuleContext{Principal: stranger, Tenant: tenant, Admission: door}, false},
		{"no admission door refuses", api.ModuleContext{Principal: scoped, Tenant: tenant}, false},
	} {
		if got := workAdmin(context.Background(), tc.mc); got != tc.want {
			t.Errorf("%s: workAdmin = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A dedicated orchestration credential is a work admin only through its granted capability:
// work.review makes it one whatever the door answers, and no other capability does, even
// when the door would admit.
func TestWorkAdminOfAnOrchestrationCredentialIsItsCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []string
		door bool
		want bool
	}{
		{"review capability, the door refuses", []string{"work.read", "work.review"}, false, true},
		{"no review capability, the door admits", []string{"work.read"}, true, false},
	} {
		f := newOrchestrationFixture(t, tc.caps)
		p, err := auth.NewAuthenticator(f.h.st, nil).Authenticate(t.Context(), f.token)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsOrchestrationSessionCredential() {
			t.Fatalf("%s: the fixture credential is not an orchestration credential", tc.name)
		}
		mc := api.ModuleContext{Principal: p, Tenant: f.tenant, Admission: func(context.Context, auth.Request) bool { return tc.door }}
		if got := workAdmin(t.Context(), mc); got != tc.want {
			t.Errorf("%s: workAdmin = %v, want %v", tc.name, got, tc.want)
		}
	}
}
