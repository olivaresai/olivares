// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// budgetsPath is the FinOps module's budget collection (module XI routes mount
// under /v1/m/finops/). A budget is a named, enabled spend cap on a dimension
// over a period with alert thresholds and an enforcement action — the cost
// guardrail a platform team declares as code. Authoring requires
// finops:budget:write, enforced by the engine; the provider is a declarative
// client, never a governance bypass.
const budgetsPath = "/v1/m/finops/budgets"

// Budget is the wire representation of a budget, matching the FinOps module's
// budgetDTO (budgetDTO embeds budgetSpec, so the fields are flattened here).
// Dimension/Period/Currency/Action are filled with engine defaults on read, so
// they are reported back even when omitted on write.
type Budget struct {
	ID               string    `json:"id,omitempty"`
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	Dimension        string    `json:"dimension"`
	Key              string    `json:"key,omitempty"`
	LimitMicroUSD    int64     `json:"limit_micro_usd"`
	Period           string    `json:"period"`
	Thresholds       []float64 `json:"thresholds,omitempty"`
	Currency         string    `json:"currency,omitempty"`
	Action           string    `json:"action,omitempty"`
	ReservedMicroUSD int64     `json:"reserved_micro_usd,omitempty"`
}

// budgetList is the list envelope returned by GET /budgets.
type budgetList struct {
	Items   []Budget `json:"items"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"has_more"`
}

// CreateBudget authors a budget (POST). tenantOverride, when non-empty, replaces
// the client-level tenant for this call.
func (c *Client) CreateBudget(ctx context.Context, tenantOverride string, b Budget) (*Budget, error) {
	var out Budget
	if err := c.sendInto(ctx, http.MethodPost, budgetsPath, tenantOverride, b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBudget reads one budget (GET). A 404 returns ErrNotFound so the resource can
// be dropped from state (the engine also 404s an id whose policy is not a budget).
func (c *Client) GetBudget(ctx context.Context, tenantOverride, id string) (*Budget, error) {
	var out Budget
	if err := c.getInto(ctx, budgetsPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateBudget updates a budget in place (PUT).
func (c *Client) UpdateBudget(ctx context.Context, tenantOverride, id string, b Budget) (*Budget, error) {
	var out Budget
	if err := c.sendInto(ctx, http.MethodPut, budgetsPath+"/"+id, tenantOverride, b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBudget removes a budget (DELETE). A 404 is treated as already-deleted.
func (c *Client) DeleteBudget(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, budgetsPath+"/"+id, tenantOverride)
}

// ListBudgets returns every budget, following the cursor so a data source sees
// the full set.
func (c *Client) ListBudgets(ctx context.Context, tenantOverride string) ([]Budget, error) {
	var all []Budget
	cursor := ""
	for {
		path := budgetsPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page budgetList
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
