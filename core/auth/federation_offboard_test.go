// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The decorator commits the real offboard after the real SSO session commit,
// before CompleteSSO continues. No nested transaction or production hook is used.
type offboardAfterSSOMintStore struct {
	store.Store
	user      model.ID
	afterMint func() error
}

func (s *offboardAfterSSOMintStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	if err := s.Store.AuthMutate(ctx, fn); err != nil {
		return err
	}
	if s.afterMint == nil {
		return nil
	}
	var minted bool
	if err := s.Store.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Sessions().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: s.user.String()},
		}, Limit: 100})
		for _, row := range rows {
			minted = minted || slices.Contains(row.AMR, "sso")
		}
		return err
	}); err != nil {
		return err
	}
	if minted {
		callback := s.afterMint
		s.afterMint = nil
		return callback()
	}
	return nil
}

func TestSSOLateGroupAssertionCannotRestoreOffboardedSubject(t *testing.T) {
	for _, engine := range []struct {
		name string
		open func(*testing.T) store.Store
	}{
		{"sqlite", testStore},
		{"postgres", func(t *testing.T) store.Store { st, _ := openLoginCapabilityPostgres(t); return st }},
	} {
		t.Run(engine.name, func(t *testing.T) {
			for _, survivingMembership := range []bool{false, true} {
				t.Run(map[bool]string{false: "membership removed", true: "unlifted fence with membership"}[survivingMembership], func(t *testing.T) {
					ctx := t.Context()
					f := newSSOCompletionFixture(t, engine.open(t))
					adminToken, _, err := f.a.Login(ctx, "root@example.com", "bootstrap-pass-123", ssoCompletionIP)
					if err != nil {
						t.Fatal(err)
					}
					admin, err := f.a.Authenticate(ctx, adminToken)
					if err != nil {
						t.Fatal(err)
					}
					identity := f.identity(ssoCompletionIssuer, "eng-sub")
					// Exact subject correlation is already established; the only delayed
					// completion effect under test is the asserted group addition.
					if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
						u, err := as.Users().Get(ctx, f.userID)
						if err != nil {
							return err
						}
						u.SsoSubject = identity.QualifiedSubject()
						_, err = as.Users().Update(ctx, u)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					var fenced ssoCompletionState
					decorated := &offboardAfterSSOMintStore{Store: f.st, user: f.userID}
					decorated.afterMint = func() error {
						err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
							if _, err := f.a.OffboardFromTenant(ctx, as, admin, f.userID, f.tenant, "late-sso-assertion"); err != nil {
								return err
							}
							if survivingMembership {
								// Deliberately inconsistent standing proves the exclusion
								// check independently of the direct-membership check.
								_, err := as.Memberships().Create(ctx, model.Membership{UserID: f.userID, TargetTenantID: f.tenant, Role: auth.RoleViewer})
								return err
							}
							return nil
						})
						if err == nil {
							fenced = f.state(t)
						}
						return err
					}
					f.a = auth.NewAuthenticator(decorated, nil).WithGroupMapper(fakeGroupMapper{})
					token, _, err := f.a.CompleteSSO(ctx, identity, ssoCompletionIP, f.tenant, false)
					if err != nil || token == "" || decorated.afterMint != nil {
						t.Fatalf("SSO did not reach the committed-session/offboard boundary: %v", err)
					}
					if after := f.state(t); after != fenced || after.memberships != 0 {
						t.Fatalf("late assertion changed fenced subject: %+v -> %+v", fenced, after)
					}
					if _, err := f.a.Authenticate(ctx, token); err == nil {
						t.Fatal("the offboarded SSO session still authenticates")
					}
				})
			}
		})
	}
}
