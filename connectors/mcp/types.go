// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import "encoding/json"

// protocolVersion is the MCP protocol revision this connector advertises in
// Initialize (the legacy 2025-11-25 handshake path). The server echoes it or
// replies with one it supports; introspection (list methods) is stable across
// recent revisions, so a version mismatch is tolerated rather than fatal (the
// negotiated revision is recorded and surfaced per server — see revision.go).
//
// This constant is intentionally NOT updated to revision20260728: the 2026-07-28
// frozen RC removes Initialize entirely. The stateless path
// (introspectStateless) uses revision20260728 in `_meta` instead. This constant
// is only sent to servers that still speak the legacy Initialize handshake.
const protocolVersion = revision20251125

// clientName and clientVersion identify this introspection client to a server.
const (
	clientName    = "olivares-mcp-connector"
	clientVersion = "0.1.0"
)

// initializeParams is the initialize request payload. The client declares no
// capabilities: it only lists, it never receives roots/sampling/elicitation.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

// clientInfo identifies the connecting client.
type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the server's initialize response (the fields this connector
// uses). Meta is the optional `_meta` map: an existing MCP extension point the
// 2026-07-28 revision standardizes to carry W3C Trace Context (AIP-09 seam, tracecontext.go).
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ServerInfo      serverInfo     `json:"serverInfo"`
	Capabilities    map[string]any `json:"capabilities"`
	Instructions    string         `json:"instructions"`
	Meta            map[string]any `json:"_meta"`
}

// serverInfo identifies the server.
type serverInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// listParams is the common request payload for the paginated list methods.
type listParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ToolAnnotations are the UNTRUSTED behavioral hints a server attaches to a tool.
// Every field is a pointer so "absent" is distinguishable from "false" — the MCP
// spec defaults are asymmetric (readOnlyHint→false, destructiveHint→true), and
// the connector must honor that to infer R/RW correctly (see annotations.go).
type ToolAnnotations struct {
	Title           string `json:"title"`
	ReadOnlyHint    *bool  `json:"readOnlyHint"`
	DestructiveHint *bool  `json:"destructiveHint"`
	IdempotentHint  *bool  `json:"idempotentHint"`
	OpenWorldHint   *bool  `json:"openWorldHint"`
}

// Tool is one tool a server exposes (the introspection fields). OutputSchema and
// Icons are governance-relevant surface (AIP-04): a non-empty OutputSchema means
// the tool declares STRUCTURED OUTPUT (a JSON Schema for its result, introduced in
// revision 2025-06-18; the default dialect became JSON Schema 2020-12 in 2025-11-25
// via SEP-1613), and Icons is the per-tool icon metadata (SEP-973, 2025-11-25).
// Both are kept as raw JSON: the connector flags their PRESENCE as untrusted
// catalog metadata; it does not validate or trust their contents (the
// edge wire type carries no rich schema; presence is the signal).
type Tool struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Annotations *ToolAnnotations `json:"annotations"`
	// InputSchema is kept raw for ONE signal only: whether it declares a
	// non-default JSON Schema dialect via $schema (the RC default is 2020-12,
	// SEP-1613/SEP-2106). Its contents are UNTRUSTED and never validated here.
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Icons        json.RawMessage `json:"icons,omitempty"`
	// Meta is the tool's `_meta` map, kept raw for the MCP Apps link (SEP-1865): `_meta.ui.resourceUri` ties a tool to its ui:// template and
	// `_meta.ui.visibility` marks app-only tools (apps.go). UNTRUSTED catalog
	// metadata — parsed for governance signals, never trusted or executed.
	Meta json.RawMessage `json:"_meta,omitempty"`
}

// listToolsResult is the tools/list response.
type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor"`
}

// Resource is one resource a server exposes.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
	// Meta is the resource's `_meta` map, kept raw for the MCP Apps surface
	// (SEP-1865): `_meta.ui.csp` / `_meta.ui.permissions` declare a ui://
	// template's sandbox posture (apps.go). UNTRUSTED catalog metadata.
	Meta json.RawMessage `json:"_meta,omitempty"`
}

// listResourcesResult is the resources/list response.
type listResourcesResult struct {
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor"`
}

// ResourceTemplate is one templated resource a server exposes.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// listResourceTemplatesResult is the resources/templates/list response.
type listResourceTemplatesResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	NextCursor        string             `json:"nextCursor"`
}

// Prompt is one prompt/skill a server exposes.
type Prompt struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// listPromptsResult is the prompts/list response.
type listPromptsResult struct {
	Prompts    []Prompt `json:"prompts"`
	NextCursor string   `json:"nextCursor"`
}

// catalog is the full introspected surface of one MCP server.
type catalog struct {
	server    InitializeResult
	tools     []Tool
	resources []Resource
	templates []ResourceTemplate
	prompts   []Prompt
	// authBound is true when introspection was performed with an OAuth token bound to
	// this server (IDN-03 token-binding-verified=true). It is false for an open server
	// or one only detected as OAuth-protected.
	authBound bool
	// trace is the W3C Trace Context the server carried in the handshake (stable) or
	// server/discover (stateless) result's `_meta`, if any (AIP-09 seam).
	trace TraceContext
	// --- stateless (2026-07-28) introspection extras. ---
	// nextRevision is true when this catalog was gathered in the 2026-07-28 stateless mode.
	nextRevision bool
	// supportedVersions is the server/discover supportedVersions list (stateless only).
	supportedVersions []string
	// cacheHints are the SEP-2549 freshness hints observed on CacheableResults
	// (server/discover + first page of each list; stateless only).
	cacheHints []cacheHint
	// negotiatedDown is true when auto-negotiation first tried the 2026-07-28
	// frozen-RC path and then intentionally fell back to legacy Initialize after
	// the server proved it did not speak the RC.
	negotiatedDown bool
	// --- deprecation-aware posture inputs. ---
	// observed are the governance-relevant server-INITIATED requests/notifications
	// the transport saw while waiting for responses (sampling/roots/elicitation) —
	// the runtime seam surface.go reserved. The connector never answers them.
	observed []serverRequestObservation
	// authReg records HOW the OAuth client identified itself to the server's AS
	// (pre-registered/CIMD/DCR + the AS's capability flags); nil when the OAuth
	// flow did not run.
	authReg *authRegistrationObservation
}

// serverRequestObservation is one server-initiated JSON-RPC message observed
// during introspection. Only the method (and, for sampling, the includeContext
// value — a closed enum) is recorded; params are never persisted (docs/SECURITY-HARDENING.md).
type serverRequestObservation struct {
	method         string
	includeContext string // sampling/createMessage only: "", "none", "thisServer", "allServers"
}

// authRegistrationObservation records the OAuth client-identification path
// actually taken (clientreg.go priority order) plus the AS capability flags the
// DCR-deprecation posture rules grade against.
type authRegistrationObservation struct {
	method               string // identityPreRegistered | identityCIMD | identityDCR
	cimdSupported        bool   // AS advertised client_id_metadata_document_supported
	registrationEndpoint bool   // AS advertised a registration_endpoint
}
