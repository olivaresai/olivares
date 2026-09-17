// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func lineageTestFacts(t *testing.T, st store.Store, tenant model.TenantID) map[model.Kind]store.AuthorizationFactRef {
	t.Helper()
	out := map[model.Kind]store.AuthorizationFactRef{}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, rel := range lineageRelations {
			f, err := store.ReadLineageFact(context.Background(), sc, rel.kind)
			if err != nil {
				return err
			}
			out[rel.kind] = f
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func openLineagePG(t *testing.T) store.Store {
	t.Helper()
	pg := isolatedPGSplit(t)
	st, err := Open(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
func TestLineageSelectiveSQLite(t *testing.T)   { testLineageSelective(t, openSQLiteTest(t, nil)) }
func TestLineageSelectivePostgres(t *testing.T) { testLineageSelective(t, openLineagePG(t)) }
func testLineageSelective(t *testing.T, st store.Store) {
	ctx := context.Background()
	tenant := provisionTenant(t, st, "lineage-selective")
	before := lineageTestFacts(t, st, tenant)
	var session model.Session
	var ws model.Workspace
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		ws, err = sc.Workspaces().Create(ctx, model.Workspace{Name: "lineage", Slug: "lineage"})
		if err != nil {
			return err
		}
		session, err = sc.Sessions().Create(ctx, model.Session{ExternalID: "lineage-session", WorkspaceID: ws.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := lineageTestFacts(t, st, tenant)
	for kind, old := range before {
		delta := int64(0)
		if kind == "core.session" || kind == "core.workspace" {
			delta = 1
		}
		if after[kind].Version != old.Version+delta {
			t.Fatalf("insert %s advanced wrong generation", kind)
		}
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		session.State = model.SessionCompleted
		session, err = sc.Sessions().Update(ctx, session)
		if err != nil {
			return err
		}
		session, err = sc.Sessions().Update(ctx, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	noops := lineageTestFacts(t, st, tenant)
	for kind, old := range after {
		if noops[kind] != old {
			t.Fatalf("presentation/no-op advanced %s", kind)
		}
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		session.ModelID = model.NewID()
		session, err = sc.Sessions().Update(ctx, session)
		if err != nil {
			return err
		}
		session.AgentID = model.NewID()
		session, err = sc.Sessions().Update(ctx, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	relevant := lineageTestFacts(t, st, tenant)
	if relevant["core.session"].Version != after["core.session"].Version+1 {
		t.Fatal("two relevant writes did not advance once")
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		if err := store.ValidateReadAuthority(ctx, sc, []store.AuthorizationFactRef{after["core.session"]}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale lineage accepted: %v", err)
		}
		confined, err := store.ConfineWorkspace(ctx, sc, ws.ID)
		if err != nil {
			return err
		}
		return store.ValidateReadAuthority(ctx, confined, []store.AuthorizationFactRef{relevant["core.session"]})
	}); err != nil {
		t.Fatal(err)
	}
	oldSession := session
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		session.ModelID = model.NewID()
		updated, e := sc.Sessions().Update(ctx, session)
		if e != nil {
			return e
		}
		_, _ = sc.Sessions().Update(ctx, oldSession) // discarded OCC error must poison the whole envelope
		session = updated
		return nil
	})
	if err == nil {
		t.Fatal("discarded writer failure committed")
	}
	rolled := lineageTestFacts(t, st, tenant)
	for kind, old := range relevant {
		if rolled[kind] != old {
			t.Fatalf("rollback advanced %s", kind)
		}
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error { return sc.Sessions().Delete(ctx, oldSession.ID) }); err != nil {
		t.Fatal(err)
	}
	deleted := lineageTestFacts(t, st, tenant)
	if deleted["core.session"].Version != relevant["core.session"].Version+1 {
		t.Fatal("soft delete did not invalidate")
	}
	// The raw app connection cannot omit writer enrollment, even for a no-op.
	ss := st.(*sqlStore)
	tx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.dia.BindTenant(ctx, tx, tenant); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, ss.dia.Rebind("UPDATE "+directoryWriterRelation(ss.dia, sessionDescriptor.Table)+" SET workspace_id=workspace_id WHERE tenant_id=?"), tenant.String())
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("raw source DML omitted lineage protocol")
	}
}

func TestLineageWriterTenantAndSystemConcurrencyPostgres(t *testing.T) {
	st := openLineagePG(t)
	// The two tenant provisions are fixture; the 20 s budget below covers the writer /
	// System concurrency only. Above them it was being spent by the fixture under -race on
	// a contended runner (01f81b8e81 / 4859cc43f3 / 346bce0c8a).
	a := provisionTenant(t, st, "lineage-a")
	b := provisionTenant(t, st, "lineage-b")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() { first <- st.Mutate(ctx, a, func(store.Scope) error { close(entered); <-release; return nil }) }()
	<-entered
	fast, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	if err := st.Mutate(fast, b, func(store.Scope) error { return nil }); err != nil {
		stop()
		close(release)
		<-first
		t.Fatalf("B blocked by A: %v", err)
	}
	if err := st.System(fast, func(sys store.SystemScope) error { _, _, err := sys.ListOrgsVisible(fast); return err }); err != nil {
		stop()
		close(release)
		<-first
		t.Fatalf("System read blocked by A: %v", err)
	}
	stop()
	sameEntered := make(chan struct{})
	same := make(chan error, 1)
	go func() { same <- st.Mutate(ctx, a, func(store.Scope) error { close(sameEntered); return nil }) }()
	systemDone := make(chan error, 1)
	go func() {
		systemDone <- st.System(ctx, func(sys store.SystemScope) error { return sys.EnsureDefaultWorkspaces(ctx) })
	}()
	select {
	case <-sameEntered:
		close(release)
		<-first
		t.Fatal("same tenant did not serialize")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-systemDone:
		close(release)
		<-first
		t.Fatalf("System mutator did not await tenant writer: %v", err)
	default:
	}
	close(release)
	for _, ch := range []<-chan error{first, same, systemDone} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	// New View does not acquire a writer gate or row lock.
	if err := st.View(ctx, a, func(sc store.Scope) error {
		f, err := store.ReadLineageFact(ctx, sc, "core.session")
		if err != nil {
			return err
		}
		if err := store.ValidateReadAuthority(ctx, sc, []store.AuthorizationFactRef{f}); err != nil {
			return err
		}
		return st.Mutate(ctx, a, func(store.Scope) error { return nil })
	}); err != nil {
		t.Fatalf("read barrier locked writer: %v", err)
	}
}
