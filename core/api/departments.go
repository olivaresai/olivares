// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// DepartmentService is the Business organization-structure surface
// (docs/editions.md, Identity sources): placing a workspace (a department) under
// another and filing a user group in a workspace. The Community build wires none,
// so PUT /v1/workspaces/{id}/parent and PUT /v1/groups/{id}/workspace answer 501
// departments_unavailable. What is already stored stays: the reads show it, and
// the department tree keeps counting for enforcement. Group nesting
// (PUT /v1/groups/{id}/parent) is published Community API and stays in every build.
//
// The handlers decode the request and, for a workspace move, refuse a confined
// caller, a non-owner and a missing step-up before the service runs. The service
// makes the change and audits it.
type DepartmentService interface {
	// SetWorkspaceParent places workspace id under parent; a zero parent makes it a root.
	SetWorkspaceParent(ctx context.Context, p auth.Principal, tenant model.TenantID, id, parent model.ID) (model.Workspace, error)
	// SetGroupWorkspace files group id in workspace; a zero workspace clears the place.
	SetGroupWorkspace(ctx context.Context, p auth.Principal, tenant model.TenantID, id, workspace model.ID) (model.UserGroup, error)
}

// ErrDepartmentsUnavailable: no department service is wired (the Community build).
// 501, like the other edition seams.
var ErrDepartmentsUnavailable = errors.New("api: departments unavailable")
