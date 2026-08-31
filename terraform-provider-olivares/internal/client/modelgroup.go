// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// modelGroupsPath is the models module's model-group collection (module routes
// mount under /v1/m/models/). A model group is a named set of model IDs that
// policies and budgets can reference as a single unit — the "which models may
// this agent use" surface a platform team declares as code. Authoring requires
// models:model-group:write, enforced by the engine; the provider is a
// declarative client, never a governance bypass.
const modelGroupsPath = "/v1/m/models/model-groups"

// ModelGroup is the wire representation of a model group, matching the models
// module's modelGroupDTO. Models is the list of model identifiers that belong
// to this group; it may be empty when the group is first created.
type ModelGroup struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Models      []string `json:"models,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// modelGroupList is the list envelope returned by GET /model-groups.
type modelGroupList struct {
	Items   []ModelGroup `json:"items"`
	Cursor  string       `json:"cursor"`
	HasMore bool         `json:"has_more"`
}

// CreateModelGroup authors a model group (POST). tenantOverride, when
// non-empty, replaces the client-level tenant for this call.
func (c *Client) CreateModelGroup(ctx context.Context, tenantOverride string, mg ModelGroup) (*ModelGroup, error) {
	var out ModelGroup
	if err := c.sendInto(ctx, http.MethodPost, modelGroupsPath, tenantOverride, mg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetModelGroup reads one model group (GET). A 404 returns ErrNotFound so the
// resource can be dropped from state.
func (c *Client) GetModelGroup(ctx context.Context, tenantOverride, id string) (*ModelGroup, error) {
	var out ModelGroup
	if err := c.getInto(ctx, modelGroupsPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateModelGroup updates a model group in place (PUT).
func (c *Client) UpdateModelGroup(ctx context.Context, tenantOverride, id string, mg ModelGroup) (*ModelGroup, error) {
	var out ModelGroup
	if err := c.sendInto(ctx, http.MethodPut, modelGroupsPath+"/"+id, tenantOverride, mg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteModelGroup removes a model group (DELETE). A 404 is treated as
// already-deleted.
func (c *Client) DeleteModelGroup(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, modelGroupsPath+"/"+id, tenantOverride)
}

// ListModelGroups returns every model group, following the cursor so a data
// source sees the full set.
func (c *Client) ListModelGroups(ctx context.Context, tenantOverride string) ([]ModelGroup, error) {
	var all []ModelGroup
	cursor := ""
	for {
		path := modelGroupsPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page modelGroupList
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
