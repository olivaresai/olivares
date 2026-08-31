// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// govDoer multiplexes by request path to the governance fixtures, extending the
// fixtureDoer pattern from openai_test.go. It supports returning 403/404 for
// selected paths (the degradation tests) and records every request.
type govDoer struct {
	t           *testing.T
	reqs        []*http.Request
	unavailable map[string]bool // path -> return 403
	statuses    map[string]int  // path -> return status
	fixtures    map[string]string
}

func (d *govDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if status, ok := d.statuses[req.URL.Path]; ok {
		return govResp(status, `{"error":"unavailable"}`), nil
	}
	if d.unavailable[req.URL.Path] {
		return govResp(403, `{"error":"forbidden"}`), nil
	}
	var file string
	if override, ok := d.fixtures[req.URL.Path]; ok {
		file = override
	} else {
		switch {
		case req.URL.Path == "/v1/organization/users":
			file = "org_users.json"
		case req.URL.Path == "/v1/organization/audit_logs":
			file = "audit_logs.json"
		case req.URL.Path == "/v1/organization/data_retention":
			file = "data_retention.json"
		case strings.HasPrefix(req.URL.Path, "/v1/organization/projects/") && strings.HasSuffix(req.URL.Path, "/data_retention"):
			// Per-project data retention: extract project ID.
			parts := strings.Split(req.URL.Path, "/")
			projID := parts[4] // /v1/organization/projects/{id}/data_retention
			file = "data_retention_" + projID + ".json"
			body, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				// Project-specific fixture not found: return same as org (no diff).
				body, err = os.ReadFile(filepath.Join("testdata", "data_retention.json"))
				if err != nil {
					d.t.Fatalf("read fixture: %v", err)
				}
			}
			return govResp(200, string(body)), nil
		case req.URL.Path == "/v1/organization/costs":
			file = "costs.json"
		case req.URL.Path == "/v1/organization/projects":
			file = "projects.json"
		case req.URL.Path == "/v1/organization/spend_alerts":
			file = "spend_alerts_org.json"
		case strings.HasPrefix(req.URL.Path, "/v1/organization/projects/") && strings.HasSuffix(req.URL.Path, "/spend_alerts"):
			parts := strings.Split(req.URL.Path, "/")
			projID := parts[4]
			file = "spend_alerts_" + projID + ".json"
			body, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				body, err = os.ReadFile(filepath.Join("testdata", "spend_alerts_empty.json"))
				if err != nil {
					d.t.Fatalf("read fixture: %v", err)
				}
			}
			return govResp(200, string(body)), nil
		case strings.HasPrefix(req.URL.Path, "/v1/organization/projects/") && strings.HasSuffix(req.URL.Path, "/model_permissions"):
			parts := strings.Split(req.URL.Path, "/")
			projID := parts[4]
			file = "model_permissions_" + projID + ".json"
		case strings.HasPrefix(req.URL.Path, "/v1/organization/projects/") && strings.HasSuffix(req.URL.Path, "/hosted_tool_permissions"):
			parts := strings.Split(req.URL.Path, "/")
			projID := parts[4]
			file = "hosted_tool_permissions_" + projID + ".json"
		case req.URL.Path == "/v1/organization/groups":
			file = "groups.json"
		case req.URL.Path == "/v1/organization/roles":
			file = "roles.json"
		case req.URL.Path == "/v1/organization/usage/completions":
			file = "usage_completions.json"
		case req.URL.Path == "/v1/organization/usage/moderations":
			file = "usage_moderations.json"
		case req.URL.Path == "/v1/models":
			file = "models.json"
		case req.URL.Path == "/v1/organization/admin_api_keys":
			file = "admin_api_keys.json"
		default:
			d.t.Fatalf("unexpected request path %q", req.URL.Path)
		}
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return govResp(200, string(body)), nil
}

func govResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// newGovSource builds a credentialed OpenAI source over the gov fixture doer.
func newGovSource(t *testing.T, doer *govDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{
		"api_key": "sk-openai-admin-test",
		"costs":   "false",
	}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// govFindings extracts FindingReport observations from the sink.
func govFindings(obs []model.Observation) []model.FindingReport {
	var out []model.FindingReport
	for _, o := range obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

// govCosts extracts CostSample observations from the sink.
func govCosts(obs []model.Observation) []model.CostSample {
	var out []model.CostSample
	for _, o := range obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

// ---------- Org Users tests ----------

func TestGatherOrgGraph_Success(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherOrgGraph(context.Background(), sink); err != nil {
		t.Fatalf("gatherOrgGraph: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 {
		t.Fatalf("emitted %d findings, want 1", len(fs))
	}
	f := fs[0]
	if f.Kind != "inventory" {
		t.Fatalf("kind = %q, want inventory", f.Kind)
	}
	if f.SubjectKind != subjectOrgUsers {
		t.Fatalf("subject kind = %q, want %s", f.SubjectKind, subjectOrgUsers)
	}
	if f.SubjectRef != "organization" {
		t.Fatalf("subject ref = %q, want organization", f.SubjectRef)
	}
	if f.Severity != model.SeverityInfo {
		t.Fatalf("severity = %q, want info", f.Severity)
	}
	// The fixture has 3 users (1 owner + 2 members); check the title carries counts.
	if !strings.Contains(f.Title, "3 user(s)") {
		t.Fatalf("title missing user count: %q", f.Title)
	}
	// DetailHash must be a SHA-256 hex.
	if len(f.DetailHash) != 64 {
		t.Fatalf("detail hash not sha-256 hex: %q", f.DetailHash)
	}
	// Minimal-data: the title must NOT contain individual email addresses.
	if strings.Contains(f.Title, "@") {
		t.Fatalf("title leaked PII: %q", f.Title)
	}
}

func TestGatherOrgGraph_403Degrades(t *testing.T) {
	doer := &govDoer{t: t, unavailable: map[string]bool{"/v1/organization/users": true}}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherOrgGraph(context.Background(), sink); err != nil {
		t.Fatalf("gatherOrgGraph must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable posture finding, got %+v", fs)
	}
	if fs[0].Severity != model.SeverityMedium {
		t.Fatalf("unavailable severity = %q, want medium", fs[0].Severity)
	}
	if !strings.Contains(fs[0].Title, "unavailable") {
		t.Fatalf("unavailable title = %q", fs[0].Title)
	}
}

// ---------- Audit Logs tests ----------

func TestGatherAuditLogs_Success(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 7 {
		t.Fatalf("emitted %d findings, want 7 (5 audit records + 2 derived posture)", len(fs))
	}
	var activity []model.FindingReport
	var wif, tunnel bool
	for _, f := range fs {
		if f.Kind == "posture" && f.SubjectRef == "workload_identity_federation" {
			wif = true
			if !strings.Contains(f.Title, "workload_identity_provider.created=1") ||
				!strings.Contains(f.Title, "workload_identity_provider_mapping.created=1") {
				t.Fatalf("WIF posture title missing counts: %q", f.Title)
			}
			continue
		}
		if f.Kind == "posture" && f.SubjectRef == "secure_mcp_tunnel" {
			tunnel = true
			if !strings.Contains(f.Title, "tunnel.created=1") {
				t.Fatalf("tunnel posture title missing counts: %q", f.Title)
			}
			continue
		}
		if f.Kind != findingKindActivity || f.SubjectKind != subjectAuditLog {
			t.Fatalf("audit finding = %+v", f)
		}
		activity = append(activity, f)
		if !strings.HasPrefix(f.SubjectRef, "audit_log-") {
			t.Fatalf("audit subject ref = %q", f.SubjectRef)
		}
		if f.Severity != model.SeverityInfo {
			t.Fatalf("audit severity = %q, want info", f.Severity)
		}
		// Actor email MUST NOT appear in the title.
		if strings.Contains(f.Title, "@") {
			t.Fatalf("audit title leaked PII: %q", f.Title)
		}
		if len(f.DetailHash) != 64 {
			t.Fatalf("detail hash not sha-256: %q", f.DetailHash)
		}
	}
	if len(activity) != 5 {
		t.Fatalf("activity findings = %d, want 5", len(activity))
	}
	if !wif || !tunnel {
		t.Fatalf("missing derived WIF/tunnel posture findings: wif=%v tunnel=%v", wif, tunnel)
	}
	// Check specific event types.
	if !strings.Contains(activity[0].Title, "api_key.created") {
		t.Fatalf("first audit title = %q, want api_key.created", activity[0].Title)
	}
	if !strings.Contains(activity[1].Title, "login.succeeded") {
		t.Fatalf("second audit title = %q, want login.succeeded", activity[1].Title)
	}
}

func TestGatherAuditLogs_NoWIFTunnelPostureWhenNoEvents(t *testing.T) {
	doer := &govDoer{t: t, fixtures: map[string]string{"/v1/organization/audit_logs": "audit_logs_no_wif_tunnel.json"}}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs: %v", err)
	}
	for _, f := range govFindings(sink.obs) {
		if f.SubjectRef == "workload_identity_federation" || f.SubjectRef == "secure_mcp_tunnel" {
			t.Fatalf("unexpected derived posture finding without matching events: %+v", f)
		}
	}
}

func TestGatherAuditLogs_403Degrades(t *testing.T) {
	doer := &govDoer{t: t, unavailable: map[string]bool{"/v1/organization/audit_logs": true}}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable posture finding, got %+v", fs)
	}
	if !strings.Contains(fs[0].Title, "unavailable") {
		t.Fatalf("unavailable title = %q", fs[0].Title)
	}
}

// ---------- Data Retention tests ----------

func TestGatherDataRetention_ZDREnabled(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherDataRetention(context.Background(), sink); err != nil {
		t.Fatalf("gatherDataRetention: %v", err)
	}
	fs := govFindings(sink.obs)
	// The fixture has ZDR=true at org level, and proj_default has ZDR=false/30d (different
	// from org). proj_old is archived so it is skipped. So we expect 2 findings.
	if len(fs) < 1 {
		t.Fatalf("emitted %d findings, want at least 1 (org-level)", len(fs))
	}
	orgF := fs[0]
	if orgF.Kind != "posture" {
		t.Fatalf("org finding kind = %q, want posture", orgF.Kind)
	}
	if orgF.SubjectKind != subjectDataRetention {
		t.Fatalf("subject kind = %q, want %s", orgF.SubjectKind, subjectDataRetention)
	}
	if orgF.SubjectRef != "organization" {
		t.Fatalf("subject ref = %q, want organization", orgF.SubjectRef)
	}
	// The fixture has ZDR enabled.
	if !strings.Contains(orgF.Title, "ZDR enabled") {
		t.Fatalf("title does not mention ZDR enabled: %q", orgF.Title)
	}
	if orgF.Severity != model.SeverityInfo {
		t.Fatalf("severity = %q, want info", orgF.Severity)
	}
	// Per-project finding for proj_default (ZDR=false != org ZDR=true).
	if len(fs) >= 2 {
		projF := fs[1]
		if projF.SubjectRef != "proj_default" {
			t.Fatalf("project subject ref = %q, want proj_default", projF.SubjectRef)
		}
		if !strings.Contains(projF.Title, "ZDR disabled") {
			t.Fatalf("project title does not mention ZDR disabled: %q", projF.Title)
		}
	}
}

func TestGatherDataRetention_V2TypeShape(t *testing.T) {
	doer := &govDoer{t: t, fixtures: map[string]string{
		"/v1/organization/projects":                             "projects_data_retention_v2.json",
		"/v1/organization/data_retention":                       "data_retention_v2.json",
		"/v1/organization/projects/proj_default/data_retention": "data_retention_v2_proj_default.json",
		"/v1/organization/projects/proj_zdr/data_retention":     "data_retention_v2_proj_zdr.json",
	}}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherDataRetention(context.Background(), sink); err != nil {
		t.Fatalf("gatherDataRetention: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 2 {
		t.Fatalf("emitted %d findings, want org + one project override", len(fs))
	}
	if fs[0].SubjectRef != "organization" ||
		!strings.Contains(fs[0].Title, "type modified_abuse_monitoring") ||
		!strings.Contains(fs[0].Title, "ZDR disabled") {
		t.Fatalf("org v2 finding = %+v", fs[0])
	}
	if fs[1].SubjectRef != "proj_zdr" ||
		!strings.Contains(fs[1].Title, "type enhanced_zero_data_retention") ||
		!strings.Contains(fs[1].Title, "ZDR enabled") {
		t.Fatalf("project v2 finding = %+v", fs[1])
	}
	for _, f := range fs {
		if strings.Contains(f.Title, "organization_default") {
			t.Fatalf("organization_default project should not emit as an override: %+v", f)
		}
	}
}

func TestGatherDataRetention_403Degrades(t *testing.T) {
	doer := &govDoer{t: t, unavailable: map[string]bool{"/v1/organization/data_retention": true}}
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherDataRetention(context.Background(), sink); err != nil {
		t.Fatalf("gatherDataRetention must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable posture finding, got %+v", fs)
	}
}

// ---------- Costs tests ----------

func TestGatherCosts_Success(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, map[string]string{"costs": "true"})
	sink := &captureSink{}
	if err := s.gatherCosts(context.Background(), sink); err != nil {
		t.Fatalf("gatherCosts: %v", err)
	}
	costs := govCosts(sink.obs)
	if len(costs) != 2 {
		t.Fatalf("emitted %d cost samples, want 2", len(costs))
	}
	// First row: $12.50 -> 12_500_000 micro-USD.
	if costs[0].CostMicroUSD != 12_500_000 {
		t.Fatalf("cost[0] = %d micro-USD, want 12500000", costs[0].CostMicroUSD)
	}
	if costs[0].Provenance != model.ProvenanceBilled {
		t.Fatalf("provenance = %q, want billed", costs[0].Provenance)
	}
	if costs[0].CostType != "GPT-4o" {
		t.Fatalf("cost type = %q, want GPT-4o (the line_item)", costs[0].CostType)
	}
	if costs[0].WorkspaceRef != "proj_default" {
		t.Fatalf("workspace = %q, want proj_default", costs[0].WorkspaceRef)
	}
	if costs[0].ProviderRef != modelprovider.ProviderOpenAI {
		t.Fatalf("provider = %q, want openai", costs[0].ProviderRef)
	}
	if costs[0].APIKeyRef != "key_default" {
		t.Fatalf("api key ref = %q, want key_default", costs[0].APIKeyRef)
	}
	if costs[0].Labels["line_item_quantity"] != "1500000" {
		t.Fatalf("line_item_quantity label = %q, want 1500000", costs[0].Labels["line_item_quantity"])
	}
	// Second row: $3.75 -> 3_750_000 micro-USD.
	if costs[1].CostMicroUSD != 3_750_000 {
		t.Fatalf("cost[1] = %d micro-USD, want 3750000", costs[1].CostMicroUSD)
	}
	if costs[1].APIKeyRef != "" {
		t.Fatalf("cost[1] api key ref = %q, want empty", costs[1].APIKeyRef)
	}
	if costs[1].Labels != nil {
		t.Fatalf("cost[1] labels = %+v, want nil", costs[1].Labels)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(doer.reqs))
	}
	groups := doer.reqs[0].URL.Query()["group_by[]"]
	if len(groups) != 3 || groups[0] != "line_item" || groups[1] != "project_id" || groups[2] != "api_key_id" {
		t.Fatalf("costs request group_by[] = %v, want [line_item project_id api_key_id]", groups)
	}
}

func TestGatherCosts_DisabledByDefault(t *testing.T) {
	doer := &govDoer{t: t}
	// costs defaults to false.
	s := newGovSource(t, doer, nil)
	sink := &captureSink{}
	// Use the full Gather to verify costs are not called.
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Verify no cost endpoint was called.
	for _, r := range doer.reqs {
		if r.URL.Path == "/v1/organization/costs" {
			t.Fatal("costs endpoint called when costs=false")
		}
	}
	// Should have zero cost samples from the costs surface.
	// (There may be CostSamples from gatherUsage, but not from gatherCosts.)
	for _, o := range sink.obs {
		if c, ok := o.(model.CostSample); ok {
			if c.Provenance == model.ProvenanceBilled {
				t.Fatalf("found a billed cost sample when costs=false: %+v", c)
			}
		}
	}
}

func TestGatherCosts_403Degrades(t *testing.T) {
	doer := &govDoer{t: t, unavailable: map[string]bool{"/v1/organization/costs": true}}
	s := newGovSource(t, doer, map[string]string{"costs": "true"})
	sink := &captureSink{}
	if err := s.gatherCosts(context.Background(), sink); err != nil {
		t.Fatalf("gatherCosts must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable posture finding, got %+v", fs)
	}
}

// ---------- Read-only constraint ----------

func TestGovernance_AllGETRequests(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, map[string]string{"costs": "true", "admin": "true"})
	sink := &captureSink{}
	if err := s.gatherOrgGraph(context.Background(), sink); err != nil {
		t.Fatalf("gatherOrgGraph: %v", err)
	}
	if err := s.gatherAuditLogs(context.Background(), sink); err != nil {
		t.Fatalf("gatherAuditLogs: %v", err)
	}
	projects, projectErr := s.fetchNonArchivedProjects(context.Background())
	if projectErr != nil {
		t.Fatalf("fetchNonArchivedProjects: %v", projectErr)
	}
	if err := s.gatherDataRetentionForProjects(context.Background(), sink, projects, nil); err != nil {
		t.Fatalf("gatherDataRetentionForProjects: %v", err)
	}
	if err := s.gatherSpendAlertsForProjects(context.Background(), sink, projects, nil); err != nil {
		t.Fatalf("gatherSpendAlertsForProjects: %v", err)
	}
	if err := s.gatherCosts(context.Background(), sink); err != nil {
		t.Fatalf("gatherCosts: %v", err)
	}
	if err := s.gatherModelPermissionsForProjects(context.Background(), sink, projects, nil); err != nil {
		t.Fatalf("gatherModelPermissionsForProjects: %v", err)
	}
	if err := s.gatherHostedToolPermissionsForProjects(context.Background(), sink, projects, nil); err != nil {
		t.Fatalf("gatherHostedToolPermissionsForProjects: %v", err)
	}
	if err := s.gatherGroups(context.Background(), sink); err != nil {
		t.Fatalf("gatherGroups: %v", err)
	}
	if err := s.gatherRoles(context.Background(), sink); err != nil {
		t.Fatalf("gatherRoles: %v", err)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-openai-admin-test" {
			t.Fatalf("bearer credential not sent on %s", r.URL.Path)
		}
	}
}

// ---------- Descriptor field ----------

func TestDescriptor_CostsField(t *testing.T) {
	d := New().Descriptor()
	var found bool
	for _, f := range d.ConfigFields {
		if f.Key == "costs" {
			found = true
			if f.Type != sdk.FieldBool {
				t.Fatalf("costs field type = %q, want bool", f.Type)
			}
			if f.Default != "false" {
				t.Fatalf("costs default = %q, want false", f.Default)
			}
		}
	}
	if !found {
		t.Fatal("Descriptor missing 'costs' config field")
	}
}
