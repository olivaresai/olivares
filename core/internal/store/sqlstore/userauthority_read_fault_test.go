// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type userAuthorityReadDialect struct {
	dialect.Dialect
	bind func(context.Context, *sql.Tx, model.TenantID) error
}

func (d userAuthorityReadDialect) BindTenant(ctx context.Context, tx *sql.Tx, tenant model.TenantID) error {
	return d.bind(ctx, tx, tenant)
}

func testUserAuthorityReadBindingFailures(t *testing.T, s *sqlStore, tenant model.TenantID, id model.ID, bundle store.AuthoritySnapshotBundle) {
	ctx := context.Background()
	injected := errors.New("injected read binding failure")
	callbackErr := errors.New("callback failure")
	for _, auth := range []bool{false, true} {
		name := "bundle"
		if auth {
			name = "auth-directory"
		}
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"capture-mismatch", "capture-failure", "initial-bind", "restore-failure", "restore-mismatch"} {
				t.Run(mode, func(t *testing.T) {
					run := func(sc *tenantScope, operation func() error, observe func() error, earlier func() error) error {
						original := s.dia
						defer func() { s.dia = original }()
						calls := 0
						s.dia = userAuthorityReadDialect{Dialect: original, bind: func(c context.Context, tx *sql.Tx, target model.TenantID) error {
							calls++
							if mode == "initial-bind" && calls == 1 || mode == "restore-failure" && calls == 2 {
								return injected
							}
							if mode == "restore-mismatch" && calls == 2 {
								if target == model.SystemTenantID {
									target = tenant
								} else {
									target = model.SystemTenantID
								}
							}
							return original.BindTenant(c, tx, target)
						}}
						if mode == "capture-mismatch" {
							wrong := model.SystemTenantID
							if sc.tenant == wrong {
								wrong = tenant
							}
							if err := original.BindTenant(ctx, sc.tx, wrong); err != nil {
								return err
							}
						}
						if mode == "capture-failure" {
							if err := sc.tx.Rollback(); err != nil {
								return err
							}
						}
						err := operation()
						if !errors.Is(err, store.ErrDirectoryUnavailable) || sc.bindingPoison == nil {
							t.Fatalf("binding failure did not poison: %v", err)
						}
						if (mode == "initial-bind" || mode == "restore-failure") && !errors.Is(err, injected) {
							t.Fatalf("lost bind cause: %v", err)
						}
						if mode == "capture-failure" && !errors.Is(err, sql.ErrTxDone) {
							t.Fatalf("lost rollback cause: %v", err)
						}
						if err := earlier(); !errors.Is(err, sql.ErrTxDone) {
							t.Fatalf("pre-obtained repository survived: %v", err)
						}
						if err := observe(); !errors.Is(err, store.ErrDirectoryUnavailable) {
							t.Fatalf("observer survived poison: %v", err)
						}
						if err := sc.ValidateAuthoritySnapshot(ctx, bundle.Facts); !errors.Is(err, store.ErrDirectoryUnavailable) {
							t.Fatalf("existing validator survived poison: %v", err)
						}
						if err := sc.ValidateAuthoritySnapshotBundle(ctx, bundle); !errors.Is(err, store.ErrDirectoryUnavailable) {
							t.Fatalf("bundle survived poison: %v", err)
						}
						if mode == "restore-mismatch" {
							return callbackErr
						}
						return nil // The transaction boundary must retain a swallowed failure.
					}
					var err error
					if auth {
						err = s.AuthView(ctx, func(as store.AuthScope) error {
							a := as.(*authScope)
							repo := as.Users()
							return run(a.ts, func() error { _, err := a.ReadDirectoryEpochFact(ctx, tenant); return err }, func() error {
								h, err := a.ReadUserAuthorityFact(ctx, id)
								if h != (store.UserAuthorityFactRef{}) {
									t.Fatal("poison returned H")
								}
								return err
							}, func() error { _, err := repo.Get(ctx, id); return err })
						})
					} else {
						err = s.View(ctx, tenant, func(sc store.Scope) error {
							repo := sc.Workspaces()
							return run(sc.(*tenantScope), func() error { return store.ValidateReadAuthorityBundle(ctx, sc, bundle) }, func() error { return store.ValidateReadAuthorityBundle(ctx, sc, bundle) }, func() error { _, _, err := repo.List(ctx, model.Query{}); return err })
						})
					}
					if !errors.Is(err, store.ErrDirectoryUnavailable) {
						t.Fatalf("nil callback cleared poison: %v", err)
					}
					if mode == "restore-mismatch" && !errors.Is(err, callbackErr) {
						t.Fatalf("lost callback error: %v", err)
					}
					if err := s.View(ctx, tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(ctx, sc, bundle) }); err != nil {
						t.Fatalf("pooled presentation leaked: %v", err)
					}
				})
			}
		})
	}
	t.Run("observer-rejects-unverified-SYSTEM", func(t *testing.T) {
		err := s.AuthView(ctx, func(as store.AuthScope) error {
			a := as.(*authScope)
			repo := as.Users()
			if err := s.dia.BindTenant(ctx, a.ts.tx, tenant); err != nil {
				return err
			}
			h, err := a.ReadUserAuthorityFact(ctx, id)
			if h != (store.UserAuthorityFactRef{}) || !errors.Is(err, store.ErrDirectoryUnavailable) {
				t.Fatalf("unverified SYSTEM observed H: %v", err)
			}
			if _, err := repo.Get(ctx, id); !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("repository survived observer abort: %v", err)
			}
			return nil
		})
		if !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("observer failure swallowed: %v", err)
		}
	})
	for _, auth := range []bool{false, true} {
		name := "bundle-cancellation"
		if auth {
			name = "directory-cancellation"
		}
		t.Run(name, func(t *testing.T) {
			run := func(sc *tenantScope, operation func(context.Context) error) error {
				methodCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				original := s.dia
				defer func() { s.dia = original }()
				calls := 0
				s.dia = userAuthorityReadDialect{Dialect: original, bind: func(c context.Context, tx *sql.Tx, target model.TenantID) error {
					calls++
					err := original.BindTenant(c, tx, target)
					if calls == 1 {
						cancel()
					}
					return err
				}}
				err := operation(methodCtx)
				if !errors.Is(err, context.Canceled) || !errors.Is(err, store.ErrDirectoryUnavailable) {
					t.Fatalf("lost cancellation: %v", err)
				}
				if sc.bindingPoison != nil {
					t.Fatalf("successful cleanup poisoned scope: %v", sc.bindingPoison)
				}
				presentation, err := readUserAuthorityPresentation(ctx, sc.tx, s.dia)
				if err != nil {
					return err
				}
				if presentation != sc.tenant.String() {
					t.Fatal("cancellation leaked presentation")
				}
				return nil
			}
			var err error
			if auth {
				err = s.AuthView(ctx, func(as store.AuthScope) error {
					return run(as.(*authScope).ts, func(c context.Context) error { _, err := as.(*authScope).ReadDirectoryEpochFact(c, tenant); return err })
				})
			} else {
				err = s.View(ctx, tenant, func(sc store.Scope) error {
					return run(sc.(*tenantScope), func(c context.Context) error { return store.ValidateReadAuthorityBundle(c, sc, bundle) })
				})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("canceled-capture-aborts", func(t *testing.T) {
		err := s.View(ctx, tenant, func(sc store.Scope) error {
			repo := sc.Workspaces()
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			err := store.ValidateReadAuthorityBundle(canceled, sc, bundle)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, store.ErrDirectoryUnavailable) {
				t.Fatalf("capture cancellation: %v", err)
			}
			if _, _, err := repo.List(ctx, model.Query{}); !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("capture did not terminate: %v", err)
			}
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("View lost cancellation: %v", err)
		}
	})
	t.Run("canceled-View-retains-rollback-cause", func(t *testing.T) {
		outer, cancel := context.WithCancel(ctx)
		defer cancel()
		err := s.AuthView(outer, func(as store.AuthScope) error {
			repo := as.Users()
			cancel()
			h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(outer, id)
			if h != (store.UserAuthorityFactRef{}) || !errors.Is(err, context.Canceled) || !errors.Is(err, store.ErrDirectoryUnavailable) {
				t.Fatalf("canceled View observation: %v", err)
			}
			if _, err := repo.Get(ctx, id); !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("canceled transaction remained usable: %v", err)
			}
			return nil
		})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("View cleared cancellation poison: %v", err)
		}
	})
	t.Run("deadline-cause", func(t *testing.T) {
		err := s.View(ctx, tenant, func(sc store.Scope) error {
			expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			defer cancel()
			return store.ValidateReadAuthorityBundle(expired, sc, bundle)
		})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("deadline cause: %v", err)
		}
	})

}

func testUserAuthorityReadSnapshot(t *testing.T, s *sqlStore, cfg store.Config, tenant model.TenantID, user model.User, bundle store.AuthoritySnapshotBundle) {
	// On SQLite this helper opens a SECOND store, and an Open runs the compiled migration
	// plan. That is fixture, not the snapshot race the 20 s budget is sampling: charged to
	// the budget it leaves the writer handoff below with the remainder, and under -race on a
	// contended runner the wait then reports `context deadline exceeded` (01f81b8e81 /
	// 4859cc43f3 / 346bce0c8a). Fixture unbounded; budget at the behaviour.
	setupCtx := context.Background()
	var writer store.Store = s
	if cfg.Engine == store.EngineSQLite {
		raw, err := Open(setupCtx, cfg, registerAuthorityLeaseFact)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		writer = raw
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	write := func() {
		close(started)
		done <- writer.AuthMutate(ctx, func(as store.AuthScope) error {
			current, err := as.Users().Get(ctx, user.ID)
			if err != nil {
				return err
			}
			current.Status = model.StatusInactive
			_, err = as.Users().Update(ctx, current)
			return err
		})
	}
	if err := s.View(ctx, tenant, func(sc store.Scope) error {
		if err := store.ValidateReadAuthorityBundle(ctx, sc, bundle); err != nil {
			return err
		}
		go write()
		<-started
		if cfg.Engine == store.EnginePostgres {
			select {
			case err := <-done:
				if err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			select {
			case err := <-done:
				t.Fatalf("SQLite writer finished while View holds writer slot: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
		}
		return store.ValidateReadAuthorityBundle(ctx, sc, bundle)
	}); err != nil {
		t.Fatal(err)
	}
	if cfg.Engine == store.EngineSQLite {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := s.View(ctx, tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(ctx, sc, bundle) }); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("fresh View accepted old H: %v", err)
	}
	current := readUserAuthorityBundle(t, s, tenant, user.ID)
	if current.UserAuthorities[0].Version != bundle.UserAuthorities[0].Version+1 {
		t.Fatal("User writer did not advance H once")
	}
	if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Sessions().Create(ctx, model.AuthSession{UserID: user.ID, Selector: "u1-session", SecretHash: []byte("test-credential"), ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour))})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(ctx, sc, current) }); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("session writer did not invalidate old H: %v", err)
	}
	next := readUserAuthorityBundle(t, s, tenant, user.ID)
	if next.UserAuthorities[0].Version != current.UserAuthorities[0].Version+1 {
		t.Fatal("session writer did not advance H once")
	}
}
