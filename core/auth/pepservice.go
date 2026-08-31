// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const pepCredentialPurpose = "pep"

// PEP service-management errors. Authentication deliberately returns only
// ErrUnauthenticated; these more specific errors are for authenticated
// administrative provisioning calls.
var (
	// ErrInvalidPEPService means a service specification or stored registration
	// is missing a tenant, name, audience, or valid capability version.
	ErrInvalidPEPService = errors.New("auth: invalid PEP service")
	// ErrPEPServiceExists means the tenant already has a service with this name.
	ErrPEPServiceExists = errors.New("auth: PEP service already exists")
	// ErrInvalidPEPCredential means a token is not eligible to become a PEP
	// credential, including superadmin and already purpose-restricted tokens.
	ErrInvalidPEPCredential = errors.New("auth: invalid PEP credential")
	// ErrPEPCredentialBound means the token has already been bound to a service.
	// A disabled historical binding still owns the token permanently.
	ErrPEPCredentialBound = errors.New("auth: PEP credential already bound")
	// ErrPEPCredentialTenant means the token and service belong to different
	// business tenants.
	ErrPEPCredentialTenant = errors.New("auth: PEP credential tenant mismatch")
)

// PEPServiceSpec is the operator-supplied registration for one stable Policy
// Enforcement Point identity.
type PEPServiceSpec struct {
	Tenant       model.TenantID
	Name         string
	PDPAudience  string
	Capabilities map[string]bool
}

// PEPIdentity is an authenticated PEP transport identity. All fields are
// unexported so only this package's credential-validation path can construct
// one; callers receive immutable snapshots through getters.
type PEPIdentity struct {
	serviceID              model.ID
	tenant                 model.TenantID
	credentialID           model.ID
	name                   string
	registeredCapabilities map[string]bool
	capabilityVersion      int
}

// ServiceID returns the stable service identity. It does not change across
// overlapping transport-credential rotation.
func (p PEPIdentity) ServiceID() model.ID { return p.serviceID }

// Tenant returns the business tenant governed by the service.
func (p PEPIdentity) Tenant() model.TenantID { return p.tenant }

// CredentialID returns the concrete API-token id used for this authentication.
func (p PEPIdentity) CredentialID() model.ID { return p.credentialID }

// Name returns the operator-facing registered service name.
func (p PEPIdentity) Name() string { return p.name }

// RegisteredCapabilities returns a defensive copy of the service's registered
// capability snapshot.
func (p PEPIdentity) RegisteredCapabilities() map[string]bool {
	return clonePEPCapabilities(p.registeredCapabilities)
}

// CapabilityVersion returns the registered capability snapshot version.
func (p PEPIdentity) CapabilityVersion() int { return p.capabilityVersion }

// RegisterPEPService creates a stable PEP service identity. The caller must
// hold at least the admin role in the target tenant (or be superadmin), and a
// workspace-confined caller cannot author this tenant-wide registration.
func (a *Authenticator) RegisterPEPService(
	ctx context.Context,
	caller Principal,
	spec PEPServiceSpec,
) (model.PEPService, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.PDPAudience = strings.TrimSpace(spec.PDPAudience)
	if spec.Tenant.IsZero() || spec.Tenant.IsSystem() ||
		spec.Name == "" || spec.PDPAudience == "" {
		return model.PEPService{}, ErrInvalidPEPService
	}
	if err := authorizePEPAdmin(caller, spec.Tenant); err != nil {
		return model.PEPService{}, err
	}

	var service model.PEPService
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		created, err := as.PEPServices().Create(ctx, model.PEPService{
			TargetTenantID:    spec.Tenant,
			Name:              spec.Name,
			PDPAudience:       spec.PDPAudience,
			Capabilities:      clonePEPCapabilities(spec.Capabilities),
			CapabilityVersion: 1,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("%w: %w", ErrPEPServiceExists, err)
			}
			return err
		}
		service = created
		return auditAct(
			ctx,
			as,
			caller,
			"pep_service.register",
			"core.pep_service",
			created.ID,
		)
	})
	if err != nil {
		return model.PEPService{}, err
	}
	return service, nil
}

// BindPEPCredential reserves an existing API token for one PEP service. The
// sanctioned audience-bearing provisioning flow is IssueToken for the tenant
// subject, ExchangeToken for the service's PDP audience, then binding that
// exchanged child here. The token purpose update, mapping row, and audit event
// commit atomically.
func (a *Authenticator) BindPEPCredential(
	ctx context.Context,
	caller Principal,
	serviceID model.ID,
	tokenID model.ID,
) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		service, err := as.PEPServices().Get(ctx, serviceID)
		if err != nil {
			return err
		}
		if err := authorizePEPAdmin(caller, service.TargetTenantID); err != nil {
			return err
		}
		if service.DisabledAt != nil || service.CapabilityVersion < 1 {
			return ErrInvalidPEPService
		}

		bindings, _, err := as.PEPServiceCredentials().List(
			ctx,
			byEq("token_id", tokenID.String(), 1),
		)
		if err != nil {
			return err
		}
		if len(bindings) != 0 {
			return ErrPEPCredentialBound
		}

		token, err := as.Tokens().Get(ctx, tokenID)
		if err != nil {
			return err
		}
		if token.IsSuperadmin || token.Purpose != "" {
			return ErrInvalidPEPCredential
		}
		if token.BoundTenantID != service.TargetTenantID {
			return ErrPEPCredentialTenant
		}

		token.Purpose = pepCredentialPurpose
		if _, err := as.Tokens().Update(ctx, token); err != nil {
			// A version conflict on the token update is only a COMPETING BIND when a
			// binding for this token now exists; any other version bump (e.g. a
			// concurrent revocation, or an unrelated token edit) must NOT be
			// mis-reported as already-bound. Reload and reclassify, deny-closed.
			if errors.Is(err, store.ErrConflict) {
				bound, rerr := pepTokenHasBinding(ctx, as, tokenID)
				if rerr != nil {
					return rerr
				}
				return classifyBindTokenConflict(err, bound)
			}
			return err
		}
		binding, err := as.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
			ServiceID: service.ID,
			TokenID:   token.ID,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("%w: %w", ErrPEPCredentialBound, err)
			}
			return err
		}
		return auditAct(
			ctx,
			as,
			caller,
			"pep_service.bind_credential",
			"core.pep_service_credential",
			binding.ID,
		)
	})
}

// UnbindPEPCredential disables one service mapping. It intentionally keeps the
// API token's Purpose set to "pep": once a secret has served as a PEP transport
// credential, it never regains ordinary API authority.
func (a *Authenticator) UnbindPEPCredential(
	ctx context.Context,
	caller Principal,
	serviceID model.ID,
	tokenID model.ID,
) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		service, err := as.PEPServices().Get(ctx, serviceID)
		if err != nil {
			return err
		}
		if err := authorizePEPAdmin(caller, service.TargetTenantID); err != nil {
			return err
		}
		binding, err := loadPEPServiceCredential(ctx, as, serviceID, tokenID)
		if err != nil {
			return err
		}
		if binding.DisabledAt != nil {
			return nil
		}
		now := a.clock.Now()
		binding.DisabledAt = &now
		updated, err := as.PEPServiceCredentials().Update(ctx, binding)
		if err != nil {
			return err
		}
		return auditAct(
			ctx,
			as,
			caller,
			"pep_service.unbind_credential",
			"core.pep_service_credential",
			updated.ID,
		)
	})
}

// UpdatePEPServiceCapabilities atomically replaces the registered capability
// vector and increments its version.
func (a *Authenticator) UpdatePEPServiceCapabilities(
	ctx context.Context,
	caller Principal,
	serviceID model.ID,
	capabilities map[string]bool,
) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		service, err := as.PEPServices().Get(ctx, serviceID)
		if err != nil {
			return err
		}
		if err := authorizePEPAdmin(caller, service.TargetTenantID); err != nil {
			return err
		}
		if service.CapabilityVersion < 1 {
			return ErrInvalidPEPService
		}
		service.Capabilities = clonePEPCapabilities(capabilities)
		service.CapabilityVersion++
		updated, err := as.PEPServices().Update(ctx, service)
		if err != nil {
			return err
		}
		return auditAct(
			ctx,
			as,
			caller,
			"pep_service.update_capabilities",
			"core.pep_service",
			updated.ID,
		)
	})
}

// DisablePEPService prevents every current credential mapping for the service
// from authenticating. Existing mapping rows remain as rotation history.
func (a *Authenticator) DisablePEPService(
	ctx context.Context,
	caller Principal,
	serviceID model.ID,
) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		service, err := as.PEPServices().Get(ctx, serviceID)
		if err != nil {
			return err
		}
		if err := authorizePEPAdmin(caller, service.TargetTenantID); err != nil {
			return err
		}
		if service.DisabledAt != nil {
			return nil
		}
		now := a.clock.Now()
		service.DisabledAt = &now
		updated, err := as.PEPServices().Update(ctx, service)
		if err != nil {
			return err
		}
		return auditAct(
			ctx,
			as,
			caller,
			"pep_service.disable",
			"core.pep_service",
			updated.ID,
		)
	})
}

// AuthenticatePEP resolves a purpose-restricted bearer token to a stable
// PEPIdentity. This is Principal.HasAudience's first productive resource-server
// caller: after token possession and the active service mapping are verified,
// the audience binding must name the service's registered PDP audience. Every
// credential or registration mismatch returns ErrUnauthenticated so callers
// cannot distinguish malformed, unknown, inactive, misbound, or misaddressed
// credentials.
func (a *Authenticator) AuthenticatePEP(
	ctx context.Context,
	bearer string,
) (PEPIdentity, error) {
	prefix, selector, secret, ok := ParseToken(bearer)
	if !ok || prefix != PrefixToken {
		return PEPIdentity{}, ErrUnauthenticated
	}

	var identity PEPIdentity
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		token, found, err := lookupAPITokenBySelector(ctx, as, selector)
		if err != nil {
			return err
		}
		// Run the constant-time secret compare on BOTH paths (a real hash on a hit, a
		// fixed dummy on a miss), so an unknown selector is timing-indistinguishable
		// from a wrong secret.
		secretOK := false
		if found {
			secretOK = SecretMatches(secret, token.SecretHash)
		} else {
			SecretMatches(secret, dummySecretHash[:])
		}
		if !found || !secretOK || token.Revoked {
			return ErrUnauthenticated
		}
		if token.ExpiresAt != nil && token.ExpiresAt.Before(a.clock.Now()) {
			return ErrUnauthenticated
		}
		// A PEP credential is never a system credential: even if a superadmin token
		// somehow gained Purpose="pep" and a mapping, it must not authenticate as a
		// PEP (defense-in-depth — a PEP identity governs one business tenant).
		if token.Purpose != pepCredentialPurpose || token.IsSuperadmin {
			return ErrUnauthenticated
		}

		bindings, _, err := as.PEPServiceCredentials().List(
			ctx,
			byEq("token_id", token.ID.String(), 1),
		)
		if err != nil {
			return err
		}
		if len(bindings) != 1 || bindings[0].DisabledAt != nil {
			return ErrUnauthenticated
		}
		binding := bindings[0]

		service, err := as.PEPServices().Get(ctx, binding.ServiceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrUnauthenticated
			}
			return err
		}
		if service.DisabledAt != nil ||
			service.TargetTenantID.IsZero() ||
			service.TargetTenantID.IsSystem() ||
			service.TargetTenantID != token.BoundTenantID ||
			service.PDPAudience == "" ||
			service.CapabilityVersion < 1 {
			return ErrUnauthenticated
		}

		tokenPrincipal := newPrincipal(
			KindToken,
			token.UserID,
			token.ID,
			false,
			token.Name,
			map[model.TenantID]string{token.BoundTenantID: token.Role},
			nil,
		)
		if token.Audience != "" {
			tokenPrincipal.audiences = strings.Split(token.Audience, "\n")
		}
		if !tokenPrincipal.HasAudience(service.PDPAudience) {
			return ErrUnauthenticated
		}

		identity = PEPIdentity{
			serviceID:              service.ID,
			tenant:                 service.TargetTenantID,
			credentialID:           token.ID,
			name:                   service.Name,
			registeredCapabilities: clonePEPCapabilities(service.Capabilities),
			capabilityVersion:      service.CapabilityVersion,
		}
		return nil
	})
	if err != nil {
		return PEPIdentity{}, err
	}
	return identity, nil
}

// pepTokenHasBinding reports whether any pep_service_credentials row (active or
// disabled/historical) currently maps this token — the signal that a competing
// bind, rather than an unrelated version bump, caused a token-update conflict.
func pepTokenHasBinding(ctx context.Context, as store.AuthScope, tokenID model.ID) (bool, error) {
	bindings, _, err := as.PEPServiceCredentials().List(ctx, byEq("token_id", tokenID.String(), 1))
	if err != nil {
		return false, err
	}
	return len(bindings) > 0, nil
}

// classifyBindTokenConflict maps a token-update version conflict during a bind:
// only a token that now owns a binding is a competing bind (ErrPEPCredentialBound);
// any other conflict is surfaced unchanged (deny-closed, typed), so a concurrent
// revocation is never mis-reported as already-bound.
func classifyBindTokenConflict(conflict error, tokenHasBinding bool) error {
	if tokenHasBinding {
		return ErrPEPCredentialBound
	}
	return conflict
}

func authorizePEPAdmin(caller Principal, tenant model.TenantID) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidPEPService
	}
	if err := checkRoleCeiling(caller, tenant, RoleAdmin); err != nil {
		return err
	}
	if _, confined := caller.ConfinedWorkspaceIn(tenant); confined {
		return ErrWorkspaceConfined
	}
	return nil
}

func loadPEPServiceCredential(
	ctx context.Context,
	as store.AuthScope,
	serviceID model.ID,
	tokenID model.ID,
) (model.PEPServiceCredential, error) {
	bindings, _, err := as.PEPServiceCredentials().List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: "service_id", Op: model.OpEq, Value: serviceID.String()},
			{Column: "token_id", Op: model.OpEq, Value: tokenID.String()},
		},
		Limit: 1,
	})
	if err != nil {
		return model.PEPServiceCredential{}, err
	}
	if len(bindings) != 1 {
		return model.PEPServiceCredential{}, store.ErrNotFound
	}
	return bindings[0], nil
}

func clonePEPCapabilities(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for name, enabled := range in {
		out[name] = enabled
	}
	return out
}
