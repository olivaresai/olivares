// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// profileAccess is the sessions module's ProfileAccess: who may use which AI tool and
// provider profile is decided by the source-scoping bindings (source types "tool", by
// driver key, and "profile", by profile reference), so the department tree, groups,
// forbids and the inheritance filter are the ones every other source already obeys.
type profileAccess struct{ scopes *sourcescope.Resolver }

// each asks the resolver about the tool, then about the profile when one is named, until
// stop says so. The caller is the authenticated principal; a person has no acting agent, an
// agent's own scope is derived from its authenticated identity by the resolver.
func (a profileAccess) each(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string, stop func(sourcescope.Decision) bool) error {
	for _, source := range []struct{ kind, ref string }{{sourcescope.SourceTool, driver}, {sourcescope.SourceProfile, profileRef}} {
		if source.ref == "" {
			continue
		}
		decision, err := a.scopes.ResolveForAgent(ctx, tenant, principal, "", source.kind, source.ref)
		if err != nil || stop(decision) {
			return err
		}
	}
	return nil
}

// MayUse reports whether the tool, and the profile when one is named, are the caller's.
func (a profileAccess) MayUse(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error) {
	allowed := true
	err := a.each(ctx, tenant, principal, profileRef, driver, func(d sourcescope.Decision) bool {
		allowed = d.Allowed
		return !allowed
	})
	return allowed && err == nil, err
}

// Limited reports whether any assignment limits who may use the tool or the profile.
func (a profileAccess) Limited(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error) {
	limited := false
	err := a.each(ctx, tenant, principal, profileRef, driver, func(d sourcescope.Decision) bool {
		limited = d.Bound
		return limited
	})
	return limited && err == nil, err
}

// modelsAccess is who may see a source in the models list: a provider key is judged by the
// profiles that run on it (mayUseRecord, the sessions module's rule for the key's own routes),
// a tool account or a tool's login by the assignment (mayUse). With no way to ask about a key
// it is not shown.
func modelsAccess(
	mayUse func(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error),
	mayUseRecord func(ctx context.Context, tenant model.TenantID, principal auth.Principal, ref string) (bool, error),
) func(context.Context, model.TenantID, auth.Principal, models.AvailabilitySource) (bool, error) {
	return func(ctx context.Context, tenant model.TenantID, principal auth.Principal, source models.AvailabilitySource) (bool, error) {
		if source.ProviderRef == "" {
			return mayUse(ctx, tenant, principal, source.AccountRef, source.Driver)
		}
		if mayUseRecord == nil {
			return false, nil
		}
		return mayUseRecord(ctx, tenant, principal, source.ProviderRef)
	}
}
