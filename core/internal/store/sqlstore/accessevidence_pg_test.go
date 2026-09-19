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
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// accessevidence_pg_test.go — the half of the access-evidence acceptance that
// only a real PostgreSQL can answer, run in the SPLIT-OWNER topology (a separate
// owner role runs the DDL, the application role carries runtime traffic).
//
// SQLite proves the contract; it cannot prove these:
//
//   - that the owner's DDL creates the four relations and the application role
//     receives DML on them through ALTER DEFAULT PRIVILEGES;
//   - that the append-only ACL revoke reaches them, so the application role
//     cannot TRUNCATE evidence — a statement no row trigger can observe;
//   - that two GENUINELY concurrent same-key ingests resolve through the unique
//     index rather than through SQLite's single writer, which serializes them
//     before they can race.

// openAccessEvidencePG opens a split-owner store, skipping when no PostgreSQL is
// configured for this run.
func openAccessEvidencePG(t *testing.T) (store.Store, pgSplitDSNs) {
	t.Helper()
	pg := isolatedPGSplit(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, pgSplitDSNs{App: pg.App, Owner: pg.Owner, Superuser: pg.Superuser, Database: pg.Database}
}

// pgSplitDSNs is the subset of the isolation DSNs these tests use.
type pgSplitDSNs struct {
	App       string
	Owner     string
	Superuser string
	Database  string
}

// TestAccessEvidencePostgresSplitOwnerRoundTrip is the PostgreSQL round trip:
// the four separate records are appended through the application role over
// owner-created relations, the store is closed and reopened, and everything —
// including the completeness verdict — reads back exactly.
func TestAccessEvidencePostgresSplitOwnerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, dsns := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "evidence-pg-"+uniqueSuffix())

	artifact := retainArtifact(t, st, tenant, "pg-artifact", "permit(principal, action, resource);")
	var transitionID, observationID, decisionID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()
		tr, err := ev.AppendAuthorityTransition(ctx, store.AuthorityTransitionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorityTransition, "pg-transition"),
			Transition: sdk.AuthorityTransitionContent{
				SchemaVersion:        sdk.AccessEvidenceSchemaVersion,
				SubjectKind:          sdk.SubjectPolicyArtifact,
				SubjectRef:           artifact.ID.String(),
				Transition:           sdk.TransitionActivate,
				GovernanceSurface:    "cedar",
				GovernanceRevision:   3,
				EffectiveAt:          canonicalInstant(t, 0),
				KnownAt:              canonicalInstant(t, time.Minute),
				ReasonCode:           "revision.published",
				SnapshotCompleteness: sdk.SnapshotCompleteWithinScope,
			},
		})
		if err != nil {
			return err
		}
		transitionID = tr.Record.ID

		dec, err := ev.AppendAuthorizationDecision(ctx, store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeAuthorizationDecision, "pg-decision"),
			Decision: decisionContent(sdk.AccessDependency{
				Kind: sdk.DependencyPolicyArtifact, Ref: artifact.ID.String(), Required: true,
			}),
		})
		if err != nil {
			return err
		}
		decisionID = dec.Record.ID

		content := observationContent(sdk.StageEffectConfirmed)
		content.Confirmation = sdk.ConfirmationResponse
		content.DecisionRef = decisionID.String()
		obs, err := ev.AppendActionObservation(ctx, store.ActionObservationAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypeActionObservation, "pg-observation"),
			Observation:          content,
		})
		if err != nil {
			return err
		}
		observationID = obs.Record.ID
		return nil
	}); err != nil {
		t.Fatalf("append on postgres: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("reopen split-owner store: %v", err)
	}
	defer reopened.Close() //nolint:errcheck // test cleanup

	if err := reopened.View(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()
		gotArtifact, err := ev.PolicyArtifact(ctx, artifact.ID)
		if err != nil {
			return fmt.Errorf("artifact: %w", err)
		}
		if gotArtifact.RecordDigest != artifact.RecordDigest || gotArtifact.LedgerRef == "" {
			t.Errorf("artifact round trip = %+v", gotArtifact.AccessEvidenceMeta)
		}
		gotTransition, err := ev.AuthorityTransition(ctx, transitionID)
		if err != nil {
			return fmt.Errorf("transition: %w", err)
		}
		if gotTransition.SubjectArtifactID != artifact.ID {
			t.Errorf("transition subject = %q, want %q", gotTransition.SubjectArtifactID, artifact.ID)
		}
		gotObservation, err := ev.ActionObservation(ctx, observationID)
		if err != nil {
			return fmt.Errorf("observation: %w", err)
		}
		if gotObservation.Observation.Confirmation != sdk.ConfirmationResponse {
			t.Errorf("confirmation = %q, want response", gotObservation.Observation.Confirmation)
		}
		if gotObservation.DecisionID != decisionID {
			t.Errorf("observation decision = %q, want %q", gotObservation.DecisionID, decisionID)
		}
		completeness, err := ev.DecisionCompleteness(ctx, decisionID)
		if err != nil {
			return fmt.Errorf("completeness: %w", err)
		}
		if !completeness.Reconstructible() {
			t.Errorf("completeness = %+v, want reconstructible", completeness)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back on postgres: %v", err)
	}
}

func TestDecisionReconstructionStampPostgres(t *testing.T) {
	st, _ := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "stamp-pg-"+uniqueSuffix())
	assertDecisionReconstructionStamp(t, st, tenant)
}

// TestAccessEvidencePostgresRollbackLeavesNothing repeats the transactional
// guarantee on the engine that actually has MVCC: an aborted transaction leaves
// neither the record nor its ledger event.
func TestAccessEvidencePostgresRollbackLeavesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, _ := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "evidence-pg-rb-"+uniqueSuffix())

	sentinel := errors.New("caller aborted after the append")
	var staged model.ID
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		res, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
			AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, "pg-rolled-back"),
			Artifact:             artifactContent("permit(principal, action, resource);"),
		})
		if aerr != nil {
			return aerr
		}
		staged = res.Record.ID
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Mutate = %v, want the caller's sentinel", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, gerr := sc.AccessEvidence().PolicyArtifact(ctx, staged)
		if !errors.Is(gerr, store.ErrNotFound) {
			t.Fatalf("after rollback the artifact reads back as %v, want ErrNotFound", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("view after rollback: %v", err)
	}
}

// TestAccessEvidencePostgresRefusesMutationAndTruncate measures the two halves
// of immutability that only exist on this engine, AS THE APPLICATION ROLE.
//
// The trigger and the ACL are different controls and neither substitutes for the
// other: a BEFORE UPDATE OR DELETE row trigger cannot observe TRUNCATE, so for a
// statement that empties an evidence relation in one shot the revoked privilege
// is the only defence the schema has.
func TestAccessEvidencePostgresRefusesMutationAndTruncate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, dsns := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "evidence-pg-immutable-"+uniqueSuffix())
	seedEveryAccessEvidenceRelation(t, st, tenant, "pg-immutable")
	assertAccessEvidenceGuardsPG(t, ctx, dsns, tenant)
}

// assertAccessEvidenceGuardsPG measures BOTH immutability controls, each against
// the role that can actually reach it.
//
// The split is the whole point, and the first version of this helper got it
// wrong by asking one role about both. On PostgreSQL the append-only ACL revoke
// denies the APPLICATION role UPDATE, DELETE and TRUNCATE outright, so its
// statements never reach the trigger: asserting "the trigger refused" against
// that role passes for the wrong reason and would keep passing with the trigger
// dropped. The trigger is the control for a role that HOLDS the privileges — the
// owner, a replication apply — so it is measured as the OWNER, which the revoke
// deliberately exempts.
//
// Together they are the guarantee: privilege stops the role that runs traffic,
// and the trigger stops the role that could otherwise rewrite history.
func assertAccessEvidenceGuardsPG(t *testing.T, ctx context.Context, dsns pgSplitDSNs, tenant model.TenantID) {
	t.Helper()

	// The application role: refused by PRIVILEGE, including TRUNCATE, which is a
	// statement-level operation no row trigger can observe.
	app := pinnedTenantConn(t, ctx, dsns.App, tenant)
	for _, table := range accessEvidenceTables() {
		var rows int
		if err := app.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&rows); err != nil {
			t.Fatalf("count %s as the application role: %v", table, err)
		}
		if rows == 0 {
			t.Fatalf("%s is empty for this tenant; the probes below would prove nothing", table)
		}
		for _, stmt := range []string{
			"UPDATE " + table + " SET record_digest = 'tampered'",
			"DELETE FROM " + table,
			"TRUNCATE TABLE " + table,
		} {
			_, err := app.ExecContext(ctx, stmt)
			if err == nil {
				t.Errorf("the application role executed %q on evidence", stmt)
				continue
			}
			if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
				t.Errorf("%q refused with %v; want the append-only privilege revoke", stmt, err)
			}
		}
	}

	// The owner role: holds the privileges, and is stopped by the TRIGGER.
	owner := pinnedTenantConn(t, ctx, dsns.Owner, tenant)
	for _, table := range accessEvidenceTables() {
		_, err := owner.ExecContext(ctx, "UPDATE "+table+" SET record_digest = 'tampered'")
		if err == nil {
			t.Errorf("%s accepted an UPDATE from the owner; the immutability trigger is not attached", table)
		} else if !strings.Contains(err.Error(), "table is append-only") {
			t.Errorf("%s refused the owner's UPDATE with %v; want the append-only trigger", table, err)
		}
		_, err = owner.ExecContext(ctx, "DELETE FROM "+table)
		if err == nil {
			t.Errorf("%s accepted a DELETE from the owner; the immutability trigger is not attached", table)
		} else if !strings.Contains(err.Error(), "table is append-only") {
			t.Errorf("%s refused the owner's DELETE with %v; want the append-only trigger", table, err)
		}
	}
}

// pinnedTenantConn reserves ONE connection and pins the tenant on it.
//
// Both halves are load-bearing. A pool would scatter the statements over
// connections that do not carry the pin, and WITHOUT the pin the FORCE-RLS
// policy raises `unrecognized configuration parameter "app.tenant_id"` — by
// design, so a forgotten bind fails loudly. That error would make every probe
// "fail" and the test would pass while measuring nothing.
func pinnedTenantConn(t *testing.T, ctx context.Context, dsn string, tenant model.TenantID) *sql.Conn {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve a connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx,
		"SELECT pg_catalog.set_config('app.tenant_id', $1, false)", string(tenant)); err != nil {
		t.Fatalf("pin the tenant: %v", err)
	}
	return conn
}

// TestAccessEvidencePostgresConcurrentSameKeyIngest is the causal concurrency
// case the contract asks for: two transactions ingest the SAME identity at the
// same time and the database's own idempotency decides, not a read that raced.
//
// The interleaving is driven by an OBSERVABLE barrier rather than a sleep: the
// second transaction is known to be blocked because PostgreSQL reports it
// waiting on a lock in pg_stat_activity — a fact about the server, not a guess
// about timing. Only then does the first transaction commit.
//
// WHAT THIS MEASURED, stated precisely because it is not what one would assume.
// The loser does NOT reach the unique index: it blocks at transaction start, on
// the per-tenant advisory lock the store already takes (observed waiting on
// `pg_advisory_xact_lock(hashtextextended($1,0))`), so by the time its own
// lookup runs the winner has committed and the delivery resolves as the exact
// duplicate it is. Same-tenant evidence ingests are therefore SERIALIZED on this
// engine, and the unique index is the backstop beneath that serialization rather
// than the thing normally exercised.
//
// So the index is measured directly as well, at the end of this test, by a raw
// duplicate INSERT as the owner. Two controls, because they answer two
// questions: this one asks what concurrent producers observe, and that one asks
// what the database would do if the serialization were ever absent.
func TestAccessEvidencePostgresConcurrentSameKeyIngest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, dsns := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "evidence-pg-race-"+uniqueSuffix())

	observer, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("open observer pool: %v", err)
	}
	defer observer.Close() //nolint:errcheck // test cleanup

	const sourceEventID = "pg-concurrent"
	inserted := make(chan struct{})
	release := make(chan struct{})
	loserErr := make(chan error, 1)
	winnerErr := make(chan error, 1)

	// The WINNER: append, announce that its row is staged, and hold the
	// transaction open until the loser is measurably blocked on it.
	go func() {
		winnerErr <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			if _, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
				AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, sourceEventID),
				Artifact:             artifactContent("permit(principal, action, resource);"),
			}); aerr != nil {
				return aerr
			}
			close(inserted)
			<-release // hold the transaction open; commit happens after this returns
			return nil
		})
	}()

	<-inserted

	// The LOSER: the same identity, concurrently. Its read misses (the winner is
	// uncommitted) and its insert waits on the unique index.
	go func() {
		loserErr <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, aerr := sc.AccessEvidence().RetainPolicyArtifact(ctx, store.PolicyArtifactAppend{
				AccessEvidenceAppend: appendMeta(t, sdk.EventTypePolicyArtifact, sourceEventID),
				Artifact:             artifactContent("permit(principal, action, resource);"),
			})
			return aerr
		})
	}()

	waitForBlockedBackend(t, ctx, observer, dsns.Database)
	close(release)

	if err := <-winnerErr; err != nil {
		t.Fatalf("the winning transaction failed: %v", err)
	}
	loser := <-loserErr
	t.Logf("the losing transaction resolved as: %v", loser)
	// Either outcome is correct and both are decided by the DATABASE, not by a
	// read: the loser's insert may error on the unique index (ErrAccessEvidenceRaced,
	// whose caller re-runs), or — if the winner committed before the loser's own
	// lookup — it resolves as the exact duplicate it is. What must never happen is
	// a second stored fact.
	if loser != nil && !errors.Is(loser, store.ErrAccessEvidenceRaced) {
		t.Fatalf("the losing transaction = %v, want nil (exact duplicate) or ErrAccessEvidenceRaced", loser)
	}

	var stored int
	if err := observer.QueryRowContext(ctx,
		"SELECT count(*) FROM "+policyArtifactTable+" WHERE tenant_id = $1 AND source_event_id = $2",
		string(tenant), sourceEventID).Scan(&stored); err != nil {
		t.Fatalf("count stored records: %v", err)
	}
	if stored != 1 {
		t.Fatalf("two concurrent ingests of one identity stored %d rows, want exactly 1", stored)
	}

	// The ground truth beneath the serialization: even a writer that bypasses the
	// repository entirely cannot store a second record under one identity. The
	// INSERT is issued as the OWNER, which append-only deliberately permits, so
	// what refuses it is the unique index and nothing else.
	owner := pinnedTenantConn(t, ctx, dsns.Owner, tenant)
	_, err = owner.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(id, tenant_id, created_at, updated_at, version,
		 schema_version, producer_instance, source_event_id, event_type, adapter_version,
		 occurred_at, recorded_at, record_digest, ledger_ref, content,
		 authority_id, surface, engine, artifact_digest, origin, availability)
		SELECT $1, tenant_id, created_at, updated_at, version,
		 schema_version, producer_instance, source_event_id, event_type, adapter_version,
		 occurred_at, recorded_at, record_digest, ledger_ref, content,
		 authority_id, surface, engine, artifact_digest, origin, availability
		FROM %s WHERE tenant_id = $2 AND source_event_id = $3`,
		policyArtifactTable, policyArtifactTable), string(model.NewID()), string(tenant), sourceEventID)
	if err == nil {
		t.Fatal("a raw duplicate insert under one identity was accepted; the ingest index is not unique")
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
		t.Fatalf("the raw duplicate insert refused with %v; want the unique-index violation", err)
	}
}

// waitForBlockedBackend blocks until PostgreSQL reports a backend on this
// database waiting for a lock. It polls the server's own view rather than
// sleeping for a guessed interval: the condition being waited for is a fact the
// server publishes, so the test can wait for the fact itself.
func waitForBlockedBackend(t *testing.T, ctx context.Context, observer *sql.DB, database string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		if err := observer.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_catalog.pg_stat_activity
			 WHERE datname = $1 AND wait_event_type = 'Lock' AND state = 'active'`,
			database).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked backends: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no backend blocked within 30s; the two ingests did not overlap and this case measured nothing")
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context done while waiting for the blocked backend: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestAccessEvidencePostgresCreatesRelationsOnASchemaThatPredatesThem is the
// PostgreSQL migration case: a database whose schema was built WITHOUT these
// four descriptors gets them created whole — columns, indexes, immutability
// trigger and append-only revoke — by the next boot, with no hand-authored
// migration and no new core version.
//
// The pre-descriptor state is built by applying the core migrations with the
// four descriptors FILTERED OUT, which is the repository's own idiom for "a
// database created by an earlier build" (guardedition_test.go seeds core v5 the
// same way). It is a faithful staging in a way that dropping the relations from
// an already-migrated database is NOT on this engine — see
// TestAccessEvidencePostgresGuardRefusesADroppedAndRecreatedRelation, which
// measures exactly why.
//
// THE DIRECTORY DESCRIPTORS ARE FILTERED TOO, and that is a correction rather than
// tidying. A <=v5 source predates BOTH families: its v2 created neither, and core v7 is
// what installs the directory relations on such a database. Leaving them in staged a
// pre-v6 database carrying one family and not the other — a pair no migration of this
// product commits — which the access-evidence boot classifier now refuses by name. The
// allowlisted legacy profile is the one this fixture reproduces.
func TestAccessEvidencePostgresCreatesRelationsOnASchemaThatPredatesThem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect unavailable")
	}
	owner, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open owner pool: %v", err)
	}
	directory := map[string]bool{}
	for _, table := range coreDirectoryRelationNames {
		directory[table] = true
	}
	legacy := make([]model.EntityDescriptor, 0)
	for _, d := range coreDescriptors() {
		if !isAccessEvidenceKind(d.Kind) && !directory[d.Table] {
			legacy = append(legacy, d)
		}
	}
	want := len(coreDescriptors()) - len(accessEvidenceDescriptors()) - len(coreDirectoryRelationNames)
	if len(legacy) != want {
		owner.Close() //nolint:errcheck // cleanup on a failure path
		t.Fatalf("the pre-descriptor set kept %d descriptors, want %d", len(legacy), want)
	}
	migrations := buildCoreMigrations(dia, legacy, nil, nil)
	if err := migrate.Apply(ctx, owner, dia, coreTrackingTable, migrations[:5]); err != nil {
		owner.Close() //nolint:errcheck // cleanup on a failure path
		t.Fatalf("seed the pre-descriptor schema: %v", err)
	}
	for _, table := range accessEvidenceTables() {
		var present bool
		if err := owner.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = 'public' AND tablename = $1)",
			table).Scan(&present); err != nil {
			owner.Close() //nolint:errcheck // cleanup on a failure path
			t.Fatalf("introspect %s: %v", table, err)
		}
		if present {
			owner.Close() //nolint:errcheck // cleanup on a failure path
			t.Fatalf("%s exists in the pre-descriptor schema; this case would measure nothing", table)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner pool: %v", err)
	}

	// The upgrade boot.
	upgraded, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("upgrade boot: %v", err)
	}
	defer upgraded.Close() //nolint:errcheck // test cleanup

	tenant := provisionTenant(t, upgraded, "evidence-pg-upgrade-"+uniqueSuffix())
	seedEveryAccessEvidenceRelation(t, upgraded, tenant, "pg-upgraded")

	// Created WHOLE means guarded, not merely present: the same two controls a
	// database that always had these relations carries.
	assertAccessEvidenceGuardsPG(t, ctx, pgSplitDSNs{App: pg.App, Owner: pg.Owner}, tenant)
}

// isAccessEvidenceKind reports whether a descriptor is one of the four
// access-evidence relations.
func isAccessEvidenceKind(kind model.Kind) bool {
	for _, d := range accessEvidenceDescriptors() {
		if d.Kind == kind {
			return true
		}
	}
	return false
}

// TestAccessEvidencePostgresGuardRefusesADroppedAndRecreatedRelation records a
// MEASURED behaviour of the append-only guard control plane, and it is a control
// worth keeping rather than a limitation to work around.
//
// Once a database has carried the guard on an evidence relation, that fact is in
// the durable inventory. Dropping the relation and letting the next boot
// re-create it does not restore the lineage the receipts describe, so the boot
// REFUSES instead of quietly accepting a relation whose history it cannot
// account for. An operator therefore cannot launder away an evidence table by
// dropping it and restarting.
//
// This is also why the migration case above stages its pre-descriptor state by
// filtering descriptors rather than by dropping tables: a dropped-and-recreated
// relation is a DIFFERENT state from one that never existed, and only the second
// is what an upgrading deployment is in.
func TestAccessEvidencePostgresGuardRefusesADroppedAndRecreatedRelation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, dsns := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "evidence-pg-dropped-"+uniqueSuffix())
	seedEveryAccessEvidenceRelation(t, st, tenant, "pg-dropped")
	if err := st.Close(); err != nil {
		t.Fatalf("close before dropping: %v", err)
	}

	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open owner pool: %v", err)
	}
	if _, err := owner.ExecContext(ctx, "DROP TABLE "+actionObservationTable); err != nil {
		owner.Close() //nolint:errcheck // cleanup on a failure path
		t.Fatalf("drop %s: %v", actionObservationTable, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner pool: %v", err)
	}

	reopened, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}, nil)
	if err == nil {
		reopened.Close() //nolint:errcheck // cleanup on the unexpected path
		t.Fatal("the boot accepted a dropped-and-recreated evidence relation; a guard whose lineage cannot be accounted for must refuse")
	}
	// THE BOUNDARY MOVED WITH CORE v9 AND THE MOVE IS A STRENGTHENING, so it is named
	// rather than loosened. Dropping one of the four now trips the read-only boot
	// classifier — an all-or-none question asked before any migration runs — instead of
	// the later guard-divergence check. Both are refusals that create nothing; this one
	// happens earlier and says exactly which relation is missing.
	if !strings.Contains(err.Error(), "divergent") &&
		!strings.Contains(err.Error(), "partial access-evidence relation set") {
		t.Fatalf("boot refusal = %v, want the partial-set or guard divergence refusal", err)
	}
}
