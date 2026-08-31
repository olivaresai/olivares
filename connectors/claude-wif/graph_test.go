// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// fedRule is a fully-populated declared rule for the graph projection tests.
const fedRule = `[{
  "issuer_id": "fdis_abc",
  "issuer_url": "https://oidc.spire.example",
  "rule_id": "fdrl_abc",
  "service_account_id": "svac_abc",
  "service_account_name": "ci-deployer",
  "oauth_scope": "workspace:developer",
  "workspace_id": "wrkspc_1",
  "subject_prefix": "repo:acme/",
  "audience": "https://api.anthropic.com",
  "claims": {"repository": "acme/app"},
  "cel_condition": "subject.startsWith('repo:acme/')",
  "token_lifetime_seconds": 3600,
  "jwks_mode": "discovery",
  "ca_cert_configured": true
}]`

func openWithFed(t *testing.T, fed string, env map[string]string) *Source {
	t.Helper()
	s := New()
	s.lookEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"federation": fed}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestWIFGraph_ProjectsDeclaredRules(t *testing.T) {
	s := openWithFed(t, fedRule, nil)
	g := s.WIFGraph()
	if len(g.Rules) != 1 || len(g.Issuers) != 1 || len(g.ServiceAccounts) != 1 {
		t.Fatalf("graph shape: rules=%d issuers=%d svac=%d", len(g.Rules), len(g.Issuers), len(g.ServiceAccounts))
	}
	r := g.Rules[0]
	if r.RuleID != "fdrl_abc" || r.ServiceAccountID != "svac_abc" || r.IssuerID != "fdis_abc" {
		t.Fatalf("rule ids wrong: %+v", r)
	}
	if !r.CACertConfigured {
		t.Fatal("ca_cert_configured presence flag must be true")
	}
	if r.SubjectPrefix != "repo:acme/" || r.CELCondition == "" || r.TokenLifetimeSeconds != 3600 {
		t.Fatalf("rule boundary metadata lost: %+v", r)
	}
	if g.Issuers[0].CACertConfigured != true || g.Issuers[0].JWKSMode != "discovery" {
		t.Fatalf("issuer projection wrong: %+v", g.Issuers[0])
	}
	if g.ServiceAccounts[0].OAuthScope != "workspace:developer" {
		t.Fatalf("svac scope wrong: %+v", g.ServiceAccounts[0])
	}
}

// TestWIFGraph_NeverEmitsKeyMaterial is the MINIMAL-DATA guardrail: the serialized
// graph must NEVER contain a PEM, an sk-ant- key, a private key, or a JWT-SVID — only
// the ca_cert_configured boolean of presence.
func TestWIFGraph_NeverEmitsKeyMaterial(t *testing.T) {
	s := openWithFed(t, fedRule, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret-value"})
	g := s.WIFGraph()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(b)
	for _, banned := range []string{"sk-ant-", "BEGIN CERTIFICATE", "PRIVATE KEY", "ca_cert_pem", "secret-value"} {
		if strings.Contains(wire, banned) {
			t.Fatalf("WIF graph leaked %q over the wire:\n%s", banned, wire)
		}
	}
	if !strings.Contains(wire, `"ca_cert_configured":true`) {
		t.Fatalf("ca_cert_configured boolean must be present:\n%s", wire)
	}
}

// TestWIFGraph_KeyShadowSignal verifies the footgun signal reports presence + the var
// name only — never the key value.
func TestWIFGraph_KeyShadowSignal(t *testing.T) {
	s := openWithFed(t, fedRule, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-xyz"})
	g := s.WIFGraph()
	if g.KeyShadow == nil || !g.KeyShadow.Present || g.KeyShadow.Var != "ANTHROPIC_API_KEY" {
		t.Fatalf("key shadow signal wrong: %+v", g.KeyShadow)
	}
	// No static key → no shadow signal.
	s2 := openWithFed(t, fedRule, nil)
	if s2.WIFGraph().KeyShadow != nil {
		t.Fatal("no static key → key_shadow must be nil")
	}
}
