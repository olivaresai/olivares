// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// rs.go is the inline MCP Resource Server PEP http.Handler (AIP-02). It is mounted IN
// FRONT OF an MCP dispatcher and enforces, in order, on every JSON-RPC request:
//
//	Origin check (PR#1439: invalid Origin → 403)
//	  → bearer present?            (no → 401 + WWW-Authenticate resource_metadata)
//	  → token valid + audience-bound (fail-closed; cross-audience → 401 invalid_token)
//	  → method:
//	      tools/call → toolset deny-by-default → scope (step-up SEP-835: 403 + scope
//	                   challenge) → HITL for destructive (ApprovalGate) → forward
//	      reads      → forward (tools/list filtered to the server-owned toolset)
//
// and forwards admitted/gated calls to the upstream WITHOUT the inbound token
// (no-passthrough). The unauthenticated discovery endpoint serves RFC 9728 Protected
// Resource Metadata. Authorization is keyed off the VALIDATED TOKEN, never the
// Mcp-Session-Id (so a future stateless core can drop the session without re-arch).

// JSON-RPC error codes used by the PEP: the JSON-RPC standard ones, plus two
// APPLICATION codes allocated OUTSIDE the reserved range on the spec's own
// instruction.
//
// MCP 2026-07-28 §"Error codes" (basic/index.mdx:117-155, read verbatim) sets
// three rules this allocation obeys:
//
//   - `-32002` MUST NOT be emitted by an implementation of this revision: it
//     means resource-not-found in 2025-11-25 and earlier, replaced by -32602.
//     The PEP used to emit it as its own "upstream forward failed" code and
//     reasoned that direction disambiguated it — but the rule is unconditional,
//     and a client that saw it would be entitled to read "resource not found".
//   - `-32000..-32019` is a LEGACY sub-range: new codes MUST NOT be allocated
//     there and new implementations SHOULD NOT use it at all. The old
//     `rpcAccessDenied = -32001` sat inside it.
//   - New codes for purposes the spec does not define SHOULD be allocated
//     OUTSIDE the JSON-RPC reserved range (-32768..-32000) entirely.
//
// So the application codes live at -31xxx: above -32000, hence outside the
// reserved range, and free of any spec meaning present or future. The last two
// digits are kept from the retired values so log greps and runbooks map across.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcInvalidParams  = -32602
	rpcAccessDenied   = -31001 // app: authorization deny (deny-by-default / scope / approval)
	rpcUpstreamError  = -31002 // app: admitted+gated but the upstream forward failed
)

// maxRPCBody caps an inbound JSON-RPC request body.
const maxRPCBody = 4 << 20 // 4 MiB

// rsRequest is a permissive inbound JSON-RPC 2.0 request: the id may be a string, a
// number, or null (a server must accept all three), so it is kept raw and echoed
// verbatim. An absent/null id marks a notification (no response body).
type rsRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports whether the request is a JSON-RPC notification (no id, or a
// null id) — it expects no response.
func (r rsRequest) isNotification() bool {
	id := strings.TrimSpace(string(r.ID))
	return id == "" || id == "null"
}

// ServeHTTP routes the unauthenticated discovery endpoint and the gated JSON-RPC
// endpoint. Everything except the well-known PRM path requires a valid, audience-bound
// bearer.
func (rs *ResourceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// RFC 9728 Protected Resource Metadata is unauthenticated discovery (a client
	// fetches it BEFORE it has a token). Match the path and any path-suffixed variant.
	if r.Method == http.MethodGet && isMetadataPath(r.URL.Path) {
		rs.serveMetadata(w)
		return
	}

	// Origin check (Streamable HTTP MUST, PR#1439): a browser request carrying an
	// Origin not on the allowlist (or any Origin when no allowlist is configured) is
	// a DNS-rebinding risk → 403. A non-browser client sends no Origin and passes.
	if !rs.originAllowed(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Authorization required. A missing token is answered with the RFC 9728
	// challenge so the client can discover the authorization server. The RS accepts
	// Bearer and DPoP schemes; a DPoP proof may also accompany the Bearer scheme.
	raw, authScheme := authorizationTokenOf(r.Header.Get("Authorization"))
	if raw == "" {
		rs.challengeMissing(w)
		return
	}
	tok, err := rs.validator.validate(r.Context(), raw)
	if err != nil {
		// Any validation failure — bad signature, expired, missing aud, or a token
		// minted for ANOTHER audience (the confused-deputy reject) — is 401
		// invalid_token (OAuth 2.1 §5.2). The reason is never leaked to the client.
		rs.challengeInvalidToken(w)
		return
	}
	tok, err = rs.enforceTokenBinding(r, raw, authScheme, tok)
	if err != nil {
		if errors.Is(err, errDPoPUseNonce) {
			rs.challengeDPoPNonce(w)
			return
		}
		rs.challengeInvalidToken(w)
		return
	}

	// revision mode decides whether this request is RC or legacy. In
	// dual mode the decision is per request from MCP-Protocol-Version: 2026-07-28
	// takes the full RC L7 header path, known older dated revisions take the
	// legacy body path, and absent/unknown values are refused. Rationale: every
	// supported dated HTTP revision at or above the gateway floor requires the
	// version header, so a headerless client is below the supported floor; the RS
	// denies closed and never guesses. An inbound Mcp-Session-Id is ignored in
	// every mode (sessions are removed in 2026-07-28; this RS never minted one —
	// authorization is keyed off the validated token).
	rcRequest, decided := rs.enforceRevisionPreBody(r.Context(), w, r, tok)
	if decided {
		return
	}

	body, err := readBounded(r)
	if err != nil {
		rs.writeRPCError(w, http.StatusBadRequest, nil, rpcParseError, "request too large or unreadable")
		return
	}
	var req rsRequest
	if json.Unmarshal(body, &req) != nil || req.Method == "" {
		rs.writeRPCError(w, http.StatusBadRequest, nil, rpcParseError, "malformed JSON-RPC request")
		return
	}

	// Header↔body consistency (all modes): the body is the source of truth and a
	// volunteered mirror that contradicts it is a smuggling attempt → HTTP 400 +
	// -32020 HeaderMismatch (spec MUST). In the RC path the mirrors are required.
	if reason, ok := rs.headerBodyConsistent(r, req, rcRequest); !ok {
		rs.writeHeaderMismatch(w, reason)
		return
	}
	if rcRequest {
		if reason, ok := rs.mcpParamsConsistent(r, req); !ok {
			rs.writeHeaderMismatch(w, reason)
			return
		}
		// The per-request protocol fields, validated SERVER-SIDE and before any
		// dispatch. Without this the gateway had a split brain: the transport era
		// was selected from the header while the body declared something else, so
		// one layer authorized and routed on the header and the upstream executed
		// on the body. Measured: header 2026-07-28 + body protocolVersion
		// "1900-01-01" was forwarded with HTTP 200.
		if code, reason, ok := rcRequestMetaValid(r, req); !ok {
			if code == rpcHeaderMismatch {
				rs.writeHeaderMismatch(w, reason)
			} else {
				rs.writeRPCError(w, http.StatusBadRequest, req.ID, code, reason)
			}
			return
		}
	}

	if req.Method == methodSubscriptionsListen {
		if !rcRequest {
			// subscriptions/listen does not exist in the legacy protocol. A
			// legacy-declared request therefore remains an honest method-not-found;
			// the durable relay is only reachable through the revision whose stream
			// framing and per-request metadata contract it implements.
			trace := requestTraceParent(r, req.Params)
			rs.auditTraced(r.Context(), tok, methodSubscriptionsListen, "", false,
				"subscriptions/listen requires revision "+revision20260728, "", "MCP07", trace)
			rs.writeRPCError(w, http.StatusNotFound, req.ID, -32601,
				"subscriptions/listen requires revision "+revision20260728)
			return
		}
		// The long-lived path has its own streaming upstream and durable cursor
		// ledger. It still runs only after the ordinary Resource Server bearer,
		// token-binding, revision-header and body/meta validation above, and the
		// handler performs its filter-specific scope decision before opening SSE.
		// Missing either seam returns 503 deny-closed; it never falls through to the
		// request/response forwarder, which cannot classify a stream outcome.
		rs.handleSubscriptionListen(r.Context(), w, r, req, tok)
		return
	}

	if rcRequest {
		if _, removed := removedNextRevisionMethods[req.Method]; removed {
			rs.writeRPCError(w, http.StatusNotFound, req.ID, -32601, "method removed in revision "+revision20260728)
			return
		}
	}

	rs.dispatch(r.Context(), w, r, req, tok)
}

// Per-family scopes the F-06 method-authorization matrix requires for the
// content-bearing non-tool methods (mirroring the `<family>:<action>` scope
// convention the tool policies use, e.g. "tools:read"). A token authenticated for
// tools is NOT thereby authorized to read arbitrary resources/prompts/completions.
const (
	scopeResourcesRead  = "resources:read"
	scopePromptsRead    = "prompts:read"
	scopeCompletionRead = "completion:read"
)

// knownMCPNotifications is the allowlist (H2) of client↔server notification methods the
// MCP revisions this gateway pins define (2025-11-25 … the 2026-07-28 RC; see
// revision.go). A notifications/* method NOT on this list is UNKNOWN and falls through to
// DEFAULT-DENY: the gateway must not relay an arbitrary, attacker-chosen
// `notifications/<x>` verbatim to the upstream merely because it carries the reserved
// prefix (the prior strings.HasPrefix match did exactly that — the smuggling gap H2
// closes). Verified against the frozen RC on 2026-07-17; re-verify after 2026-07-28
// publication. Entries removed in the RC (initialized, roots/list_changed,
// elicitation/complete) are still listed because the RC path rejects them earlier via
// removedNextRevisionMethods, while the legacy path legitimately relays them.
var knownMCPNotifications = map[string]struct{}{
	"notifications/initialized":                {}, // client→server handshake (removed in the RC)
	"notifications/cancelled":                  {}, // either direction — in-flight request cancellation
	"notifications/progress":                   {}, // either direction — progress-token updates
	"notifications/message":                    {}, // server→client logging channel
	"notifications/resources/updated":          {}, // server→client — a subscribed resource changed
	"notifications/resources/list_changed":     {}, // server→client
	"notifications/tools/list_changed":         {}, // server→client
	"notifications/prompts/list_changed":       {}, // server→client
	"notifications/roots/list_changed":         {}, // client→server (removed in the RC)
	"notifications/elicitation/complete":       {}, // async elicitation completion (removed in the RC)
	"notifications/subscriptions/acknowledged": {}, // 2026-07-28 stateless subscription ack
}

// methodPolicy is the F-06 per-method authorization matrix for the generic dispatch path
// (DEFAULT-DENY). Dedicated handlers (tools/call, ui resources/read, elicitation, sampling,
// tasks) are routed BEFORE this table. A method NOT in the table is UNKNOWN → rejected,
// never forwarded upstream with only a bearer. A known method maps to its required scope; an
// empty scope means a valid, audience-bound token suffices — the handshake, protocol and
// tools/list discovery (which is additionally filtered to the server toolset at forward).
//
// The content-bearing reads AND the resources/prompts LISTINGS require an explicit
// per-family scope, mirroring the tools/call posture that an authenticated token is not, by
// itself, authorization. Gating the listings (H1) is DELIBERATELY STRICTER than tools/list,
// which lists-then-filters: those listings expose sensitive resource URIs (file:///…) and
// prompt names, and — unlike tools/list — the gateway has NO per-resource filtering model,
// so the least-privilege posture is to require the READ family scope (resources:read /
// prompts:read) the corresponding content reads already demand. A token without that scope
// gets a 403 step-up, never an enumeration. NOTE FOR THE OWNER (): if per-resource
// policy is ever added to the gateway, this MAY be relaxed to "list + filter" to match
// tools/list; until then the listings are deny-by-default.
func methodPolicy(method string) (requiredScope string, known bool) {
	// H2: a notification carries no server content and expects no response (empty scope),
	// but ONLY a KNOWN MCP notification is admitted. An unknown notifications/* is NOT
	// relayed verbatim — it falls through to default-deny below.
	if strings.HasPrefix(method, "notifications/") {
		if _, ok := knownMCPNotifications[method]; ok {
			return "", true
		}
		return "", false // unknown notification → default-deny (never a verbatim forward)
	}
	switch method {
	case "initialize", "ping", "logging/setLevel": // handshake + protocol
		return "", true
	case methodServerDiscover:
		// MCP 2026-07-28 server/discover.mdx: "lets a client query a server's
		// supported protocol versions, capabilities, and identity BEFORE sending
		// any other requests. Servers MUST implement it." It is the stateless
		// model's replacement for the initialize handshake, so it sits with the
		// protocol methods: an audience-bound token suffices, no family scope.
		//
		// It was previously ABSENT from this table, so the only RPC the spec makes
		// mandatory fell through to default-deny and the gateway answered 403 —
		// a conforming client could not begin. Admitting it exposes no server
		// content: the result is versions, capabilities, identity and cache hints.
		// The per-family scopes still gate every listing and read behind it.
		return "", true
	case "tools/list": // discovery: admitted, then filtered to the server-owned toolset
		return "", true
	case "resources/list", "resources/templates/list": // H1: listings gated by the read-family scope
		return scopeResourcesRead, true
	case "prompts/list": // H1: prompt listing gated by the prompt read-family scope
		return scopePromptsRead, true
	case "resources/read", "resources/subscribe", "resources/unsubscribe":
		return scopeResourcesRead, true
	case "prompts/get":
		return scopePromptsRead, true
	case "completion/complete":
		return scopeCompletionRead, true
	}
	return "", false // unknown → default-deny
}

// dispatch routes an authenticated request: tools/call through the gate,
// resources/read of a ui:// template through the MCP Apps gate (SEP-1865), and every other method through the F-06 per-method authorization
// matrix (default-deny) before an admitted forward (tools/list filtered to the
// server-owned toolset).
func (rs *ResourceServer) dispatch(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) {
	if req.Method == "tools/call" {
		rs.handleToolsCall(ctx, w, r, req, tok)
		return
	}
	if req.Method == "resources/read" && rs.handleUIRead(ctx, w, r, req, tok) {
		return
	}
	// elicitation/sampling runtime PEP — the "future seam" surface.go named.
	if req.Method == "elicitation/create" {
		rs.handleElicitation(ctx, w, r, req, tok)
		return
	}
	if req.Method == "sampling/createMessage" {
		rs.handleSampling(ctx, w, r, req, tok)
		return
	}
	// Round-4: the OPERATOR reconciliation family, routed before the client
	// task methods and before the default-deny matrix (see taskreconcile.go). It
	// is gated by its own dedicated privileged scope and every mutating action is
	// evidence-bound like the rest of stage 4.
	if isTaskReconcileMethod(req.Method) {
		rs.handleTaskReconcile(ctx, w, r, req, tok)
		return
	}
	if isTaskMethod(req.Method) {
		rs.handleTaskMethod(ctx, w, r, req, tok)
		return
	}

	// F-06: per-method authorization matrix (default-deny). An UNKNOWN method is refused
	// (never forwarded with only a bearer); a KNOWN content-bearing read must carry its
	// family scope. Every decision is audited (best-effort ledger parity with tools/call).
	trace := requestTraceParent(r, req.Params)
	requiredScope, known := methodPolicy(req.Method)
	if !known {
		rs.auditTraced(ctx, tok, req.Method, "", false, "deny-by-default (method not in gateway policy matrix)", "", "MCP02", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "method not permitted by gateway policy")
		return
	}
	if !tok.hasScope(requiredScope) {
		// Insufficient scope for a content method → 403 + a step-up scope challenge (SEP-835).
		rs.auditTraced(ctx, tok, req.Method, requiredScope, false, "insufficient scope for method", "", "MCP02", trace)
		rs.challengeScope(w, req.ID, requiredScope)
		return
	}

	// Admitted read/notification → forward (no token passthrough). A notification gets
	// 202 and no body. (Evidence enforcement for this generic surface lands in stage
	// 6; until then it keeps the legacy best-effort audit with a zero binding.)
	res, err := rs.upstream.Forward(ctx, rs.upstreamReq(r, req.Method, req.Params, tok))
	result := res.Result
	if err != nil {
		if req.isNotification() {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if requiredScope != "" {
			rs.auditTraced(ctx, tok, req.Method, requiredScope, true, "authorized; upstream forward failed", "", "MCP07", trace)
		}
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, "upstream forward failed")
		return
	}
	if req.isNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method == "tools/list" {
		result = rs.filterToolsList(result, tok) // deny-by-default at discovery (per-tool + per-role)
	}
	if (req.Method == "initialize" || req.Method == methodServerDiscover) && rs.durableTasks == nil {
		var ferr error
		result, ferr = withoutTasksCapability(result)
		if ferr != nil {
			rs.auditTraced(ctx, tok, req.Method, "", false,
				"upstream capability response could not be projected without MCP Tasks",
				"", "MCP07", trace)
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream returned a malformed capability response")
			return
		}
	}
	// MRTR on the generic path. mrtr.mdx:184-192 sanctions InputRequiredResult on
	// exactly three client requests — prompts/get, resources/read, tools/call —
	// and says servers "MUST NOT send InputRequiredResult responses on any other
	// client requests".
	//
	// tools/call is mediated in its own handler; the other two arrive HERE, where
	// nothing classified the result at all, so a server could ask the user for
	// input through prompts/get and the payload reached the caller ungoverned —
	// the same content the elicitation PEP inspects when it comes through its own
	// door. Mediating it closes that bypass without removing the capability: a
	// conforming exchange still works, it is now inspected.
	if decided := rs.governGenericMRTR(ctx, w, req, tok, result, trace); decided {
		return
	}
	// Ledger parity with tools/call: audit the ALLOW for the scope-gated content reads
	// (discovery/handshake/notifications are admitted without an authorization event).
	if requiredScope != "" {
		rs.auditTraced(ctx, tok, req.Method, requiredScope, true, "method authorized", "", "MCP07", trace)
	}
	_ = rs.writeResult(w, req.Method, req.ID, result)
}

// handleToolsCall is the live tools/call gate: server-owned toolset deny-by-default →
// scope enforcement (step-up SEP-835) → HITL for destructive tools (ApprovalGate) →
// upstream forward (no token passthrough). Every exit is audited (minimal data).
func (rs *ResourceServer) handleToolsCall(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) {
	// SEP-414: the trace context every decision below is audited with —
	// the request `_meta` traceparent when present (the RC standard location),
	// else the HTTP header — so PEP decisions correlate with gen_ai spans.
	trace := requestTraceParent(r, req.Params)

	// (review round-1 P0): STRICT canonicalization is the FIRST thing that
	// touches the params — before the tool name is even resolved. It rejects
	// duplicate object keys at every depth AND case-variant aliases of the reserved
	// keys (name/arguments/_meta/op-key), extracts the operation key and STRIPS it,
	// and produces one deterministic canonical encoding used for the plan hash, the
	// EffectDigest AND the forwarded bytes (the bytes governed are the bytes sent).
	// The gate then reads the tool name from THIS strict tree with exact casing — a
	// case-insensitive struct unmarshal is exactly the smuggling vector (authorize
	// "search", forward "delete_db"). A structural failure is a protocol refusal
	// (400/-32602) BEFORE the claim and before any forward.
	canon, cerr := canonicalizeToolCallParams(req.Params)
	if cerr != nil {
		rs.auditTraced(ctx, tok, "", "", false,
			"tools/call params refused by strict canonicalization (dup/case-alias/malformed keys)", "", "MCP02", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"malformed tools/call params (strict decoding refused)")
		return
	}
	if strings.TrimSpace(canon.Name) == "" {
		// A well-formed request missing a string name is a SEP-1303 input failure →
		// HTTP 200 with isError:true, so the model can self-correct (not a protocol
		// error). Structural malformations took the 400 path above.
		rs.writeToolError(w, req.ID, "tools/call requires a string 'name'")
		return
	}
	toolName := canon.Name
	// K5: Tasks is an optional extension backed by a durable authority. A
	// request that declares it may cause the upstream to create an asynchronous
	// task, so the persistence precondition is checked before any forward. The
	// ordinary standalone gateway remains compatible for non-Tasks calls.
	if canon.DeclaresTasks && rs.durableTasks == nil {
		rs.auditTraced(ctx, tok, toolName, "", false,
			"MCP Tasks requested without a durable task store; request refused before upstream dispatch",
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
			"MCP Tasks is unavailable because durable task persistence is not configured")
		return
	}

	policy, ok := rs.toolset.resolve(toolName)
	if !ok {
		// Deny-by-default: a tool with no server-owned policy entry (or an explicit
		// deny, or a SEP-986-invalid name) is refused. This closes MCP01/MCP03 (tool
		// poisoning: the gate reads the server toolset, never the tool's annotation).
		rs.auditTraced(ctx, tok, toolName, "", false, "deny-by-default (tool not in server toolset)", "", "MCP01", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "tool not permitted by server policy")
		return
	}

	// Scope enforcement (MCP02 scope creep) + step-up (SEP-835): an insufficient scope
	// is a 403 carrying a WWW-Authenticate scope challenge so the client can step up.
	if !tok.hasScope(policy.RequiredScope) {
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, "insufficient scope", "", "MCP02", trace)
		rs.challengeScope(w, req.ID, policy.RequiredScope)
		return
	}

	// Per-role allowlist (E1): a role-restricted tool requires the caller's token to
	// carry one of the tool's AllowedRoles. Deny-closed — an empty or non-matching role set
	// is refused (least-privilege, MCP02). It is NOT a scope step-up (a role is an identity
	// attribute, not a scope the client can request), so it returns a plain 403, not a
	// scope challenge. The Claude MCP API has no native roles; this is the PEP-side layer.
	if !roleAllowed(policy, tok.Roles) {
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, "caller role not permitted for tool", "", "MCP02", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "tool not permitted for caller role")
		return
	}

	// ROUND-4 R4-05: the RETAINED task-inventory bound, checked BEFORE the forward.
	//
	// Any tools/call may answer with a durable task handle, and once the upstream
	// has created one the gateway has only two choices left: retain it (quarantine
	// deliberately BYPASSES the active caps — forgetting a live external task is the
	// failure being prevented) or forget it (a permanent invisible orphan). Round-3
	// therefore had no bound at all: a caller sitting at its active cap produced one
	// fresh, non-expiring quarantine per call and `byID` grew without limit, making
	// every lookup and every sweep scan an ever-growing map.
	//
	// The bound is enforced at the only point where a NEW task can still be
	// PREVENTED. It is deny-closed and it is not a license to drop anything: the
	// retained records leave only through a proven terminal confirmation or an
	// explicit operator retirement (the tasks/reconcile/* surface).
	//
	// ROUND-5 R5-02: the check RESERVES the slot atomically instead of reading a
	// snapshot. Round-4 read the counts and released the ledger mutex immediately,
	// so N concurrent callers all observed the same pre-forward count and all
	// passed — the reviewer's barrier probe reached 12 retained records under a
	// per-owner cap of 2. The ticket counts against the bound for the WHOLE window
	// in which this call's task may be created, and ends only after that task is
	// stored (consume) or provably absent (release). The deferred release covers
	// every path below, including the panic/early-return ones; it is idempotent, so
	// the explicit consume/release calls that mark the honest reason still stand.
	admissionOwner := taskOwnerFromToken(rs.tenant, tok)
	ticket, sat, admitted := rs.taskLedger.reserveAdmission(admissionOwner)
	if !admitted {
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
			fmt.Sprintf("retained task inventory saturated (owner %d/%d, gateway %d/%d); task-producing forwards refused until reconciled",
				sat.OwnerRetained, sat.OwnerCap, sat.TotalRetained, sat.TotalCap), "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusTooManyRequests, req.ID, rpcAccessDenied,
			"the retained task inventory is full; reconcile the retained tasks before issuing more calls")
		return
	}
	defer ticket.release()

	// pin verification — a tool whose definition changed since the operator
	// approved it is a rug-pull signal (MCP04). Deny-closed on mismatch or error.
	// The gate is additive: when PinVerifier is nil (community build) this block
	// is skipped and tools/call proceeds exactly as before.
	pin := pinBinding{State: "unwired"}
	if rs.pinVerifier != nil {
		// The call-time fingerprint binds the tool name and the CANONICAL governed
		// params (the same bytes the digest binds and the upstream receives).
		// The enterprise verifier stores the full definition fingerprint at
		// introspection time (ToolFingerprint) and can compare against it; the
		// call-time hash tells the verifier WHICH call triggered the check. The
		// call-time hash is NOT bound into the EffectDigest (it is a hash of params
		// the digest already binds); the APPROVED pin identity is (below).
		fp := toolCallFingerprint(toolName, canon.Forward)
		if attestor, ok := rs.pinVerifier.(ToolPinVerifyAttestor); ok && attestor != nil {
			// Round-3: the ATOMIC decision+attestation — both produced under
			// ONE pin-store snapshot, so the identity bound into the evidence is
			// exactly the identity that authorized (the removed two-step
			// ApprovedPin/Pins() bridge was a TOCTOU: a re-pin between Verify and a
			// separate attestation read bound an identity that never authorized).
			va, verr := attestor.VerifyAndAttest(ctx, rs.tenant, toolName, fp)
			if verr != nil {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
					"pin verification error (fail-closed)", "", "MCP04", trace)
				rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
					"tool pin verification unavailable")
				return
			}
			if !va.Allowed {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
					"pin mismatch: "+va.Reason, "", "MCP04", trace)
				rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
					"tool definition changed since approval (rug-pull detected)")
				return
			}
			if va.Attested {
				// Attested implies a COMPLETE identity: an attestation with empty
				// fields would misstate the evidence — deny-closed.
				if strings.TrimSpace(va.Pin.Fingerprint) == "" || strings.TrimSpace(va.Pin.Version) == "" {
					rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
						"pin attestation incomplete (fail-closed)", "", "MCP04", trace)
					rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
						"tool pin attestation unavailable")
					return
				}
				pin = pinBinding{State: "attested", Fingerprint: va.Pin.Fingerprint, Version: va.Pin.Version}
			} else {
				pin = pinBinding{State: "verified"} // no pin at decision time (first-use TOFU)
			}
		} else {
			allowed, pinReason, perr := rs.pinVerifier.Verify(ctx, rs.tenant, toolName, fp)
			if perr != nil {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
					"pin verification error (fail-closed)", "", "MCP04", trace)
				rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
					"tool pin verification unavailable")
				return
			}
			if !allowed {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
					"pin mismatch: "+pinReason, "", "MCP04", trace)
				rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
					"tool definition changed since approval (rug-pull detected)")
				return
			}
			// No atomic attestation capability: bind posture with EXPLICIT-ABSENT
			// identity markers. Honest absence — never a separate re-read that could
			// bind an identity Verify did not authorize (the round-3 TOCTOU).
			pin = pinBinding{State: "verified"}
		}
	}

	// COAZ evaluation — call the AuthZEN PDP with the COAZ-mapped request
	// for centralized, policy-driven MCP tool authorization. This gate is ADDITIVE:
	// a nil evaluator (community build) skips it; the toolset/scope/role/pin gates
	// all ran first. Deny-closed on error.
	coaz := coazBinding{State: "unwired"}
	if rs.coazEvaluator != nil {
		coazScopes := make(map[string]struct{}, len(tok.Scopes))
		for k, v := range tok.Scopes {
			coazScopes[k] = v
		}
		dec, cerr := rs.coazEvaluator.EvaluateToolCall(ctx, COAZRequest{
			Subject:     tok.Subject,
			Issuer:      tok.Issuer,
			Tool:        toolName,
			ServerURI:   rs.resource,
			Scopes:      coazScopes,
			Annotations: policy.Annotations,
			Tenant:      rs.tenant,
		})
		if cerr != nil {
			rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
				"COAZ evaluation error (fail-closed)", "", "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
				"tool authorization evaluation unavailable")
			return
		}
		if !dec.Allow {
			rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
				"COAZ deny: "+dec.Reason, "", "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
				"tool not permitted by authorization policy")
			return
		}
		// Bind the consulted-allow posture + the evaluator's STABLE references
		// (round-2: DecisionRef/PolicyVersion — empty = explicit absence). The
		// human-readable Reason text is deliberately NEVER bound (cosmetic edits
		// must not cause false rebinds).
		coaz = coazBinding{State: "allow", DecisionRef: dec.DecisionRef, PolicyVersion: dec.PolicyVersion}
	}

	// HITL for a non-readOnly/destructive tool (server-owned classification, NOT the
	// tool's UNTRUSTED annotation): require an ApprovalGate authorization bound to the
	// (tool, subject, args-shape) plan. Deny-closed on any gate error or non-approval.
	// the plan binds the CANONICAL argument digest — the same argument identity
	// the EffectDigest binds, so approval and evidence can never disagree about which
	// arguments were authorized.
	approvalRef, approvedPlan := "", ""
	if policy.Destructive {
		plan := toolCallPlanHash(toolName, tok.Subject, hashArgs(canon.Args))
		dec, gerr := rs.gate.Authorize(ctx, ToolApprovalRequest{
			Tenant: rs.tenant, Subject: tok.Subject, Tool: toolName,
			Scope: policy.RequiredScope, PlanHash: plan, RequestedBy: tok.Subject,
		})
		if gerr != nil {
			rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, "gate error (fail-closed)", "", "MCP07", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "approval gate error")
			return
		}
		// Review round 2 (blocker 4, same class as S5-05): the equality is
		// STRICT. `plan` is always non-empty (a canonical argument digest), so an
		// approval carrying an EMPTY PlanHash — bound to no plan — is not an approval
		// for THIS plan. The prior `PlanHash != "" &&` guard let an unbound approval
		// authorize a destructive tools/call.
		if !dec.Allowed() || dec.PlanHash != plan {
			rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, "destructive tool not approved ("+string(dec.Status)+")", dec.ApprovalRef, "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "destructive tool requires human approval ("+string(dec.Status)+")")
			return
		}
		approvalRef, approvedPlan = dec.ApprovalRef, plan
	}

	// Round-1 F-07: the mediator inspects the EXACT-CASED inputResponses
	// member extracted from the strict tree — never a case-insensitive re-parse
	// of the forwarded bytes (a case-folding upstream would consume the other
	// alias). A case-variant alias was already refused by canonicalization.
	if rs.mediateMRTRInputResponses(ctx, w, req, tok, canon.InputResponses, trace) {
		return
	}

	// --- evidence enforcement: claim → fence → forward → settle → respond ---
	//
	// The frozen S5 law (sdk/evidence.go): the effect is emitted ONLY against a
	// FRESH claim anchored for the exact binding, re-fenced immediately before the
	// dispatch; the outcome settles durably BEFORE the response is written. The
	// historical post-hoc "authorized; upstream forward failed" audits are GONE —
	// they are settlement outcomes of the anchored claim now.
	authorizedReason := "tools/call authorized"
	if policy.AppOnly {
		authorizedReason = "app-only tools/call authorized (UI-originated via rendered MCP App, SEP-1865)"
	}
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		// Cannot mint an operation identity (randomness failure): evidence refusal
		// with NO operation id (design §5: omit operation_id when derivation failed).
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
			"operation-id derivation failed (fail-closed)", "", "MCP07", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	policyDigest := toolCallPolicyDigest(policy, pin, coaz)
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveToolCallEffectDigest(
			rs.tenant, rs.resource, "tools/call", tok, "mcp.tool", toolName,
			rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon,
			policyDigest, approvalRef, approvedPlan)),
	}
	allowDec := ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: toolName, RequiredScope: policy.RequiredScope,
		Allowed: true, Reason: authorizedReason, ApprovalRef: approvalRef,
		MCPTag: "MCP07", TokenBinding: tok.Binding, TraceParent: trace,
		OperationIDKind: idKind, At: rs.clock(),
	}
	rec := rs.auditor.Record(ctx, allowDec, binding)
	if !rec.MayEmit(binding) {
		rs.refuseToolCallEvidence(w, req.ID, rec, opID)
		return
	}
	// Leadership fence IMMEDIATELY before the external call: a node that lost its
	// claim's epoch between anchor and dispatch self-fences (the claim stays
	// claimed; it is never re-dispatched).
	if fence := rs.auditor.BeforeEffect(ctx, rec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}

	ureq := rs.upstreamReq(r, "tools/call", canon.Forward, tok)
	ureq.OperationID = binding.OperationID
	ureq.EffectDigest = binding.EffectDigest
	ureq.FenceToken = rec.FenceToken
	res, ferr := rs.upstream.Forward(ctx, ureq)

	state, relay := classifyDispatch(res, ferr)
	// Review round 1 (S5-01/S5-06): BOTH result classifications — durable
	// task handle and MRTR carrier — run BEFORE the settlement and the
	// settlement NAMES the exact response-release child its caller will claim.
	// Nothing is emitted, inspected or refused here; the refusals below keep
	// their post-settlement placement so the dispatch outcome is recorded first.
	//
	// Design adjudication (§1/§2/§6): the classifications are context-led,
	// not body-led. The task-handle contract exists for this result ONLY when
	// the exact forwarded request DECLARED the Tasks extension in its
	// per-request clientCapabilities (canon.DeclaresTasks — capabilities are
	// per-request and never inferred, schema.ts:63-98) AND the exact strict-tree
	// discriminator is resultType:"task"; a permissive task-marker probe selects
	// nothing. Without the declaration, task-looking members are open-Result
	// extension data (schema.ts:208-216,223-235) and the synchronous core-result
	// contract applies. The MRTR plan then carries the authority profile the
	// selected contract implies; the body supplies only WHAT is mediated.
	var task Task
	var isTask bool
	var terr error
	var plan mrtrReleasePlan
	if relay {
		if canon.DeclaresTasks {
			task, isTask, terr = selectDeclaredTaskHandle(res.Result)
		}
		if terr == nil {
			if isTask {
				plan = rs.planMRTRRelease(res.Result, binding, releaseClassMRTRTaskHandle, mrtrAuthorityTaskHandle)
			} else {
				plan = rs.planMRTRRelease(res.Result, binding, releaseClassMRTRToolResult, mrtrAuthorityCoreResult)
			}
		}
	}
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: rec, State: state,
		ResultDigest: resultDigest(res.Result), DispatchRef: res.DispatchRef,
		ReleaseBinding: plan.release,
	})
	if settlement.FailureClass != sdk.FailureNone {
		// The outcome did NOT durably record: withhold the response. The operation
		// stays claimed/ambiguous; a same-operation retry performs status replay
		// only — never a re-dispatch.
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	if !relay {
		switch state {
		case DispatchUnknown:
			// Post-transmit / inconsistent ambiguity: the request may have landed.
			// Settled unknown; it will never be re-forwarded (at-most-once).
			rs.writeOperationIndeterminate(w, req.ID, opID)
		default:
			// not_sent / blocked / completed-with-upstream-error: a definite upstream
			// failure, durably settled against the claim.
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, "upstream tool dispatch failed")
		}
		return
	}
	if terr != nil {
		// Round-1 F-10: the upstream result LOOKS like a durable task handle
		// to a permissive reader but fails strict validation, so the identity the
		// gateway would bind is not provably the identity it would store or
		// relay. An ungovernable handle is never relayed. (Residual: the upstream
		// task keeps running and cannot even be quarantined — there is no
		// trustworthy id to quarantine it under; the refusal is audited loudly.)
		// ROUND-6 R6-05: the audit persists the stable validation CLASS, never the
		// parser text. The detail of an alias/duplicate-key failure quotes the
		// offending PROPERTY NAME, which is upstream-controlled — a secret encoded as
		// a JSON key would otherwise bypass every response projection straight into
		// the durable audit trail.
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
			"upstream durable task handle failed strict validation (ambiguous — refused); validation class: "+taskDefectClass(terr),
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned a malformed durable task handle")
		return
	}
	if plan.noAuthority && !isTask {
		// Internal bug — the planner ran without a valid authority profile. There
		// is no body-only fallback (design adjudication §5): the raw result
		// is withheld behind the fixed evidence-unavailable shape. Round 2 (F-1):
		// the parent above recorded only the dispatch fact, so the retention of
		// the observed response gets its own durable record — the release child
		// settles `withheld` (derived here: the bug path planned nothing).
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
			"MRTR release planning ran without an authority profile (internal defect, fail-safe); the result was withheld",
			"", "MCP07", trace)
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, toolName, policy.RequiredScope, plan.class, "MCP07", trace),
			deriveResponseReleaseBinding(rs.tenant, binding, plan.class, resultDigest(res.Result)),
			resultDigest(res.Result), func(reason string) {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, reason, "", "MCP07", trace)
			})
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}
	if plan.unreadable && !isTask {
		// The core contract selected an input-required result whose exact
		// `inputRequests` member is present but is not the pin's map
		// (schema.ts:553-555): the governed payload cannot be projected, so the
		// gateway cannot mediate what it cannot read and the result is REFUSED
		// rather than relayed on the plain (unmediated) leg — the retained
		// projection-integrity refusal (adjudication §4). The audit carries the
		// class only.
		//
		// A TASK HANDLE takes the same refusal INSIDE handleToolTaskResult instead,
		// after its registration: refusing here would leave a live upstream task
		// unregistered and unsweepable (the F-03 orphan class) for a defect that is
		// the response's, not the handle's.
		rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false,
			"upstream result selects the governed input-required contract — or a duplicated discriminator "+
				"makes that reading impossible to exclude — and its governed payload cannot be projected: "+
				"unreadable, refused; the mediated payloads were never released", "", "MCP07", trace)
		// Round 2 (F-1): the refusal of an OBSERVED governed result is a release
		// disposition — durable on the child, which the parent settlement named.
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, toolName, policy.RequiredScope, plan.class, "MCP07", trace),
			plan.release, resultDigest(res.Result), func(reason string) {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, reason, "", "MCP07", trace)
			})
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned an ambiguous mediated result")
		return
	}
	if isTask {
		// Every retention decision for this handle — registration, quarantine,
		// collision parking, compensation — happens inside this call, so the
		// reservation is CONSUMED only after it returns: from here on the stored
		// record itself accounts for the slot (round-5 R5-02).
		//
		// ROUND-6 R6-04, stated accurately rather than favorably: between the moment
		// `handleToolTaskResult` STORES the row and the moment this line ends the
		// ticket, that one task is counted TWICE — once as a stored record and once as
		// a held reservation. The bound is therefore CONSERVATIVE, not exact: it can
		// transiently refuse unrelated work and it can report a cap saturated while one
		// slot is double-counted, but it can never admit MORE rows than the bound, and
		// no ticket leak was found (the immediate `defer ticket.release()` covers every
		// error and panic path, and ending twice is a no-op). Making the reported
		// count EXACT would require handling to return an explicit retention outcome
		// and ending the ticket at the precise row handoff; that is not done here, and
		// the reported `retained`/`saturated` must be read as an upper bound.
		rs.handleToolTaskResult(ctx, w, req, tok, toolName, policy, task, res.Result, approvalRef, trace, binding, plan)
		ticket.consume()
		return
	}
	// A synchronous result created no task: the reserved slot goes back immediately.
	ticket.release()
	// (stage 5): the MRTR result mediation is no longer a legacy best-effort
	// leg. A hijacked result releases nothing (the deny wire shape was written; a
	// deny keeps best-effort evidence by doctrine). A result that must be mediated
	// and passes is a MEDIATED RELEASE: the write is anchored as the
	// response-release child THIS tools/call settlement already named, and settled
	// with the honest write classification. An UNMEDIATED result (no mediator, or
	// no MRTR content) keeps the stage-3 shape — a plain relay with no release
	// child (GateOutcome.ReleaseBinding stays zero on plain tools/call).
	// Mediation and release are separate gates now. `mediate` asks whether there
	// is content to inspect; `inputRequired` asks whether governed MRTR bytes are
	// leaving — and those bytes carry a release child whether or not anything was
	// inspectable (r2 review P0-2). An ordinary non-MRTR result keeps the stage-3
	// shape: a plain relay with no release child.
	// Hijack: the disposition of the observed result — withheld — is made
	// durable on the release child the parent settlement named, BEFORE the deny
	// is written (round-3 settle-then-write: the withheld child must be durable
	// before the refusal leaves the process).
	if plan.mediate && rs.mediateMRTRResultEntries(ctx, w, req, tok, plan.entries, trace, func() {
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, toolName, policy.RequiredScope, plan.class, "MCP07", trace),
			plan.release, resultDigest(res.Result), func(reason string) {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, reason, "", "MCP07", trace)
			})
	}) {
		return
	}
	if plan.inputRequired {
		rel, rok := rs.anchorResponseRelease(ctx, w, req.ID,
			rs.releaseDecision(tok, toolName, policy.RequiredScope, plan.class, "MCP07", trace),
			plan.release, opID, func(reason string) {
				rs.auditTraced(ctx, tok, toolName, policy.RequiredScope, false, reason, "", "MCP07", trace)
			})
		if !rok {
			return
		}
		_ = rel.write(ctx, w, "tools/call", req.ID, res.Result)
		return
	}
	_ = rs.writeResult(w, "tools/call", req.ID, res.Result)
}

// classifyDispatch is the HONEST dispatch classification shared by every
// evidence-enforced surface (review round-1 P1): the settled outcome is the
// adapter's state, NEVER laundered. A relay of the result requires BOTH a nil
// error AND a completed classification: a nil error carrying a non-completed
// state is an INCONSISTENT adapter — treated as unknown and withheld, never
// written as a success. An error with no state defaults to unknown
// (post-invoke ambiguity).
func classifyDispatch(res UpstreamResult, ferr error) (state DispatchState, relay bool) {
	state = res.State
	relay = ferr == nil && state == DispatchCompleted
	if !relay {
		if state == "" || (ferr == nil && state != DispatchCompleted) {
			state = DispatchUnknown
		}
	}
	return state, relay
}

// settledStatePermitsCancelRetry is the cancel custodian's reading of one DURABLY
// SETTLED dispatch state: may a further AUTOMATIC cancellation attempt be minted
// for this generation? The contract is the stage-7 B-bis reading rule — blocked
// proves nothing reached the upstream; completed proves something did:
//
//	not_sent — proven pre-transport failure: nothing reached the upstream, a new
//	           attempt re-emits nothing that already ran;
//	blocked  — a decision stopped the effect BEFORE any dispatch (contractually,
//	           since the vocabulary split): the inference "blocked ⇒ never reached
//	           the upstream ⇒ retryable" is now TRUE by contract, not an accident
//	           of one writer's usage;
//	completed — never retryable: the dispatch OCCURRED, whatever governance later
//	           decided about the observed response (the disposition lives in the
//	           release child, not here). A withheld or refused RESPONSE does not
//	           un-produce the upstream effect;
//	withheld — NOT retryable. A PARENT dispatch operation never settles withheld
//	           (round 2: it is the release child's terminal state), so this branch
//	           serves readers of child rows or future journal states — and it says
//	           the same thing completed does: the dispatch ran, an automatic retry
//	           would re-execute a produced effect;
//	unknown  — never retryable (ambiguous: the request may have landed).
func settledStatePermitsCancelRetry(state DispatchState) bool {
	switch state {
	case DispatchNotSent, DispatchBlocked:
		return true
	case DispatchWithheld:
		// The round trip finished; re-attempting is re-execution, not recovery.
		return false
	default:
		return false
	}
}

// refuseToolCallEvidence maps a non-emittable GateRecord onto the design §5 wire
// shapes. Never leaks spool mode, tenant resolution, leader identity or SQL detail —
// the messages are static and the data carries identifiers/state only.
func (rs *ResourceServer) refuseToolCallEvidence(w http.ResponseWriter, id json.RawMessage, rec GateRecord, opID string) {
	switch {
	case rec.FailureClass == sdk.FailureReplay:
		// Same OperationID, different EffectDigest: the single-use claim is bound
		// to another effect. Non-retryable — a new key names a new operation.
		rs.writeEvidenceRebind(w, id, opID)
	case rec.State == GateRecordReplaySettled && rec.Recorded != nil:
		rs.writeOperationRecorded(w, id, opID, string(rec.Recorded.State), rec.Recorded.ResultDigest)
	case rec.State == GateRecordReplayPending:
		// Claimed but never settled (a crash between claim and settlement, or a
		// concurrent duplicate): status replay of the non-terminal state.
		rs.writeOperationRecorded(w, id, opID, "claimed", "")
	default:
		rs.writeEvidenceUnavailable(w, id, opID)
	}
}

// handleToolTaskResult governs a durable task handle an enforced tools/call
// returned. (stage 4): the REGISTRATION itself is a governed effect — the
// server-initiated mcp.task.track child operation, derived from the PARENT
// tools/call binding, is claim-anchored AND leadership-fenced (round-1 F-09)
// BEFORE the in-memory ledger insert and BEFORE the handle is relayed. A track
// refusal withholds the handle, retains the live upstream task as a QUARANTINE
// record (round-1 F-03 — never a governed registration, never forgotten) and
// issues the compensating cancel (itself evidence-gated). The admission DENIALS
// keep their best-effort audits and their own status codes (round-1 F-12).
func (rs *ResourceServer) handleToolTaskResult(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, tool string, policy ToolPolicy, task Task, rawResult json.RawMessage, approvalRef, trace string, parentBinding sdk.EvidenceBinding, plan mrtrReleasePlan) {
	// The IMMUTABLE normalized record (F-10: strictTaskFromResult already froze
	// the upstream handle; nothing below transforms it). The canonical OWNER
	// tuple is captured here (F-06) and is what every later task method compares.
	rec := TaskRecord{
		TaskID: task.TaskID, Tool: tool, Subject: tok.Subject, IsDelegated: tok.IsDelegated,
		ActAs: tok.ActAs, Issuer: tok.Issuer, ClientID: tok.ClientID, Tenant: rs.tenant,
		RequiredScope: policy.RequiredScope, Destructive: policy.Destructive,
		CreatedAt: rs.clock(), TTLMs: task.TTLMs, PollIntervalMs: task.PollIntervalMs, Status: task.Status,
		StatusReason: task.StatusMessage, InputRequests: durableTaskInputRefs(task.InputRequests),
	}
	if rec.Status == "" {
		rec.Status = taskStatusWorking
	}

	if rs.taskGate != nil {
		dec, err := rs.taskGate.AuthorizeTask(ctx, TaskIntent{
			Tenant: rs.tenant, Subject: tok.Subject, Tool: tool,
			TaskID: task.TaskID, TTLMs: task.TTLMs,
		})
		if err != nil {
			rs.denyCreatedTask(ctx, w, req, tok, rec,
				http.StatusForbidden, "task governance unavailable", "task gate error (fail-closed): "+err.Error(), trace,
				parentBinding, taskCancelClassAdmissionDenied)
			return
		}
		if !dec.Allow {
			status := taskGateHTTPStatus(dec.DeniedStatus)
			reason := strings.TrimSpace(dec.Reason)
			if reason == "" {
				reason = "task gate denied"
			}
			rs.denyCreatedTask(ctx, w, req, tok, rec,
				status, "durable task denied by gateway", "task gate deny: "+reason, trace,
				parentBinding, taskCancelClassAdmissionDenied)
			return
		}
	}

	// Claim + anchor the registration BEFORE the in-memory insert and BEFORE the
	// handle can reach the client (the effect never precedes the anchor).
	trackBinding := deriveTaskTrackBinding(rs.tenant, rs.upstreamDescriptor, parentBinding, rec)
	trackRec := rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: tool, RequiredScope: policy.RequiredScope,
		Allowed: true, Reason: "tools/call returned durable task handle; registered",
		ApprovalRef: approvalRef, TaskID: task.TaskID, MCPTag: "MCP07",
		TokenBinding: tok.Binding, TraceParent: trace,
		EffectAction: taskActionTrack, At: rs.clock(),
	}, trackBinding)
	if !trackRec.MayEmit(trackBinding) {
		rs.withholdUngovernedTask(ctx, tok, rec, parentBinding, taskCancelClassTrackRefused,
			"task registration evidence refused", trace)
		rs.refuseToolCallEvidence(w, req.ID, trackRec, string(trackBinding.OperationID))
		return
	}
	// Round-1 F-09: the leadership fence runs IMMEDIATELY before the
	// registration MUTATION, exactly like every other governed effect. Without
	// it a node that lost its epoch between claim and insert still registered the
	// task, and the stale registration could later drive task methods or a sweep.
	if fence := rs.auditor.BeforeEffect(ctx, trackRec); fence.MustRefuse(trackBinding) {
		rs.withholdUngovernedTask(ctx, tok, rec, parentBinding, taskCancelClassTrackRefused,
			"task registration fence refused (leadership lost after the claim)", trace)
		rs.writeEvidenceUnavailable(w, req.ID, string(trackBinding.OperationID))
		return
	}
	// The registration is now running under a FRESH, FENCED claim: only here does
	// the record acquire an ANCHORED origin. Every later server-initiated
	// compensation of this task chains from it (F-04) — a refused claim must
	// never leave behind a record that names an unanchored parent.
	rec.Origin = trackBinding
	// Round-2 N-06: the record is inserted NON-OPERABLE. Client task methods
	// refuse a pending registration, so a caller that predicted the task id
	// cannot slip a tasks/update through the window between the insert and the
	// settlement that either confirms or quarantines it.
	rec.Pending = true
	// K5: Register is the source-of-truth mutation. The in-process ledger insert
	// happens only after the adapter returns the durable generation/binding and
	// remains a cache of that exact identity.
	rec, err := rs.registerDurableTask(ctx, rec)
	if err != nil {
		rs.auditor.Settle(ctx, GateOutcome{Record: trackRec, State: DispatchBlocked})
		if errors.Is(err, ErrDurableTaskConflict) {
			alert := "durable task authority reports an ALREADY TRACKED task id; quarantined as an ambiguous collision " +
				"(no compensating cancel: it would target the existing task)"
			if perr := rs.taskLedger.parkCollision(rec); perr != nil {
				alert += "; collision parking failed: " + perr.Error()
			}
			rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false, alert, "", "MCP07", trace)
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream returned an ambiguous durable task identifier")
			return
		}
		rs.denyCreatedTask(ctx, w, req, tok, rec,
			http.StatusServiceUnavailable, "durable task persistence unavailable",
			"durable task registration failed (fail-closed)", trace,
			parentBinding, taskCancelClassTrackRefused)
		return
	}
	stored, err := rs.taskLedger.insertDurable(rec)
	switch {
	case err == nil:
		// registered under a fresh, fenced claim.
		rec = stored
	case errors.Is(err, errTaskDuplicateID):
		// Round-1 F-05: a COLLIDING upstream task id is not a capacity
		// failure. Compensating it would send tasks/cancel for an id that already
		// names ANOTHER governed task — a wrong-target actuation against a task
		// this caller may not even own. Quarantine, alert, and never cancel.
		rs.auditor.Settle(ctx, GateOutcome{Record: trackRec, State: DispatchBlocked})
		alert := "upstream returned an ALREADY TRACKED task id; quarantined as an ambiguous collision (no compensating cancel: it would target the existing task)"
		if perr := rs.taskLedger.parkCollision(rec); perr != nil {
			alert += "; collision parking failed: " + perr.Error()
		}
		rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false, alert, "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned an ambiguous durable task identifier")
		return
	default:
		// Cap refusal AFTER the claim: close the track operation as blocked (a
		// policy decision stopped the registration; best-effort — a refused
		// settlement leaves the deterministic track claim inert-but-safe) and
		// compensate upstream through the evidence-gated cancel.
		rs.auditor.Settle(ctx, GateOutcome{Record: trackRec, State: DispatchBlocked})
		rs.denyCreatedTask(ctx, w, req, tok, rec,
			http.StatusTooManyRequests, "durable task limit reached", "task ledger cap deny: "+err.Error(), trace,
			parentBinding, taskCancelClassLedgerCap)
		return
	}
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: trackRec, State: DispatchCompleted, ResultDigest: resultDigest(rawResult),
	})
	if settlement.FailureClass != sdk.FailureNone {
		// The registration outcome did not durably record: withhold the handle.
		// The record STAYS — and is marked for reconciliation, because the
		// gateway cannot prove the governed registration is durable (F-03: an
		// unconfirmed registration is retained as a quarantine, never forgotten
		// and never presented as a successful mcp.task.track). The mutation is a
		// compare-and-swap on the record's generation (N-03).
		rs.taskLedger.markQuarantine(rec.TaskID, rec.Generation, "track settlement did not record durably")
		rs.writeOperationIndeterminate(w, req.ID, string(trackBinding.OperationID))
		return
	}
	// The governed registration is durable: only NOW does the record become
	// operable for client task methods (N-06). ROUND-3 R3-04: the finalization is
	// an ATOMIC state transition owned by this settlement alone, and the handle is
	// relayed only when the record the ledger now holds is EXACTLY the generation
	// this call inserted and is operable. Round-2 ignored the compare-and-swap
	// result and relayed unconditionally, so a registration that expired, was
	// replaced, was quarantined by a failing sweep, or was canceled by a
	// concurrent kill-switch while it was still pending still handed the client a
	// task handle it could drive.
	//
	// ROUND-9 R9-01: the finalization and the RELAY PIN are ONE transition
	// (settleRegistrationAndPin). Round-8 finalized, released the ledger mutex, and
	// acquired the lease afterwards — so a TTL boundary or a concurrent sweep in that
	// window could evict or refuse the very generation whose handle was about to be
	// written, and the write happened anyway. There is no window left to lose: either
	// this call returns the operable record AND its pin, or nothing is written.
	finalized, fok := rs.taskLedger.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead)
	if !fok || finalized.Generation != rec.Generation || !finalized.operable() {
		rs.taskLedger.markQuarantine(rec.TaskID, rec.Generation,
			"registration could not be finalized as the exact registered generation")
		rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
			"durable task handle WITHHELD: the governed registration could not be finalized as the exact registered generation "+
				"(the record was replaced, quarantined or canceled during the track settlement); "+
				quarantineNote(task.TaskID, "registration finalization refused"), "", "MCP07", trace)
		rs.writeOperationIndeterminate(w, req.ID, string(trackBinding.OperationID))
		return
	}
	// ROUND-8 R8-01: the HANDLE RELAY is the moment the owner MAY come to hold the
	// unguessable task id, and it is therefore the moment the record acquires an
	// obligation to serve that owner a result. (Round-9 R9-04 / round-10 N10-02: this
	// paragraph said "the moment the owner LEARNS" the id. Local writer acceptance is
	// not remote receipt — see `writeResult` and `TaskRecord.HandleRelayed` — and the
	// governance consequence is deliberately built on the weaker, conservative fact.)
	// Round-7 discarded this write's error at exactly this line, so BOTH outcomes were
	// wrong:
	//
	//   - the successful relay recorded no provenance, and the retirement rule then
	//     inferred delivery from mutable CURRENT state (`!operable()`), which a later
	//     quarantine inverted — deleting a delivered owner's result unread;
	//   - the FAILED relay left an operable record that owed a collection nobody could
	//     perform. With `ttlMs:null` that row answered `retire` 409 for ever and
	//     permanently consumed capacity.
	//
	// ROUND-11 N11-01: the second bullet used to add "its owner had no way to perform,
	// the local writer having reported a definite failure". A write error establishes
	// no such thing — it says nothing about how many bytes the writer ACCEPTED — so the
	// failure branch below no longer infers "never delivered" from the error alone; it
	// infers it only from a PROVEN ZERO-BYTE write.
	//
	// The lease is held ACROSS the write for the same reason every other governed
	// effect holds one: between the write and the provenance install, nothing may
	// retire, release or TTL-evict the row the client is being handed. It is released
	// by DEFER, not at the end of the happy path — a panic in the writer, the ledger
	// or the auditor would otherwise leak a pin that no later retirement or eviction
	// could ever clear (the round-4 R4-06 class). The pin was taken ATOMICALLY with
	// the finalization above (R9-01), so reaching this line means it is held.
	defer rs.taskLedger.releaseEffectLease(rec.Generation)
	// ROUND-9 R9-01: the CUSTODIAN is installed BEFORE the writer, and it is deferred
	// AFTER the pin release above so it runs FIRST — the relay is closed out while its
	// generation is still pinned. Round-8 had only the pin releaser here: a panic in
	// `ResponseWriter.Write` released the pin and executed NEITHER the success nor the
	// error transition, leaving an operable row with `HandleRelayed:false` although
	// remote receipt was ambiguous — i.e. a row an operator could be told was
	// certainly never delivered and could therefore destroy.
	relay := &taskHandleRelayGuard{
		ledger: rs.taskLedger, taskID: rec.TaskID, generation: rec.Generation,
		alert: func(reason string) {
			rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false, reason, "", "MCP07", trace)
		},
	}
	defer relay.abandon()
	// Review round 1 (S5-01): a durable task handle can ITSELF be an MRTR
	// carrier — `status:"input_required"` with `inputRequests` payloads, a shape
	// the strict parser explicitly accepts (tasks.go:171-273). Those payloads are
	// handed to the caller WITH the handle, so stage 5's placement of the MRTR
	// mediation on the synchronous leg only meant an accepted task handle relayed
	// them with no mediation and no response-release evidence at all.
	//
	// They are mediated HERE, before a byte of the handle is written, and an
	// approved handle is released only against an anchored response-release child
	// — the same law every other mediated surface obeys. A DENY writes no handle:
	// the registration stands (the upstream task stays governed, sweepable and
	// cancellable — never the F-03 orphan), and the record is closed out in the
	// NEVER-DELIVERED class, which is the operator-drainable one, because zero
	// bytes of the identifier reached the transport.
	// Round 3 (Codex r2, F-1 residual): the three retention decisions below are
	// post-observation refusals of an OBSERVED governed result — exactly like
	// their synchronous siblings — so each records its disposition on the
	// RELEASE CHILD (settled `withheld`) BEFORE any refusal byte is written,
	// alongside the never-delivered custody the relay guard installs.
	handleWithheld := func(release sdk.EvidenceBinding) {
		relDec := rs.withheldReleaseDecision(tok, tool, policy.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rs.settleWithheldRelease(ctx, relDec, release, resultDigest(rawResult), func(reason string) {
			rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false, reason, "", "MCP07", trace)
		})
	}
	if plan.noAuthority {
		// Internal bug — the planner ran without a valid authority profile (design adjudication §5). The strict task parser already established a
		// trustworthy ID, so the registration/quarantine discipline completed
		// first (above); only the RELAY is withheld, in the never-delivered
		// class, behind the fixed evidence-unavailable shape.
		relay.undelivered(taskQuarantineHandleAmbiguousMRTR)
		rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
			"MRTR release planning for the task handle ran without an authority profile (internal defect, "+
				"fail-safe): the handle was NOT relayed and the registered record is retained for operator "+
				"reconciliation as never-delivered", "", "MCP07", trace)
		handleWithheld(deriveResponseReleaseBinding(rs.tenant, parentBinding, plan.class, resultDigest(rawResult)))
		rs.writeEvidenceUnavailable(w, req.ID, string(parentBinding.OperationID))
		return
	}
	if observed, reason, wire, refused := plan.coreRefusal(); refused {
		// STAGE-7 P0-3 / M-1R2, and UNREACHABLE-BY-CONSTRUCTION today: this leg
		// runs only when selectDeclaredTaskHandle selected the handle contract,
		// which requires EXACTLY ONE exact `resultType` whose value is "task"
		// (tasks.go selectsTaskVariant) — so the core input_required discriminator
		// and the duplicated-discriminator ambiguity are both excluded before the
		// plan is computed. It is written anyway because the alternative to a
		// fail-safe branch on a MUST NOT is a silent release: if the handle
		// contract's selection ever loosens, this profile must refuse, not relay.
		relay.undelivered(taskQuarantineHandleUnsanctionedMRTR)
		rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
			"the durable task handle carries "+observed+": the handle was NOT relayed and the registered "+
				"record is retained for operator reconciliation as never-delivered; reason class: "+reason,
			"", "MCP07", trace)
		handleWithheld(plan.release)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, wire)
		return
	}
	if plan.unreadable {
		// The task contract selected this handle (declared capability + exact
		// resultType:"task" design adjudication §2) and its validated
		// status makes exact `inputRequests` governed — but that member is
		// present and is not the pin's map (schema.ts:553-555), so the governed
		// payload cannot be projected. The REGISTRATION stands — the upstream
		// task is governed, sweepable and cancellable — and only the RELAY is
		// refused, in the never-delivered class. Reachable because
		// selectDeclaredTaskHandle validates the handle's identity but not the
		// VALUE of its `inputRequests` member.
		relay.undelivered(taskQuarantineHandleAmbiguousMRTR)
		rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
			"the durable task handle declares input_required but its inputRequests member is present and is "+
				"not the schema's map — unreadable, refused: the handle was NOT relayed "+
				"and the registered record is retained for operator reconciliation as never-delivered", "", "MCP07", trace)
		handleWithheld(plan.release)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned an ambiguous mediated result")
		return
	}
	if plan.mediate {
		if rs.mediateMRTRResultEntries(ctx, w, req, tok, plan.entries, trace, func() { handleWithheld(plan.release) }) {
			relay.undelivered(taskQuarantineHandleMediationDenied)
			rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
				"the durable task handle carried MRTR input requests the content mediator DENIED: the handle was "+
					"NOT relayed, no delivery was recorded, and the registered record is retained for operator "+
					"reconciliation as never-delivered", "", "MCP07", trace)
			return
		}
	}
	// ROUND-11 N11-01: the ACCEPTED BYTE COUNT of this one write is PRESERVED, because
	// the error by itself cannot classify the outcome. `json.Encoder.Encode` marshals
	// the COMPLETE JSON value, appends '\n', makes ONE `Write(b)` call and DISCARDS the
	// count it reports (encoding/json/stream.go), while `io.Writer` explicitly permits
	// `0 <= n <= len(p)` and requires a non-nil error whenever `n < len(p)`. A
	// CONFORMING `http.ResponseWriter` may therefore accept every byte except the
	// encoder's trailing newline — which JSON-RPC does not require — and still report
	// failure; the owner then holds a complete, parseable response carrying the usable
	// task id. Counting HERE, at a site whose governance depends on it, leaves the
	// other `writeResult` call sites untouched — since that is the seven that
	// deliberately discard the error (round-10 residual 6, re-counted), the owner's
	// terminal `tasks/get` on its unmediated leg, and the response-release engine
	// (which counts with this same wrapper for the same reason). Since review
	// round 1 this relay has TWO legs and the count is taken on BOTH: the mediated
	// leg is written by that same engine, and the four custody outcomes below read
	// the identical (error, proven-zero) pair whichever leg produced it —
	// and the wrapper is handed to nothing but `writeResult`, which uses only `Header`,
	// `WriteHeader` and `Write`; no `http.Flusher`/`Hijacker`/`Pusher` behavior is
	// asserted on this writer anywhere in the connector, so wrapping drops nothing.
	var werr error
	var provenZero bool
	if plan.mediate {
		// The MEDIATED handle relay: claimed and leadership-fenced as the release
		// child this tools/call settlement already named, then written and settled
		// with the same three-way byte accounting as the plain leg below (the
		// engine counts with this very wrapper — evidencerelease.go).
		relDec := rs.releaseDecision(tok, tool, policy.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rel, rok := rs.anchorResponseRelease(ctx, w, req.ID, relDec,
			plan.release, string(parentBinding.OperationID), func(reason string) {
				rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false, reason, "", "MCP07", trace)
			})
		if !rok {
			// Withheld: the release evidence could not anchor, so not one byte of the
			// handle was written. Same never-delivered class as the mediation deny.
			relay.undelivered(taskQuarantineHandleReleaseWithheld)
			rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
				"the mediated durable task handle was WITHHELD (response-release evidence refused): the handle was "+
					"NOT relayed, no delivery was recorded, and the registered record is retained for operator "+
					"reconciliation as never-delivered", "", "MCP07", trace)
			return
		}
		out := rel.write(ctx, w, "tools/call", req.ID, rawResult)
		werr, provenZero = out.err, out.provenZero
	} else {
		counted := &relayWriteCounter{ResponseWriter: w}
		werr = rs.writeResult(counted, "tools/call", req.ID, rawResult)
		provenZero = counted.provenZeroBytes()
	}
	if werr == nil {
		// The provenance compare-and-swap RESULT is enforced (R9-01). Under the pin it
		// cannot fail for eviction or retirement; if it fails anyway the row this relay
		// was authorized against is gone or was replaced, and the client is holding a
		// handle with no governance row behind it. That is a reconciliation fact an
		// operator must see, never a discarded boolean.
		if !relay.delivered() {
			rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
				"the durable task handle was RELAYED but its handle-relay provenance could not be installed: the "+
					"record no longer holds this generation; the client may hold a handle this gateway has no "+
					"governance row for — reconcile the upstream task", "", "MCP07", trace)
		}
		return
	}
	// The write FAILED. Which of the two failure classes this is depends on the byte
	// count, never on the error alone (ROUND-11 N11-01 — round 10 collapsed both into
	// "never delivered" on the theory that a failed write leaves at most a truncated,
	// unparsable prefix; a prefix can be the WHOLE JSON value with only the encoder's
	// newline missing).
	if !provenZero {
		// POSSIBLE RELAY. A positive — or unreportable — number of body bytes was
		// accepted, so a conforming owner MAY hold a usable identifier for this task.
		// This takes exactly the rule the abnormal-unwind close-out already uses:
		// `HandleRelayed:true` plus quarantine. Classifying it never-delivered would let
		// `ownerCollectionSatisfied` return true on `!HandleRelayed` alone and authorize
		// an operator to DELETE a result the owner is entitled to read — the R8-01
		// failure re-entered through the partial-write path.
		//
		// The declared cost, stated rather than argued away: such a row is TTL-immune
		// (`taskExpired` exempts every quarantined record) and consumes owner/gateway
		// capacity until an operator drains it or the process ends — round-10 residual
		// 11, which now covers this class too. Availability pressure cannot turn an
		// ambiguous delivery into deletion proof; the audited "abandon result" action
		// for genuinely uncollectable rows belongs to.
		relay.possiblyRelayed(taskQuarantineHandlePartial)
		rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
			"the durable task handle response FAILED to write, but the local writer had already ACCEPTED response "+
				"bytes: delivery is AMBIGUOUS and a conforming owner may hold a usable identifier for this task, so "+
				"the record is retained as possibly-relayed and quarantined for operator reconciliation (it is NOT "+
				"retirable without a proven collection, and it does not expire)", "", "MCP07", trace)
		return
	}
	// NEVER-DELIVERED, and only where that is PROVEN: the writer reported a failure AND
	// accepted ZERO bytes of the body, so nothing that could carry the identifier
	// reached the transport and no owner can address the task. The record is marked for
	// reconciliation, the operator-drainable state for a live upstream task nobody can
	// address: it stops being client-operable, it stops being TTL-forgettable
	// (forgetting a live external task is the F-03 failure), and it retires on the
	// operator's proof alone, `HandleRelayed` being false.
	relay.undelivered(taskQuarantineHandleUndelivered)
	rs.auditTaskTraced(ctx, tok, tool, policy.RequiredScope, task.TaskID, false,
		"the durable task handle could NOT be written to its owner: the local writer reported a failure after "+
			"accepting ZERO bytes of the response, so no identifier for this task reached the transport and no "+
			"owner can hold one; the record is retained for operator reconciliation as never-delivered",
		"", "MCP07", trace)
}

// relayWriteCounter preserves the ACCEPTED BYTE COUNT of the ONE response write whose
// outcome governs a task-handle relay (round-11 N11-01).
//
// `json.Encoder.Encode` discards the count its single `Write` reports, and `io.Writer`
// permits a conforming writer to accept `len(p)-1` bytes and still return an error. So
// "the write failed" and "nothing reached the owner" are DIFFERENT facts, and only the
// second may be classified never-delivered. The wrapper exists so the relay site can
// tell them apart without changing `writeResult`'s error contract or touching the ten
// call sites that deliberately discard it.
type relayWriteCounter struct {
	http.ResponseWriter
	accepted int64
	unknown  bool
}

func (c *relayWriteCounter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	switch {
	case n > 0:
		c.accepted += int64(n)
	case n < 0:
		// Outside the `io.Writer` contract: the writer reported no usable count, so
		// "zero bytes were accepted" is NOT established. Deny-closed → possible relay.
		c.unknown = true
	}
	return n, err
}

// provenZeroBytes reports the ONLY fact that may keep a failed relay classified as
// never-delivered: every write of this response was reported conformingly AND accepted
// nothing. A positive count, or any count outside the contract, is possible relay.
func (c *relayWriteCounter) provenZeroBytes() bool {
	return c != nil && !c.unknown && c.accepted == 0
}

// taskHandleRelayGuard is the panic-safe CUSTODIAN of ONE handle relay (round-9
// R9-01), built on the same discipline as `cancelAttemptGuard` (round-4 R4-06): it
// is installed immediately after the pinned finalization, before anything that can
// panic, it never recover()s, and it closes the relay out EXACTLY ONCE.
//
// The four outcomes are not interchangeable. The third is the one round 8 missed and
// the second is the one round 11 split off it:
//
//   - `delivered` — the local writer accepted the whole body: install the immutable
//     handle-relay provenance and report whether the compare-and-swap held;
//   - `undelivered` — a write error after a PROVEN ZERO-BYTE write: NEVER-DELIVERED,
//     quarantined for reconciliation, `HandleRelayed` stays false and the row retires
//     on the operator's proof alone;
//   - `possiblyRelayed` — a write error after a POSITIVE or unreportable accepted
//     count (round-11 N11-01): delivery is AMBIGUOUS, so possible relay is assumed
//     (`HandleRelayed:true`) and the row is quarantined, exactly like an abnormal
//     unwind;
//   - `abandon` — the deferred close-out of an ABNORMAL unwind: delivery is
//     AMBIGUOUS, so possible relay is assumed (`HandleRelayed:true`) and the row is
//     quarantined. Assuming "never delivered" in either ambiguous case would
//     authorize an operator to delete a result the owner may be entitled to read;
//     assuming "delivered and fine" would leave an unprovable registration operable.
//     The panic keeps propagating untouched.
type taskHandleRelayGuard struct {
	ledger     *taskLedger
	taskID     string
	generation string
	alert      func(reason string)
	closed     bool
}

// delivered installs the handle-relay provenance after a successful local write and
// reports whether the compare-and-swap on the generation held.
func (g *taskHandleRelayGuard) delivered() bool {
	if g == nil || g.closed {
		return false
	}
	g.closed = true
	return g.ledger.recordHandleRelay(g.taskID, g.generation)
}

// undelivered closes out a PROVEN ZERO-BYTE write failure as never-delivered.
func (g *taskHandleRelayGuard) undelivered(reason string) {
	if g == nil || g.closed {
		return
	}
	g.closed = true
	g.ledger.markQuarantine(g.taskID, g.generation, reason)
}

// possiblyRelayed closes out a write failure that followed a POSITIVE — or
// unreportable — number of accepted body bytes (round-11 N11-01). It is the same
// conservative transition the abnormal unwind uses: the owner MAY hold the
// identifier, so possible relay is assumed and the row is quarantined.
func (g *taskHandleRelayGuard) possiblyRelayed(reason string) {
	if g == nil || g.closed {
		return
	}
	g.closed = true
	g.ledger.custodyAmbiguousHandleRelay(g.taskID, g.generation, reason)
}

// abandon is the DEFERRED custody: a no-op after either classified outcome, and the
// conservative ambiguous close-out of a relay that unwound abnormally.
func (g *taskHandleRelayGuard) abandon() {
	if g == nil || g.closed {
		return
	}
	g.closed = true
	g.ledger.custodyAmbiguousHandleRelay(g.taskID, g.generation, taskQuarantineHandleAmbiguous)
	if g.alert != nil {
		g.alert("the durable task handle relay unwound ABNORMALLY; delivery is AMBIGUOUS, so the record is " +
			"retained as possibly-relayed and quarantined for operator reconciliation (it is NOT retirable " +
			"without a proven collection, and it does not expire)")
	}
}

// taskQuarantineHandleAmbiguous is the reconciliation reason of a handle relay that
// unwound abnormally (round-9 R9-01): unlike the never-delivered class, the owner MAY
// hold the identifier, so the record additionally carries handle-relay provenance and
// owes a collection.
const taskQuarantineHandleAmbiguous = "the task handle relay unwound abnormally; delivery is ambiguous (assumed possibly relayed)"

// taskQuarantineHandleUndelivered is the reconciliation reason of a registration
// whose governed handle could not be RELAYED to its owner (round-8 R8-01). It is a
// distinct class from every other quarantine: the registration itself succeeded and
// is durable — what failed is the response, so the upstream task is live and
// unaddressable rather than ungoverned.
const taskQuarantineHandleUndelivered = "the task handle was never delivered to its owner (response write failed)"

// taskQuarantineHandlePartial is the reconciliation reason of an ordinary handle-relay
// write FAILURE that the writer nevertheless reported accepting body bytes for — or
// reported no usable count for at all (round-11 N11-01). Like the abnormal-unwind
// class and unlike the never-delivered one, the owner MAY hold the identifier, so the
// record additionally carries handle-relay provenance and owes a collection.
const taskQuarantineHandlePartial = "the task handle response write failed after the writer had accepted body bytes; delivery is ambiguous (assumed possibly relayed)"

// taskQuarantineHandleMediationDenied and taskQuarantineHandleReleaseWithheld are
// the two-1 (S5-01) never-delivered classes of a registered handle
// that was never written: the content mediator denied the MRTR payloads it
// carried, or its response-release evidence could not be anchored. Both are
// stated for what they are — the registration succeeded and is durable, and ZERO
// bytes of the identifier reached the transport — rather than reusing the
// write-failure wording, which would be a false label.
const taskQuarantineHandleMediationDenied = "the task handle carried MRTR input requests the content mediator denied; the handle was never relayed"

const taskQuarantineHandleReleaseWithheld = "the task handle response release could not be anchored; the handle was never relayed"

// taskQuarantineHandleAmbiguousMRTR is the third never-delivered class of the
// same family: the registered handle's governed MRTR payload could not be
// released — its exact `inputRequests` member is present but is not the pin's
// map (unreadable design adjudication §4), or the release planner ran
// without an authority profile (the §5 internal-defect fail-safe) — so the
// relay is refused while the registration stands.
const taskQuarantineHandleAmbiguousMRTR = "the task handle's MRTR payload could not be released (inputRequests is present and is not the schema's map, or release planning lacked its authority profile); the handle was never relayed"

// taskQuarantineHandleUnsanctionedMRTR is the stage-7 P0-3 member of the same
// never-delivered family: the handle carried the CORE input_required
// discriminator, which the Tasks extension does not sanction, so the registration
// stands and the relay is refused.
const taskQuarantineHandleUnsanctionedMRTR = "the task handle carried the core input_required discriminator, which the Tasks extension does not sanction; the handle was never relayed"

// withholdUngovernedTask is the F-03 orphan discipline for a CREATED upstream
// task the gateway will not register: the task is FIRST retained as a quarantine
// record — visible to reconciliation and to every future kill-switch sweep —
// and only THEN is the evidence-gated compensating cancel attempted. A proven
// cancellation releases the record; anything else leaves it visible.
//
// The first implementation kept no record at all when the compensation refused,
// so an evidence outage turned a live external task into a permanently
// invisible orphan: the deliberate residual is that an outage DELAYS the
// cancellation, never that it erases the task.
func (rs *ResourceServer) withholdUngovernedTask(ctx context.Context, tok validatedToken, rec TaskRecord, parent sdk.EvidenceBinding, reasonClass, why, trace string) ungovernedTaskOutcome {
	// ROUND-2 N-01: ONE atomic collision/generation check happens HERE, before
	// any compensation can run. The round-1 order let the task-gate denial, the
	// track-claim refusal and the track-fence refusal reach compensation without
	// ever asking whether the identifier already named ANOTHER governed task —
	// only the late `insert` collision check did, and those branches never reach
	// it. A compensating tasks/cancel for a colliding identifier cancels the
	// EXISTING task (wrong-target actuation, possibly cross-issuer) and its
	// success then DELETED that task's governance record.
	q := rs.taskLedger.quarantine(rec, parent, why)
	if q.err != nil {
		rs.auditTaskTraced(ctx, tok, rec.Tool, rec.RequiredScope, rec.TaskID, false,
			"ungoverned task could NOT be retained for reconciliation: "+q.err.Error(), "", "MCP07", trace)
	}
	if !q.retained() {
		// Ambiguous identifier: never compensate, never mutate the existing
		// record, always alert.
		rs.auditTaskTraced(ctx, tok, rec.Tool, rec.RequiredScope, rec.TaskID, false,
			"ungoverned task identifier is AMBIGUOUS ("+string(q.kind)+"): it already names another governed task; "+
				"parked for reconciliation and NOT canceled (canceling it would target the existing task)",
			"", "MCP07", trace)
		return ungovernedTaskOutcome{collision: true, kind: q.kind}
	}
	rec = q.record
	// The `cancel_requested` transition of a successful compensation is applied
	// ATOMICALLY inside compensateCreatedTask's settlement (round-4 R4-01) — never
	// here, after the generation pin has already been released.
	out := rs.compensateCreatedTask(ctx, tok, rec, parent, reasonClass, trace)
	rs.auditTaskTraced(ctx, tok, rec.Tool, rec.RequiredScope, rec.TaskID, false,
		"task result withheld: "+why+"; compensating tasks/cancel "+out.note()+
			"; "+quarantineNote(rec.TaskID, why), "", "MCP07", trace)
	return ungovernedTaskOutcome{record: rec, cancel: out}
}

// ungovernedTaskOutcome is the typed result of retaining + compensating one
// created-but-ungoverned task (round-2 N-01).
type ungovernedTaskOutcome struct {
	// collision reports that the identifier already named a DIFFERENT governed
	// record: NOTHING was compensated and the existing record was not touched.
	collision bool
	kind      taskQuarantineKind
	record    TaskRecord
	cancel    taskCancelOutcome
}

// note renders the stable audit phrase for one ungoverned-task outcome.
func (o ungovernedTaskOutcome) note() string {
	if o.collision {
		return "NOT issued: ambiguous task identifier (" + string(o.kind) + ") — canceling it would target the existing task"
	}
	return o.cancel.note()
}

// compensateCreatedTask issues the evidence-gated compensating tasks/cancel of
// one created-but-ungoverned task, chained from the supplied ANCHORED parent and
// bound to the record's immutable GENERATION (round-2 N-03).
func (rs *ResourceServer) compensateCreatedTask(ctx context.Context, tok validatedToken, rec TaskRecord, parent sdk.EvidenceBinding, reasonClass, trace string) taskCancelOutcome {
	comp := deriveTaskCancelCompensationBinding(rs.tenant, parent, rec.TaskID, rec.Generation, reasonClass)
	return rs.anchorAndCancelTask(ctx,
		rs.taskCancelDecision(tok, rec.Tool, rec.RequiredScope, rec.TaskID,
			"compensating tasks/cancel ("+reasonClass+")", trace),
		fixedTaskCancelBinding(comp), rec.TaskID, rec.Generation, tok.Subject, trace,
		func(out taskCancelOutcome) taskCancelBookkeeping {
			if !out.succeeded {
				// The record is ALREADY quarantined (quarantine-first discipline), so a
				// failed compensation needs no further record mutation — only the
				// cancellation-unconfirmed flag the settlement installs from
				// `dispatched`, which keeps it TTL-immune.
				return taskCancelBookkeeping{}
			}
			// ROUND-2 N-02: an acknowledgement is NOT a terminal cancellation. The
			// tasks extension documents cooperative cancellation explicitly (see
			// Client.TaskCancel): the upstream may keep reporting the task as working.
			// Deleting the record here lost a live external task from every later
			// sweep. It is retained in the non-terminal `cancel_requested` state and,
			// being ungoverned, becomes a pure reconciliation artifact until an
			// upstream report or explicit reconciliation proves the terminal status.
			return taskCancelBookkeeping{
				status:                 taskCancelRequestedStatus,
				statusReason:           "compensating tasks/cancel acknowledged; terminal status unconfirmed",
				reconcileIfQuarantined: true,
			}
		})
}

func taskGateHTTPStatus(status int) int {
	switch status {
	case http.StatusPaymentRequired, http.StatusTooManyRequests:
		return status
	default:
		return http.StatusForbidden
	}
}

// denyCreatedTask refuses a durable task handle the upstream already created
// (admission denial, ledger cap). Two invariants govern this path:
//
//   - the upstream task must not keep running ungoverned, so a COMPENSATING
//     tasks/cancel is issued — itself an evidence-mandatory effect: its
//     claim is anchored BEFORE the upstream cancel, and an anchor refusal means
//     the cancel is NOT called (exploit matrix: "cancel/compensation anchor
//     failure ⇒ cancel upstream not called"). The task is quarantined first, so
//     a refused/unsettled compensation leaves it VISIBLE (F-03);
//   - the POLICY DENIAL STANDS regardless of what the compensation's evidence
//     did (round-1 F-12). The frozen doctrine is explicit — evidence is
//     mandatory for the compensating ALLOW, never for the deny — so the client
//     keeps receiving the original 402/403/429 shape instead of a 503 that
//     varies with ledger availability. The compensation fault is recorded and
//     alerted in the audit trail; it never rewrites the verdict.
//
// The task DENIAL evidence itself stays best-effort (doctrine: evidence is
// mandatory on allow, not deny); the historical post-hoc Allowed:true audit of
// the cancel is now its settlement.
func (rs *ResourceServer) denyCreatedTask(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, rec TaskRecord, status int, clientMsg, auditReason, trace string, parentBinding sdk.EvidenceBinding, reasonClass string) {
	out := rs.withholdUngovernedTask(ctx, tok, rec, parentBinding, reasonClass,
		"created task denied ("+reasonClass+")", trace)
	rs.auditTaskTraced(ctx, tok, rec.Tool, rec.RequiredScope, rec.TaskID, false,
		auditReason+"; compensating tasks/cancel "+out.note(), "", "MCP07", trace)
	// F-12 holds even for an ambiguous identifier: the POLICY denial is the
	// answer to the client, and the collision is reported through the audit
	// trail rather than rewriting the verdict.
	rs.writeRPCError(w, status, req.ID, rpcAccessDenied, clientMsg)
}

// taskCancelDecision builds the allow decision of one server-initiated
// compensating tasks/cancel (minimal data, stable reason).
func (rs *ResourceServer) taskCancelDecision(tok validatedToken, tool, scope, taskID, reason, trace string) ToolDecision {
	return ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: tool, RequiredScope: scope,
		Allowed: true, Reason: reason, TaskID: taskID, MCPTag: "MCP07",
		TokenBinding: tok.Binding, TraceParent: trace,
		EffectAction: taskActionCompensation, At: rs.clock(),
	}
}

func (rs *ResourceServer) handleTaskMethod(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) {
	trace := requestTraceParent(r, req.Params)
	// (the stage-3 round-1 P0 class, on tasks): STRICT canonicalization is
	// the FIRST thing that touches the params — before the ledger lookup and
	// before any claim. It rejects duplicate keys at every depth AND case-variant
	// aliases of the reserved keys (taskId / _meta / the op-key), extracts the
	// operation key and STRIPS it, and produces the one canonical encoding used
	// for the EffectDigest AND the forwarded bytes. The task id is read from THIS
	// strict tree with exact casing — the historical taskIDFromParams was a
	// case-insensitive json.Unmarshal, exactly the smuggling vector. A structural
	// failure is a protocol refusal (400/-32602) BEFORE the claim.
	canon, cerr := canonicalizeTaskParams(req.Params)
	if cerr != nil {
		rs.auditTaskTraced(ctx, tok, req.Method, "", "", false,
			req.Method+" params refused by strict canonicalization (dup/case-alias/malformed keys)", "", "MCP02", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"malformed task params (strict decoding refused)")
		return
	}
	taskID := canon.TaskID
	if taskID == "" {
		rs.auditTaskTraced(ctx, tok, req.Method, "", "", false,
			req.Method+" missing params.taskId", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			req.Method+" requires params.taskId")
		return
	}
	if rs.durableTasks == nil {
		rs.auditTaskTraced(ctx, tok, req.Method, "", taskID, false,
			"MCP Tasks is disabled because durable task persistence is not configured",
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
			"MCP Tasks is unavailable because durable task persistence is not configured")
		return
	}
	owner := taskOwnerFromToken(rs.tenant, tok)
	var durableGeneration int64
	if cached, exists := rs.taskLedger.lookup(taskID); exists && cached.owner().equals(owner) {
		durableGeneration = cached.DurableRef.Generation
	}
	rec, err := rs.durableTaskRecord(ctx,
		publicTaskOwner(owner, tok.IsDelegated), taskID, durableGeneration)
	if err != nil {
		if !errors.Is(err, ErrDurableTaskNotFound) {
			rs.auditTaskTraced(ctx, tok, req.Method, "", taskID, false,
				"durable task authority unavailable or inconsistent; process cache not used",
				"", "MCP07", trace)
			rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
				"durable task authority is unavailable")
			return
		}
		rs.auditTaskTraced(ctx, tok, req.Method, "", taskID, false,
			"task not tracked by this gateway", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"task not tracked by this gateway")
		return
	}
	// Round-1 F-06: ownership is the CANONICAL OWNER TUPLE, not the bare
	// `sub`. Two trusted issuers can mint the same subject, and one agent subject
	// can act on behalf of different principals through different OAuth clients;
	// comparing only `sub` let any of them drive another principal's task while
	// the EffectDigest recorded the unauthorized identity as internally valid
	// evidence. Tenant, issuer, subject, delegated act-as and OAuth client must
	// ALL match the identity the task was registered under.
	if !rec.owner().equals(owner) {
		rs.auditTaskTraced(ctx, tok, rec.Tool, rec.RequiredScope, taskID, false,
			"task not tracked by this gateway (owner tuple mismatch)", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"task not tracked by this gateway")
		return
	}
	// A QUARANTINE record is a reconciliation artifact, NOT a governance record:
	// it names a live external task whose registration the gateway could not
	// prove — including a task an admission gate DENIED and could not cancel.
	// Making it operable would let the caller keep driving a task policy refused
	// (the escalation the F-03 orphan retention would otherwise introduce), so
	// client task methods are deny-closed on it while it stays fully visible to
	// reconciliation and to the kill-switch sweep.
	// Round-2 N-06: a PENDING registration (claimed and fenced, settlement not
	// yet durable) is equally non-operable. The check is repeated as a
	// compare-and-swap immediately before the forward (enforceTaskDispatch), so
	// a concurrent quarantine can no longer be laundered by a request that
	// passed this one-time check first.
	// ROUND-7: there is no post-retirement client capability to carve out here any
	// more. Round-6 kept a retired row alive as the owner's one remaining read;
	// retirement now REFUSES until that read has already happened and then deletes
	// the row, so the only states this branch sees are the ones that were never
	// client-operable.
	if !rec.operable() {
		rs.auditTaskTraced(ctx, tok, rec.Tool, rec.RequiredScope, taskID, false,
			"task is not operable (pending registration or quarantined for reconciliation: "+rec.QuarantineReason+"); client task methods refused",
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"task is not operable (quarantined for reconciliation)")
		return
	}

	// ROUND-4 R4-04: a cancellation-unconfirmed record is a first-class,
	// generation-scoped RECONCILIATION state. `operable()` ignores status, so a
	// normal governed task whose client cancel or sweep was merely ACKNOWLEDGED —
	// neither quarantined nor reconciling — still accepted tasks/update, while the
	// delivered bar suppressed every later automatic cancellation. An upstream that
	// never honored the cooperative request could therefore be fed new input and
	// keep working with no automatic cancellation path left. The authoritative
	// tasks/get stays permitted (it is the only thing that can resolve the
	// ambiguity) and so does the client's own tasks/cancel (the per-generation
	// intent decides whether it may dispatch); further input is refused, and the
	// record is exposed through the reconciliation inventory.
	if req.Method == methodTasksUpdate && !rec.updatable() {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"tasks/update refused: the task's cancellation is unconfirmed (status "+rec.Status+
				"); it accepts no further input until reconciliation confirms its terminal status",
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"task cancellation is unconfirmed; the task accepts no further input")
		return
	}

	switch req.Method {
	case methodTasksGet:
		rs.handleTaskGet(ctx, w, r, req, tok, rec, canon, trace)
	case methodTasksCancel:
		rs.handleTaskCancel(ctx, w, r, req, tok, rec, canon, trace)
	case methodTasksUpdate:
		rs.handleTaskUpdate(ctx, w, r, req, tok, rec, canon, trace)
	default:
		rs.writeRPCError(w, http.StatusNotFound, req.ID, -32601, "unknown task method")
	}
}

// taskMethodDecision builds the allow decision of one evidence-enforced client
// task method (minimal data; references + verdict only).
func (rs *ResourceServer) taskMethodDecision(tok validatedToken, rec TaskRecord, reason, approvalRef, trace, idKind, action string) ToolDecision {
	return ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: rec.Tool, RequiredScope: rec.RequiredScope,
		Allowed: true, Reason: reason, ApprovalRef: approvalRef, TaskID: rec.TaskID,
		MCPTag: "MCP07", TokenBinding: tok.Binding, TraceParent: trace,
		OperationIDKind: idKind, EffectAction: action, At: rs.clock(),
	}
}

// taskDispatchOutcome is what enforceTaskDispatch reports back to a task
// handler: the relayable result (relayed=true) or the settled failure shape
// (state non-empty ⇔ the settlement recorded; the response was written inside).
type taskDispatchOutcome struct {
	result  json.RawMessage
	state   DispatchState
	relayed bool
	// dispatched reports that rs.upstream.Forward was actually INVOKED, so the
	// effect may have reached the upstream. Round-3 R3-05: the round-2 shape
	// returned a ZERO outcome both when nothing was transmitted (claim/fence
	// refusal) and when the settlement of a real forward failed, which made the
	// two indistinguishable to a caller that has to decide whether a further
	// automatic attempt is permissible.
	dispatched bool
	// settled reports that the outcome recorded durably.
	settled    bool
	forwardErr error
}

// cancelAttemptGuard is the panic-safe custodian of ONE reserved cancellation
// attempt AND of the generation pin that must outlive the external effect
// (round-4 R4-01 + R4-06).
//
// R4-06: round-3 paired `beginCancelAttempt` with an `endCancelAttempt` on the
// normal return path only. A panic inside `Upstream.Forward` or
// `GateAuditor.Settle` unwound past it — the generation lease released (it was
// deferred) but the reservation stayed `inFlight` forever, so every later client
// and sweep cancellation of that task was suppressed as "in flight" and even
// `clearCancelBar` refused it. Only a process restart recovered. The guard is
// installed IMMEDIATELY after a successful reservation and deliberately does NOT
// recover(): it makes the ledger state conservatively deny-closed (the ambiguous
// attempt is barred, the record becomes cancellation-unconfirmed) and the panic
// continues to propagate untouched.
//
// R4-01: the pin is dropped INSIDE settleCancelAttempt, together with the state
// install, under one ledger mutex — never by a defer that runs before the caller
// has computed its verdict.
type cancelAttemptGuard struct {
	ledger     *taskLedger
	taskID     string
	generation string
	leaseHeld  bool
	settled    bool
}

// pin records that the dispatch acquired the generation lease. It is called by
// the dispatch helper the instant the lease is taken, so an abnormal unwind
// between acquisition and settlement still releases it.
func (g *cancelAttemptGuard) pin() {
	if g != nil {
		g.leaseHeld = true
	}
}

// settle applies the atomic cancellation-unconfirmed / bar / reservation / pin
// transition exactly once.
func (g *cancelAttemptGuard) settle(s taskCancelSettlement) {
	if g == nil || g.settled {
		return
	}
	s.releaseLease = g.leaseHeld
	g.settled, g.leaseHeld = true, false
	g.ledger.settleCancelAttempt(g.taskID, g.generation, s)
}

// abandon is the DEFERRED cleanup: a no-op after a normal settlement, and the
// conservative deny-closed close-out of an attempt that unwound abnormally.
func (g *cancelAttemptGuard) abandon() {
	if g == nil || g.settled {
		return
	}
	g.settle(taskCancelSettlement{
		taskCancelBookkeeping: taskCancelBookkeeping{
			quarantine: "a cancellation attempt unwound abnormally; the task needs reconciliation",
		},
		// Conservative on BOTH axes: the forward may have run, so the record
		// becomes cancellation-unconfirmed (TTL-immune), and the attempt is barred
		// ambiguous so nothing re-emits it automatically.
		dispatched: true,
		retryable:  false,
		bar:        taskCancelBarAmbiguous,
		barReason:  "a previous cancellation attempt unwound abnormally without a proven upstream outcome",
	})
}

// enforceTaskDispatch runs the two-phase evidence lifecycle of one CLIENT task
// method (tasks/get | tasks/cancel | tasks/update): claim+anchor → MayEmit →
// leadership fence → forward the CANONICAL governed bytes with the operation
// identity → settle the honest classification → refusal wire shapes (design
// §5). Returns (outcome, true) only when the dispatch completed AND settled
// durably — the caller may then relay; on false every refusal/failure response
// has already been written, and outcome.state tells the caller what durably
// settled ("" when nothing did — claim/fence refusal or a refused settlement).
// The effect KIND selects which record states the pre-forward revalidation
// admits (round-4 R4-04). `hold` is non-nil for a CANCELLATION: the pin is then
// handed to the guard instead of being released here, so it survives until the
// cancellation-unconfirmed state is installed (round-4 R4-01).
// beforeForward, when non-nil, materializes local durable prerequisites after
// the evidence claim and generation lease but before any upstream write. Exact
// pending/blocked replays may invoke it again solely to finish its idempotent
// local receipts; replay never authorizes another upstream dispatch.
// planRelease, when non-nil, is called with the RELAYABLE upstream result
// immediately before the settlement and returns the response-release child
// binding the settlement must NAME (review round 1, S5-06; zero when the
// caller will attempt no release). The MRTR-mediating legs — tasks/get AND
// tasks/cancel — supply it; every other task method passes nil.
func (rs *ResourceServer) enforceTaskDispatch(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalTaskParams, dec ToolDecision, binding sdk.EvidenceBinding, opID, failMsg string, kind taskEffectKind, hold *cancelAttemptGuard, beforeForward func() error, planRelease func(json.RawMessage) sdk.EvidenceBinding) (taskDispatchOutcome, bool) {
	gateRec := rs.auditor.Record(ctx, dec, binding)
	if !gateRec.MayEmit(binding) {
		if beforeForward != nil && gateRec.FailureClass != sdk.FailureReplay &&
			(gateRec.State == GateRecordReplayPending ||
				(gateRec.State == GateRecordReplaySettled && gateRec.Recorded != nil &&
					gateRec.Recorded.State == DispatchBlocked)) {
			if err := beforeForward(); err != nil {
				rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
					"durable task input-response communication is unavailable")
				return taskDispatchOutcome{}, false
			}
		}
		rs.refuseToolCallEvidence(w, req.ID, gateRec, opID)
		return taskDispatchOutcome{}, false
	}
	if fence := rs.auditor.BeforeEffect(ctx, gateRec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return taskDispatchOutcome{}, false
	}
	// ROUND-2 N-03/N-06 + ROUND-3 R3-03: acquire the generation LEASE — the
	// compare-and-swap on the record generation AND the in-flight pin that holds
	// it across the external call. Between the lookup that authorized this request
	// and this line the record may have been quarantined by a failing track
	// settlement, released after a proven cancellation, TTL-expired, or replaced
	// by a DIFFERENT owner's task reusing the same identifier — in which case the
	// forward would apply this caller's update/cancel to somebody else's task. A
	// bare check here was not enough: it released the ledger mutex before the
	// transport write, so the very same replacement could still slip in. While the
	// lease is held the record can neither expire nor be released, so the
	// identifier the forward carries cannot change owner mid-flight.
	// Nothing was transmitted, so the claim settles `blocked` (an honest,
	// durable "stopped before the effect") and the caller is refused.
	if verr := rs.taskLedger.acquireEffectLease(rec.TaskID, rec.Generation, kind); verr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"task record revalidation refused immediately before the effect: "+verr.Error(), "", "MCP07", dec.TraceParent)
		rs.auditor.Settle(ctx, GateOutcome{Record: gateRec, State: DispatchBlocked})
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"task is not operable (the authorized task record changed)")
		return taskDispatchOutcome{state: DispatchBlocked}, false
	}
	if hold != nil {
		// ROUND-4 R4-01: a CANCELLATION keeps its pin past this function. Releasing
		// it here — as round-3's unconditional defer did — reopened the TTL window
		// between "the cancellation ACK arrived" and "the record records that its
		// cancellation is unconfirmed". The guard is told about the pin IMMEDIATELY,
		// so even an abnormal unwind releases it (R4-06).
		hold.pin()
	} else {
		// Released only after the dispatch has been CLASSIFIED and settled — never
		// before the outcome is known.
		defer rs.taskLedger.releaseEffectLease(rec.Generation)
	}
	if beforeForward != nil {
		if err := beforeForward(); err != nil {
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"durable pre-forward communication could not be prepared; upstream dispatch was blocked",
				"", "MCP07", dec.TraceParent)
			rs.auditor.Settle(ctx, GateOutcome{Record: gateRec, State: DispatchBlocked})
			rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
				"durable task input-response communication is unavailable")
			return taskDispatchOutcome{state: DispatchBlocked}, false
		}
	}
	ureq := rs.upstreamReq(r, req.Method, canon.Forward, tok)
	ureq.OperationID = binding.OperationID
	ureq.EffectDigest = binding.EffectDigest
	ureq.FenceToken = gateRec.FenceToken
	res, ferr := rs.upstream.Forward(ctx, ureq)
	state, relay := classifyDispatch(res, ferr)
	out := taskDispatchOutcome{
		result: res.Result, state: state, relayed: relay, dispatched: true, forwardErr: ferr,
	}
	outcome := GateOutcome{
		Record: gateRec, State: state,
		ResultDigest: resultDigest(res.Result), DispatchRef: res.DispatchRef,
	}
	if relay && planRelease != nil {
		// Review round 1 (S5-06): the parent settlement NAMES the release child
		// its caller will claim. The plan is a pure classification of the result;
		// nothing is mediated, emitted or refused by this call.
		outcome.ReleaseBinding = planRelease(res.Result)
	}
	settlement := rs.auditor.Settle(ctx, outcome)
	if settlement.FailureClass != sdk.FailureNone {
		// The outcome did NOT durably record: withhold the response; the claim
		// stays claimed/ambiguous (status replay only, never a re-dispatch). The
		// outcome still reports `dispatched` — the effect may have landed.
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return out, false
	}
	out.settled = true
	if !relay {
		if state == DispatchUnknown {
			rs.writeOperationIndeterminate(w, req.ID, opID)
		} else {
			// not_sent / blocked / completed-with-upstream-error: a definite
			// upstream failure, durably settled against the claim.
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, failMsg)
		}
		return out, false
	}
	return out, true
}

func (rs *ResourceServer) handleTaskGet(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalTaskParams, trace string) {
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"operation-id derivation failed (fail-closed)", "", "MCP07", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	pd := taskPolicyDigest(rec.RequiredScope, rec.Destructive, nil, coazBinding{State: coazStateUnconsulted})
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveTaskEffectDigest(rs.tenant, rs.resource, methodTasksGet, tok,
			rec.TaskID, rec.Generation, rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon, pd, "", "")),
	}
	var plan mrtrReleasePlan
	out, ok := rs.enforceTaskDispatch(ctx, w, r, req, tok, rec, canon,
		rs.taskMethodDecision(tok, rec, "tasks/get authorized", "", trace, idKind, taskActionGetPrefix+idKind),
		binding, opID, "upstream task fetch failed", taskEffectClientRead, nil, nil,
		func(result json.RawMessage) sdk.EvidenceBinding {
			// Design adjudication §2: the TASK-GET authority profile — the
			// ledger-selected, owned, generation-pinned record (rec.TaskID and
			// rec.Generation are bound into this operation's EffectDigest above,
			// and the generation lease holds across the forward) selects the task
			// contract; the response supplies its current exact variant.
			plan = rs.planMRTRRelease(result, binding, releaseClassMRTRTaskResult, mrtrAuthorityTaskGet)
			return plan.release
		})
	if !ok {
		return
	}
	// tasks/get is the AUTHORITATIVE confirmation path of round-2 N-02: a
	// terminal status read from the upstream's own report (and only that) may
	// retire a task whose cancellation was merely acknowledged. ROUND-5 R5-01:
	// only a COMPLETE, conforming GetTaskResult is such a report; anything else is
	// an upstream protocol fault that confirms nothing and is audited as one (the
	// upstream's body is still relayed to the client unchanged — the gateway
	// refuses to BELIEVE it, it does not rewrite it).
	//
	// ROUND-7: the retired-row special case is gone with the handoff cache. Every
	// client `tasks/get` is now the ordinary authoritative confirmation path, and
	// its SUCCESSFUL relay of a conforming TERMINAL report is additionally the PROOF
	// OF DELIVERY that operator retirement waits for (see
	// `taskLedger.recordOwnerTerminalCollection`).
	// ROUND-8 R8-02: the collection proof is bound to the DIGEST of the report that
	// was actually served, so the two facts travel together from here to the ledger.
	var reported, reportDigest string
	var serr error
	rep, reportErr := strictGetTaskResult(rec.TaskID, out.result)
	observation := DurableTaskObservation{
		Kind:         DurableTaskObservationGet,
		ObservedAt:   rs.clock(),
		ResultDigest: resultDigest(out.result),
		OperationID:  opID,
		Dispatched:   true,
	}
	if reportErr != nil {
		serr = reportErr
		observation.Verdict = DurableTaskVerdictBroken
		observation.Status = rec.Status
		observation.StatusReason = taskDefectClass(reportErr)
		observation.TTLMs = cloneInt64(rec.TTLMs)
		observation.PollIntervalMs = cloneInt64(rec.PollIntervalMs)
	} else {
		observation.Status = rep.Status
		observation.StatusReason = rep.Reason
		observation.TTLMs = cloneInt64(rep.TTLMs)
		observation.PollIntervalMs = cloneInt64(rep.PollIntervalMs)
		observation.Verdict = DurableTaskVerdictClean
		observation.ResultDigest = rep.Digest
		observation.Terminal = taskStatusTerminal(rep.Status)
		observation.InputRequests = cloneDurableTaskInputRefs(rep.InputRequests)
	}
	// The durable binding is updated before the process cache and before any
	// upstream task result is returned. A failed write leaves the cache untouched
	// and withholds the result; it is never replaced by the stale local row.
	if err := rs.persistDurableTaskObservation(ctx, rec, observation); err != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"durable tasks/get observation could not be recorded; result WITHHELD and process cache left unchanged",
			"", "MCP07", trace)
		relDec := rs.withheldReleaseDecision(tok, rec.Tool, rec.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rs.settleWithheldRelease(ctx, relDec, plan.release, resultDigest(out.result), func(reason string) {
			rs.auditTaskRecordTraced(ctx, tok, rec, false, reason, "", "MCP07", trace)
		})
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	if reportErr == nil {
		if updated, applied := rs.applyTaskReport(rec, rep); applied {
			reported, reportDigest = updated.Status, rep.Digest
		}
	}
	if serr != nil {
		// ROUND-6 R6-05: the persisted reason carries the stable validation CLASS,
		// never `serr.Error()` — the strict decoder's text quotes upstream-controlled
		// PROPERTY NAMES, so raw parser text in the audit trail is a content channel
		// around the response projection.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"upstream tasks/get result is not a conforming SEP-2663 GetTaskResult; the local status was NOT confirmed; validation class: "+
				taskDefectClass(serr), "", "MCP07", trace)
	}
	// (stage 5): the MRTR result mediation of the authoritative tasks/get
	// response is evidence-bound now (it was the stage-4 LEGACY residual this
	// stage closes). The dispatch outcome settled durably BEFORE the mediation
	// could hijack the response (the outcome is durable regardless); a MEDIATED
	// response that passed is then released ONLY against an anchored
	// response-release child of this tasks/get operation, while an UNMEDIATED
	// response keeps the stage-4 shape below (plain write, error consumed).
	// Round 2 (F-1): each post-observation refusal below records its disposition
	// on the RELEASE CHILD (settled `withheld`); the parent already settled the
	// dispatch fact inside enforceTaskDispatch.
	taskGetWithheld := func(release sdk.EvidenceBinding) {
		relDec := rs.withheldReleaseDecision(tok, rec.Tool, rec.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rs.settleWithheldRelease(ctx, relDec, release, resultDigest(out.result), func(reason string) {
			rs.auditTaskRecordTraced(ctx, tok, rec, false, reason, "", "MCP07", trace)
		})
	}
	if plan.noAuthority {
		// Internal bug — no authority profile reached the planner (design
		// adjudication §5): withhold the raw result behind the fixed
		// evidence-unavailable shape; no delivery is recorded, the record stays
		// retained.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"MRTR release planning for the tasks/get result ran without an authority profile (internal defect, "+
				"fail-safe); the result was withheld and the record is RETAINED", "", "MCP07", trace)
		taskGetWithheld(deriveResponseReleaseBinding(rs.tenant, binding, plan.class, resultDigest(out.result)))
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}
	if observed, reason, wire, refused := plan.coreRefusal(); refused {
		// STAGE-7 P0-3 / M-1R2: a result no conforming server may send on an
		// authoritative tasks/get — the forbidden core discriminator (SEP-2663
		// GetTaskResult is `resultType:"complete"`; an input requirement is
		// reported by the task STATUS) or a duplicated discriminator that is
		// ambiguous by construction. A protocol fault, never a content question:
		// refused before the mediator and before any release, recording only what
		// was OBSERVED; no delivery is recorded and the record stays RETAINED, so
		// the owner can come back for an answer its own contract admits.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"the upstream tasks/get result carries "+observed+": REFUSED, no delivery was recorded and the "+
				"record is RETAINED; reason class: "+reason, "", "MCP07", trace)
		taskGetWithheld(plan.release)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, wire)
		return
	}
	if plan.unreadable {
		// The ledger-selected task contract makes this result's input-required
		// payload governed, and its exact `inputRequests` member is present but is
		// not the pin's map (schema.ts:553-555) — unreadable, so it is refused
		// rather than relayed on the plain leg (adjudication §4, the retained
		// projection-integrity check). No delivery is recorded and the record
		// stays retained: the owner can come back for an unambiguous answer.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"the upstream tasks/get result selects the governed input-required task contract — or a duplicated "+
				"status discriminator makes that reading impossible to exclude — and its governed payload cannot "+
				"be projected: unreadable, refused; no delivery was recorded and the record is RETAINED",
			"", "MCP07", trace)
		taskGetWithheld(plan.release)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned an ambiguous mediated result")
		return
	}
	if plan.mediate && rs.mediateMRTRResultEntries(ctx, w, req, tok, plan.entries, trace, func() {
		// The hijack's disposition is durable on the release child BEFORE the
		// deny is written (round-3 settle-then-write).
		taskGetWithheld(plan.release)
	}) {
		// The client did NOT receive the result, so no delivery is recorded: the
		// record stays retained, capacity-counting and un-retirable (deny-closed).
		return
	}
	var werr error
	// The release gate is the CLASSIFICATION, never the mediation intent (the r2
	// review's P0-2, which stage-7 B-bis closed on tools/call and ui:// but left
	// standing here): a requestState-only report, a valid empty `inputRequests:{}`
	// and a community build with no mediator all carry governed MRTR bytes out of
	// this surface, and every one of them took the plain write leg with no release
	// child for a hostile or unavailable journal to refuse. Zero inspections is
	// not zero evidence.
	if plan.inputRequired {
		relDec := rs.releaseDecision(tok, rec.Tool, rec.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rel, rok := rs.anchorResponseRelease(ctx, w, req.ID, relDec, plan.release, opID, func(reason string) {
			rs.auditTaskRecordTraced(ctx, tok, rec, false, reason, "", "MCP07", trace)
		})
		if !rok {
			// Withheld: no byte was written, so no delivery is recorded and the
			// record stays retained — the owner can come back and collect it
			// (stage-5 inheritance: a withheld response stays retryable without
			// cleaning owner obligations; a re-read is a new parent operation and
			// therefore a new release child).
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"the mediated tasks/get response was WITHHELD (release evidence refused); no delivery was recorded and the record is RETAINED",
				"", "MCP07", trace)
			return
		}
		werr = rel.write(ctx, w, req.Method, req.ID, out.result).err
	} else {
		werr = rs.writeResult(w, req.Method, req.ID, out.result)
	}
	// ROUND-7 R7-03, the whole point of making the write report itself: proof of
	// delivery is recorded ONLY when a conforming TERMINAL report was actually
	// written to the owner. Round-6 discarded this error and let an attempted write
	// discharge the owner's authorization — the owner disconnected, the write
	// failed, and the record was destroyed anyway. A failed write leaves the record
	// exactly as it was: visible, counted and refused to `retire`, so the owner can
	// come back and collect it.
	//
	// ROUND-8 R8-06, the honest reading of a nil error: this process's
	// `http.ResponseWriter` accepted every byte of the encoded body. It is NOT a
	// remote acknowledgement, and no Go HTTP contract provides one. Everything built
	// on it is deliberately the conservative direction — a failed write records
	// nothing, and a successful write only ever REMOVES an obligation the same owner
	// could re-acquire by reading again.
	if werr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"the terminal tasks/get response could not be written to its owner; no delivery was recorded and the record is RETAINED",
			"", "MCP07", trace)
		return
	}
	if serr == nil && taskStatusTerminal(reported) {
		// ROUND-8 R8-02: bound to the exact report served. If a concurrent privileged
		// read confirmed a DIFFERENT terminal report while this response was being
		// written, the compare-and-swap refuses and the record keeps owing a
		// collection — of the answer that is now authoritative.
		rs.taskLedger.recordOwnerTerminalCollection(rec.TaskID, rec.Generation, reportDigest)
	}
}

func (rs *ResourceServer) handleTaskCancel(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalTaskParams, trace string) {
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"operation-id derivation failed (fail-closed)", "", "MCP07", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	// ROUND-3 R3-05: the client's own cancellation participates in the SAME
	// per-generation cancellation intent as the sweep and the compensations, and
	// it RESERVES that intent BEFORE its forward. Round-2 the client path only
	// recorded a bar afterwards, so a kill-switch sweep could reserve and dispatch
	// the same logical cancellation of the same generation while the client's was
	// in flight: two governed operations claiming one cooperative cancellation,
	// two upstream effects, and accounting that says two different operations
	// emitted the same actuation. A suppressed reservation NEVER forwards.
	res := rs.taskLedger.beginCancelAttempt(rec.TaskID, rec.Generation)
	if !res.ok {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"tasks/cancel suppressed by the per-generation cancellation intent ("+string(res.bar)+"): "+res.reason,
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusConflict, req.ID, rpcAccessDenied,
			"a cancellation of this task is already in flight or has already been delivered")
		return
	}
	// ROUND-4 R4-06: the panic-safe custodian is installed IMMEDIATELY after the
	// successful reservation — before any code that can panic — and it also owns
	// the generation pin the dispatch will take (R4-01).
	guard := &cancelAttemptGuard{ledger: rs.taskLedger, taskID: rec.TaskID, generation: rec.Generation}
	defer guard.abandon()

	pd := taskPolicyDigest(rec.RequiredScope, rec.Destructive, nil, coazBinding{State: coazStateUnconsulted})
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveTaskEffectDigest(rs.tenant, rs.resource, methodTasksCancel, tok,
			rec.TaskID, rec.Generation, rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon, pd, "", "")),
	}
	var plan mrtrReleasePlan
	out, ok := rs.enforceTaskDispatch(ctx, w, r, req, tok, rec, canon,
		rs.taskMethodDecision(tok, rec, "tasks/cancel authorized", "", trace, idKind, taskActionCancelPrefix+idKind),
		binding, opID, "upstream task cancellation failed", taskEffectClientCancel, guard, nil,
		func(result json.RawMessage) sdk.EvidenceBinding {
			// Design adjudication §2 + stage-7 P0-3: the TASK-CANCEL profile —
			// a cooperative ack-only acknowledgement (SEP-2663 CancelTaskResult is
			// `resultType:"complete"`), never an authoritative task-status read.
			// NEITHER discriminator is interpreted on this leg: `status` is never
			// task state, and the core `resultType:"input_required"` is an upstream
			// MUST NOT the plan reports as unsanctionedCore — refused below, and
			// (H-1) excluded from the acknowledgement fact the custody settlement
			// records.
			plan = rs.planMRTRRelease(result, binding, releaseClassMRTRTaskCancel, mrtrAuthorityTaskCancel)
			return plan.release
		})

	// ONE atomic transition closes the attempt (round-4 R4-01): the
	// cancellation-unconfirmed record state, the bar (`delivered` when the upstream
	// acknowledged — never dropped or downgraded, R3-05), the end of the
	// reservation and the release of the generation pin, all under one ledger
	// mutex. Round-3 released the pin FIRST and wrote the status afterwards, so a
	// TTL eviction in between deleted a still-`working` record whose cancellation
	// had already been acknowledged.
	//
	// A further AUTOMATIC attempt is permissible only when this one provably
	// emitted nothing (nothing dispatched) or durably settled a state proving the
	// request never reached the upstream (settledStatePermitsCancelRetry: not_sent,
	// or blocked — pre-dispatch by contract since the stage-7 vocabulary split;
	// withheld is the settled proof of the OPPOSITE and never permits a retry).
	// H-1 (stage-7 r1 contrast, 2026-07-30): `out.relayed` is a TRANSPORT fact —
	// a completed round trip whose body was relayable. It is NOT, by itself, an
	// acknowledgement: when the body carries the core input_required discriminator
	// the extension forbids on this method, the client is answered 502 and nothing
	// may durably claim the cancellation was delivered. Collapsing the two facts
	// wrote `cancel_requested` and armed the `delivered` bar for a refused result
	// — a false delivery claim on the evidence surface, in exactly the place the
	// ledger reserves `ambiguous` for what needs reconciliation.
	//
	// H-1R2/H-1R3: the conformity decision is the SHARED one (cancelAckViolation)
	// — the same the server-initiated emitter and the exported Client.TaskCancel
	// consume — never a local reading of the plan, so the emitters cannot drift.
	// It refuses BOTH discriminator-level classes: the forbidden core literal
	// (plan.unsanctionedCore) and the duplicated discriminator
	// (plan.ambiguousDiscriminator), so it is deliberately STRICTER than either
	// plan flag alone.
	conformingAck := conformingCancelAck(out.relayed, out.result)
	settlement := taskCancelSettlement{
		dispatched: out.dispatched,
		acked:      conformingAck,
		retryable: !out.dispatched ||
			(out.settled && settledStatePermitsCancelRetry(out.state)),
		bar:       taskCancelBarAmbiguous,
		barReason: "a previous client tasks/cancel ended without a proven upstream outcome",
	}
	switch {
	case conformingAck:
		// ROUND-2 N-02: a bare acknowledgement is NOT a terminal cancellation.
		// tasks/cancel is COOPERATIVE by contract (Client.TaskCancel): the upstream
		// may legitimately keep reporting the task as working. The round-1 code
		// stored terminal `canceled` here, which removed a possibly still-live task
		// from every kill-switch sweep. The gateway records the non-terminal
		// `cancel_requested` state, keeps the task fully visible, and bars automatic
		// server-initiated re-dispatch for this generation; only an upstream
		// tasks/get (or explicit reconciliation) makes it terminal.
		settlement.status = taskCancelRequestedStatus
		settlement.statusReason = "client requested cancellation; terminal status unconfirmed"
		settlement.barReason = "a client tasks/cancel was already delivered for this task"
	case out.relayed:
		// The round trip finished but what came back is outside the
		// CancelTaskResult contract — not an acknowledgement (the P0-3 disposition
		// table): no status is written (the record stays as it was), `dispatched`
		// installs CancelUnconfirmed, retryable stays false (the request DID reach
		// the upstream) and the ambiguous bar demands reconciliation instead of
		// asserting a delivery nobody proved. The reason is deliberately
		// CLAIM-FREE (M-1R2): which non-conformance it was — the forbidden core
		// discriminator or a duplicated one — is the refusal branch's record, and
		// this durable phrase must not affirm a literal the body may not carry.
		settlement.barReason = "a client tasks/cancel round trip returned a result that is not a conforming " +
			"acknowledgement; delivery of the cancellation is unproven"
	case out.settled && out.state != DispatchUnknown && out.state != DispatchBlocked && out.forwardErr != nil:
		// Definite, durably settled upstream failure: keep the local record active
		// as cancel_failed so the kill-switch sweep can retry it. An unknown or
		// unsettled outcome writes no status (the cancel may have landed) — but it
		// still installs CancelUnconfirmed through `dispatched`, so the record can
		// never be TTL-evicted while that ambiguity stands.
		settlement.status = taskCancelFailedStatus
		settlement.statusReason = out.forwardErr.Error()
	}
	verdict := DurableTaskVerdictUnobservable
	if conformingAck {
		verdict = DurableTaskVerdictClean
	} else if out.relayed {
		verdict = DurableTaskVerdictBroken
	}
	durableStatus := settlement.status
	if durableStatus == "" {
		durableStatus = rec.Status
	}
	durableErr := rs.persistDurableTaskObservation(ctx, rec, DurableTaskObservation{
		Kind: DurableTaskObservationCancel, Verdict: verdict,
		Status: durableStatus, StatusReason: settlement.barReason,
		ResultDigest: resultDigest(out.result), OperationID: opID,
		Dispatched: out.dispatched, Acknowledged: conformingAck,
		CancelRequested: out.dispatched,
	})
	if durableErr != nil {
		// The upstream effect may already have landed. Keep the local cancellation
		// intent conservative, but never report success while its durable binding
		// could not record the observation.
		settlement.retryable = !out.dispatched
		settlement.bar = taskCancelBarAmbiguous
		settlement.barReason = "durable cancellation observation could not be recorded"
		if out.dispatched {
			settlement.quarantine = "durable cancellation observation could not be recorded; reconciliation required"
		}
	}
	guard.settle(settlement)
	if durableErr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"durable tasks/cancel observation could not be recorded; no success returned",
			"", "MCP07", trace)
		if ok {
			rs.writeOperationIndeterminate(w, req.ID, opID)
		}
		return
	}
	if !ok {
		return
	}
	// Review round 1, SAME CLASS as S5-01 on a surface the review did not
	// examine: a cancellation result is an upstream task result relayed verbatim,
	// so it can carry MRTR input requests. It gets the identical treatment as the
	// other two MRTR result sites — unreadable governed member ⇒ refused,
	// mediated ⇒ released only
	// against the anchored child this cancellation's settlement already named.
	// The cancellation itself is already settled and its ledger transition already
	// applied above, so withholding this response changes no custody state (this
	// write drew no conclusion before and draws none now).
	// (tasks/update needs none of this: strictTaskUpdateAck allow-lists the ack
	// members and refuses any state-reporting body, `inputRequests` included.)
	// Round 2 (F-1): each post-observation refusal below records its disposition
	// on the RELEASE CHILD (settled `withheld`); the parent already settled the
	// dispatch fact inside enforceTaskDispatch.
	taskCancelWithheld := func(release sdk.EvidenceBinding) {
		relDec := rs.withheldReleaseDecision(tok, rec.Tool, rec.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rs.settleWithheldRelease(ctx, relDec, release, resultDigest(out.result), func(reason string) {
			rs.auditTaskRecordTraced(ctx, tok, rec, false, reason, "", "MCP07", trace)
		})
	}
	if plan.noAuthority {
		// Internal bug — no authority profile reached the planner (design
		// adjudication §5): withhold the raw result behind the fixed
		// evidence-unavailable shape. The cancellation itself is settled above.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"MRTR release planning for the tasks/cancel result ran without an authority profile (internal defect, "+
				"fail-safe); the result was withheld; the cancellation itself is settled", "", "MCP07", trace)
		taskCancelWithheld(deriveResponseReleaseBinding(rs.tenant, binding, plan.class, resultDigest(out.result)))
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}
	if observed, reason, wire, refused := plan.coreRefusal(); refused {
		// STAGE-7 P0-3 / M-1R2: SEP-2663 defines CancelTaskResult as the ack-only
		// `resultType:"complete"` shape, so neither the core input_required
		// discriminator nor a duplicated discriminator belongs on a cancellation
		// acknowledgement. Refused before the mediator and before any release,
		// recording only what was OBSERVED. The cancellation CUSTODY already
		// recorded the non-ack disposition above — status untouched, ambiguous
		// bar, CancelUnconfirmed (the settlement owns custody); this branch owns
		// the evidence child and the wire refusal.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"the upstream tasks/cancel result carries "+observed+": REFUSED; the cancellation itself is "+
				"settled as a NON-acknowledgement; reason class: "+reason, "", "MCP07", trace)
		taskCancelWithheld(plan.release)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, wire)
		return
	}
	// The two legs below are UNREACHABLE-BY-CONSTRUCTION since the stage-7 guard,
	// and deliberately kept: the cancel profile has no authoritative MRTR
	// discriminator left (neither the core resultType nor the task status), so
	// `unreadable` and `mediate` can only become reachable again if some future
	// edit gives it one — and this is the machinery such an edit must not have to
	// re-derive. TestStage7GuardStrictness pins the invariant that makes them
	// dead, so the change that revives them cannot happen silently.
	if plan.unreadable {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"the upstream tasks/cancel result declares an input requirement but its inputRequests member is "+
				"present and is not the schema's map — unreadable, refused; the cancellation itself is settled",
			"", "MCP07", trace)
		taskCancelWithheld(plan.release)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned an ambiguous mediated result")
		return
	}
	if plan.mediate && rs.mediateMRTRResultEntries(ctx, w, req, tok, plan.entries, trace, func() {
		// The disposition of the observed cancellation result is durable on the
		// release child BEFORE the deny is written (round-3 settle-then-write).
		taskCancelWithheld(plan.release)
	}) {
		return
	}
	// Same P0-2 correction as the tasks/get leg: the release gate is the
	// CLASSIFICATION, not the mediation intent — governed bytes leave under an
	// anchored child whether or not anything was inspectable.
	if plan.inputRequired {
		relDec := rs.releaseDecision(tok, rec.Tool, rec.RequiredScope, plan.class, "MCP07", trace)
		relDec.TaskID = rec.TaskID
		rel, rok := rs.anchorResponseRelease(ctx, w, req.ID, relDec, plan.release, opID, func(reason string) {
			rs.auditTaskRecordTraced(ctx, tok, rec, false, reason, "", "MCP07", trace)
		})
		if !rok {
			return
		}
		_ = rel.write(ctx, w, req.Method, req.ID, out.result)
		return
	}
	_ = rs.writeResult(w, req.Method, req.ID, out.result)
}

func (rs *ResourceServer) handleTaskUpdate(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken, rec TaskRecord, canon canonicalTaskParams, trace string) {
	currentPolicy, ok := rs.toolset.resolve(rec.Tool)
	if !ok {
		rs.denyTaskUpdateRevokedTool(ctx, w, req, tok, rec, trace)
		return
	}
	if !tok.hasScope(rec.RequiredScope) {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"insufficient scope for tasks/update", "", "MCP02", trace)
		rs.challengeScope(w, req.ID, rec.RequiredScope)
		return
	}
	if !roleAllowed(currentPolicy, tok.Roles) {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"caller role not permitted for tasks/update", "", "MCP02", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			"task update not permitted for caller role")
		return
	}

	// Review: tasks/update is an actuation on the original tool, so the COAZ
	// centralized-policy gate re-runs here exactly as it does for tools/call
	// (rs.go handleToolsCall) — additive when nil, deny-closed on error. The
	// consulted-allow posture + stable references bind into the effect identity
	// (same round-2 rule as tools/call: never the Reason text).
	coaz := coazBinding{State: "unwired"}
	if rs.coazEvaluator != nil {
		coazScopes := make(map[string]struct{}, len(tok.Scopes))
		for k, v := range tok.Scopes {
			coazScopes[k] = v
		}
		cdec, cerr := rs.coazEvaluator.EvaluateToolCall(ctx, COAZRequest{
			Subject:     tok.Subject,
			Issuer:      tok.Issuer,
			Tool:        rec.Tool,
			ServerURI:   rs.resource,
			Scopes:      coazScopes,
			Annotations: currentPolicy.Annotations,
			Tenant:      rs.tenant,
		})
		if cerr != nil {
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"COAZ evaluation error on tasks/update (fail-closed)", "", "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
				"task update authorization evaluation unavailable")
			return
		}
		if !cdec.Allow {
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"COAZ deny on tasks/update: "+cdec.Reason, "", "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
				"task update not permitted by authorization policy")
			return
		}
		coaz = coazBinding{State: "allow", DecisionRef: cdec.DecisionRef, PolicyVersion: cdec.PolicyVersion}
	}

	approvalRef, approvedPlan := "", ""
	if rec.Destructive {
		// Round-1 F-08: the approval plan binds the EXACT canonical update
		// payload (operation-key/trace excluded — the same view the EffectDigest
		// binds) and the canonical owner. A payload-blind plan hash let one
		// approved benign update authorize arbitrary different inputResponses
		// under a fresh operation key: the gate saw the same plan and returned the
		// old approval while the journal happily anchored the changed effect.
		plan := taskUpdatePlanHash(rec.TaskID, rec.owner().digest(), rec.Tool, canon.effectHash())
		dec, gerr := rs.gate.Authorize(ctx, ToolApprovalRequest{
			Tenant: rs.tenant, Subject: tok.Subject, Tool: rec.Tool,
			Scope: rec.RequiredScope, PlanHash: plan, RequestedBy: tok.Subject,
		})
		if gerr != nil {
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"tasks/update gate error (fail-closed)", "", "MCP07", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
				"approval gate error")
			return
		}
		// Review round 2 (blocker 4, same class as S5-05): STRICT equality —
		// `plan` is always non-empty, so an approval bound to an empty PlanHash does
		// not authorize a destructive tasks/update. The `PlanHash != "" &&` guard let
		// an unbound approval through.
		if !dec.Allowed() || dec.PlanHash != plan {
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"destructive tasks/update not approved ("+string(dec.Status)+")", dec.ApprovalRef, "MCP02", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
				"destructive task update requires human approval ("+string(dec.Status)+")")
			return
		}
		approvalRef, approvedPlan = dec.ApprovalRef, plan
	}

	// MRTR input-response mediation of the REQUEST stays pre-claim (part of the
	// authorization surface, like tools/call) — over the EXACT-CASED member of
	// the strict tree (F-07), never a case-insensitive re-parse of the bytes.
	if rs.mediateMRTRInputResponses(ctx, w, req, tok, canon.InputResponses, trace) {
		return
	}

	// --- evidence enforcement: claim → fence → forward → settle → respond.
	// The historical post-hoc "tasks/update authorized[; upstream forward
	// failed]" audits are GONE — they are settlement outcomes of the claim now.
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"operation-id derivation failed (fail-closed)", "", "MCP07", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	pd := taskPolicyDigest(rec.RequiredScope, rec.Destructive, currentPolicy.AllowedRoles, coaz)
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveTaskEffectDigest(rs.tenant, rs.resource, methodTasksUpdate, tok,
			rec.TaskID, rec.Generation, rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon, pd, approvalRef, approvedPlan)),
	}
	out, okd := rs.enforceTaskDispatch(ctx, w, r, req, tok, rec, canon,
		rs.taskMethodDecision(tok, rec, "tasks/update authorized", approvalRef, trace, idKind, taskActionUpdatePrefix+idKind),
		binding, opID, "upstream task update failed", taskEffectClientUpdate, nil,
		func() error {
			return rs.prepareDurableTaskInputResponses(ctx, rec, binding, canon.InputResponses)
		}, nil)
	if !okd {
		return
	}
	// ROUND-3 R3-01: a tasks/update result is an ACK-ONLY acknowledgement. The
	// extension defines UpdateTaskResult as an empty, eventually-consistent
	// acknowledgement and directs clients to observe status through tasks/get or
	// task notifications, so an update result is NEVER authoritative about task
	// state. Round-2 fed it to syncTaskStatusFromResult, which calls
	// confirmStatus: a broken or hostile upstream could answer a tasks/update
	// with `{"status":"canceled"}` and CONFIRM a terminal status for a task
	// nobody read and nobody canceled, removing a live task from `active()` and
	// from every later kill-switch sweep. The status synchronization is gone
	// entirely — only a strictly validated tasks/get result may call
	// confirmStatus — and the ack shape is validated strictly so a
	// state-reporting body is not relayed to the client either: handing the
	// caller an authoritative-looking status the governance view deliberately
	// ignores is exactly the two-consumer differential this surface refuses
	// everywhere else.
	if aerr := strictTaskUpdateAck(out.result); aerr != nil {
		if err := rs.persistDurableTaskObservation(ctx, rec, DurableTaskObservation{
			Kind: DurableTaskObservationUpdate, Verdict: DurableTaskVerdictBroken,
			Status: rec.Status, StatusReason: taskDefectClass(aerr), ResultDigest: resultDigest(out.result),
			OperationID: opID, Dispatched: out.dispatched,
		}); err != nil {
			rs.auditTaskRecordTraced(ctx, tok, rec, false,
				"durable tasks/update fault observation could not be recorded; result WITHHELD",
				"", "MCP07", trace)
			rs.writeOperationIndeterminate(w, req.ID, opID)
			return
		}
		// ROUND-7 R7-06: the PERSISTED reason carries the stable validation CLASS, not
		// `aerr.Error()`. The ack validator quotes the offending upstream PROPERTY NAME
		// verbatim (`carries "…"`), and the alias rejector quotes the alias, so raw
		// validator text here was the same durable content channel R6-05 closed on the
		// tasks/get paths — the taxonomy simply had not reached this validator.
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"upstream tasks/update result violates the ack-only shape and was NOT applied to the governance view; validation class: "+
				taskDefectClass(aerr),
			"", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
			"upstream returned a malformed tasks/update acknowledgement")
		return
	}
	if err := rs.persistDurableTaskObservation(ctx, rec, DurableTaskObservation{
		Kind: DurableTaskObservationUpdate, Verdict: DurableTaskVerdictClean,
		Status: rec.Status, StatusReason: rec.StatusReason,
		ResultDigest: resultDigest(out.result), OperationID: opID,
		Dispatched: out.dispatched, Acknowledged: true,
	}); err != nil {
		rs.auditTaskRecordTraced(ctx, tok, rec, false,
			"durable tasks/update acknowledgement could not be recorded; result WITHHELD and process cache left unchanged",
			"", "MCP07", trace)
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	_ = rs.writeResult(w, req.Method, req.ID, out.result)
}

// denyTaskUpdateRevokedTool denies a tasks/update whose original tool no longer
// resolves in the server toolset AND compensates upstream: the running task must
// not keep executing a revoked tool, so a compensating tasks/cancel is issued —
// evidence-gated.
//
// Two round-1 findings shape this path:
//
//   - F-04: the compensation is derived from the TASK's own anchored
//     creation/track binding (taskOriginBinding), NEVER from the client's
//     unclaimed update request. Deriving a child from an unverified client
//     operation tuple skipped the parent's rebind/replay check entirely: a key
//     already durably bound to another effect would have returned 409 on the
//     normal path, but the revoked branch never called Record for it and simply
//     minted a fresh child claim.
//   - F-12: the POLICY denial (403, revoked tool) stands whatever the
//     compensation's evidence did. Only the compensating ALLOW needs evidence.
//
// A refused compensation anchor leaves the upstream cancel un-issued and the
// record quarantined-and-active (an upstream task that was not canceled must
// stay visible to the kill-switch sweep).
func (rs *ResourceServer) denyTaskUpdateRevokedTool(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, rec TaskRecord, trace string) {
	parent := taskOriginBinding(rs.tenant, rec)
	comp := deriveTaskCancelCompensationBinding(rs.tenant, parent, rec.TaskID, rec.Generation, taskCancelClassToolRevoked)
	// Round-4 R4-01: every record transition of this compensation is applied
	// ATOMICALLY with the bar/reservation/pin release, so the record can never be
	// TTL-evicted in the window between the upstream cancel returning and the
	// gateway recording what it means.
	out := rs.anchorAndCancelTask(ctx,
		rs.taskCancelDecision(tok, rec.Tool, rec.RequiredScope, rec.TaskID,
			"compensating tasks/cancel: original tool no longer permitted", trace),
		fixedTaskCancelBinding(comp), rec.TaskID, rec.Generation, tok.Subject, trace,
		func(o taskCancelOutcome) taskCancelBookkeeping {
			if o.succeeded {
				// Round-2 N-02 applies here too, through the status-CONFIRMATION
				// dimension: the local status records the cancellation the gateway
				// requested, but it is marked UNCONFIRMED, so taskRecordActive keeps the
				// task visible to reconciliation and to every future sweep until an
				// upstream report proves it terminal.
				return taskCancelBookkeeping{
					status:       taskStatusCanceled,
					statusReason: "tool no longer permitted (cancellation acknowledged, terminal status unconfirmed)",
				}
			}
			// NOT provably canceled: keep the task visible and flagged for
			// reconciliation. Only a durably settled, definitively failed dispatch
			// moves the status (an ambiguous outcome mutates none) — and a
			// NON-CONFORMING acknowledgement moves none either (H-1R2: the round
			// trip finished, but writing cancel_failed would claim an upstream
			// verdict nobody observed).
			book := taskCancelBookkeeping{quarantine: "revoked-tool compensation not confirmed"}
			if o.disposition == taskCancelSettled && o.state != DispatchUnknown && !o.nonConformingAck {
				book.status = taskCancelFailedStatus
				book.statusReason = "compensating cancel " + o.note()
			}
			return book
		})

	reason := "tasks/update denied because original tool is no longer permitted; compensating tasks/cancel " + out.note()
	if !out.succeeded {
		if out.disposition == taskCancelSuppressed {
			// Nothing was reserved, so no settlement ran: mark the record for
			// reconciliation here (a plain compare-and-swap; no effect is in flight).
			rs.taskLedger.markQuarantine(rec.TaskID, rec.Generation, "revoked-tool compensation not confirmed")
		}
		reason += "; " + quarantineNote(rec.TaskID, "revoked-tool compensation not confirmed")
	}
	if updated, uok := rs.taskLedger.lookup(rec.TaskID); uok && updated.Generation == rec.Generation {
		rec = updated
	}
	rs.auditTaskRecordTraced(ctx, tok, rec, false, reason, "", "MCP07", trace)
	rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
		"task tool no longer permitted by server policy")
}

// CancelActiveTasks cooperatively cancels tracked active tasks matching match
// (the composition-root kill-switch sweep).: every upstream cancellation is
// ONE evidence-bound operation per task, whose identity comes from the ledger's
// ATOMIC per-task cancellation intent (round-1 F-01):
//
//   - at most one attempt per task may be in flight, so two concurrent sweeps
//     can never both dispatch the same logical cancellation;
//   - the attempt generation advances — minting a NEW operation identity — only
//     when the previous attempt provably emitted nothing (claim/fence refusal)
//     or durably settled not_sent/blocked. After an `unknown` or unsettled
//     outcome, or after a cancellation that was actually delivered, every
//     automatic re-attempt is barred and reported for reconciliation. The first
//     implementation minted a RANDOM id per pass, which evaded the frozen
//     single-use-effect invariant instead of enforcing it.
//
// A cancellation whose claim cannot anchor is SKIPPED — the upstream is NOT
// called and the task stays active — and the first fault is returned while the
// remaining tasks continue. Only a cancellation whose upstream result itself
// SUCCEEDED marks the task canceled or counts toward the returned total
// (round-1 F-02: a completed round-trip carrying a JSON-RPC error is an observed
// protocol exchange, never a successful actuation). Deliberate consequence
// (design §7, safety-over-liveness): a ledger outage blocks even the emergency
// cancellation; break-glass does not bypass evidence.
func (rs *ResourceServer) CancelActiveTasks(ctx context.Context, match func(TaskRecord) bool, reason string) (int, error) {
	records := rs.taskLedger.active(match)
	canceled := 0
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, rec := range records {
		owner := rec.owner()
		taskID, generation := rec.TaskID, rec.Generation
		out := rs.anchorAndCancelTask(ctx, ToolDecision{
			Tenant: rs.tenant, Subject: rec.Subject, IsDelegated: rec.IsDelegated, ActAs: rec.ActAs,
			Tool: rec.Tool, RequiredScope: rec.RequiredScope,
			Allowed: true, Reason: reason, TaskID: taskID, MCPTag: "MCP07",
			OperationIDKind: opIDKindRequestInstance, EffectAction: taskActionSweep, At: rs.clock(),
		}, func(attempt uint64) sdk.EvidenceBinding {
			return deriveTaskSweepBinding(rs.tenant, rs.upstreamDescriptor, owner, taskID, generation,
				taskCancelClassKillSwitch, attempt)
		}, taskID, generation, rec.Subject, "", func(o taskCancelOutcome) taskCancelBookkeeping {
			// ROUND-4 R4-01: the sweep's record transitions are applied ATOMICALLY
			// with the bar/reservation/pin release. Round-3 applied them after
			// anchorAndCancelTask returned — i.e. after the pin had been dropped —
			// so a TTL eviction could delete the still-`working` record of a task
			// whose cancellation had already been acknowledged upstream.
			if o.succeeded {
				// ROUND-2 N-02: the upstream ACKNOWLEDGED the cancellation, which the
				// tasks extension explicitly does not make terminal. The task is
				// retained in the non-terminal `cancel_requested` state (visible to
				// reconciliation and to future sweeps, which the intent bars from
				// re-emitting) and becomes terminal only when tasks/get or explicit
				// reconciliation confirms it.
				return taskCancelBookkeeping{
					status: taskCancelRequestedStatus, statusReason: reason,
					reconcileIfQuarantined: true,
				}
			}
			switch o.disposition {
			case taskCancelUnsettled:
				return taskCancelBookkeeping{quarantine: "sweep cancellation outcome did not record"}
			case taskCancelSettled:
				book := taskCancelBookkeeping{quarantine: "sweep cancellation did not succeed"}
				if o.nonConformingAck {
					// H-1R2: the round trip completed but its result is outside the
					// CancelTaskResult contract. `cancel_failed` would claim an upstream
					// verdict nobody observed, so the status stays UNTOUCHED — the
					// quarantine, the CancelUnconfirmed flag and the ambiguous bar carry
					// the reconciliation demand.
					book.quarantine = "sweep cancellation returned a non-conforming acknowledgement; delivery unproven"
					return book
				}
				if o.state != DispatchUnknown {
					book.status = taskCancelFailedStatus
					book.statusReason = sweepCancelFailure(taskID, o).Error()
				}
				return book
			default:
				// refused / already-governed: nothing was dispatched and no record
				// state changed.
				return taskCancelBookkeeping{}
			}
		})
		if out.observationErr != nil {
			fail(fmt.Errorf("mcp: rs: sweep cancel of task %s: durable task observation did not record: %w",
				taskID, out.observationErr))
			continue
		}
		switch out.disposition {
		case taskCancelSuppressed:
			if out.bar == taskCancelBarDelivered {
				// The steady state after a successful cancellation: the request
				// was already delivered for THIS generation and the frozen law
				// forbids re-emitting it. Not a fault — the task stays visible
				// until an upstream report confirms its terminal status.
				continue
			}
			fail(fmt.Errorf("mcp: rs: sweep cancel of task %s suppressed (%s); reconciliation required", taskID, out.suppressed))
			rs.taskLedger.markQuarantine(taskID, generation, "sweep cancellation suppressed: "+out.suppressed)
			continue
		case taskCancelRefused:
			fail(fmt.Errorf("mcp: rs: sweep cancel of task %s: evidence refused (upstream cancel not issued)", taskID))
			continue
		case taskCancelAlreadyGoverned:
			// The exact attempt operation is already claimed: a claim never
			// re-forwards. Nothing to do; the intent bars further attempts.
			continue
		case taskCancelUnsettled:
			fail(fmt.Errorf("mcp: rs: sweep cancel of task %s: outcome did not record durably", taskID))
			continue
		}
		if !out.succeeded {
			ferr := sweepCancelFailure(taskID, out)
			fail(ferr)
			// The record transitions that keep the task visible ran inside the
			// atomic settlement above; only the reporting happens here.
			rs.auditTaskRecord(ctx, rec, false,
				strings.TrimSpace(reason)+"; upstream tasks/cancel did not succeed: "+ferr.Error(), "", "MCP07", "")
			continue
		}
		// The counter reports cancellations successfully REQUESTED.
		canceled++
	}
	return canceled, firstErr
}

// sweepCancelFailure renders the stable error of one unsuccessful sweep
// cancellation (the upstream's own error when it produced one).
func sweepCancelFailure(taskID string, out taskCancelOutcome) error {
	if out.nonConformingAck {
		return fmt.Errorf("mcp: rs: sweep cancel of task %s completed a round trip whose result is not a conforming "+
			"CancelTaskResult acknowledgement; delivery of the cancellation is unproven", taskID)
	}
	if out.forwardErr != nil {
		return out.forwardErr
	}
	return fmt.Errorf("mcp: rs: sweep cancel of task %s settled %s without a successful upstream cancellation", taskID, out.state)
}

// cancelAckViolation is THE acknowledgement-conformity decision of every
// tasks/cancel emission in this package (stage-7 contrasts H-1R2/H-1R3). Two
// facts used to be collapsed into one boolean: "the transport returned a
// relayable completed round trip" and "what it returned is an acknowledgement
// the Tasks extension contract admits". The first is the transport's verdict;
// only the SECOND may become `acked`, `succeeded`, a
// `cancel_requested`/`canceled` status, the `delivered` bar — or the exported
// client's nil. It returns "" for a conforming acknowledgement, else the
// claim-accurate description of the OBSERVED violation (M-1R2: never a literal
// the body may not carry).
//
// ALL THREE emitters consume this one decision, so no surface can convert
// transport into contract on its own:
//
//   - the client handler, handleTaskCancel (via conformingCancelAck);
//   - the server-initiated actuator, dispatchAnchoredCancel (via
//     conformingCancelAck) — the kill-switch sweep, the created-task
//     compensation and the revoked-tool compensation;
//   - the EXPORTED Client.TaskCancel (tasks.go), whose public success return is
//     an acknowledgement claim to an external consumer (r3 contrast, H-1R3).
//
// The refusal classes it reads are the stage-7 classification under the cancel
// authority profile: the forbidden core input_required discriminator
// (unsanctionedCore) and the duplicated discriminator (ambiguousDiscriminator).
// (The full CancelTaskResult ack-shape validator — the strictTaskUpdateAck
// analog — remains a declared residual of the r2-review blast-radius list;
// this decision is deliberately the shared SEAM that validator will slot into.)
func cancelAckViolation(result json.RawMessage) string {
	cls := classifyMRTRResult(result, mrtrAuthorityTaskCancel)
	switch {
	case cls.unsanctionedCore:
		return "carries the core input_required discriminator, which the Tasks extension does not sanction on a " +
			"cancellation acknowledgement"
	case cls.ambiguousDiscriminator:
		return "carries a duplicated exact resultType discriminator (ambiguous by construction)"
	}
	return ""
}

// conformingCancelAck is the boolean face of cancelAckViolation for the two
// ResourceServer emitters, which additionally require the transport fact.
func conformingCancelAck(relayed bool, result json.RawMessage) bool {
	return relayed && cancelAckViolation(result) == ""
}

// taskCancelDisposition classifies one anchorAndCancelTask run.
type taskCancelDisposition string

const (
	// taskCancelRefused: the claim or its fence refused — the upstream cancel
	// was NOT called.
	taskCancelRefused taskCancelDisposition = "refused"
	// taskCancelAlreadyGoverned: an exact replay of the (deterministic)
	// compensation operation — a claim never re-forwards.
	taskCancelAlreadyGoverned taskCancelDisposition = "already-governed"
	// taskCancelSettled: the cancel was dispatched and its honest outcome
	// recorded durably (state carries the classification).
	taskCancelSettled taskCancelDisposition = "settled"
	// taskCancelUnsettled: the cancel was dispatched but the settlement did NOT
	// record — the operation stays claimed/ambiguous.
	taskCancelUnsettled taskCancelDisposition = "unsettled"
	// taskCancelSuppressed: the ledger's per-task cancellation intent refused to
	// mint an attempt — another attempt is in flight, or a previous one ended
	// ambiguously/delivered. NOTHING was claimed and nothing was dispatched
	// (round-1 F-01).
	taskCancelSuppressed taskCancelDisposition = "suppressed"
)

// taskCancelOutcome is the result of one evidence-gated upstream task cancel.
type taskCancelOutcome struct {
	disposition taskCancelDisposition
	rec         GateRecord
	state       DispatchState // settled dispatch state (settled/unsettled only)
	// resultDigest commits to the upstream acknowledgement without retaining its
	// body. It is carried to the DurableTaskStore before the process cache is
	// allowed to reflect the cancellation outcome.
	resultDigest string
	// observationErr means the upstream/evidence outcome was obtained but its
	// durable task observation could not be recorded. Callers must not report the
	// cancellation as successfully governed in that case.
	observationErr error
	// succeeded reports that the UPSTREAM CANCELLATION ITSELF succeeded: a
	// relayable completed round trip whose result is a CONFORMING acknowledgement
	// (conformingCancelAck). Round-1 F-02: the first implementation discarded the
	// classifier's relay verdict and treated `state == completed` as success, so a
	// strictly valid upstream JSON-RPC error ("task cannot be canceled") marked
	// the task locally canceled and hid a live task from every future sweep.
	// Stage-7 H-1R2 is the same lesson one layer up: a relayable round trip whose
	// BODY the extension forbids is not a success either — "completed" describes
	// an observed protocol round trip, never a successful actuation, and `relay`
	// alone never proves an acknowledgement.
	succeeded bool
	// nonConformingAck marks the H-1R2 case apart from an ordinary failure: the
	// round trip completed and was relayable, but its result is outside the
	// CancelTaskResult contract. Callers use it to keep the record's status
	// UNTOUCHED (writing cancel_failed would claim an upstream verdict nobody
	// observed) while still reporting the fault.
	nonConformingAck bool
	attempt          uint64
	bar              taskCancelBar // why the intent suppressed the attempt (disposition == suppressed)
	suppressed       string        // human-readable form of bar
	forwardErr       error
}

// note renders the stable audit phrase for one cancel outcome.
func (o taskCancelOutcome) note() string {
	if o.observationErr != nil {
		return "upstream cancellation outcome observed but its durable task observation did NOT record"
	}
	switch o.disposition {
	case taskCancelSuppressed:
		return "suppressed (" + o.suppressed + "); upstream cancel NOT issued"
	case taskCancelRefused:
		return "evidence refused; upstream cancel NOT issued"
	case taskCancelAlreadyGoverned:
		return "already governed by an existing claim; never re-forwarded"
	case taskCancelUnsettled:
		return "dispatched but the outcome did NOT record durably"
	case taskCancelSettled:
		if o.succeeded {
			return "settled completed (upstream accepted the cancellation)"
		}
		if o.nonConformingAck {
			return "settled completed but the result is NOT a conforming CancelTaskResult acknowledgement; " +
				"delivery of the cancellation is unproven"
		}
		return "settled " + string(o.state) + " WITHOUT a successful upstream cancellation"
	default:
		return string(o.disposition)
	}
}

// fixedTaskCancelBinding adapts a deterministic (attempt-independent) binding —
// every compensation — to the attempt-parameterized binder anchorAndCancelTask
// takes for the sweep.
func fixedTaskCancelBinding(binding sdk.EvidenceBinding) func(uint64) sdk.EvidenceBinding {
	return func(uint64) sdk.EvidenceBinding { return binding }
}

// anchorAndCancelTask is the ONLY path to the upstream tasks/cancel actuator
// (per-site plan: the shared cancel helper must be impossible to call without a
// fresh, fenced claim — the historical free cancelTaskUpstream is gone):
//
//	reserve the per-task cancellation intent (F-01: one in-flight attempt, no
//	automatic re-attempt after an ambiguous or delivered outcome)
//	  → claim+anchor the attempt's binding → leadership fence
//	  → forward the cancel with the operation identity → settle the honest
//	    classification → release the intent with the retry verdict.
//
// A refused claim/fence NEVER reaches the upstream; an exact replay never
// re-forwards; a suppressed attempt never even claims.
// The reservation, the attempt identity, the pre-forward revalidation and the
// retry verdict are all scoped to the record GENERATION (round-2 N-03), never to
// the textual task id: a stale bar must not suppress the cancellation of a
// REPLACEMENT task, and a stale caller must not cancel one.
// `book` is the caller's DECLARATIVE record-state policy for the settled attempt
// (round-4 R4-01): it is computed from the outcome OUTSIDE the ledger mutex and
// then applied ATOMICALLY with the bar, the end of the reservation and the
// release of the generation pin. No caller may perform those mutations itself —
// round-3's separate markCancelRequested/updateStatus/markQuarantine calls ran
// AFTER the pin had been released, which is exactly the TTL window R4-01 names.
// `book` is never invoked for a SUPPRESSED attempt: nothing was reserved, nothing
// was pinned and nothing was dispatched, so there is no settlement to apply.
func (rs *ResourceServer) anchorAndCancelTask(ctx context.Context, dec ToolDecision, bind func(attempt uint64) sdk.EvidenceBinding, taskID, generation, subject, trace string, book func(taskCancelOutcome) taskCancelBookkeeping) taskCancelOutcome {
	res := rs.taskLedger.beginCancelAttempt(taskID, generation)
	if !res.ok {
		return taskCancelOutcome{disposition: taskCancelSuppressed, bar: res.bar, suppressed: res.reason}
	}
	// ROUND-4 R4-06: installed IMMEDIATELY after the successful reservation, before
	// anything that can panic. It never recovers — it only makes the ledger state
	// deny-closed and lets the panic keep propagating.
	guard := &cancelAttemptGuard{ledger: rs.taskLedger, taskID: taskID, generation: generation}
	defer guard.abandon()

	out := rs.dispatchAnchoredCancel(ctx, dec, bind(res.attempt), taskID, generation, subject, trace, guard)
	out.attempt = res.attempt
	bookkeeping := taskCancelBookkeeping{}
	if book != nil {
		bookkeeping = book(out)
	}
	// A NEW attempt identity is permissible only when this one provably emitted
	// nothing (claim/fence refusal) or durably settled a state that proves the
	// request never reached the upstream (settledStatePermitsCancelRetry: not_sent,
	// or blocked — pre-dispatch by contract since the stage-7 vocabulary split).
	// `withheld` (the dispatch ran, only the response was withheld), `unknown`, an
	// unsettled outcome, a delivered cancellation and a completed-with-error round
	// trip all BAR automatic re-attempts.
	settlement := taskCancelSettlement{
		taskCancelBookkeeping: bookkeeping,
		dispatched:            out.disposition == taskCancelSettled || out.disposition == taskCancelUnsettled,
		acked:                 out.succeeded,
		retryable: out.disposition == taskCancelRefused ||
			(out.disposition == taskCancelSettled &&
				settledStatePermitsCancelRetry(out.state)),
		bar:       taskCancelBarAmbiguous,
		barReason: "previous cancellation attempt " + out.note(),
	}
	// The durable task binding is the source of truth. Record every dispatched
	// server-initiated cancellation outcome before applying its process-local
	// bookkeeping. A failed write cannot undo an upstream effect, so the local
	// intent stays barred and cancellation-unconfirmed, but its status is left
	// untouched and the row is quarantined for reconciliation.
	if settlement.dispatched {
		if task, ok := rs.taskLedger.lookup(taskID); ok && task.Generation == generation &&
			task.DurableRef.Generation > 0 {
			effectDispatched := out.state != DispatchNotSent && out.state != DispatchBlocked
			status := bookkeeping.status
			if status == "" {
				status = task.Status
			}
			reason := bookkeeping.statusReason
			if reason == "" {
				reason = out.note()
			}
			verdict := DurableTaskVerdictUnobservable
			switch {
			case out.succeeded:
				verdict = DurableTaskVerdictClean
			case out.nonConformingAck ||
				(out.disposition == taskCancelSettled && out.state != DispatchUnknown && out.state != DispatchBlocked):
				verdict = DurableTaskVerdictBroken
			}
			if err := rs.persistDurableTaskObservation(ctx, task, DurableTaskObservation{
				Kind: DurableTaskObservationCancel, Verdict: verdict,
				Status: status, StatusReason: reason,
				ResultDigest: out.resultDigest,
				OperationID:  string(out.rec.Binding.OperationID),
				Dispatched:   effectDispatched, Acknowledged: out.succeeded,
				CancelRequested: effectDispatched,
			}); err != nil {
				out.observationErr = err
				settlement.taskCancelBookkeeping = taskCancelBookkeeping{
					quarantine: "durable cancellation observation could not be recorded; reconciliation required",
				}
			}
		}
	}
	guard.settle(settlement)
	return out
}

// dispatchAnchoredCancel runs the claim → fence → forward → settle lifecycle of
// one reserved cancellation attempt.
func (rs *ResourceServer) dispatchAnchoredCancel(ctx context.Context, dec ToolDecision, binding sdk.EvidenceBinding, taskID, generation, subject, trace string, guard *cancelAttemptGuard) taskCancelOutcome {
	rec := rs.auditor.Record(ctx, dec, binding)
	if !rec.MayEmit(binding) {
		if rec.State == GateRecordReplayPending || rec.State == GateRecordReplaySettled {
			return taskCancelOutcome{disposition: taskCancelAlreadyGoverned, rec: rec}
		}
		return taskCancelOutcome{disposition: taskCancelRefused, rec: rec}
	}
	if fence := rs.auditor.BeforeEffect(ctx, rec); fence.MustRefuse(binding) {
		return taskCancelOutcome{disposition: taskCancelRefused, rec: rec}
	}
	// Round-2 N-03 + round-3 R3-03: acquire the generation LEASE immediately
	// before the external call. If the record that holds this identifier changed
	// while the claim was being anchored, the cancellation would target a task
	// nobody authorized canceling. Nothing was transmitted, so the claim is
	// settled `blocked` — an honest, durable "stopped before the effect" — and the
	// attempt stays retryable for the generation that actually holds the id. The
	// lease then HOLDS that identity across the upstream call: a server-initiated
	// cancellation is as exposed to the evict-and-replace race as a client method,
	// and it carries the textual task id on the wire just the same.
	if verr := rs.taskLedger.acquireEffectLease(taskID, generation, taskEffectServerCancel); verr != nil {
		rs.auditor.Settle(ctx, GateOutcome{Record: rec, State: DispatchBlocked})
		return taskCancelOutcome{
			disposition: taskCancelSettled, rec: rec, state: DispatchBlocked, forwardErr: verr,
		}
	}
	// ROUND-4 R4-01: the pin is handed to the attempt guard and released inside
	// settleCancelAttempt, together with the cancellation-unconfirmed state.
	// Round-3's `defer releaseEffectLease` here unpinned the record BEFORE the
	// caller had recorded the delivered bar or the `cancel_requested` status, so a
	// TTL eviction in that window deleted a still-`working` record whose
	// cancellation had already reached the upstream.
	guard.pin()
	var res UpstreamResult
	params, ferr := json.Marshal(struct {
		TaskID string `json:"taskId"`
	}{TaskID: taskID})
	if ferr != nil {
		// Unreachable for a string field; classified honestly as not_sent
		// (nothing touched the transport) and settled below.
		res = UpstreamResult{State: DispatchNotSent}
	} else {
		res, ferr = rs.upstream.Forward(ctx, UpstreamRequest{
			Method:       methodTasksCancel,
			Params:       params,
			Subject:      subject,
			TraceParent:  trace,
			OperationID:  binding.OperationID,
			EffectDigest: binding.EffectDigest,
			FenceToken:   rec.FenceToken,
		})
	}
	// The classifier's relay verdict is CARRIED, not discarded (F-02).
	state, relay := classifyDispatch(res, ferr)
	digest := resultDigest(res.Result)
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: rec, State: state,
		ResultDigest: digest, DispatchRef: res.DispatchRef,
	})
	if settlement.FailureClass != sdk.FailureNone {
		return taskCancelOutcome{
			disposition: taskCancelUnsettled, rec: rec, state: state,
			resultDigest: digest, forwardErr: ferr,
		}
	}
	// H-1R2: `relay` is the transport verdict; SUCCESS is the shared
	// acknowledgement-conformity predicate — the same one the client handler
	// consumes — so a sweep/compensation/revocation cancel answered with a
	// variant the extension forbids is never recorded as delivered: acked stays
	// false (the ledger keeps the ambiguous bar, not `delivered`), no status
	// advances, and the caller reports the fault instead of a success.
	acked := conformingCancelAck(relay, res.Result)
	return taskCancelOutcome{
		disposition: taskCancelSettled, rec: rec, state: state,
		resultDigest: digest, succeeded: acked,
		nonConformingAck: relay && !acked, forwardErr: ferr,
	}
}

func isTaskMethod(method string) bool {
	switch method {
	case methodTasksGet, methodTasksUpdate, methodTasksCancel:
		return true
	default:
		return false
	}
}

// syncTaskStatusFromResult applies the upstream's authoritative task status to
// the local record. Round-1 F-10: the status is read from the STRICT tree
// with exact casing, so a result carrying both `status:"working"` and
// `Status:"canceled"` can no longer make the local record terminal (and
// unsweepable) while an exact-casing client still sees a live task. Any
// ambiguity leaves the record untouched — the safe direction.
//
// ROUND-5 R5-01: the gate is now the COMPLETE `GetTaskResult` validation
// (strictGetTaskResult), not a bare status read. The validation error is
// RETURNED so a caller can distinguish "the upstream reported a non-terminal
// status" (nothing to do) from "the upstream answered with something that is not
// a conforming task report" (an upstream protocol fault the operator must see).
// Only the second may never be treated as proof, and the record alone cannot
// tell them apart.
// ROUND-8: it returns the validated REPORT as well, because two of its facts have
// to reach the caller. The report's canonical digest is what an owner's collection
// proof is bound to (R8-02), and its `ttlMs` is the current effective retention the
// ledger applies (R8-04) — both were being validated and dropped.
func (rs *ResourceServer) syncTaskStatusFromResult(rec TaskRecord, result json.RawMessage) (TaskRecord, bool, taskReport, error) {
	rep, err := strictGetTaskResult(rec.TaskID, result)
	if err != nil {
		return TaskRecord{}, false, taskReport{}, err
	}
	updated, applied := rs.applyTaskReport(rec, rep)
	return updated, applied, rep, nil
}

func (rs *ResourceServer) applyTaskReport(rec TaskRecord, rep taskReport) (TaskRecord, bool) {
	// This is the ONLY authoritative confirmation path (round-2 N-02): the status
	// came from the upstream's own report about this exact task, so a terminal
	// value here — and only here — may retire the record from reconciliation and
	// from future sweeps. The write is a compare-and-swap on the generation.
	updated, uok := rs.taskLedger.confirmStatus(rec.TaskID, rec.Generation, rep)
	if uok && taskStatusTerminal(updated.Status) && updated.Reconciling {
		// A reconciliation artifact whose terminal status is now CONFIRMED can
		// finally be forgotten — through the ONE compare-and-delete predicate the
		// operator retirement uses (round-8 R8-01). It is deliberately allowed to
		// refuse: an artifact whose handle its OWNER holds still owes that owner its
		// result, and this shortcut used to delete it anyway.
		rs.taskLedger.release(rec.TaskID, rec.Generation)
	}
	return updated, uok
}

// mrtrReleasePlan is the PURE classification of one upstream result UNDER ITS
// CALLER-SELECTED AUTHORITY PROFILE (design adjudication §2): whether it
// carries MRTR input requests that must be mediated before release, and — when
// it does — the exact response-release CHILD binding that mediation will claim.
//
// Review round 1 (S5-06): it exists so the PARENT settlement can NAME the
// child it authorizes. Every input is the result, the parent binding and the
// caller's authority profile, so the plan is computed BEFORE the settlement and
// the mediator is consulted after it; the classification never depends on the
// mediator's verdict — and never on incidental body shape deciding its own
// contract (the adjudicated premise correction).
//
// The naming is PROSPECTIVE, exactly like the three direct mediated sites.
// Stage-7 B-bis round 2 closed the stage-5 residual this used to carry: a
// mediator deny (or an unreadable/ungovernable result) after the settlement no
// longer leaves the named child unanchored — the call site claims it and
// settles it `withheld` (settleWithheldRelease), so the disposition of every
// observed governed result has its own durable record.
type mrtrReleasePlan struct {
	entries map[string]json.RawMessage
	// mediate is true when at least one entry carries inspectable content AND a
	// mediator is wired — the condition under which the MEDIATOR IS CONSULTED,
	// and nothing else. The release child is claimed for the CLASSIFICATION
	// (`inputRequired`), never for the mediation intent: gating the child on this
	// field was the r2-review P0-2 evidence bypass (zero inspections read as zero
	// evidence), closed by 1df55da6 on tools/call, ui:// and the task surfaces.
	mediate bool
	// unreadable is true when the exact governed member the selected contract
	// names is PRESENT but cannot be projected (an `inputRequests` that is not
	// the pin's map, schema.ts:553-555). The result is then never released —
	// the retained projection-integrity refusal (adjudication §4). It says
	// nothing about extension members or hypothetical non-conforming decoders.
	unreadable bool
	// noAuthority is true when the caller supplied NO valid authority profile —
	// an internal bug, not a classification. There is no body-only fallback:
	// the call site withholds the raw result behind a fixed
	// evidence-unavailable response (adjudication §5).
	noAuthority bool
	// unsanctionedCore is true when the result carries the CORE input-required
	// discriminator — a single exact string — under a profile whose contract
	// forbids it (every Tasks profile — stage-7 P0-3). ambiguousDiscriminator is
	// the sibling refusal (M-1R2): the exact discriminator key is DUPLICATED,
	// whatever the value types, and the record states THAT — never a literal the
	// body may not carry. For both, the call site refuses with 502/-31002 and the
	// controlled reason class coreRefusal() names; nothing is mediated and
	// nothing is released, but the child binding IS derived, because the refusal
	// of an OBSERVED result is a release disposition (settled `withheld`).
	unsanctionedCore       bool
	ambiguousDiscriminator bool
	// inputRequired is the classification itself, kept separate from `mediate`:
	// a requestState-only input_required result IS an MRTR variant with nothing
	// to inspect, and a route that must decide "is this the MRTR contract or my
	// own?" needs the classification, not the mediation intent.
	inputRequired bool
	class         string
	release       sdk.EvidenceBinding
}

// coreRefusal reports whether the plan refused the WHOLE result at the
// discriminator level — before projection or mediation — and, when it did, the
// OBSERVED fact for the audit record (never a literal the body may not carry,
// M-1R2), the controlled reason class and the client-facing message.
func (p mrtrReleasePlan) coreRefusal() (observed, reason, wire string, refused bool) {
	switch {
	case p.unsanctionedCore:
		return "the CORE input_required discriminator, which this method's contract does not sanction",
			mrtrReasonUnsanctionedOrigin, mrtrUnsanctionedWireMessage, true
	case p.ambiguousDiscriminator:
		return "a DUPLICATED exact resultType discriminator — ambiguous by construction; no conforming server produces it",
			mrtrReasonMalformedResult, mrtrAmbiguousDiscriminatorWireMessage, true
	}
	return "", "", "", false
}

// planMRTRRelease classifies one upstream result under the caller-selected
// authority profile and derives the release child binding its release will
// claim. The profile is REQUIRED: there is no default, and the invalid zero
// value plans nothing.
//
// TWO INDEPENDENT QUESTIONS, deliberately not one boolean (Codex r2 review
// 2026-07-29, P0-1/P0-2):
//
//   - `mediate` — is there CONTENT for the mediator to inspect? Nil mediator,
//     zero entries or entries with no projectable payload all answer no.
//   - `release` — are governed bytes about to leave? Any classified
//     InputRequiredResult answers YES, including a requestState-only result and
//     a valid empty `inputRequests:{}`.
//
// Collapsing them let `mediate=false` authorize a PLAIN write. Measured, that
// was a real evidence-or-refuse bypass: with a release journal that refuses
// every `mcp.release.*` action, a requestState-only result still went out 200
// with the server-controlled opaque state in the body, because no child was
// ever derived for the journal to refuse. `entries==0` decides "zero
// inspections", never "zero evidence".
//
// What did NOT change, and why: a nil mediator is still a clean pass for
// CONTENT. That is the documented open-core boundary, not an oversight —
// cmd/olivares/wire_noenterprise.go:362-375 returns nil for both the render
// inspector and the elicitation mediator, handleElicitation and handleSampling
// skip their mediation blocks the same way (rs.go:3764), and the comment
// names the intent: "the surface.go detective still inventories the capability
// advertisement (no rug-pull)". Making MRTR the ONE surface that refuses on the
// default artifact would break a conforming exchange the specification permits
// and would remove a capability rather than govern it. The review asked for a
// 503 there; the honest answer is that the missing property was EVIDENCE, and
// evidence is what this now derives — a community build still relays, but a
// refusing or unavailable release journal now withholds, on every route that
// owns a parent claim.
func (rs *ResourceServer) planMRTRRelease(result json.RawMessage, parent sdk.EvidenceBinding, class string, authority mrtrAuthority) mrtrReleasePlan {
	plan := mrtrReleasePlan{class: class}
	if !authority.valid() {
		plan.noAuthority = true
		return plan
	}
	cls := classifyMRTRResult(result, authority)
	entries, unreadable := cls.entries, cls.unreadable
	plan.entries, plan.unreadable, plan.inputRequired = entries, unreadable, cls.inputRequired
	plan.unsanctionedCore = cls.unsanctionedCore
	plan.ambiguousDiscriminator = cls.ambiguousDiscriminator
	if cls.unsanctionedCore || cls.ambiguousDiscriminator {
		// The upstream sent the forbidden core variant (stage-7 P0-3) or a
		// duplicated discriminator no conforming server produces (M-1R2). Refused
		// outright: nothing is projected, nothing is mediated, nothing is
		// released. The child binding is still derived for the same reason the
		// unreadable refusal derives one — the bytes were OBSERVED, so their
		// retention is a release disposition the parent settlement names and the
		// call site settles `withheld`.
		plan.entries = nil
		plan.release = deriveResponseReleaseBinding(rs.tenant, parent, class, resultDigest(result))
		return plan
	}
	if unreadable {
		// Refused outright by the caller: nothing is released. The child binding
		// is still derived (round 2, F-1): the governed contract selected this
		// result, so its refusal is a release DISPOSITION — the call site settles
		// the child `withheld`, and the parent settlement names it.
		plan.entries = nil
		plan.release = deriveResponseReleaseBinding(rs.tenant, parent, class, resultDigest(result))
		return plan
	}
	if rs.elicitationMediator == nil || len(entries) == 0 {
		plan.entries = nil
	} else {
		for _, raw := range entries {
			if len(extractMRTRPayloadContent(raw)) > 0 {
				plan.mediate = true
				break
			}
		}
	}
	if plan.inputRequired {
		plan.release = deriveResponseReleaseBinding(rs.tenant, parent, class, resultDigest(result))
	}
	return plan
}

// mediateMRTRResultEntries mediates the MRTR input-request payloads an upstream
// RESULT carries, before that result is released to the caller. The entries are
// the EXACT-CASED members the strict classification extracted (review round
// 1, S5-02) — never a case-folding re-parse of the result bytes.
//
// Round-3 ordering (stage-7 B-bis): on a hijack verdict, settleChild — the
// caller's withheld release-child settlement — runs BEFORE the deny is written:
// the withheld child must be durable before the refusal leaves the process,
// exactly as the parent's dispatch fact is durable before any verdict. The
// historical shape wrote the deny inside the mediation machinery first, so a
// crash between the accepted deny bytes and the settlement left an observed,
// retained payload with no durable disposition.
func (rs *ResourceServer) mediateMRTRResultEntries(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, entries map[string]json.RawMessage, trace string, settleChild func()) bool {
	if rs.elicitationMediator == nil {
		return false
	}
	verdict, reason := rs.evaluateMRTREntries(ctx, tok, ChannelMRTRInputRequest, entries, trace)
	if verdict == mediationPass {
		return false
	}
	if settleChild != nil {
		settleChild()
	}
	rs.writeMediationDeny(ctx, w, req, tok, verdict, reason, ChannelMRTRInputRequest, trace)
	return true
}

// mediateMRTRInputResponses mediates the MRTR answers a request carries. The
// entries are the EXACT-CASED members the strict canonicalization extracted
// (round-1 F-07) — the PEP never re-parses the forwarded bytes with a
// case-folding decoder, so it can no longer authorize a different logical member
// than the actuator consumes. This REQUEST-side mediation is part of the
// authorization surface and stays pre-claim (stage-4 placement); only the
// hijack verdict matters to its callers.
func (rs *ResourceServer) mediateMRTRInputResponses(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, entries map[string]json.RawMessage, trace string) bool {
	if rs.elicitationMediator == nil {
		return false
	}
	return rs.mediateMRTREntries(ctx, w, req, tok, ChannelMRTRInputResponse, entries, trace)
}

func (rs *ResourceServer) mediateMRTREntries(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, channel string, entries map[string]json.RawMessage, trace string) (hijacked bool) {
	verdict, reason := rs.evaluateMRTREntries(ctx, tok, channel, entries, trace)
	if verdict == mediationPass {
		return false
	}
	rs.writeMediationDeny(ctx, w, req, tok, verdict, reason, channel, trace)
	return true
}

// evaluateMRTREntries mediates the MRTR payloads of one channel and returns the
// FIRST non-pass verdict (HITL gate consultation included) WITHOUT writing
// anything, so a result-side caller can make its withheld release child durable
// between the verdict and the deny response (round-3 settle-then-write).
func (rs *ResourceServer) evaluateMRTREntries(ctx context.Context, tok validatedToken, channel string, entries map[string]json.RawMessage, trace string) (mediationVerdict, string) {
	if len(entries) == 0 {
		return mediationPass, ""
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		content := extractMRTRPayloadContent(entries[id])
		if len(content) == 0 {
			continue
		}
		dec := rs.elicitationMediator.Mediate(ctx, ElicitationInspectionInput{
			Tenant:      rs.tenant,
			Subject:     tok.Subject,
			Channel:     channel,
			Content:     content,
			ContentHash: contentSHA256(content),
			TraceParent: trace,
		})
		rs.publishElicitationFindings(ctx, tok, dec.Findings, trace)
		rs.emitElicitationMeter(dec.Meter)
		if verdict := rs.evaluateElicitationVerdict(ctx, tok, dec, channel); verdict != mediationPass {
			return verdict, dec.Reason
		}
	}
	return mediationPass, ""
}

func (rs *ResourceServer) auditTaskRecordTraced(ctx context.Context, tok validatedToken, rec TaskRecord, allowed bool, reason, approvalRef, mcpTag, trace string) {
	subject := tok.Subject
	if subject == "" {
		subject = rec.Subject
	}
	tenant := rec.Tenant
	if tenant == "" {
		tenant = rs.tenant
	}
	rs.auditor.Record(ctx, ToolDecision{
		Tenant: tenant, Subject: subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: rec.Tool, RequiredScope: rec.RequiredScope,
		Allowed: allowed, Reason: reason, ApprovalRef: approvalRef, TaskID: rec.TaskID,
		MCPTag: mcpTag, TokenBinding: tok.Binding, TraceParent: trace, At: rs.clock(),
	}, sdk.EvidenceBinding{}) // best-effort DENIAL evidence (task-surface allows are claims now)
}

func (rs *ResourceServer) auditTaskRecord(ctx context.Context, rec TaskRecord, allowed bool, reason, approvalRef, mcpTag, trace string) {
	tenant := rec.Tenant
	if tenant == "" {
		tenant = rs.tenant
	}
	rs.auditor.Record(ctx, ToolDecision{
		Tenant: tenant, Subject: rec.Subject, IsDelegated: rec.IsDelegated, ActAs: rec.ActAs,
		Tool: rec.Tool, RequiredScope: rec.RequiredScope,
		Allowed: allowed, Reason: reason, ApprovalRef: approvalRef, TaskID: rec.TaskID,
		MCPTag: mcpTag, TraceParent: trace, At: rs.clock(),
	}, sdk.EvidenceBinding{}) // best-effort DENIAL evidence (task-surface allows are claims now)
}

func (rs *ResourceServer) auditTaskTraced(ctx context.Context, tok validatedToken, tool, scope, taskID string, allowed bool, reason, approvalRef, mcpTag, trace string) {
	rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: tool, RequiredScope: scope,
		Allowed: allowed, Reason: reason, ApprovalRef: approvalRef, TaskID: taskID,
		MCPTag: mcpTag, TokenBinding: tok.Binding, TraceParent: trace, At: rs.clock(),
	}, sdk.EvidenceBinding{}) // best-effort DENIAL evidence (task-surface allows are claims now)
}

// handleUIRead is the MCP Apps render gate (SEP-1865): a resources/read
// whose uri carries the reserved ui:// scheme fetches an App TEMPLATE the host
// will render sandboxed — an actuation surface, not a passive document. It is
// gated like tools/call: the server-owned pre-declared inventory (appSet) is
// deny-by-default (an undeclared template — or ANY template when no inventory
// is configured — is refused), a require_consent template renders only with a
// consent RECORDED for (subject, template) in the ConsentStore seam, and every
// decision is audited — the auditable trail for the postMessage surface (the
// render fetch is the moment the iframe channel comes into existence).
// Returns false for a non-ui:// read (the generic admitted-forward path owns it).
//
// (stage 5) + stage-7 B-bis round 2: the render is EVIDENCE-ENFORCED. The
// fetch is claimed and leadership-fenced BEFORE the upstream call; an observed
// round trip settles the parent COMPLETED immediately — the dispatch fact,
// naming the response-release child. The content inspection then decides the
// child's disposition: an inspector deny (or an uninspectable render) settles
// the RELEASE CHILD `withheld` (durable evidence that the observed bytes never
// left governance); an allow writes the response ONLY against an anchored
// release claim. The historical post-hoc "authorized; upstream forward failed"
// audit is gone: upstream failures are settlements.
func (rs *ResourceServer) handleUIRead(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) bool {
	trace := requestTraceParent(r, req.Params)

	// STRICT canonicalization is the first thing that touches the params (the
	// stage-3/4 discipline): the uri the gate authorizes is the EXACT-CASED "uri"
	// member of the strict tree — never a case-insensitive struct unmarshal — and
	// the bytes forwarded are the canonical bytes the EffectDigest binds.
	canon, cerr := canonicalizeMediatedParams(req.Params, uiReadReservedKeys)
	uri := ""
	if cerr == nil && canon.Presence == paramsPresent {
		uri = canon.stringMember("uri")
	}

	// Smuggling guard (fail-closed, the header↔body-consistency posture): when
	// the RAW params mention the reserved scheme but the STRICT tree does not
	// yield a clean ui:// uri — malformed params, a duplicate "uri" key at any
	// depth (Go keeps the LAST occurrence; an upstream keeping the FIRST would
	// serve the smuggled template ungated), a case-variant alias of a reserved
	// key, a whitespace-padded uri (the gate would resolve one spelling while the
	// forwarded bytes carry another), or the scheme hidden in another field — the
	// request is ambiguous and is REFUSED rather than forwarded around the gate.
	if cerr != nil || uri != strings.TrimSpace(uri) || !hasUIScheme(uri) {
		if rawMentionsUIScheme(req.Params) {
			rs.auditUITraced(ctx, tok, uri, false, "ui-template deny (ambiguous params mention the ui:// scheme — smuggling guard)", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "ambiguous resources/read params referencing a ui:// template")
			return true
		}
		return false // a plain non-UI read: the generic admitted-forward path owns it
	}

	policy, ok := rs.apps.resolve(uri)
	if !ok {
		rs.auditUITraced(ctx, tok, uri, false, "ui-template deny (not in the pre-declared inventory)", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "ui template not in the pre-declared inventory")
		return true
	}
	consentGranted := false
	if policy.RequireConsent {
		granted, cserr := rs.consent.Granted(ctx, tok.Subject, uri)
		if cserr != nil {
			rs.auditUITraced(ctx, tok, uri, false, "ui-template deny (consent store error — fail-closed)", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "ui template consent check failed")
			return true
		}
		if !granted {
			rs.auditUITraced(ctx, tok, uri, false, "ui-template deny (no recorded user consent)", trace)
			rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied, "ui template requires recorded user consent")
			return true
		}
		consentGranted = true
	}

	// --- evidence enforcement: claim → fence → forward → inspect → settle →
	// anchored release → write (per-site reorder plan, UI row).
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		rs.auditUITraced(ctx, tok, uri, false, "operation-id derivation failed (fail-closed)", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return true
	}
	pd := mediatedPolicyDigest(releaseClassUI, policy.RequireConsent, consentGranted, rs.renderInspector != nil, false)
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveMediatedEffectDigest(rs.tenant, rs.resource, "resources/read", tok,
			"mcp.ui.template", uri, rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon, pd)),
	}
	claimRec := rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: uri, Allowed: true, Reason: "ui-template render authorized",
		TokenBinding: tok.Binding, TraceParent: trace,
		OperationIDKind: idKind, EffectAction: mediatedActionUIReadPrefix + idKind, At: rs.clock(),
	}, binding)
	if !claimRec.MayEmit(binding) {
		rs.refuseToolCallEvidence(w, req.ID, claimRec, opID)
		return true
	}
	if fence := rs.auditor.BeforeEffect(ctx, claimRec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return true
	}

	ureq := rs.upstreamReq(r, "resources/read", canon.Forward, tok)
	ureq.OperationID = binding.OperationID
	ureq.EffectDigest = binding.EffectDigest
	ureq.FenceToken = claimRec.FenceToken
	res, ferr := rs.upstream.Forward(ctx, ureq)
	state, relay := classifyDispatch(res, ferr)
	if !relay {
		settlement := rs.auditor.Settle(ctx, GateOutcome{
			Record: claimRec, State: state,
			ResultDigest: resultDigest(res.Result), DispatchRef: res.DispatchRef,
		})
		if settlement.FailureClass != sdk.FailureNone {
			rs.writeOperationIndeterminate(w, req.ID, opID)
			return true
		}
		if state == DispatchUnknown {
			rs.writeOperationIndeterminate(w, req.ID, opID)
		} else {
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, "upstream resource fetch failed")
		}
		return true
	}
	result := res.Result

	// MRTR classification comes FIRST, before the render gate (Codex 2026-07-29
	// §4.2, P0 finding 2). `resources/read` is one of the three requests the
	// specification sanctions for an InputRequiredResult (mrtr.mdx:184-192), and
	// a ui:// read is still a resources/read — so this route can receive one.
	//
	// The render gate below is not a check that happens to also cover it: it
	// reads `contents` looking for HTML, and an input_required result has no
	// `contents`, so extractHTMLFromResult returns nothing, the inspector is
	// never consulted, and the payload was released having "passed" an
	// inspection that never looked at it. That is the same bypass the generic
	// dispatch had, on the route that already owns the strongest evidence
	// machinery in the package — which is why the fix reuses that machinery
	// rather than adding a second one: the parent claim, fence and settlement
	// are the ones taken above; only the release CLASS and the inspector change.
	if uiMRTR := rs.planMRTRRelease(result, binding, releaseClassMRTRUIResource, mrtrAuthorityCoreResult); uiMRTR.inputRequired {
		return rs.releaseUIMRTRResult(ctx, w, req, tok, uri, opID, claimRec, res, uiMRTR, trace)
	}

	// Round 2 (F-1): the parent settles the DISPATCH FACT — completed, naming the
	// response-release child — IMMEDIATELY after the round trip is observed and
	// BEFORE any inspection verdict. An observed dispatch must be durable the
	// moment it is observed; the release disposition (released or withheld) is
	// the CHILD operation's record. A refused parent settlement withholds the
	// response behind the indeterminate shape: the dispatch outcome is not
	// durable, so neither a release nor a deny disposition may build on it.
	release := deriveResponseReleaseBinding(rs.tenant, binding, releaseClassUI, resultDigest(result))
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: claimRec, State: DispatchCompleted,
		ResultDigest: resultDigest(result), DispatchRef: res.DispatchRef,
		ReleaseBinding: release,
	})
	if settlement.FailureClass != sdk.FailureNone {
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return true
	}
	// uiWithheld records the deny disposition durably on the release child
	// (settled `withheld`); the refusal stands regardless of the journal's fate.
	uiWithheld := func() {
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, uri, "", releaseClassUI, "", trace),
			release, resultDigest(result), func(reason string) {
				rs.auditUITraced(ctx, tok, uri, false, reason, trace)
			})
	}

	// renderApprovalRef names the human approval that released a HITL render, so
	// the release child can carry it (empty when no HITL decision was involved).
	renderApprovalRef := ""

	// inspect the HTML content of the rendered template before it can be
	// released to the client. A nil inspector is a clean pass (the community
	// build injects nil; the render-gate + consent keep working).
	if rs.renderInspector != nil {
		htmlContent, ambiguous := extractHTMLFromResult(result)
		if ambiguous {
			// Review round 1 (S5-03, UI leg): the fetched render is one a strict
			// reader and a case-folding reader could read DIFFERENTLY (a duplicate or
			// case-variant alias of a member the gate interprets), so the inspector
			// cannot be shown the html the client would actually render. The render
			// never leaves governance: the release child settles WITHHELD.
			uiWithheld()
			rs.auditUITraced(ctx, tok, uri, false,
				"ui-template render WITHHELD: the upstream result is ambiguous to the render gate (duplicate or "+
					"case-variant alias of an interpreted member), so its content could not be inspected", trace)
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream returned an ambiguous ui template result")
			return true
		}
		if len(htmlContent) > 0 {
			hash := contentSHA256(htmlContent)
			idec := rs.renderInspector.InspectRender(ctx, RenderInspectionInput{
				Tenant:      rs.tenant,
				Subject:     tok.Subject,
				TemplateURI: uri,
				Content:     htmlContent,
				ContentHash: hash,
				TraceParent: trace,
			})
			rs.publishRenderFindings(ctx, tok, uri, idec.Findings, trace)
			rs.emitRenderMeter(idec.Meter)
			// CONTENT DENY FIRST — the content-firewall contract's precedence is
			// `deny > hitl`, and the three fields are independent: nothing stops an inspector answering HITL=true
			// with Allow=false + Reason. A deny that arrives with a HITL bit is
			// still a DENY, so it must never open an approval a human could
			// grant — the render would be refused for content afterwards anyway,
			// and the operator would have approved something already refused.
			//
			// ⚠ This is DELIBERATELY the opposite order to the elicitation
			// sibling (evaluateElicitationVerdict consults the gate first). The
			// Contrast refuted the claim that the sibling's order preserves
			// the contract: it does not — it yields "hitl until approved, THEN
			// deny". The sibling's order is left alone (its cells pin that
			// behavior and changing it is not this slice's call) and reported;
			// this leg implements what the contract actually says.
			if !idec.Allow && idec.Reason != "" {
				// Inspector deny after the observed fetch: the disposition is the
				// release child's — settled WITHHELD — never the parent's.
				uiWithheld()
				rs.auditUITraced(ctx, tok, uri, false,
					"ui-template render denied by content inspector: "+idec.Reason, trace)
				rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
					"ui template content denied by inspection")
				return true
			}
			// the `hitl` action of the firewall policy reaches the render
			// surface HERE — "bloquea + abre aprobación + finding", the wording the
			// content-firewall contract uses. Before this leg existed, RenderInspectionDecision had no HITL field at all, so an
			// inspector configured `hitl` for this channel could only express
			// itself as Allow=false + Reason and the render collapsed onto plain
			// `deny`: the approval was never opened and never could be.
			//
			// The render was already FETCHED when this runs (content cannot be
			// inspected before it is retrieved), so an unapproved render is not
			// "never forwarded" like a mediated request — it is WITHHELD, and
			// the disposition is the release CHILD's, never the parent's.
			if idec.HITL {
				ref, ok := rs.approvedForPlan(ctx, tok, ChannelRender,
					renderApprovalPlan(idec.ApprovalPlanHash, tok.Subject, uri, hash))
				if !ok {
					uiWithheld()
					rs.auditUITraced(ctx, tok, uri, false,
						"ui-template render WITHHELD: the content inspector requires human approval and no "+
							"approval is bound to THIS render's plan hash", trace)
					rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
						"ui template render requires human approval")
					return true
				}
				// Carried into the release child below so the delivery record
				// names the approval that authorized it.
				renderApprovalRef = ref
			}
		}
	}

	// Allow: anchor the exact release binding the parent settlement named, then
	// write. A refused release anchor withholds the response (the design matrix
	// row "Response-release anchor failure").
	//
	// The release decision NAMES the approval when a HITL decision authorized it
	// (contrast, P1): a delivery record that cannot say which human approval
	// released the bytes is durable but not attributable, and tools/call has kept
	// that reference since (rs.go:637-641, :679).
	relDec := rs.releaseDecision(tok, uri, "", releaseClassUI, "", trace)
	relDec.ApprovalRef = renderApprovalRef
	rel, rok := rs.anchorResponseRelease(ctx, w, req.ID, relDec,
		release, opID, func(reason string) {
			rs.auditUITraced(ctx, tok, uri, false, reason, trace)
		})
	if !rok {
		return true
	}
	_ = rel.write(ctx, w, "resources/read", req.ID, result)
	return true
}

// releaseUIMRTRResult finishes a ui:// resources/read whose upstream answered
// with an InputRequiredResult rather than a template. It runs on the parent
// claim handleUIRead already took and fenced, so there is exactly ONE parent
// operation and ONE release child for these bytes — the "no double parent claim
// and no two releases for the same bytes" rule of the adjudication (§4.2).
//
// What differs from the render leg is only WHICH inspector governs the release:
// the payload is an elicitation surface arriving through a different door, so it
// goes to the elicitation mediator on ChannelMRTRInputRequest — the same channel,
// and therefore the same policy, as the tools/call MRTR leg and the generic
// dispatch. A rule that stops a server asking for a password through
// elicitation/create stops it asking through a ui:// resource read.
//
// It always reports true: the response is written on every path below.
func (rs *ResourceServer) releaseUIMRTRResult(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken,
	uri, opID string, claimRec GateRecord, res UpstreamResult, plan mrtrReleasePlan, trace string) bool {
	result := res.Result
	// Round 2 (F-1): the parent settles the DISPATCH FACT — completed, naming the
	// release child — IMMEDIATELY, before any release decision. The upstream
	// fetch happened, and an observed dispatch must be durable the moment it is
	// observed. Every refusal below then records its disposition on the CHILD
	// (settled `withheld`): the same unit the render-deny leg above and the
	// elicitation response-deny legs use for the identical situation. A refusal
	// stands even when the child's journaling refuses: evidence is mandatory on
	// allow, never on deny.
	release := plan.release
	if !release.Valid() {
		// The noAuthority bug path planned nothing; the disposition of an observed
		// result still needs its durable child operation.
		release = deriveResponseReleaseBinding(rs.tenant, claimRec.Binding, plan.class, resultDigest(result))
	}
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: claimRec, State: DispatchCompleted,
		ResultDigest: resultDigest(result), DispatchRef: res.DispatchRef,
		ReleaseBinding: release,
	})
	if settlement.FailureClass != sdk.FailureNone {
		// The dispatch outcome is not durable: neither a release nor a deny
		// disposition may build on it — withhold behind the indeterminate shape.
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return true
	}
	uiMRTRWithheld := func() {
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, uri, "", plan.class, "MCP07", trace),
			release, resultDigest(result), func(reason string) {
				rs.auditUITraced(ctx, tok, uri, false, reason, trace)
			})
	}
	withholdAndRefuse := func(auditReason, wireReason string, status, code int) bool {
		uiMRTRWithheld()
		rs.auditUITraced(ctx, tok, uri, false, auditReason, trace)
		rs.writeRPCError(w, status, req.ID, code, wireReason)
		return true
	}
	if plan.noAuthority {
		// Internal defect, never a classification: fail safe rather than guess a
		// contract (adjudication §5 — no body-only fallback).
		return withholdAndRefuse(
			"ui-template MRTR release planning ran without an authority profile (internal defect, fail-safe); the result was withheld",
			"upstream returned an ungovernable mediated result", http.StatusBadGateway, rpcUpstreamError)
	}
	if plan.unreadable {
		return withholdAndRefuse(
			"ui-template read selects the governed input-required contract — or a duplicated discriminator makes "+
				"that reading impossible to exclude — and its governed payload cannot be projected: unreadable, "+
				"refused; the mediated payloads were never released",
			"upstream returned an ambiguous mediated result", http.StatusBadGateway, rpcUpstreamError)
	}
	if plan.mediate {
		// A hijack verdict releases nothing: the disposition of the observed
		// payload is made durable on the release child and only THEN is the deny
		// written (round-3 settle-then-write — the withheld child must be
		// durable before the refusal leaves the process).
		if rs.mrtrUIHijacked(ctx, w, req, tok, plan, trace, uiMRTRWithheld) {
			return true
		}
	}
	// Every classified InputRequiredResult is released through the child — a
	// requestState-only result (schema.ts:571-595) and a valid empty
	// `inputRequests:{}` included. There is nothing for the mediator to inspect
	// in those, and that is precisely the distinction: zero inspections is not
	// zero evidence. Releasing them plainly meant a hostile or unavailable
	// release journal never saw the operation it was supposed to be able to
	// refuse, and the server-controlled opaque state went out regardless.
	rel, rok := rs.anchorResponseRelease(ctx, w, req.ID,
		rs.releaseDecision(tok, uri, "", plan.class, "MCP07", trace),
		plan.release, opID, func(reason string) {
			rs.auditUITraced(ctx, tok, uri, false, reason, trace)
		})
	if !rok {
		return true
	}
	_ = rel.write(ctx, w, "resources/read", req.ID, result)
	return true
}

// mrtrUIHijacked runs the mediator over a ui:// read's MRTR payloads. It is a
// named seam rather than an inline call so the UI leg and the generic leg
// provably share ChannelMRTRInputRequest: a future edit that gives one route its
// own channel has to delete this, not merely diverge from it. settleChild is
// the withheld release-child settlement, run before the deny is written
// (round-3 settle-then-write).
func (rs *ResourceServer) mrtrUIHijacked(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, plan mrtrReleasePlan, trace string, settleChild func()) bool {
	return rs.mediateMRTRResultEntries(ctx, w, req, tok, plan.entries, trace, settleChild)
}

// auditUITraced records one ui-template render decision (minimal data: the
// template URI is the governed resource reference, never content).: the UI
// render ALLOWS are evidence-enforced claims now; this helper carries the
// best-effort DENIAL/alert evidence only.
func (rs *ResourceServer) auditUITraced(ctx context.Context, tok validatedToken, uri string, allowed bool, reason, trace string) {
	rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs, Tool: uri,
		Allowed: allowed, Reason: reason, TokenBinding: tok.Binding, TraceParent: trace, At: rs.clock(),
	}, sdk.EvidenceBinding{}) // best-effort denial/alert evidence (UI allows are claims)
}

// rawMentionsUIScheme reports whether raw JSON-RPC params mention the reserved
// ui:// scheme anywhere (case-insensitive) — the smuggling-guard trigger.
func rawMentionsUIScheme(params []byte) bool {
	return strings.Contains(strings.ToLower(string(params)), uiScheme)
}

// upstreamReq builds the no-passthrough upstream request: it carries the method, raw
// params, the VALIDATED subject + scopes and the inbound W3C trace context — never
// the inbound bearer (which never leaves the validator). The trace context prefers
// the request `_meta` (the RC's standard placement, SEP-414) over the HTTP header,
// so the value propagated upstream is the one the gen_ai pipeline correlates on.
func (rs *ResourceServer) upstreamReq(r *http.Request, method string, params []byte, tok validatedToken) UpstreamRequest {
	scopes := make([]string, 0, len(tok.Scopes))
	for s := range tok.Scopes {
		scopes = append(scopes, s)
	}
	return UpstreamRequest{
		Method:      method,
		Params:      params,
		Subject:     tok.Subject,
		Scopes:      scopes,
		TraceParent: requestTraceParent(r, params),
	}
}

// filterToolsList removes from a tools/list result every tool the validated caller can
// never call (deny-by-default at discovery: a client never sees a tool it cannot invoke).
// Filtering is BOTH per-tool (absent/denied in the server toolset) AND per-role (the
// caller's roles do not satisfy the tool's AllowedRoles, E1). A result it cannot parse is
// returned unchanged (the gate at call time is the real enforcement point; discovery
// filtering is defense in depth).
//
// Every sibling field of "tools" is PRESERVED (the RC adds REQUIRED result-level
// fields — resultType, ttlMs, cacheScope — that re-marshaling must not drop), and a
// present cacheScope is downgraded to "private": the filtered list varies by
// authorization context, so no shared cache may serve it across principals (SEP-2549).
func (rs *ResourceServer) filterToolsList(result json.RawMessage, tok validatedToken) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(result, &obj) != nil {
		return result
	}
	var tools []json.RawMessage
	if json.Unmarshal(obj["tools"], &tools) != nil {
		return result
	}
	allowed := rs.toolset.allowedNamesForRoles(tok.Roles)
	kept := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		var nm struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t, &nm) != nil {
			continue
		}
		if _, ok := allowed[nm.Name]; ok {
			kept = append(kept, t)
		}
	}
	rawKept, err := json.Marshal(kept)
	if err != nil {
		return result
	}
	obj["tools"] = rawKept
	out, err := json.Marshal(obj)
	if err != nil {
		return result
	}
	return downgradeCacheScopePrivate(out)
}

// serveMetadata writes the RFC 9728 Protected Resource Metadata document
// (unauthenticated). resource + authorization_servers are mandatory;
// bearer_methods_supported is always ["header"] (the RS accepts the bearer ONLY in the
// Authorization header — never a query/body token, RFC 6750 §2); scopes_supported,
// resource_name and resource_documentation are advertised when known. The document is
// HONEST: DPoP proof verification is advertised because the RS honors it, DPoP-bound
// token REQUIREMENT is advertised only when required, and mTLS certificate-bound
// token support is advertised only when enabled for this TLS-terminating process.
func (rs *ResourceServer) serveMetadata(w http.ResponseWriter) {
	doc := map[string]any{
		"resource":                          rs.resource,
		"authorization_servers":             rs.authServers,
		"bearer_methods_supported":          []string{"header"},
		"dpop_signing_alg_values_supported": mcpAllowedAlgNames(),
	}
	if rs.requireDPoP {
		doc["dpop_bound_access_tokens_required"] = true
	}
	if rs.acceptMTLSBound {
		doc["tls_client_certificate_bound_access_tokens"] = true
	}
	if len(rs.scopes) > 0 {
		doc["scopes_supported"] = rs.scopes
	}
	if rs.resourceName != "" {
		doc["resource_name"] = rs.resourceName
	}
	if rs.resourceDocs != "" {
		doc["resource_documentation"] = rs.resourceDocs
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(doc)
}

// --- challenges + responses ------------------------------------------------------

// challengeMissing answers a request with no bearer: 401 + the RFC 9728 challenge
// pointing at this server's PRM document so the client can discover the AS.
func (rs *ResourceServer) challengeMissing(w http.ResponseWriter) {
	if rs.requireDPoP {
		w.Header().Set("WWW-Authenticate", rs.dpopChallenge(""))
		http.Error(w, "authorization required", http.StatusUnauthorized)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+rs.metadataURL+`"`)
	http.Error(w, "authorization required", http.StatusUnauthorized)
}

// challengeInvalidToken answers an invalid/cross-audience token: 401 invalid_token +
// the RFC 9728 challenge. The specific reason is NEVER leaked (a cross-audience token
// and an expired one look identical to the client).
func (rs *ResourceServer) challengeInvalidToken(w http.ResponseWriter) {
	if rs.requireDPoP {
		w.Header().Set("WWW-Authenticate", rs.dpopChallenge("invalid_token"))
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+rs.metadataURL+`"`)
	http.Error(w, "invalid token", http.StatusUnauthorized)
}

func (rs *ResourceServer) challengeDPoPNonce(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", rs.dpopChallenge("use_dpop_nonce"))
	w.Header().Set("DPoP-Nonce", rs.dpopNonces.fresh(rs.clock()))
	http.Error(w, "invalid token", http.StatusUnauthorized)
}

func (rs *ResourceServer) dpopChallenge(errorValue string) string {
	parts := []string{}
	if errorValue != "" {
		parts = append(parts, `error="`+errorValue+`"`)
	}
	parts = append(parts,
		`algs="`+mcpAllowedAlgList()+`"`,
		`resource_metadata="`+rs.metadataURL+`"`,
	)
	return "DPoP " + strings.Join(parts, ", ")
}

// challengeScope answers an insufficient-scope tools/call: 403 insufficient_scope with
// the REQUIRED scope, so the client can step up (SEP-835 incremental authorization).
func (rs *ResourceServer) challengeScope(w http.ResponseWriter, id json.RawMessage, scope string) {
	w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+scope+`", resource_metadata="`+rs.metadataURL+`"`)
	rs.writeRPCError(w, http.StatusForbidden, id, rpcAccessDenied, "insufficient scope for tool (step up required)")
}

// writeResult writes a JSON-RPC success response (HTTP 200) echoing the request id.
// The relayed result's SEP-2549 cache metadata is translated into Cache-Control for
// downstream intermediaries (deny-closed: no metadata ⇒ no-store, rsnext.go).
//
// ROUND-7 R7-03: it REPORTS whether the response was written. The encoder error
// used to be discarded, and one caller — the owner's terminal `tasks/get` — then
// treated an attempted write as receipt and destroyed the record it had just failed
// to deliver.
//
// WHAT A NIL ERROR MEANS, exactly (round-8 R8-06 — the round-7 comment called it
// "proof of delivery", which overclaims the Go HTTP contract): this process's
// `http.ResponseWriter` accepted every byte of the encoded body. It is NOT an
// acknowledgement from the remote client and nothing here can be. Both governance
// consumers are therefore written to be safe under that weaker fact.
//
// There are TEN production call sites (re-count: the UI/elicitation/sampling
// writes moved behind the anchored release engine). THREE consume the error,
// because a governance state transition depends on it: the initial task-handle
// relay (handleToolTaskResult — handle-relay provenance, R8-01), the owner's
// terminal `tasks/get` on its unmediated leg (handleTaskGet — collection proof,
// R7-03/R8-02), and the response-release engine (anchoredRelease.write,
// evidencerelease.go — the release settlement classifies the write with the same
// three-way byte accounting; since review round 1 the task-handle relay's
// MEDIATED leg writes through it too, so the first consumer above is that
// relay's UNMEDIATED leg). The other SEVEN are RESPONSE-ONLY relays that draw
// no conclusion from the write; each discards the error EXPLICITLY (`_ =`) rather
// than by omission, so "this site ignores it" is a visible decision and not an
// oversight. A failed write there costs the client its response — which it
// already knows — and changes no governance state.
//
// ROUND-9 R9-01: for the FIRST consumer the returned error is only one of several
// outcomes. That relay is closed out by a deferred custodian
// (taskHandleRelayGuard), so an ABNORMAL unwind through this function — where the
// local writer neither returned nor reported a failure — is classified too, and
// conservatively, as possible delivery.
//
// ROUND-11 N11-01: and for that same consumer the ERROR alone is not a classification
// either. `json.Encoder.Encode` makes ONE `Write` call and discards the byte count it
// reports, while `io.Writer` lets a conforming writer accept `len(p)-1` bytes and still
// fail — the missing byte being the newline `Encode` appends, which JSON-RPC does not
// require. The relay site therefore passes a `relayWriteCounter` and treats only a
// PROVEN ZERO-BYTE write as never-delivered. Nothing about this function's signature or
// about the other NINE production call sites changed. (review round 1 corrects the
// number: this paragraph said "eleven", which no re-count in this file supports — there
// are TEN production sites in total, one of which is the relay site itself.)
func (rs *ResourceServer) writeResult(w http.ResponseWriter, method string, id, result json.RawMessage) error {
	rs.setCacheHeaders(w, method, result)
	resp := map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id)}
	if len(result) > 0 {
		resp["result"] = result
	} else {
		resp["result"] = json.RawMessage("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

// writeToolError writes a tools/call result with isError:true (SEP-1303) — a Tool
// Execution Error the MODEL can self-correct, returned as a JSON-RPC RESULT (HTTP 200),
// NOT a protocol error.
func (rs *ResourceServer) writeToolError(w http.ResponseWriter, id json.RawMessage, msg string) {
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// writeRPCError writes a JSON-RPC error with the given HTTP status (used for authz
// denials at 403 and upstream failures at 502; transport/parse errors at 400). The
// message is non-sensitive.
func (rs *ResourceServer) writeRPCError(w http.ResponseWriter, httpStatus int, id json.RawMessage, code int, msg string) {
	rs.writeRPCErrorData(w, httpStatus, id, code, msg, nil)
}

// writeRPCErrorData is writeRPCError with a structured error.data object (the
// evidence refusal shapes). The id is echoed verbatim; Cache-Control is always
// no-store (refusals are never cacheable).
func (rs *ResourceServer) writeRPCErrorData(w http.ResponseWriter, httpStatus int, id json.RawMessage, code int, msg string, data map[string]any) {
	errObj := map[string]any{"code": code, "message": msg}
	if len(data) > 0 {
		errObj["data"] = data
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(id),
		"error":   errObj,
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(resp)
}

// --- evidence refusal wire shapes (design §5) --------------------------------

// evidenceNotificationRefusal handles the notification leg of an evidence refusal:
// a JSON-RPC notification (absent/null id) expects no response object, so the
// refusal is the HTTP status + cache/retry headers with an EMPTY body (design §5).
// Returns true when the refusal was written (the caller stops).
func evidenceNotificationRefusal(w http.ResponseWriter, id json.RawMessage, status int, retryAfter bool) bool {
	trimmed := strings.TrimSpace(string(id))
	if trimmed != "" && trimmed != "null" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if retryAfter {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	return true
}

// writeEvidenceUnavailable: the mandatory evidence anchor could not be obtained and
// the request was NOT forwarded. HTTP 503 + Retry-After 1 (a transient
// infrastructure fault — retryABLE); operation_id omitted when derivation failed.
// The specific EvidenceFault (spool mode, leader state, SQL detail) is NEVER leaked.
func (rs *ResourceServer) writeEvidenceUnavailable(w http.ResponseWriter, id json.RawMessage, opID string) {
	if evidenceNotificationRefusal(w, id, http.StatusServiceUnavailable, true) {
		return
	}
	data := map[string]any{
		"failure_class": string(sdk.FailureEvidenceFault),
		"retryable":     true,
	}
	if opID != "" {
		data["operation_id"] = opID
	}
	w.Header().Set("Retry-After", "1")
	rs.writeRPCErrorData(w, http.StatusServiceUnavailable, id, rpcEvidenceUnavailable,
		"governance evidence unavailable; request was not forwarded", data)
}

// writeEvidenceRebind: same OperationID, different EffectDigest — the single-use
// claim is bound to another effect. Non-retryable (a new key names a new operation).
func (rs *ResourceServer) writeEvidenceRebind(w http.ResponseWriter, id json.RawMessage, opID string) {
	if evidenceNotificationRefusal(w, id, http.StatusConflict, false) {
		return
	}
	rs.writeRPCErrorData(w, http.StatusConflict, id, rpcEvidenceRebind,
		"operation key is already bound to a different request", map[string]any{
			"failure_class": string(sdk.FailureReplay),
			"retryable":     false,
			"operation_id":  opID,
		})
}

// writeOperationRecorded: exact replay of a claimed/settled operation — the recorded
// state is returned and the effect is NEVER re-emitted. Only recorded state, result
// digest and operation id (the journal retains no raw results).
func (rs *ResourceServer) writeOperationRecorded(w http.ResponseWriter, id json.RawMessage, opID, state, recordedResultDigest string) {
	if evidenceNotificationRefusal(w, id, http.StatusConflict, false) {
		return
	}
	data := map[string]any{
		"operation_id": opID,
		"state":        state,
	}
	if recordedResultDigest != "" {
		data["result_digest"] = recordedResultDigest
	}
	rs.writeRPCErrorData(w, http.StatusConflict, id, rpcOperationRecorded,
		"operation already recorded; it will not be forwarded again", data)
}

// writeOperationIndeterminate: post-transmit ambiguity (dispatch state unknown, or a
// settlement that failed to record after the forward). The claim is burned:
// at-most-once, never re-forwarded.
func (rs *ResourceServer) writeOperationIndeterminate(w http.ResponseWriter, id json.RawMessage, opID string) {
	if evidenceNotificationRefusal(w, id, http.StatusServiceUnavailable, false) {
		return
	}
	data := map[string]any{
		"retryable": false,
	}
	if opID != "" {
		data["operation_id"] = opID
	}
	rs.writeRPCErrorData(w, http.StatusServiceUnavailable, id, rpcOperationRecorded,
		"operation outcome is indeterminate; it will not be forwarded again", data)
}

// auditTraced records a tools/call gate decision to the (seam) auditor with minimal
// data, carrying the request's W3C traceparent (SEP-414) so the decision can
// be correlated with the gen_ai spans of the same trace — an identifier, never a
// payload.
func (rs *ResourceServer) auditTraced(ctx context.Context, tok validatedToken, tool, scope string, allowed bool, reason, approvalRef, mcpTag, trace string) {
	rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: tool, RequiredScope: scope,
		Allowed: allowed, Reason: reason, ApprovalRef: approvalRef, MCPTag: mcpTag,
		TokenBinding: tok.Binding, TraceParent: trace, At: rs.clock(),
	}, sdk.EvidenceBinding{}) // legacy best-effort surface (evidence enforcement: stages 4-6)
}

func (rs *ResourceServer) clock() time.Time {
	if rs.now != nil {
		return rs.now().UTC()
	}
	return time.Now().UTC()
}

// originAllowed enforces the Origin check (PR#1439): a request with no Origin header
// (a non-browser client) is allowed; a request WITH an Origin must match the operator
// allowlist (and an empty allowlist matches nothing, so any browser Origin is refused).
func (rs *ResourceServer) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	_, ok := rs.allowedOrigins[strings.ToLower(origin)]
	return ok
}

// --- helpers --------------------------------------------------------------------

func (rs *ResourceServer) enforceTokenBinding(r *http.Request, raw, authScheme string, tok validatedToken) (validatedToken, error) {
	if tok.Binding == "" {
		tok.Binding = tokenBindingBearer
	}
	proofPresented := len(r.Header.Values("DPoP")) > 0 || strings.EqualFold(authScheme, "dpop")

	// A sender-constrained token accepted as a plain bearer is exactly the replay
	// attack the constraint exists to stop. This mirrors the ID-JAG cnf rejection:
	// once a verified token carries a recognized cnf binding, the RS must verify
	// that binding or fail closed, regardless of operator opt-in knobs.
	if tok.ConfirmationJKT != "" || proofPresented {
		jkt, err := rs.validateDPoPProof(r, raw, tok.ConfirmationJKT)
		if err != nil {
			return validatedToken{}, err
		}
		if tok.ConfirmationJKT != "" && secureEqualString(jkt, tok.ConfirmationJKT) {
			tok.Binding = tokenBindingDPoP
		}
	}

	if tok.ConfirmationX5T != "" {
		if !rs.acceptMTLSBound {
			return validatedToken{}, errDPoPProof
		}
		thumb, ok := mtlsCertificateThumbprint(r)
		if !ok || !secureEqualString(thumb, tok.ConfirmationX5T) {
			return validatedToken{}, errDPoPProof
		}
		if tok.Binding == tokenBindingBearer {
			tok.Binding = tokenBindingMTLS
		}
	}

	if rs.requireDPoP && tok.Binding != tokenBindingDPoP {
		return validatedToken{}, errDPoPProof
	}
	return tok, nil
}

// isMetadataPath reports whether p is the RFC 9728 well-known path or a path-suffixed
// variant (/.well-known/oauth-protected-resource/<server-path>).
func isMetadataPath(p string) bool {
	return p == wellKnownProtectedResource || strings.HasPrefix(p, wellKnownProtectedResource+"/")
}

// authorizationTokenOf extracts the token from an Authorization header using the
// Bearer or DPoP scheme. The scheme is returned lower-case.
func authorizationTokenOf(header string) (string, string) {
	h := strings.TrimSpace(header)
	i := strings.IndexByte(h, ' ')
	if i <= 0 {
		return "", ""
	}
	scheme := strings.ToLower(h[:i])
	if scheme != "bearer" && scheme != "dpop" {
		return "", ""
	}
	return strings.TrimSpace(h[i+1:]), scheme
}

// bearerOf extracts the token from an "Authorization: Bearer <token>" header.
func bearerOf(header string) string {
	tok, scheme := authorizationTokenOf(header)
	if scheme != "bearer" {
		return ""
	}
	return tok
}

// readBounded reads the request body up to maxRPCBody.
func readBounded(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, maxRPCBody))
}

// rawOrNull returns id if non-empty, else a JSON null (a response to a request whose id
// could not be parsed still carries a null id per JSON-RPC 2.0).
func rawOrNull(id json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(id))) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// --- elicitation/sampling runtime PEP ----------------------------------------

// handleElicitation mediates an elicitation/create request (SEP-1865/SEP-1036):
// the server asks the client for structured user input. The RS inspects the
// prompt (and optionally the user's response) through the ElicitationMediator
// seam before forwarding. A nil mediator is a clean pass (the community build;
// no rug-pull). HITL routes through the existing ApprovalGate.
//
// HONESTY: the RS mediates what transits through it. In a deployment where
// server→client messages bypass the RS (direct SSE), this PEP does not see
// them — verified-deployed at the PEP, never "impossible to bypass."
//
// (stage 5) + stage-7 B-bis round 2: the surface is EVIDENCE-ENFORCED with
// the two-phase + response-release shape (per-site reorder plan, elicitation
// row): strict canonicalization → request mediation (pre-claim authorization)
// → claim/fence the outbound request → forward the canonical bytes → settle the
// parent COMPLETED naming the release child (the dispatch fact, durable the
// moment it is observed) → mediate the response — a deny settles the RELEASE
// CHILD `withheld` — → write the response ONLY against an anchored release
// claim.
func (rs *ResourceServer) handleElicitation(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) {
	trace := requestTraceParent(r, req.Params)

	// STRICT canonicalization first (the stage-3/4 P0 class): the members the
	// mediator inspects — message, requestedSchema, the URL-mode _meta — are read
	// from the strict tree with EXACT casing, never a case-folding json.Unmarshal
	// of the bytes a case-folding upstream would consume differently.
	canon, cerr := canonicalizeMediatedParams(req.Params, elicitationReservedKeys)
	if cerr != nil || canon.Presence != paramsPresent {
		rs.auditElicitationTraced(ctx, tok, ChannelElicitationRequest, false,
			"elicitation/create params refused by strict canonicalization (dup/case-alias/malformed keys)", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"malformed elicitation/create params")
		return
	}

	// Inspect the server's elicitation REQUEST (the prompt + schema + URL-mode).
	// Pre-claim: this mediation is part of the authorization surface.
	if rs.elicitationMediator != nil {
		content := []byte(canon.stringMember("message"))
		if len(content) > 0 {
			dec := rs.elicitationMediator.Mediate(ctx, ElicitationInspectionInput{
				Tenant:      rs.tenant,
				Subject:     tok.Subject,
				Channel:     ChannelElicitationRequest,
				Content:     content,
				ContentHash: contentSHA256(content),
				URLTarget:   canon.elicitationURLTarget(),
				SchemaHash:  rawSHA256(canon.rawMember("requestedSchema")),
				TraceParent: trace,
			})
			rs.publishElicitationFindings(ctx, tok, dec.Findings, trace)
			rs.emitElicitationMeter(dec.Meter)
			if rs.handleElicitationVerdict(ctx, w, req, tok, dec, ChannelElicitationRequest, trace) {
				return
			}
		}
	}

	// --- evidence enforcement: claim → fence → forward → settle → release.
	// The historical post-hoc "elicitation authorized[; upstream forward failed]"
	// audits are GONE — they are the claim and its settlement now.
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		rs.auditElicitationTraced(ctx, tok, ChannelElicitationRequest, false,
			"operation-id derivation failed (fail-closed)", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	pd := mediatedPolicyDigest(releaseClassElicitation, false, false, false, rs.elicitationMediator != nil)
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveMediatedEffectDigest(rs.tenant, rs.resource, "elicitation/create", tok,
			"mcp.elicitation", "", rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon, pd)),
	}
	claimRec := rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: ChannelElicitationRequest, Allowed: true, Reason: "elicitation/create authorized",
		MCPTag: "MCP10", TokenBinding: tok.Binding, TraceParent: trace,
		OperationIDKind: idKind, EffectAction: mediatedActionElicitationPrefix + idKind, At: rs.clock(),
	}, binding)
	if !claimRec.MayEmit(binding) {
		rs.refuseToolCallEvidence(w, req.ID, claimRec, opID)
		return
	}
	if fence := rs.auditor.BeforeEffect(ctx, claimRec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}

	ureq := rs.upstreamReq(r, "elicitation/create", canon.Forward, tok)
	ureq.OperationID = binding.OperationID
	ureq.EffectDigest = binding.EffectDigest
	ureq.FenceToken = claimRec.FenceToken
	res, ferr := rs.upstream.Forward(ctx, ureq)
	state, relay := classifyDispatch(res, ferr)
	if !relay {
		settlement := rs.auditor.Settle(ctx, GateOutcome{
			Record: claimRec, State: state,
			ResultDigest: resultDigest(res.Result), DispatchRef: res.DispatchRef,
		})
		if settlement.FailureClass != sdk.FailureNone {
			rs.writeOperationIndeterminate(w, req.ID, opID)
			return
		}
		if state == DispatchUnknown {
			rs.writeOperationIndeterminate(w, req.ID, opID)
		} else {
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream elicitation forward failed")
		}
		return
	}
	result := res.Result

	// Round 2 (F-1): the parent settles the DISPATCH FACT — completed, naming
	// the response-release child — IMMEDIATELY after the round trip is observed
	// and BEFORE the response inspection. The release disposition (released or
	// withheld) is the CHILD operation's record. A refused parent settlement
	// withholds the response behind the indeterminate shape.
	release := deriveResponseReleaseBinding(rs.tenant, binding, releaseClassElicitation, resultDigest(result))
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: claimRec, State: DispatchCompleted,
		ResultDigest: resultDigest(result), DispatchRef: res.DispatchRef,
		ReleaseBinding: release,
	})
	if settlement.FailureClass != sdk.FailureNone {
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	elicitationWithheld := func() {
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, ChannelElicitationRequest, "", releaseClassElicitation, "MCP10", trace),
			release, resultDigest(result), func(reason string) {
				rs.auditElicitationTraced(ctx, tok, ChannelElicitationResponse, false, reason, trace)
			})
	}

	// STAGE-7 P0-3 (T14): elicitation/create is NOT one of the three requests a
	// server may answer with an InputRequiredResult, and this route relays its
	// upstream result without ever asking the MRTR classifier — the response
	// mediator reads `content`, which an input_required result does not carry, so
	// the payload used to leave with 200 and no governance at all. A MUST NOT
	// enforced only where the classifier happens to run is enforced only there.
	// M-1R2: the refusal records what was OBSERVED — the forbidden literal or a
	// duplicated discriminator — each under its own controlled class.
	if ref := coreMRTRRefusalOn(req.Method, result); ref.refused() {
		elicitationWithheld()
		rs.auditElicitationTraced(ctx, tok, ChannelElicitationResponse, false,
			"elicitation response WITHHELD: "+ref.wire()+"; reason class: "+ref.reason(), trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, ref.wire())
		return
	}

	// inspect the elicitation RESPONSE (the user's input — the exfil route).
	// Always inspected (decision). Any `content` member carries user data
	// worth inspecting, whatever the action says (review round 1, S5-03: the
	// RC schema's action for a submitted form is `accept`, schema.ts:3121-3136).
	// Round 2 (F-1): a deny AFTER the round trip records its disposition on the
	// release child — settled WITHHELD — never on the parent, whose completed
	// settlement above states only the observed dispatch. The deny STANDS even
	// when the child's journaling refuses (evidence is mandatory on allow, never
	// on deny).
	if rs.elicitationMediator != nil {
		respContent, ambiguous := extractElicitationResponseContent(result)
		if ambiguous {
			// The response cannot be read strictly (it does not decode, or it carries a
			// case-variant alias of an interpreted member), so the exfil route cannot be
			// inspected: the release child settles WITHHELD and the response is
			// refused. Never "nothing to inspect".
			elicitationWithheld()
			rs.auditElicitationTraced(ctx, tok, ChannelElicitationResponse, false,
				"elicitation response WITHHELD: the upstream result is ambiguous to the response mediator "+
					"(malformed, duplicate or case-variant alias of an interpreted member)", trace)
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream returned an ambiguous elicitation response")
			return
		}
		if len(respContent) > 0 {
			dec := rs.elicitationMediator.Mediate(ctx, ElicitationInspectionInput{
				Tenant:      rs.tenant,
				Subject:     tok.Subject,
				Channel:     ChannelElicitationResponse,
				Content:     respContent,
				ContentHash: contentSHA256(respContent),
				TraceParent: trace,
			})
			rs.publishElicitationFindings(ctx, tok, dec.Findings, trace)
			rs.emitElicitationMeter(dec.Meter)
			if verdict := rs.evaluateElicitationVerdict(ctx, tok, dec, ChannelElicitationResponse); verdict != mediationPass {
				elicitationWithheld()
				rs.writeMediationDeny(ctx, w, req, tok, verdict, dec.Reason, ChannelElicitationResponse, trace)
				return
			}
		}
	}

	// Allow: anchor the exact release binding the parent settlement named, then
	// write.
	rel, rok := rs.anchorResponseRelease(ctx, w, req.ID,
		rs.releaseDecision(tok, ChannelElicitationRequest, "", releaseClassElicitation, "MCP10", trace),
		release, opID, func(reason string) {
			rs.auditElicitationTraced(ctx, tok, ChannelElicitationRequest, false, reason, trace)
		})
	if !rok {
		return
	}
	_ = rel.write(ctx, w, "elicitation/create", req.ID, result)
}

// handleSampling mediates a sampling/createMessage request (SEP-1577): the
// server injects messages for the client's model — prompt-injection-via-server
// directly at the model (MCP10, OWASP LLM01:2025). Deny-closed/HITL.
//
// (stage 5): the same two-phase and response-release shape as elicitation
// (per-site reorder plan, sampling row). Sampling has no response-side content
// channel (only ChannelSamplingRequest exists), so the release follows the
// completed settlement directly.
func (rs *ResourceServer) handleSampling(ctx context.Context, w http.ResponseWriter, r *http.Request, req rsRequest, tok validatedToken) {
	trace := requestTraceParent(r, req.Params)

	canon, cerr := canonicalizeMediatedParams(req.Params, samplingReservedKeys)
	if cerr != nil || canon.Presence != paramsPresent {
		rs.auditElicitationTraced(ctx, tok, ChannelSamplingRequest, false,
			"sampling/createMessage params refused by strict canonicalization (dup/case-alias/malformed keys)", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"malformed sampling/createMessage params")
		return
	}

	if rs.elicitationMediator != nil {
		// The inspected text is read from the STRICT canonical TREE of the governed
		// "messages" member, with exact casing at every nesting level (review
		// round 1, S5-03: a nested `Text` alias used to empty the inspected text
		// while a case-folding consumer of the very bytes forwarded read the other
		// spelling). An unreadable or alias-ambiguous messages structure is a
		// PROTOCOL refusal here — pre-claim, nothing forwarded — never a silent
		// "nothing to inspect".
		content, ambiguous := extractSamplingText(canon.treeMember("messages"))
		if ambiguous {
			rs.auditElicitationTraced(ctx, tok, ChannelSamplingRequest, false,
				"sampling/createMessage params refused: the sampling messages carry case-variant aliases of "+
					"mediated members, or are not the schema's array of message objects (ambiguous — refused)", trace)
			rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
				"malformed sampling/createMessage params")
			return
		}
		if len(content) > 0 {
			dec := rs.elicitationMediator.Mediate(ctx, ElicitationInspectionInput{
				Tenant:      rs.tenant,
				Subject:     tok.Subject,
				Channel:     ChannelSamplingRequest,
				Content:     content,
				ContentHash: contentSHA256(content),
				TraceParent: trace,
			})
			rs.publishElicitationFindings(ctx, tok, dec.Findings, trace)
			rs.emitElicitationMeter(dec.Meter)
			if rs.handleElicitationVerdict(ctx, w, req, tok, dec, ChannelSamplingRequest, trace) {
				return
			}
		}
	}

	// --- evidence enforcement: claim → fence → forward → settle → release.
	opID, idKind, derr := deriveToolCallOperationID(rs.tenant, rs.resource, tok, canon.OperationKey)
	if derr != nil {
		rs.auditElicitationTraced(ctx, tok, ChannelSamplingRequest, false,
			"operation-id derivation failed (fail-closed)", trace)
		rs.writeEvidenceUnavailable(w, req.ID, "")
		return
	}
	pd := mediatedPolicyDigest(releaseClassSampling, false, false, false, rs.elicitationMediator != nil)
	binding := sdk.EvidenceBinding{
		OperationID: sdk.OperationID(opID),
		EffectDigest: sdk.EffectDigest(deriveMediatedEffectDigest(rs.tenant, rs.resource, "sampling/createMessage", tok,
			"mcp.sampling", "", rs.upstreamDescriptor, sortedScopeSet(tok.Scopes), canon, pd)),
	}
	claimRec := rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: ChannelSamplingRequest, Allowed: true, Reason: "sampling/createMessage authorized",
		MCPTag: "MCP10", TokenBinding: tok.Binding, TraceParent: trace,
		OperationIDKind: idKind, EffectAction: mediatedActionSamplingPrefix + idKind, At: rs.clock(),
	}, binding)
	if !claimRec.MayEmit(binding) {
		rs.refuseToolCallEvidence(w, req.ID, claimRec, opID)
		return
	}
	if fence := rs.auditor.BeforeEffect(ctx, claimRec); fence.MustRefuse(binding) {
		rs.writeEvidenceUnavailable(w, req.ID, opID)
		return
	}

	ureq := rs.upstreamReq(r, "sampling/createMessage", canon.Forward, tok)
	ureq.OperationID = binding.OperationID
	ureq.EffectDigest = binding.EffectDigest
	ureq.FenceToken = claimRec.FenceToken
	res, ferr := rs.upstream.Forward(ctx, ureq)
	state, relay := classifyDispatch(res, ferr)
	if !relay {
		settlement := rs.auditor.Settle(ctx, GateOutcome{
			Record: claimRec, State: state,
			ResultDigest: resultDigest(res.Result), DispatchRef: res.DispatchRef,
		})
		if settlement.FailureClass != sdk.FailureNone {
			rs.writeOperationIndeterminate(w, req.ID, opID)
			return
		}
		if state == DispatchUnknown {
			rs.writeOperationIndeterminate(w, req.ID, opID)
		} else {
			rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError,
				"upstream sampling forward failed")
		}
		return
	}
	result := res.Result

	release := deriveResponseReleaseBinding(rs.tenant, binding, releaseClassSampling, resultDigest(result))
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: claimRec, State: DispatchCompleted,
		ResultDigest: resultDigest(result), DispatchRef: res.DispatchRef,
		ReleaseBinding: release,
	})
	if settlement.FailureClass != sdk.FailureNone {
		rs.writeOperationIndeterminate(w, req.ID, opID)
		return
	}
	// STAGE-7 P0-3 (T14): sampling/createMessage is not a sanctioned MRTR origin
	// either, and this route has no result classifier at all. The refusal of an
	// OBSERVED result records its disposition on the release child the settlement
	// above just named — the same unit every other surface uses. M-1R2: the
	// record states what was observed, each cause under its own controlled class.
	if ref := coreMRTRRefusalOn(req.Method, result); ref.refused() {
		rs.settleWithheldRelease(ctx,
			rs.withheldReleaseDecision(tok, ChannelSamplingRequest, "", releaseClassSampling, "MCP10", trace),
			release, resultDigest(result), func(reason string) {
				rs.auditElicitationTraced(ctx, tok, ChannelSamplingRequest, false, reason, trace)
			})
		rs.auditElicitationTraced(ctx, tok, ChannelSamplingRequest, false,
			"sampling response WITHHELD: "+ref.wire()+"; reason class: "+ref.reason(), trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, ref.wire())
		return
	}
	rel, rok := rs.anchorResponseRelease(ctx, w, req.ID,
		rs.releaseDecision(tok, ChannelSamplingRequest, "", releaseClassSampling, "MCP10", trace),
		release, opID, func(reason string) {
			rs.auditElicitationTraced(ctx, tok, ChannelSamplingRequest, false, reason, trace)
		})
	if !rok {
		return
	}
	_ = rel.write(ctx, w, "sampling/createMessage", req.ID, result)
}

// mediationVerdict classifies one mediator decision evaluation.
type mediationVerdict int

const (
	mediationPass mediationVerdict = iota
	mediationDenyHITL
	mediationDenyContent
)

// approvedForPlan consults the HITL approval gate about `plan` and re-checks the
// PLAN EQUALITY the anti-TOCTOU binding depends on. It is the ONE implementation
// of that rule for every mediated surface (elicitation/sampling and the ui://
// render), deliberately: the rule below is subtle enough that a second copy is a
// second chance to get it wrong, and the render channel was added to it in
// rather than beside it.
//
// Review round 1 (S5-05): the PLAN EQUALITY re-check is the caller's
// obligation — GateDecision.Allowed() is "approved", not "approved for THIS
// plan" (gate.go:60-70), and tools/call re-checks it (rs.go:570). Without it an
// approval issued for another prompt/input/render authorized this mediated
// request AND its response release: precisely the anti-TOCTOU hole
// ApprovalPlanHash exists to close.
//
// Review round 2 (blocker 4): the equality is STRICT — callers always pass
// a non-empty plan, so an approval carrying an EMPTY PlanHash (bound to no plan)
// is not an approval for THIS plan and is denied. The prior `PlanHash != "" &&`
// guard let an unbound approval authorize the request.
//
// Deny-closed with no gate wired: rs.gate defaults to denyApprovalGate
// (rsconfig.go:355), never nil, so an un-wired deployment refuses every HITL
// decision instead of releasing it.
//
// It returns the ApprovalRef of the approval that authorized the call so the
// caller can name it in the release evidence: a HITL delivery whose record does
// not say WHICH human approval released it is durable but not attributable, and
// tools/call already keeps that reference through to its claim (rs.go:637-641,
// :679). An empty ref with ok=true is possible — a gate may approve without
// naming a reference — and is not an error.
func (rs *ResourceServer) approvedForPlan(ctx context.Context, tok validatedToken, tool, plan string) (string, bool) {
	gdec, gerr := rs.gate.Authorize(ctx, ToolApprovalRequest{
		Tenant: rs.tenant, Subject: tok.Subject,
		Tool: tool, Scope: "", PlanHash: plan,
		RequestedBy: tok.Subject,
	})
	if gerr != nil || !gdec.Allowed() || gdec.PlanHash != plan {
		return "", false
	}
	return gdec.ApprovalRef, true
}

// renderApprovalPlan resolves the anti-TOCTOU plan hash ONE render approval is
// bound to: the inspector's own ApprovalPlanHash when it named one, otherwise a
// fallback over the subject, the template URI and the CONTENT FINGERPRINT of the
// very bytes fetched.
//
// ⛔ THE ENCODING IS THE SECURITY PROPERTY, NOT THE FIELD LIST. This started as
// `SHA256(channel + "|" + subject + "|" + uri + "|" + contentHash)` and the
// contrast refuted it with a concrete collision: nothing forbids `|` in a `sub`
// claim (tokenvalidate.go) or in an inventoried ui:// URI (apps.go), so
//
//	subject "alice"            + uri "ui://srv/a|ui://srv/b"
//	subject "alice|ui://srv/a" + uri "ui://srv/b"
//
// hash the SAME preimage. Two DIFFERENT subjects then share one plan, and since
// the production adapter keys an approval on (action, tool, plan) and NOT on the
// subject (cmd/olivares/mcpgateway.go, approvalbridge.go), one subject's approval
// released another's render. More fields in the seed made it LOOK stronger while
// the delimiter let the boundaries between them disappear.
//
// evidenceLPDigest is the package's own answer to exactly this — length-prefixed,
// "never delimiter joins" (evidence.go:751-760) — and it is what binds here now.
//
// The fallback binds the CONTENT on purpose, unlike the elicitation seam's seed
// (evaluateElicitationVerdict, channel+subject only): this call site already
// holds the SHA-256 of the inspected HTML, and a fallback that ignored it would
// leave open the reuse-across-content hole the field exists to close. The
// elicitation seed is NOT changed here — doing so would silently invalidate
// approvals already bound to the old value — but it is reported: it has the
// weakness this one just shed, minus the delimiter bug.
func renderApprovalPlan(planHash, subject, uri, contentHash string) string {
	if planHash != "" {
		return planHash
	}
	return evidenceLPDigest(ChannelRender, subject, uri, contentHash)
}

// evaluateElicitationVerdict evaluates the mediator's decision — HITL gate
// consultation included — WITHOUT writing anything, so a release site can place
// its blocked settlement between the verdict and the deny response.
func (rs *ResourceServer) evaluateElicitationVerdict(ctx context.Context, tok validatedToken, dec ElicitationInspectionDecision, channel string) mediationVerdict {
	if dec.HITL {
		plan := dec.ApprovalPlanHash
		if plan == "" {
			plan = contentSHA256([]byte(channel + "|" + tok.Subject))
		}
		if _, ok := rs.approvedForPlan(ctx, tok, channel, plan); !ok {
			return mediationDenyHITL
		}
	}
	if !dec.Allow && dec.Reason != "" {
		return mediationDenyContent
	}
	return mediationPass
}

// writeMediationDeny audits and writes one mediation deny (the wire shapes and
// audit phrases are the historical handleElicitationVerdict ones, unchanged).
func (rs *ResourceServer) writeMediationDeny(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, verdict mediationVerdict, reason, channel, trace string) {
	switch verdict {
	case mediationDenyHITL:
		rs.auditElicitationTraced(ctx, tok, channel, false,
			channel+" denied by HITL gate ("+reason+")", trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			channel+" requires human approval")
	case mediationDenyContent:
		rs.auditElicitationTraced(ctx, tok, channel, false,
			channel+" denied by content mediator: "+reason, trace)
		rs.writeRPCError(w, http.StatusForbidden, req.ID, rpcAccessDenied,
			channel+" denied by content mediator")
	}
}

// handleElicitationVerdict evaluates the mediator's decision: deny, HITL, or
// pass. Returns true if the request was terminated (deny or HITL-denied).
func (rs *ResourceServer) handleElicitationVerdict(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, dec ElicitationInspectionDecision, channel, trace string) bool {
	verdict := rs.evaluateElicitationVerdict(ctx, tok, dec, channel)
	if verdict == mediationPass {
		return false
	}
	rs.writeMediationDeny(ctx, w, req, tok, verdict, dec.Reason, channel, trace)
	return true
}

// auditElicitationTraced records one elicitation/sampling PEP decision.:
// the elicitation/sampling ALLOWS are evidence-enforced claims now; this helper
// carries the best-effort DENIAL/alert evidence only.
func (rs *ResourceServer) auditElicitationTraced(ctx context.Context, tok validatedToken, channel string, allowed bool, reason, trace string) {
	rs.auditor.Record(ctx, ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs, Tool: channel,
		Allowed: allowed, Reason: reason, TraceParent: trace,
		MCPTag: "MCP10", TokenBinding: tok.Binding, At: rs.clock(),
	}, sdk.EvidenceBinding{}) // best-effort denial/alert evidence (mediated allows are claims)
}

// publishRenderFindings publishes the inspector's render findings through the
// auditor as tool decisions (minimal data: severity+title, never HTML).
func (rs *ResourceServer) publishRenderFindings(ctx context.Context, tok validatedToken, uri string, findings []RenderInspectionFinding, trace string) {
	for _, f := range findings {
		rs.auditor.Record(ctx, ToolDecision{
			Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs, Tool: uri,
			Allowed: f.Severity != "high", MCPTag: "MCP10",
			TokenBinding: tok.Binding,
			Reason:       "render inspection finding [" + f.Severity + "]: " + f.Title,
			TraceParent:  trace, At: rs.clock(),
		}, sdk.EvidenceBinding{}) // legacy best-effort finding record
	}
}

// publishElicitationFindings publishes the mediator's findings.
func (rs *ResourceServer) publishElicitationFindings(ctx context.Context, tok validatedToken, findings []ElicitationInspectionFinding, trace string) {
	for _, f := range findings {
		rs.auditor.Record(ctx, ToolDecision{
			Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs, Tool: f.Channel,
			Allowed: f.Severity != "high", MCPTag: "MCP10",
			TokenBinding: tok.Binding,
			Reason:       "elicitation/sampling finding [" + f.Severity + "]: " + f.Title,
			TraceParent:  trace, At: rs.clock(),
		}, sdk.EvidenceBinding{}) // legacy best-effort finding record
	}
}

// emitRenderMeter is a no-op placeholder — the metering CostSample is emitted
// by the AGPL glue (cmd/olivares/mcpcontentgate.go), not the Apache connector.
func (rs *ResourceServer) emitRenderMeter(_ RenderInspectionMeter) {}

// emitElicitationMeter is a no-op placeholder — metering is in the AGPL glue.
func (rs *ResourceServer) emitElicitationMeter(_ ElicitationInspectionMeter) {}

// rawSHA256 returns the SHA-256 hex of raw JSON bytes; "" for nil/empty.
func rawSHA256(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return contentSHA256(raw)
}

// refuseCoreMRTR applies a discriminator-level classification on the generic relay route.
// It is a SEAM, and the reason it exists is the defect it closes (P-3, hub review
// 2026-08-01): the decision used to live inline as `switch ref { case A: … case B: … }`
// with no default, so a class the switch did not name fell through to the relay path,
// while elicitation (:4283) and sampling (:4478) asked ref.refused() and denied it.
// coreMRTRRefusal is an OPEN enum — this very stage widened it from two values to three —
// so consuming it by enumeration is fail-open by extension.
//
// Taking the classification as a PARAMETER is what makes that testable: a fourth class
// cannot be driven through ServeHTTP without adding it to production code, and a property
// that can only be checked by editing the thing under test is not checked. See
// TestGenericMRTRRefusesAnUnmappedClass.
func (rs *ResourceServer) refuseCoreMRTR(ctx context.Context, w http.ResponseWriter, req rsRequest,
	tok validatedToken, ref coreMRTRRefusal, trace string) bool {
	if !ref.refused() {
		return false
	}
	rs.auditTraced(ctx, tok, req.Method, "", false, ref.genericAuditSentence(), "", "MCP07", trace)
	rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, ref.wire())
	return true
}

// governGenericMRTR classifies an upstream result on the generic dispatch path
// and, when it is an InputRequiredResult, either mediates it (on the two
// sanctioned methods) or refuses it (on every other, which the specification
// forbids outright). It reports whether the response was already written.
//
// mrtr.mdx:184-192, verbatim: "Servers MAY send `InputRequiredResult` responses
// on the following client requests: prompts/get | Yes; resources/read | Yes;
// tools/call | Yes … Servers MUST NOT send `InputRequiredResult` responses on
// any other client requests."
//
// Two distinct duties, deliberately not conflated:
//
//   - SANCTIONED methods: the embedded inputRequests are the elicitation surface
//     arriving by a different door. They go through the same mediator, on the
//     same channel (ChannelMRTRInputRequest), as the tools/call path — so a
//     policy that stops a server asking for a password through elicitation/create
//     also stops it asking through prompts/get.
//   - EVERY other method: a result the spec forbids is not relayed. It is not a
//     policy decision about content, it is a non-conforming upstream, so it is an
//     upstream error rather than a mediation deny.
//
// No payload is persisted: only the input-request KEYS travel to the audit
// ledger (docs/SECURITY-HARDENING.md), which is what the existing MRTR mediation already does.
func (rs *ResourceServer) governGenericMRTR(ctx context.Context, w http.ResponseWriter, req rsRequest, tok validatedToken, result json.RawMessage, trace string) (decided bool) {
	// The MUST NOT first, from the SHARED matrix (stage-7 P0-3): this route used
	// to carry its own two-entry copy of the sanctioned list, which is exactly how
	// a matrix drifts per route. Every relay surface now answers the question from
	// mrtrSanctionedCoreMethods, directly or through its authority profile.
	// M-1R2: the two discriminator-level refusals keep their own records — only
	// the observed single exact literal is recorded as input_required; a
	// duplicated discriminator is recorded as the ambiguity it is.
	//
	// P-3 (2026-08-01): the DECISION asks refused(), exactly like the other two
	// consumers of this predicate — elicitation (:4283) and sampling (:4478) — and only
	// the audit SENTENCE switches on the class. This used to be a bare `switch ref` with
	// the two modeled cases and NO default, so anything else fell through to the relay
	// path below. coreMRTRRefusal is an open enum by design and this very stage widened
	// it from two values to three: a fourth class would have been refused by elicitation
	// and sampling and silently ADMITTED here — on prompts/get, resources/read,
	// completion/complete and the whole generic dispatch. Fail-open by extension, on the
	// one route whose own comment above records that it used to keep a private copy of
	// the sanctioned list.
	if rs.refuseCoreMRTR(ctx, w, req, tok, coreMRTRRefusalOn(req.Method, result), trace) {
		return true
	}
	cls := classifyMRTRResult(result, mrtrAuthorityCoreResult)
	if !cls.inputRequired {
		return false
	}
	entries, unreadable := cls.entries, cls.unreadable
	if unreadable {
		// The governed member cannot be projected, so it cannot be inspected —
		// and an uninspectable elicitation payload is exactly what must not be
		// relayed. Deny-closed, never a raw pass-through.
		//
		// M-1R3: the WIRE states the observed cause. A duplicated discriminator
		// reaches this leg on the sanctioned methods, and "input-required result"
		// would name a literal such a body may not carry; the genuine
		// input-required projection failure keeps its specific message.
		wire := "upstream returned an ambiguous input-required result"
		if duplicatedResultTypeDiscriminator(result) {
			wire = mrtrAmbiguousDiscriminatorWireMessage
		}
		rs.auditTraced(ctx, tok, req.Method, "", false,
			"upstream result selects the governed input-required contract — or a duplicated discriminator makes "+
				"that reading impossible to exclude — and is unreadable to the mediator; withheld", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadGateway, req.ID, rpcUpstreamError, wire)
		return true
	}
	// No settleChild: the generic dispatch (prompts/get, ordinary
	// resources/read) has NO parent claim yet — the declared stage-7 §A
	// residual 1 (gate.go's pending evidence contract for the generic
	// surfaces). Without a parent operation there is no child binding to
	// derive; when that residual closes, this call site inherits the same
	// settle-then-write closure as every governed surface.
	return rs.mediateMRTRResultEntries(ctx, w, req, tok, entries, trace, nil)
}
