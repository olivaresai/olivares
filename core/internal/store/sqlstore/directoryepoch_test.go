// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryEpochSQLiteCreateUsesDatabaseClockAndSecondBoot(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "directory-epoch-create.db")
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	cfg := store.Config{
		Engine: store.EngineSQLite,
		DSN:    dsn,
		Clock:  transactionClockFixedAppClock{now: fixed},
	}

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open SQLite directory store: %v", err)
	}
	before := time.Now().UTC().Add(-time.Second)
	tenant := provisionTenant(t, st, "directory-epoch-clock")
	after := time.Now().UTC().Add(time.Second)
	directoryEpochTestWantStatus(t, st, store.DirectoryStatus{
		EpochCoverageComplete: true,
		ControlMode:           store.DirectoryControlStaged,
		WriterPosture:         store.DirectoryWriterSQLiteCapability,
		ExpectedGeneration:    1,
	})
	if err := st.Close(); err != nil {
		t.Fatalf("close first SQLite boot: %v", err)
	}

	raw := directoryEpochTestOpenSQLite(t, dsn)
	defer raw.Close() //nolint:errcheck
	created := directoryEpochTestReadRow(t, raw, tenant)
	if created.id != tenant.String() || created.rowTenant != tenant.String() || created.version != 1 {
		t.Fatalf("seeded epoch = %+v, want id=tenant and version 1", created)
	}
	if created.createdAt == fixed.String() || created.updatedAt == fixed.String() {
		t.Fatalf("seeded epoch used skewed application clock %s", fixed.String())
	}
	createdTime, err := model.ParseTimestamp(created.createdAt)
	if err != nil {
		t.Fatalf("parse seeded created_at: %v", err)
	}
	if createdTime.Time().Before(before) || createdTime.Time().After(after) {
		t.Fatalf("seeded DB time %s outside [%s, %s]", created.createdAt, before, after)
	}
	if created.updatedAt != created.createdAt {
		t.Fatalf("seeded updated_at %s differs from created_at %s", created.updatedAt, created.createdAt)
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)

	reopened, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("second SQLite boot: %v", err)
	}
	directoryEpochTestWantStatus(t, reopened, store.DirectoryStatus{
		EpochCoverageComplete: true,
		ControlMode:           store.DirectoryControlStaged,
		WriterPosture:         store.DirectoryWriterSQLiteCapability,
		ExpectedGeneration:    1,
	})
	if err := reopened.Close(); err != nil {
		t.Fatalf("close second SQLite boot: %v", err)
	}
	if got := directoryEpochTestReadRow(t, raw, tenant); got != created {
		t.Fatalf("second boot rewrote an existing epoch: got %+v want %+v", got, created)
	}
}

func TestDirectoryEpochSQLiteBackfillRollsBackAllAndRetries(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "directory-epoch-backfill.db")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("seed boot: %v", err)
	}
	tenants := []model.TenantID{
		provisionTenant(t, st, "directory-backfill-a"),
		provisionTenant(t, st, "directory-backfill-b"),
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seed boot: %v", err)
	}
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].String() < tenants[j].String() })

	raw := directoryEpochTestOpenSQLite(t, dsn)
	defer raw.Close() //nolint:errcheck
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin epoch removal: %v", err)
	}
	for _, tenant := range tenants {
		directoryEpochTestBindSQLite(t, tx, tenant)
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM main.core_directory_epoch WHERE tenant_id = ?", tenant.String()); err != nil {
			t.Fatalf("remove epoch %s: %v", tenant, err)
		}
	}
	directoryEpochTestBindSQLite(t, tx, model.SystemTenantID)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit epoch removals: %v", err)
	}

	boom := errors.New("stop second epoch insert")
	directoryEpochBeforeInsertTestHook = func(tenant model.TenantID) error {
		if tenant == tenants[1] {
			return boom
		}
		return nil
	}
	t.Cleanup(func() { directoryEpochBeforeInsertTestHook = nil })
	failed, err := Open(ctx, cfg, nil)
	if failed != nil {
		_ = failed.Close()
	}
	if !errors.Is(err, boom) {
		t.Fatalf("failed backfill err = %v, want injected failure", err)
	}
	if got := directoryEpochTestCountRows(t, raw, tenants...); got != 0 {
		t.Fatalf("failed backfill committed %d partial epoch row(s), want 0", got)
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)

	directoryEpochBeforeInsertTestHook = nil
	healed, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("retry backfill: %v", err)
	}
	directoryEpochTestWantStatus(t, healed, store.DirectoryStatus{
		EpochCoverageComplete: true,
		ControlMode:           store.DirectoryControlStaged,
		WriterPosture:         store.DirectoryWriterSQLiteCapability,
		ExpectedGeneration:    1,
	})
	if err := healed.Close(); err != nil {
		t.Fatalf("close healed boot: %v", err)
	}
	if got := directoryEpochTestCountRows(t, raw, tenants...); got != len(tenants) {
		t.Fatalf("healed backfill rows = %d, want %d", got, len(tenants))
	}
	first := directoryEpochTestReadRow(t, raw, tenants[0])

	idempotent, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("idempotent boot after heal: %v", err)
	}
	if err := idempotent.Close(); err != nil {
		t.Fatalf("close idempotent boot: %v", err)
	}
	if got := directoryEpochTestReadRow(t, raw, tenants[0]); got != first {
		t.Fatalf("idempotent backfill rewrote epoch: got %+v want %+v", got, first)
	}
}

func TestDirectoryEpochSQLiteSystemRestoresAndDropIsAtomic(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "directory-epoch-drop.db")
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open SQLite drop store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "directory-drop")

	var identity model.Identity
	var agent model.Agent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "drop identity", Kind: "service", ExternalID: "drop:identity",
		})
		if err != nil {
			return err
		}
		agent, err = sc.Agents().Create(ctx, model.Agent{
			Name: "drop agent", Kind: "test", IdentityID: identity.ID,
			Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed tenant facts: %v", err)
	}

	raw := directoryEpochTestOpenSQLite(t, dsn)
	defer raw.Close() //nolint:errcheck
	var preSystemTenant string
	if err := raw.QueryRowContext(ctx,
		"SELECT tenant_id FROM main."+dialect.ScopeTenantTable).Scan(&preSystemTenant); err != nil {
		t.Fatalf("read pre-System SQLite tenant pin: %v", err)
	}
	if preSystemTenant != tenant.String() {
		t.Fatalf("Mutate precondition pin=%q, want real tenant %s", preSystemTenant, tenant)
	}
	boom := errors.New("rollback system callback")
	err = st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.GetOrg(ctx, tenant); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rebinding callback rollback err = %v, want sentinel", err)
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.GetOrg(ctx, tenant)
		return err
	}); err != nil {
		t.Fatalf("successful rebinding System callback: %v", err)
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)

	err = st.System(ctx, func(sys store.SystemScope) error {
		if err := sys.DropTenant(ctx, tenant); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("DropTenant rollback err = %v, want sentinel", err)
	}
	for table, id := range map[string]string{
		"orgs": tenant.String(), "core_directory_epoch": tenant.String(),
		"identities": identity.ID.String(), "agents": agent.ID.String(),
	} {
		if got := directoryEpochTestCountID(t, raw, table, id); got != 1 {
			t.Fatalf("rollback left %s row count %d, want 1", table, got)
		}
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)

	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, tenant)
	}); err != nil {
		t.Fatalf("commit DropTenant: %v", err)
	}
	for table, id := range map[string]string{
		"orgs": tenant.String(), "core_directory_epoch": tenant.String(),
		"identities": identity.ID.String(), "agents": agent.ID.String(),
	} {
		if got := directoryEpochTestCountID(t, raw, table, id); got != 0 {
			t.Fatalf("committed drop left %s row count %d, want 0", table, got)
		}
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)
	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, tenant)
	}); !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("second DropTenant err = %v, want ErrDirectoryUnavailable", err)
	}
}

func TestDirectoryEpochDropValidatesTenantBeforeWriterLock(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "directory-epoch-invalid-drop.db")
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open SQLite invalid-drop store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw := directoryEpochTestOpenSQLite(t, dsn)
	defer raw.Close() //nolint:errcheck
	if _, err := raw.ExecContext(ctx,
		"DELETE FROM main.directory_writer_control"); err != nil {
		t.Fatalf("poison writer control: %v", err)
	}

	for _, tenant := range []model.TenantID{
		"550e8400-e29b-41d4-a716-446655440000",
		model.TenantID(strings.ToUpper(model.NewTenantID().String())),
	} {
		err := st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, tenant)
		})
		if !errors.Is(err, store.ErrInvalidDescriptor) {
			t.Fatalf("DropTenant(%q) err = %v, want pre-lock ErrInvalidDescriptor", tenant, err)
		}
	}
}

func TestDirectoryEpochNoAdminReadsStatusWithoutStartingReconcileTransaction(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open probe database: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS public"); err != nil {
		t.Fatalf("attach public probe schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE public.directory_writer_control (
control_key TEXT NOT NULL, mode TEXT NOT NULL, expected_generation INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create probe control: %v", err)
	}
	controlInsert := `INSERT INTO public.directory_writer_control
(control_key, mode, expected_generation) VALUES ('core.directory.writer', 'enforced', 7)`
	if _, err := db.ExecContext(ctx, controlInsert); err != nil {
		t.Fatalf("seed probe control: %v", err)
	}
	pgDia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("PostgreSQL dialect unavailable")
	}
	result, err := reconcileDirectoryEpochs(ctx, db, db, pgDia, guardRoleFact{})
	if err != nil {
		t.Fatalf("no-admin status-only reconcile: %v", err)
	}
	if result.coverageComplete || result.control.Mode != directoryWriterEnforced ||
		result.control.ExpectedGeneration != 7 {
		t.Fatalf("no-admin result = %+v, want incomplete enforced/generation 7", result)
	}
	// Any acquire/bind leg would have executed PostgreSQL catalog functions on
	// this SQLite probe and failed. Success therefore distinguishes the early,
	// read-only status path from the transactional reconciler.
}

func TestEpochReconcilersPinOneReadOnlyAdminSnapshot(t *testing.T) {
	directorySource, err := os.ReadFile("directoryepoch.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(directorySource)
	requiredInOrder := []string{
		"adminDB.BeginTx(ctx, &sql.TxOptions{",
		"Isolation: sql.LevelRepeatableRead",
		"ReadOnly:  true",
		"dia.ConnRolePosture(ctx, adminTx)",
		"requirePinnedDirectoryAdminPosture(posture, adminRoleForBoot)",
		"verifyDirectoryActivationDatabaseIdentity(",
		"directoryActivationWitnesses{admin: adminTx}",
		"out.queryer = adminTx",
	}
	position := 0
	for _, fragment := range requiredInOrder {
		next := strings.Index(text[position:], fragment)
		if next < 0 {
			t.Fatalf("directoryepoch.go lacks ordered admin snapshot fragment %q", fragment)
		}
		position += next + len(fragment)
	}
	if strings.Contains(text, "queryer = adminDB") ||
		strings.Contains(text, "enumerateDirectoryTenants(ctx, adminDB") {
		t.Fatal("epoch reconciliation enumerates the AdminDSN pool outside its attested transaction")
	}

	for _, test := range []struct {
		path string
		seq  []string
	}{
		{
			path: "directoryepoch.go",
			seq: []string{
				"inventory, err := openDirectoryReconcileInventory(",
				"enumerateDirectoryTenants(ctx, inventory.queryer, dia)",
				"inventory.commit()",
				"tx.Commit()",
			},
		},
		{
			path: "authorizationepoch.go",
			seq: []string{
				"inventory, err := openDirectoryReconcileInventory(",
				"enumerateDirectoryTenants(ctx, inventory.queryer, dia)",
				"inventory.commit()",
				"tx.Commit()",
			},
		},
	} {
		source, readErr := os.ReadFile(test.path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		body := string(source)
		position = 0
		for _, fragment := range test.seq {
			next := strings.Index(body[position:], fragment)
			if next < 0 {
				t.Fatalf("%s lacks ordered reconcile fragment %q", test.path, fragment)
			}
			position += next + len(fragment)
		}
	}
}

func TestTenantDropAuthorizationFactsUseGlobalLockOrder(t *testing.T) {
	st := openSQLiteTest(t, nil)
	ordered := tenantDropDescriptors(st.(*sqlStore).reg)
	var facts []model.EntityDescriptor
	seenOrdinary := false
	for _, desc := range ordered {
		if desc.AuthorizationFact {
			if seenOrdinary {
				t.Fatalf("authorization fact %s appears after ordinary tenant data", desc.Kind)
			}
			facts = append(facts, desc)
		} else {
			seenOrdinary = true
		}
	}
	if len(facts) != 3 || facts[0].Kind != identityDescriptor.Kind ||
		facts[1].Kind != agentDescriptor.Kind ||
		facts[2].Kind != model.AuthorizationEpochKind ||
		facts[0].AuthorizationLockOrder >= facts[1].AuthorizationLockOrder ||
		facts[1].AuthorizationLockOrder >= facts[2].AuthorizationLockOrder {
		t.Fatalf("DropTenant authorization order = %+v, want Identity(10), Agent(20), AuthorizationEpoch(25)", facts)
	}
}

func TestDirectoryEpochLifecycleRequiresPersistedAuditInDegradeMode(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "directory-epoch-audit-degrade.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "directory-audit-drop")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial audit store: %v", err)
	}

	st := openSQLiteSpoolTest(t, store.Config{
		DSN:                dsn,
		AuditSpoolMaxBytes: 1,
		AuditSpoolOnFull:   store.AuditSpoolDegrade,
	})
	raw := directoryEpochTestOpenSQLite(t, dsn)
	defer raw.Close() //nolint:errcheck

	var discardedDropErr error
	err := st.System(ctx, func(sys store.SystemScope) error {
		discardedDropErr = sys.DropTenant(ctx, tenant)
		return nil
	})
	if !errors.Is(discardedDropErr, store.ErrAuditSpoolFull) ||
		!errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("discarded degraded DropTenant inner=%v outer=%v, want ErrAuditSpoolFull",
			discardedDropErr, err)
	}
	if directoryEpochTestCountID(t, raw, "orgs", tenant.String()) != 1 ||
		directoryEpochTestCountID(t, raw, "core_directory_epoch", tenant.String()) != 1 {
		t.Fatal("degraded DropTenant committed without its required audit event")
	}

	beforeOrgs := directoryEpochTestCountRealTenants(t, raw, "orgs")
	beforeEpochs := directoryEpochTestCountRealTenants(t, raw, "core_directory_epoch")
	var discardedCreateErr error
	err = st.System(ctx, func(sys store.SystemScope) error {
		_, discardedCreateErr = sys.CreateOrg(ctx, model.Org{
			Name: "must rollback", Slug: "directory-audit-create", Status: model.StatusActive,
		})
		return nil
	})
	if !errors.Is(discardedCreateErr, store.ErrAuditSpoolFull) ||
		!errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("discarded degraded CreateOrg inner=%v outer=%v, want ErrAuditSpoolFull",
			discardedCreateErr, err)
	}
	if got := directoryEpochTestCountRealTenants(t, raw, "orgs"); got != beforeOrgs {
		t.Fatalf("degraded CreateOrg changed real org count from %d to %d", beforeOrgs, got)
	}
	if got := directoryEpochTestCountRealTenants(t, raw, "core_directory_epoch"); got != beforeEpochs {
		t.Fatalf("degraded CreateOrg changed epoch count from %d to %d", beforeEpochs, got)
	}
	directoryEpochTestWantSQLiteBaseline(t, raw)
}

func TestDirectoryEpochSQLiteTempShadowsCannotDivertCreateOrDrop(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "directory-epoch-temp-shadow.db")
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open temp-shadow store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	victim := provisionTenant(t, st, "directory-shadow-victim")
	var identity model.Identity
	var agent model.Agent
	if err := st.Mutate(ctx, victim, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "shadow identity", Kind: "service", ExternalID: "shadow:identity",
		})
		if err != nil {
			return err
		}
		agent, err = sc.Agents().Create(ctx, model.Agent{
			Name: "shadow agent", Kind: "test", IdentityID: identity.ID,
			Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed shadow victim: %v", err)
	}

	ss := st.(*sqlStore)
	targetAndConfigColumns := "id TEXT, tenant_id TEXT, target_tenant_id TEXT, config_id TEXT"
	targetAndServiceColumns :=
		"id TEXT, tenant_id TEXT, target_tenant_id TEXT, pep_service_id TEXT"
	shadowTables := map[string]string{
		dialect.ScopeTenantTable:              "tenant_id TEXT",
		dialect.DirectoryWriterControlTable:   "control_key TEXT, mode TEXT, expected_generation INTEGER",
		dialect.DirectoryWriterMarkerTable:    "control_key TEXT, generation INTEGER",
		"core_directory_epoch":                "id TEXT, tenant_id TEXT",
		"orgs":                                "id TEXT, tenant_id TEXT",
		"workspaces":                          "id TEXT, tenant_id TEXT",
		"identities":                          "id TEXT, tenant_id TEXT",
		"agents":                              "id TEXT, tenant_id TEXT",
		userGroupMemberDescriptor.Table:       "id TEXT, tenant_id TEXT, group_id TEXT",
		userGroupDescriptor.Table:             "id TEXT, tenant_id TEXT, target_tenant_id TEXT",
		membershipDescriptor.Table:            "id TEXT, tenant_id TEXT, target_tenant_id TEXT",
		apiTokenDescriptor.Table:              "id TEXT, tenant_id TEXT, bound_tenant_id TEXT",
		userInviteDescriptor.Table:            "id TEXT, tenant_id TEXT, target_tenant_id TEXT",
		federationConfigDescriptor.Table:      "id TEXT, tenant_id TEXT, target_tenant_id TEXT",
		federationDomainClaimDescriptor.Table: targetAndConfigColumns,
		secretEntryDescriptor.Table:           "id TEXT, tenant_id TEXT, scope TEXT",
		sourceDefDescriptor.Table:             "id TEXT, tenant_id TEXT, scope TEXT, tenant TEXT",
		pepServiceDescriptor.Table:            "id TEXT, tenant_id TEXT, target_tenant_id TEXT",
		pepServiceCredentialDescriptor.Table:  "id TEXT, tenant_id TEXT, service_id TEXT, token_id TEXT",
		delegationHandleDescriptor.Table:      targetAndServiceColumns,
		pdpDecisionClaimDescriptor.Table:      targetAndServiceColumns,
	}
	for table, columns := range shadowTables {
		if _, err := ss.db.ExecContext(ctx,
			"CREATE TEMP TABLE "+quoteIdent(table)+" ("+columns+")"); err != nil {
			t.Fatalf("create temp shadow %s: %v", table, err)
		}
	}

	var created model.Org
	if err := st.System(ctx, func(sys store.SystemScope) error {
		var err error
		created, err = sys.CreateOrg(ctx, model.Org{
			Name: "shadow create", Slug: "directory-shadow-create", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("CreateOrg with temp shadows: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, "orgs", created.ID.String()) != 1 ||
		directoryEpochTestCountID(t, ss.db, "core_directory_epoch", created.ID.String()) != 1 {
		t.Fatal("CreateOrg was diverted away from main org/epoch tables")
	}
	var workspaces int
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.workspaces WHERE tenant_id = ?", created.TenantID.String()).
		Scan(&workspaces); err != nil || workspaces != 1 {
		t.Fatalf("main default workspace count=%d err=%v, want 1", workspaces, err)
	}

	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, victim)
	}); err != nil {
		t.Fatalf("DropTenant with temp shadows: %v", err)
	}
	for table, id := range map[string]string{
		"orgs": victim.String(), "core_directory_epoch": victim.String(),
		"identities": identity.ID.String(), "agents": agent.ID.String(),
	} {
		if got := directoryEpochTestCountID(t, ss.db, table, id); got != 0 {
			t.Fatalf("DropTenant left main.%s row count %d", table, got)
		}
	}
	for table := range shadowTables {
		var count int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM temp."+quoteIdent(table)).Scan(&count); err != nil {
			t.Fatalf("count temp shadow %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("temp shadow %s received %d diverted row(s)", table, count)
		}
	}

	if _, err := ss.db.ExecContext(ctx, `CREATE TEMP TABLE audit_events (
id TEXT, tenant_id TEXT, seq INTEGER, occurred_at TEXT, actor TEXT, actor_kind TEXT,
action TEXT, target_kind TEXT, target_id TEXT, meta TEXT, meta_blind BLOB,
payload_hash BLOB, prev_hash BLOB, hash BLOB, sig BLOB)`); err != nil {
		t.Fatalf("create temp audit_events shadow: %v", err)
	}
	var eventShadowOrg model.Org
	err = st.System(ctx, func(sys store.SystemScope) error {
		var err error
		eventShadowOrg, err = sys.CreateOrg(ctx, model.Org{
			Name: "audit shadow create", Slug: "directory-audit-shadow-create",
			Status: model.StatusActive,
		})
		return err
	})
	if err != nil {
		t.Fatalf("CreateOrg with temp audit_events: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, "orgs", eventShadowOrg.ID.String()) != 1 ||
		directoryEpochTestCountID(t, ss.db, "core_directory_epoch", eventShadowOrg.ID.String()) != 1 {
		t.Fatal("audit-events shadow diverted CreateOrg away from main")
	}
	err = st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, eventShadowOrg.TenantID)
	})
	if err != nil {
		t.Fatalf("DropTenant with temp audit_events: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, "orgs", eventShadowOrg.ID.String()) != 0 ||
		directoryEpochTestCountID(t, ss.db, "core_directory_epoch", eventShadowOrg.ID.String()) != 0 {
		t.Fatal("audit-events shadow diverted DropTenant away from main")
	}
	var divertedAuditRows int
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM temp.audit_events").Scan(&divertedAuditRows); err != nil {
		t.Fatalf("count temp audit rows: %v", err)
	}
	if divertedAuditRows != 0 {
		t.Fatalf("temp audit shadow contains %d diverted rows", divertedAuditRows)
	}
	if _, err := ss.db.ExecContext(ctx, "DROP TABLE temp.audit_events"); err != nil {
		t.Fatalf("drop temp audit_events shadow: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx, `CREATE TEMP TABLE audit_heads (
tenant_id TEXT PRIMARY KEY, seq INTEGER, hash BLOB)`); err != nil {
		t.Fatalf("create temp audit_heads shadow: %v", err)
	}
	var headShadowOrg model.Org
	err = st.System(ctx, func(sys store.SystemScope) error {
		var err error
		headShadowOrg, err = sys.CreateOrg(ctx, model.Org{
			Name: "audit head shadow create", Slug: "directory-audit-head-shadow-create",
			Status: model.StatusActive,
		})
		return err
	})
	if err != nil {
		t.Fatalf("CreateOrg with temp audit_heads: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, "orgs", headShadowOrg.ID.String()) != 1 ||
		directoryEpochTestCountID(t, ss.db, "core_directory_epoch", headShadowOrg.ID.String()) != 1 {
		t.Fatal("audit-heads shadow diverted CreateOrg away from main")
	}
	err = st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, headShadowOrg.TenantID)
	})
	if err != nil {
		t.Fatalf("DropTenant with temp audit_heads: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, "orgs", headShadowOrg.ID.String()) != 0 ||
		directoryEpochTestCountID(t, ss.db, "core_directory_epoch", headShadowOrg.ID.String()) != 0 {
		t.Fatal("audit-heads shadow diverted DropTenant away from main")
	}
	var divertedHeadRows int
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM temp.audit_heads").Scan(&divertedHeadRows); err != nil {
		t.Fatalf("count temp audit heads: %v", err)
	}
	if divertedHeadRows != 0 {
		t.Fatalf("temp audit head shadow contains %d diverted rows", divertedHeadRows)
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryEpochSQLiteSystemErrorRestoresBeforePoolWaiter(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenant := provisionTenant(t, st, "directory-system-waiter")
	if err := st.Mutate(ctx, tenant, func(store.Scope) error { return nil }); err != nil {
		t.Fatalf("publish real-tenant baseline: %v", err)
	}

	type waiterResult struct {
		tenant string
		err    error
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan waiterResult, 1)
	waitCountBefore := ss.db.Stats().WaitCount
	boom := errors.New("system callback failure before compensation")
	err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.GetOrg(ctx, tenant); err != nil {
			return err
		}
		go func() {
			close(started)
			conn, err := ss.db.Conn(waitCtx)
			if err != nil {
				done <- waiterResult{err: err}
				return
			}
			defer conn.Close() //nolint:errcheck
			var got string
			err = conn.QueryRowContext(waitCtx,
				"SELECT tenant_id FROM main."+dialect.ScopeTenantTable).Scan(&got)
			done <- waiterResult{tenant: got, err: err}
		}()
		<-started
		deadline := time.NewTimer(500 * time.Millisecond)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for ss.db.Stats().WaitCount <= waitCountBefore {
			select {
			case result := <-done:
				t.Fatalf("pool waiter acquired System's pinned connection early: %+v", result)
			case <-tick.C:
			case <-deadline.C:
				t.Fatalf("pool waiter never entered the connection queue: stats=%+v", ss.db.Stats())
			}
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("failed System callback err=%v, want sentinel", err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.tenant != model.SystemTenantID.String() {
			t.Fatalf("pool waiter observed tenant=%q err=%v after compensation, want SYSTEM",
				result.tenant, result.err)
		}
	case <-waitCtx.Done():
		t.Fatalf("pool waiter did not resume after bounded compensation: %v", waitCtx.Err())
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryEpochSQLiteCreateRequiresExactRows(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	systemID := model.SystemTenantID.String()
	baselineOrgs := directoryEpochTestCountRealTenants(t, ss.db, orgDescriptor.Table)
	baselineEpochs := directoryEpochTestCountRealTenants(t, ss.db, directoryEpochDescriptor.Table)
	baselineWorkspaces := directoryEpochTestCountRealTenants(t, ss.db, workspaceDescriptor.Table)

	for _, tc := range []struct {
		name    string
		table   string
		trigger string
		want    string
	}{
		{name: "organization", table: orgDescriptor.Table, trigger: "ignore_directory_org_insert",
			want: "insert organization affected 0 rows"},
		{name: "default workspace", table: workspaceDescriptor.Table,
			trigger: "ignore_directory_workspace_insert",
			want:    "insert default workspace affected 0 rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statement := fmt.Sprintf(`CREATE TRIGGER main.%s BEFORE INSERT ON %s
WHEN NEW.tenant_id <> '%s' BEGIN SELECT RAISE(IGNORE); END`,
				tc.trigger, quoteIdent(tc.table), systemID)
			if _, err := ss.db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create ignore trigger: %v", err)
			}
			var discarded error
			err := st.System(ctx, func(sys store.SystemScope) error {
				_, discarded = sys.CreateOrg(ctx, model.Org{
					Name: "ignored create", Slug: "ignored-" + tc.trigger,
					Status: model.StatusActive,
				})
				return nil
			})
			if discarded == nil || !strings.Contains(discarded.Error(), tc.want) ||
				err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ignored %s insert inner=%v outer=%v, want %q",
					tc.table, discarded, err, tc.want)
			}
			if _, err := ss.db.ExecContext(ctx, "DROP TRIGGER main."+quoteIdent(tc.trigger)); err != nil {
				t.Fatalf("drop ignore trigger: %v", err)
			}
			gotOrgs := directoryEpochTestCountRealTenants(t, ss.db, orgDescriptor.Table)
			if gotOrgs != baselineOrgs {
				t.Fatalf("ignored insert changed org count to %d, want %d", gotOrgs, baselineOrgs)
			}
			gotEpochs := directoryEpochTestCountRealTenants(t, ss.db, directoryEpochDescriptor.Table)
			if gotEpochs != baselineEpochs {
				t.Fatalf("ignored insert changed epoch count to %d, want %d", gotEpochs, baselineEpochs)
			}
			gotWorkspaces := directoryEpochTestCountRealTenants(t, ss.db, workspaceDescriptor.Table)
			if gotWorkspaces != baselineWorkspaces {
				t.Fatalf("ignored insert changed workspace count to %d, want %d",
					gotWorkspaces, baselineWorkspaces)
			}
			directoryEpochTestWantSQLiteBaseline(t, ss.db)
		})
	}
}

func TestDirectoryEpochSQLiteDropPostconditionsPoisonDiscardedErrors(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenant := provisionTenant(t, st, "directory-drop-postcondition")

	workspaceTrigger := fmt.Sprintf(`CREATE TRIGGER main.ignore_directory_workspace_delete
BEFORE DELETE ON workspaces WHEN OLD.tenant_id = '%s' BEGIN SELECT RAISE(IGNORE); END`,
		tenant.String())
	if _, err := ss.db.ExecContext(ctx, workspaceTrigger); err != nil {
		t.Fatalf("create workspace delete ignore trigger: %v", err)
	}
	var discarded error
	err := st.System(ctx, func(sys store.SystemScope) error {
		discarded = sys.DropTenant(ctx, tenant)
		return nil
	})
	if !errors.Is(discarded, store.ErrDirectoryUnavailable) ||
		!errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("discarded ordinary-delete failure inner=%v outer=%v", discarded, err)
	}
	if _, err := ss.db.ExecContext(ctx,
		"DROP TRIGGER main.ignore_directory_workspace_delete"); err != nil {
		t.Fatalf("drop workspace ignore trigger: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, orgDescriptor.Table, tenant.String()) != 1 ||
		directoryEpochTestCountID(t, ss.db, directoryEpochDescriptor.Table, tenant.String()) != 1 {
		t.Fatal("discarded ordinary delete failure committed org/epoch retirement")
	}

	var token model.APIToken
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		var err error
		token, err = a.Tokens().Create(ctx, model.APIToken{
			Name: "drop-postcondition", Selector: "drop-postcondition",
			SecretHash: []byte("hash"), BoundTenantID: tenant, Role: "viewer",
		})
		return err
	}); err != nil {
		t.Fatalf("seed target-bound token: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER main.ignore_directory_token_delete
BEFORE DELETE ON api_tokens WHEN OLD.id = '%s' BEGIN SELECT RAISE(IGNORE); END`,
		token.ID.String())); err != nil {
		t.Fatalf("create token delete ignore trigger: %v", err)
	}
	discarded = nil
	err = st.System(ctx, func(sys store.SystemScope) error {
		discarded = sys.DropTenant(ctx, tenant)
		return nil
	})
	if !errors.Is(discarded, store.ErrDirectoryUnavailable) ||
		!errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("discarded credential-delete failure inner=%v outer=%v", discarded, err)
	}
	if err := st.AuthView(ctx, func(a store.AuthScope) error {
		_, err := a.Tokens().Get(ctx, token.ID)
		return err
	}); err != nil {
		t.Fatalf("credential delete refusal did not roll token back: %v", err)
	}
	if directoryEpochTestCountID(t, ss.db, orgDescriptor.Table, tenant.String()) != 1 ||
		directoryEpochTestCountID(t, ss.db, directoryEpochDescriptor.Table, tenant.String()) != 1 {
		t.Fatal("credential delete refusal committed org/epoch retirement")
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryEpochSQLiteDropPurgesClosedAuthEstate(t *testing.T) {
	st := openSQLiteTest(t, nil)
	directoryEpochTestExerciseAuthEstateDrop(t, st)
}

func TestDirectoryEpochSQLiteLifecyclePathsShareGlobalWriterReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dsn := filepath.Join(t.TempDir(), "directory-lifecycle-lock.db")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open SQLite lifecycle-lock store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	missing := provisionTenant(t, st, "directory-sqlite-lock-backfill")
	raw := directoryEpochTestOpenSQLite(t, dsn)
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin SQLite epoch removal: %v", err)
	}
	directoryEpochTestBindSQLite(t, tx, missing)
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM main.core_directory_epoch WHERE tenant_id = ?", missing.String()); err != nil {
		t.Fatalf("remove SQLite epoch: %v", err)
	}
	directoryEpochTestBindSQLite(t, tx, model.SystemTenantID)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit SQLite epoch removal: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw SQLite lifecycle pool: %v", err)
	}

	backfillPaused := make(chan struct{})
	releaseBackfill := make(chan struct{})
	var once sync.Once
	directoryEpochBeforeInsertTestHook = func(tenant model.TenantID) error {
		if tenant != missing {
			return nil
		}
		once.Do(func() { close(backfillPaused) })
		select {
		case <-releaseBackfill:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { directoryEpochBeforeInsertTestHook = nil })
	type openResult struct {
		st  store.Store
		err error
	}
	backfillDone := make(chan openResult, 1)
	go func() {
		reopened, err := Open(ctx, cfg, nil)
		backfillDone <- openResult{st: reopened, err: err}
	}()
	select {
	case <-backfillPaused:
	case result := <-backfillDone:
		if result.st != nil {
			_ = result.st.Close()
		}
		t.Fatalf("SQLite backfill ended before pause: %v", result.err)
	case <-ctx.Done():
		t.Fatalf("SQLite backfill did not reach pause: %v", ctx.Err())
	}
	createDuringBackfill := make(chan error, 1)
	go func() {
		createDuringBackfill <- st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "SQLite after backfill", Slug: "sqlite-after-backfill",
				Status: model.StatusActive,
			})
			return err
		})
	}()
	select {
	case err := <-createDuringBackfill:
		t.Fatalf("SQLite CreateOrg crossed held backfill reservation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseBackfill)
	result := <-backfillDone
	if result.err != nil || result.st == nil {
		t.Fatalf("release SQLite backfill: store=%v err=%v", result.st, result.err)
	}
	reopened := result.st
	t.Cleanup(func() { _ = reopened.Close() })
	if err := <-createDuringBackfill; err != nil {
		t.Fatalf("SQLite CreateOrg after backfill commit: %v", err)
	}
	directoryEpochBeforeInsertTestHook = nil

	victim := provisionTenant(t, st, "directory-sqlite-lock-drop")
	if err := st.Mutate(ctx, victim, func(sc store.Scope) error {
		_, err := sc.Identities().Create(ctx, model.Identity{
			Name: "SQLite lifecycle identity", Kind: "service", ExternalID: "sqlite:lock",
		})
		return err
	}); err != nil {
		t.Fatalf("seed SQLite lifecycle-lock victim: %v", err)
	}
	dropPaused := make(chan struct{})
	releaseDrop := make(chan struct{})
	var dropOnce sync.Once
	tenantDropAfterAuthorizationFactTestHook = func(kind model.Kind) error {
		if kind != identityDescriptor.Kind {
			return nil
		}
		dropOnce.Do(func() { close(dropPaused) })
		select {
		case <-releaseDrop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { tenantDropAfterAuthorizationFactTestHook = nil })
	dropDone := make(chan error, 1)
	go func() {
		dropDone <- st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, victim)
		})
	}()
	select {
	case <-dropPaused:
	case err := <-dropDone:
		t.Fatalf("SQLite DropTenant ended before pause: %v", err)
	case <-ctx.Done():
		t.Fatalf("SQLite DropTenant did not reach pause: %v", ctx.Err())
	}
	createDuringDrop := make(chan error, 1)
	go func() {
		createDuringDrop <- reopened.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "SQLite after drop", Slug: "sqlite-after-drop",
				Status: model.StatusActive,
			})
			return err
		})
	}()
	select {
	case err := <-createDuringDrop:
		t.Fatalf("SQLite CreateOrg crossed held DropTenant reservation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseDrop)
	if err := <-dropDone; err != nil {
		t.Fatalf("release SQLite DropTenant: %v", err)
	}
	if err := <-createDuringDrop; err != nil {
		t.Fatalf("SQLite CreateOrg after DropTenant commit: %v", err)
	}
	tenantDropAfterAuthorizationFactTestHook = nil
}

type directoryEpochTestAuthEstate struct {
	membership  model.Membership
	group       model.UserGroup
	groupMember model.UserGroupMember
	token       model.APIToken
	invite      model.UserInvite
	config      model.FederationConfig
	domain      model.FederationDomainClaim
	secret      model.SecretEntry
	sources     []model.SourceDef
	service     model.PEPService
	credential  model.PEPServiceCredential
	handle      model.DelegationHandle
	claim       model.PDPDecisionClaim
}

type directoryEpochTestRetainedAuth struct {
	user       model.User
	session    model.AuthSession
	credential model.WebAuthnCredential
	seen       model.SETSeenJTI
}

func directoryEpochTestExerciseAuthEstateDrop(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	victim := provisionTenant(t, st, "directory-auth-estate-victim")
	other := provisionTenant(t, st, "directory-auth-estate-other")
	retained, err := directoryEpochTestSeedRetainedAuth(ctx, st, "directory-estate")
	if err != nil {
		t.Fatalf("seed retained auth estate: %v", err)
	}
	victimEstate, err := directoryEpochTestSeedAuthEstate(
		ctx, st, victim, "victim", retained.user.ID,
	)
	if err != nil {
		t.Fatalf("seed victim auth estate: %v", err)
	}
	otherEstate, err := directoryEpochTestSeedAuthEstate(
		ctx, st, other, "other", retained.user.ID,
	)
	if err != nil {
		t.Fatalf("seed other-tenant auth estate: %v", err)
	}
	globalEstate, err := directoryEpochTestSeedAuthEstate(
		ctx, st, model.SystemTenantID, "global", retained.user.ID,
	)
	if err != nil {
		t.Fatalf("seed global auth estate: %v", err)
	}

	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, victim)
	}); err != nil {
		t.Fatalf("DropTenant with complete auth estate: %v", err)
	}
	directoryEpochTestWantAuthEstate(t, st, victimEstate, false)
	directoryEpochTestWantAuthEstate(t, st, otherEstate, true)
	directoryEpochTestWantAuthEstate(t, st, globalEstate, true)
	directoryEpochTestWantRetainedAuth(t, st, retained)
}

func directoryEpochTestSeedRetainedAuth(
	ctx context.Context,
	st store.Store,
	tag string,
) (directoryEpochTestRetainedAuth, error) {
	var out directoryEpochTestRetainedAuth
	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		var err error
		out.user, err = a.Users().Create(ctx, model.User{
			Email: tag + "@example.test", DisplayName: tag, Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		out.session, err = a.Sessions().Create(ctx, model.AuthSession{
			UserID: out.user.ID, Selector: tag + "-session", SecretHash: []byte("session-hash"),
			ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		out.credential, err = a.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
			UserID: out.user.ID, Name: tag, CredentialID: tag + "-credential",
			Credential: []byte(`{"publicKey":"test"}`),
		})
		if err != nil {
			return err
		}
		out.seen, err = a.SeenJTIs().Create(ctx, model.SETSeenJTI{
			JTI: tag + "-jti", PublisherID: tag + "-publisher",
			ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		return err
	})
	return out, err
}

func directoryEpochTestSeedAuthEstate(
	ctx context.Context,
	st store.Store,
	target model.TenantID,
	tag string,
	userID model.ID,
) (directoryEpochTestAuthEstate, error) {
	var out directoryEpochTestAuthEstate
	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		memberships := a.Memberships()
		groups := a.Groups()
		groupMembers := a.GroupMembers()
		if target.IsSystem() {
			// Historical fixture only: production writers now reject SYSTEM as a
			// directory target because it has no business-tenant epoch. DropTenant's
			// preservation test still needs pre-cutover global rows to prove a
			// business-tenant drop cannot delete them, so seed those three guarded
			// source rows through the package-private raw repos while control is staged.
			raw := a.(*authScope).ts
			memberships = newTypedRepo(raw.repo(membershipDescriptor), membershipCodec)
			groups = newTypedRepo(raw.repo(userGroupDescriptor), userGroupCodec)
			groupMembers = newTypedRepo(
				raw.repo(userGroupMemberDescriptor), userGroupMemberCodec,
			)
		}
		var err error
		out.membership, err = memberships.Create(ctx, model.Membership{
			UserID: userID, TargetTenantID: target, Role: "viewer",
		})
		if err != nil {
			return err
		}
		out.group, err = groups.Create(ctx, model.UserGroup{
			TargetTenantID: target, DisplayName: tag + " group", ExternalID: tag + "-group",
		})
		if err != nil {
			return err
		}
		out.groupMember, err = groupMembers.Create(ctx, model.UserGroupMember{
			GroupID: out.group.ID, UserID: userID,
		})
		if err != nil {
			return err
		}
		out.token, err = a.Tokens().Create(ctx, model.APIToken{
			Name: tag + " token", UserID: userID, Selector: tag + "-token",
			SecretHash: []byte("token-hash"), BoundTenantID: target, Role: "viewer",
		})
		if err != nil {
			return err
		}
		out.invite, err = a.Invites().Create(ctx, model.UserInvite{
			Email: tag + "-invite@example.test", TargetTenantID: target, Role: "viewer",
			Selector: tag + "-invite", SecretHash: []byte("invite-hash"),
			ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		out.config, err = a.FederationConfigs().Create(ctx, model.FederationConfig{
			TargetTenantID: target, Alias: tag, Protocol: "oidc", Status: model.StatusActive,
			OIDCIssuer: "https://" + tag + ".example.test", OIDCClientID: tag,
		})
		if err != nil {
			return err
		}
		out.domain, err = a.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{
			TargetTenantID: target, ConfigID: out.config.ID, Domain: tag + ".example.test",
		})
		if err != nil {
			return err
		}
		out.secret, err = a.Secrets().Create(ctx, model.SecretEntry{
			Scope: target, Name: tag + "-secret", ValueSealed: "sealed-" + tag,
		})
		if err != nil {
			return err
		}
		for _, source := range []model.SourceDef{
			{Scope: target, Name: tag + "-scope-source", Kind: "test", Tenant: target.String()},
			{
				Scope: model.SystemTenantID, Name: tag + "-tenant-source",
				Kind: "test", Tenant: target.String(),
			},
		} {
			created, err := a.Sources().Create(ctx, source)
			if err != nil {
				return err
			}
			out.sources = append(out.sources, created)
		}
		out.service, err = a.PEPServices().Create(ctx, model.PEPService{
			TargetTenantID: target, Name: tag + "-pep", PDPAudience: "urn:test:" + tag,
			Capabilities: map[string]bool{"streaming": true}, CapabilityVersion: 1,
		})
		if err != nil {
			return err
		}
		out.credential, err = a.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
			ServiceID: out.service.ID, TokenID: out.token.ID,
		})
		if err != nil {
			return err
		}
		out.handle, err = a.DelegationHandles().Create(ctx, model.DelegationHandle{
			TargetTenantID: target, Selector: tag + "-handle", SecretHash: []byte("handle-hash"),
			SourceCredKind: "token", SourceCredID: out.token.ID, SubjectUserID: userID,
			MintRole: "viewer", PEPServiceID: out.service.ID, Audience: out.service.PDPAudience,
			Operations: []string{"messages"},
			ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		out.claim, _, err = a.ClaimDecision(ctx, model.PDPDecisionClaim{
			TargetTenantID: target, HandleJTI: out.handle.ID, PEPServiceID: out.service.ID,
			NonceHash: tag + "-nonce", RequestFingerprint: tag + "-fingerprint",
			RequestIssuedAt: model.NewTimestamp(time.Now().UTC()), CapabilityVersion: 1,
			EffectiveCapabilities: map[string]bool{"streaming": true},
		}, nil)
		return err
	})
	return out, err
}

func directoryEpochTestWantAuthEstate(
	t *testing.T,
	st store.Store,
	estate directoryEpochTestAuthEstate,
	wantPresent bool,
) {
	t.Helper()
	err := st.AuthView(context.Background(), func(a store.AuthScope) error {
		checks := []struct {
			name string
			get  func() error
		}{
			{"membership", func() error {
				_, err := a.Memberships().Get(context.Background(), estate.membership.ID)
				return err
			}},
			{"group", func() error {
				_, err := a.Groups().Get(context.Background(), estate.group.ID)
				return err
			}},
			{"group member", func() error {
				_, err := a.GroupMembers().Get(context.Background(), estate.groupMember.ID)
				return err
			}},
			{"API token", func() error {
				_, err := a.Tokens().Get(context.Background(), estate.token.ID)
				return err
			}},
			{"invite", func() error {
				_, err := a.Invites().Get(context.Background(), estate.invite.ID)
				return err
			}},
			{"federation config", func() error {
				_, err := a.FederationConfigs().Get(context.Background(), estate.config.ID)
				return err
			}},
			{"federation domain claim", func() error {
				_, err := a.FederationDomainClaims().Get(context.Background(), estate.domain.ID)
				return err
			}},
			{"secret", func() error {
				_, err := a.Secrets().Get(context.Background(), estate.secret.ID)
				return err
			}},
			{"PEP service", func() error {
				_, err := a.PEPServices().Get(context.Background(), estate.service.ID)
				return err
			}},
			{"PEP credential", func() error {
				_, err := a.PEPServiceCredentials().Get(context.Background(), estate.credential.ID)
				return err
			}},
			{"delegation handle", func() error {
				_, err := a.DelegationHandles().Get(context.Background(), estate.handle.ID)
				return err
			}},
			{"PDP claim", func() error {
				_, err := a.PDPDecisionClaims().Get(context.Background(), estate.claim.ID)
				return err
			}},
		}
		for i := range estate.sources {
			source := estate.sources[i]
			checks = append(checks, struct {
				name string
				get  func() error
			}{"source", func() error {
				_, err := a.Sources().Get(context.Background(), source.ID)
				return err
			}})
		}
		for _, check := range checks {
			err := check.get()
			if wantPresent && err != nil {
				return fmt.Errorf("%s should survive: %w", check.name, err)
			}
			if !wantPresent && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%s after drop err=%v, want ErrNotFound", check.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func directoryEpochTestWantRetainedAuth(
	t *testing.T,
	st store.Store,
	retained directoryEpochTestRetainedAuth,
) {
	t.Helper()
	err := st.AuthView(context.Background(), func(a store.AuthScope) error {
		for _, check := range []struct {
			name string
			get  func() error
		}{
			{"user", func() error {
				_, err := a.Users().Get(context.Background(), retained.user.ID)
				return err
			}},
			{"auth session", func() error {
				_, err := a.Sessions().Get(context.Background(), retained.session.ID)
				return err
			}},
			{"WebAuthn credential", func() error {
				_, err := a.WebAuthnCredentials().Get(context.Background(), retained.credential.ID)
				return err
			}},
			{"SET seen JTI", func() error {
				_, err := a.SeenJTIs().Get(context.Background(), retained.seen.ID)
				return err
			}},
		} {
			if err := check.get(); err != nil {
				return fmt.Errorf("retained %s after DropTenant: %w", check.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type directoryEpochTestRow struct {
	id, rowTenant, createdAt, updatedAt string
	version                             int64
}

func directoryEpochTestOpenSQLite(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := openSQLite(dsn)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	return db
}

func directoryEpochTestReadRow(
	t *testing.T,
	db *sql.DB,
	tenant model.TenantID,
) directoryEpochTestRow {
	t.Helper()
	var row directoryEpochTestRow
	if err := db.QueryRowContext(context.Background(), `SELECT
id, tenant_id, created_at, updated_at, version
FROM main.core_directory_epoch WHERE tenant_id = ?`, tenant.String()).Scan(
		&row.id, &row.rowTenant, &row.createdAt, &row.updatedAt, &row.version,
	); err != nil {
		t.Fatalf("read directory epoch %s: %v", tenant, err)
	}
	return row
}

func directoryEpochTestCountRows(t *testing.T, db *sql.DB, tenants ...model.TenantID) int {
	t.Helper()
	var count int
	for _, tenant := range tenants {
		count += directoryEpochTestCountID(t, db, "core_directory_epoch", tenant.String())
	}
	return count
}

func directoryEpochTestCountID(t *testing.T, db *sql.DB, table, id string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main."+table+" WHERE id = ?", id).Scan(&count); err != nil {
		t.Fatalf("count %s id %s: %v", table, id, err)
	}
	return count
}

func directoryEpochTestCountRealTenants(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main."+table+" WHERE tenant_id <> ?",
		model.SystemTenantID.String()).Scan(&count); err != nil {
		t.Fatalf("count real tenants in %s: %v", table, err)
	}
	return count
}

func directoryEpochTestBindSQLite(t *testing.T, tx *sql.Tx, tenant model.TenantID) {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(),
		"DELETE FROM main."+dialect.ScopeTenantTable); err != nil {
		t.Fatalf("clear SQLite tenant pin: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		"INSERT INTO main."+dialect.ScopeTenantTable+"(tenant_id) VALUES (?)",
		tenant.String()); err != nil {
		t.Fatalf("bind SQLite tenant %s: %v", tenant, err)
	}
}

func directoryEpochTestWantSQLiteBaseline(t *testing.T, db *sql.DB) {
	t.Helper()
	var tenant string
	if err := db.QueryRowContext(context.Background(),
		"SELECT tenant_id FROM main."+dialect.ScopeTenantTable).Scan(&tenant); err != nil {
		t.Fatalf("read SQLite tenant baseline: %v", err)
	}
	if tenant != model.SystemTenantID.String() {
		t.Fatalf("SQLite tenant baseline = %q, want SYSTEM", tenant)
	}
	var markerRows int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main."+dialect.DirectoryWriterMarkerTable).Scan(&markerRows); err != nil {
		t.Fatalf("read SQLite marker baseline: %v", err)
	}
	if markerRows != 0 {
		t.Fatalf("SQLite marker baseline contains %d rows, want 0", markerRows)
	}
}

func directoryEpochTestWantStatus(t *testing.T, st store.Store, want store.DirectoryStatus) {
	t.Helper()
	statuser, ok := st.(store.DirectoryStatuser)
	if !ok {
		t.Fatal("SQL store does not implement DirectoryStatuser")
	}
	got, supported, err := statuser.DirectoryStatus(context.Background())
	if err != nil || !supported {
		t.Fatalf("DirectoryStatus = %+v supported=%t err=%v", got, supported, err)
	}
	if got != want {
		t.Fatalf("DirectoryStatus = %+v, want %+v", got, want)
	}
}
