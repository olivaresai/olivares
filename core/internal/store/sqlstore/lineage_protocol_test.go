// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
)

func TestLineageProjectionMatrixSQLite(t *testing.T) {
	testLineageProjectionMatrix(t, openSQLiteTest(t, nil))
}
func TestLineageProjectionMatrixPostgres(t *testing.T) {
	testLineageProjectionMatrix(t, openLineagePG(t))
}
func testLineageProjectionMatrix(t *testing.T, st store.Store) {
	ctx := context.Background()
	tenant := provisionTenant(t, st, "projection")
	ss := st.(*sqlStore)
	ids := map[model.Kind]model.ID{}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, e := sc.Workspaces().Create(ctx, model.Workspace{Name: "workspace", Slug: "projection"})
		if e != nil {
			return e
		}
		ids["core.workspace"] = ws.ID
		a, e := sc.Agents().Create(ctx, model.Agent{Name: "agent", WorkspaceID: ws.ID})
		if e != nil {
			return e
		}
		ids["core.agent"] = a.ID
		s, e := sc.Sessions().Create(ctx, model.Session{ExternalID: "projection", WorkspaceID: ws.ID, AgentID: a.ID})
		if e != nil {
			return e
		}
		ids["core.session"] = s.ID
		r, e := sc.Resources().Create(ctx, model.Resource{Name: "resource", WorkspaceID: ws.ID, Kind: "folder"})
		if e != nil {
			return e
		}
		ids["core.resource"] = r.ID
		g, e := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "group", Slug: "group", WorkspaceID: ws.ID})
		if e != nil {
			return e
		}
		ids["core.agent_group"] = g.ID
		m, e := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: g.ID, AgentID: a.ID})
		ids["core.agent_group_member"] = m.ID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range lineageRelations {
		for _, column := range rel.columns {
			t.Run(rel.table+"/"+column, func(t *testing.T) {
				before := lineageTestFacts(t, st, tenant)
				if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
					ts := sc.(*tenantScope)
					if err := ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }); err != nil {
						return err
					}
					var old any
					q := "SELECT " + column + " FROM " + directoryWriterRelation(ss.dia, rel.table) + " WHERE tenant_id=? AND id=?"
					if err := ts.tx.QueryRowContext(ctx, ss.dia.Rebind(q), tenant.String(), ids[rel.kind].String()).Scan(&old); err != nil {
						return err
					}
					value := any(model.NewID().String())
					if column == "deleted_at" {
						value = "2026-09-05T00:00:00.000000000Z"
					}
					q = "UPDATE " + directoryWriterRelation(ss.dia, rel.table) + " SET " + column + "=? WHERE tenant_id=? AND id=?"
					for _, v := range []any{value, old} {
						if _, err := ts.tx.ExecContext(ctx, ss.dia.Rebind(q), v, tenant.String(), ids[rel.kind].String()); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				after := lineageTestFacts(t, st, tenant)
				for kind, old := range before {
					delta := int64(0)
					if kind == rel.kind {
						delta = 1
					}
					if after[kind].Version != old.Version+delta {
						t.Fatalf("%s delta incorrect for %s", column, kind)
					}
				}
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					return store.ValidateReadAuthority(ctx, sc, []store.AuthorizationFactRef{before[rel.kind]})
				}); !errors.Is(err, store.ErrConflict) {
					t.Fatalf("ABA old fact accepted: %v", err)
				}
			})
		}
	}
	// A physical removal also closes a previously nonempty query, even for tables
	// whose normal repository exposes a soft deletion.
	for _, rel := range lineageRelations {
		before := lineageTestFacts(t, st, tenant)
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			ts := sc.(*tenantScope)
			if err := ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }); err != nil {
				return err
			}
			_, err := ts.tx.ExecContext(ctx, ss.dia.Rebind("DELETE FROM "+directoryWriterRelation(ss.dia, rel.table)+" WHERE tenant_id=? AND id=?"), tenant.String(), ids[rel.kind].String())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if after := lineageTestFacts(t, st, tenant); after[rel.kind].Version != before[rel.kind].Version+1 {
			t.Fatalf("delete failed to bump %s", rel.table)
		}
	}
}

func TestLineageMixedOrderSQLite(t *testing.T)   { testLineageMixedOrder(t, openSQLiteTest(t, nil)) }
func TestLineageMixedOrderPostgres(t *testing.T) { testLineageMixedOrder(t, openLineagePG(t)) }
func testLineageMixedOrder(t *testing.T, st store.Store) {
	ctx := context.Background()
	tenant := provisionTenant(t, st, "mixed")
	before := lineageTestFacts(t, st, tenant)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Create(ctx, model.Agent{Name: "mixed"})
		if err != nil {
			return err
		}
		if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{before["core.session"]}); err != nil {
			return err
		}
		_, err = sc.Sessions().Create(ctx, model.Session{ExternalID: "mixed"})
		if err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: "lineage.mixed"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stable := lineageTestFacts(t, st, tenant)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if e := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{stable["core.session"]}); e != nil {
			return e
		}
		_, _ = sc.Agents().Create(ctx, model.Agent{Name: "inverse"})
		return nil
	})
	if err == nil {
		t.Fatal("inverse directory after facts committed discarded error")
	}
	for k, v := range lineageTestFacts(t, st, tenant) {
		if stable[k] != v {
			t.Fatal("poison committed lineage")
		}
	}
}

func TestLineageAppSQLProtocolPostgres(t *testing.T) {
	st := openLineagePG(t)
	ss := st.(*sqlStore)
	ctx := context.Background()
	tenant := provisionTenant(t, st, "sql")
	other := provisionTenant(t, st, "foreign")
	deny := func(t *testing.T, state string, fn func(*sql.Tx) error) {
		t.Helper()
		tx, e := ss.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = ss.dia.BindTenant(ctx, tx, tenant); e != nil {
			t.Fatal(e)
		}
		if e = fn(tx); e == nil {
			t.Fatal("direct app action unexpectedly allowed")
		}
		var pgerr *pgconn.PgError
		if !errors.As(e, &pgerr) || pgerr.Code != state {
			t.Fatalf("wrong SQL refusal, want %s: %v", state, e)
		}
	}
	for _, table := range []string{lineageWriterTable, lineageTouchedTable, lineageSeededTable} {
		t.Run("direct_"+table, func(t *testing.T) {
			deny(t, "42501", func(tx *sql.Tx) error { _, e := tx.ExecContext(ctx, "DELETE FROM public."+table); return e })
		})
	}
	for _, query := range []string{"TRUNCATE public.sessions", "UPDATE public.core_sessions_lineage_epoch SET version=1", "SELECT public.olivares_lineage_begin($1)"} {
		t.Run(query, func(t *testing.T) {
			state := "42501"
			if query == "SELECT public.olivares_lineage_begin($1)" {
				state = "P0001"
			}
			deny(t, state, func(tx *sql.Tx) error {
				var e error
				if query == "SELECT public.olivares_lineage_begin($1)" {
					_, e = tx.ExecContext(ctx, query, tenant.String())
				} else {
					_, e = tx.ExecContext(ctx, query)
				}
				return e
			})
		})
	}
	t.Run("preinsert_touched", func(t *testing.T) {
		deny(t, "42501", func(tx *sql.Tx) error {
			tracker := newLineageWriteTracker(tx, ss.dia, false)
			if err := tracker.start(ctx, tenant); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO public.core_lineage_touched(writer_id,tenant_id,relation_name) VALUES(pg_catalog.txid_current()::text,$1,'sessions')", tenant.String())
			return err
		})
	})
	t.Run("foreign_enrollment", func(t *testing.T) {
		deny(t, "P0001", func(tx *sql.Tx) error {
			tracker := newLineageWriteTracker(tx, ss.dia, false)
			if e := tracker.start(ctx, tenant); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "SELECT public.olivares_lineage_begin($1)", other.String())
			return e
		})
	})
	t.Run("claimed_guc", func(t *testing.T) {
		deny(t, "P0001", func(tx *sql.Tx) error {
			if _, e := tx.ExecContext(ctx, "SELECT pg_catalog.set_config('app.lineage_writer','true',true)"); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "SELECT public.olivares_lineage_begin($1)", tenant.String())
			return e
		})
	})
	// Full retirement cannot permit a later reset, even through the callable
	// lifecycle helper under real exclusive participation.
	if err := st.System(ctx, func(sys store.SystemScope) error { return sys.DropTenant(ctx, other) }); err != nil {
		t.Fatal(err)
	}
	t.Run("retired_reseed", func(t *testing.T) {
		deny(t, "23505", func(tx *sql.Tx) error {
			if err := ss.dia.BindTenant(ctx, tx, other); err != nil {
				return err
			}
			if _, e := tx.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))", lineageGateKey); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "SELECT public.olivares_lineage_seed($1)", other.String())
			return e
		})
	})
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Sessions().Create(ctx, model.Session{ExternalID: "cleanup"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{lineageWriterTable, lineageTouchedTable} {
		var n int
		if e := ss.adminDB.QueryRowContext(ctx, "SELECT count(*) FROM public."+table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != 0 {
			t.Fatal(fmt.Sprintf("%s leaked markers", table))
		}
	}
}

func TestLineageSingleRolePostgres(t *testing.T) {
	pg := isolatedPG(t)
	st, err := Open(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testLineageSelective(t, st)
}

func TestLineageResourceDiscardedDeleteSQLite(t *testing.T) {
	st := openSQLiteTest(t, nil)
	ctx := context.Background()
	tenant := provisionTenant(t, st, "resource-poison")
	before := lineageTestFacts(t, st, tenant)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Resources().Create(ctx, model.Resource{Name: "uncommitted", Kind: "folder"}); err != nil {
			return err
		}
		_ = sc.Resources().Delete(ctx, model.NewID())
		return nil
	})
	if err == nil {
		t.Fatal("discarded resource Delete error committed")
	}
	for k, v := range lineageTestFacts(t, st, tenant) {
		if v != before[k] {
			t.Fatal("resource poison changed epoch")
		}
	}
}
