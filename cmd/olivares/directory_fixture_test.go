// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/auth/authtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Directory fixtures. The inbound SCIM server and the account engine it drives are Business, so
// a Community suite that needs an external identity, a directory group or a leaver writes them
// the way a directory leaves them: a member leaves through the member removal Community serves
// (the same offboard and retirement steps), and attributes and groups are rows in the store.

// directoryAttrs is the attribute set a directory leaves on an account.
type directoryAttrs struct {
	UserName, ExternalID, DisplayName   string
	Active                              bool
	EmployeeNumber, Department, Manager string
}

// principalOfToken authenticates a bearer token.
func principalOfToken(t *testing.T, authr *auth.Authenticator, token string) auth.Principal {
	t.Helper()
	p, err := authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

// isDirectoryRefusal reports whether err is a refusal a directory write may meet (the
// member is gone, the account is not governed by the tenant alone, the address's domain
// is claimed elsewhere, its removal has not completed) and not a server fault.
func isDirectoryRefusal(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, auth.ErrNotTenantGoverned) ||
		errors.Is(err, auth.ErrForeignDomain) || errors.Is(err, auth.ErrRetirementPending)
}

// deprovisionMember removes a member from tenant through the member removal: the leaver
// path. A fixture that means to remove a member asserts first that it is one.
func deprovisionMember(t *testing.T, authr *auth.Authenticator, actor auth.Principal, tenant model.TenantID, user model.ID) {
	t.Helper()
	ctx := context.Background()
	if _, err := authr.SCIMGetMember(ctx, tenant, user); err != nil {
		t.Fatalf("deprovision: %s is not a member of the tenant: %v", user, err)
	}
	if err := authr.RemoveMember(ctx, actor, tenant, user); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
}

// patchDirectoryUserErr changes some attributes of a tenant member and keeps the rest;
// active=false removes the member from the tenant.
func patchDirectoryUserErr(st store.Store, authr *auth.Authenticator, actor auth.Principal, tenant model.TenantID, id model.ID, change func(*directoryAttrs)) error {
	ctx := context.Background()
	cur, err := authr.SCIMGetMember(ctx, tenant, id)
	if err != nil {
		return err
	}
	in := directoryAttrs{
		UserName: cur.Email, ExternalID: cur.ExternalID, DisplayName: cur.DisplayName, Active: true,
		EmployeeNumber: cur.EmployeeNumber, Department: cur.Department, Manager: cur.Manager,
	}
	change(&in)
	if !in.Active {
		return authr.RemoveMember(ctx, actor, tenant, id)
	}
	return authtest.WriteUser(ctx, st, id, func(u *model.User) {
		u.ExternalID, u.DisplayName = in.ExternalID, in.DisplayName
		u.EmployeeNumber, u.Department, u.Manager = in.EmployeeNumber, in.Department, in.Manager
	})
}

// setDirectoryUser applies a full directory attribute set to a tenant member.
func setDirectoryUser(t *testing.T, eng *engine, token string, tenant model.TenantID, id model.ID, in directoryAttrs) {
	t.Helper()
	if err := patchDirectoryUserErr(eng.store, eng.authr, principalOfToken(t, eng.authr, token), tenant, id, func(cur *directoryAttrs) { *cur = in }); err != nil {
		t.Fatalf("set directory attributes of %s: %v", id, err)
	}
}

// directoryGroupWithMember stores a directory user-group holding member and returns its
// canonical id, so a second closure subject for the same principal is a directory fact and
// not a fixture row.
func directoryGroupWithMember(t *testing.T, eng *engine, token string, tenant model.TenantID, display string, member model.ID) model.ID {
	t.Helper()
	return authtest.Group(t, eng.store, tenant, display, "", member).Group.ID
}

// emptyDirectoryGroup removes every member of a directory group and keeps the group.
func emptyDirectoryGroup(t *testing.T, eng *engine, token string, tenant model.TenantID, group model.ID) {
	t.Helper()
	authtest.SetMembers(t, eng.store, group)
}
