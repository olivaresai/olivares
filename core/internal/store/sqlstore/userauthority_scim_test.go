// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestUserAuthoritySCIMCompound(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, target := range []bool{false, true} {
			for _, orphan := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/target_%t/orphan_%t", engine, target, orphan), func(t *testing.T) {
					ctx := context.Background()
					cfg, f, _ := sqlstore.F2AOldFixtureForTest(t, engine, "staged", false)
					raw, err := sqlstore.Open(ctx, cfg, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = raw.Close() }()
					u := f.Users[0]
					// Predeclaration of a legacy absence is not a source write or an H fact.
					if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
						w := as.(store.AuthUserAuthorityWriter)
						if err := w.PrepareUserAuthorityWrite(ctx, []model.ID{u.ID, u.ID}); err != nil {
							return err
						}
						return w.PrepareUserAuthorityWrite(ctx, []model.ID{u.ID})
					}); err != nil {
						t.Fatal(err)
					}
					if got := sqlstore.F2AUserAuthorityVersionForTest(t, raw, u.ID); got != 0 {
						t.Fatalf("predeclaration created H=%d", got)
					}
					var groupMember model.UserGroupMember
					var tokens []model.APIToken
					if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
						if !orphan {
							if _, err := as.Memberships().Create(ctx, model.Membership{UserID: u.ID, TargetTenantID: f.Tenants[1], Role: auth.RoleViewer}); err != nil {
								return err
							}
						}
						group, err := as.Groups().Create(ctx, model.UserGroup{TargetTenantID: f.Tenants[0], DisplayName: "SCIM leaver"})
						if err != nil {
							return err
						}
						groupMember, err = as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: group.ID, UserID: u.ID})
						if err != nil {
							return err
						}
						parent, err := as.Tokens().Create(ctx, model.APIToken{UserID: u.ID, Name: "parent", Selector: "f2a-parent", SecretHash: []byte("fixture"), BoundTenantID: f.Tenants[0], Role: auth.RoleViewer})
						if err != nil {
							return err
						}
						child, err := as.Tokens().Create(ctx, model.APIToken{UserID: f.Users[1].ID, Name: "child", Selector: "f2a-child", SecretHash: []byte("fixture"), ParentTokenID: parent.ID, BoundTenantID: f.Tenants[0], Role: auth.RoleViewer})
						if err != nil {
							return err
						}
						tokens = []model.APIToken{parent, child}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if target {
						if err := raw.Close(); err != nil {
							t.Fatal(err)
						}
						if _, _, _, err := sqlstore.OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err != nil {
							t.Fatal(err)
						}
						raw, err = sqlstore.Open(ctx, cfg, nil)
						if err != nil {
							t.Fatal(err)
						}
					}
					beforeH := sqlstore.F2AUserAuthorityVersionForTest(t, raw, u.ID)
					a := auth.NewAuthenticator(raw, nil)
					actor := auth.Principal{Kind: auth.KindUser, UserID: f.Users[1].ID, CredID: f.Sessions[1].ID, Superadmin: true}
					if err := a.SCIMDeprovisionUser(ctx, actor, f.Tenants[0], u.ID); err != nil {
						t.Fatalf("compound SCIM: %v", err)
					}
					if err := raw.AuthView(ctx, func(as store.AuthScope) error {
						got, err := as.Users().Get(ctx, u.ID)
						if err != nil {
							return err
						}
						if (got.Status == model.StatusInactive) != orphan {
							t.Errorf("orphan=%t status=%s", orphan, got.Status)
						}
						session, err := as.Sessions().Get(ctx, f.Sessions[0].ID)
						if err != nil {
							return err
						}
						if session.Revoked != orphan {
							t.Errorf("orphan=%t revoked=%t", orphan, session.Revoked)
						}
						for _, tok := range tokens {
							got, err := as.Tokens().Get(ctx, tok.ID)
							if err != nil {
								return err
							}
							if !got.Revoked {
								t.Error("token cascade survived")
							}
						}
						rows, _, err := as.GroupMembers().List(ctx, model.Query{Filters: []model.Filter{{Column: "id", Op: model.OpEq, Value: groupMember.ID.String()}}})
						if err != nil {
							return err
						}
						if len(rows) != 0 {
							t.Error("SCIM group membership survived")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					gotH := sqlstore.F2AUserAuthorityVersionForTest(t, raw, u.ID)
					if orphan && gotH != beforeH+2 {
						t.Fatalf("orphan H=%d want%d (session revoke + User disable)", gotH, beforeH+2)
					}
					if !orphan && gotH != beforeH {
						t.Fatalf("non-orphan H changed %d -> %d", beforeH, gotH)
					}
					if err := a.SCIMDeprovisionUser(ctx, actor, f.Tenants[0], u.ID); err != nil {
						t.Fatalf("SCIM retry: %v", err)
					}
					if got := sqlstore.F2AUserAuthorityVersionForTest(t, raw, u.ID); got != gotH {
						t.Fatalf("SCIM retry bumped H=%d", got)
					}
				})
			}
		}
	}
}
