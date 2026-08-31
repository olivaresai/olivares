// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"fmt"
	"net/url"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// The Admin API object shapes. Field names are verbatim from the Anthropic Admin
// API reference (platform.claude.com/docs/en/api/admin-api). The connector reads
// only these metadata fields and never a key secret — api_keys returns a masked
// partial_key_hint, never the value.

// orgUser is one /v1/organizations/users row (an organization member).
type orgUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Role    string `json:"role"` // user|developer|billing|admin|claude_code_user
	AddedAt string `json:"added_at"`
	Type    string `json:"type"` // always "user"
}

// invite is one /v1/organizations/invites row (a pending/expired org invite).
type invite struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`   // same enum as orgUser.Role
	Status    string `json:"status"` // pending|accepted|expired|deleted
	InvitedAt string `json:"invited_at"`
	ExpiresAt string `json:"expires_at"`
	Type      string `json:"type"` // always "invite"
}

// apiKey is one /v1/organizations/api_keys row (key inventory metadata, no secret).
type apiKey struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	WorkspaceID    string `json:"workspace_id"`
	Status         string `json:"status"` // active|inactive|archived
	PartialKeyHint string `json:"partial_key_hint"`
	CreatedAt      string `json:"created_at"`
}

// workspace is one /v1/organizations/workspaces row.
type workspace struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ArchivedAt string `json:"archived_at"`
	CreatedAt  string `json:"created_at"`
}

// workspaceMember is one /v1/organizations/workspaces/{id}/members row.
type workspaceMember struct {
	Type          string `json:"type"`
	UserID        string `json:"user_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceRole string `json:"workspace_role"` // workspace_user|workspace_developer|workspace_restricted_developer|workspace_admin|workspace_billing
}

// page is the Admin API list envelope: a typed data array with cursor pagination.
type page[T any] struct {
	Data    []T    `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// listAll pages an Admin API list endpoint to completion (bounded by maxPages),
// following the after_id cursor. baseQuery carries any endpoint-specific filters
// (e.g. workspace_id). It is read-only: a GET per page, decoded into the typed
// envelope. A nil client (no credential) yields no rows.
func listAll[T any](ctx context.Context, client *modelprovider.Client, maxPages int, path string, baseQuery url.Values) ([]T, error) {
	if client == nil {
		return nil, nil
	}
	var out []T
	after := ""
	for i := 0; i < maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"limit": {"100"}}
		for k, vs := range baseQuery {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		if after != "" {
			q.Set("after_id", after)
		}
		var resp page[T]
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// fetchUsers lists organization members.
func (s *Source) fetchUsers(ctx context.Context) ([]orgUser, error) {
	return listAll[orgUser](ctx, s.client, s.maxPages, pathUsers, nil)
}

// fetchInvites lists pending/expired organization invites.
func (s *Source) fetchInvites(ctx context.Context) ([]invite, error) {
	return listAll[invite](ctx, s.client, s.maxPages, pathInvites, nil)
}

// fetchAPIKeys lists API keys (optionally scoped to the configured workspace).
func (s *Source) fetchAPIKeys(ctx context.Context) ([]apiKey, error) {
	q := url.Values{}
	if s.wsFilter != "" {
		q.Set("workspace_id", s.wsFilter)
	}
	return listAll[apiKey](ctx, s.client, s.maxPages, pathAPIKeys, q)
}

// fetchWorkspaces lists workspaces.
func (s *Source) fetchWorkspaces(ctx context.Context) ([]workspace, error) {
	return listAll[workspace](ctx, s.client, s.maxPages, pathWorkspaces, nil)
}

// fetchWorkspaceMembers lists the members of one workspace.
func (s *Source) fetchWorkspaceMembers(ctx context.Context, workspaceID string) ([]workspaceMember, error) {
	path := fmt.Sprintf(pathWorkspaceMembersFmt, url.PathEscape(workspaceID))
	return listAll[workspaceMember](ctx, s.client, s.maxPages, path, nil)
}
