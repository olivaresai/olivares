// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// rbacGrantsPath is the governance module's RBAC grant collection (module I
// routes mount under /v1/m/governance/). A grant is an immutable binding of a
// subject (user, group, service account) to a role, optionally scoped to a
// resource boundary. Authoring requires governance:rbac:admin, enforced by the
// engine; the provider is a declarative client, never a governance bypass.
const rbacGrantsPath = "/v1/m/governance/rbac/grants"

// RBACGrant is the wire representation of an RBAC grant, matching the
// governance module's grant DTO. Grants are immutable: they are created and
// deleted, never updated in place.
type RBACGrant struct {
	ID          string `json:"id,omitempty"`
	SubjectType string `json:"subject_type"`
	SubjectRef  string `json:"subject_ref"`
	Role        string `json:"role"`
	Scope       string `json:"scope,omitempty"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// rbacGrantList is the list envelope returned by GET /rbac/grants.
type rbacGrantList struct {
	Items   []RBACGrant `json:"items"`
	Cursor  string      `json:"cursor"`
	HasMore bool        `json:"has_more"`
}

// CreateRBACGrant authors a grant (POST). tenantOverride, when non-empty,
// replaces the client-level tenant for this call.
func (c *Client) CreateRBACGrant(ctx context.Context, tenantOverride string, g RBACGrant) (*RBACGrant, error) {
	var out RBACGrant
	if err := c.sendInto(ctx, http.MethodPost, rbacGrantsPath, tenantOverride, g, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRBACGrant reads one grant (GET). A 404 returns ErrNotFound so the resource
// can be dropped from state.
func (c *Client) GetRBACGrant(ctx context.Context, tenantOverride, id string) (*RBACGrant, error) {
	var out RBACGrant
	if err := c.getInto(ctx, rbacGrantsPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRBACGrant removes a grant (DELETE). A 404 is treated as already-deleted.
func (c *Client) DeleteRBACGrant(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, rbacGrantsPath+"/"+id, tenantOverride)
}

// ListRBACGrants returns every grant, following the cursor so a data source
// sees the full set.
func (c *Client) ListRBACGrants(ctx context.Context, tenantOverride string) ([]RBACGrant, error) {
	var all []RBACGrant
	cursor := ""
	for {
		path := rbacGrantsPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page rbacGrantList
		if err := c.getInto(ctx, path, tenantOverride, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasMore || page.Cursor == "" {
			return all, nil
		}
		cursor = page.Cursor
	}
}
