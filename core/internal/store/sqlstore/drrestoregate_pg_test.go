// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// The operation identities these cases install. They are fixed literals rather
// than random values so a refusal message can be asserted to NAME the operation
// that holds a destination — the property that distinguishes an earned identity
// from an invented one.
const (
	drTestOpA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	drTestOpB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	drTestPlanA = "1111111111111111111111111111111111111111111111111111111111111111"
	drTestPlanB = "2222222222222222222222222222222222222222222222222222222222222222"
	drTestKeyB  = "4444444444444444444444444444444444444444444444444444444444444444"
)

var drTestKeyA = drFactualKeyset().SHA256 // test public identities, never selected-signer authority

func drPGConfig(pg pgtest.DSNs) store.Config {
	return store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
	}
}

func drOpenSuper(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open the maintenance connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// drRelationNames lists every relation of the engine schema, which is how the
// early installer's footprint is measured: not "did it create the control" but
// "did it create ANYTHING else".
func drRelationNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT c.relname FROM pg_catalog.pg_class c
                 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f','S')
                ORDER BY c.relname`, dialect.EngineSchema)
	if err != nil {
		t.Fatalf("list relations: %v", err)
	}
	defer rows.Close() //nolint:errcheck // test helper
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// THE EARLY, MINIMAL INSTALLER ON A max0 DESTINATION.
//
// The whole claim of the pre-dump installer is negative: it fences a destination
// and creates NOTHING ELSE. So this measures the footprint rather than the
// success — the relation list before and after, and by name the objects a boot
// would have created (the migration tracker, the rollout trio, SYSTEM's org table,
// the H authority relation, the audit ledger). Any of those appearing here would
// mean the installer had quietly become a boot.
func TestPostgresRestoreControlInstallsOnAnEmptyDestinationAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isolate func(testing.TB) pgtest.DSNs
	}{
		{"single-role", isolatedPG},
		{"split-role", isolatedPGSplit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pg := tc.isolate(t)
			ctx := context.Background()
			super := drOpenSuper(t, pg.Superuser)

			before := drRelationNames(t, super)
			report, err := InstallPendingRestoreControl(ctx, drPGConfig(pg), PendingRestoreSpec{
				OpID: drTestOpA, PlanSHA256: drTestPlanA,
			})
			if err != nil {
				t.Fatalf("install the restore control on an empty destination: %v", err)
			}
			if !report.Present || !report.Created || report.Revision != 1 || report.State != opgate.StatePending {
				t.Fatalf("the installer's read-back is not a fresh pending control: %+v", report)
			}
			if report.OpID != drTestOpA || report.PlanSHA256 != drTestPlanA {
				t.Fatalf("the read-back names another operation: %+v", report)
			}
			if report.Database != pg.Database || report.Schema != dialect.EngineSchema {
				t.Fatalf("the control is not bound to this destination: %+v", report)
			}
			if report.SystemIdentifier == "" {
				t.Fatal("the control recorded no cluster identity, so a completed control could not be compared against one")
			}

			after := drRelationNames(t, super)
			added := map[string]bool{}
			had := map[string]bool{}
			for _, n := range before {
				had[n] = true
			}
			for _, n := range after {
				if !had[n] {
					added[n] = true
				}
			}
			if len(added) != 1 || !added[dialect.DRRestoreControlTable] {
				t.Fatalf("the early installer created more than its control.\nbefore: %v\nafter:  %v", before, after)
			}
			for _, forbidden := range []string{
				coreTrackingTable, moduleTablesTracking,
				dialect.ControlRolloutStateTable, dialect.ControlRolloutTransitionTable,
				dialect.ControlRolloutClassificationTable,
				dialect.DirectoryWriterControlTable,
				"orgs", "core_user_authority", "core_directory_epoch", "audit_events",
			} {
				if added[forbidden] {
					t.Fatalf("the early installer created %q: it must create no SYSTEM, no genesis, no directory authority and no schema growth outside its control", forbidden)
				}
			}
			// And no coverage is claimed: the destination is still one no store has
			// ever opened, which the absence of the core tracker proves.
			var trackerRows int
			if err := super.QueryRowContext(ctx,
				`SELECT count(*) FROM pg_catalog.pg_class c
                     JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                    WHERE n.nspname = $1 AND c.relname = $2`,
				dialect.EngineSchema, coreTrackingTable).Scan(&trackerRows); err != nil {
				t.Fatal(err)
			}
			if trackerRows != 0 {
				t.Fatal("the early installer left a core migration tracker behind")
			}
		})
	}
}

// The ACL the control is installed with: the roles that must READ it can, and
// split app/admin cannot write it; the owner retains inherent authority. PUBLIC is
// asserted explicitly because a PUBLIC grant here
// would break the closed H/G inventory role's own posture check, which counts
// table-wide, PUBLIC and inherited grants over every relation.
func TestPostgresRestoreControlAccessPostureIsExactAndNeverPublic(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)
	cfg.AdminDSN = pg.Admin
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	super := drOpenSuper(t, pg.Superuser)
	rel := dialect.EngineSchema + "." + dialect.DRRestoreControlTable

	var publicSelect bool
	if err := super.QueryRowContext(ctx,
		`SELECT pg_catalog.has_table_privilege('public', $1, 'SELECT')`, rel).Scan(&publicSelect); err != nil {
		t.Fatalf("ask about PUBLIC: %v", err)
	}
	if publicSelect {
		t.Fatal("the restore control is readable by PUBLIC: that grant is counted by the closed directory-inventory role's posture check and would refuse a correct deployment")
	}

	app := drRoleOf(t, pg.App)
	for _, tc := range []struct {
		privilege string
		want      bool
	}{
		{"SELECT", true},
		{"INSERT", false},
		{"UPDATE", false},
		{"DELETE", false},
		{"TRUNCATE", false},
	} {
		var got bool
		if err := super.QueryRowContext(ctx,
			`SELECT pg_catalog.has_table_privilege($1, $2, $3)`, app, rel, tc.privilege).Scan(&got); err != nil {
			t.Fatalf("ask about %s: %v", tc.privilege, err)
		}
		if got != tc.want {
			t.Fatalf("the application role %q has %s=%t on the restore control, want %t: it reads the fence and never writes it", app, tc.privilege, got, tc.want)
		}
	}
}

func drRoleOf(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test helper
	var role string
	if err := db.QueryRowContext(context.Background(), "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	return role
}

// THE CONTROL FOR EVERY REFUSAL BELOW: a destination with no control and no
// witness opens normally, with every guard it already had.
func TestPostgresOpenProceedsWithoutAControlOrWitness(t *testing.T) {
	pg := isolatedPG(t)
	st, err := Open(context.Background(), drPGConfig(pg), nil)
	if err != nil {
		t.Fatalf("an ordinary destination with no restore control was refused: %v", err)
	}
	_ = st.Close()
}

func TestPostgresOpenRefusesAControlThatBlocksPublication(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	st, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open PUBLISHED a store for a destination whose restore control is pending")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("a pending control produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), drTestOpA) {
		t.Fatalf("the refusal does not name the operation holding the destination: %v", err)
	}
	// The refusal happened BEFORE anything was created: the migration tracker is
	// still absent, so this destination is exactly as the installer left it.
	super := drOpenSuper(t, pg.Superuser)
	for _, name := range drRelationNames(t, super) {
		if name != dialect.DRRestoreControlTable {
			t.Fatalf("the fenced boot created %q before refusing", name)
		}
	}
}

// ABSENT WITH A WITNESS is a LOSS, and it is the row a source map cannot infer:
// nothing in the database says a control was ever there.
func TestPostgresOpenRefusesAnAbsentControlWithASurvivingWitness(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)

	witness := RestoreEnrolmentWitness{
		Enrolled:    true,
		Destination: drFixtureDestination(t, pg.Superuser, pg.Database),
	}
	st, err := OpenWithRestoreWitness(ctx, cfg, nil, witness)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open PUBLISHED a store for a destination whose control is gone while local evidence says it was enrolled")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("a lost control produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), "LOSS") {
		t.Fatalf("the refusal does not say the control was lost: %v", err)
	}

	// A foreign surviving local record is a binding refusal; it cannot be
	// silently discarded to turn this request into unwitnessed legacy admission.
	foreign := RestoreEnrolmentWitness{Enrolled: true, Destination: drFixtureDestination(t, pg.Superuser, "someone_elses_db")}
	st2, err := OpenWithRestoreWitness(ctx, cfg, nil, foreign)
	if err == nil {
		_ = st2.Close()
		t.Fatal("a foreign local witness was silently ignored")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("wrong foreign-witness refusal: %v", err)
	}
}

// A relation carrying the control's NAME and another shape is refused, never
// adopted and never repaired.
func TestPostgresOpenRefusesAMalformedControl(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	super := drOpenSuper(t, pg.Superuser)
	if _, err := super.ExecContext(ctx,
		`CREATE TABLE `+dialect.EngineSchema+`.`+dialect.DRRestoreControlTable+` (control_key text PRIMARY KEY, note text)`); err != nil {
		t.Fatalf("plant a look-alike control: %v", err)
	}
	st, err := Open(ctx, drPGConfig(pg), nil)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open ADOPTED a relation carrying the control's name and another shape")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("a malformed control produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), "did not write") {
		t.Fatalf("the refusal does not say the control is foreign: %v", err)
	}
	// The look-alike is left exactly as it was: refused, not repaired, not dropped.
	var columns int
	if err := super.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_catalog.pg_attribute a
                 JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
                 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped`,
		dialect.EngineSchema, dialect.DRRestoreControlTable).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 2 {
		t.Fatalf("the foreign relation was altered: it now has %d columns", columns)
	}
}

// A control that exists and cannot be READ is a refusal. This is the row that
// keeps a hardened or drifted grant from being read as "no control here".
func TestPostgresOpenRefusesAnUnreadableControl(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)
	_, _, keyset := drCompleteReaderFixture(t, pg, cfg)
	// The completed control opens normally first: without this the revoke below
	// could be refusing for some other reason entirely.
	//
	// It goes through the BOOT ADMISSION with the matching measurement, because a
	// completed destination has no other path now: a plain Open supplies no observation
	// of the custody it loaded and is refused for that, which would make this positive
	// control measure the wrong refusal.
	st, err := drOpenUnderBootAdmission(ctx, t, cfg, drObservationFor(t, keyset))
	if err != nil {
		t.Fatalf("a completed control bound to this destination was refused: %v", err)
	}
	_ = st.Close()

	super := drOpenSuper(t, pg.Superuser)
	app := drRoleOf(t, pg.App)
	if _, err := super.ExecContext(ctx,
		fmt.Sprintf(`REVOKE SELECT ON %s.%s FROM %q`, dialect.EngineSchema, dialect.DRRestoreControlTable, app)); err != nil {
		t.Fatalf("revoke the application role's read: %v", err)
	}
	st2, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st2.Close()
		t.Fatal("Open PUBLISHED a store for a destination whose control it could not read")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("an unreadable control produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), "missing explicit ACL") {
		t.Fatalf("the refusal did not identify the missing control SELECT before row access: %v", err)
	}
}

// A COMPLETED control is compared EXACTLY: against the destination it names, and
// against the custody local evidence says was authorized.
func TestPostgresOpenComparesACompletedControlExactly(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)
	_, _, keyset := drCompleteReaderFixture(t, pg, cfg)
	// Matching evidence opens — through the BOOT ADMISSION, which is the only path a
	// completed destination has now. A direct Open with a matching witness no longer
	// suffices: the witness says this destination was enrolled under that digest, and
	// the ratified contract requires the custody this process ACTUALLY loaded as well.
	// The leg below is unchanged, because a witness naming another keyset is refused
	// at the gate before any custody comparison is reached.
	st, err := drOpenUnderBootAdmission(ctx, t, cfg, drObservationFor(t, keyset))
	if err != nil {
		t.Fatalf("a completed control and matching local custody evidence were refused: %v", err)
	}
	_ = st.Close()

	// Evidence naming ANOTHER keyset is a node whose custody selection is not the
	// one the completed operation published.
	st2, err := OpenWithRestoreWitness(ctx, cfg, nil, RestoreEnrolmentWitness{
		Enrolled: true, Destination: drFixtureDestination(t, pg.Superuser, pg.Database), KeysetSHA256: drTestKeyB,
	})
	if err == nil {
		_ = st2.Close()
		t.Fatal("Open PUBLISHED a store whose local custody evidence names a keyset the completed control did not authorize")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), "custody selection") {
		t.Fatalf("the keyset mismatch produced the wrong refusal: %v", err)
	}

	// And a control bound to another DESTINATION is not evidence about this one.
	super := drOpenSuper(t, pg.Superuser)
	if _, err := super.ExecContext(ctx,
		`UPDATE `+dialect.EngineSchema+`.`+dialect.DRRestoreControlTable+` SET destination_database = 'somewhere_else'`); err != nil {
		t.Fatalf("rebind the control: %v", err)
	}
	st3, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st3.Close()
		t.Fatal("Open accepted a completed control bound to another destination")
	}
	if !strings.Contains(err.Error(), "does not match the measured PostgreSQL destination") {
		t.Fatalf("the destination mismatch produced the wrong refusal: %v", err)
	}
}

// ONE TRY AGAINST A HELD EXCLUSIVE FENCE, and the OP_ID is EARNED, not invented.
//
// The holder takes the fence BEFORE it installs anything, which is the real
// sequence: so the first half of this case has a fenced destination with no
// readable control, and the refusal must say that no identity is available rather
// than substituting one.
func TestPostgresCoordinationIsOneTryAndNamesOnlyAReadableOperation(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)

	holder, err := openDRCoordinationExclusive(ctx, pg.Owner, drPGConfig(pg))
	if err != nil {
		t.Fatalf("take the fence exclusively: %v", err)
	}
	// No control installed yet: the busy refusal must NOT name an operation.
	_, err = openDRCoordinationShared(ctx, cfg)
	if !errors.Is(err, ErrRestorePublicationBusy) {
		_ = holder.close()
		t.Fatalf("a held fence produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), "none is invented") {
		_ = holder.close()
		t.Fatalf("the busy refusal does not say that no operation identity is available: %v", err)
	}
	for _, invented := range []string{drTestOpA, "pid", "unknown-operation"} {
		if strings.Contains(err.Error(), invented) {
			_ = holder.close()
			t.Fatalf("the busy refusal invented an identity (%q): %v", invented, err)
		}
	}
	// An ordinary Open sees the same refusal, through the same one try.
	st, oerr := Open(ctx, cfg, nil)
	if oerr == nil {
		_ = st.Close()
		_ = holder.close()
		t.Fatal("Open PUBLISHED a store for a destination another session holds exclusively")
	}
	if !errors.Is(oerr, ErrRestorePublicationBusy) {
		_ = holder.close()
		t.Fatalf("Open's refusal on a held destination has the wrong classification: %v", oerr)
	}
	if err := holder.close(); err != nil {
		t.Fatalf("release the fence: %v", err)
	}

	// With a readable control in place, the identity IS available and is named.
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpB, PlanSHA256: drTestPlanB,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	holder2, err := openDRCoordinationExclusive(ctx, pg.Owner, drPGConfig(pg))
	if err != nil {
		t.Fatalf("re-take the fence: %v", err)
	}
	defer holder2.close() //nolint:errcheck // asserted below by the release case
	_, err = openDRCoordinationShared(ctx, cfg)
	if !errors.Is(err, ErrRestorePublicationBusy) {
		t.Fatalf("a held fence produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), drTestOpB) {
		t.Fatalf("the busy refusal does not name the operation whose control is readable: %v", err)
	}
}

// The fence is OWNED: it is released when the call that took it returns, on the
// success path as well as the refusal path. A fence left behind would refuse the
// next boot of a healthy destination.
func TestPostgresFenceIsReleasedOnEveryPath(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)

	held := func() bool {
		t.Helper()
		super := drOpenSuper(t, pg.Superuser)
		var n int
		if err := super.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_catalog.pg_locks
              WHERE locktype = 'advisory'
                AND objid = (pg_catalog.hashtextextended($1 || $2 || ':' || $3, 0) & 4294967295)::oid
                AND classid = ((pg_catalog.hashtextextended($1 || $2 || ':' || $3, 0) >> 32) & 4294967295)::oid`,
			drRestoreLockName+":", pg.Database, dialect.EngineSchema).Scan(&n); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		return n > 0
	}

	if held() {
		t.Fatal("the destination was already fenced before this case started")
	}
	// The success path.
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = st.Close()
	if held() {
		t.Fatal("the publication fence survived a SUCCESSFUL Open: a construction lock held by a serving process would claim an exclusion the product does not promise")
	}
	// The refusal path.
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if held() {
		t.Fatal("the installer's exclusive fence survived its own call")
	}
	if _, err := Open(ctx, cfg, nil); err == nil {
		t.Fatal("the pending control did not refuse")
	}
	if held() {
		t.Fatal("the publication fence survived a REFUSED Open")
	}
}

// MaxConns=1 ON BOTH POOLS, AND NO SELF-BLOCK.
//
// This is the case the dedicated coordination pool exists for. cfg.MaxConns caps
// the application pool, the owner pool and the admin pool alike, so a coordination
// connection taken from any of them would be one this boot could no longer have.
func TestPostgresMaxConnsOneDoesNotSelfBlock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isolate func(testing.TB) pgtest.DSNs
	}{
		{"single-role", isolatedPG},
		{"split-role", isolatedPGSplit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pg := tc.isolate(t)
			ctx := context.Background()
			cfg := store.Config{
				Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 1,
			}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("a one-connection pool could not boot with the publication fence held: %v", err)
			}
			_ = st.Close()
			// And the installer, which holds the fence AND the migration lock AND a
			// transaction at the same time.
			if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
				OpID: drTestOpA, PlanSHA256: drTestPlanA,
			}); err != nil {
				t.Fatalf("the installer self-blocked on a one-connection pool: %v", err)
			}
		})
	}
}

// CAS belongs to a live private control operation. The old public COMPLETE
// premise was the authority defect: noncomplete CAS retains the substantive
// predecessor/operation/revision checks; completed Open is a separate SQL fixture.
func TestPostgresRestoreControlTransitionsByCompareAndSet(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)
	spec := PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}
	installed, err := InstallPendingRestoreControl(ctx, cfg, spec)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	super := drOpenSuper(t, pg.Superuser)
	before := drRawControlSnapshot(t, super)
	if _, err := drTransitionControlForTest(ctx, cfg, spec, restorePredecessor{installed.Revision + 1, opgate.StatePending}, opgate.StateIndeterminate); !errors.Is(err, ErrRestoreControlConflict) {
		t.Fatalf("nonexistent predecessor accepted: %v", err)
	}
	if _, err := drTransitionControlForTest(ctx, cfg, PendingRestoreSpec{OpID: drTestOpB, PlanSHA256: drTestPlanA}, restorePredecessor{installed.Revision, opgate.StatePending}, opgate.StateIndeterminate); !errors.Is(err, ErrRestoreControlConflict) {
		t.Fatalf("another operation transitioned this control: %v", err)
	}
	if after := drRawControlSnapshot(t, super); before != after {
		t.Fatal("refused CAS changed the predecessor")
	}
	done, err := drTransitionControlForTest(ctx, cfg, spec, restorePredecessor{installed.Revision, opgate.StatePending}, opgate.StateIndeterminate)
	if err != nil {
		t.Fatalf("exact predecessor refused: %v", err)
	}
	if done.State != opgate.StateIndeterminate || done.Revision != installed.Revision+1 || done.KeysetSHA256 != "" {
		t.Fatalf("wrong noncomplete readback: %+v", done)
	}
	// Reader-only premise: compiled family/ACL from the real installer, complete
	// row from clearly named test SQL. This performs no restore or finalization.
	drSetCompleteReaderRowForTest(t, pg, cfg)
	// Through the boot admission with the fixture's own measurement: under the ratified
	// sublot 6 an ordinary boot of a COMPLETED destination is one that loaded the
	// custody that operation published and can show it.
	st, err := drOpenUnderBootAdmission(ctx, t, cfg, drObservationFor(t, drFactualKeyset()))
	if err != nil {
		t.Fatalf("a completed reader fixture refused ordinary boot: %v", err)
	}
	_ = st.Close()
}

// A control that is already there is VERIFIED, never adopted: another operation or
// another plan is a refusal, and the installed control is left untouched.
func TestPostgresRestoreControlRefusesAnotherOperationsControl(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := drPGConfig(pg)
	first, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// The SAME operation is idempotent.
	again, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	})
	if err != nil {
		t.Fatalf("re-installing the same operation's control was refused: %v", err)
	}
	if again.Created || again.Revision != first.Revision {
		t.Fatalf("re-installing rewrote the control: %+v", again)
	}
	// Another operation is not.
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpB, PlanSHA256: drTestPlanB,
	}); !errors.Is(err, ErrRestoreControlConflict) {
		t.Fatalf("a second operation installed itself over the first: %v", err)
	}
	read, err := ReadPostgresRestoreControl(ctx, cfg)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if read.OpID != drTestOpA || read.Revision != first.Revision {
		t.Fatalf("the refused installation changed the control: %+v", read)
	}
}
