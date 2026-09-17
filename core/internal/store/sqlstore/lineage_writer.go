// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const lineageGateKey = "core.lineage.writer.class.v8"
const lineageTenantKeyPrefix = "core.lineage.writer.tenant.v8:"

// L0 shared + L1 tenant precede the Mutate callback. A System mutator takes
// L0 exclusive before discovering targets; System reads take neither gate.
// These gates coordinate writers only. A read barrier never calls this code.
type lineageWriteTracker struct {
	tx       *sql.Tx
	dia      dialect.Dialect
	id       string
	started  bool
	system   bool
	armed    map[model.TenantID]bool
	poisoned error
}

func newLineageWriteTracker(tx *sql.Tx, dia dialect.Dialect, system bool) *lineageWriteTracker {
	return &lineageWriteTracker{tx: tx, dia: dia, system: system, armed: make(map[model.TenantID]bool)}
}
func (t *lineageWriteTracker) poison(err error) {
	if err != nil && t.poisoned == nil {
		t.poisoned = err
	}
}
func (t *lineageWriteTracker) start(ctx context.Context, tenant model.TenantID) (retErr error) {
	defer func() { t.poison(retErr) }()
	if t.poisoned != nil {
		return t.poisoned
	}
	if t.started {
		return nil
	}
	if !t.system && tenant.IsSystem() {
		return nil
	}
	if err := lockLineageWriterClass(ctx, t.tx, t.dia, t.system); err != nil {
		return err
	}
	if t.dia.Name() == store.EnginePostgres {
		if !t.system {
			if _, err := canonicalDirectoryTenants([]model.TenantID{tenant}); err != nil {
				return err
			}
			if _, err := t.tx.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))", lineageTenantKeyPrefix+tenant.String()); err != nil {
				return err
			}
		}
	}
	value := "lower(hex(randomblob(16)))"
	if t.dia.Name() == store.EnginePostgres {
		value = "pg_catalog.txid_current()::text"
	}
	if err := t.tx.QueryRowContext(ctx, "SELECT "+value).Scan(&t.id); err != nil {
		return err
	}
	t.started = true
	if !t.system {
		return t.arm(ctx, tenant)
	}
	return nil
}

// lockLineageWriterClass is the L0 half shared by legacy lineage writers and
// narrow selective business transactions. System takes it exclusive; business
// takes it shared. It performs no L1 acquisition, writer enrollment or epoch
// operation by itself.
func lockLineageWriterClass(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	system bool,
) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	fn := "pg_catalog.pg_advisory_xact_lock_shared"
	if system {
		fn = "pg_catalog.pg_advisory_xact_lock"
	}
	_, err := tx.ExecContext(ctx, "SELECT "+fn+"(pg_catalog.hashtextextended($1,0))", lineageGateKey)
	return err
}
func (t *lineageWriteTracker) arm(ctx context.Context, tenant model.TenantID) (retErr error) {
	defer func() { t.poison(retErr) }()
	if tenant.IsSystem() {
		return nil
	}
	if t.poisoned != nil {
		return t.poisoned
	}
	if !t.started {
		return lineageUnavailable("writer not started", nil)
	}
	if t.armed[tenant] {
		return nil
	}
	if _, err := canonicalDirectoryTenants([]model.TenantID{tenant}); err != nil {
		return err
	}
	if t.dia.Name() == store.EnginePostgres {
		if _, err := t.tx.ExecContext(ctx, "SELECT public.olivares_lineage_begin($1)", tenant.String()); err != nil {
			return err
		}
	} else {
		if _, err := t.tx.ExecContext(ctx, "INSERT INTO main.core_lineage_writer(writer_id,tenant_id) VALUES (?,?)", t.id, tenant.String()); err != nil {
			return err
		}
	}
	t.armed[tenant] = true
	return nil
}
func (t *lineageWriteTracker) finish(ctx context.Context) error {
	if !t.started {
		return nil
	}
	if t.poisoned != nil {
		return t.poisoned
	}
	if t.dia.Name() == store.EnginePostgres {
		_, err := t.tx.ExecContext(ctx, "SELECT public.olivares_lineage_finish()")
		return err
	}
	_, err := t.tx.ExecContext(ctx, "DELETE FROM main.core_lineage_writer WHERE writer_id=?", t.id)
	return err
}
func (sys *systemScope) beginLineageMutation(ctx context.Context) error {
	if sys.lineageWriter == nil {
		return lineageUnavailable("System writer absent", nil)
	}
	err := sys.lineageWriter.start(ctx, "")
	sys.poison(err)
	return err
}

type lineageTrackedRepo[T any] struct {
	store.Repository[T]
	tracker *lineageWriteTracker
}

func (r *lineageTrackedRepo[T]) Create(ctx context.Context, in T) (_ T, retErr error) {
	defer func() { r.tracker.poison(retErr) }()
	return r.Repository.Create(ctx, in)
}
func (r *lineageTrackedRepo[T]) Update(ctx context.Context, in T) (_ T, retErr error) {
	defer func() { r.tracker.poison(retErr) }()
	return r.Repository.Update(ctx, in)
}
func (r *lineageTrackedRepo[T]) Delete(ctx context.Context, id model.ID) (retErr error) {
	defer func() { r.tracker.poison(retErr) }()
	return r.Repository.Delete(ctx, id)
}
func (r *lineageTrackedRepo[T]) Lock(ctx context.Context, id model.ID) (T, error) {
	if lock, ok := r.Repository.(store.RowLocker[T]); ok {
		return lock.Lock(ctx, id)
	}
	var zero T
	return zero, fmt.Errorf("lineage repository lacks row locking")
}
