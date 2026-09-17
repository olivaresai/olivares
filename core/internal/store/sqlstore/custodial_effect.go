// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// custodial_effect.go (P2 / W1) — the sqlstore half of store.CustodialEffectClaimer.
//
// The custodial order differs from the generic journal in ONE deliberate way,
// and everything else here exists to make that difference safe:
//
//	generic Claim: append the claim evidence, THEN insert the claimed row.
//	custodial Claim: insert a provisional REFUSED row, THEN append, THEN promote
//	                 it to claimed under a version CAS.
//
// Inverting the order is what lets a managed Stop burn an operation identity
// that a degrade-mode spool dropped. Under the generic order a Seq==0 drop
// stages nothing but the loss accounting, so the same identity can be presented
// again as if it had never been used; under the custodial order the refused row
// commits with the loss accounting, carries no claim, outcome, result or
// dispatch reference, can never be settled, and answers every later replay.
//
// The inversion also moves the unique-index race EARLIER: a concurrent
// duplicate now loses at the insert, before an audit event has been appended,
// so the loser rolls back without having burned a ledger sequence.
//
// Everything the caller could otherwise substitute is taken from the engine:
// the surface, the action, the relation, the claim row (reached from the run
// proven visible in this transaction), the persisted lineage inside the digest,
// and the leader epoch.

// custodialEffectHandle is one bound operation over the fixed relation. It is
// valid only inside the transaction whose Scope produced it.
type custodialEffectHandle struct {
	sc      *tenantScope
	binding store.CustodialEffectBinding
	rel     custodialRelation
	// digest is the ENGINE's effect digest. It binds the caller's semantic
	// digest to the tenant, the surface, the fixed target kind, the run's key
	// and generation, and the run's PERSISTED authorization lineage — so the
	// same operation ID presented from another workspace computes a different
	// digest and can never reach the first workspace's row state.
	digest string
	// epoch is the durable fence stamped at Bind. The caller never supplies it.
	epoch uint64
	// claimID and claimVersion are the qualified claim row. They are recorded by
	// the engine at Bind and are the ONLY row identity TouchClaim will use.
	claimID      model.ID
	claimVersion int64
	claimHolder  string
	claimFence   int64
	claimSubject string
	// touched and done are the per-handle ordering guards.
	touched bool
	done    bool
}

var _ store.CustodialEffectHandle = (*custodialEffectHandle)(nil)
var _ store.CustodialEffectClaimer = (*tenantScope)(nil)
var _ store.CustodialEffectReadiness = (*sqlStore)(nil)

// maxCustodialOperationID bounds the journal identity. The prefix is 25 bytes
// and the client half is bounded at 128 by the route that produces it, so a
// well-formed identity never exceeds this; the bound is here so a malformed one
// is refused before it reaches a column.
const maxCustodialOperationID = 160

// ManagedStopReadiness reports whether this store can bind a custodial effect
// at all, without opening a transaction. The three unavailability reasons are
// checked in the order an operator can act on them: a schema transition is a
// migration, an invalid relation is a module declaration, and an elector
// without the durable fence is deployment wiring.
func (s *sqlStore) ManagedStopReadiness(ctx context.Context) store.CustodialReadiness {
	if !s.evidenceRefusedSupported {
		return store.CustodialReadiness{
			Reason: store.CustodyUnreadySchema,
			Detail: "the evidence journal has not crossed the controlled refused-state transition",
		}
	}
	if s.custodyRelationErr != nil {
		return store.CustodialReadiness{
			Reason: store.CustodyUnreadyRelation,
			Detail: s.custodyRelationErr.Error(),
		}
	}
	if _, ok := s.elector.(store.EpochFencer); !ok {
		return store.CustodialReadiness{
			Reason: store.CustodyUnreadyLeader,
			Detail: fmt.Sprintf("elector %T does not implement the durable epoch fence", s.elector),
		}
	}
	return store.CustodialReadiness{Ready: true}
}

// readinessError maps a not-ready store to the bind-time refusal. Each reason
// carries BOTH its specific sentinel and ErrCustodyUnavailable: a caller that
// only wants to know "can I do this at all" matches the second, while an
// operator tool that reports WHICH half is missing matches the first.
func custodialReadinessError(r store.CustodialReadiness) error {
	if r.Ready {
		return nil
	}
	var specific error
	switch r.Reason {
	case store.CustodyUnreadySchema:
		specific = store.ErrCustodySchemaUnsupported
	case store.CustodyUnreadyRelation:
		specific = store.ErrCustodyRelationInvalid
	case store.CustodyUnreadyLeader:
		specific = store.ErrCustodyLeaderUnwired
	default:
		specific = store.ErrCustodyUnavailable
	}
	return fmt.Errorf("%w: %w: %s", store.ErrCustodyUnavailable, specific, r.Detail)
}

// BindCustodialEffect implements store.CustodialEffectClaimer.
//
// Order, and why: the caller's own binding shape is checked first, because a
// malformed request must never cause I/O; then the engine's readiness, because
// an unavailable capability must refuse before it touches the transaction's
// state; then the one-binding-per-transaction reservation and the pre-bind
// write set, which are properties of the transaction rather than of the
// request; and only then the target reads, the generation compare, the epoch
// stamp and the claim qualification.
func (sc *tenantScope) BindCustodialEffect(
	ctx context.Context, b store.CustodialEffectBinding,
) (store.CustodialEffectHandle, error) {
	if err := validateCustodialBinding(b); err != nil {
		return nil, err
	}
	if b.Mode != store.CustodialLookup && sc.readOnly {
		return nil, store.ErrReadOnly
	}
	if err := custodialReadinessError(sc.s.ManagedStopReadiness(ctx)); err != nil {
		return nil, err
	}
	if err := sc.reserveCustodyBinding(b.Mode); err != nil {
		return nil, err
	}
	h := &custodialEffectHandle{sc: sc, binding: b, rel: sc.s.custodyRelation}

	run, err := h.readTarget(ctx)
	if err != nil {
		return nil, err
	}
	lineage := run.String(custodyRunLineageColumn)
	if b.Mode == store.CustodialClaim {
		generation := run.String(custodyRunLaunchColumn)
		if generation == "" || generation != b.RunLaunchID.String() {
			return nil, sc.poisonCustody(fmt.Errorf(
				"%w: run %s carries launch %q, not the expected one",
				store.ErrCustodyTargetGeneration, b.RunRef, generation))
		}
	}
	// The epoch is stamped from the store's OWN elector. A fence that cannot be
	// taken right now refuses before anything is written and does not poison:
	// nothing was staged, so the caller may retire, refuse and commit whatever
	// it had already decided.
	fencer, ok := sc.s.elector.(store.EpochFencer)
	if !ok {
		return nil, fmt.Errorf("%w: %w", store.ErrCustodyUnavailable, store.ErrCustodyLeaderUnwired)
	}
	epoch, err := fencer.FencedEpoch(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", store.ErrCustodyLeaderUnavailable, err)
	}
	h.epoch = epoch
	if b.Mode == store.CustodialClaim {
		if err := h.qualifyClaim(ctx, run); err != nil {
			return nil, err
		}
	}
	h.digest = custodialEffectDigest(
		sc.tenant, custodyRunKind, b.RunRef, b.RunLaunchID.String(), lineage, b.SemanticDigest)
	return h, nil
}

// validateCustodialBinding refuses a malformed request before any I/O. It is a
// caller bug, so it neither poisons nor consumes the transaction's single
// binding.
func validateCustodialBinding(b store.CustodialEffectBinding) error {
	switch {
	case !b.Mode.Valid():
		return fmt.Errorf("%w: unknown mode", store.ErrCustodyBindingInvalid)
	case strings.TrimSpace(b.OperationID) != b.OperationID || b.OperationID == "":
		return fmt.Errorf("%w: operation id required", store.ErrCustodyBindingInvalid)
	case !strings.HasPrefix(b.OperationID, store.ManagedStopOperationPrefix):
		return fmt.Errorf("%w: operation id does not carry the reserved surface prefix",
			store.ErrCustodyBindingInvalid)
	case len(b.OperationID) <= len(store.ManagedStopOperationPrefix):
		return fmt.Errorf("%w: operation id carries the prefix and nothing else",
			store.ErrCustodyBindingInvalid)
	case len(b.OperationID) > maxCustodialOperationID:
		return fmt.Errorf("%w: operation id exceeds %d bytes",
			store.ErrCustodyBindingInvalid, maxCustodialOperationID)
	case strings.TrimSpace(b.SemanticDigest) == "":
		return fmt.Errorf("%w: semantic digest required", store.ErrCustodyBindingInvalid)
	case strings.TrimSpace(b.RunRef) == "":
		return fmt.Errorf("%w: run reference required", store.ErrCustodyBindingInvalid)
	case strings.TrimSpace(b.Actor) == "" || strings.TrimSpace(b.ActorKind) == "":
		return fmt.Errorf("%w: actor attribution required", store.ErrCustodyBindingInvalid)
	}
	if b.Mode == store.CustodialClaim {
		if b.RunLaunchID.IsZero() {
			return fmt.Errorf("%w: claim requires the expected launch generation",
				store.ErrCustodyBindingInvalid)
		}
		if b.ClaimVersion < 1 {
			return fmt.Errorf("%w: claim requires the observed claim version",
				store.ErrCustodyBindingInvalid)
		}
	}
	return nil
}

// readTarget reads the run by its natural key THROUGH THIS SCOPE'S OWN
// CONFINEMENT. Absent, foreign-tenant and out-of-boundary are one answer: the
// three are indistinguishable to the caller on purpose, because telling them
// apart is an existence probe.
func (h *custodialEffectHandle) readTarget(ctx context.Context) (model.Record, error) {
	filters := []model.Filter{{Column: custodyRunRefColumn, Op: model.OpEq, Value: h.binding.RunRef}}
	lineageFilter, confined, err := h.binding.WorkspaceConstraint(h.rel.run)
	if err != nil {
		// A confined binding over a descriptor without lineage cannot be
		// answered. The relation check makes this unreachable while the store is
		// ready; it stays because "unreachable" is a property of today's
		// descriptors, not of the type.
		return nil, err
	}
	if confined {
		filters = append(filters, lineageFilter)
	}
	rows, _, err := h.sc.repo(h.rel.run).List(ctx, model.Query{Filters: filters, Limit: 2})
	if err != nil {
		return nil, wrapUnavailableErr(err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: run %s", store.ErrCustodyTargetConcealed, h.binding.RunRef)
	}
	if len(rows) > 1 {
		// The relation check requires a unique index on (tenant_id, run_ref), so
		// two rows mean the index is gone from the live database. That is damage
		// to report, never a choice to make.
		return nil, h.sc.poisonCustody(fmt.Errorf(
			"%w: run %s resolves to more than one row",
			store.ErrCustodyTargetConcealed, h.binding.RunRef))
	}
	run := rows[0]
	// Belt and braces beneath the forced predicate: a lineage value the declared
	// encoding cannot read is a fault, and a faulty row is denied rather than
	// admitted. inside is already guaranteed by the filter on every engine; ok
	// is the half a predicate cannot express.
	if inside, readable := h.binding.WorkspaceOwns(h.rel.run, run.String(custodyRunLineageColumn)); !readable || !inside {
		return nil, fmt.Errorf("%w: run %s lineage is not inside this scope's boundary",
			store.ErrCustodyTargetConcealed, h.binding.RunRef)
	}
	return run, nil
}

// qualifyClaim performs the FIXED claim qualification of correction 2 §5.5.
//
// The claim row is reached from the run proven visible above — never from a
// caller-supplied row id — so a different valid claim row cannot be selected
// even by a caller that knows its id and version. A version that is accurate
// for ANOTHER row is therefore a mismatch here, not a substitution.
func (h *custodialEffectHandle) qualifyClaim(ctx context.Context, run model.Record) error {
	subject := run.String(custodyRunClaimSubjectCol)
	holder := run.String(custodyRunClaimHolderCol)
	fence := run.Int(custodyRunClaimFenceCol)
	if subject == "" || holder == "" {
		return h.sc.poisonCustody(fmt.Errorf(
			"%w: run %s carries no admission stamp", store.ErrCustodyClaimMismatch, h.binding.RunRef))
	}
	// The claim relation carries no workspace lineage and is read through the
	// internal tenant repository. That is not a widening: the subject came from
	// a row this scope's own confinement already admitted, so the read cannot
	// reach a claim the caller could not already name.
	rows, _, err := h.sc.repo(h.rel.claim).List(ctx, model.Query{
		Filters: []model.Filter{{Column: custodyClaimSubjectColumn, Op: model.OpEq, Value: subject}},
		Limit:   2,
	})
	if err != nil {
		return wrapUnavailableErr(err)
	}
	if len(rows) != 1 {
		return h.sc.poisonCustody(fmt.Errorf(
			"%w: subject %s resolves to %d claim rows, want exactly one",
			store.ErrCustodyClaimMismatch, subject, len(rows)))
	}
	claim := rows[0]
	now, err := h.sc.TransactionNow(ctx)
	if err != nil {
		return err
	}
	deadlineRaw := claim.String(h.rel.lease.DeadlineColumn)
	deadline, perr := model.ParseTimestamp(deadlineRaw)
	if perr != nil {
		return h.sc.poisonCustody(fmt.Errorf(
			"%w: subject %s carries an unreadable lease deadline", store.ErrCustodyClaimMismatch, subject))
	}
	switch {
	case claim.String(h.rel.lease.SubjectColumn) != subject:
		return h.sc.poisonCustody(claimMismatch(subject, "subject"))
	case claim.String(custodyClaimHolderColumn) != holder:
		return h.sc.poisonCustody(claimMismatch(subject, "holder"))
	case claim.Int(h.rel.lease.FenceColumn) != fence:
		return h.sc.poisonCustody(claimMismatch(subject, "fence"))
	case claim.String(h.rel.lease.StateColumn) != h.rel.lease.ActiveValue:
		return h.sc.poisonCustody(claimMismatch(subject, "state"))
	case !now.Before(deadline):
		return h.sc.poisonCustody(claimMismatch(subject, "lease deadline"))
	case claim.Int(model.ColVersion) != h.binding.ClaimVersion:
		return h.sc.poisonCustody(claimMismatch(subject, "version"))
	}
	h.claimID = model.ID(claim.String(model.ColID))
	h.claimVersion = claim.Int(model.ColVersion)
	h.claimHolder = holder
	h.claimFence = fence
	h.claimSubject = subject
	if h.claimID.IsZero() {
		return h.sc.poisonCustody(claimMismatch(subject, "row identity"))
	}
	return nil
}

// claimMismatch names WHICH coordinate disqualified the claim. The subject is a
// session reference, not a secret; no holder value, fence or deadline is
// echoed, so the message cannot be used to read the row it refused.
func claimMismatch(subject, coordinate string) error {
	return fmt.Errorf("%w: subject %s: %s does not qualify", store.ErrCustodyClaimMismatch, subject, coordinate)
}

// custodialEffectDigest is the ENGINE's effect digest, v1.
//
// Framing: the ASCII domain and one NUL byte, then seven fields, each a
// one-byte tag, an unsigned 64-bit big-endian byte length and its UTF-8 bytes.
// No field is omitted and an empty field has length zero, so no two distinct
// field vectors share a preimage.
//
// lineageValue is the PERSISTED authorization workspace read from the target
// row in this transaction, never a caller value. That is what makes the digest
// a confinement fact: a handle bound in workspace B, presenting workspace A's
// operation id and A's semantic digest, computes a different digest and gets a
// rebind refusal instead of reaching A's row state.
func custodialEffectDigest(
	tenant model.TenantID, targetKind model.Kind, targetKey, targetGeneration, lineageValue, semanticDigest string,
) string {
	h := sha256.New()
	h.Write([]byte("olivares.store.custodial-effect.v1"))
	h.Write([]byte{0x00})
	for i, field := range []string{
		tenant.String(),
		store.ManagedStopSurface,
		string(targetKind),
		targetKey,
		targetGeneration,
		lineageValue,
		semanticDigest,
	} {
		var framing [9]byte
		framing[0] = byte(i + 1)
		binary.BigEndian.PutUint64(framing[1:], uint64(len(field)))
		h.Write(framing[:])
		h.Write([]byte(field))
	}
	return "cev1:" + hex.EncodeToString(h.Sum(nil))
}

// ---- handle operations -------------------------------------------------

// Lookup decodes the bound row. It writes nothing. A row recorded under a
// DIFFERENT engine digest is a rebind and is refused rather than presented: the
// operation identity is bound to one effect, and returning another effect's
// state under it is exactly the substitution the digest exists to stop.
func (h *custodialEffectHandle) Lookup(ctx context.Context) (model.EvidenceOperation, bool, error) {
	if err := h.usable(); err != nil {
		return model.EvidenceOperation{}, false, err
	}
	op, ok, err := h.readJournalRow(ctx)
	if err != nil || !ok {
		return model.EvidenceOperation{}, false, err
	}
	if op.EffectDigest != h.digest {
		return model.EvidenceOperation{}, false, h.rebind()
	}
	return op, true, nil
}

// TouchClaim advances the QUALIFIED claim row's version and updated_at and
// assigns no other column, so value preservation holds by construction rather
// than by comparing a before and after image.
//
// Every value in the predicate is one the engine qualified at Bind. The caller
// supplies none of them, which is why an accurate id and version for another
// row cannot select it.
func (h *custodialEffectHandle) TouchClaim(ctx context.Context) error {
	if err := h.usable(); err != nil {
		return err
	}
	if h.binding.Mode != store.CustodialClaim {
		return fmt.Errorf("%w: TouchClaim requires a claim binding", store.ErrCustodyHandleStale)
	}
	if h.touched {
		return h.sc.poisonCustody(fmt.Errorf("%w: TouchClaim runs at most once", store.ErrCustodyAlreadyClaimed))
	}
	h.touched = true
	now, err := h.sc.TransactionNow(ctx)
	if err != nil {
		return err
	}
	repo := h.sc.custodyRepo(h.rel.claim)
	if err := repo.noteWrite(h.claimID); err != nil {
		return err
	}
	// The statement is built here rather than through updateAt because the SET
	// list must be exactly these two assignments: a generic update writes every
	// declared field back, which would make value preservation a property of the
	// caller's record instead of a property of the SQL.
	q := h.sc.s.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET %s = ?, %s = %s + 1"+
			" WHERE %s = ? AND %s = ? AND %s = ? AND %s = ? AND %s = ? AND %s = ? AND %s = ?",
		repo.relation(),
		model.ColUpdatedAt, model.ColVersion, model.ColVersion,
		model.ColTenantID, model.ColID, model.ColVersion,
		custodyClaimSubjectColumn, custodyClaimHolderColumn,
		custodyClaimFenceColumn, custodyClaimStateColumn))
	res, err := h.sc.tx.ExecContext(ctx, q,
		now.String(),
		h.sc.tenant.String(), h.claimID.String(), h.claimVersion,
		h.claimSubject, h.claimHolder, h.claimFence, h.rel.lease.ActiveValue)
	if err != nil {
		return h.sc.poisonCustody(mapWriteErr(err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return h.sc.poisonCustody(err)
	}
	if n != 1 {
		return h.sc.poisonCustody(fmt.Errorf(
			"%w: the qualified claim row changed under the custodial touch", store.ErrConflict))
	}
	h.claimVersion++
	return nil
}

// Claim performs the custodial order. It SEALS the scope on every exit, so a
// caller cannot write after the operation identity has been decided — with a
// result or with an error.
func (h *custodialEffectHandle) Claim(ctx context.Context) (store.CustodialClaimResult, error) {
	var zero store.CustodialClaimResult
	if err := h.usable(); err != nil {
		return zero, err
	}
	if h.binding.Mode != store.CustodialClaim {
		return zero, fmt.Errorf("%w: Claim requires a claim binding", store.ErrCustodyHandleStale)
	}
	h.done = true
	defer h.sc.sealCustody()

	// 1. Lookup. A recorded row under another digest is a rebind, refused with
	//    no write. A recorded row under THIS digest is a replay of whatever was
	//    decided before, also with no write.
	prior, ok, err := h.readJournalRow(ctx)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	if !ok && h.sc.s.evidenceClaimAfterMissTestHook != nil {
		// The SAME read-miss boundary the generic journal exposes, and the only
		// point at which the provisional insert can be made to lose.
		//
		// It is NOT reachable from a second ordinary transaction on this tenant:
		// every Mutate arms the lineage writer for its tenant first, and that
		// gate is exclusive — the single writer on SQLite, a transaction-scoped
		// advisory lock on PostgreSQL — so a competitor cannot even begin until
		// this one commits, and then it replays. The conflict branch below is a
		// backstop for a writer OUTSIDE the transaction protocol, which is
		// exactly what this seam lets a test be.
		if err := h.sc.s.evidenceClaimAfterMissTestHook(ctx, h.binding.OperationID); err != nil {
			return zero, h.sc.poisonCustody(err)
		}
	}
	if ok {
		if prior.EffectDigest != h.digest {
			return zero, h.rebind()
		}
		switch prior.State {
		case model.EvidenceOpRefused:
			return store.CustodialClaimResult{Outcome: store.CustodialReplayRefused, Op: prior}, nil
		case model.EvidenceOpClaimed:
			return store.CustodialClaimResult{Outcome: store.CustodialReplayClaimed, Op: prior}, nil
		default:
			return store.CustodialClaimResult{Outcome: store.CustodialReplaySettled, Op: prior}, nil
		}
	}

	// 2. Provisional refused row, inserted BEFORE the append. Its blank claim
	//    reference is what keeps every generic consumer deny-closed on it.
	repo := h.sc.custodyRepo(evidenceOpDescriptor)
	rec, err := evidenceOpCodec.Encode(model.EvidenceOperation{
		OperationID: h.binding.OperationID, EffectDigest: h.digest,
		Surface: store.ManagedStopSurface, Action: store.ManagedStopAction,
		State:       model.EvidenceOpRefused,
		LeaderEpoch: h.epoch,
	})
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// The duplicate lost the unique index BEFORE this side appended
			// evidence, so the loser rolls back without having burned a ledger
			// sequence. Inverting the order is what moves the race here.
			return zero, h.sc.poisonCustody(fmt.Errorf(
				"%w: operation %s", store.ErrEvidenceRaced, h.binding.OperationID))
		}
		return zero, h.sc.poisonCustody(err)
	}
	provisional, err := decodeEvidenceOp(created)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}

	// 3. One append, attributed to the custodial handle.
	var ev model.AuditEvent
	if err := h.sc.auditLog().withCustodyOrigin(func() error {
		var aerr error
		ev, aerr = h.sc.auditLog().Append(ctx, model.AuditDraft{
			Actor:      h.binding.Actor,
			ActorKind:  h.binding.ActorKind,
			Action:     store.ManagedStopAction + ".claim",
			TargetKind: evidenceOpDescriptor.Kind,
			TargetID:   model.ID(h.binding.OperationID),
			Meta: map[string]any{
				"operation_id": h.binding.OperationID, "effect_digest": h.digest,
				"surface": store.ManagedStopSurface, "action": store.ManagedStopAction,
				"run_ref": h.binding.RunRef, "runtime_launch_id": h.binding.RunLaunchID.String(),
			},
		})
		return aerr
	}); err != nil {
		return zero, h.sc.poisonCustody(err)
	}

	// 4. Seq==0 is the degrade drop. The refused row and the loss accounting
	//    COMMIT: the identity is burned, nothing is dispatched, and no error is
	//    returned, because returning one would roll the loss accounting back.
	if ev.Seq == 0 {
		return store.CustodialClaimResult{Outcome: store.CustodialRefusedFresh, Op: provisional}, nil
	}

	// 5. Promote the SAME row under a version CAS, then re-verify the fence.
	promoted := provisional
	promoted.State = model.EvidenceOpClaimed
	promoted.ClaimEvidenceRef = hex.EncodeToString(ev.Hash)
	updRec, err := evidenceOpCodec.Encode(promoted)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	updRec[model.ColID] = provisional.ID.String()
	updRec[model.ColVersion] = provisional.Version
	updated, err := repo.Update(ctx, updRec)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	out, err := decodeEvidenceOp(updated)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	if err := h.verifyStampedEpoch(ctx, h.epoch); err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	return store.CustodialClaimResult{Outcome: store.CustodialFreshAnchored, Op: out}, nil
}

// Settle records the terminal outcome of a claimed operation, in its own
// transaction under a new binding. It seals the scope on every exit.
//
// There is no generation compare here: by the time an outcome is known the
// live run may have cleared its launch, so the binding's value — the one the
// claim was made under — is a digest input only.
func (h *custodialEffectHandle) Settle(
	ctx context.Context, s store.CustodialSettlement,
) (store.CustodialSettleResult, error) {
	var zero store.CustodialSettleResult
	if err := h.usable(); err != nil {
		return zero, err
	}
	if h.binding.Mode != store.CustodialSettle {
		return zero, fmt.Errorf("%w: Settle requires a settle binding", store.ErrCustodyHandleStale)
	}
	if !s.State.Terminal() {
		return zero, fmt.Errorf("%w: state %q is not a terminal settlement state",
			store.ErrEvidenceInvalid, s.State)
	}
	h.done = true
	defer h.sc.sealCustody()

	op, ok, err := h.readJournalRow(ctx)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	if !ok {
		return zero, h.sc.poisonCustody(fmt.Errorf(
			"%w: settle: operation %s has no row", store.ErrEvidenceIntegrity, h.binding.OperationID))
	}
	if op.EffectDigest != h.digest {
		return zero, h.rebind()
	}
	// A refused operation was never claimed and can never be settled. It is
	// rejected BEFORE the re-settle comparison so no requested state can be read
	// as an idempotent replay of it.
	if op.State == model.EvidenceOpRefused {
		return zero, h.sc.poisonCustody(fmt.Errorf(
			"%w: settle: operation %s was refused and cannot be settled",
			store.ErrEvidenceIntegrity, h.binding.OperationID))
	}
	// The settlement epoch must equal the CLAIM's stamped token. A newer
	// legitimate epoch refuses with NO write and does NOT poison: the row stays
	// claimed, which is the safe non-replayable shape, and adopting a
	// pre-failover claim stays an explicit later operation rather than an
	// implicit side effect of settling.
	if h.epoch != op.LeaderEpoch {
		return zero, fmt.Errorf(
			"%w: operation %s was claimed at epoch %d, this node is fenced at %d",
			store.ErrCustodyEpochChanged, h.binding.OperationID, op.LeaderEpoch, h.epoch)
	}
	if op.State != model.EvidenceOpClaimed {
		if op.State == s.State && op.ResultDigest == s.ResultDigest && op.DispatchRef == s.DispatchRef {
			if strings.TrimSpace(op.OutcomeEvidenceRef) == "" {
				return zero, h.sc.poisonCustody(fmt.Errorf(
					"%w: settle: operation %s is settled without an outcome evidence ref",
					store.ErrEvidenceIntegrity, h.binding.OperationID))
			}
			return store.CustodialSettleResult{Op: op}, nil
		}
		return zero, h.sc.poisonCustody(fmt.Errorf(
			"%w: settle: operation %s is already settled %s (requested %s)",
			store.ErrEvidenceIntegrity, h.binding.OperationID, op.State, s.State))
	}

	meta := map[string]any{
		"operation_id": h.binding.OperationID, "effect_digest": h.digest,
		"state": string(s.State),
	}
	if s.ResultDigest != "" {
		meta["result_digest"] = s.ResultDigest
	}
	if s.DispatchRef != "" {
		meta["dispatch_ref"] = s.DispatchRef
	}
	var ev model.AuditEvent
	if err := h.sc.auditLog().withCustodyOrigin(func() error {
		var aerr error
		ev, aerr = h.sc.auditLog().Append(ctx, model.AuditDraft{
			Actor:      h.binding.Actor,
			ActorKind:  h.binding.ActorKind,
			Action:     store.ManagedStopAction + ".settle",
			TargetKind: evidenceOpDescriptor.Kind,
			TargetID:   model.ID(h.binding.OperationID),
			Meta:       meta,
		})
		return aerr
	}); err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	if ev.Seq == 0 {
		// The row deliberately STAYS claimed; only the loss accounting commits.
		return store.CustodialSettleResult{Dropped: true}, nil
	}

	op.State = s.State
	op.OutcomeEvidenceRef = hex.EncodeToString(ev.Hash)
	op.ResultDigest = s.ResultDigest
	op.DispatchRef = s.DispatchRef
	rec, err := evidenceOpCodec.Encode(op)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	rec[model.ColID] = op.ID.String()
	rec[model.ColVersion] = op.Version
	updated, err := h.sc.custodyRepo(evidenceOpDescriptor).Update(ctx, rec)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	out, err := decodeEvidenceOp(updated)
	if err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	if err := h.verifyStampedEpoch(ctx, op.LeaderEpoch); err != nil {
		return zero, h.sc.poisonCustody(err)
	}
	return store.CustodialSettleResult{Op: out, Fresh: true}, nil
}

// usable is the per-call ordering and liveness guard shared by all four
// operations.
func (h *custodialEffectHandle) usable() error {
	if !h.sc.live() {
		return fmt.Errorf("%w: the transaction has finished", store.ErrCustodyHandleStale)
	}
	if fault := h.sc.custodyFault(); fault != nil {
		return fault
	}
	if h.done {
		return h.sc.poisonCustody(fmt.Errorf(
			"%w: this custodial effect was already decided", store.ErrCustodyAlreadyClaimed))
	}
	return nil
}

// readJournalRow reads the bound operation's row, decoded. A read fault is
// wrapped like the write side so an unreachable database classifies as an
// availability fault rather than a write error.
func (h *custodialEffectHandle) readJournalRow(ctx context.Context) (model.EvidenceOperation, bool, error) {
	rows, _, err := h.sc.repo(evidenceOpDescriptor).List(ctx, model.Query{
		Filters: []model.Filter{{Column: "operation_id", Op: model.OpEq, Value: h.binding.OperationID}},
		Limit:   1,
	})
	if err != nil {
		return model.EvidenceOperation{}, false, wrapUnavailableErr(err)
	}
	if len(rows) == 0 {
		return model.EvidenceOperation{}, false, nil
	}
	op, derr := decodeEvidenceOp(rows[0])
	if derr != nil {
		return model.EvidenceOperation{}, false, derr
	}
	return op, true, nil
}

// rebind refuses a same-identity/different-digest presentation. It poisons in
// the writing modes, where the transaction must not commit whatever else it
// staged, and refuses cleanly in Lookup, which writes nothing.
func (h *custodialEffectHandle) rebind() error {
	err := fmt.Errorf("%w: operation %s", store.ErrEvidenceRebind, h.binding.OperationID)
	if h.binding.Mode == store.CustodialLookup {
		return err
	}
	return h.sc.poisonCustody(err)
}

// verifyStampedEpoch re-runs the DURABLE fence before the callback returns and
// requires it to equal want. A leader that lost its lock session mid-transaction
// therefore rolls the whole staged operation back instead of committing under a
// superseded epoch.
func (h *custodialEffectHandle) verifyStampedEpoch(ctx context.Context, want uint64) error {
	fencer, ok := h.sc.s.elector.(store.EpochFencer)
	if !ok {
		return fmt.Errorf("%w: %w", store.ErrCustodyUnavailable, store.ErrCustodyLeaderUnwired)
	}
	current, err := fencer.FencedEpoch(ctx)
	if err != nil {
		return fmt.Errorf("%w: pre-commit epoch fence: %w", store.ErrNotLeader, err)
	}
	if current != want {
		return fmt.Errorf("%w: pre-commit epoch fence: cluster epoch moved (stamped %d, current %d)",
			store.ErrCustodyEpochChanged, want, current)
	}
	return nil
}
