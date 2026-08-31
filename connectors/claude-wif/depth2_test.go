// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

// TestFederationLifetimeAndJWKSValidation proves the ANT2-08 governance-integrity
// checks: an out-of-range token lifetime or an unrecognized JWKS mode fails at parse
// (never silently accepted), while in-range/known values pass.
func TestFederationLifetimeAndJWKSValidation(t *testing.T) {
	bad := []string{
		`[{"rule_id":"fdrl_1","service_account_id":"svac_1","token_lifetime_seconds":30}]`,     // < 60
		`[{"rule_id":"fdrl_1","service_account_id":"svac_1","token_lifetime_seconds":100000}]`, // > 86400
		`[{"rule_id":"fdrl_1","service_account_id":"svac_1","jwks_mode":"telepathy"}]`,         // unknown mode
	}
	for _, b := range bad {
		if _, err := parseFederation(b); err == nil {
			t.Errorf("expected validation error for %s", b)
		}
	}
	good := `[{"rule_id":"fdrl_1","service_account_id":"svac_1","token_lifetime_seconds":3600,"jwks_mode":"discovery"}]`
	if _, err := parseFederation(good); err != nil {
		t.Errorf("valid rule rejected: %v", err)
	}
	// 0 lifetime (not declared) is allowed (server default applies).
	if _, err := parseFederation(`[{"rule_id":"fdrl_1","service_account_id":"svac_1"}]`); err != nil {
		t.Errorf("undeclared lifetime rejected: %v", err)
	}
}

// TestRosterCarriesWIFLintMetadata proves the ANT2-08 security-boundary metadata
// (subject_prefix/audience/CEL/token_lifetime/jwks_mode) flows onto the rule's roster
// attributes, so the WIF lint can read it — without the connector evaluating CEL.
func TestRosterCarriesWIFLintMetadata(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	s.federation = []FederationRule{{
		RuleID:               "fdrl_ci",
		ServiceAccountID:     "svac_ci",
		IssuerID:             "fdis_gh",
		SubjectPrefix:        "repo:acme/",
		Audience:             "https://api.anthropic.com",
		CELCondition:         "claims.ref == 'refs/heads/main'",
		Claims:               map[string]string{"repository": "acme/app"},
		TokenLifetimeSeconds: 900,
		JWKSMode:             "discovery",
		CACertConfigured:     true,
	}}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var rule *identitysource.Collection
	for i := range g.Collections {
		if g.Collections[i].Ref == "fdrl_ci" {
			rule = &g.Collections[i]
		}
	}
	if rule == nil {
		t.Fatal("federation rule collection not found")
	}
	a := rule.Attributes
	if a["subject_prefix"] != "repo:acme/" || a["audience"] != "https://api.anthropic.com" {
		t.Errorf("rule missing subject/audience match metadata: %v", a)
	}
	if a["cel_condition"] == "" || a["jwks_mode"] != "discovery" {
		t.Errorf("rule missing cel/jwks metadata: %v", a)
	}
	if a["token_lifetime_seconds"] != "900" || a["claims_count"] != "1" || a["ca_cert_configured"] != "true" {
		t.Errorf("rule missing lifetime/claims/ca metadata: %v", a)
	}
}
