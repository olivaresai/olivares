// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestRSRenderInspectorSourceFollowsSettingsWithoutRebuild: the resource server is built
// once; each render asks the source what is in force. A nil answer is "no inspection", the
// same meaning a nil RenderInspector has.
func TestRSRenderInspectorSourceFollowsSettingsWithoutRebuild(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"contents":[{"uri":"ui://srv/dashboard","mimeType":"text/html;profile=mcp-app","text":"<script>alert(1)</script>"}]}`)}
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	var live atomic.Pointer[fakeRenderInspector]
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer, IssuerJWKS: jwks,
		Toolset: ts, Upstream: up, Auditor: &recordingAuditor{}, Clock: rsClock,
		RenderInspectorSource: func() RenderInspector {
			if i := live.Load(); i != nil {
				return i
			}
			return nil
		},
		UITemplates:                []UITemplatePolicy{{URI: "ui://srv/dashboard"}},
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	read := func() int {
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))
		return rec.Code
	}

	if code := read(); code != http.StatusOK {
		t.Fatalf("no inspector configured: the render must flow as before the add-on; status=%d", code)
	}
	inspector := &fakeRenderInspector{allow: false, reason: "prompt injection detected in template HTML"}
	live.Store(inspector)
	if code := read(); code != http.StatusForbidden {
		t.Fatalf("inspector saved while running must govern the next render; status=%d", code)
	}
	if !inspector.called {
		t.Fatal("inspector never consulted")
	}
	live.Store(nil)
	if code := read(); code != http.StatusOK {
		t.Fatalf("inspector turned off while running must let the next render flow; status=%d", code)
	}
}
