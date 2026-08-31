// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetJSONDecodesAndAppliesAuth(t *testing.T) {
	var gotAuth, gotAccept, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-Api-Version")
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("query not forwarded: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"name":"alice"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client(), Bearer("s3cret"), map[string]string{"X-Api-Version": "1"})
	var out struct{ Name string }
	if err := c.GetJSON(context.Background(), "/users", url.Values{"limit": {"50"}}, &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if out.Name != "alice" {
		t.Errorf("decoded name = %q", out.Name)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("accept = %q", gotAccept)
	}
	if gotVersion != "1" {
		t.Errorf("static header not sent: %q", gotVersion)
	}
}

func TestGetJSONErrorCarriesStatusNotCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client(), Bearer("topsecret"), nil)
	err := c.GetJSON(context.Background(), "/x", nil, nil)
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "insufficient_scope") {
		t.Errorf("error should carry status + excerpt: %v", err)
	}
	if strings.Contains(err.Error(), "topsecret") {
		t.Errorf("error must never contain the credential: %v", err)
	}
	// The non-2xx error is TYPED so a connector can discriminate the status
	// (e.g. a gated research preview's 403/404) without parsing the message.
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("non-2xx error must be a *StatusError, got %T", err)
	}
	if se.Status != 403 || se.Path != "/x" || !strings.Contains(se.Excerpt, "insufficient_scope") {
		t.Errorf("StatusError fields wrong: %+v", se)
	}
}

func TestEmptyTokenSendsNoAuthHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client(), Bearer(""), nil) // unconfigured
	if err := c.GetJSON(context.Background(), "/x", nil, nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if hadAuth {
		t.Error("unconfigured connector must send no Authorization header")
	}
}

func TestHeaderAuthGuarded(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Vault-Token")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tok := "hvs.abc"
	c := New(srv.URL, srv.Client(), Header("X-Vault-Token", tok, tok), nil)
	if err := c.GetJSON(context.Background(), "/v1/sys/health", nil, nil); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if gotToken != "hvs.abc" {
		t.Errorf("vault token header = %q", gotToken)
	}
}

func TestURLSameOriginAbsolute(t *testing.T) {
	c := New("https://base.example/api", nil, nil, nil)
	// A same-origin absolute pagination link (what Okta/Graph return) is followed.
	got, err := c.url("https://base.example/api/v1/users?after=x")
	if err != nil || got != "https://base.example/api/v1/users?after=x" {
		t.Errorf("same-origin absolute URL = %q, %v", got, err)
	}
	if got, err := c.url("/users"); err != nil || got != "https://base.example/api/users" {
		t.Errorf("relative path join wrong: %q, %v", got, err)
	}
	if got, err := c.url("users"); err != nil || got != "https://base.example/api/users" {
		t.Errorf("path without leading slash wrong: %q, %v", got, err)
	}
}

func TestURLRefusesCrossOriginLink(t *testing.T) {
	c := New("https://base.example/api", nil, nil, nil)
	// A server-controlled pagination link must never carry the credential to
	// another host: cross-origin absolute links are refused, deny-closed.
	if _, err := c.url("https://attacker.example/steal"); err == nil {
		t.Fatal("cross-origin absolute link was not refused")
	}
	if _, err := c.url("http://base.example/api/v1/users"); err == nil {
		t.Fatal("scheme-downgrade link was not refused")
	}
	// A baseless client takes caller-supplied absolute URLs verbatim: there is no
	// origin to pin against, and the URL is the caller's deliberate choice (the
	// cross-host federation fetcher), not a server-controlled cursor.
	empty := New("", nil, nil, nil)
	if got, err := empty.url("https://any.example/x"); err != nil || got != "https://any.example/x" {
		t.Fatalf("baseless absolute URL = %q, %v", got, err)
	}
	// The refusal error names the host, never a credential.
	err := c.GetJSON(context.Background(), "https://attacker.example/steal", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "attacker.example") {
		t.Fatalf("GetJSON cross-origin error = %v", err)
	}
}

func TestURLDefaultPortIsSameOrigin(t *testing.T) {
	// RFC 6454: an explicit default port is the same origin as an omitted one —
	// a provider writing ":443" into its cursors must not abort pagination.
	c := New("https://base.example/api", nil, nil, nil)
	if _, err := c.url("https://base.example:443/api/v1/users?after=x"); err != nil {
		t.Errorf("explicit :443 vs bare https base refused: %v", err)
	}
	withPort := New("https://base.example:443/api", nil, nil, nil)
	if _, err := withPort.url("https://base.example/api/v1/users"); err != nil {
		t.Errorf("bare link vs :443 base refused: %v", err)
	}
	// A genuinely different port stays a different origin.
	if _, err := c.url("https://base.example:8443/api"); err == nil {
		t.Error("different explicit port was not refused")
	}
}

func TestRefusesCrossOriginRedirect(t *testing.T) {
	// The origin pin would be hollow if the server could 302 the client — with
	// the credential header — to another host: the transport must refuse.
	var attacked atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		attacked.Store(true)
	}))
	defer attacker.Close()
	directory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/landed", http.StatusFound)
			return
		}
		if r.URL.Path == "/landed" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Redirect(w, r, attacker.URL+"/exfil", http.StatusFound)
	}))
	defer directory.Close()

	// nil doer => hardened default client.
	c := New(directory.URL, nil, Header("X-Vault-Token", "s.SECRET", "s.SECRET"), nil)
	if err := c.GetJSON(context.Background(), "/v1/users", nil, nil); err == nil {
		t.Error("cross-origin redirect was followed without error")
	}
	if attacked.Load() {
		t.Fatal("the credentialed request reached the attacker host")
	}
	// A same-origin redirect (trailing-slash 301s and the like) still works.
	var out struct{ OK bool }
	if err := c.GetJSON(context.Background(), "/same", nil, &out); err != nil || !out.OK {
		t.Errorf("same-origin redirect broken: %v ok=%v", err, out.OK)
	}
	// An injected *http.Client without a redirect policy is hardened by copy —
	// and the caller's client is not mutated.
	injected := &http.Client{}
	c2 := New(directory.URL, injected, nil, nil)
	if err := c2.GetJSON(context.Background(), "/v1/users", nil, nil); err == nil {
		t.Error("injected client followed a cross-origin redirect")
	}
	if injected.CheckRedirect != nil {
		t.Error("caller's client was mutated")
	}
}

func TestGet429HonorsRetryAfterOnce(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client(), nil, nil)
	var out struct{ OK bool }
	if err := c.GetJSON(context.Background(), "/v1/users", nil, &out); err != nil {
		t.Fatalf("GetJSON after 429 retry: %v", err)
	}
	if !out.OK || atomic.LoadInt32(&calls) != 2 {
		t.Errorf("retry behavior wrong: ok=%v calls=%d", out.OK, calls)
	}
}

func TestGet429WithoutHintIsStatusError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client(), nil, nil)
	err := c.GetJSON(context.Background(), "/v1/users", nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Fatalf("429 without hint = %v, want StatusError 429", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("blind retry happened: calls=%d", calls)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	mk := func(h map[string]string) *http.Response {
		r := &http.Response{Header: http.Header{}}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	if d, ok := retryAfter(mk(map[string]string{"Retry-After": "7"}), now); !ok || d != 7*time.Second {
		t.Errorf("seconds form = %v %v", d, ok)
	}
	if d, ok := retryAfter(mk(map[string]string{"Retry-After": now.Add(3 * time.Second).Format(http.TimeFormat)}), now); !ok || d <= 0 || d > 3*time.Second {
		t.Errorf("http-date form = %v %v", d, ok)
	}
	// Okta's epoch reset header is the fallback.
	if d, ok := retryAfter(mk(map[string]string{"X-Rate-Limit-Reset": strconv.FormatInt(now.Add(5*time.Second).Unix(), 10)}), now); !ok || d <= 0 || d > 5*time.Second {
		t.Errorf("okta epoch form = %v %v", d, ok)
	}
	if _, ok := retryAfter(mk(nil), now); ok {
		t.Error("no hint must report ok=false")
	}
	if _, ok := retryAfter(mk(map[string]string{"Retry-After": "garbage"}), now); ok {
		t.Error("garbage hint must report ok=false")
	}
}

func TestGetRawExposesHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://x/next>; rel="next"`)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client(), nil, nil)
	resp, err := c.GetRaw(context.Background(), "/groups", nil)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !strings.Contains(resp.Header.Get("Link"), `rel="next"`) {
		t.Errorf("Link header not exposed: %q", resp.Header.Get("Link"))
	}
}
