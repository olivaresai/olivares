// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// rsnext.go — the PEP's 2026-07-28 frozen-RC additions, all
// deny-closed:
//
//  1. L7 header enforcement (SEP-2243). The RC mirrors method/name into the
//     Mcp-Method / Mcp-Name headers precisely so intermediaries can decide
//     WITHOUT parsing the body. In rc-strict mode the RS requires the headers
//     on every POST, makes the
//     tools/call deny decision (toolset / scope / role) from the headers BEFORE
//     reading the body, and rejects requests whose MCP-Protocol-Version does not
//     mandate header–body validation (the spec's downgrade-attack guard: never
//     trust unvalidated headers). In legacy mode the 2025-11-25 behavior is
//     unchanged — except
//     that a request which VOLUNTEERS the headers is still checked for
//     header↔body consistency after the body is read: a proven mismatch is a
//     smuggling attempt and is refused in every mode. Dual mode selects the RC
//     or legacy path per request from MCP-Protocol-Version.
//  2. Cache-scope enforcement (SEP-2549). The RS itself never caches; what it
//     controls is what DOWNSTREAM caches may do, via Cache-Control derived from
//     the upstream result's ttlMs/cacheScope. private (and everything without
//     explicit cache metadata) is no-store/private; tools/list is ALWAYS
//     downgraded to private because the RS filters it per token/role, so the
//     response varies by authorization context — serving it from a shared cache
//     would leak one principal's tool view to another.
//  3. Trace correlation (SEP-414). The RC carries W3C Trace Context in the
//     request `_meta`; the RS prefers it over the HTTP traceparent header,
//     propagates it upstream, and records it on every ToolDecision so a PEP
//     decision can be correlated with the gen_ai spans of the same trace.
//
// Error-code note: 2026-07-28 assigns -32020 as the Streamable HTTP
// HeaderMismatch error. The pre-freeze draft's -32001 collided with what was
// then this PEP's app-level rpcAccessDenied; that collision is gone twice over,
// because the assigned wire code moved AND the PEP's own codes did. The app
// codes are NOT unchanged, as this note used to claim: stage 6 moved them out
// of the JSON-RPC reserved range entirely (rpcAccessDenied -31001,
// rpcUpstreamError -31002 in rs.go; -31010..-31012 in jsonrpc.go), which is
// what basic/index.mdx:153-155 instructs for codes the spec does not define.

// rpcHeaderMismatch is the RC HeaderMismatch error code (HTTP 400): a required
// standard header is missing/malformed, or a header value does not match the
// corresponding request-body value.
const rpcHeaderMismatch = -32020

// rpcHeaderMismatchPreFreeze is accepted only for client-side transition
// classification: pre-freeze draft servers used -32001 before HeaderMismatch
// moved into the -32020..-32099 spec-assigned range.
const rpcHeaderMismatchPreFreeze = -32001

// removedNextRevisionMethods are core methods the RC deletes. A flagged
// (RC-mode) RS answers them 404 + -32601 Method not found at L7 — per the spec
// for unknown methods — instead of forwarding traffic the upstream no longer
// understands. tasks/list and tasks/result died with the Tasks redesign
// (SEP-2663: a task list "can't be scoped safely without sessions");
// resources/subscribe|unsubscribe were replaced by subscriptions/listen; ping,
// logging/setLevel and the initialize handshake were removed from the core.
var removedNextRevisionMethods = map[string]struct{}{
	"initialize":                         {},
	"notifications/initialized":          {},
	"ping":                               {},
	"logging/setLevel":                   {},
	"resources/subscribe":                {},
	"resources/unsubscribe":              {},
	"notifications/roots/list_changed":   {},
	"notifications/elicitation/complete": {},
	"tasks/list":                         {},
	"tasks/result":                       {},
}

// mcpNameRequirement classifies a method's Mcp-Name contract: the header is
// REQUIRED for tools/call + prompts/get (params.name), resources/read
// (params.uri) and the Tasks-extension methods (params.taskId) — and MUST be
// omitted for every other method.
func mcpNameRequired(method string) bool {
	switch method {
	case "tools/call", "prompts/get", "resources/read", "tasks/get", "tasks/update", "tasks/cancel":
		return true
	}
	return false
}

// enforceRevisionPreBody selects the per-request revision path. rcRequest=true
// means the caller must run RC post-body validation after parsing. decided=true
// means a denial was already written and ServeHTTP must stop.
func (rs *ResourceServer) enforceRevisionPreBody(ctx context.Context, w http.ResponseWriter, r *http.Request, tok validatedToken) (rcRequest, decided bool) {
	switch rs.revisionMode {
	case revisionModeLegacy:
		return false, false
	case revisionModeRCStrict:
		return true, rs.enforceNextHeadersPreBody(ctx, w, r, tok)
	case revisionModeDual:
		v := strings.TrimSpace(r.Header.Get(headerMCPProtocolVersion))
		switch {
		case v == revision20260728:
			return true, rs.enforceNextHeadersPreBody(ctx, w, r, tok)
		case revisionIndex(v) >= 0 && revisionIndex(v) < revisionIndex(revision20260728):
			return false, false
		case v == "":
			// Nothing named: the client did not state a version at all, which is a
			// header-level defect, not a version this server declines.
			rs.writeHeaderMismatch(w, headerMCPProtocolVersion+" header required with accepted revisions: "+strings.Join(acceptedHTTPRevisions(), ", "))
			return false, true
		default:
			// A version WAS named and this server does not implement it.
			rs.writeUnsupportedProtocolVersion(w, v)
			return false, true
		}
	default:
		// Construction validates the mode; this branch is a final deny-closed
		// guard if a test or future mutation bypasses the constructor.
		rs.writeHeaderMismatch(w, "unknown ResourceServer revision mode")
		return false, true
	}
}

func acceptedHTTPRevisions() []string {
	return append([]string(nil), revisionTimeline...)
}

// enforceNextHeadersPreBody is the flag-ON, pre-body L7 gate. It returns true
// when the request was decided (denied) at header level — the body is never
// read on a deny. Order per the transport spec: version guard (downgrade
// protection) → required headers → removed-method check → tools/call policy
// pre-check from Mcp-Name.
func (rs *ResourceServer) enforceNextHeadersPreBody(ctx context.Context, w http.ResponseWriter, r *http.Request, tok validatedToken) (denied bool) {
	trace := r.Header.Get("traceparent")

	// Downgrade guard: only a revision that mandates header–body validation may
	// be enforced at header level; older/absent versions are rejected, never
	// trusted (spec: "reject the request rather than trusting unvalidated
	// header values").
	if v := strings.TrimSpace(r.Header.Get(headerMCPProtocolVersion)); v != revision20260728 {
		// Split by cause, because the spec assigns them different codes and the
		// client needs different information from each: an ABSENT header is a
		// header-level defect (-32020), while a NAMED version this server does not
		// implement MUST return UnsupportedProtocolVersion (-32022) carrying the
		// supported list the client is expected to retry with.
		if v == "" {
			rs.writeHeaderMismatch(w, "MCP-Protocol-Version header required")
		} else {
			rs.writeUnsupportedProtocolVersion(w, v)
		}
		return true
	}
	method := r.Header.Get(headerMcpMethod)
	if method == "" {
		rs.writeHeaderMismatch(w, "Mcp-Method header required")
		return true
	}
	if _, removed := removedNextRevisionMethods[method]; removed {
		rs.writeRPCError(w, http.StatusNotFound, nil, -32601, "method removed in revision "+revision20260728)
		return true
	}
	// DECODE ONCE, HERE. The =?base64?…?= sentinel must be resolved at the FIRST
	// point that inspects the value, not merely where it is later compared to the
	// body: streamable-http.mdx §Value Encoding — "Servers and intermediaries that
	// need to inspect these values MUST decode them accordingly."
	//
	// Fixing only the post-body comparison left this L7 gate resolving the RAW
	// header against the toolset, so a conforming client whose tool name needed
	// encoding was denied 403 as an unknown tool before the body was ever read —
	// the refusal simply moved earlier. Measured end to end, not reasoned.
	rawName := r.Header.Get(headerMcpName)
	name, nameErr := decodeMcpHeaderValue(rawName)
	if nameErr != nil {
		// A sentinel the server cannot decode cannot be validated against the body
		// either, so it is a header defect (HeaderMismatch), NOT an unknown tool:
		// answering "tool not permitted" would misreport a malformed header as an
		// authorization decision.
		rs.writeHeaderMismatch(w, "Mcp-Name header carries a malformed =?base64?...?= value")
		return true
	}
	if mcpNameRequired(method) && name == "" {
		rs.writeHeaderMismatch(w, "Mcp-Name header required for "+method)
		return true
	}
	if !mcpNameRequired(method) && name != "" {
		rs.writeHeaderMismatch(w, "Mcp-Name header must be omitted for "+method)
		return true
	}

	// L7 policy pre-check for tools/call: the same deny-by-default toolset,
	// scope and role gates the body path enforces, decided from Mcp-Name alone.
	// Only the DENY is final here; an allow still passes through body parsing,
	// header↔body consistency, and (for destructive tools) the approval gate.
	if method == "tools/call" {
		policy, ok := rs.toolset.resolve(name)
		if !ok {
			rs.auditTraced(ctx, tok, name, "", false, "deny-by-default at L7 (Mcp-Name not in server toolset)", "", "MCP01", trace)
			rs.writeRPCError(w, http.StatusForbidden, nil, rpcAccessDenied, "tool not permitted by server policy")
			return true
		}
		if !tok.hasScope(policy.RequiredScope) {
			rs.auditTraced(ctx, tok, name, policy.RequiredScope, false, "insufficient scope at L7", "", "MCP02", trace)
			rs.challengeScope(w, nil, policy.RequiredScope)
			return true
		}
		if !roleAllowed(policy, tok.Roles) {
			rs.auditTraced(ctx, tok, name, policy.RequiredScope, false, "caller role not permitted at L7", "", "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, nil, rpcAccessDenied, "tool not permitted for caller role")
			return true
		}
	}
	return false
}

// headerBodyConsistent verifies the RC routing headers against the parsed body
// (the body is the source of truth; the mirrors must match byte-exact). It is
// applied whenever the request carries an Mcp-Method/Mcp-Name header — in BOTH
// modes — and always in flag-ON mode: a request that volunteers a mirror that
// contradicts its body is refused (-32020, HTTP 400) regardless of the mode.
// ok=true means consistent (or nothing to check).
func (rs *ResourceServer) headerBodyConsistent(r *http.Request, req rsRequest, requireNextHeaders bool) (reason string, ok bool) {
	hm := r.Header.Get(headerMcpMethod)
	hn := r.Header.Get(headerMcpName)
	if !requireNextHeaders && hm == "" && hn == "" {
		return "", true // legacy request without mirrors: nothing volunteered, nothing to check
	}
	if hm != "" && hm != req.Method {
		return "Mcp-Method header does not match body method", false
	}
	if hn != "" {
		if !mcpNameRequired(req.Method) {
			return "Mcp-Name header present for a method that does not carry one", false
		}
		// MUST decode before comparing. streamable-http.mdx §Value Encoding:
		// "servers MUST decode an encoded `Mcp-Name` or `Mcp-Param-{Name}` value
		// before comparing it to the corresponding request body value during
		// Server Validation." Tool/prompt names are only SHOULD-constrained to
		// header-safe characters, so a conforming client carries anything outside
		// that set as `=?base64?{value}?=`.
		//
		// Comparing the raw header (as this did) rejected exactly those conforming
		// clients with HeaderMismatch — a false refusal of a correct request, and
		// the mirror image of the risk this validation exists for. The sibling
		// Mcp-Param-* path already decoded; only this one did not.
		// Decoded here too: this helper runs on the dual/legacy path as well, where
		// the pre-body gate did not execute. Decoding twice is idempotent (a value
		// without the sentinel is returned unchanged).
		decoded, derr := decodeMcpHeaderValue(hn)
		if derr != nil {
			// A malformed sentinel is a header the server cannot validate against
			// the body, which the spec makes a rejection ("A header value contains
			// invalid characters"), never a silent pass.
			return "Mcp-Name header carries a malformed =?base64?...?= value", false
		}
		if decoded != bodyMcpName(req) {
			return "Mcp-Name header does not match body value", false
		}
	}
	if requireNextHeaders && mcpNameRequired(req.Method) && hn == "" {
		return "Mcp-Name header required for " + req.Method, false
	}
	return "", true
}

// mcpParamsConsistent validates RC Mcp-Param-* routing mirrors against
// params.arguments after parsing the body. Values use the header's string form:
// JSON strings compare as their decoded string, numbers as json.Number.String,
// booleans as true/false, and null as "null". The =?base64?...?= sentinel is
// decoded before comparison so non-ASCII argument values can be mirrored safely.
func (rs *ResourceServer) mcpParamsConsistent(r *http.Request, req rsRequest) (reason string, ok bool) {
	params := mcpParamHeaders(r.Header)
	if len(params) == 0 {
		return "", true
	}
	var p struct {
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return "Mcp-Param header present but params.arguments is malformed", false
	}
	for name, headerValue := range params {
		raw, exists := argumentByHeaderName(p.Arguments, name)
		if !exists {
			return "Mcp-Param-" + name + " header names an absent argument", false
		}
		want, err := decodeMcpHeaderValue(headerValue)
		if err != nil {
			return "Mcp-Param-" + name + " header is not valid sentinel base64", false
		}
		got, scalar := canonicalJSONScalarString(raw)
		if !scalar {
			return "Mcp-Param-" + name + " body argument is not a JSON scalar", false
		}
		if want != got {
			return "Mcp-Param-" + name + " header does not match body argument", false
		}
	}
	return "", true
}

// argumentByHeaderName resolves the body argument an Mcp-Param header mirrors:
// exact name first, then a case-insensitive match (HTTP canonicalizes header
// names, so the exact argument casing is not recoverable from the wire). An
// AMBIGUOUS case-insensitive match — two arguments differing only by case — is
// refused rather than guessed (deny-closed; picking one would ride
// nondeterministic map-iteration order).
func argumentByHeaderName(args map[string]json.RawMessage, headerName string) (json.RawMessage, bool) {
	if raw, ok := args[headerName]; ok {
		return raw, true
	}
	var found json.RawMessage
	matches := 0
	for name, raw := range args {
		if strings.EqualFold(name, headerName) {
			found = raw
			matches++
		}
	}
	if matches != 1 {
		return nil, false
	}
	return found, true
}

func mcpParamHeaders(h http.Header) map[string]string {
	const prefix = "Mcp-Param-"
	out := map[string]string{}
	for key, values := range h {
		if len(values) == 0 || !strings.HasPrefix(http.CanonicalHeaderKey(key), prefix) {
			continue
		}
		name := strings.TrimPrefix(http.CanonicalHeaderKey(key), prefix)
		if name == "" {
			continue
		}
		out[name] = values[0]
	}
	return out
}

func decodeMcpHeaderValue(v string) (string, error) {
	if !strings.HasPrefix(v, "=?base64?") || !strings.HasSuffix(v, "?=") {
		return v, nil
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(v, "=?base64?"), "?=")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func canonicalJSONScalarString(raw json.RawMessage) (string, bool) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	case nil:
		return "null", true
	default:
		return "", false
	}
}

// bodyMcpName extracts the body value the Mcp-Name header mirrors for a
// name-carrying method (name / uri / taskId).
func bodyMcpName(req rsRequest) string {
	var p struct {
		Name   string `json:"name"`
		URI    string `json:"uri"`
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return ""
	}
	switch req.Method {
	case "resources/read":
		return p.URI
	case "tasks/get", "tasks/update", "tasks/cancel":
		return p.TaskID
	default:
		return p.Name
	}
}

// writeHeaderMismatch writes the RC header-validation rejection: HTTP 400 +
// JSON-RPC -32020 HeaderMismatch (spec MUST for servers; the id is null — the
// body was deliberately not read).
func (rs *ResourceServer) writeHeaderMismatch(w http.ResponseWriter, msg string) {
	rs.writeRPCError(w, http.StatusBadRequest, nil, rpcHeaderMismatch, "header mismatch: "+msg)
}

// writeUnsupportedProtocolVersion answers a request that NAMED a protocol
// version this server does not implement.
//
// streamable-http.mdx §Protocol Version Header, verbatim: "If the server does
// not implement the requested protocol version (whether the version is unknown
// to the server, or is a known version the server has chosen not to support),
// it MUST respond with 400 Bad Request and an UnsupportedProtocolVersionError
// listing its supported versions." The schema makes the payload obligatory:
// required ["code","data"], with data.required ["requested","supported"].
//
// This is a DIFFERENT failure from a missing/unparseable header, which is
// legitimately HeaderMismatch (-32020). Both used to funnel into
// writeHeaderMismatch, so a client that named an unsupported version got
// -32020 with no data — and therefore no `supported` list to fall back to,
// which is the entire point of the error. The repository's own conformance
// fixture (testdata/2026-07-28_unsupported_protocol_version.json) already
// encoded the correct shape: fixture and server contradicted each other.
func (rs *ResourceServer) writeUnsupportedProtocolVersion(w http.ResponseWriter, requested string) {
	rs.writeRPCErrorData(w, http.StatusBadRequest, nil, rpcUnsupportedProtocolVersion,
		"Unsupported protocol version",
		map[string]any{
			"requested": requested,
			"supported": rs.supportedRevisions(),
		})
}

// supportedRevisions is what this server will actually accept RIGHT NOW, which
// depends on the configured mode — not the full protocol timeline.
//
// The distinction is load-bearing rather than cosmetic: the `supported` list is
// what a client retries with. In RC-strict the gateway accepts only
// 2026-07-28, so advertising the whole timeline would send a conforming client
// to retry 2025-11-25 and be refused again — an infinite, well-behaved loop
// caused by our own answer.
func (rs *ResourceServer) supportedRevisions() []string {
	switch rs.revisionMode {
	case revisionModeRCStrict:
		return []string{revision20260728}
	case revisionModeLegacy:
		// Legacy mode never reaches this writer (it does not police the header),
		// but be explicit rather than fall through to "everything".
		return append([]string(nil), revisionTimeline[:revisionIndex(revision20260728)]...)
	default:
		return acceptedHTTPRevisions()
	}
}

func isHeaderMismatchCode(code int) bool {
	return code == rpcHeaderMismatch || code == rpcHeaderMismatchPreFreeze
}

// --- SEP-2549 cache-scope enforcement ---------------------------------------

// cacheableResultMethods are the six operations whose results carry REQUIRED
// ttlMs/cacheScope in the RC.
var cacheableResultMethods = map[string]struct{}{
	methodServerDiscover:       {},
	"tools/list":               {},
	"prompts/list":             {},
	"resources/list":           {},
	"resources/templates/list": {},
	"resources/read":           {},
}

// setCacheHeaders translates a relayed result's SEP-2549 cache metadata into
// HTTP caching directives for downstream intermediaries. Deny-closed: anything
// without explicit, well-formed PUBLIC cache metadata is no-store/private —
// and tools/list is ALWAYS private because the RS filters it per token/role
// (the response varies by authorization context; a shared cache MUST NOT serve
// one principal's filtered view to another). Vary: Authorization is set on
// every response for the same reason.
func (rs *ResourceServer) setCacheHeaders(w http.ResponseWriter, method string, result json.RawMessage) {
	w.Header().Add("Vary", "Authorization")
	if _, cacheable := cacheableResultMethods[method]; !cacheable {
		w.Header().Set("Cache-Control", "no-store")
		return
	}
	var meta struct {
		TTLMs      *int64 `json:"ttlMs"`
		CacheScope string `json:"cacheScope"`
	}
	if json.Unmarshal(result, &meta) != nil || meta.TTLMs == nil || *meta.TTLMs < 0 {
		w.Header().Set("Cache-Control", "no-store")
		return
	}
	maxAge := strconv.FormatInt(*meta.TTLMs/1000, 10) // round DOWN; never extend the server's freshness grant
	switch {
	case meta.CacheScope == cacheScopePublic && method != "tools/list":
		w.Header().Set("Cache-Control", "public, max-age="+maxAge)
	case meta.CacheScope == cacheScopePublic || meta.CacheScope == cacheScopePrivate:
		// private — or a public tools/list downgraded to private (per-principal filtering).
		w.Header().Set("Cache-Control", "private, max-age="+maxAge)
	default:
		w.Header().Set("Cache-Control", "no-store") // unknown scope: never guess
	}
}

// downgradeCacheScopePrivate rewrites a result's cacheScope to "private" when
// present, preserving every other field — applied to the per-principal
// filtered tools/list so the BODY metadata agrees with the HTTP directive.
func downgradeCacheScopePrivate(result json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(result, &obj) != nil {
		return result
	}
	if _, has := obj["cacheScope"]; !has {
		return result
	}
	obj["cacheScope"] = json.RawMessage(`"` + cacheScopePrivate + `"`)
	out, err := json.Marshal(obj)
	if err != nil {
		return result
	}
	return out
}

// --- SEP-414 trace correlation ------------------------------------------------

// requestTraceParent resolves the W3C trace context for a request: the RC
// carries it in the request `_meta` (preferred — it is the MCP-level signal the
// rest of the gen_ai pipeline correlates on); the HTTP traceparent header is
// the fallback. Only correlation identifiers, never payloads (docs/SECURITY-HARDENING.md).
func requestTraceParent(r *http.Request, params json.RawMessage) string {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) == nil {
		if tc := extractTraceContext(p.Meta); tc.present() {
			return tc.TraceParent
		}
	}
	return r.Header.Get("traceparent")
}

// rcRequestMetaValid validates the per-request protocol fields a 2026-07-28
// request MUST carry in `params._meta`, and that the version it declares is the
// one the transport negotiated.
//
// basic/index.mdx §"Per-request protocol fields" (read verbatim) makes exactly
// two fields required — `io.modelcontextprotocol/protocolVersion` and
// `io.modelcontextprotocol/clientCapabilities` — while `clientInfo` is only
// SHOULD ("Clients SHOULD include … unless specifically configured not to") and
// `logLevel` is optional. "A request missing any required field is malformed;
// the server MUST reject it with JSON-RPC error code -32602 (Invalid params).
// On HTTP, the response status MUST be 400 Bad Request."
//
// The version comparison is separate and carries the transport's code: the
// Protocol Version Header section says "The header value MUST match", and a
// mismatch is HeaderMismatch (-32020) rather than invalid params — the request
// is well-formed, it is the two statements about itself that disagree.
//
// This is a SECURITY property, not conformance bookkeeping: without it the
// header decides the era for authorization and routing while the body decides
// what the upstream executes, which is the split-brain a gateway exists to
// prevent. An earlier revision of this package called these "the three required
// keys", counting clientInfo among them — wrong in both directions: it demanded
// what the spec merely recommends while never checking what the spec requires.
func rcRequestMetaValid(r *http.Request, req rsRequest) (code int, reason string, ok bool) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) != nil {
		return rpcInvalidParams, "params is not an object", false
	}
	if p.Meta == nil {
		return rpcInvalidParams, "params._meta is required on a " + revision20260728 + " request", false
	}

	rawVersion, hasVersion := p.Meta[metaProtocolVersion]
	if !hasVersion {
		return rpcInvalidParams, "params._meta." + metaProtocolVersion + " is required", false
	}
	var version string
	if json.Unmarshal(rawVersion, &version) != nil || version == "" {
		return rpcInvalidParams, "params._meta." + metaProtocolVersion + " must be a non-empty string", false
	}

	// clientCapabilities is required but MAY be an empty object: declaring no
	// capabilities is a meaningful statement (the server must then not rely on
	// any), so presence is what is checked, never contents.
	rawCaps, hasCaps := p.Meta[metaClientCapabilities]
	if !hasCaps {
		return rpcInvalidParams, "params._meta." + metaClientCapabilities + " is required", false
	}
	var caps map[string]json.RawMessage
	if json.Unmarshal(rawCaps, &caps) != nil {
		return rpcInvalidParams, "params._meta." + metaClientCapabilities + " must be an object", false
	}

	if hv := strings.TrimSpace(r.Header.Get(headerMCPProtocolVersion)); hv != "" && hv != version {
		return rpcHeaderMismatch, headerMCPProtocolVersion + " header (" + hv + ") does not match params._meta." +
			metaProtocolVersion + " (" + version + "); the transport era and the executed request must be the same revision", false
	}
	return 0, "", true
}
