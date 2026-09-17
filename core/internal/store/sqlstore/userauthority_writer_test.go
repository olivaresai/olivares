// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func f2aFreshTarget(t *testing.T, engine store.Engine) (*sqlStore, store.Config, []model.User, []model.TenantID) {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "authority.db")}
	if engine == store.EnginePostgres {
		pg := isolatedPGSplit(t)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
	}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	tenants := []model.TenantID{provisionTenant(t, raw, "authority-a"), provisionTenant(t, raw, "authority-b"), provisionTenant(t, raw, "authority-c")}
	var users []model.User
	if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
		for i := 0; i < 3; i++ {
			u, err := as.Users().Create(ctx, model.User{Email: fmt.Sprintf("authority-%d@example.test", i), Status: model.StatusActive})
			if err != nil {
				return err
			}
			users = append(users, u)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err != nil {
		t.Fatal(err)
	}
	raw, err = Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw.(*sqlStore), cfg, users, tenants
}

func TestUserAuthorityWritersAndOrdering(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			wantH := []int64{1, 1, 1}
			check := func() {
				t.Helper()
				for i, u := range users {
					if h := F2AUserAuthorityVersionForTest(t, s, u.ID); h != wantH[i] {
						t.Fatalf("H[%d]=%d want%d", i, h, wantH[i])
					}
				}
			}
			// Lock intent is finite, sorted and idempotent; it changes no H.
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				w := as.(store.AuthUserAuthorityWriter)
				if err := w.PrepareUserAuthorityWrite(ctx, []model.ID{users[1].ID, users[0].ID, users[1].ID}); err != nil {
					return err
				}
				return w.PrepareUserAuthorityWrite(ctx, []model.ID{users[0].ID})
			}); err != nil {
				t.Fatal(err)
			}
			check()
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				users[0].DisplayName = "changed"
				u, err := as.Users().Update(ctx, users[0])
				users[0] = u
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantH[0]++
			check()
			var session model.AuthSession
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				var err error
				session, err = as.Sessions().Create(ctx, model.AuthSession{UserID: users[0].ID, Selector: "h-session", SecretHash: []byte("test-credential"), ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour))})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantH[0]++
			check()
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				session.ExpiresAt = model.NewTimestamp(session.ExpiresAt.Time().Add(time.Hour))
				var err error
				session, err = as.Sessions().Update(ctx, session)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			check() // Pure expiry extension preserves earlier evidence's shorter window.
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				session.UserID = users[1].ID
				session.AMR = []string{"mfa"}
				session.AAL = 2
				var err error
				session, err = as.Sessions().Update(ctx, session)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantH[0]++
			wantH[1]++
			check()
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				session.Revoked = true
				var err error
				session, err = as.Sessions().Update(ctx, session)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantH[1]++
			check()
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error { return as.Sessions().Delete(ctx, session.ID) }); err != nil {
				t.Fatal(err)
			}
			wantH[1]++
			check()
			for _, tenant := range tenants {
				if g := f2aEpochForTest(t, s, tenant); g != 2 {
					t.Fatalf("ordinary User/session writer fanned out G=%d want2", g)
				}
			}
			// A compound caller ignoring the late-H error must lose its membership
			// write and G bump too. No independently staged partial effect survives.
			err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				if _, err := as.Memberships().Create(ctx, model.Membership{UserID: users[0].ID, TargetTenantID: tenants[0], Role: "viewer"}); err != nil {
					return err
				}
				_, err := as.Users().Update(ctx, users[2])
				if !errors.Is(err, errUserAuthorityOrder) {
					t.Errorf("late H error=%v", err)
				}
				return nil
			})
			if !errors.Is(err, errUserAuthorityOrder) {
				t.Fatalf("ignored late-H error committed: %v", err)
			}
			if g := f2aEpochForTest(t, s, tenants[0]); g != 2 {
				t.Fatalf("poison left G=%d", g)
			}
			check()
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				rows, _, err := as.Memberships().List(ctx, model.Query{})
				if len(rows) != 0 {
					t.Errorf("poison left membership rows")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Declaring the same finite H set first makes the actual compound write valid.
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				if err := as.(store.AuthUserAuthorityWriter).PrepareUserAuthorityWrite(ctx, []model.ID{users[2].ID, users[0].ID}); err != nil {
					return err
				}
				if _, err := as.Memberships().Create(ctx, model.Membership{UserID: users[0].ID, TargetTenantID: tenants[0], Role: "viewer"}); err != nil {
					return err
				}
				var err error
				users[2], err = as.Users().Update(ctx, users[2])
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantH[2]++
			check()
			// Retirement retains H and invalidates only actual structural G.
			if _, err := RetireUser(ctx, s, UserRetirementRequest{UserID: users[0].ID, ExpectedVersion: users[0].Version, Actor: model.ActorSystem, ActorKind: model.ActorSystem}); err != nil {
				t.Fatalf("retire: %v", err)
			}
			wantH[0]++
			check()
			if g := f2aEpochForTest(t, s, tenants[0]); g != 4 {
				t.Fatalf("structural retirement G=%d want4", g)
			}
			for _, tenant := range tenants[1:] {
				if g := f2aEpochForTest(t, s, tenant); g != 2 {
					t.Fatalf("unaffected retirement G=%d want2", g)
				}
			}
			err = s.AuthMutate(ctx, func(as store.AuthScope) error {
				a := as.(*authScope)
				return a.ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) {
					return nil, a.ts.directoryWriter.insertUserAuthority(ctx, users[0].ID)
				})
			})
			if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("retired H identity reused: %v", err)
			}
			// MaxInt64 must deny the source mutation, not wrap to a valid small H.
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				a := as.(*authScope)
				w := a.ts.directoryWriter
				if err := w.prepare(ctx, func() ([]model.TenantID, error) { return nil, w.lockUserAuthorities(ctx, []model.ID{users[1].ID}) }); err != nil {
					return err
				}
				_, err := a.ts.tx.ExecContext(ctx, s.dia.Rebind("UPDATE core_user_authority SET version=? WHERE id=?"), int64(math.MaxInt64), users[1].ID.String())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			err = s.AuthMutate(ctx, func(as store.AuthScope) error { _, err := as.Users().Update(ctx, users[1]); return err })
			if err == nil {
				t.Fatal("H overflow admitted User update")
			}
			if h := F2AUserAuthorityVersionForTest(t, s, users[1].ID); h != math.MaxInt64 {
				t.Fatalf("H overflow changed version=%d", h)
			}
		})
	}
}

func TestUserAuthorityLegacyLateAbsencePoisonsTransaction(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			cfg, f, _ := F2AOldFixtureForTest(t, engine, "staged", false)
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			err = raw.AuthMutate(ctx, func(as store.AuthScope) error {
				if _, err := as.Memberships().Create(ctx, model.Membership{UserID: f.Users[1].ID, TargetTenantID: f.Tenants[0], Role: "viewer"}); err != nil {
					return err
				}
				if err := as.(store.AuthUserAuthorityWriter).PrepareUserAuthorityWrite(ctx, []model.ID{f.Users[0].ID}); !errors.Is(err, errUserAuthorityOrder) {
					t.Errorf("late absence reservation=%v", err)
				}
				return nil
			})
			if !errors.Is(err, errUserAuthorityOrder) {
				t.Fatalf("ignored late legacy absence committed: %v", err)
			}
			for _, u := range f.Users {
				if h := F2AUserAuthorityVersionForTest(t, raw, u.ID); h != 0 {
					t.Fatalf("late reservation created H=%d", h)
				}
			}
			for _, tenant := range f.Tenants {
				if g := f2aEpochForTest(t, raw, tenant); g != 2 {
					t.Fatalf("poison left G=%d", g)
				}
			}
			if err := raw.AuthView(ctx, func(as store.AuthScope) error {
				rows, _, err := as.Memberships().List(ctx, model.Query{})
				if len(rows) != 2 {
					t.Errorf("poison left membership count=%d", len(rows))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
