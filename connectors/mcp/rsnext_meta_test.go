// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRCRequestMetaIsValidatedServerSide pins the per-request protocol fields a
// 2026-07-28 request MUST carry, and the header↔body agreement about which
// revision is in play.
//
// The split-brain case is the security one, and it was measured before the fix:
// header 2026-07-28 with a body declaring protocolVersion "1900-01-01" was
// FORWARDED with HTTP 200. One layer authorized and routed on the header while
// the upstream executed a request that said it was a different revision — which
// is precisely what a governing gateway exists to prevent.
//
// The required/optional split follows basic/index.mdx exactly: protocolVersion
// and clientCapabilities are required (-32602 when absent), clientInfo is only
// SHOULD, so its absence must NOT be refused. Demanding clientInfo would reject
// conforming clients "specifically configured not to" send it.
func TestRCRequestMetaIsValidatedServerSide(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	call := func(t *testing.T, meta string) *httptest.ResponseRecorder {
		t.Helper()
		up := &fakeUpstream{}
		rs := newRSNext(t, jwks, up, &capturingAuditor{})
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","_meta":` + meta + `}}`
		req := nextReqRaw(token, "tools/call", "search", body)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		return w
	}

	full := `{"` + metaProtocolVersion + `":"` + revision20260728 + `","` + metaClientCapabilities + `":{}}`

	t.Run("conforming request is admitted", func(t *testing.T) {
		if w := call(t, full); w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("clientInfo is optional (SHOULD, not MUST)", func(t *testing.T) {
		// full already omits clientInfo; assert explicitly that this is fine.
		if w := call(t, full); w.Code != http.StatusOK {
			t.Errorf("a request without clientInfo was refused (%d %s); the spec marks it SHOULD, so refusing it rejects conforming clients", w.Code, w.Body.String())
		}
	})

	t.Run("missing protocolVersion is invalid params", func(t *testing.T) {
		w := call(t, `{"`+metaClientCapabilities+`":{}}`)
		if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcInvalidParams {
			t.Errorf("= %d/%s, want 400/%d (-32602 Invalid params)", w.Code, w.Body.String(), rpcInvalidParams)
		}
	})

	t.Run("missing clientCapabilities is invalid params", func(t *testing.T) {
		w := call(t, `{"`+metaProtocolVersion+`":"`+revision20260728+`"}`)
		if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcInvalidParams {
			t.Errorf("= %d/%s, want 400/%d (-32602 Invalid params)", w.Code, w.Body.String(), rpcInvalidParams)
		}
	})

	t.Run("header and body version must not diverge", func(t *testing.T) {
		w := call(t, `{"`+metaProtocolVersion+`":"1900-01-01","`+metaClientCapabilities+`":{}}`)
		if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcHeaderMismatch {
			t.Fatalf("= %d/%s, want 400/%d HeaderMismatch: a request whose header and body claim different revisions must never be forwarded", w.Code, w.Body.String(), rpcHeaderMismatch)
		}
	})
}
