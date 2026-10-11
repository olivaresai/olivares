// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestPasswordResetEndsAccountSessionsAndManagedAuthority(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			f := newOwnerRefreshFixture(t, engine)
			token := f.mint(t, f.owner, "live")
			sibling, _, err := f.a.Login(t.Context(), f.user.Email, "renewal-password", "test")
			if err != nil {
				t.Fatal(err)
			}
			siblingOwner, err := f.a.Authenticate(t.Context(), sibling)
			if err != nil {
				t.Fatal(err)
			}
			siblingToken := f.mint(t, siblingOwner, "sibling")
			other, err := f.a.CreateUser(t.Context(), f.admin, auth.NewUser{Email: "other-reset@example.invalid", Password: "other-password", Tenant: f.scope.TenantID, Role: auth.RoleEditor})
			if err != nil {
				t.Fatal(err)
			}
			otherBearer, _, err := f.a.Login(t.Context(), other.Email, "other-password", "test")
			if err != nil {
				t.Fatal(err)
			}
			otherOwner, err := f.a.Authenticate(t.Context(), otherBearer)
			if err != nil {
				t.Fatal(err)
			}
			otherToken := f.mint(t, otherOwner, "other")
			if err = f.a.SetPassword(t.Context(), f.admin, f.user.ID, "replacement-password"); err != nil {
				t.Fatal(err)
			}
			for _, run := range []string{"live", "sibling"} {
				scope, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, run)
				if !errors.Is(err, auth.ErrSessionAccessEnded) || scope.RunRef != run || scope.WorkspaceID != f.scope.WorkspaceID {
					t.Fatalf("reset retained owner authority for %s: %v", run, err)
				}
			}
			for _, bearer := range []string{f.bearer, sibling} {
				if _, err := f.a.Authenticate(t.Context(), bearer); !errors.Is(err, auth.ErrUnauthenticated) {
					t.Fatalf("old login survived reset: %v", err)
				}
			}
			for _, bearer := range []string{token, siblingToken} {
				if _, _, err := f.issuer.Resolve(t.Context(), bearer); !errors.Is(err, auth.ErrUnauthenticated) {
					t.Fatalf("managed bearer survived reset: %v", err)
				}
			}
			if _, _, err := f.a.RefreshSession(t.Context(), f.owner); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("reset login renewed: %v", err)
			}
			if _, _, err := f.a.Login(t.Context(), f.user.Email, "renewal-password", "test"); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("retired password logged in: %v", err)
			}
			if _, _, err := f.a.Login(t.Context(), f.user.Email, "replacement-password", "test"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.a.Authenticate(t.Context(), otherBearer); err != nil {
				t.Fatalf("reset affected other account: %v", err)
			}
			if _, _, err := f.issuer.Resolve(t.Context(), otherToken); err != nil {
				t.Fatalf("reset affected other account run: %v", err)
			}
			if _, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, "other"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The real native transaction and repositories remain in use. Only this reset's
// audit result is controlled: append failure or the documented degrade result.
type passwordResetAuditStore struct {
	store.Store
	appendError error
	drop        bool
}

func (s *passwordResetAuditStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(passwordChangeScope{AuthScope: as, AuthUserAuthorityWriter: as.(store.AuthUserAuthorityWriter), AuthPrincipalEvidenceScope: as.(store.AuthPrincipalEvidenceScope), TransactionLocker: as.(store.TransactionLocker), audit: passwordResetAudit{AuditLog: as.Audit(), owner: s}})
	})
}

type passwordResetAudit struct {
	store.AuditLog
	owner *passwordResetAuditStore
}

func (a passwordResetAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if draft.Action == "user.set_password" {
		if a.owner.drop {
			return model.AuditEvent{}, nil
		}
		if a.owner.appendError != nil {
			// Persist provisionally first, so rollback must remove the event too.
			if _, err := a.AuditLog.Append(ctx, draft); err != nil {
				return model.AuditEvent{}, err
			}
			return model.AuditEvent{}, a.owner.appendError
		}
	}
	return a.AuditLog.Append(ctx, draft)
}

func TestPasswordResetAuditFailureRollsBack(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			for _, cause := range []string{"append-error", "zero-sequence"} {
				t.Run(cause, func(t *testing.T) {
					f := newOwnerRefreshFixture(t, engine)
					st := &passwordResetAuditStore{Store: f.st.Store}
					f.st.Store = st
					token := f.mint(t, f.owner, "live")
					sibling, _, err := f.a.Login(t.Context(), f.user.Email, "renewal-password", "test")
					if err != nil {
						t.Fatal(err)
					}
					siblingOwner, err := f.a.Authenticate(t.Context(), sibling)
					if err != nil {
						t.Fatal(err)
					}
					siblingToken := f.mint(t, siblingOwner, "sibling")
					var before model.User
					var sessions []model.AuthSession
					var head store.HeadRef
					if err := st.AuthView(t.Context(), func(as store.AuthScope) error {
						var err error
						before, err = as.Users().Get(t.Context(), f.user.ID)
						if err != nil {
							return err
						}
						sessions, _, err = as.Sessions().List(t.Context(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.user.ID.String()}}, Limit: 100})
						if err != nil {
							return err
						}
						head, _, err = as.Audit().Head(t.Context())
						return err
					}); err != nil {
						t.Fatal(err)
					}
					failure := errors.New("injected reset audit append failure")
					want := failure
					if cause == "zero-sequence" {
						st.drop = true
						want = store.ErrDirectoryUnavailable
					} else {
						st.appendError = failure
					}
					if err := f.a.SetPassword(t.Context(), f.admin, f.user.ID, "replacement-password"); !errors.Is(err, want) {
						t.Fatalf("reset without persisted audit = %v, want %v", err, want)
					}
					st.drop, st.appendError = false, nil
					if err := st.AuthView(t.Context(), func(as store.AuthScope) error {
						after, err := as.Users().Get(t.Context(), f.user.ID)
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(after, before) {
							t.Fatal("audit failure committed password/account mutation")
						}
						rows, _, err := as.Sessions().List(t.Context(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.user.ID.String()}}, Limit: 100})
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(rows, sessions) {
							t.Fatal("audit failure committed native session mutation")
						}
						afterHead, _, err := as.Audit().Head(t.Context())
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(afterHead, head) {
							t.Fatal("audit failure committed audit mutation")
						}
						return as.Audit().Walk(t.Context(), 0, func(event model.AuditEvent) error {
							if event.Action == "user.set_password" {
								t.Fatal("failed reset committed success audit")
							}
							return nil
						})
					}); err != nil {
						t.Fatal(err)
					}
					for _, bearer := range []string{f.bearer, sibling} {
						if _, err := f.a.Authenticate(t.Context(), bearer); err != nil {
							t.Fatalf("audit failure revoked login: %v", err)
						}
					}
					for _, bearer := range []string{token, siblingToken} {
						if _, _, err := f.issuer.Resolve(t.Context(), bearer); err != nil {
							t.Fatalf("audit failure revoked managed bearer: %v", err)
						}
					}
					for _, run := range []string{"live", "sibling"} {
						if _, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.scope.TenantID, run); err != nil {
							t.Fatalf("audit failure ended live owner authority: %v", err)
						}
					}
					if err := f.a.SetPassword(t.Context(), f.admin, f.user.ID, "replacement-password"); err != nil {
						t.Fatal(err)
					}
					subject, err := f.admin.AttributableActor()
					if err != nil {
						t.Fatal(err)
					}
					count := 0
					if err := st.AuthView(t.Context(), func(as store.AuthScope) error {
						return as.Audit().Walk(t.Context(), 0, func(event model.AuditEvent) error {
							if event.Action != "user.set_password" {
								return nil
							}
							count++
							if event.Seq <= 0 || event.Actor != subject || event.ActorKind != f.admin.ActorKind() || event.TargetKind != "core.user" || event.TargetID != f.user.ID {
								t.Fatal("persisted reset lost action, actor or target provenance")
							}
							return nil
						})
					}); err != nil {
						t.Fatal(err)
					}
					if count != 1 {
						t.Fatalf("persisted reset events=%d", count)
					}
				})
			}
		})
	}
}

func TestPasswordResetRollbackAndRetiredLoginProof(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			t.Run("rollback", func(t *testing.T) {
				f := newOwnerRefreshFixture(t, engine)
				token := f.mint(t, f.owner, "live")
				f.st.fail = true
				if err := f.a.SetPassword(t.Context(), f.admin, f.user.ID, "replacement-password"); err == nil {
					t.Fatal("reset ignored native transaction failure")
				}
				f.st.fail = false
				if _, err := f.a.Authenticate(t.Context(), f.bearer); err != nil {
					t.Fatal("failed reset revoked login")
				}
				if _, _, err := f.issuer.Resolve(t.Context(), token); err != nil {
					t.Fatal("failed reset ended managed authority")
				}
				if _, _, err := f.a.Login(t.Context(), f.user.Email, "renewal-password", "test"); err != nil {
					t.Fatal("failed reset changed password")
				}
				if _, _, err := f.a.Login(t.Context(), f.user.Email, "replacement-password", "test"); err == nil {
					t.Fatal("failed reset committed new password")
				}
				if err := f.st.AuthView(t.Context(), func(as store.AuthScope) error {
					return as.Audit().Walk(t.Context(), 0, func(event model.AuditEvent) error {
						if event.Action == "user.set_password" {
							t.Fatal("failed reset committed success audit")
						}
						return nil
					})
				}); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("retired-password-proof", func(t *testing.T) {
				factory := testStore
				if engine == store.EnginePostgres {
					factory = passwordChangePostgresStore
				}
				st, a, p, _, _ := passwordChangeFixture(t, factory)
				st.before = func() {
					if err := a.SetPassword(context.Background(), p, p.UserID, "replacement-secret"); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := a.Login(t.Context(), "password@example.test", "current-secret", "test"); !errors.Is(err, auth.ErrInvalidCredentials) {
					t.Fatalf("retired pre-reset password proof issued login: %v", err)
				}
			})
		})
	}
}
