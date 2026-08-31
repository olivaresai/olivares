// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// jsonRPCVersion is the only JSON-RPC version MCP uses.
const jsonRPCVersion = "2.0"

// rpcRequest is a JSON-RPC 2.0 request. A request carries an id; a notification
// (no response expected) is sent with isNotification set so id is omitted.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`

	isNotification bool `json:"-"`
}

// marshal encodes the request, omitting id entirely for a notification (a
// notification with an id would be treated as a callable request by the server).
func (r rpcRequest) marshal() ([]byte, error) {
	if r.isNotification {
		return json.Marshal(struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  any    `json:"params,omitempty"`
		}{JSONRPC: jsonRPCVersion, Method: r.Method, Params: r.Params})
	}
	r.JSONRPC = jsonRPCVersion
	return json.Marshal(r)
}

// rpcMessage is a permissive view of any inbound JSON-RPC message: a response
// (id + result/error), a server notification (method + params, no id) or a
// server request (method + id). The 2025-11-25 path only consumes responses;
// the stateless subscriptions/listen stream also consumes notifications,
// demultiplexed by the subscriptionId `_meta` tag in Params.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// isResponseTo reports whether m is the response to request id.
func (m rpcMessage) isResponseTo(id int64) bool {
	return m.ID != nil && *m.ID == id && m.Method == ""
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Error renders the error for Go error handling.
func (e *rpcError) Error() string {
	return fmt.Sprintf("mcp: rpc error %d: %s", e.Code, e.Message)
}

// --- dual-revision error semantics -------------------------------------
//
// The 2026-07-28 frozen RC re-maps resource-not-found from the MCP-custom
// -32002 to the JSON-RPC standard -32602 Invalid params (SEP-2164), reserves
// -32020..-32099 for spec-assigned codes, and assigns -32021
// MissingRequiredClientCapability plus -32022 UnsupportedProtocolVersion. The
// Streamable HTTP transport additionally assigns -32020 HeaderMismatch.
// classifyNotFound is the single chokepoint that reads "not found" across both
// revisions, so callers never hard-code a code that means something else on the
// other side of the flag.

// Error codes whose meaning differs (or is new) across the two revisions.
const (
	// rpcNotFoundLegacy: resource not found in revisions ≤ 2025-11-25 — the
	// MCP-custom code the PEP retires. READ-ONLY: it classifies what a legacy
	// server sends, and nothing in this package emits it (2026-07-28 says a
	// server MUST NOT). It used to share its value with the PEP's own
	// upstream-error code, "disambiguated by direction"; that coincidence is
	// gone — rpcUpstreamError is -31002 (rs.go), outside the reserved range.
	rpcNotFoundLegacy = -32002

	// --- evidence-gate refusal codes (implementation-defined) --------------
	//
	// Allocated OUTSIDE the JSON-RPC reserved range (-32768..-32000) entirely,
	// which is what MCP 2026-07-28 instructs for codes it does not define
	// (basic/index.mdx:153-155). The earlier -3201x values sat inside the
	// -32000..-32019 LEGACY sub-range, which the same section says new
	// implementations SHOULD NOT use at all; the trailing digits are preserved so
	// existing runbooks and log greps still map.

	// rpcEvidenceUnavailable: the mandatory evidence anchor could not be obtained
	// — the request was NOT forwarded. HTTP 503 + Retry-After (retryable: the
	// fault is infrastructure, not the request).
	rpcEvidenceUnavailable = -31010
	// rpcEvidenceRebind: the supplied operation key is already bound to a
	// DIFFERENT effect (same OperationID, different EffectDigest). HTTP 409,
	// non-retryable — a new key names a new operation.
	rpcEvidenceRebind = -31011
	// rpcOperationRecorded: the exact operation is already claimed/settled (HTTP
	// 409: recorded state returned, never a re-forward) or its outcome is
	// indeterminate (HTTP 503: settled unknown / settlement withheld — it will
	// not be forwarded again).
	rpcOperationRecorded = -31012
	// rpcMissingClientCapability (RC): the request used a capability/extension it
	// did not declare; data.requiredCapabilities lists what is missing.
	rpcMissingClientCapability = -32021
	// rpcUnsupportedProtocolVersion (RC): the server does not implement the
	// requested protocol version; data.supported lists what it does.
	rpcUnsupportedProtocolVersion = -32022

	// rpcUnsupportedProtocolVersionPreFreeze is accepted during the frozen-RC
	// transition because pre-freeze draft servers emitted -32004 before the code
	// moved into the -32020..-32099 spec-assigned range.
	rpcUnsupportedProtocolVersionPreFreeze = -32004
)

// classifyNotFound reports whether err is a resource-not-found JSON-RPC error
// under the given revision mode. Legacy (≤2025-11-25): the MCP-custom -32002.
// Next revision (RC): -32602 Invalid params — which the RC deliberately merged
// with malformed-params, so "not found" is no longer distinguishable from a bad
// request by code alone; the merge is the spec's choice, surfaced as-is.
func classifyNotFound(err error, nextRevision bool) bool {
	var rpc *rpcError
	if !errors.As(err, &rpc) {
		return false
	}
	if nextRevision {
		return rpc.Code == -32602
	}
	return rpc.Code == rpcNotFoundLegacy
}

// unsupportedVersionDetail extracts the data.supported list of an
// UnsupportedProtocolVersionError, so an introspection failure can say WHICH
// revisions the server speaks (e.g. a server not yet on the RC). During the
// frozen-RC transition it accepts both the assigned -32022 and the pre-freeze
// draft -32004. It returns nil when err is not that error.
func unsupportedVersionDetail(err error) []string {
	var rpc *rpcError
	if !errors.As(err, &rpc) || !isUnsupportedProtocolVersionCode(rpc.Code) {
		return nil
	}
	var data struct {
		Supported []string `json:"supported"`
	}
	if json.Unmarshal(rpc.Data, &data) != nil {
		return nil
	}
	return data.Supported
}

func isUnsupportedProtocolVersionCode(code int) bool {
	return code == rpcUnsupportedProtocolVersion || code == rpcUnsupportedProtocolVersionPreFreeze
}
