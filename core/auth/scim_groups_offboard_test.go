// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSCIMGroupCannotRestoreOffboardedSubject(t *testing.T) {
	for _, engine := range []struct {
		name string
		open func(*testing.T) store.Store
	}{
		{"sqlite", testStore},
		{"postgres", func(t *testing.T) store.Store { st, _ := openLoginCapabilityPostgres(t); return st }},
	} {
		t.Run(engine.name, func(t *testing.T) {
			ctx := t.Context()
			st := engine.open(t)
			a := auth.NewAuthenticator(st, nil)
			admin := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "fenced-scim")
			user, _ := mustMember(t, ctx, a, admin, tenant, "leaver@fenced-scim.test", auth.RoleViewer)
			active, _ := mustMember(t, ctx, a, admin, tenant, "active@fenced-scim.test", auth.RoleViewer)
			group, err := a.SCIMCreateGroup(ctx, admin, tenant, auth.SCIMGroupInput{DisplayName: "Existing roster"})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				if _, err := a.OffboardFromTenant(ctx, as, admin, user, tenant, "scim-group-fence"); err != nil {
					return err
				}
				// Surviving direct membership must not defeat the retirement fence.
				_, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: tenant, Role: auth.RoleViewer})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"create", "replace"} {
				t.Run(method, func(t *testing.T) {
					input := auth.SCIMGroupInput{DisplayName: "New roster", Members: []model.ID{user, active, user}}
					var result auth.SCIMGroup
					var err error
					if method == "create" {
						result, err = a.SCIMCreateGroup(ctx, admin, tenant, input)
					} else {
						input.DisplayName = group.Group.DisplayName
						result, err = a.SCIMReplaceGroup(ctx, admin, tenant, group.Group.ID, input, group.Group.Version)
					}
					if err != nil {
						t.Fatal(err)
					}
					if result.SkippedMembers != 1 || len(result.Members) != 1 || result.Members[0].ID != active {
						t.Fatalf("fenced roster result: skipped=%d members=%v; want one skip and only the active member", result.SkippedMembers, result.Members)
					}
					got, err := a.SCIMGetGroup(ctx, tenant, result.Group.ID)
					if err != nil || len(got.Members) != 1 || got.Members[0].ID != active {
						t.Fatalf("persisted roster restored the excluded subject: members=%v err=%v", got.Members, err)
					}
					if excluded, err := a.SubjectExcluded(ctx, tenant, user); err != nil || !excluded {
						t.Fatalf("roster write changed exclusion: excluded=%v err=%v", excluded, err)
					}
				})
			}
		})
	}
}

// The error seam uses the same live AuthScope; only the exclusion read fails.
type scimExclusionErrorStore struct {
	store.Store
	err error
}

func (s *scimExclusionErrorStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(&scimExclusionErrorScope{AuthScope: as, err: s.err})
	})
}

type scimExclusionErrorScope struct {
	store.AuthScope
	err error
}

func (s *scimExclusionErrorScope) PrepareUserAuthorityWrite(ctx context.Context, ids []model.ID) error {
	writer, ok := s.AuthScope.(store.AuthUserAuthorityWriter)
	if !ok {
		return store.ErrDirectoryUnavailable
	}
	return writer.PrepareUserAuthorityWrite(ctx, ids)
}
func (s *scimExclusionErrorScope) TenantExclusions() store.Repository[model.TenantExclusion] {
	return &scimExclusionErrorRows{Repository: s.AuthScope.TenantExclusions(), err: s.err}
}

type scimExclusionErrorRows struct {
	store.Repository[model.TenantExclusion]
	err error
}

func (r *scimExclusionErrorRows) List(context.Context, model.Query) ([]model.TenantExclusion, model.Page, error) {
	return nil, model.Page{}, r.err
}

func TestSCIMGroupFenceValidationFailureRollsBack(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			user := f.seed(t, true, false)
			a := auth.NewAuthenticator(f.raw, nil)
			group, err := a.SCIMCreateGroup(f.ctx, f.actor, f.tenant, auth.SCIMGroupInput{DisplayName: "Validation roster"})
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"create", "replace"} {
				for _, fault := range []string{"authority", "membership", "exclusion"} {
					t.Run(method+"/"+fault, func(t *testing.T) {
						before := f.state(t, user.ID)
						groups, err := a.SCIMListGroups(f.ctx, f.tenant)
						if err != nil {
							t.Fatal(err)
						}
						refused := errors.New("SCIM group validation unavailable")
						var wrapped store.Store
						switch fault {
						case "authority":
							wrapped = &scimGuardStore{Store: f.raw, prepareErr: refused}
						case "membership":
							wrapped = &scimGuardStore{Store: f.raw, membershipErr: refused}
						case "exclusion":
							wrapped = &scimExclusionErrorStore{Store: f.raw, err: refused}
						}
						guarded := auth.NewAuthenticator(wrapped, nil)
						input := auth.SCIMGroupInput{DisplayName: "Changed roster", Members: []model.ID{user.ID}}
						if method == "create" {
							_, err = guarded.SCIMCreateGroup(f.ctx, f.actor, f.tenant, input)
						} else {
							_, err = guarded.SCIMReplaceGroup(f.ctx, f.actor, f.tenant, group.Group.ID, input, group.Group.Version)
						}
						if !errors.Is(err, refused) {
							t.Fatalf("validation error=%v; want injected refusal", err)
						}
						after, err := a.SCIMListGroups(f.ctx, f.tenant)
						if err != nil || !reflect.DeepEqual(groups, after) || !reflect.DeepEqual(before, f.state(t, user.ID)) {
							t.Fatalf("failed validation changed roster, authority, or audit: err=%v", err)
						}
					})
				}
			}
		})
	}
}

func TestSCIMGroupMemberValidationSerializesOffboard(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			user := f.seed(t, true, false)
			a := auth.NewAuthenticator(f.raw, nil)
			group, err := a.SCIMCreateGroup(f.ctx, f.actor, f.tenant, auth.SCIMGroupInput{DisplayName: "Serialized roster"})
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"create", "replace"} {
				t.Run(method, func(t *testing.T) {
					reads := 0
					wrapped := &scimGuardStore{Store: f.raw, onMemberRead: func() {
						reads++
						f.probeWriter(t, false)
					}}
					guarded := auth.NewAuthenticator(wrapped, nil)
					input := auth.SCIMGroupInput{DisplayName: "Serialized roster new", Members: []model.ID{user.ID}}
					f.probeWriter(t, true)
					if method == "create" {
						_, err = guarded.SCIMCreateGroup(f.ctx, f.actor, f.tenant, input)
					} else {
						input.DisplayName = group.Group.DisplayName
						_, err = guarded.SCIMReplaceGroup(f.ctx, f.actor, f.tenant, group.Group.ID, input, group.Group.Version)
					}
					if err != nil {
						t.Fatal(err)
					}
					if reads == 0 {
						t.Fatal("SCIM group did not corroborate current membership")
					}
					f.probeWriter(t, true)
				})
			}
		})
	}
}
