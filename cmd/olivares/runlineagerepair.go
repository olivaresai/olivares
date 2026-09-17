// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// runlineagerepair.go (P2 / W3) is the engine's bounded run-lineage maintenance
// loop. Per tick it drives the sessions module's exported, tenant-scoped
// RepairRunLineage page by page for every served business tenant, until the pass
// it started is exhausted. The module owns every semantic: candidate selection,
// the same-transaction identity and workspace recheck, and the exact version
// compare-and-swap. This loop owns only cadence, tenant enumeration and pass
// custody, on the runtime's EXISTING periodic scheduler (the retention-sweep and
// eventing-pump precedent, never a parallel timer).
//
// It is not a request-side repair. A run created before its authorization lineage
// existed stays hidden from every confined reader until a pass resolves it; no
// Stop, read or route repairs on demand.
//
// HA: only the ACTIVE writer repairs. Leadership is checked before every tenant and
// before every page, so a demoted node starts no further page, and the store's
// write gate refuses any Mutate that begins after the demotion. A page whose
// transaction had already begun may still commit; each of its writes is
// conditioned on the row's exact version and on its lineage still being NULL. A
// restart or a promoted standby starts a new pass, and repeating a pass is safe
// for the same reason.
//
// Each page runs through the module's ordinary data handle, which boot binds to the
// composed store. A tenant pinned to another region or whose service is withdrawn
// is refused there exactly as on every other path; servedBusinessTenants only stops
// the loop from attempting that work.

const (
	// runLineageRepairJobName is the runtime scheduler's job name.
	runLineageRepairJobName = "sessions.run_lineage_repair"
	// defaultRunLineageRepairInterval is the job-owned cadence Root selected for the
	// initial bounded implementation. It is not an operator setting.
	defaultRunLineageRepairInterval = time.Minute
)

// runLineageRepairInterval is the cadence boot registers. Production never assigns
// it; a boot-level test shortens it to observe the scheduled job without waiting a
// minute.
var runLineageRepairInterval = defaultRunLineageRepairInterval

// errRunLineageCursorStalled refuses a continuation that does not keep the pass's
// captured bound or does not advance: following it could repeat pages forever.
var errRunLineageCursorStalled = errors.New("run-lineage-repair: the repair cursor did not advance within its captured bound")

// runLineageRepairer is the one module operation the loop drives.
type runLineageRepairer interface {
	RepairRunLineage(ctx context.Context, tenant model.TenantID, cursor sessions.RunLineageRepairCursor) (sessions.RunLineageRepairResult, error)
}

// runLineageRepairLoop drives the periodic tenant-scoped lineage repair.
type runLineageRepairLoop struct {
	st       store.Store
	repair   runLineageRepairer
	interval time.Duration
	log      *slog.Logger
}

// newRunLineageRepairLoop builds the loop over the composed store and the sessions
// module. It is nil only when the sessions module is not composed, in which case
// there are no runs to repair.
func newRunLineageRepairLoop(st store.Store, sm *sessions.Module, log *slog.Logger) *runLineageRepairLoop {
	if st == nil || sm == nil {
		return nil
	}
	return &runLineageRepairLoop{st: st, repair: sm, interval: runLineageRepairInterval, log: log}
}

// register schedules the loop on the runtime's own scheduler. It must run before
// Start: the scheduler refuses later registrations.
func (l *runLineageRepairLoop) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(runLineageRepairJobName, l.interval, false, l.runOnce)
}

// runLineagePass is what one pass over one tenant observed. The outcomes stay
// separate: a row that lost its compare-and-swap and a row whose identity cannot
// establish a workspace are different facts, and neither is exhaustion.
type runLineagePass struct {
	Pages      int
	Scanned    int
	Repaired   int
	Conflicts  int
	Unresolved int
	// Exhausted is true only when the module reported that the cursor passed the
	// bound captured at the start of the pass.
	Exhausted bool
	// Stopped names why the pass ended before exhaustion without an error; it is
	// "leadership" when this node stopped being the active writer.
	Stopped string
}

func (p runLineagePass) attrs(tenant model.TenantID) []any {
	return []any{
		"tenant", tenant.String(), "pages", p.Pages, "scanned", p.Scanned,
		"repaired", p.Repaired, "conflicts", p.Conflicts, "unresolved", p.Unresolved,
		"exhausted", p.Exhausted,
	}
}

// runOnce repairs every served business tenant. A tenant failure is logged and the
// remaining tenants still run. Cancellation of the engine lifecycle and loss of
// leadership end the tick. Logged fields are counts only, never row content.
func (l *runLineageRepairLoop) runOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.st.Leader().Active() {
		l.log.Debug("run-lineage-repair skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := servedBusinessTenants(ctx, l.st)
	if err != nil {
		// An enumeration cut short by the engine lifecycle is shutdown, not a fault.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		l.log.Warn("run-lineage-repair: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		pass, err := l.repairTenant(ctx, t)
		switch {
		case err != nil && ctx.Err() != nil:
			l.log.Info("run-lineage-repair: pass interrupted by the engine lifecycle; a later pass repeats it safely",
				pass.attrs(t)...)
			return ctx.Err()
		case err != nil:
			l.log.Warn("run-lineage-repair: tenant pass failed; continuing with the remaining tenants",
				append(pass.attrs(t), "err", err)...)
		case pass.Stopped != "":
			l.log.Info("run-lineage-repair: pass stopped before exhaustion; the active writer starts a new pass",
				append(pass.attrs(t), "stopped", pass.Stopped)...)
			return nil
		case pass.Scanned > 0:
			l.log.Info("run-lineage-repair: tenant pass exhausted", pass.attrs(t)...)
		}
	}
	return nil
}

// repairTenant runs one complete pass over one tenant: the zero cursor starts it,
// each page's Next continues it with the same captured bound, and only the module's
// Exhausted ends it. A page that repaired nothing is never taken as completion.
func (l *runLineageRepairLoop) repairTenant(ctx context.Context, tenant model.TenantID) (runLineagePass, error) {
	var pass runLineagePass
	var cursor sessions.RunLineageRepairCursor
	for {
		if err := ctx.Err(); err != nil {
			return pass, err
		}
		if !l.st.Leader().Active() {
			pass.Stopped = "leadership"
			return pass, nil
		}
		page, err := l.repair.RepairRunLineage(ctx, tenant, cursor)
		if err != nil {
			return pass, err
		}
		pass.Pages++
		pass.Scanned += page.Scanned
		pass.Repaired += page.Repaired
		pass.Conflicts += page.Conflicts
		pass.Unresolved += page.Unresolved
		if page.Exhausted {
			pass.Exhausted = true
			return pass, nil
		}
		next := page.Next
		if next.UpperBound.IsZero() || next.After <= cursor.After ||
			(!cursor.UpperBound.IsZero() && next.UpperBound != cursor.UpperBound) {
			return pass, fmt.Errorf("%w: from (%s, %s] to (%s, %s]", errRunLineageCursorStalled,
				cursor.After, cursor.UpperBound, next.After, next.UpperBound)
		}
		cursor = next
	}
}
