// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
)

// auditTable is the evidence ledger table name; auditHeadsTable records each
// tenant's current chain tip so Verify can detect tail truncation.
const (
	auditTable           = "audit_events"
	auditHeadsTable      = "audit_heads"
	auditSpoolUsageTable = "audit_spool_usage"
	auditSpoolGapsTable  = "audit_spool_gaps"

	// gapSealEvery bounds in-chain honesty during an unbounded outage: one
	// small signed marker is admitted for every 10k lost events.
	gapSealEvery int64 = 10000
)

// auditColumns is the read column order for the ledger.
var auditColumns = []string{
	"id", "tenant_id", "seq", "occurred_at", "actor", "actor_kind",
	"action", "target_kind", "target_id", "meta", "meta_blind", "payload_hash", "prev_hash", "hash", "sig",
}

// auditLog implements store.AuditLog over the append-only audit_events table for
// one pinned tenant. The chain is per tenant and hash-continuous; sequence holes
// exist only when a signed audit.gap marker declares an explicit degrade
// episode. Append reads the tenant's tail, links the new event, and inserts it
// inside the caller's transaction.
type auditLog struct {
	tx       *sql.Tx
	tenant   model.TenantID
	dia      dialect.Dialect
	clock    model.Clock
	readOnly bool // set in a View scope: Append is rejected
	// signEvent, when non-nil, signs each appended event over its chain hash
	// (store.AuditEventSigner). It is NOT applied to a draft that already carries a
	// signature (a checkpoint), so the checkpoint and per-event signature domains
	// never collide.
	signEvent store.AuditEventSigner
	// spoolMaxBytes is the enabled logical-byte budget; spoolOnFull chooses the
	// deny-closed block policy or the explicit, marker-declared degrade policy.
	spoolMaxBytes int64
	spoolOnFull   store.AuditSpoolMode
	// blindMeta selects the metadata-commitment rule NEW appends seal under. It
	// governs WRITING only — every read resolves the rule from the row's own
	// stored blind — which is what lets a fleet upgrade before it flips, and lets
	// nodes disagree during the flip without breaking the chain.
	blindMeta bool
	// directoryWriter is non-nil only for a mutable tenant scope. It records
	// audit-first ordering before lockTenant so a later directory source write
	// rejects before acquiring the inverse global lock.
	directoryWriter *directoryWriteTracker
}

// relation pins every audit read/write to the engine schema. SQLite resolves
// unqualified names through TEMP first, and PostgreSQL follows search_path;
// neither may redirect the ledger, its head, nor spool accounting.
func (a *auditLog) relation(table string) string {
	return directoryWriterRelation(a.dia, table)
}

// Append seals one event and inserts it. Under the explicit degrade policy, a
// zero-valued event with a nil error means the evidence was dropped; callers
// that require the persisted event must check ev.Seq != 0. Per-tenant appends
// are serialized (a Postgres advisory transaction lock; SQLite is
// single-writer), and a unique(tenant_id, seq) constraint is the ground-truth
// backstop against any race that slips through.
func (a *auditLog) Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	if a.readOnly {
		return model.AuditEvent{}, store.ErrReadOnly
	}
	if a.directoryWriter != nil {
		a.directoryWriter.noteAudit()
	}
	if d.Action == store.ActionAuditGap {
		return model.AuditEvent{}, store.ErrReservedAuditAction
	}
	// Recovery markers are structural, off-box-signed epoch boundaries. A normal
	// caller may name the action only when it carries the detached recovery
	// signature produced by core/audit.RecordRecovery; an unsigned draft is a
	// forged marker and must never enter the immutable ledger.
	if d.Action == store.ActionAuditRecover && len(d.Sig) == 0 {
		return model.AuditEvent{}, store.ErrReservedAuditAction
	}
	// Key-rotation markers are the same shape (F-07): the off-box-signed
	// signing-key epoch boundary produced by core/audit.RecordKeyRotation. Its
	// signature FENCES a retired key, so an unsigned draft naming the action is a
	// forged boundary and must never enter the ledger.
	if d.Action == store.ActionAuditKeyRotation && len(d.Sig) == 0 {
		return model.AuditEvent{}, store.ErrReservedAuditAction
	}
	// OBS-03 ledger correlation: stamp the W3C trace_id/span_id of the request's span
	// (or the extracted inbound trace, when the engine exports none of its own) into
	// the already-redacted, minimal-data Meta — a trace id is correlation, not payload
	// (docs/SECURITY-HARDENING.md). With no trace context this is a no-op. It is the single point that
	// ties every ledger event to the caller's trace (consumed by + any SIEM via
	// the existing audit export).
	d.Meta = obstrace.EnrichAuditMeta(ctx, d.Meta)
	if err := a.lockTenant(ctx); err != nil {
		return model.AuditEvent{}, err
	}
	pending, hasPending, err := a.pendingGap(ctx)
	if err != nil {
		return model.AuditEvent{}, err
	}
	lastSeq, prevHash, err := a.tail(ctx)
	if err != nil {
		return model.AuditEvent{}, err
	}

	mode := a.spoolOnFull
	if mode == "" {
		mode = store.AuditSpoolBlock
	}
	exempt := len(d.Sig) > 0 || d.Action == store.ActionAuditArchiveSegment
	// A draft-carried signature (a checkpoint) was computed over the CURRENT head
	// before this call (core/audit signs the (tenant, headSeq, headHash) preimage
	// inside the same Mutate). Sealing a marker in front of it would re-link its
	// PrevHash to the marker and break the archive verifier's O(1) attested-head
	// binding (checkpointPreimage over line.Seq-1 + prev_hash) on a legitimate
	// chain. So signed drafts never seal: they append contiguously at tail+1 and
	// the episode keeps accumulating until the next unsigned write — the hole
	// positions are chosen at seal time, so a mid-episode checkpoint only shifts
	// them, never the declared count.
	sealsPending := hasPending && len(d.Sig) == 0

	var (
		marker      model.AuditEvent
		markerMeta  string
		markerBlind []byte
		markerSize  int64
	)
	if sealsPending {
		marker, markerMeta, markerBlind, err = a.buildGapMarker(pending, lastSeq, prevHash)
		if err != nil {
			return model.AuditEvent{}, err
		}
		if a.spoolMaxBytes > 0 {
			markerSize = auditEventSpoolBytes(marker, markerMeta, markerBlind)
		}
	}

	incomingSeq, incomingPrev := lastSeq+1, prevHash
	if sealsPending {
		incomingSeq, incomingPrev = marker.Seq+1, marker.Hash
	}
	ev, metaJSON, blind, err := a.buildEvent(d, incomingSeq, incomingPrev)
	if err != nil {
		return model.AuditEvent{}, err
	}

	var usage, spoolBytes int64
	if a.spoolMaxBytes > 0 {
		spoolBytes = auditEventSpoolBytes(ev, metaJSON, blind)
		usage, err = a.lockSpoolUsage(ctx)
		if err != nil {
			return model.AuditEvent{}, err
		}
		// Integrity machinery is small and rate-bounded; checkpoints, archive
		// anchors and gap markers must keep the ledger provable while governance is
		// halted. They are therefore always admitted but remain fully accounted.
		overBudget := budgetWouldExceed(usage, markerSize, a.spoolMaxBytes)
		if !overBudget {
			overBudget = budgetWouldExceed(usage+markerSize, spoolBytes, a.spoolMaxBytes)
		}
		if !exempt && overBudget {
			if mode == store.AuditSpoolBlock {
				// Inherited degrade state remains untouched. Sealing a marker in a
				// transaction that returns this error would only roll it back.
				return model.AuditEvent{}, store.ErrAuditSpoolFull
			}

			current := auditGap{dropped: 1, firstDroppedAt: a.clock.Now().String()}
			if hasPending {
				current.dropped += pending.dropped
				current.firstDroppedAt = pending.firstDroppedAt
			}
			if current.dropped >= gapSealEvery {
				marker, markerMeta, markerBlind, err := a.buildGapMarker(current, lastSeq, prevHash)
				if err != nil {
					return model.AuditEvent{}, err
				}
				if err := a.persistEvent(ctx, marker, markerMeta, markerBlind); err != nil {
					return model.AuditEvent{}, err
				}
				if err := a.deletePendingGap(ctx); err != nil {
					return model.AuditEvent{}, err
				}
				return model.AuditEvent{}, nil
			}
			if err := a.recordDrop(ctx, current.firstDroppedAt); err != nil {
				return model.AuditEvent{}, err
			}
			return model.AuditEvent{}, nil
		}
	}

	if sealsPending {
		if err := a.persistEvent(ctx, marker, markerMeta, markerBlind); err != nil {
			return model.AuditEvent{}, err
		}
		if err := a.deletePendingGap(ctx); err != nil {
			return model.AuditEvent{}, err
		}
	}
	if err := a.persistEvent(ctx, ev, metaJSON, blind); err != nil {
		return model.AuditEvent{}, err
	}
	return ev, nil
}

// auditGap is one pending degrade episode. dropped is the number of sequence
// positions the eventual marker must declare; firstDroppedAt remains stable for
// the entire episode.
type auditGap struct {
	dropped        int64
	firstDroppedAt string
}

// pendingGap reads the tenant's degrade episode after lockTenant has serialized
// this chain's appenders.
func (a *auditLog) pendingGap(ctx context.Context) (auditGap, bool, error) {
	q := a.dia.Rebind("SELECT dropped, first_dropped_at FROM " +
		a.relation(auditSpoolGapsTable) + " WHERE tenant_id = ?")
	var gap auditGap
	err := a.tx.QueryRowContext(ctx, q, a.tenant.String()).Scan(&gap.dropped, &gap.firstDroppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auditGap{}, false, nil
	}
	if err != nil {
		return auditGap{}, false, wrapUnavailableErr(err)
	}
	return gap, true, nil
}

// recordDrop increments mutable episode state without advancing either the
// ledger or its usage counter. ON CONFLICT deliberately preserves the first
// timestamp while incrementing the durable count.
func (a *auditLog) recordDrop(ctx context.Context, firstDroppedAt string) error {
	q := a.dia.Rebind("INSERT INTO " + a.relation(auditSpoolGapsTable) +
		" (tenant_id, dropped, first_dropped_at) VALUES (?, 1, ?) " +
		"ON CONFLICT (tenant_id) DO UPDATE SET dropped = " + auditSpoolGapsTable + ".dropped + 1")
	_, err := a.tx.ExecContext(ctx, q, a.tenant.String(), firstDroppedAt)
	return wrapUnavailableErr(err)
}

// deletePendingGap clears mutable episode state only after its signed marker is
// in-chain. A later error rolls both operations back with the caller's tx.
func (a *auditLog) deletePendingGap(ctx context.Context) error {
	q := a.dia.Rebind("DELETE FROM " + a.relation(auditSpoolGapsTable) + " WHERE tenant_id = ?")
	_, err := a.tx.ExecContext(ctx, q, a.tenant.String())
	return wrapUnavailableErr(err)
}

// buildGapMarker constructs the only legitimate audit.gap event. Its hash link
// remains continuous while its sequence skips the dropped positions.
func (a *auditLog) buildGapMarker(gap auditGap, lastSeq int64, prevHash []byte) (model.AuditEvent, string, []byte, error) {
	return a.buildEvent(model.AuditDraft{
		Actor:      model.ActorSystem,
		ActorKind:  model.ActorSystem,
		Action:     store.ActionAuditGap,
		TargetKind: "core.audit_gap",
		Meta: map[string]any{
			store.GapMetaFromSeq: lastSeq + 1,
			store.GapMetaToSeq:   lastSeq + gap.dropped,
			store.GapMetaCount:   gap.dropped,
			store.GapMetaReason:  store.GapReasonSpoolFull,
			store.GapMetaAt:      gap.firstDroppedAt,
		},
	}, lastSeq+gap.dropped+1, prevHash)
}

// buildEvent applies the normal canonical hash and per-event signature flow to
// both caller drafts and internal gap markers.
func (a *auditLog) buildEvent(d model.AuditDraft, seq int64, prevHash []byte) (model.AuditEvent, string, []byte, error) {
	metaJSON, err := canon.CanonicalMeta(d.Meta)
	if err != nil {
		return model.AuditEvent{}, "", nil, err
	}
	// One fresh blind per record, from the CSPRNG, so the metadata commitment that
	// travels off-box is hiding rather than a dictionary-attackable digest. This
	// runs for gap markers too — they reach buildEvent directly, and their metadata
	// is the lowest-entropy in the ledger, so they are exactly the records that
	// would leak under an unblinded rule.
	//
	// When the write gate is closed the record is sealed under the LEGACY rule with
	// no blind, so a node still running the previous binary can verify it. That is
	// a deliberate, operator-visible downgrade for the duration of a rollout, not a
	// fallback: nothing here silently picks the weaker rule.
	var blind []byte
	if a.blindMeta {
		blind = make([]byte, canon.BlindLen)
		if _, err := rand.Read(blind); err != nil {
			return model.AuditEvent{}, "", nil, fmt.Errorf("audit: metadata blind: %w", err)
		}
		// Fail closed on the VALUE, not only on the error. Under Go 1.26 crypto/rand
		// does not report a failure by returning one — it kills the process — so the
		// branch above is a guard against a substituted reader, not against the
		// platform. What must not happen silently is sealing a record with a blind
		// that does not hide: an all-zero blind makes the commitment a deterministic
		// function of the metadata again, reopening the confirmation oracle for that
		// record while everything downstream still looks correct. A real CSPRNG
		// yields this with probability 2^-256, so refusing costs nothing and the
		// refusal is the honest outcome: no row, no marker, no evidence written under
		// a rule that is not the one being claimed.
		if allZero(blind) {
			return model.AuditEvent{}, "", nil, fmt.Errorf("audit: refusing to seal an event with an all-zero metadata blind: the commitment would not be hiding, which reopens the confirmation oracle the blind exists to close; the CSPRNG is not delivering entropy")
		}
	}
	commitment, err := canon.MetaCommitmentFor(blind, metaJSON)
	if err != nil {
		return model.AuditEvent{}, "", nil, err
	}
	ev := model.AuditEvent{
		ID:             model.NewID(),
		TenantID:       a.tenant,
		Seq:            seq,
		OccurredAt:     a.clock.Now(),
		Actor:          d.Actor,
		ActorKind:      d.ActorKind,
		Action:         d.Action,
		TargetKind:     d.TargetKind,
		TargetID:       d.TargetID,
		Meta:           d.Meta,
		MetaCommitment: commitment,
		MetaBlinded:    blind != nil,
		PayloadHash:    d.PayloadHash,
		PrevHash:       prevHash,
		// Sig is a detached Ed25519 signature (audit checkpoints, core/audit). It
		// is excluded from the hash preimage by design (see canon), so it is
		// persisted but does not alter the chain hash it attests.
		Sig: d.Sig,
	}
	pre := canon.Event{
		TenantID:       ev.TenantID.String(),
		Seq:            ev.Seq,
		OccurredAt:     ev.OccurredAt.String(),
		Actor:          ev.Actor,
		ActorKind:      ev.ActorKind,
		Action:         ev.Action,
		TargetKind:     string(ev.TargetKind),
		TargetID:       ev.TargetID.String(),
		MetaCommitment: ev.MetaCommitment,
		PayloadHash:    ev.PayloadHash,
		PrevHash:       ev.PrevHash,
	}
	// EventHash validates the width invariants itself, so a wrong-width digest can
	// never reach the preimage encoder — which zero-pads and truncates to keep the
	// preimage unambiguous, and would otherwise emit a well-formed hash over a stub
	// nobody could explain later.
	ev.Hash, err = canon.EventHash(pre)
	if err != nil {
		return model.AuditEvent{}, "", nil, err
	}
	// Per-event signatures bind tenant, sequence and the computed chain hash.
	// Draft-carried checkpoint signatures stay in their distinct domain and are
	// never overwritten.
	if len(ev.Sig) == 0 && a.signEvent != nil {
		ev.Sig = a.signEvent(ev.TenantID.String(), ev.Seq, ev.Hash)
	}
	return ev, metaJSON, blind, nil
}

// persistEvent inserts one already-sealed event, advances the head, and accounts
// its exact logical bytes when the spool budget is enabled. Internal gap markers
// use this same path and are sanctioned overflow: no guard is applied here.
func (a *auditLog) persistEvent(ctx context.Context, ev model.AuditEvent, metaJSON string, blind []byte) error {
	q := a.dia.Rebind(`INSERT INTO ` + a.relation(auditTable) + `
(id, tenant_id, seq, occurred_at, actor, actor_kind, action, target_kind, target_id, meta, meta_blind, payload_hash, prev_hash, hash, sig)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	_, err := a.tx.ExecContext(ctx, q,
		ev.ID.String(), ev.TenantID.String(), ev.Seq, ev.OccurredAt.String(),
		ev.Actor, ev.ActorKind, ev.Action, string(ev.TargetKind), ev.TargetID.String(),
		metaJSON, blind, ev.PayloadHash, ev.PrevHash, ev.Hash, ev.Sig)
	if err != nil {
		return mapWriteErr(err)
	}
	if err := a.advanceHead(ctx, ev.Seq, ev.Hash); err != nil {
		return err
	}
	if a.spoolMaxBytes > 0 {
		q = a.dia.Rebind("UPDATE " + a.relation(auditSpoolUsageTable) +
			" SET bytes = bytes + ? WHERE id = 1")
		if _, err := a.tx.ExecContext(ctx, q, auditEventSpoolBytes(ev, metaJSON, blind)); err != nil {
			return wrapUnavailableErr(err)
		}
	}
	return nil
}

// budgetWouldExceed performs overflow-safe max-budget arithmetic.
func budgetWouldExceed(usage, additional, max int64) bool {
	return usage > max || additional > max-usage
}

// lockSpoolUsage reads and, on Postgres, locks the single global usage row so
// budget checks across tenants serialize with the additive update. SQLite's
// single-writer connection already supplies that serialization and does not
// support SELECT FOR UPDATE.
func (a *auditLog) lockSpoolUsage(ctx context.Context) (int64, error) {
	q := "SELECT bytes FROM " + a.relation(auditSpoolUsageTable) + " WHERE id = 1"
	if a.dia.Name() == store.EnginePostgres {
		q += " FOR UPDATE"
	}
	var usage int64
	if err := a.tx.QueryRowContext(ctx, q).Scan(&usage); err != nil {
		return 0, wrapUnavailableErr(err)
	}
	return usage, nil
}

// auditEventSpoolBytes mirrors the INSERT bindings exactly. Strings use the
// same String conversions, seq is the fixed eight-byte logical integer, and
// nil byte slices naturally contribute zero through len.
//
// blind is a separate parameter rather than a field of ev because the event
// carries the derived COMMITMENT, not the blind that produced it: the blind is
// stored material that never enters the in-memory event. Omitting it would
// under-count every blinded row by BlindLen bytes, and this counter is not
// bookkeeping — it gates ADR-0024 Q2 admission, which decides when evidence stops
// being recorded and starts being declared dropped in a signed gap marker. An
// under-count moves that threshold, so the ledger would admit more than the
// operator's configured budget before degrading.
func auditEventSpoolBytes(ev model.AuditEvent, metaJSON string, blind []byte) int64 {
	return int64(len(ev.ID.String()) +
		len(ev.TenantID.String()) +
		8 +
		len(ev.OccurredAt.String()) +
		len(ev.Actor) +
		len(ev.ActorKind) +
		len(ev.Action) +
		len(string(ev.TargetKind)) +
		len(ev.TargetID.String()) +
		len(metaJSON) +
		len(blind) +
		len(ev.PayloadHash) +
		len(ev.PrevHash) +
		len(ev.Hash) +
		len(ev.Sig))
}

// advanceHead records the new chain tip for the tenant. The head lets Verify
// detect deletion of the most-recent events (which leaves no sequence gap). The
// head is not itself signed, so a determined attacker with full database write
// access could tamper both consistently; cryptographic completeness (a signed
// head) is the deferred Ed25519 checkpoint work. It still catches accidental
// truncation and naive tampering, on top of the append-only triggers.
func (a *auditLog) advanceHead(ctx context.Context, seq int64, hash []byte) error {
	q := a.dia.Rebind("INSERT INTO " + a.relation(auditHeadsTable) +
		" (tenant_id, seq, hash) VALUES (?, ?, ?) " +
		"ON CONFLICT (tenant_id) DO UPDATE SET seq = excluded.seq, hash = excluded.hash")
	_, err := a.tx.ExecContext(ctx, q, a.tenant.String(), seq, hash)
	return wrapUnavailableErr(err)
}

// head returns the recorded chain tip for the tenant, or ok=false if none.
func (a *auditLog) head(ctx context.Context) (seq int64, hash []byte, ok bool, err error) {
	q := a.dia.Rebind("SELECT seq, hash FROM " +
		a.relation(auditHeadsTable) + " WHERE tenant_id = ?")
	err = a.tx.QueryRowContext(ctx, q, a.tenant.String()).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	return seq, hash, true, nil
}

// lockTenant serializes concurrent appends for this tenant. On Postgres it takes
// a per-tenant advisory lock for the transaction; on SQLite the single-writer
// model already serializes writers, so it is a no-op.
func (a *auditLog) lockTenant(ctx context.Context) error {
	if a.dia.Name() != store.EnginePostgres {
		return nil
	}
	_, err := a.tx.ExecContext(ctx,
		"SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))", a.tenant.String())
	return wrapUnavailableErr(err)
}

// LockAppends implements store.AuditAppendLocker. It lets a multi-step
// ceremony acquire every transaction-scoped lock on which Append may wait
// before taking its final database-time observation. The order deliberately
// matches Append: tenant first, then the global spool-usage row when budget
// accounting is enabled. Both locks are re-entrant for the same Postgres
// transaction; SQLite is already single-writer and does not use FOR UPDATE.
func (a *auditLog) LockAppends(ctx context.Context) error {
	if a.readOnly {
		return store.ErrReadOnly
	}
	// Same notice Append gives before its own lockTenant. This capability takes the
	// SAME per-tenant audit lock, so without it the tracker never learns that audit
	// was taken first and prepare cannot refuse the inverse order before it becomes a
	// cycle. Nothing reaches this through AuthMutate today -- its only production
	// consumer transacts with Mutate -- but the capability is handed out on the public
	// AuthScope.Audit surface, so an unreached hole is tomorrow's blind spot.
	if a.directoryWriter != nil {
		a.directoryWriter.noteAudit()
	}
	if err := a.lockTenant(ctx); err != nil {
		return err
	}
	if a.spoolMaxBytes > 0 {
		_, err := a.lockSpoolUsage(ctx)
		return err
	}
	return nil
}

// tail returns the tenant's highest sequence number and its hash, or (0,
// ZeroHash) for an empty chain.
func (a *auditLog) tail(ctx context.Context) (int64, []byte, error) {
	q := a.dia.Rebind("SELECT seq, hash FROM " + a.relation(auditTable) +
		" WHERE tenant_id = ? ORDER BY seq DESC LIMIT 1")
	var seq int64
	var hash []byte
	err := a.tx.QueryRowContext(ctx, q, a.tenant.String()).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, canon.ZeroHash(), nil
	}
	if err != nil {
		return 0, nil, wrapUnavailableErr(err)
	}
	return seq, hash, nil
}

// Verify walks the tenant's live chain from fromSeq and reports the first
// structural break. If fromSeq lands inside a declared hole, anchoring at the
// last physical row at or below fromSeq-1 verifies a slightly earlier superset
// so the marker can declare that hole. Declared-gap authenticity is proved
// separately by the marker's per-event Ed25519 signature in
// audit.VerifyEvents/the archive verifier with pinned keys; olivares audit
// verify runs all three checks.
func (a *auditLog) Verify(ctx context.Context, fromSeq int64) (store.VerifyReport, error) {
	if fromSeq < 1 {
		fromSeq = 1
	}
	anchorSeq, expectedPrev, err := a.anchorAtOrBelow(ctx, fromSeq-1)
	if err != nil {
		return store.VerifyReport{}, err
	}
	expectedSeq := anchorSeq + 1

	q := a.dia.Rebind("SELECT " + columnList(auditColumns) + " FROM " + a.relation(auditTable) +
		" WHERE tenant_id = ? AND seq >= ? ORDER BY seq ASC")
	rows, err := a.tx.QueryContext(ctx, q, a.tenant.String(), expectedSeq)
	if err != nil {
		return store.VerifyReport{}, err
	}
	defer rows.Close()

	var report store.VerifyReport
	for rows.Next() {
		ev, metaJSON, _, err := scanAudit(rows)
		if err != nil {
			// An illegal row shape is a chain break to REPORT, not an error to
			// propagate: the ledger is readable and the answer is "this row is not a
			// legal record". Reporting it as unavailability would let an altered
			// discriminator read as an outage.
			var bad malformedRowErr
			if errors.As(err, &bad) {
				report.Checked++
				report.BreakAt, report.Reason = bad.seq, "meta-blind-malformed"
				return report, nil
			}
			return store.VerifyReport{}, err
		}
		report.Checked++
		if ev.Seq != expectedSeq {
			if ev.Action != store.ActionAuditGap {
				report.BreakAt, report.Reason = expectedSeq, "seq-gap"
				return report, nil
			}
			if !declaresGap(metaJSON, expectedSeq, ev.Seq) {
				report.BreakAt, report.Reason = ev.Seq, "gap-mismatch"
				return report, nil
			}
			report.DeclaredGaps++
			expectedSeq = ev.Seq
		} else if ev.Action == store.ActionAuditGap {
			// A marker is meaningful only when its own sequence follows the hole
			// it declares. The reserved-action guard leaves no legitimate writer
			// for an in-place marker.
			report.BreakAt, report.Reason = ev.Seq, "gap-mismatch"
			return report, nil
		}
		if !bytesEqual(ev.PrevHash, expectedPrev) {
			report.BreakAt, report.Reason = ev.Seq, "prev-mismatch"
			return report, nil
		}
		want, err := canon.EventHash(canon.Event{
			TenantID:       ev.TenantID.String(),
			Seq:            ev.Seq,
			OccurredAt:     ev.OccurredAt.String(),
			Actor:          ev.Actor,
			ActorKind:      ev.ActorKind,
			Action:         ev.Action,
			TargetKind:     string(ev.TargetKind),
			TargetID:       ev.TargetID.String(),
			MetaCommitment: ev.MetaCommitment,
			PayloadHash:    ev.PayloadHash,
			PrevHash:       ev.PrevHash,
		})
		if err != nil {
			// Same stance as a malformed blind: a stored digest of the wrong width is
			// an illegal record, and naming it beats reporting an outage.
			report.BreakAt, report.Reason = ev.Seq, "malformed-record"
			return report, nil
		}
		if !bytesEqual(want, ev.Hash) {
			report.BreakAt, report.Reason = ev.Seq, "hash-mismatch"
			return report, nil
		}
		expectedPrev = ev.Hash
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return store.VerifyReport{}, err
	}

	// Tail-truncation check: the recorded head must match the last verified
	// event. A head ahead of the chain means the most-recent events were
	// deleted (no sequence gap is left behind, so the walk alone cannot see it).
	lastSeq := expectedSeq - 1
	headSeq, headHash, hasHead, err := a.head(ctx)
	if err != nil {
		return store.VerifyReport{}, err
	}
	if hasHead {
		switch {
		case headSeq > lastSeq:
			report.BreakAt, report.Reason = lastSeq+1, "tail-truncated"
			return report, nil
		case headSeq == lastSeq && lastSeq >= fromSeq && !bytesEqual(headHash, expectedPrev):
			report.BreakAt, report.Reason = lastSeq, "head-mismatch"
			return report, nil
		}
	}

	// An empty range proves nothing. In particular, post-erasure and compliance
	// callers must not be able to turn an absent ledger into "verified" evidence
	// through vacuous truth.
	if report.Checked == 0 {
		report.Reason = "no-events"
		return report, nil
	}
	report.OK = true
	return report, nil
}

// declaresGap delegates the single acceptance rule shared by live and archive
// verification to core/store.
func declaresGap(metaJSON string, expectedSeq, markerSeq int64) bool {
	return store.DeclaresGap(metaJSON, expectedSeq, markerSeq)
}

// Head returns the tenant's current chain tip (highest seq + its hash), reading
// the actual last event, or ok=false for an empty chain.
func (a *auditLog) Head(ctx context.Context) (store.HeadRef, bool, error) {
	seq, hash, err := a.tail(ctx)
	if err != nil {
		return store.HeadRef{}, false, err
	}
	if seq == 0 {
		return store.HeadRef{}, false, nil
	}
	return store.HeadRef{Seq: seq, Hash: hash}, true, nil
}

// RecordedHead implements store.RecordedHeadReader: the tip recorded in
// audit_heads, which is NOT the same question as Head (the last surviving event
// row). Only a caller that can see both can tell an emptied ledger from an empty
// one — see the interface doc.
func (a *auditLog) RecordedHead(ctx context.Context) (store.HeadRef, bool, error) {
	seq, hash, ok, err := a.head(ctx)
	if err != nil || !ok {
		return store.HeadRef{}, false, err
	}
	return store.HeadRef{Seq: seq, Hash: hash}, true, nil
}

// Walk streams the tenant's events in sequence order from fromSeq.
func (a *auditLog) Walk(ctx context.Context, fromSeq int64, fn func(model.AuditEvent) error) error {
	q := a.dia.Rebind("SELECT " + columnList(auditColumns) + " FROM " + a.relation(auditTable) +
		" WHERE tenant_id = ? AND seq >= ? ORDER BY seq ASC")
	rows, err := a.tx.QueryContext(ctx, q, a.tenant.String(), fromSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		ev, _, _, err := scanAudit(rows)
		if err != nil {
			return err
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	return rows.Err()
}

// WalkCanonical implements store.CanonicalWalker: the same SELECT and ordering
// as Walk, but yielding the STORED canonical meta string scanAudit already
// returns instead of discarding it. That string is the authoritative MetaDigest
// input (canon), so a consumer (archival export) can re-derive
// canon.EventHash offline with no round-trip ambiguity.
func (a *auditLog) WalkCanonical(ctx context.Context, fromSeq int64, fn func(ev model.AuditEvent, metaCanonical string, metaBlind []byte) error) error {
	q := a.dia.Rebind("SELECT " + columnList(auditColumns) + " FROM " + a.relation(auditTable) +
		" WHERE tenant_id = ? AND seq >= ? ORDER BY seq ASC")
	rows, err := a.tx.QueryContext(ctx, q, a.tenant.String(), fromSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		ev, meta, blind, err := scanAudit(rows)
		if err != nil {
			return err
		}
		if err := fn(ev, meta, blind); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ReadVerifiedAuditAnchor implements store.VerifiedAuditAnchorReader. The
// target and its immediate predecessor are selected by one statement so a
// concurrent append cannot move a separately-read head underneath the proof.
// Two canonical hashes are sufficient for this receipt-local claim: the
// target row is well formed and links to the exact stored predecessor. Full
// chain/tail verification remains AuditLog.Verify's separate responsibility.
func (a *auditLog) ReadVerifiedAuditAnchor(
	ctx context.Context,
	seq int64,
) (model.AuditEvent, string, bool, error) {
	if seq < 1 {
		return model.AuditEvent{}, "", false, nil
	}
	relation := a.relation(auditTable)
	query := a.dia.Rebind("SELECT " + columnList(auditColumns) + " FROM " + relation +
		" WHERE tenant_id = ? AND seq IN (SELECT seq FROM " + relation +
		" WHERE tenant_id = ? AND seq <= ? ORDER BY seq DESC LIMIT 2) ORDER BY seq ASC")
	rows, err := a.tx.QueryContext(ctx, query, a.tenant.String(), a.tenant.String(), seq)
	if err != nil {
		return model.AuditEvent{}, "", false, err
	}
	defer rows.Close()
	type canonicalRow struct {
		event model.AuditEvent
		meta  string
	}
	selected := make([]canonicalRow, 0, 2)
	for rows.Next() {
		event, meta, _, scanErr := scanAudit(rows)
		if scanErr != nil {
			return model.AuditEvent{}, "", false, scanErr
		}
		selected = append(selected, canonicalRow{event: event, meta: meta})
	}
	if err := rows.Err(); err != nil {
		return model.AuditEvent{}, "", false, err
	}
	if len(selected) == 0 || selected[len(selected)-1].event.Seq != seq {
		return model.AuditEvent{}, "", false, nil
	}
	verifyHash := func(event model.AuditEvent) error {
		want, hashErr := canon.EventHash(canon.Event{
			TenantID: event.TenantID.String(), Seq: event.Seq,
			OccurredAt: event.OccurredAt.String(), Actor: event.Actor,
			ActorKind: event.ActorKind, Action: event.Action,
			TargetKind: string(event.TargetKind), TargetID: event.TargetID.String(),
			MetaCommitment: event.MetaCommitment, PayloadHash: event.PayloadHash,
			PrevHash: event.PrevHash,
		})
		if hashErr != nil || !bytesEqual(want, event.Hash) {
			return fmt.Errorf("audit anchor seq %d has an invalid canonical hash", event.Seq)
		}
		return nil
	}
	target := selected[len(selected)-1]
	if err := verifyHash(target.event); err != nil {
		return model.AuditEvent{}, "", false, err
	}
	if seq == 1 {
		if len(selected) != 1 || !bytesEqual(target.event.PrevHash, make([]byte, 32)) {
			return model.AuditEvent{}, "", false, fmt.Errorf("audit anchor seq 1 has a predecessor")
		}
	} else {
		if len(selected) != 2 || selected[0].event.Seq != seq-1 {
			return model.AuditEvent{}, "", false, fmt.Errorf("audit anchor seq %d lacks its predecessor", seq)
		}
		predecessor := selected[0].event
		if err := verifyHash(predecessor); err != nil {
			return model.AuditEvent{}, "", false, err
		}
		if !bytesEqual(target.event.PrevHash, predecessor.Hash) {
			return model.AuditEvent{}, "", false, fmt.Errorf("audit anchor seq %d breaks predecessor linkage", seq)
		}
	}
	return target.event, target.meta, true, nil
}

// Compile-time proof the ledger exposes the canonical-walk capability.
var _ store.CanonicalWalker = (*auditLog)(nil)
var _ store.VerifiedAuditAnchorReader = (*auditLog)(nil)

// anchorAtOrBelow returns the last physical row at or below seq, or the genesis
// anchor when no such row exists. The non-exact lookup is what lets Verify
// begin from a sequence position inside a declared hole.
func (a *auditLog) anchorAtOrBelow(ctx context.Context, seq int64) (int64, []byte, error) {
	q := a.dia.Rebind("SELECT seq, hash FROM " + a.relation(auditTable) +
		" WHERE tenant_id = ? AND seq <= ? ORDER BY seq DESC LIMIT 1")
	var anchorSeq int64
	var h []byte
	err := a.tx.QueryRowContext(ctx, q, a.tenant.String(), seq).Scan(&anchorSeq, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, canon.ZeroHash(), nil
	}
	if err != nil {
		return 0, nil, err
	}
	return anchorSeq, h, nil
}

// rowScanner is the row-reading surface shared by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAudit reads one ledger row into an AuditEvent plus its stored canonical
// meta string (needed to recompute the chain hash without round-trip ambiguity).
// malformedRowErr marks a stored audit row whose own shape is illegal, and
// carries the sequence so Verify can name the break instead of failing opaquely.
// It is deliberately distinct from an I/O error: an unreadable database makes
// verification UNAVAILABLE, while an illegal row is a finding ABOUT the ledger.
type malformedRowErr struct {
	seq int64
	id  string
	err error
}

func (e malformedRowErr) Error() string {
	return fmt.Sprintf("audit event %s seq %d: %v", e.id, e.seq, e.err)
}

func (e malformedRowErr) Unwrap() error { return e.err }

func scanAudit(rs rowScanner) (model.AuditEvent, string, []byte, error) {
	var (
		ev                      model.AuditEvent
		id, tenantID            string
		targetKind, targetID    string
		occurredAt, meta        string
		metaBlind               []byte
		payloadHash, prev, hash []byte
		sig                     []byte
	)
	if err := rs.Scan(&id, &tenantID, &ev.Seq, &occurredAt, &ev.Actor, &ev.ActorKind,
		&ev.Action, &targetKind, &targetID, &meta, &metaBlind, &payloadHash, &prev, &hash, &sig); err != nil {
		return model.AuditEvent{}, "", nil, err
	}
	ts, err := model.ParseTimestamp(occurredAt)
	if err != nil {
		return model.AuditEvent{}, "", nil, err
	}
	ev.ID = model.ID(id)
	ev.TenantID = model.TenantID(tenantID)
	ev.OccurredAt = ts
	ev.TargetKind = model.Kind(targetKind)
	ev.TargetID = model.ID(targetID)
	ev.Meta = nil // the canonical string is authoritative; callers re-parse if needed
	// The commitment IS resolved here, unlike Meta: it is the hash input every read
	// path needs and the one value the projections carry off-box, so resolving it
	// once at the decoder is what stops the choice of rule (blinded vs the legacy
	// unblinded digest, keyed on the stored blind) from diverging between Verify,
	// the export, the archive and the push feed.
	//
	// Resolving here also makes the decoder the single gate on the discriminator:
	// a row whose blind is neither absent nor BlindLen bytes never becomes an
	// in-memory event at all, so no downstream path has to remember to check it.
	ev.MetaCommitment, err = canon.MetaCommitmentFor(metaBlind, meta)
	if err != nil {
		return model.AuditEvent{}, "", nil, malformedRowErr{seq: ev.Seq, id: id, err: err}
	}
	// The rule travels with the row. Without this the derived commitment is 32
	// opaque bytes with no way to tell which rule made it, and every projection
	// downstream would have to guess — which is how the legacy unblinded digest
	// would end up on the wire.
	ev.MetaBlinded = metaBlind != nil
	ev.PayloadHash = payloadHash
	ev.PrevHash = prev
	ev.Hash = hash
	ev.Sig = sig
	return ev, meta, metaBlind, nil
}

// columnList joins column names for a SELECT.
func columnList(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// bytesEqual is a constant-length-agnostic equality for digests.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// allZero reports whether every byte is zero.
func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
