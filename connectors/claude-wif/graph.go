// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

// WIFGraph is the projected object graph for the identity console (mirrors the
// frontend WifGraphData). All fields are inventory/posture; none is a secret.
type WIFGraph struct {
	Issuers         []WIFIssuer         `json:"issuers"`
	Rules           []WIFRule           `json:"rules"`
	ServiceAccounts []WIFServiceAccount `json:"service_accounts"`
	// KeyShadow is the static-key footgun signal: a present ANTHROPIC_API_KEY /
	// ANTHROPIC_AUTH_TOKEN shadows federation in the precedence. nil when no static
	// key is present in the environment this connector sees.
	KeyShadow *WIFKeyShadow `json:"key_shadow,omitempty"`
	// Reconciliation reports whether the graph was reconciled against the live WIF Admin
	// API and, if not, why (honestly — an unreadable live API is never a silent green).
	// nil on the declared-only WIFGraph() (no org:admin token configured).
	Reconciliation *WIFReconciliation `json:"reconciliation,omitempty"`
}

// WIFReconciliation is the honest declared-vs-actual reconciliation status. When
// Reconciled is false and Unavailable is set, the live config could not be listed (the
// console shows the declared baseline and says so — never a fabricated "all clear").
type WIFReconciliation struct {
	Reconciled  bool   `json:"reconciled"`
	ObservedAt  string `json:"observed_at,omitempty"`
	Unavailable string `json:"unavailable,omitempty"` // sanitized reason; never carries a secret
}

// WIFIssuer is a federation issuer (fdis_). ca_cert_configured is a presence flag.
type WIFIssuer struct {
	ID               string `json:"id"`
	IssuerURL        string `json:"issuer_url,omitempty"`
	JWKSMode         string `json:"jwks_mode,omitempty"`
	CACertConfigured bool   `json:"ca_cert_configured"`
	// Source is the object's provenance (declared|live|both); empty on WIFGraph().
	Source string `json:"source,omitempty"`
}

// WIFRule is a federation rule (fdrl_) — the security boundary. token_lifetime_seconds
// is 0 when undeclared. ca_cert_configured is a presence flag; there is NO ca_cert_pem.
type WIFRule struct {
	RuleID               string            `json:"rule_id"`
	IssuerID             string            `json:"issuer_id,omitempty"`
	ServiceAccountID     string            `json:"service_account_id"`
	ServiceAccountName   string            `json:"service_account_name,omitempty"`
	OAuthScope           string            `json:"oauth_scope,omitempty"`
	WorkspaceID          string            `json:"workspace_id,omitempty"`
	SubjectPrefix        string            `json:"subject_prefix,omitempty"`
	Audience             string            `json:"audience,omitempty"`
	Claims               map[string]string `json:"claims,omitempty"`
	CELCondition         string            `json:"cel_condition,omitempty"`
	TokenLifetimeSeconds int               `json:"token_lifetime_seconds,omitempty"`
	JWKSMode             string            `json:"jwks_mode,omitempty"`
	CACertConfigured     bool              `json:"ca_cert_configured"`
	// AppliesToAllWorkspaces / WorkspaceIDs carry the live rule's workspace breadth (a
	// rule enabled for every workspace is a breadth signal). Empty on a declared rule.
	AppliesToAllWorkspaces bool     `json:"applies_to_all_workspaces,omitempty"`
	WorkspaceIDs           []string `json:"workspace_ids,omitempty"`
	// Source is the object's provenance (declared|live|both); empty on WIFGraph().
	Source string `json:"source,omitempty"`
}

// WIFServiceAccount is a service account (svac_) — a first-class NHI principal.
type WIFServiceAccount struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	OAuthScope  string `json:"oauth_scope,omitempty"`
	IssuerID    string `json:"issuer_id,omitempty"`
	RuleID      string `json:"rule_id,omitempty"`
	// OrganizationRole is the live svac org role (developer|admin). An admin-role service
	// account is the only target a rule can use to mint org:admin tokens — a posture
	// signal. Empty on a declared service account.
	OrganizationRole string `json:"organization_role,omitempty"`
	// Source is the object's provenance (declared|live|both); empty on WIFGraph().
	Source string `json:"source,omitempty"`
}

// WIFKeyShadow reports a static Anthropic key present in the environment (which would
// silently shadow federation in the precedence). Var names WHICH variable; never a value.
type WIFKeyShadow struct {
	Present bool   `json:"present"`
	Var     string `json:"var,omitempty"`
}
