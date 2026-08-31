// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// stubDoer is a test transport: it records the last request it saw and returns a
// canned response, so a connector can be exercised against a recorded API shape
// with no live network call.
type stubDoer struct {
	lastReq *http.Request
	status  int
	body    string
	err     error
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.lastReq = req
	if d.err != nil {
		return nil, d.err
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     make(http.Header),
	}, nil
}

func TestGetJSON_DecodesAndIsReadOnly(t *testing.T) {
	doer := &stubDoer{body: `{"data":[{"id":"m1"},{"id":"m2"}]}`}
	c := NewClient("https://api.example.com/", doer, AuthBearer, "secret-token", map[string]string{"x-extra": "v"})

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	q := url.Values{"limit": {"2"}}
	if err := c.GetJSON(context.Background(), "/v1/models", q, &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if len(out.Data) != 2 || out.Data[0].ID != "m1" {
		t.Fatalf("decode = %+v", out)
	}

	req := doer.lastReq
	if req.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET (read-only)", req.Method)
	}
	if req.Body != nil {
		t.Fatalf("GET request carried a body")
	}
	if got := req.URL.String(); got != "https://api.example.com/v1/models?limit=2" {
		t.Fatalf("url = %s", got)
	}
	if req.Header.Get("x-extra") != "v" {
		t.Fatalf("static header not sent")
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Fatalf("Accept header not set")
	}
}

func TestGetJSON_AuthSchemes(t *testing.T) {
	cases := []struct {
		scheme     AuthScheme
		cred       string
		header     string
		wantValue  string
		wantAbsent bool
	}{
		{AuthBearer, "tok", "Authorization", "Bearer tok", false},
		{AuthAnthropicKey, "k", "x-api-key", "k", false},
		{AuthGoogleKey, "g", "x-goog-api-key", "g", false},
		{AuthFalKey, "kid:secret", "Authorization", "Key kid:secret", false},
		{AuthNone, "ignored", "Authorization", "", true},
		{AuthBearer, "", "Authorization", "", true}, // empty cred => no header
	}
	for _, c := range cases {
		doer := &stubDoer{body: `{}`}
		cl := NewClient("https://x", doer, c.scheme, c.cred, nil)
		if err := cl.GetJSON(context.Background(), "/p", nil, nil); err != nil {
			t.Fatalf("scheme %s: %v", c.scheme, err)
		}
		got := doer.lastReq.Header.Get(c.header)
		if c.wantAbsent {
			if got != "" {
				t.Fatalf("scheme %s: header %s = %q, want absent", c.scheme, c.header, got)
			}
			continue
		}
		if got != c.wantValue {
			t.Fatalf("scheme %s: header %s = %q, want %q", c.scheme, c.header, got, c.wantValue)
		}
	}
}

func TestGetJSON_ErrorStatus_NoCredentialLeak(t *testing.T) {
	doer := &stubDoer{status: http.StatusUnauthorized, body: `{"error":"invalid key"}`}
	c := NewClient("https://x", doer, AuthBearer, "super-secret-credential", nil)

	err := c.GetJSON(context.Background(), "/v1/usage", nil, new(struct{}))
	if err == nil {
		t.Fatal("want error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q lacks status", err)
	}
	if strings.Contains(err.Error(), "super-secret-credential") {
		t.Fatalf("error leaked the credential: %q", err)
	}
	// The provider's (non-sensitive) error message is surfaced for diagnostics.
	if !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("error %q dropped the provider message", err)
	}
}

// TestGetJSON_ReturnsTypedAPIError proves GetJSON returns a typed *APIError carrying the
// real status, so a caller can route on 403 vs 404 WITHOUT substring-matching a
// server-controlled body — even when that body itself contains a misleading "status NNN"
// literal. Error() stays byte-compatible for the existing string-matching callers.
func TestGetJSON_ReturnsTypedAPIError(t *testing.T) {
	doer := &stubDoer{status: http.StatusForbidden, body: `{"error":"upstream said status 404"}`}
	c := NewClient("https://x", doer, AuthNone, "", nil)
	err := c.GetJSON(context.Background(), "/v1/thing", nil, new(struct{}))
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want a typed *APIError, got %T (%v)", err, err)
	}
	if ae.Status != http.StatusForbidden {
		t.Errorf("typed Status = %d, want 403 (must not be confused by 'status 404' in the body)", ae.Status)
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Errorf("Error() must stay byte-compatible for legacy string matchers: %q", err.Error())
	}
}

func TestGetText_ReturnsBodyReadOnly(t *testing.T) {
	doer := &stubDoer{body: "vllm:prompt_tokens_total{model_name=\"m\"} 42\n"}
	c := NewClient("https://x", doer, AuthNone, "", nil)
	body, err := c.GetText(context.Background(), "/metrics", nil)
	if err != nil {
		t.Fatalf("GetText: %v", err)
	}
	if !strings.Contains(body, "prompt_tokens_total") {
		t.Fatalf("body = %q", body)
	}
	if doer.lastReq.Method != http.MethodGet {
		t.Fatalf("GetText method = %s, want GET", doer.lastReq.Method)
	}
}

func TestGetText_ErrorStatus(t *testing.T) {
	doer := &stubDoer{status: http.StatusServiceUnavailable, body: "down"}
	c := NewClient("https://x", doer, AuthNone, "", nil)
	if _, err := c.GetText(context.Background(), "/metrics", nil); err == nil {
		t.Fatal("want error on 503")
	}
}

func TestGetJSON_NilDoerUsesDefault(t *testing.T) {
	// NewClient must not panic with a nil doer; it falls back to the default client.
	c := NewClient("https://127.0.0.1:1", nil, AuthNone, "", nil)
	// Unreachable host => an error, but no panic and no credential involved.
	if err := c.GetJSON(context.Background(), "/", nil, nil); err == nil {
		t.Skip("unexpected success against closed port; environment-dependent")
	}
}
