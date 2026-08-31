// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import "context"

// identitiesPath is the governance module's reconciled identity roster (the NHI
// roster fed by the deployment's identity connectors). It is read-tier
// (governance:identity:read). The roster PROVIDERS themselves are operator-config
// (wired via the engine's UseRosterProviders), not a REST-managed object — only
// the resulting identities are readable here.
const identitiesPath = "/v1/m/governance/identities"

// Identity is the governance view of one reconciled identity, matching the
// governance module's identityDTO. PrincipalType distinguishes nhi / human /
// unknown (never coerced — honest confidence, ARCHITECTURE.md).
type Identity struct {
	ID            string `json:"id"`
	Ref           string `json:"ref"`
	Name          string `json:"name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Source        string `json:"source,omitempty"`
	PrincipalType string `json:"principal_type,omitempty"`
	Disabled      bool   `json:"disabled"`
}

// identityList is the list envelope returned by GET /identities.
type identityList struct {
	Items   []Identity `json:"items"`
	Cursor  string     `json:"cursor"`
	HasMore bool       `json:"has_more"`
}

// ListIdentities returns the reconciled identity roster, following the cursor.
func (c *Client) ListIdentities(ctx context.Context, tenantOverride string) ([]Identity, error) {
	var all []Identity
	cursor := ""
	for {
		path := identitiesPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page identityList
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
