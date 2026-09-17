// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CH1 on a real PostgreSQL server, in both interleaving directions. The system
// chain takes no lineage lock in a Custody transaction, so its tenant append lock
// is the only thing that serializes a checkpoint against an auth-partition append.
// SQLite cannot show this: its single writer serializes the two regardless.
//
// Both directions are forced with barriers, not timing:
//   - forward: the checkpoint's off-box key parks inside SignCheckpoint, after
//     LockAppends and Head, and a competing AuthMutate append must WAIT;
//   - reverse: the competitor parks inside its AuthMutate callback after Append,
//     holding the lock, and the checkpoint must WAIT and then sign and link to
//     the competitor's committed event.
//
// A wait counts only when pg_locks shows the waiting backend requesting the
// system chain's append-lock key while blocked (pg_blocking_pids) by the backend
// holding it, and each backend is identified by the application_name of the Store
// it belongs to. The key is not assumed: before the interleaving, the fixture runs
// the pinned implementation's own lock statement (sqlstore/audit.go lockTenant:
// pg_advisory_xact_lock(hashtextextended($1, 0)) with the tenant id) for the system
// tenant in a transaction of its own, and requires the pg_locks row it creates to
// carry the bigint split of the key the server computes.
//
// Teardown releases every barrier and cancels the shared context before any store
// closes, then joins every launched goroutine through its result channel, so a
// failed run keeps both results. Worker goroutines never call testing.T. The
// context is the cooperative bound; `go test -timeout` stays the outer hard limit.

const (
	serializationCheckpointApp = "ch1_checkpoint"
	serializationCompetitorApp = "ch1_competitor"
	serializationObserverApp   = "ch1_observer"
	serializationTestTimeout   = 2 * time.Minute
	serializationPollInterval  = 20 * time.Millisecond
)

// appendLockSQL is the append-lock statement of the pinned implementation
// (sqlstore/audit.go lockTenant); appendLockKeySQL evaluates its key.
const (
	appendLockSQL    = `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`
	appendLockKeySQL = `SELECT pg_catalog.hashtextextended($1, 0)`
)

const advisoryLockRowsSQL = `
SELECT l.pid::bigint,
       COALESCE(a.application_name, ''),
       l.granted,
       COALESCE(l.classid::bigint, -1),
       COALESCE(l.objid::bigint, -1),
       COALESCE(l.objsubid::bigint, -1),
       pg_catalog.array_to_string(pg_catalog.pg_blocking_pids(l.pid), ',')
FROM pg_catalog.pg_locks l
LEFT JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
WHERE l.locktype = 'advisory'
  AND l.database = (SELECT d.oid FROM pg_catalog.pg_database d WHERE d.datname = pg_catalog.current_database())`

type serializationCheckpointKey struct {
	priv    ed25519.PrivateKey
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (k *serializationCheckpointKey) SignCheckpoint(ctx context.Context, preimage []byte) ([]byte, error) {
	k.once.Do(func() { close(k.entered) })
	select {
	case <-k.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return ed25519.Sign(k.priv, preimage), nil
}

func (k *serializationCheckpointKey) Algorithm() audit.SigAlg { return audit.AlgEd25519 }

func (k *serializationCheckpointKey) KeyID() string { return "test-serialization-key" }

func (k *serializationCheckpointKey) PublicKey(context.Context) ([]byte, error) {
	return k.priv.Public().(ed25519.PublicKey), nil
}

type serializationCheckpointResult struct {
	ev  model.AuditEvent
	ok  bool
	err error
}

type serializationAppendResult struct {
	ev  model.AuditEvent
	err error
}

func checkpointOutcome(launched bool, r *serializationCheckpointResult) interleavedOutcome {
	if r == nil {
		return interleavedOutcome{Launched: launched}
	}
	return interleavedOutcome{Launched: launched, Joined: true, Err: r.err, Wrote: r.ok, Seq: r.ev.Seq}
}

func appendOutcome(launched bool, r *serializationAppendResult) interleavedOutcome {
	if r == nil {
		return interleavedOutcome{Launched: launched}
	}
	return interleavedOutcome{Launched: launched, Joined: true, Err: r.err, Wrote: r.ev.Seq != 0, Seq: r.ev.Seq}
}

type rowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func readAdvisoryLockRows(ctx context.Context, q rowQueryer, onlyOwnBackend bool) ([]advisoryLockRow, error) {
	query := advisoryLockRowsSQL
	if onlyOwnBackend {
		query += "\n  AND l.pid = pg_catalog.pg_backend_pid()"
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read pg_locks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []advisoryLockRow
	for rows.Next() {
		var r advisoryLockRow
		var blocking string
		if err := rows.Scan(&r.PID, &r.App, &r.Granted, &r.Key.ClassID, &r.Key.ObjID, &r.Key.ObjSubID, &blocking); err != nil {
			return nil, fmt.Errorf("scan pg_locks: %w", err)
		}
		if r.BlockedBy, err = parseBlockingPIDs(blocking); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pg_locks: %w", err)
	}
	return out, nil
}

// probeAppendLockKey runs the pinned append-lock statement for tenant in a
// transaction of its own and requires the one advisory row it creates in pg_locks
// to be bigintAdvisoryKey of the key the same expression evaluates to. It returns
// that pg_locks key. The probe lock ends with the rolled-back transaction, before
// any interleaving starts.
func probeAppendLockKey(ctx context.Context, db *sql.DB, tenant string) (advisoryKey, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return advisoryKey{}, fmt.Errorf("begin the key probe: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, appendLockSQL, tenant); err != nil {
		return advisoryKey{}, fmt.Errorf("take the append lock in the probe: %w", err)
	}
	var key int64
	if err := tx.QueryRowContext(ctx, appendLockKeySQL, tenant).Scan(&key); err != nil {
		return advisoryKey{}, fmt.Errorf("evaluate the append-lock key: %w", err)
	}
	rows, err := readAdvisoryLockRows(ctx, tx, true)
	if err != nil {
		return advisoryKey{}, err
	}
	want := bigintAdvisoryKey(key)
	if len(rows) != 1 || !rows[0].Granted || rows[0].Key != want {
		return advisoryKey{}, fmt.Errorf("pg_locks shows %+v for the probe's append lock, want one granted row with %+v", rows, want)
	}
	return want, nil
}

// awaitLockWait polls pg_locks until a backend of waiterApp waits for key while
// blocked by the backend of blockerApp holding it. If peerDone delivers first, the
// peer's result is returned so the caller can still join and report it. It never
// touches testing.T.
func awaitLockWait[R any](ctx context.Context, observer *sql.DB, key advisoryKey, waiterApp, blockerApp string, peerDone <-chan R) (lockWait, *R, string, error) {
	ticker := time.NewTicker(serializationPollInterval)
	defer ticker.Stop()
	last := "no pg_locks observation yet"
	for {
		rows, err := readAdvisoryLockRows(ctx, observer, false)
		if err != nil {
			return lockWait{}, nil, last, err
		}
		wait, found, reason := findLockWait(rows, key, waiterApp, blockerApp)
		if found {
			return wait, nil, "", nil
		}
		last = reason
		select {
		case r := <-peerDone:
			return lockWait{}, &r, last, nil
		case <-ctx.Done():
			return lockWait{}, nil, last, fmt.Errorf("no matching wait before the test deadline: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

type serializationFixture struct {
	checkpointStore store.Store
	competitorStore store.Store
	observer        *sql.DB
	appendLockKey   advisoryKey
	head            store.HeadRef
}

func newSerializationFixture(ctx context.Context, t *testing.T) *serializationFixture {
	t.Helper()
	pg := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	dsnFor := func(app string) string {
		dsn, err := withApplicationName(pg.App, app)
		if err != nil {
			t.Fatalf("application DSN for %s: %v", app, err)
		}
		return dsn
	}

	checkpointStore, err := sqlstore.Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsnFor(serializationCheckpointApp), MaxConns: 4}, nil)
	if err != nil {
		t.Fatalf("open the checkpoint store: %v", err)
	}
	t.Cleanup(func() { _ = checkpointStore.Close() })
	if err := checkpointStore.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatalf("ensure the system tenant: %v", err)
	}
	for _, action := range []string{"test.system_seed_one", "test.system_seed_two"} {
		if _, err := appendSystemChainEventForSerialization(ctx, checkpointStore, action); err != nil {
			t.Fatalf("seed the system chain: %v", err)
		}
	}

	competitorStore, err := sqlstore.Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsnFor(serializationCompetitorApp), MaxConns: 4}, nil)
	if err != nil {
		t.Fatalf("open the competitor store: %v", err)
	}
	t.Cleanup(func() { _ = competitorStore.Close() })

	observer, err := sql.Open("pgx", dsnFor(serializationObserverApp))
	if err != nil {
		t.Fatalf("open the lock observer: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	appendLockKey, err := probeAppendLockKey(ctx, observer, model.SystemTenantID.String())
	if err != nil {
		t.Fatalf("confirm the system chain's append-lock key in pg_locks: %v", err)
	}

	return &serializationFixture{
		checkpointStore: checkpointStore,
		competitorStore: competitorStore,
		observer:        observer,
		appendLockKey:   appendLockKey,
		head:            serializationSystemTip(ctx, t, checkpointStore),
	}
}

func appendSystemChainEventForSerialization(ctx context.Context, st store.Store, action string) (model.AuditEvent, error) {
	var ev model.AuditEvent
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var aerr error
		ev, aerr = as.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: action, TargetKind: "core.test",
		})
		return aerr
	})
	return ev, err
}

func serializationSystemTip(ctx context.Context, t *testing.T, st store.Store) store.HeadRef {
	t.Helper()
	var tip store.HeadRef
	var empty bool
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(ctx)
		tip, empty = head, !ok
		return err
	}); err != nil {
		t.Fatalf("read the system chain tip: %v", err)
	}
	if empty {
		t.Fatal("setup: the system chain is empty")
	}
	return tip
}

// verifySystemChainAfterContention requires the checkpoint signatures to verify and
// attest wantAttested, the structure to verify, and the committed tip to be wantTip.
func verifySystemChainAfterContention(ctx context.Context, t *testing.T, st store.Store, verifier *audit.CheckpointVerifier, wantTip model.AuditEvent, wantAttested int64) {
	t.Helper()
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		checkpoints, err := audit.VerifyCheckpointsWith(ctx, sc.Audit(), verifier)
		if err != nil {
			return err
		}
		if !checkpoints.OK || checkpoints.LatestAttestedSeq != wantAttested {
			t.Errorf("checkpoint verification after contention = %+v, want ok attesting seq %d", checkpoints, wantAttested)
		}
		structure, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		if !structure.OK {
			t.Errorf("structural verification after contention = %+v", structure)
		}
		tip, ok, err := sc.Audit().Head(ctx)
		if err != nil {
			return err
		}
		if !ok || tip.Seq != wantTip.Seq || !bytes.Equal(tip.Hash, wantTip.Hash) {
			t.Errorf("chain tip is seq %d, want seq %d", tip.Seq, wantTip.Seq)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify the committed system chain: %v", err)
	}
}

// TestCheckpointSystemChainHoldsTheAppendLockAcrossSigningOnPostgres: the checkpoint
// holds the append lock while it signs, so a competing append waits behind it.
func TestCheckpointSystemChainHoldsTheAppendLockAcrossSigningOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), serializationTestTimeout)
	defer cancel()
	f := newSerializationFixture(ctx, t)

	_, onBox, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate on-box key: %v", err)
	}
	_, offBox, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate off-box key: %v", err)
	}
	key := &serializationCheckpointKey{priv: offBox, entered: make(chan struct{}), release: make(chan struct{})}
	signer, err := audit.NewSigner(onBox, audit.WithCheckpointKey(key))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(key.release) }) }

	checkpointDone := make(chan serializationCheckpointResult, 1)
	competitorDone := make(chan serializationAppendResult, 1)
	var checkpoint *serializationCheckpointResult
	var competitor *serializationAppendResult
	var checkpointLaunched, competitorLaunched bool
	defer func() {
		release()
		cancel()
		if checkpointLaunched && checkpoint == nil {
			r := <-checkpointDone
			checkpoint = &r
		}
		if competitorLaunched && competitor == nil {
			r := <-competitorDone
			competitor = &r
		}
		t.Logf("joined: checkpoint %s; competitor %s",
			checkpointOutcome(checkpointLaunched, checkpoint), appendOutcome(competitorLaunched, competitor))
	}()

	checkpointLaunched = true
	go func() {
		ev, ok, err := signer.Checkpoint(ctx, f.checkpointStore, model.SystemTenantID)
		checkpointDone <- serializationCheckpointResult{ev: ev, ok: ok, err: err}
	}()
	select {
	case <-key.entered:
	case r := <-checkpointDone:
		checkpoint = &r
		t.Fatalf("the checkpoint returned before reaching its signer: %s", checkpointOutcome(true, checkpoint))
	case <-ctx.Done():
		t.Fatalf("the checkpoint did not reach its signer before the test deadline: %v", ctx.Err())
	}

	competitorLaunched = true
	go func() {
		ev, err := appendSystemChainEventForSerialization(ctx, f.competitorStore, "test.concurrent_system_append")
		competitorDone <- serializationAppendResult{ev: ev, err: err}
	}()

	wait, early, last, err := awaitLockWait(ctx, f.observer, f.appendLockKey, serializationCompetitorApp, serializationCheckpointApp, competitorDone)
	if err != nil {
		t.Fatalf("observe the competitor: %v (last observation: %s)", err, last)
	}
	if early != nil {
		competitor = early
		t.Errorf("the competitor %s before any wait was observed (last observation: %s): the checkpoint did not hold the system chain's append lock across signing",
			appendOutcome(true, competitor), last)
	} else {
		t.Logf("observed %s backend %d waiting for the system chain's append lock held by %s backend %d",
			serializationCompetitorApp, wait.WaiterPID, serializationCheckpointApp, wait.BlockerPID)
	}

	release()
	r := <-checkpointDone
	checkpoint = &r
	if competitor == nil {
		r := <-competitorDone
		competitor = &r
	}

	if checkpoint.err != nil || !checkpoint.ok {
		t.Fatalf("checkpoint %s; competitor %s", checkpointOutcome(true, checkpoint), appendOutcome(true, competitor))
	}
	if competitor.err != nil {
		t.Fatalf("competitor %s; checkpoint %s", appendOutcome(true, competitor), checkpointOutcome(true, checkpoint))
	}
	if checkpoint.ev.Seq != f.head.Seq+1 || !bytes.Equal(checkpoint.ev.PrevHash, f.head.Hash) {
		t.Errorf("checkpoint landed at seq %d, want seq %d linked to the head it read (competitor at seq %d)", checkpoint.ev.Seq, f.head.Seq+1, competitor.ev.Seq)
	}
	if competitor.ev.Seq != f.head.Seq+2 || !bytes.Equal(competitor.ev.PrevHash, checkpoint.ev.Hash) {
		t.Errorf("competitor landed at seq %d, want seq %d linked to the checkpoint", competitor.ev.Seq, f.head.Seq+2)
	}
	verifier, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatalf("checkpoint verifier: %v", err)
	}
	verifySystemChainAfterContention(ctx, t, f.checkpointStore, verifier, competitor.ev, f.head.Seq)
}

// TestCheckpointWaitsForAnAppendLockHeldByACompetitorOnPostgres: a competitor already
// holds the append lock, so the checkpoint waits, then reads and signs the
// competitor's committed event as its predecessor.
func TestCheckpointWaitsForAnAppendLockHeldByACompetitorOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), serializationTestTimeout)
	defer cancel()
	f := newSerializationFixture(ctx, t)

	_, onBox, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate on-box key: %v", err)
	}
	signer, err := audit.NewSigner(onBox)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	holding := make(chan struct{})
	releaseCompetitor := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCompetitor) }) }

	competitorDone := make(chan serializationAppendResult, 1)
	checkpointDone := make(chan serializationCheckpointResult, 1)
	var competitor *serializationAppendResult
	var checkpoint *serializationCheckpointResult
	var competitorLaunched, checkpointLaunched bool
	defer func() {
		release()
		cancel()
		if competitorLaunched && competitor == nil {
			r := <-competitorDone
			competitor = &r
		}
		if checkpointLaunched && checkpoint == nil {
			r := <-checkpointDone
			checkpoint = &r
		}
		t.Logf("joined: competitor %s; checkpoint %s",
			appendOutcome(competitorLaunched, competitor), checkpointOutcome(checkpointLaunched, checkpoint))
	}()

	competitorLaunched = true
	go func() {
		var ev model.AuditEvent
		err := f.competitorStore.AuthMutate(ctx, func(as store.AuthScope) error {
			appended, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: "test.lock_holding_system_append", TargetKind: "core.test",
			})
			if err != nil {
				return err
			}
			ev = appended
			close(holding)
			select {
			case <-releaseCompetitor:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		competitorDone <- serializationAppendResult{ev: ev, err: err}
	}()
	select {
	case <-holding:
	case r := <-competitorDone:
		competitor = &r
		t.Fatalf("the competitor returned before holding the append lock: %s", appendOutcome(true, competitor))
	case <-ctx.Done():
		t.Fatalf("the competitor did not take the append lock before the test deadline: %v", ctx.Err())
	}

	checkpointLaunched = true
	go func() {
		ev, ok, err := signer.Checkpoint(ctx, f.checkpointStore, model.SystemTenantID)
		checkpointDone <- serializationCheckpointResult{ev: ev, ok: ok, err: err}
	}()

	wait, early, last, err := awaitLockWait(ctx, f.observer, f.appendLockKey, serializationCheckpointApp, serializationCompetitorApp, checkpointDone)
	if err != nil {
		t.Fatalf("observe the checkpoint: %v (last observation: %s)", err, last)
	}
	if early != nil {
		checkpoint = early
		t.Errorf("the checkpoint %s while the competitor still held the system chain's append lock (last observation: %s)",
			checkpointOutcome(true, checkpoint), last)
	} else {
		t.Logf("observed %s backend %d waiting for the system chain's append lock held by %s backend %d",
			serializationCheckpointApp, wait.WaiterPID, serializationCompetitorApp, wait.BlockerPID)
	}

	release()
	rc := <-competitorDone
	competitor = &rc
	if checkpoint == nil {
		r := <-checkpointDone
		checkpoint = &r
	}

	if competitor.err != nil {
		t.Fatalf("competitor %s; checkpoint %s", appendOutcome(true, competitor), checkpointOutcome(true, checkpoint))
	}
	if checkpoint.err != nil || !checkpoint.ok {
		t.Fatalf("checkpoint %s; competitor %s", checkpointOutcome(true, checkpoint), appendOutcome(true, competitor))
	}
	if competitor.ev.Seq != f.head.Seq+1 || !bytes.Equal(competitor.ev.PrevHash, f.head.Hash) {
		t.Errorf("competitor landed at seq %d, want seq %d linked to the seeded head", competitor.ev.Seq, f.head.Seq+1)
	}
	if checkpoint.ev.Seq != f.head.Seq+2 || !bytes.Equal(checkpoint.ev.PrevHash, competitor.ev.Hash) {
		t.Errorf("checkpoint landed at seq %d, want seq %d linked to the competitor's committed event", checkpoint.ev.Seq, f.head.Seq+2)
	}
	verifier, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatalf("checkpoint verifier: %v", err)
	}
	verifySystemChainAfterContention(ctx, t, f.checkpointStore, verifier, checkpoint.ev, f.head.Seq+1)
}
