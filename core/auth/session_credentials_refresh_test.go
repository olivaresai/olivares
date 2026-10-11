// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type ownerRefreshStore struct {
	store.Store
	fail bool
}

func (s *ownerRefreshStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := fn(as); err != nil {
			return err
		}
		if s.fail {
			return errors.New("injected renewal rollback")
		}
		return nil
	})
}

type ownerRefreshFixture struct {
	st      *ownerRefreshStore
	a       *auth.Authenticator
	clock   *stepClock
	owner   auth.Principal
	bearer  string
	admin   auth.Principal
	user    model.User
	scope   auth.SessionScope
	issuer  *auth.SessionCredentials
	invalid map[string]bool
}

func newOwnerRefreshFixture(t *testing.T, engine store.Engine) *ownerRefreshFixture {
	t.Helper()
	st := &ownerRefreshStore{Store: openScopeStore(t, engine)}
	clock := &stepClock{t: time.Now().UTC().Truncate(time.Millisecond)}
	a := auth.NewAuthenticator(st, clock)
	admin := mustSuperadmin(t, t.Context(), a)
	tenant := provisionTenant(t, st, "owner-refresh")
	user, err := a.CreateUser(t.Context(), admin, auth.NewUser{Email: "renewal@example.invalid", Password: "renewal-password", Tenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	bearer, _, err := a.Login(t.Context(), user.Email, "renewal-password", "test")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Authenticate(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	f := &ownerRefreshFixture{st: st, a: a, clock: clock, owner: owner, bearer: bearer, admin: admin, user: user, invalid: make(map[string]bool), scope: auth.SessionScope{TenantID: tenant, WorkspaceID: model.NewID(), FolderRef: "folder", SessionRef: "session", RunRef: "live", Holder: "holder", Fence: 7, AllowedTools: []string{"work"}, StopEpoch: "original"}}
	f.issuer = auth.NewSessionCredentials(a, func(_ context.Context, s auth.SessionScope) error {
		if f.invalid[s.RunRef] {
			return auth.ErrUnauthenticated
		}
		return nil
	})
	return f
}
func (f *ownerRefreshFixture) mint(t *testing.T, owner auth.Principal, run string) string {
	t.Helper()
	scope := f.scope
	scope.RunRef = run
	tok, err := f.issuer.Mint(t.Context(), owner, scope)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
func (f *ownerRefreshFixture) renew(t *testing.T) {
	t.Helper()
	old := f.bearer
	fresh, row, err := f.a.RefreshSession(t.Context(), f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != f.owner.CredID || row.ExpiresAt.Time().Sub(f.clock.Now().Time()) != 12*time.Hour {
		t.Fatal("refresh changed credential identity or default TTL")
	}
	if _, err = f.a.Authenticate(t.Context(), old); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old bearer: %v", err)
	}
	f.bearer = fresh
	f.owner, err = f.a.Authenticate(t.Context(), fresh)
	if err != nil {
		t.Fatal(err)
	}
}
func TestManagedSessionCredentialRefreshPreservesExactLiveAuthority(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			f := newOwnerRefreshFixture(t, engine)
			ctx := t.Context()
			live := f.mint(t, f.owner, "live")
			before, scope, err := f.issuer.Resolve(ctx, live)
			if err != nil {
				t.Fatal(err)
			}
			staleRef, _ := f.owner.Ref()
			q, _ := auth.QueuedCredentialFrom(f.owner)
			q, err = f.a.BindQueuedCredential(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := f.a.Login(ctx, f.user.Email, "renewal-password", "sibling")
			if err != nil {
				t.Fatal(err)
			}
			otherOwner, err := f.a.Authenticate(ctx, other)
			if err != nil {
				t.Fatal(err)
			}
			sibling := f.mint(t, otherOwner, "sibling")
			stopped := f.mint(t, f.owner, "stopped")
			failed := f.mint(t, f.owner, "failed")
			revoked := f.mint(t, f.owner, "revoked")
			f.invalid["stopped"] = true
			f.invalid["failed"] = true
			f.issuer.Revoke(f.scope.TenantID, "revoked")
			// A later role promotion cannot widen the captured launch ceiling.
			if err = f.st.AuthMutate(ctx, func(as store.AuthScope) error {
				rows, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.user.ID.String()}}, Limit: 10})
				if err != nil {
					return err
				}
				for _, row := range rows {
					if row.TargetTenantID == f.scope.TenantID {
						row.Role = auth.RoleOwner
						_, err = as.Memberships().Update(ctx, row)
						return err
					}
				}
				return store.ErrNotFound
			}); err != nil {
				t.Fatal(err)
			}
			for cycle := 0; cycle < 2; cycle++ {
				f.renew(t)
				after, got, err := f.issuer.Resolve(ctx, live)
				if err != nil || !reflect.DeepEqual(got, scope) || !reflect.DeepEqual(after, before) {
					t.Fatalf("ordinary refresh lost or widened live managed authority: %v", err)
				}
				if _, _, err = f.issuer.CheckOwnerAccess(ctx, f.scope.TenantID, "live"); err != nil {
					t.Fatalf("refresh became owner withdrawal: %v", err)
				}
				if _, _, err = f.issuer.Resolve(ctx, sibling); err != nil {
					t.Fatalf("same-user sibling affected: %v", err)
				}
			}
			deadline, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if _, err = f.a.ResolvePrincipalScope(deadline, staleRef, f.scope.TenantID); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("old arbitrary PrincipalRef: %v", err)
			}
			if _, err = f.a.RevalidateQueuedCredential(ctx, q); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("old queued credential: %v", err)
			}
			for _, token := range []string{stopped, failed, revoked} {
				if _, _, err = f.issuer.Resolve(ctx, token); err == nil {
					t.Fatal("retired work resurrected")
				}
			}
			f.invalid["stopped"] = false
			f.invalid["failed"] = false
			for _, token := range []string{stopped, failed, revoked} {
				if _, _, err = f.issuer.Resolve(ctx, token); err == nil {
					t.Fatal("retired generation resumed")
				}
			}
			if err = f.a.RevokeSession(ctx, f.owner, f.owner.CredID); err != nil {
				t.Fatal(err)
			}
			if _, _, err = f.issuer.CheckOwnerAccess(ctx, f.scope.TenantID, "live"); !errors.Is(err, auth.ErrSessionAccessEnded) {
				t.Fatalf("revoke did not end renewed owner: %v", err)
			}
			if _, _, err = f.issuer.Resolve(ctx, live); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("revoke left bearer usable: %v", err)
			}
			if _, _, err = f.issuer.Resolve(ctx, sibling); err != nil {
				t.Fatalf("revocation crossed same-user credentials: %v", err)
			}
		})
	}
}
func TestManagedSessionCredentialRefreshRollbackAndConcurrentUse(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			f := newOwnerRefreshFixture(t, engine)
			token := f.mint(t, f.owner, "live")
			f.st.fail = true
			if _, _, err := f.a.RefreshSession(t.Context(), f.owner); err == nil {
				t.Fatal("injected rollback committed")
			}
			f.st.fail = false
			if _, err := f.a.Authenticate(t.Context(), f.bearer); err != nil {
				t.Fatalf("rollback changed bearer: %v", err)
			}
			if _, _, err := f.issuer.Resolve(t.Context(), token); err != nil {
				t.Fatalf("rollback published binding: %v", err)
			}
			// Two requests authenticated the same old revision. Only one can rotate it.
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, _, err := f.a.RefreshSession(t.Context(), f.owner); results <- err }()
			}
			wg.Wait()
			close(results)
			wins := 0
			for err := range results {
				if err == nil {
					wins++
				} else if !errors.Is(err, auth.ErrUnauthenticated) {
					t.Fatalf("concurrent refresh: %v", err)
				}
			}
			if wins != 1 {
				t.Fatalf("concurrent rotations: %d winners", wins)
			}
			if _, _, err := f.issuer.Resolve(t.Context(), token); err != nil {
				t.Fatalf("concurrent renewal lost binding: %v", err)
			}
		})
	}
}
func TestManagedSessionCredentialRenewalDoesNotExtendBearerLifetime(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			f := newOwnerRefreshFixture(t, engine)
			token := f.mint(t, f.owner, "live")
			for cycle := 0; cycle < 2; cycle++ {
				f.clock.advance(8 * time.Hour)
				f.renew(t)
			}
			f.clock.advance(8*time.Hour - time.Millisecond)
			f.renew(t)
			if _, _, err := f.issuer.Resolve(t.Context(), token); err != nil {
				t.Fatalf("managed bearer expired before its exact boundary: %v", err)
			}
			f.clock.advance(time.Millisecond)
			// The launcher revision is current, so only the original 24h managed
			// bearer deadline can refuse this exact boundary.
			if _, err := f.a.Authenticate(t.Context(), f.bearer); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.issuer.Resolve(t.Context(), token); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("24-hour managed bearer boundary changed: %v", err)
			}
		})
	}
}

func TestManagedSessionCredentialRenewalKeepsWithdrawalAndExpiryClosed(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			for _, cause := range []string{"expired", "logout", "password-change", "membership-removed", "disabled"} {
				t.Run(cause, func(t *testing.T) {
					f := newOwnerRefreshFixture(t, engine)
					token := f.mint(t, f.owner, "live")
					f.renew(t)
					switch cause {
					case "expired":
						f.clock.advance(12 * time.Hour)
					case "logout":
						if err := f.a.RevokeSession(t.Context(), f.owner, f.owner.CredID); err != nil {
							t.Fatal(err)
						}
					case "password-change":
						sibling, _, err := f.a.Login(t.Context(), f.user.Email, "renewal-password", "password change")
						if err != nil {
							t.Fatal(err)
						}
						p, err := f.a.Authenticate(t.Context(), sibling)
						if err != nil {
							t.Fatal(err)
						}
						if err = f.a.ChangeOwnPassword(t.Context(), p, "renewal-password", "changed-password", "test", nil); err != nil {
							t.Fatal(err)
						}
					case "membership-removed":
						if err := f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
							rows, _, err := as.Memberships().List(t.Context(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.user.ID.String()}}, Limit: 10})
							if err != nil {
								return err
							}
							for _, row := range rows {
								if row.TargetTenantID == f.scope.TenantID {
									return as.Memberships().Delete(t.Context(), row.ID)
								}
							}
							return store.ErrNotFound
						}); err != nil {
							t.Fatal(err)
						}
					case "disabled":
						if err := f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
							row, err := as.Users().Get(t.Context(), f.user.ID)
							if err != nil {
								return err
							}
							row.Status = model.StatusInactive
							_, err = as.Users().Update(t.Context(), row)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					if _, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "live"); !errors.Is(err, auth.ErrSessionAccessEnded) {
						t.Fatalf("%s did not end renewed owner: %v", cause, err)
					}
					if _, _, err := f.issuer.Resolve(t.Context(), token); err == nil {
						t.Fatalf("%s retained authority", cause)
					}
					if cause != "membership-removed" {
						if _, _, err := f.a.RefreshSession(t.Context(), f.owner); !errors.Is(err, auth.ErrUnauthenticated) {
							t.Fatalf("%s revived credential: %v", cause, err)
						}
					}
				})
			}
		})
	}
}
