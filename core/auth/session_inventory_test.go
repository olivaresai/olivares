// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func readSession(t *testing.T, st store.Store, id model.ID) model.AuthSession {
	t.Helper()
	var got model.AuthSession
	if err := st.AuthView(context.Background(), func(as store.AuthScope) error {
		var err error
		got, err = as.Sessions().Get(context.Background(), id)
		return err
	}); err != nil {
		t.Fatalf("read session: %v", err)
	}
	return got
}

// TestSessionAuthenticationRecordsLastUse pins last-use tracking on the session
// path: an authentication stamps last_used_at and the observed transport, a busy
// session is written at most once a minute, and the stamp never changes the
// row's version or updated_at.
func TestSessionAuthenticationRecordsLastUse(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	if _, err := a.BootstrapSuperadmin(ctx, "root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatal(err)
	}
	token, minted, err := a.Login(ctx, "root@example.com", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	loginAt := clock.Now()
	if got := readSession(t, st, minted.ID); got.LastUsedAt == nil || !got.LastUsedAt.Time().Equal(loginAt.Time()) {
		t.Fatalf("a new session's last use = %v, want its creation %v", got.LastUsedAt, loginAt)
	}

	use := func(kind string) model.AuthSession {
		t.Helper()
		if _, err := a.Authenticate(auth.WithSessionClientKind(ctx, kind), token); err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		got := readSession(t, st, minted.ID)
		if got.Version != minted.Version || !got.UpdatedAt.Time().Equal(minted.UpdatedAt.Time()) {
			t.Fatalf("recording a use changed version/updated_at: %d/%v, was %d/%v", got.Version, got.UpdatedAt, minted.Version, minted.UpdatedAt)
		}
		return got
	}
	clock.advance(30 * time.Second)
	first := clock.Now()
	if got := use(model.SessionClientToken); !got.LastUsedAt.Time().Equal(first.Time()) || got.ClientKind != model.SessionClientToken {
		t.Fatalf("the first authentication = %v/%q, want %v/token", got.LastUsedAt, got.ClientKind, first)
	}
	clock.advance(30 * time.Second)
	if got := use(model.SessionClientToken); !got.LastUsedAt.Time().Equal(first.Time()) {
		t.Fatalf("a use inside the minute rewrote last use to %v, want %v", got.LastUsedAt, first)
	}
	clock.advance(31 * time.Second)
	if got := use(model.SessionClientToken); !got.LastUsedAt.Time().Equal(clock.Now().Time()) {
		t.Fatalf("a use past the minute left last use at %v, want %v", got.LastUsedAt, clock.Now())
	}
	clock.advance(time.Second)
	if got := use(model.SessionClientBrowser); got.ClientKind != model.SessionClientBrowser {
		t.Fatalf("the same session seen from a browser is %q, want browser", got.ClientKind)
	}
}

// TestOwnSessionsRefuseDelegatedPrincipals pins that only the person lists or
// ends their sessions: an agent acting for them carries their source session
// but is not them.
func TestOwnSessionsRefuseDelegatedPrincipals(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, newStepClock())
	person := mustSuperadmin(t, ctx, a)
	if sessions, err := a.ListOwnSessions(ctx, person); err != nil || len(sessions) != 1 {
		t.Fatalf("the person's own list = %d, %v, want 1", len(sessions), err)
	}
	for name, delegated := range map[string]auth.Principal{
		"agent":     person.WithAgentIdentity("agent-1"),
		"on behalf": person.WithActAs(person.UserID),
	} {
		if _, err := a.ListOwnSessions(ctx, delegated); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s listed the person's sessions: %v", name, err)
		}
		if err := a.RevokeOwnSession(ctx, delegated, model.NewID()); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s ended a session of the person: %v", name, err)
		}
	}
}

// TestListOwnSessionsHidesEndedAndExpired pins that the inventory lists live
// sign-ins only.
func TestListOwnSessionsHidesEndedAndExpired(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	if _, err := a.BootstrapSuperadmin(ctx, "root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatal(err)
	}
	login := func() (string, model.AuthSession) {
		t.Helper()
		token, row, err := a.Login(ctx, "root@example.com", "bootstrap-pass-123", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return token, row
	}
	tokenA, rowA := login()
	_, rowB := login()
	_, rowC := login()
	actor, err := a.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeOwnSession(ctx, actor, rowB.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err := a.ListOwnSessions(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[model.ID]bool{}
	for _, s := range sessions {
		ids[s.ID] = true
	}
	if len(ids) != 2 || !ids[rowA.ID] || !ids[rowC.ID] || ids[rowB.ID] {
		t.Fatalf("live list = %v, want A and C without the ended B", ids)
	}
	clock.advance(auth.DefaultSessionTTL + time.Second)
	tokenD, rowD := login()
	actorD, err := a.Authenticate(ctx, tokenD)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err = a.ListOwnSessions(ctx, actorD)
	if err != nil || len(sessions) != 1 || sessions[0].ID != rowD.ID {
		t.Fatalf("after expiry the list = %d, %v, want only the new sign-in", len(sessions), err)
	}
}

func TestOwnSessionsStayWithinFederatedSessionScope(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	userID, account := mustMember(t, ctx, f.a, f.super, f.tA, "sessions@a.test", auth.RoleEditor)
	f.seedMembership(t, userID, f.tB, auth.RoleViewer)
	var sessions []model.AuthSession
	var tokens []string
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		for _, tenant := range []model.TenantID{f.tA, f.tA, f.tB} {
			credential, err := auth.NewCredential(auth.PrefixScopedSession)
			if err != nil {
				return err
			}
			session, err := as.Sessions().Create(ctx, model.AuthSession{
				UserID: userID, Selector: credential.Selector, SecretHash: credential.SecretHash,
				ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)), TenantScope: tenant, AAL: 1,
			})
			if err != nil {
				return err
			}
			sessions = append(sessions, session)
			tokens = append(tokens, credential.Token)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	actor, err := f.a.Authenticate(ctx, tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	listed, err := f.a.ListOwnSessions(ctx, actor)
	if err != nil || len(listed) != 2 {
		t.Fatalf("tenant-scoped inventory = %d, %v, want the two sessions of that tenant", len(listed), err)
	}
	for _, session := range listed {
		if session.TenantScope != f.tA {
			t.Fatalf("tenant-scoped inventory exposed session in scope %q", session.TenantScope)
		}
	}
	for _, id := range []model.ID{account.CredID, sessions[2].ID} {
		if err := f.a.RevokeOwnSession(ctx, actor, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("tenant-scoped end outside its scope = %v, want not found", err)
		}
		if readSession(t, f.st, id).Revoked {
			t.Fatal("tenant-scoped end revoked a session outside its tenant")
		}
	}
	listed, err = f.a.ListOwnSessions(ctx, account)
	if err != nil || len(listed) != 4 {
		t.Fatalf("account inventory = %d, %v, want all four own sessions", len(listed), err)
	}
	if err := f.a.RevokeOwnSession(ctx, actor, sessions[1].ID); err != nil {
		t.Fatalf("end another session within the tenant scope: %v", err)
	}
	if !readSession(t, f.st, sessions[1].ID).Revoked {
		t.Fatal("the other session within the tenant scope survived")
	}
}

// A read-only transaction still resolves the real credential, while its real
// bookkeeping write fails with ErrReadOnly. Authentication must remain usable.
type sessionUseReadOnlyStore struct {
	store.Store
	writes *int
}

func (s sessionUseReadOnlyStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	*s.writes++
	return s.Store.AuthView(ctx, fn)
}
func TestSessionAuthenticationSurvivesUseWriteFailure(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	if _, err := a.BootstrapSuperadmin(ctx, "root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatal(err)
	}
	token, minted, err := a.Login(ctx, "root@example.com", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	writes := 0
	readonly := auth.NewAuthenticator(sessionUseReadOnlyStore{st, &writes}, clock)
	principal, err := readonly.Authenticate(ctx, token)
	if err != nil || principal.CredID != minted.ID {
		t.Fatalf("read-only authentication = %v / %v", principal, err)
	}
	if writes != 1 {
		t.Fatalf("bookkeeping attempts = %d, want 1", writes)
	}
	got := readSession(t, st, minted.ID)
	if got.LastUsedAt.String() != minted.LastUsedAt.String() {
		t.Fatal("read-only store changed last use")
	}
	if _, err := readonly.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("failed write was retried inside the minute: %d", writes)
	}
}

func TestAdminSessionEndRefusesInvalidScopesAndConfinedActors(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	member, _ := mustMember(t, ctx, f.a, f.super, f.tA, "guard-member@example.test", auth.RoleViewer)
	adminID, admin := mustMember(t, ctx, f.a, f.super, f.tA, "guard-admin@example.test", auth.RoleAdmin)
	for _, tenant := range []model.TenantID{"", model.SystemTenantID} {
		if err := f.a.RevokeSessionsInTenant(ctx, admin, tenant, member); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("invalid tenant %q = %v", tenant, err)
		}
	}
	f.seedMembership(t, f.super.UserID, f.tA, auth.RoleOwner)
	if err := f.a.RevokeSessionsInTenant(ctx, admin, f.tA, f.super.UserID); err == nil {
		t.Fatal("admin ended superadmin access")
	}
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: adminID.String()}}, Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TargetTenantID == f.tA {
				row.WorkspaceID = model.NewID()
				_, err = as.Memberships().Update(ctx, row)
				return err
			}
		}
		return store.ErrNotFound
	}); err != nil {
		t.Fatal(err)
	}
	confined := loginPrincipal(t, ctx, f.a, "guard-admin@example.test")
	if err := f.a.RevokeSessionsInTenant(ctx, confined, f.tA, member); !errors.Is(err, auth.ErrWorkspaceConfined) {
		t.Fatalf("confined admin = %v", err)
	}
}

// Model a directory writer that waits until the transaction context is cancelled.
// A busy writer must get a short bookkeeping budget, not the request's lifetime.
type sessionUseHeldWriter struct{ store.Store }

func (s sessionUseHeldWriter) AuthMutate(ctx context.Context, _ func(store.AuthScope) error) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestSessionAuthenticationBoundsUseWriterWait(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login(ctx, "root@example.com", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reader := auth.NewAuthenticator(sessionUseHeldWriter{st}, nil)
	start := time.Now()
	if _, err := reader.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("authentication with held bookkeeping writer: %s", elapsed)
	if elapsed > time.Second {
		t.Fatalf("bookkeeping blocked authentication for %s", elapsed)
	}
}

// Observe drafts at the append seam and still commit through the real ledger.
// Walk intentionally withholds metadata, so it cannot prove these counters.
type sessionAuditCaptureStore struct {
	store.Store
	meta map[string]any
}

func (s *sessionAuditCaptureStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(sessionAuditCaptureScope{AuthScope: as, AuthUserAuthorityWriter: as.(store.AuthUserAuthorityWriter), capture: s})
	})
}

type sessionAuditCaptureScope struct {
	store.AuthScope
	store.AuthUserAuthorityWriter
	capture *sessionAuditCaptureStore
}

func (s sessionAuditCaptureScope) Audit() store.AuditLog {
	return sessionAuditCaptureLog{s.AuthScope.Audit(), s.capture}
}

type sessionAuditCaptureLog struct {
	store.AuditLog
	capture *sessionAuditCaptureStore
}

func (s sessionAuditCaptureLog) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	event, err := s.AuditLog.Append(ctx, draft)
	if err == nil && draft.Action == "auth.sessions.admin_revoke" {
		s.capture.meta = draft.Meta
	}
	return event, err
}
func TestAdminSessionEndAuditsRevokedAndExcludedCounts(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	userID, member := mustMember(t, ctx, f.a, f.super, f.tA, "audit-counts@example.test", auth.RoleViewer)
	credential, err := auth.NewCredential(auth.PrefixScopedSession)
	if err != nil {
		t.Fatal(err)
	}
	var scoped model.AuthSession
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		scoped, err = as.Sessions().Create(ctx, model.AuthSession{UserID: userID, Selector: credential.Selector, SecretHash: credential.SecretHash, TenantScope: f.tA, AAL: 1, ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour))})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	capture := &sessionAuditCaptureStore{Store: f.st}
	a := auth.NewAuthenticator(capture, nil)
	if err := a.RevokeSessionsInTenant(ctx, f.super, f.tA, userID); err != nil {
		t.Fatal(err)
	}
	if capture.meta["tenant"] != f.tA.String() || capture.meta["revoked"] != 1 || capture.meta["excluded"] != 1 {
		t.Fatalf("affected counts = %v, want this tenant, 1 revoked, 1 excluded", capture.meta)
	}
	if !readSession(t, f.st, scoped.ID).Revoked || readSession(t, f.st, member.CredID).Revoked {
		t.Fatal("audit counts differ from stored revocation effects")
	}
}
