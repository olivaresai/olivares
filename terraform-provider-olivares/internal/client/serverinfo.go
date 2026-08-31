// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import "context"

// serverInfoPath is the core server-info probe. It is unauthenticated metadata
// the provider exposes as a data source so a module can branch on the engine's
// version/license without hardcoding assumptions (the honest capability-detection
// path).
const serverInfoPath = "/v1/server-info"

// ServerInfo is the wire representation of GET /v1/server-info.
type ServerInfo struct {
	Version       string `json:"version"`
	Engine        string `json:"engine"`
	SetupRequired bool   `json:"setup_required"`
	License       struct {
		Status   string `json:"status"`
		Licensee string `json:"licensee"`
	} `json:"license"`
}

// GetServerInfo reads the control plane's server-info metadata.
func (c *Client) GetServerInfo(ctx context.Context, tenantOverride string) (*ServerInfo, error) {
	var out ServerInfo
	if err := c.getInto(ctx, serverInfoPath, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
