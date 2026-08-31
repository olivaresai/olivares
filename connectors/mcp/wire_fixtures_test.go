// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type wireFixture struct {
	Revision       string            `json:"revision"`
	SourceURL      string            `json:"source_url"`
	RequestHeaders map[string]string `json:"request_headers"`
	Request        json.RawMessage   `json:"request"`
	HTTPStatus     int               `json:"http_status"`
	Response       json.RawMessage   `json:"response"`
}

func loadWireFixture(t *testing.T, name string) wireFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var fx wireFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	if fx.HTTPStatus == 0 {
		fx.HTTPStatus = http.StatusOK
	}
	return fx
}

func fixtureRequestMethod(t *testing.T, fx wireFixture) string {
	t.Helper()
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(fx.Request, &msg); err != nil {
		t.Fatalf("fixture request method: %v", err)
	}
	return msg.Method
}

func fixtureResponseResult(t *testing.T, fx wireFixture) json.RawMessage {
	t.Helper()
	var msg struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(fx.Response, &msg); err != nil {
		t.Fatalf("fixture response result: %v", err)
	}
	if len(msg.Result) == 0 {
		t.Fatalf("fixture %s has no result", fx.SourceURL)
	}
	return msg.Result
}

func assertJSONEqual(t *testing.T, gotRaw []byte, wantRaw json.RawMessage) {
	t.Helper()
	got := decodeJSONValue(t, gotRaw)
	want := decodeJSONValue(t, wantRaw)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", strings.TrimSpace(string(gotRaw)), strings.TrimSpace(string(wantRaw)))
	}
}

func decodeJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode json %s: %v", string(raw), err)
	}
	return v
}

func assertFixtureHeaders(t *testing.T, h http.Header, fx wireFixture) {
	t.Helper()
	for name, want := range fx.RequestHeaders {
		if got := h.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func assertFixtureMetaKeys(t *testing.T, body []byte) {
	t.Helper()
	var msg struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("decode request for _meta: %v", err)
	}
	want := []string{metaProtocolVersion, metaClientInfo, metaClientCapabilities}
	if len(msg.Params.Meta) != len(want) {
		t.Fatalf("_meta keys = %v, want exactly %v", keysOf(msg.Params.Meta), want)
	}
	for _, key := range want {
		if _, ok := msg.Params.Meta[key]; !ok {
			t.Fatalf("_meta missing byte-exact key %q in %v", key, msg.Params.Meta)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func writeFixtureResponse(t *testing.T, w http.ResponseWriter, fx wireFixture) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(fx.HTTPStatus)
	_, _ = w.Write(fx.Response)
}

func TestWireFixturesLegacyIntrospection(t *testing.T) {
	initFx := loadWireFixture(t, "2025-11-25_initialize.json")
	listFx := loadWireFixture(t, "2025-11-25_tools_list.json")
	var calls []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var msg struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		calls = append(calls, msg.Method)
		switch msg.Method {
		case "initialize":
			assertJSONEqual(t, body, initFx.Request)
			writeFixtureResponse(t, w, initFx)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			assertFixtureHeaders(t, r.Header, listFx)
			assertJSONEqual(t, body, listFx.Request)
			writeFixtureResponse(t, w, listFx)
		default:
			t.Fatalf("unexpected legacy method %q", msg.Method)
		}
	}))
	defer srv.Close()

	cat, err := introspect(context.Background(), serverSpec{Name: "legacy-fixture", URL: srv.URL, NextRevision: boolRef(false)})
	if err != nil {
		t.Fatalf("legacy fixture introspect: %v", err)
	}
	if cat.server.ProtocolVersion != revision20251125 || len(cat.tools) != 1 || cat.tools[0].Name != "search" {
		t.Fatalf("legacy catalog = version %q tools %+v", cat.server.ProtocolVersion, cat.tools)
	}
	if !reflect.DeepEqual(calls, []string{"initialize", "notifications/initialized", "tools/list"}) {
		t.Fatalf("legacy calls = %v", calls)
	}
}

func TestWireFixturesStatelessIntrospection(t *testing.T) {
	discoverFx := loadWireFixture(t, "2026-07-28_server_discover.json")
	listFx := loadWireFixture(t, "2026-07-28_tools_list_meta.json")
	var calls []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var msg struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		calls = append(calls, msg.Method)
		switch msg.Method {
		case methodServerDiscover:
			assertFixtureHeaders(t, r.Header, discoverFx)
			assertFixtureMetaKeys(t, body)
			assertJSONEqual(t, body, discoverFx.Request)
			writeFixtureResponse(t, w, discoverFx)
		case "tools/list":
			assertFixtureHeaders(t, r.Header, listFx)
			if got := r.Header.Get(headerMcpName); got != "" {
				t.Fatalf("tools/list must not carry Mcp-Name, got %q", got)
			}
			assertFixtureMetaKeys(t, body)
			assertJSONEqual(t, body, listFx.Request)
			writeFixtureResponse(t, w, listFx)
		default:
			t.Fatalf("unexpected stateless method %q", msg.Method)
		}
	}))
	defer srv.Close()

	cat, err := introspect(context.Background(), serverSpec{Name: "rc-fixture", URL: srv.URL, NextRevision: boolRef(true)})
	if err != nil {
		t.Fatalf("stateless fixture introspect: %v", err)
	}
	if !cat.nextRevision || cat.server.ProtocolVersion != revision20260728 ||
		!reflect.DeepEqual(cat.supportedVersions, []string{revision20251125, revision20260728}) {
		t.Fatalf("stateless catalog revision fields: next=%v version=%q supported=%v", cat.nextRevision, cat.server.ProtocolVersion, cat.supportedVersions)
	}
	if len(cat.tools) != 1 || cat.tools[0].Name != "search" {
		t.Fatalf("stateless tools = %+v", cat.tools)
	}
	// The fixture carries its identity where DiscoverResult defines it —
	// result._meta["io.modelcontextprotocol/serverInfo"] — and the catalog must
	// project all three members. Asserting only the revision/tools left the
	// fixture free to keep a TOP-LEVEL serverInfo the schema does not define:
	// the catalog then reported the zero identity and the test stayed green,
	// which is exactly how the wrong shape survived a full revision.
	if want := (serverInfo{Name: "fixture-rc", Title: "Fixture RC", Version: "2.0.0"}); cat.server.ServerInfo != want {
		t.Fatalf("stateless identity = %+v, want %+v (from result._meta[%q])", cat.server.ServerInfo, want, metaServerInfo)
	}
	if !reflect.DeepEqual(calls, []string{methodServerDiscover, "tools/list"}) {
		t.Fatalf("stateless calls = %v", calls)
	}
}

type fixtureUpstream struct {
	result json.RawMessage
	called bool
	got    UpstreamRequest
}

func (u *fixtureUpstream) Forward(_ context.Context, req UpstreamRequest) (UpstreamResult, error) {
	u.called = true
	u.got = req
	return UpstreamResult{Result: u.result, State: DispatchCompleted}, nil
}

func newFixtureRS(t *testing.T, jwks []byte, up Upstream, mode string) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		Gate:                 fakeToolGate{StatusApproved},
		Upstream:             up,
		Auditor:              &capturingAuditor{},
		Clock:                rsClock,
		RevisionMode:         mode,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

func requestFromFixture(t *testing.T, token string, fx wireFixture) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, rsResource, bytes.NewReader(fx.Request))
	req.Header.Set("Authorization", "Bearer "+token)
	for name, value := range fx.RequestHeaders {
		req.Header.Set(name, value)
	}
	return req
}

func TestWireFixturesResourceServerPEPBothRevisions(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct {
		name string
		file string
		mode string
	}{
		{"legacy", "2025-11-25_tools_call_gated.json", revisionModeLegacy},
		{"rc", "2026-07-28_tools_call_gated.json", revisionModeRCStrict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := loadWireFixture(t, tc.file)
			up := &fixtureUpstream{result: fixtureResponseResult(t, fx)}
			rs := newFixtureRS(t, jwks, up, tc.mode)
			rec := httptest.NewRecorder()
			rs.ServeHTTP(rec, requestFromFixture(t, token, fx))
			if rec.Code != http.StatusOK {
				t.Fatalf("PEP %s status = %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
			if !up.called || up.got.Method != "tools/call" || toolNameFromParams(t, up.got.Params) != "search" {
				t.Fatalf("upstream %s = called:%v req:%+v", tc.name, up.called, up.got)
			}
			assertJSONEqual(t, rec.Body.Bytes(), fx.Response)
		})
	}
}

func toolNameFromParams(t *testing.T, raw []byte) string {
	t.Helper()
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode upstream params: %v", err)
	}
	return p.Name
}

func TestWireFixturesResourceServerHeaderMismatch(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read tools:admin", validExp())
	fx := loadWireFixture(t, "2026-07-28_header_mismatch.json")
	up := &fixtureUpstream{result: json.RawMessage(`{}`)}
	rs := newFixtureRS(t, jwks, up, revisionModeRCStrict)
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, requestFromFixture(t, token, fx))
	if rec.Code != fx.HTTPStatus {
		t.Fatalf("HeaderMismatch status = %d body=%s", rec.Code, rec.Body.String())
	}
	if up.called {
		t.Fatal("header mismatch must not reach upstream")
	}
	if got := rpcErrorCode(t, rec.Body.String()); got != rpcHeaderMismatch {
		t.Fatalf("HeaderMismatch code = %d, want %d", got, rpcHeaderMismatch)
	}
}

func TestWireFixturesUnsupportedProtocolVersion(t *testing.T) {
	fx := loadWireFixture(t, "2026-07-28_unsupported_protocol_version.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		assertFixtureHeaders(t, r.Header, fx)
		assertJSONEqual(t, body, fx.Request)
		writeFixtureResponse(t, w, fx)
	}))
	defer srv.Close()

	tr, err := newStatelessHTTPTransport(serverSpec{Name: "rc-error", URL: srv.URL})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	params := map[string]any{
		"_meta": map[string]any{
			metaProtocolVersion:    "1900-01-01",
			metaClientInfo:         map[string]any{"name": clientName, "version": clientVersion},
			metaClientCapabilities: map[string]any{},
		},
	}
	_, err = tr.roundTrip(context.Background(), rpcRequest{Method: methodServerDiscover, Params: params})
	var rpc *rpcError
	if !errors.As(err, &rpc) || rpc.Code != rpcUnsupportedProtocolVersion {
		t.Fatalf("unsupported version error = %v, want -32022", err)
	}
	if got := unsupportedVersionDetail(rpc); !reflect.DeepEqual(got, []string{revision20260728, revision20251125}) {
		t.Fatalf("supported = %v", got)
	}
}

func TestWireFixturesTaskAndInputRequiredResults(t *testing.T) {
	taskFx := loadWireFixture(t, "2026-07-28_tools_call_task.json")
	task, ok := taskFromResult(fixtureResponseResult(t, taskFx))
	if !ok || task.TaskID != "task-fixture-1" || task.Status != taskStatusWorking {
		t.Fatalf("task handle fixture parsed as ok=%v task=%+v", ok, task)
	}

	inputFx := loadWireFixture(t, "2026-07-28_tools_call_input_required.json")
	rt, err := checkResultEnvelope("tools/call", fixtureResponseResult(t, inputFx))
	var ir *errInputRequired
	if rt != resultTypeInputRequired || !errors.As(err, &ir) || len(ir.keys) != 1 || ir.keys[0] != "approval" {
		t.Fatalf("input_required fixture = rt=%q err=%v keys=%v", rt, err, ir)
	}

	// The embedded request must itself be a CONFORMING ElicitRequest, not merely
	// present under a key with the right name. schema.ts ElicitRequestFormParams
	// makes `requestedSchema` REQUIRED (no `?`) alongside `message`; the fixture
	// shipped without it and this test could not tell, because it read the keys of
	// `inputRequests` and never once looked inside `approval.params`. A conformance
	// fixture that is itself non-conforming teaches the wrong wire shape to every
	// reader who copies it.
	var envelope struct {
		InputRequests map[string]struct {
			Method string `json:"method"`
			Params struct {
				Mode            string          `json:"mode"`
				Message         string          `json:"message"`
				RequestedSchema json.RawMessage `json:"requestedSchema"`
			} `json:"params"`
		} `json:"inputRequests"`
	}
	if err := json.Unmarshal(fixtureResponseResult(t, inputFx), &envelope); err != nil {
		t.Fatalf("decode input_required fixture: %v", err)
	}
	approval, ok := envelope.InputRequests["approval"]
	if !ok {
		t.Fatalf("the fixture lost its `approval` input request")
	}
	if approval.Method != "elicitation/create" {
		t.Errorf("embedded method = %q, want elicitation/create", approval.Method)
	}
	if approval.Params.Message == "" {
		t.Errorf("ElicitRequestFormParams.message is required and is empty")
	}
	if approval.Params.Mode == "form" && len(approval.Params.RequestedSchema) == 0 {
		t.Errorf("form-mode elicitation without requestedSchema: schema.ts marks it REQUIRED, so this fixture would teach a non-conforming request")
	}
}

func ioReadAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
