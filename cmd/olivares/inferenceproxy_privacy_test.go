// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestProxySignedLedgerStoresFingerprintsNotInferenceContent attacks the final
// proxy-to-ledger hop with distinct request and response canaries. The evidence
// must be Ed25519-signed and commit to both byte fingerprints without persisting
// either raw body in the event or its canonical metadata.
func TestProxySignedLedgerStoresFingerprintsNotInferenceContent(t *testing.T) {
	const (
		requestCanary  = "prompt alice.s373@example.com secret=REQUEST-AUDIT-CANARY"
		responseCanary = "completion SSN 078-05-1120 RESPONSE-AUDIT-CANARY"
		requestRef     = "0123456789abcdef0123456789abcdef"
	)
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	ipx := inferenceproxy.New()
	st, tenant := provisionTenantWithConfig(t, ipx, "", store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	})

	reqBody := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` + requestCanary + `"}]}`)
	respBody := []byte(`{"content":[{"type":"text","text":"` + responseCanary + `"}]}`)
	reqSHA := sha256.Sum256(reqBody)
	respSHA := sha256.Sum256(respBody)
	out := claudeapi.ProxyForwardResult{
		Response: claudeapi.MessageResponse{
			Model:   "claude-opus-4-8",
			Content: []claudeapi.ContentBlock{claudeapi.TextBlock(responseCanary)},
		},
		ReqSHA: reqSHA[:], ReqBytes: int64(len(reqBody)),
		RespSHA: respSHA[:], RespBytes: int64(len(respBody)), UpstreamStatus: 200,
	}
	sess := &proxySession{
		tenant: tenant, actor: "user:u1", actorKind: "user",
		modelRef: "claude-opus-4-8", requestRef: requestRef,
	}
	d := &inferenceProxyDecider{
		surface: "direct", store: st,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	d.anchorOutcome(ctx, sess, out, "allow")

	wantHash := proxyOutcomeHash(
		requestRef, "direct", tenant.String(), sess.modelRef, "allow",
		out.ReqBytes, out.RespBytes, reqSHA[:], respSHA[:], sess.inputDigest, sess.effectiveDigest,
	)
	found := 0
	var sigReport audit.EventSigReport
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose canonical metadata")
		}
		if err := walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != "inference.proxy.recorded" {
				return nil
			}
			found++
			if !bytes.Equal(ev.PayloadHash, wantHash) {
				t.Errorf("payload fingerprint = %x, want %x", ev.PayloadHash, wantHash)
			}
			if len(ev.PayloadHash) != sha256.Size {
				t.Errorf("payload fingerprint length = %d, want %d", len(ev.PayloadHash), sha256.Size)
			}
			if len(ev.Sig) == 0 {
				t.Error("inference.proxy.recorded event is not Ed25519-signed")
			}
			blob, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				return marshalErr
			}
			persisted := string(blob) + meta
			for _, canary := range []string{requestCanary, responseCanary} {
				if strings.Contains(persisted, canary) {
					t.Fatalf("signed proxy ledger leaked raw inference content %q: %s", canary, persisted)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		var verifyErr error
		sigReport, verifyErr = audit.VerifyEvents(ctx, sc.Audit(), pub)
		return verifyErr
	})
	if err != nil {
		t.Fatalf("inspect proxy ledger: %v", err)
	}
	if found != 1 {
		t.Fatalf("inference.proxy.recorded events = %d, want 1", found)
	}
	if !sigReport.OK || sigReport.Events == 0 || sigReport.Events != sigReport.Signed {
		t.Fatalf("signed audit verification failed: %+v", sigReport)
	}
}

type privacyTraceDecider struct{}

func (privacyTraceDecider) Authorize(_ context.Context, req claudeapi.MessageRequest, _ string) claudeapi.ProxyDecision {
	return claudeapi.ProxyDecision{Allow: true, Request: req, Session: struct{}{}}
}

func (privacyTraceDecider) Finalize(context.Context, any, claudeapi.ProxyForwardResult) claudeapi.ProxyResponseVerdict {
	return claudeapi.ProxyResponseVerdict{}
}

func (privacyTraceDecider) AuthorizeBatch(context.Context, []claudeapi.BatchRequest, string) claudeapi.ProxyBatchDecision {
	return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusForbidden}
}

func (privacyTraceDecider) FinalizeBatch(context.Context, any, claudeapi.ProxyBatchForwardResult) {}

// TestDedicatedProxyOTelIsHashOnly pushes raw PII and secrets through the same
// traced Messages proxy handler assembled in production: method-only ingress span
// plus the bounded GenAI transport. The exported OTLP must contain the request and
// response SHA-256 fingerprints, but none of either body or the bearer credential.
func TestDedicatedProxyOTelIsHashOnly(t *testing.T) {
	const (
		requestCanary  = "alice.s373@example.com secret=REQUEST-OTLP-CANARY"
		responseCanary = "SSN 078-05-1120 RESPONSE-OTLP-CANARY"
		bearerCanary   = "BEARER-OTLP-CANARY"
	)
	upstreamBody := `{"id":"msg_privacy","type":"message","role":"assistant",` +
		`"model":"claude-opus-4-8","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"` + responseCanary + `"}],` +
		`"usage":{"input_tokens":5,"output_tokens":2}}`
	var upstreamRequest []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequest, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	var (
		exportMu sync.Mutex
		exports  []*collectortracepb.ExportTraceServiceRequest
	)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "traces") {
			var req collectortracepb.ExportTraceServiceRequest
			if err := proto.Unmarshal(body, &req); err != nil {
				t.Errorf("decode OTLP traces: %v", err)
			} else {
				exportMu.Lock()
				exports = append(exports, &req)
				exportMu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	tracer, err := obstrace.New(context.Background(), obstrace.Config{
		Enabled: true, Endpoint: collector.URL, Protocol: obstrace.ProtocolHTTP,
		SampleRatio: 1, ServiceName: "proxy-privacy-test", ServiceVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{
		BaseURL: upstream.URL, APIKey: "operator-key", Gateway: sdkmodel.GatewayDirect,
		Doer: tracer.AnthropicHTTPClient(nil),
	})
	proxy := claudeapi.NewMessagesProxy(inf, privacyTraceDecider{}, nil, time.Now)
	handler := tracer.HTTPMiddleware(proxy)
	inboundBody := `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"` + requestCanary + `"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(inboundBody))
	req.Header.Set("Authorization", "Bearer "+bearerCanary)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), responseCanary) {
		t.Fatalf("proxy response = %d %s", rec.Code, rec.Body.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tracer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("flush OTLP: %v", err)
	}

	exportMu.Lock()
	gotExports := append([]*collectortracepb.ExportTraceServiceRequest(nil), exports...)
	exportMu.Unlock()
	if len(gotExports) == 0 {
		t.Fatal("dedicated proxy exported no OTLP trace records")
	}
	wantRequestHash := hashBytesHex(upstreamRequest)
	wantResponseHash := hashBytesHex([]byte(upstreamBody))
	genAISpans := 0
	for _, export := range gotExports {
		blob, err := protojson.Marshal(export)
		if err != nil {
			t.Fatal(err)
		}
		for _, canary := range []string{requestCanary, responseCanary, bearerCanary} {
			if strings.Contains(string(blob), canary) {
				t.Fatalf("dedicated proxy OTLP leaked raw privacy canary %q: %s", canary, blob)
			}
		}
		for _, resourceSpans := range export.ResourceSpans {
			for _, scopeSpans := range resourceSpans.ScopeSpans {
				for _, span := range scopeSpans.Spans {
					if !strings.HasPrefix(span.Name, "chat ") {
						continue
					}
					genAISpans++
					attrs := map[string]string{}
					for _, kv := range span.Attributes {
						attrs[kv.Key] = kv.Value.GetStringValue()
					}
					if got := attrs["ai.olivares.inference.request.body_sha256"]; got != wantRequestHash {
						t.Errorf("request trace hash = %q, want %q", got, wantRequestHash)
					}
					if got := attrs["ai.olivares.inference.response.body_sha256"]; got != wantResponseHash {
						t.Errorf("response trace hash = %q, want %q", got, wantResponseHash)
					}
					// Freeze: no product key may use the bare pre-freeze spelling.
					for key := range attrs {
						if strings.HasPrefix(key, "olivares.") {
							t.Errorf("bare pre-freeze attribute key %q on a GenAI span", key)
						}
					}
				}
			}
		}
	}
	if genAISpans != 1 {
		t.Fatalf("GenAI spans = %d, want 1", genAISpans)
	}
}

func hashBytesHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
