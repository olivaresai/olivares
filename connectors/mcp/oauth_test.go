// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestResourceMetadataURL(t *testing.T) {
	cases := map[string]string{
		`Bearer resource_metadata="https://h/.well-known/oauth-protected-resource"`:  "https://h/.well-known/oauth-protected-resource",
		`Bearer realm="x", resource_metadata="https://h/prm", error="invalid_token"`: "https://h/prm",
		`Bearer resource_metadata=https://h/prm`:                                     "https://h/prm",
		`Basic realm="x"`:                                                            "",
		``:                                                                           "",
	}
	for header, want := range cases {
		if got := resourceMetadataURL(header); got != want {
			t.Errorf("resourceMetadataURL(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestASMetadataCandidates: SEP-2351 — the spec's exact discovery candidate order:
// RFC 8414 insertion, then OIDC insertion, then OIDC appending (path issuers); the
// two-insertion order for pathless issuers.
func TestASMetadataCandidates(t *testing.T) {
	cases := map[string][]string{
		"https://as.example": {
			"https://as.example/.well-known/oauth-authorization-server",
			"https://as.example/.well-known/openid-configuration",
		},
		"https://as.example/": {
			"https://as.example/.well-known/oauth-authorization-server",
			"https://as.example/.well-known/openid-configuration",
		},
		"https://as.example/tenant": {
			"https://as.example/.well-known/oauth-authorization-server/tenant",
			"https://as.example/.well-known/openid-configuration/tenant",
			"https://as.example/tenant/.well-known/openid-configuration",
		},
	}
	for issuer, want := range cases {
		got, err := asMetadataCandidates(issuer)
		if err != nil {
			t.Fatalf("asMetadataCandidates(%q): %v", issuer, err)
		}
		if len(got) != len(want) {
			t.Fatalf("asMetadataCandidates(%q) = %v, want %v", issuer, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("asMetadataCandidates(%q)[%d] = %q, want %q", issuer, i, got[i], want[i])
			}
		}
	}
}

func TestCanonicalResourceURI(t *testing.T) {
	cases := map[string]string{
		"https://MCP.Example.com/mcp":      "https://mcp.example.com/mcp",
		"https://mcp.example.com/mcp/":     "https://mcp.example.com/mcp",
		"https://mcp.example.com/":         "https://mcp.example.com",
		"https://mcp.example.com/mcp#frag": "https://mcp.example.com/mcp",
		"https://mcp.example.com:8443/mcp": "https://mcp.example.com:8443/mcp",
	}
	for in, want := range cases {
		got, err := canonicalResourceURI(in)
		if err != nil || got != want {
			t.Errorf("canonicalResourceURI(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestValidateOutboundURL(t *testing.T) {
	ctx := context.Background()
	ok := []string{"https://example.com/x", "http://127.0.0.1:8080/x", "http://localhost/x"}
	for _, u := range ok {
		if err := validateOutboundURL(ctx, u); err != nil {
			t.Errorf("validateOutboundURL(%q) unexpected error: %v", u, err)
		}
	}
	bad := []string{
		"http://example.com/x",      // non-loopback http
		"https://10.0.0.5/x",        // private
		"https://169.254.169.254/x", // link-local (cloud metadata)
		"https://192.168.1.1/x",     // private
	}
	for _, u := range bad {
		if err := validateOutboundURL(ctx, u); err == nil {
			t.Errorf("validateOutboundURL(%q) should have been refused", u)
		}
	}
}

func TestPKCEAndAuthorizationURL(t *testing.T) {
	pk, err := newPKCE()
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	sum := sha256.Sum256([]byte(pk.verifier))
	if pk.challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("challenge must be base64url(sha256(verifier))")
	}

	raw, err := buildAuthorizationURL("https://as/authorize", "client1", "https://app/cb",
		"https://mcp.example.com/mcp", "state-xyz", []string{"read"}, pk)
	if err != nil {
		t.Fatalf("buildAuthorizationURL: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != pkceMethodS256 ||
		q.Get("code_challenge") != pk.challenge || q.Get("resource") != "https://mcp.example.com/mcp" ||
		q.Get("client_id") != "client1" || q.Get("state") != "state-xyz" {
		t.Errorf("authorization url query = %v", q)
	}
}

// --- Integration: Phase 1 (detect) and Phase 2 (authorized introspection) ---

type oauthSink struct{ obs []model.Observation }

func (s *oauthSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *oauthSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func TestMCPOAuthPhase1Detect(t *testing.T) {
	// A server that always 401s with an OAuth challenge and no auth is configured.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+oauthBase(r)+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := introspect(context.Background(), serverSpec{Name: "p", Transport: transportHTTP, URL: srv.URL})
	var oerr *oauthRequiredError
	if !errors.As(err, &oerr) {
		t.Fatalf("expected oauthRequiredError, got %v", err)
	}
	if oerr.attempted {
		t.Error("attempted should be false when no auth configured")
	}
	if !strings.Contains(oerr.resourceMetadata, "oauth-protected-resource") {
		t.Errorf("resource metadata url = %q", oerr.resourceMetadata)
	}

	// The Phase-1 finding carries the token-binding-verified=false dimension.
	f := introspectFinding("p", err, time.Time{})
	if f.Kind != "mcp_auth" || !strings.Contains(f.Title, "token-binding-verified=false") {
		t.Errorf("phase-1 finding = %+v", f)
	}
}

func TestMCPOAuthPhase2Authorized(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			writeJSON(w, fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q]}`, base+"/mcp", base))
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"code_challenge_methods_supported":["S256"]}`, base, base+"/token"))
		case "/token":
			writeJSON(w, `{"access_token":"tok-abc","token_type":"Bearer","expires_in":3600}`)
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer tok-abc" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mcpHTTPHandler(false)(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	base = srv.URL
	defer srv.Close()

	spec := serverSpec{
		Name: "s", Transport: transportHTTP, URL: base + "/mcp",
		Auth: &serverAuth{ClientID: "cid", ClientSecret: "csec"},
	}
	cat, err := introspect(context.Background(), spec)
	if err != nil {
		t.Fatalf("authorized introspect: %v", err)
	}
	if !cat.authBound {
		t.Error("catalog must record token-binding-verified (authBound) after authorized introspection")
	}
	if len(cat.tools) != 2 {
		t.Errorf("tools = %d, want 2", len(cat.tools))
	}

	// Gather emits the positive token-binding-verified finding plus capability edges.
	// Use legacy mode: mcpHTTPHandler speaks 2025-11-25 Initialize, not the 2026-07-28
	// stateless path. Explicitly disable next_revision_preview (default ON since).
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"servers":              fmt.Sprintf(`[{"name":"s","transport":"http","url":%q,"auth":{"client_id":"cid","client_secret":"csec"}}]`, base+"/mcp"),
		cfgNextRevisionPreview: "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &oauthSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var sawVerified bool
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "token-binding-verified=true") {
			sawVerified = true
		}
	}
	if !sawVerified {
		t.Error("expected a token-binding-verified=true finding after authorized introspection")
	}
}

func oauthBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}
