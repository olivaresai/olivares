// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"encoding/json"
	"net/http"
)

// policiesPath is the governance module's policy collection. The governance
// module (module VI) mounts its routes under /v1/m/governance/; authoring policy
// requires the governance:policy:admin permission, which the engine enforces on
// the bearer token — the provider is a declarative client, never a governance
// bypass.
const policiesPath = "/v1/m/governance/policies"

// Policy is the wire representation of a governance policy, matching the
// governance module's policyDTO. Kind is one of "abac" | "approval". Spec is the
// engine's CANONICAL re-serialization of the typed spec on read (unknown/free
// fields are dropped by the engine, so it may differ from the submitted spec —
// the resource keeps the configured spec and exposes this canonical form as the
// drift signal, mirroring olivares_deployment's spec_hash).
type Policy struct {
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Spec    json.RawMessage `json:"spec,omitempty"`
}

// policyRequest is the create/update body declaring a policy. It carries only the
// writable fields the engine accepts; the engine strict-parses spec against the
// kind (DisallowUnknownFields), so an unknown field is rejected server-side.
type policyRequest struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Spec    json.RawMessage `json:"spec"`
}

// policyList is the list envelope returned by GET /policies.
type policyList struct {
	Items   []Policy `json:"items"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"has_more"`
}

// CreatePolicy authors a governance policy (POST). tenantOverride, when non-empty,
// replaces the client-level tenant for this call.
func (c *Client) CreatePolicy(ctx context.Context, tenantOverride string, p Policy) (*Policy, error) {
	var out Policy
	body := policyRequest{Name: p.Name, Kind: p.Kind, Enabled: p.Enabled, Spec: p.Spec}
	if err := c.sendInto(ctx, http.MethodPost, policiesPath, tenantOverride, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPolicy reads one governance policy (GET). A 404 returns ErrNotFound so the
// resource can be dropped from state.
func (c *Client) GetPolicy(ctx context.Context, tenantOverride, id string) (*Policy, error) {
	var out Policy
	if err := c.getInto(ctx, policiesPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePolicy updates a governance policy in place (PUT). Kind is immutable
// server-side; the resource marks it RequiresReplace so it is never changed here.
func (c *Client) UpdatePolicy(ctx context.Context, tenantOverride, id string, p Policy) (*Policy, error) {
	var out Policy
	body := policyRequest{Name: p.Name, Kind: p.Kind, Enabled: p.Enabled, Spec: p.Spec}
	if err := c.sendInto(ctx, http.MethodPut, policiesPath+"/"+id, tenantOverride, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePolicy removes a governance policy (DELETE). A 404 is treated as
// already-deleted.
func (c *Client) DeletePolicy(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, policiesPath+"/"+id, tenantOverride)
}

// ListPolicies returns governance policies, optionally filtered by kind (""=all).
// It follows the cursor to return the full set so a data source sees every policy.
func (c *Client) ListPolicies(ctx context.Context, tenantOverride, kind string) ([]Policy, error) {
	var all []Policy
	cursor := ""
	for {
		path := policiesPath
		sep := "?"
		if kind != "" {
			path += sep + "kind=" + kind
			sep = "&"
		}
		if cursor != "" {
			path += sep + "cursor=" + cursor
		}
		var page policyList
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
