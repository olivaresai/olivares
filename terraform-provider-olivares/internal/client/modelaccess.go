// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// modelAccessPath is the models module's model-access collection (module routes
// mount under /v1/m/models/). A model access rule is a subject-scoped
// allow/deny gate on a model pattern with a priority — the access control a
// platform team declares as code. Authoring requires
// models:model-access:admin, enforced by the engine; the provider is a
// declarative client, never a governance bypass.
const modelAccessPath = "/v1/m/models/model-access"

// ModelAccess is the wire representation of a model access rule, matching the
// models module's modelAccessDTO. SubjectType/SubjectRef identify the grantee,
// ModelPattern is the glob matched against model identifiers, Effect is
// "allow" or "deny", and Priority orders evaluation (higher wins).
type ModelAccess struct {
	ID           string `json:"id,omitempty"`
	SubjectType  string `json:"subject_type"`
	SubjectRef   string `json:"subject_ref"`
	ModelPattern string `json:"model_pattern"`
	Effect       string `json:"effect"`
	Priority     int64  `json:"priority"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// modelAccessList is the list envelope returned by GET /model-access.
type modelAccessList struct {
	Items   []ModelAccess `json:"items"`
	Cursor  string        `json:"cursor"`
	HasMore bool          `json:"has_more"`
}

// CreateModelAccess authors a model access rule (POST). tenantOverride, when
// non-empty, replaces the client-level tenant for this call.
func (c *Client) CreateModelAccess(ctx context.Context, tenantOverride string, ma ModelAccess) (*ModelAccess, error) {
	var out ModelAccess
	if err := c.sendInto(ctx, http.MethodPost, modelAccessPath, tenantOverride, ma, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetModelAccess reads one model access rule (GET). A 404 returns ErrNotFound
// so the resource can be dropped from state.
func (c *Client) GetModelAccess(ctx context.Context, tenantOverride, id string) (*ModelAccess, error) {
	var out ModelAccess
	if err := c.getInto(ctx, modelAccessPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateModelAccess updates a model access rule in place (PUT).
func (c *Client) UpdateModelAccess(ctx context.Context, tenantOverride, id string, ma ModelAccess) (*ModelAccess, error) {
	var out ModelAccess
	if err := c.sendInto(ctx, http.MethodPut, modelAccessPath+"/"+id, tenantOverride, ma, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteModelAccess removes a model access rule (DELETE). A 404 is treated as
// already-deleted.
func (c *Client) DeleteModelAccess(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, modelAccessPath+"/"+id, tenantOverride)
}

// ListModelAccess returns every model access rule, following the cursor so a
// data source sees the full set.
func (c *Client) ListModelAccess(ctx context.Context, tenantOverride string) ([]ModelAccess, error) {
	var all []ModelAccess
	cursor := ""
	for {
		path := modelAccessPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page modelAccessList
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
