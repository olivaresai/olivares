// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Managed SSO/IdP configuration (FASE X, carved open-core): the
// store-backed replacement for the env-only federation wiring. The CONFIG
// (model.FederationConfig) is plain data; the PROVIDER is built by an injected
// FederationBuilder. Since the single-IdP OIDC/SAML builder is OPEN-CORE
// (core/auth/federation, wired in BOTH builds), so the base build does real
// single-IdP login from managed config — go-oidc/crewjam ARE in core's tree now.
//
// The open build resolves the single GLOBAL config (TargetTenantID ==
// SystemTenantID) and enforces the single-IdP CAP: at most one ACTIVE config
// deployment-wide. The reserved enterprise MultiIDP capability (nil in the base
// build) lifts the cap and resolves the request tenant's own IdP first, falling
// back to global. SSO-enforcement/posture (require-SSO + network/IP allow-list)
// SHIPPED in as the reserved enterprise add-on (enterprise/ssoenforce): the open
// build STORES the posture on this config (RequireSSO / NetworkAllowCIDRs) and serves
// it (Posture, enforced_by=unavailable) but NEVER enforces it — the closed engine
// decides (login_policy.go). Managed SCIM is still reserved (LICENSING.md); the open
// build never fakes either.

// GlobalFederationScope is the TargetTenantID under which the deployment-wide
// (V1) SSO config is stored.
var GlobalFederationScope = model.SystemTenantID

// ErrFederationBuilderUnavailable means no SSO provider builder is wired. Since
// The single-IdP OIDC/SAML builder is OPEN-CORE and wired in BOTH builds, so
// this only surfaces for an embedder/test that injected a nil builder; login fails
// closed to NoFederation (501) in that case.
var ErrFederationBuilderUnavailable = errors.New("auth: SSO provider builder unavailable")

// ErrMultiIDPRequiresEnterprise is the explicit, honest PRODUCT limit of the open
// (AGPL) build: SSO login from ONE IdP is open-core, but resolving more
// than one ACTIVE IdP per deployment (per-tenant multi-IdP, IdP-by-domain) is the
// reserved enterprise line. Activating a second federation config while another is
// already active returns this — never a generic 501, never a crash. It is a
// product cut, not a security control. The enterprise build injects a MultiIDP
// capability (enterprise/federation) that lifts it.
var ErrMultiIDPRequiresEnterprise = errors.New("auth: multi_idp_requires_enterprise: a second active SSO IdP requires the enterprise build")

// ErrScopeActiveIdPExists is the U4 per-scope single-active rule (BOTH builds):
// a scope may hold SEVERAL IdP configs (the first-class IdP entity, keyed by alias),
// but only ONE may be ACTIVE at a time, because nothing in the login flow yet selects
// among several active IdPs of one scope — resolving them by lexicographic accident
// would silently change which identity domain a tenant's users authenticate against.
// Switching the active IdP is therefore an explicit deactivate-then-activate flip
// (audited), never an implicit steal. U5 relaxes this to one active IdP per (scope,
// claimed email domain), once the home-realm login flow can disambiguate by domain.
var ErrScopeActiveIdPExists = errors.New("auth: scope_active_idp_exists: this scope already has an active SSO IdP; deactivate it before activating another")

// ErrBadFederationAlias is returned for a malformed IdP alias (slug validation).
var ErrBadFederationAlias = errors.New("auth: invalid SSO IdP alias")

// ErrGlobalIdPMustBeDefault refuses ACTIVATING a non-"default" IdP in the GLOBAL scope
// (U4): the deployment-wide login resolves the single "default" global IdP, and
// nothing routes among several active GLOBAL IdPs yet — that is the U5 home-realm-by-
// domain line. A non-default global IdP may be STAGED (inactive) but not activated, so an
// operator can never reach a "console shows it active but login ignores it" state.
var ErrGlobalIdPMustBeDefault = errors.New("auth: global_idp_must_be_default: the deployment-wide login IdP must use the \"default\" alias")

// ErrDomainClaimed enforces GLOBAL uniqueness of a home-realm email domain (U5, D7):
// a domain is claimed by at most one IdP config across every scope. Because the domain
// determines WHICH IdP authenticates a home-realm login, a duplicate would let one config
// silently steer another tenant's users — so the claim is refused at write time.
var ErrDomainClaimed = errors.New("auth: domain_claimed: this email domain is already claimed by another SSO IdP")

// federationAliasPattern is the canonical slug an IdP alias must match (after
// model.NormalizeFederationAlias trims + lowercases it): a DNS-label-like token, 1–31
// chars of [a-z0-9-] starting alphanumeric. It is safe as a URL segment (the U5
// ?idp=<alias> home-realm routing key), an audit value and a unique-index key, and
// cannot be confused by case or whitespace. "default" (the reserved primary alias)
// matches it.
var federationAliasPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30})$`)

// federationDomainPattern is the RFC 1123 host shape a normalized claimed domain must
// match (U5): one or more labels of 1–63 [a-z0-9-] chars (alphanumeric start/end)
// separated by dots, at least one dot. It is ASCII-only, so an IDN must be entered in its
// punycode (xn--…) form — the same canonical form used at login-time equality matching.
var federationDomainPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// validateFederationAlias rejects a malformed IdP alias at write time (deny-closed),
// so a bad value never becomes a distinct row, a 404, or a confusable duplicate.
func validateFederationAlias(alias string) error {
	if a := model.NormalizeFederationAlias(alias); !federationAliasPattern.MatchString(a) {
		return fmt.Errorf("%w: %q must be 1-31 chars of [a-z0-9-] starting alphanumeric", ErrBadFederationAlias, alias)
	}
	return nil
}

// ValidateFederationAlias is the exported alias validator for the API layer: the
// per-IdP admin handlers reject a malformed {alias} path segment with 400 BEFORE any
// store round-trip. See validateFederationAlias.
func ValidateFederationAlias(alias string) error { return validateFederationAlias(alias) }

// FederationSealer seals/opens a secret-bearing SSO field at rest, bound to the
// config's scope so a ciphertext cannot be replayed across scopes. The
// composition root implements it over an engine-held key (cmd/olivares); the core
// never sees key material. Same shape as the eventing SecretSealer.
type FederationSealer interface {
	Seal(ctx context.Context, scope model.TenantID, plaintext []byte) (string, error)
	Open(ctx context.Context, scope model.TenantID, sealed string) ([]byte, error)
}

// FederationParams is the PLAINTEXT configuration handed to a FederationBuilder to
// construct a provider. Secrets are opened from the sealed store fields just
// before the build and never persisted on this struct.
type FederationParams struct {
	Protocol string

	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	// OIDCGroupsClaim names the ID-token/UserInfo claim carrying the subject's
	// directory groups (U1); "" ⇒ groups not read. Open-core config.
	OIDCGroupsClaim string

	SAMLMetadataURL string
	SAMLEntityID    string
	SAMLACSURL      string
	SAMLIDPSSOURL   string
	SAMLEmailAttr   string
	// SAMLGroupsAttr names the multi-valued SAML attribute carrying the subject's
	// directory groups (U1); "" ⇒ groups not read. Open-core config.
	SAMLGroupsAttr string
	SAMLSPCertPEM  string // encryption keypair (RSA)
	SAMLSPKeyPEM   string
	// SAML SP signing keypair (RSA or EC): signs AuthnRequests and is
	// published as the use="signing" KeyDescriptor.
	SAMLSPSignCertPEM string
	SAMLSPSignKeyPEM  string
}

// FederationBuilder constructs a provider from plaintext params. It is injected
// under -tags enterprise (federation.FromConfig); nil in the base build.
type FederationBuilder func(ctx context.Context, p FederationParams) (Federation, error)

// FederationResolver returns the active SSO provider for a login. The SSO login
// handlers depend on this seam, not on a static provider.
type FederationResolver interface {
	// Resolve returns the provider for the given request tenant. The open build
	// always returns the single global provider; with a MultiIDP capability wired
	// (enterprise) it resolves the request tenant's IdP first, else the global one.
	Resolve(ctx context.Context, tenant model.TenantID) (Federation, error)
}

// MultiIDP is the RESERVED ENTERPRISE capability: per-tenant / by-domain
// resolution of more than one active SSO IdP. It is injected ONLY by the
// enterprise composition root (enterprise/federation, under -tags enterprise); it
// is nil in the base AGPL build, which is what enforces the single-IdP cap (the
// open binary literally has no code to pick among several IdPs — this is the
// GitLab ee/ seam, not a flag that disables linked code). Its mere presence lifts
// the single-active-config cap in PutConfig.
type MultiIDP interface {
	// SelectActive picks the active FederationConfig for a login from all active configs,
	// using every signal in the SelectionInput (U5): an explicit ?idp=<alias> within a
	// hinted tenant, else a hinted tenant confines the choice to that tenant (its
	// domain-matching IdP, else its domainless default), else — a home-realm login with NO
	// tenant — a cross-tenant lookup of the IdP that claims the email domain. ok=false means
	// "no match — fall back to the global IdP". A domain that is ambiguously claimed (a
	// uniqueness breach) resolves DENY-CLOSED to ok=false, never a nondeterministic pick.
	SelectActive(in SelectionInput, active []model.FederationConfig) (model.FederationConfig, bool)

	// AllowsAdditionalActiveIdP is asked by PutConfig AT CALL TIME, every time, before it
	// activates a second IdP. nil means allowed; any error is returned to the caller as-is,
	// so a refusal arrives carrying its own name instead of a generic cap error.
	//
	// WHY A QUESTION AND NOT THE MERE PRESENCE OF THIS CAPABILITY. Until now the cap was a
	// nil-check: wiring a MultiIDP lifted it, full stop. The enterprise composition root wires
	// one unconditionally, so a customer who bought a DIFFERENT pack got multi-IdP for free —
	// the capability is compiled into one binary and entitlement is per pack, so "present in
	// the build" and "paid for" stopped being the same thing.
	//
	// AND WHY THE OBVIOUS FIX WAS REJECTED, measured by another lane against this repo's own
	// enterprise/federation/groupmap.go: refusing to CONSTRUCT the capability when the term has
	// lapsed is evaluated ONCE, at boot. That breaks the hot-apply contract addongate states at
	// addongate.go:201-203, makes start-up ORDER decide the answer exactly where
	// addongate_enterprise.go:37-42 says it must not, and a `return nil` throws away the NAME of
	// the refusal — the caller gets the open-core cap error and is told to buy a build it
	// already has. Asking at call time keeps all three properties.
	//
	// ⛔ THE OPEN BINARY STILL CONSULTS NO LICENSE, and that is the whole point of the shape.
	// This is an interface method on a capability the AGPL build never wires: with multiIDP nil
	// the cap is enforced exactly as before, by code that knows nothing about entitlement. The
	// license check lives in the enterprise implementation, on the other side of the seam —
	// ADR-0010 intact: the license never gates the OPEN binary.
	//
	// It is gateable at all because the cap is, in this file's own words, "a product cut, not a
	// security control". A security control would not be for sale.
	AllowsAdditionalActiveIdP(ctx context.Context) error
}

// SelectionInput carries every signal a login can use to pick an IdP (U5): the
// optional request tenant (?tenant=), the home-realm email domain (?domain=, email-first
// login), and an explicit IdP alias (?idp=). Empty fields mean "not provided". The open
// build ignores all of it (single global IdP); the reserved MultiIDP capability selects
// among them.
type SelectionInput struct {
	Tenant      model.TenantID
	EmailDomain string
	Alias       string
}

// ResolvedIdP names the IdP a login actually resolved to (U5), so the callback
// completes against the SAME IdP and the SELECTED tenant — not the raw ?tenant= hint —
// governs JIT/SCIM/group reconciliation, and the asserted email can be constrained to the
// IdP's claimed domains. It carries NO secrets.
type ResolvedIdP struct {
	// Scope is the authenticating tenant (the selected config's TargetTenantID). This, not
	// the request hint, is the scope CompleteSSO must use so D4 (SCIM-authoritative) and U2
	// (group reconciliation) key on the IdP that actually validated the assertion.
	Scope model.TenantID
	Alias string
	// ClaimedDomains lets the callback enforce that the IdP only vouches for identities in
	// the domains it claimed (empty ⇒ unconstrained, e.g. the global/default IdP).
	ClaimedDomains []string
	// SCIMAuthoritative is the RESOLVED IdP's D4 flag, so CompleteSSO reads SCIM authority
	// from the config that actually authenticated the user — not a scope-LIMIT-1 lookup that
	// (post-U4, several configs per scope) could read a sibling's flag and bypass D4.
	SCIMAuthoritative bool
}

// AllowsEmail is the U5 domain boundary: an IdP that claims domains may only vouch for
// identities whose email is in those domains, so a (mis)configured or compromised IdP cannot
// assert an out-of-domain address to seize another account via the email-fallback path. An
// IdP with NO claimed domains (the global/default) is unconstrained, preserving single-IdP
// behavior. The comparison is on the normalized domain, matching how domains are stored.
func (r ResolvedIdP) AllowsEmail(email string) bool {
	if len(r.ClaimedDomains) == 0 {
		return true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return false // no domain to match against a constrained IdP
	}
	dom := model.NormalizeFederationDomain(email[at+1:])
	for _, d := range r.ClaimedDomains {
		if d == dom {
			return true
		}
	}
	return false
}

// FederationConfigInput is the console's create/update payload. An empty secret
// field means "keep the sealed value already stored" (so editing config does not
// force re-entering the client secret / SP key).
type FederationConfigInput struct {
	Protocol string
	Enabled  bool

	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string // "" = keep existing sealed value

	SAMLMetadataURL string
	SAMLEntityID    string
	SAMLACSURL      string
	SAMLIDPSSOURL   string
	SAMLEmailAttr   string
	SAMLSPCertPEM   string
	SAMLSPKeyPEM    string // "" = keep existing sealed value
	// SAML SP signing keypair. An empty key keeps the existing sealed value.
	SAMLSPSignCertPEM string
	SAMLSPSignKeyPEM  string // "" = keep existing sealed value

	// Login-enforcement posture, non-secret. Unlike the secret fields these
	// are REPLACED verbatim on every write (no "blank keeps existing"): require_sso is
	// a plain toggle and the CIDR list is the operator's full intended set, so an empty
	// list clears the allow-list. Malformed CIDRs are rejected at write time
	// (validateFederationInput); the open build persists them but never enforces them.
	RequireSSO        bool
	NetworkAllowCIDRs []string

	// Group mapping + JIT coherence, non-secret, replaced verbatim on every
	// write (like the posture, not "blank keeps existing"). OIDCGroupsClaim /
	// SAMLGroupsAttr name where the provider reads the subject's groups;
	// SCIMAuthoritative makes SCIM the sole authority (SSO login never JIT-creates).
	OIDCGroupsClaim   string
	SAMLGroupsAttr    string
	SCIMAuthoritative bool

	// ClaimedDomains (U5) are the email domains this IdP serves (home-realm routing),
	// replaced verbatim on every write. Normalized + validated + checked for GLOBAL
	// uniqueness at write time; an empty list means "no domain routing" (the IdP is only
	// reachable by tenant/alias). The open build stores them but never routes by them.
	ClaimedDomains []string
}

// FederationConfigView is the console's read shape: every field EXCEPT secrets,
// plus non-secret hints and whether this build can activate SSO.
type FederationConfigView struct {
	Configured        bool
	ProviderAvailable bool
	// Alias is the IdP's scope-unique identifier (U4); "default" is the scope's
	// primary IdP. The console lists a scope's IdPs by alias and edits one at a time.
	Alias    string
	Protocol string
	Status   string

	OIDCIssuer           string
	OIDCClientID         string
	OIDCClientSecretHint string

	SAMLMetadataURL string
	SAMLEntityID    string
	SAMLACSURL      string
	SAMLIDPSSOURL   string
	SAMLEmailAttr   string
	SAMLSPCertPEM   string
	SAMLSPKeyHint   string
	// SAML SP signing keypair: the public cert and a non-secret key hint.
	SAMLSPSignCertPEM string
	SAMLSPSignKeyHint string

	// Login-enforcement posture, non-secret. The console renders these plus the
	// build's enforced_by signal (computed by the API from whether a LoginPolicy is
	// wired) so it can show "configured but NOT enforced by this build" honestly.
	RequireSSO        bool
	NetworkAllowCIDRs []string

	// Group mapping + JIT coherence, non-secret. The console shows the
	// configured claim/attr and whether SCIM is authoritative; whether the asserted
	// groups actually MAP is a build capability the API adds as a separate
	// groups_mapped_by signal (computed from whether the reserved GroupMapper is
	// wired), symmetric with the posture's enforced_by — never on this view.
	OIDCGroupsClaim   string
	SAMLGroupsAttr    string
	SCIMAuthoritative bool

	// ClaimedDomains (U5) are the email domains this IdP serves for home-realm
	// routing. The console shows them; whether the build ROUTES by them is a capability
	// signal (the reserved MultiIDP), symmetric with groups_mapped_by / enforced_by.
	ClaimedDomains []string

	UpdatedAt model.Timestamp
}

// FederationService owns the managed SSO config: storage (sealed secrets), the
// console CRUD, and provider resolution (cached per scope by row version). It
// implements FederationResolver.
type FederationService struct {
	st      store.Store
	sealer  FederationSealer
	builder FederationBuilder
	// fallback is the provider used when NO managed config row exists for the
	// scope — the env-configured provider (or NoFederation), so existing env-only
	// deployments keep working until an operator configures SSO from the console.
	// A stored config (enabled OR disabled) is authoritative and overrides it.
	fallback Federation
	// multiIDP is the reserved enterprise capability: nil in the base build
	// (single-IdP cap enforced; login resolves only the global config), non-nil
	// under -tags enterprise (per-tenant multi-IdP, cap lifted). See MultiIDP.
	multiIDP MultiIDP

	mu sync.Mutex
	// cache is keyed by the config's own ID (U4), NOT its scope: several IdPs can
	// share a scope, and switching which one is active must not return a stale provider
	// from a sibling that happened to share a row version. Writes clear it wholesale
	// (invalidateAll) — the set of IdPs is tiny and writes are rare.
	cache map[model.ID]cachedFederation
}

type cachedFederation struct {
	version int64
	fed     Federation
}

var _ FederationResolver = (*FederationService)(nil)

// NewFederationService builds the service. A nil sealer makes config writes
// fail closed (a secret can never be stored in cleartext); a nil builder leaves
// SSO inert (login 501) while config can still be stored. Since the
// single-IdP builder is open-core, so the base build wires a real one. The
// fallback provider (env-configured, or NoFederation) is used only when NO
// managed config row exists, so env-only deployments keep working. multiIDP is
// the reserved enterprise capability (nil in the base build → single-IdP cap).
func NewFederationService(st store.Store, sealer FederationSealer, builder FederationBuilder, fallback Federation, multiIDP MultiIDP) *FederationService {
	if fallback == nil {
		fallback = NoFederation{}
	}
	return &FederationService{st: st, sealer: sealer, builder: builder, fallback: fallback, multiIDP: multiIDP, cache: map[model.ID]cachedFederation{}}
}

// configsForScope drains EVERY IdP config row of a scope (U4 — a scope may hold
// several, keyed by alias). The set is tiny, so a full drain is cheap; the page bound
// is a runaway guard.
func (s *FederationService) configsForScope(ctx context.Context, scope model.TenantID) ([]model.FederationConfig, error) {
	var rows []model.FederationConfig
	err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		r, e := drainList(ctx, as.FederationConfigs().List, byEq("target_tenant_id", scope.String(), 0))
		rows = r
		return e
	})
	return rows, err
}

// loadConfigByAlias returns a scope's IdP config for a given alias, ok=false if none.
// The match is on the DECODED alias (the codec normalizes a stored NULL/empty →
// "default"), NOT a SQL predicate — so a legacy pre-U4 row (alias NULL) is correctly
// found under the reserved "default" alias even before the boot-time backfill rewrites
// it. This is what keeps an upgraded deployment's global/default IdP reachable.
func (s *FederationService) loadConfigByAlias(ctx context.Context, scope model.TenantID, alias string) (model.FederationConfig, bool, error) {
	want := model.NormalizeFederationAlias(alias)
	rows, err := s.configsForScope(ctx, scope)
	if err != nil {
		return model.FederationConfig{}, false, err
	}
	for _, c := range rows {
		if c.Alias == want {
			return c, true, nil
		}
	}
	return model.FederationConfig{}, false, nil
}

// loadConfig returns a scope's PRIMARY ("default") IdP config — the single-IdP path
// that Resolve (global), Posture and the console default surface share.
func (s *FederationService) loadConfig(ctx context.Context, scope model.TenantID) (model.FederationConfig, bool, error) {
	return s.loadConfigByAlias(ctx, scope, model.DefaultFederationAlias)
}

// viewOf builds the console view (no secrets) of a stored config row.
func (s *FederationService) viewOf(cfg model.FederationConfig) FederationConfigView {
	return FederationConfigView{
		// A row with no protocol is a deleted/tombstoned config — report it as
		// unconfigured (DeleteConfig clears the DEFAULT row rather than removing it, so the
		// "off" state authoritatively overrides any env-configured fallback at login).
		Configured:           cfg.Protocol != "",
		ProviderAvailable:    s.builder != nil,
		Alias:                model.NormalizeFederationAlias(cfg.Alias),
		Protocol:             cfg.Protocol,
		Status:               string(cfg.Status),
		OIDCIssuer:           cfg.OIDCIssuer,
		OIDCClientID:         cfg.OIDCClientID,
		OIDCClientSecretHint: cfg.OIDCClientSecretHint,
		SAMLMetadataURL:      cfg.SAMLMetadataURL,
		SAMLEntityID:         cfg.SAMLEntityID,
		SAMLACSURL:           cfg.SAMLACSURL,
		SAMLIDPSSOURL:        cfg.SAMLIDPSSOURL,
		SAMLEmailAttr:        cfg.SAMLEmailAttr,
		SAMLSPCertPEM:        cfg.SAMLSPCertPEM,
		SAMLSPKeyHint:        cfg.SAMLSPKeyHint,
		SAMLSPSignCertPEM:    cfg.SAMLSPSignCertPEM,
		SAMLSPSignKeyHint:    cfg.SAMLSPSignKeyHint,
		RequireSSO:           cfg.RequireSSO,
		NetworkAllowCIDRs:    cfg.NetworkAllowCIDRs,
		OIDCGroupsClaim:      cfg.OIDCGroupsClaim,
		SAMLGroupsAttr:       cfg.SAMLGroupsAttr,
		SCIMAuthoritative:    cfg.SCIMAuthoritative,
		ClaimedDomains:       cfg.ClaimedDomains,
		UpdatedAt:            cfg.UpdatedAt,
	}
}

// GetConfigIdP returns the console view of one IdP (scope, alias); an unconfigured
// alias yields the empty view (Configured=false) carrying the alias + build capability,
// so the console can render a create form for it.
func (s *FederationService) GetConfigIdP(ctx context.Context, scope model.TenantID, alias string) (FederationConfigView, error) {
	cfg, ok, err := s.loadConfigByAlias(ctx, scope, alias)
	if err != nil {
		return FederationConfigView{}, err
	}
	if !ok {
		return FederationConfigView{ProviderAvailable: s.builder != nil, Alias: model.NormalizeFederationAlias(alias)}, nil
	}
	return s.viewOf(cfg), nil
}

// GetConfig returns the console view of a scope's PRIMARY ("default") IdP (no secrets).
func (s *FederationService) GetConfig(ctx context.Context, scope model.TenantID) (FederationConfigView, error) {
	return s.GetConfigIdP(ctx, scope, model.DefaultFederationAlias)
}

// ListIdPs returns the console view of EVERY IdP configured under a scope (U4),
// deterministically ordered (the "default" primary first, then alias ascending). A
// tombstoned default (protocol "") is included as Configured=false so the console can
// re-enable it; it never carries a secret (viewOf copies only hints).
func (s *FederationService) ListIdPs(ctx context.Context, scope model.TenantID) ([]FederationConfigView, error) {
	rows, err := s.configsForScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	views := make([]FederationConfigView, 0, len(rows))
	for _, c := range rows {
		views = append(views, s.viewOf(c))
	}
	sort.SliceStable(views, func(i, j int) bool {
		di := views[i].Alias == model.DefaultFederationAlias
		dj := views[j].Alias == model.DefaultFederationAlias
		if di != dj {
			return di // default first
		}
		return views[i].Alias < views[j].Alias
	})
	return views, nil
}

// Configured reports whether a scope has an SSO provider configured, including
// the supported environment-backed fallback for the deployment-wide scope.
// A stored default row is authoritative: even a tombstone suppresses the
// fallback, matching ResolveLogin. Non-default rows do not suppress it because
// resolveGlobal still uses the fallback when the default row is absent.
func (s *FederationService) Configured(ctx context.Context, scope model.TenantID) (bool, error) {
	rows, err := s.configsForScope(ctx, scope)
	if err != nil {
		return false, err
	}
	hasDefault := false
	for _, cfg := range rows {
		if cfg.Protocol != "" {
			return true, nil
		}
		if model.NormalizeFederationAlias(cfg.Alias) == model.DefaultFederationAlias {
			hasDefault = true
		}
	}
	return scope == GlobalFederationScope && !hasDefault && s.fallback.Protocol() != "", nil
}

// PutConfig creates or updates a scope's PRIMARY ("default") IdP — the single-IdP
// surface preserved across U4 for existing callers (the base console route, tests).
func (s *FederationService) PutConfig(ctx context.Context, actor Principal, scope model.TenantID, in FederationConfigInput) (FederationConfigView, error) {
	return s.PutConfigIdP(ctx, actor, scope, model.DefaultFederationAlias, in)
}

// PutConfigIdP creates or updates ONE IdP identified by (scope, alias) (U4),
// sealing any newly supplied secrets and preserving the existing sealed value when a
// secret field is empty. Deny-closed: a write with no sealer wired is refused (never
// cleartext); a malformed alias is refused. It enforces two activation caps atomically
// inside the write transaction (see the cap block below).
func (s *FederationService) PutConfigIdP(ctx context.Context, actor Principal, scope model.TenantID, alias string, in FederationConfigInput) (FederationConfigView, error) {
	if err := validateFederationAlias(alias); err != nil {
		return FederationConfigView{}, err
	}
	if err := validateFederationInput(in); err != nil {
		return FederationConfigView{}, err
	}
	alias = model.NormalizeFederationAlias(alias)
	// The global scope's login resolves the single "default" IdP (multi-global-IdP by
	// domain is U5), so a non-default global IdP may be staged but never ACTIVATED — this
	// prevents an unreachable "active but not resolved" global config.
	if in.Enabled && scope == GlobalFederationScope && alias != model.DefaultFederationAlias {
		return FederationConfigView{}, ErrGlobalIdPMustBeDefault
	}
	existing, found, err := s.loadConfigByAlias(ctx, scope, alias)
	if err != nil {
		return FederationConfigView{}, err
	}
	// Carry forward existing sealed secrets; seal newly supplied ones.
	next := existing
	next.TargetTenantID = scope
	next.Alias = alias
	next.Protocol = in.Protocol
	if in.Enabled {
		next.Status = model.StatusActive
	} else {
		next.Status = model.StatusInactive
	}
	next.OIDCIssuer = in.OIDCIssuer
	next.OIDCClientID = in.OIDCClientID
	next.SAMLMetadataURL = in.SAMLMetadataURL
	next.SAMLEntityID = in.SAMLEntityID
	next.SAMLACSURL = in.SAMLACSURL
	next.SAMLIDPSSOURL = in.SAMLIDPSSOURL
	next.SAMLEmailAttr = in.SAMLEmailAttr
	// SP KEYPAIRS: the PUBLIC cert follows the SAME "blank = keep" rule as its SEALED
	// private key (below). It is one credential in two halves, and a half-pair does not
	// degrade — it FAILS: samlFromParts enters on `cert != "" || key != ""` and
	// loadSigning/EncryptionKeypair then rejects the missing half with ErrNotConfigured, so
	// the provider never builds and SSO login through THIS IdP stops (password login is a
	// separate path; a total lockout also needs require-SSO enforced on top).
	//
	// Replacing the cert verbatim while preserving the key made that state reachable from
	// any client that omits the field — the console omitted the signing pair entirely. The
	// same rule for both halves removes the OMISSION route; the mixed state is still
	// reachable BY CONSTRUCTION (a new IdP with one half, a unilateral rotation), and
	// validateResolvedSPKeypairs below refuses that.
	//
	// ⚠ There is NO selective "clear this keypair": absent and "" are the same Go zero here,
	// so neither can mean "delete". Removing key material today means deleting the whole IdP
	// (DeleteConfigIdP), which also destroys protocol, URLs, claims, posture and domains.
	// That is a real gap, not a design — closing it needs a tri-state input (absent = keep,
	// explicit clear = drop BOTH halves, value = replace).
	next.SAMLSPCertPEM = resolvePublic(in.SAMLSPCertPEM, existing.SAMLSPCertPEM)
	next.SAMLSPSignCertPEM = resolvePublic(in.SAMLSPSignCertPEM, existing.SAMLSPSignCertPEM)
	// Posture (non-secret): replaced verbatim. The CIDR list is normalized
	// (trimmed, empties dropped) so the stored set is clean; validateFederationInput
	// already rejected any malformed CIDR above.
	next.RequireSSO = in.RequireSSO
	next.NetworkAllowCIDRs = normalizeCIDRs(in.NetworkAllowCIDRs)
	// Group mapping + JIT coherence (non-secret, replaced verbatim).
	next.OIDCGroupsClaim = strings.TrimSpace(in.OIDCGroupsClaim)
	next.SAMLGroupsAttr = strings.TrimSpace(in.SAMLGroupsAttr)
	next.SCIMAuthoritative = in.SCIMAuthoritative
	// U5 home-realm domains (non-secret, replaced verbatim). Normalized here;
	// validated + checked for global uniqueness before the write (below).
	next.ClaimedDomains = normalizeDomains(in.ClaimedDomains)

	if in.OIDCClientSecret != "" {
		sealed, hint, err := s.seal(ctx, scope, in.OIDCClientSecret)
		if err != nil {
			return FederationConfigView{}, err
		}
		next.OIDCClientSecretSealed, next.OIDCClientSecretHint = sealed, hint
	}
	if in.SAMLSPKeyPEM != "" {
		sealed, hint, err := s.seal(ctx, scope, in.SAMLSPKeyPEM)
		if err != nil {
			return FederationConfigView{}, err
		}
		next.SAMLSPKeySealed, next.SAMLSPKeyHint = sealed, hint
	}
	if in.SAMLSPSignKeyPEM != "" {
		sealed, hint, err := s.seal(ctx, scope, in.SAMLSPSignKeyPEM)
		if err != nil {
			return FederationConfigView{}, err
		}
		next.SAMLSPSignKeySealed, next.SAMLSPSignKeyHint = sealed, hint
	}
	// A brand-new OIDC config MUST carry a client secret (no existing one to keep).
	if in.Protocol == ProtocolOIDC && next.OIDCClientSecretSealed == "" {
		return FederationConfigView{}, fmt.Errorf("%w: oidc client secret required", ErrBadFederationConfig)
	}
	// The RESOLVED SP keypairs must be whole. "blank = keep" above makes a half-pair
	// unreachable by OMISSION, but not by construction: a brand-new IdP supplying only one
	// half, or a rotation replacing one half of a stored pair, still assembles a config that
	// cannot build. This is the check validateFederationInput already PROMISES ("so a config
	// can never be stored active yet fail to build") and did not perform for key material —
	// it validated only the four SAML URLs. Refusing here is a 400 the operator can act on,
	// instead of a row that saves cleanly and takes SSO down at the next resolve.
	if err := validateResolvedSPKeypairs(next); err != nil {
		return FederationConfigView{}, err
	}

	err = s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// One drain of every config (tiny set) backs both invariants below. Atomicity note:
		// on SQLite the single writer serializes AuthMutate, so drain-then-write is atomic;
		// on Postgres (READ COMMITTED) it is best-effort. For the activation CAP that is
		// acceptable (a product boundary; the open login only resolves the global IdP). For
		// DOMAIN uniqueness it is the strongest guard the index framework allows today (no
		// partial-unique index), and the login SELECTION is deny-closed on a duplicate
		// domain (SelectActive refuses an ambiguous claim), so a racing breach fails safe
		// rather than steering a login to the wrong tenant.
		others, lerr := drainList(ctx, as.FederationConfigs().List, model.Query{})
		if lerr != nil {
			return lerr
		}

		// GLOBAL domain uniqueness (U5, D7): a claimed domain belongs to at most one IdP
		// across every scope, checked for ANY write (active or staged), excluding self.
		if len(next.ClaimedDomains) > 0 {
			want := make(map[string]bool, len(next.ClaimedDomains))
			for _, d := range next.ClaimedDomains {
				want[d] = true
			}
			for _, c := range others {
				if c.ID == next.ID || c.Protocol == "" {
					continue
				}
				for _, d := range c.ClaimedDomains {
					if want[d] {
						return fmt.Errorf("%w: %q", ErrDomainClaimed, d)
					}
				}
			}
		}

		// Activation caps (+ U4/U5) — only when ACTIVATING. Re-saving the SAME IdP
		// (by row ID), deactivating, and staging any number of INACTIVE IdPs are always
		// allowed.
		if next.Status == model.StatusActive {
			for _, c := range others {
				if c.ID == next.ID || c.Status != model.StatusActive || c.Protocol == "" {
					continue // itself, or a non-active / tombstoned row — never counts
				}
				// Rule 1 — open-core deployment cap: the base build (no MultiIDP wired)
				// resolves a SINGLE global IdP, so at most one IdP may be active deployment-wide.
				if s.multiIDP == nil {
					return ErrMultiIDPRequiresEnterprise
				}
				// The capability is present — now ASK IT, at call time, whether this activation is
				// allowed. Presence used to be the whole answer, which meant one binary served every
				// pack and whoever bought a different one got multi-IdP for free. The open build never
				// reaches this line (multiIDP is nil above), so it still consults no license.
				//
				// The error is returned UNWRAPPED on purpose: the refusal names itself. Folding it into
				// ErrMultiIDPRequiresEnterprise would tell an enterprise customer to buy the build they
				// are already running, which is the failure mode this replaces.
				if err := s.multiIDP.AllowsAdditionalActiveIdP(ctx); err != nil {
					return err
				}
				// Rule 2 (U4, RELAXED in U5): a scope may now run SEVERAL active IdPs as
				// long as each is disambiguated at login — a domain-bearing IdP routes by its
				// (globally-unique) domains, so only a second active DOMAINLESS IdP in the same
				// scope is refused (two fallbacks with no way to choose). Switching the
				// domainless default is still an explicit deactivate-then-activate flip.
				if c.TargetTenantID == scope && len(c.ClaimedDomains) == 0 && len(next.ClaimedDomains) == 0 {
					return ErrScopeActiveIdPExists
				}
			}
		}
		var (
			saved model.FederationConfig
			werr  error
		)
		if found {
			saved, werr = as.FederationConfigs().Update(ctx, next)
		} else {
			// Create stamps a fresh id — capture it: on the create path next.ID is empty,
			// so auditing next.ID would record an EMPTY target for the most sensitive
			// federation op (first connecting an IdP). Audit the stored row's real id.
			saved, werr = as.FederationConfigs().Create(ctx, next)
		}
		if werr != nil {
			return werr
		}
		// U8: maintain the derived domain index in the SAME transaction, so the
		// UNIQUE(domain) constraint enforces global uniqueness at COMMIT — a racing duplicate
		// claim fails here (mapped to ErrDomainClaimed) rather than committing a duplicate the
		// app-scan above can miss under Postgres READ COMMITTED.
		if err := writeDomainClaims(ctx, as, saved.ID, scope, next.ClaimedDomains); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "federation.config.update", "core.federation_config", saved.ID)
	})
	if err != nil {
		return FederationConfigView{}, err
	}
	s.invalidateAll()
	return s.GetConfigIdP(ctx, scope, alias)
}

// DeleteConfig turns off a scope's PRIMARY ("default") IdP (the single-IdP surface).
func (s *FederationService) DeleteConfig(ctx context.Context, actor Principal, scope model.TenantID) error {
	return s.DeleteConfigIdP(ctx, actor, scope, model.DefaultFederationAlias)
}

// DeleteConfigIdP removes one IdP (scope, alias). The DEFAULT primary is TOMBSTONED,
// not physically removed: it is cleared to a DISABLED tombstone (protocol "", status
// inactive, every secret/field wiped) because Resolve treats a stored (even disabled)
// default as authoritative "SSO off" and returns NoFederation (501), whereas a MISSING
// default falls back to the env-configured provider — so turning the primary off must
// never silently reactivate an env IdP. A NON-default (added) IdP is a discrete entity
// and is physically REMOVED, freeing its alias for reuse and dropping it from the
// scope's IdP list.
func (s *FederationService) DeleteConfigIdP(ctx context.Context, actor Principal, scope model.TenantID, alias string) error {
	if err := validateFederationAlias(alias); err != nil {
		return err
	}
	alias = model.NormalizeFederationAlias(alias)
	cfg, found, err := s.loadConfigByAlias(ctx, scope, alias)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrNotFound
	}
	isDefault := alias == model.DefaultFederationAlias
	err = s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if isDefault {
			// Keep id/version (OCC) + the alias but reset every other field: a cleared,
			// disabled tombstone with no protocol and no secrets.
			tombstone := model.FederationConfig{
				BaseFields:     cfg.BaseFields,
				TargetTenantID: scope,
				Alias:          model.DefaultFederationAlias,
				Status:         model.StatusInactive,
			}
			if _, err := as.FederationConfigs().Update(ctx, tombstone); err != nil {
				return err
			}
		} else if err := as.FederationConfigs().Delete(ctx, cfg.ID); err != nil {
			return err
		}
		// U8: drop the config's derived domain-index rows in the SAME transaction — for
		// BOTH the physical delete AND the default tombstone (which clears ClaimedDomains).
		// Without this the UNIQUE(domain) row would outlive the claim and permanently block any
		// other IdP from re-claiming the domain, while the app-scan reports it free (desync).
		if err := writeDomainClaims(ctx, as, cfg.ID, scope, nil); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "federation.config.delete", "core.federation_config", cfg.ID)
	})
	if err != nil {
		return err
	}
	s.invalidateAll()
	return nil
}

// TestConfig builds a provider from the supplied input (opening the existing
// sealed secret when the input leaves it blank) WITHOUT persisting anything, so
// the console can validate connectivity (OIDC discovery / SAML metadata fetch)
// before saving. Requires the enterprise builder.
func (s *FederationService) TestConfig(ctx context.Context, scope model.TenantID, in FederationConfigInput) error {
	return s.TestConfigIdP(ctx, scope, model.DefaultFederationAlias, in)
}

// TestConfigIdP validates a candidate config for one IdP (scope, alias) without
// persisting, opening the alias's existing sealed secret when the input leaves it blank.
func (s *FederationService) TestConfigIdP(ctx context.Context, scope model.TenantID, alias string, in FederationConfigInput) error {
	if err := validateFederationAlias(alias); err != nil {
		return err
	}
	if err := validateFederationInput(in); err != nil {
		return err
	}
	if s.builder == nil {
		return ErrFederationBuilderUnavailable
	}
	existing, _, err := s.loadConfigByAlias(ctx, scope, alias)
	if err != nil {
		return err
	}
	params := FederationParams{
		Protocol: in.Protocol, OIDCIssuer: in.OIDCIssuer, OIDCClientID: in.OIDCClientID,
		OIDCGroupsClaim: in.OIDCGroupsClaim,
		SAMLMetadataURL: in.SAMLMetadataURL, SAMLEntityID: in.SAMLEntityID, SAMLACSURL: in.SAMLACSURL,
		SAMLIDPSSOURL: in.SAMLIDPSSOURL, SAMLEmailAttr: in.SAMLEmailAttr, SAMLGroupsAttr: in.SAMLGroupsAttr,
		// Same "blank = keep" rule as PutConfigIdP, for the same reason: the test builds a
		// provider from the candidate config, so taking the cert verbatim while resolving the
		// key from the stored seal made "Test connection" fail with ErrNotConfigured on a
		// perfectly good stored keypair — the operator would be debugging their IdP over a
		// defect on our side.
		SAMLSPCertPEM:     resolvePublic(in.SAMLSPCertPEM, existing.SAMLSPCertPEM),
		SAMLSPSignCertPEM: resolvePublic(in.SAMLSPSignCertPEM, existing.SAMLSPSignCertPEM),
	}
	params.OIDCClientSecret, err = s.resolveSecret(ctx, scope, in.OIDCClientSecret, existing.OIDCClientSecretSealed)
	if err != nil {
		return err
	}
	params.SAMLSPKeyPEM, err = s.resolveSecret(ctx, scope, in.SAMLSPKeyPEM, existing.SAMLSPKeySealed)
	if err != nil {
		return err
	}
	params.SAMLSPSignKeyPEM, err = s.resolveSecret(ctx, scope, in.SAMLSPSignKeyPEM, existing.SAMLSPSignKeySealed)
	if err != nil {
		return err
	}
	_, err = s.builder(ctx, params)
	return err
}

// MultiIDPAvailable reports whether THIS build routes a login among more than one IdP —
// the reserved enterprise capability (U5 home-realm-by-domain, per-tenant multi-IdP).
// The console uses it as the honest routed_by signal: the open build STORES claimed
// domains but never routes by them (single global IdP), and must say so, not fake it.
func (s *FederationService) MultiIDPAvailable() bool { return s.multiIDP != nil }

// Resolve returns the active provider for a login by request tenant only — the simple
// seam (SAML SP-metadata, the callback probe). It is ResolveLogin with a tenant-only
// SelectionInput; the ResolvedIdP is discarded.
func (s *FederationService) Resolve(ctx context.Context, tenant model.TenantID) (Federation, error) {
	fed, _ := s.ResolveLogin(ctx, SelectionInput{Tenant: tenant})
	return fed, nil
}

// ResolveLogin resolves the IdP for a login from the full SelectionInput (U5) and
// returns BOTH the provider and the ResolvedIdP (which IdP was chosen), so the caller can
// persist the SELECTED (scope, alias) and complete against the same IdP. The open build
// (no MultiIDP) resolves the single GLOBAL config regardless of the input. With the
// capability wired it enters the enterprise leg whenever ANY selector is present (tenant,
// email domain OR alias) — so a tenant-LESS email-first home-realm login reaches domain
// routing (the guard the design review caught). It fails CLOSED: a selected IdP that
// cannot build, or a store error while a specific selector was requested, yields
// NoFederation (login 501) rather than silently falling through to the wrong identity
// domain.
func (s *FederationService) ResolveLogin(ctx context.Context, in SelectionInput) (Federation, ResolvedIdP) {
	hasSelector := (in.Tenant != "" && in.Tenant != GlobalFederationScope) || in.EmailDomain != "" || in.Alias != ""
	if s.multiIDP != nil && hasSelector {
		// U8: index-narrow the candidate set with targeted lookups (per-scope for a tenant
		// hint, the domain point-index for a home-realm login) instead of draining every config.
		// The narrowed set is the small superset SelectActive can match for THIS input, so the
		// selector's decision is identical to the old full drain — reached in O(log n).
		active, err := s.selectionCandidates(ctx, in)
		if err != nil {
			// A store error while a specific IdP was requested must not fall through to the
			// global IdP (wrong realm). Fail closed.
			return NoFederation{}, ResolvedIdP{}
		}
		if cfg, ok := s.multiIDP.SelectActive(in, active); ok && cfg.Protocol != "" && s.builder != nil {
			fed, berr := s.buildCached(ctx, cfg)
			if berr != nil {
				// Selected but unbuildable → fail closed; keep the identity for auditing.
				return NoFederation{}, resolvedOf(cfg)
			}
			return fed, resolvedOf(cfg)
		}
		// No per-tenant / by-domain match → fall through to the global default.
	}
	fed, scim := s.resolveGlobal(ctx)
	return fed, ResolvedIdP{Scope: GlobalFederationScope, Alias: model.DefaultFederationAlias, SCIMAuthoritative: scim}
}

// resolvedOf is the ResolvedIdP describing a chosen config (no secrets).
func resolvedOf(cfg model.FederationConfig) ResolvedIdP {
	return ResolvedIdP{
		Scope: cfg.TargetTenantID, Alias: cfg.Alias,
		ClaimedDomains: cfg.ClaimedDomains, SCIMAuthoritative: cfg.SCIMAuthoritative,
	}
}

// ResolveByAlias re-resolves ONE specific IdP by (scope, alias) — the login CALLBACK uses
// it to complete against exactly the IdP the start leg selected, avoiding any drift from
// re-running domain/priority selection if config changed mid-flow. Returns the provider
// (NoFederation, fail-closed, if it is gone / disabled / unbuildable) and the ResolvedIdP
// (its scope + claimed domains) so the callback can scope CompleteSSO and constrain the
// asserted email domain.
func (s *FederationService) ResolveByAlias(ctx context.Context, scope model.TenantID, alias string) (Federation, ResolvedIdP) {
	// The GLOBAL "default" is resolved through resolveGlobal so the callback matches the
	// start leg EXACTLY — including the env-configured fallback when NO managed row exists
	// (an env-only SSO deployment must complete its callback, not 501) and the
	// authoritative-off tombstone. ResolveByAlias's managed-only lookup is for per-tenant /
	// non-default IdPs, which never have an env fallback.
	if scope == GlobalFederationScope && model.NormalizeFederationAlias(alias) == model.DefaultFederationAlias {
		fed, scim := s.resolveGlobal(ctx)
		return fed, ResolvedIdP{Scope: GlobalFederationScope, Alias: model.DefaultFederationAlias, SCIMAuthoritative: scim}
	}
	cfg, ok, err := s.loadConfigByAlias(ctx, scope, alias)
	if err != nil || !ok || cfg.Status != model.StatusActive || cfg.Protocol == "" || s.builder == nil {
		return NoFederation{}, ResolvedIdP{Scope: scope, Alias: model.NormalizeFederationAlias(alias)}
	}
	fed, berr := s.buildCached(ctx, cfg)
	if berr != nil {
		return NoFederation{}, resolvedOf(cfg)
	}
	return fed, resolvedOf(cfg)
}

// resolveGlobal resolves the deployment-wide GLOBAL "default" IdP (the fallback and the
// only IdP the open build ever uses), returning its D4 SCIM-authoritative flag alongside.
// No managed config → the env-configured fallback; stored-but-disabled → NoFederation
// (authoritative off); build error → fail closed.
func (s *FederationService) resolveGlobal(ctx context.Context) (Federation, bool) {
	cfg, ok, err := s.loadConfig(ctx, GlobalFederationScope)
	if err != nil {
		return NoFederation{}, false // a transient store error must not 500 the login surface
	}
	if !ok {
		return s.fallback, false // no managed config: defer to the env-configured provider
	}
	if cfg.Status != model.StatusActive || cfg.Protocol == "" || s.builder == nil {
		return NoFederation{}, cfg.SCIMAuthoritative
	}
	fed, err := s.buildCached(ctx, cfg)
	if err != nil {
		return NoFederation{}, cfg.SCIMAuthoritative
	}
	return fed, cfg.SCIMAuthoritative
}

// isTenantScope reports whether t names a real per-tenant scope (not empty, not the global
// fallback) — the same predicate the enterprise SelectActive ladder uses to CONFINE a hinted
// login to its tenant.
func isTenantScope(t model.TenantID) bool { return t != "" && t != GlobalFederationScope }

// selectionCandidates index-narrows the config set the enterprise multi-IdP selector picks
// from, replacing the full-drain (the old activeConfigs) with targeted indexed lookups
// (U8). It returns the SMALL superset SelectActive can match for THIS input, so the
// selector's decision is IDENTICAL to draining every config — reached in O(log n):
//   - a real tenant hint CONFINES selection to that tenant, so its own configs (target_tenant_id
//     indexed) cover the alias (ladder a) and tenant-confined-domain/default (ladder b) rungs;
//   - a tenant-LESS home-realm login matches only by claimed domain, so the single domain-indexed
//     claimant (ladder c) suffices — deny-closed on an ambiguous claim because
//     ReconcileDomainClaims quarantines a duplicate (no index row → loadConfigByDomain returns
//     none → fall to global), exactly as the drain's matchDomain n!=1 did;
//   - no usable selector → nil, so SelectActive abstains and the login falls to the global IdP (d).
//
// It preserves the OLD activeConfigs() contract EXACTLY: the selector receives only ACTIVE,
// non-tombstone configs. The real enterprise ladder re-checks eligible() defensively too, but
// filtering here keeps a passive selector — and every test double — byte-identical to the full
// drain. Invoked ONLY on the enterprise leg (s.multiIDP != nil); the open build never selects.
func (s *FederationService) selectionCandidates(ctx context.Context, in SelectionInput) ([]model.FederationConfig, error) {
	var (
		rows []model.FederationConfig
		err  error
	)
	switch {
	case isTenantScope(in.Tenant):
		rows, err = s.configsForScope(ctx, in.Tenant)
	case in.EmailDomain != "":
		var cfg model.FederationConfig
		var ok bool
		cfg, ok, err = s.loadConfigByDomain(ctx, in.EmailDomain)
		if ok {
			rows = []model.FederationConfig{cfg}
		}
	}
	if err != nil {
		return nil, err
	}
	active := make([]model.FederationConfig, 0, len(rows))
	for _, c := range rows {
		if c.Status == model.StatusActive && c.Protocol != "" {
			active = append(active, c)
		}
	}
	return active, nil
}

// loadConfigByDomain resolves the single live IdP that CLAIMS `domain` via the derived,
// unique-indexed federation_domain_claims table — an O(log n) point lookup that replaces
// scanning every config's ClaimedDomains (U8). `domain` is normalized to the stored
// canonical form. It is deny-closed and defends against a stale index row: it re-verifies the
// loaded config is a non-tombstone that STILL claims the domain, so an orphan/stale row (a
// rolling-upgrade window, or a crash between a config write and its index maintenance) yields
// ok=false — the caller falls back to the global IdP rather than routing to a wrong/dead IdP.
func (s *FederationService) loadConfigByDomain(ctx context.Context, domain string) (model.FederationConfig, bool, error) {
	d := model.NormalizeFederationDomain(domain)
	if d == "" {
		return model.FederationConfig{}, false, nil
	}
	var (
		cfg model.FederationConfig
		ok  bool
	)
	err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, e := drainList(ctx, as.FederationDomainClaims().List, byEq("domain", d, 0))
		if e != nil {
			return e
		}
		// UNIQUE(tenant_id, domain) ⇒ at most one committed claim; be deny-closed on any breach.
		if len(rows) != 1 {
			return nil
		}
		c, e := as.FederationConfigs().Get(ctx, rows[0].ConfigID)
		if errors.Is(e, store.ErrNotFound) {
			return nil // orphan index row (config gone) → deny-closed
		}
		if e != nil {
			return e
		}
		if c.Protocol != "" && domainClaimed(c, d) {
			cfg, ok = c, true
		}
		return nil
	})
	if err != nil {
		return model.FederationConfig{}, false, err
	}
	return cfg, ok, nil
}

// domainClaimed reports whether cfg's ClaimedDomains contains the normalized domain nd.
func domainClaimed(cfg model.FederationConfig, nd string) bool {
	for _, d := range cfg.ClaimedDomains {
		if model.NormalizeFederationDomain(d) == nd {
			return true
		}
	}
	return false
}

// writeDomainClaims replaces a config's rows in the derived federation_domain_claims index
// INSIDE the caller's AuthMutate (U8): it deletes the config's existing claim rows and
// inserts one per normalized domain, so the UNIQUE(tenant_id, domain) constraint enforces
// GLOBAL domain uniqueness at COMMIT — closing the Postgres READ-COMMITTED race the app-level
// scan cannot. A domain already claimed by ANOTHER config fails the insert (store.ErrConflict),
// which maps to ErrDomainClaimed and rolls the whole config write back. Passing no domains (a
// tombstone, or a config that dropped its domains) simply clears its rows.
func writeDomainClaims(ctx context.Context, as store.AuthScope, configID model.ID, scope model.TenantID, domains []string) error {
	repo := as.FederationDomainClaims()
	existing, err := drainList(ctx, repo.List, byEq("config_id", configID.String(), 0))
	if err != nil {
		return err
	}
	for _, e := range existing {
		if err := repo.Delete(ctx, e.ID); err != nil {
			return err
		}
	}
	for _, d := range domains {
		nd := model.NormalizeFederationDomain(d)
		if nd == "" {
			continue
		}
		if _, err := repo.Create(ctx, model.FederationDomainClaim{TargetTenantID: scope, ConfigID: configID, Domain: nd}); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("%w: %q", ErrDomainClaimed, nd)
			}
			return err
		}
	}
	return nil
}

// ReconcileDomainClaims converges the derived federation_domain_claims index onto the
// authoritative ClaimedDomains of every live FederationConfig (U8). It is idempotent and
// collision-safe, so it runs at boot on EVERY node (the composition root calls it after
// building the service) without ever bricking startup:
//   - a domain claimed by exactly ONE config is (re)indexed to that config;
//   - a domain claimed by MORE THAN ONE config (a legacy duplicate — pre-U8 uniqueness was a
//     best-effort app scan, so a Postgres race could have committed one) is QUARANTINED: no row
//     is written, so a home-realm login for it deny-closes to the global IdP until an operator
//     reconciles — MATCHING SelectActive's deny-closed-on-ambiguity, never steering a login to
//     an arbitrary lowest-id winner (the U4 alias backfill picks a winner because an alias MUST
//     exist; a domain claim must not, so quarantine, not pick);
//   - a stale/orphan/quarantined index row (domain no longer solely claimed, or its config gone)
//     is removed.
//
// On upgrade this backfills the index so existing home-realm domains route via the indexed path;
// a missing/lagging index only degrades routing to the global fallback, never to a wrong IdP. A
// concurrent multi-node boot is safe: a duplicate INSERT loses to the UNIQUE constraint and is
// tolerated as already-present, and the target set is a deterministic function of the configs so
// every node converges to the same index.
func (s *FederationService) ReconcileDomainClaims(ctx context.Context) error {
	return s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		configs, err := drainList(ctx, as.FederationConfigs().List, model.Query{})
		if err != nil {
			return err
		}
		claimants := map[string]map[model.ID]bool{} // domain → set of claiming config ids
		scopeOf := map[model.ID]model.TenantID{}
		for _, c := range configs {
			scopeOf[c.ID] = c.TargetTenantID
			for _, d := range c.ClaimedDomains {
				nd := model.NormalizeFederationDomain(d)
				if nd == "" {
					continue
				}
				if claimants[nd] == nil {
					claimants[nd] = map[model.ID]bool{}
				}
				claimants[nd][c.ID] = true
			}
		}
		desired := map[string]model.ID{} // domain → sole claimant (multi-claimed ⇒ quarantined)
		for d, ids := range claimants {
			if len(ids) == 1 {
				for cid := range ids {
					desired[d] = cid
				}
			}
		}
		existing, err := drainList(ctx, as.FederationDomainClaims().List, model.Query{})
		if err != nil {
			return err
		}
		have := map[string]model.FederationDomainClaim{}
		for _, r := range existing {
			// Defensively collapse any duplicate index row (shouldn't exist under UNIQUE).
			if _, dup := have[r.Domain]; dup {
				if err := as.FederationDomainClaims().Delete(ctx, r.ID); err != nil {
					return err
				}
				continue
			}
			have[r.Domain] = r
		}
		// Remove stale/orphan/quarantined rows.
		for d, r := range have {
			if want, ok := desired[d]; !ok || r.ConfigID != want {
				if err := as.FederationDomainClaims().Delete(ctx, r.ID); err != nil {
					return err
				}
				delete(have, d)
			}
		}
		// Insert missing desired rows (tolerating a concurrent node that inserted first).
		for d, cid := range desired {
			if _, ok := have[d]; ok {
				continue
			}
			if _, err := as.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{
				TargetTenantID: scopeOf[cid], ConfigID: cid, Domain: d,
			}); err != nil {
				if errors.Is(err, store.ErrConflict) {
					continue
				}
				return err
			}
		}
		return nil
	})
}

// buildCached returns the provider for a config, building it (opening its sealed
// secrets) on a cache miss and caching it per scope by row version.
func (s *FederationService) buildCached(ctx context.Context, cfg model.FederationConfig) (Federation, error) {
	s.mu.Lock()
	if c, hit := s.cache[cfg.ID]; hit && c.version == cfg.Version {
		s.mu.Unlock()
		return c.fed, nil
	}
	s.mu.Unlock()
	// Build WITHOUT holding s.mu: provider construction does network I/O (OIDC discovery /
	// SAML metadata fetch), so a slow or hanging IdP must not block Resolve for OTHER IdPs
	// or block a config write's invalidateAll. A rare duplicate concurrent build on a cache
	// miss is harmless (construction is idempotent; the last store wins).
	fed, err := s.build(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[cfg.ID] = cachedFederation{version: cfg.Version, fed: fed}
	s.mu.Unlock()
	return fed, nil
}

// build constructs a provider from a stored config, opening its sealed secrets.
func (s *FederationService) build(ctx context.Context, cfg model.FederationConfig) (Federation, error) {
	params := FederationParams{
		Protocol: cfg.Protocol, OIDCIssuer: cfg.OIDCIssuer, OIDCClientID: cfg.OIDCClientID,
		OIDCGroupsClaim: cfg.OIDCGroupsClaim,
		SAMLMetadataURL: cfg.SAMLMetadataURL, SAMLEntityID: cfg.SAMLEntityID, SAMLACSURL: cfg.SAMLACSURL,
		SAMLIDPSSOURL: cfg.SAMLIDPSSOURL, SAMLEmailAttr: cfg.SAMLEmailAttr, SAMLGroupsAttr: cfg.SAMLGroupsAttr,
		SAMLSPCertPEM:     cfg.SAMLSPCertPEM,
		SAMLSPSignCertPEM: cfg.SAMLSPSignCertPEM,
	}
	var err error
	if params.OIDCClientSecret, err = s.openSealed(ctx, cfg.TargetTenantID, cfg.OIDCClientSecretSealed); err != nil {
		return nil, err
	}
	if params.SAMLSPKeyPEM, err = s.openSealed(ctx, cfg.TargetTenantID, cfg.SAMLSPKeySealed); err != nil {
		return nil, err
	}
	if params.SAMLSPSignKeyPEM, err = s.openSealed(ctx, cfg.TargetTenantID, cfg.SAMLSPSignKeySealed); err != nil {
		return nil, err
	}
	return s.builder(ctx, params)
}

// validateResolvedSPKeypairs refuses a config whose SP key material is half present, for
// EITHER keypair. It runs on the RESOLVED row (after "blank = keep" and after sealing), so
// it judges what will actually be stored rather than what the request happened to carry —
// an ordinary edit that omits both halves of a stored pair is untouched.
//
// Absent-and-absent is valid: both SP keypairs are optional (SAML works unsigned and
// unencrypted). Only the mixed state is refused, because samlFromParts enters each branch
// on `cert != "" || key != ""` and then rejects the missing half with ErrNotConfigured — so
// the row would persist cleanly and fail at the next login instead of at the write.
//
// It cannot check that a cert and a key MATCH each other: the private half is sealed and
// this runs before any provider is built. tls.X509KeyPair catches a mismatched pair at
// build time (core/auth/federation/saml.go), later than here but still deny-closed.
func validateResolvedSPKeypairs(cfg model.FederationConfig) error {
	for _, p := range [...]struct{ what, cert, key string }{
		{"encryption", cfg.SAMLSPCertPEM, cfg.SAMLSPKeySealed},
		{"signing", cfg.SAMLSPSignCertPEM, cfg.SAMLSPSignKeySealed},
	} {
		if (p.cert == "") == (p.key == "") {
			continue // both present or both absent — the two valid states
		}
		missing, supplied := "certificate", "private key"
		if p.cert == "" {
			missing, supplied = "private key", "certificate"
		}
		return fmt.Errorf("%w: the SAML SP %s keypair has a %s but no %s; supply both halves "+
			"(a keypair with only one half cannot be loaded and would stop SSO logins for this provider)",
			ErrBadFederationConfig, p.what, supplied, missing)
	}
	return nil
}

// resolvePublic returns the public keypair half to use: the supplied value if non-empty,
// else the stored one. It is the cleartext twin of resolveSecret — the two halves of an SP
// keypair MUST obey the same rule, because a config carrying only one of them does not
// build at all (see the note in PutConfigIdP).
func resolvePublic(supplied, stored string) string {
	if supplied != "" {
		return supplied
	}
	return stored
}

// resolveSecret returns the plaintext to use for a test: the supplied value if
// non-empty, else the opened existing sealed value (or "" when neither exists).
func (s *FederationService) resolveSecret(ctx context.Context, scope model.TenantID, supplied, sealed string) (string, error) {
	if supplied != "" {
		return supplied, nil
	}
	return s.openSealed(ctx, scope, sealed)
}

func (s *FederationService) openSealed(ctx context.Context, scope model.TenantID, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if s.sealer == nil {
		return "", ErrNoFederationSealer
	}
	b, err := s.sealer.Open(ctx, scope, sealed)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// seal seals a plaintext secret and returns the sealed value plus a non-secret
// fingerprint (for display).
func (s *FederationService) seal(ctx context.Context, scope model.TenantID, plaintext string) (sealed, hint string, err error) {
	if s.sealer == nil {
		return "", "", ErrNoFederationSealer
	}
	sealed, err = s.sealer.Seal(ctx, scope, []byte(plaintext))
	if err != nil {
		return "", "", err
	}
	return sealed, fingerprint(plaintext), nil
}

// invalidateAll clears the whole provider cache. A config write may change which IdP
// is active in a scope (a flip between siblings) and the cache is keyed by config ID,
// so clearing wholesale is both correct and cheap (the IdP set is tiny, writes rare).
func (s *FederationService) invalidateAll() {
	s.mu.Lock()
	clear(s.cache)
	s.mu.Unlock()
}

// ErrNoFederationSealer is returned when a secret must be sealed/opened but no
// sealer is wired (fail-closed; never store or accept cleartext).
var ErrNoFederationSealer = errors.New("auth: no SSO secret sealer wired; cannot store an SSO secret")

// ErrBadFederationConfig is the validation error for an incomplete/invalid config.
var ErrBadFederationConfig = errors.New("auth: invalid SSO configuration")

// validateFederationInput checks the minimum fields per protocol (deny-closed), plus
// the posture: every supplied network allow-list entry must be a parseable CIDR.
// Rejecting malformed CIDRs at WRITE time (open-core input validation, not enforcement)
// means the stored allow-list is always well-formed, so the enterprise engine never has
// to choose between a bad rule and allow-all at login (it still fails closed if it
// somehow sees one — defense in depth).
func validateFederationInput(in FederationConfigInput) error {
	for _, c := range in.NetworkAllowCIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("%w: network allow-list entry %q is not a valid CIDR (e.g. 10.0.0.0/8 or 2001:db8::/32)", ErrBadFederationConfig, c)
		}
	}
	// U5: every claimed home-realm domain must be a valid host after normalization,
	// since the value is equality-matched at login and audited. Reject a malformed entry
	// (400) rather than silently drop it — the operator must see what they typed is wrong.
	for _, d := range in.ClaimedDomains {
		if raw := strings.TrimSpace(d); raw == "" {
			continue
		}
		nd := model.NormalizeFederationDomain(d)
		if !federationDomainPattern.MatchString(nd) {
			return fmt.Errorf("%w: claimed domain %q is not a valid domain (e.g. corp.example.com)", ErrBadFederationConfig, d)
		}
	}
	switch in.Protocol {
	case ProtocolOIDC:
		if in.OIDCIssuer == "" || in.OIDCClientID == "" {
			return fmt.Errorf("%w: oidc requires issuer and client_id", ErrBadFederationConfig)
		}
	case ProtocolSAML:
		// Match what samlFromParts actually needs (core/auth/federation/saml.go),
		// so a config can never be stored active yet fail to build (silent 501).
		// ⚠ This function sees only the REQUEST, so it cannot judge key material: whether a
		// keypair ends up whole depends on what is already stored and sealed. That half of
		// the same promise lives in validateResolvedSPKeypairs, on the resolved row.
		if in.SAMLEntityID == "" || in.SAMLMetadataURL == "" || in.SAMLACSURL == "" || in.SAMLIDPSSOURL == "" {
			return fmt.Errorf("%w: saml requires entity_id, metadata_url, acs_url and idp_sso_url", ErrBadFederationConfig)
		}
	default:
		return fmt.Errorf("%w: protocol must be oidc or saml", ErrBadFederationConfig)
	}
	return nil
}

// fingerprint is a short, non-secret SHA-256 prefix of a secret, for display
// (so the console can show "a secret is set" and detect a change without
// revealing it).
func fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:12]
}

// normalizeCIDRs trims each entry and drops blanks, preserving order, so the stored
// network allow-list is clean (validateFederationInput already rejected malformed
// entries). A list that is all-blank normalizes to nil (no restriction).
func normalizeCIDRs(in []string) []string {
	var out []string
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// normalizeDomains canonicalizes each claimed email domain (U5), dropping blanks and
// de-duplicating within the list, preserving order. Case/whitespace are folded
// (model.NormalizeFederationDomain) so the stored set and the login-time match are stable;
// validateFederationInput rejects a malformed domain, and the write path enforces global
// uniqueness across configs.
func normalizeDomains(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range in {
		d = model.NormalizeFederationDomain(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Posture resolves the deployment-wide (global, V1) login-enforcement posture from the
// stored SSO config: whether SSO is required, whether an ACTIVE IdP backs it, and the
// network allow-list. It is the OPEN-CORE reader two consumers share — the console
// posture display and the enterprise LoginPolicy (enterprise/ssoenforce, wired with
// this method as its posture provider). The open build computes it but never enforces
// it. A missing config row (no SSO ever configured) is the zero posture (no
// enforcement). Per-tenant scoping is the additive future (the SSO config's own road).
func (s *FederationService) Posture(ctx context.Context) (LoginEnforcementPosture, error) {
	cfg, ok, err := s.loadConfig(ctx, GlobalFederationScope)
	if err != nil {
		return LoginEnforcementPosture{}, err
	}
	if !ok {
		return LoginEnforcementPosture{}, nil
	}
	return LoginEnforcementPosture{
		RequireSSO:        cfg.RequireSSO,
		HasActiveIdP:      cfg.Status == model.StatusActive && cfg.Protocol != "",
		NetworkAllowCIDRs: cfg.NetworkAllowCIDRs,
	}, nil
}
