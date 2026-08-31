// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Live Workload Identity Federation reconciliation (CLA-12/ANT2-08, gap P0 #3).
//
// Anthropic ships a full WIF Admin API that LISTS the org's real federation config:
//
//	GET /v1/organizations/service_accounts   (svac_)
//	GET /v1/organizations/federation_issuers (fdis_)
//	GET /v1/organizations/federation_rules   (fdrl_)
//
// These endpoints REQUIRE an org:admin OAuth bearer token and explicitly REJECT the
// sk-ant-admin Admin API key the roster reads use (verified vs the WIF Admin API
// reference, platform.claude.com/docs/en/manage-claude/wif-admin-api). So the live
// listing rides s.wifClient (modelprovider.AuthBearer, built from org_admin_oauth_token),
// distinct from s.client (modelprovider.AuthAnthropicKey, the x-api-key roster client).
// With no org:admin token the wifClient is nil and reconciliation simply does not happen
// — an honest offline, mirroring admin.go's listAll (the connector never fabricates a
// live roster).
//
// Read-first and minimal data (docs/SECURITY-HARDENING.md-3): every call is a GET; the issuer jwks union
// is reduced to {mode, ca_cert_configured bool} — the inline JWK material (keys) and the
// ca_cert_pem are NEVER decoded into a stored/emitted field; the org:admin token and any
// minted credential are never persisted or logged. The reconciliation reports declared-
// vs-actual divergence as model.FindingReport (the footgun.go pattern) and annotates the
// projected WIFGraph with per-object provenance — never a secret value.

// WIF Admin API list paths (org:admin OAuth bearer; Admin API keys are rejected here).
const (
	pathServiceAccounts   = "/v1/organizations/service_accounts"
	pathFederationIssuers = "/v1/organizations/federation_issuers"
	pathFederationRules   = "/v1/organizations/federation_rules"
)

// Privileged OAuth scopes a federation rule can grant beyond a single workspace's
// developer access. A live rule granting either is a high-value posture signal (an
// org:admin rule can mint org-wide admin tokens). org:manage_tunnels is the org-wide
// MCP-tunnels scope; org:admin is full Admin API access.
const scopeOrgAdmin = "org:admin"

// subjectNHI is the FindingReport SubjectKind for findings about a federation NHI (an
// issuer fdis_; a service-account-level finding would use it too), matching the roster-NHI
// convention (entra-agent uses "identity"); rule-level findings use subjectFederation.
const subjectNHI = "identity"

// driftCase discriminates a reconciliation finding. It rides the FindingReport Title and
// the stable DetailHash so the engine de-duplicates and a SIEM can query the case.
type driftCase string

const (
	driftUndeclaredLiveRule driftCase = "undeclared_live_rule"       // a live rule the operator never declared/governs
	driftDeclaredNotLive    driftCase = "declared_rule_not_live"     // a declared rule that does not exist live
	driftOverBroadSubject   driftCase = "over_broad_subject"         // a live rule with no real subject constraint
	driftScope              driftCase = "scope_drift"                // live oauth_scope diverged from the declared scope
	driftLifetime           driftCase = "lifetime_drift"             // live token_lifetime_seconds diverged from declared
	driftOrphanRule         driftCase = "orphan_rule"                // a live rule referencing a missing issuer/service account
	driftOrphanIssuer       driftCase = "orphan_issuer"              // a live issuer referenced by no rule
	driftReconUnavailable   driftCase = "reconciliation_unavailable" // the live WIF API could not be listed (honest signal)
)

// wifPage is the WIF Admin API list envelope: a typed data array with OPAQUE-CURSOR
// pagination ({data, next_page}). This is DISTINCT from the Admin API's
// {data, has_more, last_id} envelope (admin.go page[T]): the WIF endpoints return an
// opaque next_page cursor that is passed back as the `page` query param, null/"" on the
// last page (verified vs the WIF Admin API reference, Pagination section).
type wifPage[T any] struct {
	Data     []T    `json:"data"`
	NextPage string `json:"next_page"`
}

// liveServiceAccount is one /v1/organizations/service_accounts row (svac_). Metadata
// only — no credential is returned by this endpoint.
type liveServiceAccount struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	OrganizationRole string `json:"organization_role"` // developer|admin — admin can be a target for org:admin minting
	ArchivedAt       string `json:"archived_at"`
}

// liveIssuer is one /v1/organizations/federation_issuers row (fdis_). The jwks union is
// reduced to a presence-only view (see jwksConfig) — the connector never retains the
// inline JWK material or the CA PEM.
type liveIssuer struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	Name       string     `json:"name"`
	IssuerURL  string     `json:"issuer_url"`
	JWKS       jwksConfig `json:"jwks"`
	ArchivedAt string     `json:"archived_at"`
}

// jwksConfig decodes ONLY the discriminator (type) and the presence of a custom CA cert.
// MINIMAL DATA (docs/SECURITY-HARDENING.md): ca_cert_pem is decoded as json.RawMessage purely to derive a
// presence boolean and is NEVER copied into a stored or emitted field; the inline `keys`
// array (public JWKs, but bulky and never needed) is not decoded at all.
type jwksConfig struct {
	Type      string          `json:"type"`        // discovery|explicit_url|inline
	CACertPEM json.RawMessage `json:"ca_cert_pem"` // presence only — never retained or emitted
}

// mode normalizes the jwks discriminator to the connector's documented set.
func (j jwksConfig) mode() string {
	switch j.Type {
	case jwksDiscovery, jwksExplicitURL, jwksInline:
		return j.Type
	default:
		return ""
	}
}

// caConfigured reports whether a custom CA cert is pinned, without retaining the PEM.
func (j jwksConfig) caConfigured() bool {
	s := strings.TrimSpace(string(j.CACertPEM))
	return s != "" && s != "null" && s != `""`
}

// liveRule is one /v1/organizations/federation_rules row (fdrl_). The security boundary
// (match) and the service-account target are NESTED objects in the wire shape; the issuer
// reference (issuer_id) is top-level. The `attributes` field is intentionally not decoded
// (the API documents it as not-yet-supported / always null).
type liveRule struct {
	ID                     string     `json:"id"`
	Type                   string     `json:"type"`
	Name                   string     `json:"name"`
	IssuerID               string     `json:"issuer_id"`
	IssuerName             string     `json:"issuer_name"`
	Match                  ruleMatch  `json:"match"`
	Target                 ruleTarget `json:"target"`
	OAuthScope             string     `json:"oauth_scope"` // space-separated scopes
	TokenLifetimeSeconds   int        `json:"token_lifetime_seconds"`
	AppliesToAllWorkspaces bool       `json:"applies_to_all_workspaces"`
	WorkspaceID            string     `json:"workspace_id"`  // legacy single binding
	WorkspaceIDs           []string   `json:"workspace_ids"` // explicit per-workspace enablement
	ArchivedAt             string     `json:"archived_at"`
}

// ruleMatch is a rule's security boundary: the conditions a verified JWT must satisfy
// (all populated fields ANDed). condition is a CEL expression carried for the lint but
// NEVER evaluated here (no CEL engine in an Apache connector — that is job).
type ruleMatch struct {
	SubjectPrefix string            `json:"subject_prefix"`
	Audience      string            `json:"audience"`
	Claims        map[string]string `json:"claims"`
	Condition     string            `json:"condition"`
}

// ruleTarget is a rule's minted-identity target: the service account (svac_) the minted
// tokens act as. The id is NESTED here, not a top-level service_account_id.
type ruleTarget struct {
	ServiceAccountID   string `json:"service_account_id"`
	ServiceAccountName string `json:"service_account_name"`
	Type               string `json:"type"`
}

// liveSet is one read of the org's live federation config.
type liveSet struct {
	issuers         []liveIssuer
	rules           []liveRule
	serviceAccounts []liveServiceAccount
}

// listWIF pages a WIF Admin API list endpoint to completion (bounded by maxPages),
// following the opaque next_page cursor (passed back as ?page=). It is read-only (a GET
// per page) and a nil client yields no rows.
func listWIF[T any](ctx context.Context, client *modelprovider.Client, maxPages int, path string) ([]T, error) {
	if client == nil {
		return nil, nil
	}
	var out []T
	page := ""
	for i := 0; i < maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"limit": {"100"}}
		if page != "" {
			q.Set("page", page)
		}
		var resp wifPage[T]
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if strings.TrimSpace(resp.NextPage) == "" {
			break
		}
		page = resp.NextPage
	}
	return out, nil
}

// fetchLiveSet lists the org's live federation config (issuers, rules, service accounts)
// via the org:admin OAuth client. It returns ok=false (with a nil error) when no org:admin
// token is configured — the honest offline that disables reconciliation.
//
// The list query never sets include_archived, so the WIF Admin API returns only ACTIVE
// objects by default; archived (soft-deleted) rows are additionally dropped here so a
// tombstoned rule/issuer/service account can never be mistaken for a live grant (defensive
// — an archived rule mints nothing, so it must not fire an "undeclared live rule" finding
// nor mask a declared-not-live drift).
func (s *Source) fetchLiveSet(ctx context.Context) (liveSet, bool, error) {
	if s.wifClient == nil {
		return liveSet{}, false, nil
	}
	issuers, err := listWIF[liveIssuer](ctx, s.wifClient, s.maxPages, pathFederationIssuers)
	if err != nil {
		return liveSet{}, true, err
	}
	rules, err := listWIF[liveRule](ctx, s.wifClient, s.maxPages, pathFederationRules)
	if err != nil {
		return liveSet{}, true, err
	}
	sas, err := listWIF[liveServiceAccount](ctx, s.wifClient, s.maxPages, pathServiceAccounts)
	if err != nil {
		return liveSet{}, true, err
	}
	out := liveSet{}
	for _, i := range issuers {
		if i.ArchivedAt == "" {
			out.issuers = append(out.issuers, i)
		}
	}
	for _, r := range rules {
		if r.ArchivedAt == "" {
			out.rules = append(out.rules, r)
		}
	}
	for _, sa := range sas {
		if sa.ArchivedAt == "" {
			out.serviceAccounts = append(out.serviceAccounts, sa)
		}
	}
	return out, true, nil
}

// reconcileFindings diffs the operator-DECLARED federation (s.federation) against the
// LIVE set and returns the drift findings, deterministically ordered. It is the
// declared-vs-actual reconciliation: undeclared (shadow) live rules, declared rules that
// no longer exist, over-broad live matches, scope/lifetime drift, and orphan rules/issuers.
// Posture is detect/alert-first — it only reports; it never mutates a federation object.
func (s *Source) reconcileFindings(live liveSet, at time.Time) []model.FindingReport {
	declaredByID := make(map[string]FederationRule, len(s.federation))
	for _, r := range s.federation {
		declaredByID[r.RuleID] = r
	}
	liveIssuerIDs := make(map[string]struct{}, len(live.issuers))
	for _, i := range live.issuers {
		liveIssuerIDs[i.ID] = struct{}{}
	}
	liveSAIDs := make(map[string]struct{}, len(live.serviceAccounts))
	for _, sa := range live.serviceAccounts {
		liveSAIDs[sa.ID] = struct{}{}
	}
	issuerReferenced := make(map[string]struct{}, len(live.issuers))

	var out []model.FindingReport
	liveRuleIDs := make(map[string]struct{}, len(live.rules))

	for _, lr := range live.rules {
		liveRuleIDs[lr.ID] = struct{}{}
		if lr.IssuerID != "" {
			issuerReferenced[lr.IssuerID] = struct{}{}
		}
		decl, declared := declaredByID[lr.ID]

		// (1) Undeclared live rule — an ungoverned/shadow federation path (worst case,
		// the footgun class): a live grant the operator's declared inventory does not know.
		if !declared {
			out = append(out, s.driftFinding(driftUndeclaredLiveRule, model.SeverityHigh,
				subjectFederation, lr.ID,
				"Live WIF rule is not declared or governed",
				"undeclared", at))
		}

		// (2) Orphan rule — references an issuer or service account that does not exist
		// live (a broken/misconfigured rule that cannot mint as intended).
		if (lr.IssuerID != "" && !has(liveIssuerIDs, lr.IssuerID)) ||
			(lr.Target.ServiceAccountID != "" && !has(liveSAIDs, lr.Target.ServiceAccountID)) {
			out = append(out, s.driftFinding(driftOrphanRule, model.SeverityMedium,
				subjectFederation, lr.ID,
				"Live WIF rule references a missing issuer or service account",
				"missing_ref", at))
		}

		// (3) Over-broad subject — the live rule places no real constraint on the JWT
		// subject (empty or a bare "*"). The stricter per-axis check fires even when
		// another axis narrows (operator decision): an unconstrained subject is a
		// breadth signal worth review. A non-trivial prefix wildcard is surfaced by the
		// UI lint at info level, not as a ledger finding (kept high-signal).
		if broad, kind := subjectBreadth(lr.Match.SubjectPrefix); broad && kind != "prefix" {
			out = append(out, s.driftFinding(driftOverBroadSubject, model.SeverityMedium,
				subjectFederation, lr.ID,
				"Live WIF rule has an over-broad subject match",
				"subject="+kind, at))
		}

		// (4)+(5) Value drift for a rule present in BOTH declared and live.
		if declared {
			// Normalize BOTH sides (trim/dedup/sort) so an identical scope SET written in a
			// different token order/whitespace is NOT reported as drift (false positive).
			if liveScope, declScope := normScope(lr.OAuthScope), normScope(decl.scope()); liveScope != declScope {
				sev := model.SeverityMedium
				if grantsPrivileged(lr.OAuthScope) && !grantsPrivileged(declScope) {
					sev = model.SeverityHigh // live broadened to an org-wide/admin scope the operator did not declare
				}
				out = append(out, s.driftFinding(driftScope, sev,
					subjectFederation, lr.ID,
					"Live WIF rule scope drifted from the declared scope",
					"scope|live="+liveScope+"|declared="+declScope, at))
			}
			if decl.TokenLifetimeSeconds != 0 && lr.TokenLifetimeSeconds != 0 &&
				lr.TokenLifetimeSeconds != decl.TokenLifetimeSeconds {
				sev := model.SeverityMedium
				if lr.TokenLifetimeSeconds > decl.TokenLifetimeSeconds {
					sev = model.SeverityHigh // live token lives longer than the governed baseline (larger blast radius)
				}
				out = append(out, s.driftFinding(driftLifetime, sev,
					subjectFederation, lr.ID,
					"Live WIF rule token lifetime drifted from the declared value",
					fmt.Sprintf("lifetime|live=%d|declared=%d", lr.TokenLifetimeSeconds, decl.TokenLifetimeSeconds), at))
			}
		}
	}

	// (6) Declared-but-not-live — a governed rule that no longer exists upstream (the
	// declared inventory is fiction; stale governance).
	declaredIDs := make([]string, 0, len(declaredByID))
	for id := range declaredByID {
		declaredIDs = append(declaredIDs, id)
	}
	sort.Strings(declaredIDs)
	for _, id := range declaredIDs {
		if !has(liveRuleIDs, id) {
			out = append(out, s.driftFinding(driftDeclaredNotLive, model.SeverityMedium,
				subjectFederation, id,
				"Declared WIF rule does not exist in the live organization",
				"missing_live", at))
		}
	}

	// (7) Orphan issuer — a live issuer referenced by no live rule (config debt; it can
	// mint nothing). Hygiene, so detect/alert at Low.
	for _, iss := range live.issuers {
		if _, ok := issuerReferenced[iss.ID]; !ok {
			out = append(out, s.driftFinding(driftOrphanIssuer, model.SeverityLow,
				subjectNHI, iss.ID,
				"Live WIF federation issuer is referenced by no rule",
				"unreferenced", at))
		}
	}
	return out
}

// driftFinding builds one reconciliation FindingReport. The DetailHash fingerprints a
// stable, non-sensitive key (kind|source|subject|case-discriminator) so the engine
// de-duplicates across Gather passes and a value change re-fires; it NEVER embeds a
// secret, a token, or a CEL expression value.
func (s *Source) driftFinding(c driftCase, sev model.Severity, subjectKind, ref, title, discriminator string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        identitysource.FindingFederationDrift,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  redact.Hash(identitysource.FindingFederationDrift + "|anthropic|" + ref + "|" + string(c) + "|" + discriminator),
		OccurredAt:  at,
	}
}

// has reports set membership.
func has(set map[string]struct{}, k string) bool {
	_, ok := set[k]
	return ok
}

// normScope normalizes a rule's space-separated oauth_scope to a stable, comparable form
// (trimmed, deduplicated, sorted). An empty scope defaults to workspace:developer (the
// same default federation.go applies to a declared rule).
func normScope(scope string) string {
	fields := strings.Fields(scope)
	if len(fields) == 0 {
		return scopeWorkspaceDeveloper
	}
	seen := make(map[string]struct{}, len(fields))
	uniq := fields[:0]
	for _, f := range fields {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		uniq = append(uniq, f)
	}
	sort.Strings(uniq)
	return strings.Join(uniq, " ")
}

// grantsPrivileged reports whether a scope grants beyond a single workspace's developer
// access (org:admin or org:manage_tunnels) — the high-value breadth signal.
func grantsPrivileged(scope string) bool {
	for _, f := range strings.Fields(scope) {
		if f == scopeOrgAdmin || f == scopeManageTunnels {
			return true
		}
	}
	return false
}

// subjectBreadth classifies a rule's subject_prefix match breadth. "empty" = no subject
// constraint at all; "wildcard" = a bare "*" (matches every subject); "prefix" = a
// non-trivial value ending in "*" (a prefix match over a class of subjects — surfaced
// softly). A specific, non-wildcard prefix returns broad=false.
func subjectBreadth(prefix string) (broad bool, kind string) {
	p := strings.TrimSpace(prefix)
	switch {
	case p == "":
		return true, "empty"
	case p == "*":
		return true, "wildcard"
	case strings.HasSuffix(p, "*"):
		return true, "prefix"
	default:
		return false, ""
	}
}
