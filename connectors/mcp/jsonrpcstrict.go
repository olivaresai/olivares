// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// jsonrpcstrict.go (round-2) — STRICT validation of an upstream JSON-RPC 2.0
// response before its outcome may be settled `completed`. A permissive struct
// unmarshal is the laundering vector the evidence contract forbids: encoding/json
// matches members case-INSENSITIVELY and keeps the LAST duplicate, so
// {"JSONRPC":"2.0","ID":1,"RESULT":{}} or a duplicated member would "confirm" an
// outcome no compliant upstream produced. This parser reuses the same strict
// token-walk the request canonicalization uses (evidence.go): exactly ONE JSON
// value, duplicate object keys rejected at EVERY depth, no trailing data, and all
// reserved members looked up with EXACT casing.
//
// It is exported: the composition root's HTTP forwarder (cmd/olivares, AGPL)
// validates upstream responses with it — the import direction (cmd → connectors)
// is the legal one, and keeping the single strict-walk implementation here avoids
// a diverging copy.

// jsonrpcResponseReservedMembers are the JSON-RPC 2.0 response members this
// parser reads by exact name; case-variant aliases are refused (the same
// discipline as the request-side reserved keys).
var jsonrpcResponseReservedMembers = []string{"jsonrpc", "id", "result", "error"}

// StrictRPCError is the validated JSON-RPC error object of a strict response:
// code was present as an INTEGER and message as a STRING (exact types, exact
// member casing).
type StrictRPCError struct {
	Code    int64
	Message string
}

// ParseStrictJSONRPCResponse validates raw as EXACTLY one JSON-RPC 2.0 response
// correlated to wantID and returns the VERBATIM result member bytes, or the
// validated error object — never both. Any deviation returns a non-nil err and
// the caller must classify the dispatch outcome as UNKNOWN (the response cannot
// confirm anything):
//
//   - not exactly one JSON object (trailing data, non-object) → err;
//   - duplicate object keys at any depth, or a case-variant alias of a reserved
//     response member → err;
//   - jsonrpc member absent or not the string "2.0" → err;
//   - id member absent, non-integer, or ≠ wantID (a string "1" does NOT
//     correlate to a sent numeric 1) → err;
//   - not EXACTLY one of result|error → err;
//   - error member present but not an object with an integer "code" and a string
//     "message" (exact casing, exact types) → err.
func ParseStrictJSONRPCResponse(raw []byte, wantID int64) (json.RawMessage, *StrictRPCError, error) {
	v, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	if v.kind != canonObject {
		return nil, nil, fmt.Errorf("mcp: rpc response: not a JSON object")
	}
	for i := range v.obj {
		key := v.obj[i].key
		for _, reserved := range jsonrpcResponseReservedMembers {
			if key != reserved && strings.EqualFold(key, reserved) {
				return nil, nil, fmt.Errorf("mcp: rpc response: case-variant alias %q of reserved member %q", key, reserved)
			}
		}
	}
	ver := v.member("jsonrpc")
	if ver == nil || ver.val.kind != canonString || ver.val.str != "2.0" {
		return nil, nil, fmt.Errorf("mcp: rpc response: jsonrpc member missing or not \"2.0\"")
	}
	id := v.member("id")
	if id == nil || id.val.kind != canonNumber {
		return nil, nil, fmt.Errorf("mcp: rpc response: id member missing or not a number")
	}
	gotID, perr := strconv.ParseInt(id.val.num.String(), 10, 64)
	if perr != nil || gotID != wantID {
		return nil, nil, fmt.Errorf("mcp: rpc response: id does not correlate to the request")
	}
	result := v.member("result")
	rpcErr := v.member("error")
	switch {
	case result != nil && rpcErr != nil:
		return nil, nil, fmt.Errorf("mcp: rpc response: carries BOTH result and error")
	case result == nil && rpcErr == nil:
		return nil, nil, fmt.Errorf("mcp: rpc response: carries NEITHER result nor error")
	case rpcErr != nil:
		if rpcErr.val.kind != canonObject {
			return nil, nil, fmt.Errorf("mcp: rpc response: error member is not an object")
		}
		code := rpcErr.val.member("code")
		if code == nil || code.val.kind != canonNumber {
			return nil, nil, fmt.Errorf("mcp: rpc response: error.code missing or not a number")
		}
		codeInt, cerr := strconv.ParseInt(code.val.num.String(), 10, 64)
		if cerr != nil {
			return nil, nil, fmt.Errorf("mcp: rpc response: error.code is not an integer")
		}
		msg := rpcErr.val.member("message")
		if msg == nil || msg.val.kind != canonString {
			return nil, nil, fmt.Errorf("mcp: rpc response: error.message missing or not a string")
		}
		return nil, &StrictRPCError{Code: codeInt, Message: msg.val.str}, nil
	default:
		// Extract the VERBATIM result bytes (relayed downstream byte-exact, and
		// the settlement result digest binds them). The strict pass above proved
		// no exact-duplicate keys exist, so a map extraction cannot clobber.
		var members map[string]json.RawMessage
		if uerr := json.Unmarshal(raw, &members); uerr != nil { // unreachable after the strict pass
			return nil, nil, fmt.Errorf("mcp: rpc response: %w", uerr)
		}
		return members["result"], nil, nil
	}
}
