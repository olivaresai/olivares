// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// verifiedDoerCard is verifiedDoer with a custom card base map, so a test can mint a
// signed (trustVerified) card whose declared skills / capabilities it controls.
func verifiedDoerCard(t *testing.T, agent, rpc string, base map[string]any) (*stubDoer, []byte) {
	t.Helper()
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", base)
	return &stubDoer{cardBytes: card, rpcBytes: []byte(rpc)}, jwks
}

// cardNoStreaming returns a base card whose signed capabilities do NOT advertise
// streaming (streaming:false).
func cardNoStreaming(name string) map[string]any {
	c := baseCard(name)
	c["capabilities"] = map[string]any{"streaming": false}
	return c
}

// TestDelegateCapabilityDenySkillNotDeclared: even when the operator allowlist PERMITS a
// skill, a delegation to a skill the SIGNED card does not declare is refused (deny-closed,
// before the allowlist matters). Nothing is emitted; the refusal is a CapabilityError.
func TestDelegateCapabilityDenySkillNotDeclared(t *testing.T) {
	// The card declares only skill s1/"summarize"; the operator allowlist permits
	// "translate" — so the allowlist is NOT the thing denying. The signed card is.
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: NewAllowlist([]AllowRule{{Agent: "billing", Skill: "translate", Scopes: []string{"x"}}}),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	spec := okSpec()
	spec.Skill, spec.Scope = "translate", "x" // permitted by allowlist, NOT declared by the card
	_, err := d.Delegate(context.Background(), spec)
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("expected a CapabilityError for an undeclared skill, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a capability deny must emit NOTHING, got %d POSTs", doer.postCount)
	}
	if aud.last().Allowed {
		t.Error("a capability deny must be audited as not-allowed")
	}
}

// TestDelegateCapabilityAllowById: a delegation whose skill matches the card's declared
// skill ID (not just its name) passes the capability gate and is delegated.
func TestDelegateCapabilityAllowById(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: NewAllowlist([]AllowRule{{Agent: "billing", Skill: "s1", Scopes: []string{"reports:read"}}}),
		Gate:      &fakeGate{status: StatusApproved},
	})
	spec := okSpec()
	spec.Skill = "s1" // the card's declared skill ID
	if _, err := d.Delegate(context.Background(), spec); err != nil {
		t.Fatalf("a delegation to a declared skill id must succeed, got %v", err)
	}
	if doer.postCount != 1 {
		t.Errorf("want exactly 1 emission, got %d", doer.postCount)
	}
}

// TestDelegateStreamingCapabilityDeny: a streaming delegation to a card that does NOT
// advertise the streaming capability is refused (deny-closed), even with a declared skill
// + an approving gate. Nothing streams.
func TestDelegateStreamingCapabilityDeny(t *testing.T) {
	doer, jwks := verifiedDoerCard(t, "billing", "", cardNoStreaming("billing"))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	err := d.DelegateStreaming(context.Background(), okSpec(), func(StreamEvent) error { return nil })
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("streaming to a non-streaming card must be a CapabilityError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a streaming-capability deny must open NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestSubscribeToTaskStreamingCapabilityDeny: resuming an SSE stream on an agent whose
// signed card does not advertise streaming is refused (deny-closed).
func TestSubscribeToTaskStreamingCapabilityDeny(t *testing.T) {
	doer, jwks := verifiedDoerCard(t, "billing", "", cardNoStreaming("billing"))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	err := d.SubscribeToTask(context.Background(),
		TaskRef{AgentName: "billing", AgentURL: "https://billing.example.com", TaskID: "t1"},
		func(StreamEvent) error { return nil })
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("resume on a non-streaming card must be a CapabilityError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a resume capability deny must open NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestSendMessageCapableDeny: the capability-bound emission primitive refuses a skill the
// signed card does not declare, emitting nothing.
func TestSendMessageCapableDeny(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	_, err := c.SendMessageCapable(context.Background(), SendSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com", Skill: "deploy", Text: "go",
	})
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("SendMessageCapable to an undeclared skill must be a CapabilityError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a capability deny must emit NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestSendMessageCapableAllow: a declared skill passes and emits exactly once.
func TestSendMessageCapableAllow(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	res, err := c.SendMessageCapable(context.Background(), SendSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com", Skill: "summarize", Text: "go",
	})
	if err != nil {
		t.Fatalf("SendMessageCapable to a declared skill must succeed, got %v", err)
	}
	if res.State != TaskStateSubmitted || doer.postCount != 1 {
		t.Errorf("want one emission in SUBMITTED, got state=%q posts=%d", res.State, doer.postCount)
	}
}

// TestRequireDeclaredSkillBlankIsSkillless: a blank skill is a skill-less delegation (the
// agent's general endpoint) and is NOT capability-denied here — the allowlist governs it.
func TestRequireDeclaredSkillBlankIsSkillless(t *testing.T) {
	card := AgentCard{Skills: []agentSkill{{ID: "s1", Name: "summarize"}}}
	if err := requireDeclaredSkill(card, "billing", ""); err != nil {
		t.Errorf("a blank skill must not be capability-denied, got %v", err)
	}
	if err := requireDeclaredSkill(card, "billing", "summarize"); err != nil {
		t.Errorf("a declared skill (by name) must pass, got %v", err)
	}
	if err := requireDeclaredSkill(card, "billing", "nope"); err == nil {
		t.Error("an undeclared skill must be denied")
	}
}

// TestRequireSecuritySchemeFloorDeny: a card with no SecuritySchemes is denied
// (floor: agent must declare at least one recognized auth scheme).
func TestRequireSecuritySchemeFloorDeny(t *testing.T) {
	card := AgentCard{SecuritySchemes: nil}
	err := requireSecurityScheme(card, "billing", "oauth2")
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("no schemes must be a CapabilityError, got %v", err)
	}
	if !strings.Contains(ce.Reason, "no security scheme") {
		t.Errorf("reason should mention no security scheme: %s", ce.Reason)
	}
}

// TestRequireSecuritySchemeBindingDeny: a card that declares apiKey but receives a
// bearer (oauth2) credential is denied (binding mismatch).
func TestRequireSecuritySchemeBindingDeny(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"key1": {APIKey: &apiKeyScheme{}},
	}}
	err := requireSecurityScheme(card, "billing", "oauth2")
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("binding mismatch must be a CapabilityError, got %v", err)
	}
	if !strings.Contains(ce.Reason, "incompatible") {
		t.Errorf("reason should mention incompatible: %s", ce.Reason)
	}
}

// TestRequireSecuritySchemeOAuth2BearerAllow: a card declaring oauth2 matches a
// bearer credential (the happy path).
func TestRequireSecuritySchemeOAuth2BearerAllow(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"main": {OAuth2: &oauth2Scheme{}},
	}}
	if err := requireSecurityScheme(card, "billing", "oauth2"); err != nil {
		t.Errorf("oauth2 card + oauth2 credential must pass, got %v", err)
	}
}

// TestRequireSecuritySchemeOpenIDConnectBearerAllow: openIdConnect is OAuth2-based
// and matches a bearer credential (mapped to both oauth2 and openIdConnect).
func TestRequireSecuritySchemeOpenIDConnectBearerAllow(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"oidc": {OpenIDConnect: &openIDConnectScheme{}},
	}}
	if err := requireSecurityScheme(card, "billing", "oauth2"); err != nil {
		t.Errorf("openIdConnect card + bearer credential must pass, got %v", err)
	}
}

// TestRequireSecuritySchemeMtlsAllow: a card declaring mutualTLS matches mTLS credential.
func TestRequireSecuritySchemeMtlsAllow(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"tls": {MutualTLS: &mtlsScheme{}},
	}}
	if err := requireSecurityScheme(card, "billing", "mutualTLS"); err != nil {
		t.Errorf("mtls card + mtls credential must pass, got %v", err)
	}
}

// TestRequireSecuritySchemeMultiSchemeAllow: a card declaring MULTIPLE schemes where
// at least one matches the credential passes.
func TestRequireSecuritySchemeMultiSchemeAllow(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"key":  {APIKey: &apiKeyScheme{}},
		"main": {OAuth2: &oauth2Scheme{}},
	}}
	if err := requireSecurityScheme(card, "billing", "oauth2"); err != nil {
		t.Errorf("multi-scheme card with one matching must pass, got %v", err)
	}
}

// TestRequireSecuritySchemeV0xFallback: a v0.x card using the legacy Type string
// is recognized at the floor level (at least one scheme declared).
func TestRequireSecuritySchemeV0xFallback(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"legacy": {Type: "http"},
	}}
	if err := requireSecurityScheme(card, "billing", "http"); err != nil {
		t.Errorf("v0.x type=http + http credential must pass, got %v", err)
	}
	// But a v0.x type that doesn't match the credential is denied.
	err := requireSecurityScheme(card, "billing", "oauth2")
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("v0.x type mismatch must be CapabilityError, got %v", err)
	}
}

// TestRequireSecuritySchemeEmptyCredentialPassesBinding: when the caller provides no
// credential kind (empty string), the floor still applies (card must declare schemes)
// but binding is skipped (no credential to match against).
func TestRequireSecuritySchemeEmptyCredentialPassesBinding(t *testing.T) {
	card := AgentCard{SecuritySchemes: map[string]securityScheme{
		"main": {OAuth2: &oauth2Scheme{}},
	}}
	if err := requireSecurityScheme(card, "billing", ""); err != nil {
		t.Errorf("empty credential kind should pass binding (floor only), got %v", err)
	}
}

// TestCredentialSchemeKind: infer scheme kind from HTTP headers.
func TestCredentialSchemeKind(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"bearer", map[string]string{"Authorization": "Bearer eyJ..."}, "oauth2"},
		{"basic", map[string]string{"Authorization": "Basic dXNlcg=="}, "http"},
		{"apikey header", map[string]string{"X-API-Key": "abc123"}, "apiKey"},
		{"no auth", map[string]string{"Accept": "application/json"}, ""},
		{"empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := credentialSchemeKind(tt.headers)
			if got != tt.want {
				t.Errorf("credentialSchemeKind(%v) = %q, want %q", tt.headers, got, tt.want)
			}
		})
	}
}
