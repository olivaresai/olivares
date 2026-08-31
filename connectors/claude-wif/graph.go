// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"time"
)

// This file projects the federation rules into the WIF OBJECT GRAPH the identity
// console's linter consumes (E, contract §(d)): fdis_ issuers → fdrl_ rules →
// svac_ service accounts → scopes, plus the static-key-shadow footgun signal.
//
// Two projections, one shape:
//   - WIFGraph() projects the operator-DECLARED federation (s.federation) only, with no
//     network call — the offline/declared baseline.
//   - ReconciledWIFGraph(ctx) additionally LISTS the org's live federation config via the
//     org:admin OAuth client (reconcile.go) and merges it in, marking each object's
//     provenance (declared|live|both) so the console can show the ACTUAL config and the
//     linter can flag declared-vs-actual drift. The reconciliation is READ-ONLY: it lists
//     and diffs; it NEVER creates/edits/deletes a federation object.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md, INNEGOCIABLE): the graph carries `ca_cert_configured` as a
// PRESENCE BOOLEAN only — there is NO ca_cert_pem on this struct (the connector never
// stores the PEM, declared or live), NO sk-ant-… key, NO org:admin OAuth token, NO minted
// JWT-SVID, and NO secret value of any kind. The match metadata
// (subject_prefix/audience/claims/cel_condition) is governance boundary criteria, not
// credential material.

// Provenance markers for a reconciled object: whether it was operator-declared, observed
// live via the WIF Admin API, or both. Empty (omitted) on the declared-only WIFGraph().
const (
	sourceDeclared = "declared" // declared by the operator, not seen live (stale governance / config)
	sourceLive     = "live"     // observed live, never declared (ungoverned / shadow federation)
	sourceBoth     = "both"     // declared AND observed live (the reconciled baseline)
)

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

// WIFGraph projects the operator-declared federation rules into the object graph. It
// dedupes issuers by fdis_ id and service accounts by svac_ id (carrying the first
// declaration's attributes), and attaches the static-key-shadow signal. It performs NO
// network call and reveals NO secret — only what the operator declared plus the
// presence of a shadowing env var.
func (s *Source) WIFGraph() WIFGraph {
	g := WIFGraph{
		Issuers:         []WIFIssuer{},
		Rules:           []WIFRule{},
		ServiceAccounts: []WIFServiceAccount{},
	}
	seenIssuer := map[string]struct{}{}
	seenSA := map[string]struct{}{}
	for _, r := range s.federation {
		g.Rules = append(g.Rules, WIFRule{
			RuleID:               r.RuleID,
			IssuerID:             r.IssuerID,
			ServiceAccountID:     r.ServiceAccountID,
			ServiceAccountName:   r.ServiceAccountName,
			OAuthScope:           r.OAuthScope,
			WorkspaceID:          r.WorkspaceID,
			SubjectPrefix:        r.SubjectPrefix,
			Audience:             r.Audience,
			Claims:               r.Claims,
			CELCondition:         r.CELCondition,
			TokenLifetimeSeconds: r.TokenLifetimeSeconds,
			JWKSMode:             r.JWKSMode,
			CACertConfigured:     r.CACertConfigured,
		})
		if r.IssuerID != "" {
			if _, ok := seenIssuer[r.IssuerID]; !ok {
				seenIssuer[r.IssuerID] = struct{}{}
				g.Issuers = append(g.Issuers, WIFIssuer{
					ID:               r.IssuerID,
					IssuerURL:        r.IssuerURL,
					JWKSMode:         r.JWKSMode,
					CACertConfigured: r.CACertConfigured,
				})
			}
		}
		if _, ok := seenSA[r.ServiceAccountID]; !ok {
			seenSA[r.ServiceAccountID] = struct{}{}
			g.ServiceAccounts = append(g.ServiceAccounts, WIFServiceAccount{
				ID:          r.ServiceAccountID,
				Name:        r.ServiceAccountName,
				WorkspaceID: r.WorkspaceID,
				OAuthScope:  r.scope(),
				IssuerID:    r.IssuerID,
				RuleID:      r.RuleID,
			})
		}
	}
	if ks, ok := s.keyShadow(); ok {
		g.KeyShadow = &ks
	}
	return g
}

// keyShadow reports whether a static Anthropic key is present in the environment this
// connector sees (the footgun precondition). It mirrors detectShadowing's env probe
// but reports only PRESENCE (never a value) and does not gate on federation-in-use —
// the consumer (the WIF linter) gates the finding on federation being present, since
// the graph IS the federation inventory.
func (s *Source) keyShadow() (WIFKeyShadow, bool) {
	if _, hasKey := s.envLookup(envAPIKey); hasKey {
		return WIFKeyShadow{Present: true, Var: envAPIKey}, true
	}
	if _, hasAuth := s.envLookup(envAuthToken); hasAuth {
		return WIFKeyShadow{Present: true, Var: envAuthToken}, true
	}
	return WIFKeyShadow{}, false
}

// ReconciledWIFGraph returns the WIF object graph reconciled against the org's LIVE
// federation config (the declared-vs-actual view the identity console serves). With no
// org:admin OAuth token configured (wifClient == nil) it is identical to WIFGraph()
// (declared-only, no Reconciliation block). Otherwise it LISTS the live
// issuers/rules/service-accounts (read-only) and merges them with the declared
// federation, marking each object's provenance (declared|live|both). On a live-list error
// it returns the DECLARED graph with an honest Reconciliation{Reconciled:false,
// Unavailable:…} — never a fabricated live graph — AND the error, so the caller can log it
// while still serving the honest declared baseline.
func (s *Source) ReconciledWIFGraph(ctx context.Context) (WIFGraph, error) {
	declared := s.WIFGraph()
	if s.wifClient == nil {
		return declared, nil // offline: declared-only; no reconciliation is claimed
	}
	live, _, err := s.fetchLiveSet(ctx)
	if err != nil {
		declared.Reconciliation = &WIFReconciliation{Reconciled: false, Unavailable: unavailableReason}
		return declared, err
	}
	g := s.mergeLive(declared, live)
	g.Reconciliation = &WIFReconciliation{Reconciled: true, ObservedAt: s.clock().UTC().Format(time.RFC3339)}
	return g, nil
}

// unavailableReason is the honest, secret-free status the console shows when the live WIF
// listing fails (the full error goes to the server log via the caller, never the UI).
const unavailableReason = "live WIF reconciliation unavailable (check the org:admin OAuth token and server logs)"

// mergeLive merges the LIVE federation set into the declared graph, producing the
// reconciled graph with per-object provenance. Live objects come first (source=live); a
// declared object also seen live is marked source=both (keeping the LIVE/actual values);
// a declared object not seen live is appended as source=declared. The static-key footgun
// (KeyShadow) is env-derived and carried through unchanged.
func (s *Source) mergeLive(declared WIFGraph, live liveSet) WIFGraph {
	// Index live issuers' jwks presence view to backfill each live rule's jwks signal
	// (in the live wire shape jwks lives on the issuer, not the rule).
	type jwksView struct {
		mode string
		ca   bool
	}
	issuerJWKS := make(map[string]jwksView, len(live.issuers))
	for _, li := range live.issuers {
		issuerJWKS[li.ID] = jwksView{mode: li.JWKS.mode(), ca: li.JWKS.caConfigured()}
	}
	// First live rule targeting each service account (mirrors the declared projection,
	// which links a service account to its rule).
	ruleBySA := make(map[string]liveRule, len(live.rules))
	for _, lr := range live.rules {
		if sa := lr.Target.ServiceAccountID; sa != "" {
			if _, ok := ruleBySA[sa]; !ok {
				ruleBySA[sa] = lr
			}
		}
	}

	g := WIFGraph{
		Issuers:         []WIFIssuer{},
		Rules:           []WIFRule{},
		ServiceAccounts: []WIFServiceAccount{},
		KeyShadow:       declared.KeyShadow,
	}

	// Rules: live first, then declared-only.
	ruleIdx := make(map[string]int, len(live.rules))
	for _, lr := range live.rules {
		wr := liveRuleToWIF(lr, sourceLive)
		if jv, ok := issuerJWKS[lr.IssuerID]; ok {
			wr.JWKSMode = jv.mode
			wr.CACertConfigured = jv.ca
		}
		ruleIdx[lr.ID] = len(g.Rules)
		g.Rules = append(g.Rules, wr)
	}
	for _, dr := range declared.Rules {
		if i, ok := ruleIdx[dr.RuleID]; ok {
			g.Rules[i].Source = sourceBoth
			continue
		}
		dr.Source = sourceDeclared
		g.Rules = append(g.Rules, dr)
	}

	// Issuers: live first, then declared-only.
	issIdx := make(map[string]int, len(live.issuers))
	for _, li := range live.issuers {
		issIdx[li.ID] = len(g.Issuers)
		g.Issuers = append(g.Issuers, WIFIssuer{
			ID:               li.ID,
			IssuerURL:        li.IssuerURL,
			JWKSMode:         li.JWKS.mode(),
			CACertConfigured: li.JWKS.caConfigured(),
			Source:           sourceLive,
		})
	}
	for _, di := range declared.Issuers {
		if i, ok := issIdx[di.ID]; ok {
			g.Issuers[i].Source = sourceBoth
			continue
		}
		di.Source = sourceDeclared
		g.Issuers = append(g.Issuers, di)
	}

	// Service accounts: live first, then declared-only.
	saIdx := make(map[string]int, len(live.serviceAccounts))
	for _, ls := range live.serviceAccounts {
		sa := WIFServiceAccount{
			ID:               ls.ID,
			Name:             ls.Name,
			OrganizationRole: ls.OrganizationRole,
			Source:           sourceLive,
		}
		if lr, ok := ruleBySA[ls.ID]; ok {
			sa.RuleID = lr.ID
			sa.IssuerID = lr.IssuerID
			sa.OAuthScope = normScope(lr.OAuthScope)
			sa.WorkspaceID = liveWorkspaceID(lr)
		}
		saIdx[ls.ID] = len(g.ServiceAccounts)
		g.ServiceAccounts = append(g.ServiceAccounts, sa)
	}
	for _, ds := range declared.ServiceAccounts {
		if i, ok := saIdx[ds.ID]; ok {
			g.ServiceAccounts[i].Source = sourceBoth
			continue
		}
		ds.Source = sourceDeclared
		g.ServiceAccounts = append(g.ServiceAccounts, ds)
	}
	return g
}

// liveRuleToWIF maps a live federation rule to the projected WIFRule shape (jwks fields
// are backfilled from the issuer by the caller). It carries the match metadata
// (subject/audience/claims/CEL) as governance boundary criteria — never a secret.
func liveRuleToWIF(lr liveRule, src string) WIFRule {
	return WIFRule{
		RuleID:                 lr.ID,
		IssuerID:               lr.IssuerID,
		ServiceAccountID:       lr.Target.ServiceAccountID,
		ServiceAccountName:     lr.Target.ServiceAccountName,
		OAuthScope:             normScope(lr.OAuthScope),
		WorkspaceID:            liveWorkspaceID(lr),
		SubjectPrefix:          lr.Match.SubjectPrefix,
		Audience:               lr.Match.Audience,
		Claims:                 lr.Match.Claims,
		CELCondition:           lr.Match.Condition,
		TokenLifetimeSeconds:   lr.TokenLifetimeSeconds,
		AppliesToAllWorkspaces: lr.AppliesToAllWorkspaces,
		WorkspaceIDs:           lr.WorkspaceIDs,
		Source:                 src,
	}
}

// liveWorkspaceID picks a representative workspace id for the live rule: the legacy
// single binding if present, else the first explicit workspace id, else empty (an
// applies-to-all-workspaces rule carries the breadth in AppliesToAllWorkspaces).
func liveWorkspaceID(lr liveRule) string {
	if lr.WorkspaceID != "" {
		return lr.WorkspaceID
	}
	if len(lr.WorkspaceIDs) > 0 {
		return lr.WorkspaceIDs[0]
	}
	return ""
}
