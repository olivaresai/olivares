// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_pdp.go is the PDP-SERVICE identity adapter for the runGates
// seam. Where resolveBearerIdentity resolves a subject from an INBOUND transport
// credential, resolveDelegatedIdentity resolves the subject from a DelegationProof
// presented by an ALREADY-AUTHENTICATED PEP service (sdk/pdp.go invariant #2): it
// verifies+claims the proof, builds the SUBJECT principal exactly as the equivalent
// direct credential would present it, and seals the SAME resolvedIdentity snapshot
// the shared, deny-closed gate chain consumes.
//
// It DELIBERATELY separates the two failure domains the bearer adapter conflates
// (inferenceproxy.go:534-538): PEP-service AUTHENTICATION is the CALLER's step
// (AuthenticatePEP), not this adapter's; this adapter only maps SUBJECT-delegation
// faults (proof malformed / invalid / replayed / verification plane down). Every
// edge fails closed.

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk"
)

// delegationVerifier verifies a PEP-presented DelegationProof and atomically
// claims a single-use decision. *auth.Authenticator satisfies it; the adapter
// depends on this narrow seam so tests can substitute a stub for the fault-mapping
// rows while the integration path runs the real store-backed verifier.
type delegationVerifier interface {
	VerifyAndClaimDelegation(ctx context.Context, pep auth.PEPIdentity, proof auth.DelegationProofInput, presented auth.PresentedRequest) (auth.VerifiedDelegation, error)
}

var _ delegationVerifier = (*auth.Authenticator)(nil)

// resolveDelegatedIdentity is the PDP-SERVICE adapter for the runGates seam. Its
// PRECONDITION is that pep is already an AUTHENTICATED PEP service identity
// (AuthenticatePEP has run in the caller): this adapter does NOT authenticate the
// PEP — it verifies the SUBJECT delegation the PEP presents and resolves the
// subject principal.
//
// Flow (mirroring resolveBearerIdentity, deny-closed at every edge):
//  1. VerifyAndClaimDelegation → the sealed VerifiedDelegation (or a typed fault
//     mapped by mapDelegationFault).
//  2. tenant = the delegation's governed tenant; deny-closed unless it is non-zero
//     AND equals the authenticated PEP's tenant (defense-in-depth: never seal a
//     subject with no governed tenant, nor one bound to a different tenant than the
//     PEP the decision is claimed against).
//  3. d.policy.Policy(ctx, tenant) — the SAME unreadable-plane deny-closed posture
//     as the bearer adapter (a security proxy that cannot read its own governance
//     config must not forward).
//  4. PrincipalForDelegation seals the SUBJECT principal; newResolvedIdentity seals
//     the snapshot (an unsealable half-resolved subject denies closed).
//
// It returns the sealed snapshot AND the VerifiedDelegation outcome, so the future
// service composition can bind the decision (DecisionID, Retried, StoredVerdictJSON)
// to later protocol phases. On any deny it returns the UNSEALED zero snapshot, a
// zero outcome, the semantic deny, and ok=false.
//
// RESIDUAL (documented, not fixed here — owner: the later service stage,
// S9): even when VerifyAndClaimDelegation resolves an already-FINAL claim
// (Retried with a StoredVerdictJSON), this adapter still re-reads per-tenant policy
// and re-seals identity, so a policy-store outage returns 503 for a request whose
// verdict is already durably stored. This is DENY-CLOSED and therefore safe (a
// proxy that cannot read its own governance config must not forward). The
// verdict-REPLAY optimization — returning the stored verdict WITHOUT re-reading
// policy — is deliberately deferred to the service stage that first consumes the
// sealed identity and stored verdict; nothing consumes them in this stage. It is
// NOT added here: an early-return replay branch that skipped policy would have to
// return ok=true with an unsealed identity, a footgun the shared gate chain must
// never be handed.
func (d *inferenceProxyDecider) resolveDelegatedIdentity(ctx context.Context, pep auth.PEPIdentity, proof auth.DelegationProofInput, presented auth.PresentedRequest) (resolvedIdentity, auth.VerifiedDelegation, gateResult, bool) {
	verified, err := d.delegation.VerifyAndClaimDelegation(ctx, pep, proof, presented)
	if err != nil {
		return resolvedIdentity{}, auth.VerifiedDelegation{}, d.mapDelegationFault(err), false
	}

	// The decision is bound to the authenticated PEP's tenant. A verified delegation
	// that does not name that exact tenant (or names none) is refused — never sealed.
	tenant := verified.Tenant()
	if tenant.IsZero() || tenant != pep.Tenant() {
		return resolvedIdentity{}, auth.VerifiedDelegation{}, gateDeny(gateCodeDelegationInvalid, sdk.FailureDelegationInvalid, http.StatusForbidden, "permission_error", "delegated subject tenant does not match the authenticated PEP service (deny-closed)"), false
	}

	// Load the per-tenant governance config. As in the bearer adapter, an unreadable
	// governance plane is the proxy-DOWN case: default fail-CLOSED (2026-06-17,
	// D1) — we cannot even read the fail-open knob, so we do not forward.
	pol, perr := d.policy.Policy(ctx, tenant)
	if perr != nil {
		d.log.Warn("inference-proxy: governance config unreadable for delegated identity; denying (deny-closed)", "err", perr)
		return resolvedIdentity{}, auth.VerifiedDelegation{}, gateDeny(gateCodePolicyUnreadable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error", "governance configuration unavailable (deny-closed)"), false
	}

	// Build the SUBJECT principal from the sealed VerifiedDelegation (the gated
	// constructor — the ONLY path that turns unexported delegation material into a
	// Principal), then seal the snapshot with the SAME formula every gate uses.
	principal := auth.PrincipalForDelegation(verified)
	id, sealed := newResolvedIdentity(principal, tenant, pol)
	if !sealed {
		return resolvedIdentity{}, auth.VerifiedDelegation{}, gateDeny(gateCodeIdentityUnverified, sdk.FailureDelegationInvalid, http.StatusServiceUnavailable, "api_error", "delegated identity resolution incomplete (deny-closed)"), false
	}
	return id, verified, gateResult{}, true
}

// mapDelegationFault maps the delegation verifier's typed domain faults onto the
// PEP-neutral gateCode + sdk.FailureClass taxonomy the future PDP verdict layer
// consumes. Reasons are static, non-sensitive strings — never a selector, secret,
// nonce, digest, or any other wire value (the verifier already returns
// indistinguishable, scrubbed domain errors).
//
// Replay status justification: a replayed single-use claim (nonce reuse / stale
// IssuedAt / a burned handle presented again) is a state CONFLICT — the request
// contradicts the durable single-use decision record — so it maps to 409 Conflict.
// The Anthropic-shaped error family is invalid_request_error: the caller can
// correct it (a fresh nonce + IssuedAt succeeds), unlike a firm permission_error or
// a server-side api_error. The repo has no prior FailureReplay HTTP mapping; this
// sets it, consistent with the frozen design's failure table.
//
// The default arm is deny-closed: any error that is NOT one of the three typed
// domain faults is treated as a verification-plane fault (503), never silently
// allowed — a raw store error propagates through VerifyAndClaimDelegation unchanged.
func (d *inferenceProxyDecider) mapDelegationFault(err error) gateResult {
	switch {
	case errors.Is(err, auth.ErrDelegationSchemeReserved):
		// The reserved "actas-token" scheme maps to the same protocol failure class
		// as a malformed proof, but carries a distinct, stable reason so a caller can
		// tell "reserved/unimplemented scheme" apart from "malformed token".
		return gateDeny(gateCodeDelegationProtocol, sdk.FailureProtocolError, http.StatusBadRequest, "invalid_request_error", "delegation scheme is reserved and unsupported in this protocol version")
	case errors.Is(err, auth.ErrDelegationProtocol):
		return gateDeny(gateCodeDelegationProtocol, sdk.FailureProtocolError, http.StatusBadRequest, "invalid_request_error", "delegation proof is malformed or uses an unsupported scheme")
	case errors.Is(err, auth.ErrDelegationInvalid):
		return gateDeny(gateCodeDelegationInvalid, sdk.FailureDelegationInvalid, http.StatusForbidden, "permission_error", "the delegation proof could not be verified")
	case errors.Is(err, auth.ErrDelegationReplay):
		return gateDeny(gateCodeDelegationReplay, sdk.FailureReplay, http.StatusConflict, "invalid_request_error", "the delegation request was already claimed; present a fresh nonce and issued-at")
	case errors.Is(err, auth.ErrDelegationEvidenceFault):
		// A security-critical delegation effect committed durably but its per-operation
		// audit was dropped by a DEGRADE-mode spool (evidence-or-refuse, sdk/evidence.go).
		// The durable loss accounting is committed; only the OBSERVABLE decision is refused.
		// It is an EVIDENCE fault, not a decision-plane outage — 503 deny-closed.
		return gateDeny(gateCodeDelegationEvidenceFault, sdk.FailureEvidenceFault, http.StatusServiceUnavailable, "api_error", "delegation decision could not be durably anchored (deny-closed)")
	default:
		d.log.Warn("inference-proxy: delegation verification plane unreadable; denying (deny-closed)", "err", err)
		return gateDeny(gateCodeDelegationPlaneUnavailable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error", "delegation verification plane unavailable (deny-closed)")
	}
}
