// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// --- EMA grant tests --------------------------------------------------------

func TestEMAGrant_Success(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:    "https://idp.example.com",
		Subject:   "user-ext-001",
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
		Scopes:    []string{"tools", "resources"},
		Email:     "ema-dev@acme.com",
	}

	res, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil)
	if err != nil {
		t.Fatalf("Grant = %v", err)
	}
	if res.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", res.TokenType)
	}
	if res.AccessToken == "" {
		t.Error("AccessToken is empty")
	}
	if res.ExpiresIn <= 0 {
		t.Errorf("ExpiresIn = %d, want >0", res.ExpiresIn)
	}
	if !strings.Contains(res.Scope, "tools") || !strings.Contains(res.Scope, "resources") {
		t.Errorf("Scope = %q, want to contain tools and resources", res.Scope)
	}
}

func TestEMAGrant_ScopeNarrowing(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:    "https://idp.example.com",
		Subject:   "user-ext-001",
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
		Scopes:    []string{"tools", "resources", "admin"},
	}

	res, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", []string{"tools"})
	if err != nil {
		t.Fatalf("Grant = %v", err)
	}
	if res.Scope != "tools" {
		t.Errorf("Scope = %q, want narrowed to 'tools'", res.Scope)
	}
}

func TestEMAGrant_AssertionValidationFailure(t *testing.T) {
	g, receiver := newEMAGrant(t)
	receiver.err = errors.New("mcp: idjag: assertion signature: invalid_grant")

	_, err := g.Grant(context.Background(), "bad-assertion", "client-abc", "https://mcp.example.com", nil)
	if err == nil {
		t.Fatal("expected error for bad assertion")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error = %v, want to contain invalid_grant", err)
	}
}

func TestEMAGrant_NoLinkedUser(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:    "https://idp.example.com",
		Subject:   "unknown-subject-id",
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
	}

	_, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil)
	if err == nil {
		t.Fatal("expected error for unlinked user")
	}
	if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("error = %v, want ErrInvalidExchange", err)
	}
}

func TestEMAGrant_EmailFallback(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:    "https://idp.example.com",
		Subject:   "unknown-external-id",
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
		Scopes:    []string{"tools"},
		Email:     "ema-dev@acme.com",
		// Single-IdP deployment: the sole trusted issuer is legitimately
		// authoritative for every account, so the pre-U3 SCIM email fallback applies.
		SoleTrustedIssuer: true,
	}

	res, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil)
	if err != nil {
		t.Fatalf("Grant with email fallback = %v", err)
	}
	if res.AccessToken == "" {
		t.Error("AccessToken is empty (email fallback should resolve the user)")
	}
}

// TestEMAGrant_SSOSubjectPrimary proves the primary correlation is the (issuer,
// subject) PAIR: the correct issuer + subject resolves via the issuer-qualified
// sso_subject even with NO email hint (so it cannot be the email fallback doing it).
func TestEMAGrant_SSOSubjectPrimary(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:    "https://idp.example.com",
		Subject:   "user-ext-001",
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
		Scopes:    []string{"tools"},
		// No Email: resolution MUST come from the issuer-qualified sso_subject.
	}

	res, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil)
	if err != nil {
		t.Fatalf("Grant via sso_subject = %v", err)
	}
	if res.AccessToken == "" {
		t.Error("AccessToken empty — the issuer-qualified sso_subject should have resolved the user")
	}
}

// TestEMAGrant_CrossIdPCollisionRefused is the security regression test. The seeded
// account is bound (sso_subject) to issuer https://idp.example.com with subject
// "user-ext-001" (and carries that same value as its unqualified external_id). A
// DIFFERENT trusted issuer that asserts the SAME bare subject must NOT resolve to that
// account — the cross-IdP identity confusion the old unqualified external_id lookup
// allowed. No email is asserted, so the email fallback cannot mask the collision.
func TestEMAGrant_CrossIdPCollisionRefused(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:    "https://evil-idp.example.com", // a different (also trusted) issuer
		Subject:   "user-ext-001",                 // the victim's bare subject / external_id
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
		// No Email: isolate the subject path — the collision must be refused on its own.
	}

	_, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil)
	if err == nil {
		t.Fatal("cross-IdP subject collision resolved a foreign account — must be refused")
	}
	if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("error = %v, want ErrInvalidExchange (no linked local identity)", err)
	}
}

// TestEMAGrant_SuperadminEmailRefused proves the EMA path refuses a superadmin the
// same way the SSO login path does (federation_login.go:76-79). A superadmin is a
// local, first-party account with no sso_subject, so an assertion can only reach it
// via the verified-email fallback; minting an unattended token for the cross-tenant
// root — e.g. from a second, differently-trusted IdP asserting the root's email —
// must be refused.
func TestEMAGrant_SuperadminEmailRefused(t *testing.T) {
	st := testStore(t)
	a := auth.NewAuthenticator(st, model.SystemClock{})
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Users().Create(ctx, model.User{
			Email:        "root@acme.com",
			DisplayName:  "Root",
			Status:       model.StatusActive,
			IsSuperadmin: true, // local first-party account; no sso_subject, no external_id
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiver := &stubEMAReceiver{result: auth.EMAResult{
		Issuer:    "https://idp.example.com",
		Subject:   "some-idp-subject",
		ClientID:  "client-abc",
		Resources: []string{"https://mcp.example.com"},
		Email:     "root@acme.com", // the superadmin's email — the takeover vector
		// Single-IdP: let the email fallback REACH the superadmin gate, so this test
		// proves the superadmin refusal itself (not the domain-authority gate).
		SoleTrustedIssuer: true,
	}}
	g, err := auth.NewEMAGrant(auth.EMAGrantConfig{
		Receiver:   receiver,
		SigningKey: priv,
		Issuer:     "https://as.example.com",
		TokenTTL:   5 * time.Minute,
		Clock:      func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) },
	}, a)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := g.Grant(ctx, "valid-assertion", "client-abc", "https://mcp.example.com", nil); err == nil {
		t.Fatal("EMA minted a token for a superadmin via email fallback — must be refused")
	} else if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("error = %v, want ErrInvalidExchange", err)
	}
}

// TestEMAGrant_CrossIdPEmailTakeoverRefused is the F1 security regression: a
// multi-IdP EMA keyring where a trusted-but-domain-scoped issuer asserts the VICTIM's
// email in a domain it does NOT own. Step-1 (issuer-qualified subject) misses because
// no account is bound to the attacker issuer; the verified-email fallback must be
// DENIED because the asserting issuer is not authoritative for the email's domain —
// otherwise EMA mints a victim-bound token (cross-IdP account takeover, defeating the
// same domain boundary the first-party SSO path enforces at handlers_federation.go).
func TestEMAGrant_CrossIdPEmailTakeoverRefused(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:               "https://evil-idp.example.com", // a different (also trusted) issuer
		Subject:              "attacker-subject",             // no local account bound to this issuer
		ClientID:             "client-abc",
		Resources:            []string{"https://mcp.example.com"},
		Email:                "ema-dev@acme.com",           // the victim's email — the takeover vector
		IssuerClaimedDomains: []string{"evil.example.com"}, // authoritative ONLY for evil.example.com
		SoleTrustedIssuer:    false,                        // ≥2 trusted issuers
	}

	if _, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil); err == nil {
		t.Fatal("EMA resolved the victim by email for an issuer not authoritative for acme.com — cross-IdP takeover")
	} else if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("error = %v, want ErrInvalidExchange (email fallback denied → no linked identity)", err)
	}
}

// TestEMAGrant_EmailFallbackDomainAuthoritative proves the fix does not break the
// legitimate case: in a MULTI-issuer deployment an issuer that CLAIMS the email's
// domain may still vouch for that account by email (domain authority, not
// sole-issuer, authorizes the fallback).
func TestEMAGrant_EmailFallbackDomainAuthoritative(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:               "https://idp.example.com",
		Subject:              "unknown-external-id", // step-1 misses → email fallback
		ClientID:             "client-abc",
		Resources:            []string{"https://mcp.example.com"},
		Scopes:               []string{"tools"},
		Email:                "ema-dev@acme.com",
		IssuerClaimedDomains: []string{"ACME.com"}, // authoritative for acme.com (case-normalized)
		SoleTrustedIssuer:    false,                // multi-issuer — authority comes from the claim
	}

	res, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil)
	if err != nil {
		t.Fatalf("domain-authoritative email fallback = %v", err)
	}
	if res.AccessToken == "" {
		t.Error("AccessToken empty — a domain-authoritative issuer should resolve the account by email")
	}
}

// TestEMAGrant_EmailFallbackUnconstrainedMultiIssuerDenied proves the interim-guard
// subset: an issuer that claims NO domains is authoritative for every account ONLY as
// the sole trusted issuer; in a multi-issuer keyring the bare-email fallback is denied.
func TestEMAGrant_EmailFallbackUnconstrainedMultiIssuerDenied(t *testing.T) {
	g, receiver := newEMAGrant(t)

	receiver.result = auth.EMAResult{
		Issuer:               "https://idp.example.com",
		Subject:              "unknown-external-id",
		ClientID:             "client-abc",
		Resources:            []string{"https://mcp.example.com"},
		Email:                "ema-dev@acme.com",
		IssuerClaimedDomains: nil,   // unconstrained
		SoleTrustedIssuer:    false, // but NOT the sole issuer
	}

	if _, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil); err == nil {
		t.Fatal("unconstrained issuer in a multi-issuer keyring resolved by bare email — must be denied")
	} else if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("error = %v, want ErrInvalidExchange", err)
	}
}

func TestEMAGrant_NilReceiver(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, err := auth.NewEMAGrant(auth.EMAGrantConfig{
		Receiver:   nil,
		SigningKey: priv,
		Issuer:     "https://as.example.com",
	}, nil)
	if err == nil {
		t.Fatal("expected error for nil receiver")
	}
	if !errors.Is(err, auth.ErrEMAUnavailable) {
		t.Errorf("error = %v, want ErrEMAUnavailable", err)
	}
}

func TestEMAGrant_NilGrant(t *testing.T) {
	var g *auth.EMAGrant
	_, err := g.Grant(context.Background(), "x", "y", "z", nil)
	if !errors.Is(err, auth.ErrEMAUnavailable) {
		t.Errorf("nil grant call = %v, want ErrEMAUnavailable", err)
	}
}

// --- PrincipalForExternalID tests -------------------------------------------

func TestPrincipalForExternalID_Found(t *testing.T) {
	a, _ := newAuthWithEMAUser(t)
	p, found, err := a.PrincipalForExternalID(context.Background(), "user-ext-001", auth.AAL1)
	if err != nil {
		t.Fatalf("PrincipalForExternalID = %v", err)
	}
	if !found {
		t.Fatal("expected found=true for user with matching external_id")
	}
	if p.UserID.IsZero() {
		t.Error("UserID is zero")
	}
}

func TestPrincipalForExternalID_NotFound(t *testing.T) {
	a, _ := newAuthWithEMAUser(t)
	_, found, err := a.PrincipalForExternalID(context.Background(), "nonexistent", auth.AAL1)
	if err != nil {
		t.Fatalf("PrincipalForExternalID = %v", err)
	}
	if found {
		t.Error("expected found=false for nonexistent external_id")
	}
}

func TestPrincipalForExternalID_Empty(t *testing.T) {
	a, _ := newAuthWithEMAUser(t)
	_, found, err := a.PrincipalForExternalID(context.Background(), "", auth.AAL1)
	if err != nil {
		t.Fatalf("PrincipalForExternalID = %v", err)
	}
	if found {
		t.Error("expected found=false for empty external_id")
	}
}

// --- narrowScopes tests (exported for testing via NarrowScopesForTest) -------

func TestNarrowScopes(t *testing.T) {
	tests := []struct {
		name      string
		granted   []string
		requested []string
		want      string
	}{
		{"no narrowing", []string{"a", "b"}, nil, "a b"},
		{"subset", []string{"a", "b", "c"}, []string{"a", "c"}, "a c"},
		{"disjoint", []string{"a", "b"}, []string{"x"}, ""},
		{"empty granted", nil, []string{"a"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auth.NarrowScopesForTest(tt.granted, tt.requested)
			gotStr := strings.Join(got, " ")
			if gotStr != tt.want {
				t.Errorf("narrowScopes = %q, want %q", gotStr, tt.want)
			}
		})
	}
}

// --- helpers ----------------------------------------------------------------

type stubEMAReceiver struct {
	result auth.EMAResult
	err    error
}

func (s *stubEMAReceiver) ValidateAssertion(_ context.Context, _, _ string) (auth.EMAResult, error) {
	return s.result, s.err
}

func newAuthWithEMAUser(t *testing.T) (*auth.Authenticator, store.Store) {
	t.Helper()
	st := testStore(t)
	a := auth.NewAuthenticator(st, model.SystemClock{})

	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Users().Create(ctx, model.User{
			Email:       "ema-dev@acme.com",
			DisplayName: "EMA Dev",
			Status:      model.StatusActive,
			ExternalID:  "user-ext-001",
			// Bound (via a prior federated login) to ONE issuer. The issuer-qualified
			// key is the cross-IdP-safe correlation the EMA path now uses (U3).
			SsoSubject: auth.FederatedIdentity{
				Issuer: "https://idp.example.com", Subject: "user-ext-001",
			}.QualifiedSubject(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return a, st
}

func newEMAGrant(t *testing.T) (*auth.EMAGrant, *stubEMAReceiver) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newAuthWithEMAUser(t)
	receiver := &stubEMAReceiver{}
	g, err := auth.NewEMAGrant(auth.EMAGrantConfig{
		Receiver:   receiver,
		SigningKey: priv,
		Issuer:     "https://as.example.com",
		TokenTTL:   5 * time.Minute,
		Clock:      func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) },
	}, a)
	if err != nil {
		t.Fatal(err)
	}
	return g, receiver
}

// TestCampaignEMACrossIdPEmailFallbackTakeover is the re-runnable security-campaign
// regression for F1 (task security:campaign → go test -run Campaign ./...): a
// trusted-but-domain-scoped EMA issuer in a MULTI-IdP keyring must not resolve a
// victim's account via the verified-email fallback for a domain it does not own. It is
// the durable half of the harness for the EMA email-takeover finding.
func TestCampaignEMACrossIdPEmailFallbackTakeover(t *testing.T) {
	g, receiver := newEMAGrant(t) // seeds ema-dev@acme.com, sso-bound to https://idp.example.com

	receiver.result = auth.EMAResult{
		Issuer:               "https://evil-idp.example.com",
		Subject:              "attacker-subject",
		ClientID:             "client-abc",
		Resources:            []string{"https://mcp.example.com"},
		Email:                "ema-dev@acme.com",           // victim's email
		IssuerClaimedDomains: []string{"evil.example.com"}, // NOT authoritative for acme.com
		SoleTrustedIssuer:    false,                        // ≥2 trusted issuers
	}

	if _, err := g.Grant(context.Background(), "valid-assertion", "client-abc", "https://mcp.example.com", nil); err == nil {
		t.Fatal("F1 regression: EMA minted a victim-bound token via the cross-domain email fallback")
	} else if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("error = %v, want ErrInvalidExchange", err)
	}
}
