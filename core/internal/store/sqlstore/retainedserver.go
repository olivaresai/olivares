// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// retainedServerChallenge owns only a read transaction, never its connection.
// The holder stays on that transaction across every actual pinned witness. The
// owner must roll it back successfully before starting any DDL transaction.
type retainedServerChallenge struct {
	tx         *sql.Tx
	key        int64
	database   string
	backendPID int
}

func beginRetainedServerChallenge(ctx context.Context, conn *sql.Conn) (_ *retainedServerChallenge, err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin the challenge transaction: %w", err)
	}
	c := &retainedServerChallenge{tx: tx}
	ready := false
	defer func() {
		if !ready {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var restoreKey, migrationKey int64
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.current_database(),
 pg_catalog.hashtextextended($1 || pg_catalog.current_database() || ':' || $2,0),
 `+migrationLockKeyExpr+`, pg_catalog.pg_backend_pid()`, drRestoreLockName+":", dialect.EngineSchema).Scan(&c.database, &restoreKey, &migrationKey, &c.backendPID); err != nil {
		return nil, fmt.Errorf("read the challenge holder identity: %w", err)
	}
	// Even the negligible random collision is refused: the challenge never uses
	// the restore or migration key. A finite draw budget also bounds that path.
	for range 4 {
		key, err := randomDirectoryActivationLockKey()
		if err != nil {
			return nil, fmt.Errorf("draw a challenge key: %w", err)
		}
		if key == restoreKey || key == migrationKey {
			continue
		}
		c.key = key
		if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, key); err != nil {
			return nil, fmt.Errorf("hold the challenge key: %w", err)
		}
		ready = true
		return c, nil
	}
	return nil, errors.New("could not draw a distinct live-server challenge key")
}

type retainedServerVerdict struct {
	store.SameServerVerdict
	backendPID int
}

func (c *retainedServerChallenge) witness(ctx context.Context, conn *sql.Conn, label string) (v retainedServerVerdict) {
	v.Label = label
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		v.Err = fmt.Errorf("begin the witness transaction: %w", err).Error()
		return v
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			v.SameServer = false
			v.Err = fmt.Errorf("rollback the witness transaction: %w", err).Error()
		}
	}()
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&v.backendPID); err != nil {
		v.Err = fmt.Errorf("read the witness backend identity: %w", err).Error()
		return v
	}
	facts, err := askAdvisoryLockWitness(ctx, tx, c.key, label)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	v.Database = facts.Database
	v.AcquiredHoldersLock = facts.Acquired
	v.SameServer = !facts.Acquired && facts.Database == c.database
	return v
}

func (c *retainedServerChallenge) rollback() error { return c.tx.Rollback() }
