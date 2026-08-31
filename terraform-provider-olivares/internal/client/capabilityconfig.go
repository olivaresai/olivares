// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// capabilityConfigsPath is the capabilities module's MCP server configuration
// collection (module routes mount under /v1/m/capabilities/). A config is the
// managed connection to an MCP server — the "connector/source" a platform team
// declares as code: transport, endpoint (a reference, never an inline
// credential), scope and secret references. Authoring requires the capabilities
// admin permission, enforced by the engine; the provider is a declarative client.
//
// MINIMUM DATA (docs/SECURITY-HARDENING.md): the engine REFUSES an endpoint carrying an inline
// credential and a secret ref whose locator is the credential value — secrets are
// referenced by locator only. The provider forwards what the operator declares;
// the engine is the authority that rejects a cleartext secret.
const capabilityConfigsPath = "/v1/m/capabilities/configs"

// SecretRef is one secret reference on an MCP server config, matching the
// capabilities module's secretRefDTO. Ref is a LOCATOR (env var name, Vault path,
// secret-manager key, file path), never the secret value; Hint is an optional
// short masked partial for operator recognition, never a full credential.
type SecretRef struct {
	Name    string `json:"name"`
	RefKind string `json:"ref_kind"`
	Ref     string `json:"ref"`
	Hint    string `json:"hint,omitempty"`
}

// CapabilityConfig is the wire representation of an MCP server config, matching
// the capabilities module's configDTO. Revision is the engine-assigned version
// (bumped on each update); the config keeps a revision history server-side.
type CapabilityConfig struct {
	ID         string      `json:"id,omitempty"`
	ServerRef  string      `json:"server_ref"`
	Transport  string      `json:"transport"`
	Endpoint   string      `json:"endpoint,omitempty"`
	Scope      string      `json:"scope,omitempty"`
	SecretRefs []SecretRef `json:"secret_refs"`
	Enabled    bool        `json:"enabled"`
	Note       string      `json:"note,omitempty"`
	Revision   int64       `json:"revision,omitempty"`
}

// capabilityConfigList is the list envelope returned by GET /configs.
type capabilityConfigList struct {
	Items   []CapabilityConfig `json:"items"`
	Cursor  string             `json:"cursor"`
	HasMore bool               `json:"has_more"`
}

// CreateCapabilityConfig declares an MCP server config (POST). tenantOverride,
// when non-empty, replaces the client-level tenant for this call.
func (c *Client) CreateCapabilityConfig(ctx context.Context, tenantOverride string, cfg CapabilityConfig) (*CapabilityConfig, error) {
	var out CapabilityConfig
	if err := c.sendInto(ctx, http.MethodPost, capabilityConfigsPath, tenantOverride, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCapabilityConfig reads one MCP server config (GET). A 404 returns
// ErrNotFound so the resource can be dropped from state.
func (c *Client) GetCapabilityConfig(ctx context.Context, tenantOverride, id string) (*CapabilityConfig, error) {
	var out CapabilityConfig
	if err := c.getInto(ctx, capabilityConfigsPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateCapabilityConfig updates an MCP server config in place (PUT). The engine
// bumps the revision and records the prior one in history.
func (c *Client) UpdateCapabilityConfig(ctx context.Context, tenantOverride, id string, cfg CapabilityConfig) (*CapabilityConfig, error) {
	var out CapabilityConfig
	if err := c.sendInto(ctx, http.MethodPut, capabilityConfigsPath+"/"+id, tenantOverride, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteCapabilityConfig removes an MCP server config (DELETE). A 404 is treated
// as already-deleted.
func (c *Client) DeleteCapabilityConfig(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, capabilityConfigsPath+"/"+id, tenantOverride)
}

// ListCapabilityConfigs returns every MCP server config, following the cursor.
func (c *Client) ListCapabilityConfigs(ctx context.Context, tenantOverride string) ([]CapabilityConfig, error) {
	var all []CapabilityConfig
	cursor := ""
	for {
		path := capabilityConfigsPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page capabilityConfigList
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
