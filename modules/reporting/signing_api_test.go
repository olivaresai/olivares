// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type signingRoutes struct {
	recordingRoutes
	system map[string]api.ModuleHandler
}

func (r *signingRoutes) HandleSystem(method, path string, h api.ModuleHandler) {
	if r.system == nil {
		r.system = map[string]api.ModuleHandler{}
	}
	r.system[method+" "+path] = h
}

type managedSigningSource struct {
	EnterpriseReportSource
	status SigningStatus
	writes int
	actor  auth.Principal
}

func (s *managedSigningSource) ReportingSigning(context.Context) (SigningStatus, error) {
	return s.status, nil
}
func (s *managedSigningSource) SetReportingSigning(_ context.Context, p auth.Principal, enabled bool) (SigningStatus, error) {
	s.writes++
	s.actor = p
	s.status.Enabled = enabled
	s.status.Ready = enabled
	return s.status, nil
}

func TestReportingSigningUsesSystemRoutesAndRejectsInvalidUpdates(t *testing.T) {
	source := &managedSigningSource{status: SigningStatus{Enabled: true, Ready: true, KeyID: "test", PublicKey: "public-key", Source: "product"}}
	m := New(WithEnterpriseReports(source))
	reg := &signingRoutes{}
	m.APIRoutes(reg)
	get, put := reg.system["GET /signing"], reg.system["PUT /signing"]
	if get == nil || put == nil {
		t.Fatal("signing is not registered through the native system-admin gate")
	}
	for _, route := range reg.routes {
		if route.pattern == "/signing" {
			t.Fatal("deployment signing key is reachable through a tenant route")
		}
	}
	rec := httptest.NewRecorder()
	get(rec, httptest.NewRequest(http.MethodGet, "/signing", nil), api.ModuleContext{})
	var status SigningStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || rec.Code != 200 || !status.Ready || status.KeyID != "test" || status.PublicKey != "public-key" {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{}`, `{"enabled":"yes"}`, `{"enabled":true,"private_key":"forbidden-input"}`, `{"enabled":true} {}`} {
		rec = httptest.NewRecorder()
		put(rec, httptest.NewRequest(http.MethodPut, "/signing", strings.NewReader(body)), api.ModuleContext{})
		if rec.Code != http.StatusBadRequest || source.writes != 0 {
			t.Fatalf("invalid input reached key manager: status=%d", rec.Code)
		}
	}
	actor := auth.Principal{Superadmin: true}
	rec = httptest.NewRecorder()
	put(rec, httptest.NewRequest(http.MethodPut, "/signing", strings.NewReader(`{"enabled":false}`)), api.ModuleContext{Principal: actor})
	if rec.Code != 200 || source.writes != 1 || !source.actor.Superadmin {
		t.Fatal("admitted actor or update was lost")
	}
	doc := api.ModuleOpenAPIDocument([]api.Module{m})
	operation := doc["paths"].(map[string]any)["/v1/m/reporting/signing"].(map[string]any)["put"].(map[string]any)
	if operation["x-olivares-scope"] != "system" || operation["x-required-permission"] != "system:admin" {
		t.Fatal("signing documentation lost system authority")
	}
	body := operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["additionalProperties"] != false || body["properties"].(map[string]any)["enabled"].(map[string]any)["type"] != "boolean" {
		t.Fatal("signing documentation does not match the strict enabled decoder")
	}
}

func TestReportingSigningPreservesRequestBodyRefusals(t *testing.T) {
	const enabled = `{"enabled":true}`
	atLimit := enabled + strings.Repeat(" ", 1024-len(enabled))
	for _, tc := range []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"empty", "", http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"missing enabled", `{}`, http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"null enabled", `{"enabled":null}`, http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"wrong type", `{"enabled":"private-value"}`, http.StatusBadRequest, `Choose whether report signing is enabled.: invalid value for field "enabled"`},
		{"unknown field", `{"enabled":true,"extra":true}`, http.StatusBadRequest, `Choose whether report signing is enabled.: unknown field "extra"`},
		{"malformed first document", `{"enabled":true`, http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"oversized first document", strings.Repeat(" ", 1024) + enabled, http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"missing enabled before second document", `{} {}`, http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"null enabled before malformed tail", `{"enabled":null} }`, http.StatusBadRequest, "Choose whether report signing is enabled."},
		{"second document", enabled + ` {}`, http.StatusBadRequest, "Send one signing setting."},
		{"second scalar", enabled + ` false`, http.StatusBadRequest, "Send one signing setting."},
		{"malformed tail", enabled + ` }`, http.StatusBadRequest, "Send one signing setting."},
		{"oversized trailing whitespace", atLimit + " ", http.StatusBadRequest, "Send one signing setting."},
		{"exact byte limit", atLimit, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &managedSigningSource{}
			m := New(WithEnterpriseReports(source))
			reg := &signingRoutes{}
			m.APIRoutes(reg)
			rec := httptest.NewRecorder()
			reg.system["PUT /signing"](rec, httptest.NewRequest(http.MethodPut, "/signing", strings.NewReader(tc.body)), api.ModuleContext{})
			if rec.Code != tc.status {
				t.Fatalf("status=%d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				if source.writes != 1 || !source.status.Enabled {
					t.Fatal("valid input did not enable signing exactly once")
				}
				return
			}
			if source.writes != 0 {
				t.Fatal("refused input reached the signing manager")
			}
			var response struct {
				Error struct {
					Message string `json:"message"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Message != tc.message || response.Error.Code != http.StatusText(tc.status) {
				t.Fatalf("refusal=%+v, want %q / %q", response.Error, http.StatusText(tc.status), tc.message)
			}
		})
	}
}
