// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"context"
	"net/url"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// client is the read-only Claude Code Remote API client: a GET-only JSON
// client (httpx) that fetches the trigger inventory. By construction it cannot
// mutate the system it reads.
type client struct {
	http     *httpx.Client
	maxPages int
}

func newClient(cfg config, doer httpx.Doer) *client {
	auth := httpx.Header("x-api-key", cfg.apiKey, cfg.apiKey)
	return &client{
		http:     httpx.New(cfg.baseURL, doer, auth, nil),
		maxPages: cfg.maxPages,
	}
}

// fetchTriggers lists all triggers via paginated GET, accumulating across pages
// up to the safety bound.
func (c *client) fetchTriggers(ctx context.Context) ([]trigger, error) {
	var out []trigger
	cursor := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp listTriggersResponse
		if err := c.http.GetJSON(ctx, "/api/triggers", q, &resp); err != nil {
			return out, err
		}
		out = append(out, resp.Triggers...)
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return out, nil
}
