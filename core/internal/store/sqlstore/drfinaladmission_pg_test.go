// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func ir3Terminate(t *testing.T, db *sql.DB, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var dead bool
	if err := db.QueryRowContext(ctx, "SELECT pg_catalog.pg_terminate_backend($1, 1000)", pid).Scan(&dead); err != nil || !dead {
		t.Fatalf("terminate backend %d: dead=%v err=%v", pid, dead, err)
	}
}

func ir3ClientBackends(t *testing.T, db *sql.DB, database string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname=$1 AND backend_type='client backend'`,
		database).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ir3Holder is one granted advisory-lock holder as the SERVER reports it, read
// through a supervisor session rather than taken from the code under test.
type ir3Holder struct {
	pid   int
	mode  string
	start time.Time
}

// ir3FenceKey recomputes the destination's advisory key ON THE SERVER from the
// destination names, so a comparison against the retained key is independent of
// whatever the admission stored.
func ir3FenceKey(t *testing.T, super *sql.DB, database, schema string) int64 {
	t.Helper()
	var key int64
	if err := super.QueryRowContext(context.Background(),
		"SELECT pg_catalog.hashtextextended($1 || $2 || ':' || $3, 0)",
		drRestoreLockName+":", database, schema).Scan(&key); err != nil {
		t.Fatalf("derive the destination fence key: %v", err)
	}
	return key
}

func ir3AdvisoryHolders(t *testing.T, super *sql.DB, key int64, database string) []ir3Holder {
	t.Helper()
	rows, err := super.QueryContext(context.Background(), `SELECT l.pid, l.mode, a.backend_start
 FROM pg_catalog.pg_locks l
 JOIN pg_catalog.pg_database d ON d.oid = l.database
 JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
 WHERE l.locktype='advisory' AND l.granted AND d.datname=$2
   AND l.classid=(($1::pg_catalog.int8>>32)&4294967295)::pg_catalog.oid
   AND l.objid=($1::pg_catalog.int8&4294967295)::pg_catalog.oid
   AND l.objsubid=1
 ORDER BY l.pid`, key, database)
	if err != nil {
		t.Fatalf("read the fence holders: %v", err)
	}
	defer rows.Close() //nolint:errcheck // test helper
	var out []ir3Holder
	for rows.Next() {
		var h ir3Holder
		if err := rows.Scan(&h.pid, &h.mode, &h.start); err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// ir3DropSharedFence drops the destination's shared advisory lock ON THE ORIGINAL
// STILL-LIVE SESSION, with the same key derivation the acquisition used. It is the
// mechanism every delivered pre-L case was missing: terminating the backend cannot
// tell "the final decision inspects the exact key in pg_locks" apart from "any
// query on a dead connection fails".
func ir3DropSharedFence(ctx context.Context, c *drCoordination) (bool, error) {
	var released bool
	err := c.conn.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_advisory_unlock_shared(pg_catalog.hashtextextended($1 || $2 || ':' || $3, 0))",
		drRestoreLockName+":", c.dest.Database, c.dest.Schema).Scan(&released)
	return released, err
}

// ir3LogSink captures the process logger while one non-parallel case runs. The
// previous logger is restored in cleanup.
type ir3LogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *ir3LogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *ir3LogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func ir3CaptureWarnings(t *testing.T) *ir3LogSink {
	t.Helper()
	sink := &ir3LogSink{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return sink
}

// ir3DSNPassword is the fixture password the captured diagnostics must never
// contain. It is read from the DSN the test itself configured; no secret is
// introduced by reading it.
func ir3DSNPassword(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse the fixture DSN: %v", err)
	}
	password, _ := u.User.Password()
	if password == "" {
		t.Fatal("the fixture DSN carries no password, so the redaction check would prove nothing")
	}
	return password
}

func ir3ControlPresent(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`,
		dialect.EngineSchema, dialect.DRRestoreControlTable).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestIR3CoordinationLossBeforeFinalDecisionRefuses(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var other *drCoordination
	var hookErr error
	hit := false
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() {
		publicationFinalDecisionTestHook = old
		if other != nil {
			_ = other.close()
		}
	})
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		if a == nil || a.fence == nil || a.fence.coord == nil {
			hookErr = errors.New("missing retained session at final decision")
			return
		}
		ir3Terminate(t, super, a.fence.coord.backendPID)
		other, hookErr = openDRCoordinationExclusive(ctx, cfg.OwnerDSN, cfg)
	}

	s, err := Open(ctx, cfg, nil)
	if s != nil {
		_ = s.Close()
	}
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if hookErr != nil {
		t.Fatalf("causal setup: %v", hookErr)
	}
	if err == nil {
		t.Fatal("Open published after its coordination session died and another restore acquired exclusive before the final observation")
	}
	if !errors.Is(err, ErrRestoreCoordinationUnknown) && !strings.Contains(err.Error(), "restore publication coordination") && !strings.Contains(err.Error(), "no longer holds") {
		t.Fatalf("want retained-session refusal, got %v", err)
	}
}

// TestIR3NoLossSuccessSingleAcquisition measures the RETAINED identity against an
// INDEPENDENT server observation of the same destination.
//
// Its previous form assigned the acquisition identity from the decide hook itself
// (acquirePID = decidePID), so the comparison could not fail and would have kept
// passing if the identity had been re-captured at decide time — the exact defect it
// exists to exclude. Nothing about the admission's own state is trusted here now:
// the lock key is recomputed BY THE SERVER from the destination names, the fence's
// granted holders are read through a supervisor session, and the retained
// connection is asked who it is. Exactly one acquisition happened in this process
// and a session-level advisory lock can only be held by the session that took it,
// so the single observed holder IS the acquiring backend.
func TestIR3NoLossSuccessSingleAcquisition(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	drCoordinationAcquisitions.Store(0)
	var retainedPID, livePID int
	var retainedStart time.Time
	var retainedKey, serverKey int64
	var holders []ir3Holder
	var hookErr error
	hit := false
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		c := a.fence.coord
		retainedPID, retainedStart, retainedKey = c.backendPID, c.backendStart, c.lockKey
		serverKey = ir3FenceKey(t, super, c.dest.Database, c.dest.Schema)
		holders = ir3AdvisoryHolders(t, super, serverKey, c.dest.Database)
		if err := c.conn.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&livePID); err != nil {
			hookErr = err
		}
	}

	s, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if hookErr != nil {
		t.Fatalf("independent observation: %v", hookErr)
	}
	if got := drCoordinationAcquisitions.Load(); got != 1 {
		t.Fatalf("want exactly one coordination acquisition, got %d", got)
	}
	if retainedPID == 0 || retainedStart.IsZero() || retainedKey == 0 {
		t.Fatalf("the admission retained an incomplete identity pid=%d start=%v key=%d", retainedPID, retainedStart, retainedKey)
	}
	if retainedKey != serverKey {
		t.Fatalf("the retained lock key %d is not the key the server derives for this destination (%d)", retainedKey, serverKey)
	}
	if len(holders) != 1 {
		t.Fatalf("want exactly one granted holder of the destination fence at L, got %+v", holders)
	}
	if holders[0].mode != "ShareLock" {
		t.Fatalf("ordinary publication must hold the destination fence in shared mode, got %q", holders[0].mode)
	}
	if holders[0].pid != retainedPID || !holders[0].start.Equal(retainedStart) {
		t.Fatalf("the identity frozen at acquisition (pid=%d start=%v) is not the backend the server observes holding the fence at L (pid=%d start=%v)",
			retainedPID, retainedStart, holders[0].pid, holders[0].start)
	}
	if livePID != retainedPID {
		t.Fatalf("the retained connection answers as backend %d while the admission's frozen identity is %d", livePID, retainedPID)
	}
}

func TestIR3MaxConns1NoLeakedSessionOnFinalFailure(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	super := drOpenSuper(t, pg.Superuser)
	before := ir3ClientBackends(t, super, pg.Database)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	hit := false
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		ir3Terminate(t, super, a.fence.coord.backendPID)
	}

	s, err := Open(ctx, cfg, nil)
	if s != nil {
		_ = s.Close()
	}
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if err == nil {
		t.Fatal("Open succeeded after pre-L loss")
	}
	deadline := time.Now().Add(3 * time.Second)
	var after int
	for {
		after = ir3ClientBackends(t, super, pg.Database)
		if after <= before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaked client backends after pre-L refusal: before=%d after=%d", before, after)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestIR3PreLUnknownDistinctFromPostLCleanup(t *testing.T) {
	t.Run("pre-L-cancel", func(t *testing.T) {
		pg := isolatedPGSplit(t)
		cfg := drPGConfig(pg)
		cfg.MaxConns = 1
		cfg.AuditSpoolMaxBytes = 0
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		old := publicationFinalDecisionTestHook
		t.Cleanup(func() { publicationFinalDecisionTestHook = old })
		var cancelAtDecision context.CancelFunc
		ctx, cancelAtDecision = context.WithCancel(ctx)
		hit := false
		publicationFinalDecisionTestHook = func(*publicationAdmission) { hit = true; cancelAtDecision() }
		s, err := Open(ctx, cfg, nil)
		if s != nil {
			_ = s.Close()
		}
		if !hit {
			t.Fatal("final-decision hook did not run")
		}
		if err == nil {
			t.Fatal("Open published after pre-L cancellation")
		}
		if !errors.Is(err, ErrRestoreCoordinationUnknown) && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "restore publication coordination") {
			t.Fatalf("want pre-L unknown/cancel, got %v", err)
		}
	})
	t.Run("post-L-cleanup-uncertainty", func(t *testing.T) {
		pg := isolatedPGSplit(t)
		cfg := drPGConfig(pg)
		cfg.MaxConns = 1
		cfg.AuditSpoolMaxBytes = 0
		super := drOpenSuper(t, pg.Superuser)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		var afterL, confirmed bool
		var cleanupErr error
		var hit bool
		oldAfter := publicationAfterLinearizationTestHook
		oldClean := publicationCleanupTestHook
		t.Cleanup(func() {
			publicationAfterLinearizationTestHook = oldAfter
			publicationCleanupTestHook = oldClean
		})
		publicationAfterLinearizationTestHook = func(a *publicationAdmission) {
			hit = true
			ir3Terminate(t, super, a.fence.coord.backendPID)
		}
		publicationCleanupTestHook = func(l, ok bool, err error) {
			afterL, confirmed, cleanupErr = l, ok, err
		}
		s, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("post-L cleanup uncertainty must not refuse publication: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if !hit {
			t.Fatal("linearization hook did not run")
		}
		if !afterL {
			t.Fatal("cleanup reported as pre-L exclusion failure")
		}
		if confirmed && cleanupErr == nil {
			t.Fatal("expected uncertain post-L unlock after terminating the original session")
		}
	})
}

func TestIR3ControlWriteHolderLossCannotCommitOnSurvivingDDL(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	old := restoreOperationBeforeCommitTestHook
	t.Cleanup(func() { restoreOperationBeforeCommitTestHook = old })
	hit := false
	restoreOperationBeforeCommitTestHook = func(op *restoreOperation) {
		hit = true
		ir3Terminate(t, super, op.coord.backendPID)
	}

	_, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA})
	if !hit {
		t.Fatal("commit hook did not run")
	}
	if err == nil {
		t.Fatal("install committed after the exclusive holder was lost")
	}
	if ir3ControlPresent(t, super) {
		t.Fatal("control relation committed on a surviving connection after holder loss")
	}
}

// TestIR3SchemaOnlyLossBeforeCompletionRefuses also shows what the refusal does
// NOT do. The migrations committed before the final decision, so the schema this
// call applied is still on the destination afterwards: refusing the preparation
// withholds its publication, it does not roll it back. The contract permits
// exactly this, and the diagnostic now says so rather than leaving an operator to
// read "refused" as "nothing was applied".
func TestIR3SchemaOnlyLossBeforeCompletionRefuses(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	before := drRelationNames(t, super)
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	hit := false
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		ir3Terminate(t, super, a.fence.coord.backendPID)
	}
	err := ApplyMigrations(ctx, cfg, nil)
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if err == nil {
		t.Fatal("schema-only preparation completed after pre-L loss")
	}
	if !strings.Contains(err.Error(), "does not roll it back") {
		t.Fatalf("the schema-only refusal must state that it is not a rollback, got %v", err)
	}
	after := drRelationNames(t, super)
	if len(after) <= len(before) {
		t.Fatalf("the applied schema did not survive the refusal: before=%d after=%d relations", len(before), len(after))
	}
	t.Logf("applied schema after the refusal: %d relations (was %d); the refusal withheld publication and reversed nothing", len(after), len(before))
}

// TestIR3LiveSessionDroppingTheExactKeyRefuses is the causal case the delivery did
// not have: the retained session STAYS ALIVE and merely stops holding the fence.
//
// Every other pre-L case here terminates the backend, so none of them can
// distinguish the required behaviour — "inspect the exact key/mode in pg_locks
// rather than taking the lock again" — from "any query on a dead connection
// fails". An implementation that ran SELECT 1 on the retained connection would
// pass those and fail this one. The positive controls are part of the case: before
// the drop the server shows exactly one granted ShareLock on the destination key
// held by the frozen backend, after it the same backend is still alive and usable,
// and another operation really can take the destination exclusively.
func TestIR3LiveSessionDroppingTheExactKeyRefuses(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var other *drCoordination
	var hookErr error
	var holders []ir3Holder
	var dropped, sameBackendAlive bool
	var frozenPID, livePID int
	hit := false
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() {
		publicationFinalDecisionTestHook = old
		if other != nil {
			_ = other.close()
		}
	})
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		c := a.fence.coord
		frozenPID = c.backendPID
		holders = ir3AdvisoryHolders(t, super, ir3FenceKey(t, super, c.dest.Database, c.dest.Schema), c.dest.Database)
		released, err := ir3DropSharedFence(ctx, c)
		if err != nil {
			hookErr = err
			return
		}
		dropped = released
		if err := c.conn.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&livePID); err != nil {
			hookErr = err
			return
		}
		sameBackendAlive = livePID == frozenPID
		other, hookErr = openDRCoordinationExclusive(ctx, cfg.OwnerDSN, cfg)
	}

	s, err := Open(ctx, cfg, nil)
	if s != nil {
		_ = s.Close()
	}
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if hookErr != nil {
		t.Fatalf("causal setup: %v", hookErr)
	}
	if len(holders) != 1 || holders[0].mode != "ShareLock" || holders[0].pid != frozenPID {
		t.Fatalf("before the drop the destination must carry exactly one ShareLock held by the retained backend %d, got %+v", frozenPID, holders)
	}
	if !dropped {
		t.Fatal("pg_advisory_unlock_shared did not report the fence released on the original session")
	}
	if !sameBackendAlive {
		t.Fatalf("the original backend did not survive the drop (live=%d frozen=%d)", livePID, frozenPID)
	}
	if other == nil {
		t.Fatal("exclusive acquisition elsewhere did not happen, so exclusion was not actually lost")
	}
	if err == nil {
		t.Fatal("Open published while its ORIGINAL LIVE session no longer held the publication fence")
	}
	if !errors.Is(err, ErrRestoreCoordinationUnknown) || !strings.Contains(err.Error(), "no longer holds its publication fence") {
		t.Fatalf("want the pg_locks observation refusal, got %v", err)
	}
}

// TestIR3UnrelatedAdvisoryKeyIsNotTheFence proves the final observation is keyed to
// the DESTINATION. The live session holds an advisory lock, shared, on the same
// connection — only the key differs — and publication is still refused.
func TestIR3UnrelatedAdvisoryKeyIsNotTheFence(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var hookErr error
	var decoyHeld bool
	hit := false
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		c := a.fence.coord
		if _, err := ir3DropSharedFence(ctx, c); err != nil {
			hookErr = err
			return
		}
		if _, err := c.conn.ExecContext(ctx,
			"SELECT pg_catalog.pg_advisory_lock_shared($1::pg_catalog.int8)", c.lockKey+1); err != nil {
			hookErr = err
			return
		}
		if err := c.conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_locks l
 WHERE l.locktype='advisory' AND l.granted AND l.pid=pg_catalog.pg_backend_pid())`).Scan(&decoyHeld); err != nil {
			hookErr = err
		}
	}

	s, err := Open(ctx, cfg, nil)
	if s != nil {
		_ = s.Close()
	}
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if hookErr != nil {
		t.Fatalf("causal setup: %v", hookErr)
	}
	if !decoyHeld {
		t.Fatal("the decoy advisory lock was not held, so the case proves nothing")
	}
	if err == nil {
		t.Fatal("Open published while the live session held only an UNRELATED advisory lock")
	}
	if !errors.Is(err, ErrRestoreCoordinationUnknown) || !strings.Contains(err.Error(), "no longer holds its publication fence") {
		t.Fatalf("want the exact-key refusal, got %v", err)
	}
}

// TestIR3DirectoryMaintenanceAdmissionDecides covers the third admission path,
// which the delivery described in prose and exercised nowhere: maintenance must
// refuse when the retained session is lost before its final completion decision.
//
// The control leg is load-bearing. Without a maintenance run that succeeds
// unimpeded on this fixture, a refusal in the negative leg would prove nothing
// about the admission.
func TestIR3DirectoryMaintenanceAdmissionDecides(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.MaxConns = 1
	cfg.AuditSpoolMaxBytes = 0
	// The closed inventory routine is absent on this fixture, so maintenance needs
	// the admin pool branch to read the directory inventory at all.
	cfg.AdminDSN = pg.Admin
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s0, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("baseline boot: %v", err)
	}
	if err := s0.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatalf("system tenant: %v", err)
	}
	if err := s0.Close(); err != nil {
		t.Fatalf("close the baseline store: %v", err)
	}

	if _, _, _, cerr := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); cerr != nil {
		t.Fatalf("control maintenance must succeed before the loss case: %v", cerr)
	}

	hit := false
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	publicationFinalDecisionTestHook = func(a *publicationAdmission) {
		hit = true
		ir3Terminate(t, super, a.fence.coord.backendPID)
	}
	_, _, _, merr := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1)
	if !hit {
		t.Fatalf("the final decision was never reached on the maintenance path; maintenance returned %v", merr)
	}
	if merr == nil {
		t.Fatal("directory maintenance completed successfully after pre-L loss of the retained session")
	}
	if !strings.Contains(merr.Error(), "does not roll it back") {
		t.Fatalf("the maintenance refusal must state that it is not a rollback, got %v", merr)
	}
}

// TestIR3RequireMutatingRefusesAfterLiveFenceLoss is the early mutation guard's own
// negative case: the boot and maintenance paths call requireMutating BEFORE they
// mutate, and nothing exercised its refusal. The live-drop mechanism keeps it about
// the exact key rather than about a dead connection.
func TestIR3RequireMutatingRefusesAfterLiveFenceLoss(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adm, _, err := beginPostgresPublicationAdmission(ctx, cfg, RestoreEnrolmentWitness{}, opgate.Keyset{})
	if err != nil {
		t.Fatalf("take the admission: %v", err)
	}
	t.Cleanup(func() { _ = adm.close() })

	// POSITIVE CONTROL: while the fence is held, mutation is admitted.
	if err := adm.requireMutating(ctx); err != nil {
		t.Fatalf("a held admission must admit mutation: %v", err)
	}

	released, err := ir3DropSharedFence(ctx, adm.fence.coord)
	if err != nil || !released {
		t.Fatalf("drop the fence on the live session: released=%t err=%v", released, err)
	}
	var livePID int
	if err := adm.fence.coord.conn.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&livePID); err != nil {
		t.Fatalf("the session must stay alive for this case: %v", err)
	}
	if livePID != adm.fence.coord.backendPID {
		t.Fatalf("the retained backend changed (live=%d frozen=%d)", livePID, adm.fence.coord.backendPID)
	}

	merr := adm.requireMutating(ctx)
	if merr == nil {
		t.Fatal("requireMutating admitted a mutation after the live session lost the exact fence")
	}
	if !errors.Is(merr, ErrRestoreCoordinationUnknown) || !strings.Contains(merr.Error(), "no longer holds its publication fence") {
		t.Fatalf("want the pg_locks refusal from requireMutating, got %v", merr)
	}
	// And a lost admission cannot then be decided.
	if derr := adm.decide(ctx, CustodyObservation{}, true); derr == nil {
		t.Fatal("a refused-or-lost admission still reached a final decision")
	}
}

// TestIR3FinalObservationHasItsOwnFiniteBound is the causal for the bound.
//
// The caller's budget is six times the observation's own, and the case asserts the
// caller's context never expired — so the refusal can only have come from the bound
// decide owns. The stall is a real one: a supervisor session holds ACCESS EXCLUSIVE
// on the control relation, which is what the final verification must read, so the
// last readiness observation blocks on the server exactly as a wedged destination
// would make it block. Without the bound this boot would wait indefinitely WHILE
// HOLDING the shared publication fence.
func TestIR3FinalObservationHasItsOwnFiniteBound(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.AuditSpoolMaxBytes = 0
	_, _, keyset := drCompleteReaderFixture(t, pg, cfg)
	super := drOpenSuper(t, pg.Superuser)

	// POSITIVE CONTROL: with nothing stalling the final read, this fixture publishes.
	cctx, ccancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer ccancel()
	observed := drObservationFor(t, keyset)
	control, err := drOpenUnderBootAdmission(cctx, t, cfg, observed)
	if err != nil {
		t.Fatalf("control boot on the completed fixture: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("close the control store: %v", err)
	}

	callerBudget := 6 * drCoordinationTimeout
	ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
	defer cancel()
	blocker, err := super.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("open the blocking session: %v", err)
	}
	t.Cleanup(func() { _ = blocker.Rollback() })
	if err := blocker.QueryRowContext(ctx, "SELECT 1").Scan(new(int)); err != nil {
		t.Fatalf("establish the blocking session: %v", err)
	}
	before := ir3ClientBackends(t, super, pg.Database)

	var stalledAt time.Time
	var hookErr error
	hit := false
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	publicationFinalDecisionTestHook = func(*publicationAdmission) {
		hit = true
		if _, err := blocker.ExecContext(ctx, "LOCK TABLE "+drControlRelation+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			hookErr = err
			return
		}
		stalledAt = time.Now()
	}

	s, oerr := drOpenUnderBootAdmission(ctx, t, cfg, observed)
	elapsed := time.Since(stalledAt)
	if s != nil {
		_ = s.Close()
	}
	if !hit {
		t.Fatal("final-decision hook did not run")
	}
	if hookErr != nil {
		t.Fatalf("causal setup: %v", hookErr)
	}
	if oerr == nil {
		t.Fatal("Open published although its final readiness observation never completed")
	}
	if ctx.Err() != nil {
		t.Fatalf("the caller's context expired, so this proves nothing about the observation's own bound: %v", ctx.Err())
	}
	if elapsed >= callerBudget/2 {
		t.Fatalf("the final observation was not bounded by its own budget: %s elapsed against a caller budget of %s", elapsed, callerBudget)
	}
	if elapsed < drCoordinationTimeout/2 {
		t.Fatalf("the refusal came back in %s, too early to have been the stalled observation; it is not the behaviour under test", elapsed)
	}
	if !errors.Is(oerr, ErrRestorePublicationFenced) && !errors.Is(oerr, ErrRestoreCoordinationUnknown) {
		t.Fatalf("want a publication refusal from the stalled observation, got %v", oerr)
	}
	t.Logf("stalled final observation refused after %s under a %s caller budget: %v", elapsed, callerBudget, oerr)
	// The would-be store, its pools and the coordination session are gone. Wait for the
	// server to report the fenced backends closed. This drain is sub-second on an idle box,
	// but the assertion is about EVENTUAL closure, not its latency: under `-race` (a 2-10x
	// slowdown) with all nine race-* jobs sharing one loaded CI runner, server-side backend
	// exit lags well past the old 5s wall-clock window and turned a contention delay into a
	// false failure. The budget is widened to keep the invariant (backends must drain) while
	// giving a loaded runner realistic time; it stays well inside the caller budget and uses
	// context.Background, so it never races the caller's own deadline.
	deadline := time.Now().Add(30 * time.Second)
	for {
		after := ir3ClientBackends(t, super, pg.Database)
		if after <= before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unpublished sessions survived the bounded refusal: before=%d after=%d", before, after)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestIR3ReleaseUncertaintyIsDiagnosed pins the two halves of cleanup state to two
// DISTINCT operator-visible facts. Before the decision the boot is refused and the
// unconfirmed release must still be said out loud — openPrepared's deferred close
// discards the error, so without the warning the fact is lost. After the decision
// the store is published and the same unconfirmed release is cleanup uncertainty,
// explicitly not a pre-decision exclusion failure. Neither line may repeat, and
// neither may carry a credential.
func TestIR3ReleaseUncertaintyIsDiagnosed(t *testing.T) {
	const preL = "could not be confirmed released before the final decision"
	const postL = "cleanup is uncertain after linearization"

	t.Run("pre-L-refusal-is-diagnosed", func(t *testing.T) {
		pg := isolatedPGSplit(t)
		cfg := drPGConfig(pg)
		cfg.MaxConns = 1
		cfg.AuditSpoolMaxBytes = 0
		super := drOpenSuper(t, pg.Superuser)
		password := ir3DSNPassword(t, cfg.DSN)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		old := publicationFinalDecisionTestHook
		t.Cleanup(func() { publicationFinalDecisionTestHook = old })
		hit := false
		publicationFinalDecisionTestHook = func(a *publicationAdmission) {
			hit = true
			ir3Terminate(t, super, a.fence.coord.backendPID)
		}
		logs := ir3CaptureWarnings(t)
		s, err := Open(ctx, cfg, nil)
		if s != nil {
			_ = s.Close()
		}
		captured := logs.String()
		if !hit {
			t.Fatal("final-decision hook did not run")
		}
		if err == nil {
			t.Fatal("Open published after pre-L loss; the failure result must be preserved")
		}
		if got := strings.Count(captured, preL); got != 1 {
			t.Fatalf("want exactly one pre-decision release diagnostic, got %d in:\n%s", got, captured)
		}
		if got := strings.Count(captured, postL); got != 0 {
			t.Fatalf("a refused boot reported post-linearization cleanup uncertainty %d times:\n%s", got, captured)
		}
		if strings.Contains(captured, password) {
			t.Fatal("the cleanup diagnostic leaked the destination credential")
		}
	})

	t.Run("post-L-cleanup-is-distinct", func(t *testing.T) {
		pg := isolatedPGSplit(t)
		cfg := drPGConfig(pg)
		cfg.MaxConns = 1
		cfg.AuditSpoolMaxBytes = 0
		super := drOpenSuper(t, pg.Superuser)
		password := ir3DSNPassword(t, cfg.DSN)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		old := publicationAfterLinearizationTestHook
		t.Cleanup(func() { publicationAfterLinearizationTestHook = old })
		hit := false
		publicationAfterLinearizationTestHook = func(a *publicationAdmission) {
			hit = true
			ir3Terminate(t, super, a.fence.coord.backendPID)
		}
		logs := ir3CaptureWarnings(t)
		s, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("post-L cleanup uncertainty must not refuse publication: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close the published store: %v", err)
		}
		captured := logs.String()
		if !hit {
			t.Fatal("linearization hook did not run")
		}
		if got := strings.Count(captured, postL); got != 1 {
			t.Fatalf("want exactly one post-linearization cleanup diagnostic, got %d in:\n%s", got, captured)
		}
		if got := strings.Count(captured, preL); got != 0 {
			t.Fatalf("a published store reported a pre-decision exclusion failure %d times:\n%s", got, captured)
		}
		if strings.Contains(captured, password) {
			t.Fatal("the cleanup diagnostic leaked the destination credential")
		}
	})
}
