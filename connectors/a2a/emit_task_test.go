// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// stubDoer is a deterministic, offline Transport: it serves the configured card on
// GET and the configured JSON-RPC response on POST, capturing both requests so a
// test can inspect headers and the exact body that left the process.
type stubDoer struct {
	cardBytes  []byte
	cardStatus int
	rpcBytes   []byte
	rpcStatus  int

	getReq    *http.Request
	postReq   *http.Request
	postBody  []byte
	postCount int
	getCount  int
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodGet:
		s.getCount++
		s.getReq = req
		return mkResp(orDefault(s.cardStatus, 200), s.cardBytes), nil
	case http.MethodPost:
		s.postCount++
		s.postReq = req
		if req.Body != nil {
			s.postBody, _ = io.ReadAll(req.Body)
		}
		return mkResp(orDefault(s.rpcStatus, 200), s.rpcBytes), nil
	default:
		return mkResp(405, nil), nil
	}
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func mkResp(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

// rpcTask is a v1.0 SendMessage response: the result is the SendMessageResponse
// ONEOF, so the Task arrives wrapped in a "task" member (a2a.proto).
func rpcTask(state string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"task-123","contextId":"ctx-9","status":{"state":"` + state + `"}}}}`)
}

const secretToken = "Bearer s3cr3t-out-of-band-token"

// newVerifiedTestClient mints an operator-anchored signed card and a Client whose
// Doer serves it; the agent's card url ("https://summarizer.example.com") becomes
// the SendMessage endpoint.
func newVerifiedTestClient(t *testing.T, rpc []byte) (*Client, *stubDoer, SendSpec) {
	t.Helper()
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard("summarizer"))
	doer := &stubDoer{cardBytes: card, rpcBytes: rpc}
	c := NewClient(EmitConfig{
		TrustJWKS: jwks,
		Headers:   map[string]string{"Authorization": secretToken},
		Doer:      doer,
	})
	return c, doer, SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "summarize the weekly report", Skill: "summarize"}
}

func TestSendMessage_VerifiedCardEmitsTask(t *testing.T) {
	c, doer, spec := newVerifiedTestClient(t, rpcTask("TASK_STATE_WORKING"))
	res, err := c.SendMessage(context.Background(), spec)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if doer.postCount != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", doer.postCount)
	}
	if res.TaskID != "task-123" || res.State != TaskStateWorking {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Terminal || res.Interrupt {
		t.Fatalf("WORKING is neither terminal nor interrupt: %+v", res)
	}
	if res.TrustLevel != string(trustVerified) {
		t.Fatalf("expected trust verified, got %q", res.TrustLevel)
	}
}

func TestSendMessage_UsesV1MethodRoleAndVersionHeader(t *testing.T) {
	c, doer, spec := newVerifiedTestClient(t, rpcTask("TASK_STATE_SUBMITTED"))
	if _, err := c.SendMessage(context.Background(), spec); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	var env struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			Message struct {
				Role     string           `json:"role"`
				Parts    []map[string]any `json:"parts"`
				Metadata map[string]any   `json:"metadata"`
			} `json:"message"`
		} `json:"params"`
	}
	if err := json.Unmarshal(doer.postBody, &env); err != nil {
		t.Fatalf("decode posted body: %v", err)
	}
	if env.JSONRPC != "2.0" {
		t.Fatalf("not JSON-RPC 2.0: %q", env.JSONRPC)
	}
	if env.Method != "SendMessage" {
		t.Fatalf("expected v1.0 method SendMessage, got %q", env.Method)
	}
	// ProtoJSON enum serialization (§5.5 / ADR-001): the v0.x "user" became ROLE_USER.
	if env.Params.Message.Role != "ROLE_USER" {
		t.Fatalf("expected role ROLE_USER, got %q", env.Params.Message.Role)
	}
	if env.Params.Message.Metadata["skill"] != "summarize" {
		t.Fatalf("skill metadata not carried: %+v", env.Params.Message.Metadata)
	}
	// A2A-Version is MANDATORY on every protocol request (§3.6.1) and carries
	// Major.Minor only (patch MUST NOT be used in negotiation).
	if got := doer.postReq.Header.Get("A2A-Version"); got != "1.0" {
		t.Fatalf("A2A-Version header = %q, want 1.0", got)
	}
	// JSON-RPC binding content type stays application/json (§9.1) — the a2a+json
	// preference is REST/webhook-scoped (#1753).
	if ct := doer.postReq.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// TestSendMessage_LegacyBareResultStillParses: a pre-1.0 peer answering with a bare
// Task result (no SendMessageResponse oneof wrapper) is parsed leniently.
func TestSendMessage_LegacyBareResultStillParses(t *testing.T) {
	bare := []byte(`{"jsonrpc":"2.0","id":"1","result":{"id":"task-9","contextId":"c","status":{"state":"TASK_STATE_WORKING"}}}`)
	c, _, spec := newVerifiedTestClient(t, bare)
	res, err := c.SendMessage(context.Background(), spec)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.TaskID != "task-9" || res.State != TaskStateWorking {
		t.Fatalf("legacy bare result mishandled: %+v", res)
	}
}

// TestSendMessage_MessageOneofIsSynchronousReply: a {"message": ...} oneof member is
// the agent answering without opening a Task — a completed synchronous reply.
func TestSendMessage_MessageOneofIsSynchronousReply(t *testing.T) {
	msg := []byte(`{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"done"},{"data":{"score":1}},{"file":{"uri":"artifact:report-1"}}]}}}`)
	c, _, spec := newVerifiedTestClient(t, msg)
	res, err := c.SendMessage(context.Background(), spec)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.State != TaskStateCompleted || !res.Terminal || res.TaskID != "m1" ||
		res.ResultKind != "message" || res.MessageID != "m1" || len(res.MessageDigest) != 64 {
		t.Fatalf("message reply mishandled: %+v", res)
	}
	if len(res.MessageParts) != 3 || res.MessageParts[0].Kind != "text" ||
		res.MessageParts[0].Text != "done" || len(res.MessageParts[0].Digest) != 64 ||
		res.MessageParts[1].Kind != "data" || res.MessageParts[1].Text != "" ||
		len(res.MessageParts[1].Digest) != 64 || res.MessageParts[2].Kind != "file" ||
		res.MessageParts[2].Reference != "artifact:report-1" || len(res.MessageParts[2].Digest) != 64 {
		t.Fatalf("message reply parts = %+v", res.MessageParts)
	}
}

func TestSendMessage_MessageOneofRequiresBoundedParts(t *testing.T) {
	msg := []byte(`{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[]}}}`)
	c, _, spec := newVerifiedTestClient(t, msg)
	if _, err := c.SendMessage(context.Background(), spec); !errors.Is(err, ErrAfterTransmit) {
		t.Fatalf("empty Message parts error = %v, want ErrAfterTransmit", err)
	}
}

func TestSendMessage_MessageOneofRequiresAgentRoleAndCanonicalIDs(t *testing.T) {
	for _, result := range []string{
		`{"messageId":"m1","contextId":"c","role":"ROLE_USER","parts":[{"text":"done"}]}`,
		`{"messageId":" m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"done"}]}`,
		`{"messageId":"m1","contextId":"c\nother","role":"ROLE_AGENT","parts":[{"text":"done"}]}`,
		`{"messageId":"m1","contextId":"c","taskId":" task-1","role":"ROLE_AGENT","parts":[{"text":"done"}]}`,
		`{"messageId":"m1","role":"ROLE_AGENT","parts":[{"text":"done"}]}`,
	} {
		body := []byte(`{"jsonrpc":"2.0","id":"1","result":{"message":` + result + `}}`)
		c, _, spec := newVerifiedTestClient(t, body)
		if _, err := c.SendMessage(context.Background(), spec); !errors.Is(err, ErrAfterTransmit) {
			t.Fatalf("invalid Message result %s error = %v, want ErrAfterTransmit", result, err)
		}
	}
}

func TestSendMessage_ResponseRejectsMultipleOneofValues(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t1","contextId":"c","status":{"state":"TASK_STATE_WORKING"}},"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"done"}]}}}`)
	c, _, spec := newVerifiedTestClient(t, body)
	if _, err := c.SendMessage(context.Background(), spec); !errors.Is(err, ErrAfterTransmit) {
		t.Fatalf("multiple oneof values error = %v, want ErrAfterTransmit", err)
	}
}

func TestSendMessage_MessagePartsArePlainAndReferencesAreSanitized(t *testing.T) {
	msg := []byte(`{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"line\r\nnext\u0000"},{"data":{"secret":"never projected"}},{"file":{"uri":"https://user:pass@example.test/report?q=secret#fragment"}}]}}}`)
	c, _, spec := newVerifiedTestClient(t, msg)
	res, err := c.SendMessage(context.Background(), spec)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := res.MessageParts[0].Text; got != "line\nnext" {
		t.Fatalf("sanitized text = %q, want plain normalized text", got)
	}
	if got := res.MessageParts[1]; got.Kind != "data" ||
		!strings.HasPrefix(got.Reference, "a2a-part:") || got.Text != "" {
		t.Fatalf("data projection = %+v, want digest-only reference", got)
	}
	if got := res.MessageParts[2]; got.Kind != "file" ||
		!strings.HasPrefix(got.Reference, "a2a-part:") ||
		strings.Contains(got.Reference, "secret") || strings.Contains(got.Reference, "pass") {
		t.Fatalf("file projection = %+v, want sanitized digest reference", got)
	}
}

// TestSendMessage_NoJSONRPCInterfaceRefused: a v1.0 card that declares interfaces but
// none with the JSONRPC binding must be refused — emitting to another binding's URL
// would act outside the card's declared surface.
func TestSendMessage_NoJSONRPCInterfaceRefused(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	base := baseCard("summarizer")
	base["supportedInterfaces"] = []any{
		map[string]any{"url": "https://summarizer.example.com/grpc", "protocolBinding": "GRPC", "protocolVersion": "1.0"},
	}
	card := signedCardBytes(t, priv, "k1", base)
	doer := &stubDoer{cardBytes: card, rpcBytes: rpcTask("TASK_STATE_WORKING")}
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	_, err := c.SendMessage(context.Background(), SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "JSONRPC") {
		t.Fatalf("expected a no-JSONRPC-interface refusal, got %v", err)
	}
	if doer.postCount != 0 {
		t.Fatalf("nothing may be emitted without a JSONRPC interface, got %d POSTs", doer.postCount)
	}
}

// TestSendMessage_TenantEchoedFromInterface: when the selected AgentInterface
// declares a tenant routing id, every request to it MUST carry that tenant
// (a2a.proto SendMessageRequest.tenant).
func TestSendMessage_TenantEchoedFromInterface(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	base := baseCard("summarizer")
	base["supportedInterfaces"] = []any{
		map[string]any{"url": "https://summarizer.example.com", "protocolBinding": "JSONRPC", "protocolVersion": "1.0", "tenant": "acme-eu"},
	}
	card := signedCardBytes(t, priv, "k1", base)
	doer := &stubDoer{cardBytes: card, rpcBytes: rpcTask("TASK_STATE_WORKING")}
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	if _, err := c.SendMessage(context.Background(), SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "go"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	var env struct {
		Params struct {
			Tenant string `json:"tenant"`
		} `json:"params"`
	}
	if err := json.Unmarshal(doer.postBody, &env); err != nil {
		t.Fatalf("decode posted body: %v", err)
	}
	if env.Params.Tenant != "acme-eu" {
		t.Fatalf("tenant = %q, want acme-eu (interface tenant must be echoed)", env.Params.Tenant)
	}
}

// TestSendMessage_LegacyCardFallsBackToTopLevelURL: a pre-1.0 card with no
// supportedInterfaces still resolves via its legacy top-level url.
func TestSendMessage_LegacyCardFallsBackToTopLevelURL(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", legacyCard("summarizer"))
	doer := &stubDoer{cardBytes: card, rpcBytes: rpcTask("TASK_STATE_WORKING")}
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	res, err := c.SendMessage(context.Background(), SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "go"})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.TaskID != "task-123" || doer.postCount != 1 {
		t.Fatalf("legacy card emission failed: %+v posts=%d", res, doer.postCount)
	}
}

// The enterprise MUST: credentials travel in HTTP headers, NEVER in the A2A payload.
func TestSendMessage_CredentialInHeaderNeverInPayload(t *testing.T) {
	c, doer, spec := newVerifiedTestClient(t, rpcTask("TASK_STATE_WORKING"))
	if _, err := c.SendMessage(context.Background(), spec); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := doer.postReq.Header.Get("Authorization"); got != secretToken {
		t.Fatalf("auth header not set on POST: %q", got)
	}
	if doer.getReq.Header.Get("Authorization") != secretToken {
		t.Fatalf("auth header not set on card GET")
	}
	if bytes.Contains(doer.postBody, []byte("s3cr3t")) {
		t.Fatalf("SECRET LEAK: credential found in A2A payload: %s", doer.postBody)
	}
}

func TestSendMessage_InterruptStatesAreActionableNotComplete(t *testing.T) {
	for _, st := range []string{"TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED"} {
		c, _, spec := newVerifiedTestClient(t, rpcTask(st))
		res, err := c.SendMessage(context.Background(), spec)
		if err != nil {
			t.Fatalf("%s: SendMessage: %v", st, err)
		}
		if !res.Interrupt {
			t.Fatalf("%s should be an interrupt (actionable), got %+v", st, res)
		}
		if res.Terminal {
			t.Fatalf("%s must not be terminal/completed, got %+v", st, res)
		}
	}
}

func TestSendMessage_UnspecifiedStateHandled(t *testing.T) {
	c, _, spec := newVerifiedTestClient(t, rpcTask("TASK_STATE_UNSPECIFIED"))
	res, err := c.SendMessage(context.Background(), spec)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.State != TaskStateUnspecified || res.Terminal || res.Interrupt {
		t.Fatalf("UNSPECIFIED mishandled: %+v", res)
	}
}

// Deny-closed: an unsigned card must NOT cause a Task to be emitted.
func TestSendMessage_UnsignedCardDeniesEmission(t *testing.T) {
	_, jwks := keypair(t, "k1")
	unsigned, _ := json.Marshal(baseCard("summarizer")) // no signatures field
	doer := &stubDoer{cardBytes: unsigned, rpcBytes: rpcTask("TASK_STATE_WORKING")}
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	_, err := c.SendMessage(context.Background(), SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "go"})
	if err == nil {
		t.Fatal("expected emission to be denied for unsigned card")
	}
	if doer.postCount != 0 {
		t.Fatalf("DENY-CLOSED VIOLATION: %d Task(s) emitted to an unverified agent", doer.postCount)
	}
}

// Deny-closed: a card signed by a key NOT in the operator anchor must NOT emit.
func TestSendMessage_WrongAnchorDeniesEmission(t *testing.T) {
	priv, _ := keypair(t, "k1")      // signs the card
	_, otherJWKS := keypair(t, "k2") // operator anchor holds a DIFFERENT key
	card := signedCardBytes(t, priv, "k1", baseCard("summarizer"))
	doer := &stubDoer{cardBytes: card, rpcBytes: rpcTask("TASK_STATE_WORKING")}
	c := NewClient(EmitConfig{TrustJWKS: otherJWKS, Doer: doer})
	_, err := c.SendMessage(context.Background(), SendSpec{AgentName: "summarizer", AgentURL: "https://summarizer.example.com", Text: "go"})
	if err == nil {
		t.Fatal("expected emission to be denied when card does not verify against the anchor")
	}
	if doer.postCount != 0 {
		t.Fatalf("DENY-CLOSED VIOLATION: emitted to an unverified agent")
	}
}

// HTTPS is a MUST: a non-https agent url is refused before any fetch/emit.
func TestSendMessage_RefusesNonHTTPS(t *testing.T) {
	_, jwks := keypair(t, "k1")
	doer := &stubDoer{cardBytes: []byte(`{}`), rpcBytes: rpcTask("TASK_STATE_WORKING")}
	c := NewClient(EmitConfig{TrustJWKS: jwks, Doer: doer})
	_, err := c.SendMessage(context.Background(), SendSpec{AgentName: "x", AgentURL: "http://insecure.example.com", Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https refusal, got %v", err)
	}
	if doer.getCount != 0 || doer.postCount != 0 {
		t.Fatalf("no request should be made to a non-https endpoint")
	}
}

// A JSON-RPC error response is a failure, not a fake success.
func TestSendMessage_RPCErrorIsFailure(t *testing.T) {
	rpcErr := []byte(`{"jsonrpc":"2.0","id":"1","error":{"code":-32602,"message":"invalid params"}}`)
	c, doer, spec := newVerifiedTestClient(t, rpcErr)
	_, err := c.SendMessage(context.Background(), spec)
	if err == nil {
		t.Fatal("expected a JSON-RPC error to surface as an error")
	}
	if doer.postCount != 1 {
		t.Fatalf("expected the POST to have been attempted once")
	}
}
