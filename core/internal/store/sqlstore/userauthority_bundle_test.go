// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func f2aBundle(tenant model.TenantID, ids ...model.ID) store.AuthoritySnapshotBundle {
	b := store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 2}}}
	for _, id := range ids {
		b.UserAuthorities = append(b.UserAuthorities, store.UserAuthorityFactRef{UserID: id, Version: 1})
	}
	return b
}

func TestUserAuthorityBundleBindingAndGrammar(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant := tenants[0]
			// More than 64 H is valid; Facts retains its own 1..64 budget.
			ids := []model.ID{users[0].ID, users[1].ID, users[2].ID}
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				for i := 0; i < 63; i++ {
					u, err := as.Users().Create(ctx, model.User{Email: fmt.Sprintf("bundle-%d@example.test", i), Status: model.StatusActive})
					if err != nil {
						return err
					}
					ids = append(ids, u.ID)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			b := f2aBundle(tenant, ids...)
			b.UserAuthorities = append(b.UserAuthorities, b.UserAuthorities[0])
			if err := s.Mutate(ctx, tenant, func(raw store.Scope) error {
				sc := raw.(*tenantScope)
				if err := sc.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) { return nil, nil }); err != nil {
					return err
				}
				workspace, err := raw.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
				if err != nil {
					return err
				}
				twice, err := store.ConfineWorkspace(ctx, confined, workspace.ID)
				if err != nil {
					return err
				}
				if twice != confined {
					t.Error("same confinement was not idempotent")
				}
				if _, err := store.ConfineWorkspace(ctx, confined, model.NewID()); err == nil {
					t.Error("bundle decorator allowed retargeting")
				}
				if _, ok := confined.(store.AuthoritySnapshotLocker); !ok {
					t.Error("lost existing authority locker")
				}
				if _, ok := confined.(store.TransactionLocker); !ok {
					t.Error("lost record locker")
				}
				if err := confined.(store.AuthoritySnapshotBundleLocker).LockAuthoritySnapshotBundle(ctx, b); err != nil {
					return err
				}
				presented, err := readUserAuthorityPresentation(ctx, sc.tx, s.dia)
				if err != nil {
					return err
				}
				if presented != tenant.String() {
					t.Errorf("presentation=%s", presented)
				}
				if engine == store.EngineSQLite {
					var protocol string
					var gen int64
					if err := sc.tx.QueryRowContext(ctx, "SELECT coverage_protocol,generation FROM main.directory_writer_marker").Scan(&protocol, &gen); err != nil {
						return err
					}
					if protocol != coverageProtocolTarget || gen != 2 {
						t.Error("H binding erased writer marker")
					}
				}
				_, _, err = confined.Identities().List(ctx, model.Query{})
				if !errors.Is(err, store.ErrWorkspaceLineageRequired) {
					t.Errorf("tenant repository widened: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// A raw scope exposing only the bundle must not acquire other
			// optional capabilities from the underlying SQL implementation.
			if err := s.Mutate(ctx, tenant, func(raw store.Scope) error {
				thin := struct {
					store.Scope
					store.AuthoritySnapshotBundleLocker
				}{raw, raw.(store.AuthoritySnapshotBundleLocker)}
				ws, err := raw.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				confined, err := store.ConfineWorkspace(ctx, thin, ws.ID)
				if err != nil {
					return err
				}
				if _, ok := confined.(store.TransactionLocker); ok {
					t.Error("manufactured transaction locker")
				}
				if _, ok := confined.(store.AuthoritySnapshotLocker); ok {
					t.Error("manufactured legacy authority locker")
				}
				if _, ok := confined.(store.DirectorySnapshotReader); ok {
					t.Error("manufactured directory reader")
				}
				return confined.(store.AuthoritySnapshotBundleLocker).LockAuthoritySnapshotBundle(ctx, b)
			}); err != nil {
				t.Fatal(err)
			}
			invalid := []store.AuthoritySnapshotBundle{
				{UserAuthorities: b.UserAuthorities},
				{Facts: append(b.Facts, make([]store.AuthorizationFactRef, 64)...), UserAuthorities: b.UserAuthorities},
				{Facts: b.Facts, UserAuthorities: []store.UserAuthorityFactRef{{UserID: users[0].ID, Version: 1}, {UserID: users[0].ID, Version: 2}}},
				{Facts: []store.AuthorizationFactRef{{Kind: model.UserAuthorityKind, ID: users[0].ID, Version: 1}}, UserAuthorities: b.UserAuthorities},
				{Facts: b.Facts, UserAuthorities: []store.UserAuthorityFactRef{{UserID: model.ID(model.SystemTenantID), Version: 1}}},
			}
			for i, bad := range invalid {
				rebound := false
				userAuthorityRestoreTestHook = func() error { rebound = true; return nil }
				err := s.Mutate(ctx, tenant, func(sc store.Scope) error {
					locker := sc.(store.AuthoritySnapshotBundleLocker)
					err := locker.LockAuthoritySnapshotBundle(ctx, bad)
					if err != nil {
						if second := locker.LockAuthoritySnapshotBundle(ctx, b); second == nil {
							t.Error("failed bundle acquisition was reusable")
						}
					}
					return err
				})
				userAuthorityRestoreTestHook = nil
				if err == nil || rebound {
					t.Fatalf("invalid grammar %d reached SYSTEM=%t err=%v", i, rebound, err)
				}
			}
			if err := s.View(ctx, tenant, func(sc store.Scope) error {
				err := sc.(store.AuthoritySnapshotBundleLocker).LockAuthoritySnapshotBundle(ctx, b)
				if !errors.Is(err, store.ErrReadOnly) {
					t.Errorf("View=%v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// A restore failure poisons the envelope even if the callback ignores it.
			injected := errors.New("injected restoration failure")
			userAuthorityRestoreTestHook = func() error { return injected }
			var created model.Workspace
			err := s.Mutate(ctx, tenant, func(raw store.Scope) error {
				var err error
				created, err = raw.Workspaces().Create(ctx, model.Workspace{Name: "rollback", Slug: "rollback"})
				if err != nil {
					return err
				}
				_ = raw.(store.AuthoritySnapshotBundleLocker).LockAuthoritySnapshotBundle(ctx, b)
				return nil
			})
			userAuthorityRestoreTestHook = nil
			if !errors.Is(err, injected) {
				t.Fatalf("ignored restore error committed: %v", err)
			}
			if err := s.View(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.Workspaces().Get(ctx, created.ID)
				if !errors.Is(err, store.ErrNotFound) {
					t.Errorf("restore poison retained source: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserAuthorityBundleWriterRaces(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, writerFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/writer_first_%t", engine, writerFirst), func(t *testing.T) {
				s, _, users, tenants := f2aFreshTarget(t, engine)
				bundle := f2aBundle(tenants[0], users[0].ID)
				// The budget is for the RACE, not for the fixture: a fresh target under -race on a
				// loaded runner spends most of 15 s in Open, the compiled migration plan and the
				// seed writes, so a context started before it measured scheduling, not the lock
				// handoff — the sqlite subtests ended at 15–17 s with `context deadline exceeded`
				// before `held` on runs 34967128097 and 35004444187 (race-core p2), no DATA RACE.
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				held := make(chan struct{})
				release := make(chan struct{})
				first := make(chan error, 1)
				second := make(chan error, 1)
				started := make(chan struct{})
				writer := func(hold bool) error {
					return s.AuthMutate(ctx, func(as store.AuthScope) error {
						u := users[0]
						u.DisplayName = "raced"
						if _, err := as.Users().Update(ctx, u); err != nil {
							return err
						}
						if hold {
							close(held)
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
						}
						return nil
					})
				}
				keeper := func(hold bool) error {
					return s.Mutate(ctx, tenants[0], func(sc store.Scope) error {
						if err := sc.(store.AuthoritySnapshotBundleLocker).LockAuthoritySnapshotBundle(ctx, bundle); err != nil {
							return err
						}
						if hold {
							close(held)
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
						}
						return nil
					})
				}
				if writerFirst {
					go func() { first <- writer(true) }()
				} else {
					go func() { first <- keeper(true) }()
				}
				select {
				case <-held:
				case err := <-first:
					t.Fatalf("first failed before lock: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				go func() {
					close(started)
					if writerFirst {
						second <- keeper(false)
					} else {
						second <- writer(false)
					}
				}()
				<-started
				select {
				case err := <-second:
					close(release)
					t.Fatalf("second completed while H held: %v", err)
				case <-time.After(150 * time.Millisecond):
				}
				close(release)
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				err := <-second
				if writerFirst && !errors.Is(err, store.ErrConflict) {
					t.Fatalf("W-first stale bundle=%v", err)
				}
				if !writerFirst && err != nil {
					t.Fatalf("K-first writer=%v", err)
				}
				if got := F2AUserAuthorityVersionForTest(t, s, users[0].ID); got != 2 {
					t.Fatalf("raced H=%d", got)
				}
				if err := keeper(false); !errors.Is(err, store.ErrConflict) {
					t.Fatalf("old H remained pinnable: %v", err)
				}
			})
		}
	}
}
