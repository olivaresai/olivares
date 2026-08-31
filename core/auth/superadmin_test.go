// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The internal-superadmin lifecycle (superadmin.go): enable/disable a global,
// system-path principal, deny-closed against TOTAL lockout, NON-destructive, with
// credentials cut on disable. A disabled superadmin no longer counts toward the
// active-seat cap (seatcap.go counts only ACTIVE accounts).

const testSuperPassword = "supersecret-pw"

func mustBootstrapSuper(t *testing.T, a *auth.Authenticator, email string) model.User {
	t.Helper()
	u, err := a.BootstrapSuperadmin(context.Background(), email, testSuperPassword)
	if err != nil {
		t.Fatalf("bootstrap superadmin %q: %v", email, err)
	}
	return u
}

func mustCreateSuper(t *testing.T, a *auth.Authenticator, email string) model.User {
	t.Helper()
	u, err := a.CreateUser(context.Background(), fedTestActor(), auth.NewUser{
		Email: email, DisplayName: "Super", Password: testSuperPassword, Superadmin: true,
	})
	if err != nil {
		t.Fatalf("create superadmin %q: %v", email, err)
	}
	return u
}

func TestSuperadmin_DisableLastActive_DenyClosed(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	admin := mustBootstrapSuper(t, a, "only@acme.test")

	// The sole superadmin cannot be disabled — that would lock out the System path.
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), admin.ID, false); !errors.Is(err, auth.ErrLastSuperadmin) {
		t.Fatalf("disable last superadmin err = %v, want ErrLastSuperadmin", err)
	}
	// It is unchanged: still active.
	admins, err := a.ListSuperadmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 1 || admins[0].Status != model.StatusActive {
		t.Fatalf("after a refused disable, superadmin must stay active; got %+v", admins)
	}
}

func TestSuperadmin_DisableEnable_GuardIsDynamic(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	adminA := mustBootstrapSuper(t, a, "a@acme.test")
	adminB := mustCreateSuper(t, a, "b@acme.test")

	// With two active superadmins, disabling one is allowed.
	got, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, false)
	if err != nil {
		t.Fatalf("disable B (A still active): %v", err)
	}
	if got.Status != model.StatusInactive {
		t.Fatalf("B status = %q, want inactive", got.Status)
	}
	// Now A is the LAST active superadmin: disabling it is refused.
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminA.ID, false); !errors.Is(err, auth.ErrLastSuperadmin) {
		t.Fatalf("disable last active (A) err = %v, want ErrLastSuperadmin", err)
	}
	// Re-enable B; now A can be disabled (B is active again).
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, true); err != nil {
		t.Fatalf("enable B: %v", err)
	}
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminA.ID, false); err != nil {
		t.Fatalf("disable A once B is active again: %v", err)
	}
}

func TestSuperadmin_DisableRevokesUnboundToken(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	mustBootstrapSuper(t, a, "a@acme.test") // keeps the lockout guard satisfied
	adminB := mustCreateSuper(t, a, "b@acme.test")

	// B holds an UNBOUND superadmin token. authToken does NOT re-check user status,
	// so a mere status flip would leave it valid — only an explicit revoke cuts it.
	bActor := auth.Principal{Kind: auth.KindUser, UserID: adminB.ID, Superadmin: true}
	tok, _, err := a.IssueToken(ctx, bActor, auth.TokenSpec{Name: "b-sys", Superadmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, tok); err != nil {
		t.Fatalf("token must authenticate before disable: %v", err)
	}

	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, false); err != nil {
		t.Fatalf("disable B: %v", err)
	}
	if _, err := a.Authenticate(ctx, tok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled superadmin's unbound token still authenticates (err=%v) — revoke failed", err)
	}
	// Re-enabling restores the account but never revives a revoked credential.
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, true); err != nil {
		t.Fatalf("enable B: %v", err)
	}
	if _, err := a.Authenticate(ctx, tok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("re-enable revived a revoked token (err=%v)", err)
	}
}

func TestSuperadmin_DisableRevokesSessionAndBlocksLogin(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	mustBootstrapSuper(t, a, "a@acme.test")
	adminB := mustCreateSuper(t, a, "b@acme.test")

	sessTok, _, err := a.Login(ctx, "b@acme.test", testSuperPassword, "203.0.113.1")
	if err != nil {
		t.Fatalf("login B before disable: %v", err)
	}
	if _, err := a.Authenticate(ctx, sessTok); err != nil {
		t.Fatalf("session must authenticate before disable: %v", err)
	}

	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, false); err != nil {
		t.Fatalf("disable B: %v", err)
	}
	// Existing session is dead and a fresh login is refused (inactive account).
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled superadmin session still valid: %v", err)
	}
	if _, _, err := a.Login(ctx, "b@acme.test", testSuperPassword, "203.0.113.1"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("disabled superadmin login err = %v, want ErrInvalidCredentials", err)
	}

	// Re-enable: a NEW login works, but the OLD session stays revoked.
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, true); err != nil {
		t.Fatalf("enable B: %v", err)
	}
	if _, _, err := a.Login(ctx, "b@acme.test", testSuperPassword, "203.0.113.1"); err != nil {
		t.Fatalf("re-enabled superadmin must log in: %v", err)
	}
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("re-enable revived a revoked session (err=%v)", err)
	}
}

func TestSuperadmin_RejectsNonSuperadminTarget(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	mustBootstrapSuper(t, a, "a@acme.test")
	member, err := a.CreateUser(ctx, fedTestActor(), auth.NewUser{
		Email: "member@acme.test", DisplayName: "M", Password: testSuperPassword, Superadmin: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), member.ID, false); !errors.Is(err, auth.ErrNotSuperadmin) {
		t.Fatalf("disable non-superadmin err = %v, want ErrNotSuperadmin", err)
	}
}

func TestSuperadmin_DisableIsIdempotent(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	mustBootstrapSuper(t, a, "a@acme.test")
	adminB := mustCreateSuper(t, a, "b@acme.test")

	if _, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, false); err != nil {
		t.Fatalf("first disable: %v", err)
	}
	// A no-op re-disable of an already-inactive account must not error or trip the
	// lockout guard (it changes no active count).
	got, err := a.SetSuperadminActive(ctx, fedTestActor(), adminB.ID, false)
	if err != nil {
		t.Fatalf("idempotent re-disable: %v", err)
	}
	if got.Status != model.StatusInactive {
		t.Fatalf("re-disable status = %q, want inactive", got.Status)
	}
}

func TestSuperadmin_DisableLeavesActivePopulation(t *testing.T) {
	// Disabling a superadmin removes it from the ACTIVE population the console
	// reports (ActiveUserCount). Since B10 that population is a USAGE figure and
	// never a quota, so account creation is admitted before and after, whatever the
	// count — the community policy is wired here precisely to prove it.
	a := auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	ctx, actor := context.Background(), fedTestActor()
	mustBootstrapSuper(t, a, "a@acme.test") // active #1 (superadmin)
	adminB := mustCreateSuper(t, a, "b@acme.test")
	for i := 0; i < 4; i++ {
		if _, err := a.CreateUser(ctx, actor, auth.NewUser{
			Email: fmt.Sprintf("m%d@acme.test", i), DisplayName: "M", Password: testSuperPassword,
		}); err != nil {
			t.Fatalf("create member %d: %v", i, err)
		}
	}
	count, _, err := a.ActiveUserCount(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveUserCount: %v", err)
	}
	if count != 6 {
		t.Fatalf("active users = %d, want 6", count)
	}
	// Disabling B drops it from the active population, non-destructively.
	if _, err := a.SetSuperadminActive(ctx, actor, adminB.ID, false); err != nil {
		t.Fatalf("disable B: %v", err)
	}
	count, _, err = a.ActiveUserCount(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveUserCount after disable: %v", err)
	}
	if count != 5 {
		t.Fatalf("active users after disabling a superadmin = %d, want 5", count)
	}
	// Creation keeps working either way — no seat was ever being freed or consumed.
	if _, err := a.CreateUser(ctx, actor, auth.NewUser{Email: "more@acme.test", DisplayName: "X", Password: testSuperPassword}); err != nil {
		t.Fatalf("a new account must be admitted (accounts are unlimited): %v", err)
	}
}

func TestSuperadmin_ListSuperadminsExcludesMembers(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	ctx := context.Background()
	mustBootstrapSuper(t, a, "a@acme.test")
	mustCreateSuper(t, a, "b@acme.test")
	if _, err := a.CreateUser(ctx, fedTestActor(), auth.NewUser{
		Email: "member@acme.test", DisplayName: "M", Password: testSuperPassword, Superadmin: false,
	}); err != nil {
		t.Fatal(err)
	}
	admins, err := a.ListSuperadmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 2 {
		t.Fatalf("ListSuperadmins returned %d, want 2 (the member must be excluded)", len(admins))
	}
	for _, u := range admins {
		if !u.IsSuperadmin {
			t.Fatalf("non-superadmin %q in superadmin list", u.ID)
		}
	}
}
