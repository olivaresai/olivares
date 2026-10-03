// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/license"
)

func TestReportingSigningCommunityRefusalNamesBusiness(t *testing.T) {
	m := New()
	reg := &signingRoutes{}
	m.APIRoutes(reg)
	for _, tc := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPut, `{"enabled":true}`},
		{http.MethodPut, `{"enabled":false}`},
	} {
		t.Run(tc.method+tc.body, func(t *testing.T) {
			handler := reg.system[tc.method+" /signing"]
			if handler == nil {
				t.Fatal("signing route is missing its system-admin registration")
			}
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(tc.method, "/v1/m/reporting/signing", strings.NewReader(tc.body)), api.ModuleContext{Principal: auth.Principal{Superadmin: true}})
			var response struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusNotImplemented || response.Error.Message != "Report signing is part of Business; this edition does not include it." {
				t.Fatalf("Community signing refusal: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

type signingErrorSource struct {
	managedSigningSource
	err error
}

func (s *signingErrorSource) SetReportingSigning(context.Context, auth.Principal, bool) (SigningStatus, error) {
	return SigningStatus{}, s.err
}

func TestReportingSigningLicenseRefusalCarriesAdmissionCode(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
		status     int
	}{
		{"typed refusal", "addon_requires_license", license.AddonRequired("reporting", "configure-report-signing"), http.StatusForbidden},
		{"wrapped refusal", "addon_requires_license", fmt.Errorf("request: %w", license.AddonRequired("reporting", "configure-report-signing")), http.StatusForbidden},
		{"sentinel refusal", "addon_requires_license", license.ErrAddonRequiresLicense, http.StatusForbidden},
		{"store fault", http.StatusText(http.StatusServiceUnavailable), errors.New("private store failure"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(WithEnterpriseReports(&signingErrorSource{err: tc.err}))
			reg := &signingRoutes{}
			m.APIRoutes(reg)
			handler := reg.system["PUT /signing"]
			if handler == nil {
				t.Fatal("signing route is missing its system-admin registration")
			}
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodPut, "/v1/m/reporting/signing", strings.NewReader(`{"enabled":true}`)), api.ModuleContext{Principal: auth.Principal{Superadmin: true}})
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || response.Error.Code != tc.code || response.Error.Message == "" {
				t.Fatalf("license refusal lost its admission code: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusServiceUnavailable && response.Error.Message == tc.err.Error() {
				t.Fatal("unexpected signing fault exposed its internal message")
			}
		})
	}
}
