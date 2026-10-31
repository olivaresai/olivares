// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Login-time group reconciliation. The IdP asserted a membership, the session was
// issued, and the write that stores the membership failed. Group membership is a fact
// deny rules reason over, so the sign-in must not complete with fewer memberships than
// the IdP asserted: it is refused, with one audit record that carries a stable code and
// nothing from the assertion.

var errReconcileOutage = errors.New("injected store outage")

// reconcileOutageStore fails the AuthMutate calls that follow the SSO session commit,
// the first of which is the reconcile write: the identity under test carries no issuer,
// so nothing else writes between the mint and the reconcile. The session commit is
// detected by offboardAfterSSOMintStore, which this wraps; no production hook is used.
type reconcileOutageStore struct {
	store.Store
	left   atomic.Int32       // writes still to fail
	cancel context.CancelFunc // when set, called at the first failure: the client has gone
}

func (s *reconcileOutageStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.left.Add(-1) >= 0 {
		if s.cancel != nil {
			s.cancel()
		}
		return errReconcileOutage
	}
	return s.Store.AuthMutate(ctx, fn)
}

// outageAfterMint returns an Authenticator, with the group mapper wired, over f's store
// that fails the next `failures` writes once the SSO session is committed: 1 is the
// reconcile alone, 2 adds the session revoke, 3 the refusal's audit record. A non-nil
// cancel is called at the first failure, as when the client disconnects.
func (f *overageFixture) outageAfterMint(t *testing.T, failures int32, cancel context.CancelFunc) *auth.Authenticator {
	t.Helper()
	user, _ := f.account(t, overageEmail)
	outage := &reconcileOutageStore{cancel: cancel}
	outage.Store = &offboardAfterSSOMintStore{Store: f.st, user: user.ID, afterMint: func() error {
		outage.left.Store(failures)
		return nil
	}}
	return auth.NewAuthenticator(outage, nil).WithGroupMapper(fakeGroupMapper{})
}

// liveSessions counts the user's sessions that are not revoked, and of those the ones
// opened by SSO.
func (f *overageFixture) liveSessions(t *testing.T, user model.ID) (all, sso int) {
	t.Helper()
	if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
		rows, _, err := as.Sessions().List(context.Background(), model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
		}, Limit: 100})
		for _, row := range rows {
			if !row.Revoked {
				all++
				if slices.Contains(row.AMR, "sso") {
					sso++
				}
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return all, sso
}

func TestCompleteSSOReconcileFailureRefusesTheSignIn(t *testing.T) {
	ctx := context.Background()
	f := newOverageFixture(t)
	user, _ := f.account(t, overageEmail)
	// The client goes at the failing write: the withdrawal must not depend on its context.
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := f.outageAfterMint(t, 1, cancel)
	seq := auditHead(t, ctx, f.st)
	liveBefore, _ := f.liveSessions(t, user.ID)

	asserted := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject", Groups: []string{"grp-eng"}}
	tok, _, err := a.CompleteSSO(reqCtx, asserted, scopeIP, f.tenant, false)
	if err == nil || tok != "" {
		t.Fatalf("CompleteSSO signed %s in (token issued=%t, err=%v) holding GroupsIn=%v although the IdP asserted [grp-eng] and storing it failed",
			overageEmail, tok != "", err, f.groups(t))
	}
	// The caller is answered with the fixed sentinel; the store's own error stays in the log.
	if !errors.Is(err, auth.ErrSSOGroupReconcile) || errors.Is(err, errReconcileOutage) {
		t.Fatalf("CompleteSSO = %v, want ErrSSOGroupReconcile without the store error", err)
	}

	blocked := f.blockedSince(t, seq, "sso_group_reconcile_failed")
	if len(blocked) != 1 {
		t.Fatalf("refusal left %d audit records, want exactly 1", len(blocked))
	}
	keys := make([]string, 0, len(blocked[0].Meta))
	for k := range blocked[0].Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"ip", "reason", "scope"}; len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("audit meta keys = %v, want %v (nothing from the assertion)", keys, want)
	}
	if want := "user:" + user.ID.String(); blocked[0].Actor != want {
		t.Fatalf("audit actor = %q, want %q (the account was already resolved)", blocked[0].Actor, want)
	}

	// The session minted before the failure is withdrawn: nobody holds its token, and it
	// must not sit in the account's session list as a live sign-in. Only that one: the
	// account's other sessions stay.
	if all, sso := f.liveSessions(t, user.ID); sso != 0 || all != liveBefore {
		t.Fatalf("after the refused sign-in: %d live SSO sessions (want 0), %d live in all (want the %d from before)", sso, all, liveBefore)
	}

	// The failure was transient: the same assertion on the next attempt reconciles and signs in.
	if tok, _, err := f.a.CompleteSSO(ctx, asserted, scopeIP, f.tenant, false); err != nil || tok == "" {
		t.Fatalf("retry after the outage: token issued=%t, err=%v", tok != "", err)
	}
	if got := f.groups(t); len(got) != 1 || got[0] != f.group.String() {
		t.Fatalf("GroupsIn after the retry = %v, want [%s]", got, f.group)
	}
}

// The refusal does not depend on its own bookkeeping: with the revoke and the audit write
// failing too (the store is down, which is the likely cause of the reconcile failure), the
// sign-in is still refused and no token leaves.
func TestCompleteSSOReconcileFailureRefusesWhenWithdrawalFails(t *testing.T) {
	ctx := context.Background()
	f := newOverageFixture(t)
	// The Authenticator takes slog.Default() at construction.
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	a := f.outageAfterMint(t, 3, nil)
	asserted := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject", Groups: []string{"grp-eng"}}
	tok, _, err := a.CompleteSSO(ctx, asserted, scopeIP, f.tenant, false)
	if !errors.Is(err, auth.ErrSSOGroupReconcile) || tok != "" {
		t.Fatalf("CompleteSSO = token issued=%t, err=%v; want a refusal with ErrSSOGroupReconcile", tok != "", err)
	}
	// Each failed write is visible to the operator, and nothing from the assertion is.
	for _, want := range []string{"sso sign-in refused after the session was issued", "withdrawing a refused sso session", "recording blocked login", "sso_group_reconcile_failed"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logged.String())
		}
	}
	for _, leak := range []string{overageEmail, "grp-eng", "eng-subject"} {
		if strings.Contains(logged.String(), leak) {
			t.Errorf("log carries %q from the assertion:\n%s", leak, logged.String())
		}
	}
}

// Where CompleteSSO does not reconcile (SCIM is authoritative), a failing write after the
// mint is not on its path: the login is admitted as before.
func TestCompleteSSOReconcileFailureOnlyWhereGroupsApply(t *testing.T) {
	ctx := context.Background()
	f := newOverageFixture(t)
	a := f.outageAfterMint(t, 1, nil)
	asserted := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject", Groups: []string{"grp-eng"}}
	if tok, _, err := a.CompleteSSO(ctx, asserted, scopeIP, f.tenant, true); err != nil || tok == "" {
		t.Fatalf("SCIM-authoritative login: token issued=%t, err=%v, want it admitted", tok != "", err)
	}
	if got := f.groups(t); len(got) != 0 {
		t.Fatalf("SCIM-authoritative login reconciled groups: %v", got)
	}
}

// An IdP that asserted no groups at all has nothing to store: a failing store after the
// mint is not on its path, and "known empty" is not turned into a refusal.
func TestCompleteSSOKnownEmptyGroupsAreNotRefusedByAFailingStore(t *testing.T) {
	ctx := context.Background()
	f := newOverageFixture(t)
	a := f.outageAfterMint(t, 1, nil)
	asserted := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject"}
	if tok, _, err := a.CompleteSSO(ctx, asserted, scopeIP, f.tenant, false); err != nil || tok == "" {
		t.Fatalf("login with no asserted groups: token issued=%t, err=%v, want it admitted", tok != "", err)
	}
}

// The withdrawal's two writes are independent: when the session revoke fails, the
// refusal stands and its audit record is still written.
func TestCompleteSSOReconcileFailureStillAuditsWhenTheRevokeFails(t *testing.T) {
	ctx := context.Background()
	f := newOverageFixture(t)
	a := f.outageAfterMint(t, 2, nil)
	seq := auditHead(t, ctx, f.st)
	asserted := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject", Groups: []string{"grp-eng"}}
	tok, _, err := a.CompleteSSO(ctx, asserted, scopeIP, f.tenant, false)
	if !errors.Is(err, auth.ErrSSOGroupReconcile) || tok != "" {
		t.Fatalf("CompleteSSO = token issued=%t, err=%v; want a refusal with ErrSSOGroupReconcile", tok != "", err)
	}
	if n := len(f.blockedSince(t, seq, "sso_group_reconcile_failed")); n != 1 {
		t.Fatalf("refusal left %d audit records, want exactly 1", n)
	}
}
