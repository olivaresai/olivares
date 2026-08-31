// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// getInto issues a GET against path (relative to the configured endpoint) and
// decodes a 2xx JSON body into out. A 404 returns ErrNotFound; any other non-2xx
// is mapped through the shared error envelope. It is the read primitive the
// governance data sources are built on, so a new read-only endpoint needs no
// bespoke decode boilerplate.
func (c *Client) getInto(ctx context.Context, path, tenantOverride string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, c.endpoint+path, tenantOverride, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("olivares: get %s: %w", path, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorFromResponse(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("olivares: decode %s (status %d): %w", path, resp.StatusCode, err)
	}
	return nil
}

// sendInto marshals body (when non-nil) and issues a write (POST/PUT) against
// path, decoding a 2xx JSON body into out. out may be nil when the caller does
// not need the response body. A 404 returns ErrNotFound so write-then-read flows
// can detect a vanished parent.
func (c *Client) sendInto(ctx context.Context, method, path, tenantOverride string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("olivares: encode %s body: %w", path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := c.newRequest(ctx, method, c.endpoint+path, tenantOverride, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("olivares: %s %s: %w", method, path, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorFromResponse(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("olivares: decode %s (status %d): %w", path, resp.StatusCode, err)
	}
	return nil
}

// deleteResource issues a DELETE against path, treating 204 and 404 as success
// (idempotent delete) and mapping any other non-2xx through the error envelope.
func (c *Client) deleteResource(ctx context.Context, path, tenantOverride string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, c.endpoint+path, tenantOverride, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("olivares: delete %s: %w", path, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return errorFromResponse(resp)
}
