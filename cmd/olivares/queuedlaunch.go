// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func authorizeQueuedSessionLaunch(ctx context.Context, authr *auth.Authenticator, authz *auth.Authorizer, tenant model.TenantID, credential auth.QueuedCredential, runID string, workspace model.ID) (auth.Principal, error) {
	principal, err := authr.RevalidateQueuedCredential(ctx, credential)
	if err != nil {
		return auth.Principal{}, err
	}
	permission := auth.Permission("sessions:run:write")
	resource := auth.ResourceFor(permission)
	resource.ID, resource.WorkspaceID = runID, workspace
	decision := authz.Authorize(ctx, auth.Request{Principal: principal, Tenant: tenant, Permission: permission, Resource: resource})
	if !decision.Allow {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return principal, nil
}
