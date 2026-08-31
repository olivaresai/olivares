// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestS391F01ResolveTenantHintRequiresMembership is the RED repro for F-01.
//
// resolveTenant selects WHICH tenant's governed hook policy applies to a Claude
// Code tool-call. The tenant "hint" is client-supplied (HTTP header
// X-Olivares-Hook-Tenant -> HookDecisionInput.Identity.Tenant). On the hint
// branch the code only confirmed that a governed policy EXISTS for the hinted
// tenant (`d.tenants[tid]`), never that the authenticated principal is a member
// of it. So a principal authenticated for tenant A can name tenant B and have
// B's (possibly permissive) policy govern the call — a cross-tenant governance
// boundary escape. The correct pattern already lives in inferenceProxyDecider
// .resolveTenant (Superadmin || IsMember) and appsGatewayHandler.resolveTenant.
//
// SECURE behavior (asserted here): the hint is honored ONLY when authErr==nil
// AND (principal is a member of the hinted tenant OR superadmin). RED today for
// the cross-tenant case; the positive cases must keep working after the fix.
func TestS391F01ResolveTenantHintRequiresMembership(t *testing.T) {
	tidA := model.NewTenantID()
	tidB := model.NewTenantID()

	dec := &claudeHookDecider{
		tenants: map[model.TenantID]resolvedTenant{
			tidA: {tenant: tidA},
			tidB: {tenant: tidB},
		},
	}

	memberOfA := auth.ScopedPrincipal(model.NewID(), "agent-a", tidA, "editor")
	superadmin := func() auth.Principal {
		p := auth.ScopedPrincipal(model.NewID(), "root", tidA, "owner")
		p.Superadmin = true
		return p
	}()

	t.Run("cross-tenant hint from a non-member is denied", func(t *testing.T) {
		got, res := dec.resolveTenant(tidB.String(), memberOfA, nil)
		if res.found || !got.IsZero() {
			t.Fatalf("F-01: member of A hinted B and it was accepted (tenant=%q found=%v); "+
				"cross-tenant governance policy applies to a non-member", got, res.found)
		}
	})

	t.Run("hint to own tenant is honored", func(t *testing.T) {
		got, res := dec.resolveTenant(tidA.String(), memberOfA, nil)
		if !res.found || got != tidA {
			t.Fatalf("member of A hinting A must resolve to A: tenant=%q found=%v reason=%q", got, res.found, res.reason)
		}
	})

	t.Run("superadmin may hint any configured tenant", func(t *testing.T) {
		got, res := dec.resolveTenant(tidB.String(), superadmin, nil)
		if !res.found || got != tidB {
			t.Fatalf("superadmin hinting B must resolve to B: tenant=%q found=%v reason=%q", got, res.found, res.reason)
		}
	})

	t.Run("hint with a failed authentication is denied", func(t *testing.T) {
		got, res := dec.resolveTenant(tidA.String(), memberOfA, errors.New("bad token"))
		if res.found || !got.IsZero() {
			t.Fatalf("F-01: hint accepted despite authErr!=nil (tenant=%q found=%v)", got, res.found)
		}
	})
}
