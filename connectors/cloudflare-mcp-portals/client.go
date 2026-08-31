// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxPages = 1000

type envelope struct {
	Success    bool              `json:"success"`
	Errors     []apiError        `json:"errors"`
	Result     json.RawMessage   `json:"result"`
	ResultInfo *resultInfo       `json:"result_info"`
	Messages   []json.RawMessage `json:"messages"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

type apiFault struct {
	status int
	errs   []apiError
	msg    string
}

func (e *apiFault) Error() string {
	if len(e.errs) > 0 {
		parts := make([]string, 0, len(e.errs))
		for _, a := range e.errs {
			parts = append(parts, fmt.Sprintf("%d: %s", a.Code, a.Message))
		}
		return fmt.Sprintf("cfmcpportals api error (status %d): %s", e.status, strings.Join(parts, "; "))
	}
	if e.msg != "" {
		return fmt.Sprintf("cfmcpportals api error (status %d): %s", e.status, e.msg)
	}
	return fmt.Sprintf("cfmcpportals api error (status %d)", e.status)
}

type client struct {
	httpClient *http.Client
	base       string
	token      string
}

func newClient(base, token string, hc *http.Client) *client {
	return &client{httpClient: hc, base: strings.TrimRight(base, "/"), token: token}
}

func (c *client) get(ctx context.Context, path string, query url.Values) ([]json.RawMessage, error) {
	var all []json.RawMessage
	page := 1
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{}
		for k, vs := range query {
			q[k] = vs
		}
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		env, err := c.fetch(ctx, path, q)
		if err != nil {
			return nil, err
		}
		rows, err := decodeRows(env.Result)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)

		if env.ResultInfo == nil || env.ResultInfo.TotalPages <= 1 || page >= env.ResultInfo.TotalPages || page >= maxPages {
			return all, nil
		}
		page++
	}
}

func (c *client) fetch(ctx context.Context, path string, q url.Values) (*envelope, error) {
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}

	var env envelope
	if len(body) > 0 {
		if jerr := json.Unmarshal(body, &env); jerr != nil {
			if resp.StatusCode >= http.StatusBadRequest {
				return nil, &apiFault{status: resp.StatusCode, msg: "non-JSON error body"}
			}
			return nil, fmt.Errorf("cfmcpportals: decode %s: %w", path, jerr)
		}
	}
	if resp.StatusCode >= http.StatusBadRequest || !env.Success {
		return nil, &apiFault{status: resp.StatusCode, errs: env.Errors}
	}
	return &env, nil
}

func decodeRows(result json.RawMessage) ([]json.RawMessage, error) {
	if len(result) == 0 || string(result) == "null" {
		return nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(result, &rows); err != nil {
		return nil, fmt.Errorf("cfmcpportals: decode result rows: %w", err)
	}
	return rows, nil
}
