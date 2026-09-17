// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// drCoordinationAcquisitions counts successful lock takes on this process. It
// exists so IR3 can prove ordinary publication acquired once and never
// reconnected; it is not a capability.
var drCoordinationAcquisitions atomic.Int64

// The coordination errors. They are sentinels because a caller has to be able to
// tell "a restore holds this destination" from "I could not find out", and because
// collapsing the second into the first would report a fence that may not exist.
var (
	// ErrRestorePublicationBusy means a restore holds publication exclusion on this
	// destination RIGHT NOW. It is produced inside ONE try, without waiting.
	ErrRestorePublicationBusy = errors.New("sqlstore: a restore holds publication exclusion on this destination")
	// ErrRestoreCoordinationUnknown means coordination could not be established at
	// all — the session, the deadline or the query failed. It is never downgraded to
	// "no restore is running".
	ErrRestoreCoordinationUnknown = errors.New("sqlstore: restore publication coordination could not be established")
)

// drCoordinationTimeout bounds getting the coordination session and asking it the
// one question it exists to ask.
//
// It is FINITE and short on purpose, and it is not derived from a migration budget:
// the whole contract of the shared side is that it never waits for a restore to
// finish. A boot that cannot establish coordination within this bound reports that
// it could not, which is a different verdict from "nothing is running".
const drCoordinationTimeout = 10 * time.Second

// drRestoreLockName is the advisory key's stable prefix. It is neither the leader
// key nor the migration key nor the random same-server challenge key: sharing any
// of those would make one operation's fence another operation's coincidence.
const drRestoreLockName = "olivares.restore.v1"

// drDestination is the exact identity of the destination a control is bound to.
//
// SystemIdentifierKnown is a separate field rather than an empty string, because a
// cluster identity that could not be READ and one that is genuinely absent are
// different facts and only the first one forbids a comparison.
type drDestination struct {
	Database string
	Schema   string
	// Catalog identities are measured on retained control-operation sessions.
	// Ordinary publication's final session decision remains a separate sublot.
	DatabaseOID           int64
	SchemaOID             int64
	SystemIdentifier      string
	SystemIdentifierKnown bool
	SystemIdentifierErr   error
}

// matches reports whether a control's recorded binding names THIS destination.
//
// The cluster identity participates only when both sides know it. An unreadable
// identity does not silently match; it makes the comparison unavailable, and the
// caller (judgeDRRestoreGate) turns that into a refusal wherever a control exists.
func (d drDestination) matches(database, schema, systemIdentifier string) (bool, string) {
	if d.Database != database {
		return false, fmt.Sprintf("database %q, control names %q", d.Database, database)
	}
	if d.Schema != schema {
		return false, fmt.Sprintf("schema %q, control names %q", d.Schema, schema)
	}
	if d.SystemIdentifierKnown && systemIdentifier != "" && d.SystemIdentifier != systemIdentifier {
		return false, fmt.Sprintf("cluster %q, control names %q", d.SystemIdentifier, systemIdentifier)
	}
	return true, ""
}

// drCoordination is one held publication/restore coordination session.
//
// THE POOL IS ITS OWN, and that is a correctness requirement rather than a
// resource preference. cfg.MaxConns caps the application pool (openPostgres), the
// owner pool (openOwnerPool) and the admin pool (openAdminPool) alike, so a
// coordination connection taken from any of them is one the boot it is coordinating
// can no longer have — at MaxConns=1 that is not contention, it is a boot that
// blocks on itself. The precedent is the migration block observer, whose pool lives
// outside cfg.MaxConns for the same reason: competing with the thing you are
// diagnosing deadlocks the diagnosis against it.
type drCoordination struct {
	pool      *sql.DB
	conn      *sql.Conn
	exclusive bool
	// acquired records whether the advisory lock was actually taken. close() must
	// not attempt an unlock it never earned: pg_advisory_unlock returns false for a
	// lock this session does not hold, and reporting that as a failed release would
	// turn every ordinary busy verdict into a spurious cleanup warning.
	// It is HISTORY, not current ownership: observeHeld must re-read pg_locks.
	acquired     bool
	backendPID   int
	backendStart time.Time
	lockKey      int64
	dest         drDestination
	released     bool
}

// openDRCoordinationShared is what an ORDINARY publication decision takes: exactly
// one pg_try_advisory_lock_shared under a finite deadline.
//
// ok=false is the busy verdict. There is no polling, no sleep, no retry and no
// budget borrowed from migration coordination: waiting here would mean holding a
// boot open for the length of a restore, which is precisely what the durable
// control exists to avoid.
func openDRCoordinationShared(ctx context.Context, cfg store.Config) (*drCoordination, error) {
	return openDRCoordination(ctx, cfg.DSN, false, cfg)
}

// openDRCoordinationExclusive is what an operation that CHANGES the destination
// takes. It is also a single try in this cut: a second restore is told to wait for
// the first rather than queued behind it.
func openDRCoordinationExclusive(ctx context.Context, dsn string, cfg store.Config) (*drCoordination, error) {
	return openDRCoordination(ctx, dsn, true, cfg)
}

func openDRCoordination(ctx context.Context, dsn string, exclusive bool, cfg store.Config) (*drCoordination, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%w: no DSN for the coordination session", ErrRestoreCoordinationUnknown)
	}
	pool, err := openPGPinnedToEngineSchema(dsn, 1)
	if err != nil {
		return nil, fmt.Errorf("%w: open the coordination pool: %v", ErrRestoreCoordinationUnknown, err)
	}
	// Exactly one physical session backs the lock: a session-level advisory lock
	// lives on the connection that took it, and the same connection must be the one
	// that reads the control and later releases it.
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(0)

	bctx, cancel := context.WithTimeout(ctx, drCoordinationTimeout)
	defer cancel()

	conn, err := pool.Conn(bctx)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("%w: take the coordination connection: %v", ErrRestoreCoordinationUnknown, err)
	}
	c := &drCoordination{pool: pool, conn: conn, exclusive: exclusive}

	if err := c.readDestination(bctx); err != nil {
		_ = c.close()
		return nil, err
	}
	// The key is derived from the DESTINATION, not from a constant: advisory locks
	// are cluster-wide, so one key for every database would let a restore of estate A
	// fence estate B and, worse, let two restores of different estates believe they
	// were the same operation. The schema is the pinned engine schema rather than
	// current_schema(), because every pool this package opens is pinned to it and a
	// key that followed a search_path would differ between two sessions on one
	// destination.
	fn := "pg_catalog.pg_try_advisory_lock_shared"
	if exclusive {
		fn = "pg_catalog.pg_try_advisory_lock"
	}
	var acquired bool
	err = c.conn.QueryRowContext(bctx,
		"SELECT "+fn+"(pg_catalog.hashtextextended($1 || $2 || ':' || $3, 0))",
		drRestoreLockName+":", c.dest.Database, c.dest.Schema,
	).Scan(&acquired)
	if err != nil {
		_ = c.close()
		return nil, fmt.Errorf("%w: ask for the publication fence: %v", ErrRestoreCoordinationUnknown, err)
	}
	if !acquired {
		return nil, c.busyRefusal(bctx, cfg)
	}
	c.acquired = true
	if err := c.captureHeldIdentity(bctx); err != nil {
		_ = c.close()
		return nil, err
	}
	drCoordinationAcquisitions.Add(1)
	return c, nil
}

// busyRefusal renders the fenced verdict, and it is where OP_ID is EARNED rather
// than invented.
//
// The holder may not have installed its control yet — a restore takes the fence
// before it writes anything — so this reads the control once, on the session it is
// about to retire, within the budget it already has, and names the operation ONLY
// if a well-formed control was actually there. It never substitutes a PID, a
// placeholder or the holder's backend identity: the fenced diagnosis is sufficient
// on its own, and an invented identity would be worse than none.
func (c *drCoordination) busyRefusal(ctx context.Context, cfg store.Config) error {
	gate := drGate{Verdict: drGateUnreadable}
	// Spend only the original one-try deadline. The configured authorities are
	// measured independently; the table cannot nominate its own expected owner.
	roles, err := resolveDRControlRoles(ctx, cfg)
	if err == nil {
		gate = verifyDRRestoreControl(ctx, c.conn, c.dest, roles)
	}
	_ = c.close()
	switch gate.Verdict {
	case drGatePending, drGateIndeterminate, drGateQuarantined, drGateComplete:
		if gate.OpID != "" {
			return fmt.Errorf("%w: %s.%s is held by operation %s (state %q); nothing here waits for it to finish",
				ErrRestorePublicationBusy, c.dest.Database, c.dest.Schema, gate.OpID, gate.Verdict)
		}
	}
	return fmt.Errorf("%w: %s.%s is held by another operation, which has no verified control bound to this destination — so no operation identity is available and none is invented; nothing here waits for it to finish",
		ErrRestorePublicationBusy, c.dest.Database, c.dest.Schema)
}

// readDestination resolves the identity in the SAME session that holds the fence,
// so the binding a control is compared against is a fact about this connection
// rather than about a string somebody configured.
func (c *drCoordination) readDestination(ctx context.Context) error {
	if err := c.conn.QueryRowContext(ctx,
		"SELECT pg_catalog.current_database(), pg_catalog.pg_backend_pid()").Scan(&c.dest.Database, &c.backendPID); err != nil {
		return fmt.Errorf("%w: read the destination database: %v", ErrRestoreCoordinationUnknown, err)
	}
	c.dest.Schema = dialect.EngineSchema
	// The cluster identity is read BEST EFFORT and its failure is REMEMBERED rather
	// than swallowed. EXECUTE on pg_control_system() is granted to PUBLIC by initdb,
	// so a failure here means a hardened catalogue — which must not become a new
	// refusal for a destination that carries no control at all, and must not become
	// a silent match for one that does. judgeDRRestoreGate is where that distinction
	// is spent.
	var identifier string
	if err := c.conn.QueryRowContext(ctx,
		"SELECT control.system_identifier::pg_catalog.text FROM pg_catalog.pg_control_system() AS control",
	).Scan(&identifier); err != nil {
		c.dest.SystemIdentifierErr = err
		return nil
	}
	c.dest.SystemIdentifier = identifier
	c.dest.SystemIdentifierKnown = true
	return nil
}

// destination reports the identity this session resolved.
func (c *drCoordination) destination() drDestination { return c.dest }

// querier is the ONLY handle this type hands out, and it is read-shaped: the fence
// and the control read have to happen on one session or the read is not serialized
// by the lock that makes it meaningful.
func (c *drCoordination) querier() rowQuerier { return c.conn }

// captureHeldIdentity freezes the exact backend that took the lock. A PID alone
// is not transferable proof: the start instant and destination OIDs bind it.
func (c *drCoordination) captureHeldIdentity(ctx context.Context) error {
	priorPID := c.backendPID
	err := c.conn.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid(), a.backend_start,
 (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_database WHERE datname = pg_catalog.current_database()),
 (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_namespace WHERE nspname = $1),
 pg_catalog.hashtextextended($2 || pg_catalog.current_database() || ':' || $1, 0)
 FROM pg_catalog.pg_stat_activity a WHERE a.pid = pg_catalog.pg_backend_pid()`,
		dialect.EngineSchema, drRestoreLockName+":").Scan(
		&c.backendPID, &c.backendStart, &c.dest.DatabaseOID, &c.dest.SchemaOID, &c.lockKey)
	if err != nil {
		return fmt.Errorf("%w: capture the held coordination identity: %v", ErrRestoreCoordinationUnknown, err)
	}
	if priorPID != 0 && priorPID != c.backendPID {
		return fmt.Errorf("%w: the coordination connection changed backends after taking the fence", ErrRestoreCoordinationUnknown)
	}
	if c.backendPID == 0 || c.backendStart.IsZero() || c.dest.DatabaseOID <= 0 || c.dest.SchemaOID <= 0 || c.lockKey == 0 {
		return fmt.Errorf("%w: the held coordination identity is incomplete", ErrRestoreCoordinationUnknown)
	}
	return c.observeHeld(ctx)
}

func (c *drCoordination) lockMode() string {
	if c.exclusive {
		return "ExclusiveLock"
	}
	return "ShareLock"
}

// observeHeld proves THIS original session still owns the exact advisory key.
// It inspects pg_locks; it never tries the lock again.
func (c *drCoordination) observeHeld(ctx context.Context) error {
	if c == nil || c.conn == nil || c.released {
		return fmt.Errorf("%w: no retained coordination session", ErrRestoreCoordinationUnknown)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrRestoreCoordinationUnknown, err)
	}
	var held bool
	err := c.conn.QueryRowContext(ctx, `SELECT
 pg_catalog.pg_backend_pid() = $1
 AND a.backend_start = $2
 AND (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_database WHERE datname = pg_catalog.current_database()) = $3
 AND (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_namespace WHERE nspname = $4) = $5
 AND EXISTS (
  SELECT 1 FROM pg_catalog.pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted AND l.pid = pg_catalog.pg_backend_pid()
    AND l.database = $3::pg_catalog.oid
    AND l.classid = (($6::pg_catalog.int8>>32)&4294967295)::pg_catalog.oid
    AND l.objid = ($6::pg_catalog.int8&4294967295)::pg_catalog.oid
    AND l.objsubid = 1
    AND l.mode = $7
 )
 FROM pg_catalog.pg_stat_activity a WHERE a.pid = pg_catalog.pg_backend_pid()`,
		c.backendPID, c.backendStart, c.dest.DatabaseOID, dialect.EngineSchema, c.dest.SchemaOID, c.lockKey, c.lockMode(),
	).Scan(&held)
	if err != nil {
		return fmt.Errorf("%w: observe the retained publication fence: %v", ErrRestoreCoordinationUnknown, err)
	}
	if !held {
		return fmt.Errorf("%w: the original coordination session no longer holds its publication fence", ErrRestoreCoordinationUnknown)
	}
	return nil
}

// unlockChecked asks the original session to drop the fence and reports the
// boolean PostgreSQL returned. It never reconnects or tries a different session.
func (c *drCoordination) unlockChecked(ctx context.Context) (bool, error) {
	if c == nil || c.conn == nil || !c.acquired {
		return true, nil
	}
	fn := "pg_catalog.pg_advisory_unlock_shared"
	if c.exclusive {
		fn = "pg_catalog.pg_advisory_unlock"
	}
	var released bool
	err := c.conn.QueryRowContext(ctx,
		"SELECT "+fn+"(pg_catalog.hashtextextended($1 || $2 || ':' || $3, 0))",
		drRestoreLockName+":", c.dest.Database, c.dest.Schema,
	).Scan(&released)
	if err != nil {
		return false, err
	}
	return released, nil
}

// close releases the fence and retires the session.
//
// The session is ALWAYS retired rather than pooled, on every path. A session-level
// advisory lock belongs to the session, pg_advisory_lock is re-entrant per session,
// and pgx's ResetSession does not clear advisory locks on reuse — so a pooled
// ex-fence session would answer the next try with a stale count. Ending the session
// also releases the lock server-side, which is the stronger remedy when the explicit
// unlock could not be confirmed.
//
// Cleanup failure is REPORTED, not swallowed: an unconfirmed release leaves a
// destination that may still look fenced to the next boot, and that is an operator
// fact rather than a debug detail.
func (c *drCoordination) close() error {
	if c == nil || c.released {
		return nil
	}
	c.released = true
	var firstErr error
	if c.conn != nil && c.acquired {
		// WithoutCancel: the release must still run when the boot that took it was
		// canceled, and it must still be bounded so a damaged server cannot hold the
		// caller open.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), migrationUnlockTimeout)
		released, uerr := c.unlockChecked(uctx)
		cancel()
		if uerr != nil || !released {
			firstErr = fmt.Errorf("sqlstore: the restore publication fence on %s.%s could not be confirmed released (released=%t): %w; its session is being retired",
				c.dest.Database, c.dest.Schema, released, uerr)
		}
	}
	if c.conn != nil {
		if derr := forceDiscard(c.conn); derr != nil && firstErr == nil {
			firstErr = fmt.Errorf("sqlstore: retire the restore coordination session: %w", derr)
		}
	}
	if c.pool != nil {
		if perr := c.pool.Close(); perr != nil && firstErr == nil {
			firstErr = fmt.Errorf("sqlstore: close the restore coordination pool: %w", perr)
		}
	}
	return firstErr
}
