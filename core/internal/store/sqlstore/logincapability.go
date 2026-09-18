// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// loginCapabilityLockWait caps ONE acquisition statement (ROOT-CONSTRUCTION-R5-1 §4).
// An earlier parent deadline or a stricter server timeout still wins; connection and
// server settings are never changed. Measured behavior of the child context with the
// tree's pgx is retained under an internal design note (not shipped)
const loginCapabilityLockWait = 5 * time.Second

// sqliteLoginCapabilityTimeLayout is the canonical UTC text SQLite stores.
const sqliteLoginCapabilityTimeLayout = "2006-01-02T15:04:05.000Z"

// loginCapabilityTx is the capability state of ONE transaction. It lives on the
// tenantScope, so every port handed out by that transaction's AuthScope shares it.
type loginCapabilityTx struct {
	locked      bool
	observed    bool
	observation store.LoginCapabilityObservation
	poisoned    error
}

func (s *loginCapabilityTx) poison(err error) error {
	if s.poisoned == nil {
		s.poisoned = err
	}
	return err
}

// LoginCapability returns the transaction-owned capability port.
func (a *authScope) LoginCapability() store.LoginCapabilityPort {
	return loginCapabilityPort{ts: a.ts}
}

type loginCapabilityPort struct{ ts *tenantScope }

// state returns the transaction's capability state with its mutex held.
func (p loginCapabilityPort) state() (*loginCapabilityTx, func()) {
	p.ts.loginCapabilityMu.Lock()
	if p.ts.loginCapability == nil {
		p.ts.loginCapability = &loginCapabilityTx{}
	}
	return p.ts.loginCapability, p.ts.loginCapabilityMu.Unlock
}

// loginCapabilityPoison is the envelope's view of a discarded capability failure.
func (sc *tenantScope) loginCapabilityPoison() error {
	sc.loginCapabilityMu.Lock()
	defer sc.loginCapabilityMu.Unlock()
	if sc.loginCapability == nil {
		return nil
	}
	return sc.loginCapability.poisoned
}

func (p loginCapabilityPort) Lock(ctx context.Context) (store.LoginCapabilityObservation, error) {
	st, unlock := p.state()
	defer unlock()
	if p.ts.readOnly {
		return store.LoginCapabilityObservation{}, store.ErrReadOnly
	}
	if st.poisoned != nil {
		return store.LoginCapabilityObservation{}, st.poisoned
	}
	if st.locked {
		return st.observation, nil
	}
	if err := p.orderError(); err != nil {
		return store.LoginCapabilityObservation{}, st.poison(err)
	}
	if err := acquireLoginCapabilityLock(ctx, p.ts.tx, p.ts.s.dia); err != nil {
		return store.LoginCapabilityObservation{}, st.poison(err)
	}
	obs, err := readLoginCapability(ctx, p.ts.tx, p.ts.s.dia)
	if err != nil {
		return store.LoginCapabilityObservation{}, st.poison(err)
	}
	st.locked = true
	st.observation = obs
	return obs, nil
}

// orderError consults the actual transaction lock facts the directory writer and
// tenant scope record. It does not claim that arbitrary earlier reads are tracked.
func (p loginCapabilityPort) orderError() error {
	var held []string
	if dw := p.ts.directoryWriter; dw != nil {
		if dw.locked {
			held = append(held, "directory writer lock")
		}
		if dw.auditBeforeDirectory {
			held = append(held, "tenant audit lock")
		}
		if dw.authorityBeforeDirectory || len(dw.heldUsers) > 0 {
			held = append(held, "user authority lock")
		}
	}
	if p.ts.authorityLocked {
		held = append(held, "tenant authority lock")
	}
	if len(held) == 0 {
		return nil
	}
	return fmt.Errorf("%w: already held or noted: %v", store.ErrLoginCapabilityLockOrder, held)
}

func (p loginCapabilityPort) Observe(ctx context.Context, artifactVersion string) (store.LoginCapabilityObservation, error) {
	st, unlock := p.state()
	defer unlock()
	if p.ts.readOnly {
		return store.LoginCapabilityObservation{}, store.ErrReadOnly
	}
	if st.poisoned != nil {
		return store.LoginCapabilityObservation{}, st.poisoned
	}
	if !st.locked {
		return store.LoginCapabilityObservation{}, st.poison(store.ErrLoginCapabilityNotLocked)
	}
	if artifactVersion == "" {
		return store.LoginCapabilityObservation{}, st.poison(store.ErrLoginCapabilityArtifactVersion)
	}
	if st.observed {
		return st.observation, nil
	}
	obs, err := upsertLoginCapability(ctx, p.ts.tx, p.ts.s.dia, artifactVersion)
	if err != nil {
		return store.LoginCapabilityObservation{}, st.poison(err)
	}
	st.observed = true
	st.observation = obs
	return obs, nil
}

func (p loginCapabilityPort) Read(ctx context.Context) (store.LoginCapabilityObservation, error) {
	st, unlock := p.state()
	defer unlock()
	if st.poisoned != nil {
		return store.LoginCapabilityObservation{}, st.poisoned
	}
	obs, err := readLoginCapability(ctx, p.ts.tx, p.ts.s.dia)
	if err != nil {
		if p.ts.readOnly {
			return store.LoginCapabilityObservation{}, err
		}
		return store.LoginCapabilityObservation{}, st.poison(err)
	}
	return obs, nil
}

func loginCapabilityRelation(dia dialect.Dialect) string {
	return directoryWriterRelation(dia, dialect.LoginCapabilityObservationTable)
}

// acquireLoginCapabilityLock serializes on a constant key whether or not the row exists.
func acquireLoginCapabilityLock(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	actx, cancel := context.WithTimeout(ctx, loginCapabilityLockWait)
	defer cancel()
	switch dia.Name() {
	case store.EnginePostgres:
		if _, err := tx.ExecContext(actx,
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			dialect.LoginCapabilityLockKey,
		); err != nil {
			return fmt.Errorf("sqlstore: take the login capability lock: %w", err)
		}
	case store.EngineSQLite:
		// #nosec G202 -- the relation is a dialect constant quoted by directoryWriterRelation.
		if _, err := tx.ExecContext(actx, "DELETE FROM "+loginCapabilityRelation(dia)+" WHERE 0"); err != nil {
			return fmt.Errorf("sqlstore: take the SQLite login capability reservation: %w", err)
		}
	default:
		return fmt.Errorf("sqlstore: login capability lock: unsupported engine %q", dia.Name())
	}
	return nil
}

const loginCapabilityColumns = "capability_key, first_observed_at, last_observed_at, last_artifact_version, observation_count"

func readLoginCapability(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (store.LoginCapabilityObservation, error) {
	// #nosec G202 -- internal constants only.
	rows, err := tx.QueryContext(ctx, "SELECT "+loginCapabilityColumns+" FROM "+loginCapabilityRelation(dia))
	if err != nil {
		return store.LoginCapabilityObservation{}, fmt.Errorf("sqlstore: read the login capability observation: %w", err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	var (
		out   store.LoginCapabilityObservation
		count int
	)
	for rows.Next() {
		count++
		if count > 1 {
			return store.LoginCapabilityObservation{}, fmt.Errorf("%w: more than one row", store.ErrLoginCapabilityMalformed)
		}
		obs, serr := scanLoginCapability(rows, dia)
		if serr != nil {
			return store.LoginCapabilityObservation{}, serr
		}
		out = obs
	}
	if err := rows.Err(); err != nil {
		return store.LoginCapabilityObservation{}, fmt.Errorf("sqlstore: read the login capability observation: %w", err)
	}
	return out, nil
}

type loginCapabilityScanner interface{ Scan(dest ...any) error }

func scanLoginCapability(row loginCapabilityScanner, dia dialect.Dialect) (store.LoginCapabilityObservation, error) {
	var (
		key, version string
		count        int64
		obs          store.LoginCapabilityObservation
	)
	if dia.Name() == store.EnginePostgres {
		var first, last time.Time
		if err := row.Scan(&key, &first, &last, &version, &count); err != nil {
			return obs, fmt.Errorf("%w: %w", store.ErrLoginCapabilityMalformed, err)
		}
		obs.FirstObservedAt, obs.LastObservedAt = first.UTC(), last.UTC()
	} else {
		var firstRaw, lastRaw string
		if err := row.Scan(&key, &firstRaw, &lastRaw, &version, &count); err != nil {
			return obs, fmt.Errorf("%w: %w", store.ErrLoginCapabilityMalformed, err)
		}
		first, ferr := parseSQLiteLoginCapabilityTime(firstRaw)
		last, lerr := parseSQLiteLoginCapabilityTime(lastRaw)
		if err := errors.Join(ferr, lerr); err != nil {
			return obs, err
		}
		obs.FirstObservedAt, obs.LastObservedAt = first, last
	}
	if key != dialect.LoginCapabilityKey || version == "" || count <= 0 {
		return store.LoginCapabilityObservation{}, fmt.Errorf("%w: key=%q version-empty=%t count=%d",
			store.ErrLoginCapabilityMalformed, key, version == "", count)
	}
	obs.Present = true
	obs.LastArtifactVersion = version
	obs.ObservationCount = count
	return obs, nil
}

func parseSQLiteLoginCapabilityTime(raw string) (time.Time, error) {
	t, err := time.Parse(sqliteLoginCapabilityTimeLayout, raw)
	if err != nil || t.UTC().Format(sqliteLoginCapabilityTimeLayout) != raw {
		return time.Time{}, fmt.Errorf("%w: non-canonical time %q", store.ErrLoginCapabilityMalformed, raw)
	}
	return t.UTC(), nil
}

// upsertLoginCapability inserts or increments exactly once, with database time, keeping
// the first observation time. There is no timestamp ordering: a clock reversal neither
// erases history nor blocks the observation. Overflow is a database error, never a wrap.
func upsertLoginCapability(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, artifactVersion string) (store.LoginCapabilityObservation, error) {
	now := "pg_catalog.statement_timestamp()"
	if dia.Name() == store.EngineSQLite {
		now = "strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	}
	rel := loginCapabilityRelation(dia)
	// #nosec G202 -- internal constants only; values are bound.
	query := dia.Rebind("INSERT INTO " + rel + " (" + loginCapabilityColumns + ") VALUES (?, " + now + ", " + now + ", ?, 1) " +
		"ON CONFLICT (capability_key) DO UPDATE SET " +
		"last_observed_at = excluded.last_observed_at, " +
		"last_artifact_version = excluded.last_artifact_version, " +
		"observation_count = " + rel + ".observation_count + 1 " +
		"RETURNING " + loginCapabilityColumns)
	obs, err := scanLoginCapability(tx.QueryRowContext(ctx, query, dialect.LoginCapabilityKey, artifactVersion), dia)
	if err != nil {
		return store.LoginCapabilityObservation{}, fmt.Errorf("sqlstore: record the login capability observation: %w", err)
	}
	return obs, nil
}
