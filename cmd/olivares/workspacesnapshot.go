// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

type workspaceSnapshotAuthority struct {
	data       store.Store
	principals *auth.Authenticator
	authz      *auth.Authorizer
	scopes     *sourcescope.Resolver
}

// Rehydrate the exact authenticated credential and use the existing authorizers.
// A permitted synthetic principal, old credential or unavailable policy evidence
// cannot become registered-folder snapshot authority.
func (a workspaceSnapshotAuthority) Check(ctx context.Context, tenant model.TenantID, supplied auth.Principal, ref string, registration model.ID) (string, error) {
	if a.data == nil || a.principals == nil || a.authz == nil || a.scopes == nil || registration.IsZero() {
		return "", store.ErrConflict
	}
	credential, valid := supplied.Ref()
	if !valid {
		return "", store.ErrConflict
	}
	principal, err := a.principals.ResolvePrincipalScope(ctx, credential, tenant)
	if err != nil {
		return "", err
	}
	before, err := a.scopes.ReadAuthorityVersion(ctx, tenant, principal, sourcescope.SourceData, ref)
	if err != nil {
		return "", err
	}
	question := auth.Request{
		Principal: principal, Tenant: tenant, Permission: "sessions:workspace:read",
		Resource: auth.ResourceAttrs{Kind: "sessions.workspace", ID: registration.String()},
	}
	receipt, err := a.authz.DecideRouteRead(ctx, question)
	if err != nil || !receipt.Allowed() {
		return "", store.ErrConflict
	}
	decision, err := a.scopes.ResolveForAgent(ctx, tenant, principal, "", sourcescope.SourceData, ref)
	if err != nil || !decision.Allowed {
		return "", store.ErrConflict
	}
	after, err := a.scopes.ReadAuthorityVersion(ctx, tenant, principal, sourcescope.SourceData, ref)
	if err != nil || before != after {
		return "", store.ErrConflict
	}
	var bundle store.AuthoritySnapshotBundle
	// Revalidate complete native evidence and its horizon at database time after
	// the source walk/checks. Rehydration at entry cannot extend an expired proof.
	err = a.data.View(ctx, tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return store.ErrConflict
		}
		reader, ok := sc.(store.AuthoritySnapshotBundleReader)
		if !ok {
			return store.ErrConflict
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil || now.IsZero() {
			return store.ErrConflict
		}
		bundle, err = receipt.AuthorityFor(now.Time(), question)
		if err != nil {
			return err
		}
		if err := reader.ValidateAuthoritySnapshotBundle(ctx, bundle); err != nil {
			return err
		}
		current, err := sourcescope.SourceAuthorityVersion(ctx, sc, principal, sourcescope.SourceData, ref)
		if err != nil || current != before {
			return store.ErrConflict
		}
		now, err = clock.TransactionNow(ctx)
		if err != nil || now.IsZero() {
			return store.ErrConflict
		}
		_, err = receipt.AuthorityFor(now.Time(), question)
		return err
	})
	if err != nil {
		return "", err
	}
	// Bind the current principal's effective values as well as policy versions:
	// an authority change that still happens to permit this read is a new source.
	role, _ := principal.RoleIn(tenant)
	groups := principal.GroupsIn(tenant)
	sort.Strings(groups)
	workspace, _ := principal.ConfinedWorkspaceIn(tenant)
	permissions, _ := principal.PurposePermissionsIn(tenant)
	audiences := principal.Audiences()
	sort.Strings(audiences)
	actAs, _ := principal.ActAs()
	floor, _ := principal.RetirementFloor(tenant)
	raw, err := json.Marshal(struct {
		Source      string
		Facts       []store.AuthorizationFactRef
		Users       []store.UserAuthorityFactRef
		Principal   auth.Principal
		Role        string
		Groups      []string
		Workspace   model.ID
		Permissions []auth.Permission
		Audiences   []string
		ActAs       model.ID
		Scope       model.TenantID
		Floor       int64
	}{before, bundle.Facts, bundle.UserAuthorities, principal, role, groups, workspace, permissions, audiences, actAs, principal.SessionScope(), floor})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
