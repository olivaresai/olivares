// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// client is the read-only CMA API client: a GET-only JSON client (httpx) bound to the
// Anthropic API with the x-api-key credential applied per request (never logged, never
// in a URL) and the anthropic-version + anthropic-beta headers every Managed Agents
// endpoint requires. By construction it cannot mutate the system it reads.
//
// dreams is a sibling handle to the SAME API whose anthropic-beta header additionally
// carries the dreaming-2026-04-21 gate (the Dreams endpoints require BOTH betas,
// comma-separated — verified 2026-06-10). Everything else (credential, version,
// transport) is identical.
type client struct {
	http     *httpx.Client
	dreams   *httpx.Client
	maxPages int
}

// newClient builds the CMA client. doer is the transport (nil = http.DefaultClient; a
// test injects a stub). With an empty apiKey httpx sends no credential header, so the
// caller must gate calls behind config.pollEnabled().
func newClient(cfg config, doer httpx.Doer) *client {
	headers := map[string]string{
		"anthropic-version": cfg.version,
		"anthropic-beta":    cfg.beta,
	}
	dreamHeaders := map[string]string{
		"anthropic-version": cfg.version,
		"anthropic-beta":    dreamsBeta(cfg.beta),
	}
	auth := httpx.Header("x-api-key", cfg.apiKey, cfg.apiKey)
	return &client{
		http:     httpx.New(cfg.baseURL, doer, auth, headers),
		dreams:   httpx.New(cfg.baseURL, doer, auth, dreamHeaders),
		maxPages: cfg.maxPages,
	}
}

// dreamsBeta appends the dreaming beta gate to the configured beta header value,
// avoiding a duplicate when the operator already included it.
func dreamsBeta(beta string) string {
	if strings.Contains(beta, dreamsBetaSuffix) {
		return beta
	}
	if strings.TrimSpace(beta) == "" {
		return dreamsBetaSuffix
	}
	return beta + "," + dreamsBetaSuffix
}

// getJSON issues a read-only GET and decodes the JSON body into out.
func (c *client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	return c.http.GetJSON(ctx, path, query, out)
}

// listQuery builds the standard list query with a page-size of 100 and an optional cursor
// under the given cursor key (CMA list endpoints differ: the control-plane resources use
// after_id over a data/has_more/last_id envelope; the an internal design note (not shipped) family uses
// an opaque page cursor over a data/next_page envelope). Passing an empty cursor omits it.
func listQuery(cursorKey, cursor string) url.Values {
	q := url.Values{"limit": {"100"}}
	if cursor != "" && cursorKey != "" {
		q.Set(cursorKey, cursor)
	}
	return q
}
