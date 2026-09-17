// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
	"testing"
)

func f2aHistoricalRender(t *testing.T, engine store.Engine) string {
	t.Helper()
	dia, _ := dialect.New(engine)
	type entry struct {
		Version                   int
		Name                      string
		Stmts                     []string
		NonTransactional, HasExec bool
	}
	var plan []entry
	for _, m := range buildCoreMigrations(dia, coreDescriptors(), nil, nil) {
		if m.Version <= 9 {
			plan = append(plan, entry{m.Version, m.Name, m.Stmts, m.NonTransactional, m.Exec != nil})
		}
	}
	var lineage []string
	for _, obj := range lineageGuardObjects(dia) {
		lineage = append(lineage, obj.statement)
	}
	raw, err := json.Marshal(struct {
		Plan               []entry
		V7Control, Lineage []string
	}{plan, dia.DirectoryWriterControlStmts(), lineage})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// Values measured from unmodified ae19349 v9 source with the committed test adapter.
func TestUserAuthorityPreservesHistoricalRender(t *testing.T) {
	want := map[store.Engine]string{
		store.EngineSQLite:   "13add410f6e4483b465d4d9437d1743cb1b52bd2719786a404509e9fce6af171",
		store.EnginePostgres: "1dd7adeb2f951b1b293314095aebefecca0f0184f51b7379982b3fa753494fac",
	}
	for _, engine := range store.SupportedEngines() {
		if got := f2aHistoricalRender(t, engine); got != want[engine] {
			t.Fatalf("%s historical render changed: got%s want%s", engine, got, want[engine])
		}
	}
}
