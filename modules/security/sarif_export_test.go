// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSARIFExportMapsFindingsFiltersAndDrainsAllPages(t *testing.T) {
	h := newHarness(t, func(pub ed25519.PublicKey) []Option {
		return []Option{WithCheckpointKey(pub), WithEngineVersion("test-build")}
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sarif-export")
	viewer := h.roleToken(admin, tenant, "sarif-viewer@example.com", "viewer")
	subjectID := model.NewID()

	var matching []model.Finding
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		base := model.Finding{
			Kind: "prompt_injection", Severity: model.SeverityCritical,
			Status: model.FindingOpen, Source: "guardrail", SubjectKind: "agent",
			SubjectID: subjectID, Title: "Untrusted instructions reached an agent",
			OccurredAt: model.NewTimestamp(time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)),
			Metadata: map[string]any{
				"rule_ref":     "OLV-PI-1",
				"artifact_uri": "policies/agents/prompt.cedar",
				"owasp_llm":    []string{"LLM01:2025"},
				"owasp_asi":    []string{"ASI01"},
				"atlas":        []string{"AML.T0051.001"},
			},
		}
		for i := 0; i < 2; i++ {
			created, err := sc.Findings().Create(context.Background(), base)
			if err != nil {
				return err
			}
			matching = append(matching, created)
		}
		nonMatching := []model.Finding{
			withFinding(base, func(f *model.Finding) { f.Kind = "jailbreak" }),
			withFinding(base, func(f *model.Finding) { f.Severity = model.SeverityHigh }),
			withFinding(base, func(f *model.Finding) { f.Status = model.FindingResolved }),
			withFinding(base, func(f *model.Finding) { f.Source = "redteam" }),
			withFinding(base, func(f *model.Finding) { f.SubjectKind = "session" }),
		}
		for _, finding := range nonMatching {
			if _, err := sc.Findings().Create(context.Background(), finding); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed findings: %v", err)
	}

	path := "/v1/m/security/findings/export?format=sarif&limit=1" +
		"&kind=prompt_injection&severity=critical&status=open&source=guardrail&subject_kind=agent"
	rec := doSARIFExportRequest(h, path, viewer, tenant)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/sarif+json" {
		t.Fatalf("Content-Type = %q, want application/sarif+json", got)
	}
	if got := rec.Header().Get("X-Olivares-Truncated"); got != "" {
		t.Fatalf("X-Olivares-Truncated = %q below cap, want absent", got)
	}

	var doc sarifTestDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode SARIF: %v\n%s", err, rec.Body.String())
	}
	if doc.Version != "2.1.0" || len(doc.Runs) != 1 {
		t.Fatalf("SARIF envelope = version %q, runs %d", doc.Version, len(doc.Runs))
	}
	run := doc.Runs[0]
	if run.AutomationDetails.ID != "security/findings" {
		t.Fatalf("automationDetails.id = %q", run.AutomationDetails.ID)
	}
	if run.Tool.Driver.Name != "Olivares AI" || run.Tool.Driver.Version != "test-build" {
		t.Fatalf("tool driver = %+v", run.Tool.Driver)
	}
	if len(run.Tool.Driver.Rules) != 1 {
		t.Fatalf("rules = %d, want one de-duplicated rule", len(run.Tool.Driver.Rules))
	}
	rule := run.Tool.Driver.Rules[0]
	if rule.ID != "OLV-PI-1" || rule.DefaultConfiguration.Level != "error" ||
		rule.Properties.SecuritySeverity != "9.5" {
		t.Fatalf("rule mapping = %+v", rule)
	}
	wantTags := []string{
		"external/owasp_llm/LLM01:2025",
		"external/owasp_asi/ASI01",
		"external/atlas/AML.T0051.001",
	}
	if strings.Join(rule.Properties.Tags, "|") != strings.Join(wantTags, "|") {
		t.Fatalf("tags = %v, want %v", rule.Properties.Tags, wantTags)
	}
	if len(run.Results) != 2 {
		t.Fatalf("results = %d, want both matches despite limit=1", len(run.Results))
	}
	wantFingerprints := make(map[string]bool, len(matching))
	for _, finding := range matching {
		sum := sha256.Sum256([]byte(tenant.String() + "/" + finding.ID.String()))
		wantFingerprints[hex.EncodeToString(sum[:])] = true
	}
	for _, result := range run.Results {
		if result.RuleID != "OLV-PI-1" || result.RuleIndex != 0 || result.Level != "error" {
			t.Errorf("result rule mapping = %+v", result)
		}
		if result.Message.Text != baseFindingTitle(matching) {
			t.Errorf("message.text = %q", result.Message.Text)
		}
		if len(result.Locations) != 1 {
			t.Fatalf("locations = %d, want 1", len(result.Locations))
		}
		location := result.Locations[0].PhysicalLocation
		if location.ArtifactLocation.URI != "policies/agents/prompt.cedar" ||
			location.ArtifactLocation.URIBaseID != "%SRCROOT%" || location.Region.StartLine != 1 {
			t.Errorf("physical location = %+v", location)
		}
		if !wantFingerprints[result.PartialFingerprints.PrimaryLocationLineHash] {
			t.Errorf("fingerprint = %q, not derived from tenant/finding id",
				result.PartialFingerprints.PrimaryLocationLineHash)
		}
	}

	if sarifFindingLimit != 25000 {
		t.Fatalf("SARIF hard cap = %d, want GitHub per-run limit 25000", sarifFindingLimit)
	}
	internal := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	New(WithEngineVersion("test-build")).writeSARIFExport(internal, req, api.ModuleContext{
		Tenant: tenant,
		Data:   api.NewScopedData(h.st, tenant),
	}, 1)
	if internal.Code != http.StatusOK {
		t.Fatalf("bounded export = %d %s", internal.Code, internal.Body.String())
	}
	if got := internal.Header().Get("X-Olivares-Truncated"); got != "true" {
		t.Fatalf("bounded X-Olivares-Truncated = %q, want true", got)
	}
	var bounded sarifTestDocument
	if err := json.Unmarshal(internal.Body.Bytes(), &bounded); err != nil {
		t.Fatalf("decode bounded SARIF: %v", err)
	}
	if got := len(bounded.Runs[0].Results); got != 1 {
		t.Fatalf("bounded results = %d, want 1", got)
	}
}

func TestSARIFExportRejectsUnknownFormat(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sarif-format")

	rec := doSARIFExportRequest(h, "/v1/m/security/findings/export?format=csv", admin, tenant)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown format = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "valid values: sarif") {
		t.Fatalf("unknown-format response does not name valid values: %s", rec.Body.String())
	}
}

// The export must not widen read access: a principal with a role only in
// ANOTHER tenant gets no findings export from this one.
func TestSARIFExportDeniedWithoutTenantMembership(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "sarif-authz-a")
	tenantB := h.createOrg(admin, "sarif-authz-b")
	outsider := h.roleToken(admin, tenantB, "sarif-outsider@example.com", "viewer")

	rec := doSARIFExportRequest(h, "/v1/m/security/findings/export?format=sarif", outsider, tenantA)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant export = %d, want 403/404: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"runs"`) {
		t.Fatalf("cross-tenant export leaked a SARIF body: %s", rec.Body.String())
	}
}

func TestFindingToSARIFSeverityAndFallbacks(t *testing.T) {
	tenant := model.NewTenantID()
	findingID := model.NewID()
	subjectID := model.NewID()
	for _, tc := range []struct {
		severity model.Severity
		level    string
		score    string
	}{
		{model.SeverityCritical, "error", "9.5"},
		{model.SeverityHigh, "error", "8.0"},
		{model.SeverityMedium, "warning", "5.0"},
		{model.SeverityLow, "note", "2.0"},
		{model.Severity("info"), "note", "0.5"},
	} {
		finding := model.Finding{
			BaseFields: model.BaseFields{ID: findingID},
			Kind:       "guardrail", Severity: tc.severity, SubjectKind: "agent",
			SubjectID: subjectID, Title: "fallback finding",
		}
		got := findingToSARIF(tenant, finding)
		if got.Level != tc.level || got.SecuritySeverity != tc.score {
			t.Errorf("%q => level %q score %q, want %q/%q",
				tc.severity, got.Level, got.SecuritySeverity, tc.level, tc.score)
		}
		if got.RuleID != "olv-guardrail" {
			t.Errorf("fallback rule id = %q", got.RuleID)
		}
		if got.ArtifactURI != "governance/agent/"+subjectID.String() {
			t.Errorf("fallback artifact URI = %q", got.ArtifactURI)
		}
		sum := sha256.Sum256([]byte(tenant.String() + "/" + findingID.String()))
		if got.Fingerprint != hex.EncodeToString(sum[:]) {
			t.Errorf("fallback fingerprint = %q", got.Fingerprint)
		}
	}
}

func doSARIFExportRequest(h *harness, path, token string, tenant model.TenantID) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func withFinding(in model.Finding, mutate func(*model.Finding)) model.Finding {
	out := in
	out.Metadata = make(map[string]any, len(in.Metadata))
	for key, value := range in.Metadata {
		out.Metadata[key] = value
	}
	mutate(&out)
	return out
}

func baseFindingTitle(findings []model.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	return findings[0].Title
}

type sarifTestDocument struct {
	Version string `json:"version"`
	Runs    []struct {
		AutomationDetails struct {
			ID string `json:"id"`
		} `json:"automationDetails"`
		Tool struct {
			Driver struct {
				Name           string `json:"name"`
				Version        string `json:"version"`
				InformationURI string `json:"informationUri"`
				Rules          []struct {
					ID                   string `json:"id"`
					DefaultConfiguration struct {
						Level string `json:"level"`
					} `json:"defaultConfiguration"`
					Properties struct {
						Tags             []string `json:"tags"`
						SecuritySeverity string   `json:"security-severity"`
					} `json:"properties"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID    string `json:"ruleId"`
			RuleIndex int    `json:"ruleIndex"`
			Level     string `json:"level"`
			Message   struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI       string `json:"uri"`
						URIBaseID string `json:"uriBaseId"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
			PartialFingerprints struct {
				PrimaryLocationLineHash string `json:"primaryLocationLineHash"`
			} `json:"partialFingerprints"`
		} `json:"results"`
	} `json:"runs"`
}
