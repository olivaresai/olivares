// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestFederationSettingsUpgradePreservesLegacyProvider(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			testFederationSettingsUpgradePreservesLegacyProvider(t, engine)
		})
	}
}

func testFederationSettingsUpgradePreservesLegacyProvider(t *testing.T, engine store.Engine) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	config := store.Config{Engine: engine, DSN: path, Debug: true}
	driver, ownerDSN := "sqlite", path
	if engine == store.EnginePostgres {
		pg := isolatedPGSplit(t)
		config.DSN, config.OwnerDSN, config.AdminDSN = pg.App, pg.Owner, pg.Admin
		driver, ownerDSN = "pgx", pg.Owner
	} else if err := initializedSQLiteCoreTemplate.copyInto(path); err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	var id model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		cfg, err := as.FederationConfigs().Create(ctx, model.FederationConfig{
			TargetTenantID: model.SystemTenantID, Protocol: "oidc", Status: model.StatusActive,
			OIDCIssuer: "https://idp.example", OIDCClientID: "legacy-client", OIDCClientSecretSealed: "sealed-fixture",
		})
		id = cfg.ID
		return err
	}); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the missing settings columns AND the pre-v21 tracking prefix.
	// v21 owns descriptor schema adoption; keeping it tracked would skip the
	// upgrade. Later records must go too, so the history remains contiguous.
	db, err := sql.Open(driver, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"display_name", "assurance_mapping"} {
		if _, err := db.ExecContext(ctx, "ALTER TABLE federation_configs DROP COLUMN "+column); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations_core WHERE version > 20"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		cfg, err := as.FederationConfigs().Get(ctx, id)
		if err == nil && (cfg.AssuranceMapping != nil || cfg.DisplayName != "" || cfg.TargetTenantID != model.SystemTenantID || cfg.Protocol != "oidc" || cfg.Status != model.StatusActive || cfg.OIDCClientID != "legacy-client" || cfg.OIDCIssuer != "https://idp.example" || cfg.OIDCClientSecretSealed != "sealed-fixture") {
			t.Fatalf("upgrade changed the legacy provider: %+v", cfg)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var recorded int
	if err := st.(*sqlStore).db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations_core WHERE version = 21 AND name = 'descriptor_schema'").Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("descriptor upgrade not recorded: count=%d err=%v", recorded, err)
	}
	want := &model.FederationAssuranceMapping{AMR: []string{}, ACR: []string{"urn:corp:mfa"}}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		cfg, err := as.FederationConfigs().Get(ctx, id)
		if err != nil {
			return err
		}
		cfg.DisplayName, cfg.AssuranceMapping = "Contoso", want
		_, err = as.FederationConfigs().Update(ctx, cfg)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, config, nil)
	if err != nil {
		t.Fatalf("reopen upgraded provider: %v", err)
	}
	st = reopened
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		cfg, err := as.FederationConfigs().Get(ctx, id)
		if err == nil && (cfg.DisplayName != "Contoso" || !reflect.DeepEqual(cfg.AssuranceMapping, want)) {
			t.Fatalf("operator settings lost empty/nil distinction: %+v", cfg)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
