// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeMode int

const (
	fakeOK fakeMode = iota
	fakeHang
	fakeStatus
)

type fakeCfg struct {
	protocol Protocol
	mode     fakeMode
	status   int
	started  chan struct{}
	closed   chan struct{}
	write2   chan struct{}
	writes   *atomic.Int32
	// requests counts every request the upstream receives, so a test can assert
	// that a call was never made. Without it, "the driver did not go upstream"
	// is unobservable and a test that claims it passes on any driver.
	requests *atomic.Int32
}

func newFake(t *testing.T, cfg fakeCfg) *httptest.Server {
	t.Helper()
	if cfg.status == 0 {
		cfg.status = http.StatusOK
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.requests != nil {
			cfg.requests.Add(1)
		}
		if cfg.started != nil {
			select {
			case <-cfg.started:
			default:
				close(cfg.started)
			}
		}
		if cfg.mode == fakeHang {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			if cfg.closed != nil {
				close(cfg.closed)
			}
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		stream := strings.Contains(string(body), `"stream":true`) || strings.Contains(string(body), `"stream": true`)
		if cfg.mode == fakeStatus {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(cfg.status)
			_, _ = w.Write([]byte(`{"error":{"message":"denied"}}`))
			return
		}
		switch cfg.protocol {
		case ProtocolOpenAICompat:
			serveOpenAI(r.Context(), w, stream, cfg)
		case ProtocolAnthropic:
			serveAnthropic(r.Context(), w, stream, cfg)
		case ProtocolOllama:
			serveOllama(r.Context(), w, stream, cfg)
		default:
			http.Error(w, "unknown protocol", 500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitNext(ctx context.Context, write2 chan struct{}) bool {
	if write2 == nil {
		return true
	}
	select {
	case <-write2:
		return true
	case <-ctx.Done():
		return false
	}
}

func serveOpenAI(ctx context.Context, w http.ResponseWriter, stream bool, cfg fakeCfg) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"gpt-test","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
	if fl != nil {
		fl.Flush()
	}
	if cfg.writes != nil {
		cfg.writes.Add(1)
	}
	if !waitNext(ctx, cfg.write2) {
		return
	}
	_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n"))
	if fl != nil {
		fl.Flush()
	}
	if cfg.writes != nil {
		cfg.writes.Add(1)
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if fl != nil {
		fl.Flush()
	}
}

func serveAnthropic(ctx context.Context, w http.ResponseWriter, stream bool, cfg fakeCfg) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"))
	_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n"))
	if fl != nil {
		fl.Flush()
	}
	if cfg.writes != nil {
		cfg.writes.Add(1)
	}
	if !waitNext(ctx, cfg.write2) {
		return
	}
	_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n"))
	_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"))
	_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	if fl != nil {
		fl.Flush()
	}
	if cfg.writes != nil {
		cfg.writes.Add(1)
	}
}

func serveOllama(ctx context.Context, w http.ResponseWriter, stream bool, cfg fakeCfg) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"llama-test","message":{"role":"assistant","content":"hello"},"done":true,"prompt_eval_count":3,"eval_count":2}`))
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"message\":{\"role\":\"assistant\",\"content\":\"hel\"},\"done\":false}\n"))
	if fl != nil {
		fl.Flush()
	}
	if cfg.writes != nil {
		cfg.writes.Add(1)
	}
	if !waitNext(ctx, cfg.write2) {
		return
	}
	_, _ = w.Write([]byte("{\"message\":{\"role\":\"assistant\",\"content\":\"lo\"},\"done\":true,\"prompt_eval_count\":3,\"eval_count\":2}\n"))
	if fl != nil {
		fl.Flush()
	}
	if cfg.writes != nil {
		cfg.writes.Add(1)
	}
}

func testDriver(t *testing.T, proto Protocol, srv *httptest.Server, hook CostHook) Driver {
	t.Helper()
	cl := srv.Client()
	if tr, ok := cl.Transport.(*http.Transport); ok {
		cloned := tr.Clone()
		cloned.DisableKeepAlives = true
		cl.Transport = cloned
	}
	cfg := Config{BaseURL: srv.URL, Credential: "test-key", CostHook: hook, Doer: cl}
	var (
		d   Driver
		err error
	)
	switch proto {
	case ProtocolOpenAICompat:
		d, err = NewOpenAICompat(cfg)
	case ProtocolAnthropic:
		d, err = NewAnthropic(cfg)
	case ProtocolOllama:
		d, err = NewOllama(cfg)
	default:
		t.Fatalf("unknown protocol %s", proto)
	}
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}
	return d
}

func sampleReq() MessageRequest {
	return MessageRequest{
		Model:     "test-model",
		MaxTokens: 32,
		Messages:  []Message{{Role: "user", Content: "hi"}},
	}
}
