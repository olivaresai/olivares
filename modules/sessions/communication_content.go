// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// PrepareProtectedPayload canonicalizes one typed content slot and applies the
// channel's exact protection policy. It is intentionally transaction-free:
// callers generate the carrier ID and complete any sealer/KMS work before they
// enter the short mutation that revalidates AAD, revisions and authority.
func PrepareProtectedPayload(
	ctx context.Context,
	sealer CommunicationContentSealer,
	slot ProtectedPayloadSlot,
	policy ProtectedPayloadPolicy,
	aad ContentAAD,
	value any,
) (ProtectedPayload, error) {
	canonical, err := CanonicalProtectedPayloadSlot(slot, value)
	if err != nil {
		return ProtectedPayload{}, err
	}
	schema, ok := slot.schema()
	if !ok || !policy.Encoding.Valid() || policy.ProtectionGeneration < 1 ||
		aad.Schema != schema || aad.ProtectionGeneration != policy.ProtectionGeneration ||
		protectedPayloadEntityKind(slot) != aad.EntityKind {
		return ProtectedPayload{}, communicationError(
			ErrInvalidCommunicationModel,
			"protected payload preparation does not match slot AAD or policy",
		)
	}
	if err := ValidateContentAAD(aad); err != nil {
		return ProtectedPayload{}, err
	}

	payload := ProtectedPayload{
		Encoding:             policy.Encoding,
		Schema:               schema,
		ProtectionGeneration: policy.ProtectionGeneration,
	}
	if policy.Encoding == PayloadPlainJSON {
		digest := sha256.Sum256(canonical)
		payload.PlainJSON = append(json.RawMessage(nil), canonical...)
		payload.Digest = append([]byte(nil), digest[:]...)
		if err := ValidateProtectedPayloadSlot(payload, slot, policy); err != nil {
			return ProtectedPayload{}, err
		}
		return payload, nil
	}

	if !communicationPortBound(sealer) {
		return ProtectedPayload{}, communicationContentUnavailable("content sealer is not configured", nil)
	}
	ciphertext, sealVersion, err := sealer.Seal(ctx, aad, append([]byte(nil), canonical...))
	if err != nil {
		return ProtectedPayload{}, communicationContentUnavailable("seal protected payload", err)
	}
	// A port implementation may use scratch storage. Freeze the Seal result
	// before invoking Digest so a reused buffer cannot rewrite the envelope.
	ciphertext = append([]byte(nil), ciphertext...)
	digest, digestVersion, err := sealer.Digest(ctx, aad, append([]byte(nil), canonical...))
	if err != nil {
		return ProtectedPayload{}, communicationContentUnavailable("digest protected payload", err)
	}
	payload.Sealed = &SealedPayload{
		Ciphertext: ciphertext,
		KeyVersion: sealVersion,
	}
	payload.Digest = append([]byte(nil), digest...)
	payload.SealKeyVersion = sealVersion
	payload.DigestKeyVersion = digestVersion
	if err := ValidateProtectedPayloadSlot(payload, slot, policy); err != nil {
		return ProtectedPayload{}, communicationContentUnavailable("sealer returned an invalid envelope", err)
	}
	return payload, nil
}

// PlanProtectedPayloadRead is the persisted-read boundary. The pure E planner
// classifies malformed model input as ErrInvalidCommunicationModel; once those
// bytes came from a locked durable carrier, the same condition means the
// service cannot establish trustworthy evidence and must answer UNKNOWN/503.
func PlanProtectedPayloadRead(
	payload ProtectedPayload,
	slot ProtectedPayloadSlot,
	policy ProtectedPayloadPolicy,
	aad ContentAAD,
	lockedCarrierAAD ContentAAD,
) (ProtectedPayloadOpenPlan, error) {
	plan, err := PlanProtectedPayloadOpen(payload, slot, policy, aad, lockedCarrierAAD)
	if err != nil {
		return ProtectedPayloadOpenPlan{}, communicationContentUnavailable(
			"persisted protected payload cannot be planned for open", err,
		)
	}
	return plan, nil
}

// OpenProtectedPayload executes a previously validated open plan. The plan is
// bound to the locked carrier AAD before this helper is called; sealed content
// is accepted only when the persisted digest verifies under its exact historic
// digest-key version. There is no active-key fallback.
func OpenProtectedPayload(
	ctx context.Context,
	sealer CommunicationContentSealer,
	plan ProtectedPayloadOpenPlan,
) (json.RawMessage, error) {
	if err := validateProtectedPayloadOpenPlan(plan); err != nil {
		return nil, communicationContentUnavailable("invalid persisted protected payload open plan", err)
	}
	if !plan.RequiresSealer {
		return append(json.RawMessage(nil), plan.PlainJSON...), nil
	}
	if !communicationPortBound(sealer) {
		return nil, communicationContentUnavailable("content sealer is not configured", nil)
	}
	plaintext, err := sealer.Open(
		ctx, plan.AAD, append([]byte(nil), plan.Ciphertext...), plan.SealKeyVersion,
	)
	if err != nil {
		return nil, communicationContentUnavailable("open protected payload", err)
	}
	plaintext = append([]byte(nil), plaintext...)
	verified, err := sealer.VerifyDigest(
		ctx,
		plan.AAD,
		append([]byte(nil), plaintext...),
		append([]byte(nil), plan.Digest...),
		plan.DigestKeyVersion,
	)
	if err != nil {
		return nil, communicationContentUnavailable("verify protected payload digest", err)
	}
	if !verified {
		return nil, communicationContentUnavailable("protected payload digest mismatch", nil)
	}
	canonical := json.RawMessage(append([]byte(nil), plaintext...))
	if err := validatePlainPayloadSlot(plan.Slot, canonical); err != nil {
		return nil, communicationContentUnavailable("opened payload is not canonical slot content", err)
	}
	return canonical, nil
}

func protectedPayloadEntityKind(slot ProtectedPayloadSlot) model.Kind {
	switch slot {
	case PayloadSlotMessage, PayloadSlotMessageTerminalReason:
		return messageKind
	case PayloadSlotAckNote:
		return messageAckKind
	case PayloadSlotDecisionRequest:
		return decisionRequestKind
	case PayloadSlotDecisionResponse:
		return decisionResponseKind
	case PayloadSlotHandoff, PayloadSlotHandoffTerminalReason:
		return handoffKind
	default:
		return ""
	}
}

func validateProtectedPayloadOpenPlan(plan ProtectedPayloadOpenPlan) error {
	if err := ValidateContentAAD(plan.AAD); err != nil {
		return err
	}
	policy := ProtectedPayloadPolicy{
		Encoding: plan.Encoding, ProtectionGeneration: plan.AAD.ProtectionGeneration,
	}
	payload := ProtectedPayload{
		Encoding:             plan.Encoding,
		Schema:               plan.AAD.Schema,
		Digest:               append([]byte(nil), plan.Digest...),
		DigestKeyVersion:     plan.DigestKeyVersion,
		ProtectionGeneration: plan.AAD.ProtectionGeneration,
	}
	if plan.RequiresSealer {
		payload.Sealed = &SealedPayload{
			Ciphertext: append([]byte(nil), plan.Ciphertext...),
			KeyVersion: plan.SealKeyVersion,
		}
		payload.SealKeyVersion = plan.SealKeyVersion
	} else {
		payload.PlainJSON = append(json.RawMessage(nil), plan.PlainJSON...)
	}
	if protectedPayloadEntityKind(plan.Slot) != plan.AAD.EntityKind {
		return communicationError(ErrInvalidCommunicationModel, "open plan has the wrong carrier kind")
	}
	return ValidateProtectedPayloadSlot(payload, plan.Slot, policy)
}

func communicationContentUnavailable(operation string, cause error) error {
	if cause == nil {
		return communicationError(ErrCommunicationEvidenceUnknown, "%s", operation)
	}
	return fmt.Errorf("%w: %s: %v", ErrCommunicationEvidenceUnknown, operation, cause)
}
