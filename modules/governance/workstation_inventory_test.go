// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// recordingRegistry keeps what the module registers while the real registry
// validates it.
type recordingRegistry struct {
	store.ExtensionRegistry
	got map[model.Kind]model.EntityDescriptor
}

func (r *recordingRegistry) Register(d model.EntityDescriptor) error {
	r.got[d.Kind] = d
	return r.ExtensionRegistry.Register(d)
}

// registeredGovernanceSchema opens a real store with the governance schema and returns
// every descriptor the module registered, by kind.
func registeredGovernanceSchema(t *testing.T) map[model.Kind]model.EntityDescriptor {
	t.Helper()
	rec := &recordingRegistry{got: map[model.Kind]model.EntityDescriptor{}}
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		func(reg store.ExtensionRegistry) error {
			rec.ExtensionRegistry = reg
			return New().RegisterSchema(rec)
		})
	if err != nil {
		t.Fatalf("open a store with the governance schema: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return rec.got
}

// The tool inventory has one table in the Community schema (lint:schema-parity), one
// row per (host, driver), no secret and no account column. Community never writes it.
func TestWorkstationToolTableIsInTheCommunitySchema(t *testing.T) {
	got := registeredGovernanceSchema(t)

	d, ok := got["governance.workstation_tool"]
	if !ok {
		t.Fatal("governance.workstation_tool is not registered")
	}
	if d.Table != "governance_workstation_tool" {
		t.Fatalf("table = %q", d.Table)
	}
	var names []string
	for _, f := range d.Fields {
		names = append(names, f.Name)
	}
	want := []string{"scope", "driver", "tool_version", "state", "checked_in_at"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("columns = %v, want %v", names, want)
	}
	// A workstation id can carry a person's login: it is scanned. The rest name no principal.
	for i, f := range d.Fields {
		switch f.Name {
		case "scope":
			if f.Principal == nil || f.Principal.Form != model.FormScan || f.Principal.Class != model.ClassEvidence {
				t.Errorf("scope = %#v, want a scanned evidence column", f.Principal)
			}
		case "driver", "tool_version", "state":
			if f.Principal == nil || f.Principal.Form != model.FormNone {
				t.Errorf("column %d %s = %#v, want a None declaration", i, f.Name, f.Principal)
			}
		}
	}
	if defects := d.PrincipalDefects(); len(defects) != 0 {
		t.Fatalf("principal declarations: %v", defects)
	}
	if len(d.Indexes) != 1 || !d.Indexes[0].Unique ||
		!reflect.DeepEqual(d.Indexes[0].Columns, []string{model.ColTenantID, "scope", "driver"}) {
		t.Fatalf("indexes = %#v, want one unique (tenant_id, scope, driver)", d.Indexes)
	}
}
