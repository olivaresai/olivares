// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestReportMetadataAndEnterpriseTypesRoundTripJSON(t *testing.T) {
	meta := ReportMeta{
		Type:        ReportFinOps,
		Title:       "FinOps",
		Description: "Spend",
		Formats:     []Format{FormatHTML, FormatPDF},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal ReportMeta: %v", err)
	}
	var decoded ReportMeta
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal ReportMeta: %v", err)
	}
	if decoded.Type != ReportFinOps || decoded.Formats[1] != FormatPDF {
		t.Fatalf("decoded ReportMeta = %+v", decoded)
	}

	schedule := ScheduleConfig{ID: "s1", ReportType: ReportAuditSummary, Format: FormatHTML, Cron: "0 8 * * 1", Enabled: true}
	data, err = json.Marshal(schedule)
	if err != nil {
		t.Fatalf("marshal ScheduleConfig: %v", err)
	}
	if !jsonContains(data, `"report_type":"audit-summary"`) || !jsonContains(data, `"enabled":true`) {
		t.Fatalf("schedule JSON = %s", data)
	}

	branding := BrandingConfig{CompanyName: "Acme", FooterText: "Internal"}
	data, err = json.Marshal(branding)
	if err != nil {
		t.Fatalf("marshal BrandingConfig: %v", err)
	}
	if jsonContains(data, "logo_path") || !jsonContains(data, `"company_name":"Acme"`) {
		t.Fatalf("branding JSON = %s", data)
	}
}

func TestComplianceSourceContractCarriesTenantAndFramework(t *testing.T) {
	src := &recordingComplianceSource{}
	tenant := model.TenantID("tenant-a")
	got, err := src.GatherComplianceData(context.Background(), tenant, "iso_42001")
	if err != nil {
		t.Fatalf("GatherComplianceData: %v", err)
	}
	if src.tenant != tenant || src.framework != "iso_42001" {
		t.Fatalf("source saw tenant/framework = %q/%q", src.tenant, src.framework)
	}
	if got.Frameworks[0].ID != "iso_42001" {
		t.Fatalf("framework ID = %q", got.Frameworks[0].ID)
	}
}

type recordingComplianceSource struct {
	tenant    model.TenantID
	framework string
}

func (r *recordingComplianceSource) GatherComplianceData(_ context.Context, tenant model.TenantID, framework string) (ComplianceData, error) {
	r.tenant = tenant
	r.framework = framework
	return ComplianceData{Frameworks: []FrameworkReport{{ID: framework}}}, nil
}

func jsonContains(data []byte, sub string) bool {
	return stringsContains(string(data), sub)
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
