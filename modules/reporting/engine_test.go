// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"strings"
	"testing"
	"time"
)

func TestRenderHTMLEscapesReportDataAndUsesTemplateFunctions(t *testing.T) {
	e := NewEngine(nil)
	html, err := e.RenderHTML(ReportComplianceEvidence, ComplianceData{
		Generated:  time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
		Disclaimer: "done",
		Frameworks: []FrameworkReport{{
			Name:    "SOC 2",
			Version: "2026",
			Summary: AssessmentSummary{Total: 0},
			Controls: []ControlReport{{
				ID:          "CC1.1",
				Title:       "<script>alert(1)</script>",
				Status:      "gap",
				Requirement: "Board oversight.",
			}},
		}},
	}, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)
	if strings.Contains(s, "<script>alert(1)</script>") {
		t.Fatalf("report data was not escaped: %s", s)
	}
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "badge-gap", "0%"} {
		if !strings.Contains(s, want) {
			t.Fatalf("rendered HTML missing %q", want)
		}
	}
}

func TestFormatDateZeroAndDateTime(t *testing.T) {
	if got := formatDate(time.Time{}); got != string(rune(0x2014)) {
		t.Fatalf("zero formatDate = %q, want em dash", got)
	}
	when := time.Date(2026, 7, 3, 4, 5, 0, 0, time.UTC)
	if got := formatDateTime(when); got != "2026-07-03 04:05 UTC" {
		t.Fatalf("formatDateTime = %q", got)
	}
}
