// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These tests cross the real Authenticator/Store interface. The only scheduling
// seam removes a real membership AFTER the actual successful preflight has
// returned, BEFORE the lifecycle transaction starts; it never fabricates a row.
type scimGuardStore struct {
	store.Store
	preflightOK   bool
	beforeWrite   func()
	onMemberRead  func()
	prepareErr    error
	membershipErr error
	auditErr      error
}

func (s *scimGuardStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	err := s.Store.AuthView(ctx, fn)
	s.preflightOK = err == nil
	return err
}

func (s *scimGuardStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(&scimGuardScope{AuthScope: as, onMemberRead: s.onMemberRead,
			prepareErr: s.prepareErr, membershipErr: s.membershipErr, auditErr: s.auditErr})
	})
}

type scimGuardScope struct {
	store.AuthScope
	onMemberRead  func()
	prepareErr    error
	membershipErr error
	auditErr      error
}

func (s *scimGuardScope) PrepareUserAuthorityWrite(ctx context.Context, ids []model.ID) error {
	if s.prepareErr != nil {
		return s.prepareErr
	}
	w, ok := s.AuthScope.(store.AuthUserAuthorityWriter)
	if !ok {
		return store.ErrDirectoryUnavailable
	}
	return w.PrepareUserAuthorityWrite(ctx, ids)
}

func (s *scimGuardScope) Memberships() store.Repository[model.Membership] {
	return &scimGuardMembers{Repository: s.AuthScope.Memberships(), beforeList: s.onMemberRead, listErr: s.membershipErr}
}

func (s *scimGuardScope) Audit() store.AuditLog {
	return &scimGuardAudit{AuditLog: s.AuthScope.Audit(), appendErr: s.auditErr}
}

type scimGuardAudit struct {
	store.AuditLog
	appendErr error
}

func (a *scimGuardAudit) Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	if a.appendErr != nil {
		return model.AuditEvent{}, a.appendErr
	}
	return a.AuditLog.Append(ctx, d)
}

type scimGuardMembers struct {
	store.Repository[model.Membership]
	beforeList func()
	listErr    error
}

func (r *scimGuardMembers) List(ctx context.Context, q model.Query) ([]model.Membership, model.Page, error) {
	if r.beforeList != nil {
		r.beforeList()
	}
	if r.listErr != nil {
		return nil, model.Page{}, r.listErr
	}
	return r.Repository.List(ctx, q)
}

type scimGuardFixture struct {
	ctx           context.Context
	raw           store.Store
	observer      *sql.DB
	engine        store.Engine
	tenant, other model.TenantID
	actor         auth.Principal
	sessionTokens map[model.ID]string
}

func newSCIMGuardFixture(t *testing.T, engine store.Engine) *scimGuardFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	f := &scimGuardFixture{ctx: ctx, engine: engine, sessionTokens: make(map[model.ID]string),
		actor: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true},
	}
	if engine == store.EnginePostgres {
		// Real isolated database with owner/app/admin split and multiple app
		// connections; the observer is a separate connection, not a fake store.
		f.raw, f.observer = openLoginCapabilityPostgres(t)
	} else {
		dsn := filepath.Join(t.TempDir(), "scim-membership.db")
		var err error
		f.raw, err = sqlstore.Open(ctx, store.Config{Engine: engine, DSN: dsn}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.raw.Close() })
		f.observer, err = sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		f.observer.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = f.observer.Close() })
		if _, err := f.observer.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
			t.Fatal(err)
		}
	}
	f.tenant = provisionTenant(t, f.raw, "scim-guard")
	f.other = provisionTenant(t, f.raw, "scim-guard-other")
	return f
}

func (f *scimGuardFixture) seed(t *testing.T, member, otherMember bool) model.User {
	t.Helper()
	var u model.User
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		u, err = as.Users().Create(f.ctx, model.User{
			Email: model.NewID().String() + "@example.test", DisplayName: "Original", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.credentials(t, u.ID)
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		for _, target := range []struct {
			id     model.TenantID
			member bool
		}{{f.tenant, member}, {f.other, otherMember}} {
			if target.member {
				if _, err := as.Memberships().Create(f.ctx, model.Membership{UserID: u.ID, TargetTenantID: target.id, Role: auth.RoleViewer}); err != nil {
					return err
				}
			}
			// A stale group row is possible without a direct membership. DELETE
			// of an absent SCIM resource must not silently mutate that row either.
			g, err := as.Groups().Create(f.ctx, model.UserGroup{TargetTenantID: target.id, DisplayName: "Guard"})
			if err != nil {
				return err
			}
			if _, err := as.GroupMembers().Create(f.ctx, model.UserGroupMember{UserID: u.ID, GroupID: g.ID}); err != nil {
				return err
			}
			parent, err := as.Tokens().Create(f.ctx, model.APIToken{UserID: u.ID, Name: "parent", Selector: model.NewID().String(), SecretHash: []byte("fixture"), BoundTenantID: target.id, Role: auth.RoleViewer})
			if err != nil {
				return err
			}
			if _, err := as.Tokens().Create(f.ctx, model.APIToken{UserID: u.ID, Name: "child", Selector: model.NewID().String(), SecretHash: []byte("fixture"), BoundTenantID: target.id, ParentTokenID: parent.ID, Role: auth.RoleViewer}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return u
}

func (f *scimGuardFixture) credentials(t *testing.T, id model.ID) {
	t.Helper()
	credential, err := auth.NewCredential(auth.PrefixSession)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		if _, err := as.Sessions().Create(f.ctx, model.AuthSession{UserID: id, Selector: credential.Selector, SecretHash: credential.SecretHash, ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour))}); err != nil {
			return err
		}
		_, err := as.WebAuthnCredentials().Create(f.ctx, model.WebAuthnCredential{UserID: id, CredentialID: model.NewID().String(), Credential: []byte(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.sessionTokens[id] = credential.Token
}

type scimGuardState struct {
	user              model.User
	members           []model.Membership
	groups            []model.UserGroupMember
	sessions          []model.AuthSession
	tokens            []model.APIToken
	credentials       []model.WebAuthnCredential
	audit             store.HeadRef
	auditPresent      bool
	authority         store.UserAuthorityFactRef
	epoch, otherEpoch store.AuthorizationFactRef
}

func scimGuardRows[T any](t *testing.T, ctx context.Context, repo store.ReadRepository[T], id model.ID) []T {
	t.Helper()
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: id.String()}}, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *scimGuardFixture) state(t *testing.T, id model.ID) scimGuardState {
	t.Helper()
	var s scimGuardState
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		var err error
		if s.user, err = as.Users().Get(f.ctx, id); err != nil {
			return err
		}
		s.members = scimGuardRows(t, f.ctx, as.Memberships(), id)
		s.groups = scimGuardRows(t, f.ctx, as.GroupMembers(), id)
		s.sessions = scimGuardRows(t, f.ctx, as.Sessions(), id)
		s.tokens = scimGuardRows(t, f.ctx, as.Tokens(), id)
		s.credentials = scimGuardRows(t, f.ctx, as.WebAuthnCredentials(), id)
		if s.audit, s.auditPresent, err = as.Audit().Head(f.ctx); err != nil {
			return err
		}
		if s.authority, err = as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(f.ctx, id); err != nil {
			return err
		}
		e := as.(store.AuthPrincipalEvidenceScope)
		if s.epoch, err = e.ReadDirectoryEpochFact(f.ctx, f.tenant); err != nil {
			return err
		}
		s.otherEpoch, err = e.ReadDirectoryEpochFact(f.ctx, f.other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *scimGuardFixture) removeMembership(t *testing.T, id model.ID) {
	t.Helper()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		rows := scimGuardRows(t, f.ctx, as.Memberships(), id)
		for _, m := range rows {
			if m.TargetTenantID == f.tenant {
				return as.Memberships().Delete(f.ctx, m.ID)
			}
		}
		return store.ErrNotFound
	}); err != nil {
		t.Fatalf("remove real membership at preflight barrier: %v", err)
	}
}

func scimGuardWrite(ctx context.Context, a *auth.Authenticator, f *scimGuardFixture, id model.ID, method string, active bool) error {
	switch method {
	case "update":
		_, err := a.SCIMUpdateUser(ctx, f.actor, f.tenant, id, auth.SCIMUserInput{DisplayName: "Changed", Active: active})
		return err
	case "set":
		return a.SCIMSetMemberActive(ctx, f.actor, f.tenant, id, active)
	default:
		return a.SCIMDeprovisionUser(ctx, f.actor, f.tenant, id)
	}
}

func TestSCIMMembershipGuardRemovedAfterPreflight(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			for _, method := range []string{"update", "set"} {
				t.Run(method, func(t *testing.T) {
					u := f.seed(t, true, true)
					wrapped := &scimGuardStore{Store: f.raw}
					var before scimGuardState
					removed := false
					wrapped.beforeWrite = func() {
						if !wrapped.preflightOK {
							t.Fatal("target mutation reached without a real successful preflight")
						}
						f.removeMembership(t, u.ID)
						removed = true
						before = f.state(t, u.ID)
					}
					err := scimGuardWrite(f.ctx, auth.NewAuthenticator(wrapped, nil), f, u.ID, method, false)
					if !removed {
						t.Fatal("preflight-removal barrier was not crossed")
					}
					if !errors.Is(err, store.ErrNotFound) {
						t.Fatalf("write after membership removal = %v; want ErrNotFound", err)
					}
					if after := f.state(t, u.ID); !reflect.DeepEqual(before, after) {
						t.Fatal("refused write changed account, credentials, membership, groups, authority or audit")
					}
				})
			}
		})
	}
}

func TestSCIMMembershipGuardAbsentDelete(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			for _, repeated := range []bool{false, true} {
				name := "never-member"
				if repeated {
					name = "repeated-after-independent-reactivation"
				}
				t.Run(name, func(t *testing.T) {
					u := f.seed(t, repeated, false)
					a := auth.NewAuthenticator(f.raw, nil)
					if repeated {
						if err := a.SCIMDeprovisionUser(f.ctx, f.actor, f.tenant, u.ID); err != nil {
							t.Fatal(err)
						}
						// A different authorized lifecycle may reactivate the now
						// unbound account. Replaying the old tenant DELETE grants
						// no authority over its newly issued credentials.
						if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
							current, err := as.Users().Get(f.ctx, u.ID)
							if err != nil {
								return err
							}
							current.Status = model.StatusActive
							_, err = as.Users().Update(f.ctx, current)
							return err
						}); err != nil {
							t.Fatal(err)
						}
						f.credentials(t, u.ID)
					}
					if principal, err := a.Authenticate(f.ctx, f.sessionTokens[u.ID]); err != nil || principal.UserID != u.ID {
						t.Fatalf("absent-member control session cannot authenticate: %v", err)
					}
					before := f.state(t, u.ID)
					if len(before.members) != 0 || before.user.Status != model.StatusActive || len(before.credentials) == 0 {
						t.Fatal("absent-member control lacks an active unbound account or credential")
					}
					live := false
					for _, s := range before.sessions {
						live = live || (!s.Revoked && s.ExpiresAt.Time().After(time.Now()))
					}
					if !live {
						t.Fatal("absent-member control lacks a live session")
					}
					if err := a.SCIMDeprovisionUser(f.ctx, f.actor, f.tenant, u.ID); err != nil {
						t.Fatal(err)
					}
					if after := f.state(t, u.ID); !reflect.DeepEqual(before, after) {
						t.Fatal("absent DELETE changed account, credentials, groups, versions or audit")
					}
					if err := a.SCIMDeprovisionUser(f.ctx, f.actor, f.tenant, model.NewID()); err != nil {
						t.Fatalf("unknown-user DELETE = %v; want idempotent no-op", err)
					}
					if after := f.state(t, u.ID); !reflect.DeepEqual(before, after) {
						t.Fatal("unknown-user DELETE changed audit or directory state")
					}
				})
			}
		})
	}
}

func TestSCIMMembershipGuardAuthorizedLifecycle(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			a := auth.NewAuthenticator(f.raw, nil)
			for _, method := range []string{"update", "set"} {
				t.Run(method, func(t *testing.T) {
					u := f.seed(t, true, true)
					if err := scimGuardWrite(f.ctx, a, f, u.ID, method, true); err != nil {
						t.Fatal(err)
					}
					before := f.state(t, u.ID)
					if method == "update" && before.user.DisplayName != "Changed" {
						t.Fatal("authorized attribute update did not apply")
					}
					if err := scimGuardWrite(f.ctx, a, f, u.ID, method, false); err != nil {
						t.Fatal(err)
					}
					disabled := f.state(t, u.ID)
					if disabled.user.Status != model.StatusInactive || !reflect.DeepEqual(before.members, disabled.members) || !reflect.DeepEqual(before.groups, disabled.groups) || !reflect.DeepEqual(before.credentials, disabled.credentials) {
						t.Fatal("disable must preserve membership, groups and registered credentials")
					}
					for _, s := range disabled.sessions {
						if !s.Revoked {
							t.Fatal("disable retained a live session")
						}
					}
					for _, tok := range disabled.tokens {
						if tok.Revoked != (tok.BoundTenantID == f.tenant) {
							t.Fatal("disable revoked the wrong tenant's token tree")
						}
					}
					if err := scimGuardWrite(f.ctx, a, f, u.ID, method, true); err != nil {
						t.Fatal(err)
					}
					after := f.state(t, u.ID)
					if after.user.Status != model.StatusActive || !reflect.DeepEqual(disabled.sessions, after.sessions) || !reflect.DeepEqual(disabled.tokens, after.tokens) {
						t.Fatal("reactivation must restore status without reviving revoked credentials")
					}
				})
			}
			for _, otherMember := range []bool{false, true} {
				name := "orphan"
				if otherMember {
					name = "other-tenant-member"
				}
				t.Run(name, func(t *testing.T) {
					u := f.seed(t, true, otherMember)
					before := f.state(t, u.ID)
					if err := a.SCIMDeprovisionUser(f.ctx, f.actor, f.tenant, u.ID); err != nil {
						t.Fatal(err)
					}
					after := f.state(t, u.ID)
					if (after.user.Status == model.StatusActive) != otherMember {
						t.Fatal("deprovision orphan status is wrong")
					}
					for _, m := range after.members {
						if m.TargetTenantID != f.other {
							t.Fatal("departed membership survived")
						}
					}
					if (len(after.members) == 1) != otherMember || len(after.groups) != 1 {
						t.Fatal("deprovision removed or retained the wrong membership/group")
					}
					for _, g := range after.groups {
						if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
							group, err := as.Groups().Get(f.ctx, g.GroupID)
							if err == nil && group.TargetTenantID != f.other {
								t.Error("remaining group belongs to departed tenant")
							}
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					for _, s := range after.sessions {
						if s.Revoked == otherMember {
							t.Fatal("deprovision revoked the wrong sessions")
						}
					}
					for _, tok := range after.tokens {
						if tok.Revoked != (tok.BoundTenantID == f.tenant) {
							t.Fatal("deprovision changed the wrong tenant token tree")
						}
					}
					if otherMember {
						if !reflect.DeepEqual(before.credentials, after.credentials) || before.authority != after.authority {
							t.Fatal("non-orphan departure changed global credentials/authority")
						}
					} else if len(after.credentials) != 0 {
						t.Fatal("orphan retained WebAuthn credentials")
					}
					if err := a.SCIMDeprovisionUser(f.ctx, f.actor, f.tenant, u.ID); err != nil {
						t.Fatal(err)
					}
					if again := f.state(t, u.ID); !reflect.DeepEqual(after, again) {
						t.Fatal("repeated DELETE changed committed departure state")
					}
				})
			}
		})
	}
}

// probeWriter checks actual engine exclusion on a separate connection. Neither
// outcome depends on elapsed time: SQLite refuses a competing immediate writer;
// PostgreSQL refuses the exact transaction advisory key used by directory writers.
func (f *scimGuardFixture) probeWriter(t *testing.T, wantAvailable bool) {
	t.Helper()
	if f.engine == store.EnginePostgres {
		tx, err := f.observer.BeginTx(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var available bool
		if err := tx.QueryRowContext(f.ctx, "SELECT pg_catalog.pg_try_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))", "core.directory.writer").Scan(&available); err != nil {
			t.Fatal(err)
		}
		if available != wantAvailable {
			t.Fatalf("competing PG directory writer available=%t, want %t", available, wantAvailable)
		}
		return
	}
	conn, err := f.observer.Conn(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.ExecContext(f.ctx, "BEGIN IMMEDIATE")
	if err == nil {
		if _, rollbackErr := conn.ExecContext(f.ctx, "ROLLBACK"); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if !wantAvailable {
			t.Fatal("competing SQLite writer acquired reservation during membership use")
		}
		return
	}
	var coded interface{ Code() int }
	if wantAvailable || !errors.As(err, &coded) || coded.Code()&0xff != 5 {
		t.Fatalf("competing SQLite reservation = %v; want available=%t (SQLITE_BUSY when held)", err, wantAvailable)
	}
}

func TestSCIMMembershipGuardHoldsWriterAtMembershipRead(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			for _, method := range []string{"update", "set", "delete"} {
				t.Run(method, func(t *testing.T) {
					u := f.seed(t, true, false)
					reads := 0
					wrapped := &scimGuardStore{Store: f.raw, onMemberRead: func() {
						reads++
						f.probeWriter(t, false)
					}}
					f.probeWriter(t, true)
					if err := scimGuardWrite(f.ctx, auth.NewAuthenticator(wrapped, nil), f, u.ID, method, false); err != nil {
						t.Fatal(err)
					}
					if reads == 0 {
						t.Fatal("lifecycle did not corroborate membership inside mutation")
					}
					f.probeWriter(t, true)
				})
			}
		})
	}
}

func TestSCIMMembershipGuardFailureRollsBack(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			for _, method := range []string{"update", "set", "delete"} {
				for _, fault := range []string{"authority", "membership", "audit"} {
					t.Run(method+"/"+fault, func(t *testing.T) {
						u := f.seed(t, true, false)
						before := f.state(t, u.ID)
						refused := errors.New("SCIM guard fixture refusal")
						wrapped := &scimGuardStore{Store: f.raw}
						switch fault {
						case "authority":
							wrapped.prepareErr = refused
						case "membership":
							wrapped.membershipErr = refused
						case "audit":
							wrapped.auditErr = refused
						}
						err := scimGuardWrite(f.ctx, auth.NewAuthenticator(wrapped, nil), f, u.ID, method, false)
						if !errors.Is(err, refused) {
							t.Fatalf("%s refusal = %v; want injected error", fault, err)
						}
						if after := f.state(t, u.ID); !reflect.DeepEqual(before, after) {
							t.Fatal("failed lifecycle committed partial user, credential, directory or audit effects")
						}
					})
				}
			}
		})
	}
}
