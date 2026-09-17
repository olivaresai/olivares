// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// ⛔ IR5-C2 / F1: `migrate apply` WAS PERMANENTLY REFUSED ON A RESTORED DESTINATION.
//
// The independent review measured it against the baseline on one fixture: on a COMPLETE
// PostgreSQL control, `ApplyMigrations` succeeded at 07729a2 and refused at aa7694a/7b9ef17.
// The refusal was unconditional — the path loads no signing key, so no input existed that
// could satisfy the custody requirement — and it landed AFTER the schema had been applied.
// The ratified operating order restore → migrate → GRANT → serve could not complete on any
// destination this product restores.
//
// Custody is the SERVING publication's duty. The gate, the pre-mutation admission check and
// the explicit final successful-completion decision are every mutating preparation's, and
// they are all still here — the cases below prove both halves.
func TestIR5C2MigrateApplySucceedsOnACompletedDestination(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, digest := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
	ir5CompleteControlWithKeyset(t, cfg, pg.Superuser, digest)

	// The ADMIN DSN IS SET AND MUST NOT BE OPENED. ApplyMigrations copies the config by
	// value and clears it, because a schema-only phase performs no cross-tenant read; a
	// credential that is merely unused is still one this phase must not dial. An
	// unroutable value proves it: if the phase opened it, this call could not succeed.
	migrateCfg := cfg
	migrateCfg.AdminDSN = "postgres://nobody:nobody@127.0.0.1:1/never?sslmode=disable&connect_timeout=1"

	before := drCoordinationAcquisitions.Load()
	if err := ApplyMigrations(ctx, migrateCfg, nil); err != nil {
		t.Fatalf("migrate apply was refused on a destination a restore completed: %v", err)
	}
	// ONE shared acquisition for the whole phase, and no reacquisition: the session that
	// read the control before the migration is the session the final decision is made on.
	if got := drCoordinationAcquisitions.Load() - before; got != 1 {
		t.Fatalf("the schema-only phase took %d shared acquisitions, not exactly one", got)
	}

	// The schema really is applied — the phase did work rather than returning early.
	super := drOpenSuper(t, pg.Superuser)
	var relations int
	if err := super.QueryRowContext(ctx,
		`SELECT pg_catalog.count(*)::pg_catalog.int4 FROM pg_catalog.pg_class c
                  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
                  WHERE n.nspname=$1 AND c.relkind='r'`, "public").Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations < 2 {
		t.Fatalf("the schema-only phase returned success having created %d relations", relations)
	}
}

// The OTHER half of the distinction, on the same destination: a SERVING publication of a
// completed target still refuses without the actual selected custody. If this regressed,
// the F1 fix would have been a removal of the requirement rather than a scoping of it.
func TestIR5C2ServingOpenStillRefusesTheSameCompletedDestination(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	authorized, digest := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
	ir5CompleteControlWithKeyset(t, cfg, pg.Superuser, digest)

	if err := ApplyMigrations(ctx, cfg, nil); err != nil {
		t.Fatalf("migrate apply: %v", err)
	}
	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("a serving Open published a completed destination with no custody measurement")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) ||
		!strings.Contains(err.Error(), "no observation of the custody it actually loaded") {
		t.Fatalf("wrong refusal for a serving publication: %v", err)
	}

	// And the authorized custody publishes, through the admission that loads it.
	wire, kerr := opgate.NewKeyset(authorized)
	if kerr != nil {
		t.Fatal(kerr)
	}
	adm, aerr := BeginLocalAdmission(ctx, cfg, t.TempDir())
	if aerr != nil {
		t.Fatalf("take the admission: %v", aerr)
	}
	defer adm.Close()
	served, serr := adm.Open(ctx, nil, drObservationFor(t, wire), nil)
	if serr != nil {
		t.Fatalf("the authorized custody was refused: %v", serr)
	}
	_ = served.Close()
}

// ir5CompletedMaintainableDestination builds the destination the two directory-maintenance
// cases below need: migrated, carrying a SYSTEM organization witness, and then enrolled
// under a COMPLETE restore control. It returns the admin-bearing config the maintenance
// caller uses.
//
// ORDER MATTERS, and it is the product's own order: migrate, then the restore control, then
// serve/maintain. The control's expected ACL is a function of the CONFIGURED ROLES, admin
// included (resolveDRControlRoles), and ApplyMigrations deliberately copies the config and
// clears AdminDSN — a schema-only phase performs no cross-tenant read, so an admin
// credential must not merely go unused, it must not be opened. Installing the control AFTER
// the migration therefore keeps one role set for the install and the verification.
//
// The SYSTEM witness the reconcile needs is provisioned through the EXISTING REAL SEAM — an
// ordinary serving Open of the still-unenrolled destination, then EnsureSystemTenant —
// rather than by planting rows or installing a hook. A configured AdminDSN then makes the
// reconcile use the admin-DSN inventory authority instead of the closed routine
// (directoryepoch.go), which is the supported split-pool posture and the prerequisite that
// lets the callback and the final decision actually execute.
func ir5CompletedMaintainableDestination(ctx context.Context, t *testing.T, pg pgtest.DSNs) store.Config {
	t.Helper()
	cfg := drPGConfig(pg)
	cfg.AdminDSN = pg.Admin
	if err := ApplyMigrations(ctx, drPGConfig(pg), nil); err != nil {
		t.Fatalf("migrate apply on the clean destination: %v", err)
	}
	seed, serr := Open(ctx, cfg, nil)
	if serr != nil {
		t.Fatalf("open the clean destination to provision its SYSTEM witness: %v", serr)
	}
	if err := seed.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		_ = seed.Close()
		t.Fatalf("provision the SYSTEM organization witness: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seeding store: %v", err)
	}
	_, digest := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
	ir5CompleteControlWithKeyset(t, cfg, pg.Superuser, digest)
	return cfg
}

// ir5ReplaceCustodyAtFinalDecision installs the final-decision hook that replaces the
// custody generation the control authorizes, under the preparation, after its requirement
// was frozen. It returns a reader for the number of times the hook ran, so a case can prove
// it measured something rather than passing because the boundary was never reached.
func ir5ReplaceCustodyAtFinalDecision(ctx context.Context, t *testing.T, superDSN string) func() int {
	t.Helper()
	super := drOpenSuper(t, superDSN)
	_, other := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
	old := publicationFinalDecisionTestHook
	t.Cleanup(func() { publicationFinalDecisionTestHook = old })
	hits := 0
	publicationFinalDecisionTestHook = func(*publicationAdmission) {
		hits++
		if _, err := super.ExecContext(ctx,
			`UPDATE `+drControlRelation+` SET keyset_sha256=pg_catalog.decode($1,'hex')`, other); err != nil {
			t.Errorf("plant the replacement: %v", err)
		}
	}
	return func() int { return hits }
}

// Directory maintenance is the third actual preparation caller and the one the review
// could not reach: its probe stopped at the closed directory-inventory prerequisite in
// both trees, so the custody leg was code-read only.
//
// The prerequisite is met with the EXISTING REAL SEAM rather than a hook: a configured
// AdminDSN makes the reconcile use the admin-DSN inventory authority instead of the
// closed routine (directoryepoch.go), which is the supported split-pool posture. The
// callback then runs and the final successful-completion decision executes.
func TestIR5C2DirectoryMaintenanceFinalizesOnACompletedDestination(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg := ir5CompletedMaintainableDestination(ctx, t, pg)

	before := drCoordinationAcquisitions.Load()
	beforeStatus, afterStatus, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1)
	if err != nil {
		t.Fatalf("directory maintenance was refused on a destination a restore completed: %v", err)
	}
	if got := drCoordinationAcquisitions.Load() - before; got != 1 {
		t.Fatalf("directory maintenance took %d shared acquisitions, not exactly one", got)
	}
	// The callback really ran: it reports the inventory authority it reconciled under,
	// which is exactly the prerequisite the review's probe could not satisfy. An empty
	// authority on both sides would mean the callback returned before doing its work.
	if beforeStatus.InventoryAuthority == "" && afterStatus.InventoryAuthority == "" {
		t.Fatalf("the maintenance callback returned no durable testimony: before=%+v after=%+v changed=%t",
			beforeStatus, afterStatus, changed)
	}
	if afterStatus.InventoryAuthority != "admin_dsn" {
		t.Fatalf("the maintenance ran under %q rather than the configured admin-DSN inventory authority",
			afterStatus.InventoryAuthority)
	}
}

// Every mutating preparation still refuses a PENDING target. Scoping the custody leg must
// not have loosened the gate that all three callers share.
func TestIR5C2EveryPreparationStillRefusesAPendingTarget(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	}); err != nil {
		t.Fatalf("install the pending control: %v", err)
	}
	assertPending := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s proceeded on a PENDING target", what)
		}
		if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), opgate.StatePending) {
			t.Fatalf("%s: wrong refusal: %v", what, err)
		}
	}
	assertPending("migrate apply", ApplyMigrations(ctx, cfg, nil))

	// One config shape for all three, so the control is verified under the roles it was
	// installed with. Maintenance needs no AdminDSN here: a PENDING control refuses at
	// the shared gate, long before the reconcile that would consult an inventory
	// authority, and that is precisely the property this case asserts.
	_, _, _, merr := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1)
	assertPending("directory maintenance", merr)

	st, oerr := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	assertPending("serving open", oerr)
}

// A control REPLACED between the admission that read it and the final decision is a
// refusal: the requirement is compared, never adopted. This leg proves scoping the
// custody comparison did not turn the final decision into a formality for the
// schema-only purpose. The directory-maintenance purpose is covered by the case after it;
// the serving purpose already refuses at its own custody leg.
func TestIR5C2AChangedControlRefusesSchemaOnlyFinalization(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, digest := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
	ir5CompleteControlWithKeyset(t, cfg, pg.Superuser, digest)
	hits := ir5ReplaceCustodyAtFinalDecision(ctx, t, pg.Superuser)

	err := ApplyMigrations(ctx, cfg, nil)
	if hits() == 0 {
		t.Fatal("the final-decision hook did not run, so nothing was measured")
	}
	if err == nil {
		t.Fatal("a schema-only phase finalized against a control that had been replaced under it")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), "was replaced between the admission") {
		t.Fatalf("wrong refusal for a replaced control: %v", err)
	}
	// The limitation is stated rather than hidden: the schema already committed.
	if !strings.Contains(err.Error(), "remains on the destination") {
		t.Fatalf("the refusal does not state the no-rollback limit: %v", err)
	}
}

// The SAME replaced-control refusal at the DIRECTORY-MAINTENANCE final decision.
//
// A maintenance callback runs with serving == false, so the custody comparison is skipped
// for it by design. This is the case that proves what is NOT skipped: the held admission is
// still re-observed, the control is still verified, and reconcileRequirement still refuses a
// control whose completed generation was replaced under the preparation — after the callback
// has already committed its cutover, which the diagnostic states rather than hides.
//
// Without this leg, scoping the custody comparison to the serving publication could have
// reduced the non-serving final decision to a formality and nothing would have measured it.
func TestIR5C2AChangedControlRefusesDirectoryMaintenanceFinalization(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cfg := ir5CompletedMaintainableDestination(ctx, t, pg)
	hits := ir5ReplaceCustodyAtFinalDecision(ctx, t, pg.Superuser)

	_, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1)
	if hits() == 0 {
		t.Fatal("the final-decision hook did not run during directory maintenance, so nothing was measured")
	}
	if err == nil {
		t.Fatal("directory maintenance finalized against a control that had been replaced under it")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), "was replaced between the admission") {
		t.Fatalf("wrong refusal for a replaced control at maintenance finalization: %v", err)
	}
	// The no-rollback limit is stated for this purpose too: the cutover already committed.
	if !strings.Contains(err.Error(), "remains on the destination") {
		t.Fatalf("the refusal does not state the no-rollback limit: %v", err)
	}
}

var _ = store.EnginePostgres
