// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// NHI lifecycle actuation contract. This file extends the identity
// contract with the WRITE-capable side of NHI lifecycle management: rotating,
// disabling, restoring and definitively revoking the credentials behind a
// roster identity.
//
// Design rules (docs/SECURITY-HARDENING.md read-first, the claude-wif Exchanger precedent):
//
//   - An actuator is a SEPARATE, explicitly-wired type. It is never part of
//     GraphProvider/Gather: the read-only guarantee of every Source stays true
//     by construction. The composition root builds an actuator only when the
//     operator opts in with a write-capable credential.
//   - Capabilities are declared honestly. A provider that exposes no rotation
//     API yields no OpRotate capability — the consumer must degrade honestly
//     (finding + manual guidance), never fabricate an actuation.
//   - Receipts are ledger-safe: they carry metadata only, never credential
//     material. The single exception is RotatedCredential.Secret — the freshly
//     minted credential returned ONCE to the approved caller, exactly like
//     claude-wif's MintedToken: it is never logged and never persisted.
//   - Every actuation is expected to be governed (HITL approval) and audited
//     by the consumer (modules/governance). The contract itself carries
//     no governance: it is the mechanical arm, not the brain.
//
// Like GraphProvider, this contract is in-process only: it does not cross the
// sdk/plugin RPC boundary.

package identitysource

import (
	"context"
	"errors"
	"time"
)

// LifecycleOp names one lifecycle actuation a provider may support.
type LifecycleOp string

// The lifecycle operations.
const (
	// OpDisable reversibly disables the credential/identity at the source
	// (quarantine). The exact provider semantics ride the receipt Detail.
	OpDisable LifecycleOp = "disable"
	// OpRestore reverses OpDisable within the product's recovery window.
	OpRestore LifecycleOp = "restore"
	// OpFinalize is the definitive end of the offboarding cascade. Providers
	// differ: some archive (Anthropic), some can only keep the identity
	// disabled (Vault) — the receipt says honestly what happened.
	OpFinalize LifecycleOp = "finalize"
	// OpRotate mints a NEW credential for the identity. The old credential is
	// NOT retired by this op (grace for cutover); see OpRetire.
	OpRotate LifecycleOp = "rotate"
	// OpRetire destroys previously issued credentials (by their non-secret
	// refs/accessors) after a rotation cutover.
	OpRetire LifecycleOp = "retire"
)

// ActuatorCapability is one honestly-declared operation an actuator can really
// perform against its provider. The consumer builds its per-provider capability
// matrix from these declarations — absence of a capability is the honest
// "degrade" signal, not an error.
type ActuatorCapability struct {
	// Op is the supported operation.
	Op LifecycleOp
	// TargetKind is the roster Identity.Kind the op applies to (e.g. "api_key",
	// "vault_entity"). Empty means any identity of this source.
	TargetKind string
	// Detail states the honest provider semantics, surfaced verbatim to the
	// operator (e.g. "disables the Vault entity; existing tokens are blocked
	// from use but NOT revoked").
	Detail string
	// RequiresTargetRef is true when the op needs an operator-declared
	// actuation target (ActuationRequest.TargetRef) because the roster ref
	// alone does not identify the credential to act on (e.g. Vault AppRole
	// rotation needs the role name; the product never guesses it).
	RequiresTargetRef bool
}

// ActuationRequest identifies what to act on.
type ActuationRequest struct {
	// Ref is the identity's roster reference (Identity.Ref / external_id).
	Ref string
	// Kind is the roster Identity.Kind, for actuators that key semantics on it.
	Kind string
	// TargetRef is the operator-declared actuation target for ops whose
	// capability sets RequiresTargetRef (e.g. "approle:billing-agent"). The
	// consumer persists and supplies it; an actuator must reject an op that
	// needs one and got none.
	TargetRef string
	// CredentialRefs are the non-secret credential references (accessors,
	// version ids) OpRetire destroys. Ignored by other ops.
	CredentialRefs []string
}

// ActuationReceipt is the non-secret, ledger-safe record of one actuation.
type ActuationReceipt struct {
	// Op is the operation performed.
	Op LifecycleOp
	// Ref is the identity acted on (roster reference).
	Ref string
	// Provider is the source the actuation ran against.
	Provider SourceKind
	// Detail honestly describes what the provider actually did (including any
	// caveat, e.g. "tokens blocked from use but not revoked").
	Detail string
	// OccurredAt is when the actuation completed (actuator clock, UTC).
	OccurredAt time.Time
}

// RotationReceipt is the non-secret record of an OpRotate.
type RotationReceipt struct {
	ActuationReceipt
	// NewCredentialRef is the non-secret reference of the minted credential
	// (an accessor or version id) — usable later for OpRetire bookkeeping.
	NewCredentialRef string
	// OldCredentialRefs are the non-secret references of credentials that
	// remain active after the mint (the retirement work list).
	OldCredentialRefs []string
	// ExpiresAt is the minted credential's expiry; zero when the provider sets
	// none.
	ExpiresAt time.Time
}

// RotatedCredential carries the minted credential exactly once, to the approved
// caller. Secret is SECRET: never logged, never persisted, never placed in any
// receipt, finding or audit record — Receipt is the only part that may be
// stored (the MintedToken.Audit() rule).
type RotatedCredential struct {
	// Receipt is the ledger-safe record of the rotation.
	Receipt RotationReceipt
	// Secret is the new credential material, returned once.
	Secret string
}

// ErrUnsupportedOperation is returned by an actuator for an operation it does
// not (or cannot honestly) support against its provider. Consumers translate it
// into the honest-degrade path, never into a retry.
var ErrUnsupportedOperation = errors.New("identitysource: lifecycle operation not supported by this provider")

// ErrTargetRefRequired is returned when an op whose capability declares
// RequiresTargetRef is invoked without ActuationRequest.TargetRef.
var ErrTargetRefRequired = errors.New("identitysource: lifecycle operation requires a declared actuation target ref")

// LifecycleActuator is the write-capable lifecycle arm an identity/secrets
// connector MAY expose as a separate opt-in type beside its read-only Source.
// The host wires it explicitly (never via the Gather plane) and the consumer
// (modules/governance) invokes it only behind a granted HITL approval.
//
// Implementations return ErrUnsupportedOperation for ops they do not declare in
// Capabilities. All errors must be non-sensitive: no credential, no token, no
// secret material ever rides an error string.
type LifecycleActuator interface {
	// Capabilities declares, honestly, the operations this actuator can
	// actually perform. It is static per configuration and safe to call
	// without a credential.
	Capabilities() []ActuatorCapability
	// Disable reversibly disables the identity/credential at the source.
	Disable(ctx context.Context, req ActuationRequest) (ActuationReceipt, error)
	// Restore reverses Disable.
	Restore(ctx context.Context, req ActuationRequest) (ActuationReceipt, error)
	// Finalize performs the provider's definitive offboarding step. The
	// receipt Detail states exactly how definitive it really is.
	Finalize(ctx context.Context, req ActuationRequest) (ActuationReceipt, error)
	// Rotate mints a new credential for the identity and returns it once. The
	// old credential stays active for cutover; retire it with Retire.
	Rotate(ctx context.Context, req ActuationRequest) (RotatedCredential, error)
	// Retire destroys the credentials named by req.CredentialRefs (non-secret
	// refs from a prior RotationReceipt).
	Retire(ctx context.Context, req ActuationRequest) (ActuationReceipt, error)
}

// FindCapability returns the first declared capability matching op and kind
// (an empty capability TargetKind matches any kind) and whether one was found.
func FindCapability(caps []ActuatorCapability, op LifecycleOp, kind string) (ActuatorCapability, bool) {
	for _, c := range caps {
		if c.Op != op {
			continue
		}
		if c.TargetKind == "" || c.TargetKind == kind {
			return c, true
		}
	}
	return ActuatorCapability{}, false
}
