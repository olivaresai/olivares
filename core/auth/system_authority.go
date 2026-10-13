// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// systemAuthorityLifetime caps the evidence window of an engine system
// principal. A human or token window ends at its credential's expiry; the engine
// has no credential, so this ceiling stands in for that expiry. The caller's
// deadline can only shorten it.
const systemAuthorityLifetime = 5 * time.Minute

// systemAuthorityRole is the tenant grant of an engine system principal. It is
// not an RBAC role, so it grants nothing by itself: the principal's whole
// authority is its exact permission ceiling.
const systemAuthorityRole = "engine-system"

// systemAuthoritySealDomain replaces the ordinary seal domain for the engine
// system shape, the only sealed shape that carries local attribution. Every
// human and token preimage stays byte-for-byte unchanged.
const systemAuthoritySealDomain = "olivares.auth.engine-system-seal.v1\x00"

// ResolveSystemScope seals an engine system operator for exactly one business
// tenant and exactly the permissions it names, so a path no human triggered can
// obtain native mutation authority through AuthorizeRouteMutation like any
// request principal.
//
// operator must be a NewSystemOperator value and nothing more: it supplies the
// attribution (path, trigger, host) and no authority of its own. permissions is
// the engine path's hard ceiling in tenant, enforced like a work-session
// credential's: every other permission is refused before any grant or policy is
// consulted, PermSystemAdmin can never be named, and the principal holds no RBAC
// role. It has no User and no credential: nothing is borrowed from a human or an
// approver. Its provenance is the tenant's directory generation and the database
// time, read in one AuthView, and its window ends at the earlier of the caller's
// deadline and systemAuthorityLifetime. A directory change after resolution,
// including a suspension, is caught by the complete-bundle lock on the mutation
// transaction, exactly as for a human.
//
// Holding the Authenticator is the trust boundary, as for the session credential
// issuers: only the composition root may call this, never with request input.
//
// Every refusal is ErrPrincipalEvidenceUnavailable and a zero Principal.
func (a *Authenticator) ResolveSystemScope(
	ctx context.Context,
	operator Principal,
	tenant model.TenantID,
	permissions ...Permission,
) (Principal, error) {
	if ctx == nil || a == nil || a.st == nil || !unsealedSystemOperator(operator) ||
		!validPrincipalEvidenceTenant(tenant) || !validSystemPermissions(permissions) {
		return Principal{}, principalEvidenceUnavailable("invalid system scope input", nil)
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || deadline.IsZero() {
		return Principal{}, principalEvidenceUnavailable("finite context deadline is required", nil)
	}

	var (
		resolved      Principal
		callbackCalls int
	)
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		callbackCalls++
		if callbackCalls != 1 {
			return principalEvidenceUnavailable("auth view callback was invoked more than once", nil)
		}
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok || evidence == nil {
			return principalEvidenceUnavailable("auth scope lacks principal evidence capability", nil)
		}
		epoch, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil || !validPrincipalDirectoryEpochFact(tenant, epoch) {
			return principalEvidenceUnavailable("tenant directory generation", err)
		}
		now, err := evidence.TransactionNow(ctx)
		if err != nil || now.IsZero() {
			return principalEvidenceUnavailable("database clock is unavailable", err)
		}
		freshUntil := earlierDeadline(deadline, now.Time().Add(systemAuthorityLifetime))
		if !freshUntil.After(now.Time()) {
			return principalEvidenceUnavailable("context deadline is not after database time", nil)
		}
		p := Principal{
			Kind:         KindUser,
			DisplayName:  operator.localVia,
			grants:       map[model.TenantID]string{tenant: systemAuthorityRole},
			sessionScope: tenant,
			localVia:     operator.localVia,
			localSubject: operator.localSubject,
			localMeta:    operator.AuditMeta(),
			localSystem:  true,
			evidence: principalEvidenceProvenance{
				tenant:         tenant,
				directoryEpoch: epoch,
				authorityMode:  principalEngineSystemAuthority,
				observedAt:     now.Time(),
				freshUntil:     freshUntil,
			},
		}.withRestrictedPermissions(tenant, permissions...)
		seal, err := computePrincipalAuthoritySeal(p)
		if err != nil {
			return principalEvidenceUnavailable("seal system principal authority", err)
		}
		p.evidence.seal = seal
		resolved = p
		return nil
	})
	if callbackCalls != 1 {
		return Principal{}, principalEvidenceUnavailable("auth view callback was not invoked exactly once", err)
	}
	if err != nil {
		if errors.Is(err, ErrPrincipalEvidenceUnavailable) {
			return Principal{}, err
		}
		return Principal{}, principalEvidenceUnavailable("auth view failed", err)
	}
	// A store that ran the callback and dropped its error must not turn into a
	// successful empty principal.
	if !validPrincipalAuthoritySeal(resolved) {
		return Principal{}, principalEvidenceUnavailable("system principal was not sealed", nil)
	}
	return resolved, nil
}

// unsealedSystemOperator accepts exactly what NewSystemOperator returns: engine
// attribution with no tenant grant, credential, User or sealed provenance.
func unsealedSystemOperator(p Principal) bool {
	return p.IsSystemOperator() && p.IsLocalOperator() && len(p.localMeta) != 0 &&
		p.Kind == KindUser && p.Superadmin && p.UserID.IsZero() && p.CredID.IsZero() &&
		len(p.grants) == 0 && p.restricted == nil && p.credentialRef == (PrincipalRef{}) &&
		p.evidence.authorityMode == 0
}

// validSystemPermissions is a non-empty ceiling of distinct, canonical
// permissions that never includes the system permission.
func validSystemPermissions(permissions []Permission) bool {
	if len(permissions) == 0 {
		return false
	}
	seen := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		if _, dup := seen[permission]; dup || permission == PermSystemAdmin ||
			permission.Verb() == "" || permission.Resource() == "" ||
			strings.TrimSpace(string(permission)) != string(permission) ||
			strings.ContainsAny(string(permission), "\n\r\x00") {
			return false
		}
		seen[permission] = struct{}{}
	}
	return true
}

// validSealedSystemPrincipal is the closed shape ResolveSystemScope produces.
// Every human, token, delegation and session field is empty; the single grant is
// the non-RBAC system role in the sealed tenant, to which the principal is
// scoped and whose exact permission ceiling is its whole authority.
func validSealedSystemPrincipal(p Principal) bool {
	e := p.evidence
	plainEpoch := store.AuthorizationFactRef{Kind: e.directoryEpoch.Kind, ID: e.directoryEpoch.ID, Version: e.directoryEpoch.Version}
	role, admitted := p.grants[e.tenant]
	ceiling := p.restricted[e.tenant]
	permissions := make([]Permission, 0, len(ceiling))
	for permission := range ceiling {
		permissions = append(permissions, permission)
	}
	return p.Kind == KindUser && p.localSystem && p.localVia != "" && p.localSubject != "" &&
		p.DisplayName == p.localVia && p.localMeta["actor_via"] == p.localVia &&
		p.localMeta["actor_subject"] == p.localSubject &&
		p.UserID.IsZero() && p.CredID.IsZero() && !p.Superadmin && p.Email == "" &&
		p.AAL == 0 && len(p.AMR) == 0 && p.AgentIdentity == "" && p.SessionIdentity == "" &&
		p.SessionWorkspaceID.IsZero() && p.SessionRunRef == "" && p.SessionFence == 0 &&
		len(p.grants) == 1 && admitted && role == systemAuthorityRole && p.sessionScope == e.tenant &&
		validSystemPermissions(permissions) && exactRestrictedPermissionSet(p.restricted, e.tenant, permissions...) &&
		len(p.groups) == 0 && len(p.audiences) == 0 && p.actAs.IsZero() &&
		len(p.confined) == 0 && len(p.excluded) == 0 && len(p.floors) == 0 &&
		p.sessionOrigin == nil && p.credentialRef == (PrincipalRef{}) && e.ref == (PrincipalRef{}) &&
		validPrincipalEvidenceTenant(e.tenant) && validPrincipalDirectoryEpochFact(e.tenant, e.directoryEpoch) &&
		e.directoryEpoch == plainEpoch && e.userAuthority == (store.UserAuthorityFactRef{}) &&
		e.authenticatedAt.IsZero() && !e.observedAt.IsZero() && e.freshUntil.After(e.observedAt)
}

// evidenceCredential is the credential a principal's sealed provenance names:
// its exact Ref for a human or a token, and none for the engine system
// principal, which has no credential to name. ok is false when a system
// principal carries a credential or any other principal lacks one. It checks
// only the credential: every caller must still verify the seal.
func (p Principal) evidenceCredential() (PrincipalRef, bool) {
	ref, ok := p.Ref()
	if p.evidence.authorityMode == principalEngineSystemAuthority {
		return PrincipalRef{}, !ok
	}
	return ref, ok
}
