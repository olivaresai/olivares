// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// TestCompiledCoreMigrationVersionsExportsThePlan pins the export the `olivares` module reads
// to the compiled plan itself, on both engines: the same ordered versions the preflight
// derives, ending at the supported ceiling, with reserved v12 absent (ROOT-CONSTRUCTION-R5-1 §2).
func TestCompiledCoreMigrationVersionsExportsThePlan(t *testing.T) {
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13}
	for _, engine := range store.SupportedEngines() {
		got, err := CompiledCoreMigrationVersions(engine)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: exported plan = %v, want %v", engine, got, want)
		}
		dia, ok := dialect.New(engine)
		if !ok {
			t.Fatalf("%s: no dialect", engine)
		}
		internal := compiledCoreMigrationVersionOrder(dia)
		if len(internal) != len(got) {
			t.Fatalf("%s: export has %d versions, the compiled order %d", engine, len(got), len(internal))
		}
		for i := range internal {
			if int(internal[i]) != got[i] {
				t.Fatalf("%s: export %v differs from the compiled order %v", engine, got, internal)
			}
		}
		if got[len(got)-1] != coreSupportedMigrationVersion {
			t.Fatalf("%s: the plan ends at v%d, the supported ceiling is v%d", engine, got[len(got)-1], coreSupportedMigrationVersion)
		}
	}
	if _, err := CompiledCoreMigrationVersions("mysql"); err == nil {
		t.Fatal("an unsupported engine must be refused, not answered with a plan")
	}
}
