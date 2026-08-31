// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Managed SSO/IdP configuration endpoints (FASE X): the console moves SSO
// off env-only onto a store-backed, sealed-secret config. V1 governs a single
// GLOBAL (deployment-wide) config, so the endpoints are SUPERADMIN-gated — a
// deployment-wide login setting must not be editable by a single tenant's admin
// (per-tenant SSO, when added, will gate the per-tenant config on tenant:admin in
// that tenant). Writes additionally require an AAL3 step-up (secret-bearing,
// privilege-shaped). Secrets are never returned — only a non-secret hint.

// ssoConfigDTO is the read shape: every field EXCEPT secrets, plus hints, the
// exact callback URL to register at the IdP, and whether this build can activate
// SSO (the enterprise provider builder).
type ssoConfigDTO struct {
	Configured        bool   `json:"configured"`
	ProviderAvailable bool   `json:"provider_available"`
	Protocol          string `json:"protocol,omitempty"`
	Status            string `json:"status,omitempty"`
	RedirectURI       string `json:"redirect_uri"`

	// TargetTenant is the scope this config governs (U6): "" for the
	// deployment-wide global config, else the tenant id whose IdP this is. The
	// console uses it to label the per-tenant surface; a per-tenant IdP only
	// RESOLVES at login in an enterprise build (the MultiIDP cap-lift), and the
	// single-active cap still refuses activating a second IdP in the open build.
	TargetTenant string `json:"target_tenant,omitempty"`

	// Alias is the IdP's scope-unique identifier (U4); "default" is the scope's
	// primary IdP (the base /console/sso[/tenants/{tenant}] surface). The per-IdP admin
	// routes (/idps, /idps/{alias}) let a scope hold more than one IdP.
	Alias string `json:"alias,omitempty"`

	OIDCIssuer           string `json:"oidc_issuer,omitempty"`
	OIDCClientID         string `json:"oidc_client_id,omitempty"`
	OIDCClientSecretHint string `json:"oidc_client_secret_hint,omitempty"`

	SAMLMetadataURL   string `json:"saml_metadata_url,omitempty"`
	SAMLEntityID      string `json:"saml_entity_id,omitempty"`
	SAMLACSURL        string `json:"saml_acs_url,omitempty"`
	SAMLIDPSSOURL     string `json:"saml_idp_sso_url,omitempty"`
	SAMLEmailAttr     string `json:"saml_email_attr,omitempty"`
	SAMLSPCertPEM     string `json:"saml_sp_cert_pem,omitempty"`
	SAMLSPKeyHint     string `json:"saml_sp_key_hint,omitempty"`
	SAMLSPSignCertPEM string `json:"saml_sp_sign_cert_pem,omitempty"`
	SAMLSPSignKeyHint string `json:"saml_sp_sign_key_hint,omitempty"`

	// Login-enforcement posture (non-secret). require_sso + network_allowlist are
	// the stored operator intent; enforced_by is the HONEST signal of whether THIS build
	// enforces THIS ROW's posture — "enterprise", "unavailable", or "out_of_scope" when the
	// build enforces but this row lies outside the scope the posture reader resolves (see
	// ssoEnforcedBy: it is a THREE-value signal, and a consumer that treats "not
	// unavailable" as "enforced" reintroduces the false green badge it replaced).
	// network_allowlist is always an array (never null) for the console.
	RequireSSO       bool     `json:"require_sso"`
	NetworkAllowlist []string `json:"network_allowlist"`
	EnforcedBy       string   `json:"enforced_by"`

	// Group mapping + JIT coherence (non-secret). oidc_groups_claim /
	// saml_groups_attr name where the provider reads the subject's groups;
	// scim_authoritative makes SCIM the sole identity authority. groups_mapped_by is
	// the HONEST capability signal — "enterprise" when THIS build can turn an asserted
	// group into a grant (a GroupMapper is wired), "unavailable" in the open binary
	// (which extracts groups but never maps them) — symmetric with enforced_by.
	OIDCGroupsClaim   string `json:"oidc_groups_claim,omitempty"`
	SAMLGroupsAttr    string `json:"saml_groups_attr,omitempty"`
	SCIMAuthoritative bool   `json:"scim_authoritative"`
	GroupsMappedBy    string `json:"groups_mapped_by"`

	// U5 home-realm routing: the email domains this IdP claims (globally unique).
	// Always an array (never null). Whether the build ROUTES by them is a build capability
	// (routed_by), symmetric with enforced_by / groups_mapped_by.
	ClaimedDomains []string `json:"claimed_domains"`
	RoutedBy       string   `json:"routed_by"`

	UpdatedAt string `json:"updated_at,omitempty"`
}

func toSSOConfigDTO(v auth.FederationConfigView, scope model.TenantID, redirectURI, enforcedBy, groupsMappedBy, routedBy string) ssoConfigDTO {
	cidrs := v.NetworkAllowCIDRs
	if cidrs == nil {
		cidrs = []string{} // the console expects an array, never null
	}
	domains := v.ClaimedDomains
	if domains == nil {
		domains = []string{}
	}
	targetTenant := ""
	if scope != auth.GlobalFederationScope {
		targetTenant = scope.String()
	}
	d := ssoConfigDTO{
		Configured: v.Configured, ProviderAvailable: v.ProviderAvailable, Protocol: v.Protocol,
		Status: v.Status, RedirectURI: redirectURI, TargetTenant: targetTenant, Alias: v.Alias,
		OIDCIssuer: v.OIDCIssuer, OIDCClientID: v.OIDCClientID, OIDCClientSecretHint: v.OIDCClientSecretHint,
		SAMLMetadataURL: v.SAMLMetadataURL, SAMLEntityID: v.SAMLEntityID, SAMLACSURL: v.SAMLACSURL,
		SAMLIDPSSOURL: v.SAMLIDPSSOURL, SAMLEmailAttr: v.SAMLEmailAttr, SAMLSPCertPEM: v.SAMLSPCertPEM,
		SAMLSPKeyHint: v.SAMLSPKeyHint, SAMLSPSignCertPEM: v.SAMLSPSignCertPEM, SAMLSPSignKeyHint: v.SAMLSPSignKeyHint,
		RequireSSO: v.RequireSSO, NetworkAllowlist: cidrs, EnforcedBy: enforcedBy,
		OIDCGroupsClaim: v.OIDCGroupsClaim, SAMLGroupsAttr: v.SAMLGroupsAttr,
		SCIMAuthoritative: v.SCIMAuthoritative, GroupsMappedBy: groupsMappedBy,
		ClaimedDomains: domains, RoutedBy: routedBy,
	}
	if !v.UpdatedAt.IsZero() {
		d.UpdatedAt = v.UpdatedAt.String()
	}
	return d
}

// ssoGroupsMappedBy is the HONEST groups_mapped_by signal: "enterprise" when THIS
// build wires the reserved GroupMapper (login-time group→grant mapping U2),
// else "unavailable" (the open binary extracts asserted groups but never maps them).
// It reads the live Authenticator capability, so it never claims a capability the
// build lacks — symmetric with ssoEnforcedBy.
func (s *Server) ssoGroupsMappedBy() string {
	if s.authr != nil && s.authr.GroupMappingAvailable() {
		return "enterprise"
	}
	return "unavailable"
}

// ssoRoutedBy is the HONEST routed_by signal (U5): "enterprise" when THIS build routes
// a login among several IdPs by email domain / tenant (the reserved MultiIDP capability),
// else "unavailable" (the open binary stores claimed domains but resolves the single global
// IdP). Symmetric with enforced_by / groups_mapped_by.
func (s *Server) ssoRoutedBy() string {
	if s.fedSvc != nil && s.fedSvc.MultiIDPAvailable() {
		return "enterprise"
	}
	return "unavailable"
}

// ssoEnforcedBy is the HONEST enforced_by signal for ONE IdP row. It answers three ways,
// never two — the console prints it directly above THAT ROW's stored posture, so a
// build-wide yes/no was read as a claim about a posture nobody reads:
//
//	"enterprise"   — this build enforces AND this row is the one whose posture it reads
//	"unavailable"  — this build never enforces (no LoginPolicy wired; the open binary)
//	"out_of_scope" — this build enforces, but NOT over this row's posture
//
// The third answer exists because of the SCOPE the enforcement engine reads:
// FederationService.Posture takes no scope and resolves (GlobalFederationScope, "default")
// only, so a per-tenant IdP — or a second, non-default global one — stores a posture the
// wired LoginPolicy never reads. Reporting "enterprise" there marked a security control
// ACTIVE over a row it does not govern.
//
// It is NOT "unavailable" either: that denies enforcement this build genuinely performs,
// and the console's remedy for "unavailable" is "rebuild with the enterprise tag", which
// would fix nothing here. The remedy is to set the posture on the deployment-wide primary.
//
// ⛔ AND IT IS NOT "unknown", which is what this returned first. A contrast
// (external contrast) refuted that: the value is a DEFINITE
// no, not an open question. The shipped composition passes fedSvc.Posture literally as the
// engine's posture reader, so a non-(global, default) row is provably not consulted. The
// tell was the asymmetry — claiming "enterprise" for the default row uses knowledge of that
// same composition, so pleading "we cannot know" for the others was having it both ways.
// "I could not look" is a legitimate answer (canon §1.5) but it is a LIE when you did look.
//
// Widening the posture reader to per-scope (PostureFor(ctx, scope, alias)) is what turns
// this into "enterprise" for more rows; it is an enterprise-side change and is NOT in this
// clone. Do not delete the third value when it lands — the console distinguishes it, and
// collapsing it back into two is the defect this replaced.
func (s *Server) ssoEnforcedBy(scope model.TenantID, alias string) string {
	if s.authr == nil || !s.authr.EnforcesLogin() {
		return "unavailable"
	}
	if scope == auth.GlobalFederationScope && model.NormalizeFederationAlias(alias) == model.DefaultFederationAlias {
		return "enterprise"
	}
	return "out_of_scope"
}

// ssoConfigInput is the PUT/test payload. A blank secret field keeps the sealed
// value already stored (so editing config never forces re-entering the secret).
type ssoConfigInput struct {
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`

	OIDCIssuer       string `json:"oidc_issuer"`
	OIDCClientID     string `json:"oidc_client_id"`
	OIDCClientSecret string `json:"oidc_client_secret"`

	SAMLMetadataURL   string `json:"saml_metadata_url"`
	SAMLEntityID      string `json:"saml_entity_id"`
	SAMLACSURL        string `json:"saml_acs_url"`
	SAMLIDPSSOURL     string `json:"saml_idp_sso_url"`
	SAMLEmailAttr     string `json:"saml_email_attr"`
	SAMLSPCertPEM     string `json:"saml_sp_cert_pem"`
	SAMLSPKeyPEM      string `json:"saml_sp_key_pem"`
	SAMLSPSignCertPEM string `json:"saml_sp_sign_cert_pem"`
	SAMLSPSignKeyPEM  string `json:"saml_sp_sign_key_pem"`

	// Login-enforcement posture (non-secret). Replaced verbatim on every PUT:
	// require_sso toggles password-login enforcement; network_allowlist is the full
	// intended CIDR set (an empty/absent list clears the allow-list). Malformed CIDRs
	// are rejected (400). The open build persists these but never enforces them.
	RequireSSO       bool     `json:"require_sso"`
	NetworkAllowlist []string `json:"network_allowlist"`

	// Group mapping + JIT coherence (non-secret, replaced verbatim on every PUT).
	OIDCGroupsClaim   string `json:"oidc_groups_claim"`
	SAMLGroupsAttr    string `json:"saml_groups_attr"`
	SCIMAuthoritative bool   `json:"scim_authoritative"`

	// U5 home-realm domains (non-secret, replaced verbatim). Normalized + validated +
	// checked for global uniqueness by the backend (400/409 on a bad or already-claimed one).
	ClaimedDomains []string `json:"claimed_domains"`
}

func (in ssoConfigInput) toParams() auth.FederationConfigInput {
	return auth.FederationConfigInput{
		Protocol: in.Protocol, Enabled: in.Enabled,
		OIDCIssuer: in.OIDCIssuer, OIDCClientID: in.OIDCClientID, OIDCClientSecret: in.OIDCClientSecret,
		SAMLMetadataURL: in.SAMLMetadataURL, SAMLEntityID: in.SAMLEntityID, SAMLACSURL: in.SAMLACSURL,
		SAMLIDPSSOURL: in.SAMLIDPSSOURL, SAMLEmailAttr: in.SAMLEmailAttr,
		SAMLSPCertPEM: in.SAMLSPCertPEM, SAMLSPKeyPEM: in.SAMLSPKeyPEM,
		SAMLSPSignCertPEM: in.SAMLSPSignCertPEM, SAMLSPSignKeyPEM: in.SAMLSPSignKeyPEM,
		RequireSSO: in.RequireSSO, NetworkAllowCIDRs: in.NetworkAllowlist,
		OIDCGroupsClaim: in.OIDCGroupsClaim, SAMLGroupsAttr: in.SAMLGroupsAttr,
		SCIMAuthoritative: in.SCIMAuthoritative, ClaimedDomains: in.ClaimedDomains,
	}
}

// ssoService returns the managed config service, or writes 501 when it is not
// wired (an embedder/test that did not opt in).
func (s *Server) ssoService(w http.ResponseWriter, r *http.Request) (*auth.FederationService, bool) {
	if s.fedSvc == nil {
		s.writeError(w, r, auth.ErrSSONotConfigured)
		return nil, false
	}
	return s.fedSvc, true
}

// ssoScope resolves the federation-config scope a request targets (U6): the
// {tenant} path param on the per-tenant admin routes, or the deployment-wide global
// scope on the base routes (no path param). A superadmin manages ANY tenant's IdP by
// scope — the config CRUD is superadmin-gated (ratified D8: superadmin creates/activates
// the IdP; a tenant:admin edits only its own tenant's group→role mapping, a separate
// endpoint). The per-tenant config only RESOLVES at login in an enterprise build (the
// MultiIDP cap-lift); in the open build it can be stored but the single-active cap
// refuses activating a second IdP. A malformed tenant id is a 400 — never silently
// treated as global — and the global scope must be reached via the base route, so the
// two surfaces stay distinct and separately auditable.
func (s *Server) ssoScope(w http.ResponseWriter, r *http.Request) (model.TenantID, bool) {
	raw := chi.URLParam(r, "tenant")
	if raw == "" {
		return auth.GlobalFederationScope, true
	}
	t, err := model.ParseTenantID(raw)
	if err != nil {
		s.badRequest(w, r, "invalid tenant id")
		return "", false
	}
	if t == auth.GlobalFederationScope {
		s.badRequest(w, r, "use the global SSO route for the deployment-wide config")
		return "", false
	}
	return t, true
}

// ssoAlias resolves the IdP alias a request targets (U4): the {alias} path param on
// the per-IdP admin routes (/console/sso[/tenants/{tenant}]/idps/{alias}), or the reserved
// "default" primary on the base routes (no param). A malformed alias is a 400 BEFORE any
// store round-trip (ValidateFederationAlias), so a bad path segment never becomes a row or
// a 404; the value is normalized (trimmed, lowercased) so case/whitespace can't fork the
// same IdP.
func (s *Server) ssoAlias(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "alias")
	if raw == "" {
		return model.DefaultFederationAlias, true // the base route targets the primary
	}
	// A present-but-blank segment (whitespace) is malformed input, NOT the primary — only
	// the base route (no segment) resolves to "default". Reject it before normalization
	// would otherwise fold it onto "default".
	if strings.TrimSpace(raw) == "" {
		s.writeError(w, r, auth.ErrBadFederationAlias)
		return "", false
	}
	alias := model.NormalizeFederationAlias(raw)
	if err := auth.ValidateFederationAlias(alias); err != nil {
		s.writeError(w, r, err)
		return "", false
	}
	return alias, true
}

// handleListSSOIdPs lists every IdP configured under a scope (U4), default first.
// No secrets — only hints (toSSOConfigDTO).
func (s *Server) handleListSSOIdPs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.ssoService(w, r)
	if !ok {
		return
	}
	scope, ok := s.ssoScope(w, r)
	if !ok {
		return
	}
	views, err := svc.ListIdPs(r.Context(), scope)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// groups_mapped_by and routed_by are BUILD capabilities and are identical for every
	// row. enforced_by is NOT: it depends on the row's own (scope, alias), because the
	// posture reader the enforcement engine composes resolves exactly one of them. Hoisting
	// it out of the loop is what stamped "enterprise" onto every IdP in the list.
	redirectURI, groupsMappedBy, routedBy := federationCallbackURL(r), s.ssoGroupsMappedBy(), s.ssoRoutedBy()
	items := make([]ssoConfigDTO, 0, len(views))
	for _, v := range views {
		items = append(items, toSSOConfigDTO(v, scope, redirectURI, s.ssoEnforcedBy(scope, v.Alias), groupsMappedBy, routedBy))
	}
	writeJSON(w, http.StatusOK, map[string]any{"idps": items})
}

func (s *Server) handleGetSSOConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.ssoService(w, r)
	if !ok {
		return
	}
	scope, ok := s.ssoScope(w, r)
	if !ok {
		return
	}
	alias, ok := s.ssoAlias(w, r)
	if !ok {
		return
	}
	view, err := svc.GetConfigIdP(r.Context(), scope, alias)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSSOConfigDTO(view, scope, federationCallbackURL(r), s.ssoEnforcedBy(scope, alias), s.ssoGroupsMappedBy(), s.ssoRoutedBy()))
}

func (s *Server) handlePutSSOConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.ssoService(w, r)
	if !ok {
		return
	}
	scope, ok := s.ssoScope(w, r)
	if !ok {
		return
	}
	alias, ok := s.ssoAlias(w, r)
	if !ok {
		return
	}
	var in ssoConfigInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	view, err := svc.PutConfigIdP(r.Context(), p, scope, alias, in.toParams())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSSOConfigDTO(view, scope, federationCallbackURL(r), s.ssoEnforcedBy(scope, alias), s.ssoGroupsMappedBy(), s.ssoRoutedBy()))
}

func (s *Server) handleDeleteSSOConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.ssoService(w, r)
	if !ok {
		return
	}
	scope, ok := s.ssoScope(w, r)
	if !ok {
		return
	}
	alias, ok := s.ssoAlias(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteConfigIdP(r.Context(), p, scope, alias); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestSSOConfig validates a candidate config (OIDC discovery / SAML metadata
// fetch) without persisting it, so the operator can confirm connectivity before
// saving. Requires the enterprise provider builder (501 otherwise).
func (s *Server) handleTestSSOConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.ssoService(w, r)
	if !ok {
		return
	}
	scope, ok := s.ssoScope(w, r)
	if !ok {
		return
	}
	alias, ok := s.ssoAlias(w, r)
	if !ok {
		return
	}
	var in ssoConfigInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if err := svc.TestConfigIdP(r.Context(), scope, alias, in.toParams()); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
