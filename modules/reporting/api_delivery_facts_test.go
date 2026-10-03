// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

type catalogSigningSource struct {
	EnterpriseReportSource
	ready  bool
	reason string
}

func (s catalogSigningSource) BundleSigningStatus() (bool, string) { return s.ready, s.reason }

type catalogLegacySource struct{ EnterpriseReportSource }

func TestReportCatalogReflectsAvailableFormatsAndSigning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pdf    bool
		source EnterpriseReportSource
		ready  bool
		reason string
	}{
		{name: "community without renderer", reason: "evidence bundle engine is unavailable"},
		{name: "unsigned without renderer", source: catalogSigningSource{reason: "no signing key configured"}, reason: "no signing key configured"},
		{name: "unusable key with renderer", pdf: true, source: catalogSigningSource{reason: "configured signing key is not usable"}, reason: "configured signing key is not usable"},
		{name: "signed with renderer", pdf: true, source: catalogSigningSource{ready: true}, ready: true},
		{name: "legacy source", source: catalogLegacySource{}, reason: "signing readiness is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.pdf {
				// Only exercise the same executable lookup as generation; no browser is run.
				name := "chromium"
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir)
			m := New(WithEnterpriseReports(tc.source))
			rec := httptest.NewRecorder()
			m.handleListReports(rec, httptest.NewRequest(http.MethodGet, "/reports", nil), api.ModuleContext{})
			if rec.Code != http.StatusOK {
				t.Fatalf("catalog status = %d", rec.Code)
			}
			var response struct {
				Items         []ReportMeta `json:"items"`
				BundleSigning struct {
					Ready  *bool   `json:"ready"`
					Reason *string `json:"reason"`
				} `json:"bundle_signing"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			want := []Format{FormatHTML}
			if tc.pdf {
				want = append(want, FormatPDF)
			}
			if len(response.Items) != 5 {
				t.Fatalf("catalog report count = %d, want 5", len(response.Items))
			}
			for _, item := range response.Items {
				if !reflect.DeepEqual(item.Formats, want) {
					t.Errorf("%s advertises formats %v, want %v", item.Type, item.Formats, want)
				}
			}
			if response.BundleSigning.Ready == nil || *response.BundleSigning.Ready != tc.ready || response.BundleSigning.Reason == nil || *response.BundleSigning.Reason != tc.reason {
				t.Fatalf("catalog must report bound-engine signing readiness: ready=%v reason=%v", response.BundleSigning.Ready, response.BundleSigning.Reason)
			}
		})
	}
}
