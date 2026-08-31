// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ClaimDecision inserts the durable pending row that owns a delegation handle
// JTI and a service-scoped nonce. ON CONFLICT DO NOTHING is supported by both
// engines and, unlike an insert-then-catch strategy, does not abort a Postgres
// transaction on a uniqueness collision. The subsequent read therefore runs
// in the same usable transaction and returns the collision as protocol data.
//
// On a fresh insert (created) it emits the REQUIRED per-operation audits INSIDE the
// caller's transaction — delegation.claim always, and delegation.capability_overclaim
// when droppedOverclaims is non-empty (sanitized, sorted vocabulary) — and PERSISTS
// their outcome on the row's evidence_anchored column: true IFF the claim audit AND
// (when attempted) the overclaim audit both anchored (Seq>0). Under the DEGRADE spool
// policy an over-budget Append durably commits loss accounting and returns a
// zero-sequence event with a nil error; the row's evidence_anchored then stays false —
// a deny-closed tombstone the caller refuses evidence-or-refuse AFTER commit, WITHOUT
// rolling back the gap accounting (never return an error here for a drop). The persisted
// flag is the SOURCE OF TRUTH: an exact second presentation resolves the existing row
// (created==false) and inherits its evidence_anchored, so a dropped anchor keeps
// refusing every retry instead of bypassing on the second call.
func (a *authScope) ClaimDecision(
	ctx context.Context,
	claim model.PDPDecisionClaim,
	droppedOverclaims map[string]bool,
) (model.PDPDecisionClaim, bool, error) {
	if a.ts.readOnly {
		return model.PDPDecisionClaim{}, false, store.ErrReadOnly
	}

	now := a.ts.s.clock.Now()
	claim.State = "pending"
	claim.ClaimedAt = now
	claim.FinalizedAt = nil
	// Deny-closed default: insert un-anchored; only a durable claim (+ overclaim, when
	// attempted) audit below flips it true.
	claim.EvidenceAnchored = false

	encoded, err := pdpDecisionClaimCodec.Encode(claim)
	if err != nil {
		return model.PDPDecisionClaim{}, false, err
	}
	record := make(model.Record, len(encoded)+5)
	for _, field := range pdpDecisionClaimDescriptor.Fields {
		record[field.Name] = redactField(field, encoded[field.Name])
	}
	base := model.BaseFields{
		ID:        model.NewID(),
		TenantID:  a.ts.tenant,
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}
	baseToRecord(record, base, false)

	columns := pdpDecisionClaimDescriptor.AllColumns()
	args := make([]any, len(columns))
	for i, column := range columns {
		args[i] = record[column]
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		pdpDecisionClaimDescriptor.Table,
		strings.Join(columns, ", "),
		placeholders(len(columns)),
	)
	repo := a.ts.repo(pdpDecisionClaimDescriptor)
	repo.guard(query)
	result, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query), args...)
	if err != nil {
		return model.PDPDecisionClaim{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.PDPDecisionClaim{}, false, err
	}
	switch rows {
	case 1:
		stored, err := decodePDPDecisionClaim(record)
		if err != nil {
			return model.PDPDecisionClaim{}, false, err
		}
		anchored, err := a.auditClaimAndOverclaim(ctx, stored, droppedOverclaims)
		if err != nil {
			return model.PDPDecisionClaim{}, false, err
		}
		if anchored {
			// Both required audits anchored: persist evidence_anchored=true so the row
			// is a usable (non-tombstone) decision every later path accepts.
			if err := a.setClaimEvidenceAnchored(ctx, stored.ID, true); err != nil {
				return model.PDPDecisionClaim{}, false, err
			}
		}
		stored.EvidenceAnchored = anchored
		return stored, true, nil
	case 0:
		stored, err := a.loadDecisionClaimConflict(ctx, claim)
		return stored, false, err
	default:
		return model.PDPDecisionClaim{}, false, fmt.Errorf(
			"claim decision inserted %d rows, want at most one", rows,
		)
	}
}

// auditClaimAndOverclaim emits the REQUIRED per-operation audits for a freshly
// inserted claim inside the caller's transaction: delegation.claim always, and
// delegation.capability_overclaim when droppedOverclaims is non-empty. Both are
// attributed to the claim's OWN immutable PEPServiceID (never a caller-supplied
// actor). It returns whether the row is ANCHORED: the claim audit anchored (Seq>0)
// AND, when an overclaim audit was attempted, that anchored too. A DEGRADE-mode drop
// (Seq==0, nil error) leaves anchored=false WITHOUT returning an error, so the caller
// commits the effect + the durable gap accounting and refuses AFTER commit.
func (a *authScope) auditClaimAndOverclaim(
	ctx context.Context,
	stored model.PDPDecisionClaim,
	droppedOverclaims map[string]bool,
) (bool, error) {
	claimEv, err := a.Audit().Append(ctx, model.AuditDraft{
		Actor:      "pep_service:" + stored.PEPServiceID.String(),
		ActorKind:  "pep_service",
		Action:     "delegation.claim",
		TargetKind: pdpDecisionClaimDescriptor.Kind,
		TargetID:   stored.ID,
	})
	if err != nil {
		return false, err
	}
	anchored := claimEv.Seq != 0
	if len(droppedOverclaims) > 0 {
		// EffectiveCapabilities already restricted these to the registered SDK
		// vocabulary, so no attacker-controlled arbitrary capability string can reach
		// the evidence ledger. A degrade-mode drop of THIS audit is also an evidence
		// fault (Seq==0): it leaves the row un-anchored (never returns an error).
		overEv, err := a.Audit().Append(ctx, model.AuditDraft{
			Actor:      "pep_service:" + stored.PEPServiceID.String(),
			ActorKind:  "pep_service",
			Action:     "delegation.capability_overclaim",
			TargetKind: pdpDecisionClaimDescriptor.Kind,
			TargetID:   stored.ID,
			Meta:       map[string]any{"dropped_capabilities": strings.Join(sortedBoolKeys(droppedOverclaims), ",")},
		})
		if err != nil {
			return false, err
		}
		anchored = anchored && overEv.Seq != 0
	}
	return anchored, nil
}

// setClaimEvidenceAnchored persists the evidence_anchored flag on a claim row inside
// the caller's transaction. It is a targeted flag flip (no version bump): within the
// same transaction no external observer sees the intermediate un-anchored state.
func (a *authScope) setClaimEvidenceAnchored(ctx context.Context, id model.ID, anchored bool) error {
	query := fmt.Sprintf(
		"UPDATE %s SET evidence_anchored = ? WHERE id = ? AND tenant_id = ?",
		pdpDecisionClaimDescriptor.Table,
	)
	repo := a.ts.repo(pdpDecisionClaimDescriptor)
	repo.guard(query)
	_, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query), anchored, id.String(), a.ts.tenant.String())
	return err
}

// sortedBoolKeys returns the keys of a boolean-valued set in deterministic sort
// order, matching the sanitized overclaim meta the auth layer produced before this
// audit was centralized in the store op.
func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadDecisionClaimConflict returns the row owning either unique key. Handle
// ownership takes deterministic precedence if malformed input collides with
// two different rows; either row remains replay data for the auth layer.
func (a *authScope) loadDecisionClaimConflict(
	ctx context.Context,
	claim model.PDPDecisionClaim,
) (model.PDPDecisionClaim, error) {
	columns := pdpDecisionClaimDescriptor.AllColumns()
	query := fmt.Sprintf(
		`SELECT %s FROM %s
WHERE tenant_id = ?
  AND (handle_jti = ? OR (pep_service_id = ? AND nonce_hash = ?))
ORDER BY CASE WHEN handle_jti = ? THEN 0 ELSE 1 END, id
LIMIT 1`,
		strings.Join(columns, ", "),
		pdpDecisionClaimDescriptor.Table,
	)
	repo := a.ts.repo(pdpDecisionClaimDescriptor)
	repo.guard(query)
	scan, err := newScanState(pdpDecisionClaimDescriptor, columns)
	if err != nil {
		return model.PDPDecisionClaim{}, err
	}
	err = a.ts.tx.QueryRowContext(
		ctx,
		a.ts.s.dia.Rebind(query),
		a.ts.tenant.String(),
		claim.HandleJTI.String(),
		claim.PEPServiceID.String(),
		claim.NonceHash,
		claim.HandleJTI.String(),
	).Scan(scan.dests...)
	if errors.Is(err, sql.ErrNoRows) {
		return model.PDPDecisionClaim{}, fmt.Errorf(
			"claim decision conflict row disappeared: %w", store.ErrNotFound,
		)
	}
	if err != nil {
		return model.PDPDecisionClaim{}, err
	}
	return decodePDPDecisionClaim(scan.record())
}

// FinalizeDecisionClaim transitions the pending claim identified by id to final
// in a single version-locked, pending-guarded UPDATE. It takes only the MINIMAL
// finalization inputs: the store FORCES state='final' and stamps finalized_at and
// updated_at from its OWN clock (never from the caller), self-guards the verdict
// material (empty/non-JSON verdictJSON or a verdictHash that is not
// sha256(verdictJSON) is rejected with store.ErrInvalidVerdict), and emits the
// delegation.finalize audit — attributed to the claim's OWN immutable PEPServiceID,
// NOT a caller-supplied actor — INSIDE this same transaction, so the audit is
// inseparable from the state change and a raw caller cannot forge finalize
// attribution. It is the ONLY claim mutation on AuthScope (generic
// Create/Update/Delete are not exposed), so a core caller can neither forge a claim,
// finalize with forged material, finalize without evidence, nor overwrite a
// finalized decision. A stale version OR an
// already-final row affects zero rows and returns store.ErrConflict, which the
// auth layer reloads and re-classifies; an absent row returns store.ErrNotFound.
// The evidenceDropped return reports that the pending→final transition committed
// but its delegation.finalize audit was dropped by a DEGRADE-mode spool (Append
// returned Seq==0 after committing durable loss accounting). The caller COMMITS the
// transaction (so the state change AND the gap accounting persist) and refuses the
// observable decision AFTER commit — evidence-or-refuse (sdk/evidence.go). The
// idempotent no-op paths (already-final, or a lost version-locked race) attempt no
// new audit, so evidenceDropped is false there.
func (a *authScope) FinalizeDecisionClaim(
	ctx context.Context,
	id model.ID,
	version int64,
	verdictJSON []byte,
	verdictHash, policyVersion string,
) (bool, error) {
	if a.ts.readOnly {
		return false, store.ErrReadOnly
	}
	if id.IsZero() {
		return false, store.ErrNotFound
	}
	// Self-guard the verdict material: the store never trusts a caller to hand it a
	// verdict document and hash that agree. Empty or non-JSON verdicts, or a hash
	// that is not the SHA-256 of the exact bytes, are refused before any write.
	if len(verdictJSON) == 0 || !json.Valid(verdictJSON) {
		return false, store.ErrInvalidVerdict
	}
	if verdictHash != sha256HexBytes(verdictJSON) {
		return false, store.ErrInvalidVerdict
	}
	now := a.ts.s.clock.Now()
	// Reset evidence_anchored to the deny-closed default in the SAME version-locked
	// transition: the finalize audit below is now the row's most-recent REQUIRED anchor,
	// so a claim that anchored at claim time but whose FINALIZE audit drops becomes a
	// deny-closed tombstone. Only a durable finalize audit flips it true (second UPDATE).
	//
	// The WHERE guards `evidence_anchored = ?` (true) so a TOMBSTONED pending claim (its
	// own claim/overclaim audit dropped) can NEVER be finalized — otherwise a healthy
	// finalize would resurrect a decision whose claim anchor is still lost. This is the
	// store-level backstop for the same check the auth wrapper makes: a raw AuthScope
	// caller cannot bypass it.
	query := fmt.Sprintf(
		`UPDATE %s
SET state = 'final', verdict_json = ?, verdict_hash = ?, policy_version = ?, evidence_anchored = ?, finalized_at = ?, updated_at = ?, version = version + 1
WHERE id = ? AND tenant_id = ? AND version = ? AND state = 'pending' AND evidence_anchored = ?`,
		pdpDecisionClaimDescriptor.Table,
	)
	repo := a.ts.repo(pdpDecisionClaimDescriptor)
	repo.guard(query)
	result, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query),
		string(verdictJSON),
		verdictHash,
		encOptStr(policyVersion),
		false,
		now.String(),
		now.String(),
		id.String(),
		a.ts.tenant.String(),
		version,
		true,
	)
	if err != nil {
		return false, mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		// Distinguish a missing/other-tenant row, a tombstoned (un-anchored) pending
		// claim, and a stale-version / already-final row.
		existing, gerr := a.PDPDecisionClaims().Get(ctx, id)
		if errors.Is(gerr, store.ErrNotFound) {
			return false, store.ErrNotFound
		}
		if gerr != nil {
			return false, gerr
		}
		if existing.State == "pending" && !existing.EvidenceAnchored {
			return false, store.ErrEvidenceMissing
		}
		return false, store.ErrConflict
	}
	// Derive the audit actor from the claim's OWN immutable PEPServiceID (never a
	// caller-supplied actor) so a raw caller cannot sign the finalize as another
	// service or "". A Get here is consistent with the rows==0 branch above.
	finalized, gerr := a.PDPDecisionClaims().Get(ctx, id)
	if gerr != nil {
		return false, gerr
	}
	// Emit the finalize audit inside the SAME transaction so a raw store finalize
	// can never happen without evidence. Under the DEGRADE spool policy the Append
	// durably commits loss accounting and returns a zero-sequence event with a nil
	// error; capture that as evidenceDropped so the caller refuses AFTER commit
	// WITHOUT rolling back the gap accounting (never return an error here for a drop).
	ev, aerr := a.Audit().Append(ctx, model.AuditDraft{
		Actor:      "pep_service:" + finalized.PEPServiceID.String(),
		ActorKind:  "pep_service",
		Action:     "delegation.finalize",
		TargetKind: pdpDecisionClaimDescriptor.Kind,
		TargetID:   id,
	})
	if aerr != nil {
		return false, aerr
	}
	if ev.Seq != 0 {
		// The finalize audit anchored: persist evidence_anchored=true so the final row
		// is a usable decision (and its idempotent retry is not refused as a tombstone).
		if err := a.setClaimEvidenceAnchored(ctx, id, true); err != nil {
			return false, err
		}
	}
	return ev.Seq == 0, nil
}

// sha256HexBytes returns the lowercase-hex SHA-256 of b, matching the verdict
// hash the auth layer binds so the store can verify caller-supplied material.
func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func decodePDPDecisionClaim(record model.Record) (model.PDPDecisionClaim, error) {
	base, err := baseFromRecord(record)
	if err != nil {
		return model.PDPDecisionClaim{}, err
	}
	return pdpDecisionClaimCodec.Decode(base, record)
}
