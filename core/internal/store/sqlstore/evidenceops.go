// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// evidenceops.go (q1) — the sqlstore implementation of the durable
// evidence operation journal (store.EvidenceOperationRepo). See the descriptor
// below for the schema and core/store/evidenceops.go for the contract.

// evidenceOpDescriptor is the journal's schema. Like every core descriptor it
// is the single source of truth: a fresh database creates the table in the v2
// "core_entities" migration, an existing one gets it created whole by
// reconcileColumns (the additive schema-reconciliation path — schema.go). The
// UNIQUE(tenant_id, operation_id) index is the ground truth of the single-use
// claim: whatever races past the in-transaction read is stopped here.
// Refs and digests only — no raw parameters, result bodies, bearer material or
// reason text (docs/SECURITY-HARDENING.md).
var evidenceOpDescriptor = model.EntityDescriptor{
	Kind:  "core.evidence_operation",
	Table: "evidence_operations",
	Fields: []model.FieldSpec{
		field("operation_id", model.KindText, false),
		field("effect_digest", model.KindText, false),
		field("surface", model.KindText, false),
		field("action", model.KindText, false),
		// state is indexed so an operator sweep for stuck 'claimed' operations
		// (crash between claim and settle) is one index scan.
		indexedField("state", model.KindText, false),
		field("claim_evidence_ref", model.KindText, false),
		field("outcome_evidence_ref", model.KindText, true),
		field("result_digest", model.KindText, true),
		field("dispatch_ref", model.KindText, true),
		field("leader_epoch", model.KindInt, false),
	},
	Indexes: []model.IndexSpec{{
		Name:    "evidence_operations_op_uniq",
		Columns: []string{"tenant_id", "operation_id"},
		Unique:  true,
	}},
	// Database-level backstop for the lifecycle vocabulary (review P2):
	// even a raw out-of-band write cannot plant an unknown state. Built from the
	// model constants so the constraint can never drift from the Go vocabulary.
	// This CHECK is emitted at CREATE TABLE (fresh-v2 and reconcile-create), so
	// a database created BEFORE a vocabulary word existed still carries the old
	// list: 'withheld' (stage-7 B-bis) is widened onto an existing table by
	// reconcileEvidenceOpStateCheck (Postgres; SQLite cannot ALTER a CHECK and
	// fails CLOSED — a withheld settlement refuses, the response is withheld
	// anyway, and the operation stays claimed until the table is rebuilt).
	Checks: []string{fmt.Sprintf("state IN ('%s','%s','%s','%s','%s','%s')",
		model.EvidenceOpClaimed, model.EvidenceOpCompleted, model.EvidenceOpNotSent,
		model.EvidenceOpUnknown, model.EvidenceOpBlocked, model.EvidenceOpWithheld)},
}

var evidenceOpCodec = model.Codec[model.EvidenceOperation]{
	Base: func(o *model.EvidenceOperation) *model.BaseFields { return &o.BaseFields },
	Encode: func(o model.EvidenceOperation) (model.Record, error) {
		return model.Record{
			"operation_id": o.OperationID, "effect_digest": o.EffectDigest,
			"surface": o.Surface, "action": o.Action, "state": string(o.State),
			"claim_evidence_ref":   o.ClaimEvidenceRef,
			"outcome_evidence_ref": encOptStr(o.OutcomeEvidenceRef),
			"result_digest":        encOptStr(o.ResultDigest),
			"dispatch_ref":         encOptStr(o.DispatchRef),
			// The elector epoch is a small monotonic counter; the int64 column
			// cannot overflow it in practice.
			"leader_epoch": int64(o.LeaderEpoch),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.EvidenceOperation, error) {
		op := model.EvidenceOperation{
			BaseFields:  b,
			OperationID: r.String("operation_id"), EffectDigest: r.String("effect_digest"),
			Surface: r.String("surface"), Action: r.String("action"),
			State:              model.EvidenceOperationState(r.String("state")),
			ClaimEvidenceRef:   r.String("claim_evidence_ref"),
			OutcomeEvidenceRef: r.String("outcome_evidence_ref"),
			ResultDigest:       r.String("result_digest"),
			DispatchRef:        r.String("dispatch_ref"),
			LeaderEpoch:        uint64(r.Int("leader_epoch")),
		}
		if err := validateEvidenceOpRow(op); err != nil {
			return model.EvidenceOperation{}, err
		}
		return op, nil
	},
}

// validateEvidenceOpRow enforces the journal's lifecycle invariants on every
// decode (review P2) — defense in depth beneath the state CHECK
// constraint, and the only enforcement point for the cross-column invariants a
// portable CHECK does not cover:
//
//   - the state must be one of the six known values (a legacy pre-CHECK table
//     decodes through this same codec);
//   - a 'claimed' row must carry NO outcome evidence ref (its settlement never
//     happened);
//   - a terminal row must carry one (a terminal state without its outcome
//     anchor is a corrupt/forged settlement and must never replay as settled).
//
// Violations wrap store.ErrEvidenceIntegrity so callers classify them exactly
// like any other journal contradiction.
func validateEvidenceOpRow(op model.EvidenceOperation) error {
	if !op.State.Valid() {
		return fmt.Errorf("%w: operation %s row carries unknown state %q",
			store.ErrEvidenceIntegrity, op.OperationID, op.State)
	}
	outcomeRef := strings.TrimSpace(op.OutcomeEvidenceRef)
	if op.State == model.EvidenceOpClaimed && outcomeRef != "" {
		return fmt.Errorf("%w: operation %s is claimed but carries an outcome evidence ref",
			store.ErrEvidenceIntegrity, op.OperationID)
	}
	if op.State.Terminal() && outcomeRef == "" {
		return fmt.Errorf("%w: operation %s is settled %s without an outcome evidence ref",
			store.ErrEvidenceIntegrity, op.OperationID, op.State)
	}
	return nil
}

// evidenceOpsRepo implements store.EvidenceOperationRepo over the tenant-pinned
// genericRepo and the scope's shared audit log (one chain head per transaction).
type evidenceOpsRepo struct {
	g     *genericRepo
	audit *auditLog
}

func newEvidenceOpsRepo(g *genericRepo, audit *auditLog) *evidenceOpsRepo {
	return &evidenceOpsRepo{g: g, audit: audit}
}

// lookup reads the tenant's journal row for operationID, decoded. Read-side
// backend-availability failures are wrapped like the write side (mapWriteErr),
// so an unreachable database classifies ledger_unavailable, not write_error.
func (r *evidenceOpsRepo) lookup(ctx context.Context, operationID string) (model.EvidenceOperation, bool, error) {
	recs, _, err := r.g.List(ctx, model.Query{
		Filters: []model.Filter{{Column: "operation_id", Op: model.OpEq, Value: operationID}},
		Limit:   1,
	})
	if err != nil {
		return model.EvidenceOperation{}, false, wrapUnavailableErr(err)
	}
	if len(recs) == 0 {
		return model.EvidenceOperation{}, false, nil
	}
	op, err := decodeEvidenceOp(recs[0])
	return op, err == nil, err
}

func decodeEvidenceOp(rec model.Record) (model.EvidenceOperation, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.EvidenceOperation{}, err
	}
	return evidenceOpCodec.Decode(base, rec)
}

// Get returns the journal row for operationID, or ErrNotFound.
func (r *evidenceOpsRepo) Get(ctx context.Context, operationID string) (model.EvidenceOperation, error) {
	op, ok, err := r.lookup(ctx, operationID)
	if err != nil {
		return model.EvidenceOperation{}, err
	}
	if !ok {
		return model.EvidenceOperation{}, store.ErrNotFound
	}
	return op, nil
}

// Claim implements the single-Mutate claim semantics (an internal design note (not shipped)
// anchoring discipline — see store.EvidenceOperationRepo):
//
//	(a) read by (tenant, operation_id);
//	(b) exists + exact digest ⇒ replay: return the recorded row, append NOTHING;
//	(c) exists + different digest ⇒ ErrEvidenceRebind (⇒ sdk.FailureReplay);
//	(d) absent ⇒ append the claim evidence event AND insert the 'claimed' row
//	    in the caller's SAME transaction;
//	(e) Seq==0 degrade drop ⇒ stage NO row, report Dropped, return nil so the
//	    loss accounting the store already staged COMMITS (the sentinel-inside-tx
//	    rollback bug this discipline abolishes — sdk/evidence.go:63-68);
//	(f) a concurrent duplicate that slips past (a) hits the
//	    UNIQUE(tenant_id, operation_id) insert (mapWriteErr ⇒ ErrConflict) and
//	    returns ErrEvidenceRaced: the caller's Mutate rolls the WHOLE losing
//	    transaction back — its evidence append included — and a re-run re-reads
//	    the committed winner at (b). On SQLite the single-writer connection
//	    serializes transactions so (f) is unreachable and losers take (b)
//	    directly; on Postgres two interleaved read-miss/insert transactions
//	    genuinely reach it (the second insert waits on the unique index until
//	    the winner commits, then errors).
func (r *evidenceOpsRepo) Claim(ctx context.Context, c store.EvidenceClaim) (store.EvidenceClaimResult, error) {
	var zero store.EvidenceClaimResult
	if r.g.readOnly {
		return zero, store.ErrReadOnly
	}
	if err := store.ValidateEvidenceClaim(c); err != nil {
		return zero, err
	}
	prior, ok, err := r.lookup(ctx, c.OperationID)
	if err != nil {
		return zero, err
	}
	if ok {
		if prior.EffectDigest != c.EffectDigest {
			return zero, fmt.Errorf("%w: operation %s", store.ErrEvidenceRebind, c.OperationID)
		}
		return store.EvidenceClaimResult{Op: prior}, nil // exact replay, Fresh=false
	}

	ev, err := r.audit.Append(ctx, model.AuditDraft{
		Actor:      c.Actor,
		ActorKind:  c.ActorKind,
		Action:     c.Action + ".claim",
		TargetKind: evidenceOpDescriptor.Kind,
		TargetID:   model.ID(c.OperationID),
		Meta: map[string]any{
			"operation_id": c.OperationID, "effect_digest": c.EffectDigest,
			"surface": c.Surface, "action": c.Action,
		},
	})
	if err != nil {
		return zero, err // block-mode spool-full / write fault ⇒ rollback, deny-closed
	}
	if ev.Seq == 0 {
		return store.EvidenceClaimResult{Dropped: true}, nil // (e): commit the gap, refuse after
	}

	op := model.EvidenceOperation{
		OperationID: c.OperationID, EffectDigest: c.EffectDigest,
		Surface: c.Surface, Action: c.Action,
		State:            model.EvidenceOpClaimed,
		ClaimEvidenceRef: hex.EncodeToString(ev.Hash),
		LeaderEpoch:      c.LeaderEpoch,
	}
	rec, err := evidenceOpCodec.Encode(op)
	if err != nil {
		return zero, err
	}
	created, err := r.g.Create(ctx, rec)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return zero, fmt.Errorf("%w: operation %s", store.ErrEvidenceRaced, c.OperationID)
		}
		return zero, err
	}
	out, err := decodeEvidenceOp(created)
	if err != nil {
		return zero, err
	}
	return store.EvidenceClaimResult{Op: out, Fresh: true}, nil
}

// Settle records the terminal outcome atomically with its evidence event,
// mirroring modules/orchestration/operation.go settleOperation's stance on a
// missing claim row (an INTEGRITY error — the transaction must roll back, never
// record a settlement against a claim that vanished). Re-settles: ONLY an exact
// {State, ResultDigest, DispatchRef} match against a row whose recorded
// OutcomeEvidenceRef is intact replays idempotently (nothing appended) — a
// divergent dispatch ref is the signature of a double dispatch and refuses
// (review P1-2); anything else is an integrity error. The same F9 degrade
// discipline as Claim applies to the outcome append: on Seq==0 the row STAYS
// 'claimed' (only the loss accounting commits) — ambiguous but safe, because a
// claimed operation is never re-dispatched.
func (r *evidenceOpsRepo) Settle(ctx context.Context, s store.EvidenceSettlement) (store.EvidenceSettleResult, error) {
	var zero store.EvidenceSettleResult
	if r.g.readOnly {
		return zero, store.ErrReadOnly
	}
	if err := store.ValidateEvidenceSettlement(s); err != nil {
		return zero, err
	}
	op, ok, err := r.lookup(ctx, s.OperationID)
	if err != nil {
		return zero, err
	}
	if !ok {
		return zero, fmt.Errorf("%w: settle: operation %s claim row missing", store.ErrEvidenceIntegrity, s.OperationID)
	}
	if op.EffectDigest != s.EffectDigest {
		return zero, fmt.Errorf("%w: settle: operation %s", store.ErrEvidenceRebind, s.OperationID)
	}
	if op.State != model.EvidenceOpClaimed {
		if op.State == s.State && op.ResultDigest == s.ResultDigest && op.DispatchRef == s.DispatchRef {
			// Belt and braces below the decode invariant: never replay a
			// "settled" row that lacks its outcome anchor.
			if strings.TrimSpace(op.OutcomeEvidenceRef) == "" {
				return zero, fmt.Errorf("%w: settle: operation %s settled without an outcome evidence ref",
					store.ErrEvidenceIntegrity, s.OperationID)
			}
			return store.EvidenceSettleResult{Op: op}, nil // exact idempotent re-settle, Fresh=false
		}
		return zero, fmt.Errorf(
			"%w: settle: operation %s already settled %s/%s/%s (requested %s/%s/%s)",
			store.ErrEvidenceIntegrity, s.OperationID,
			op.State, op.ResultDigest, op.DispatchRef, s.State, s.ResultDigest, s.DispatchRef)
	}

	meta := map[string]any{
		"operation_id": s.OperationID, "effect_digest": s.EffectDigest,
		"state": string(s.State),
	}
	if s.ResultDigest != "" {
		meta["result_digest"] = s.ResultDigest
	}
	if s.DispatchRef != "" {
		meta["dispatch_ref"] = s.DispatchRef
	}
	ev, err := r.audit.Append(ctx, model.AuditDraft{
		Actor:      s.Actor,
		ActorKind:  s.ActorKind,
		Action:     op.Action + ".settle",
		TargetKind: evidenceOpDescriptor.Kind,
		TargetID:   model.ID(s.OperationID),
		Meta:       meta,
	})
	if err != nil {
		return zero, err
	}
	if ev.Seq == 0 {
		return store.EvidenceSettleResult{Dropped: true}, nil // row stays 'claimed'; gap commits
	}

	op.State = s.State
	op.OutcomeEvidenceRef = hex.EncodeToString(ev.Hash)
	op.ResultDigest = s.ResultDigest
	op.DispatchRef = s.DispatchRef
	rec, err := evidenceOpCodec.Encode(op)
	if err != nil {
		return zero, err
	}
	rec[model.ColID] = op.ID.String()
	rec[model.ColVersion] = op.Version
	updated, err := r.g.Update(ctx, rec)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// A concurrent settle won the optimistic-concurrency CAS; the caller
			// rolls back (this append included) and re-reads, resolving to the
			// idempotent-or-integrity branch above.
			return zero, fmt.Errorf("%w: operation %s (settle)", store.ErrEvidenceRaced, s.OperationID)
		}
		return zero, err
	}
	out, err := decodeEvidenceOp(updated)
	if err != nil {
		return zero, err
	}
	return store.EvidenceSettleResult{Op: out, Fresh: true}, nil
}
