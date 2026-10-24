// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The Business X.Y-1 -> Community X.Y -> Business X.Y crossing (#1883, #2741): Community
// starts on a directory a Business build wrote, keeps every row, and Business reopens it.
func TestCommunityOpensBusinessWrittenDirectory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "business.db")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: path}
	business, err := Open(ctx, cfg, registerCommercialGuardFixture)
	if err != nil {
		t.Fatalf("Business first open: %.500s", err)
	}
	tenant := provisionTenant(t, business, "crossing")
	var id model.ID
	if err := business.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(widgetDescriptor.Kind)
		if err != nil {
			return err
		}
		record, err := repo.Create(ctx, model.Record{"label": "kept through crossing", "count": int64(7)})
		id = model.ID(record.String("id"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	for _, edition := range []struct {
		name     string
		register func(store.ExtensionRegistry) error
	}{
		{"Community", registerWidget},
		{"Business again", registerCommercialGuardFixture},
		{"Community again", registerWidget},
	} {
		s, err := Open(ctx, cfg, edition.register)
		if err != nil {
			t.Fatalf("%s open on a Business-written directory: %.500s", edition.name, err)
		}
		if err := s.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(widgetDescriptor.Kind)
			if err != nil {
				return err
			}
			record, err := repo.Get(ctx, id)
			if err != nil {
				return err
			}
			if record.String("label") != "kept through crossing" {
				t.Fatalf("%s: stored data changed", edition.name)
			}
			report, err := sc.Audit().Verify(ctx, 1)
			if err != nil {
				return err
			}
			if !report.OK {
				t.Fatalf("%s: audit continuity lost: %+v", edition.name, report)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Only a history that records the full Business set widens the census, and the widened
// census still has to verify as one exact compiled history: an unknown relation, an
// incomplete completion and a plain Community ledger keep their refusals (#2741).
func TestGuardCensusForBusinessWrittenHistory(t *testing.T) {
	sourceTables := append([]string{"audit_events", "set_seen_jtis", guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}, guardAccessEvidenceTables[:]...)
	business := append(append([]string(nil), sourceTables...), commercialGuardFixtureTables...)
	seed := func(t *testing.T, tables []string) (*sql.DB, dialect.Dialect) {
		t.Helper()
		manifest, err := buildGuardManifest(tables)
		if err != nil {
			t.Fatal(err)
		}
		variants, err := guardBootstrapReceiptVariants(manifest)
		if err != nil {
			t.Fatal(err)
		}
		db, dia := guardEmptyEditionSQLiteDB(t)
		for _, variant := range variants {
			if variant.State == guardEditionReceiptsCurrentV9Completed && variant.Path == "5" {
				seedGuardEditionVariant(t, db, dia, manifest, variant.Path, variant.Receipts)
				return db, dia
			}
		}
		t.Fatal("missing source variant")
		return nil, nil
	}
	ctx := context.Background()

	t.Run("full Business set widens the census and verifies", func(t *testing.T) {
		db, dia := seed(t, business)
		census, _, _, err := bootGuardCensus(ctx, db, dia, sourceTables)
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string(nil), business...)
		sort.Strings(want)
		if !slices.Equal(census, want) {
			t.Fatalf("census = %v, want %v", census, want)
		}
		before := accessEvidenceReadDurableCensus(t, db)
		if _, err := runGuardAdmissionUnits(ctx, db, dia, census, "", false, nil); err != nil {
			t.Fatalf("Community start on the Business history: %.500s", err)
		}
		accessEvidenceAssertDurableCensusUnchanged(t, "Community start on Business history", before, accessEvidenceReadDurableCensus(t, db))
	})

	t.Run("a Community ledger keeps its own census", func(t *testing.T) {
		db, dia := seed(t, sourceTables)
		census, _, _, err := bootGuardCensus(ctx, db, dia, sourceTables)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(census, sourceTables) {
			t.Fatalf("census = %v, want the registered %v", census, sourceTables)
		}
	})

	t.Run("an empty database keeps its own census", func(t *testing.T) {
		db, dia := guardEmptyEditionSQLiteDB(t)
		census, _, _, err := bootGuardCensus(ctx, db, dia, sourceTables)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(census, sourceTables) {
			t.Fatalf("census = %v, want the registered %v", census, sourceTables)
		}
	})

	t.Run("an unknown relation in the history still refuses", func(t *testing.T) {
		db, dia := seed(t, append(append([]string(nil), business...), "unknown_evidence"))
		census, _, _, err := bootGuardCensus(ctx, db, dia, sourceTables)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(census, sourceTables) {
			t.Fatalf("an unknown history widened the census: %v, want the registered %v", census, sourceTables)
		}
		before := accessEvidenceReadDurableCensus(t, db)
		_, err = runGuardAdmissionUnits(ctx, db, dia, census, "", false, nil)
		if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
			t.Fatalf("unknown Business history admitted or refused for another reason: %v", err)
		}
		accessEvidenceAssertDurableCensusUnchanged(t, "refused unknown history", before, accessEvidenceReadDurableCensus(t, db))
	})

	t.Run("a receipt that fails its own digest keeps the registry census", func(t *testing.T) {
		db, dia := seed(t, business)
		// The ledger is append-only; tamper with it the way someone with file access would.
		triggers, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'trigger'")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for triggers.Next() {
			var name string
			if err := triggers.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := triggers.Close(); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if _, err := db.ExecContext(ctx, `DROP TRIGGER "`+name+`"`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ExecContext(ctx, dia.Rebind("UPDATE "+guardOnly(dia)+guardReceiptsTable+" SET code_sha256 = ? WHERE event_ordinal = (SELECT MIN(event_ordinal) FROM "+guardOnly(dia)+guardReceiptsTable+")"), bytes.Repeat([]byte{7}, 32)); err != nil {
			t.Fatal(err)
		}
		census, _, _, err := bootGuardCensus(ctx, db, dia, sourceTables)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(census, sourceTables) {
			t.Fatalf("a tampered ledger widened the census: %v, want the registered %v", census, sourceTables)
		}
	})

	t.Run("a partial Business set keeps its own census", func(t *testing.T) {
		for _, n := range []int{1, 2} {
			db, dia := seed(t, append(append([]string(nil), sourceTables...), commercialGuardFixtureTables[:n]...))
			census, _, _, err := bootGuardCensus(ctx, db, dia, sourceTables)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(census, sourceTables) {
				t.Fatalf("%d of 3 Business relations: census = %v, want the registered %v", n, census, sourceTables)
			}
		}
	})
}

// A Business directory that also holds a relation no Community compiles is not a known
// history: Community refuses it at admission, and the same directory still opens in the
// build that wrote it (#2741: unknown history refuses).
func TestCommunityRefusesBusinessDirectoryWithUnknownRelation(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "business.db")}
	writer := func(reg store.ExtensionRegistry) error {
		if err := registerCommercialGuardFixture(reg); err != nil {
			return err
		}
		return reg.Register(model.EntityDescriptor{Kind: "unknown.evidence", Table: "unknown_evidence", AppendOnly: true})
	}
	s, err := Open(ctx, cfg, writer)
	if err != nil {
		t.Fatalf("writer first open: %.500s", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if c, err := Open(ctx, cfg, registerWidget); err == nil {
		_ = c.Close()
		t.Fatal("Community admitted a history with a relation no Community compiles")
	} else if !strings.Contains(err.Error(), "bootstrap history is not an exact compiled") {
		t.Fatalf("unexpected refusal: %.500s", err)
	}
	s, err = Open(ctx, cfg, writer)
	if err != nil {
		t.Fatalf("writer reopen after the refusal: %.500s", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// The other order: a directory Community created and Business then sealed carries receipts
// bound to both censuses, and Community must still reopen it (Community -> Business -> Community).
func TestCommunityReopensDirectoryBusinessSealed(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "community.db")}
	for _, edition := range []struct {
		name     string
		register func(store.ExtensionRegistry) error
	}{
		{"Community", registerWidget},
		{"Business", registerCommercialGuardFixture},
		{"Community again", registerWidget},
		{"Business again", registerCommercialGuardFixture},
	} {
		s, err := Open(ctx, cfg, edition.register)
		if err != nil {
			t.Fatalf("%s open: %.500s", edition.name, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
