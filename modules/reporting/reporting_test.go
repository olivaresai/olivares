// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"testing"
	"time"
)

func TestNewEngine(t *testing.T) {
	e := NewEngine(nil)
	if e == nil {
		t.Fatal("NewEngine returned nil")
	}
	if e.tmpl == nil {
		t.Fatal("templates not loaded")
	}
}

func TestRenderHTML_ComplianceEvidence(t *testing.T) {
	e := NewEngine(nil)
	data := ComplianceData{
		Generated:  time.Now(),
		Disclaimer: "Test disclaimer.",
		Frameworks: []FrameworkReport{
			{
				ID: "iso_27001", Name: "ISO/IEC 27001", Version: "2022", Authority: "ISO",
				Summary: AssessmentSummary{Total: 10, Satisfied: 7, ByDesign: 1, Partial: 1, Gap: 1},
				Controls: []ControlReport{
					{ID: "A.5.1", Title: "Policies for information security", Status: "satisfied", Requirement: "Defined and approved by management."},
					{ID: "A.8.1", Title: "Asset management", Status: "gap", Requirement: "Inventory of assets.", Note: "No asset inventory found."},
				},
			},
		},
	}
	html, err := e.RenderHTML(ReportComplianceEvidence, data, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if len(html) == 0 {
		t.Fatal("empty HTML output")
	}
	s := string(html)
	if !contains(s, "ISO/IEC 27001") {
		t.Error("missing framework name in output")
	}
	if !contains(s, "A.5.1") {
		t.Error("missing control ID in output")
	}
	if !contains(s, "satisfied") {
		t.Error("missing status badge in output")
	}
}

func TestRenderHTML_AuditSummary(t *testing.T) {
	e := NewEngine(nil)
	data := AuditData{
		From:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		To:              time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		TotalEvents:     1234,
		CheckpointOK:    true,
		CheckpointCount: 5,
		LedgerHead:      1234,
		EventsByAction: []ActionCount{
			{Action: "agent.create", Count: 50},
			{Action: "policy.update", Count: 30},
		},
	}
	html, err := e.RenderHTML(ReportAuditSummary, data, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)
	if !contains(s, "agent.create") {
		t.Error("missing action in output")
	}
}

func TestRenderHTML_FinOps(t *testing.T) {
	e := NewEngine(nil)
	data := FinOpsData{
		From:          time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		TotalMicroUSD: 150_000_000,
		InputTokens:   5_000_000,
		OutputTokens:  1_200_000,
		Samples:       500,
		ByModel:       []SpendBucket{{Key: "claude-opus-4", CostMicroUSD: 100_000_000, InputTokens: 3_000_000, OutputTokens: 800_000, Samples: 300}},
		ByProvider:    []SpendBucket{{Key: "anthropic", CostMicroUSD: 150_000_000, Samples: 500}},
		Budgets: []BudgetStatus{
			{ID: "b1", Name: "Monthly AI", LimitMicroUSD: 200_000_000, SpendMicroUSD: 150_000_000, ConsumedPct: 75, Over: false},
		},
	}
	html, err := e.RenderHTML(ReportFinOps, data, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)
	if !contains(s, "claude-opus-4") {
		t.Error("missing model name in output")
	}
	if !contains(s, "$150") {
		t.Error("missing total spend in output")
	}
}

func TestRenderHTML_AccessReview(t *testing.T) {
	e := NewEngine(nil)
	data := AccessData{
		Generated: time.Now(),
		Users: []UserAccess{
			{UserID: "u1", DisplayName: "Alice Admin", Email: "alice@example.com", Roles: []string{"admin"}, Superadmin: true},
			{UserID: "u2", DisplayName: "Bob User", Email: "bob@example.com", Roles: []string{"viewer"}, Inactive: true},
		},
	}
	html, err := e.RenderHTML(ReportAccessReview, data, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)
	if !contains(s, "Alice Admin") {
		t.Error("missing user name in output")
	}
}

func TestRenderHTML_ExecutiveSummary(t *testing.T) {
	e := NewEngine(nil)
	data := ExecutiveData{
		Generated:     time.Now(),
		From:          time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		TotalSpendUSD: 150.50,
		ActiveAgents:  25,
		ActiveUsers:   10,
		FindingsOpen:  3,
		ComplianceSummary: []FrameworkBrief{
			{Name: "ISO 27001", SatisfiedPct: 80, GapCount: 2},
		},
		AuditIntegrityOK: true,
	}
	html, err := e.RenderHTML(ReportExecutiveSummary, data, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)
	if !contains(s, "ISO 27001") {
		t.Error("missing framework in output")
	}
}

func TestRenderHTML_WithBranding(t *testing.T) {
	e := NewEngine(nil)
	data := ExecutiveData{Generated: time.Now(), From: time.Now(), To: time.Now()}
	branding := BrandingConfig{CompanyName: "Acme Corp", FooterText: "Confidential"}
	html, err := e.RenderHTML(ReportExecutiveSummary, data, "es", branding)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)
	if !contains(s, "Acme Corp") {
		t.Error("missing company name in branded output")
	}
	if !contains(s, "Confidential") {
		t.Error("missing footer text in branded output")
	}
}

func TestRenderHTML_UnknownTemplate(t *testing.T) {
	e := NewEngine(nil)
	_, err := e.RenderHTML("nonexistent", nil, "en", BrandingConfig{})
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestI18n(t *testing.T) {
	v := T("en", "report.compliance_evidence.title")
	if v == "report.compliance_evidence.title" {
		t.Error("English translation not found")
	}
	v = T("es", "report.compliance_evidence.title")
	if v == "report.compliance_evidence.title" {
		t.Error("Spanish translation not found")
	}
	v = T("xx", "report.compliance_evidence.title")
	if v == "report.compliance_evidence.title" {
		t.Error("Fallback to English failed")
	}
}

func TestI18n_AllLocales(t *testing.T) {
	for _, locale := range SupportedLocales() {
		v := T(locale, "report.compliance_evidence.title")
		if v == "report.compliance_evidence.title" {
			t.Errorf("translation not found for locale %s", locale)
		}
	}
}

func TestFormatMicroUSD(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "$0.0000"},
		{500_000, "$0.5000"},
		{1_000_000, "$1.00"},
		{150_000_000, "$150.00"},
		{1_500_000_000, "$1500"},
	}
	for _, tt := range tests {
		got := formatMicroUSD(tt.input)
		if got != tt.want {
			t.Errorf("formatMicroUSD(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1500, "1.5K"},
		{1_500_000, "1.5M"},
		{1_500_000_000, "1.5B"},
	}
	for _, tt := range tests {
		got := formatNumber(tt.input)
		if got != tt.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCache(t *testing.T) {
	c := NewCache(nil)
	defer c.Close()

	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("cache hit on empty cache")
	}

	c.Put("k1", []byte("hello"))
	data, ok := c.Get("k1")
	if !ok {
		t.Fatal("cache miss after put")
	}
	if string(data) != "hello" {
		t.Errorf("cached data = %q, want %q", data, "hello")
	}
}

func TestPDFAvailable(t *testing.T) {
	// Just verify the function doesn't panic.
	_ = PDFAvailable()
}

func TestValidReportType(t *testing.T) {
	if !validReportType(ReportComplianceEvidence) {
		t.Error("compliance-evidence should be valid")
	}
	if validReportType("invalid") {
		t.Error("invalid type should not be valid")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (len(s) >= len(substr)) && (s == substr || len(s) > len(substr) && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
