// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudflare

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

// maxPages caps pagination so a hostile or buggy API can never spin the
// connector forever; the cap is generous relative to any real account.
const maxPages = 1000

// envelope is the Cloudflare REST API v4 response envelope. result is kept as raw
// JSON because the connector decodes each list into its own shape (a script, a
// bucket, a job); some endpoints wrap the array under an object (e.g. R2 buckets
// under result.buckets), which is handled by the per-endpoint decode.
type envelope struct {
	Success    bool              `json:"success"`
	Errors     []apiError        `json:"errors"`
	Result     json.RawMessage   `json:"result"`
	ResultInfo *resultInfo       `json:"result_info"`
	Messages   []json.RawMessage `json:"messages"`
}

// apiError is one Cloudflare error object. The message is operator-facing API
// metadata (e.g. "Authentication error"); it is folded into a typed error whose
// detail is hashed before it ever reaches a finding.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// resultInfo carries Cloudflare's pagination cursor. page/total_pages drive the
// page loop; a nil resultInfo (or total_pages<=1) means a single page.
type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

// apiFault is the typed error returned when an envelope reports success=false or
// the transport/HTTP layer fails. It is the value a health finding hashes; it
// never carries a secret (the request carried the token in a header, not the URL).
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
		return fmt.Sprintf("cloudflare api error (status %d): %s", e.status, strings.Join(parts, "; "))
	}
	if e.msg != "" {
		return fmt.Sprintf("cloudflare api error (status %d): %s", e.status, e.msg)
	}
	return fmt.Sprintf("cloudflare api error (status %d)", e.status)
}

// client is a minimal read-only Cloudflare REST client. It issues ONLY HTTP GET,
// sets the Bearer token on every request, decodes the v4 envelope, follows
// result_info pagination and returns an *apiFault when success=false. The token
// lives only in this struct's field; it is never logged.
type client struct {
	httpClient *http.Client
	base       string
	token      string
}

// newClient builds a client bound to base with the given token and an http.Client
// whose Timeout bounds each request.
func newClient(base, token string, hc *http.Client) *client {
	return &client{httpClient: hc, base: strings.TrimRight(base, "/"), token: token}
}

// get fetches every page of a list endpoint and returns the concatenated result
// rows as raw JSON. path is appended to the API base; query holds any non-paging
// query parameters (page is added by the loop). It returns an *apiFault on a
// non-2xx status or a success=false envelope, so a target's failure becomes a
// health finding upstream. unwrap, when non-nil, extracts the array from a result
// object (e.g. R2's {"buckets":[...]}); when nil, result is decoded as an array.
func (c *client) get(ctx context.Context, path string, query url.Values, unwrap func(json.RawMessage) (json.RawMessage, error)) ([]json.RawMessage, error) {
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
		rows, err := decodeRows(env.Result, unwrap)
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

// fetch performs one GET, decodes the envelope, and converts a non-2xx status or
// success=false into an *apiFault. A request error (DNS, timeout, ctx cancel) is
// returned as-is so ctx.Err() propagates unchanged for the cancel path.
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
			// A non-2xx with an unparseable body (e.g. an HTML 502) is still a fault.
			if resp.StatusCode >= http.StatusBadRequest {
				return nil, &apiFault{status: resp.StatusCode, msg: "non-JSON error body"}
			}
			return nil, fmt.Errorf("cloudflare: decode %s: %w", path, jerr)
		}
	}
	if resp.StatusCode >= http.StatusBadRequest || !env.Success {
		return nil, &apiFault{status: resp.StatusCode, errs: env.Errors}
	}
	return &env, nil
}

// decodeRows turns a result payload into its array of rows. With unwrap nil the
// result is expected to be a JSON array; with unwrap set the array is extracted
// from a result object (e.g. R2's {"buckets":[...]}). A null/empty result yields
// no rows, not an error.
func decodeRows(result json.RawMessage, unwrap func(json.RawMessage) (json.RawMessage, error)) ([]json.RawMessage, error) {
	arr := result
	if unwrap != nil {
		un, err := unwrap(result)
		if err != nil {
			return nil, err
		}
		arr = un
	}
	if len(arr) == 0 || string(arr) == "null" {
		return nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(arr, &rows); err != nil {
		return nil, fmt.Errorf("cloudflare: decode result rows: %w", err)
	}
	return rows, nil
}
