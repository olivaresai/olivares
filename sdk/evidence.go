// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import "strings"

// evidence.go — the PEP-neutral "evidence-or-refuse" contract (D6/F9).
//
// This is the single, fail-closed evidence law every Policy Enforcement Point (PEP)
// obeys before it emits a governed effect: the in-process inference proxy, the Claude
// hook-PEP, the MCP resource-server gate, tool-pins, and orchestration schedule-fire /
// workflow. It generalizes the inference PDP's "evidence is mandatory on allow|modify;
// an allow that cannot be anchored degrades to deny (FailureEvidenceFault)" (pdp.go)
// into ONE predicate every PEP shares. Like the rest of this SDK it is Apache-2.0 and
// zero-dependency (stdlib only), so any PEP — including the Apache connectors that must
// never import the AGPL engine — can build against it.
//
// # The law
//
// A PEP MUST hold a per-operation EvidenceReceipt that is AnchoredFor the exact effect
// it is about to emit BEFORE it emits that effect. If the receipt is not anchored for
// that binding — for ANY reason — the decision is a DENY. There is no best-effort emit:
// "authorize now, audit maybe later" is the fail-open bug (the MCP gate's historical
// no-op auditor, tool-pin's discarded Mutate error) that this contract abolishes.
//
//	Claim   → reserve the OperationID single-use (before any effect)
//	Anchor  → durably record decision evidence, obtain the receipt
//	Effect  → emit the governed effect, ONLY if receipt.AnchoredFor(binding)
//	Settle  → record the outcome / release the claim (idempotent)
//
// These are documented orderings, not a frozen enum: the inference PDP runs the richer
// Decide→Activate→Postflight→Settle machine (pdp.go); a purely local mutation commits
// its anchor and its effect in ONE store transaction; an external effect commits
// claim+anchor+outbox in one transaction, THEN dispatches, THEN settles. What is common
// — and all this file freezes — is the receipt predicate and the anchoring discipline.
//
// # Why a binding, not a bare boolean
//
// The receipt is bound to {OperationID, EffectDigest}. AnchoredFor takes the EXPECTED
// binding and refuses unless the receipt matches it. This stops a confused-deputy at the
// evidence layer: a valid receipt minted for operation A can never green-light effect B
// (the same discipline F3 applies to the forwarded bytes). A binding-less "is this
// receipt OK?" predicate would accept a valid receipt for the wrong operation, so this
// contract deliberately exposes none.
//
// # The anchoring discipline (do NOT copy the rollback bug)
//
// The tamper-evident ledger commits inside a store transaction. Under the explicit
// audit-spool DEGRADE policy an over-budget Append returns a ZERO-sequence event with a
// NIL error after durably committing loss accounting (the audit_spool_gaps counter that
// eventually seals a signed in-chain gap marker). The transaction commits ONLY when its
// callback returns nil. Therefore a PEP MUST:
//
//  1. Inside the transaction, call Append.
//  2. On a real Append error, return it (roll back — nothing durable was written).
//  3. On a zero-sequence event (Seq==0), capture appendDropped=true, stage NO governed
//     mutation / claim / outbox effect, and return nil so the loss accounting commits.
//  4. On a real event, stage the effect's claim/mutation/outbox in the SAME transaction.
//  5. Only AFTER the transaction returns, call ClassifyAnchor to build the receipt.
//  6. Dispatch an external effect only against an AnchoredFor receipt.
//
// Returning the spool-full sentinel from INSIDE the transaction on Seq==0 (the historical
// bug — cmd/olivares/claudehookpep.go, core/api/handlers_accessreview.go, and the
// inference proxy's own intent legs) rolls back the loss accounting: the drop counter
// never advances, the signed gap marker never seals, and the degrade episode becomes
// invisible in the chain — while the caller still (correctly) denies. Commit the gap,
// THEN refuse. ClassifyAnchor exists to make that the path of least resistance.
//
// Mapping a raw store error to an EvidenceFault (spool-full vs not-leader vs no-ledger)
// is engine-side glue that necessarily lives in /core; this file classifies only the
// post-commit observation (dropped? anchored? which fault?), staying zero-dependency.

// OperationID is the stable, single-use idempotency identity of exactly ONE governed
// effect. It is server-minted or namespaced from a stable idempotency input — never a
// raw client reference and never merely the effect digest. Normative semantics every
// PEP MUST uphold:
//
//   - one OperationID governs exactly one logical effect;
//   - the SAME OperationID with the SAME EffectDigest is an idempotent retry that returns
//     the same receipt and does NOT re-emit the effect;
//   - the SAME OperationID with a DIFFERENT EffectDigest is a replay/rebind and MUST refuse;
//   - the dispatcher/outbox propagates the SAME OperationID end to end.
type OperationID string

// EffectDigest is the opaque, versioned, domain-separated digest of the FULL governed-
// effect binding — not merely its payload. A PEP binds into it, as applicable: the
// binding profile/domain + version, tenant, PEP service identity + surface, action,
// resolved subject, any immutable target snapshot, the exact effective request or
// approved plan, and the approval/decision reference + policy version. For the inference
// proxy this is the F3 EffectiveRequestDigest over the frozen forward bytes.
type EffectDigest string

// EvidenceBinding is the effect identity an evidence receipt must match to authorize the
// effect. It is the "expected" the PEP computes before it looks at any receipt.
type EvidenceBinding struct {
	OperationID  OperationID  `json:"operation_id"`
	EffectDigest EffectDigest `json:"effect_digest"`
}

// Valid reports whether the binding names a concrete effect. An incomplete binding never
// authorizes an effect (AnchoredFor requires Valid), so a PEP that cannot compute a full
// binding fails closed by construction.
func (b EvidenceBinding) Valid() bool {
	return strings.TrimSpace(string(b.OperationID)) != "" &&
		strings.TrimSpace(string(b.EffectDigest)) != ""
}

// EvidenceFault is the stable, machine-readable reason a per-operation anchor is
// unavailable. It is the audit dimension of a deny, orthogonal to a policy refusal. The
// empty value is the ONLY non-fault; every other (or unknown) value fails closed.
type EvidenceFault string

const (
	// EvidenceFaultNone means the per-operation evidence anchored durably.
	EvidenceFaultNone EvidenceFault = ""
	// EvidenceFaultLedgerUnwired: the required evidence adapter was not configured
	// (e.g. a recording-mandating tenant on a build with no ledger store).
	EvidenceFaultLedgerUnwired EvidenceFault = "ledger_unwired"
	// EvidenceFaultLedgerUnavailable: the evidence backend/leader/transaction service is
	// unreachable right now (e.g. a standby node's write gate, a transport fault).
	EvidenceFaultLedgerUnavailable EvidenceFault = "ledger_unavailable"
	// EvidenceFaultSpoolFull: a BLOCK-mode audit spool refused the append; NOTHING was
	// committed and the transaction rolled back.
	EvidenceFaultSpoolFull EvidenceFault = "spool_full"
	// EvidenceFaultSpoolDegraded: a DEGRADE-mode audit spool committed durable loss
	// accounting but produced no per-operation anchor. The effect is still refused; the
	// episode is durably counted (and eventually sealed as a signed in-chain gap marker).
	EvidenceFaultSpoolDegraded EvidenceFault = "spool_degraded"
	// EvidenceFaultTenantUnresolved: no trustworthy tenant chain could be selected, so the
	// decision cannot be attributed to an evidence ledger.
	EvidenceFaultTenantUnresolved EvidenceFault = "tenant_unresolved"
	// EvidenceFaultWriteError: any other append, mutation, or commit failure.
	EvidenceFaultWriteError EvidenceFault = "write_error"
)

// EvidenceReceipt is the committed result of attempting to anchor ONE binding. It has a
// single source of truth: an anchored receipt carries a non-empty EvidenceRef and no
// Fault; a refused receipt carries a Fault and no EvidenceRef. There is no independent
// "anchored" or "gap-declared" flag to fall out of sync with those two fields.
type EvidenceReceipt struct {
	OperationID  OperationID  `json:"operation_id"`
	EffectDigest EffectDigest `json:"effect_digest"`
	// EvidenceRef is the tamper-evident ledger anchor (the sibling of pdp.go's
	// DecisionVerdict.EvidenceRef), present ONLY when the per-operation event committed.
	EvidenceRef string        `json:"evidence_ref,omitempty"`
	Fault       EvidenceFault `json:"fault,omitempty"`
}

// AnchoredFor reports whether the evidence precondition is satisfied for the EXACT
// expected effect. It is the only green light for emitting an effect. It says nothing
// about policy/approval authorization — that is a separate decision the PEP also makes.
//
// It matches the receipt's baked-in {OperationID, EffectDigest} against the expected
// binding, but it CANNOT verify that r.EvidenceRef genuinely came from the ledger event
// committed for THIS binding — an opaque ref is unforgeable only if the anchoring caller
// derived it, in the SAME transaction, from the event it appended for this exact binding.
// A caller that passes a stale, cached, or cross-operation ref (see ClassifyAnchor) can
// mint a receipt that AnchoredFor accepts. That provenance is the caller's transaction
// discipline, not something a zero-dependency predicate can re-check.
func (r EvidenceReceipt) AnchoredFor(binding EvidenceBinding) bool {
	return binding.Valid() &&
		r.OperationID == binding.OperationID &&
		r.EffectDigest == binding.EffectDigest &&
		strings.TrimSpace(r.EvidenceRef) != "" &&
		r.Fault == EvidenceFaultNone
}

// MustRefuse is the single evidence-or-refuse law: a PEP MUST refuse the effect unless
// the receipt is anchored for the exact expected binding.
func (r EvidenceReceipt) MustRefuse(binding EvidenceBinding) bool {
	return !r.AnchoredFor(binding)
}

// FailureClass maps the receipt to the PDP failure taxonomy for the exact binding:
// FailureNone only when anchored, else FailureEvidenceFault. Every evidence fault is an
// evidence fault — it is never FailurePlaneUnavailable (that names the DECISION plane
// being unreachable, pdp.go), which is why the mapping ignores the specific fault value.
func (r EvidenceReceipt) FailureClass(binding EvidenceBinding) FailureClass {
	if r.AnchoredFor(binding) {
		return FailureNone
	}
	return FailureEvidenceFault
}

// ClassifyAnchor converts a post-transaction observation into a receipt. The caller runs
// the store transaction, COMMITS it (loss accounting included), and only THEN calls this
// — never from inside the transaction (see the anchoring discipline above). Arguments:
//
//   - binding: the effect identity the caller expected to anchor.
//   - evidenceRef: the ledger anchor of the event committed FOR THIS binding, IN THE SAME
//     anchoring transaction — never a precomputed, cached, or cross-operation ref. This
//     classifier cannot check that provenance (the ref is opaque), so a caller that hands
//     it a foreign ref forges a durability claim AnchoredFor will then accept: derive the
//     ref only from the append this call just committed. Empty unless a real event committed.
//   - appendDropped: the captured zero-sequence observation (Seq==0) — a DEGRADE drop whose
//     loss accounting the transaction already committed.
//   - transactionFault: the mapped fault when the transaction itself failed
//     (EvidenceFaultNone means the transaction committed).
//
// A committed transaction with neither a drop nor a usable ref, or an invalid binding, is
// a contract violation and classifies as write_error rather than a silent anchor.
func ClassifyAnchor(binding EvidenceBinding, evidenceRef string, appendDropped bool, transactionFault EvidenceFault) EvidenceReceipt {
	r := EvidenceReceipt{OperationID: binding.OperationID, EffectDigest: binding.EffectDigest}
	switch {
	case transactionFault != EvidenceFaultNone:
		r.Fault = transactionFault
	case appendDropped:
		// The transaction committed loss accounting; there is no per-operation anchor. A
		// ref alongside a drop is contradictory (nothing was persisted) → write_error.
		if strings.TrimSpace(evidenceRef) != "" {
			r.Fault = EvidenceFaultWriteError
		} else {
			r.Fault = EvidenceFaultSpoolDegraded
		}
	case !binding.Valid() || strings.TrimSpace(evidenceRef) == "":
		r.Fault = EvidenceFaultWriteError
	default:
		r.EvidenceRef = evidenceRef
	}
	return r
}
