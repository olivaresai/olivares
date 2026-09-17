// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestUserAuthorityReadSQLite(t *testing.T)   { testUserAuthorityRead(t, store.EngineSQLite) }
func TestUserAuthorityReadPostgres(t *testing.T) { testUserAuthorityRead(t, store.EnginePostgres) }

func userAuthorityReadStore(t *testing.T, engine store.Engine) (*sqlStore, store.Config, model.TenantID, model.User) {
	t.Helper()
	cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "read.db"), Debug: true, MaxConns: 4}
	if engine == store.EnginePostgres {
		pg := isolatedPGSplit(t)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
	}
	raw, err := Open(context.Background(), cfg, registerAuthorityLeaseFact)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	tenant := provisionTenant(t, raw, "authority-read")
	var user model.User
	if err := raw.AuthMutate(context.Background(), func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(context.Background(), model.User{Email: "read@example.test", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return raw.(*sqlStore), cfg, tenant, user
}

func readUserAuthorityBundle(t *testing.T, s *sqlStore, tenant model.TenantID, id model.ID) store.AuthoritySnapshotBundle {
	t.Helper()
	ctx := context.Background()
	var b store.AuthoritySnapshotBundle
	if err := s.AuthView(ctx, func(as store.AuthScope) error {
		h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(ctx, id)
		b.UserAuthorities = []store.UserAuthorityFactRef{h}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, tenant, func(sc store.Scope) error {
		g, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		b.Facts = []store.AuthorizationFactRef{{Kind: model.DirectoryEpochKind, ID: g.ID, Version: g.Version}}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return b
}

func testUserAuthorityRead(t *testing.T, engine store.Engine) {
	s, cfg, tenant, user := userAuthorityReadStore(t, engine)
	ctx := context.Background()
	bundle := readUserAuthorityBundle(t, s, tenant, user.ID)
	validate := func(b store.AuthoritySnapshotBundle) error {
		return s.View(ctx, tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(ctx, sc, b) })
	}
	t.Run("observe-and-confined-bundle", func(t *testing.T) {
		if bundle.UserAuthorities[0] != (store.UserAuthorityFactRef{UserID: user.ID, Version: 1}) {
			t.Fatal("wrong initial User fence")
		}
		if err := s.View(ctx, tenant, func(sc store.Scope) error {
			ws, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			confined, err := store.ConfineWorkspace(ctx, sc, ws.ID)
			if err != nil {
				return err
			}
			if err := store.ValidateReadAuthorityBundle(ctx, confined, bundle); err != nil {
				return err
			}
			if sc.(*tenantScope).directoryWriter != nil || sc.(*tenantScope).authorityLocked {
				t.Fatal("read reserved a directory writer or authority lock")
			}
			presented, err := readUserAuthorityPresentation(ctx, sc.(*tenantScope).tx, s.dia)
			if err != nil {
				return err
			}
			if presented != tenant.String() {
				t.Fatal("bundle did not restore business presentation")
			}
			if _, _, err := confined.Identities().List(ctx, model.Query{}); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
				t.Fatalf("repository widened: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("optional-capability-and-mode", func(t *testing.T) {
		if err := s.View(ctx, tenant, func(sc store.Scope) error {
			thin := struct{ store.Scope }{sc}
			if err := store.ValidateReadAuthorityBundle(ctx, thin, bundle); !errors.Is(err, store.ErrLineageUnavailable) {
				t.Fatalf("absent reader: %v", err)
			}
			ws, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			confined, err := store.ConfineWorkspace(ctx, thin, ws.ID)
			if err != nil {
				return err
			}
			if err := store.ValidateReadAuthorityBundle(ctx, confined, bundle); !errors.Is(err, store.ErrLineageUnavailable) {
				t.Fatalf("manufactured reader: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
			if err := store.ValidateReadAuthorityBundle(ctx, sc, bundle); err == nil {
				t.Fatal("Mutate accepted by read validator")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
			h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(ctx, user.ID)
			if h != bundle.UserAuthorities[0] {
				t.Fatal("AuthMutate observation changed H")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("grammar-before-any-SQL", func(t *testing.T) {
		bad := []struct {
			name      string
			b         store.AuthoritySnapshotBundle
			directory bool
		}{
			{"empty-facts", store.AuthoritySnapshotBundle{UserAuthorities: bundle.UserAuthorities}, false},
			{"facts-overflow", store.AuthoritySnapshotBundle{Facts: make([]store.AuthorizationFactRef, 65)}, false},
			{"duplicate-facts", store.AuthoritySnapshotBundle{Facts: append(append([]store.AuthorizationFactRef{}, bundle.Facts...), bundle.Facts...)}, false},
			{"H-in-facts", store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{{Kind: model.UserAuthorityKind, ID: user.ID, Version: 1}}}, false},
			{"H-overflow", store.AuthoritySnapshotBundle{Facts: bundle.Facts, UserAuthorities: make([]store.UserAuthorityFactRef, 65)}, true},
			{"H-conflict", store.AuthoritySnapshotBundle{Facts: bundle.Facts, UserAuthorities: []store.UserAuthorityFactRef{{UserID: user.ID, Version: 1}, {UserID: user.ID, Version: 2}}}, true},
			{"H-id", store.AuthoritySnapshotBundle{Facts: bundle.Facts, UserAuthorities: []store.UserAuthorityFactRef{{UserID: "invalid", Version: 1}}}, true},
			{"H-version", store.AuthoritySnapshotBundle{Facts: bundle.Facts, UserAuthorities: []store.UserAuthorityFactRef{{UserID: user.ID, Version: 0}}}, true},
		}
		for _, tc := range bad {
			t.Run(tc.name, func(t *testing.T) {
				if err := s.View(ctx, tenant, func(sc store.Scope) error {
					if err := sc.(*tenantScope).tx.Rollback(); err != nil {
						return err
					}
					err := store.ValidateReadAuthorityBundle(ctx, sc, tc.b)
					if err == nil || errors.Is(err, sql.ErrTxDone) || errors.Is(err, store.ErrDirectoryUnavailable) != tc.directory {
						t.Fatalf("grammar reached SQL or changed class: %v", err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
		b := bundle
		b.UserAuthorities = make([]store.UserAuthorityFactRef, 64)
		for i := range b.UserAuthorities {
			b.UserAuthorities[i] = bundle.UserAuthorities[0]
		}
		if err := validate(b); err != nil {
			t.Fatalf("64 supplied equal H: %v", err)
		}
		b.UserAuthorities = append(b.UserAuthorities, bundle.UserAuthorities[0])
		if err := validate(b); !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("overflow deduplicated before budget: %v", err)
		}
	})
	t.Run("exact-error-classes-and-restoration", func(t *testing.T) {
		for _, stale := range []bool{false, true} {
			b := bundle
			b.UserAuthorities = append([]store.UserAuthorityFactRef{}, bundle.UserAuthorities...)
			want := store.ErrDirectoryUnavailable
			if stale {
				b.UserAuthorities[0].Version++
				want = store.ErrConflict
			} else {
				b.UserAuthorities[0].UserID = model.NewID()
			}
			if err := s.View(ctx, tenant, func(sc store.Scope) error {
				err := store.ValidateReadAuthorityBundle(ctx, sc, b)
				if !errors.Is(err, want) {
					t.Fatalf("H classification: %v", err)
				}
				if sc.(*tenantScope).bindingPoison != nil {
					t.Fatal("ordinary H failure poisoned View")
				}
				_, err = sc.DefaultWorkspace(ctx)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		missing := store.AuthorizationFactRef{Kind: "core.agent", ID: model.NewID(), Version: 1}
		for _, users := range [][]store.UserAuthorityFactRef{nil, bundle.UserAuthorities} {
			if err := validate(store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{missing}, UserAuthorities: users}); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("ordinary tenant absence: %v", err)
			}
		}
		if err := s.AuthView(ctx, func(as store.AuthScope) error {
			for _, id := range []model.ID{model.NewID(), "malformed"} {
				h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(ctx, id)
				if h != (store.UserAuthorityFactRef{}) || !errors.Is(err, store.ErrDirectoryUnavailable) {
					t.Fatalf("observer accepted missing/malformed H: %v", err)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("lease-read-has-no-OCC-touch", func(t *testing.T) {
		var row model.Record
		if err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
			r, err := sc.Ext(authorityLeaseFactKind)
			if err != nil {
				return err
			}
			row, err = r.Create(ctx, model.Record{"sid": "u1-lease", "holder": "holder", "fence": int64(7), "claim_state": "active", "lease_expires_at": model.NewTimestamp(time.Now().Add(time.Hour)).String()})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		ref := authorityLeaseFactRef(t, row)
		subject, fence, deadline, _ := ref.LeaseFenceWitness()
		wrong, err := store.NewLeaseFenceAuthorizationFactRef(ref.Kind, ref.ID, ref.Version, subject, fence+1, deadline)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{wrong}, UserAuthorities: bundle.UserAuthorities}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("lease fence mismatch: %v", err)
		}
		if err := s.View(ctx, tenant, func(sc store.Scope) error {
			b := store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{ref}, UserAuthorities: bundle.UserAuthorities}
			if err := store.ValidateReadAuthorityBundle(ctx, sc, b); err != nil {
				return err
			}
			r, err := sc.Ext(authorityLeaseFactKind)
			if err != nil {
				return err
			}
			got, err := r.Get(ctx, ref.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(got, row) {
				t.Fatal("read changed lease row within the View")
			}
			b.Facts[0].ID = model.NewID()
			if err := store.ValidateReadAuthorityBundle(ctx, sc, b); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("missing leased fact: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	if engine == store.EnginePostgres {
		t.Run("restricted-app-and-FORCE-RLS", func(t *testing.T) {
			if err := s.View(ctx, tenant, func(sc store.Scope) error {
				tx := sc.(*tenantScope).tx
				var super, bypass, owner, canRead, forced bool
				if err := tx.QueryRowContext(ctx, `SELECT r.rolsuper,r.rolbypassrls,c.relowner=r.oid,pg_catalog.has_table_privilege(c.oid,'SELECT'),c.relforcerowsecurity FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE r.rolname=current_user AND n.nspname='public' AND c.relname='core_user_authority'`).Scan(&super, &bypass, &owner, &canRead, &forced); err != nil {
					return err
				}
				var readOnly, isolation string
				if err := tx.QueryRowContext(ctx, "SELECT current_setting('transaction_read_only'), current_setting('transaction_isolation')").Scan(&readOnly, &isolation); err != nil {
					return err
				}
				if readOnly != "on" || isolation != "repeatable read" {
					t.Fatal("PostgreSQL View lost snapshot/read-only options")
				}
				if super || bypass || owner || !canRead || !forced {
					t.Fatal("PostgreSQL app role or H RLS contract is not restricted")
				}
				for i := 0; i < 2; i++ {
					var count int
					if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM public.core_user_authority WHERE id=$1", user.ID.String()).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						t.Fatal("business scope observed SYSTEM H")
					}
					if i == 0 {
						if err := store.ValidateReadAuthorityBundle(ctx, sc, bundle); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("binding-failure-and-cancellation", func(t *testing.T) { testUserAuthorityReadBindingFailures(t, s, tenant, user.ID, bundle) })
	if engine == store.EngineSQLite {
		t.Run("strict-storage-with-no-persisted-corruption", func(t *testing.T) {
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				tx := as.(*authScope).ts.tx
				if _, err := tx.ExecContext(ctx, "UPDATE main.core_user_authority SET version=CAST('1' AS BLOB) WHERE id=?", user.ID.String()); err != nil {
					return err
				}
				h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(ctx, user.ID)
				if !errors.Is(err, store.ErrDirectoryUnavailable) || h != (store.UserAuthorityFactRef{}) {
					t.Fatalf("noninteger H accepted: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := readUserAuthorityBundle(t, s, tenant, user.ID); !reflect.DeepEqual(got, bundle) {
				t.Fatal("negative read fixture persisted corruption")
			}
		})
	}
	t.Run("no-persisted-read-effects", func(t *testing.T) {
		snapshot := func() []any {
			var out []any
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				current, err := as.Users().Get(ctx, user.ID)
				if err != nil {
					return err
				}
				h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(ctx, user.ID)
				if err != nil {
					return err
				}
				head, found, err := as.Audit().Head(ctx)
				if err != nil {
					return err
				}
				var count int
				query := s.dia.Rebind("SELECT count(*) FROM " + directoryWriterRelation(s.dia, userAuthorityDescriptor.Table) + " WHERE tenant_id=?")
				if err := as.(*authScope).ts.tx.QueryRowContext(ctx, query, model.SystemTenantID.String()).Scan(&count); err != nil {
					return err
				}
				out = append(out, current, h, head, found, count)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.View(ctx, tenant, func(sc store.Scope) error {
				epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
				if err != nil {
					return err
				}
				workspace, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				head, found, err := sc.Audit().Head(ctx)
				if err != nil {
					return err
				}
				out = append(out, epoch, workspace, head, found)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return out
		}
		before := snapshot()
		if err := validate(bundle); err != nil {
			t.Fatal(err)
		}
		missing := bundle
		missing.UserAuthorities = []store.UserAuthorityFactRef{{UserID: model.NewID(), Version: 1}}
		if err := validate(missing); !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("absent read: %v", err)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("read changed User, H, directory, workspace or audit evidence")
		}
	})
	t.Run("snapshot-and-writer", func(t *testing.T) { testUserAuthorityReadSnapshot(t, s, cfg, tenant, user, bundle) })
}
