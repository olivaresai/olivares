// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// hcr1Needs indexes the inventory by relation so an assertion can name one.
func hcr1Needs(t *testing.T, cfg upgradePreflightConfig, blindingWillActuate bool) map[string][]string {
	t.Helper()
	reg := coreOnlyRegistry(t)
	out := map[string][]string{}
	for _, need := range upgradePreflightNeeds(reg, cfg, blindingWillActuate) {
		out[need.table] = need.privileges
	}
	return out
}

// TestUpgradePreflightInventoryIsClosedAndOwnerOnlyRelationsAreAbsent pins the two
// halves of the inventory that a future change is most likely to get wrong in opposite
// directions: a relation the application role really does consume being dropped, and an
// OWNER-ONLY relation acquiring a requirement it must never have.
//
// The second half is the one worth a test of its own. Every relation named here is
// created by the owner under the migration lock, and three of them are relations the
// hardened split posture deliberately DENIES the application role INSERT on. A
// preflight that demanded a positive privilege on those would refuse exactly the
// deployments that are most correctly provisioned — the hardened ones — and it would do
// it at boot, before any durable change, so the estate would simply stop upgrading.
func TestUpgradePreflightInventoryIsClosedAndOwnerOnlyRelationsAreAbsent(t *testing.T) {
	needs := hcr1Needs(t, upgradePreflightConfig{}, false)

	// The positive half, including core v10's H. H is mutable and pinned to the SYSTEM
	// tenant; "tenantless" describes its authority, not a missing column, and it is a
	// member of mutableTenantTables like any other mutable descriptor.
	for _, want := range []struct {
		table      string
		privileges string
	}{
		{userAuthorityDescriptor.Table, "SELECT,INSERT,UPDATE,DELETE"},
		{"orgs", "SELECT,INSERT,UPDATE,DELETE"},
		{auditHeadsTable, "SELECT,INSERT,UPDATE,DELETE"},
		{auditTable, "SELECT,INSERT"},
		{dialect.ControlRolloutStateTable, "SELECT,UPDATE"},
		{dialect.ControlRolloutTransitionTable, "SELECT,INSERT"},
		{dialect.DirectoryWriterControlTable, "SELECT"},
		{lineageControlTable, "SELECT"},
		{lineageSeededTable, "SELECT"},
		{leaderEpochTable, "SELECT,INSERT,UPDATE"},
		// SELECT *and* DELETE, and neither is conditional — see
		// TestUpgradePreflightGapRecoveryIsAnUnconditionalCapability.
		{dialect.AuditSpoolGapsTable, "SELECT,DELETE"},
		{dialect.AuditBlindingStateTable, "SELECT"},
	} {
		got, ok := needs[want.table]
		if !ok {
			t.Errorf("relation %q generates no application-role requirement at all", want.table)
			continue
		}
		if joined := strings.Join(got, ","); joined != want.privileges {
			t.Errorf("relation %q requires %s, want %s", want.table, joined, want.privileges)
		}
	}

	// The negative half. Every one of these is owner-only.
	for _, table := range []string{
		coreTrackingTable,
		moduleTablesTracking,
		dialect.ControlRolloutClassificationTable,
		dialect.ControlAppendOnlyScopeTable,
		dialect.GuardGateEventsTable,
		dialect.GuardInventoryEventsTable,
		dialect.GuardReceiptsTable,
		lineageWriterTable,
		lineageTouchedTable,
	} {
		if got, ok := needs[table]; ok {
			t.Errorf("owner-only relation %q acquired an application-role requirement %v — the hardened split posture denies the app role writes there, so demanding one would refuse the best-provisioned deployments",
				table, got)
		}
	}
}

// TestUpgradePreflightConditionalNeedsFollowConfiguration is the "no privilege for an
// inactive option" rule, stated as a test because it is the rule most easily lost.
//
// An audit spool budget that is off, a degrade policy that is not selected and a
// blinding rule that will not actuate must each cost the deployment NOTHING. Requiring
// their privileges anyway would refuse a correctly-provisioned estate for a capability
// it deliberately never enabled — and it would refuse it at boot, before any durable
// change, so the operator's only clue would be a demand for a privilege nothing uses.
func TestUpgradePreflightConditionalNeedsFollowConfiguration(t *testing.T) {
	off := hcr1Needs(t, upgradePreflightConfig{}, false)
	if _, ok := off[dialect.AuditSpoolUsageTable]; ok {
		t.Errorf("the spool counter is required with no budget configured: %v", off[dialect.AuditSpoolUsageTable])
	}
	if got := strings.Join(off[dialect.AuditSpoolGapsTable], ","); got != "SELECT,DELETE" {
		// Both are unconditional, for two different reasons. auditLog.Append READS the
		// tenant's pending episode on every single append, whatever the budget says; and
		// if one is pending, any unsigned append SEALS it and CLEARS the row — outside
		// the budget block, so with no budget at all that is the direct path.
		t.Errorf("the degrade episode table requires %q with no budget, want SELECT,DELETE", got)
	}
	if got := strings.Join(off[dialect.AuditBlindingStateTable], ","); got != "SELECT" {
		t.Errorf("the blinding record requires %q when no actuation will happen, want SELECT only", got)
	}

	budgeted := hcr1Needs(t, upgradePreflightConfig{spoolBudgeted: true}, false)
	if got := strings.Join(budgeted[dialect.AuditSpoolUsageTable], ","); got != "SELECT,UPDATE" {
		t.Errorf("the spool counter requires %q under a budget, want SELECT,UPDATE", got)
	}
	if got := strings.Join(budgeted[dialect.AuditSpoolGapsTable], ","); got != "SELECT,DELETE" {
		t.Errorf("a budget alone made the degrade episode table require %q — CREATING episode state (INSERT/UPDATE) needs the degrade POLICY too, not merely a budget", got)
	}

	degrading := hcr1Needs(t, upgradePreflightConfig{spoolBudgeted: true, spoolDegrade: true}, false)
	if got := strings.Join(degrading[dialect.AuditSpoolGapsTable], ","); got != "SELECT,INSERT,UPDATE,DELETE" {
		t.Errorf("the degrade episode table requires %q under budget+degrade, want SELECT,INSERT,UPDATE,DELETE", got)
	}

	actuating := hcr1Needs(t, upgradePreflightConfig{}, true)
	if got := strings.Join(actuating[dialect.AuditBlindingStateTable], ","); got != "SELECT,UPDATE" {
		t.Errorf("the blinding record requires %q when this boot will actuate, want SELECT,UPDATE", got)
	}
}

// TestUpgradePreflightGapRecoveryIsAnUnconditionalCapability pins the corrected
// obligation for audit_spool_gaps, which the first cut of this preflight got wrong.
//
// The mistake was folding DELETE into the configuration that CREATES episodes. Those are
// two different configurations. recordDrop needs a budget and the degrade policy; but the
// row it writes SURVIVES the process, and the next boot — same estate, same operator,
// budget switched off — is the one that has to seal and clear it. auditLog.Append reads
// the pending episode before it looks at any budget, and seals it outside the budget
// block, so under AuditSpoolMaxBytes == 0 that is the direct path. Requiring only SELECT
// there admits a role that opens the service and then fails 42501 on the first append
// that meets inherited state.
//
// The obligation is therefore stated as a capability rather than measured: deciding
// whether a row exists is a GLOBAL read of a FORCE-RLS table, which the NOBYPASSRLS owner
// cannot answer and which this preflight will not acquire an AdminDSN or weaken a guard
// to ask. The runtime reachability is measured instead, on a real engine, by
// TestAuditGapPersistedEpisodeIsSealedWithTheBudgetOff.
func TestUpgradePreflightGapRecoveryIsAnUnconditionalCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  upgradePreflightConfig
		want string
	}{
		{"no budget, default policy", upgradePreflightConfig{}, "SELECT,DELETE"},
		{"budget, block policy", upgradePreflightConfig{spoolBudgeted: true}, "SELECT,DELETE"},
		{"degrade policy but no budget", upgradePreflightConfig{spoolDegrade: true}, "SELECT,DELETE"},
		{"budget and degrade", upgradePreflightConfig{spoolBudgeted: true, spoolDegrade: true}, "SELECT,INSERT,UPDATE,DELETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(hcr1Needs(t, tc.cfg, false)[dialect.AuditSpoolGapsTable], ",")
			if got != tc.want {
				t.Fatalf("audit_spool_gaps requires %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUpgradePreflightGapObligationCannotDependOnExistence is the STABILITY half, and it
// is a structural assertion rather than a scenario.
//
// The naive fix — demand SELECT+DELETE on the relation once it exists, but only SELECT
// while it is still pending — produces a contract that invalidates itself: the first boot
// accepts a posture, the schema phase creates the table, and the very next boot with the
// same binary, configuration, grants and data refuses. Nothing about the deployment
// changed except that the relation the rule describes came into existence.
//
// In this design the obligation is computed once per relation and existence only decides
// WHICH reading consumes it — the OID measurement or the future union. The only thing
// that can drop a relation from the future union is provisionedByReconciler, which marks
// a relation whose own reconciler grants the access. audit_spool_gaps has no such
// reconciler, so asserting that flag is false is asserting that the two readings cannot
// diverge. The PostgreSQL end of the same property is
// TestUpgradePreflightGapObligationSurvivesTheSchemaThatCreatesIt.
func TestUpgradePreflightGapObligationCannotDependOnExistence(t *testing.T) {
	reg := coreOnlyRegistry(t)
	for _, cfg := range []upgradePreflightConfig{
		{},
		{spoolBudgeted: true},
		{spoolBudgeted: true, spoolDegrade: true},
	} {
		var found bool
		for _, need := range upgradePreflightNeeds(reg, cfg, false) {
			if need.table != dialect.AuditSpoolGapsTable {
				continue
			}
			found = true
			if need.provisionedByReconciler {
				t.Fatalf("audit_spool_gaps is marked provisioned by a reconciler, so the future union would silently drop its obligation while the existing-relation reading keeps it: %+v", cfg)
			}
			if !slices.Contains(need.privileges, "DELETE") {
				t.Fatalf("audit_spool_gaps lost DELETE under %+v, so a persisted episode could not be cleared", cfg)
			}
		}
		if !found {
			t.Fatalf("audit_spool_gaps generates no requirement under %+v", cfg)
		}
	}
}

// TestUpgradePreflightConfigResolvesTheEffectiveSpoolMode pins the writer's own rule:
// anything that is not an explicit degrade is block. An empty on_full is the common
// case and must not be read as degrade, which would demand INSERT/UPDATE/DELETE on the
// episode table from every default deployment.
func TestUpgradePreflightConfigResolvesTheEffectiveSpoolMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cfg         store.Config
		wantBudget  bool
		wantDegrade bool
	}{
		{"empty mode is block", store.Config{AuditSpoolMaxBytes: 1}, true, false},
		{"explicit block", store.Config{AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolBlock}, true, false},
		{"explicit degrade", store.Config{AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade}, true, true},
		{"degrade without a budget still reports the policy", store.Config{AuditSpoolOnFull: store.AuditSpoolDegrade}, false, true},
		{"no budget at all", store.Config{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := upgradePreflightConfigOf(tc.cfg)
			if got.spoolBudgeted != tc.wantBudget || got.spoolDegrade != tc.wantDegrade {
				t.Fatalf("budgeted=%t degrade=%t, want budgeted=%t degrade=%t",
					got.spoolBudgeted, got.spoolDegrade, tc.wantBudget, tc.wantDegrade)
			}
		})
	}
	// A budget with degrade is the only combination that demands episode WRITES, and
	// the resolution above is what the inventory consumes. Stated together so a change
	// to either half cannot pass alone.
	needs := hcr1Needs(t, upgradePreflightConfigOf(store.Config{AuditSpoolOnFull: store.AuditSpoolDegrade}), false)
	if got := strings.Join(needs[dialect.AuditSpoolGapsTable], ","); got != "SELECT,DELETE" {
		t.Fatalf("a degrade policy with NO budget demanded %q on the episode table: recordDrop is unreachable without a budget, so only the unconditional recovery pair is owed", got)
	}
}

// TestApplyMigrationsRefusesSQLiteBeforeTouchingTheFilesystem covers the engine guard
// AND the thing that makes it worth having: the refusal has to happen before the driver
// is asked to open anything, because opening a SQLite DSN CREATES the file. A guard
// that ran after the open would leave an empty database behind on every refusal, and
// the operator's next `migrate status` would report a store that this command had
// silently created.
func TestApplyMigrationsRefusesSQLiteBeforeTouchingTheFilesystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	err := ApplyMigrations(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: path,
	}, registerWidget)
	if err == nil {
		t.Fatal("ApplyMigrations ACCEPTED sqlite: the migrate/grant/serve phase separates a PostgreSQL owner/app split that sqlite does not have, so its success would mean something different")
	}
	if !strings.Contains(err.Error(), "requires postgres") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("the refused call left a database at %q behind (stat: %v)", path, statErr)
	}
}

// TestApplyMigrationsClearsAdminDSNOnItsOwnCopy proves the clearing cannot reach the
// caller's Config. store.Config is taken by value, so this is a property of the
// signature rather than of the body — which is exactly why it is worth pinning: a
// future refactor to a pointer receiver would silently start mutating the caller's
// configuration, and the engine boot shares one Config across a process.
func TestApplyMigrationsClearsAdminDSNOnItsOwnCopy(t *testing.T) {
	cfg := store.Config{
		Engine:   store.EngineSQLite, // refused early; the DSN is never dialed
		AdminDSN: "postgres://admin@127.0.0.1:1/none",
	}
	_ = ApplyMigrations(context.Background(), cfg, nil)
	if cfg.AdminDSN == "" {
		t.Fatal("ApplyMigrations blanked the CALLER's AdminDSN: the clearing must apply to its own copy only")
	}
}

// TestOpenPreparedRefusesASchemaOnlyMaintenanceCallback pins the one combination the
// two purposes must never quietly accept. The schema-only purpose returns as soon as
// the migration lock is released, which is BEFORE the maintenance callback is
// reachable — so accepting both would hand the caller a call that silently never ran
// its callback and reported success.
func TestOpenPreparedRefusesASchemaOnlyMaintenanceCallback(t *testing.T) {
	_, err := openPrepared(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "unused.db"),
	}, nil, prepareSchemaOnly, func(*sqlStore) error { return nil }, publicationInputs{})
	if err == nil {
		t.Fatal("openPrepared ACCEPTED a schema-only purpose with a maintenance callback that could never run")
	}
	if !strings.Contains(err.Error(), "maintenance callback") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
}

// TestUpgradePreflightIsInertWithoutASplitOwner covers the two postures in which the
// question this preflight asks does not exist: a non-PostgreSQL engine, and a
// PostgreSQL deployment whose application role IS the owner.
//
// Inertness matters as much as the refusal. Single-role is the default topology, the
// app role owns every table the plan creates, and there is no separate subject to ask
// about — so a preflight that produced any verdict there would be inventing one.
func TestUpgradePreflightIsInertWithoutASplitOwner(t *testing.T) {
	ctx := context.Background()
	reg := coreOnlyRegistry(t)
	pg, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	sqliteDia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	// A nil Execer proves inertness by construction: any of these reaching the database
	// would panic rather than pass.
	for _, tc := range []struct {
		name  string
		dia   dialect.Dialect
		roles guardRoles
	}{
		{"sqlite", sqliteDia, guardRoles{
			App: guardRoleFact{Role: "a", Known: true}, Owner: guardRoleFact{Role: "o", Known: true}, OwnerConfigured: true}},
		{"no owner configured", pg, guardRoles{App: guardRoleFact{Role: "a", Known: true}}},
		{"two DSNs, one role", pg, guardRoles{
			App: guardRoleFact{Role: "same", Known: true}, Owner: guardRoleFact{Role: "same", Known: true}, OwnerConfigured: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := preflightPostgresUpgradePrivileges(ctx, nil, tc.dia, tc.roles, reg, nil,
				prepareThroughReadiness, upgradePreflightConfig{}); err != nil {
				t.Fatalf("the preflight produced a verdict where it has no question to ask: %v", err)
			}
		})
	}
}

// TestUpgradePreflightRefusesAnUnresolvedRole is the deny-closed half of the topology
// switch, and it is deliberately NOT inert.
//
// An OwnerDSN is configured and one of the two roles could not be read. The whole
// question is "what will THAT role be able to do", so an unread role leaves it
// unanswerable — and an unanswerable privilege question is a refusal, never a pass.
// Treating it as single-role would silently skip the check on a deployment that asked
// for the split.
func TestUpgradePreflightRefusesAnUnresolvedRole(t *testing.T) {
	pg, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	for _, tc := range []struct {
		name  string
		roles guardRoles
	}{
		{"owner unread", guardRoles{
			App: guardRoleFact{Role: "app", Known: true}, OwnerConfigured: true}},
		{"app unread", guardRoles{
			Owner: guardRoleFact{Role: "owner", Known: true}, OwnerConfigured: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := preflightPostgresUpgradePrivileges(context.Background(), nil, pg, tc.roles,
				coreOnlyRegistry(t), nil, prepareThroughReadiness, upgradePreflightConfig{})
			if err == nil {
				t.Fatal("an unresolved role was treated as a PASS")
			}
			if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
				t.Fatalf("refusal does not carry the preflight sentinel: %v", err)
			}
		})
	}
}
