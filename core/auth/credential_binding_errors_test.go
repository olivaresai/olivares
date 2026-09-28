// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errCredentialBindingOutage = errors.New("store outage")

// outageStore fails the auth partition on demand, as a database outage would.
type outageStore struct {
	store.Store
	view, mutate bool
}

func (s outageStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.view {
		return errCredentialBindingOutage
	}
	return s.Store.AuthView(ctx, fn)
}

func (s outageStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.mutate {
		return errCredentialBindingOutage
	}
	return s.Store.AuthMutate(ctx, fn)
}

// requireOperational asserts err is an operational failure carrying its cause,
// never the domain refusal that would send a run to reauthentication.
func requireOperational(t *testing.T, err, cause error, what string) {
	t.Helper()
	if err == nil || errors.Is(err, ErrCredentialBindingInvalid) || !errors.Is(err, cause) {
		t.Fatalf("%s = %v, want an operational failure wrapping %v, not the invalid-provenance refusal", what, err, cause)
	}
}

// STD-1/F1: an outage, a cancellation or a timeout is not invalid provenance.
// Domain-invalid provenance stays the one non-enumerating refusal.
func TestCredentialBindingSeparatesUnavailableEvidence(t *testing.T) {
	f := newCredentialBindingFixture(t)
	p, _, _ := f.session(f.userA, nil)
	run := model.NewID()
	s := f.subject(run, f.userA.ID)
	b := f.mustBind(p, s)

	viewDown := NewAuthenticator(outageStore{Store: f.st, view: true}, nil)
	mutateDown := NewAuthenticator(outageStore{Store: f.st, mutate: true}, nil)

	_, err := viewDown.ResolveCredentialBinding(f.deadline(), b, s)
	requireOperational(t, err, errCredentialBindingOutage, "resolve during an outage")

	canceled, cancel := context.WithTimeout(context.Background(), time.Minute)
	cancel()
	_, err = f.a.ResolveCredentialBinding(canceled, b, s)
	requireOperational(t, err, context.Canceled, "resolve after cancellation")

	_, err = viewDown.BindCredential(f.ctx, p, f.subject(model.NewID(), f.userA.ID))
	requireOperational(t, err, errCredentialBindingOutage, "bind during a read outage")
	_, err = mutateDown.BindCredential(f.ctx, p, f.subject(model.NewID(), f.userA.ID))
	requireOperational(t, err, errCredentialBindingOutage, "bind during a write outage")

	fresh, _, _ := f.session(f.userA, nil)
	_, err = mutateDown.RebindCredential(f.ctx, b, 1, fresh, s, fresh)
	requireOperational(t, err, errCredentialBindingOutage, "rebind during a write outage")
	_, err = viewDown.RebindCredential(f.ctx, b, 1, fresh, s, fresh)
	requireOperational(t, err, errCredentialBindingOutage, "rebind during a read outage")

	// Domain refusals stay the invalid-provenance refusal.
	if _, err := f.a.BindCredential(f.ctx, p, CredentialBindingSubject{
		Tenant: f.other, Kind: CredentialBindingWorkflowRun, Ref: model.NewID(), User: f.userA.ID,
	}); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("bind in a tenant the credential is not admitted to = %v, want ErrCredentialBindingInvalid", err)
	}
	if _, err := f.a.ResolveCredentialBinding(f.deadline(), CredentialBinding{}, s); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("resolve a missing handle = %v, want ErrCredentialBindingInvalid", err)
	}
	f.mustResolve(b, s)
}

// STD-2/F8: a long succession history is not a concurrent rebind. Successions
// read only the current row and the requested generation, so no history cap can
// turn into a misleading conflict.
func TestCredentialBindingSuccessionHasNoHistoryCap(t *testing.T) {
	f := newCredentialBindingFixture(t)
	p, _, _ := f.session(f.userA, nil)
	s := f.subject(model.NewID(), f.userA.ID)
	current := f.mustBind(p, s)
	for generation := int64(1); generation <= 300; generation++ {
		next, err := f.a.RebindCredential(f.ctx, current, generation, p, s, p)
		if err != nil {
			t.Fatalf("succession %d = %v, want a successor", generation, err)
		}
		current = next
	}
	f.mustResolve(current, s)
	// A real concurrent loser is still the named conflict.
	other, _, _ := f.session(f.userA, nil)
	if _, err := f.a.RebindCredential(f.ctx, current, 300, other, s, other); !errors.Is(err, ErrCredentialBindingConflict) {
		t.Fatalf("a second successor for generation 300 = %v, want ErrCredentialBindingConflict", err)
	}
}

// token seeds an ordinary API token of user bound to tenant with role, as an
// agent's on-behalf-of token when agent is set, and authenticates it.
func (f *credentialBindingFixture) token(user model.User, tenant model.TenantID, role, agent string) Principal {
	f.t.Helper()
	cred, err := NewCredential(PrefixToken)
	if err != nil {
		f.t.Fatalf("mint token: %v", err)
	}
	expires := model.NewTimestamp(time.Now().Add(time.Hour))
	row := model.APIToken{
		Name: "binding-" + role, UserID: user.ID, Selector: cred.Selector, SecretHash: cred.SecretHash,
		BoundTenantID: tenant, Role: role, ExpiresAt: &expires,
	}
	if agent != "" {
		row.AgentRef = agent
		row.Scope = strings.Join(scopeForTier(verbTierForRole(role)), " ")
	}
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		_, err := as.Tokens().Create(f.ctx, row)
		return err
	}); err != nil {
		f.t.Fatalf("seed token: %v", err)
	}
	p, err := f.a.Authenticate(f.ctx, cred.Token)
	if err != nil {
		f.t.Fatalf("authenticate token: %v", err)
	}
	return p
}

// confinedMember seeds an account whose membership in the tenant is confined
// to workspace, and returns it.
func (f *credentialBindingFixture) confinedMember(email string, workspace model.ID) model.User {
	f.t.Helper()
	var user model.User
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(f.ctx, model.User{Email: email, DisplayName: "Confined", Status: model.StatusActive})
		if err != nil {
			return err
		}
		_, err = as.Memberships().Create(f.ctx, model.Membership{
			UserID: user.ID, TargetTenantID: f.tenant, Role: RoleAdmin, WorkspaceID: workspace,
		})
		return err
	}); err != nil {
		f.t.Fatalf("seed confined member: %v", err)
	}
	return user
}

func (f *credentialBindingFixture) confine(user model.User, workspace model.ID) {
	f.t.Helper()
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(f.ctx, model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.UserID == user.ID && row.TargetTenantID == f.tenant {
				row.WorkspaceID = workspace
				_, err = as.Memberships().Update(f.ctx, row)
				return err
			}
		}
		return errors.New("membership not found")
	}); err != nil {
		f.t.Fatalf("move confinement: %v", err)
	}
}

// F3: a continuation keeps the run's recorded ceiling. The same account's
// credential may continue it as it was or narrower; a credential wider than the
// binding's ceiling — a session after a token, a higher role, an unconfined or
// differently confined credential, another agent identity — is refused before
// anything is written.
func TestCredentialBindingContinuationKeepsTheCeiling(t *testing.T) {
	f := newCredentialBindingFixture(t)
	refused := func(err error, what string) {
		t.Helper()
		if !errors.Is(err, ErrCredentialBindingCeiling) {
			t.Fatalf("%s = %v, want ErrCredentialBindingCeiling", what, err)
		}
	}
	rebind := func(from CredentialBinding, s CredentialBindingSubject, p Principal) (CredentialBinding, error) {
		return f.a.RebindCredential(f.ctx, from, 1, p, s, p)
	}

	// Compatible: the same account's fresh session.
	started, _, _ := f.session(f.userA, nil)
	s := f.subject(model.NewID(), f.userA.ID)
	b := f.mustBind(started, s)
	fresh, _, _ := f.session(f.userA, nil)
	if _, err := rebind(b, s, fresh); err != nil {
		t.Fatalf("compatible same-account continuation = %v", err)
	}

	// Narrower: a token of the same account with a lower role.
	s = f.subject(model.NewID(), f.userA.ID)
	b = f.mustBind(started, s)
	if _, err := rebind(b, s, f.token(f.userA, f.tenant, RoleEditor, "")); err != nil {
		t.Fatalf("narrower token continuation = %v", err)
	}

	// Wider: a token-started run continued by a session, or by a higher role.
	s = f.subject(model.NewID(), f.userA.ID)
	b = f.mustBind(f.token(f.userA, f.tenant, RoleEditor, ""), s)
	_, err := rebind(b, s, fresh)
	refused(err, "session continuing a token-started run")
	_, err = rebind(b, s, f.token(f.userA, f.tenant, RoleAdmin, ""))
	refused(err, "higher-role token continuing a token-started run")

	// Session confinement: an unconfined or differently confined credential.
	workspace := model.NewID()
	confined := f.confinedMember("confined@binding.test", workspace)
	confinedSession, _, _ := f.session(confined, nil)
	s = f.subject(model.NewID(), confined.ID)
	b = f.mustBind(confinedSession, s)
	_, err = rebind(b, s, f.token(confined, f.tenant, RoleViewer, ""))
	refused(err, "unconfined token continuing a confined run")
	f.confine(confined, model.NewID())
	elsewhere, _, _ := f.session(confined, nil)
	_, err = rebind(b, s, elsewhere)
	refused(err, "session confined elsewhere continuing a confined run")
	f.confine(confined, workspace)
	again, _, _ := f.session(confined, nil)
	if _, err := rebind(b, s, again); err != nil {
		t.Fatalf("same-confinement continuation = %v", err)
	}

	// Agent confinement: an agent's token continued without that agent.
	s = f.subject(model.NewID(), f.userA.ID)
	b = f.mustBind(f.token(f.userA, f.tenant, RoleEditor, "agent-worker"), s)
	_, err = rebind(b, s, f.token(f.userA, f.tenant, RoleEditor, ""))
	refused(err, "token without the agent continuing an agent's run")
	_, err = rebind(b, s, f.token(f.userA, f.tenant, RoleEditor, "agent-other"))
	refused(err, "another agent continuing an agent's run")
}

// R2: a subject with no binding has no authoritative record of the authority
// it was started with, so nothing proves the ceiling or confinement a
// continuation must keep. A never-bound subject — one from before v15, or one
// whose initial bind core/auth refused — is continued by no credential, and
// nothing is written: the refusal is the same before the owner reserves
// (VerifyCredentialContinuation) and in the succession (RebindCredential). A
// bound subject still admits the same credential and a narrower one.
func TestCredentialBindingUnprovenCeilingIsNotContinued(t *testing.T) {
	f := newCredentialBindingFixture(t)
	attempt := func(s CredentialBindingSubject, p Principal, what string) {
		t.Helper()
		if err := f.a.VerifyCredentialContinuation(f.ctx, p, s); !errors.Is(err, ErrCredentialBindingUnproven) {
			t.Fatalf("%s: verify before reserving = %v, want ErrCredentialBindingUnproven", what, err)
		}
		if _, err := f.a.RebindCredential(f.ctx, CredentialBinding{}, 1, p, s, p); !errors.Is(err, ErrCredentialBindingUnproven) {
			t.Fatalf("%s: succession = %v, want ErrCredentialBindingUnproven", what, err)
		}
		if rows := f.rows(s.Ref); len(rows) != 0 {
			t.Fatalf("%s: the refusal wrote %d binding rows", what, len(rows))
		}
	}
	session, _, _ := f.session(f.userA, nil)

	// Pre-v15 or missing binding: the subject has no row at all, whatever
	// credential started it.
	attempt(f.subject(model.NewID(), f.userA.ID), session, "pre-v15 run, the owner's session")
	attempt(f.subject(model.NewID(), f.userA.ID), f.token(f.userA, f.tenant, RoleViewer, ""),
		"pre-v15 run, the owner's narrow token")

	// Refused initial bind: core/auth refused the starting credential (a
	// session revoked after it authenticated), so the run started unbound.
	stale, _, row := f.session(f.userA, nil)
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		row.Revoked = true
		_, err := as.Sessions().Update(f.ctx, row)
		return err
	}); err != nil {
		t.Fatalf("revoke the starting session: %v", err)
	}
	refused := f.subject(model.NewID(), f.userA.ID)
	if _, err := f.a.BindCredential(f.ctx, stale, refused); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("initial bind of a revoked session = %v, want ErrCredentialBindingInvalid", err)
	}
	attempt(refused, session, "refused initial bind, the owner's session")

	// Token-to-session widening: a token-started run that was never bound
	// (its token recorded only in the run's own snapshot) is not continued by
	// the owner's interactive session, which also carries its group grants.
	attempt(f.subject(model.NewID(), f.userA.ID), session, "never-bound token-started run, the owner's session")

	// Absent confinement: a confined account's never-bound run is not
	// continued by its unconfined token.
	confined := f.confinedMember("unproven-confined@binding.test", model.NewID())
	attempt(f.subject(model.NewID(), confined.ID), f.token(confined, f.tenant, RoleViewer, ""),
		"never-bound confined run, an unconfined token")

	// A bound run keeps working: the same account's fresh session, then a
	// narrower token.
	s := f.subject(model.NewID(), f.userA.ID)
	b := f.mustBind(session, s)
	fresh, _, _ := f.session(f.userA, nil)
	if err := f.a.VerifyCredentialContinuation(f.ctx, fresh, s); err != nil {
		t.Fatalf("bound run, same continuation: verify = %v", err)
	}
	b2, err := f.a.RebindCredential(f.ctx, b, 1, fresh, s, fresh)
	if err != nil {
		t.Fatalf("bound run, same continuation: succession = %v", err)
	}
	narrower := f.token(f.userA, f.tenant, RoleViewer, "")
	if err := f.a.VerifyCredentialContinuation(f.ctx, narrower, s); err != nil {
		t.Fatalf("bound run, narrower continuation: verify = %v", err)
	}
	if _, err := f.a.RebindCredential(f.ctx, b2, 2, narrower, s, narrower); err != nil {
		t.Fatalf("bound run, narrower continuation: succession = %v", err)
	}
}
