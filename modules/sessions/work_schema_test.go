// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type workSchemaCaptureRegistry struct {
	descriptors []model.EntityDescriptor
}

func (r *workSchemaCaptureRegistry) Register(d model.EntityDescriptor) error {
	r.descriptors = append(r.descriptors, d)
	return nil
}
func (*workSchemaCaptureRegistry) Migrations(string, fs.FS) error { return nil }
func (*workSchemaCaptureRegistry) SchemaInvariants(
	string,
	map[store.Engine][]store.SchemaTrigger,
) error {
	return nil
}
func (*workSchemaCaptureRegistry) WorkspaceInitializer(store.WorkspaceInitializer) error { return nil }
func (*workSchemaCaptureRegistry) RolloutControl(store.RolloutControl) error             { return nil }

func TestWorkMutableNoDeleteDescriptorsDeclareTenantRetention(t *testing.T) {
	t.Parallel()

	reg := &workSchemaCaptureRegistry{}
	if err := New().registerWorkSchema(reg); err != nil {
		t.Fatalf("register work schema: %v", err)
	}
	want := map[model.Kind]bool{
		workItemKind:         true,
		workDependencyKind:   true,
		workAcceptanceKind:   true,
		workDecisionHeadKind: true,
		workLeaseKind:        true,
		workOutboxKind:       true,
		workGuardKind:        true,
	}
	got := 0
	for _, descriptor := range reg.descriptors {
		if !want[descriptor.Kind] {
			continue
		}
		got++
		if !descriptor.RetainOnTenantDrop {
			t.Errorf("mutable work descriptor %s lost tenant retention", descriptor.Kind)
		}
		if descriptor.AppendOnly {
			t.Errorf("%s is append-only; this set must contain the mutable no-delete projections", descriptor.Kind)
		}
		delete(want, descriptor.Kind)
	}
	if got != 7 || len(want) != 0 {
		t.Fatalf("retained mutable descriptors = %d, missing = %v; want exactly 7", got, want)
	}
}

func TestWorkSchemaInvariantDeclarationComplete(t *testing.T) {
	t.Parallel()

	validated := []store.SchemaTrigger{
		{Name: "sessions_work_item_state_guard", Table: workItemTable},
		{Name: "sessions_work_dependency_guard", Table: workDependencyTable},
		{Name: "sessions_work_acceptance_guard", Table: workAcceptanceTable},
		{Name: "sessions_work_decision_guard", Table: workDecisionTable},
		{Name: "sessions_work_decision_head_guard", Table: workDecisionHeadTable},
		{Name: "sessions_work_command_guard", Table: workCommandTable},
		{Name: "sessions_work_event_guard", Table: workEventTable},
		{Name: "sessions_work_outbox_guard", Table: workOutboxTable},
		{Name: "sessions_work_guard_guard", Table: workGuardTable},
	}
	noDelete := []store.SchemaTrigger{
		{Name: "sessions_work_item_no_delete", Table: workItemTable},
		{Name: "sessions_work_dependency_no_delete", Table: workDependencyTable},
		{Name: "sessions_work_acceptance_no_delete", Table: workAcceptanceTable},
		{Name: "sessions_work_decision_head_no_delete", Table: workDecisionHeadTable},
		{Name: "sessions_work_outbox_no_delete", Table: workOutboxTable},
		{Name: "sessions_work_guard_no_delete", Table: workGuardTable},
	}
	got := workSchemaInvariants()
	const postgresK2 = 3
	const sqliteK2 = 4
	if len(got[store.EnginePostgres]) != len(validated)+len(noDelete)+postgresK2 {
		t.Fatalf("postgres invariants = %d, want %d", len(got[store.EnginePostgres]), len(validated)+len(noDelete)+postgresK2)
	}
	if len(got[store.EngineSQLite]) != 2*len(validated)+len(noDelete)+sqliteK2 {
		t.Fatalf("sqlite invariants = %d, want %d", len(got[store.EngineSQLite]), 2*len(validated)+len(noDelete)+sqliteK2)
	}
	for i, inv := range validated {
		pg := got[store.EnginePostgres][i]
		if pg.Name != inv.Name || pg.Table != inv.Table {
			t.Errorf("postgres invariant %d = %+v, want %s on %s", i, pg, inv.Name, inv.Table)
		}
		workSchemaRequireDigest(t, "postgres "+pg.Name, pg.DefinitionSHA256)
		if ins := got[store.EngineSQLite][2*i]; ins.Name != inv.Name+"_ins" || ins.Table != inv.Table {
			t.Errorf("sqlite insert invariant %d = %+v, want %s on %s", i, ins, inv.Name+"_ins", inv.Table)
		} else {
			workSchemaRequireDigest(t, "sqlite "+ins.Name, ins.DefinitionSHA256)
		}
		if upd := got[store.EngineSQLite][2*i+1]; upd.Name != inv.Name+"_upd" || upd.Table != inv.Table {
			t.Errorf("sqlite update invariant %d = %+v, want %s on %s", i, upd, inv.Name+"_upd", inv.Table)
		} else {
			workSchemaRequireDigest(t, "sqlite "+upd.Name, upd.DefinitionSHA256)
		}
	}
	for i, inv := range noDelete {
		pg := got[store.EnginePostgres][len(validated)+i]
		if pg.Name != inv.Name || pg.Table != inv.Table {
			t.Errorf("postgres no-delete invariant %d = %+v, want %s on %s", i, pg, inv.Name, inv.Table)
		}
		workSchemaRequireDigest(t, "postgres "+pg.Name, pg.DefinitionSHA256)
		sq := got[store.EngineSQLite][2*len(validated)+i]
		if sq.Name != inv.Name || sq.Table != inv.Table {
			t.Errorf("sqlite no-delete invariant %d = %+v, want %s on %s", i, sq, inv.Name, inv.Table)
		}
		workSchemaRequireDigest(t, "sqlite "+sq.Name, sq.DefinitionSHA256)
	}
	pgK2 := []store.SchemaTrigger{
		{Name: "sessions_work_lease_guard", Table: workLeaseTable},
		{Name: "sessions_work_guard_clock_monotonic", Table: workGuardTable},
		{Name: "sessions_work_lease_no_delete", Table: workLeaseTable},
	}
	sqliteK2Invariants := []store.SchemaTrigger{
		{Name: "sessions_work_lease_guard_ins", Table: workLeaseTable},
		{Name: "sessions_work_lease_guard_upd", Table: workLeaseTable},
		{Name: "sessions_work_guard_clock_monotonic", Table: workGuardTable},
		{Name: "sessions_work_lease_no_delete", Table: workLeaseTable},
	}
	for i, want := range pgK2 {
		inv := got[store.EnginePostgres][len(validated)+len(noDelete)+i]
		if inv.Name != want.Name || inv.Table != want.Table {
			t.Errorf("postgres K2 invariant %d = %+v, want %s on %s", i, inv, want.Name, want.Table)
		}
		workSchemaRequireDigest(t, "postgres "+inv.Name, inv.DefinitionSHA256)
	}
	for i, want := range sqliteK2Invariants {
		inv := got[store.EngineSQLite][2*len(validated)+len(noDelete)+i]
		if inv.Name != want.Name || inv.Table != want.Table {
			t.Errorf("sqlite K2 invariant %d = %+v, want %s on %s", i, inv, want.Name, want.Table)
		}
		workSchemaRequireDigest(t, "sqlite "+inv.Name, inv.DefinitionSHA256)
	}
}

func TestWorkNoDeleteInvariantCatalogDigests(t *testing.T) {
	t.Parallel()

	declared := workSchemaInvariants()
	declaration := func(engine store.Engine, name string) string {
		for _, invariant := range declared[engine] {
			if invariant.Name == name {
				return invariant.DefinitionSHA256
			}
		}
		return ""
	}
	assertDigest := func(t *testing.T, engine store.Engine, name, definition string) {
		t.Helper()
		sum := sha256.Sum256([]byte(definition))
		got := hex.EncodeToString(sum[:])
		t.Logf("%s %s %s", engine, name, got)
		if want := declaration(engine, name); got != want {
			t.Errorf("%s %s catalog digest = %s, declared %s", engine, name, got, want)
		}
	}
	names := []string{
		"sessions_work_item_no_delete", "sessions_work_dependency_no_delete",
		"sessions_work_acceptance_no_delete", "sessions_work_decision_head_no_delete",
		"sessions_work_outbox_no_delete", "sessions_work_guard_no_delete",
	}

	t.Run("sqlite", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "no-delete-digests.db")
		st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}, New().RegisterSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close() //nolint:errcheck
		for _, name := range names {
			var definition string
			if err := raw.QueryRow("SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?", name).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			assertDigest(t, store.EngineSQLite, name, definition)
		}
	})

	if !enginetest.PostgresAvailable(t) {
		t.Logf("%s unset: PostgreSQL no-delete digest catalog NOT exercised", enginetest.EnvSuperuserDSN)
		return
	}
	t.Run("postgres", func(t *testing.T) {
		pg := enginetest.IsolatedPostgres(t)
		st, err := engine.Open(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, Debug: true}, New().RegisterSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close() //nolint:errcheck
		for _, name := range names {
			var triggerDef, functionDef string
			if err := raw.QueryRow(`
SELECT pg_catalog.pg_get_triggerdef(t.oid, false), pg_catalog.pg_get_functiondef(t.tgfoid)
FROM pg_catalog.pg_trigger t
WHERE t.tgname = $1 AND NOT t.tgisinternal`, name).Scan(&triggerDef, &functionDef); err != nil {
				t.Fatal(err)
			}
			definition := fmt.Sprintf("trigger:%d:%sfunction:%d:%s", len(triggerDef), triggerDef, len(functionDef), functionDef)
			assertDigest(t, store.EnginePostgres, name, definition)
		}
	})
}

func TestWorkK2InvariantCatalogDigests(t *testing.T) {
	t.Parallel()

	names := []string{
		"sessions_work_lease_guard",
		"sessions_work_guard_clock_monotonic",
		"sessions_work_lease_no_delete",
	}
	declared := workSchemaInvariants()
	declaration := func(engine store.Engine, name string) string {
		for _, invariant := range declared[engine] {
			if invariant.Name == name {
				return invariant.DefinitionSHA256
			}
		}
		return ""
	}
	assert := func(engine store.Engine, name, definition string) {
		sum := sha256.Sum256([]byte(definition))
		got := hex.EncodeToString(sum[:])
		t.Logf("%s %s %s", engine, name, got)
		if want := declaration(engine, name); want != "" && got != want {
			t.Errorf("%s %s catalog digest = %s, declared %s", engine, name, got, want)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "k2-invariant-digests.db")
		st, err := engine.Open(context.Background(), store.Config{
			Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		}, New().RegisterSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close() //nolint:errcheck
		for _, name := range append([]string{
			"sessions_work_lease_guard_ins", "sessions_work_lease_guard_upd",
		}, names[1:]...) {
			var definition string
			if err := raw.QueryRow(
				"SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?", name,
			).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			assert(store.EngineSQLite, name, definition)
		}
	})

	if !enginetest.PostgresAvailable(t) {
		t.Logf("%s unset: PostgreSQL K2 digest catalog NOT exercised", enginetest.EnvSuperuserDSN)
		return
	}
	t.Run("postgres", func(t *testing.T) {
		pg := enginetest.IsolatedPostgres(t)
		st, err := engine.Open(context.Background(), store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, Debug: true,
		}, New().RegisterSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close() //nolint:errcheck
		for _, name := range names {
			var triggerDef, functionDef string
			if err := raw.QueryRow(`
SELECT pg_catalog.pg_get_triggerdef(t.oid, false), pg_catalog.pg_get_functiondef(t.tgfoid)
FROM pg_catalog.pg_trigger t
WHERE t.tgname = $1 AND NOT t.tgisinternal`, name).Scan(&triggerDef, &functionDef); err != nil {
				t.Fatal(err)
			}
			definition := fmt.Sprintf(
				"trigger:%d:%sfunction:%d:%s",
				len(triggerDef), triggerDef, len(functionDef), functionDef,
			)
			assert(store.EnginePostgres, name, definition)
		}
	})
}

func TestWorkMutableNoDeleteRowsSurviveDropTenantAcrossBackends(t *testing.T) {
	t.Parallel()

	type backend struct {
		name   string
		engine store.Engine
		dsn    string
	}
	backends := []backend{{
		name: "sqlite", engine: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "work-retention.db"),
	}}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgres(t)
		backends = append(backends, backend{
			name: "postgres", engine: store.EnginePostgres, dsn: pg.App,
		})
	} else {
		t.Logf("%s unset: PostgreSQL tenant retention NOT exercised", enginetest.EnvSuperuserDSN)
	}

	retainedKinds := []model.Kind{
		workItemKind,
		workDependencyKind,
		workAcceptanceKind,
		workDecisionHeadKind,
		workLeaseKind,
		workOutboxKind,
		workGuardKind,
	}
	for _, be := range backends {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			m := New()
			st, err := engine.Open(ctx, store.Config{
				Engine: be.engine, DSN: be.dsn, Debug: true,
			}, m.RegisterSchema)
			if err != nil {
				t.Fatalf("open %s: %v", be.name, err)
			}
			defer st.Close() //nolint:errcheck
			m.UseData(api.NewModuleData(st))

			var tenant model.TenantID
			if err := st.System(ctx, func(sys store.SystemScope) error {
				if _, err := sys.EnsureSystemTenant(ctx); err != nil {
					return err
				}
				org, err := sys.CreateOrg(ctx, model.Org{
					Name: "Retained work", Slug: "retained-work", Status: model.StatusActive,
				})
				tenant = org.TenantID
				return err
			}); err != nil {
				t.Fatalf("create tenant: %v", err)
			}
			workspace, _ := workSchemaWorkspaces(t, ctx, m, tenant)

			item := workSchemaMustCreate(
				t, ctx, m, tenant, workItemKind, workSchemaItem(workspace, "retained item"),
			)
			dependencyTarget := workSchemaMustCreate(
				t, ctx, m, tenant, workItemKind, workSchemaItem(workspace, "dependency target"),
			)
			itemID := item.String(model.ColID)
			workSchemaMustCreate(t, ctx, m, tenant, workDependencyKind,
				workSchemaDependency(workspace, itemID, dependencyTarget.String(model.ColID)))
			workSchemaMustCreate(t, ctx, m, tenant, workAcceptanceKind,
				workSchemaAcceptance(workspace, itemID, "retained"))
			decisionHash := workSchemaHash("retained-decision")
			decision := workSchemaMustCreate(t, ctx, m, tenant, workDecisionKind,
				workSchemaDecision(workspace, itemID, "retained", decisionHash))
			workSchemaMustCreate(t, ctx, m, tenant, workDecisionHeadKind,
				workSchemaDecisionHead(
					workspace, itemID, "retained", decision.String(model.ColID), decisionHash,
				))
			workSchemaMustCreate(t, ctx, m, tenant, workLeaseKind, model.Record{
				colWorkWorkspaceID: workspace.String(), colWorkItemID: itemID,
				colLeaseFence: int64(0), colLeaseState: workLeaseVacant,
				colLeaseRenewalCount: int64(0),
			})
			command := workSchemaMustCreate(t, ctx, m, tenant, workCommandKind,
				workSchemaCommand(workspace, itemID, "retained"))
			event := workSchemaMustCreate(t, ctx, m, tenant, workEventKind,
				workSchemaEvent(workspace, itemID, command.String(colCommandID), 1, "retained"))
			workSchemaMustCreate(t, ctx, m, tenant, workOutboxKind,
				workSchemaOutbox(workspace, event.String(colEventID)))
			workSchemaMustCreate(t, ctx, m, tenant, workGuardKind,
				workSchemaGuard(workspace, "dependency_graph"))
			// Seed one ordinary mutable entity owned by the same module. This keeps
			// the test capable of killing an implementation that accidentally skips
			// every sessions table instead of consulting each descriptor's lifecycle.
			workSchemaMustCreate(t, ctx, m, tenant, timelineKind, model.Record{
				colTLSessionRef: "drop-control", colTLAt: model.NewTimestamp(time.Now()).String(),
				colTLKind: "control",
			})

			before := make(map[model.Kind]int, len(retainedKinds))
			for _, kind := range retainedKinds {
				before[kind] = workSchemaRowCount(t, ctx, m, tenant, kind)
				if before[kind] == 0 {
					t.Fatalf("seeded %s rows = 0", kind)
				}
			}

			if err := st.System(ctx, func(sys store.SystemScope) error {
				return sys.DropTenant(ctx, tenant)
			}); err != nil {
				t.Fatalf("DropTenant: %v", err)
			}
			if err := st.System(ctx, func(sys store.SystemScope) error {
				_, err := sys.GetOrg(ctx, tenant)
				return err
			}); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("org after DropTenant = %v, want ErrNotFound", err)
			}

			for _, kind := range retainedKinds {
				if got := workSchemaRowCount(t, ctx, m, tenant, kind); got != before[kind] {
					t.Errorf("%s rows after DropTenant = %d, want retained %d", kind, got, before[kind])
				}
				// DropTenant must not achieve retention by disabling the guard.
				// A normal hard-delete still reaches the live no-delete trigger.
				row := workSchemaFirstRow(t, ctx, m, tenant, kind)
				err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					return repo.Delete(ctx, model.ID(row.String(model.ColID)))
				})
				wantDeleteError := "sessions work rows cannot be hard-deleted"
				if be.engine == store.EngineSQLite && kind == workLeaseKind {
					wantDeleteError = "sessions work leases cannot be deleted"
				}
				if err == nil || !strings.Contains(err.Error(), wantDeleteError) {
					t.Errorf("%s delete error after DropTenant = %v, want %q", kind, err, wantDeleteError)
				}
			}
			if got := workSchemaRowCount(t, ctx, m, tenant, timelineKind); got != 0 {
				t.Errorf("ordinary sessions.timeline rows after DropTenant = %d, want 0", got)
			}

			if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				workspaces, _, err := sc.Workspaces().List(ctx, model.Query{})
				if err == nil && len(workspaces) != 0 {
					t.Errorf("ordinary workspace rows after DropTenant = %d, want 0", len(workspaces))
				}
				return err
			}); err != nil {
				t.Fatalf("inspect ordinary rows after DropTenant: %v", err)
			}
		})
	}
}

func TestWorkSchemaMutableRowsRejectHardDeleteAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			f := workFixtureForBackend(t, m, tenant)
			left := applyCreate(t, f, "no hard delete left")
			right := applyCreate(t, f, "no hard delete right")
			dependency, err := m.Apply(context.Background(), tenant, f.principal, WorkCommand{
				Command: "dependency.add", WorkItemID: left.ResultID, DependsOnID: right.ResultID,
				ExpectedVersion: left.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: "POST",
			})
			if err != nil {
				t.Fatalf("create dependency: %v", err)
			}
			decision := decisionCommand(left.ResultID, "decision.set", "retention")
			decision.ExpectedVersion = dependency.Version
			if _, err := m.Apply(context.Background(), tenant, f.principal, decision); err != nil {
				t.Fatalf("create decision head: %v", err)
			}

			for _, kind := range []model.Kind{
				workItemKind, workDependencyKind, workAcceptanceKind,
				workDecisionHeadKind, workOutboxKind, workGuardKind,
			} {
				kind := kind
				t.Run(string(kind), func(t *testing.T) {
					var id model.ID
					if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
						repo, err := sc.Ext(kind)
						if err != nil {
							return err
						}
						rows, _, err := repo.List(context.Background(), model.Query{Limit: 1})
						if err != nil {
							return err
						}
						if len(rows) != 1 {
							return fmt.Errorf("rows = %d, want at least one", len(rows))
						}
						id = recordID(rows[0])
						return nil
					}); err != nil {
						t.Fatalf("select delete witness: %v", err)
					}

					err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
						repo, err := sc.Ext(kind)
						if err != nil {
							return err
						}
						return repo.Delete(context.Background(), id)
					})
					if err == nil {
						t.Fatal("hard delete succeeded")
					}

					if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
						repo, err := sc.Ext(kind)
						if err != nil {
							return err
						}
						_, err = repo.Get(context.Background(), id)
						return err
					}); err != nil {
						t.Fatalf("row did not survive rejected delete: %v", err)
					}
				})
			}
		})
	}
}

func TestWorkDecisionHeadOptimisticCASAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		seed := func(t *testing.T, key string) (
			*Module, model.TenantID, model.ID, string, model.Record,
		) {
			t.Helper()
			m, tenant, _ := be.open(t)
			workspace, _ := workSchemaWorkspaces(t, context.Background(), m, tenant)
			item := workSchemaMustCreate(
				t, context.Background(), m, tenant, workItemKind,
				workSchemaItem(workspace, "decision head "+key),
			)
			hash := workSchemaHash("decision-head-" + key)
			decision := workSchemaMustCreate(
				t, context.Background(), m, tenant, workDecisionKind,
				workSchemaDecision(workspace, item.String(model.ColID), key, hash),
			)
			head := workSchemaMustCreate(
				t, context.Background(), m, tenant, workDecisionHeadKind,
				workSchemaDecisionHead(
					workspace, item.String(model.ColID), key, decision.String(model.ColID), hash,
				),
			)
			return m, tenant, workspace, decision.String(model.ColID), head
		}
		advance := func(
			t *testing.T,
			m *Module,
			tenant model.TenantID,
			workspace model.ID,
			head model.Record,
			sequence int64,
			supersedes string,
		) (model.Record, model.Record) {
			t.Helper()
			key := head.String(colDecisionKey)
			hash := workSchemaHash(fmt.Sprintf("decision-head-%s-%d", key, sequence))
			decisionInput := workSchemaDecision(
				workspace, head.String(colWorkItemID), key, hash,
			)
			decisionInput[colDecisionSeq] = sequence
			decisionInput[colDecisionOperation] = "supersede"
			decisionInput[colDecisionSupersedesID] = supersedes
			decision := workSchemaMustCreate(
				t, context.Background(), m, tenant, workDecisionKind, decisionInput,
			)
			next := workSchemaClone(head)
			next[colDecisionCurrentID] = decision.String(model.ColID)
			next[colDecisionCurrentSeq] = sequence
			next[colDecisionHeadHash] = hash
			return decision, next
		}

		t.Run(be.name+" stale snapshot is rejected", func(t *testing.T) {
			m, tenant, workspace, firstDecisionID, head := seed(t, "stale")
			secondDecision, secondHead := advance(
				t, m, tenant, workspace, head, 2, firstDecisionID,
			)
			if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(workDecisionHeadKind)
				if err != nil {
					return err
				}
				_, err = repo.Update(context.Background(), secondHead)
				return err
			}); err != nil {
				t.Fatalf("first head CAS: %v", err)
			}
			_, staleThirdHead := advance(
				t, m, tenant, workspace, head, 3, secondDecision.String(model.ColID),
			)
			err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, openErr := sc.Ext(workDecisionHeadKind)
				if openErr != nil {
					return openErr
				}
				_, updateErr := repo.Update(context.Background(), staleThirdHead)
				return updateErr
			})
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("stale decision head CAS = %v, want conflict", err)
			}
		})

		t.Run(be.name+" fresh snapshot remains writable", func(t *testing.T) {
			m, tenant, workspace, firstDecisionID, head := seed(t, "fresh")
			_, secondHead := advance(t, m, tenant, workspace, head, 2, firstDecisionID)
			var updated model.Record
			if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(workDecisionHeadKind)
				if err != nil {
					return err
				}
				updated, err = repo.Update(context.Background(), secondHead)
				return err
			}); err != nil {
				t.Fatalf("fresh decision head CAS: %v", err)
			}
			if got := updated.Int(model.ColVersion); got != 2 {
				t.Fatalf("fresh decision head version = %d, want 2", got)
			}
		})
	}
}

func workSchemaRequireDigest(t *testing.T, label, digest string) {
	t.Helper()
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
		t.Errorf("%s definition digest = %q, want %d-byte lowercase SHA-256", label, digest, sha256.Size)
	}
}

func TestWorkSchemaBootRejectsMissingTrigger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "work-schema.db")
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER sessions_work_item_state_guard_ins"); err != nil {
		_ = raw.Close()
		t.Fatalf("drop required trigger: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	st, err = engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}, m.RegisterSchema)
	if st != nil {
		_ = st.Close()
	}
	if !errors.Is(err, store.ErrSchemaTriggerMissing) {
		t.Fatalf("open without required trigger: err = %v, want ErrSchemaTriggerMissing", err)
	}
}

// TestWorkSchemaBootRejectsReplacedSQLiteTrigger is the compiling mutation for
// the definition digest. The required name and table remain present, but the
// executable body is a no-op. Restoring the catalog's original statement must
// make boot clean again, so a control that rejects every database cannot pass.
func TestWorkSchemaBootRejectsReplacedSQLiteTrigger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "work-schema-replaced.db")
	open := func() (store.Store, error) {
		return engine.Open(
			ctx,
			store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true},
			New().RegisterSchema,
		)
	}

	st, err := open()
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	const trigger = "sessions_work_guard_guard_ins"
	var original string
	if err := raw.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = ?", trigger,
	).Scan(&original); err != nil {
		t.Fatalf("read original trigger: %v", err)
	}
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER "+trigger); err != nil {
		t.Fatalf("drop original trigger: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER sessions_work_guard_guard_ins
BEFORE INSERT ON sessions_work_guard
BEGIN
  SELECT 1;
END`); err != nil {
		t.Fatalf("install compiling no-op trigger mutant: %v", err)
	}

	st, err = open()
	if st != nil {
		_ = st.Close()
	}
	if !errors.Is(err, store.ErrSchemaTriggerTampered) {
		t.Fatalf("open with same-name no-op trigger: err = %v, want ErrSchemaTriggerTampered", err)
	}

	if _, err := raw.ExecContext(ctx, "DROP TRIGGER "+trigger); err != nil {
		t.Fatalf("drop no-op trigger mutant: %v", err)
	}
	if _, err := raw.ExecContext(ctx, original); err != nil {
		t.Fatalf("restore original trigger: %v", err)
	}
	st, err = open()
	if err != nil {
		t.Fatalf("open after restoring original trigger: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close restored store: %v", err)
	}
}

// TestWorkSchemaPostgresBootRejectsReplacedTriggerAndFunction mutates each
// half of PostgreSQL's trigger object independently. Both mutants are valid DDL
// and retain the declared object name; each must fail boot by digest, then the
// exact catalog definition is restored and must boot cleanly.
func TestWorkSchemaPostgresBootRejectsReplacedTriggerAndFunction(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: PostgreSQL trigger/function mutation not exercised", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	ctx := context.Background()
	open := func() (store.Store, error) {
		return engine.Open(
			ctx,
			store.Config{Engine: store.EnginePostgres, DSN: pg.App, Debug: true},
			New().RegisterSchema,
		)
	}
	assertTampered := func(half string) {
		t.Helper()
		st, err := open()
		if st != nil {
			_ = st.Close()
		}
		if !errors.Is(err, store.ErrSchemaTriggerTampered) {
			t.Fatalf("open with replaced PostgreSQL %s: err = %v, want ErrSchemaTriggerTampered", half, err)
		}
	}
	assertClean := func(stage string) {
		t.Helper()
		st, err := open()
		if err != nil {
			t.Fatalf("open %s: %v", stage, err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close %s: %v", stage, err)
		}
	}

	assertClean("before mutation")
	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw PostgreSQL: %v", err)
	}
	defer raw.Close() //nolint:errcheck

	var originalFunction string
	if err := raw.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_get_functiondef('public.olivares_sessions_work_validate()'::regprocedure)",
	).Scan(&originalFunction); err != nil {
		t.Fatalf("read original validation function: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE OR REPLACE FUNCTION olivares_sessions_work_validate()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RETURN NEW;
END;
$function$`); err != nil {
		t.Fatalf("install compiling no-op function mutant: %v", err)
	}
	assertTampered("function body")
	if _, err := raw.ExecContext(ctx, originalFunction); err != nil {
		t.Fatalf("restore original validation function: %v", err)
	}
	assertClean("after restoring function")

	const trigger = "sessions_work_item_state_guard"
	var originalTrigger string
	if err := raw.QueryRowContext(ctx, `
SELECT pg_catalog.pg_get_triggerdef(t.oid, false)
FROM pg_catalog.pg_trigger t
WHERE t.tgname = $1
  AND t.tgrelid = 'public.sessions_work_item'::regclass`, trigger,
	).Scan(&originalTrigger); err != nil {
		t.Fatalf("read original work-item trigger: %v", err)
	}
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER "+trigger+" ON sessions_work_item"); err != nil {
		t.Fatalf("drop original work-item trigger: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER sessions_work_item_state_guard
BEFORE INSERT ON sessions_work_item
FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate()`); err != nil {
		t.Fatalf("install compiling reduced-event trigger mutant: %v", err)
	}
	assertTampered("trigger definition")
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER "+trigger+" ON sessions_work_item"); err != nil {
		t.Fatalf("drop trigger mutant: %v", err)
	}
	if _, err := raw.ExecContext(ctx, originalTrigger); err != nil {
		t.Fatalf("restore original work-item trigger: %v", err)
	}
	assertClean("after restoring trigger")
}

// TestWorkSchemaPostgresRLSFunctionallyDenies is a raw SQL probe: the query has
// no tenant predicate, so only FORCE RLS can hide tenant A's row from tenant B.
// The same query bound to tenant A must see the row, proving no over-denial.
func TestWorkSchemaPostgresRLSFunctionallyDenies(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: PostgreSQL K1 RLS probe not exercised", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	ctx := context.Background()
	m := New()
	st, err := engine.Open(
		ctx,
		store.Config{Engine: store.EnginePostgres, DSN: pg.App, Debug: true},
		m.RegisterSchema,
	)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var tenantA, tenantB model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		orgA, err := sys.CreateOrg(ctx, model.Org{Name: "Work RLS A", Slug: "work-rls-a", Status: model.StatusActive})
		if err != nil {
			return err
		}
		orgB, err := sys.CreateOrg(ctx, model.Org{Name: "Work RLS B", Slug: "work-rls-b", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenantA, tenantB = orgA.TenantID, orgB.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenants: %v", err)
	}
	m.UseData(api.NewModuleData(st))

	var workspaceA model.ID
	if err := m.data.View(ctx, tenantA, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(ctx)
		if err == nil {
			workspaceA = workspace.ID
		}
		return err
	}); err != nil {
		t.Fatalf("tenant A default workspace: %v", err)
	}
	itemA := workSchemaMustCreate(t, ctx, m, tenantA, workItemKind, workSchemaItem(workspaceA, "tenant A secret"))

	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw PostgreSQL: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	rawCount := func(tenant model.TenantID) int {
		t.Helper()
		tx, err := raw.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin raw RLS probe: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.ExecContext(ctx,
			"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String(),
		); err != nil {
			t.Fatalf("bind raw RLS probe to tenant %s: %v", tenant, err)
		}
		var count int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM sessions_work_item WHERE id = $1", itemA.String(model.ColID),
		).Scan(&count); err != nil {
			t.Fatalf("raw work-item count for tenant %s: %v", tenant, err)
		}
		return count
	}
	crossTenantCount := rawCount(tenantB)
	sameTenantCount := rawCount(tenantA)
	t.Run("cross-tenant denied", func(t *testing.T) {
		if crossTenantCount != 0 {
			t.Fatalf("RLS leaked tenant A's work item to tenant B: count = %d, want 0", crossTenantCount)
		}
	})
	t.Run("same-tenant allowed", func(t *testing.T) {
		if sameTenantCount != 1 {
			t.Fatalf("RLS over-denied tenant A's own work item: count = %d, want 1", sameTenantCount)
		}
	})
}

func TestWorkSchemaGuardsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			ctx := context.Background()
			workspaceA, workspaceB := workSchemaWorkspaces(t, ctx, m, tenant)

			itemAInput := workSchemaItem(workspaceA, "item A")
			itemBInput := workSchemaItem(workspaceA, "item B")
			itemCInput := workSchemaItem(workspaceB, "item C")
			itemA := workSchemaMustCreate(t, ctx, m, tenant, workItemKind, itemAInput)
			itemB := workSchemaMustCreate(t, ctx, m, tenant, workItemKind, itemBInput)
			itemC := workSchemaMustCreate(t, ctx, m, tenant, workItemKind, itemCInput)
			itemAID := itemA.String(model.ColID)
			itemBID := itemB.String(model.ColID)
			itemCID := itemC.String(model.ColID)

			dependencyInput := workSchemaDependency(workspaceA, itemAID, itemBID)
			workSchemaMustCreate(t, ctx, m, tenant, workDependencyKind, dependencyInput)
			acceptanceInput := workSchemaAcceptance(workspaceA, itemAID, "definition")
			workSchemaMustCreate(t, ctx, m, tenant, workAcceptanceKind, acceptanceInput)

			decisionHash := workSchemaHash("decision-scope")
			decisionInput := workSchemaDecision(workspaceA, itemAID, "scope", decisionHash)
			decision := workSchemaMustCreate(t, ctx, m, tenant, workDecisionKind, decisionInput)
			headInput := workSchemaDecisionHead(
				workspaceA, itemAID, "scope", decision.String(model.ColID), decisionHash,
			)
			workSchemaMustCreate(t, ctx, m, tenant, workDecisionHeadKind, headInput)

			reviewHash := workSchemaHash("decision-review")
			reviewDecision := workSchemaMustCreate(
				t, ctx, m, tenant, workDecisionKind,
				workSchemaDecision(workspaceA, itemAID, "review", reviewHash),
			)

			commandInput := workSchemaCommand(workspaceA, itemAID, "valid")
			command := workSchemaMustCreate(t, ctx, m, tenant, workCommandKind, commandInput)
			commandID := command.String(colCommandID)
			eventInput := workSchemaEvent(workspaceA, itemAID, commandID, 1, "valid")
			event := workSchemaMustCreate(t, ctx, m, tenant, workEventKind, eventInput)
			workSchemaMustCreate(
				t, ctx, m, tenant, workOutboxKind,
				workSchemaOutbox(workspaceA, event.String(colEventID)),
			)

			eventForLifecycle := workSchemaMustCreate(
				t, ctx, m, tenant, workEventKind,
				workSchemaEvent(workspaceA, itemAID, commandID, 2, "outbox-lifecycle"),
			)
			eventForLineage := workSchemaMustCreate(
				t, ctx, m, tenant, workEventKind,
				workSchemaEvent(workspaceB, itemCID, commandID, 1, "outbox-lineage"),
			)
			guardInput := workSchemaGuard(workspaceA, "dependency_graph")
			workSchemaMustCreate(t, ctx, m, tenant, workGuardKind, guardInput)

			invalidItemVocabulary := workSchemaClone(itemAInput)
			invalidItemVocabulary[colWorkPriority] = "urgent"
			invalidItemHash := workSchemaClone(itemAInput)
			invalidItemHash[colWorkBriefHash] = []byte("short")
			invalidBlockedState := workSchemaClone(itemAInput)
			invalidBlockedState[colWorkStatus] = "blocked"
			invalidBlockedState[colWorkReadyAt] = workSchemaTime()
			invalidItemLineage := workSchemaClone(itemAInput)
			invalidItemLineage[colWorkParentID] = itemCID

			invalidSelfDependency := workSchemaDependency(workspaceA, itemAID, itemAID)
			invalidCrossDependency := workSchemaDependency(workspaceA, itemAID, itemCID)
			invalidCycle := workSchemaDependency(workspaceA, itemBID, itemAID)

			invalidAcceptanceEvidence := workSchemaAcceptance(workspaceA, itemAID, "evidence-required")
			invalidAcceptanceEvidence[colAccState] = "passed"
			invalidAcceptanceLineage := workSchemaAcceptance(workspaceA, itemCID, "lineage")

			invalidDecisionHash := workSchemaDecision(
				workspaceA, itemAID, "bad-hash", []byte("short"),
			)
			invalidDecisionReferences := workSchemaDecision(
				workspaceA, itemAID, "bad-reference", workSchemaHash("bad-reference"),
			)
			invalidDecisionReferences[colDecisionOperation] = "revoke"
			invalidDecisionHead := workSchemaDecisionHead(
				workspaceA, itemAID, "review", reviewDecision.String(model.ColID), workSchemaHash("wrong-head"),
			)

			invalidCommandHash := workSchemaCommand(workspaceA, itemAID, "bad-hash")
			invalidCommandHash[colCommandRequestHash] = []byte("short")
			invalidCommandLineage := workSchemaCommand(workspaceA, itemCID, "bad-lineage")

			invalidEventHash := workSchemaEvent(workspaceA, itemAID, commandID, 3, "bad-hash")
			invalidEventHash[colEventPayloadHash] = []byte("short")
			invalidEventLineage := workSchemaEvent(workspaceA, itemCID, commandID, 4, "bad-lineage")

			invalidOutboxLifecycle := workSchemaOutbox(
				workspaceA, eventForLifecycle.String(colEventID),
			)
			invalidOutboxLifecycle[colOutboxState] = "delivering"
			invalidOutboxLineage := workSchemaOutbox(
				workspaceA, eventForLineage.String(colEventID),
			)

			invalidGuardVocabulary := workSchemaGuard(workspaceA, "counter")
			invalidGuardState := workSchemaGuard(workspaceB, "dependency_graph")
			invalidGuardState[colGuardLastDBTime] = workSchemaTime()

			cases := []struct {
				name string
				kind model.Kind
				rec  model.Record
			}{
				{name: "work item vocabulary", kind: workItemKind, rec: invalidItemVocabulary},
				{name: "work item 32 byte hash", kind: workItemKind, rec: invalidItemHash},
				{name: "work item blocked reason coherence", kind: workItemKind, rec: invalidBlockedState},
				{name: "work item parent workspace", kind: workItemKind, rec: invalidItemLineage},
				{name: "dependency self edge", kind: workDependencyKind, rec: invalidSelfDependency},
				{name: "dependency endpoint workspace", kind: workDependencyKind, rec: invalidCrossDependency},
				{name: "dependency cycle", kind: workDependencyKind, rec: invalidCycle},
				{name: "acceptance evidence coherence", kind: workAcceptanceKind, rec: invalidAcceptanceEvidence},
				{name: "acceptance parent workspace", kind: workAcceptanceKind, rec: invalidAcceptanceLineage},
				{name: "decision 32 byte hash", kind: workDecisionKind, rec: invalidDecisionHash},
				{name: "decision operation references", kind: workDecisionKind, rec: invalidDecisionReferences},
				{name: "decision head evidence", kind: workDecisionHeadKind, rec: invalidDecisionHead},
				{name: "command 32 byte hash", kind: workCommandKind, rec: invalidCommandHash},
				{name: "command result workspace", kind: workCommandKind, rec: invalidCommandLineage},
				{name: "event 32 byte hash", kind: workEventKind, rec: invalidEventHash},
				{name: "event aggregate workspace", kind: workEventKind, rec: invalidEventLineage},
				{name: "outbox lifecycle coherence", kind: workOutboxKind, rec: invalidOutboxLifecycle},
				{name: "outbox event workspace", kind: workOutboxKind, rec: invalidOutboxLineage},
				{name: "guard vocabulary", kind: workGuardKind, rec: invalidGuardVocabulary},
				{name: "guard state coherence", kind: workGuardKind, rec: invalidGuardState},
			}
			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					if _, err := workSchemaCreate(ctx, m, tenant, tc.kind, tc.rec); err == nil {
						t.Fatalf("invalid %s row was accepted", tc.kind)
					}
				})
			}
		})
	}
}

func workSchemaWorkspaces(
	t *testing.T,
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
) (model.ID, model.ID) {
	t.Helper()
	var defaultID model.ID
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(ctx)
		defaultID = workspace.ID
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	var otherID model.ID
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		workspace, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Other", Slug: "other", Status: model.StatusActive,
		})
		otherID = workspace.ID
		return err
	}); err != nil {
		t.Fatalf("create second workspace: %v", err)
	}
	return defaultID, otherID
}

func workSchemaCreate(
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
	rec model.Record,
) (model.Record, error) {
	var created model.Record
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		created, err = repo.Create(ctx, rec)
		return err
	})
	return created, err
}

func workSchemaMustCreate(
	t *testing.T,
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
	rec model.Record,
) model.Record {
	t.Helper()
	created, err := workSchemaCreate(ctx, m, tenant, kind, rec)
	if err != nil {
		t.Fatalf("create valid %s: %v", kind, err)
	}
	return created
}

func workSchemaRowCount(
	t *testing.T,
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
) int {
	t.Helper()
	rows := workSchemaRows(t, ctx, m, tenant, kind)
	return len(rows)
}

func workSchemaFirstRow(
	t *testing.T,
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
) model.Record {
	t.Helper()
	rows := workSchemaRows(t, ctx, m, tenant, kind)
	if len(rows) == 0 {
		t.Fatalf("%s has no rows", kind)
	}
	return rows[0]
}

func workSchemaRows(
	t *testing.T,
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
) []model.Record {
	t.Helper()
	var rows []model.Record
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, err = listAll(ctx, repo)
		return err
	}); err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	return rows
}

func workSchemaItem(workspace model.ID, title string) model.Record {
	brief := "durable brief for " + title
	return model.Record{
		colWorkWorkspaceID:        workspace.String(),
		colWorkKind:               "implementation",
		colWorkTitle:              title,
		colWorkBrief:              brief,
		colWorkBriefHash:          workSchemaHash(brief),
		colWorkContextRefs:        "[]",
		colWorkStatus:             "draft",
		colWorkPriority:           "p2",
		colWorkOwnerKind:          "user",
		colWorkOwnerRef:           "user:test",
		colWorkOwnerEpoch:         int64(1),
		colWorkProvKind:           "human",
		colWorkProvRef:            "test",
		colWorkAcceptanceRevision: int64(1),
		colWorkLastEventSeq:       int64(0),
	}
}

func workSchemaDependency(workspace model.ID, itemID, dependsOnID string) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(),
		colWorkItemID:      itemID,
		colDepDependsOnID:  dependsOnID,
		colDepRelation:     "blocks",
		colDepActive:       true,
		colDepAddedByKind:  "user",
		colDepAddedByRef:   "user:test",
	}
}

func workSchemaAcceptance(workspace model.ID, itemID, key string) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(),
		colWorkItemID:      itemID,
		colAccKey:          key,
		colAccOrdinal:      int64(0),
		colAccStatement:    "observable outcome",
		colAccRequired:     true,
		colAccState:        "pending",
	}
}

func workSchemaDecision(workspace model.ID, itemID, key string, hash []byte) model.Record {
	return model.Record{
		colWorkWorkspaceID:     workspace.String(),
		colWorkItemID:          itemID,
		colDecisionKey:         key,
		colDecisionSeq:         int64(1),
		colDecisionSubjectKind: "work_item",
		colDecisionSubjectRef:  itemID,
		colDecisionOperation:   "set",
		colDecisionStatement:   "use the durable kernel",
		colDecisionRationale:   "the work must survive sessions",
		colDecisionByKind:      "user",
		colDecisionByRef:       "user:test",
		colDecisionAuthority:   "brief:test",
		colDecisionEffectiveAt: workSchemaTime(),
		colDecisionHash:        hash,
	}
}

func workSchemaDecisionHead(
	workspace model.ID,
	itemID, key, decisionID string,
	hash []byte,
) model.Record {
	return model.Record{
		colWorkWorkspaceID:    workspace.String(),
		colWorkItemID:         itemID,
		colDecisionKey:        key,
		colDecisionCurrentID:  decisionID,
		colDecisionCurrentSeq: int64(1),
		colDecisionHeadState:  "effective",
		colDecisionHeadHash:   hash,
	}
}

func workSchemaCommand(workspace model.ID, resultID, seed string) model.Record {
	return model.Record{
		colWorkWorkspaceID:     workspace.String(),
		colCommandID:           model.NewID().String(),
		colCommandActorFP:      workSchemaHash("actor-" + seed),
		colCommandScope:        "POST:/v1/work-items",
		colCommandIdempotency:  workSchemaHash("idempotency-" + seed),
		colCommandRequestHash:  workSchemaHash("request-" + seed),
		colCommandPlanHash:     workSchemaHash("plan-" + seed),
		colCommandResultKind:   "sessions.work_item",
		colCommandResultID:     resultID,
		colCommandHTTPStatus:   int64(201),
		colCommandResponse:     "{}",
		colCommandResponseHash: workSchemaHash("response-" + seed),
		colCommandAuditSeq:     int64(1),
		colCommandAuditHash:    workSchemaHash("audit-" + seed),
		colCommandCompletedAt:  workSchemaTime(),
	}
}

func workSchemaEvent(
	workspace model.ID,
	aggregateID, commandID string,
	seq int64,
	seed string,
) model.Record {
	return model.Record{
		colWorkWorkspaceID:    workspace.String(),
		colEventID:            model.NewID().String(),
		colEventAggregateKind: "sessions.work_item",
		colEventAggregateID:   aggregateID,
		colEventSeq:           seq,
		colEventType:          "work.created",
		colEventActorKind:     "user",
		colEventActorRef:      "user:test",
		colEventOccurredAt:    workSchemaTime(),
		colEventPayload:       "{}",
		colEventPayloadHash:   hashBytes([]byte("{}")),
		colEventCommandID:     commandID,
		colEventAuditSeq:      seq,
		colEventAuditHash:     workSchemaHash("event-audit-" + seed),
	}
}

func workSchemaOutbox(workspace model.ID, eventID string) model.Record {
	return model.Record{
		colWorkWorkspaceID:     workspace.String(),
		colOutboxEventID:       eventID,
		colOutboxState:         "pending",
		colOutboxAttempts:      int64(0),
		colOutboxNextAttemptAt: workSchemaTime(),
	}
}

func workSchemaGuard(workspace model.ID, kind string) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(),
		colGuardKind:       kind,
		colGuardEpoch:      int64(0),
	}
}

func workSchemaClone(in model.Record) model.Record {
	out := make(model.Record, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func workSchemaHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func workSchemaTime() string {
	return model.NewTimestamp(time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)).String()
}
