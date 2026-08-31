// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestUnsupportedProtocolVersionMatchesTheConformanceFixture closes the gap the
// stage-6 spec diff found: the repository SHIPPED a conformance fixture
// (testdata/2026-07-28_unsupported_protocol_version.json) encoding the correct
// answer — 400, -32022, data.requested + data.supported — while the server
// funneled that case into HeaderMismatch (-32020) with no data. Fixture and
// server contradicted each other and nothing compared them, so the defect was
// invisible to a green suite.
//
// The spec's requirement is that a client be able to RETRY: without
// data.supported it has no list to fall back to, which is the whole purpose of
// UnsupportedProtocolVersionError.
func TestUnsupportedProtocolVersionMatchesTheConformanceFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "2026-07-28_unsupported_protocol_version.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx struct {
		RequestHeaders map[string]string `json:"request_headers"`
		HTTPStatus     int               `json:"http_status"`
		Response       struct {
			Error struct {
				Code int `json:"code"`
				Data struct {
					Requested string `json:"requested"`
				} `json:"data"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	rs := newRSDual(t, jwks, &fakeUpstream{}, &capturingAuditor{})
	req := toolsCallReq(token, "search", "{}")
	req.Header.Set(headerMCPProtocolVersion, fx.RequestHeaders["MCP-Protocol-Version"])
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)

	if w.Code != fx.HTTPStatus {
		t.Errorf("status = %d, fixture says %d", w.Code, fx.HTTPStatus)
	}
	var got struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Requested string   `json:"requested"`
				Supported []string `json:"supported"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if got.Error.Code != fx.Response.Error.Code {
		t.Errorf("error code = %d, fixture says %d", got.Error.Code, fx.Response.Error.Code)
	}
	if got.Error.Data.Requested != fx.Response.Error.Data.Requested {
		t.Errorf("data.requested = %q, fixture says %q", got.Error.Data.Requested, fx.Response.Error.Data.Requested)
	}
	// The schema makes data.supported obligatory, and it must be non-empty or the
	// client has nothing to retry with.
	if len(got.Error.Data.Supported) == 0 {
		t.Error("data.supported is empty: the client is left with no version to retry")
	}
	for _, v := range got.Error.Data.Supported {
		if v == fx.Response.Error.Data.Requested {
			t.Errorf("data.supported advertises %q, the very version just refused", v)
		}
	}
}

// TestSupportedRevisionsReflectTheMode pins that the advertised list is what the
// server will actually accept. Advertising the whole timeline from RC-strict
// would send a conforming client to retry a revision that mode also refuses —
// a well-behaved infinite loop caused by our own answer.
func TestSupportedRevisionsReflectTheMode(t *testing.T) {
	t.Parallel()
	strict := &ResourceServer{revisionMode: revisionModeRCStrict}
	if got := strict.supportedRevisions(); len(got) != 1 || got[0] != revision20260728 {
		t.Errorf("rc-strict supported = %v, want exactly [%s]", got, revision20260728)
	}
	dual := &ResourceServer{revisionMode: revisionModeDual}
	if len(dual.supportedRevisions()) < 2 {
		t.Errorf("dual supported = %v, want the accepted timeline", dual.supportedRevisions())
	}
}
