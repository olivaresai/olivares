// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionCredentialExpiryIsTypedAndFencedAtOriginalDeadline(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			for _, path := range []string{"idle-check", "resolve", "idle-owner-read-unavailable", "idle-parent-renewed-at-deadline", "idle-parent-revoked-at-deadline"} {
				t.Run(path, func(t *testing.T) {
					f := newOwnerRefreshFixture(t, engine)
					observed := &ownerAccessReadStore{Store: f.st.Store}
					f.st.Store = observed
					notices := 0
					f.issuer = auth.NewSessionCredentials(f.a, func(context.Context, auth.SessionScope) error { return nil }, func(context.Context, auth.SessionScope, string) error { notices++; return nil })
					token := f.mint(t, f.owner, "live")
					for range 2 {
						f.clock.advance(8 * time.Hour)
						f.renew(t)
					}
					f.clock.advance(8*time.Hour - time.Millisecond)
					if _, _, err := f.issuer.Resolve(t.Context(), token); err != nil {
						t.Fatalf("bearer denied before original deadline: %v", err)
					}
					if _, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "live"); err != nil {
						t.Fatal(err)
					}
					current := f.mint(t, f.owner, "current")
					f.clock.advance(time.Millisecond)
					if _, err := f.a.Authenticate(t.Context(), f.bearer); err != nil {
						t.Fatalf("fixture owner is not current at child expiry: %v", err)
					}
					if path == "idle-parent-renewed-at-deadline" || path == "idle-parent-revoked-at-deadline" {
						f.renew(t)
					}
					if path == "idle-parent-revoked-at-deadline" {
						if err := f.a.RevokeSession(t.Context(), f.owner, f.owner.CredID); err != nil {
							t.Fatal(err)
						}
						if _, scope, err := f.issuer.Resolve(t.Context(), token); !errors.Is(err, auth.ErrUnauthenticated) || scope.RunRef != "live" {
							t.Fatalf("expired withdrawn bearer granted authority: %v", err)
						}
						scope, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "live")
						if !errors.Is(err, auth.ErrSessionAccessEnded) || scope.Fence != f.scope.Fence {
							t.Fatalf("expiry hid proven revoked owner: %v", err)
						}
						return
					}
					if path == "idle-owner-read-unavailable" {
						observed.fail.Store(true)
					}
					var err error
					if path != "resolve" {
						_, _, err = f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "live")
					} else {
						_, _, err = f.issuer.Resolve(t.Context(), token)
					}
					if !errors.Is(err, auth.ErrSessionCredentialExpired) || !errors.Is(err, auth.ErrUnauthenticated) || err == nil || !strings.Contains(err.Error(), "Session credential expired after 24 hours. Start a successor session.") {
						t.Fatalf("exact24h did not report typed credential expiry: %v", err)
					}
					if errors.Is(err, auth.ErrSessionAccessEnded) {
						t.Fatal("expiry misclassified an owner read as withdrawal")
					}
					observed.fail.Store(false)
					if _, _, err = f.issuer.ResolveRun(t.Context(), f.scope.TenantID, "live"); err == nil {
						t.Fatal("expired run retained in-process authority")
					}
					if path == "resolve" && notices != 1 {
						t.Fatalf("expiry notifications=%d, want1", notices)
					}
					if _, _, err = f.issuer.Resolve(t.Context(), current); err != nil {
						t.Fatalf("expiry affected another current run: %v", err)
					}
					if _, _, err = f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "never-bound"); !errors.Is(err, auth.ErrSessionOwnerUnbound) {
						t.Fatalf("never-bound run became expiry: %v", err)
					}
					successorScope := f.scope
					successorScope.Fence++
					successor, err := f.issuer.Mint(t.Context(), f.owner, successorScope)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err = f.issuer.Resolve(t.Context(), token); err == nil {
						t.Fatal("old bearer revived on successor mint")
					}
					if _, scope, err := f.issuer.Resolve(t.Context(), successor); err != nil || scope.Fence != successorScope.Fence {
						t.Fatalf("expired generation damaged successor: %v", err)
					}
					if err = f.a.RevokeSession(t.Context(), f.owner, f.owner.CredID); err != nil {
						t.Fatal(err)
					}
					if _, _, err = f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "current"); !errors.Is(err, auth.ErrSessionAccessEnded) {
						t.Fatalf("expiry hid real owner withdrawal: %v", err)
					}
				})
			}
		})
	}
}
