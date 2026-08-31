// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// evidenceops.go (q1) — the durable evidence operation journal: the
// engine-side enforcement seam for the frozen S5 evidence contract
// (sdk/evidence.go). A PEP that emits an EXTERNAL effect (the MCP gateway,
// stage 3) claims the effect's single-use OperationID and anchors its evidence
// BEFORE the effect, dispatches only on an AnchoredFor receipt, and settles the
// outcome durably afterwards. The journal is generic and tenant-scoped; it
// stores refs and digests ONLY (docs/SECURITY-HARDENING.md) — the ledger events its refs point
// at are the evidence.
//
// Layering: the sdk (Apache-2.0, zero-dependency) freezes the receipt predicate
// and the anchoring discipline; mapping a RAW store error to an EvidenceFault
// is engine-side glue that sdk/evidence.go:70-72 explicitly assigns to /core —
// EvidenceFaultForStoreError below is that single mapping, so no consumer
// re-implements it. Importing sdk here is inward-legal: core depends on core +
// sdk (scripts/check-boundary.sh); the forbidden directions are sdk→core and
// core→modules/connectors.

// Sentinel errors of the evidence operation journal. Callers match them with
// errors.Is. They are deliberately NOT EvidenceFaults: a rebind or an integrity
// violation is a refusal of the caller's request, not a failure to anchor.
var (
	// ErrEvidenceRebind is a same-OperationID / different-EffectDigest replay:
	// the single-use claim is bound to another effect. The caller maps it to
	// sdk.FailureReplay and refuses.
	ErrEvidenceRebind = errors.New("evidence operation rebind (effect digest mismatch)")
	// ErrEvidenceRaced is a concurrent duplicate claim that lost the
	// UNIQUE(tenant_id, operation_id) insert. The store's Mutate rolls the whole
	// losing transaction back — the losing evidence append included — so nothing
	// durable was written; the caller re-runs the claim and finds the committed
	// winner (an exact replay). ClaimEvidenceOperation does that retry itself.
	ErrEvidenceRaced = errors.New("evidence operation claim raced; retry")
	// ErrEvidenceIntegrity is a settlement that contradicts the journal: settling
	// a missing claim row, or re-settling with a different outcome. The caller's
	// transaction must roll back rather than record the contradiction.
	ErrEvidenceIntegrity = errors.New("evidence operation integrity violation")
	// ErrEvidenceInvalid is an incomplete/invalid claim or settlement input (a
	// caller bug, not a store fault). A claim with an invalid binding still
	// fails CLOSED at the driver — sdk.ClassifyAnchor classifies it write_error
	// — while an invalid settlement surfaces loudly (see the drivers).
	ErrEvidenceInvalid = errors.New("evidence operation input invalid")
)

// EvidenceClaim is the input to EvidenceOperationRepo.Claim: the operation's
// identity, binding and attribution. Refs and digests only — no raw request.
type EvidenceClaim struct {
	// OperationID is the single-use idempotency identity (sdk.OperationID).
	OperationID string
	// EffectDigest is the full-binding digest (sdk.EffectDigest).
	EffectDigest string
	// Surface is the claiming PEP surface (e.g. "mcp.gateway").
	Surface string
	// Action is the governed action verb; the claim ledger event is
	// "<Action>.claim" and the settlement event "<Action>.settle".
	Action string
	// Actor and ActorKind attribute the ledger events (model.AuditDraft).
	Actor     string
	ActorKind string
	// LeaderEpoch is the fencing token stored with the claim.
	// ClaimEvidenceOperation stamps it from the DURABLE fence
	// (EpochFencer.FencedEpoch); a caller running the repo primitive directly
	// supplies its own durably-verified value.
	LeaderEpoch uint64
}

// EvidenceClaimResult is the in-transaction outcome of a Claim.
type EvidenceClaimResult struct {
	// Op is the journal row (freshly created, or the recorded winner on an
	// exact replay). Zero when Dropped.
	Op model.EvidenceOperation
	// Fresh is true when THIS call created the claim — the only green light to
	// dispatch the effect (after the post-commit receipt check).
	Fresh bool
	// Dropped is true on a DEGRADE-mode Seq==0 evidence drop: the loss
	// accounting is staged in the transaction and NOTHING else was — the caller
	// MUST return nil so it commits (F9 discipline, sdk/evidence.go), then
	// refuse the effect (spool_degraded).
	Dropped bool
}

// EvidenceSettlement is the input to EvidenceOperationRepo.Settle: the durable
// outcome of one claimed operation. Refs and digests only.
type EvidenceSettlement struct {
	// OperationID names the claimed operation.
	OperationID string
	// EffectDigest must match the claimed binding (a mismatch is a rebind).
	EffectDigest string
	// State is the terminal settlement state: completed | not_sent | unknown |
	// blocked | withheld (model.EvidenceOperationState.Terminal).
	State model.EvidenceOperationState
	// ResultDigest is the opaque digest of the outcome (may be empty for
	// outcomes without a result).
	ResultDigest string
	// DispatchRef is an opaque dispatch reference (may be empty). It IS part of
	// settlement identity: {State, ResultDigest, DispatchRef} must ALL match the
	// recorded outcome for a re-settle to replay idempotently — a retry carrying
	// the same state/result but a DIFFERENT upstream dispatch id is the
	// signature of a double dispatch and refuses with ErrEvidenceIntegrity
	// (review P1-2).
	DispatchRef string
	// Actor and ActorKind attribute the settlement ledger event.
	Actor     string
	ActorKind string
}

// EvidenceSettleResult is the in-transaction outcome of a Settle.
type EvidenceSettleResult struct {
	// Op is the settled row (or the recorded row on an idempotent re-settle).
	Op model.EvidenceOperation
	// Fresh is true when THIS call recorded the settlement.
	Fresh bool
	// Dropped is true on a DEGRADE-mode Seq==0 drop of the outcome event: the
	// row deliberately STAYS 'claimed' (nothing but the loss accounting was
	// staged — same F9 discipline as the claim), which is the safe ambiguous
	// crash-shape: a claimed-never-settled operation is never re-dispatched.
	Dropped bool
}

// EvidenceOperationRepo is the tenant-pinned durable journal of external-effect
// operations, reached from Scope.EvidenceOperations() like the ledger from
// Scope.Audit(). Claim and Settle are IN-TRANSACTION primitives: each appends
// its ledger event and stages its row change in the caller's open transaction,
// so anchor and journal commit atomically. They follow the F9 anchoring
// discipline — a real append error is returned (rollback, deny-closed); a
// DEGRADE Seq==0 drop stages nothing and reports Dropped so the caller commits
// the loss accounting and refuses AFTER the commit (sdk.ClassifyAnchor). The
// ClaimEvidenceOperation / SettleEvidenceOperation drivers run that full
// transaction + post-commit classification for callers that hold a Store.
type EvidenceOperationRepo interface {
	// Get returns the journal row for operationID, or ErrNotFound.
	Get(ctx context.Context, operationID string) (model.EvidenceOperation, error)
	// Claim reserves the operation single-use: absent ⇒ append the claim
	// evidence event AND insert the 'claimed' row (Fresh); present with the
	// exact digest ⇒ return the recorded row WITHOUT appending or writing
	// (replay, Fresh=false); present with a different digest ⇒ ErrEvidenceRebind;
	// a concurrent duplicate insert ⇒ ErrEvidenceRaced (roll back and re-read).
	Claim(ctx context.Context, c EvidenceClaim) (EvidenceClaimResult, error)
	// Settle records the terminal outcome: append the outcome evidence event
	// and update the row atomically. A missing row is ErrEvidenceIntegrity; a
	// re-settle whose {State, ResultDigest, DispatchRef} all equal the recorded
	// outcome AND whose recorded OutcomeEvidenceRef is intact (non-empty) is
	// idempotent (Fresh=false, nothing appended); any other re-settle is
	// ErrEvidenceIntegrity; a digest mismatch is ErrEvidenceRebind.
	Settle(ctx context.Context, s EvidenceSettlement) (EvidenceSettleResult, error)
}

// EvidenceClaimOutcome is the post-commit result of ClaimEvidenceOperation.
type EvidenceClaimOutcome struct {
	// Binding is the effect identity the claim was made for.
	Binding sdk.EvidenceBinding
	// Receipt is the committed evidence receipt for Binding. The caller MUST
	// refuse the effect when Receipt.MustRefuse(Binding) — evidence faults
	// (spool_full, spool_degraded, ledger_unavailable, ledger_unwired,
	// write_error) surface HERE, never as a Go error.
	Receipt sdk.EvidenceReceipt
	// Op is the journal row (zero when the receipt refuses).
	Op model.EvidenceOperation
	// Fresh is true when this call created the claim: the effect is dispatched
	// only for a Fresh, anchored claim. A non-Fresh anchored outcome is an
	// exact replay — return the recorded state, never re-dispatch.
	Fresh bool
}

// ClaimEvidenceOperation runs the full claim in ONE store transaction and
// classifies the anchor AFTER it commits (sdk.ClassifyAnchor — never inside).
// On a concurrent duplicate claim the losing transaction rolls back (its
// evidence append included) and the claim re-runs once to re-read the
// committed winner.
//
// PREVALIDATION happens before ANY store I/O (review P1-3):
//
//  1. An sdk-invalid binding (empty or whitespace-only OperationID/EffectDigest
//     under the TrimSpace rule, sdk/evidence.go) refuses with a write_error
//     receipt — the same class sdk.ClassifyAnchor assigns an invalid binding —
//     without appending evidence or creating a row.
//  2. Missing/whitespace surface, action or actor metadata is a caller bug:
//     ErrEvidenceInvalid, loudly, again without store I/O.
//  3. A nil st is the unwired ledger: ledger_unwired receipt.
//  4. The claim's LeaderEpoch is stamped from the DURABLE fence
//     (EpochFencer.FencedEpoch) — never from the process-local Epoch() cache. A
//     fence failure refuses ledger_unavailable; an elector without the fence
//     capability refuses ledger_unwired (fencing infrastructure not wired —
//     fail closed, never fall back to the in-memory pair).
//
// PRE-COMMIT FENCE (round-3 item 1): for a FRESH claim the fence runs a
// SECOND time INSIDE the Mutate callback — after the writes, immediately before
// the callback returns nil, i.e. before commit — and must still equal the
// stamped epoch. A leader that loses its lock session mid-transaction (locally
// still active until its poll tick) therefore rolls the whole staged claim —
// evidence append included — back instead of committing under a superseded
// epoch; the failure classifies ledger_unavailable. The recheck deliberately
// does NOT run for an exact replay (no writes to protect) nor for the Seq==0
// Dropped path: that transaction stages ONLY the loss accounting, and NO fence
// check may roll committed loss accounting back (F9).
//
// The only error returns are ErrEvidenceInvalid (caller bug), ErrEvidenceRebind
// (map to sdk.FailureReplay) and a repeated ErrEvidenceRaced (defensive; the
// winner's row should always be readable after one retry). Everything else is
// expressed as a receipt fault.
func ClaimEvidenceOperation(ctx context.Context, st Store, tenant model.TenantID, claim EvidenceClaim) (EvidenceClaimOutcome, error) {
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(claim.OperationID),
		EffectDigest: sdk.EffectDigest(claim.EffectDigest),
	}
	out := EvidenceClaimOutcome{Binding: binding}
	if !binding.Valid() {
		out.Receipt = sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultWriteError)
		return out, nil
	}
	if err := ValidateEvidenceClaim(claim); err != nil {
		return out, err
	}
	if st == nil {
		out.Receipt = sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
		return out, nil
	}
	fencer, ok := st.Leader().(EpochFencer)
	if !ok {
		out.Receipt = sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
		return out, nil
	}
	epoch, ferr := fencer.FencedEpoch(ctx)
	if ferr != nil {
		out.Receipt = sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnavailable)
		return out, nil
	}
	claim.LeaderEpoch = epoch
	for attempt := 0; ; attempt++ {
		var res EvidenceClaimResult
		txErr := st.Mutate(ctx, tenant, func(sc Scope) error {
			var err error
			res, err = sc.EvidenceOperations().Claim(ctx, claim)
			if err != nil {
				return err
			}
			if !res.Fresh {
				// Exact replay (no writes) or a Seq==0 Dropped transaction whose
				// only staged change is the loss accounting — F9: it must commit,
				// so no fence check may run here.
				return nil
			}
			// Pre-commit recheck of the DURABLE fence against the epoch this
			// claim was stamped with (see the doc comment above).
			return evidencePreCommitFence(ctx, fencer, claim.LeaderEpoch)
		})
		switch {
		case errors.Is(txErr, ErrEvidenceRebind):
			return out, txErr
		case errors.Is(txErr, ErrEvidenceRaced):
			if attempt == 0 {
				continue // the loser rolled back whole; re-read the committed winner
			}
			return out, txErr
		case txErr != nil:
			out.Receipt = sdk.ClassifyAnchor(binding, "", false, EvidenceFaultForStoreError(txErr))
			return out, nil
		case res.Dropped:
			// The transaction committed ONLY the loss accounting (F9).
			out.Receipt = sdk.ClassifyAnchor(binding, "", true, sdk.EvidenceFaultNone)
			return out, nil
		}
		out.Op, out.Fresh = res.Op, res.Fresh
		out.Receipt = sdk.ClassifyAnchor(binding, res.Op.ClaimEvidenceRef, false, sdk.EvidenceFaultNone)
		return out, nil
	}
}

// EvidenceSettleOutcome is the post-commit result of SettleEvidenceOperation.
type EvidenceSettleOutcome struct {
	// Binding is the settled effect identity.
	Binding sdk.EvidenceBinding
	// Receipt carries the OUTCOME event's anchor (or the recorded one on an
	// idempotent re-settle); on an evidence fault it refuses and the row stayed
	// 'claimed'.
	Receipt sdk.EvidenceReceipt
	// Op is the settled journal row (zero when the receipt refuses).
	Op model.EvidenceOperation
	// Fresh is true when this call recorded the settlement.
	Fresh bool
}

// SettleEvidenceOperation records the terminal outcome of a claimed operation
// in ONE transaction (outcome evidence event + row update) and classifies the
// anchor after commit, mirroring ClaimEvidenceOperation. On a DEGRADE evidence
// drop the row deliberately remains 'claimed' (only the loss accounting
// commits): ambiguous-but-safe, since a claimed operation is never
// re-dispatched. Error returns: ErrEvidenceInvalid (validation — checked FIRST,
// before store availability, so a nil store or a standby can never mask a
// caller bug as an anchoring fault review P1-3), ErrEvidenceIntegrity
// (missing row, conflicting re-settle), ErrEvidenceRebind (digest mismatch),
// and a repeated concurrency conflict; evidence faults surface as receipt
// faults.
//
// PRE-COMMIT FENCE + THE SETTLEMENT-EPOCH DECISION (round-3 item 1): a
// FRESH settlement re-runs the DURABLE fence inside the Mutate callback, after
// the writes and before commit, comparing against the STORED row's LeaderEpoch
// (the claim's fencing token). Consequence, chosen deliberately and
// deny-closed: a settlement under a NEWER legitimate epoch — the row was
// claimed at epoch N, the cluster is now at N+1 after a failover — REFUSES
// with ledger_unavailable and rolls back. A post-failover node must not
// silently adopt a pre-failover claim: it cannot know whether the old leader's
// dispatch raced its demotion, so recording its outcome would launder an
// unfenced effect into settled evidence. The row stays 'claimed' (the safe,
// non-replayable crash-shape); adopting/settling orphaned pre-failover claims
// is a future EXPLICIT takeover operation with its own evidence trail, never
// an implicit side effect of settle. Idempotent re-settles (no writes) and the
// Seq==0 Dropped path (loss accounting only — F9) never run the fence.
func SettleEvidenceOperation(ctx context.Context, st Store, tenant model.TenantID, settlement EvidenceSettlement) (EvidenceSettleOutcome, error) {
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(settlement.OperationID),
		EffectDigest: sdk.EffectDigest(settlement.EffectDigest),
	}
	out := EvidenceSettleOutcome{Binding: binding}
	if err := ValidateEvidenceSettlement(settlement); err != nil {
		return out, err
	}
	if st == nil {
		out.Receipt = sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
		return out, nil
	}
	fencer, ok := st.Leader().(EpochFencer)
	if !ok {
		out.Receipt = sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
		return out, nil
	}
	for attempt := 0; ; attempt++ {
		var res EvidenceSettleResult
		txErr := st.Mutate(ctx, tenant, func(sc Scope) error {
			var err error
			res, err = sc.EvidenceOperations().Settle(ctx, settlement)
			if err != nil {
				return err
			}
			if !res.Fresh {
				// Idempotent replay (no writes) or Seq==0 Dropped (loss accounting
				// only — F9 commits, never fenced).
				return nil
			}
			// Pre-commit recheck against the CLAIM's stored fencing token (the
			// settlement-epoch decision above).
			return evidencePreCommitFence(ctx, fencer, res.Op.LeaderEpoch)
		})
		switch {
		case errors.Is(txErr, ErrEvidenceRebind), errors.Is(txErr, ErrEvidenceIntegrity),
			errors.Is(txErr, ErrEvidenceInvalid):
			// Journal contradictions and caller bugs are NOT anchoring faults:
			// surface them loudly (a write_error receipt could be mistaken for a
			// transient anchor failure and retried forever).
			return out, txErr
		case errors.Is(txErr, ErrEvidenceRaced):
			if attempt == 0 {
				continue // concurrent settle won the version CAS; re-read resolves idempotent-or-integrity
			}
			return out, txErr
		case txErr != nil:
			out.Receipt = sdk.ClassifyAnchor(binding, "", false, EvidenceFaultForStoreError(txErr))
			return out, nil
		case res.Dropped:
			out.Receipt = sdk.ClassifyAnchor(binding, "", true, sdk.EvidenceFaultNone)
			return out, nil
		}
		out.Op, out.Fresh = res.Op, res.Fresh
		out.Receipt = sdk.ClassifyAnchor(binding, res.Op.OutcomeEvidenceRef, false, sdk.EvidenceFaultNone)
		return out, nil
	}
}

// evidencePreCommitFence is the in-transaction leadership recheck the drivers
// run at the END of a WRITING Mutate callback (fresh claim / fresh settlement),
// after the writes and before the commit. It wraps its failures in ErrNotLeader
// — the condition it durably discovered — so the rolled-back transaction
// classifies ledger_unavailable through the standard mapping, exactly like the
// write gate's own refusal. Multi-%w keeps the fence's underlying cause
// matchable.
func evidencePreCommitFence(ctx context.Context, f EpochFencer, claimEpoch uint64) error {
	current, err := f.FencedEpoch(ctx)
	if err != nil {
		return fmt.Errorf("%w: pre-commit epoch fence: %w", ErrNotLeader, err)
	}
	if current != claimEpoch {
		return fmt.Errorf("%w: pre-commit epoch fence: cluster epoch moved (claim %d, current %d)",
			ErrNotLeader, claimEpoch, current)
	}
	return nil
}

// EvidenceFaultForStoreError is THE raw-store→EvidenceFault mapping that
// sdk/evidence.go:70-72 assigns to /core, shared by every PEP instead of being
// re-implemented per consumer (the historical copies in
// modules/capabilities/toolpins.go and cmd/olivares/claudehookpep.go remain
// local to their modules; new consumers use this one):
//
//	nil                      → EvidenceFaultNone
//	ErrAuditSpoolFull        → spool_full   (block mode refused; nothing committed)
//	ErrNotLeader             → ledger_unavailable (standby write gate)
//	ErrStoreUnavailable      → ledger_unavailable (backend unreachable: lost/refused
//	                           connection, SQLSTATE class 08 — sdk/evidence.go:120)
//	anything else            → write_error (constraint/serialization/ordinary faults)
//
// Two faults are NOT reachable from an error by design and are produced by the
// drivers directly: spool_degraded comes from the committed Seq==0 drop
// (appendDropped, never an error — returning one would roll the loss
// accounting back), and ledger_unwired from a nil Store.
func EvidenceFaultForStoreError(err error) sdk.EvidenceFault {
	switch {
	case err == nil:
		return sdk.EvidenceFaultNone
	case errors.Is(err, ErrAuditSpoolFull):
		return sdk.EvidenceFaultSpoolFull
	case errors.Is(err, ErrNotLeader), errors.Is(err, ErrStoreUnavailable):
		return sdk.EvidenceFaultLedgerUnavailable
	default:
		return sdk.EvidenceFaultWriteError
	}
}

// EvidenceEpochFence is the BeforeEffect fencing check a consumer runs
// IMMEDIATELY before emitting the external effect of a claimed operation: it
// durably verifies (EpochFencer.FencedEpoch — held lock session + persisted
// epoch/holder on Postgres; process liveness on single-node) that this node
// still leads AND that the cluster epoch equals the claim's stored LeaderEpoch.
// Any error means REFUSE the dispatch: the claim stays 'claimed' and is never
// re-dispatched (an operator/consumer decision settles it). An elector without
// the EpochFencer capability fails closed — the in-memory Active()/Epoch()
// pair is deliberately NOT an accepted fallback (review P1: it lags
// durable reality by up to a poll tick, or indefinitely on a paused process).
//
// Placement decision: the fence lives at the store layer because
// LeaderElector/EpochFencer are defined here; the consumer owns WHEN to call it
// (the store cannot know the moment of dispatch). HONEST RESIDUAL: even a
// durable fence leaves the check-to-effect window — the node can be paused
// BETWEEN a passing fence and the network write of the effect. Closing that
// window requires receiver-side fencing at the upstream (the receiver rejects
// a stale epoch presented with the effect); until the dispatch protocol carries
// the epoch, this fence bounds the window, it does not eliminate it.
func EvidenceEpochFence(ctx context.Context, le LeaderElector, claimEpoch uint64) error {
	if le == nil {
		return fmt.Errorf("evidence epoch fence: no leader elector (fail closed)")
	}
	f, ok := le.(EpochFencer)
	if !ok {
		return fmt.Errorf("evidence epoch fence: elector %T lacks the durable fence capability (fail closed)", le)
	}
	current, err := f.FencedEpoch(ctx)
	if err != nil {
		return fmt.Errorf("evidence epoch fence: %w", err)
	}
	if current != claimEpoch {
		return fmt.Errorf("evidence epoch fence: cluster epoch moved (claim %d, current %d): leadership changed since the claim; refuse the effect", claimEpoch, current)
	}
	return nil
}

// blankEvidenceField reports an empty or whitespace-only required field — the
// same TrimSpace semantics the sdk applies to binding identities
// (sdk/evidence.go EvidenceBinding.Valid), so " " never passes validation and
// then commits store I/O (review P1-3).
func blankEvidenceField(s string) bool { return strings.TrimSpace(s) == "" }

// ValidateEvidenceClaim checks that c names a complete claim (fail closed: an
// incomplete binding never enters the journal; whitespace-only values are as
// invalid as empty ones). The store implementation calls it inside Claim; the
// driver calls it BEFORE opening any transaction. Failures wrap
// ErrEvidenceInvalid.
func ValidateEvidenceClaim(c EvidenceClaim) error {
	switch {
	case blankEvidenceField(c.OperationID):
		return fmt.Errorf("%w: claim: operation id required", ErrEvidenceInvalid)
	case blankEvidenceField(c.EffectDigest):
		return fmt.Errorf("%w: claim: effect digest required", ErrEvidenceInvalid)
	case blankEvidenceField(c.Surface):
		return fmt.Errorf("%w: claim: surface required", ErrEvidenceInvalid)
	case blankEvidenceField(c.Action):
		return fmt.Errorf("%w: claim: action required", ErrEvidenceInvalid)
	case blankEvidenceField(c.Actor) || blankEvidenceField(c.ActorKind):
		return fmt.Errorf("%w: claim: actor attribution required", ErrEvidenceInvalid)
	}
	return nil
}

// ValidateEvidenceSettlement checks that s names a complete, terminal
// settlement (TrimSpace semantics, like ValidateEvidenceClaim). Failures wrap
// ErrEvidenceInvalid.
func ValidateEvidenceSettlement(s EvidenceSettlement) error {
	switch {
	case blankEvidenceField(s.OperationID):
		return fmt.Errorf("%w: settlement: operation id required", ErrEvidenceInvalid)
	case blankEvidenceField(s.EffectDigest):
		return fmt.Errorf("%w: settlement: effect digest required", ErrEvidenceInvalid)
	case !s.State.Terminal():
		return fmt.Errorf("%w: settlement: state %q is not a terminal settlement state", ErrEvidenceInvalid, s.State)
	case blankEvidenceField(s.Actor) || blankEvidenceField(s.ActorKind):
		return fmt.Errorf("%w: settlement: actor attribution required", ErrEvidenceInvalid)
	}
	return nil
}
