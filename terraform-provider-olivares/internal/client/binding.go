// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// bindingsPath is the governance module's agent↔identity binding list. Binding an
// agent to its NHI identity is admin-tier (governance:identity:admin) and the
// engine self-audits it; the provider only declares the desired binding.
const bindingsPath = "/v1/m/governance/bindings"

// agentIdentityPath returns the bind/unbind route for one agent.
func agentIdentityPath(agentID string) string {
	return "/v1/m/governance/agents/" + agentID + "/identity"
}

// Binding is the wire representation of an agent↔identity binding (the NHI
// attribution bridge). Shared reports whether the identity is bound to more than
// one agent (which collapses per-agent attribution — surfaced, never faked).
type Binding struct {
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name,omitempty"`
	IdentityID  string `json:"identity_id"`
	IdentityRef string `json:"identity_ref,omitempty"`
	Minted      bool   `json:"minted,omitempty"`
	Shared      bool   `json:"shared"`
	AgentCount  int64  `json:"agent_count"`
}

// bindRequest selects the identity to bind: exactly one of identity_id /
// identity_ref / mint. allow_unknown permits binding to an identity whose
// principal type the source never revealed.
type bindRequest struct {
	IdentityID   string `json:"identity_id,omitempty"`
	IdentityRef  string `json:"identity_ref,omitempty"`
	Mint         bool   `json:"mint,omitempty"`
	AllowUnknown bool   `json:"allow_unknown,omitempty"`
}

// bindingList is the list envelope returned by GET /bindings.
type bindingList struct {
	Items   []Binding `json:"items"`
	Cursor  string    `json:"cursor"`
	HasMore bool      `json:"has_more"`
}

// BindAgentIdentity binds an agent to an NHI identity (POST). Exactly one of
// identityID / identityRef / mint must be set. It returns the resulting binding.
func (c *Client) BindAgentIdentity(ctx context.Context, tenantOverride, agentID, identityID, identityRef string, mint, allowUnknown bool) (*Binding, error) {
	var out Binding
	body := bindRequest{IdentityID: identityID, IdentityRef: identityRef, Mint: mint, AllowUnknown: allowUnknown}
	if err := c.sendInto(ctx, http.MethodPost, agentIdentityPath(agentID), tenantOverride, body, &out); err != nil {
		return nil, err
	}
	// The engine echoes agent_id only when it differs; ensure it is always set so
	// the resource can key state on it regardless of the response shape.
	if out.AgentID == "" {
		out.AgentID = agentID
	}
	return &out, nil
}

// UnbindAgentIdentity clears an agent's identity binding (DELETE). It is
// idempotent: an already-unbound agent returns nil.
func (c *Client) UnbindAgentIdentity(ctx context.Context, tenantOverride, agentID string) error {
	return c.deleteResource(ctx, agentIdentityPath(agentID), tenantOverride)
}

// GetBinding reads the current binding for one agent by scanning the bindings
// list (there is no single-binding GET endpoint). It returns ErrNotFound when the
// agent has no binding, so the resource is dropped from state on out-of-band
// unbind.
func (c *Client) GetBinding(ctx context.Context, tenantOverride, agentID string) (*Binding, error) {
	bindings, err := c.ListBindings(ctx, tenantOverride)
	if err != nil {
		return nil, err
	}
	for i := range bindings {
		if bindings[i].AgentID == agentID {
			return &bindings[i], nil
		}
	}
	return nil, ErrNotFound
}

// ListBindings returns every agent↔identity binding, following the cursor.
func (c *Client) ListBindings(ctx context.Context, tenantOverride string) ([]Binding, error) {
	var all []Binding
	cursor := ""
	for {
		path := bindingsPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page bindingList
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
