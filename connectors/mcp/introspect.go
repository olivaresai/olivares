// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Transport selectors accepted in a server spec's transport/type field.
const (
	transportStdio = "stdio"
	transportHTTP  = "http"
	transportSSE   = "sse"
)

// introspect connects to one MCP server and lists its tools, resources,
// resource templates and prompts — gated on the capabilities the server
// advertises, so an unsupported method is never called. This helper keeps the
// legacy-compatible default for direct tests/callers; Source.introspectOne uses
// introspectWithPreview so the connector-level next_revision_preview flag can
// drive auto-negotiation. A connect or handshake/discover failure is returned as
// an error (the connector reports it as a finding); a per-list failure after
// that yields a partial catalog rather than aborting the whole server.
func introspect(ctx context.Context, spec serverSpec) (catalog, error) {
	return introspectWithPreview(ctx, spec, false)
}

// introspectWithPreview applies the tri-state next_revision contract. Explicit
// true is stateless-only and fail-loud; explicit false is legacy-only. When the
// per-server field is absent, connectorPreview=true auto-negotiates: try the
// 2026-07-28 frozen-RC stateless path, then fall back to legacy only when the
// failure proves the server does not speak the RC. Transport/network errors are
// not proof of an old server and therefore do not fall back.
func introspectWithPreview(ctx context.Context, spec serverSpec, connectorPreview bool) (catalog, error) {
	if spec.NextRevision != nil {
		if *spec.NextRevision {
			return introspectStateless(ctx, spec)
		}
		return introspectLegacy(ctx, spec)
	}
	if connectorPreview {
		return introspectAuto(ctx, spec)
	}
	return introspectLegacy(ctx, spec)
}

func introspectAuto(ctx context.Context, spec serverSpec) (catalog, error) {
	cat, err := introspectStateless(ctx, spec)
	if err == nil {
		return cat, nil
	}
	if !statelessFailureAllowsLegacyFallback(err) {
		return catalog{}, err
	}
	cat, legacyErr := introspectLegacy(ctx, spec)
	if legacyErr != nil {
		return catalog{}, legacyErr
	}
	cat.negotiatedDown = true
	return cat, nil
}

// introspectLegacy is the 2025-11-25 Initialize path. Callers route explicit
// next_revision=true to introspectStateless before ever reaching here
// (introspectWithPreview), so this function is unconditionally legacy.
func introspectLegacy(ctx context.Context, spec serverSpec) (catalog, error) {
	t, err := transportFor(ctx, spec)
	if err != nil {
		return catalog{}, err
	}
	c := newClient(t)
	defer func() { _ = c.Close() }()

	init, err := c.Initialize(ctx)
	if err != nil {
		return catalog{}, err
	}
	cat := catalog{server: init}
	// AIP-09: extract any W3C Trace Context the server carried in the initialize
	// result's `_meta` (forward seam; empty when absent).
	cat.trace = extractTraceContext(init.Meta)

	listInto(ctx, c, init.Capabilities, &cat)
	// Record whether introspection used an OAuth token bound to this server
	// (token-binding-verified). stdio transports do not implement this.
	if tb, ok := t.(interface{ tokenBound() bool }); ok {
		cat.authBound = tb.tokenBound()
	}
	stampObservations(t, &cat)
	return cat, nil
}

type unsupportedDiscoverVersionsError struct {
	server    string
	supported []string
}

func (e *unsupportedDiscoverVersionsError) Error() string {
	return fmt.Sprintf("mcp: server %q answered %s but does not list revision %s (supportedVersions: %s)",
		e.server, methodServerDiscover, revision20260728, strings.Join(e.supported, ", "))
}

func statelessFailureAllowsLegacyFallback(err error) bool {
	var rpc *rpcError
	if errors.As(err, &rpc) {
		switch {
		case rpc.Code == -32601:
			return true
		case isHeaderMismatchCode(rpc.Code):
			return true
		case isUnsupportedProtocolVersionCode(rpc.Code):
			supported := unsupportedVersionDetail(rpc)
			return supported != nil && !containsRevision(supported, revision20260728)
		default:
			return false
		}
	}
	var unsupported *unsupportedDiscoverVersionsError
	if errors.As(err, &unsupported) {
		return !containsRevision(unsupported.supported, revision20260728)
	}
	return false
}

func containsRevision(revisions []string, want string) bool {
	for _, rev := range revisions {
		if rev == want {
			return true
		}
	}
	return false
}

// stampObservations copies the transport's runtime observations onto the
// catalog: server-initiated requests seen mid-stream and the OAuth registration
// path taken. Both are optional transport capabilities (the tokenBound pattern).
func stampObservations(t transport, cat *catalog) {
	if o, ok := t.(interface {
		observedServerRequests() []serverRequestObservation
	}); ok {
		cat.observed = o.observedServerRequests()
	}
	if a, ok := t.(interface {
		authRegistration() *authRegistrationObservation
	}); ok {
		cat.authReg = a.authRegistration()
	}
}

// introspectStateless is the 2026-07-28 stateless path: capabilities come from
// server/discover (servers MUST implement it), every request carries `_meta`,
// and the catalog records the stateless extras (supported versions, cache hints).
// A server that answers discover but does not list the 2026-07-28 revision fails
// loudly — the operator opted this server in; nothing falls back silently.
func introspectStateless(ctx context.Context, spec serverSpec) (catalog, error) {
	t, err := statelessTransportFor(ctx, spec)
	if err != nil {
		return catalog{}, err
	}
	c := newStatelessClient(t)
	defer func() { _ = c.Close() }()

	disc, err := c.Discover(ctx)
	if err != nil {
		if supported := unsupportedVersionDetail(err); supported != nil {
			return catalog{}, fmt.Errorf("mcp: server %q does not support revision %s (supports: %s): %w",
				spec.Name, revision20260728, strings.Join(supported, ", "), err)
		}
		return catalog{}, err
	}
	if !disc.supports(revision20260728) {
		return catalog{}, &unsupportedDiscoverVersionsError{server: spec.Name, supported: disc.SupportedVersions}
	}

	// Map the discover result onto the catalog's server view so every consumer
	// (surface, posture, capability gating, edges) works unchanged in both modes.
	cat := catalog{
		server: InitializeResult{
			ProtocolVersion: revision20260728,
			ServerInfo:      disc.serverIdentity(),
			Capabilities:    disc.Capabilities,
			Instructions:    disc.Instructions,
			Meta:            disc.Meta,
		},
		nextRevision:      true,
		supportedVersions: disc.SupportedVersions,
	}
	cat.trace = extractTraceContext(disc.Meta)
	if disc.TTLMs != nil || disc.CacheScope != "" {
		cat.cacheHints = append(cat.cacheHints, cacheHint{method: methodServerDiscover, ttlMs: disc.TTLMs, scope: disc.CacheScope})
	}

	listInto(ctx, c, disc.Capabilities, &cat)
	cat.cacheHints = append(cat.cacheHints, c.cacheHints()...)
	if tb, ok := t.(interface{ tokenBound() bool }); ok {
		cat.authBound = tb.tokenBound()
	}
	stampObservations(t, &cat)
	return cat, nil
}

// listInto runs the capability-gated list probes shared by both revisions (the
// list methods are wire-identical across them; only the surrounding protocol
// differs).
func listInto(ctx context.Context, c *Client, caps map[string]any, cat *catalog) {
	if hasCapability(caps, "tools") {
		cat.tools, _ = c.ListTools(ctx)
	}
	if hasCapability(caps, "resources") {
		cat.resources, _ = c.ListResources(ctx)
		cat.templates, _ = c.ListResourceTemplates(ctx)
	}
	if hasCapability(caps, "prompts") {
		cat.prompts, _ = c.ListPrompts(ctx)
	}
}

// transportFor builds the transport for a spec: stdio when a command is given (or
// the transport is explicitly stdio), Streamable HTTP when a URL is given. The
// legacy HTTP+SSE transport is not separately implemented; a "sse" spec is
// attempted over Streamable HTTP (best-effort) since many servers accept both.
func transportFor(ctx context.Context, spec serverSpec) (transport, error) {
	switch {
	case spec.Transport == transportStdio || (spec.Transport == "" && spec.Command != ""):
		return newStdioTransport(ctx, spec)
	case spec.Transport == transportHTTP || spec.Transport == transportSSE || (spec.Transport == "" && spec.URL != ""):
		if spec.URL == "" {
			return nil, fmt.Errorf("mcp: server %q has transport %q but no url", spec.Name, spec.Transport)
		}
		return newHTTPTransport(spec)
	default:
		return nil, fmt.Errorf("mcp: server %q: cannot determine transport (no command, no url)", spec.Name)
	}
}

// statelessTransportFor builds the RC-mode transport: the stateless
// Streamable HTTP variant for URL specs, and the UNCHANGED stdio transport for
// command specs — stdio carries no headers or session, so the stateless
// semantics (`_meta` per request, no handshake) live entirely in the Client.
// An "sse" spec is attempted over stateless Streamable HTTP: the RC formally
// deprecates the old HTTP+SSE transport (SEP-2596), so there is nothing newer
// to fall back to.
func statelessTransportFor(ctx context.Context, spec serverSpec) (transport, error) {
	switch {
	case spec.Transport == transportStdio || (spec.Transport == "" && spec.Command != ""):
		return newStdioTransport(ctx, spec)
	case spec.Transport == transportHTTP || spec.Transport == transportSSE || (spec.Transport == "" && spec.URL != ""):
		if spec.URL == "" {
			return nil, fmt.Errorf("mcp: server %q has transport %q but no url", spec.Name, spec.Transport)
		}
		return newStatelessHTTPTransport(spec)
	default:
		return nil, fmt.Errorf("mcp: server %q: cannot determine transport (no command, no url)", spec.Name)
	}
}

// hasCapability reports whether the server advertised capability cap (a non-nil
// entry under the capability key), so the connector only calls list methods the
// server supports.
func hasCapability(caps map[string]any, name string) bool {
	if caps == nil {
		return false
	}
	v, ok := caps[strings.ToLower(name)]
	return ok && v != nil
}
