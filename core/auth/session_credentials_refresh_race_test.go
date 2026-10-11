// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

type ownerRenewalCommitGate struct {
	store.Store
	armed     atomic.Bool
	committed chan struct{}
	release   chan struct{}
}

func (s *ownerRenewalCommitGate) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	wait := s.armed.CompareAndSwap(true, false)
	err := s.Store.AuthMutate(ctx, fn)
	if wait && err == nil {
		close(s.committed)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func TestManagedSessionCredentialRenewalCommitPublicationAndRevoke(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			for _, revoke := range []bool{false, true} {
				name := "reader"
				if revoke {
					name = "revoke"
				}
				t.Run(name, func(t *testing.T) {
					f := newOwnerRefreshFixture(t, engine)
					gate := &ownerRenewalCommitGate{Store: f.st, committed: make(chan struct{}), release: make(chan struct{})}
					f.a = auth.NewAuthenticator(gate, f.clock)
					f.issuer = auth.NewSessionCredentials(f.a, func(context.Context, auth.SessionScope) error { return nil })
					token := f.mint(t, f.owner, "live")
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					released := false
					defer func() {
						if !released {
							close(gate.release)
						}
					}()
					gate.armed.Store(true)
					refreshed := make(chan error, 1)
					go func() { _, _, err := f.a.RefreshSession(ctx, f.owner); refreshed <- err }()
					select {
					case <-gate.committed:
					case <-ctx.Done():
						t.Fatal("native renewal did not commit")
					}
					if revoke {
						if err := f.a.RevokeSession(ctx, f.owner, f.owner.CredID); err != nil {
							t.Fatal(err)
						}
					}
					// The native row is committed while publication is held. Readers must not
					// turn this temporary revision difference into a permanent owner stop.
					read := make(chan error, 1)
					go func() { _, _, err := f.issuer.Resolve(ctx, token); read <- err }()
					close(gate.release)
					released = true
					if err := <-refreshed; err != nil {
						t.Fatal(err)
					}
					err := <-read
					if revoke && !errors.Is(err, auth.ErrUnauthenticated) {
						t.Fatalf("concurrent revoke lost: %v", err)
					}
					if !revoke && err != nil {
						t.Fatalf("commit/publication gap poisoned live binding: %v", err)
					}
					_, _, err = f.issuer.CheckOwnerAccess(ctx, f.scope.TenantID, "live")
					if revoke && !errors.Is(err, auth.ErrSessionAccessEnded) {
						t.Fatalf("revoked run remained owned: %v", err)
					}
					if !revoke && err != nil {
						t.Fatalf("owner check lost renewal: %v", err)
					}
				})
			}
		})
	}
}

func TestManagedSessionCredentialRenewalRacingMintRetriesBeforeRotation(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			f := newOwnerRefreshFixture(t, engine)
			var armed atomic.Bool
			started, release := make(chan struct{}), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			f.issuer = auth.NewSessionCredentials(f.a, func(ctx context.Context, _ auth.SessionScope) error {
				if armed.CompareAndSwap(true, false) {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			})
			live := f.mint(t, f.owner, "live")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			armed.Store(true)
			done := make(chan error, 1)
			go func() { _, _, err := f.a.RefreshSession(ctx, f.owner); done <- err }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("renewal validation did not start")
			}
			raced := f.mint(t, f.owner, "new-live")
			close(release)
			released = true
			if err := <-done; !errors.Is(err, store.ErrConflict) {
				t.Fatalf("unvalidated new binding rotated: %v", err)
			}
			if _, err := f.a.Authenticate(ctx, f.bearer); err != nil {
				t.Fatalf("retry refusal changed old bearer: %v", err)
			}
			f.renew(t)
			for _, token := range []string{live, raced} {
				if _, _, err := f.issuer.Resolve(ctx, token); err != nil {
					t.Fatalf("retry lost current live binding: %v", err)
				}
			}
		})
	}
}

// Migration rotates the same native row but is not ordinary live-owner renewal.
func TestManagedSessionCredentialBrowserMigrationDoesNotRenewLiveAuthority(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			f := newOwnerRefreshFixture(t, engine)
			token := f.mint(t, f.owner, "live")
			next, _, err := f.a.MigrateBrowserSession(t.Context(), f.owner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.Authenticate(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			if _, _, err = f.issuer.Resolve(t.Context(), token); !errors.Is(err, auth.ErrSessionAccessEnded) {
				t.Fatalf("non-renewal rotation moved live authority: %v", err)
			}
		})
	}
}
