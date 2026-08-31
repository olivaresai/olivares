// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_pdp_test.go pins the PDP-service identity adapter
// (resolveDelegatedIdentity): the seam that turns an authenticated PEP service's
// presented DelegationProof into the SAME sealed resolvedIdentity snapshot the
// bearer adapter produces, while mapping the delegation verifier's typed domain
// faults onto the PEP-neutral gateCode + sdk.FailureClass taxonomy the future PDP
// verdict layer consumes. AuthenticatePEP is the CALLER's step (a precondition of
// this adapter): these tests hand it an already-authenticated PEPIdentity.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// delegDigest returns a canonical lowercase-hex SHA-256, the shape a presented
// ContentDigest must have (enforced by the verifier's validatePresentedRequest).
func delegDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const delegTestAudience = "urn:olivares:pdp:inference"

// permissiveAgentChecker passes every agent-lifecycle check, so an agent-bound
// handle can be minted and claimed without wiring the governance module. It is
// INERT for a non-agent handle (revalidateSubject only consults the checker when
// handle.AgentRef != "").
type permissiveAgentChecker struct{}

func (permissiveAgentChecker) CheckAgentForExchange(context.Context, model.TenantID, string, string) error {
	return nil
}

// stubDelegationVerifier returns a fixed (VerifiedDelegation, error) so the
// adapter's fault-mapping and deny-closed guards can be exercised in isolation
// from the real store. VerifiedDelegation's fields are unexported, so the only
// value a stub can return is the ZERO delegation — which is exactly what the
// tenant deny-closed guard must refuse when paired with a non-error return.
type stubDelegationVerifier struct {
	v   auth.VerifiedDelegation
	err error
}

func (s stubDelegationVerifier) VerifyAndClaimDelegation(context.Context, auth.PEPIdentity, auth.DelegationProofInput, auth.PresentedRequest) (auth.VerifiedDelegation, error) {
	return s.v, s.err
}

// delegEnv is a fully provisioned delegation environment built entirely from the
// exported auth API (mirroring core/auth's delegFixture, but reachable from
// package main): a real store with the core auth schema, a real Authenticator, a
// governed SUBJECT (a user session, editor in the tenant), a registered+bound PEP
// service, and its authenticated PEPIdentity.
type delegEnv struct {
	ctx              context.Context
	a                *auth.Authenticator
	tenant           model.TenantID
	admin            auth.Principal
	subjectSession   string
	subjectSessionID model.ID
	subjectUserID    model.ID
	service          model.PEPService
	pep              auth.PEPIdentity
}

func newDelegEnv(t *testing.T) delegEnv {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	a := auth.NewAuthenticator(st, nil)
	a.SetAgentLifecycleChecker(permissiveAgentChecker{})

	if _, err := a.BootstrapSuperadmin(ctx, "root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap superadmin: %v", err)
	}
	superTok, _, err := a.Login(ctx, "root@example.com", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("login superadmin: %v", err)
	}
	super, err := a.Authenticate(ctx, superTok)
	if err != nil {
		t.Fatalf("authenticate superadmin: %v", err)
	}

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	admin := delegMember(t, ctx, a, super, tenant, "admin@acme.test", auth.RoleAdmin)

	su, err := a.CreateUser(ctx, super, auth.NewUser{Email: "subject@acme.test", DisplayName: "Subject", Password: "subject-password-1"})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}
	if _, err := a.GrantMembership(ctx, super, su.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("grant subject membership: %v", err)
	}
	subjectSession, subjSess, err := a.Login(ctx, "subject@acme.test", "subject-password-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("login subject: %v", err)
	}

	service, err := a.RegisterPEPService(ctx, admin, auth.PEPServiceSpec{
		Tenant: tenant, Name: "deleg-pep-a", PDPAudience: delegTestAudience,
		Capabilities: map[string]bool{"buffer_request": true, "streaming": true},
	})
	if err != nil {
		t.Fatalf("register PEP service: %v", err)
	}

	// Bind an audience-bearing exchanged token to the service, then authenticate it.
	provTok, _, err := a.IssueToken(ctx, super, auth.TokenSpec{Name: "pep-prov", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatalf("issue provisioning token: %v", err)
	}
	provCaller, err := a.Authenticate(ctx, provTok)
	if err != nil {
		t.Fatalf("authenticate provisioning token: %v", err)
	}
	ex, err := a.ExchangeToken(ctx, provCaller, auth.ExchangeRequest{
		SubjectToken: provTok, SubjectTokenType: auth.TokenTypeAccessToken,
		Audiences: []string{delegTestAudience}, Name: "pep-transport",
	})
	if err != nil {
		t.Fatalf("exchange PEP transport token: %v", err)
	}
	if err := a.BindPEPCredential(ctx, admin, service.ID, ex.Stored.ID); err != nil {
		t.Fatalf("bind PEP credential: %v", err)
	}
	pep, err := a.AuthenticatePEP(ctx, ex.AccessToken)
	if err != nil {
		t.Fatalf("authenticate PEP: %v", err)
	}

	return delegEnv{
		ctx: ctx, a: a, tenant: tenant, admin: admin,
		subjectSession: subjectSession, subjectSessionID: subjSess.ID,
		subjectUserID: su.ID, service: service, pep: pep,
	}
}

func delegMember(t *testing.T, ctx context.Context, a *auth.Authenticator, super auth.Principal, tenant model.TenantID, email, role string) auth.Principal {
	t.Helper()
	u, err := a.CreateUser(ctx, super, auth.NewUser{Email: email, DisplayName: role, Password: "member-password-1"})
	if err != nil {
		t.Fatalf("create %s: %v", role, err)
	}
	if _, err := a.GrantMembership(ctx, super, u.ID, tenant, role, model.ID("")); err != nil {
		t.Fatalf("grant %s: %v", role, err)
	}
	session, _, err := a.Login(ctx, email, "member-password-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("login %s: %v", role, err)
	}
	p, err := a.Authenticate(ctx, session)
	if err != nil {
		t.Fatalf("authenticate %s: %v", role, err)
	}
	return p
}

func (e delegEnv) mint(t *testing.T, req auth.MintDelegationRequest) (string, model.DelegationHandle) {
	t.Helper()
	if req.SubjectToken == "" {
		req.SubjectToken = e.subjectSession
	}
	if req.PEPServiceID.IsZero() {
		req.PEPServiceID = e.service.ID
	}
	if len(req.Operations) == 0 {
		req.Operations = []string{"messages"}
	}
	token, stored, err := e.a.MintDelegationHandle(e.ctx, e.admin, req)
	if err != nil {
		t.Fatalf("mint delegation handle: %v", err)
	}
	return token, stored
}

func delegHandleProof(token string) auth.DelegationProofInput {
	return auth.DelegationProofInput{Scheme: "handle", Token: []byte(token)}
}

func delegPresented(nonce, op string) auth.PresentedRequest {
	return auth.PresentedRequest{
		Nonce:                nonce,
		OperationKind:        op,
		Model:                "claude-opus-4-8",
		Stream:               false,
		ContentDigest:        delegDigest("content-abc"),
		ContentSize:          42,
		MediaType:            "application/json",
		IssuedAt:             time.Now(),
		DeclaredCapabilities: map[string]bool{"buffer_request": true},
	}
}

// delegDecider wires a decider whose ONLY live seams are the delegation verifier
// and the per-tenant policy source — everything resolveDelegatedIdentity touches.
func delegDecider(verifier delegationVerifier, pol proxyPolicySource) *inferenceProxyDecider {
	return &inferenceProxyDecider{
		surface:    "direct",
		delegation: verifier,
		policy:     pol,
		clock:      time.Now,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// --- happy path: the sealed snapshot names the SUBJECT -------------------------

func TestResolveDelegatedIdentityHappyPathNamesSubject(t *testing.T) {
	e := newDelegEnv(t)
	token, stored := e.mint(t, auth.MintDelegationRequest{Operations: []string{"messages", "messages_batch"}})
	d := delegDecider(e.a, fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()})

	id, outcome, deny, ok := d.resolveDelegatedIdentity(e.ctx, e.pep, delegHandleProof(token), delegPresented("nonce-happy", "messages"))
	if !ok {
		t.Fatalf("expected a sealed delegated identity; got deny code=%q class=%q status=%d", deny.code, deny.class, deny.decision.Status)
	}
	if !id.ok {
		t.Fatal("resolved identity must be sealed")
	}
	if id.tenant != e.tenant {
		t.Errorf("tenant = %q, want %q", id.tenant, e.tenant)
	}
	// The snapshot's normative subject is the SUBJECT USER — never the handle JTI nor the PEP.
	if id.subjectKind != string(auth.KindUser) {
		t.Errorf("subjectKind = %q, want user", id.subjectKind)
	}
	if id.subjectID != e.subjectUserID.String() {
		t.Errorf("subjectID = %q, want the subject user %q", id.subjectID, e.subjectUserID)
	}
	if id.actor != id.principal.Actor() || id.actor != "user:"+e.subjectUserID.String() {
		t.Errorf("actor = %q, want user:%q", id.actor, e.subjectUserID)
	}
	if id.principal.Kind != auth.KindUser || id.principal.UserID != e.subjectUserID {
		t.Errorf("principal = {kind %s, user %s}, want {user, %s}", id.principal.Kind, id.principal.UserID, e.subjectUserID)
	}
	if id.principal.CredID == stored.ID {
		t.Error("principal CredID must be the subject SOURCE credential, never the handle JTI")
	}
	if id.principal.CredID != e.subjectSessionID {
		t.Errorf("principal CredID = %q, want the subject session %q", id.principal.CredID, e.subjectSessionID)
	}
	// A delegated handle carries NO human assurance and is never superadmin.
	if id.principal.AAL != 0 || id.principal.AMR != nil || id.principal.Superadmin {
		t.Errorf("principal AAL/AMR/superadmin = %d/%v/%v, want 0/nil/false", id.principal.AAL, id.principal.AMR, id.principal.Superadmin)
	}
	// No agent binding on this handle.
	if id.sessionRef != "" || id.principal.AgentIdentity != "" {
		t.Errorf("unexpected agent binding: sessionRef=%q agentIdentity=%q", id.sessionRef, id.principal.AgentIdentity)
	}
	if r, _ := id.principal.RoleIn(e.tenant); r != auth.RoleEditor {
		t.Errorf("principal role = %q, want editor", r)
	}
	// The outcome surfaces the server-minted decision to the future service composition.
	if outcome.DecisionID().IsZero() {
		t.Error("outcome DecisionID must be set")
	}
	if outcome.Retried() {
		t.Error("a first claim must not be a retry")
	}
	if deny.code != "" || deny.decision.Status != 0 {
		t.Errorf("allow path must carry a zero gateResult; got code=%q status=%d", deny.code, deny.decision.Status)
	}
}

// TestResolveDelegatedIdentityCarriesAgentIdentity pins that a handle minted with
// an agent_ref surfaces the agent binding on the sealed principal (WithAgentIdentity),
// so the modelaccessgate F-01 formula binds the delegated subject to its agent.
func TestResolveDelegatedIdentityCarriesAgentIdentity(t *testing.T) {
	e := newDelegEnv(t)
	const agentRef = "agent-external-7"
	token, _ := e.mint(t, auth.MintDelegationRequest{AgentRef: agentRef})
	d := delegDecider(e.a, fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()})

	id, _, deny, ok := d.resolveDelegatedIdentity(e.ctx, e.pep, delegHandleProof(token), delegPresented("nonce-agent", "messages"))
	if !ok {
		t.Fatalf("expected a sealed delegated identity; got deny code=%q status=%d", deny.code, deny.decision.Status)
	}
	if id.principal.AgentIdentity != agentRef {
		t.Errorf("principal AgentIdentity = %q, want %q", id.principal.AgentIdentity, agentRef)
	}
	if id.sessionRef != agentRef {
		t.Errorf("sessionRef = %q, want the agent ref %q", id.sessionRef, agentRef)
	}
}

// --- failure mapping table (adapter concern, isolated via a stub verifier) ------

func TestResolveDelegatedIdentityFailureMapping(t *testing.T) {
	rows := []struct {
		name    string
		err     error
		code    gateCode
		class   sdk.FailureClass
		status  int
		errType string
	}{
		{"protocol", auth.ErrDelegationProtocol, gateCodeDelegationProtocol, sdk.FailureProtocolError, http.StatusBadRequest, "invalid_request_error"},
		{"invalid", auth.ErrDelegationInvalid, gateCodeDelegationInvalid, sdk.FailureDelegationInvalid, http.StatusForbidden, "permission_error"},
		{"replay", auth.ErrDelegationReplay, gateCodeDelegationReplay, sdk.FailureReplay, http.StatusConflict, "invalid_request_error"},
		{"plane", errors.New("store down"), gateCodeDelegationPlaneUnavailable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			d := delegDecider(stubDelegationVerifier{err: row.err}, fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()})
			id, outcome, deny, ok := d.resolveDelegatedIdentity(context.Background(), auth.PEPIdentity{}, delegHandleProof("olvd_ignored"), delegPresented("n", "messages"))
			if ok {
				t.Fatal("a delegation fault must deny")
			}
			if id.ok || id.principal.Kind != "" || !id.tenant.IsZero() {
				t.Errorf("identity must be the UNSEALED zero value; got ok=%v kind=%q tenant=%q", id.ok, id.principal.Kind, id.tenant)
			}
			if !outcome.DecisionID().IsZero() || outcome.Retried() {
				t.Error("outcome must be the zero VerifiedDelegation on a fault")
			}
			if deny.code != row.code || deny.class != row.class {
				t.Errorf("semantics = %q/%q, want %q/%q", deny.code, deny.class, row.code, row.class)
			}
			if deny.decision.Status != row.status || deny.decision.ErrorType != row.errType {
				t.Errorf("presentation = %d/%q, want %d/%q", deny.decision.Status, deny.decision.ErrorType, row.status, row.errType)
			}
			// The reason prose is a static, non-sensitive string.
			if deny.decision.Reason == "" {
				t.Error("a deny must carry a human-readable reason")
			}
		})
	}
}

// TestResolveDelegatedIdentityReservedSchemeReason pins L2: the reserved
// "actas-token" scheme maps to the SAME protocol failure class as a malformed
// proof (400 / invalid_request_error) but carries a DISTINCT, stable reason, so a
// caller can tell an unimplemented scheme apart from a malformed token.
func TestResolveDelegatedIdentityReservedSchemeReason(t *testing.T) {
	e := newDelegEnv(t)
	d := delegDecider(e.a, fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()})

	_, _, reserved, ok := d.resolveDelegatedIdentity(e.ctx, e.pep,
		auth.DelegationProofInput{Scheme: "actas-token", Token: []byte("olvd_ignored")},
		delegPresented("n-reserved", "messages"))
	if ok {
		t.Fatal("reserved scheme must deny")
	}
	_, _, malformed, ok := d.resolveDelegatedIdentity(e.ctx, e.pep,
		auth.DelegationProofInput{Scheme: "handle", Token: []byte("olvk_wrong_prefix")},
		delegPresented("n-malformed", "messages"))
	if ok {
		t.Fatal("malformed token must deny")
	}

	if reserved.decision.Status != http.StatusBadRequest || reserved.class != sdk.FailureProtocolError || reserved.decision.ErrorType != "invalid_request_error" {
		t.Errorf("reserved deny = %d/%q/%q, want 400/protocol_error/invalid_request_error",
			reserved.decision.Status, reserved.class, reserved.decision.ErrorType)
	}
	if reserved.decision.Reason == malformed.decision.Reason {
		t.Errorf("reserved and malformed reasons must differ; both = %q", reserved.decision.Reason)
	}
	if !strings.Contains(reserved.decision.Reason, "reserved") {
		t.Errorf("reserved reason = %q, want it to mention 'reserved'", reserved.decision.Reason)
	}
}

// TestResolveDelegatedIdentityTenantMismatchDeniesClosed pins the defense-in-depth
// guard: a verify that "succeeded" but yielded a delegation whose tenant does not
// match the authenticated PEP (here: a zero tenant) is refused, never sealed.
func TestResolveDelegatedIdentityTenantMismatchDeniesClosed(t *testing.T) {
	// Zero VerifiedDelegation ⇒ Tenant() is zero; a zero PEPIdentity ⇒ Tenant() is
	// zero too, so the guard's IsZero() clause is what denies (never seal a subject
	// with no governed tenant).
	d := delegDecider(stubDelegationVerifier{}, fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()})
	id, _, deny, ok := d.resolveDelegatedIdentity(context.Background(), auth.PEPIdentity{}, delegHandleProof("olvd_x"), delegPresented("n", "messages"))
	if ok || id.ok {
		t.Fatal("a delegation with no matching governed tenant must deny closed")
	}
	if deny.code != gateCodeDelegationInvalid || deny.class != sdk.FailureDelegationInvalid || deny.decision.Status != http.StatusForbidden {
		t.Errorf("semantics = %q/%q/%d, want delegation_invalid/delegation_invalid/403", deny.code, deny.class, deny.decision.Status)
	}
}

// TestResolveDelegatedIdentityPolicyPlaneUnreadable reuses the bearer adapter's
// deny-closed posture: a verified delegation whose per-tenant governance config
// cannot be read denies 503 (a security proxy that cannot decide must not forward).
func TestResolveDelegatedIdentityPolicyPlaneUnreadable(t *testing.T) {
	e := newDelegEnv(t)
	token, _ := e.mint(t, auth.MintDelegationRequest{})
	d := delegDecider(e.a, fakeProxyPolicy{err: errors.New("policy store down")})

	id, _, deny, ok := d.resolveDelegatedIdentity(e.ctx, e.pep, delegHandleProof(token), delegPresented("nonce-plane", "messages"))
	if ok || id.ok {
		t.Fatal("an unreadable policy plane must deny closed")
	}
	if deny.code != gateCodePolicyUnreadable || deny.class != sdk.FailurePlaneUnavailable || deny.decision.Status != http.StatusServiceUnavailable {
		t.Errorf("semantics = %q/%q/%d, want policy_unreadable/plane_unavailable/503", deny.code, deny.class, deny.decision.Status)
	}
}

// TestResolveDelegatedIdentityRetriedPathSurfacesStoredDecision pins the outcome
// passthrough the future service composition depends on: after a claim is
// finalized, an identical retry through the adapter resolves the SAME decision and
// surfaces the stored verdict, still sealed and allowed.
func TestResolveDelegatedIdentityRetriedPathSurfacesStoredDecision(t *testing.T) {
	e := newDelegEnv(t)
	token, _ := e.mint(t, auth.MintDelegationRequest{})
	d := delegDecider(e.a, fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()})
	pr := delegPresented("nonce-retry", "messages")

	_, firstOutcome, _, ok := d.resolveDelegatedIdentity(e.ctx, e.pep, delegHandleProof(token), pr)
	if !ok {
		t.Fatalf("first claim must seal")
	}
	if firstOutcome.Retried() {
		t.Error("first claim must not be a retry")
	}

	verdict := []byte(`{"decision":"allow"}`)
	if err := e.a.FinalizeDecisionClaim(e.ctx, firstOutcome.DecisionID(), verdict, "policy-v1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	id, outcome, _, ok := d.resolveDelegatedIdentity(e.ctx, e.pep, delegHandleProof(token), pr)
	if !ok || !id.ok {
		t.Fatal("an idempotent retry must still seal an allowed identity")
	}
	if !outcome.Retried() {
		t.Error("the second identical claim must be a retry")
	}
	if outcome.DecisionID() != firstOutcome.DecisionID() {
		t.Errorf("retry decision = %q, want %q", outcome.DecisionID(), firstOutcome.DecisionID())
	}
	if outcome.StoredVerdictJSON() != string(verdict) {
		t.Errorf("retry stored verdict = %q, want %q", outcome.StoredVerdictJSON(), verdict)
	}
	// The retried identity still names the subject.
	if id.subjectID != e.subjectUserID.String() {
		t.Errorf("retried subjectID = %q, want %q", id.subjectID, e.subjectUserID)
	}
}
