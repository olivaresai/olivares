// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The parity gate is only as trustworthy as the manifest's determinism: two
// collections of the SAME binary must be byte-identical, and the manifest must carry
// real schema (a silent empty manifest would let the cross-tag diff false-pass).
func TestSchemaManifestDeterministicAndNonEmpty(t *testing.T) {
	a, err := collectSchemaManifest()
	if err != nil {
		t.Fatalf("collect #1: %v", err)
	}
	b, err := collectSchemaManifest()
	if err != nil {
		t.Fatalf("collect #2: %v", err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("schema manifest is NON-deterministic across two collections")
	}
	if len(a.Entities) < 50 {
		t.Fatalf("manifest has only %d entities — the collector looks broken", len(a.Entities))
	}
	if a.SchemaVersion != 3 {
		t.Fatalf("schema manifest version = %d, want 3 for workspace initializers", a.SchemaVersion)
	}
	// The canonical ordering: entities sorted by table.
	for i := 1; i < len(a.Entities); i++ {
		if a.Entities[i-1].Table > a.Entities[i].Table {
			t.Fatalf("entities not sorted by table at %d (%q > %q)", i, a.Entities[i-1].Table, a.Entities[i].Table)
		}
	}
	// The eventing module's file migration must appear (the only one today).
	found := false
	for _, m := range a.Migrations {
		if m.Namespace == "eventing" {
			found = true
			if len(m.Files) == 0 {
				t.Fatal("eventing migration namespace has no files")
			}
			for _, f := range m.Files {
				if len(f.SHA256) != 64 {
					t.Fatalf("migration file %q has a non-sha256 hash %q", f.Path, f.SHA256)
				}
			}
		}
	}
	if !found {
		t.Fatal("eventing migration namespace missing from the manifest")
	}
	foundInitializer := false
	for _, initializer := range a.WorkspaceInitializers {
		if initializer.Key == "sessions.communication_guard.v1" {
			foundInitializer = true
		}
	}
	if !foundInitializer {
		t.Fatal("sessions communication guard workspace initializer missing from manifest")
	}
}

// The recorder must faithfully capture a descriptor's shape (table, columns, indexes,
// guards) so the manifest reflects the real registered schema.
func TestSchemaManifestRecorder(t *testing.T) {
	r := &schemaManifestRegistry{}
	if err := r.Register(model.EntityDescriptor{
		Kind:               "demo.thing",
		Table:              "demo_thing",
		Audited:            true,
		RetainOnTenantDrop: true,
		Fields: []model.FieldSpec{
			{Name: "label", Kind: model.KindText, Indexed: true},
			{Name: "secret", Kind: model.KindText, Redact: true, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{Name: "demo_thing_uniq", Columns: []string{"tenant_id", "label"}, Unique: true},
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(r.entities) != 1 {
		t.Fatalf("recorded %d entities, want 1", len(r.entities))
	}
	e := r.entities[0]
	if e.Table != "demo_thing" || e.Kind != "demo.thing" || !e.Audited || !e.RetainOnTenantDrop {
		t.Fatalf("entity = %+v, want demo_thing/demo.thing/audited/retained", e)
	}
	if len(e.Fields) != 2 || e.Fields[0].Name != "label" || e.Fields[0].Kind != model.KindText.String() {
		t.Fatalf("fields = %+v, want label:%s first", e.Fields, model.KindText.String())
	}
	if !e.Fields[1].Redact || !e.Fields[1].Nullable {
		t.Fatalf("second field must carry redact+nullable: %+v", e.Fields[1])
	}
	if len(e.Indexes) != 1 || !e.Indexes[0].Unique || e.Indexes[0].Columns[0] != "tenant_id" {
		t.Fatalf("index = %+v, want unique on (tenant_id, label)", e.Indexes)
	}
	initializer := store.WorkspaceInitializer{
		Key: "demo.thing.v1",
		Initialize: func(context.Context, store.WorkspaceInitializationScope) error {
			return nil
		},
	}
	if err := r.WorkspaceInitializer(initializer); err != nil {
		t.Fatalf("WorkspaceInitializer: %v", err)
	}
	if len(r.initializers) != 1 || r.initializers[0].Key != initializer.Key {
		t.Fatalf("workspace initializers = %#v, want %q", r.initializers, initializer.Key)
	}
	if err := r.WorkspaceInitializer(initializer); err == nil {
		t.Fatal("duplicate workspace initializer was not rejected")
	}
}

// RetainOnTenantDrop changes whether a binary deletes or preserves tenant rows,
// so it must change the parity oracle's trailing digest. Merely recording the
// field in memory is insufficient if the JSON vocabulary drops it.
func TestSchemaManifestDigestIncludesTenantRetentionLifecycle(t *testing.T) {
	base := schemaManifest{
		SchemaVersion: 3,
		Entities:      []manifestEntity{{Kind: "demo.thing", Table: "demo_thing"}},
	}
	retained := base
	retained.Entities = append([]manifestEntity(nil), base.Entities...)
	retained.Entities[0].RetainOnTenantDrop = true

	digest := func(manifest schemaManifest) [32]byte {
		t.Helper()
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal schema manifest: %v", err)
		}
		return sha256.Sum256(encoded)
	}
	if digest(base) == digest(retained) {
		t.Fatal("retain_on_tenant_drop did not change the schema manifest digest")
	}
}

// TestTheManifestRecordsASchemaInvariant covers the recorder directly, which the manifest's
// other tests do not: they assert entities and migrations, so every property of the
// invariants path was unasserted and a regression there would have been invisible.
//
// The manifest is a PARITY ORACLE — it exists to tell two builds apart. An invariants
// recorder that dropped, reordered or flattened a declaration would let it report two builds
// identical while they required different security triggers, which is the failure its own
// comment says moving SchemaInvariants onto ExtensionRegistry closed.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. `return nil` without appending to r.invariants. Red in `the declaration is recorded`.
//  2. Drop DefinitionSHA256 from the recorded trigger. Red in `the digest survives` — and
//     that field is the whole point: two triggers can share a name and differ in body.
//  3. Remove the sort.Slice. Red in `triggers are ordered deterministically`.
func TestTheManifestRecordsASchemaInvariant(t *testing.T) {
	reg := &schemaManifestRegistry{}
	err := reg.SchemaInvariants("demo", map[store.Engine][]store.SchemaTrigger{
		store.EnginePostgres: {
			{Name: "z_guard", Table: "demo_thing", DefinitionSHA256: "deadbeef"},
			{Name: "a_guard", Table: "demo_thing", DefinitionSHA256: "cafe"},
		},
		store.EngineSQLite: {{Name: "s_guard", Table: "demo_thing"}},
	})
	if err != nil {
		t.Fatalf("SchemaInvariants: %v", err)
	}

	t.Run("the declaration is recorded", func(t *testing.T) {
		if len(reg.invariants) != 1 || reg.invariants[0].Namespace != "demo" {
			t.Fatalf("recorded %+v, want one entry for namespace demo: a recorder that returns "+
				"success and keeps nothing makes the parity oracle blind to security triggers",
				reg.invariants)
		}
	})
	t.Run("both engines survive separately", func(t *testing.T) {
		got := reg.invariants[0].ByEngine
		if len(got["postgres"]) != 2 || len(got["sqlite"]) != 1 {
			t.Fatalf("by engine = %+v, want 2 postgres and 1 sqlite: engines are not "+
				"interchangeable and collapsing them hides a per-engine divergence", got)
		}
	})
	t.Run("triggers are ordered deterministically", func(t *testing.T) {
		pg := reg.invariants[0].ByEngine["postgres"]
		if pg[0].Name != "a_guard" || pg[1].Name != "z_guard" {
			t.Fatalf("postgres triggers = %+v, want sorted by name: a manifest whose order "+
				"follows map iteration reports two identical builds as different", pg)
		}
	})
	t.Run("the digest survives", func(t *testing.T) {
		pg := reg.invariants[0].ByEngine["postgres"]
		if pg[0].DefinitionSHA256 != "cafe" || pg[1].DefinitionSHA256 != "deadbeef" {
			t.Fatalf("digests = %q/%q, want cafe/deadbeef: two triggers can share a name and "+
				"differ in body, so the name alone is not the identity", pg[0].DefinitionSHA256, pg[1].DefinitionSHA256)
		}
	})
}
