// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// fakeConsentStore grants consent for an explicit (subject, uri) set.
type fakeConsentStore struct{ granted map[string]bool }

func (f *fakeConsentStore) Granted(_ context.Context, subject, uri string) (bool, error) {
	return f.granted[subject+"|"+uri], nil
}

// recordingAuditor captures gate decisions for assertions (granting journal
// embedded for the evidence legs).
type recordingAuditor struct {
	fakeEvidenceJournal
	decisions []ToolDecision
}

func (a *recordingAuditor) Record(ctx context.Context, d ToolDecision, binding sdk.EvidenceBinding) GateRecord {
	a.decisions = append(a.decisions, d)
	return a.fakeEvidenceJournal.Record(ctx, d, binding)
}

// newAppsRS builds an RS with a ui:// template inventory: dashboard (free),
// settings (consent-gated), plus a toolset carrying an app-only tool.
func newAppsRS(t *testing.T, jwks []byte, consent ConsentStore, up Upstream, aud *recordingAuditor) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "refresh_panel", RequiredScope: "tools:read", AppOnly: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		Upstream:             up,
		DurableTaskStore:     newMemoryDurableTaskStore(),
		Consent:              consent,
		Auditor:              aud,
		Clock:                rsClock,
		UITemplates: []UITemplatePolicy{
			{URI: "ui://srv/dashboard"},
			{URI: "ui://srv/settings", RequireConsent: true},
		},
		// Tests in this file send 2025-11-25 style requests; opt out of the
		// 2026-07-28 header gate so they can focus on apps/consent behavior.
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

func resourcesReadReq(token, uri string) *http.Request {
	body := `{"jsonrpc":"2.0","id":7,"method":"resources/read","params":{"uri":"` + uri + `"}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// TestRSUIReadUndeclaredDenied: a resources/read of a ui:// template outside
// the pre-declared inventory is DENIED (the DoD case: undeclared ui:// → deny)
// and audited; the upstream is never reached.
func TestRSUIReadUndeclaredDenied(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	aud := &recordingAuditor{}
	rs := newAppsRS(t, jwks, nil, up, aud)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/rogue-panel"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not in the pre-declared inventory") {
		t.Errorf("deny reason missing: %s", rec.Body.String())
	}
	if up.called {
		t.Error("upstream must not be reached for a denied template")
	}
	if len(aud.decisions) != 1 || aud.decisions[0].Allowed || aud.decisions[0].Tool != "ui://srv/rogue-panel" {
		t.Errorf("deny must be audited with the template URI: %+v", aud.decisions)
	}
}

// TestRSUIReadDeclaredForwarded: a declared, consent-free template renders —
// the read forwards upstream and the allow is audited.
//
// (stage 5) — annotated assertion change: the render allow used to be the
// ONLY recorded decision (`len == 1`). The render is an evidence-enforced CLAIM
// now and its write is a response-release CHILD claim, so exactly TWO allow
// decisions are recorded. The assertion pins the new, stronger shape: the render
// claim (with its journal action) AND the release claim both present.
func TestRSUIReadDeclaredForwarded(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"contents":[{"uri":"ui://srv/dashboard","mimeType":"text/html;profile=mcp-app","text":"<html></html>"}]}`)}
	aud := &recordingAuditor{}
	rs := newAppsRS(t, jwks, nil, up, aud)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !up.called || up.gotReq.Method != "resources/read" {
		t.Errorf("upstream not forwarded correctly: %+v", up.gotReq)
	}
	if len(aud.decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (render claim + release claim): %+v", len(aud.decisions), aud.decisions)
	}
	render, release := aud.decisions[0], aud.decisions[1]
	if !render.Allowed || !strings.Contains(render.Reason, "render authorized") ||
		!strings.HasPrefix(render.EffectAction, "mcp.ui.read.") {
		t.Errorf("the render allow must be the enforced claim: %+v", render)
	}
	if !release.Allowed || release.EffectAction != "mcp.release.ui" {
		t.Errorf("the response write must be the release claim: %+v", release)
	}
}

// TestRSUIReadConsentGate: a consent-gated template denies with NO store wired
// (deny-closed default), denies for a subject without recorded consent, and
// renders once consent is recorded.
func TestRSUIReadConsentGate(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	// Deny-closed default: no ConsentStore wired.
	rs := newAppsRS(t, jwks, nil, &fakeUpstream{}, &recordingAuditor{})
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/settings"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "requires recorded user consent") {
		t.Fatalf("unwired consent store must deny: %d %s", rec.Code, rec.Body.String())
	}

	// Recorded consent for the token subject ("agent:claude") → renders.
	store := &fakeConsentStore{granted: map[string]bool{"agent:claude|ui://srv/settings": true}}
	up := &fakeUpstream{}
	rs = newAppsRS(t, jwks, store, up, &recordingAuditor{})
	rec = httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/settings"))
	if rec.Code != http.StatusOK || !up.called {
		t.Fatalf("recorded consent must render: %d (upstream called=%v)", rec.Code, up.called)
	}

	// A different (ungranted) subject's consent never bleeds over.
	store2 := &fakeConsentStore{granted: map[string]bool{"someone-else|ui://srv/settings": true}}
	rs = newAppsRS(t, jwks, store2, &fakeUpstream{}, &recordingAuditor{})
	rec = httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/settings"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("consent is per-subject; got %d", rec.Code)
	}
}

// TestRSUIReadNonUIPassesThrough: a non-ui:// resources/read takes the generic
// admitted-forward path untouched by the apps gate.
func TestRSUIReadNonUIPassesThrough(t *testing.T) {
	// F-06: a non-ui resources/read takes the generic forward path, which now requires the
	// resources:read family scope (the apps gate still does not touch it).
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read resources:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"contents":[]}`)}
	rs := newAppsRS(t, jwks, nil, up, &recordingAuditor{})

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "file:///etc/motd"))
	if rec.Code != http.StatusOK || !up.called {
		t.Fatalf("non-ui read must forward: %d (upstream called=%v)", rec.Code, up.called)
	}
}

// TestRSAppOnlyTool: an app-only tool is EXCLUDED from the model-facing
// tools/list, yet its call (UI-originated by construction) passes the gates and
// is audited with the UI-origin marker.
func TestRSAppOnlyTool(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"tools":[{"name":"search"},{"name":"refresh_panel"}]}`)}
	aud := &recordingAuditor{}
	rs := newAppsRS(t, jwks, nil, up, aud)

	// Discovery: refresh_panel must be filtered out.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "refresh_panel") {
		t.Errorf("app-only tool leaked into the model-facing tools/list: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "search") {
		t.Errorf("regular tool missing from tools/list: %s", rec.Body.String())
	}

	// Call: allowed (scope holds) and audited as UI-originated.
	up2 := &fakeUpstream{}
	aud2 := &recordingAuditor{}
	rs = newAppsRS(t, jwks, nil, up2, aud2)
	rec = httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "refresh_panel", `{}`))
	if rec.Code != http.StatusOK || !up2.called {
		t.Fatalf("app-only call must pass the gates: %d (upstream called=%v)", rec.Code, up2.called)
	}
	found := false
	for _, d := range aud2.decisions {
		if d.Tool == "refresh_panel" && d.Allowed && strings.Contains(d.Reason, "UI-originated") {
			found = true
		}
	}
	if !found {
		t.Errorf("app-only call must be audited with the UI-origin marker: %+v", aud2.decisions)
	}
}

// TestRSUIReadCaseVariantDenied: RFC 3986 schemes are case-insensitive — a
// "UI://" read must be GATED (and denied: the canonical declared inventory
// never matches a case variant), not slip past the prefix check to the
// generic forward.
func TestRSUIReadCaseVariantDenied(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newAppsRS(t, jwks, nil, up, &recordingAuditor{})

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "UI://srv/dashboard"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("case-variant scheme must be gated and denied, got %d %s", rec.Code, rec.Body.String())
	}
	if up.called {
		t.Error("case-variant ui read must never reach the upstream")
	}
}

// TestRSUIReadSmugglingGuard: params whose RAW bytes mention ui:// while the
// PARSED uri is not a ui:// URI (duplicate-key smuggling: Go keeps the LAST
// "uri", an upstream keeping the FIRST would serve the template ungated) are
// refused as ambiguous — fail-closed, the header↔body-consistency posture.
func TestRSUIReadSmugglingGuard(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newAppsRS(t, jwks, nil, up, &recordingAuditor{})

	body := `{"jsonrpc":"2.0","id":9,"method":"resources/read","params":{"uri":"ui://srv/dashboard","uri":"file:///etc/motd"}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "ambiguous") {
		t.Fatalf("duplicate-key ui smuggle must be refused as ambiguous, got %d %s", rec.Code, rec.Body.String())
	}
	if up.called {
		t.Error("ambiguous params must never reach the upstream")
	}
}

// TestRSUITemplateConfigValidation: a non-ui:// policy URI and a duplicate are
// constructor errors (a malformed inventory must never serve looking valid).
func TestRSUITemplateConfigValidation(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	base := ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
	}

	cfg := base
	cfg.UITemplates = []UITemplatePolicy{{URI: "https://srv/dashboard"}}
	if _, err := NewResourceServer(cfg); err == nil || !strings.Contains(err.Error(), "ui://") {
		t.Errorf("non-ui:// policy URI must fail construction, got %v", err)
	}

	cfg = base
	cfg.UITemplates = []UITemplatePolicy{{URI: "ui://srv/a"}, {URI: "ui://srv/a"}}
	if _, err := NewResourceServer(cfg); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate policy URI must fail construction, got %v", err)
	}
}
