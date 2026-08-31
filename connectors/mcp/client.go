// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// maxPages caps pagination so a server that returns a non-advancing cursor cannot
// loop the connector forever.
const maxPages = 1000

// Client is a minimal MCP client that performs read-only introspection over a
// transport. It is dual-revision: the stateless client speaks the
// 2026-07-28 frozen RC (no handshake; per-request `_meta`; server/discover); a
// client built with newClient performs the legacy 2025-11-25 Initialize
// handshake for backward compatibility with servers that do not yet speak the RC.
// It never calls a tool or reads a resource's contents in either mode.
type Client struct {
	t transport
	// meta, when non-nil, puts the client in stateless (RC) mode: every request
	// carries `_meta` (protocolVersion/clientInfo/clientCapabilities) and every
	// result's envelope is checked (MRTR input_required is declined deny-closed).
	meta *requestMeta
	// hints are the SEP-2549 freshness hints recorded in stateless mode (the
	// catalog-freshness signal; the connector itself never caches).
	hints []cacheHint
}

// newClient wraps a transport in a legacy-revision (2025-11-25) Client.
func newClient(t transport) *Client { return &Client{t: t} }

// newStatelessClient wraps a transport in a stateless (2026-07-28) Client.
func newStatelessClient(t transport) *Client {
	m := nextRequestMeta()
	return &Client{t: t, meta: &m}
}

// stateless reports whether the client runs in the 2026-07-28 stateless mode.
func (c *Client) stateless() bool { return c.meta != nil }

// call performs one request in the client's mode: the legacy mode passes params
// through; the stateless mode injects `_meta` and checks the result envelope —
// an MRTR "input_required" answer is returned as errInputRequired (declined
// deny-closed, never retried; the client declares no input capabilities so a
// conforming server never sends one).
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !c.stateless() {
		return c.t.roundTrip(ctx, rpcRequest{Method: method, Params: params})
	}
	obj, err := c.meta.inject(params)
	if err != nil {
		return nil, err
	}
	raw, err := c.t.roundTrip(ctx, rpcRequest{Method: method, Params: obj})
	if err != nil {
		return nil, err
	}
	if _, envErr := checkResultEnvelope(method, raw); envErr != nil {
		return nil, envErr
	}
	return raw, nil
}

// Initialize performs the MCP handshake: it sends initialize, records the
// negotiated protocol version, and sends the required notifications/initialized.
// It is the legacy-mode entry point only — the RC removed the handshake, so a
// stateless client refuses it rather than emit a method the spec deleted.
func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	if c.stateless() {
		return InitializeResult{}, fmt.Errorf("mcp: initialize is removed in the %s revision (use Discover)", revision20260728)
	}
	raw, err := c.t.roundTrip(ctx, rpcRequest{
		Method: "initialize",
		Params: initializeParams{
			ProtocolVersion: protocolVersion,
			Capabilities:    map[string]any{},
			ClientInfo:      clientInfo{Name: clientName, Version: clientVersion},
		},
	})
	if err != nil {
		return InitializeResult{}, fmt.Errorf("initialize: %w", err)
	}
	var res InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return InitializeResult{}, fmt.Errorf("initialize result: %w", err)
	}
	if res.ProtocolVersion != "" {
		c.t.setProtocolVersion(res.ProtocolVersion)
	}
	if err := c.t.notify(ctx, "notifications/initialized", nil); err != nil {
		return InitializeResult{}, fmt.Errorf("initialized notification: %w", err)
	}
	return res, nil
}

// Discover queries server/discover (RC: servers MUST implement it) for the
// server's supported versions, capabilities and identity. Stateless mode only —
// a legacy-mode client uses Initialize, and a pre-RC server would answer
// -32601 anyway.
func (c *Client) Discover(ctx context.Context) (discoverResult, error) {
	if !c.stateless() {
		return discoverResult{}, fmt.Errorf("mcp: %s requires the %s (stateless) mode", methodServerDiscover, revision20260728)
	}
	raw, err := c.call(ctx, methodServerDiscover, nil)
	if err != nil {
		return discoverResult{}, fmt.Errorf("%s: %w", methodServerDiscover, err)
	}
	var res discoverResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return discoverResult{}, fmt.Errorf("%s result: %w", methodServerDiscover, err)
	}
	return res, nil
}

// Listen opens a subscriptions/listen stream (RC: replaces the standalone GET
// SSE stream and resources/subscribe) and delivers each notification to
// onEvent until ctx is done or the server closes the stream. It requires the
// stateless mode AND a transport that supports streaming (the stateless HTTP
// transport); a transport without listen support is refused, never faked.
func (c *Client) Listen(ctx context.Context, filter subscriptionFilter, onEvent func(subscriptionEvent)) error {
	if !c.stateless() {
		return fmt.Errorf("mcp: %s requires the %s (stateless) mode", methodSubscriptionsListen, revision20260728)
	}
	l, ok := c.t.(interface {
		listen(context.Context, rpcRequest, func(subscriptionEvent)) error
	})
	if !ok {
		return fmt.Errorf("mcp: transport does not support %s", methodSubscriptionsListen)
	}
	params := struct {
		Notifications subscriptionFilter `json:"notifications"`
	}{Notifications: filter}
	obj, err := c.meta.inject(params)
	if err != nil {
		return err
	}
	return l.listen(ctx, rpcRequest{Method: methodSubscriptionsListen, Params: obj}, onEvent)
}

// ListTools returns every tool the server exposes, following pagination.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	err := c.paginate(ctx, "tools/list", func(raw json.RawMessage) (string, error) {
		var res listToolsResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return "", err
		}
		out = append(out, res.Tools...)
		return res.NextCursor, nil
	})
	return out, err
}

// ListResources returns every resource the server exposes, following pagination.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	err := c.paginate(ctx, "resources/list", func(raw json.RawMessage) (string, error) {
		var res listResourcesResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return "", err
		}
		out = append(out, res.Resources...)
		return res.NextCursor, nil
	})
	return out, err
}

// ListResourceTemplates returns every resource template the server exposes.
func (c *Client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	var out []ResourceTemplate
	err := c.paginate(ctx, "resources/templates/list", func(raw json.RawMessage) (string, error) {
		var res listResourceTemplatesResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return "", err
		}
		out = append(out, res.ResourceTemplates...)
		return res.NextCursor, nil
	})
	return out, err
}

// ListPrompts returns every prompt the server exposes, following pagination.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var out []Prompt
	err := c.paginate(ctx, "prompts/list", func(raw json.RawMessage) (string, error) {
		var res listPromptsResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return "", err
		}
		out = append(out, res.Prompts...)
		return res.NextCursor, nil
	})
	return out, err
}

// Close releases the underlying transport.
func (c *Client) Close() error { return c.t.Close() }

// paginate calls method repeatedly, passing each page's raw result to collect,
// which appends and returns the next cursor (""=done). It stops on an empty or
// non-advancing cursor and at the page cap, so a misbehaving server cannot loop.
// In stateless mode the first page's CacheableResult freshness hint is recorded
// (SEP-2549: cacheScope MUST be identical across the pages of one list request,
// so the first page is representative; ttlMs may vary per page).
func (c *Client) paginate(ctx context.Context, method string, collect func(json.RawMessage) (string, error)) error {
	cursor := ""
	for page := 0; page < maxPages; page++ {
		raw, err := c.call(ctx, method, listParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if page == 0 && c.stateless() {
			c.recordCacheHint(method, raw)
		}
		next, err := collect(raw)
		if err != nil {
			return fmt.Errorf("%s result: %w", method, err)
		}
		if next == "" || next == cursor {
			return nil
		}
		cursor = next
	}
	return nil
}

// recordCacheHint captures a result's SEP-2549 freshness metadata (ttlMs /
// cacheScope), the RC catalog-freshness signal. Absent fields yield no hint
// (a pre-RC or non-conformant answer is not fabricated into one).
func (c *Client) recordCacheHint(method string, raw json.RawMessage) {
	var meta struct {
		TTLMs      *int64 `json:"ttlMs"`
		CacheScope string `json:"cacheScope"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	if meta.TTLMs == nil && meta.CacheScope == "" {
		return
	}
	c.hints = append(c.hints, cacheHint{method: method, ttlMs: meta.TTLMs, scope: meta.CacheScope})
}

// cacheHints returns the freshness hints recorded during this client's calls.
func (c *Client) cacheHints() []cacheHint { return c.hints }
