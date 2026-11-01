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
// another, filing a user group in a workspace and nesting a group under another.
// The Community build wires none, so PUT /v1/workspaces/{id}/parent,
// PUT /v1/groups/{id}/workspace and PUT /v1/groups/{id}/parent answer 501
// departments_unavailable. What is already stored stays: the reads show it, and
// the department tree and group nesting keep counting for enforcement.
//
// The handlers decode the request and, for a workspace move, refuse a confined
// caller, a non-owner and a missing step-up before the service runs. The service
// makes the change and audits it, and the Business build gates each write on its
// licence.
type DepartmentService interface {
	// SetWorkspaceParent places workspace id under parent; a zero parent makes it a root.
	SetWorkspaceParent(ctx context.Context, p auth.Principal, tenant model.TenantID, id, parent model.ID) (model.Workspace, error)
	// SetGroupWorkspace files group id in workspace; a zero workspace clears the place.
	SetGroupWorkspace(ctx context.Context, p auth.Principal, tenant model.TenantID, id, workspace model.ID) (model.UserGroup, error)
	// SetGroupParent nests group id under parent; a zero parent un-nests it.
	SetGroupParent(ctx context.Context, p auth.Principal, tenant model.TenantID, id, parent model.ID) (model.UserGroup, error)
}

// DepartmentCreator is the OPTIONAL capability of a DepartmentService that creates
// a workspace under a parent: POST /v1/workspaces with a parent_id. It creates ws
// under ws.ParentID in one write and audits it, so an unparented department never
// exists. The route and handler have already refused a confined caller, one who neither
// owns the tenant nor administers the parent (WorkspaceAdministration), and a
// missing step-up. It is optional, the repository's idiom for an added edition
// capability, so a service built before it keeps compiling; a service without it
// answers 501 departments_unavailable, which grants less.
type DepartmentCreator interface {
	CreateWorkspace(ctx context.Context, p auth.Principal, tenant model.TenantID, ws model.Workspace) (model.Workspace, error)
}

// WorkspaceAdministration is the OPTIONAL capability of a mounted module that says
// whether a caller administers a department: one of its delegation domains (tenant
// administration or an admin-capable scoped grant) contains the workspace. The
// governance module answers it with the definition its delegation ceiling and its
// inheritance filters use, so a department admin creates under its own department
// and nowhere else. Without such a module only the owner creates under a parent.
type WorkspaceAdministration interface {
	AdministersWorkspace(ctx context.Context, p auth.Principal, tenant model.TenantID, workspace model.ID) (bool, error)
}

// ErrDepartmentsUnavailable: no department service is wired (the Community build).
// 501, like the other edition seams.
var ErrDepartmentsUnavailable = errors.New("api: departments unavailable")
