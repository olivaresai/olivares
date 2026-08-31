// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// The seat seam (seatcap.go) after B10 (2026-07-27): self-hosted user accounts are
// UNLIMITED in every tier — Community, Business, add-ons and Enterprise alike — so
// nothing in this binary may refuse an account for seat reasons
// (an internal design note (not shipped) `self_hosted.users: unlimited`,
// an internal design note (not shipped) §B10).
//
// The seam itself is retained as a compatibility no-op, which is exactly what these
// tests pin: creation is driven through the real CreateUser / OnboardMember store
// transactions with the community policy wired, with NO policy wired, and with a
// policy that deliberately claims a tiny finite limit — every one of them must admit
// accounts without end. A regression that re-introduces a cap fails here.

// stubSeatPolicy reports an exact figure. Since B10 it can only affect the SeatLimit
// DISPLAY accessor; it must never be able to refuse an account.
type stubSeatPolicy struct {
	limit int
	ok    bool
}

func (s stubSeatPolicy) MaxActiveUsers() (int, bool) { return s.limit, s.ok }

func bootstrapAdmin(t *testing.T, a *auth.Authenticator) {
	t.Helper()
	if _, err := a.BootstrapSuperadmin(context.Background(), "admin@acme.test", "supersecret-pw"); err != nil {
		t.Fatalf("bootstrap superadmin: %v", err)
	}
}

// fillAccounts creates n additional accounts and fails on the first refusal, naming
// the account that was refused (a re-introduced cap shows up as "account #4 refused").
func fillAccounts(t *testing.T, a *auth.Authenticator, n int) {
	t.Helper()
	ctx, actor := context.Background(), fedTestActor()
	for i := 0; i < n; i++ {
		if _, err := a.CreateUser(ctx, actor, auth.NewUser{
			Email: fmt.Sprintf("u%d@acme.test", i), DisplayName: "U",
		}); err != nil {
			t.Fatalf("account #%d must be admitted (accounts are unlimited): %v", i+2, err)
		}
	}
}

func TestSeatCap_CommunityPolicyIsUnlimited(t *testing.T) {
	// The constant is pinned at 0 = unlimited. It is NOT deleted: the closed
	// enterprise overlay (separate repository) reads it as its no-license fallback,
	// so 0 is what stops a LAPSED license from degrading a deployment to 3 seats.
	if auth.CommunitySeatLimit != 0 {
		t.Fatalf("CommunitySeatLimit = %d, want 0 (unlimited — B10 removed the cap of 3)", auth.CommunitySeatLimit)
	}
	if limit, ok := auth.NewCommunitySeatPolicy().MaxActiveUsers(); ok || limit > 0 {
		t.Fatalf("community policy MaxActiveUsers = (%d,%v), want unlimited (0,false)", limit, ok)
	}
}

func TestSeatCap_Community_UnlimitedWithoutLicense(t *testing.T) {
	// The community binary's exact wiring (cmd/olivares/wire_noenterprise.go) and no
	// license anywhere: the old cap refused the 4th account, so go well past it.
	a := auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	bootstrapAdmin(t, a) // active #1
	fillAccounts(t, a, 24)

	count, capped, err := a.ActiveUserCount(context.Background(), 1000)
	if err != nil {
		t.Fatalf("ActiveUserCount: %v", err)
	}
	if count != 25 || capped {
		t.Fatalf("active users = (%d, capped=%v), want (25,false)", count, capped)
	}
	// And the deployment still reports no enforced limit.
	if limit, ok := a.SeatLimit(); ok || limit > 0 {
		t.Fatalf("SeatLimit = (%d,%v), want unlimited (0,false)", limit, ok)
	}
}

func TestSeatCap_Community_HighAccountCount(t *testing.T) {
	// A deliberately high number: there is no threshold at which creation starts to
	// fail, so nothing between 3 and "a real estate" may be special-cased.
	a := auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	bootstrapAdmin(t, a)
	fillAccounts(t, a, 499)

	count, capped, err := a.ActiveUserCount(context.Background(), 10000)
	if err != nil {
		t.Fatalf("ActiveUserCount: %v", err)
	}
	if count != 500 || capped {
		t.Fatalf("active users = (%d, capped=%v), want (500,false)", count, capped)
	}
}

func TestSeatCap_Community_OnboardMemberUnlimited(t *testing.T) {
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	ctx, actor := context.Background(), fedTestActor()
	bootstrapAdmin(t, a) // account 1 (superadmin)
	tenant := provisionTenant(t, st, "acme")

	// The other account-creating path (invite/JIT onboarding), also far past 3.
	for i := 0; i < 20; i++ {
		if _, err := a.OnboardMember(ctx, actor, tenant, auth.OnboardInput{
			Email: fmt.Sprintf("m%d@acme.test", i), DisplayName: "M", Role: auth.RoleViewer, Password: "password-1x",
		}); err != nil {
			t.Fatalf("onboarding member %d must be admitted: %v", i, err)
		}
	}
	// Re-onboarding an EXISTING account still just (re)grants the membership.
	if _, err := a.OnboardMember(ctx, actor, tenant, auth.OnboardInput{
		Email: "m0@acme.test", DisplayName: "M", Role: auth.RoleEditor, Password: "password-1x",
	}); err != nil {
		t.Fatalf("re-onboarding an existing account: %v", err)
	}
}

func TestSeatCap_NilPolicy_Unlimited(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil) // no seat policy (library/test embedder)
	bootstrapAdmin(t, a)
	fillAccounts(t, a, 6)
}

func TestSeatCap_FinitePolicyNeverGates(t *testing.T) {
	// THE B10 invariant: even a policy that claims a finite limit — the shape the
	// closed enterprise overlay injects, and the shape a lapsed license used to
	// produce — cannot refuse an account. The seam is a no-op; seats NEVER gate
	// runtime (PRICING-CANON.md). Its figure only reaches the display accessor.
	a := auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(stubSeatPolicy{limit: 3, ok: true})
	bootstrapAdmin(t, a) // active #1, already "at" the old cap boundary after 2 more
	fillAccounts(t, a, 9)

	if limit, ok := a.SeatLimit(); !ok || limit != 3 {
		t.Fatalf("SeatLimit = (%d,%v), want the advertised (3,true) — display only", limit, ok)
	}
}

func TestSeatCap_ZeroWithOKIsReportedAsUnlimited(t *testing.T) {
	// A policy saying "the limit is 0 and it applies" must NOT reach the wire as
	// seat_limited=true / seat_limit=0 — a client would read that as "no account may
	// exist", the exact opposite of the truth. SeatLimit normalizes every
	// non-positive figure to the single unlimited spelling (0,false).
	a := auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(stubSeatPolicy{limit: 0, ok: true})
	if limit, ok := a.SeatLimit(); ok || limit != 0 {
		t.Fatalf("SeatLimit for (0,true) = (%d,%v), want the normalized unlimited (0,false)", limit, ok)
	}
	a = auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(stubSeatPolicy{limit: -5, ok: true})
	if limit, ok := a.SeatLimit(); ok || limit != 0 {
		t.Fatalf("SeatLimit for (-5,true) = (%d,%v), want (0,false)", limit, ok)
	}
}

func TestSeatCap_ProvisioningPathsWithoutTheSeamAreUnlimitedToo(t *testing.T) {
	// SCIM provisioning writes to the store DIRECTLY (core/auth/scim.go:93) and never
	// went through the seat seam — so the old cap of 3 was already bypassable by any
	// deployment with SCIM (or federated JIT, core/auth/federation_login.go:180)
	// wired. That is further evidence the cap was a commercial boundary and never a
	// security control; this pins that those paths stay unlimited after B10 too.
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	ctx := context.Background()
	bootstrapAdmin(t, a)
	tenant := provisionTenant(t, st, "acme")
	super := mustTestOperator("admin")
	for i := 0; i < 12; i++ {
		if _, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
			UserName: fmt.Sprintf("s%d@acme.test", i), Active: true,
		}); err != nil {
			t.Fatalf("SCIM provisioning #%d must be admitted: %v", i+1, err)
		}
	}
	count, _, err := a.ActiveUserCount(ctx, 1000)
	if err != nil {
		t.Fatalf("ActiveUserCount: %v", err)
	}
	if count != 13 {
		t.Fatalf("active users = %d, want 13 (superadmin + 12 SCIM)", count)
	}
}

func TestSeatCap_UnlimitedEntitlement(t *testing.T) {
	// A policy that declines to set a finite figure (ok=false) is unlimited, which is
	// what an attested MaxUsers of 0 — the only figure issued since B10 — produces.
	a := auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(stubSeatPolicy{ok: false})
	bootstrapAdmin(t, a)
	fillAccounts(t, a, 6)
}
