// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func inheritedReadDescriptors() (model.EntityDescriptor, model.EntityDescriptor) {
	parent := model.EntityDescriptor{
		Kind: "irr.parent", Table: "irr_parent",
		WorkspaceLineage: model.WorkspaceLineageSpec{Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden},
		Fields:           []model.FieldSpec{{Name: "workspace_id", Kind: model.KindUUID, Nullable: true}, {Name: "ref", Kind: model.KindText}},
		Indexes:          []model.IndexSpec{{Name: "parent_ref_uniq", Columns: []string{model.ColTenantID, "ref"}, Unique: true}},
	}
	child := model.EntityDescriptor{
		Kind: "irr.child", Table: "irr_child", AppendOnly: true,
		Fields:                 []model.FieldSpec{{Name: "parent_ref", Kind: model.KindText}, {Name: "detail", Kind: model.KindText}},
		WorkspaceInheritedRead: model.WorkspaceInheritedReadSpec{ParentKind: parent.Kind, ParentColumn: "ref", Column: "parent_ref"},
	}
	return parent, child
}

// A child opt-in must never turn an arbitrary readable parent into authority
// over another collection. Bad relations are refused before any store opens.
func TestInheritedReadRegistryRejectsInvalidRelations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*model.EntityDescriptor, *model.EntityDescriptor)
	}{
		{"partial parent", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.ParentKind = "" }},
		{"partial parent key", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.ParentColumn = "" }},
		{"partial child key", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.Column = "" }},
		{"unknown parent", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.ParentKind = "irr.unknown" }},
		{"self parent", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.ParentKind = c.Kind }},
		{"foreign namespace", func(p, c *model.EntityDescriptor) {
			p.Kind = "foreign.parent"
			p.Table = "foreign_parent"
			c.WorkspaceInheritedRead.ParentKind = p.Kind
		}},
		{"no parent lineage", func(p, c *model.EntityDescriptor) { p.WorkspaceLineage = model.WorkspaceLineageSpec{} }},
		{"missing parent key", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.ParentColumn = "missing" }},
		{"missing child key", func(p, c *model.EntityDescriptor) { c.WorkspaceInheritedRead.Column = "missing" }},
		{"nullable child key", func(p, c *model.EntityDescriptor) { c.Fields[0].Nullable = true }},
		{"nullable parent key", func(p, c *model.EntityDescriptor) { p.Fields[1].Nullable = true }},
		{"redacted child key", func(p, c *model.EntityDescriptor) { c.Fields[0].Redact = true }},
		{"redacted parent key", func(p, c *model.EntityDescriptor) { p.Fields[1].Redact = true }},
		{"mismatched key types", func(p, c *model.EntityDescriptor) { c.Fields[0].Kind = model.KindUUID }},
		{"unreadable key types", func(p, c *model.EntityDescriptor) { c.Fields[0].Kind = model.KindInt; p.Fields[1].Kind = model.KindInt }},
		{"no unique parent key", func(p, c *model.EntityDescriptor) { p.Indexes = nil }},
		{"nonunique parent key", func(p, c *model.EntityDescriptor) { p.Indexes[0].Unique = false }},
		{"broader unique key", func(p, c *model.EntityDescriptor) {
			p.Indexes[0].Columns = []string{model.ColTenantID, "ref", "workspace_id"}
		}},
		{"mutable child", func(p, c *model.EntityDescriptor) { c.AppendOnly = false }},
		{"direct and inherited", func(p, c *model.EntityDescriptor) {
			c.WorkspaceLineage = model.WorkspaceLineageSpec{Column: "parent_ref", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, child := inheritedReadDescriptors()
			tc.change(&parent, &child)
			r := newRegistry()
			if err := r.Register(parent); err != nil {
				t.Fatalf("parent fixture: %v", err)
			}
			if err := r.Register(child); !errors.Is(err, store.ErrInvalidDescriptor) {
				t.Fatalf("unsafe inherited reader registered: %v", err)
			}
		})
	}

}

func TestInheritedReadRegistryAcceptsDeclaredRelation(t *testing.T) {
	parent, child := inheritedReadDescriptors()
	r := newRegistry()
	if err := r.Register(parent); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(child); err != nil {
		t.Fatal(err)
	}
	// A missing declaration remains valid, with no inherited capability.
	child.Kind, child.Table = "irr.ordinary", "irr_ordinary"
	child.WorkspaceInheritedRead = model.WorkspaceInheritedReadSpec{}
	if err := r.Register(child); err != nil {
		t.Fatal(err)
	}
}

func registerInheritedRead(reg store.ExtensionRegistry) error {
	parent, child := inheritedReadDescriptors()
	if err := reg.Register(parent); err != nil {
		return err
	}
	if err := reg.Register(child); err != nil {
		return err
	}
	child.Kind, child.Table = "irr.ordinary", "irr_ordinary"
	child.WorkspaceInheritedRead = model.WorkspaceInheritedReadSpec{}
	return reg.Register(child)
}

func TestInheritedReadSQLiteConfinement(t *testing.T) {
	testInheritedReadConfinement(t, openSQLiteTest(t, registerInheritedRead))
}

func TestInheritedReadPostgresConfinement(t *testing.T) {
	pg := isolatedPGSplit(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4,
	}, registerInheritedRead)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testInheritedReadConfinement(t, st)
}

func TestInheritedReadKeepsHistoricalSQLiteRows(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "history.db"), Debug: true}
	parent, child := inheritedReadDescriptors()
	oldChild := child
	oldChild.WorkspaceInheritedRead = model.WorkspaceInheritedReadSpec{}
	st, err := Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		if err := reg.Register(parent); err != nil {
			return err
		}
		if err := reg.Register(oldChild); err != nil {
			return err
		}
		oldChild.Kind, oldChild.Table = "irr.ordinary", "irr_ordinary"
		return reg.Register(oldChild)
	})
	if err != nil {
		t.Fatal(err)
	}
	first := st
	t.Cleanup(func() { _ = first.Close() })
	tenant := provisionTenant(t, st, "historical-inherited")
	var workspace, parentID model.ID
	var oldEvent model.Record
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "history", Slug: "history", Status: model.StatusActive})
		if err != nil {
			return err
		}
		workspace = ws.ID
		parents, err := sc.Ext(parent.Kind)
		if err != nil {
			return err
		}
		rec, err := parents.Create(ctx, model.Record{"ref": "unchanged-historical-ref", "workspace_id": workspace.String()})
		if err != nil {
			return err
		}
		parentID = model.ID(rec.String(model.ColID))
		events, err := sc.Ext(child.Kind)
		if err != nil {
			return err
		}
		oldEvent, err = events.Create(ctx, model.Record{"parent_ref": "unchanged-historical-ref", "detail": "original-evidence-hash"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, cfg, registerInheritedRead)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, sc, workspace)
		if err != nil {
			return err
		}
		rows, _, err := store.ListInheritedExtension(ctx, sc, child.Kind, parentID, model.Query{})
		if err != nil {
			return err
		}
		if len(rows) != 1 || len(rows[0]) != len(oldEvent) {
			t.Fatalf("historical row shape changed: old=%v rows=%v", oldEvent, rows)
		}
		for key, value := range oldEvent {
			if fmt.Sprint(value) != fmt.Sprint(rows[0][key]) {
				t.Fatalf("historical column %s rewritten: old=%v current=%v", key, value, rows[0][key])
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The fixture stores only the historic parent_ref in each immutable child.
// Neither a new column nor a rewritten event is needed for inherited reads.
func testInheritedReadConfinement(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "inherited")
	otherTenant := provisionTenant(t, st, "other-inherited")
	var workspaceA, workspaceB model.ID
	var inside, otherInside, outside, unset, unknownWorkspace, foreign model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i, target := range []*model.ID{&workspaceA, &workspaceB} {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{
				Name: []string{"A", "B"}[i], Slug: []string{"inherited-a", "inherited-b"}[i], Status: model.StatusActive,
			})
			if err != nil {
				return err
			}
			*target = ws.ID
		}
		parents, err := sc.Ext("irr.parent")
		if err != nil {
			return err
		}
		for _, p := range []struct {
			id        *model.ID
			ref       string
			workspace model.ID
		}{
			{&inside, "inside-ref", workspaceA}, {&otherInside, "other-inside-ref", workspaceA},
			{&outside, "outside-ref", workspaceB}, {&unset, "unset-ref", ""},
			{&unknownWorkspace, "unknown-workspace-ref", model.NewID()},
		} {
			r := model.Record{"ref": p.ref}
			if !p.workspace.IsZero() {
				r["workspace_id"] = p.workspace.String()
			}
			rec, err := parents.Create(ctx, r)
			if err != nil {
				return err
			}
			*p.id = model.ID(rec.String(model.ColID))
		}
		children, err := sc.Ext("irr.child")
		if err != nil {
			return err
		}
		for _, ref := range []string{"inside-ref", "inside-ref", "other-inside-ref", "outside-ref", "unset-ref", "orphan-ref"} {
			if _, err := children.Create(ctx, model.Record{"parent_ref": ref, "detail": ref + "-event"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(ctx, otherTenant, func(sc store.Scope) error {
		parents, err := sc.Ext("irr.parent")
		if err != nil {
			return err
		}
		// A valid-looking workspace and identical public key in another tenant
		// can never make its children visible through the bound tenant's scope.
		rec, err := parents.Create(ctx, model.Record{"ref": "inside-ref", "workspace_id": workspaceA.String()})
		if err != nil {
			return err
		}
		foreign = model.ID(rec.String(model.ColID))
		children, err := sc.Ext("irr.child")
		if err != nil {
			return err
		}
		_, err = children.Create(ctx, model.Record{"parent_ref": "inside-ref", "detail": "FOREIGN-TENANT-SECRET"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view := func(t *testing.T, confined bool, fn func(store.Scope)) {
		t.Helper()
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			if confined {
				var err error
				sc, err = store.ConfineWorkspace(ctx, sc, workspaceA)
				if err != nil {
					return err
				}
			}
			fn(sc)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		id   model.ID
	}{
		{"foreign workspace", outside}, {"other tenant", foreign}, {"missing", model.NewID()},
		{"unset lineage", unset}, {"orphan key is not a parent", "orphan-ref"},
		{"unknown workspace", unknownWorkspace},
		{"public key is not a row ID", "inside-ref"}, {"empty parent", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view(t, true, func(sc store.Scope) {
				rows, page, err := store.ListInheritedExtension(ctx, sc, "irr.child", tc.id, model.Query{})
				if !errors.Is(err, store.ErrNotFound) || len(rows) != 0 || page.HasMore {
					t.Fatalf("concealed parent: rows=%v page=%+v err=%v", rows, page, err)
				}
			})
		})
	}
	t.Run("direct collection stays refused", func(t *testing.T) {
		view(t, true, func(sc store.Scope) {
			if _, err := sc.Ext("irr.child"); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
				t.Fatalf("direct confined child handle: %v", err)
			}
		})
	})
	t.Run("undeclared relation stays refused", func(t *testing.T) {
		view(t, true, func(sc store.Scope) {
			rows, _, err := store.ListInheritedExtension(ctx, sc, "irr.ordinary", inside, model.Query{})
			if !errors.Is(err, store.ErrWorkspaceLineageRequired) || len(rows) != 0 {
				t.Fatalf("undeclared reader: rows=%v err=%v", rows, err)
			}
		})
	})
	t.Run("opaque confined decorator has no raw fallback", func(t *testing.T) {
		view(t, true, func(sc store.Scope) {
			rows, _, err := store.ListInheritedExtension(ctx, struct{ store.Scope }{sc}, "irr.child", inside, model.Query{})
			if !errors.Is(err, store.ErrWorkspaceLineageRequired) || len(rows) != 0 {
				t.Fatalf("opaque scope unwrapped: rows=%v err=%v", rows, err)
			}
		})
	})
	t.Run("conflicting caller filters cannot choose another run", func(t *testing.T) {
		view(t, true, func(sc store.Scope) {
			q := model.Query{Filters: []model.Filter{
				{Column: "parent_ref", Op: model.OpEq, Value: "outside-ref"},
				{Column: "parent_ref", Op: model.OpEq, Value: "other-inside-ref"},
			}}
			rows, _, err := store.ListInheritedExtension(ctx, sc, "irr.child", inside, q)
			if err != nil || len(rows) != 2 {
				t.Fatalf("forced parent query: rows=%v err=%v", rows, err)
			}
			for _, row := range rows {
				if row.String("detail") != "inside-ref-event" || row.String(model.ColTenantID) != tenant.String() {
					t.Fatalf("parent/tenant confinement lost: %v", row)
				}
			}
			if q.Filters[0].Value != "outside-ref" || q.Filters[1].Value != "other-inside-ref" {
				t.Fatal("reader mutated the caller's query")
			}
		})
	})
	t.Run("other filters and paging stay bounded", func(t *testing.T) {
		view(t, true, func(sc store.Scope) {
			q := model.Query{Limit: 1, Filters: []model.Filter{{Column: "detail", Op: model.OpEq, Value: "inside-ref-event"}}}
			rows, page, err := store.ListInheritedExtension(ctx, sc, "irr.child", inside, q)
			if err != nil || len(rows) != 1 || !page.HasMore || page.Cursor == "" {
				t.Fatalf("first page: rows=%v page=%+v err=%v", rows, page, err)
			}
			q.Cursor = page.Cursor
			last, page, err := store.ListInheritedExtension(ctx, sc, "irr.child", inside, q)
			if err != nil || len(last) != 1 || page.HasMore || last[0].String(model.ColID) == rows[0].String(model.ColID) {
				t.Fatalf("next page: rows=%v page=%+v err=%v", last, page, err)
			}
			q.Cursor, q.Filters[0].Value = "", "outside-ref-event"
			rows, _, err = store.ListInheritedExtension(ctx, sc, "irr.child", inside, q)
			if err != nil || len(rows) != 0 {
				t.Fatalf("other filter discarded: rows=%v err=%v", rows, err)
			}
		})
	})
	for _, tc := range []struct {
		name     string
		id       model.ID
		ref      string
		count    int
		confined bool
	}{
		{"historical same run", inside, "inside-ref", 2, true},
		{"other same-workspace run", otherInside, "other-inside-ref", 1, true},
		{"unconfined outside run", outside, "outside-ref", 1, false},
		{"unconfined unset run", unset, "unset-ref", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view(t, tc.confined, func(sc store.Scope) {
				rows, _, err := store.ListInheritedExtension(ctx, sc, "irr.child", tc.id, model.Query{})
				if err != nil || len(rows) != tc.count {
					t.Fatalf("authorized parent: rows=%v err=%v", rows, err)
				}
				for _, row := range rows {
					if row.String("detail") != tc.ref+"-event" {
						t.Fatalf("another run or tenant's event returned: %v", row)
					}
				}
			})
		})
	}
	t.Run("parent moved after authorization stays concealed", func(t *testing.T) {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			parents, err := sc.Ext("irr.parent")
			if err != nil {
				return err
			}
			rec, err := parents.Get(ctx, inside)
			if err != nil {
				return err
			}
			rec["workspace_id"] = workspaceB.String()
			_, err = parents.Update(ctx, rec)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		view(t, true, func(sc store.Scope) {
			rows, _, err := store.ListInheritedExtension(ctx, sc, "irr.child", inside, model.Query{})
			if !errors.Is(err, store.ErrNotFound) || len(rows) != 0 {
				t.Fatalf("old parent identity overrode stored workspace: rows=%v err=%v", rows, err)
			}
		})
	})
}
