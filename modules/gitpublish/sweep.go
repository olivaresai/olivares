// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// sweepBatch bounds the intents one tenant pass observes.
const sweepBatch = 50

// Leader reports whether this node is the active writer.
// store.LeaderElector satisfies it.
type Leader interface{ Active() bool }

// SweepPump returns the periodic body the composition root schedules on the
// runtime scheduler (its registration belongs to the wiring commit). Only
// the active writer sweeps; tenants come from the engine's enumeration,
// never from the module. A per-tenant failure does not stop the others.
func (m *Module) SweepPump(l Leader, tenants func(context.Context) ([]model.TenantID, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		if l == nil || !l.Active() {
			return nil
		}
		list, err := tenants(ctx)
		if err != nil {
			return nil
		}
		var errs []error
		for _, t := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.SweepDue(ctx, t); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// SweepDue settles one tenant's stale dispatches and observes its uncertain
// intents. It never dispatches. A dispatching intent past dispatch_deadline
// plus the skew, in database time, becomes uncertain: a crashed or silent
// dispatcher cannot prove its request was not sent. Uncertain intents are
// then observed; only a matching effect ends their uncertainty.
func (m *Module) SweepDue(ctx context.Context, tenant model.TenantID) error {
	if !m.wired() {
		return errUnavailable
	}
	var due []Intent
	seen := map[model.ID]bool{}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		now := txNow(ctx, sc, m.now())
		for _, state := range []string{StateDispatching, StateUncertain} {
			recs, err := listAll(ctx, sc, kindIntent, eq("state", state))
			if err != nil {
				return err
			}
			for _, r := range recs {
				if len(due) == sweepBatch {
					return nil
				}
				in := intentFrom(r)
				if seen[in.ID] {
					continue // marked uncertain earlier in this pass
				}
				seen[in.ID] = true
				if in.State == StateUncertain && !in.Observed.At.IsZero() && m.now().Sub(in.Observed.At) < m.opts.SweepObserveInterval {
					continue // observed recently: re-observation is bounded
				}
				if in.State == StateDispatching {
					if !now.After(in.DispatchDeadline.Add(m.opts.Skew)) {
						continue
					}
					in.State, in.Reason = StateUncertain, "dispatcher_silent"
					if err := appendObservation(ctx, sc, in, Observation{Source: "sweep", Result: "deadline_passed", At: now}, ""); err != nil {
						return err
					}
					var uerr error
					if in, uerr = update(ctx, sc, in); uerr != nil {
						return uerr
					}
					meta := intentMeta(in)
					meta["result"], meta["source"] = "deadline_passed", "sweep"
					if err := appendAudit(ctx, sc, "", "", "gitpublish.intent.reconciled", kindIntent, in.ID, meta); err != nil {
						return err
					}
				}
				due = append(due, in)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	c := Caller{Tenant: tenant}
	for _, in := range due {
		if _, err := m.observe(ctx, c, in, "sweep", ""); err != nil {
			return err
		}
	}
	return nil
}
