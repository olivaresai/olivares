// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuthenticationFreshnessSchemaAndAuthority(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, cfg, users, tenants := f2aFreshTarget(t, engine)
			var legacy model.AuthSession
			until := model.NewTimestamp(time.Now().Add(time.Hour))
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				var err error
				legacy, err = as.Sessions().Create(ctx, model.AuthSession{UserID: users[0].ID,
					Selector: "freshness-legacy", SecretHash: []byte("test hash"), ExpiresAt: until,
					AAL: 3, AMR: []string{"piv"}, AALExpiresAt: &until})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			freshColumns, err := s.dia.TableColumns(ctx, s.db, "auth_sessions")
			if err != nil || !freshColumns["aal_authenticated_at"] {
				t.Fatalf("fresh schema missing witness: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			driver, dsn := "sqlite", cfg.DSN
			if engine == store.EnginePostgres {
				driver, dsn = "pgx", cfg.OwnerDSN
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			// Reconstruct both halves of the exact v15 predecessor: nullable
			// column absent AND its later tracking records absent. No backfill.
			// Every version above v15 goes, not just v17: the plan now continues
			// past it (v18 TOTP), and a surviving later row would leave a gapped
			// history the boot classification refuses.
			if _, err := db.ExecContext(ctx, "ALTER TABLE auth_sessions DROP COLUMN aal_authenticated_at"); err != nil {
				t.Fatal(err)
			}
			// v19 creates this relation with its version record. Remove both
			// halves when reconstructing the predecessor, not just the record.
			for _, table := range dialect.FinOpsCustodyControlTables() {
				if _, err := db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
					t.Fatal(err)
				}
			}
			if engine == store.EnginePostgres {
				if _, err := db.ExecContext(ctx, "DROP FUNCTION public."+dialect.PostgresCustodyGuardFunction+"()"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, s.dia.Rebind("DELETE FROM "+coreTrackingTable+" WHERE version > ?"), 15); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			s = raw.(*sqlStore)
			upgraded, err := s.dia.TableColumns(ctx, s.db, "auth_sessions")
			if err != nil || !reflect.DeepEqual(freshColumns, upgraded) {
				t.Fatalf("fresh/upgrade schema differs: %v", err)
			}
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(ctx, legacy.ID)
				if err == nil && row.AALAuthenticatedAt != nil {
					t.Fatal("migration invented an authentication event")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			stamp := model.NewTimestamp(time.Date(2026, 9, 29, 1, 2, 3, 123456789, time.UTC))
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(ctx, legacy.ID)
				if err != nil {
					return err
				}
				row.AALAuthenticatedAt = &stamp
				_, err = as.Sessions().Update(ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(ctx, legacy.ID)
				if err == nil && (row.AALAuthenticatedAt == nil || row.AALAuthenticatedAt.String() != "2026-09-29T01:02:03.123456789Z") {
					t.Fatal("codec lost witness precision")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, changeStamp := range []bool{false, true} {
				before := ataFacts(t, s, tenants[0], users[0].ID)
				if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					row, err := as.Sessions().Get(ctx, legacy.ID)
					if err != nil {
						return err
					}
					row.ExpiresAt = model.NewTimestamp(row.ExpiresAt.Time().Add(time.Hour))
					if changeStamp {
						v := model.NewTimestamp(stamp.Time().Add(time.Nanosecond))
						row.AALAuthenticatedAt = &v
					}
					_, err = as.Sessions().Update(ctx, row)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				after := ataFacts(t, s, tenants[0], users[0].ID)
				if (after.UserAuthorities[0].Version != before.UserAuthorities[0].Version) != changeStamp {
					t.Fatal("witness-only change bypassed User authority or renewal unnecessarily invalidated it")
				}
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					return as.(store.AuthTenantAuthorityBarrier).LockAuthTenantAuthority(ctx, tenants[0], before)
				})
				if (err != nil) != changeStamp {
					t.Fatalf("retained authority admission after witness change=%t: %v", changeStamp, err)
				}
			}
		})
	}
}

func TestAuthenticationFreshnessMigrationPreservesPermanentGapAndHistoricalRender(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		dia, _ := dialect.New(engine)
		versions, err := CompiledCoreMigrationVersions(engine)
		if err != nil || !slices.Equal(versions, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 14, 15, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29}) {
			t.Fatalf("plan: %v, %v", versions, err)
		}
		// The descriptor used by historical migrations stays frozen at v27;
		// authSessionInventoryV28 owns the observed-use columns separately.
		for _, f := range authSessionDescriptor.Fields {
			switch f.Name {
			case "last_used_at", "client_kind", "device_name", "pairing_source_id":
				t.Fatalf("%s: historical descriptor includes v28 column %s", engine, f.Name)
			}
		}
		// The old descriptor is independent of the historical-render helper:
		// remove only the newly appended field, then compare every old DDL byte.
		old := authSessionDescriptor
		old.Fields = slices.Clone(old.Fields[:len(old.Fields)-1])
		want := dia.CreateTableStmts(beforeConsentCustody(old))
		got := dia.CreateTableStmts(beforeConsentCustody(beforeAuthenticationFreshness(authSessionDescriptor)))
		if !slices.Equal(got, want) {
			t.Fatalf("%s changed historical session DDL", engine)
		}
		// v21 is frozen too: the migration, not the old descriptor, owns v28.
		for _, d := range coreDescriptorsV21() {
			if d.Kind == authSessionDescriptor.Kind {
				for _, f := range d.Fields {
					if f.Name == "last_used_at" || f.Name == "client_kind" {
						t.Fatalf("%s: v21 renders new session column %s", engine, f.Name)
					}
				}
			}
		}
	}
}

func TestAuthenticationFreshnessOpenRefusesEditedHistory(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			for _, kind := range []string{"missing intermediate record", "unregistered v16"} {
				t.Run(kind, func(t *testing.T) {
					ctx := context.Background()
					s, cfg, _, _ := f2aFreshTarget(t, engine)
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					// Positive control: Open accepts the real completed plan with
					// no v16. This is the core boot path, not generic migrate.Apply.
					valid, err := Open(ctx, cfg, nil)
					if err != nil {
						t.Fatalf("valid history refused: %v", err)
					}
					if err := valid.Close(); err != nil {
						t.Fatal(err)
					}
					driver, dsn := "sqlite", cfg.DSN
					if engine == store.EnginePostgres {
						driver, dsn = "pgx", cfg.OwnerDSN
					}
					db, err := sql.Open(driver, dsn)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = db.Close() })
					if kind == "missing intermediate record" {
						result, err := db.ExecContext(ctx, s.dia.Rebind("DELETE FROM "+coreTrackingRelation(s.dia)+" WHERE version = ?"), 15)
						if err != nil {
							t.Fatal(err)
						}
						if count, err := result.RowsAffected(); err != nil || count != 1 {
							t.Fatalf("fixture did not remove v15: count=%d err=%v", count, err)
						}
					} else {
						if _, err := db.ExecContext(ctx, s.dia.Rebind("INSERT INTO "+coreTrackingRelation(s.dia)+" (version, name, applied_at, phase) VALUES (?, ?, ?, ?)"),
							16, "unregistered_fixture", "2026-09-29T00:00:00Z", "expand"); err != nil {
							t.Fatal(err)
						}
					}
					before, err := readCanonicalCoreTrackingVersions(ctx, db, s.dia)
					if err != nil {
						t.Fatal(err)
					}
					schema := coreVersionSchemaSnapshot(t, ctx, db, s.dia)
					opened, err := Open(ctx, cfg, nil)
					if opened != nil {
						_ = opened.Close()
						t.Fatal("edited history returned a serving Store")
					}
					if kind == "unregistered v16" {
						if !errors.Is(err, ErrCoreSchemaVersionUnrecognized) {
							t.Fatalf("v16 refusal = %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), "is not a contiguous prefix") {
						t.Fatalf("missing-intermediate refusal = %v", err)
					}
					after, err := readCanonicalCoreTrackingVersions(ctx, db, s.dia)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("refusal repaired history: before=%v after=%v err=%v", before, after, err)
					}
					if !reflect.DeepEqual(schema, coreVersionSchemaSnapshot(t, ctx, db, s.dia)) {
						t.Fatal("refusal changed schema")
					}
				})
			}
		})
	}
}

func TestAuthenticationFreshnessCodecRefusesMalformedWitness(t *testing.T) {
	exp := model.NewTimestamp(time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC))
	r, err := authSessionCodec.Encode(model.AuthSession{ExpiresAt: exp})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"not-a-time", "2026-09-29", "2026-09-29T01:00:00Z"} {
		r["aal_authenticated_at"] = raw
		if _, err := authSessionCodec.Decode(model.BaseFields{}, r); err == nil {
			t.Fatalf("malformed witness %q silently decoded", raw)
		}
	}
}

func TestAuthSessionInventoryMigrationUpgradesCurrentStore(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, cfg, users, _ := f2aFreshTarget(t, engine)
			var legacy model.AuthSession
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				var err error
				legacy, err = as.Sessions().Create(ctx, model.AuthSession{
					UserID: users[0].ID, Selector: "inventory-legacy", SecretHash: []byte("test hash"),
					ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			driver, dsn := "sqlite", cfg.DSN
			if engine == store.EnginePostgres {
				driver, dsn = "pgx", cfg.OwnerDSN
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, stmt := range []string{
				"ALTER TABLE auth_sessions DROP COLUMN last_used_at",
				"ALTER TABLE auth_sessions DROP COLUMN client_kind",
				"ALTER TABLE auth_sessions DROP COLUMN device_name",
				"ALTER TABLE auth_sessions DROP COLUMN pairing_source_id",
				"DELETE FROM schema_migrations_core WHERE version>=28",
			} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			reopened, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("reopen a v27 store: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			columns, err := reopened.(*sqlStore).dia.TableColumns(ctx, reopened.(*sqlStore).db, "auth_sessions")
			if err != nil || !columns["last_used_at"] || !columns["client_kind"] || !columns["device_name"] || !columns["pairing_source_id"] {
				t.Fatalf("v28 did not restore inventory columns: %v", err)
			}
			var recorded int
			if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations_core WHERE version=28 AND name='auth_session_inventory'").Scan(&recorded); err != nil || recorded != 1 {
				t.Fatalf("v28 recorded %d times (err %v), want once", recorded, err)
			}
			usedAt := model.NewTimestamp(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
			var got model.AuthSession
			if err := reopened.AuthView(ctx, func(as store.AuthScope) error {
				var err error
				got, err = as.Sessions().Get(ctx, legacy.ID)
				if err == nil && (got.LastUsedAt != nil || got.ClientKind != "" || got.DeviceName != "" || !got.PairingSourceID.IsZero()) {
					t.Fatal("migration invented historical session use")
				}
				if err := as.RecordAuthSessionUse(ctx, legacy.ID, usedAt, model.SessionClientBrowser); !errors.Is(err, store.ErrReadOnly) {
					t.Errorf("read-only use write = %v, want ErrReadOnly", err)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := reopened.AuthMutate(ctx, func(as store.AuthScope) error {
				return as.RecordAuthSessionUse(ctx, legacy.ID, usedAt, model.SessionClientBrowser)
			}); err != nil {
				t.Fatal(err)
			}
			if err := reopened.AuthView(ctx, func(as store.AuthScope) error {
				var err error
				got, err = as.Sessions().Get(ctx, legacy.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got.LastUsedAt == nil || !got.LastUsedAt.Time().Equal(usedAt.Time()) || got.ClientKind != model.SessionClientBrowser {
				t.Errorf("observed use = %v/%q, want %v/browser", got.LastUsedAt, got.ClientKind, usedAt)
			}
			older := model.NewTimestamp(usedAt.Time().Add(-time.Minute))
			if err := reopened.AuthMutate(ctx, func(as store.AuthScope) error {
				return as.RecordAuthSessionUse(ctx, legacy.ID, older, model.SessionClientToken)
			}); err != nil {
				t.Fatal(err)
			}
			if err := reopened.AuthView(ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(ctx, legacy.ID)
				if err == nil && (row.LastUsedAt == nil || row.LastUsedAt.String() != usedAt.String() || row.ClientKind != model.SessionClientBrowser) {
					t.Errorf("older request overwrote newer metadata: %v/%s", row.LastUsedAt, row.ClientKind)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got.Version != legacy.Version || !got.UpdatedAt.Time().Equal(legacy.UpdatedAt.Time()) {
				t.Errorf("version/updated_at changed: %d/%v, want %d/%v", got.Version, got.UpdatedAt, legacy.Version, legacy.UpdatedAt)
			}
			if err := reopened.AuthMutate(ctx, func(as store.AuthScope) error {
				return as.RecordAuthSessionUse(ctx, model.NewID(), usedAt, model.SessionClientBrowser)
			}); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("absent session = %v, want ErrNotFound", err)
			}
			if err := reopened.AuthMutate(ctx, func(as store.AuthScope) error {
				return as.RecordAuthSessionUse(ctx, legacy.ID, usedAt, "forged")
			}); err == nil {
				t.Error("invalid client kind was accepted")
			}
			if err := reopened.AuthMutate(ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(ctx, legacy.ID)
				if err != nil {
					return err
				}
				row.DeviceName, row.PairingSourceID = "Work phone", legacy.ID
				_, err = as.Sessions().Update(ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := reopened.AuthView(ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(ctx, legacy.ID)
				if err == nil && (row.DeviceName != "Work phone" || row.PairingSourceID != legacy.ID) {
					t.Fatal("pairing columns did not round-trip on upgraded store")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
