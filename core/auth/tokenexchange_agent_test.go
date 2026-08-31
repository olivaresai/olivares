// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// mockAgentChecker is a test double for auth.AgentLifecycleChecker.
// validAgents maps agentRef → expected sponsorRef; blockedAgents marks
// agents that are enforcement-blocked.
type mockAgentChecker struct {
	validAgents   map[string]string // agentRef → expected sponsorRef
	blockedAgents map[string]bool
}

func (m *mockAgentChecker) CheckAgentForExchange(_ context.Context, _ model.TenantID, agentRef, sponsorRef string) error {
	if m.blockedAgents[agentRef] {
		return errors.New("agent is blocked by enforcement policy")
	}
	expected, ok := m.validAgents[agentRef]
	if !ok {
		return errors.New("agent not found or not registered as an agent identity")
	}
	if expected != sponsorRef {
		return errors.New("sponsor mismatch: subject is not the registered sponsor")
	}
	return nil
}

// agentExchangeFixture builds a tenant with a user who has a SCIM externalId
// set (the convergence anchor for sponsor matching) and a session token. The
// session carries UserID = sponsor.ID, so resolveUserExternalID can look up
// the externalId from the user row.
type agentExchangeFixture struct {
	a            *auth.Authenticator
	st           store.Store
	tenant       model.TenantID
	sponsorExtID string // the user's SCIM externalId (stored as sponsor_ref in NHI)
	sessionTok   string // session token (subject for agent-OBO exchange)
	sessionExp   model.Timestamp
	clock        *stepClock
}

func newAgentExchangeFixture(t *testing.T) agentExchangeFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme-agent")

	const sponsorExtID = "idp-sponsor-1"

	// 1. Create the sponsor user with a password (so we can log in).
	sponsor, err := a.CreateUser(ctx, super, auth.NewUser{
		Email:    "sponsor@acme.com",
		Password: "sponsor-pass-123",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// 2. Grant tenant membership so SCIMUpdateUser can find them.
	if _, err := a.GrantMembership(ctx, super, sponsor.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}

	// 3. Set ExternalID via SCIM update (the convergence anchor).
	if _, err := a.SCIMUpdateUser(ctx, super, tenant, sponsor.ID, auth.SCIMUserInput{
		UserName:    "sponsor@acme.com",
		DisplayName: "Agent Sponsor",
		ExternalID:  sponsorExtID,
		Active:      true,
	}); err != nil {
		t.Fatalf("SCIMUpdateUser: %v", err)
	}

	// 4. Login as the sponsor — the session token carries UserID = sponsor.ID,
	// which is what resolveUserExternalID uses to look up the externalId.
	sessionTok, session, err := a.Login(ctx, "sponsor@acme.com", "sponsor-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login sponsor: %v", err)
	}

	return agentExchangeFixture{
		a: a, st: st, tenant: tenant,
		sponsorExtID: sponsorExtID,
		sessionTok:   sessionTok,
		sessionExp:   session.ExpiresAt,
		clock:        clock,
	}
}

// TestExchangeAgentOBO_Success verifies a valid agent-OBO exchange:
// the subject is the sponsor's session token, requested_actor names a valid
// agent, and the minted token carries AgentRef.
func TestExchangeAgentOBO_Success(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	const agentRef = "ext-agent-claude-1"

	checker := &mockAgentChecker{
		validAgents: map[string]string{agentRef: f.sponsorExtID},
	}
	f.a.SetAgentLifecycleChecker(checker)

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = agentRef

	res, err := f.a.ExchangeToken(ctx, caller, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Stored.AgentRef != agentRef {
		t.Errorf("AgentRef = %q, want %q", res.Stored.AgentRef, agentRef)
	}
	child, err := f.a.Authenticate(ctx, res.AccessToken)
	if err != nil {
		t.Fatalf("authenticate child token: %v", err)
	}
	if child.AgentIdentity != agentRef {
		t.Errorf("child principal AgentIdentity = %q, want %q", child.AgentIdentity, agentRef)
	}
	if res.Stored.Role == "" {
		t.Error("child token must have a role")
	}
}

func TestExchangeSessionSubjectHasNoTokenParentAndClampsExpiry(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	// Leave less than the exchange TTL on the session so this control proves
	// credentialExpiry's KindUser branch actually clamps the child.
	f.clock.advance(auth.DefaultSessionTTL - 2*time.Minute)
	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatalf("authenticate session subject: %v", err)
	}
	res, err := f.a.ExchangeToken(ctx, caller, accessReq(f.sessionTok))
	if err != nil {
		t.Fatalf("exchange session subject: %v", err)
	}
	if !res.Stored.ParentTokenID.IsZero() {
		t.Fatalf("session exchange parent_token_id = %s, want zero", res.Stored.ParentTokenID)
	}
	if res.Stored.ExpiresAt == nil ||
		!res.Stored.ExpiresAt.Time().Equal(f.sessionExp.Time()) {
		t.Fatalf("session exchange expiry = %v, want clamp to %s", res.Stored.ExpiresAt, f.sessionExp)
	}
}

// TestExchangeAgentOBO_AuditMetaIncludesAgentRef verifies that a successful
// agent-OBO exchange records agent_ref in the audit event's meta.
func TestExchangeAgentOBO_AuditMetaIncludesAgentRef(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	const agentRef = "ext-agent-claude-audit-test"

	checker := &mockAgentChecker{
		validAgents: map[string]string{agentRef: f.sponsorExtID},
	}
	f.a.SetAgentLifecycleChecker(checker)

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = agentRef

	res, err := f.a.ExchangeToken(ctx, caller, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Stored.AgentRef != agentRef {
		t.Errorf("AgentRef = %q, want %q", res.Stored.AgentRef, agentRef)
	}

	// Verify the audit event's meta includes agent_ref with the correct value
	var foundAuditEvent bool
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		cw, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose WalkCanonical")
		}
		return cw.WalkCanonical(ctx, 1, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			if ev.Action == "token.exchange" && ev.TargetID == res.Stored.ID {
				// Check that agent_ref is present and equals the expected value
				if strings.Contains(metaCanonical, `"agent_ref":"ext-agent-claude-audit-test"`) {
					foundAuditEvent = true
				} else {
					t.Errorf("audit meta does not contain expected agent_ref: %s", metaCanonical)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("reading audit: %v", err)
	}

	if !foundAuditEvent {
		t.Error("no token.exchange audit event found with correct agent_ref in meta")
	}
}

// TestExchangeAgentOBO_BlockedAgent verifies that requesting a blocked agent
// returns ErrAgentBlocked.
func TestExchangeAgentOBO_BlockedAgent(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	const agentRef = "ext-agent-blocked"

	checker := &mockAgentChecker{
		validAgents:   map[string]string{agentRef: f.sponsorExtID},
		blockedAgents: map[string]bool{agentRef: true},
	}
	f.a.SetAgentLifecycleChecker(checker)

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = agentRef

	if _, err := f.a.ExchangeToken(ctx, caller, req); !errors.Is(err, auth.ErrAgentBlocked) {
		t.Errorf("blocked agent: err = %v, want ErrAgentBlocked", err)
	}
}

// TestExchangeAgentOBO_SponsorMismatch verifies that a subject who is NOT the
// agent's registered sponsor is refused with ErrAgentBlocked.
func TestExchangeAgentOBO_SponsorMismatch(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	const agentRef = "ext-agent-other-sponsor"

	// The agent is registered under a DIFFERENT sponsor than f.sponsorExtID.
	checker := &mockAgentChecker{
		validAgents: map[string]string{agentRef: "idp-other-sponsor"},
	}
	f.a.SetAgentLifecycleChecker(checker)

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = agentRef

	if _, err := f.a.ExchangeToken(ctx, caller, req); !errors.Is(err, auth.ErrAgentBlocked) {
		t.Errorf("wrong sponsor: err = %v, want ErrAgentBlocked", err)
	}
}

// TestExchangeAgentOBO_NoChecker verifies that requesting an agent-OBO
// exchange when no lifecycle checker is configured returns ErrInvalidExchange.
func TestExchangeAgentOBO_NoChecker(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	// Deliberately do NOT set a checker.

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = "ext-agent-1"

	if _, err := f.a.ExchangeToken(ctx, caller, req); !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("no checker: err = %v, want ErrInvalidExchange", err)
	}
}

// TestExchangeAgentOBO_DownScopeStillApplies verifies that the down-scope
// clamp still applies even when agent-OBO is in effect (the child's authority
// cannot exceed the subject's).
func TestExchangeAgentOBO_DownScopeStillApplies(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	const agentRef = "ext-agent-scope-test"

	checker := &mockAgentChecker{
		validAgents: map[string]string{agentRef: f.sponsorExtID},
	}
	f.a.SetAgentLifecycleChecker(checker)

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	// Subject is editor-role (write tier); request read only — should be narrowed.
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = agentRef
	req.Scope = []string{"read"}

	res, err := f.a.ExchangeToken(ctx, caller, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Stored.Role != auth.RoleViewer {
		t.Errorf("child role = %q, want viewer (read down-scope)", res.Stored.Role)
	}
	if res.Stored.AgentRef != agentRef {
		t.Errorf("AgentRef = %q, want %q", res.Stored.AgentRef, agentRef)
	}
	if !res.Narrowed {
		t.Error("expected Narrowed=true (read < editor)")
	}
}

// TestExchangeAgentOBO_AgentRefEmptyWithoutRequestedActor verifies that a
// plain exchange (no requested_actor) does NOT set AgentRef on the minted token,
// and that agent_ref is NOT included in the audit meta.
func TestExchangeAgentOBO_AgentRefEmptyWithoutRequestedActor(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)

	checker := &mockAgentChecker{
		validAgents: map[string]string{"ext-agent-1": f.sponsorExtID},
	}
	f.a.SetAgentLifecycleChecker(checker)

	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	// Plain exchange — no requested_actor.
	res, err := f.a.ExchangeToken(ctx, caller, accessReq(f.sessionTok))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Stored.AgentRef != "" {
		t.Errorf("AgentRef should be empty on non-agent exchange, got %q", res.Stored.AgentRef)
	}
	child, err := f.a.Authenticate(ctx, res.AccessToken)
	if err != nil {
		t.Fatalf("authenticate child token: %v", err)
	}
	if child.AgentIdentity != "" {
		t.Errorf("child principal AgentIdentity should be empty on non-agent exchange, got %q", child.AgentIdentity)
	}

	// Verify the audit event's meta does NOT include agent_ref
	var foundAuditEvent bool
	var hasAgentRef bool
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		cw, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose WalkCanonical")
		}
		return cw.WalkCanonical(ctx, 1, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			if ev.Action == "token.exchange" && ev.TargetID == res.Stored.ID {
				foundAuditEvent = true
				// Check that agent_ref is NOT present in the meta
				if strings.Contains(metaCanonical, `"agent_ref"`) {
					hasAgentRef = true
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("reading audit: %v", err)
	}

	if !foundAuditEvent {
		t.Error("no token.exchange audit event found")
	}
	if hasAgentRef {
		t.Error("audit meta should not contain agent_ref for non-agent exchange")
	}
}
