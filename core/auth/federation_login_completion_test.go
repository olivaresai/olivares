// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SSO completion effects after session issuance (ROOT-R5-SSO-COMPLETION-1). An existing
// active, non-superadmin, email-matched account with a verified Issuer and Subject logs
// in through CompleteSSO with a wired GroupMapper. When the absent-component guard refuses
// the session, neither the subject binding nor the group reconciliation may persist. When
// the session is issued (staging without history, or host break-glass), both completion
// effects run before the token is returned, so the first Authenticate already carries
// the reconciled group grant.

const (
	ssoCompletionEmail  = "eng@acme.com"
	ssoCompletionIssuer = "https://idp.acme.test"
	ssoCompletionIP     = "10.0.0.9"
)

type ssoCompletionFixture struct {
	st      store.Store
	a       *auth.Authenticator
	fed     *auth.FederationService
	tenant  model.TenantID
	userID  model.ID
	groupID model.ID
}

func newSSOCompletionFixture(t *testing.T, st store.Store) *ssoCompletionFixture {
	t.Helper()
	ctx := context.Background()
	a := auth.NewAuthenticator(st, nil).WithGroupMapper(fakeGroupMapper{})
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	userID, _ := mustMember(t, ctx, a, super, tenant, ssoCompletionEmail, auth.RoleViewer)
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "grp-eng"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatalf("map group role: %v", err)
	}
	configureGlobalLoginPosture(t, st)
	return &ssoCompletionFixture{st: st, a: a, fed: federationOn(st), tenant: tenant, userID: userID, groupID: g.Group.ID}
}

func (f *ssoCompletionFixture) identity(issuer, subject string) auth.FederatedIdentity {
	return auth.FederatedIdentity{Issuer: issuer, Subject: subject, Email: ssoCompletionEmail, Groups: []string{"grp-eng"}}
}

type ssoCompletionState struct {
	subject     string
	memberships int
	sessions    int
	auditSeq    int64
}

func (f *ssoCompletionFixture) state(t *testing.T) ssoCompletionState {
	t.Helper()
	ctx := context.Background()
	var s ssoCompletionState
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, f.userID)
		if err != nil {
			return err
		}
		members, _, err := as.GroupMembers().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.userID.String()}}, Limit: 100,
		})
		if err != nil {
			return err
		}
		sessions, _, err := as.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.userID.String()}}, Limit: 100,
		})
		if err != nil {
			return err
		}
		head, _, err := as.Audit().Head(ctx)
		if err != nil {
			return err
		}
		s = ssoCompletionState{subject: u.SsoSubject, memberships: len(members), sessions: len(sessions), auditSeq: head.Seq}
		return nil
	}); err != nil {
		t.Fatalf("read SSO completion state: %v", err)
	}
	return s
}

// runSSOCompletionAfterIssuance is shared by the SQLite and PostgreSQL tests.
func runSSOCompletionAfterIssuance(t *testing.T, open func(*testing.T) store.Store) {
	t.Run("refused session leaves no binding, membership or event", func(t *testing.T) {
		ctx := context.Background()
		f := newSSOCompletionFixture(t, open(t))
		recordLoginCapability(t, f.st)
		installClassified(t, f.a, f.fed, false, false, nil)

		before := f.state(t)
		if before.subject != "" || before.memberships != 0 {
			t.Fatalf("precondition: subject %q memberships %d, want unbound and no group", before.subject, before.memberships)
		}
		tok, sess, err := f.a.CompleteSSO(ctx, f.identity(ssoCompletionIssuer, "eng-sub"), ssoCompletionIP, f.tenant, false)
		if !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
			t.Fatalf("CompleteSSO = %v, want ErrLoginEnforcementComponentAbsent", err)
		}
		if tok != "" || !sess.ID.IsZero() {
			t.Fatalf("refused CompleteSSO returned token empty=%t session %s, want both empty", tok == "", sess.ID)
		}
		if after := f.state(t); after != before {
			t.Fatalf("refused SSO completion changed state %+v -> %+v (subject binding, group membership, session or audit event)", before, after)
		}

		// Host break-glass on the same artifact: the admitted login runs both completion
		// effects, and the first Authenticate of the returned token carries the group.
		installClassified(t, f.a, f.fed, false, true, nil)
		f.assertAdmittedCompletion(t, before)
	})

	t.Run("staging without history completes and never overwrites", func(t *testing.T) {
		ctx := context.Background()
		f := newSSOCompletionFixture(t, open(t))
		installClassified(t, f.a, f.fed, false, false, nil)
		before := f.state(t)
		f.assertAdmittedCompletion(t, before)

		// A second issuer matching the same email is admitted but never overwrites the binding.
		tok, _, err := f.a.CompleteSSO(ctx, f.identity("https://idp-other.test", "other-sub"), ssoCompletionIP, f.tenant, false)
		if err != nil || tok == "" {
			t.Fatalf("second-issuer CompleteSSO = %v (token empty %t)", err, tok == "")
		}
		if got, want := f.state(t).subject, (auth.FederatedIdentity{Issuer: ssoCompletionIssuer, Subject: "eng-sub"}).QualifiedSubject(); got != want {
			t.Fatalf("binding after a second issuer = %q, want %q (never overwrite)", got, want)
		}
	})
}

// assertAdmittedCompletion logs in with the verified issuer and checks the session, the
// binding, the single added membership, the three audit events (sso.login,
// sso.user.subject_bound, sso.group.reconcile) and first-use grants through Authenticate.
func (f *ssoCompletionFixture) assertAdmittedCompletion(t *testing.T, before ssoCompletionState) {
	t.Helper()
	ctx := context.Background()
	id := f.identity(ssoCompletionIssuer, "eng-sub")
	tok, sess, err := f.a.CompleteSSO(ctx, id, ssoCompletionIP, f.tenant, false)
	if err != nil || tok == "" || sess.UserID != f.userID {
		t.Fatalf("admitted CompleteSSO = %v (token empty %t, session user %s), want the member's session", err, tok == "", sess.UserID)
	}
	after := f.state(t)
	if after.subject != id.QualifiedSubject() {
		t.Fatalf("subject binding = %q, want %q", after.subject, id.QualifiedSubject())
	}
	if after.memberships != before.memberships+1 || after.sessions != before.sessions+1 {
		t.Fatalf("memberships %d -> %d and sessions %d -> %d, want one more of each", before.memberships, after.memberships, before.sessions, after.sessions)
	}
	if after.auditSeq != before.auditSeq+3 {
		t.Fatalf("audit seq %d -> %d, want exactly three events (login, binding, reconcile)", before.auditSeq, after.auditSeq)
	}
	var actions []string
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(ctx, before.auditSeq+1, func(e model.AuditEvent) error {
			actions = append(actions, e.Action)
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit events: %v", err)
	}
	if want := []string{"sso.login", "sso.user.subject_bound", "sso.group.reconcile"}; len(actions) != len(want) ||
		actions[0] != want[0] || actions[1] != want[1] || actions[2] != want[2] {
		t.Fatalf("audit actions after the admitted login = %v, want %v (session issuance first)", actions, want)
	}
	p, err := f.a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("first Authenticate of the returned token: %v", err)
	}
	if got := p.GroupsIn(f.tenant); len(got) != 1 || got[0] != f.groupID.String() {
		t.Fatalf("first-use GroupsIn = %v, want [%s]", got, f.groupID)
	}
	if role, _ := p.RoleIn(f.tenant); role != auth.RoleEditor {
		t.Fatalf("first-use RoleIn = %q, want editor from the reconciled group", role)
	}
}

func TestSSOCompletionAfterIssuance(t *testing.T) {
	runSSOCompletionAfterIssuance(t, testStore)
}

func TestSSOCompletionAfterIssuance_Postgres(t *testing.T) {
	runSSOCompletionAfterIssuance(t, func(t *testing.T) store.Store {
		st, _ := openLoginCapabilityPostgres(t)
		return st
	})
}
