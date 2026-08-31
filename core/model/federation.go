// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import "strings"

// DefaultFederationAlias is the reserved alias of a scope's PRIMARY IdP (U4):
// the pre-U4 single-config-per-scope row and the global fallback resolve THIS alias.
// A legacy row (added before U4, stored alias NULL/empty) normalizes to it, so the
// single-IdP code paths (Resolve global, Posture, the console "default" surface)
// keep working across the upgrade.
const DefaultFederationAlias = "default"

// NormalizeFederationAlias canonicalizes an IdP alias to the value stored and
// compared: trimmed and lowercased, with an empty result mapped to
// DefaultFederationAlias. Case/whitespace folding here means the (tenant_id,
// target_tenant_id, alias) unique index cannot be defeated by "Okta" vs "okta" vs
// "okta ", and a legacy NULL/empty alias decodes to the default. It does NOT enforce
// the slug charset — the service rejects a malformed alias at write time
// (validateFederationAlias); this only guarantees a non-empty, case-stable key.
func NormalizeFederationAlias(alias string) string {
	if a := strings.ToLower(strings.TrimSpace(alias)); a != "" {
		return a
	}
	return DefaultFederationAlias
}

// NormalizeFederationDomain canonicalizes a claimed email domain (U5) to the value
// stored, compared and matched at login: trimmed, lowercased, with a leading "@" and a
// trailing "." stripped. Case/whitespace folding here means the global-uniqueness check
// and the login-time domain match cannot be defeated by "Corp.COM" vs "corp.com ". It does
// NOT validate the shape (the service rejects a domain with no dot / invalid host at write
// time); it only guarantees a canonical key. A junk input can normalize to "".
func NormalizeFederationDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "@")
	return strings.TrimSuffix(d, ".")
}

// FederationConfig is the MANAGED SSO/IdP configuration (FASE X): the
// store-backed replacement for the env-only federation wiring, so an enterprise
// connects an OIDC/SAML provider from the console instead of a redeploy. Like
// every auth row it lives in the system tenant (BaseFields.TenantID ==
// SystemTenantID); the SCOPE the config applies to is TargetTenantID — V1 stores a
// single GLOBAL config under TargetTenantID == SystemTenantID, and the column is
// present from day one so per-tenant SSO is an additive toggle (a per-tenant row),
// never a schema change.
//
// Secret-bearing fields (the OIDC client secret, the SAML SP private key) are
// SEALED at rest (an AES-256-GCM SecretSealer bound to the scope; never the
// cleartext, never a one-way hash — the engine must replay them to the IdP) and
// carry a non-secret *Hint (a SHA-256 fingerprint prefix) for display. The public
// SAML SP certificate is stored in the clear (it is public verifier material).
type FederationConfig struct {
	BaseFields
	// TargetTenantID is the scope this config governs. SystemTenantID is the global
	// (deployment-wide) config — the only one V1 resolves; a real tenant id is a
	// per-tenant config (additive, future).
	TargetTenantID TenantID
	// Alias is the IdP's stable, scope-unique identifier (U4): the first-class
	// IdP entity key that lets MORE THAN ONE config coexist under one TargetTenantID.
	// The reserved value "default" is the scope's primary IdP — the pre-U4
	// single-config-per-scope row and the global fallback resolve THIS alias, and a
	// legacy row (added before U4, stored alias NULL) reads as "default" (the codec
	// normalizes NULL/empty → "default"). Additional IdPs carry an operator-chosen
	// slug (validated lowercase [a-z0-9-]); it is the routing key the U5 home-realm
	// login flow (?idp=<alias>) and the U8 selection ladder key off. Uniqueness is
	// (tenant_id, target_tenant_id, alias); appended LAST so the additive reconcile
	// ALTERs an existing DB and v2 regenerates it on a fresh one.
	Alias string
	// Protocol is "oidc" or "saml" (empty = unconfigured).
	Protocol string
	// Status is active (enabled) or inactive (disabled but retained). A disabled
	// config makes SSO login answer 501 even though the row exists.
	Status LifecycleStatus

	// OIDC.
	OIDCIssuer             string
	OIDCClientID           string
	OIDCClientSecretSealed string
	OIDCClientSecretHint   string

	// SAML.
	SAMLMetadataURL string
	SAMLEntityID    string
	SAMLACSURL      string
	SAMLIDPSSOURL   string
	SAMLEmailAttr   string
	// SP ENCRYPTION keypair (RSA only): decrypts IdP-encrypted assertions. The
	// public cert is stored in the clear; the private key is sealed.
	SAMLSPCertPEM   string // public verifier material (not sealed)
	SAMLSPKeySealed string
	SAMLSPKeyHint   string
	// SP SIGNING keypair (RSA or EC): signs AuthnRequests and is published in
	// the SP metadata as the use="signing" KeyDescriptor. Independent of the
	// encryption keypair; the public cert is in the clear, the private key sealed.
	SAMLSPSignCertPEM   string // public verifier material (not sealed)
	SAMLSPSignKeySealed string
	SAMLSPSignKeyHint   string

	// LOGIN-ENFORCEMENT POSTURE. Non-secret operator intent stored ON the SSO
	// config row (one posture per scope, like the IdP it governs). The OPEN build
	// stores and serves these but NEVER enforces them — the reserved enterprise
	// add-on (enterprise/ssoenforce, build-tag gated) reads them and decides; the open
	// build reports enforced_by=unavailable and never fakes the control (LICENSING.md).
	// They are symmetric with the seat cap: the knob lives in the open core, the
	// enforcement is closed (LICENSING.md). Both are appended LAST so the additive
	// reconcile (sqlstore/schema.go) issues a nullable ALTER TABLE ADD COLUMN on an
	// existing DB and a fresh DB generates them — no hand-authored migration.
	//
	// RequireSSO is the operator's intent to block PASSWORD login (the enterprise
	// engine honors it only while an IdP is ACTIVE — anti-lockout). NetworkAllowCIDRs
	// is a deny-closed IP allow-list over the login surface (empty ⇒ no restriction).
	RequireSSO        bool
	NetworkAllowCIDRs []string

	// GROUP MAPPING + JIT COHERENCE. Non-secret operator intent, appended
	// LAST so the additive reconcile (sqlstore/schema.go) issues a nullable ALTER
	// TABLE ADD COLUMN on an existing DB — no hand-authored migration — exactly like
	// the posture above.
	//
	// OIDCGroupsClaim / SAMLGroupsAttr name the claim (OIDC) or attribute (SAML) the
	// provider reads the subject's directory groups from ("" ⇒ groups not read; the
	// open build extracts them, the reserved enterprise GroupMapper maps them). The
	// values are OPEN-CORE config (part of single-IdP SSO); the MAPPING is reserved.
	OIDCGroupsClaim string
	SAMLGroupsAttr  string
	// SCIMAuthoritative (D4) makes SCIM the SOLE authority for this scope: when
	// true, an SSO login NEVER JIT-provisions a new local user (it only authenticates
	// an already-provisioned one) and login-time group reconciliation is suppressed
	// (SCIM owns the roster). Default false ⇒ the pre behavior (JIT provisions;
	// login-driven group sync is add-only). Fail-inert: turning it on can only
	// REFUSE, never silently create.
	SCIMAuthoritative bool

	// ClaimedDomains are the email domains this IdP serves (U5 home-realm routing,
	// D7): an email-first login for user@<domain> routes to the IdP that claims <domain>.
	// GLOBALLY UNIQUE — a domain is claimed by at most one IdP across every scope — and
	// superadmin-attested (the superadmin owns the IdP config, D8, so setting a domain IS
	// its verification). Non-secret operator intent, appended LAST so the additive
	// reconcile issues a nullable ALTER TABLE ADD COLUMN — like NetworkAllowCIDRs. The
	// domain is ONLY a routing key: the IdP assertion remains the authority for identity,
	// and the deny-closed membership gate still governs admission. The ROUTING is reserved
	// enterprise (the base build stores domains but never selects by them — single global
	// IdP); domain selection is the MultiIDP capability.
	ClaimedDomains []string
}

// FederationDomainClaim is the DERIVED, unique-constrained home-realm routing index
// (U8): one row per email domain a FederationConfig claims, so a login for
// user@<domain> resolves the owning IdP by an INDEXED point lookup instead of scanning
// every config's ClaimedDomains. FederationConfig.ClaimedDomains stays the authoritative,
// operator-facing list (it round-trips to the console); this table is a projection of it,
// maintained inside the SAME write transaction as the config and converged at boot
// (FederationService.ReconcileDomainClaims), so the two never drift.
//
// Like every auth row it lives in the system tenant (BaseFields.TenantID == SystemTenantID);
// TargetTenantID records the owning config's SCOPE so a tenant-confined lookup is also a
// point lookup. Because every row shares SystemTenantID as tenant_id, the UNIQUE(tenant_id,
// domain) index makes a claimed domain GLOBALLY unique at the STORAGE layer — hardening U5's
// application-level, best-effort-under-Postgres-READ-COMMITTED uniqueness scan into a
// commit-time constraint that closes the concurrent-claim race (the second writer's INSERT
// fails rather than committing a duplicate that would break domain routing for both tenants).
type FederationDomainClaim struct {
	BaseFields
	// TargetTenantID is the SCOPE of the config that claims this domain (the config's own
	// TargetTenantID), so a tenant-hinted login can point-look-up within its scope.
	TargetTenantID TenantID
	// ConfigID is the FederationConfig this claim derives from.
	ConfigID ID
	// Domain is the normalized (NormalizeFederationDomain) claimed email domain — globally
	// unique via the (tenant_id, domain) index.
	Domain string
}
