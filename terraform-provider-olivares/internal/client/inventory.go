// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/url"
)

// inventory endpoints (module routes mount under /v1/m/inventory/). The inventory
// is the reconciled, cross-source view of the governed estate (agents, MCP
// servers, identities, resources…) — read-only, so a Terraform/OpenTofu module
// can reference what the control plane has discovered without reimplementing the
// REST calls. It is read-tier (inventory:read).
const (
	inventoryEntitiesPath = "/v1/m/inventory/entities"
	inventorySummaryPath  = "/v1/m/inventory/summary"
)

// InventoryEntity is one reconciled estate entity, matching the inventory
// module's entryDTO. SignalSources are the collectors that observed it (honest
// provenance — never coerced to a single source).
type InventoryEntity struct {
	Kind            string   `json:"kind"`
	EntityID        string   `json:"entity_id"`
	Name            string   `json:"name"`
	Ref             string   `json:"ref,omitempty"`
	Status          string   `json:"status"`
	SignalSources   []string `json:"signal_sources"`
	Hosts           []string `json:"hosts,omitempty"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	OccurrenceCount int64    `json:"occurrence_count"`
}

// entityList is the list envelope returned by GET /entities.
type entityList struct {
	Items   []InventoryEntity `json:"items"`
	Cursor  string            `json:"cursor"`
	HasMore bool              `json:"has_more"`
}

// InventorySummary is the inventory roll-up, matching the inventory module's
// summaryDTO: counts by kind and by source plus a total. Truncated marks a
// roll-up bounded by the scan cap (honest gradation, never silently wrong).
type InventorySummary struct {
	ByKind    map[string]InventoryKindCount `json:"by_kind"`
	BySource  map[string]int                `json:"by_source"`
	Total     int                           `json:"total"`
	Truncated bool                          `json:"truncated,omitempty"`
}

// InventoryKindCount is the per-kind breakdown in the summary.
type InventoryKindCount struct {
	Total int `json:"total"`
}

// ListInventoryEntities returns the reconciled estate entities, optionally
// filtered by kind and status (""=all), following the cursor to the full set.
func (c *Client) ListInventoryEntities(ctx context.Context, tenantOverride, kind, status string) ([]InventoryEntity, error) {
	var all []InventoryEntity
	cursor := ""
	for {
		q := url.Values{}
		if kind != "" {
			q.Set("kind", kind)
		}
		if status != "" {
			q.Set("status", status)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		path := inventoryEntitiesPath
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}
		var page entityList
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

// GetInventorySummary returns the inventory roll-up (counts by kind/source).
func (c *Client) GetInventorySummary(ctx context.Context, tenantOverride string) (*InventorySummary, error) {
	var out InventorySummary
	if err := c.getInto(ctx, inventorySummaryPath, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
