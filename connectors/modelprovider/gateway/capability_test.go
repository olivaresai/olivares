// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestEffectiveGatewayMask(t *testing.T) {
	raw := fixtureBytes(t, "effective-caps.json")
	var fx struct {
		Route     string                     `json:"route"`
		Catalog   []modelprovider.Capability `json:"catalog"`
		Effective []modelprovider.Capability `json:"effective"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if Route(fx.Route) != RouteGateway {
		t.Fatalf("fixture route = %q, want %s", fx.Route, RouteGateway)
	}
	got := Effective(fx.Catalog, RouteGateway)
	if !reflect.DeepEqual(got, fx.Effective) {
		t.Fatalf("Effective = %v, want %v", got, fx.Effective)
	}
	for _, dropped := range []modelprovider.Capability{
		modelprovider.CapToolUse,
		modelprovider.CapVision,
		modelprovider.CapStructuredOutputs,
		modelprovider.CapExtendedThinking,
		modelprovider.CapPDF,
	} {
		if modelprovider.Has(got, dropped) {
			t.Fatalf("gateway mask still has %s", dropped)
		}
	}
	if !modelprovider.Has(got, modelprovider.CapStreaming) {
		t.Fatal("streaming was listed and must remain")
	}
	// The route does not grant a flag the catalog omitted.
	if extra := Effective([]modelprovider.Capability{modelprovider.CapToolUse}, RouteGateway); len(extra) != 0 {
		t.Fatalf("Effective granted %v from a catalog that has no streaming", extra)
	}
	// A non-gateway route is not this mask, including the native Claude API route.
	for _, route := range []Route{Route("claude-api"), Route("")} {
		if got := Effective(fx.Catalog, route); len(got) != 0 {
			t.Fatalf("Effective(%q) = %v, want an empty set", route, got)
		}
	}
}

func TestEffectiveTextRequestStillEncodes(t *testing.T) {
	req := sampleReq()
	if err := ValidateRequest(req); err != nil {
		t.Fatalf("text request: %v", err)
	}
	cases := []struct {
		name string
		body any
		want string
	}{
		{"openai", mustEncode(t, encodeOpenAI, req), `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"n":1}`},
		{"anthropic", mustEncode(t, encodeAnthropic, req), `{"model":"test-model","max_tokens":32,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"ollama", mustEncode(t, encodeOllama, req), `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":false,"options":{"num_predict":32}}`},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != tc.want {
			t.Fatalf("%s encode = %s, want %s", tc.name, raw, tc.want)
		}
	}
}

func TestEffectiveTextBodyStillSends(t *testing.T) {
	raw := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`)
	if err := Refuse(raw); err != nil {
		t.Fatalf("text body refused: %v", err)
	}
	doer, d := captureDriver(t)
	resp, err := OfferText(context.Background(), d, raw)
	if err != nil {
		t.Fatalf("OfferText: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("text = %q, want hello", resp.Text)
	}
	if doer.n != 1 {
		t.Fatalf("fake doer requests = %d, want 1", doer.n)
	}
	const want = `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"n":1}`
	if doer.buf.String() != want {
		t.Fatalf("outbound = %s, want %s", doer.buf.String(), want)
	}
	if strings.Contains(doer.buf.String(), "test-key") {
		t.Fatal("credential in outbound body")
	}
}

func TestRefuseToolBeforeSend(t *testing.T) {
	assertRefused(t, []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))
}

func TestRefuseImagePartBeforeSend(t *testing.T) {
	assertRefused(t, []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aaaa"}}]}]}`))
}

func TestRefuseJSONSchemaBeforeSend(t *testing.T) {
	assertRefused(t, []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}}`))
}

func TestRefuseResponsesItemBeforeSend(t *testing.T) {
	assertRefused(t, []byte(`{"model":"grok-4.7","input":[{"type":"reasoning","encrypted_content":"opaque-item"}]}`))
}

func TestRefuseDuplicateMessagesKey(t *testing.T) {
	// The later messages value must not hide the earlier reasoning turn.
	raw := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"assistant","content":"hidden","reasoning_content":"trace"}],"messages":[{"role":"user","content":"hi"}]}`)
	assertNotSent(t, raw)
}

func TestRefuseDuplicateMessageContentKey(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi","content":"secret"}]}`)
	assertNotSent(t, raw)
}

func TestRefuseToolRole(t *testing.T) {
	assertNotSent(t, []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"tool","content":"x"}]}`))
}

func assertNotSent(t *testing.T, raw []byte) {
	t.Helper()
	doer, driver := captureDriver(t)
	_, err := OfferText(context.Background(), driver, raw)
	if doer.n != 0 || doer.buf.Len() != 0 {
		t.Fatalf("fake doer saw %d request(s), body %s", doer.n, doer.buf.String())
	}
	wantNotImplemented(t, err)
	const leak = "modelgateway: not_implemented: gateway sends text only"
	if err.Error() != leak {
		t.Fatalf("error = %q, want the fixed refusal with no request body", err.Error())
	}
}

func assertRefused(t *testing.T, raw []byte) {
	t.Helper()
	wantNotImplemented(t, Refuse(raw))
	doer, d := captureDriver(t)
	_, err := OfferText(context.Background(), d, raw)
	wantNotImplemented(t, err)
	if doer.n != 0 || doer.buf.Len() != 0 {
		t.Fatalf("fake doer saw %d request(s), buffer %d bytes; refusal must happen before send", doer.n, doer.buf.Len())
	}
	const leak = "modelgateway: not_implemented: gateway sends text only"
	if err.Error() != leak {
		t.Fatalf("error = %q, want the fixed refusal with no request body", err.Error())
	}
}

func wantNotImplemented(t *testing.T, err error) {
	t.Helper()
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("want *Error, got %v", err)
	}
	if ge.Code != CodeNotImplemented {
		t.Fatalf("code = %s, want %s", ge.Code, CodeNotImplemented)
	}
	if ge.HTTPStatus != http.StatusNotImplemented {
		t.Fatalf("HTTPStatus = %d, want %d", ge.HTTPStatus, http.StatusNotImplemented)
	}
}

func mustEncode(t *testing.T, fn func(MessageRequest, bool) (any, error), req MessageRequest) any {
	t.Helper()
	body, err := fn(req, false)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// captureDoer is the fake upstream. It records the body and does not dial.
type captureDoer struct {
	n   int
	buf bytes.Buffer
}

func (c *captureDoer) Do(req *http.Request) (*http.Response, error) {
	c.n++
	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_, _ = c.buf.Write(b)
		_ = req.Body.Close()
	}
	const resp = `{"id":"chatcmpl-1","model":"test-model","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(resp)),
		Request:    req,
	}, nil
}

func captureDriver(t *testing.T) (*captureDoer, Driver) {
	t.Helper()
	doer := &captureDoer{}
	d, err := NewOpenAICompat(Config{
		BaseURL:    "http://gateway.test",
		Credential: "test-key",
		Doer:       doer,
	})
	if err != nil {
		t.Fatalf("driver: %v", err)
	}
	return doer, d
}
