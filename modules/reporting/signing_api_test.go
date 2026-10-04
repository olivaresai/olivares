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

func TestReportingSigningBodyLimitAndTrailingRefusals(t *testing.T) {
	body := `{"enabled":true}`
	for _, tc := range []struct {
		name, body, message string
		status              int
	}{
		{"exact limit", body + strings.Repeat(" ", 1024-len(body)), "", http.StatusOK},
		{"over limit", body + strings.Repeat(" ", 1025-len(body)), "Send one signing setting.", http.StatusBadRequest},
		{"second document", body + `{}`, "Send one signing setting.", http.StatusBadRequest},
		{"malformed tail", body + `]`, "Send one signing setting.", http.StatusBadRequest},
		{"missing choice precedes tail", `{}]`, "Choose whether report signing is enabled.", http.StatusBadRequest},
		{"unknown field", `{"enabled":true,"other":false}`, "Choose whether report signing is enabled.", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &managedSigningSource{}
			m := New(WithEnterpriseReports(source))
			rec := httptest.NewRecorder()
			m.handleSetSigning(rec, httptest.NewRequest(http.MethodPut, "/signing", strings.NewReader(tc.body)), api.ModuleContext{})
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				if source.writes != 1 || !source.status.Enabled {
					t.Fatal("valid signing choice was not applied")
				}
				return
			}
			var response struct {
				Error struct{ Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if source.writes != 0 || response.Error.Message != tc.message {
				t.Fatalf("refusal changed or wrote signing state: writes=%d body=%s", source.writes, rec.Body.String())
			}
		})
	}
}
