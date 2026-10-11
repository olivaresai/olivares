//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package reporting

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func TestCommunityOnDemandReportsUnavailable(t *testing.T) {
	m := newReportingTestModule(t)
	for _, path := range []string{"/reports", "/reports/audit-summary"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := reportRequest(path, ReportAuditSummary)
			if path == "/reports" {
				m.handleListReports(w, r, api.ModuleContext{Data: servedData{}})
			} else {
				m.handleGenerateReport(w, r, api.ModuleContext{Data: servedData{}})
			}
			if w.Code != http.StatusNotImplemented {
				t.Errorf("%s = %d; want 501", path, w.Code)
			}
		})
	}
}
