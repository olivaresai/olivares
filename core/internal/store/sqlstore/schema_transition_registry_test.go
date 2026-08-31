// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestRegistryValidatesSchemaTriggerTransitionChains(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	c := strings.Repeat("c", 64)
	valid := store.SchemaTrigger{
		Name: "module_guard", Table: "module_fact", DefinitionSHA256: c,
		Transitions: []store.SchemaTriggerTransition{
			{MigrationVersion: 2, PreviousDefinitionSHA256: a},
			{MigrationVersion: 5, PreviousDefinitionSHA256: b},
		},
	}
	validPostgres := withPostgresTransitionIdentities(valid)
	if err := newRegistry().SchemaInvariants("module", bothEngines(
		[]store.SchemaTrigger{valid}, []store.SchemaTrigger{validPostgres},
	)); err != nil {
		t.Fatalf("valid transition chain refused: %v", err)
	}

	for _, tc := range []struct {
		name    string
		trigger store.SchemaTrigger
		want    string
	}{
		{
			name: "non-positive version",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: b,
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 0, PreviousDefinitionSHA256: a,
				}},
			},
			want: "non-positive migration version",
		},
		{
			name: "non-canonical previous digest",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: b,
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 2, PreviousDefinitionSHA256: strings.Repeat("A", 64),
				}},
			},
			want: "lowercase SHA-256 previous",
		},
		{
			name: "non-canonical current digest",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: "",
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 2, PreviousDefinitionSHA256: a,
				}},
			},
			want: "lowercase SHA-256 current",
		},
		{
			name: "duplicate version",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: c,
				Transitions: []store.SchemaTriggerTransition{
					{MigrationVersion: 2, PreviousDefinitionSHA256: a},
					{MigrationVersion: 2, PreviousDefinitionSHA256: b},
				},
			},
			want: "strictly increasing and unique",
		},
		{
			name: "descending version",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: c,
				Transitions: []store.SchemaTriggerTransition{
					{MigrationVersion: 4, PreviousDefinitionSHA256: a},
					{MigrationVersion: 3, PreviousDefinitionSHA256: b},
				},
			},
			want: "strictly increasing and unique",
		},
		{
			name: "unchanged intermediate digest",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: b,
				Transitions: []store.SchemaTriggerTransition{
					{MigrationVersion: 2, PreviousDefinitionSHA256: a},
					{MigrationVersion: 3, PreviousDefinitionSHA256: a},
				},
			},
			want: "identical previous and next",
		},
		{
			name: "unchanged final digest",
			trigger: store.SchemaTrigger{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: a,
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 2, PreviousDefinitionSHA256: a,
				}},
			},
			want: "identical previous and next",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newRegistry().SchemaInvariants("module", bothEngines(
				[]store.SchemaTrigger{tc.trigger},
				[]store.SchemaTrigger{withPostgresTransitionIdentities(tc.trigger)},
			))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("registration error = %v, want refusal containing %q", err, tc.want)
			}
		})
	}

	t.Run("PostgreSQL identity is required", func(t *testing.T) {
		err := newRegistry().SchemaInvariants("module", bothEngines(
			[]store.SchemaTrigger{valid}, []store.SchemaTrigger{valid},
		))
		if err == nil || !strings.Contains(err.Error(), "requires a PostgreSQL function identity change") {
			t.Fatalf("registration error = %v, want missing PostgreSQL identity refusal", err)
		}
	})

	t.Run("PostgreSQL identity must change", func(t *testing.T) {
		postgres := valid
		postgres.Transitions = []store.SchemaTriggerTransition{{
			MigrationVersion: 2, PreviousDefinitionSHA256: a,
			PostgresFunctionIdentity: &store.SchemaTriggerFunctionIdentityTransition{
				PreviousName: "same_fn", NextName: "same_fn",
			},
		}}
		postgres.DefinitionSHA256 = b
		err := newRegistry().SchemaInvariants("module", bothEngines(
			[]store.SchemaTrigger{{
				Name: "module_guard", Table: "module_fact", DefinitionSHA256: b,
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 2, PreviousDefinitionSHA256: a,
				}},
			}},
			[]store.SchemaTrigger{postgres},
		))
		if err == nil || !strings.Contains(err.Error(), "must change PostgreSQL function identity") {
			t.Fatalf("registration error = %v, want same-identity refusal", err)
		}
	})

	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "longer than pg catalog name", value: strings.Repeat("x", 64)},
		{name: "embedded NUL", value: "function\x00name"},
		{name: "invalid UTF-8", value: string([]byte{'f', 0xff, 'n'})},
	} {
		for _, field := range []string{"previous", "next"} {
			t.Run("PostgreSQL rejects "+tc.name+" "+field+" identity", func(t *testing.T) {
				postgres := cloneSchemaTrigger(validPostgres)
				identity := postgres.Transitions[0].PostgresFunctionIdentity
				if field == "previous" {
					identity.PreviousName = tc.value
				} else {
					identity.NextName = tc.value
				}
				err := newRegistry().SchemaInvariants("module", bothEngines(
					[]store.SchemaTrigger{valid}, []store.SchemaTrigger{postgres},
				))
				want := "invalid " + field + " PostgreSQL function name"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("registration error = %v, want refusal containing %q", err, want)
				}
			})
		}
	}

	t.Run("PostgreSQL identity history must be continuous", func(t *testing.T) {
		postgres := validPostgres
		postgres.Transitions = append([]store.SchemaTriggerTransition(nil), validPostgres.Transitions...)
		firstIdentity := *validPostgres.Transitions[0].PostgresFunctionIdentity
		secondIdentity := *validPostgres.Transitions[1].PostgresFunctionIdentity
		secondIdentity.PreviousName = "unrelated_function"
		postgres.Transitions[0].PostgresFunctionIdentity = &firstIdentity
		postgres.Transitions[1].PostgresFunctionIdentity = &secondIdentity
		err := newRegistry().SchemaInvariants("module", bothEngines(
			[]store.SchemaTrigger{valid}, []store.SchemaTrigger{postgres},
		))
		if err == nil || !strings.Contains(err.Error(), "preceding transition ends") {
			t.Fatalf("registration error = %v, want discontinuous-identity refusal", err)
		}
	})

	t.Run("SQLite rejects PostgreSQL identity metadata", func(t *testing.T) {
		sqlite := withPostgresTransitionIdentities(store.SchemaTrigger{
			Name: "module_guard", Table: "module_fact", DefinitionSHA256: b,
			Transitions: []store.SchemaTriggerTransition{{
				MigrationVersion: 2, PreviousDefinitionSHA256: a,
			}},
		})
		postgres := withPostgresTransitionIdentities(store.SchemaTrigger{
			Name: "module_guard", Table: "module_fact", DefinitionSHA256: b,
			Transitions: []store.SchemaTriggerTransition{{
				MigrationVersion: 2, PreviousDefinitionSHA256: a,
			}},
		})
		err := newRegistry().SchemaInvariants("module", bothEngines(
			[]store.SchemaTrigger{sqlite}, []store.SchemaTrigger{postgres},
		))
		if err == nil || !strings.Contains(err.Error(), "for SQLite") {
			t.Fatalf("registration error = %v, want SQLite identity-metadata refusal", err)
		}
	})
}

func TestRegistryDeepCopiesSchemaTriggerTransitions(t *testing.T) {
	previous := strings.Repeat("a", 64)
	current := strings.Repeat("b", 64)
	transitions := []store.SchemaTriggerTransition{{
		MigrationVersion: 2, PreviousDefinitionSHA256: previous,
	}}
	trigger := store.SchemaTrigger{
		Name: "module_guard", Table: "module_fact", DefinitionSHA256: current,
		Transitions: transitions,
	}
	postgresTrigger := withPostgresTransitionIdentities(trigger)
	identity := postgresTrigger.Transitions[0].PostgresFunctionIdentity
	r := newRegistry()
	if err := r.SchemaInvariants("module", bothEngines(
		[]store.SchemaTrigger{trigger}, []store.SchemaTrigger{postgresTrigger},
	)); err != nil {
		t.Fatal(err)
	}

	transitions[0].MigrationVersion = 999
	trigger.Transitions[0].PreviousDefinitionSHA256 = strings.Repeat("f", 64)
	got := r.schemaInvariants(store.EngineSQLite)[0].Transitions[0]
	if got.MigrationVersion != 2 || got.PreviousDefinitionSHA256 != previous {
		t.Fatalf("stored transition was aliased to caller memory: %+v", got)
	}
	projected := r.schemaInvariants(store.EngineSQLite)
	projected[0].Transitions[0].MigrationVersion = 777
	if gotAgain := r.schemaInvariants(store.EngineSQLite)[0].Transitions[0].MigrationVersion; gotAgain != 2 {
		t.Fatalf("stored transition was aliased to accessor result: version %d, want 2", gotAgain)
	}

	identity.NextName = "mutated_by_caller"
	pgProjected := r.schemaInvariants(store.EnginePostgres)
	gotIdentity := pgProjected[0].Transitions[0].PostgresFunctionIdentity
	if gotIdentity == nil || gotIdentity.NextName != "function_1" {
		t.Fatalf("stored PostgreSQL identity was aliased to caller memory: %+v", gotIdentity)
	}
	gotIdentity.NextName = "mutated_by_accessor"
	gotAgainIdentity := r.schemaInvariants(store.EnginePostgres)[0].Transitions[0].PostgresFunctionIdentity
	if gotAgainIdentity == nil || gotAgainIdentity.NextName != "function_1" {
		t.Fatalf("stored PostgreSQL identity was aliased to accessor result: %+v", gotAgainIdentity)
	}
}

func withPostgresTransitionIdentities(trigger store.SchemaTrigger) store.SchemaTrigger {
	cloned := trigger
	cloned.Transitions = append([]store.SchemaTriggerTransition(nil), trigger.Transitions...)
	for i := range cloned.Transitions {
		cloned.Transitions[i].PostgresFunctionIdentity = &store.SchemaTriggerFunctionIdentityTransition{
			PreviousName: "function_" + fmt.Sprint(i),
			NextName:     "function_" + fmt.Sprint(i+1),
		}
	}
	return cloned
}
