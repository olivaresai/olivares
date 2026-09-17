// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/hex"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// inferenceevidence.go extracts ONE thing from the inference proxy: the append/commit
// MECHANISM, and specifically the F9 anchoring discipline that is easy to get wrong
// in a way nothing notices — commit the degrade spool's loss accounting, THEN refuse.
// Returning a sentinel from inside the transaction on a zero-sequence append rolls that
// accounting back, so the drop counter never advances, the signed gap marker never seals,
// and the episode becomes invisible in the chain while the caller still (correctly)
// denies. That is a bug you cannot see from the outside, which is exactly the kind that
// should exist once rather than once per PEP.
//
// ⛔ IT AUTHORIZES NOTHING. It does not select a policy, decide a posture, invent an
// audit hash or choose an action: the caller builds the draft, the tip hash and the
// binding, and the caller decides what a non-anchored receipt means for its own effect.
// It cannot be handed a pre-existing EvidenceRef, so the ref in every receipt it returns
// was derived from the append THIS call just committed — the provenance ClassifyAnchor
// documents it cannot itself check.
//
// The Messages single/batch wrappers keep their exact action, target, payload hash,
// metadata, posture and log lines; only the transaction body is now shared.
type inferenceEvidenceWriter struct{ store store.Store }

// evidenceAppendError carries the ONE observation a returned error would otherwise lose:
// whether the degrade spool dropped this append before the transaction failed.
//
// It exists because a receipt cannot express both. ClassifyAnchor resolves a transaction
// fault first — correctly, since a rolled-back drop is not a committed drop — so on a
// commit failure the spool_degraded observation disappears from the receipt. The Messages
// outcome leg logs that drop from INSIDE its transaction today, and its logging must not
// change, so the observation travels with the error instead. Unwrap keeps errors.Is/As
// working on the store's own error and Error() is the underlying text verbatim, so a log
// line built from it is byte-identical to the one built from the raw error.
type evidenceAppendError struct {
	dropped bool
	err     error
}

func (e evidenceAppendError) Error() string { return e.err.Error() }
func (e evidenceAppendError) Unwrap() error { return e.err }

// Append commits one audit draft and returns the receipt for the caller's expected
// binding. The transaction discipline is the whole point:
//
//  1. Append inside the transaction.
//  2. A real Append error returns from the callback → rollback; nothing durable.
//  3. A zero-sequence event (the DEGRADE drop) is captured and the callback returns NIL,
//     so the loss accounting the store already wrote COMMITS.
//  4. ClassifyAnchor runs only AFTER the transaction returns, never inside it.
//
// A caller must still ask receipt.MustRefuse(binding) for its own effect; an error here is
// a transaction fault, not a policy decision.
func (w inferenceEvidenceWriter) Append(
	ctx context.Context, tenant model.TenantID,
	binding sdk.EvidenceBinding, draft model.AuditDraft,
) (sdk.EvidenceReceipt, error) {
	if w.store == nil {
		return sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired), ErrNoLedger
	}
	var appendDropped bool
	var evidenceRef string
	err := w.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev, aerr := sc.Audit().Append(ctx, draft)
		if aerr != nil {
			return aerr // block-mode spool-full / write fault ⇒ roll back, nothing durable
		}
		if ev.Seq == 0 {
			appendDropped = true // degrade drop: the loss accounting is durable; COMMIT it
			return nil
		}
		evidenceRef = hex.EncodeToString(ev.Hash)
		return nil
	})
	if err != nil {
		return sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultWriteError),
			evidenceAppendError{dropped: appendDropped, err: err}
	}
	return sdk.ClassifyAnchor(binding, evidenceRef, appendDropped, sdk.EvidenceFaultNone), nil
}

// evidenceAppendDropped reports whether the DEGRADE spool dropped this append, whether
// the transaction went on to commit or to fail. It is the predicate a best-effort outcome
// leg logs its evidence gap from.
func evidenceAppendDropped(receipt sdk.EvidenceReceipt, err error) bool {
	if err != nil {
		var appendErr evidenceAppendError
		if ok := asEvidenceAppendError(err, &appendErr); ok {
			return appendErr.dropped
		}
		return false
	}
	return receipt.Fault == sdk.EvidenceFaultSpoolDegraded
}

// asEvidenceAppendError is errors.As narrowed to this one type, kept local so no caller
// is tempted to reach through the wrapper for anything else.
func asEvidenceAppendError(err error, target *evidenceAppendError) bool {
	for err != nil {
		if e, ok := err.(evidenceAppendError); ok { //nolint:errorlint // the loop IS the unwrap
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
