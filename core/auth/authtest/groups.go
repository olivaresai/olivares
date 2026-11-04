// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package authtest writes the directory state a Community suite needs to read: a stored
// group, its roster, its nesting and its place. Community stores and enforces such groups
// (their role mapping, their nesting, their members' access) but has no writer of them: the
// engine that creates and edits them is Business. A fixture writes the rows through the
// store, the way a directory would have left them, and nothing more.
package authtest

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Group stores a group of tenant holding members. Its origin is the one a directory
// push leaves (provisioned_by empty), and members that are no tenant members are the
// caller's to avoid: nothing here validates them. The result has the shape the group
// reads of Community return, so a test reads it the same way.
func Group(t testing.TB, st store.Store, tenant model.TenantID, name, externalID string, members ...model.ID) auth.SCIMGroup {
	t.Helper()
	ctx := context.Background()
	var out auth.SCIMGroup
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Create(ctx, model.UserGroup{TargetTenantID: tenant, DisplayName: name, ExternalID: externalID})
		if err != nil {
			return err
		}
		out.Group = g
		for _, uid := range members {
			if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g.ID, UserID: uid}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("authtest: store group %q: %v", name, err)
	}
	return out
}

// Edit applies change to the stored group id and returns the result.
func Edit(t testing.TB, st store.Store, id model.ID, change func(*model.UserGroup)) model.UserGroup {
	t.Helper()
	ctx := context.Background()
	var out model.UserGroup
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Get(ctx, id)
		if err != nil {
			return err
		}
		change(&g)
		out, err = as.Groups().Update(ctx, g)
		return err
	})
	if err != nil {
		t.Fatalf("authtest: edit group %s: %v", id, err)
	}
	return out
}

// Nest stores parent as the group the group id is nested under; a zero parent un-nests it.
func Nest(t testing.TB, st store.Store, id, parent model.ID) {
	t.Helper()
	Edit(t, st, id, func(g *model.UserGroup) { g.ParentGroupID = parent })
}

// SetMembers replaces the roster of the stored group id with members.
func SetMembers(t testing.TB, st store.Store, id model.ID, members ...model.ID) {
	t.Helper()
	ctx := context.Background()
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := deleteRoster(ctx, as, id); err != nil {
			return err
		}
		for _, uid := range members {
			if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: id, UserID: uid}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("authtest: set roster of group %s: %v", id, err)
	}
}

// Delete removes the stored group id with its roster.
func Delete(t testing.TB, st store.Store, id model.ID) {
	t.Helper()
	ctx := context.Background()
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := deleteRoster(ctx, as, id); err != nil {
			return err
		}
		return as.Groups().Delete(ctx, id)
	})
	if err != nil {
		t.Fatalf("authtest: delete group %s: %v", id, err)
	}
}

func deleteRoster(ctx context.Context, as store.AuthScope, group model.ID) error {
	rows, _, err := as.GroupMembers().List(ctx, model.Query{Filters: []model.Filter{
		{Column: "group_id", Op: model.OpEq, Value: group.String()},
	}})
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := as.GroupMembers().Delete(ctx, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// WriteUser applies change to the stored account id, as a directory write leaves it.
func WriteUser(ctx context.Context, st store.Store, id model.ID, change func(*model.User)) error {
	return st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		change(&u)
		_, err = as.Users().Update(ctx, u)
		return err
	})
}

// EditUser is WriteUser for a test goroutine: a failure fails the test.
func EditUser(t testing.TB, st store.Store, id model.ID, change func(*model.User)) {
	t.Helper()
	if err := WriteUser(context.Background(), st, id, change); err != nil {
		t.Fatalf("authtest: edit user %s: %v", id, err)
	}
}

// Read returns the stored group id of tenant with its resolved members, or fails the
// test when the tenant holds no such group.
func Read(t testing.TB, a *auth.Authenticator, tenant model.TenantID, id model.ID) auth.SCIMGroup {
	t.Helper()
	groups, err := a.SCIMListGroups(context.Background(), tenant)
	if err != nil {
		t.Fatalf("authtest: list groups: %v", err)
	}
	for _, g := range groups {
		if g.Group.ID == id {
			return g
		}
	}
	t.Fatalf("authtest: tenant %s holds no group %s", tenant, id)
	return auth.SCIMGroup{}
}
