// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInferenceClient_PostStream_ReturnsBodyAndSetsSSEHeaders(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	doer := &stubDoer{body: sse}
	c := NewInferenceClient("https://api.example.com", doer, AuthAnthropicKey, "k-secret",
		map[string]string{"anthropic-version": "2023-06-01"})

	rc, err := c.PostStream(context.Background(), "/v1/messages",
		map[string]any{"model": "m", "stream": true}, map[string]string{"anthropic-beta": "b"})
	if err != nil {
		t.Fatalf("PostStream: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	if string(got) != sse {
		t.Errorf("stream body = %q, want %q", got, sse)
	}
	r := doer.lastReq
	if r.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", r.Method)
	}
	if got := r.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
	if got := r.Header.Get("x-api-key"); got != "k-secret" {
		t.Errorf("x-api-key = %q (auth not applied)", got)
	}
	if got := r.Header.Get("anthropic-beta"); got != "b" {
		t.Errorf("anthropic-beta = %q (per-request header not applied)", got)
	}
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), `"stream":true`) {
		t.Errorf("request body did not carry stream:true: %s", body)
	}
}

func TestInferenceClient_PostStream_Non2xxIsAPIError(t *testing.T) {
	doer := &stubDoer{status: http.StatusTooManyRequests, body: `{"error":{"message":"slow down"}}`}
	c := NewInferenceClient("https://api.example.com", doer, AuthAnthropicKey, "k", nil)

	rc, err := c.PostStream(context.Background(), "/v1/messages", map[string]any{}, nil)
	if rc != nil {
		t.Errorf("expected nil reader on error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", apiErr.Status)
	}
	if strings.Contains(apiErr.Error(), "k") && !strings.Contains(apiErr.Error(), "slow down") {
		t.Errorf("error should carry provider body, not the credential: %s", apiErr.Error())
	}
}
