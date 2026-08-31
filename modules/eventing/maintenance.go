// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// pruneBatch bounds one pruning transaction.
const pruneBatch = 500

// PruneExpired removes captured events older than the retention window and the
// FINISHED deliveries (delivered/dead/denied) that aged past it — the replay
// window is exactly the retention window. Queued and in-flight deliveries are
// never pruned: an old queued row whose event expires dead-letters honestly at
// claim time (event_expired) instead of vanishing. Exported for the
// composition-root pump; batched so a backlog never builds one huge
// transaction. It returns the number of rows removed.
//
// The returned count is about the RETENTION WINDOW — the number an operator reads to know how much
// of the replay window aged out. Spent writer proofs (unit H) are swept in the same
// transaction and deliberately NOT counted: they are bookkeeping garbage rather than retained
// evidence, and folding them in would make an operator-facing number mean less than it says. They
// still drive the batch loop, so a backlog of proofs drains rather than trickling one batch per
// pump tick.
func (m *Module) PruneExpired(ctx context.Context, tenant model.TenantID) (int, error) {
	if m.data == nil {
		return 0, nil
	}
	cutoff := model.NewTimestamp(m.clock.Now().Time().Add(-m.retention)).String()
	total := 0
	for {
		retained, proofs, err := m.pruneBatchOnce(ctx, tenant, cutoff)
		total += retained
		if err != nil || retained+proofs < pruneBatch {
			return total, err
		}
	}
}

// pruneBatchOnce deletes up to pruneBatch expired event rows and up to
// pruneBatch expired finished deliveries in one transaction. The seq cursor
// row (eventing_cursor) is deliberately untouched: pruning the whole log can
// never regress the monotonic sequence.
func (m *Module) pruneBatchOnce(ctx context.Context, tenant model.TenantID, cutoff string) (int, int, error) {
	// A writer proof is only ever useful inside the transaction that wrote it, so it does not need
	// the event-retention window. One hour is generous for a transaction and short enough that an
	// unconsumed proof is bounded rather than permanent.
	attestCutoff := model.NewTimestamp(m.clock.Now().Time().Add(-time.Hour)).String()
	deleted, proofs := 0, 0
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		deleted, proofs = 0, 0
		events, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		old, _, err := events.List(ctx, model.Query{
			Filters: []model.Filter{lt(model.ColCreatedAt, cutoff)},
			Limit:   pruneBatch,
		})
		if err != nil {
			return err
		}
		for _, rec := range old {
			if err := events.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
				return err
			}
			deleted++
		}
		// Unit H: unconsumed writer proofs. The fence CONSUMES a proof when it accepts a
		// mutation, so on an armed deployment this finds almost nothing; what it bounds is the
		// dormant case and any proof whose mutation was rolled back after the row committed. The
		// cutoff is the attestation's own — a proof is only ever useful inside the transaction that
		// wrote it, so keeping it for the event-retention window would be keeping garbage.
		attest, err := sc.Ext(writerAttestKind)
		if err != nil {
			return err
		}
		staleProofs, _, err := attest.List(ctx, model.Query{
			Filters: []model.Filter{lt(model.ColCreatedAt, attestCutoff)},
			Limit:   pruneBatch,
		})
		if err != nil {
			return err
		}
		for _, rec := range staleProofs {
			if err := attest.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
				return err
			}
			proofs++
		}
		deliveries, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		// Three terminal statuses, three queries: the closed query language has
		// no IN. Each is bounded by what is left of the batch budget.
		for _, status := range []string{statusDelivered, statusDead, statusDenied} {
			budget := pruneBatch - deleted
			if budget <= 0 {
				return nil
			}
			fin, _, err := deliveries.List(ctx, model.Query{
				Filters: []model.Filter{eq(colDelStatus, status), lt(model.ColCreatedAt, cutoff)},
				Limit:   budget,
			})
			if err != nil {
				return err
			}
			for _, rec := range fin {
				if err := deliveries.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
					return err
				}
				deleted++
			}
		}
		return nil
	})
	return deleted, proofs, err
}
