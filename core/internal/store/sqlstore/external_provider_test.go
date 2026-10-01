// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestExternalProviderFreshSQLiteColumnsReconcile(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "fresh.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("sqlite dialect unavailable")
	}
	columns, err := dia.TableColumns(ctx, st.(*sqlStore).db, federationConfigDescriptor.Table)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"external_connector_ref", "external_connector_generation", "external_issuer"} {
		if !columns[name] {
			t.Errorf("fresh federation config lacks %s", name)
		}
	}
}

func TestExternalProviderColumnsUpgradeLegacyConfigWithoutAdoption(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("sqlite dialect unavailable")
	}
	for _, stmt := range dia.TenancyStmts() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	legacy := federationConfigDescriptor
	legacy.Fields = nil
	for _, field := range federationConfigDescriptor.Fields {
		if !strings.HasPrefix(field.Name, "external_") {
			legacy.Fields = append(legacy.Fields, field)
		}
	}
	for _, stmt := range dia.CreateTableStmts(legacy) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+dialect.ScopeTenantTable+"(tenant_id) VALUES(?)", model.SystemTenantID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO federation_configs(id, tenant_id, created_at, updated_at, version, target_tenant_id, alias, protocol, status, oidc_issuer) VALUES(?,?,?,?,1,?,'default','oidc','active','https://existing.example')",
		"legacy-provider", model.SystemTenantID.String(), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", model.SystemTenantID.String()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := reconcileColumns(ctx, db, dia, []model.EntityDescriptor{federationConfigDescriptor}); err != nil {
			t.Fatal(err)
		}
	}
	var protocol, issuer string
	var connector, externalIssuer sql.NullString
	var generation sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT protocol, oidc_issuer, external_connector_ref, external_connector_generation, external_issuer FROM federation_configs WHERE id = 'legacy-provider'").Scan(&protocol, &issuer, &connector, &generation, &externalIssuer); err != nil {
		t.Fatal(err)
	}
	if protocol != "oidc" || issuer != "https://existing.example" || connector.Valid || generation.Valid || externalIssuer.Valid {
		t.Fatalf("legacy config changed or acquired an external identity: %s %s %v %v %v", protocol, issuer, connector, generation, externalIssuer)
	}
	base := model.BaseFields{ID: "legacy-provider", TenantID: model.SystemTenantID}
	decoded, err := federationConfigCodec.Decode(base, model.Record{"protocol": protocol, "oidc_issuer": issuer, "external_connector_ref": nil, "external_connector_generation": nil, "external_issuer": nil})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Protocol != "oidc" || decoded.OIDCIssuer != issuer || decoded.ExternalConnectorRef != "" || decoded.ExternalConnectorGeneration != 0 || decoded.ExternalIssuer != "" {
		t.Fatalf("legacy decoder invented an external slot: %+v", decoded)
	}
	decoded.Protocol = "external"
	decoded.ExternalConnectorRef, decoded.ExternalConnectorGeneration, decoded.ExternalIssuer = "connector-revision-owner", 7, "urn:provider:immutable-owner"
	record, err := federationConfigCodec.Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE federation_configs SET protocol=?, external_connector_ref=?, external_connector_generation=?, external_issuer=? WHERE id = 'legacy-provider'", record["protocol"], record["external_connector_ref"], record["external_connector_generation"], record["external_issuer"]); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT protocol, external_connector_ref, external_connector_generation, external_issuer FROM federation_configs WHERE id = 'legacy-provider'").Scan(&protocol, &connector, &generation, &externalIssuer); err != nil {
		t.Fatal(err)
	}
	got, err := federationConfigCodec.Decode(base, model.Record{"protocol": protocol, "external_connector_ref": connector.String, "external_connector_generation": generation.Int64, "external_issuer": externalIssuer.String})
	if err != nil || got.ExternalConnectorRef != decoded.ExternalConnectorRef || got.ExternalConnectorGeneration != decoded.ExternalConnectorGeneration || got.ExternalIssuer != decoded.ExternalIssuer || got.Protocol != "external" {
		t.Fatalf("persisted opaque revision round-trip = %+v / %v", got, err)
	}
}

// This checks both actual schema renderers; it does not claim a PostgreSQL
// runtime upgrade. The live SQLite upgrade is exercised above.
func TestExternalProviderDescriptorUsesNullableColumnsOnBothDialects(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			dia, ok := dialect.New(engine)
			if !ok {
				t.Fatal("dialect unavailable")
			}
			ddl := strings.Join(dia.CreateTableStmts(federationConfigDescriptor), "\n")
			count := 0
			for _, field := range federationConfigDescriptor.Fields {
				if !strings.HasPrefix(field.Name, "external_") {
					continue
				}
				count++
				if !field.Nullable || !strings.Contains(ddl, field.Name+" "+dia.ColumnType(field.Kind, true)) || strings.Contains(dia.ColumnType(field.Kind, true), "NOT NULL") {
					t.Fatalf("external field %s cannot upgrade a populated store: %s", field.Name, ddl)
				}
			}
			if count != 3 {
				t.Fatalf("external descriptor fields = %d, want 3", count)
			}
		})
	}
}
