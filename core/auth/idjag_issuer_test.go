// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// ─── helpers ────────────────────────────────────────────────────────────────

const (
	testIssuer   = "https://as.example.com"
	testAudience = "https://target-as.example.com"
	testResource = "https://mcp.example.com/server"
	testClient   = "client-abc"
	testAgent    = "agent-ext-001"
	testSponsor  = "sponsor-ext-001"
)

func newTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func newValidIssuer(t *testing.T, priv ed25519.PrivateKey, checker auth.AgentLifecycleChecker) *auth.IDJAGIssuer {
	t.Helper()
	iss, err := auth.NewIDJAGIssuer(auth.IDJAGIssuerConfig{
		SigningKey:        priv,
		Issuer:            testIssuer,
		DefaultTTL:        5 * time.Minute,
		Checker:           checker,
		ApprovedResources: []string{testResource},
		Clock:             func() time.Time { return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewIDJAGIssuer: %v", err)
	}
	return iss
}

func defaultRequest() auth.IDJAGRequest {
	return auth.IDJAGRequest{
		AgentRef:   testAgent,
		ClientID:   testClient,
		Resource:   testResource,
		Audience:   testAudience,
		Scope:      []string{"tools:read"},
		Tenant:     model.TenantID("tenant-001"),
		SponsorRef: testSponsor,
	}
}

// parseIDJAG parses and signature-verifies a JWT against the given public key
// and returns the standard+extended claims. Fails the test on any error.
func parseIDJAG(t *testing.T, token string, pub ed25519.PublicKey) (jwt.Claims, auth.IDJAGClaims) {
	t.Helper()
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		t.Fatalf("ParseSigned: %v", err)
	}
	var std jwt.Claims
	var ext auth.IDJAGClaims
	if err := parsed.Claims(pub, &std, &ext); err != nil {
		t.Fatalf("Claims (signature verify): %v", err)
	}
	return std, ext
}

// ─── Test: issue a valid ID-JAG ──────────────────────────────────────────────

func TestIDJAGIssuer_Issue_Success(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	ctx := context.Background()

	token, err := iss.Issue(ctx, defaultRequest())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("Issue returned empty token")
	}

	// Parse and verify the JWT.
	std, ext := parseIDJAG(t, token, pub)

	if std.Issuer != testIssuer {
		t.Errorf("iss = %q, want %q", std.Issuer, testIssuer)
	}
	if std.Subject != testAgent {
		t.Errorf("sub = %q, want %q", std.Subject, testAgent)
	}
	if std.ID == "" {
		t.Error("jti must be non-empty")
	}
	if std.Expiry == nil {
		t.Error("exp must be present")
	}
	if std.IssuedAt == nil {
		t.Error("iat must be present")
	}
	if std.NotBefore == nil {
		t.Error("nbf must be present")
	}
	if ext.ClientID != testClient {
		t.Errorf("client_id = %q, want %q", ext.ClientID, testClient)
	}
	if len(ext.Resources) != 1 || ext.Resources[0] != testResource {
		t.Errorf("resource = %v, want [%q]", ext.Resources, testResource)
	}
	if ext.SponsorRef != testSponsor {
		t.Errorf("sponsor_ref = %q, want %q", ext.SponsorRef, testSponsor)
	}
	if ext.AgentKind != "agent" {
		t.Errorf("agent_kind = %q, want %q", ext.AgentKind, "agent")
	}
}

// ─── Test: typ header ────────────────────────────────────────────────────────

func TestIDJAGIssuer_Issue_TypHeader(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	token, err := iss.Issue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		t.Fatalf("ParseSigned: %v", err)
	}
	if len(parsed.Headers) == 0 {
		t.Fatal("no JOSE headers")
	}
	got := parsed.Headers[0].ExtraHeaders["typ"]
	if got != auth.IDJAGTyp {
		t.Errorf("typ = %v, want %q", got, auth.IDJAGTyp)
	}
}

// ─── Test: audience is a single string (draft §4.4.1 arity) ─────────────────

func TestIDJAGIssuer_Issue_AudienceArity(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	token, err := iss.Issue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	std, _ := parseIDJAG(t, token, pub)
	aud := std.Audience
	if len(aud) != 1 {
		t.Errorf("aud has %d elements, want exactly 1", len(aud))
	}
	if len(aud) > 0 && aud[0] != testAudience {
		t.Errorf("aud[0] = %q, want %q", aud[0], testAudience)
	}
}

// ─── Test: TTL and timing ─────────────────────────────────────────────────────

func TestIDJAGIssuer_Issue_TimingClaims(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	fixedTime := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss, err := auth.NewIDJAGIssuer(auth.IDJAGIssuerConfig{
		SigningKey:        priv,
		Issuer:            testIssuer,
		DefaultTTL:        10 * time.Minute,
		Checker:           checker,
		ApprovedResources: []string{testResource},
		Clock:             func() time.Time { return fixedTime },
	})
	if err != nil {
		t.Fatalf("NewIDJAGIssuer: %v", err)
	}

	token, err := iss.Issue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	std, _ := parseIDJAG(t, token, pub)

	iat := std.IssuedAt.Time()
	exp := std.Expiry.Time()
	if !iat.Equal(fixedTime) {
		t.Errorf("iat = %v, want %v", iat, fixedTime)
	}
	wantExp := fixedTime.Add(10 * time.Minute)
	if !exp.Equal(wantExp) {
		t.Errorf("exp = %v, want %v", exp, wantExp)
	}
}

// ─── Test: jti uniqueness ─────────────────────────────────────────────────────

func TestIDJAGIssuer_Issue_JTIUnique(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	ctx := context.Background()

	tok1, err := iss.Issue(ctx, defaultRequest())
	if err != nil {
		t.Fatalf("Issue 1: %v", err)
	}
	tok2, err := iss.Issue(ctx, defaultRequest())
	if err != nil {
		t.Fatalf("Issue 2: %v", err)
	}

	std1, _ := parseIDJAG(t, tok1, pub)
	std2, _ := parseIDJAG(t, tok2, pub)
	if std1.ID == std2.ID {
		t.Errorf("jti must be unique per issuance, got same jti=%q twice", std1.ID)
	}
}

// ─── Test: signature verifiable with public key ───────────────────────────────

func TestIDJAGIssuer_Issue_SignatureVerifiable(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)

	token, err := iss.Issue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Verify with the correct public key — must succeed.
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		t.Fatalf("ParseSigned: %v", err)
	}
	var out jwt.Claims
	if err := parsed.Claims(pub, &out); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}

	// Verify with a DIFFERENT public key — must fail.
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := parsed.Claims(wrongPub, &out); err == nil {
		t.Error("signature verified with wrong key — should have failed")
	}
}

// ─── Test: deny-closed — no signing key ──────────────────────────────────────

func TestIDJAGIssuer_NewIssuer_NoKey(t *testing.T) {
	_, err := auth.NewIDJAGIssuer(auth.IDJAGIssuerConfig{
		Issuer:            testIssuer,
		ApprovedResources: []string{testResource},
		Checker:           &mockAgentChecker{},
	})
	if !errors.Is(err, auth.ErrIDJAGUnavailable) {
		t.Errorf("err = %v, want ErrIDJAGUnavailable", err)
	}
}

// ─── Test: deny-closed — no issuer ────────────────────────────────────────────

func TestIDJAGIssuer_NewIssuer_NoIssuer(t *testing.T) {
	_, priv := newTestKeyPair(t)
	_, err := auth.NewIDJAGIssuer(auth.IDJAGIssuerConfig{
		SigningKey:        priv,
		ApprovedResources: []string{testResource},
		Checker:           &mockAgentChecker{},
	})
	if !errors.Is(err, auth.ErrIDJAGUnavailable) {
		t.Errorf("err = %v, want ErrIDJAGUnavailable", err)
	}
}

// ─── Test: deny-closed — missing agent_ref ────────────────────────────────────

func TestIDJAGIssuer_Issue_MissingAgentRef(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	req := defaultRequest()
	req.AgentRef = ""

	_, err := iss.Issue(context.Background(), req)
	if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("err = %v, want ErrInvalidExchange", err)
	}
}

// ─── Test: deny-closed — missing client_id ────────────────────────────────────

func TestIDJAGIssuer_Issue_MissingClientID(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	req := defaultRequest()
	req.ClientID = ""

	_, err := iss.Issue(context.Background(), req)
	if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("err = %v, want ErrInvalidExchange", err)
	}
}

// ─── Test: deny-closed — missing audience ────────────────────────────────────

func TestIDJAGIssuer_Issue_MissingAudience(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	req := defaultRequest()
	req.Audience = ""

	_, err := iss.Issue(context.Background(), req)
	if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("err = %v, want ErrInvalidExchange", err)
	}
}

// ─── Test: deny-closed — missing resource ────────────────────────────────────

func TestIDJAGIssuer_Issue_MissingResource(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	req := defaultRequest()
	req.Resource = ""

	_, err := iss.Issue(context.Background(), req)
	if !errors.Is(err, auth.ErrInvalidExchange) {
		t.Errorf("err = %v, want ErrInvalidExchange", err)
	}
}

// ─── Test: deny-closed — unapproved resource ──────────────────────────────────

func TestIDJAGIssuer_Issue_UnapprovedResource(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)
	req := defaultRequest()
	req.Resource = "https://shadow-server.evil.com/mcp"

	_, err := iss.Issue(context.Background(), req)
	if !errors.Is(err, auth.ErrInvalidTarget) {
		t.Errorf("err = %v, want ErrInvalidTarget", err)
	}
}

// ─── Test: deny-closed — blocked agent ───────────────────────────────────────

func TestIDJAGIssuer_Issue_BlockedAgent(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents:   map[string]string{testAgent: testSponsor},
		blockedAgents: map[string]bool{testAgent: true},
	}
	iss := newValidIssuer(t, priv, checker)

	_, err := iss.Issue(context.Background(), defaultRequest())
	if !errors.Is(err, auth.ErrAgentBlocked) {
		t.Errorf("err = %v, want ErrAgentBlocked", err)
	}
}

// ─── Test: deny-closed — wrong sponsor ───────────────────────────────────────

func TestIDJAGIssuer_Issue_WrongSponsor(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: "other-sponsor"},
	}
	iss := newValidIssuer(t, priv, checker)
	req := defaultRequest()
	// req.SponsorRef = testSponsor — does NOT match "other-sponsor"

	_, err := iss.Issue(context.Background(), req)
	if !errors.Is(err, auth.ErrAgentBlocked) {
		t.Errorf("err = %v, want ErrAgentBlocked", err)
	}
}

// ─── Test: deny-closed — no lifecycle checker ────────────────────────────────

func TestIDJAGIssuer_Issue_NoChecker(t *testing.T) {
	_, priv := newTestKeyPair(t)
	// Checker is nil — should return ErrIDJAGUnavailable at Issue time.
	iss, err := auth.NewIDJAGIssuer(auth.IDJAGIssuerConfig{
		SigningKey:        priv,
		Issuer:            testIssuer,
		ApprovedResources: []string{testResource},
		// Checker intentionally omitted.
	})
	if err != nil {
		t.Fatalf("NewIDJAGIssuer: %v", err)
	}

	_, err = iss.Issue(context.Background(), defaultRequest())
	if !errors.Is(err, auth.ErrIDJAGUnavailable) {
		t.Errorf("err = %v, want ErrIDJAGUnavailable", err)
	}
}

// ─── Test: deny-closed — empty approved resources list ───────────────────────

func TestIDJAGIssuer_Issue_EmptyApprovedList(t *testing.T) {
	_, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	// Empty ApprovedResources — every resource is rejected.
	iss, err := auth.NewIDJAGIssuer(auth.IDJAGIssuerConfig{
		SigningKey: priv,
		Issuer:     testIssuer,
		Checker:    checker,
		// ApprovedResources intentionally empty.
	})
	if err != nil {
		t.Fatalf("NewIDJAGIssuer: %v", err)
	}

	_, err = iss.Issue(context.Background(), defaultRequest())
	if !errors.Is(err, auth.ErrInvalidTarget) {
		t.Errorf("err = %v, want ErrInvalidTarget", err)
	}
}

// ─── Test: PublicKey returns correct public key ───────────────────────────────

func TestIDJAGIssuer_PublicKey(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	checker := &mockAgentChecker{
		validAgents: map[string]string{testAgent: testSponsor},
	}
	iss := newValidIssuer(t, priv, checker)

	gotPub := iss.PublicKey()
	if gotPub == nil {
		t.Fatal("PublicKey returned nil")
	}
	if string(gotPub) != string(pub) {
		t.Error("PublicKey did not return the expected public key")
	}
}

// ─── Test: nil issuer returns ErrIDJAGUnavailable ────────────────────────────

func TestIDJAGIssuer_NilIssuer(t *testing.T) {
	var iss *auth.IDJAGIssuer
	_, err := iss.Issue(context.Background(), defaultRequest())
	if !errors.Is(err, auth.ErrIDJAGUnavailable) {
		t.Errorf("nil issuer: err = %v, want ErrIDJAGUnavailable", err)
	}
}
