// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
)

// stateless.go — wire vocabulary of the MCP 2026-07-28 frozen-RC stateless
// core (AIP-09). This is the connector default; the 2025-11-25 path
// (client.go/http.go) remains for backward compatibility when an operator
// explicitly opts out or auto-negotiation proves the server is legacy.
//
// Every identifier below was verified against the PRIMARY source
// (modelcontextprotocol.io/specification/draft/) on 2026-07-03. The
// frozen RC is scheduled for FINAL publication on 2026-07-28 and MUST be
// re-verified against the published spec after that date.

// Required per-request `_meta` keys (SEP-2575): with the initialize handshake
// removed, every request carries protocol version, client identity and client
// capabilities. A request missing any of them is malformed (-32602, HTTP 400).
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	// metaServerInfo is where DiscoverResult carries the server's name/version.
	// It lives INSIDE result._meta — the spec's DiscoverResult defines
	// `_meta['io.modelcontextprotocol/serverInfo']` and its normative example
	// nests it there (server/discover.mdx). An earlier revision of this file read
	// a TOP-LEVEL `serverInfo` field that the schema does not define, so the value
	// was always the zero struct and every consumer of it silently saw "".
	metaServerInfo = "io.modelcontextprotocol/serverInfo"
	// metaSubscriptionID tags every notification delivered on a
	// subscriptions/listen stream with the id of the originating listen request.
	metaSubscriptionID = "io.modelcontextprotocol/subscriptionId"
)

// methodServerDiscover is the discovery method servers MUST implement in the RC
// (it replaces the initialize capability exchange). methodSubscriptionsListen is
// the single long-lived notification stream that replaces the standalone GET SSE
// stream and resources/subscribe|unsubscribe.
const (
	methodServerDiscover      = "server/discover"
	methodSubscriptionsListen = "subscriptions/listen"
)

// RC Streamable HTTP routing headers (SEP-2243): mirrors of body values so L7
// intermediaries can route/enforce without parsing the body. Names are
// case-insensitive on the wire (canonical spellings below — note the spec mixes
// MCP-Protocol-Version and Mcp-Method/Mcp-Name casing); VALUES are
// case-sensitive and compared byte-exact. Mcp-Method rides every request and
// notification; Mcp-Name ONLY tools/call + prompts/get (params.name),
// resources/read (params.uri) and the Tasks-extension methods (params.taskId) —
// it MUST be omitted everywhere else. A missing/mismatched required header is
// HTTP 400 + -32020 HeaderMismatch.
const (
	headerMCPProtocolVersion = "MCP-Protocol-Version"
	headerMcpMethod          = "Mcp-Method"
	headerMcpName            = "Mcp-Name"
	headerMcpSessionID       = "Mcp-Session-Id" // REMOVED in the RC (ignored, never minted)
)

// notificationSubscriptionsAcknowledged MUST be the first message on a
// subscriptions/listen stream; its params echo the subset of notification types
// the server agreed to honor.
const notificationSubscriptionsAcknowledged = "notifications/subscriptions/acknowledged"

// Result envelope (SEP-2322 MRTR): every server result in the RC carries
// resultType. "complete" (or absent, for pre-RC servers) is a final result;
// "input_required" is a Multi-Round-Trip request for more input. The union is
// open (extensions add values — the Tasks extension answers tools/call with
// "task"), so an unrecognized value is surfaced to the caller, never guessed at.
const (
	resultTypeComplete      = "complete"
	resultTypeInputRequired = "input_required"
	resultTypeTask          = "task" // io.modelcontextprotocol/tasks extension
)

// extension identifiers (reverse-DNS, RC extension framework). Tasks moved out
// of core into io.modelcontextprotocol/tasks (SEP-2663, tasks/list REMOVED);
// MCP Apps is io.modelcontextprotocol/ui (SEP-1865 — note: NOT ".../apps").
const (
	extensionTasks   = "io.modelcontextprotocol/tasks"
	extensionMCPApps = "io.modelcontextprotocol/ui"
)

// cacheScope values (SEP-2549 CacheableResult). "private" results MUST NOT be
// served from a shared cache across authorization contexts; there are exactly
// two values in the RC schema.
const (
	cacheScopePublic  = "public"
	cacheScopePrivate = "private"
)

// requestMeta is the per-request identity the stateless client injects under
// `_meta`. The spec requires TWO of these — protocolVersion and
// clientCapabilities — and marks clientInfo as SHOULD; this client sends all
// three because withholding identity from a governed gateway helps nobody, but
// the server-side validation (rcRequestMetaValid) enforces only what the spec
// requires. capabilities is what the client declares
// for THIS request: the introspection client declares none (deny-closed — a
// conforming server then MUST NOT send it any MRTR inputRequests), and the
// Tasks helpers declare exactly the tasks extension.
type requestMeta struct {
	version      string
	info         clientInfo
	capabilities map[string]any
}

// nextRequestMeta is the introspection client's identity for the 2026-07-28
// revision: no capabilities declared (it only lists; it never accepts roots/
// sampling/elicitation input requests).
func nextRequestMeta() requestMeta {
	return requestMeta{
		version:      revision20260728,
		info:         clientInfo{Name: clientName, Version: clientVersion},
		capabilities: map[string]any{},
	}
}

// withExtensions returns a copy of m declaring the given extension ids (empty
// settings objects), e.g. the tasks extension for tasks/get|update|cancel — a
// server MUST NOT honor an extension the request did not declare (-32021).
func (m requestMeta) withExtensions(ids ...string) requestMeta {
	ext := map[string]any{}
	for _, id := range ids {
		ext[id] = map[string]any{}
	}
	caps := map[string]any{"extensions": ext}
	return requestMeta{version: m.version, info: m.info, capabilities: caps}
}

// inject returns params with the three required `_meta` keys added, preserving
// any existing params fields. params may be nil (e.g. server/discover carries
// nothing beyond `_meta`). A non-object params is rejected — `_meta` can only
// ride on a JSON object.
func (m requestMeta) inject(params any) (map[string]any, error) {
	obj := map[string]any{}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp: stateless params: %w", err)
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("mcp: stateless params must be a JSON object: %w", err)
		}
	}
	meta, _ := obj["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[metaProtocolVersion] = m.version
	meta[metaClientInfo] = map[string]any{"name": m.info.Name, "version": m.info.Version}
	meta[metaClientCapabilities] = m.capabilities
	obj["_meta"] = meta
	return obj, nil
}

// discoverResult is the server/discover response (the fields this connector
// uses). It is a CacheableResult, so ttlMs/cacheScope are REQUIRED top-level
// fields in the RC; extensions live INSIDE capabilities ("extensions" map).
// Meta is the result-level `_meta` (W3C Trace Context seam, tracecontext.go).
type discoverResult struct {
	ResultType        string         `json:"resultType"`
	SupportedVersions []string       `json:"supportedVersions"`
	Capabilities      map[string]any `json:"capabilities"`
	Instructions      string         `json:"instructions"`
	TTLMs             *int64         `json:"ttlMs"`
	CacheScope        string         `json:"cacheScope"`
	Meta              map[string]any `json:"_meta"`
}

// serverIdentity extracts `_meta["io.modelcontextprotocol/serverInfo"]`, the
// only place DiscoverResult defines it. The spec marks it SHOULD-include, so an
// absent or malformed value is not an error — it yields the zero identity and
// the caller reports "unknown", which is honest rather than fabricated.
//
// `title` is projected as well as name/version. The published `Implementation`
// type carries `title?`, `description?`, `websiteUrl?` and `icons?` on top of
// the two required members (schema.ts, Implementation). Only `title` is taken:
// posture.go scans it for homograph/confusable display names, so dropping it
// silently disarmed an existing defense — every server would have presented an
// empty title and passed the check vacuously. The other three optionals are
// deliberately NOT retained: they are self-reported, none of them feeds a
// decision here, and carrying an unused remote-controlled URL/icon list into
// the catalog is surface with no reader. Every field here is untrusted display
// data; none of it participates in authorization.
func (d discoverResult) serverIdentity() serverInfo {
	raw, ok := d.Meta[metaServerInfo]
	if !ok {
		return serverInfo{}
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return serverInfo{}
	}
	var si serverInfo
	if n, ok := obj["name"].(string); ok {
		si.Name = n
	}
	if t, ok := obj["title"].(string); ok {
		si.Title = t
	}
	if v, ok := obj["version"].(string); ok {
		si.Version = v
	}
	return si
}

// supports reports whether the server lists rev among its supported protocol
// versions.
func (d discoverResult) supports(rev string) bool {
	for _, v := range d.SupportedVersions {
		if v == rev {
			return true
		}
	}
	return false
}

// resultEnvelope is the RC result-envelope view used to classify a raw result
// before interpreting it: resultType plus the MRTR fields of an
// InputRequiredResult (inputRequests keys are surfaced for the finding — the
// request payloads themselves are never persisted, docs/SECURITY-HARDENING.md).
type resultEnvelope struct {
	ResultType    string                     `json:"resultType"`
	InputRequests map[string]json.RawMessage `json:"inputRequests"`
	RequestState  string                     `json:"requestState"`
}

// errInputRequired is the deny-closed MRTR outcome: the server answered with
// resultType "input_required". The introspection client declares NO client
// capabilities, so a conforming server never sends this; receiving it is
// surfaced as a terminal, non-retried outcome (the spec's sanctioned decline is
// simply not retrying — there is no decline error code). Keys are the
// server-assigned inputRequests identifiers (names only, never payloads).
type errInputRequired struct {
	method string
	keys   []string
}

func (e *errInputRequired) Error() string {
	return fmt.Sprintf("mcp: %s answered input_required with %d input request(s); declined deny-closed (introspection declares no client capabilities)", e.method, len(e.keys))
}

// checkResultEnvelope classifies a raw RC result. A "complete" (or absent — a
// pre-RC server) envelope passes through; "input_required" is the deny-closed
// errInputRequired; any other resultType (e.g. the Tasks extension's "task") is
// returned for the CALLER to interpret — the open union is never guessed at
// here. An unparseable envelope passes through untouched (the per-method
// decoder is the authority on the result's shape).
func checkResultEnvelope(method string, raw json.RawMessage) (resultType string, err error) {
	var env resultEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return "", nil
	}
	if env.ResultType == resultTypeInputRequired {
		keys := make([]string, 0, len(env.InputRequests))
		for k := range env.InputRequests {
			keys = append(keys, k)
		}
		return env.ResultType, &errInputRequired{method: method, keys: keys}
	}
	return env.ResultType, nil
}

// cacheHint is one CacheableResult's freshness metadata (SEP-2549), recorded
// per probed method as a catalog-freshness signal. The introspection itself
// does not cache (every Gather is a fresh pass); the hint is surfaced so the
// catalog layer can reason about staleness windows.
type cacheHint struct {
	method string
	ttlMs  *int64
	scope  string
}

// SubscriptionFilter is the subscriptions/listen opt-in filter (params field
// "notifications"): each notification type is strictly opt-in (a server MUST
// NOT deliver types the client did not request).
//
// It is exported because the inline Resource Server's streaming upstream seam
// is implemented at the composition root. Keeping the wire vocabulary here
// preserves the connector/core license boundary without making the composition
// layer copy protocol structs.
type SubscriptionFilter struct {
	ToolsListChanged      bool     `json:"toolsListChanged,omitempty"`
	PromptsListChanged    bool     `json:"promptsListChanged,omitempty"`
	ResourcesListChanged  bool     `json:"resourcesListChanged,omitempty"`
	ResourceSubscriptions []string `json:"resourceSubscriptions,omitempty"`
}

// subscriptionFilter is retained as an internal alias for the existing client
// implementation and tests.
type subscriptionFilter = SubscriptionFilter

// SubscriptionEvent is one notification delivered on a subscriptions/listen
// stream, demultiplexed by the metaSubscriptionID `_meta` tag.
type SubscriptionEvent struct {
	Method         string
	SubscriptionID string
	Params         json.RawMessage
}

// subscriptionEvent is retained as an internal alias for the existing client
// implementation and tests.
type subscriptionEvent = SubscriptionEvent
