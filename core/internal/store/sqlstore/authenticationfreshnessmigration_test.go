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
			// column absent AND its later tracking record absent. No backfill.
			if _, err := db.ExecContext(ctx, "ALTER TABLE auth_sessions DROP COLUMN aal_authenticated_at"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, s.dia.Rebind("DELETE FROM "+coreTrackingTable+" WHERE version = ?"), 17); err != nil {
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
		if err != nil || !slices.Equal(versions, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 14, 15, 17}) {
			t.Fatalf("plan: %v, %v", versions, err)
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
