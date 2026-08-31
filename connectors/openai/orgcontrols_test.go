// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Fixtures pinned 2026-07-04 against developers.openai.com/api/docs/api-reference/administration:
// spend_alerts_org.json, spend_alerts_proj_default.json, spend_alerts_empty.json,
// model_permissions_proj_default.json, hosted_tool_permissions_proj_default.json,
// groups.json, roles.json, and projects.json.

package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func orgControlFindings(t *testing.T, obs []model.Observation, subjectKind string) []model.FindingReport {
	t.Helper()
	var out []model.FindingReport
	for _, f := range govFindings(obs) {
		if f.SubjectKind == subjectKind {
			out = append(out, f)
		}
	}
	return out
}

// TestGatherAgentKitPosture_HonestConsoleOnly proves the E4 honest-degradation
// finding: AgentKit Connector Registry / Global Admin Console are declared Console-only
// (no API) rather than fabricating a poll, and the finding points at the API-observable
// lever (hosted_tool_permissions). It needs no HTTP — it is a structural statement.
func TestGatherAgentKitPosture_HonestConsoleOnly(t *testing.T) {
	s := newGovSource(t, &govDoer{t: t}, map[string]string{})
	sink := &captureSink{}
	if err := s.gatherAgentKitPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherAgentKitPosture: %v", err)
	}
	fs := orgControlFindings(t, sink.obs, subjectAgentKitGovernance)
	if len(fs) != 1 {
		t.Fatalf("emitted %d agentkit posture findings, want 1", len(fs))
	}
	f := fs[0]
	if f.Severity != model.SeverityInfo || f.Kind != "posture" {
		t.Errorf("finding kind/severity = %q/%v", f.Kind, f.Severity)
	}
	if !strings.Contains(f.Title, "Console-only") || !strings.Contains(f.Title, "hosted_tool_permissions") {
		t.Errorf("title is not the honest console-only statement: %q", f.Title)
	}
}

func TestGatherSpendAlerts_OrgAndProjectFixtures(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, map[string]string{"costs": "true"})
	sink := &captureSink{}
	if err := s.gatherSpendAlerts(context.Background(), sink); err != nil {
		t.Fatalf("gatherSpendAlerts: %v", err)
	}
	fs := orgControlFindings(t, sink.obs, subjectSpendAlert)
	if len(fs) != 3 {
		t.Fatalf("emitted %d spend-alert findings, want 3", len(fs))
	}
	var orgInventory, projectInventory, summary bool
	for _, f := range fs {
		if strings.Contains(f.Title, "@") {
			t.Fatalf("spend alert title leaked recipient: %q", f.Title)
		}
		if f.Kind == "inventory" && f.SubjectRef == "organization/alert_org_001" {
			orgInventory = true
			if !strings.Contains(f.Title, "threshold $1000.00/month") || !strings.Contains(f.Title, "org-level") {
				t.Fatalf("org alert title = %q", f.Title)
			}
			if len(f.DetailHash) != 64 {
				t.Fatalf("org alert detail hash = %q", f.DetailHash)
			}
		}
		if f.Kind == "inventory" && f.SubjectRef == "proj_default/alert_proj_001" {
			projectInventory = true
			if !strings.Contains(f.Title, "threshold $250.00/month") || !strings.Contains(f.Title, "project Default") {
				t.Fatalf("project alert title = %q", f.Title)
			}
		}
		if f.Kind == "posture" {
			summary = true
			if f.Severity != model.SeverityInfo || !strings.Contains(f.Title, "1 org-level, 1 project-level") {
				t.Fatalf("spend alert summary = %+v", f)
			}
		}
	}
	if !orgInventory || !projectInventory || !summary {
		t.Fatalf("missing spend-alert findings: org=%v project=%v summary=%v", orgInventory, projectInventory, summary)
	}
}

func TestGatherSpendAlerts_ZeroAlertsLowPosture(t *testing.T) {
	doer := &govDoer{t: t, fixtures: map[string]string{
		"/v1/organization/spend_alerts":                       "spend_alerts_empty.json",
		"/v1/organization/projects/proj_default/spend_alerts": "spend_alerts_empty.json",
	}}
	s := newGovSource(t, doer, map[string]string{"costs": "true"})
	sink := &captureSink{}
	if err := s.gatherSpendAlerts(context.Background(), sink); err != nil {
		t.Fatalf("gatherSpendAlerts: %v", err)
	}
	fs := orgControlFindings(t, sink.obs, subjectSpendAlert)
	if len(fs) != 1 {
		t.Fatalf("emitted %d spend-alert findings, want summary only", len(fs))
	}
	if fs[0].Kind != "posture" || fs[0].Severity != model.SeverityLow ||
		!strings.Contains(fs[0].Title, "No OpenAI spend alerts are configured") {
		t.Fatalf("zero-alert summary = %+v", fs[0])
	}
}

func TestGatherModelPermissions_ConfiguredProject(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, map[string]string{"admin": "true"})
	sink := &captureSink{}
	if err := s.gatherModelPermissions(context.Background(), sink); err != nil {
		t.Fatalf("gatherModelPermissions: %v", err)
	}
	fs := orgControlFindings(t, sink.obs, subjectModelPermissions)
	if len(fs) != 2 {
		t.Fatalf("emitted %d model-permission findings, want inventory + summary", len(fs))
	}
	if fs[0].Kind != "inventory" || fs[0].SubjectRef != "proj_default" ||
		!strings.Contains(fs[0].Title, "allow_list") || !strings.Contains(fs[0].Title, "2 model(s)") {
		t.Fatalf("model-permission inventory = %+v", fs[0])
	}
	if fs[1].Kind != "posture" || fs[1].Severity != model.SeverityInfo ||
		!strings.Contains(fs[1].Title, "1 of 1 non-archived projects") {
		t.Fatalf("model-permission summary = %+v", fs[1])
	}
}

func TestGatherModelPermissions_404IsNotConfigured(t *testing.T) {
	path := "/v1/organization/projects/proj_default/model_permissions"
	doer := &govDoer{t: t, statuses: map[string]int{path: 404}}
	s := newGovSource(t, doer, map[string]string{"admin": "true"})
	sink := &captureSink{}
	if err := s.gatherModelPermissions(context.Background(), sink); err != nil {
		t.Fatalf("gatherModelPermissions: %v", err)
	}
	fs := govFindings(sink.obs)
	for _, f := range fs {
		if f.SubjectKind == subjectSurface {
			t.Fatalf("404 must not emit unavailable finding: %+v", f)
		}
	}
	mp := orgControlFindings(t, sink.obs, subjectModelPermissions)
	if len(mp) != 1 || mp[0].Kind != "posture" || mp[0].Severity != model.SeverityLow ||
		!strings.Contains(mp[0].Title, "0 of 1 non-archived projects") {
		t.Fatalf("404 not-configured summary = %+v", mp)
	}
}

func TestGatherModelPermissions_403UnavailableOnce(t *testing.T) {
	path := "/v1/organization/projects/proj_default/model_permissions"
	doer := &govDoer{t: t, statuses: map[string]int{path: 403}}
	s := newGovSource(t, doer, map[string]string{"admin": "true"})
	sink := &captureSink{}
	if err := s.gatherModelPermissions(context.Background(), sink); err != nil {
		t.Fatalf("gatherModelPermissions: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface || fs[0].SubjectRef != "model_permissions" {
		t.Fatalf("want one model-permissions unavailable finding, got %+v", fs)
	}
}

func TestGatherHostedToolPermissions_MCPEnabledSummary(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, map[string]string{"admin": "true"})
	sink := &captureSink{}
	if err := s.gatherHostedToolPermissions(context.Background(), sink); err != nil {
		t.Fatalf("gatherHostedToolPermissions: %v", err)
	}
	fs := orgControlFindings(t, sink.obs, subjectHostedToolPermission)
	if len(fs) != 2 {
		t.Fatalf("emitted %d hosted-tool findings, want inventory + summary", len(fs))
	}
	if fs[0].Kind != "inventory" || !strings.Contains(fs[0].Title, "mcp") ||
		!strings.Contains(fs[0].Title, "code_interpreter") {
		t.Fatalf("hosted-tool inventory = %+v", fs[0])
	}
	if fs[1].Kind != "posture" || !strings.Contains(fs[1].Title, "1 with MCP enabled") {
		t.Fatalf("hosted-tool summary = %+v", fs[1])
	}
}

func TestGatherGroupsAndRoles_SummariesAndCustomRoleInventory(t *testing.T) {
	doer := &govDoer{t: t}
	s := newGovSource(t, doer, map[string]string{"admin": "true"})
	sink := &captureSink{}
	if err := s.gatherGroups(context.Background(), sink); err != nil {
		t.Fatalf("gatherGroups: %v", err)
	}
	if err := s.gatherRoles(context.Background(), sink); err != nil {
		t.Fatalf("gatherRoles: %v", err)
	}
	groups := orgControlFindings(t, sink.obs, subjectGroups)
	if len(groups) != 1 || !strings.Contains(groups[0].Title, "2 total") ||
		!strings.Contains(groups[0].Title, "1 SCIM-managed") ||
		!strings.Contains(groups[0].Title, "1 tenant_group") {
		t.Fatalf("group summary = %+v", groups)
	}
	roles := orgControlFindings(t, sink.obs, subjectRole)
	if len(roles) != 2 {
		t.Fatalf("emitted %d role findings, want summary + one custom role", len(roles))
	}
	if roles[0].Kind != "posture" || !strings.Contains(roles[0].Title, "1 predefined, 1 custom") {
		t.Fatalf("role summary = %+v", roles[0])
	}
	if roles[1].Kind != "inventory" || roles[1].SubjectRef != "role_finops_auditor" ||
		!strings.Contains(roles[1].Title, "FinOps Auditor") ||
		!strings.Contains(roles[1].Title, "api.project") ||
		!strings.Contains(roles[1].Title, "3 permission(s)") {
		t.Fatalf("custom role inventory = %+v", roles[1])
	}
}
