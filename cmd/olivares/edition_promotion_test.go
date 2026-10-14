// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type editionRecoveryFixture struct {
	recover func(context.Context) error
	close   func() error
}

func (r *editionRecoveryFixture) Recover(ctx context.Context) error { return r.recover(ctx) }
func (r *editionRecoveryFixture) Close() error                      { return r.close() }

func TestEditionPromotionBootOwnsRecovery(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	for i := 0; i < 2; i++ {
		resetBootProcessScope(t)
		var trace []string
		var registered func(context.Context) error
		var instance *editionRecoveryFixture
		var source store.Store
		dir := t.TempDir()
		thisEdition.promotionRecovery = func(_ context.Context, cfg EditionConfig, mods []api.Module, st store.Store, _ *slog.Logger) (EditionPromotionRecovery, error) {
			if cfg.DataDir != dir || st == nil || len(mods) == 0 {
				t.Fatal("constructor lost the boot configuration/store/modules")
			}
			if registered != nil {
				t.Fatal("recovery constructed after callback registration")
			}
			source = st
			trace = append(trace, "construct")
			instance = &editionRecoveryFixture{
				recover: func(ctx context.Context) error {
					// Core provisioning must remain in the same callback, ahead of edition recovery.
					if err := st.System(ctx, func(sys store.SystemScope) error {
						orgs, err := sys.ListOrgs(ctx)
						if err == nil && len(orgs) == 0 {
							return errors.New("core system tenant was not provisioned")
						}
						return err
					}); err != nil {
						return err
					}
					trace = append(trace, "edition")
					return nil
				},
				close: func() error { trace = append(trace, "close-recovery"); return nil },
			}
			return instance, nil
		}
		thisEdition.bindModuleDependencies = func(_ context.Context, _ EditionConfig, _ []api.Module, deps EditionDependencies, _ *slog.Logger) ([]io.Closer, error) {
			if instance == nil || deps.PromotionRecovery != instance || deps.Store != source {
				t.Fatal("late binding lost the boot-owned recovery")
			}
			trace = append(trace, "bind")
			return []io.Closer{closerFunc(func() error { trace = append(trace, "close-bound"); return nil })}, nil
		}
		eng, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true, Logger: discardLogger(),
			pdpPromotionRegistered: func(fn func(context.Context) error) { registered = fn },
			pdpListOrgs: func(context.Context, store.Store) ([]model.Org, error) {
				trace = append(trace, "core-pdp")
				return nil, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"construct", "core-pdp", "edition", "bind"}; !reflect.DeepEqual(trace, want) {
			t.Fatalf("initial promotion trace = %v, want %v", trace, want)
		}
		// The captured production callback must preserve both recoveries on later use.
		if err := registered(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := eng.Close(); err != nil {
			t.Fatal(err)
		}
		if want := []string{"construct", "core-pdp", "edition", "bind", "core-pdp", "edition", "close-bound", "close-recovery"}; !reflect.DeepEqual(trace, want) {
			t.Fatalf("recovery lifecycle = %v, want %v", trace, want)
		}
	}
}

func TestEditionPromotionFailedBootClosesRecovery(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	for _, phase := range []string{"construct", "core", "recover", "bind", "late"} {
		t.Run(phase, func(t *testing.T) {
			resetBootProcessScope(t)
			failure := errors.New("edition promotion fixture failure")
			if phase == "late" {
				bad := filepath.Join(t.TempDir(), "hook-pep.json")
				if err := os.WriteFile(bad, []byte("{not json"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("OLIVARES_HOOK_PEP_CONFIG", bad)
			}
			constructed, recovered, closed, boundClosed := 0, 0, 0, 0
			thisEdition.promotionRecovery = func(context.Context, EditionConfig, []api.Module, store.Store, *slog.Logger) (EditionPromotionRecovery, error) {
				constructed++
				r := &editionRecoveryFixture{recover: func(context.Context) error {
					recovered++
					if phase == "recover" {
						return failure
					}
					return nil
				}, close: func() error { closed++; return nil }}
				if phase == "construct" {
					return r, failure
				}
				return r, nil
			}
			thisEdition.bindModuleDependencies = func(context.Context, EditionConfig, []api.Module, EditionDependencies, *slog.Logger) ([]io.Closer, error) {
				resources := []io.Closer{closerFunc(func() error { boundClosed++; return nil })}
				if phase == "bind" {
					return resources, failure
				}
				return resources, nil
			}
			eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true, Logger: discardLogger(),
				pdpListOrgs: func(context.Context, store.Store) ([]model.Org, error) {
					if phase == "core" {
						return nil, failure
					}
					return nil, nil
				},
			})
			if eng != nil {
				_ = eng.Close()
				t.Fatal("failed recovery returned an engine")
			}
			if (phase != "late" && !errors.Is(err, failure)) || err == nil {
				t.Fatalf("failure was swallowed: %v", err)
			}
			wantRecovery, wantBoundClosed := 0, 0
			if phase == "recover" || phase == "bind" || phase == "late" {
				wantRecovery = 1
			}
			if phase == "bind" || phase == "late" {
				wantBoundClosed = 1
			}
			if constructed != 1 || recovered != wantRecovery || closed != 1 || boundClosed != wantBoundClosed {
				t.Fatalf("construct/recover/close/boundClose = %d/%d/%d/%d", constructed, recovered, closed, boundClosed)
			}
		})
	}
}

// Offline commands still provision core state but own no serving edition recovery.
func TestEditionPromotionOfflineDoesNotConstructRecovery(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.promotionRecovery = func(context.Context, EditionConfig, []api.Module, store.Store, *slog.Logger) (EditionPromotionRecovery, error) {
		t.Fatal("offline boot constructed serving recovery")
		return nil, nil
	}
	thisEdition.bindModuleDependencies = nil
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
}

// This elector still drains the real store's election, then reports an unlock
// failure. A subsequent store.Close may return nil because resignation is final.
type editionResignError struct {
	store.LeaderElector
	err error
}

func (e editionResignError) Resign(ctx context.Context) error {
	return errors.Join(e.LeaderElector.Resign(ctx), e.err)
}

type editionCloseStore struct {
	store.Store
	elector store.LeaderElector
	err     error
}

func (s editionCloseStore) Leader() store.LeaderElector { return s.elector }
func (s editionCloseStore) Close() error                { return errors.Join(s.Store.Close(), s.err) }

func TestEditionPromotionShutdownPreservesResignationError(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.promotionRecovery = func(context.Context, EditionConfig, []api.Module, store.Store, *slog.Logger) (EditionPromotionRecovery, error) {
		return &editionRecoveryFixture{recover: func(context.Context) error { return nil }, close: func() error { return nil }}, nil
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	resignFailure, storeFailure := errors.New("resignation failed"), errors.New("pool drain failed")
	eng.store = editionCloseStore{Store: eng.store, elector: editionResignError{eng.store.Leader(), resignFailure}, err: storeFailure}
	if err := eng.Close(); !errors.Is(err, resignFailure) || !errors.Is(err, storeFailure) {
		t.Fatalf("shutdown lost a cause: %v", err)
	}
}
