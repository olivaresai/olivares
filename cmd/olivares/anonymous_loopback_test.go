// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/model"
)

// publicReads answer without a credential by design: liveness, the published
// API descriptions, discovery metadata and the unauthenticated metrics scrape.
var publicReads = map[string]bool{
	"GET /healthz": true, "GET /livez": true, "GET /readyz": true, "GET /pod-readyz": true,
	"GET /status": true, "GET /metrics": true, "GET /v1/server-info": true,
	"GET /openapi.json": true, "GET /openapi.beta.json": true,
	"GET /.well-known/authzen-configuration": true, "GET /.well-known/oauth-authorization-server": true,
}

// proofRoutes take no credential because the request itself carries the proof
// (setup token, password, IdP or jwt-bearer assertion, pending login credential,
// single-use invite token). Without that proof they answer a proofRefusal.
var proofRoutes = map[string]bool{
	"POST /v1/setup":                        true,
	"POST /v1/auth/login":                   true,
	"POST /v1/auth/token":                   true,
	"GET /v1/auth/federation/start":         true,
	"GET /v1/auth/federation/saml/metadata": true,
	"GET /v1/auth/federation/callback":      true,
	"POST /v1/auth/federation/callback":     true,
	"POST /v1/auth/totp/enrol":              true, //nolint:misspell // Published API path.
	"POST /v1/auth/totp/activate":           true,
	"POST /v1/auth/totp/challenge":          true,
	"POST /v1/invites/accept":               true,
}

// proofRefusal are the answers of a proofRoute without its proof: rejected, or
// 501 while its feature is unconfigured.
var proofRefusal = map[int]bool{
	http.StatusBadRequest: true, http.StatusUnauthorized: true, http.StatusForbidden: true,
	http.StatusConflict: true, http.StatusUnprocessableEntity: true, http.StatusNotImplemented: true,
}

// refusedBeforeAuth answers another refusal before any credential check. The
// OS-account routes accept only a direct TLS handshake (over TLS they answer
// 401); an unknown MCP gateway server is refused before its own bearer check;
// /session/mcp serves POST (401 here) and refuses every other method.
// An unknown gateway server is all a route walk can name; the bearer check of an
// enabled one is pinned by TestMCPManagementRuntimeTestEnableAudienceAndDisable.
func refusedBeforeAuth(method, route string, overTLS bool, code int) bool {
	switch route {
	case "/v1/auth/os-account-bindings", "/v1/auth/os-account-bindings/complete", "/v1/auth/os-account-bindings/{id}":
		return !overTLS && code == http.StatusForbidden
	case "/mcp/gateway/{tenant}/{id}", "/.well-known/oauth-protected-resource/mcp/gateway/{tenant}/{id}":
		return code == http.StatusNotFound
	case "/session/mcp":
		return method != http.MethodPost && code == http.StatusMethodNotAllowed
	}
	return false
}

// TestNoAnonymousWriteOrAdminFromLoopback pins that the engine has no "trusted
// local" path: every route and method of the production router refuses a request
// that carries no credential, even from the IPv4 or IPv6 loopback address, over
// plain HTTP or TLS, naming a real tenant and claiming a loopback client in the
// proxy headers. The only answers that are not 401 are the publicReads, the
// proofRoutes without their proof, and refusedBeforeAuth. The session-local hook
// listener, which also serves /session/mcp, refuses the same request.
func TestNoAnonymousWriteOrAdminFromLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	eng, err := boot(ctx, bootConfig{
		Engine: "sqlite", DSN: filepath.Join(dir, "loopback.db"), DataDir: dir,
		Version: "loopback-test", Logger: log, NoIngest: true, ServeMode: true,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	// Setup is finished, as on any installed engine, so routes answer for real
	// instead of the first-run 409.
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "loopback@example.invalid", "loopback-fixture-123"); err != nil {
		t.Fatal(err)
	}
	admin, _, err := eng.authr.Login(ctx, "loopback@example.invalid", "loopback-fixture-123", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	code, org, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodPost, "/v1/system/orgs", admin, "", map[string]any{"name": "Loopback", "slug": "loopback"})
	tenant, _ := org["tenant_id"].(string)
	if code != http.StatusCreated || tenant == "" {
		t.Fatalf("create tenant: HTTP %d %s", code, raw)
	}
	handler := thisEdition.routes(eng.api.Handler(), eng, log)
	router, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("the production HTTP wrapper does not expose its registered routes")
	}
	checked, listed := 0, map[string]bool{}
	err = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(strings.TrimSuffix(route, "/*"), "/*/", "/")
		key := method + " " + route
		if publicReads[key] {
			listed[key] = true
			return nil
		}
		path := routeParam.ReplaceAllString(route, model.NewID().String())
		for _, via := range []struct {
			remote string
			tls    bool
		}{{"127.0.0.1:5555", false}, {"[::1]:5555", false}, {"127.0.0.1:5555", true}} {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			req.RemoteAddr = via.remote
			if via.tls {
				req.TLS = &tls.ConnectionState{}
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Olivares-Tenant", tenant)
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			req.Header.Set("X-Real-IP", "127.0.0.1")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			switch {
			case proofRoutes[key]:
				listed[key] = true
				if !proofRefusal[rec.Code] {
					t.Errorf("%s from %s (tls=%v) without its proof = %d, want a refusal", key, via.remote, via.tls, rec.Code)
				}
			case rec.Code != http.StatusUnauthorized && !refusedBeforeAuth(method, route, via.tls, rec.Code):
				t.Errorf("%s from %s (tls=%v) without a credential = %d, want 401", key, via.remote, via.tls, rec.Code)
			}
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, list := range []map[string]bool{publicReads, proofRoutes} {
		for key := range list {
			if !listed[key] {
				t.Errorf("%s is listed as anonymous, but the router no longer serves it", key)
			}
		}
	}
	// The product router serves over a thousand routes; a walk below that is no longer it.
	if checked < 1000 {
		t.Fatalf("the walk checked only %d routes; this test would pass vacuously", checked)
	}
	t.Logf("checked %d routes from IPv4 and IPv6 loopback, over plain HTTP and TLS", checked)

	// The session hook listener answers a hook with a decision, not a status code:
	// an anonymous tool call must be denied, and its /session/mcp edge refuses with 401.
	pep, err := buildClaudeHookPEPServer(eng, log)
	if err != nil || pep == nil {
		t.Fatalf("build the session hook listener: %v", err)
	}
	for _, probe := range []struct{ path, body, refusal string }{
		{"/", `{"session_id":"anon","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"tu-anon","tool_input":{"command":"touch /tmp/anon"}}`, `"permissionDecision":"deny"`},
		{"/permission-prompt", `{"session_id":"anon","tool_name":"Bash","tool_use_id":"tu-anon","tool_input":{"command":"touch /tmp/anon"}}`, `"behavior":"deny"`},
		{"/session/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, ""},
	} {
		req := httptest.NewRequest(http.MethodPost, probe.path, strings.NewReader(probe.body))
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Olivares-Hook-Tenant", tenant)
		rec := httptest.NewRecorder()
		pep.Handler.ServeHTTP(rec, req)
		switch {
		case probe.refusal == "" && rec.Code != http.StatusUnauthorized:
			t.Errorf("session hook listener POST %s without a credential = %d, want 401", probe.path, rec.Code)
		case probe.refusal != "" && (rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) || !strings.Contains(rec.Body.String(), probe.refusal)):
			t.Errorf("session hook listener POST %s without a credential = %d %s, want a %s decision", probe.path, rec.Code, rec.Body.String(), probe.refusal)
		}
	}
}
