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

// usersWithEmail lists the local accounts carrying an exact (already-normalized)
// email — 0, 1, or, only if a correlation bug forked one, more — so a test can assert
// whether a login resolved to the existing account or minted a duplicate.
func usersWithEmail(t *testing.T, ctx context.Context, st store.Store, email string) []model.User {
	t.Helper()
	var out []model.User
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: email}}, Limit: 100,
		})
		out = us
		return err
	}); err != nil {
		t.Fatalf("list users by email %q: %v", email, err)
	}
	return out
}

func qualified(issuer, subject string) string {
	return auth.FederatedIdentity{Issuer: issuer, Subject: subject}.QualifiedSubject()
}

// TestSSOSubjectCorrelation_RenameResilientThroughCompleteSSO is the U3
// wire-proof: it drives the REAL login-completion seam (CompleteSSO, the entry the
// callback handler calls) and shows the issuer-qualified subject flips a real outcome.
// A user whose IdP email is later renamed logs in under the SAME (issuer, subject):
// pre-U3 email-only correlation forks a SECOND account (losing every grant on the
// first); U3 resolves it by subject to the ORIGINAL account. The decision that changes
// is "which local principal does this assertion authenticate" — end to end.
func TestSSOSubjectCorrelation_RenameResilientThroughCompleteSSO(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	const issuer = "https://idp.acme.test"
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: issuer, Subject: "u-1", Email: "alice@acme.com",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("first CompleteSSO: %v", err)
	}
	first := usersWithEmail(t, ctx, st, "alice@acme.com")
	if len(first) != 1 {
		t.Fatalf("after first login: want exactly 1 account, got %d", len(first))
	}
	if got, want := first[0].SsoSubject, qualified(issuer, "u-1"); got != want {
		t.Fatalf("subject binding = %q, want %q", got, want)
	}

	// Same (issuer, subject), RENAMED email.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: issuer, Subject: "u-1", Email: "alice.smith@acme.com",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("second CompleteSSO (renamed email): %v", err)
	}
	if got := usersWithEmail(t, ctx, st, "alice.smith@acme.com"); len(got) != 0 {
		t.Fatalf("renamed-email login forked a NEW account: %d users under the new email", len(got))
	}
	after := usersWithEmail(t, ctx, st, "alice@acme.com")
	if len(after) != 1 || after[0].ID != first[0].ID {
		t.Fatalf("subject correlation did not resolve to the original account: %+v", after)
	}
}

// TestSSOSubjectCorrelation_CrossIdPCollisionSafe proves issuer qualification closes
// the cross-IdP collision the bare external_id column could not (§D5): two
// DIFFERENT IdPs asserting the SAME raw subject value must resolve to DIFFERENT
// accounts, never merge one tenant's user into another's.
func TestSSOSubjectCorrelation_CrossIdPCollisionSafe(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-a.test", Subject: "shared-123", Email: "a@x.test",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("idp-a CompleteSSO: %v", err)
	}
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-b.test", Subject: "shared-123", Email: "b@x.test",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("idp-b CompleteSSO: %v", err)
	}
	ua := usersWithEmail(t, ctx, st, "a@x.test")
	ub := usersWithEmail(t, ctx, st, "b@x.test")
	if len(ua) != 1 || len(ub) != 1 {
		t.Fatalf("want one account per IdP, got a=%d b=%d", len(ua), len(ub))
	}
	if ua[0].ID == ub[0].ID {
		t.Fatal("two IdPs sharing a subject value collided into ONE account (cross-IdP correlation hole)")
	}
	if ua[0].SsoSubject == ub[0].SsoSubject {
		t.Fatalf("distinct issuers produced the same binding %q", ua[0].SsoSubject)
	}
}

// TestSSOSubjectBinding_StampedOnEmailMatchNeverOverwritten proves the migration path
// and the anti-hijack rule: a pre-existing account (local/SCIM, no binding) links by
// verified email on first federated login and gets its subject STAMPED — but a second
// issuer that merely matches the same email must NEVER overwrite that binding.
func TestSSOSubjectBinding_StampedOnEmailMatchNeverOverwritten(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)

	// A pre-existing password account with no SSO binding.
	u, err := a.CreateUser(ctx, super, auth.NewUser{Email: "bob@acme.com", Password: "password-123"})
	if err != nil {
		t.Fatal(err)
	}
	if got := usersWithEmail(t, ctx, st, "bob@acme.com"); len(got) != 1 || got[0].SsoSubject != "" {
		t.Fatalf("precondition: want 1 account with no binding, got %+v", got)
	}

	// First SSO login (issuer A) links by email and STAMPS the binding.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-a.test", Subject: "sub-A", Email: "bob@acme.com",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("stamp login: %v", err)
	}
	bindA := qualified("https://idp-a.test", "sub-A")
	if got := usersWithEmail(t, ctx, st, "bob@acme.com"); got[0].SsoSubject != bindA {
		t.Fatalf("binding not stamped: got %q want %q", got[0].SsoSubject, bindA)
	}

	// A DIFFERENT issuer asserts the same email: matches by email, must NOT overwrite.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-b.test", Subject: "sub-B", Email: "bob@acme.com",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("second-issuer login: %v", err)
	}
	got := usersWithEmail(t, ctx, st, "bob@acme.com")
	if len(got) != 1 || got[0].ID != u.ID {
		t.Fatalf("second-issuer login forked/changed the account: %+v", got)
	}
	if got[0].SsoSubject != bindA {
		t.Fatalf("binding hijacked by a second issuer: got %q want %q", got[0].SsoSubject, bindA)
	}

	// After stamping, issuer A resolves by SUBJECT even under a renamed email.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-a.test", Subject: "sub-A", Email: "robert@acme.com",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("post-stamp rename login: %v", err)
	}
	if got := usersWithEmail(t, ctx, st, "robert@acme.com"); len(got) != 0 {
		t.Fatalf("post-stamp rename forked a new account: %d", len(got))
	}
}

// TestSSOSubjectCorrelation_NoIssuerIsEmailOnly locks the backward-compatible path:
// with NO issuer surfaced, the subject is never a correlation key and the login falls
// back to email exactly as before U3 — so the same subject under two emails is two
// accounts, and neither carries a (meaningless, unqualifiable) binding.
func TestSSOSubjectCorrelation_NoIssuerIsEmailOnly(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Subject: "same-sub", Email: "x@t.test",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Subject: "same-sub", Email: "y@t.test",
	}, "10.0.0.1", "", false); err != nil {
		t.Fatalf("second: %v", err)
	}
	x := usersWithEmail(t, ctx, st, "x@t.test")
	y := usersWithEmail(t, ctx, st, "y@t.test")
	if len(x) != 1 || len(y) != 1 || x[0].ID == y[0].ID {
		t.Fatalf("no-issuer correlation must be email-only (two accounts): x=%v y=%v", x, y)
	}
	if x[0].SsoSubject != "" || y[0].SsoSubject != "" {
		t.Fatalf("no-issuer login must not stamp a binding: x=%q y=%q", x[0].SsoSubject, y[0].SsoSubject)
	}
}

// TestSSOSubjectBinding_RefusedLoginLeavesNoTrace is the regression for the review's
// stamp-before-guard defect: a login REFUSED by CompleteSSO's eligibility guards — a
// disabled account, or a superadmin — must NOT persist a subject binding. Otherwise an
// unattended, ultimately-refused assertion durably captures an attacker-influenced
// (issuer, subject) key that a later reactivation would cash into a takeover.
func TestSSOSubjectBinding_RefusedLoginLeavesNoTrace(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)

	// (a) A disabled account: an assertion matching its email is refused and must not
	// stamp a binding.
	victim, err := a.CreateUser(ctx, super, auth.NewUser{Email: "victim@corp.test", Password: "password-123"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, e := as.Users().Get(ctx, victim.ID)
		if e != nil {
			return e
		}
		u.Status = model.StatusInactive
		_, e = as.Users().Update(ctx, u)
		return e
	}); err != nil {
		t.Fatalf("disable victim: %v", err)
	}
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-b.test", Subject: "attacker-sub", Email: "victim@corp.test",
	}, "10.0.0.1", "", false); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled-account SSO err = %v, want ErrUnauthenticated", err)
	}
	if got := usersWithEmail(t, ctx, st, "victim@corp.test"); len(got) != 1 || got[0].SsoSubject != "" {
		t.Fatalf("refused login planted a binding on a disabled account: %+v", got)
	}

	// (b) A superadmin: an assertion matching its email is refused and must not stamp
	// the system-root row.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp-b.test", Subject: "attacker-sub-2", Email: "root@example.com",
	}, "10.0.0.1", "", false); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("superadmin SSO err = %v, want ErrUnauthenticated", err)
	}
	if got := usersWithEmail(t, ctx, st, "root@example.com"); len(got) != 1 || got[0].SsoSubject != "" {
		t.Fatalf("refused login planted a binding on the superadmin: %+v", got)
	}
}

// TestSsoSubjectUniqueIndex proves the DB constraint directly: two accounts can never
// claim the same issuer-qualified subject, yet unbound accounts (empty ⇒ stored NULL)
// coexist without limit — the "NULLs distinct" property the codec's nil-normalization
// buys, so the unique index behaves as a partial "WHERE sso_subject IS NOT NULL" one.
func TestSsoSubjectUniqueIndex(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	mk := func(email, sub string) error {
		return st.AuthMutate(ctx, func(as store.AuthScope) error {
			_, err := as.Users().Create(ctx, model.User{
				Email: email, DisplayName: email, Status: model.StatusActive, SsoSubject: sub,
			})
			return err
		})
	}
	if err := mk("a@x.test", "iss\x1fsub-1"); err != nil {
		t.Fatalf("first bound account: %v", err)
	}
	if err := mk("b@x.test", "iss\x1fsub-1"); err == nil {
		t.Fatal("a second account with the same sso_subject must be rejected by the unique index")
	}
	if err := mk("c@x.test", ""); err != nil {
		t.Fatalf("unbound account c: %v", err)
	}
	if err := mk("d@x.test", ""); err != nil {
		t.Fatalf("unbound account d must coexist (NULLs distinct): %v", err)
	}
}
