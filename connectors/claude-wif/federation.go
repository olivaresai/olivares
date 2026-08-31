// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Anthropic Workload Identity Federation id prefixes (verified against the WIF
// reference: platform.claude.com/docs/en/manage-claude/wif-reference).
const (
	prefixIssuer         = "fdis_"   // federation issuer
	prefixRule           = "fdrl_"   // federation rule
	prefixServiceAccount = "svac_"   // service account
	prefixWorkspace      = "wrkspc_" // workspace
	workspaceDefault     = "default" // the org's default workspace (non-prefixed literal)
)

// The two oauth scopes a federation rule can grant (verified WIF reference; finer
// grained scopes are not currently available). An empty scope defaults to
// scopeWorkspaceDeveloper.
const (
	scopeWorkspaceDeveloper = "workspace:developer" // all non-administrative API endpoints in the workspace
	scopeManageTunnels      = "org:manage_tunnels"  // the MCP tunnels API
)

// token_lifetime_seconds bounds for a minted WIF token (verified WIF reference,
// ANT2-08): 60s to 24h. A declared lifetime outside this range is a declaration error.
const (
	minTokenLifetimeSeconds = 60
	maxTokenLifetimeSeconds = 86400
)

// JWKS discovery modes a federation issuer can use (ANT2-08). An empty mode is
// allowed (the operator may not declare it); a non-empty one must be recognized.
const (
	jwksDiscovery   = "discovery"    // standard OIDC .well-known discovery
	jwksExplicitURL = "explicit_url" // an explicitly-pinned JWKS URL
	jwksInline      = "inline"       // inline JWKS material
)

// FederationRule is one operator-declared WIF rule — the GOVERNED BASELINE. Anthropic's
// WIF Admin API lists/manages the live federation issuers/rules/service accounts under an
// org:admin OAuth bearer token (Admin API keys are rejected on those endpoints); when an
// org:admin token is configured the connector LISTS that live config and diffs it against
// these declared rules (reconcile.go), reporting drift. With no token it models exactly
// what the operator declares — never a fabricated roster. The same shape parameterizes the
// IDN-01 exchange (rule/org/service account/workspace) so a declared rule is both a
// governed NHI and an executable exchange target.
type FederationRule struct {
	IssuerID           string `json:"issuer_id"`            // fdis_… (optional: the external OIDC IdP)
	IssuerURL          string `json:"issuer_url"`           // the issuer's OIDC URL (metadata only)
	RuleID             string `json:"rule_id"`              // fdrl_… (required: the exchange target)
	ServiceAccountID   string `json:"service_account_id"`   // svac_… (required: the minted identity)
	ServiceAccountName string `json:"service_account_name"` // human label (optional)
	OAuthScope         string `json:"oauth_scope"`          // workspace:developer | org:manage_tunnels ("" => developer)
	WorkspaceID        string `json:"workspace_id"`         // wrkspc_… | "default" | "" (rule's sole workspace)

	// --- Additive (ANT2-08): the SECURITY-BOUNDARY match metadata the WIF lint
	// needs. These are operator-declared (the live reconciliation in reconcile.go
	// diffs them against the actual WIF Admin API config when an org:admin token is set).
	// They are inventory/posture, NEVER credential material.

	// SubjectPrefix / Audience are the rule's match criteria on the presented token's
	// subject and audience (the coarse boundary before CEL). Empty = unconstrained on
	// that axis — itself a posture signal an over-broad rule (lints it).
	SubjectPrefix string `json:"subject_prefix"`
	Audience      string `json:"audience"`
	// Claims are required claim equals-matches (claim -> expected value). They, with
	// CELCondition, are the security boundary — "CEL conditions are security boundaries"
	// (WIF reference). The connector carries them for the lint; it does NOT evaluate CEL
	// (no CEL engine dependency in an Apache connector) — that is job.
	Claims       map[string]string `json:"claims"`
	CELCondition string            `json:"cel_condition"`
	// TokenLifetimeSeconds is the minted-token lifetime (60–86400). 0 = not declared
	// (the server default applies); a non-zero value out of range is a declaration error.
	TokenLifetimeSeconds int `json:"token_lifetime_seconds"`
	// JWKSMode is how the issuer's keys are discovered (discovery|explicit_url|inline).
	// CACertConfigured records whether a custom CA cert is pinned for the issuer (a
	// presence flag — the connector does NOT store the PEM, keeping the inventory
	// minimal; the cert is public, but the lint only needs to know it is pinned).
	JWKSMode         string `json:"jwks_mode"`
	CACertConfigured bool   `json:"ca_cert_configured"`
}

// scope returns the rule's effective oauth scope (empty defaults to developer).
func (r FederationRule) scope() string {
	if strings.TrimSpace(r.OAuthScope) == "" {
		return scopeWorkspaceDeveloper
	}
	return r.OAuthScope
}

// FederationExchangeParams projects the operator-declared federation rules into the
// ExchangeParams the host's in-process credential broker (cmd, AGPL) needs to MINT a
// short-lived sk-ant-oat under a declared rule: it pairs each rule's non-secret ids
// (rule fdrl_, service account svac_, workspace) with the organization id, which lives
// on the Source — not on the rule. It is the DECLARED baseline only: no network
// call, no live reconciliation (that is the org:admin reconcile path), and it
// reveals NO secret — an ExchangeParams carries only non-secret ids. The order mirrors
// the declared federation; empty when none is declared. The host selects one rule (by id,
// or the sole declared rule) and calls Exchanger.Exchange — the connector never mints.
func (s *Source) FederationExchangeParams() []ExchangeParams {
	out := make([]ExchangeParams, 0, len(s.federation))
	for _, r := range s.federation {
		out = append(out, ExchangeParams{
			FederationRuleID: r.RuleID,
			OrganizationID:   s.orgID,
			ServiceAccountID: r.ServiceAccountID,
			WorkspaceID:      r.WorkspaceID,
		})
	}
	return out
}

// parseFederation decodes and validates the operator-declared federation rules. A
// malformed declaration, or one missing the required rule/service-account ids or
// carrying a structurally wrong id prefix, fails (the connector never models a
// fabricated or mistyped grant — governance integrity over silent acceptance). An
// empty string yields no rules.
func parseFederation(raw string) ([]FederationRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []FederationRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("claude-wif: parse federation: %w", err)
	}
	for i := range rules {
		if err := rules[i].validate(); err != nil {
			return nil, fmt.Errorf("claude-wif: federation rule #%d: %w", i, err)
		}
	}
	return rules, nil
}

// validate enforces the required ids and their structural prefixes. It does not
// reject an unknown oauth_scope (the operator may legitimately know a scope this
// build has not enumerated) — it normalizes the empty case only — but it DOES reject
// a structurally wrong id, which is always a declaration error.
func (r FederationRule) validate() error {
	if !strings.HasPrefix(r.RuleID, prefixRule) {
		return fmt.Errorf("rule_id %q must start with %q", r.RuleID, prefixRule)
	}
	if !strings.HasPrefix(r.ServiceAccountID, prefixServiceAccount) {
		return fmt.Errorf("service_account_id %q must start with %q", r.ServiceAccountID, prefixServiceAccount)
	}
	if r.IssuerID != "" && !strings.HasPrefix(r.IssuerID, prefixIssuer) {
		return fmt.Errorf("issuer_id %q must start with %q", r.IssuerID, prefixIssuer)
	}
	if r.WorkspaceID != "" && r.WorkspaceID != workspaceDefault && !strings.HasPrefix(r.WorkspaceID, prefixWorkspace) {
		return fmt.Errorf("workspace_id %q must be a %q id or the literal %q", r.WorkspaceID, prefixWorkspace, workspaceDefault)
	}
	// ANT2-08: a declared token lifetime must be within the documented 60–86400s range
	// (0 = not declared, server default). An out-of-range value is a declaration error
	// (governance integrity over silent acceptance — a 30s or 7-day token is a real
	// risk the operator must not get by typo).
	if r.TokenLifetimeSeconds != 0 && (r.TokenLifetimeSeconds < minTokenLifetimeSeconds || r.TokenLifetimeSeconds > maxTokenLifetimeSeconds) {
		return fmt.Errorf("token_lifetime_seconds %d out of range [%d,%d]", r.TokenLifetimeSeconds, minTokenLifetimeSeconds, maxTokenLifetimeSeconds)
	}
	// A declared JWKS mode must be one of the recognized values (empty = not declared).
	switch r.JWKSMode {
	case "", jwksDiscovery, jwksExplicitURL, jwksInline:
	default:
		return fmt.Errorf("jwks_mode %q must be one of %q|%q|%q", r.JWKSMode, jwksDiscovery, jwksExplicitURL, jwksInline)
	}
	return nil
}
