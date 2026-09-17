// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The K3 ChannelGrant catalog index is declared on the entity descriptor, not
// in a migration. These tests prove the two effects that declaration must have
// on BOTH engines: a fresh estate creates the index with its table, and an
// existing estate that predates the declaration receives it on the next open
// through the schema reconciler (CREATE INDEX IF NOT EXISTS), while every
// prior migration and row is left as it was.

var distinctProjectionCatalogIndex = model.IndexSpec{
	Name: "dpt_grant_catalog_upgrade",
	Columns: []string{
		model.ColTenantID, "workspace_id", "subject_kind", "subject_ref", "state",
		"can_read", "channel_id", "expires_at",
	},
}

func distinctProjectionDescriptorWithoutCatalogIndex() model.EntityDescriptor {
	desc := distinctProjectionEntity
	desc.Kind = "dpt.grant_upgrade"
	desc.Table = "dpt_grant_upgrade"
	desc.Indexes = nil
	return desc
}

func distinctProjectionDescriptorWithCatalogIndex() model.EntityDescriptor {
	desc := distinctProjectionDescriptorWithoutCatalogIndex()
	desc.Indexes = []model.IndexSpec{distinctProjectionCatalogIndex}
	return desc
}

func indexNamesOf(t *testing.T, st store.Store, engine store.Engine, table string) []string {
	t.Helper()
	s, ok := st.(*sqlStore)
	if !ok {
		t.Fatalf("store type = %T", st)
	}
	var query string
	switch engine {
	case store.EnginePostgres:
		query = "SELECT indexname FROM pg_indexes WHERE tablename = $1 ORDER BY indexname"
	default:
		query = "SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? ORDER BY name"
	}
	rows, err := s.db.QueryContext(context.Background(), query, table)
	if err != nil {
		t.Fatalf("list indexes of %s: %v", table, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		names = append(names, name)
	}
	return names
}

func hasIndex(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// runCatalogIndexUpgradeContract opens an estate WITHOUT the catalog index,
// seeds a row, closes it, reopens it WITH the declaration and asserts the index
// now exists, the row survived, and a fresh estate with the declaration carries
// the index from its first open.
func runCatalogIndexUpgradeContract(t *testing.T, engine store.Engine, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	old, err := Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		return reg.Register(distinctProjectionDescriptorWithoutCatalogIndex())
	})
	if err != nil {
		t.Fatalf("open pre-declaration estate: %v", err)
	}
	tenant := provisionTenant(t, old, "dpt-upgrade")
	defaultWS, _ := distinctProjectionWorkspaces(t, old, tenant)
	if err := old.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("dpt.grant_upgrade")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"workspace_id": defaultWS.String(), "channel_id": "00000000-0000-7000-8000-000000000001",
			"subject_kind": "user", "subject_ref": "u1", "state": "active", "can_read": true,
		})
		return err
	}); err != nil {
		t.Fatalf("seed pre-declaration row: %v", err)
	}
	if names := indexNamesOf(t, old, engine, "dpt_grant_upgrade"); hasIndex(names, distinctProjectionCatalogIndex.Name) {
		t.Fatalf("pre-declaration estate already carries %s: %v", distinctProjectionCatalogIndex.Name, names)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close pre-declaration estate: %v", err)
	}

	upgraded, err := Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		return reg.Register(distinctProjectionDescriptorWithCatalogIndex())
	})
	if err != nil {
		t.Fatalf("reopen with the catalog index declared: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	names := indexNamesOf(t, upgraded, engine, "dpt_grant_upgrade")
	if !hasIndex(names, distinctProjectionCatalogIndex.Name) {
		t.Fatalf("upgrade did not create %s on the existing table; indexes: %v", distinctProjectionCatalogIndex.Name, names)
	}
	var survived int
	if err := upgraded.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("dpt.grant_upgrade")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{})
		survived = len(rows)
		return err
	}); err != nil {
		t.Fatalf("read after upgrade: %v", err)
	}
	if survived != 1 {
		t.Fatalf("rows after upgrade = %d, want the pre-declaration row", survived)
	}
	// Idempotent: a third open changes nothing.
	if err := upgraded.Close(); err != nil {
		t.Fatalf("close upgraded estate: %v", err)
	}
	again, err := Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		return reg.Register(distinctProjectionDescriptorWithCatalogIndex())
	})
	if err != nil {
		t.Fatalf("reopen upgraded estate: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if names := indexNamesOf(t, again, engine, "dpt_grant_upgrade"); !hasIndex(names, distinctProjectionCatalogIndex.Name) {
		t.Fatalf("index lost on reopen: %v", names)
	}
	t.Logf("%s: indexes on dpt_grant_upgrade after upgrade: %s", engine, strings.Join(names, ", "))
}

func TestCatalogIndexFreshAndUpgradeSQLite(t *testing.T) {
	dir := t.TempDir()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dir + "/upgrade.db", Debug: true}
	runCatalogIndexUpgradeContract(t, store.EngineSQLite, cfg)
	fresh, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		registerDistinctProjectionEntity)
	if err != nil {
		t.Fatalf("open fresh estate: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if names := indexNamesOf(t, fresh, store.EngineSQLite, distinctProjectionEntity.Table); !hasIndex(names, "dpt_grant_catalog") {
		t.Fatalf("fresh estate lacks the declared catalog index: %v", names)
	}
}

func TestCatalogIndexFreshAndUpgradePostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	runCatalogIndexUpgradeContract(t, store.EnginePostgres, cfg)
}
