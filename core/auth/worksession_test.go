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

type grantingScopedAuthorizer struct{}

func (grantingScopedAuthorizer) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{Effect: auth.EffectGrant, Reason: "test grant"}, nil
}

func workSessionSystemActor(t *testing.T) auth.Principal {
	t.Helper()
	actor, err := auth.NewSystemOperator("test:sessions-runtime", "exercise the dedicated work-session issuer")
	if err != nil {
		t.Fatalf("system actor: %v", err)
	}
	return actor
}

func TestWorkSessionCredentialHasExactHardCeiling(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-ceiling")
	a := auth.NewAuthenticator(st, nil)
	sid := "osn_" + model.NewID().String()
	agent := "agent:" + model.NewID().String()
	runRef := model.NewID().String()

	issued, err := a.IssueWorkSessionCredential(ctx, workSessionSystemActor(t), auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: sid, RunRef: runRef, AgentRef: agent, ClaimFence: 1,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	p, err := a.Authenticate(ctx, issued.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.SessionIdentity != sid || p.SessionRunRef != runRef || p.SessionFence != 1 ||
		p.AgentIdentity != agent || !p.IsWorkSessionCredential() {
		t.Fatalf("principal identity = sid %q run %q fence %d agent %q work=%v",
			p.SessionIdentity, p.SessionRunRef, p.SessionFence, p.AgentIdentity,
			p.IsWorkSessionCredential())
	}
	if issued.Tenant != tenant || issued.SessionRef != sid || issued.RunRef != runRef ||
		issued.AgentRef != agent || issued.ClaimFence != 1 {
		t.Fatalf("issuer returned untrusted/incorrect binding: %#v", issued)
	}
	if got := p.Tenants(); len(got) != 1 || got[0] != tenant {
		t.Fatalf("tenants = %v, want only %s", got, tenant)
	}

	// A deliberately permissive scoped engine cannot widen the private ceiling.
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(grantingScopedAuthorizer{}))
	for _, permission := range []auth.Permission{auth.WorkSessionLeaseWrite, auth.WorkSessionWorkWrite} {
		if !az.Allowed(ctx, p, permission, tenant) {
			t.Errorf("exact permission %q denied", permission)
		}
	}
	for _, permission := range []auth.Permission{
		"sessions:work:read", "sessions:lease:admin", "sessions:run:write", "token:write",
	} {
		if az.Allowed(ctx, p, permission, tenant) {
			t.Errorf("hard ceiling widened to %q", permission)
		}
	}
	if az.Allowed(ctx, p, auth.WorkSessionLeaseWrite, provisionTenant(t, st, "work-session-foreign")) {
		t.Error("credential crossed its tenant binding")
	}

	simulated, found, err := a.PrincipalForToken(ctx, issued.ID)
	if err != nil || !found || simulated.SessionIdentity != sid ||
		simulated.SessionRunRef != runRef || !simulated.IsWorkSessionCredential() {
		t.Fatalf("PrincipalForToken = found=%v sid=%q run=%q work=%v err=%v",
			found, simulated.SessionIdentity, simulated.SessionRunRef,
			simulated.IsWorkSessionCredential(), err)
	}

	if _, err := a.ExchangeToken(ctx, p, auth.ExchangeRequest{
		SubjectToken: issued.Token, SubjectTokenType: auth.TokenTypeAccessToken,
	}); !errors.Is(err, auth.ErrInvalidExchange) {
		t.Fatalf("exchange restricted token = %v, want ErrInvalidExchange", err)
	}

	if err := a.RevokeWorkSessionCredential(ctx, workSessionSystemActor(t), issued.ID,
		auth.WorkSessionCredentialSpec{Tenant: tenant, SessionRef: sid, RunRef: runRef,
			AgentRef: agent, ClaimFence: 1}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := a.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("authenticate revoked = %v, want unauthenticated", err)
	}
}

func TestWorkSessionCredentialIssuerAndStoredShapeDenyClosed(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-shape")
	a := auth.NewAuthenticator(st, nil)
	sid := "osn_" + model.NewID().String()

	nonSystem := auth.ScopedPrincipal(model.NewID(), "tenant admin", tenant, auth.RoleAdmin)
	if _, err := a.IssueWorkSessionCredential(ctx, nonSystem, auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: sid, RunRef: model.NewID().String(), ClaimFence: 1,
	}); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("non-system issue = %v, want role ceiling", err)
	}
	if _, err := a.IssueWorkSessionCredential(ctx, workSessionSystemActor(t), auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_not-a-uuid", RunRef: model.NewID().String(), ClaimFence: 1,
	}); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("malformed sid issue = %v, want invalid token", err)
	}

	// A session_ref on an ordinary role token is not a hidden second issuer.
	ordinary, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	var ordinaryID model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		row, err := as.Tokens().Create(ctx, model.APIToken{
			Name: "malformed-ordinary", Selector: ordinary.Selector, SecretHash: ordinary.SecretHash,
			BoundTenantID: tenant, Role: auth.RoleEditor, SessionRef: sid,
		})
		ordinaryID = row.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, ordinary.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("ordinary token with session_ref = %v, want unauthenticated", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, ordinaryID); err != nil || found {
		t.Fatalf("PrincipalForToken malformed ordinary = found=%v err=%v", found, err)
	}
}

func TestWorkSessionCredentialRenewalFollowsRuntimeLiveness(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-renew")
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	actor := workSessionSystemActor(t)

	activeSpec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	}
	active, err := a.IssueWorkSessionCredential(ctx, actor, activeSpec)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(20 * time.Minute)
	if _, err := a.RenewWorkSessionCredential(ctx, actor, active.ID, activeSpec); err != nil {
		t.Fatalf("renew live credential: %v", err)
	}
	clock.advance(11 * time.Minute) // past the original t0+30m expiry
	if _, err := a.Authenticate(ctx, active.Token); err != nil {
		t.Fatalf("same bearer after heartbeat extension: %v", err)
	}
	clock.advance(20 * time.Minute) // past the renewed t0+50m expiry
	if _, err := a.Authenticate(ctx, active.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("credential without another heartbeat = %v, want unauthenticated", err)
	}
	if _, err := a.RenewWorkSessionCredential(ctx, actor, active.ID, activeSpec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired credential resurrected: %v", err)
	}

	crashed, err := a.IssueWorkSessionCredential(ctx, actor, auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(auth.DefaultWorkSessionCredentialTTL + time.Second)
	if _, err := a.Authenticate(ctx, crashed.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("unrenewed crash credential = %v, want unauthenticated", err)
	}
}

func TestWorkSessionCredentialRenewAndRevokeRequireExactSiblingBinding(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-sibling-handles")
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	actor := workSessionSystemActor(t)
	agent := "agent:" + model.NewID().String()
	leftSpec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), AgentRef: agent, ClaimFence: 1,
	}
	rightSpec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), AgentRef: agent, ClaimFence: 1,
	}
	left, err := a.IssueWorkSessionCredential(ctx, actor, leftSpec)
	if err != nil {
		t.Fatal(err)
	}
	right, err := a.IssueWorkSessionCredential(ctx, actor, rightSpec)
	if err != nil {
		t.Fatal(err)
	}

	clock.advance(20 * time.Minute)
	if _, err := a.RenewWorkSessionCredential(ctx, actor, left.ID, rightSpec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("cross-sibling renew = %v, want unauthenticated", err)
	}
	if _, err := a.RenewWorkSessionCredential(ctx, actor, right.ID, rightSpec); err != nil {
		t.Fatalf("exact sibling control renew: %v", err)
	}
	clock.advance(11 * time.Minute)
	if _, err := a.Authenticate(ctx, left.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("cross-sibling renew extended left token: %v", err)
	}
	if _, err := a.Authenticate(ctx, right.Token); err != nil {
		t.Fatalf("cross-sibling renew harmed right token: %v", err)
	}

	left, err = a.IssueWorkSessionCredential(ctx, actor, leftSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeWorkSessionCredential(ctx, actor, left.ID, rightSpec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("cross-sibling revoke = %v, want unauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, left.Token); err != nil {
		t.Fatalf("cross-sibling revoke killed left token: %v", err)
	}
	if err := a.RevokeWorkSessionCredential(ctx, actor, left.ID, leftSpec); err != nil {
		t.Fatalf("exact revoke: %v", err)
	}
	if _, err := a.Authenticate(ctx, left.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("exactly revoked token = %v, want unauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, right.Token); err != nil {
		t.Fatalf("exact left revoke harmed sibling: %v", err)
	}
}

func TestWorkSessionCredentialIssueSupersedesExactPriorGeneration(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-supersede")
	a := auth.NewAuthenticator(st, nil)
	actor := workSessionSystemActor(t)
	agent := "agent:" + model.NewID().String()
	exact := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), AgentRef: agent, ClaimFence: 1,
	}
	siblingSpec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: exact.RunRef, AgentRef: agent, ClaimFence: 1,
	}
	old, err := a.IssueWorkSessionCredential(ctx, actor, exact)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := a.IssueWorkSessionCredential(ctx, actor, siblingSpec)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := a.IssueWorkSessionCredential(ctx, actor, exact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, old.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("superseded exact bearer = %v, want unauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, successor.Token); err != nil {
		t.Fatalf("successor bearer: %v", err)
	}
	if _, err := a.Authenticate(ctx, sibling.Token); err != nil {
		t.Fatalf("sibling SID was revoked by exact supersede: %v", err)
	}
}

func TestWorkSessionCredentialRejectsDelayedStaleGeneration(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-stale-generation")
	a := auth.NewAuthenticator(st, nil)
	actor := workSessionSystemActor(t)
	oldSpec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), AgentRef: "agent:old", ClaimFence: 7,
	}
	old, err := a.IssueWorkSessionCredential(ctx, actor, oldSpec)
	if err != nil {
		t.Fatal(err)
	}
	successorSpec := oldSpec
	successorSpec.AgentRef = "agent:successor"
	successorSpec.ClaimFence++
	successor, err := a.IssueWorkSessionCredential(ctx, actor, successorSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, old.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("superseded old bearer = %v, want unauthenticated", err)
	}
	if _, err := a.IssueWorkSessionCredential(ctx, actor, oldSpec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("delayed stale issue = %v, want unauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, successor.Token); err != nil {
		t.Fatalf("stale issue harmed successor bearer: %v", err)
	}

	crossed := successorSpec
	crossed.AgentRef = "agent:crossed"
	if _, err := a.IssueWorkSessionCredential(ctx, actor, crossed); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("same-fence crossed binding = %v, want unauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, successor.Token); err != nil {
		t.Fatalf("crossed retry harmed successor bearer: %v", err)
	}
	if err := a.RevokeWorkSessionCredential(ctx, actor, successor.ID, successorSpec); err != nil {
		t.Fatalf("revoke successor: %v", err)
	}
	// The floor is historical, not merely a scan of active bearers. A delayed
	// generation stays stale after the successor was explicitly revoked.
	if _, err := a.IssueWorkSessionCredential(ctx, actor, oldSpec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("stale issue after high-fence revoke = %v, want unauthenticated", err)
	}
}

func TestWorkSessionCredentialExpiryBoundaryIsClosed(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "work-session-expiry-boundary")
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	actor := workSessionSystemActor(t)
	spec := auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	}
	issued, err := a.IssueWorkSessionCredential(ctx, actor, spec)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(auth.DefaultWorkSessionCredentialTTL - time.Nanosecond)
	if _, err := a.Authenticate(ctx, issued.Token); err != nil {
		t.Fatalf("one tick before expiry: %v", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, issued.ID); err != nil || !found {
		t.Fatalf("one tick before expiry simulation = found=%v err=%v", found, err)
	}
	clock.advance(time.Nanosecond)
	if _, err := a.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("exact expiry authentication = %v, want unauthenticated", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, issued.ID); err != nil || found {
		t.Fatalf("exact expiry simulation = found=%v err=%v", found, err)
	}
	if _, err := a.RenewWorkSessionCredential(ctx, actor, issued.ID, spec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("exact expiry renewal = %v, want unauthenticated", err)
	}
}

func TestWorkSessionCredentialCannotBeDelegationCaller(t *testing.T) {
	ctx := context.Background()
	f := newExchangeFixture(t)
	actor := workSessionSystemActor(t)
	spec := auth.WorkSessionCredentialSpec{
		Tenant: f.tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	}
	issued, err := f.a.IssueWorkSessionCredential(ctx, actor, spec)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := f.a.Authenticate(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ExchangeToken(ctx, caller, accessReq(f.editorTok)); !errors.Is(err, auth.ErrInvalidExchange) {
		t.Fatalf("work-session exchange caller = %v, want invalid exchange", err)
	}
	ordinary, err := f.a.Authenticate(ctx, f.editorTok)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ExchangeToken(ctx, ordinary, accessReq(f.editorTok)); err != nil {
		t.Fatalf("ordinary caller control: %v", err)
	}
}

func TestWorkSessionCredentialCannotMintDelegationHandleAsCaller(t *testing.T) {
	f := newDelegFixture(t)
	spec := auth.WorkSessionCredentialSpec{
		Tenant: f.tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	}
	issued, err := f.a.IssueWorkSessionCredential(f.ctx, workSessionSystemActor(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := f.a.Authenticate(f.ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	req := auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID, Operations: []string{"messages"},
	}
	if _, _, err := f.a.MintDelegationHandle(f.ctx, caller, req); !errors.Is(err, auth.ErrInvalidDelegationRequest) {
		t.Fatalf("work-session delegation caller = %v, want invalid request", err)
	}
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, req); err != nil {
		t.Fatalf("ordinary caller control: %v", err)
	}
}
