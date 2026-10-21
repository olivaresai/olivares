// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Directory fixtures. The inbound SCIM server is a Business capability, so a Community
// suite that needs a provisioned account, an external identity, a directory group or a
// leaver writes them through the account engine in core/auth: the code the SCIM handlers
// called, with the same actor kind (a tenant-bound administrator token), the same audit
// entries and the same retirement steps.

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

// deprovisionMember removes a member from tenant through the account engine: the leaver
// path. The engine treats a departed or unknown account as a no-op, so a fixture that means
// to remove a member asserts first that it is one.
func deprovisionMember(t *testing.T, authr *auth.Authenticator, actor auth.Principal, tenant model.TenantID, user model.ID) {
	t.Helper()
	ctx := context.Background()
	if _, err := authr.SCIMGetMember(ctx, tenant, user); err != nil {
		t.Fatalf("deprovision: %s is not a member of the tenant: %v", user, err)
	}
	if err := authr.SCIMDeprovisionUser(ctx, actor, tenant, user); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
}

// patchDirectoryUserErr changes some attributes of a tenant member and keeps the rest.
func patchDirectoryUserErr(authr *auth.Authenticator, actor auth.Principal, tenant model.TenantID, id model.ID, change func(*auth.SCIMUserInput)) error {
	ctx := context.Background()
	cur, err := authr.SCIMGetMember(ctx, tenant, id)
	if err != nil {
		return err
	}
	in := auth.SCIMUserInput{
		UserName: cur.Email, ExternalID: cur.ExternalID, DisplayName: cur.DisplayName, Active: true,
		EmployeeNumber: cur.EmployeeNumber, Department: cur.Department, Manager: cur.Manager,
	}
	change(&in)
	_, err = authr.SCIMUpdateUser(ctx, actor, tenant, id, in)
	return err
}

// setDirectoryUser applies a full directory attribute set to a tenant member, authenticated
// as the owner of token.
func setDirectoryUser(t *testing.T, eng *engine, token string, tenant model.TenantID, id model.ID, in auth.SCIMUserInput) {
	t.Helper()
	if _, err := eng.authr.SCIMUpdateUser(context.Background(), principalOfToken(t, eng.authr, token), tenant, id, in); err != nil {
		t.Fatalf("set directory attributes of %s: %v", id, err)
	}
}

// directoryGroupWithMember creates a real directory user-group holding member and returns its
// canonical id, so a second closure subject for the same principal is a directory fact and
// not a fixture row.
func directoryGroupWithMember(t *testing.T, eng *engine, token string, tenant model.TenantID, display string, member model.ID) model.ID {
	t.Helper()
	g, err := eng.authr.SCIMCreateGroup(context.Background(), principalOfToken(t, eng.authr, token), tenant,
		auth.SCIMGroupInput{DisplayName: display, Members: []model.ID{member}})
	if err != nil {
		t.Fatalf("create directory group %s: %v", display, err)
	}
	return g.Group.ID
}

// emptyDirectoryGroup removes every member of a directory group and keeps the group.
func emptyDirectoryGroup(t *testing.T, eng *engine, token string, tenant model.TenantID, group model.ID) {
	t.Helper()
	ctx := context.Background()
	cur, err := eng.authr.SCIMGetGroup(ctx, tenant, group)
	if err != nil {
		t.Fatalf("read directory group: %v", err)
	}
	in := auth.SCIMGroupInput{DisplayName: cur.Group.DisplayName, ExternalID: cur.Group.ExternalID}
	if _, err := eng.authr.SCIMReplaceGroup(ctx, principalOfToken(t, eng.authr, token), tenant, group, in, cur.Group.Version); err != nil {
		t.Fatalf("empty directory group: %v", err)
	}
}
