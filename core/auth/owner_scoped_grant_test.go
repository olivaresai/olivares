// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestTenantOwnerHoldsImplicitScopedGrant(t *testing.T) {
	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		t.Run(role, func(t *testing.T) {
			f, principal := routeParityPrincipal(t, role)
			az := NewAuthorizer(nil)
			req := routeParityRequest(principal, f.tenant, scopedGrantRoute())
			legacy := az.Authorize(context.Background(), req)
			ev := az.AuthorizeEvidence(context.Background(), req)
			w, err := az.AuthorizeRoute(context.Background(), req)
			if role == RoleOwner {
				if !legacy.Allow || legacy.Reason != "permitted (owner implicit scoped grant)" || ev.Outcome != EvidenceAllow || ev.CorePermission.Code != "owner_scoped_grant_permitted" || err != nil || !w.VerifyFor(time.Now(), req) {
					t.Fatalf("owner implicit grant: legacy=%+v evidence=%+v err=%v", legacy, ev, err)
				}
				if ev.ScopedEffect != EffectAbstain {
					t.Fatalf("implicit grant claimed a producer grant: %+v", ev)
				}
			} else if legacy.Allow || ev.Outcome != EvidenceDeny || !errors.Is(err, ErrRouteDenied) {
				t.Fatalf("non-owner needs an explicit grant: legacy=%+v evidence=%+v err=%v", legacy, ev, err)
			}
		})
	}
}

func TestTenantOwnerImplicitGrantPreservesScopeForbidsAndUnknown(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleOwner)
	req := routeParityRequest(principal, f.tenant, scopedGrantRoute())
	fact := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 11}
	scoped := &parityScopedEngine{
		legacy: ScopedDecision{Effect: EffectForbid, Reason: "workspace confinement", Class: ClassInvariant},
		typed:  principalAuthorityBrokenScoped(principal.evidence.observedAt, principal.evidence.freshUntil, fact),
	}
	az := NewAuthorizer(nil, WithScopedGrants(scoped))
	if dec := az.Authorize(context.Background(), req); dec.Allow || dec.Class != ClassInvariant {
		t.Fatalf("owner escaped confinement: %+v", dec)
	}
	if ev := az.AuthorizeEvidence(context.Background(), req); ev.Outcome != EvidenceDeny || ev.ResourceGuard.Verdict != CheckBroken {
		t.Fatalf("owner escaped typed confinement: %+v", ev)
	}
	scoped.legacyErr = errors.New("scope unavailable")
	scoped.typedErr = scoped.legacyErr
	if dec := az.Authorize(context.Background(), req); dec.Allow {
		t.Fatalf("owner escaped unavailable scope: %+v", dec)
	}
	if ev := az.AuthorizeEvidence(context.Background(), req); ev.Outcome != EvidenceUnknown {
		t.Fatalf("unavailable scope was not unknown: %+v", ev)
	}
	az = NewAuthorizer(nil)
	req.Tenant = model.NewTenantID()
	if az.Authorize(context.Background(), req).Allow {
		t.Fatal("owner acquired another tenant's scoped grant")
	}
	req.Tenant = model.SystemTenantID
	if az.Authorize(context.Background(), req).Allow {
		t.Fatal("owner acquired system authority")
	}
	req.Tenant = f.tenant
	req.Permission = PermSystemAdmin
	if az.Authorize(context.Background(), req).Allow {
		t.Fatal("owner acquired system permission")
	}
}
