// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// restoreOperation is private to one minimal PostgreSQL control transaction. Its
// constructor owns the actual restore-exclusive session, then the migration lock,
// then this transaction on that same session. No constructor or method is exposed
// through engine, Config, reports, context values, or a Store interface.
//
// This is not the later local custody/receipt maintenance handle. It cannot open
// a Store, run shared preparation, execute payloads, or authorize COMPLETE.
type restoreOperation struct {
	mu    sync.Mutex
	tx    *sql.Tx
	coord *drCoordination
	roles restoreControlRoles
	dest  drDestination
	spec  PendingRestoreSpec
	now   time.Time
}

type restorePredecessor struct {
	revision int64
	state    string
}

// requireLive is called with mu held. Historical acquisition flags are never
// proof: ask this transaction's backend for both exact owned lock keys and the
// measured database/schema/role identities. Never reconnect or reacquire here.
func (op *restoreOperation) requireLive(ctx context.Context) error {
	if op.tx == nil || op.coord == nil || !op.coord.exclusive {
		return fmt.Errorf("%w: no live exclusive restore operation", ErrRestoreControlConflict)
	}
	var bound bool
	err := op.tx.QueryRowContext(ctx, `SELECT
 (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database())=$1
 AND (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_namespace WHERE nspname=$2)=$3
 AND CURRENT_USER=$4 AND SESSION_USER=$4
 AND (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_roles WHERE rolname=CURRENT_USER)=$5
 AND NOT pg_catalog.pg_is_in_recovery()
 AND pg_catalog.current_setting('transaction_read_only')='off'
 AND pg_catalog.pg_backend_pid()=$7
 AND (SELECT backend_start FROM pg_catalog.pg_stat_activity WHERE pid=pg_catalog.pg_backend_pid())=$8
 AND NOT EXISTS (
  SELECT 1 FROM (VALUES
   (pg_catalog.hashtextextended($6 || pg_catalog.current_database() || ':' || $2,0)),
   (`+migrationLockKeyExpr+`)
  ) AS required(key)
  WHERE NOT EXISTS (
   SELECT 1 FROM pg_catalog.pg_locks l
   WHERE l.locktype='advisory' AND l.pid=pg_catalog.pg_backend_pid()
   AND l.database=$1::pg_catalog.oid AND l.granted AND l.mode='ExclusiveLock'
   AND l.classid=((required.key>>32)&4294967295)::pg_catalog.oid
   AND l.objid=(required.key&4294967295)::pg_catalog.oid AND l.objsubid=1
  )
 )`, op.dest.DatabaseOID, op.dest.Schema, op.dest.SchemaOID, op.roles.owner, op.roles.ownerOID, drRestoreLockName+":", op.coord.backendPID, op.coord.backendStart).Scan(&bound)
	if err != nil || !bound {
		return fmt.Errorf("%w: exclusive restore operation binding unavailable: %v", ErrRestoreControlConflict, err)
	}
	return nil
}

// drRestoreControlTransitionQuery updates the control for a noncomplete transition.
// Schema and table names are compile-time constants; runtime values use placeholders.
const drRestoreControlTransitionQuery = `UPDATE ` + dialect.EngineSchema + `.` + dialect.DRRestoreControlTable + `
 SET revision=revision OPERATOR(pg_catalog.+) 1,state=$1,observed_at=$2
 WHERE control_key=$3 AND revision=$4 AND state=$5 AND op_id=$6
 AND plan_sha256=pg_catalog.decode($7,'hex') AND keyset_sha256 IS NULL
 AND destination_database=$8 AND destination_schema=$9 AND destination_system_identifier=$10`

// transition has no production caller in this lot. Future noncomplete failure
// handling may use it only from this actual operation lifetime. Every field in
// the CAS is bound internally, except the exact predecessor and closed next state.
// COMPLETE has no branch, keyset parameter, ready token, constructor or finalizer.
func (op *restoreOperation) transition(ctx context.Context, predecessor restorePredecessor, next string) (RestoreControlReport, error) {
	if op == nil {
		return RestoreControlReport{}, fmt.Errorf("%w: no restore operation", ErrRestoreControlConflict)
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if err := op.requireLive(ctx); err != nil {
		return RestoreControlReport{}, err
	}
	if err := validatePendingRestoreSpec(op.spec); err != nil {
		return RestoreControlReport{}, err
	}
	gate := verifyDRRestoreControl(ctx, op.tx, op.dest, op.roles)
	switch gate.Verdict {
	case drGateAbsent:
		return RestoreControlReport{}, fmt.Errorf("%w: no predecessor control", ErrRestoreControlConflict)
	case drGateUnreadable, drGateMalformed:
		return RestoreControlReport{}, fmt.Errorf("%w: %v", ErrRestoreControlConflict, gate.Cause)
	}
	if predecessor.revision <= 0 || gate.Revision != predecessor.revision || gate.Verdict.String() != predecessor.state || gate.OpID != op.spec.OpID || gate.PlanSHA256 != op.spec.PlanSHA256 {
		return RestoreControlReport{}, fmt.Errorf("%w: control is not this operation's exact predecessor", ErrRestoreControlConflict)
	}
	allowed := (predecessor.state == opgate.StatePending && (next == opgate.StateIndeterminate || next == opgate.StateQuarantined)) ||
		(predecessor.state == opgate.StateIndeterminate && next == opgate.StateQuarantined)
	if !allowed {
		return RestoreControlReport{}, fmt.Errorf("%w: noncomplete transition %q to %q is unavailable", ErrRestoreControlConflict, predecessor.state, next)
	}
	res, err := op.tx.ExecContext(ctx, drRestoreControlTransitionQuery,
		next, op.now, dialect.DRRestoreControlKey, predecessor.revision, predecessor.state, op.spec.OpID, op.spec.PlanSHA256,
		op.dest.Database, op.dest.Schema, op.dest.SystemIdentifier)
	if err != nil {
		return RestoreControlReport{}, fmt.Errorf("sqlstore: transition the restore control: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return RestoreControlReport{}, err
	}
	if n != 1 {
		return RestoreControlReport{}, fmt.Errorf("%w: transition matched %d rows", ErrRestoreControlConflict, n)
	}
	return readBackRestoreControl(ctx, op.tx, op.dest, op.roles)
}

// restoreOperationBeforeCommitTestHook runs on the original exclusive holder
// immediately before the live check that authorizes Commit. Nil outside tests.
var restoreOperationBeforeCommitTestHook func(*restoreOperation)

func (op *restoreOperation) commit(ctx context.Context) error {
	op.mu.Lock()
	defer op.mu.Unlock()
	if restoreOperationBeforeCommitTestHook != nil {
		restoreOperationBeforeCommitTestHook(op)
	}
	tx := op.tx
	if err := op.requireLive(ctx); err != nil {
		return err
	}
	op.tx = nil
	return tx.Commit()
}

// Invalidate captured private references before the outer owner releases locks.
// Rollback itself remains the transaction owner's deferred cleanup on every path.
func (op *restoreOperation) retire() {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.tx = nil
}
