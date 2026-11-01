// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"slices"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// What a department carries is assigned by source-scoping bindings (source types "instructions"
// and "template", modules/sourcescope standards.go) and is read from the session templates they
// name (modules/sessions department_standards.go). The two modules do not import each other, so
// the composition root joins them: sessions asks which department a launch belongs to and what is
// assigned to it, and the claude-policy console renders a department's instructions file from
// the very text a session of the department is given.

// departmentStandards is the sessions module's DepartmentAssignments.
type departmentStandards struct {
	scopes *sourcescope.Resolver
	st     store.Store
}

// DepartmentsOf is the organization-tree departments of the authenticated caller of a launch:
// the one an acting agent lives in or a confined membership names, and the workspace each of the
// caller's directory groups, and the groups above them, is filed in (PUT /v1/groups/{id}/workspace).
// That filing is organization only and no authorization decision reads it; it is how the
// organization says which department a group of people is.
func (d departmentStandards) DepartmentsOf(ctx context.Context, tenant model.TenantID, principal auth.Principal) ([]string, error) {
	var slugs []string
	own, err := d.scopes.DepartmentOf(ctx, tenant, principal)
	if err != nil {
		return nil, err
	}
	if own != "" {
		slugs = append(slugs, own)
	}
	groups := principal.GroupsIn(tenant)
	if len(groups) == 0 {
		return slugs, nil
	}
	var filed []model.ID
	if err := d.st.AuthView(ctx, func(as store.AuthScope) error {
		for _, id := range groups {
			group, err := as.Groups().Get(ctx, model.ID(id))
			if errors.Is(err, store.ErrNotFound) {
				continue // a group deleted while the principal was loaded files nowhere
			}
			if err != nil {
				return err
			}
			if group.TargetTenantID == tenant && !group.WorkspaceID.IsZero() {
				filed = append(filed, group.WorkspaceID)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(filed) > 0 {
		if err := d.st.View(ctx, tenant, func(sc store.Scope) error {
			for _, id := range filed {
				ws, err := sc.Workspaces().Get(ctx, id)
				if errors.Is(err, store.ErrNotFound) {
					continue // a group filed in a workspace that is gone is in no department
				}
				if err != nil {
					return err
				}
				slugs = append(slugs, ws.Slug)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	slices.Sort(slugs)
	return slices.Compact(slugs), nil
}

// AssignedTo lists the template ids assigned to the caller and the departments under kind, least
// specific first.
func (d departmentStandards) AssignedTo(ctx context.Context, tenant model.TenantID, principal auth.Principal, departments []string, kind string) ([]string, error) {
	return d.scopes.AssignedTo(ctx, tenant, principal, departments, kind)
}

// IsAssigned reports whether a department or group is given the template.
func (d departmentStandards) IsAssigned(ctx context.Context, tenant model.TenantID, template string) (bool, error) {
	return d.scopes.IsAssigned(ctx, tenant, template)
}

// DepartmentExists reports whether department is a node of the organization tree.
func (d departmentStandards) DepartmentExists(ctx context.Context, tenant model.TenantID, department string) (bool, error) {
	return d.scopes.DepartmentExists(ctx, tenant, department)
}

// departmentInstructions is the claude-policy console's InstructionsSource: the text a session of
// the department is given.
type departmentInstructions struct{ sessions *sessions.Module }

func (d departmentInstructions) Instructions(ctx context.Context, tenant model.TenantID, department string) (string, error) {
	return d.sessions.DepartmentInstructions(ctx, tenant, department)
}
