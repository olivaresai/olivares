// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// workspacesPath is the sessions module's workspace collection (module routes
// mount under /v1/m/sessions/). A workspace is a named isolation boundary for
// sessions — the organizational primitive a platform team declares as code.
// Authoring requires sessions:workspace:admin, enforced by the engine; the
// provider is a declarative client, never a governance bypass.
const workspacesPath = "/v1/m/sessions/workspaces"

// Workspace is the wire representation of a workspace, matching the sessions
// module's workspaceDTO. Status, CreatedAt and UpdatedAt are server-assigned
// read-only fields populated on every read.
type Workspace struct {
	Ref         string `json:"ref,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// workspaceList is the list envelope returned by GET /workspaces.
type workspaceList struct {
	Items   []Workspace `json:"items"`
	Cursor  string      `json:"cursor"`
	HasMore bool        `json:"has_more"`
}

// CreateWorkspace authors a workspace (POST). tenantOverride, when non-empty,
// replaces the client-level tenant for this call.
func (c *Client) CreateWorkspace(ctx context.Context, tenantOverride string, w Workspace) (*Workspace, error) {
	var out Workspace
	if err := c.sendInto(ctx, http.MethodPost, workspacesPath, tenantOverride, w, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetWorkspace reads one workspace by ref (GET). A 404 returns ErrNotFound so
// the resource can be dropped from state.
func (c *Client) GetWorkspace(ctx context.Context, tenantOverride, ref string) (*Workspace, error) {
	var out Workspace
	if err := c.getInto(ctx, workspacesPath+"/"+ref, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWorkspace removes a workspace (DELETE). A 404 is treated as
// already-deleted.
func (c *Client) DeleteWorkspace(ctx context.Context, tenantOverride, ref string) error {
	return c.deleteResource(ctx, workspacesPath+"/"+ref, tenantOverride)
}

// ListWorkspaces returns every workspace, following the cursor so a data source
// sees the full set.
func (c *Client) ListWorkspaces(ctx context.Context, tenantOverride string) ([]Workspace, error) {
	var all []Workspace
	cursor := ""
	for {
		path := workspacesPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page workspaceList
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
