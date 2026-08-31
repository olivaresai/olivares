// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestReconcileCoreColumns proves the additive-column reconcile converges an
// already-migrated table (one that predates a descriptor's new fields) to the
// descriptor without a destructive migration, is idempotent, and refuses a
// non-nullable add. This is the safety net for adding core auth columns post-v2.
func TestReconcileCoreColumns(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // keep the in-memory schema alive across statements
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}

	// An "old" table: it has id/tenant_id/label but is missing the columns the
	// descriptor below has since grown.
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE widgets (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, label TEXT)"); err != nil {
		t.Fatal(err)
	}

	desc := model.EntityDescriptor{
		Kind:  "core.widget",
		Table: "widgets",
		Fields: []model.FieldSpec{
			{Name: "label", Kind: model.KindText, Nullable: true},
			{Name: "note", Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: "ext_id", Kind: model.KindUUID, Nullable: true},
		},
	}

	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{desc}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	cols := make(map[string]bool)
	for _, c := range tableColumns(t, db, "widgets") {
		cols[c] = true
	}
	for _, want := range []string{"note", "ext_id"} {
		if !cols[want] {
			t.Errorf("reconcile did not add column %q (have %v)", want, cols)
		}
	}

	// The indexed field's secondary index now exists.
	if !indexExists(t, db, "widgets_note_idx") {
		t.Error("reconcile did not create the widgets_note_idx index")
	}

	// Idempotent: a second pass adds nothing and does not error (the columns and
	// index already exist).
	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{desc}); err != nil {
		t.Fatalf("reconcile not idempotent: %v", err)
	}

	// A missing NON-nullable column is refused (it needs a hand-authored migration
	// with a default, not a silent ADD COLUMN that an existing populated table
	// cannot satisfy).
	bad := desc
	bad.Fields = append(append([]model.FieldSpec(nil), desc.Fields...),
		model.FieldSpec{Name: "required_col", Kind: model.KindText, Nullable: false})
	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{bad}); err == nil {
		t.Error("reconcile accepted a non-nullable column add; want an error")
	}
}

// TestReconcileColumnsModuleTableUpgrade pins the fix for MODULE-owned tables: a
// module descriptor that gained a nullable column (an in-place upgrade) MUST get the
// column on the next boot. applyModuleTables only CREATEs a not-yet-tracked table and
// SKIPs an already-tracked one — so without the reconcile the column is missing and every
// SELECT/INSERT over it breaks. This proves the regression (applyModuleTables does NOT add
// it) and the fix (reconcileColumns, now wired into the boot for module descriptors, does).
func TestReconcileColumnsModuleTableUpgrade(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // keep the in-memory schema alive across statements
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}

	v1 := model.EntityDescriptor{
		Kind: "test.thing", Table: "test_things",
		Fields: []model.FieldSpec{{Name: "label", Kind: model.KindText}},
	}
	// v2 appends a nullable column last (the expand-only shape).
	v2 := model.EntityDescriptor{
		Kind: "test.thing", Table: "test_things",
		Fields: []model.FieldSpec{
			{Name: "label", Kind: model.KindText},
			{Name: "extra", Kind: model.KindText, Nullable: true},
		},
	}
	has := func() map[string]bool {
		m := map[string]bool{}
		for _, c := range tableColumns(t, db, "test_things") {
			m[c] = true
		}
		return m
	}

	// First boot: the module table is created from v1 and tracked.
	if err := applyModuleTables(ctx, db, dia, []model.EntityDescriptor{v1}); err != nil {
		t.Fatalf("first applyModuleTables: %v", err)
	}
	// Upgrade boot: applyModuleTables with v2 sees the table already tracked and SKIPs it,
	// so the new column is NOT added by that path (the bug, without the reconcile).
	if err := applyModuleTables(ctx, db, dia, []model.EntityDescriptor{v2}); err != nil {
		t.Fatalf("second applyModuleTables: %v", err)
	}
	if has()["extra"] {
		t.Fatal("precondition: applyModuleTables must NOT add a column to an already-tracked module table")
	}
	// The fix: the boot's module reconcile adds the missing nullable column.
	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{v2}); err != nil {
		t.Fatalf("reconcileColumns (module): %v", err)
	}
	if !has()["extra"] {
		t.Errorf("reconcileColumns must add the new nullable column to the module table (have %v)", tableColumns(t, db, "test_things"))
	}
	// Idempotent on a second pass.
	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{v2}); err != nil {
		t.Fatalf("reconcileColumns not idempotent on module table: %v", err)
	}
}

// TestReconcileCreatesMissingCoreTable proves a wholly NEW core table (a
// descriptor added to the catalog after a database was migrated —
// webauthn_credentials, the SCIM group tables) is CREATED by the
// reconcile — table, indexes and guard triggers together — idempotently, so an
// existing database converges to the same schema a fresh one gets from its
// regenerated v2 migration. NOT NULL fields are fine on creation (the table is
// born empty); the guard must not just exist but FIRE.
func TestReconcileCreatesMissingCoreTable(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}
	// An "existing" database: the v1 tenancy prerequisite exists (the guard
	// triggers reference the scope-pin table), but the new descriptor's table
	// does not.
	for _, stmt := range dia.TenancyStmts() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	desc := model.EntityDescriptor{
		Kind:  "core.ghost",
		Table: "ghosts",
		Fields: []model.FieldSpec{
			{Name: "target_tenant_id", Kind: model.KindUUID, Nullable: false, Indexed: true},
			{Name: "label", Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{Name: "ghosts_uniq", Columns: []string{"tenant_id", "target_tenant_id", "label"}, Unique: true},
		},
	}

	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{desc}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// The table matches its descriptor (base columns + entity columns, in order).
	if got, want := tableColumns(t, db, "ghosts"), desc.AllColumns(); !equalStrings(got, want) {
		t.Errorf("created table columns = %v, want %v", got, want)
	}
	// Its declared indexes exist.
	for _, ix := range []string{"ghosts_target_tenant_id_idx", "ghosts_uniq"} {
		if !indexExists(t, db, ix) {
			t.Errorf("reconcile-created table is missing index %q", ix)
		}
	}
	// The tenant guard triggers exist — a reconcile-created table must be born
	// guarded, exactly like a v2- or module-created one.
	for _, trg := range []string{"ghosts_scope_ins", "ghosts_scope_upd", "ghosts_scope_del"} {
		if !triggerExists(t, db, trg) {
			t.Errorf("reconcile-created table is missing guard trigger %q", trg)
		}
	}
	// And the guard actually fires: with tenant A pinned, inserting a tenant-B
	// row aborts.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+dialect.ScopeTenantTable+"(tenant_id) VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa')"); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx,
		"INSERT INTO ghosts (id, tenant_id, created_at, updated_at, version, target_tenant_id, label) VALUES ('g1','bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb','t','t',1,'tt','x')")
	if err == nil {
		t.Error("cross-tenant insert on a reconcile-created table succeeded; want guard abort")
	}

	// Idempotent: a second pass over the now-existing table adds nothing and does
	// not error.
	if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{desc}); err != nil {
		t.Fatalf("reconcile re-run: %v", err)
	}
}

// triggerExists reports whether a named trigger is present in the SQLite schema.
func triggerExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return got == name
}

// indexExists reports whether a named index is present in the SQLite schema.
func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return got == name
}
