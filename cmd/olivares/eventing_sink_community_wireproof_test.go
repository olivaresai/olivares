// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Community composition wires no SIEM renderer (siemforward.NewRenderer is nil), and the
// eventing module refuses to author a SIEM sink without one. The module tests prove the rule;
// this proves it is in force in the booted Community engine, through the real API handler.
func TestBootedCommunityRefusesASIEMSinkAndNamesBusiness(t *testing.T) {
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	h := eng.api.Handler()
	do := func(method, path, token string, body any, tenant string) (int, string) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.RemoteAddr = "10.0.0.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if tenant != "" {
			req.Header.Set("X-Olivares-Tenant", tenant)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	if code, body := do("POST", "/v1/setup", "", map[string]any{
		"token": setupToken, "email": "root@olivares.test", "password": "Sup3r-secret-passphrase!",
	}, ""); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	code, body := do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@olivares.test", "password": "Sup3r-secret-passphrase!",
	}, "")
	var login struct {
		Token string `json:"token"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &login) != nil || login.Token == "" {
		t.Fatalf("login: %d %s", code, body)
	}
	tenant := createOrganizationFixture(t, eng.store, "Acme", "acme").TenantID.String()

	code, body = do("POST", "/v1/m/eventing/subscriptions", login.Token, map[string]any{
		"name": "siem", "endpoint": "https://splunk.example.com:8088/services/collector",
		"event_types": []string{"finding.reported"}, "role": "viewer",
		"sink_kind": "splunk_hec", "sink_cred": "tok",
	}, tenant)
	if code != http.StatusNotImplemented || !strings.Contains(body, `"business_required"`) || !strings.Contains(body, "Business") {
		t.Fatalf("a SIEM sink on the booted Community engine = %d %s; want 501 business_required naming Business", code, body)
	}
}
