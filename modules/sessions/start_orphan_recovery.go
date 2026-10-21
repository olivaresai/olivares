// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// orphanedRunsPage is how many rows one listing of a tenant's pending or running
// runs reads.
const orphanedRunsPage = 200

// stopOrphanedRuns reads as stopped the runs a crashed engine left running. At
// Start this engine holds no process, so a row that is still pending or running
// belongs to the engine that died: nothing else would end it until somebody asked
// to stop it, and a dead session showed as running.
//
// Only the serving leader does it, and only where one engine owns the rows: the
// composition names its tenants only when serving (an offline command must not
// stop a live engine's runs), a standby is not the writer, and with communication
// credentials on another runtime may hold the row's process, so leader recovery
// (RecoverRuntimeCredentials) decides for it. Best effort: a run that cannot be
// stopped is logged and keeps its row, and the next start tries again.
func (m *Module) stopOrphanedRuns(ctx context.Context) {
	if m.rt.ApprovalRecoveryTenants == nil || m.rt.CommunicationCredentialsEnabled || m.Data == nil || !m.servesAsWriter() {
		return
	}
	tenants, err := m.rt.ApprovalRecoveryTenants(ctx)
	if err != nil {
		m.warnf("sessions: cannot list the tenants whose runs the engine died with; they are read as stopped when somebody stops them",
			"err", redactErr(err))
		return
	}
	for _, tenant := range tenants {
		if err := m.stopOrphanedTenantRuns(ctx, tenant, orphanedRunsPage); err != nil {
			m.warnf("sessions: cannot read this tenant's runs the engine died with as stopped; the next start tries again",
				"tenant", tenant.String(), "err", redactErr(err))
		}
	}
}

// servesAsWriter reports whether this node may write run rows: the store's leader
// fence that managed Stop already uses, true on a single node. Stopping a row
// revokes its credentials before the row is written, so a standby must not start.
func (m *Module) servesAsWriter() bool {
	a := m.rt.ManagedStopAuthority
	return a == nil || a.elector == nil || a.elector.Active()
}

func (m *Module) stopOrphanedTenantRuns(ctx context.Context, tenant model.TenantID, pageSize int) error {
	q := model.Query{Limit: pageSize, Filters: []model.Filter{{Column: colState, Op: model.OpIn, Value: []string{statePending, stateRunning}}}}
	for {
		var rows []model.Record
		var page model.Page
		if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rows, page, err = repo.List(ctx, q)
			return err
		}); err != nil {
			return err
		}
		for _, row := range rows {
			runRef := row.String(colRunRef)
			if _, held := m.rt.getLive(tenant, runRef); held {
				continue
			}
			if _, err := m.reconcileTerminal(ctx, tenant, runRef, row, model.ActorSystem, model.ActorSystem, engineDiedDetail); err != nil {
				m.warnf("sessions: cannot stop a run the engine died with; it keeps its row until it is stopped",
					"tenant", tenant.String(), "run_ref", runRef, "err", redactErr(err))
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return nil
		}
		if q.Cursor == page.Cursor {
			return errors.New("orphaned run listing cursor did not advance")
		}
		q.Cursor = page.Cursor
	}
}
