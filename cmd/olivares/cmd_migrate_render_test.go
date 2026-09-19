// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/core/store"
)

// `migrate status` is the table whose baseline row claimed it could not move: the
// reason recorded for it said printMigrationStatus was an open-vs-enterprise
// parity ORACLE "whose bytes are compared and not read", carrying a render-exempt
// marker that said so.
//
// None of that was true of the tree. It has ONE caller, no test or script compares
// its bytes, nothing outside the file mentions its header, and the marker said
// something else ("this IS the text branch"). So it moved, and these are the tests
// a reader can check instead of a reason they cannot.

func migrationStatusFixture() []store.MigrationRecord {
	return []store.MigrationRecord{
		{Table: "schema_migrations_core", Version: 41, Name: "work_item_lease", Phase: "expand",
			AppliedAt: "2026-09-18T09:00:00Z"},
		{Table: "schema_migrations_core", Version: 42, Name: "drop_legacy_lease", Phase: "contract",
			AppliedAt: "2026-09-18T09:00:01Z"},
		// No phase: a tracking table written before the column existed. It counts
		// as an expand, which is what the engine wrote.
		{Table: "schema_migrations_mod_finops", Version: 3, Name: "backfill_rates",
			AppliedAt: "", Reverted: true},
	}
}

const migrationStatusPlainGolden = `TRACKING TABLE                VERSION  PHASE     STATE     APPLIED AT            NAME
schema_migrations_core        41       expand    applied   2026-09-18T09:00:00Z  work_item_lease
schema_migrations_core        42       contract  applied   2026-09-18T09:00:01Z  drop_legacy_lease
schema_migrations_mod_finops  3        expand    reverted  -                     backfill_rates

3 migration(s): 2 expand, 1 contract, 1 reverted.
Note: a CONTRACT migration is a destructive cleanup. Rolling the BINARY back to a
release from before a contract is unsafe — the older code may depend on what the
contract removed. See docs/UPGRADE-AND-ROLLBACK.md before rolling back across one.
`

func TestMigrationStatusPlainGolden(t *testing.T) {
	got := plainOf(func(r *termrender.Renderer) { drawMigrationStatus(r, migrationStatusFixture()) })
	if got != migrationStatusPlainGolden {
		t.Errorf("the migration status plain form changed.\n got:\n%s\nwant:\n%s", got, migrationStatusPlainGolden)
	}
}

// TestMigrationStatusRichIsPlainPlusColour: a contract row is coloured warn, which
// is the same judgement the caution below the table makes in words.
func TestMigrationStatusRichIsPlainPlusColour(t *testing.T) {
	assertRichIsPlainPlusColour(t, "migrate status", 200, func(r *termrender.Renderer) {
		drawMigrationStatus(r, migrationStatusFixture())
	})
}

// TestMigrationStatusKeepsEveryByteOnANarrowTerminal is the half the old tabwriter
// could not do. Six columns do not fit an 80-column terminal, so the renderer
// gives records instead of a smear — and no tracking-table name is cut, which is
// the property this whole presentation layer exists for.
func TestMigrationStatusKeepsEveryByteOnANarrowTerminal(t *testing.T) {
	var b strings.Builder
	off := false
	drawMigrationStatus(termrender.New(&b, termrender.Options{Color: &off, Width: 80}), migrationStatusFixture())
	got := b.String()

	if strings.Contains(got, "TRACKING TABLE                VERSION") {
		t.Fatalf("the six-column row was drawn as columns at 80, so it wraps into itself:\n%s", got)
	}
	if !strings.Contains(got, "TRACKING TABLE  schema_migrations_mod_finops") {
		t.Errorf("no record block for the third row:\n%s", got)
	}
	for _, name := range []string{"schema_migrations_mod_finops", "2026-09-18T09:00:01Z", "drop_legacy_lease"} {
		if !strings.Contains(got, name) {
			t.Errorf("%q was lost or truncated at 80 columns:\n%s", name, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, "…") || strings.HasSuffix(line, "...") {
			t.Errorf("a line was truncated: %q", line)
		}
	}
}

// TestMigrationStatusEmptyIsNotSilence: an untouched database prints a sentence,
// and no tally line about zero migrations.
func TestMigrationStatusEmptyIsNotSilence(t *testing.T) {
	got := plainOf(func(r *termrender.Renderer) { drawMigrationStatus(r, nil) })
	want := "no schema-migration tracking tables found (the engine has not migrated this database yet)\n"
	if got != want {
		t.Errorf("empty migration status = %q, want %q", got, want)
	}
}

// TestMigrationStatusCountsReadPhaseAndStateTheSameWayTheTableDoes: the tally and
// the rows come from the same two functions, so they cannot disagree about what a
// missing phase or a reverted row is.
func TestMigrationStatusCountsReadPhaseAndStateTheSameWayTheTableDoes(t *testing.T) {
	recs := migrationStatusFixture()
	expand, contract, reverted := migrationStatusCounts(recs)
	if expand != 2 || contract != 1 || reverted != 1 {
		t.Fatalf("counts = %d expand, %d contract, %d reverted", expand, contract, reverted)
	}
	table := migrationStatusTable(recs)
	gotExpand, gotContract, gotReverted := 0, 0, 0
	for _, row := range table.Rows {
		switch row[2] {
		case "expand":
			gotExpand++
		case "contract":
			gotContract++
		}
		if row[3] == "reverted" {
			gotReverted++
		}
	}
	if gotExpand != expand || gotContract != contract || gotReverted != reverted {
		t.Errorf("the table says %d/%d/%d and the tally says %d/%d/%d",
			gotExpand, gotContract, gotReverted, expand, contract, reverted)
	}
}
