// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMcpNameSentinelIsDecodedBeforeComparison pins the MUST the gateway was
// breaking. streamable-http.mdx §Value Encoding: "servers MUST decode an encoded
// `Mcp-Name` or `Mcp-Param-{Name}` value before comparing it to the
// corresponding request body value during Server Validation."
//
// Tool and prompt names are only SHOULD-constrained to header-safe characters,
// so a CONFORMING client carries anything else as `=?base64?{value}?=`. The
// gateway compared the raw header text, so those clients were refused with
// HeaderMismatch — a false rejection of a correct request. The sibling
// Mcp-Param-* path already decoded; only this one did not.
func TestMcpNameSentinelIsDecodedBeforeComparison(t *testing.T) {
	t.Parallel()
	const name = "café/informe анализ" // outside the header-safe set on purpose
	body := rsRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"` + name + `"}`)}

	enc := "=?base64?" + base64.StdEncoding.EncodeToString([]byte(name)) + "?="
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set(headerMcpMethod, "tools/call")
	r.Header.Set(headerMcpName, enc)

	rs := &ResourceServer{}
	if reason, ok := rs.headerBodyConsistent(r, body, true); !ok {
		t.Errorf("a conforming encoded Mcp-Name was refused (%s); the server MUST decode the sentinel before comparing", reason)
	}

	// A sentinel that decodes to something else must still be refused: decoding
	// is not a license to stop validating.
	r2 := httptest.NewRequest("POST", "/mcp", nil)
	r2.Header.Set(headerMcpMethod, "tools/call")
	r2.Header.Set(headerMcpName, "=?base64?"+base64.StdEncoding.EncodeToString([]byte("otherTool"))+"?=")
	if _, ok := rs.headerBodyConsistent(r2, body, true); ok {
		t.Error("an encoded Mcp-Name that decodes to a DIFFERENT name must be refused with HeaderMismatch")
	}

	// A malformed sentinel cannot be validated against the body, so it is a
	// rejection rather than a silent pass.
	r3 := httptest.NewRequest("POST", "/mcp", nil)
	r3.Header.Set(headerMcpMethod, "tools/call")
	r3.Header.Set(headerMcpName, "=?base64?not!valid!base64?=")
	if _, ok := rs.headerBodyConsistent(r3, body, true); ok {
		t.Error("a malformed =?base64?...?= Mcp-Name must be refused, never passed through unvalidated")
	}
}

// TestEncodedMcpNameSurvivesServeHTTP is the end-to-end guard the helper test
// could not give. The first version of this fix decoded only where the header is
// compared to the body, leaving the PRE-BODY gate working on the raw sentinel; a
// test that called the post-body helper directly could never see that.
//
// It uses resources/read on purpose. Mcp-Name carries the RESOURCE URI for that
// method (bodyMcpName), and a URI is exactly the value that needs encoding — a
// path with an accent or a space is ordinary. Tool names cannot exercise this:
// SEP-986 already constrains them to [A-Za-z0-9_.-], which is header-safe, so
// the encoded form never arises there. That constraint is why the first attempt
// at this test was rejected by the toolset validator, and it is worth recording:
// the decode matters for URIs and prompt names, not for tool names.
func TestEncodedMcpNameSurvivesServeHTTP(t *testing.T) {
	const uri = "file:///srv/informes/año 2026/resumen.txt"
	token, jwks := mintAccessToken(t, "k1", rsResource, "resources:read", validExp())
	up := &fakeUpstream{}
	rs := newRSNext(t, jwks, up, &capturingAuditor{})

	enc := "=?base64?" + base64.StdEncoding.EncodeToString([]byte(uri)) + "?="
	raw, err := json.Marshal(map[string]any{"uri": uri})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":` + string(raw) + `}`
	req := nextReq(token, "resources/read", "", body)
	req.Header.Set(headerMcpName, enc)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)

	if w.Code == http.StatusBadRequest && rpcErrorCode(t, w.Body.String()) == rpcHeaderMismatch {
		t.Fatalf("a conforming encoded Mcp-Name was refused as a header mismatch: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !up.called {
		t.Error("a conforming encoded request must reach the upstream")
	}

	// A malformed sentinel is a HEADER defect (400/-32020) and must be reported as
	// one, never mistaken for a value that simply does not match the body.
	req2 := nextReq(token, "resources/read", "", body)
	req2.Header.Set(headerMcpName, "=?base64?not!valid!?=")
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest || rpcErrorCode(t, w2.Body.String()) != rpcHeaderMismatch {
		t.Errorf("malformed sentinel = %d/%s, want 400/%d HeaderMismatch", w2.Code, w2.Body.String(), rpcHeaderMismatch)
	}
}

// TestEncodedMcpNameDecodedAtTheL7Gate covers the OTHER half, and the one a
// resources/read test cannot reach: the pre-body L7 gate resolves the server
// toolset from Mcp-Name, and only tools/call takes that path.
//
// SEP-986 already constrains tool names to [A-Za-z0-9_.-], so an encoded tool
// name is a client encoding a value it did not have to — permitted, and the
// spec's decode duty is unconditional ("Servers and intermediaries that need to
// inspect these values MUST decode them accordingly"). Before the fix the gate
// resolved the raw sentinel, found no such tool and answered 403 "tool not
// permitted": an authorization verdict invented from an encoding it declined to
// read. A malformed sentinel took the same path, reporting a header defect as a
// policy denial.
func TestEncodedMcpNameDecodedAtTheL7Gate(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRSNext(t, jwks, up, &capturingAuditor{})

	enc := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("search")) + "?="
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search"}}`
	req := nextReq(token, "tools/call", "search", body)
	req.Header.Set(headerMcpName, enc)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("the L7 gate refused an encoded Mcp-Name as an unknown tool (%s): it must decode before resolving the toolset", w.Body.String())
	}
	if w.Code != http.StatusOK || !up.called {
		t.Fatalf("status = %d, upstreamCalled = %v, body = %s", w.Code, up.called, w.Body.String())
	}

	// Malformed at the L7 gate: a header defect, not a policy denial.
	req2 := nextReq(token, "tools/call", "search", body)
	req2.Header.Set(headerMcpName, "=?base64?not!valid!?=")
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest || rpcErrorCode(t, w2.Body.String()) != rpcHeaderMismatch {
		t.Errorf("malformed sentinel at the L7 gate = %d/%s, want 400/%d HeaderMismatch (a 403 would report a header defect as an authorization decision)", w2.Code, w2.Body.String(), rpcHeaderMismatch)
	}
}
