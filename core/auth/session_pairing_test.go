// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionPairingOneTimeNamedAndRevocable(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	actor := mustSuperadmin(t, ctx, a)
	link, pending, err := a.CreateSessionPairing(ctx, actor, "  Work phone  ")
	if err != nil {
		t.Fatal(err)
	}
	if pending.DeviceName != "Work phone" || pending.PairingSourceID != actor.CredID || pending.ClientKind != model.SessionClientPairing {
		t.Fatalf("pending metadata: %#v", pending)
	}
	if _, err := a.Authenticate(ctx, link); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending authenticates: %v", err)
	}
	forged := strings.Replace(link, auth.PrefixPairing+"_", auth.PrefixSession+"_", 1)
	if _, err := a.Authenticate(ctx, forged); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("prefix swap authenticates pending grant: %v", err)
	}
	rows, err := a.ListOwnSessions(ctx, actor)
	if err != nil || len(rows) != 2 {
		t.Fatalf("inventory missing pending row: %d %v", len(rows), err)
	}
	credential, device, err := a.AcceptSessionPairing(ctx, link, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if device.ID != pending.ID || device.DeviceName != "Work phone" || !device.PairingSourceID.IsZero() || device.AAL != 1 {
		t.Fatalf("paired metadata: %#v", device)
	}
	if _, _, err := a.AcceptSessionPairing(ctx, link, "127.0.0.1"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("replay = %v", err)
	}
	if _, err := a.Authenticate(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeOwnSession(ctx, actor, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, credential); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("lost device retained access: %v", err)
	}
	if _, err := a.ListOwnSessions(ctx, actor); err != nil {
		t.Fatalf("original session affected: %v", err)
	}
}

func TestSessionPairingRefusesExpiredRevokedAndWithdrawnSources(t *testing.T) {
	for _, mode := range []string{"expired link", "revoked link", "revoked source", "disabled user", "network", "sso"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			clock := newStepClock()
			a := auth.NewAuthenticator(st, clock)
			actor := mustSuperadmin(t, ctx, a)
			link, pending, err := a.CreateSessionPairing(ctx, actor, "Phone")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expired link":
				clock.advance(10*time.Minute + time.Second)
			case "revoked link":
				if err := a.RevokeOwnSession(ctx, actor, pending.ID); err != nil {
					t.Fatal(err)
				}
			case "revoked source":
				if err := a.RevokeSession(ctx, actor, actor.CredID); err != nil {
					t.Fatal(err)
				}
			case "disabled user":
				if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
					u, e := as.Users().Get(ctx, actor.UserID)
					if e != nil {
						return e
					}
					u.Status = model.StatusInactive
					_, e = as.Users().Update(ctx, u)
					return e
				}); err != nil {
					t.Fatal(err)
				}
			case "network":
				a.WithLoginPolicy(&fakeLoginPolicy{networkErr: auth.ErrNetworkNotAllowed})
			case "sso":
				a.WithLoginPolicy(&fakeLoginPolicy{ssoErr: auth.ErrSSORequired})
			}
			if _, _, err := a.AcceptSessionPairing(ctx, link, "127.0.0.1"); err == nil {
				t.Fatal("withdrawn pairing accepted")
			}
		})
	}
}

func TestSessionPairingConcurrentAcceptance(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	actor := mustSuperadmin(t, ctx, a)
	link, _, err := a.CreateSessionPairing(ctx, actor, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := a.AcceptSessionPairing(ctx, link, "127.0.0.1"); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful redemptions=%d, want 1", successes)
	}
}

func TestSessionPairingKeepsScopeAndExclusions(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	userID, actor := mustMember(t, ctx, f.a, f.super, f.tA, "pairing@a.test", auth.RoleEditor)
	f.seedMembership(t, userID, f.tB, auth.RoleViewer)
	link, pending, err := f.a.CreateSessionPairing(ctx, actor, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	// A tenant ends account-session access after link creation. The redeemed
	// device inherits that exclusion instead of silently regaining access.
	if err := f.a.RevokeSessionsInTenant(ctx, f.super, f.tA, userID); err != nil {
		t.Fatal(err)
	}
	token, device, err := f.a.AcceptSessionPairing(ctx, link, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if pending.ID != device.ID {
		t.Fatal("pairing changed id")
	}
	principal, err := f.a.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.ExcludedFrom(f.tA) {
		t.Fatal("device regained excluded tenant")
	}
	if _, ok := principal.RoleIn(f.tB); !ok {
		t.Fatal("unrelated tenant lost")
	}
}

func TestSessionPairingValidatesNameAndLimit(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	actor := mustSuperadmin(t, ctx, a)
	for _, name := range []string{"", "  ", "bad\nname", strings.Repeat("x", 81)} {
		if _, _, err := a.CreateSessionPairing(ctx, actor, name); !errors.Is(err, auth.ErrPairingName) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
	for range 5 {
		if _, _, err := a.CreateSessionPairing(ctx, actor, "Phone"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := a.CreateSessionPairing(ctx, actor, "Sixth"); !errors.Is(err, auth.ErrPairingLimit) {
		t.Fatalf("pairing limit=%v", err)
	}
}

func TestSessionPairingScopedSource(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	userID, _ := mustMember(t, ctx, f.a, f.super, f.tA, "paired-scope@a.test", auth.RoleEditor)
	f.seedMembership(t, userID, f.tB, auth.RoleViewer)
	credential, err := auth.NewCredential(auth.PrefixScopedSession)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Sessions().Create(ctx, model.AuthSession{UserID: userID, Selector: credential.Selector, SecretHash: credential.SecretHash, ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)), TenantScope: f.tA, AAL: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source, err := f.a.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	link, _, err := f.a.CreateSessionPairing(ctx, source, "Scoped CLI")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := f.a.AcceptSessionPairing(ctx, link, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.a.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.SessionScope() != f.tA || session.TenantScope != f.tA || !strings.HasPrefix(token, auth.PrefixScopedSession+"_") {
		t.Fatal("pairing widened scope")
	}
	if _, ok := principal.RoleIn(f.tB); ok {
		t.Fatal("paired scoped device gained another tenant")
	}
}

func TestSessionPairingAbsentLoginComponentRefuses(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	actor := mustSuperadmin(t, ctx, a)
	link, _, err := a.CreateSessionPairing(ctx, actor, "Before policy changed")
	if err != nil {
		t.Fatal(err)
	}
	configureGlobalLoginPosture(t, st)
	recordLoginCapability(t, st)
	a.WithLoginComponentState(auth.LoginComponentAbsent)
	if _, _, err := a.CreateSessionPairing(ctx, actor, "New device"); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("create without component=%v", err)
	}
	if _, _, err := a.AcceptSessionPairing(ctx, link, "127.0.0.1"); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("accept without component=%v", err)
	}
}

func TestSessionPairingRefusesTenantEnteredAccountPrincipal(t *testing.T) {
	f := newScopeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ref, ok := f.super.Ref()
	if !ok {
		t.Fatal("missing source reference")
	}
	scoped, err := f.a.ResolvePrincipalScope(ctx, ref, f.tA)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.SessionScope() != f.tA {
		t.Fatal("fixture did not enter tenant scope")
	}
	if _, _, err := f.a.CreateSessionPairing(ctx, scoped, "Scoped administrator device"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("tenant-entered account principal minted an account-wide grant: %v", err)
	}
}
