// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ProtocolExternal is a slot for an installed identity verifier. It implements
// no browser redirect or assertion protocol in the native federation handlers.
const ProtocolExternal = "external"

// ErrExternalProviderUnavailable means the installed provider cannot establish
// readiness for the exact immutable connector revision. There is no fallback.
var ErrExternalProviderUnavailable = errors.New("auth: external provider unavailable")

// ExternalProviderSlot is non-secret metadata, not authentication evidence.
// Build receives the proposed slot during activation (ConfigID can be empty,
// ConfigVersion is the prior version), and the stored version during login.
type ExternalProviderSlot struct {
	ConfigID            model.ID
	ConfigVersion       int64
	TenantID            model.TenantID
	Alias               string
	ConnectorRef        string
	ConnectorGeneration int64
	Issuer              string
	ClaimedDomains      []string
	AllowJIT            bool
}

// ExternalLoginCredentials belongs to one verification attempt. The provider
// must not log or retain Password. It receives a bounded copy, cleared on return.
type ExternalLoginCredentials struct {
	Username string
	Password []byte
}

// ExternalProviderBuilder resolves and checks readiness of the exact immutable
// connector revision named by slot. Build runs outside the auth transaction.
// A connector revision must not change under the same generation; publishing or
// disabling it must first update/deactivate its native slot. Retired revisions
// remain immutable. No mutable in-process cache may substitute for that contract.
type ExternalProviderBuilder interface {
	Build(context.Context, ExternalProviderSlot) (ExternalProvider, error)
}

// ExternalProvider verifies identity using the exact revision Build resolved.
// Verify must establish the issuer, immutable subject and trusted email itself.
// Credential failures wrap ErrInvalidCredentials or ErrUnauthenticated; other
// typed provider/admission failures propagate without being blamed on the account.
// ValidateAdmission runs in the native auth transaction and must only validate
// retained immutable producer authority and its actual validity horizon. It must
// not call a store or network, nor refresh, extend or invent its authority.
type ExternalProvider interface {
	Verify(context.Context, ExternalLoginCredentials) (FederatedIdentity, error)
	ValidateAdmission(context.Context) error
	// ProofHorizon returns the original successful verification's finite expiry.
	// It may be zero for readiness, but a login requires a nonzero horizon.
	// Preparation captures it once; revalidation never refreshes it.
	ProofHorizon() time.Time
}

// WithExternalProviderBuilder installs the optional verifier before serving.
// A nil builder keeps external activation and authentication unavailable.
func (s *FederationService) WithExternalProviderBuilder(builder ExternalProviderBuilder) *FederationService {
	s.externalBuilder = builder
	return s
}

func (s *FederationService) providerAvailable(protocol string) bool {
	if protocol == ProtocolExternal {
		return s.externalBuilder != nil
	}
	return s.builder != nil
}

func externalSlot(c model.FederationConfig) ExternalProviderSlot {
	return ExternalProviderSlot{ConfigID: c.ID, ConfigVersion: c.Version,
		TenantID: c.TargetTenantID, Alias: model.NormalizeFederationAlias(c.Alias),
		ConnectorRef: c.ExternalConnectorRef, ConnectorGeneration: c.ExternalConnectorGeneration,
		Issuer: c.ExternalIssuer, ClaimedDomains: slices.Clone(c.ClaimedDomains), AllowJIT: !c.SCIMAuthoritative}
}

func (s *FederationService) buildExternal(ctx context.Context, slot ExternalProviderSlot) (ExternalProvider, error) {
	if s == nil || s.externalBuilder == nil {
		return nil, ErrExternalProviderUnavailable
	}
	if !isTenantScope(slot.TenantID) || validateExternalInput(FederationConfigInput{ExternalConnectorRef: slot.ConnectorRef, ExternalConnectorGeneration: slot.ConnectorGeneration, ExternalIssuer: slot.Issuer, ClaimedDomains: slot.ClaimedDomains}) != nil {
		return nil, ErrExternalProviderUnavailable
	}
	slot.ClaimedDomains = slices.Clone(slot.ClaimedDomains)
	provider, err := s.externalBuilder.Build(ctx, slot)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, ErrExternalProviderUnavailable
	}
	return provider, nil
}

var externalConnectorRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

func validateExternalInput(in FederationConfigInput) error {
	if !externalConnectorRefPattern.MatchString(in.ExternalConnectorRef) || in.ExternalConnectorGeneration < 1 || len(in.ExternalIssuer) > 1024 {
		return fmt.Errorf("%w: an external provider requires a canonical connector reference, positive generation and issuer", ErrBadFederationConfig)
	}
	if _, err := QualifiedSubjectKey(in.ExternalIssuer, "slot"); err != nil {
		return fmt.Errorf("%w: invalid external issuer", ErrBadFederationConfig)
	}
	if in.OIDCIssuer != "" || in.OIDCClientID != "" || in.OIDCClientSecret != "" || in.OIDCGroupsClaim != "" ||
		in.SAMLMetadataURL != "" || in.SAMLEntityID != "" || in.SAMLACSURL != "" || in.SAMLIDPSSOURL != "" ||
		in.SAMLEmailAttr != "" || in.SAMLSPCertPEM != "" || in.SAMLSPKeyPEM != "" || in.SAMLSPSignCertPEM != "" || in.SAMLSPSignKeyPEM != "" || in.SAMLGroupsAttr != "" {
		return fmt.Errorf("%w: external slots cannot carry browser protocol settings", ErrBadFederationConfig)
	}
	if len(normalizeDomains(in.ClaimedDomains)) == 0 {
		return fmt.Errorf("%w: an external provider must claim an email domain", ErrBadFederationConfig)
	}
	return nil
}

func clearExternalProtocolSettings(c *model.FederationConfig) {
	c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecretSealed, c.OIDCClientSecretHint, c.OIDCGroupsClaim = "", "", "", "", ""
	c.SAMLMetadataURL, c.SAMLEntityID, c.SAMLACSURL, c.SAMLIDPSSOURL, c.SAMLEmailAttr, c.SAMLGroupsAttr = "", "", "", "", "", ""
	c.SAMLSPCertPEM, c.SAMLSPKeySealed, c.SAMLSPKeyHint = "", "", ""
	c.SAMLSPSignCertPEM, c.SAMLSPSignKeySealed, c.SAMLSPSignKeyHint = "", "", ""
}

// ExternalLoginAdmission retains the actual verified provider and exact original
// slot. A pending second-factor continuation must retain this object, the selected
// tenant and federated method; it cannot reconstruct them from account custody.
// The original provider's authority horizon is rechecked, never renewed here.
type ExternalLoginAdmission struct {
	accountID model.ID
	horizon   time.Time
	slot      ExternalProviderSlot
	provider  ExternalProvider
	identity  FederatedIdentity
	key       QualifiedKey
}

// PrepareExternalLogin verifies one installed provider and commits the strictly
// bound account and initial membership, without issuing a session or credential.
// The caller must pass the returned account and retained admission through the
// native factor gate before any login session can be created. Network
// policy and native attempt limits run before Build or credential verification.
// Existing OIDC/SAML completion and its historical email fallback are unchanged.
// Existing accounts require the exact qualified subject; email never adopts one.
// No groups are reconciled here; the provider's provisioning path owns its roster.
func (a *Authenticator) PrepareExternalLogin(ctx context.Context, service *FederationService, tenant model.TenantID, alias string, credentials ExternalLoginCredentials, peerIP string) (model.User, *ExternalLoginAdmission, error) {
	return a.PrepareExternalLoginFrom(ctx, service, tenant, alias, credentials, peerIP, nil)
}

// PrepareExternalLoginFrom uses configured trusted forwarding only for throttle
// attribution. Policy, audit and session provenance retain the transport peer.
func (a *Authenticator) PrepareExternalLoginFrom(ctx context.Context, service *FederationService, tenant model.TenantID, alias string, credentials ExternalLoginCredentials, peerIP string, forwarded []string) (model.User, *ExternalLoginAdmission, error) {
	if service == nil || !isTenantScope(tenant) || validateFederationAlias(alias) != nil ||
		credentials.Username == "" || len(credentials.Username) > 512 || len(credentials.Password) == 0 || len(credentials.Password) > 4096 {
		return model.User{}, nil, ErrInvalidCredentials
	}
	alias = model.NormalizeFederationAlias(alias)
	accountKey := "external:" + tenant.String() + ":" + alias + ":" + strings.ToLower(strings.TrimSpace(credentials.Username))
	addressKey := "ip:" + a.trustedLoginProxies.clientAddress(peerIP, forwarded)
	verdict, wait := a.throttle.decide(accountKey, addressKey)
	if verdict == loginRefuse {
		return model.User{}, nil, ErrLockedOut
	}
	outcome := loginAbandoned
	defer func() { a.throttle.record(accountKey, addressKey, verdict, outcome) }()
	_, err := a.beginLogin(ctx, peerIP)
	if err != nil {
		return model.User{}, nil, err
	}
	if verdict == loginDelay {
		if err := a.throttle.waitOut(ctx, wait); err != nil {
			return model.User{}, nil, err
		}
	}
	cfg, found, err := service.loadConfigByAlias(ctx, tenant, alias)
	if err != nil {
		return model.User{}, nil, err
	}
	if !found || cfg.Protocol != ProtocolExternal || cfg.Status != model.StatusActive || cfg.DeletedAt != nil {
		return model.User{}, nil, ErrExternalProviderUnavailable
	}
	slot := externalSlot(cfg)
	provider, err := service.buildExternal(ctx, slot)
	if err != nil {
		return model.User{}, nil, err
	}
	credentials.Password = slices.Clone(credentials.Password)
	defer clear(credentials.Password)
	identity, err := provider.Verify(ctx, credentials)
	if err != nil {
		if !errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, ErrUnauthenticated) {
			return model.User{}, nil, err
		}
		outcome = loginFailed
		a.recordExternalLoginFailure(ctx, peerIP)
		return model.User{}, nil, ErrInvalidCredentials
	}
	key, err := QualifiedSubjectKey(identity.Issuer, identity.Subject)
	email := normalizeEmail(identity.Email)
	address, emailErr := mail.ParseAddress(email)
	if err != nil || len(identity.Subject) > 4096 || emailErr != nil || len(email) > 320 || address.Address != email || identity.Issuer != slot.Issuer || !externalAllowsEmail(slot, identity.Email) {
		outcome = loginFailed
		a.auditSSOOutsideScope(ctx, "", peerIP, tenant)
		return model.User{}, nil, ErrUnauthenticated
	}
	organization, err := a.isOrganization(ctx, tenant)
	if err != nil {
		return model.User{}, nil, err
	}
	if !organization {
		return model.User{}, nil, ErrUnauthenticated
	}
	admission := &ExternalLoginAdmission{slot: slot, provider: provider, identity: identity, key: key,
		horizon: provider.ProofHorizon()}
	user, err := a.prepareExternalLoginAdmission(ctx, admission)
	if err == nil {
		outcome = loginSucceeded
	}
	if err != nil {
		return model.User{}, nil, err
	}
	return user, admission, nil
}

func externalAllowsEmail(slot ExternalProviderSlot, email string) bool {
	domain := emailDomain(email)
	return domain != "" && slices.Contains(slot.ClaimedDomains, domain)
}

func (a *Authenticator) recordExternalLoginFailure(ctx context.Context, peerIP string) {
	if err := a.st.AuthMutate(ctx, func(as store.AuthScope) error { return appendLoginFail(ctx, as, "anonymous", peerIP) }); err != nil {
		a.log.Error("auth: recording failed login", "err", err)
	}
}

// validateSlot reads the exact row through the transaction issuing the session.
// The login capability lock serializes every compliant slot writer with it.
func (ad ExternalLoginAdmission) validateSlot(ctx context.Context, as store.AuthScope) error {
	c, err := as.FederationConfigs().Get(ctx, ad.slot.ConfigID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUnauthenticated
		}
		return err
	}
	if c.DeletedAt != nil || c.Protocol != ProtocolExternal || c.Status != model.StatusActive || c.Version != ad.slot.ConfigVersion ||
		c.TargetTenantID != ad.slot.TenantID || model.NormalizeFederationAlias(c.Alias) != ad.slot.Alias ||
		c.ExternalConnectorRef != ad.slot.ConnectorRef || c.ExternalConnectorGeneration != ad.slot.ConnectorGeneration || c.ExternalIssuer != ad.slot.Issuer ||
		!slices.Equal(c.ClaimedDomains, ad.slot.ClaimedDomains) || c.SCIMAuthoritative == ad.slot.AllowJIT {
		return ErrUnauthenticated
	}
	return ad.provider.ValidateAdmission(ctx)
}

// SessionScope is the original selected tenant, never account custody.
func (ad *ExternalLoginAdmission) SessionScope() model.TenantID {
	if ad == nil {
		return model.TenantID("")
	}
	return ad.slot.TenantID
}

// ProofHorizon is the producer-issued bound captured after the original Verify.
func (ad *ExternalLoginAdmission) ProofHorizon() time.Time {
	if ad == nil {
		return time.Time{}
	}
	return ad.horizon
}

// RevalidateCurrent performs a preflight using current native rows and the
// original producer. It does not authorize issuance: the continuation must also
// delegate RevalidateSessionTx in the transaction that creates the session.
func (ad *ExternalLoginAdmission) RevalidateCurrent(ctx context.Context, st store.Store) error {
	if ad == nil || st == nil {
		return ErrUnauthenticated
	}
	return st.AuthMutate(ctx, func(as store.AuthScope) error {
		return ad.RevalidateSessionTx(ctx, as, ad.accountID, ad.slot.TenantID)
	})
}

// RevalidateSessionTx checks the exact prepared account and selected scope under
// the issuing transaction's L/D/H locks. Call it before factor activation and
// again after session/audit creation. It creates no account, membership, factor
// or credential, and never repeats Build or Verify. The retained horizon is
// checked at database time on each call; unavailable owner clocks refuse.
func (ad *ExternalLoginAdmission) RevalidateSessionTx(ctx context.Context, as store.AuthScope, accountID model.ID, scope model.TenantID) error {
	if ad == nil || ad.provider == nil || ad.accountID.IsZero() || ad.horizon.IsZero() ||
		accountID != ad.accountID || scope != ad.slot.TenantID || !isTenantScope(scope) {
		return ErrUnauthenticated
	}
	if _, err := lockLoginCapability(ctx, as); err != nil {
		return err
	}
	if err := ad.validateHorizon(ctx, as); err != nil {
		return err
	}
	if err := ad.validateSlot(ctx, as); err != nil {
		return err
	}
	if err := prepareUserAuthorityWrite(ctx, as, ad.accountID); err != nil {
		return err
	}
	user, err := as.Users().Get(ctx, ad.accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUnauthenticated
		}
		return err
	}
	if user.Status != model.StatusActive || user.IsSuperadmin || user.DeletedAt != nil || user.SsoSubject != string(ad.key) ||
		!externalAllowsEmail(ad.slot, user.Email) {
		return ErrUnauthenticated
	}
	owners, _, err := as.Users().List(ctx, byEq("email", normalizeEmail(ad.identity.Email), 1))
	if err != nil {
		return err
	}
	if len(owners) > 0 && owners[0].ID != ad.accountID {
		return ErrUnauthenticated
	}
	// A withdrawn membership remains withdrawn. Completion only reads current
	// authority; it must not re-run the initial JIT admission/provisioning path.
	if _, member, err := membershipOf(ctx, as, ad.accountID, scope); err != nil {
		return err
	} else if !member {
		return ErrUnauthenticated
	}
	return ad.validateHorizon(ctx, as)
}

func (ad *ExternalLoginAdmission) validateHorizon(ctx context.Context, as store.AuthScope) error {
	now, err := configurationTransactionNow(ctx, as)
	if err != nil {
		return err
	}
	if ad.horizon.IsZero() || !now.Time().Before(ad.horizon) {
		return ErrUnauthenticated
	}
	return nil
}

func (a *Authenticator) prepareExternalLoginAdmission(ctx context.Context, admission *ExternalLoginAdmission) (model.User, error) {
	var user model.User
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := lockLoginCapability(ctx, as); err != nil {
			return err
		}
		if err := a.guardNewLoginSession(ctx, as); err != nil {
			return err
		}
		if err := admission.validateHorizon(ctx, as); err != nil {
			return err
		}
		if err := admission.validateSlot(ctx, as); err != nil {
			return err
		}
		var err error
		user, err = externalUser(ctx, as, *admission)
		if err != nil {
			return err
		}
		admission.accountID = user.ID
		return admission.RevalidateSessionTx(ctx, as, user.ID, admission.slot.TenantID)
	})
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}

func externalUser(ctx context.Context, as store.AuthScope, ad ExternalLoginAdmission) (model.User, error) {
	bySubject, _, err := as.Users().List(ctx, byEq("sso_subject", string(ad.key), 1))
	if err != nil {
		return model.User{}, err
	}
	var user model.User
	if len(bySubject) > 0 {
		user = bySubject[0]
	} else {
		byEmail, _, err := as.Users().List(ctx, byEq("email", normalizeEmail(ad.identity.Email), 1))
		if err != nil {
			return model.User{}, err
		}
		if len(byEmail) > 0 {
			// A verified email does not authorize adopting an existing local
			// account. Only the exact qualified subject can select one.
			return model.User{}, ErrUnauthenticated
		}
	}
	if !user.ID.IsZero() {
		if err := prepareUserAuthorityWrite(ctx, as, user.ID); err != nil {
			return model.User{}, err
		}
		// Re-read after the actual User/directory pins. A concurrent disable,
		// binding or membership withdrawal cannot be hidden by the earlier probe.
		user, err = as.Users().Get(ctx, user.ID)
		if err != nil {
			return model.User{}, err
		}
		if user.Status != model.StatusActive || user.IsSuperadmin || user.DeletedAt != nil || user.SsoSubject != string(ad.key) {
			return model.User{}, ErrUnauthenticated
		}
		// An established subject may survive an unclaimed email rename, but it
		// cannot assert an address owned by another account. Read this after the
		// actual directory/User pin so a concurrent email owner is not hidden.
		emailOwners, _, err := as.Users().List(ctx, byEq("email", normalizeEmail(ad.identity.Email), 1))
		if err != nil {
			return model.User{}, err
		}
		if len(emailOwners) > 0 && emailOwners[0].ID != user.ID {
			return model.User{}, ErrUnauthenticated
		}
		if err := admitToScope(ctx, as, user, ad.slot.TenantID, ad.slot.ClaimedDomains); err != nil {
			return model.User{}, ErrUnauthenticated
		}
		return user, nil
	}
	if !ad.slot.AllowJIT {
		return model.User{}, ErrUnauthenticated
	}
	name := ad.identity.DisplayName
	if name == "" {
		name = normalizeEmail(ad.identity.Email)
	}
	user, err = as.Users().Create(ctx, model.User{Email: normalizeEmail(ad.identity.Email), DisplayName: name,
		Status: model.StatusActive, ExternalID: ad.identity.Subject, SsoSubject: string(ad.key),
		CredentialCustody: model.CustodyTenant, CustodyTenantID: ad.slot.TenantID})
	if err != nil {
		return model.User{}, err
	}
	// Provision and first membership are atomic, with the actual new User pin
	// acquired by Users.Create before any membership or audit operation.
	member, err := as.Memberships().Create(ctx, model.Membership{UserID: user.ID, TargetTenantID: ad.slot.TenantID, Role: ssoJITRole})
	if err != nil {
		return model.User{}, err
	}
	if err := externalProvisionAudit(ctx, as, "sso.user.provision", "core.user", user.ID); err != nil {
		return model.User{}, err
	}
	if err := externalProvisionAudit(ctx, as, "sso.user.join", "core.membership", member.ID); err != nil {
		return model.User{}, err
	}
	return user, nil
}

func externalProvisionAudit(ctx context.Context, as store.AuthScope, action string, kind model.Kind, id model.ID) error {
	_, err := as.Audit().Append(ctx, model.AuditDraft{Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: action, TargetKind: kind, TargetID: id})
	return err
}
