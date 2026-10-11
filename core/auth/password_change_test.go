// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Hooks surround the real transaction; they never replace its repositories or
// commit/rollback semantics. A withdrawal happens after password verification
// but before the password-change transaction starts.
type passwordChangeStore struct {
	store.Store
	before     func()
	auditError error
	dropAudit  bool
}

func (s *passwordChangeStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	if before := s.before; before != nil {
		s.before = nil
		before()
	}
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(passwordChangeScope{AuthScope: as, AuthUserAuthorityWriter: as.(store.AuthUserAuthorityWriter), AuthPrincipalEvidenceScope: as.(store.AuthPrincipalEvidenceScope), TransactionLocker: as.(store.TransactionLocker), audit: passwordChangeAudit{AuditLog: as.Audit(), owner: s}})
	})
}

type passwordChangeScope struct {
	store.AuthScope
	store.AuthUserAuthorityWriter
	store.AuthPrincipalEvidenceScope
	store.TransactionLocker
	audit store.AuditLog
}

func (s passwordChangeScope) Audit() store.AuditLog { return s.audit }

type passwordChangeAudit struct {
	store.AuditLog
	owner *passwordChangeStore
}

func (a passwordChangeAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if draft.Action == "account.password.changed" {
		if a.owner.auditError != nil {
			return model.AuditEvent{}, a.owner.auditError
		}
		if a.owner.dropAudit {
			return model.AuditEvent{}, nil
		}
	}
	return a.AuditLog.Append(ctx, draft)
}

func passwordChangeFixture(t *testing.T, factory func(*testing.T) store.Store) (*passwordChangeStore, *auth.Authenticator, auth.Principal, string, string) {
	t.Helper()
	st := &passwordChangeStore{Store: factory(t)}
	a := auth.NewAuthenticator(st, nil)
	ctx := context.Background()
	if _, err := a.BootstrapSuperadmin(ctx, "password@example.test", "current-secret"); err != nil {
		t.Fatal(err)
	}
	current, _, err := a.Login(ctx, "password@example.test", "current-secret", "peer")
	if err != nil {
		t.Fatal(err)
	}
	sibling, _, err := a.Login(ctx, "password@example.test", "current-secret", "peer")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	return st, a, p, current, sibling
}

func TestAccountPasswordTransactionalRevalidation(t *testing.T) {
	testPasswordChangeRevalidation(t, testStore)
}

func TestAccountPasswordTransactionalRevalidationPostgres(t *testing.T) {
	testPasswordChangeRevalidation(t, passwordChangePostgresStore)
}

func testPasswordChangeRevalidation(t *testing.T, factory func(*testing.T) store.Store) {
	for _, name := range []string{"revoked", "expired", "disabled", "rotated", "password-changed"} {
		t.Run(name, func(t *testing.T) {
			st, a, p, _, _ := passwordChangeFixture(t, factory)
			ctx := context.Background()
			st.before = func() {
				if err := st.Store.AuthMutate(ctx, func(as store.AuthScope) error {
					if name == "disabled" || name == "password-changed" {
						u, err := as.Users().Get(ctx, p.UserID)
						if err != nil {
							return err
						}
						if name == "disabled" {
							u.Status = model.StatusInactive
						} else {
							u.PasswordHash, err = auth.HashPassword("external-change")
							if err != nil {
								return err
							}
						}
						_, err = as.Users().Update(ctx, u)
						return err
					}
					session, err := as.Sessions().Get(ctx, p.CredID)
					if err != nil {
						return err
					}
					switch name {
					case "revoked":
						session.Revoked = true
					case "expired":
						session.ExpiresAt = model.NewTimestamp(time.Now().Add(-time.Second))
					case "rotated":
						session.Selector = "rotated-selector"
					}
					_, err = as.Sessions().Update(ctx, session)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := a.ChangeOwnPassword(ctx, p, "current-secret", "replacement-secret", "peer", nil)
			if err == nil {
				t.Fatal("stale password/session proof changed the password")
			}
			if err := st.Store.AuthView(ctx, func(as store.AuthScope) error {
				u, err := as.Users().Get(ctx, p.UserID)
				if err != nil {
					return err
				}
				if match, _ := auth.VerifyPassword("replacement-secret", u.PasswordHash); match {
					t.Fatal("refused change wrote a password")
				}
				return as.Audit().Walk(ctx, 0, func(event model.AuditEvent) error {
					if event.Action == "account.password.changed" {
						t.Fatal("refused change recorded success")
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("synthetic-or-altered-principal", func(t *testing.T) {
		_, a, p, _, _ := passwordChangeFixture(t, factory)
		for _, actor := range []auth.Principal{{Kind: auth.KindUser, UserID: p.UserID, CredID: p.CredID}, func() auth.Principal { p.UserID = model.NewID(); return p }()} {
			if err := a.ChangeOwnPassword(context.Background(), actor, "current-secret", "replacement-secret", "peer", nil); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("unbound/altered caller = %v", err)
			}
		}
	})
	t.Run("identity-provider-password", func(t *testing.T) {
		st, a, p, _, _ := passwordChangeFixture(t, factory)
		if err := st.Store.AuthMutate(context.Background(), func(as store.AuthScope) error {
			u, err := as.Users().Get(context.Background(), p.UserID)
			if err != nil {
				return err
			}
			u.PasswordHash = ""
			_, err = as.Users().Update(context.Background(), u)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.ChangeOwnPassword(context.Background(), p, "current-secret", "replacement-secret", "peer", nil); !errors.Is(err, auth.ErrNoLocalPassword) {
			t.Fatalf("IdP password change = %v", err)
		}
	})
}

func TestAccountPasswordAuditRollback(t *testing.T) {
	testPasswordChangeAuditRollback(t, testStore)
}

func TestAccountPasswordAuditRollbackPostgres(t *testing.T) {
	testPasswordChangeAuditRollback(t, passwordChangePostgresStore)
}

func testPasswordChangeAuditRollback(t *testing.T, factory func(*testing.T) store.Store) {
	for _, dropped := range []bool{false, true} {
		t.Run(map[bool]string{false: "append-error", true: "dropped-evidence"}[dropped], func(t *testing.T) {
			st, a, p, current, sibling := passwordChangeFixture(t, factory)
			st.dropAudit = dropped
			if !dropped {
				st.auditError = errors.New("audit unavailable")
			}
			if err := a.ChangeOwnPassword(context.Background(), p, "current-secret", "replacement-secret", "peer", nil); err == nil {
				t.Fatal("unaudited password change succeeded")
			}
			for _, token := range []string{current, sibling} {
				if _, err := a.Authenticate(context.Background(), token); err != nil {
					t.Fatal("audit failure committed session revocation")
				}
			}
			if _, _, err := a.Login(context.Background(), "password@example.test", "current-secret", "peer"); err != nil {
				t.Fatalf("audit failure committed password update: %v", err)
			}
		})
	}
}

func TestAccountPasswordRejectsLoginVerifiedBeforeChange(t *testing.T) {
	testPasswordChangeLateLogin(t, testStore)
}

func TestAccountPasswordRejectsLoginVerifiedBeforeChangePostgres(t *testing.T) {
	testPasswordChangeLateLogin(t, passwordChangePostgresStore)
}

func testPasswordChangeLateLogin(t *testing.T, factory func(*testing.T) store.Store) {
	st, a, p, current, _ := passwordChangeFixture(t, factory)
	st.before = func() {
		if err := a.ChangeOwnPassword(context.Background(), p, "current-secret", "replacement-secret", "peer", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := a.Login(context.Background(), "password@example.test", "current-secret", "peer"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("a login that verified the retired password before the change was not refused: %v", err)
	}
	if _, err := a.Authenticate(context.Background(), current); err != nil {
		t.Fatalf("the changing session ended: %v", err)
	}
}

func passwordChangePostgresStore(t *testing.T) store.Store {
	t.Helper()
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var privileged bool
	if err := db.QueryRow(`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil {
		t.Fatal(err)
	}
	if privileged {
		t.Fatal("password-change controls must use the unprivileged application role")
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAccountPasswordRejectsRetiredMFAPasswordProof(t *testing.T) {
	testPasswordChangePendingMFA(t, testStore)
}

func TestAccountPasswordRejectsRetiredMFAPasswordProofPostgres(t *testing.T) {
	testPasswordChangePendingMFA(t, passwordChangePostgresStore)
}

type passwordChangeClock struct{ now time.Time }

func (c *passwordChangeClock) Now() model.Timestamp { return model.NewTimestamp(c.now) }

func testPasswordChangePendingMFA(t *testing.T, factory func(*testing.T) store.Store) {
	for _, method := range []string{"totp", "recovery", "forced-enrolment"} {
		t.Run(method, func(t *testing.T) {
			st, _, _, current, sibling := passwordChangeFixture(t, factory)
			ctx := context.Background()
			clock := &passwordChangeClock{now: time.Now()}
			a := auth.NewAuthenticator(st, clock).WithTOTPSeedSealer(newTOTPTestSealer(t))
			p, err := a.Authenticate(ctx, current)
			if err != nil {
				t.Fatal(err)
			}
			var codes []string
			var seed string
			if method == "forced-enrolment" {
				if err := a.SetRequireTOTPForAdmins(ctx, p, true); err != nil {
					t.Fatal(err)
				}
			} else {
				enrol, err := a.BeginTOTPEnrolment(ctx, p, "Olivares AI")
				if err != nil {
					t.Fatal(err)
				}
				seed = enrol.Secret
				codes, err = a.FinishTOTPEnrolment(ctx, p, totpCodeFor(t, seed, clock.now))
				if err != nil {
					t.Fatal(err)
				}
				clock.now = clock.now.Add(31 * time.Second)
			}
			pending, err := a.LoginFrom(ctx, "password@example.test", "current-secret", "peer", nil)
			if err != nil || !pending.RequiresMFA() {
				t.Fatal("old password did not establish an MFA challenge")
			}
			if method == "forced-enrolment" {
				enrol, err := a.BeginTOTPEnrolmentForLogin(ctx, pending.MFAToken, "Olivares AI")
				if err != nil {
					t.Fatal(err)
				}
				seed = enrol.Secret
			}
			if err := a.ChangeOwnPassword(ctx, p, "current-secret", "replacement-secret", "peer", nil); err != nil {
				t.Fatal(err)
			}
			if method == "forced-enrolment" {
				_, _, _, err = a.FinishTOTPEnrolmentForLogin(ctx, pending.MFAToken, totpCodeFor(t, seed, clock.now), "peer", nil)
			} else {
				code := totpCodeFor(t, seed, clock.now)
				if method == "recovery" {
					code = codes[0]
				}
				_, _, err = a.CompleteTOTPLogin(ctx, pending.MFAToken, code, "peer", nil)
			}
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("retired password's %s proof completed: %v", method, err)
			}
			if _, err := a.Authenticate(ctx, current); err != nil {
				t.Fatal("the changing session ended")
			}
			if _, err := a.Authenticate(ctx, sibling); err == nil {
				t.Fatal("a sibling session survived")
			}
			fresh, err := a.LoginFrom(ctx, "password@example.test", "replacement-secret", "peer", nil)
			if err != nil || !fresh.RequiresMFA() {
				t.Fatal("new password lost its factor requirement")
			}
			if method == "forced-enrolment" {
				status, err := a.TOTPStatusOf(ctx, p.UserID)
				if err != nil || status.Enrolled {
					t.Fatal("refused old proof activated a factor")
				}
				enrol, err := a.BeginTOTPEnrolmentForLogin(ctx, fresh.MFAToken, "Olivares AI")
				if err != nil {
					t.Fatal(err)
				}
				_, _, _, err = a.FinishTOTPEnrolmentForLogin(ctx, fresh.MFAToken, totpCodeFor(t, enrol.Secret, clock.now), "peer", nil)
			} else {
				code := totpCodeFor(t, seed, clock.now)
				if method == "recovery" {
					code = codes[0]
				}
				_, _, err = a.CompleteTOTPLogin(ctx, fresh.MFAToken, code, "peer", nil)
			}
			if err != nil {
				t.Fatalf("new password could not complete %s without reusing a spent factor: %v", method, err)
			}
		})
	}
}
