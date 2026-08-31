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

func TestInferenceClient_PostJSON_SetsAuthAndDecodes(t *testing.T) {
	doer := &stubDoer{body: `{"ok":true,"n":7}`}
	c := NewInferenceClient("https://api.example.com/", doer, AuthAnthropicKey, "k-secret", map[string]string{"anthropic-version": "2023-06-01"})

	var out struct {
		OK bool `json:"ok"`
		N  int  `json:"n"`
	}
	if err := c.PostJSON(context.Background(), "/v1/messages", map[string]any{"model": "m", "max_tokens": 8}, &out, map[string]string{"anthropic-beta": "x"}); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if !out.OK || out.N != 7 {
		t.Fatalf("decode mismatch: %+v", out)
	}
	r := doer.lastReq
	if r.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", r.Method)
	}
	if got := r.Header.Get("x-api-key"); got != "k-secret" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := r.Header.Get("anthropic-beta"); got != "x" {
		t.Errorf("anthropic-beta = %q (per-request header not applied)", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), `"max_tokens":8`) {
		t.Errorf("body did not carry request: %s", body)
	}
}

func TestInferenceClient_APIErrorOnNon2xx(t *testing.T) {
	doer := &stubDoer{status: http.StatusBadRequest, body: `{"error":{"message":"bad"}}`}
	c := NewInferenceClient("https://api.example.com", doer, AuthAnthropicKey, "k", nil)
	err := c.PostJSON(context.Background(), "/v1/messages", map[string]any{}, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if strings.Contains(apiErr.Error(), "k") && !strings.Contains(apiErr.Error(), "bad") {
		t.Errorf("error should carry provider body, not credential: %s", apiErr.Error())
	}
}

func TestInferenceClient_GetBytes_AbsoluteURLAndRedaction(t *testing.T) {
	doer := &stubDoer{body: "line1\nline2\n"}
	c := NewInferenceClient("https://api.example.com", doer, AuthAnthropicKey, "k", nil)
	got, err := c.GetBytes(context.Background(), "https://files.example.com/r/abc?token=secret", nil)
	if err != nil {
		t.Fatalf("GetBytes: %v", err)
	}
	if string(got) != "line1\nline2\n" {
		t.Errorf("body = %q", got)
	}
	if doer.lastReq.URL.String() != "https://files.example.com/r/abc?token=secret" {
		t.Errorf("absolute URL not used verbatim: %s", doer.lastReq.URL)
	}
	// On error the token must be redacted out of the message.
	doer2 := &stubDoer{status: 500, body: "down"}
	c2 := NewInferenceClient("https://api.example.com", doer2, AuthAnthropicKey, "k", nil)
	_, err = c2.GetBytes(context.Background(), "https://files.example.com/r/abc?token=secret", nil)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("error must redact query token: %v", err)
	}
}

func TestInferenceClient_PostMultipart(t *testing.T) {
	doer := &stubDoer{body: `{"id":"file_123","type":"file"}`}
	c := NewInferenceClient("https://api.example.com", doer, AuthAnthropicKey, "k", nil)
	var out struct {
		ID string `json:"id"`
	}
	if err := c.PostMultipart(context.Background(), "/v1/files", map[string]string{"purpose": "x"}, "file", "a.txt", []byte("hello"), &out, map[string]string{"anthropic-beta": "files-api-2025-04-14"}); err != nil {
		t.Fatalf("PostMultipart: %v", err)
	}
	if out.ID != "file_123" {
		t.Errorf("id = %q", out.ID)
	}
	if ct := doer.lastReq.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart", ct)
	}
	if doer.lastReq.Header.Get("anthropic-beta") != "files-api-2025-04-14" {
		t.Errorf("beta header missing")
	}
	body, _ := io.ReadAll(doer.lastReq.Body)
	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), `name="file"`) {
		t.Errorf("multipart body missing file part: %s", body)
	}
}

func TestEmbeddingsClient_EmbedOrdersAndValidatesDim(t *testing.T) {
	// Two vectors returned out of index order; the client must restore input order.
	doer := &stubDoer{body: `{"model":"voyage-3","data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}]}`}
	e := NewEmbeddingsClient(EmbeddingsConfig{BaseURL: "https://emb.example.com", APIKey: "k", Model: "voyage-3", Dim: 2, Doer: doer})
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.3 {
		t.Fatalf("vectors not in input order: %+v", vecs)
	}
	// Dim mismatch must fail (never a silent short vector).
	doer2 := &stubDoer{body: `{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`}
	e2 := NewEmbeddingsClient(EmbeddingsConfig{BaseURL: "https://emb.example.com", APIKey: "k", Model: "voyage-3", Dim: 2, Doer: doer2})
	if _, err := e2.Embed(context.Background(), []string{"a"}); err == nil {
		t.Errorf("want dim-mismatch error")
	}
	// Count mismatch must fail.
	doer3 := &stubDoer{body: `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`}
	e3 := NewEmbeddingsClient(EmbeddingsConfig{BaseURL: "https://emb.example.com", APIKey: "k", Model: "voyage-3", Dim: 2, Doer: doer3})
	if _, err := e3.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Errorf("want count-mismatch error")
	}
	// Bearer auth is the embeddings default.
	if got := doer.lastReq.Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("authorization = %q, want Bearer k", got)
	}
}

func TestEmbeddingsClient_RejectsBadIndexSet(t *testing.T) {
	// Duplicate index with a gap ([0,0] for 2 inputs) passes the count check but would
	// silently misalign vectors to texts — must fail closed, never corrupt retrieval.
	dup := &stubDoer{body: `{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":0,"embedding":[0.3,0.4]}]}`}
	eDup := NewEmbeddingsClient(EmbeddingsConfig{BaseURL: "https://e", APIKey: "k", Model: "voyage-3", Dim: 2, Doer: dup})
	if _, err := eDup.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("duplicate index must error (would misalign vectors)")
	}
	// Out-of-range index (1-based provider) must error.
	oob := &stubDoer{body: `{"data":[{"index":1,"embedding":[0.1,0.2]},{"index":2,"embedding":[0.3,0.4]}]}`}
	eOOB := NewEmbeddingsClient(EmbeddingsConfig{BaseURL: "https://e", APIKey: "k", Model: "voyage-3", Dim: 2, Doer: oob})
	if _, err := eOOB.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("out-of-range index must error")
	}
}
