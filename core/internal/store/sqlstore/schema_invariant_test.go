// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRegistrySchemaInvariantsAreEngineSpecificAndCloseWithRegistry(t *testing.T) {
	r := newRegistry()
	decl := map[store.Engine][]store.SchemaTrigger{
		store.EnginePostgres: {
			{Name: "module_new_org", Table: "orgs"},
			{Name: "module_config_sticky", Table: "module_config"},
		},
		store.EngineSQLite: {
			{Name: "module_new_org", Table: "orgs"},
			{Name: "module_config_sticky_insert", Table: "module_config"},
			{Name: "module_config_sticky_update", Table: "module_config"},
			{Name: "module_config_sticky_delete", Table: "module_config"},
		},
	}
	if err := r.SchemaInvariants("module", decl); err != nil {
		t.Fatalf("SchemaInvariants: %v", err)
	}
	pg := r.schemaInvariants(store.EnginePostgres)
	sqlite := r.schemaInvariants(store.EngineSQLite)
	if len(pg) != 2 || len(sqlite) != 4 {
		t.Fatalf("engine invariant counts postgres=%d sqlite=%d", len(pg), len(sqlite))
	}
	r.closed = true
	if err := r.SchemaInvariants("late", decl); err == nil ||
		!strings.Contains(err.Error(), "registration is closed") {
		t.Fatalf("closed registry accepted invariants: %v", err)
	}
}

// bothEngines builds a well-formed declaration so each malformed case below can
// isolate ONE defect. Declaring a single engine would otherwise be rejected for the
// missing-engine reason, and every case would pass without exercising its own rule.
func bothEngines(sqlite, postgres []store.SchemaTrigger) map[store.Engine][]store.SchemaTrigger {
	return map[store.Engine][]store.SchemaTrigger{
		store.EngineSQLite:   sqlite,
		store.EnginePostgres: postgres,
	}
}

func TestRegistryRejectsMalformedSchemaInvariant(t *testing.T) {
	ok := []store.SchemaTrigger{{Name: "module_ok", Table: "orgs"}}
	for _, tc := range []struct {
		name string
		ns   string
		decl map[store.Engine][]store.SchemaTrigger
	}{
		{"bad namespace", "not-valid", bothEngines(ok, ok)},
		{"unknown engine", "module", map[store.Engine][]store.SchemaTrigger{
			store.EngineSQLite: ok, store.EnginePostgres: ok, "mysql": ok,
		}},
		{"bad trigger name", "module", bothEngines(
			[]store.SchemaTrigger{{Name: "not-valid", Table: "orgs"}}, ok)},
		{"bad table", "module", bothEngines(
			[]store.SchemaTrigger{{Name: "module_ok", Table: "not-valid"}}, ok)},
		{"empty set", "module", bothEngines(nil, ok)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := newRegistry().SchemaInvariants(tc.ns, tc.decl); err == nil {
				t.Fatal("invalid invariant declaration accepted")
			}
		})
	}
}

// TestRegistryAcceptsTheSameTriggerNameOnDifferentTables pins the identity rule the
// structured self-test key depends on.
//
// PostgreSQL only requires a trigger name to be unique PER TABLE. Rejecting a name
// globally was an invented restriction that both contradicted the lookup key and
// stopped a module from installing the same guard on two of its own tables — and it
// made the "same name, different table" hazard undeclarable, so no test could cover
// it. Identity is (table, name); only that pair may not repeat.
func TestRegistryAcceptsTheSameTriggerNameOnDifferentTables(t *testing.T) {
	shared := "module_no_truncate"
	decl := bothEngines(
		[]store.SchemaTrigger{
			{Name: shared, Table: "module_config"},
			{Name: shared, Table: "module_facts"},
		},
		[]store.SchemaTrigger{
			{Name: shared, Table: "module_config"},
			{Name: shared, Table: "module_facts"},
		})

	r := newRegistry()
	if err := r.SchemaInvariants("module", decl); err != nil {
		t.Fatalf("the same trigger name on two different tables was refused: %v", err)
	}
	if got := len(r.schemaInvariants(store.EngineSQLite)); got != 2 {
		t.Fatalf("registered %d SQLite invariants, want 2 — the pair collapsed", got)
	}

	// The pair itself must still be unique.
	dup := bothEngines(
		[]store.SchemaTrigger{
			{Name: shared, Table: "module_config"},
			{Name: shared, Table: "module_config"},
		},
		[]store.SchemaTrigger{{Name: shared, Table: "module_config"}})
	if err := newRegistry().SchemaInvariants("module", dup); err == nil {
		t.Fatal("the same trigger declared twice on the SAME table was accepted")
	}
}

// TestRegistryRejectsSchemaInvariantMissingAnEngine is the P1-4 RED.
//
// The boot self-test looks up invariants for the ACTIVE engine only. A declaration
// that omits an engine therefore yields an EMPTY required set on that engine, the
// self-test returns success without checking a single trigger, and the module still
// marks its rollout invariant declared and reports healthy — the exact silent hole
// the invariant exists to prevent, reachable by a refactor or a typo rather than by
// an attack. An under-declared map must be refused at registration.
func TestRegistryRejectsSchemaInvariantMissingAnEngine(t *testing.T) {
	ok := []store.SchemaTrigger{{Name: "module_ok", Table: "orgs"}}
	for _, declared := range store.SupportedEngines() {
		t.Run("only "+string(declared), func(t *testing.T) {
			r := newRegistry()
			err := r.SchemaInvariants("module", map[store.Engine][]store.SchemaTrigger{declared: ok})
			if err == nil {
				var omitted store.Engine
				for _, e := range store.SupportedEngines() {
					if e != declared {
						omitted = e
					}
				}
				t.Fatalf("a declaration covering only %q was accepted; on %q the self-test would "+
					"verify nothing and still report success", declared, omitted)
			}
			if !strings.Contains(err.Error(), "every supported engine") {
				t.Fatalf("error %v does not explain the rule", err)
			}
		})
	}
}

func TestRegistryValidatesMutableTenantRetentionOnlyAtClose(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	desc := model.EntityDescriptor{
		Kind: "module.fact", Table: "module_fact", RetainOnTenantDrop: true,
	}
	decl := bothEngines(
		[]store.SchemaTrigger{{
			Name: "module_fact_no_delete", Table: "module_fact", DefinitionSHA256: digest,
		}},
		[]store.SchemaTrigger{{
			Name: "module_fact_no_delete", Table: "module_fact", DefinitionSHA256: digest,
		}},
	)

	r := newRegistry()
	// Register must not require an invariant that a module is still allowed to
	// declare later in the same registration hook.
	if err := r.Register(desc); err != nil {
		t.Fatalf("Register before SchemaInvariants: %v", err)
	}
	if err := r.SchemaInvariants("module", decl); err != nil {
		t.Fatalf("SchemaInvariants after Register: %v", err)
	}
	r.closed = true
	if err := r.validateRetainedDescriptors(); err != nil {
		t.Fatalf("closed registry rejected fully guarded retained descriptor: %v", err)
	}
}

func TestOpenRunsMutableTenantRetentionValidationAtRegistryClose(t *testing.T) {
	_, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:",
	}, func(reg store.ExtensionRegistry) error {
		return reg.Register(model.EntityDescriptor{
			Kind: "module.fact", Table: "module_fact", RetainOnTenantDrop: true,
		})
	})
	if !errors.Is(err, store.ErrInvalidDescriptor) ||
		!strings.Contains(err.Error(), "retained entities") {
		t.Fatalf("Open error = %v, want close-time retained-entity refusal", err)
	}
}

func TestRegistryRejectsUnprovenMutableTenantRetentionAtClose(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	desc := model.EntityDescriptor{
		Kind: "module.fact", Table: "module_fact", RetainOnTenantDrop: true,
	}
	valid := store.SchemaTrigger{
		Name: "module_fact_no_delete", Table: "module_fact", DefinitionSHA256: digest,
	}

	for _, tc := range []struct {
		name       string
		namespace  string
		sqlite     store.SchemaTrigger
		postgres   store.SchemaTrigger
		invariants bool
		want       string
	}{
		{name: "no invariants", want: "has no schema invariants"},
		{
			name: "foreign namespace", namespace: "other", sqlite: valid, postgres: valid,
			invariants: true, want: "has no schema invariants",
		},
		{
			name: "wrong trigger name", namespace: "module",
			sqlite: store.SchemaTrigger{
				Name: "module_fact_delete_guard", Table: "module_fact", DefinitionSHA256: digest,
			},
			postgres: valid, invariants: true, want: "requires trigger",
		},
		{
			name: "wrong table", namespace: "module",
			sqlite: store.SchemaTrigger{
				Name: "module_fact_no_delete", Table: "module_other", DefinitionSHA256: digest,
			},
			postgres: valid, invariants: true, want: "requires trigger",
		},
		{
			name: "empty digest", namespace: "module",
			sqlite: store.SchemaTrigger{
				Name: "module_fact_no_delete", Table: "module_fact",
			},
			postgres: valid, invariants: true, want: "full lowercase SHA-256",
		},
		{
			name: "short digest", namespace: "module",
			sqlite: store.SchemaTrigger{
				Name: "module_fact_no_delete", Table: "module_fact", DefinitionSHA256: "aa",
			},
			postgres: valid, invariants: true, want: "full lowercase SHA-256",
		},
		{
			name: "uppercase digest", namespace: "module",
			sqlite: store.SchemaTrigger{
				Name: "module_fact_no_delete", Table: "module_fact",
				DefinitionSHA256: strings.Repeat("A", 64),
			},
			postgres: valid, invariants: true, want: "full lowercase SHA-256",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry()
			if err := r.Register(desc); err != nil {
				t.Fatal(err)
			}
			if tc.invariants {
				if err := r.SchemaInvariants(tc.namespace, bothEngines(
					[]store.SchemaTrigger{tc.sqlite}, []store.SchemaTrigger{tc.postgres},
				)); err != nil {
					t.Fatalf("declare malformed lifecycle evidence: %v", err)
				}
			}
			r.closed = true
			err := r.validateRetainedDescriptors()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("close error = %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestRegistryAppendOnlyTenantRetentionUsesEngineGuard(t *testing.T) {
	r := newRegistry()
	if err := r.Register(model.EntityDescriptor{
		Kind: "module.evidence", Table: "module_evidence",
		AppendOnly: true, RetainOnTenantDrop: true,
	}); err != nil {
		t.Fatal(err)
	}
	r.closed = true
	if err := r.validateRetainedDescriptors(); err != nil {
		t.Fatalf("append-only retention unexpectedly required a module no-delete invariant: %v", err)
	}
}
