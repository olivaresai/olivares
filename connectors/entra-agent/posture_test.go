// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

func stubDriftEmpty(d *stubDoer) {
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[]}`)
}

func findingReports(t *testing.T, sink *collectSink) []model.FindingReport {
	t.Helper()
	out := make([]model.FindingReport, 0, len(sink.obs))
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("observation %T, want FindingReport", o)
		}
		out = append(out, f)
	}
	return out
}

func findingKinds(findings []model.FindingReport) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Kind)
	}
	return out
}

func runCAGather(t *testing.T, fixture string, status int) []model.FindingReport {
	t.Helper()
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("live_agents_one.json"))
	if status == http.StatusOK {
		d.on(http.MethodGet, "/beta/identity/conditionalAccess/policies", d.fixture(fixture))
	} else {
		d.onStatus(http.MethodGet, "/beta/identity/conditionalAccess/policies", status, `{"error":{"code":"Authorization_RequestDenied"}}`)
	}

	s := openSource(t, d, map[string]string{
		"risk_posture":       "false",
		"governance_posture": "false",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return findingReports(t, sink)
}

func TestGatherCAPosture(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		status    int
		wantKinds []string
	}{
		{
			name:      "enabled include list and high-risk policy",
			fixture:   "ca_policies_targeted.json",
			status:    http.StatusOK,
			wantKinds: nil,
		},
		{
			name:      "policies exist but none target agents",
			fixture:   "ca_policies_none_target_agents.json",
			status:    http.StatusOK,
			wantKinds: []string{findingAgentCAUnprotected},
		},
		{
			name:      "high-risk levels target agents without include list",
			fixture:   "ca_policies_high_risk_only.json",
			status:    http.StatusOK,
			wantKinds: nil,
		},
		{
			name:      "AllAgentIdUsers targets agent users",
			fixture:   "ca_policies_agent_users.json",
			status:    http.StatusOK,
			wantKinds: []string{findingAgentCANoRiskPolicy},
		},
		{
			name:      "All users does not include agent users",
			fixture:   "ca_policies_all_users_only.json",
			status:    http.StatusOK,
			wantKinds: []string{findingAgentCAUnprotected},
		},
		{
			name:      "disabled agent-targeting policy does not count",
			fixture:   "ca_policies_disabled_target.json",
			status:    http.StatusOK,
			wantKinds: []string{findingAgentCAUnprotected},
		},
		{
			name:      "403 skips CA leg",
			status:    http.StatusForbidden,
			wantKinds: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := runCAGather(t, tc.fixture, tc.status)
			if got := findingKinds(findings); !reflect.DeepEqual(got, tc.wantKinds) {
				t.Fatalf("finding kinds = %v, want %v", got, tc.wantKinds)
			}
			hasUnprotected := false
			hasNoRisk := false
			for _, f := range findings {
				hasUnprotected = hasUnprotected || f.Kind == findingAgentCAUnprotected
				hasNoRisk = hasNoRisk || f.Kind == findingAgentCANoRiskPolicy
				if f.SubjectKind != "tenant" || f.SubjectRef != "tenant-abc" {
					t.Errorf("tenant subject = %s/%s", f.SubjectKind, f.SubjectRef)
				}
			}
			if hasUnprotected && hasNoRisk {
				t.Fatal("CA no-risk finding must not double-emit with unprotected coverage")
			}
		})
	}
}

func TestGatherRiskyAgents(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.on(http.MethodGet, "/beta/identityProtection/riskyAgents", d.fixture("risky_agents.json"))

	s := openSource(t, d, map[string]string{
		"ca_posture":         "false",
		"governance_posture": "false",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	findings := findingReports(t, sink)
	if got := findingKinds(findings); !reflect.DeepEqual(got, []string{findingAgentRisky, findingAgentRisky, findingAgentRisky}) {
		t.Fatalf("finding kinds = %v", got)
	}

	want := []struct {
		ref      string
		severity model.Severity
		title    string
		hash     string
	}{
		{"risk-high", model.SeverityHigh, "agent at risk: High Risk Agent (atRisk/high)", redact.Hash("agent_risky|entra-agent|risk-high|atRisk|high")},
		{"risk-low", model.SeverityLow, "agent at risk: Low Risk Agent (atRisk/low)", redact.Hash("agent_risky|entra-agent|risk-low|atRisk|low")},
		{"risk-compromised", model.SeverityHigh, "agent at risk: Compromised Agent (confirmedCompromised/none)", redact.Hash("agent_risky|entra-agent|risk-compromised|confirmedCompromised|none")},
	}
	for i, w := range want {
		if findings[i].SubjectKind != "identity" || findings[i].SubjectRef != w.ref {
			t.Errorf("finding[%d] subject = %s/%s, want identity/%s", i, findings[i].SubjectKind, findings[i].SubjectRef, w.ref)
		}
		if findings[i].Severity != w.severity {
			t.Errorf("finding[%d] severity = %q, want %q", i, findings[i].Severity, w.severity)
		}
		if findings[i].Title != w.title {
			t.Errorf("finding[%d] title = %q, want %q", i, findings[i].Title, w.title)
		}
		if findings[i].DetailHash != w.hash {
			t.Errorf("finding[%d] DetailHash = %q, want %q", i, findings[i].DetailHash, w.hash)
		}
	}

	riskyCalls := 0
	for _, c := range d.calls {
		if strings.Contains(c.URL, "/beta/identityProtection/riskyAgents") {
			riskyCalls++
			if c.Prefer != "include-unknown-enum-members" {
				t.Errorf("riskyAgents Prefer = %q, want include-unknown-enum-members", c.Prefer)
			}
			continue
		}
		if c.Prefer != "" {
			t.Errorf("non-risk request carried Prefer header: %s %s", c.Method, c.URL)
		}
	}
	if riskyCalls != 1 {
		t.Fatalf("riskyAgents calls = %d, want 1", riskyCalls)
	}
}

func TestGatherGovernanceAccessPackages(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		status    int
		wantKinds []string
	}{
		{
			name:      "no agent access package policy",
			fixture:   "assignment_policies_none.json",
			status:    http.StatusOK,
			wantKinds: []string{findingAgentGovernanceNoAccessPackages},
		},
		{
			name:      "agent access package policy present",
			fixture:   "assignment_policies_agents.json",
			status:    http.StatusOK,
			wantKinds: nil,
		},
		{
			name:      "403 skips governance package leg",
			status:    http.StatusForbidden,
			wantKinds: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStub(t)
			d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
			stubDriftEmpty(d)
			d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("live_agents_one.json"))
			if tc.status == http.StatusOK {
				d.on(http.MethodGet, "/v1.0/identityGovernance/entitlementManagement/assignmentPolicies", d.fixture(tc.fixture))
			} else {
				d.onStatus(http.MethodGet, "/v1.0/identityGovernance/entitlementManagement/assignmentPolicies", tc.status, `{"error":{"code":"Authorization_RequestDenied"}}`)
			}
			d.on(http.MethodGet, "/servicePrincipals/agent-1/microsoft.graph.agentIdentity/owners", d.fixture("owners_user.json"))
			d.on(http.MethodGet, "/servicePrincipals/agent-1/microsoft.graph.agentIdentity/sponsors", d.fixture("sponsors_empty.json"))

			s := openSource(t, d, map[string]string{
				"ca_posture":   "false",
				"risk_posture": "false",
			})
			sink := &collectSink{}
			if err := s.Gather(context.Background(), sink); err != nil {
				t.Fatalf("Gather: %v", err)
			}
			if got := findingKinds(findingReports(t, sink)); !reflect.DeepEqual(got, tc.wantKinds) {
				t.Fatalf("finding kinds = %v, want %v", got, tc.wantKinds)
			}
		})
	}
}

func TestGatherSponsorlessAgents(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("live_agents_sponsorless.json"))
	d.on(http.MethodGet, "/v1.0/identityGovernance/entitlementManagement/assignmentPolicies", d.fixture("assignment_policies_agents.json"))
	d.on(http.MethodGet, "/servicePrincipals/agent-with-owner/microsoft.graph.agentIdentity/owners", d.fixture("owners_user.json"))
	d.on(http.MethodGet, "/servicePrincipals/agent-with-owner/microsoft.graph.agentIdentity/sponsors", d.fixture("sponsors_sp_only.json"))
	d.on(http.MethodGet, "/servicePrincipals/agent-sponsorless/microsoft.graph.agentIdentity/owners", d.fixture("owners_sp_only.json"))
	d.on(http.MethodGet, "/servicePrincipals/agent-sponsorless/microsoft.graph.agentIdentity/sponsors", d.fixture("sponsors_sp_only.json"))
	d.on(http.MethodGet, "/servicePrincipals/agent-denied/microsoft.graph.agentIdentity/owners", d.fixture("owners_sp_only.json"))
	d.onStatus(http.MethodGet, "/servicePrincipals/agent-denied/microsoft.graph.agentIdentity/sponsors", http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied"}}`)

	s := openSource(t, d, map[string]string{
		"ca_posture":   "false",
		"risk_posture": "false",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	findings := findingReports(t, sink)
	if got := findingKinds(findings); !reflect.DeepEqual(got, []string{findingAgentNoSponsor}) {
		t.Fatalf("finding kinds = %v, want [%s]", got, findingAgentNoSponsor)
	}
	f := findings[0]
	if f.SubjectKind != "identity" || f.SubjectRef != "agent-sponsorless" {
		t.Errorf("subject = %s/%s, want identity/agent-sponsorless", f.SubjectKind, f.SubjectRef)
	}
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %q, want medium", f.Severity)
	}
	if f.Title != "agent identity has no user owner or sponsor: Sponsorless Agent" {
		t.Errorf("title = %q", f.Title)
	}
	if f.DetailHash != redact.Hash("agent_no_sponsor|entra-agent|agent-sponsorless") {
		t.Errorf("DetailHash = %q", f.DetailHash)
	}
}

func TestGatherZeroAgentsGate(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	stubDriftEmpty(d)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("live_agents_empty.json"))
	d.on(http.MethodGet, "/beta/identityProtection/riskyAgents", `{"value":[]}`)

	s := openSource(t, d, nil)
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got := findingReports(t, sink); len(got) != 0 {
		t.Fatalf("findings = %+v, want none", got)
	}
	if !sawPath(d, "/beta/identityProtection/riskyAgents") {
		t.Error("riskyAgents leg must run even when the live agent inventory is empty")
	}
	if sawPath(d, "/beta/identity/conditionalAccess/policies") {
		t.Error("CA leg must be skipped when the live agent inventory is empty")
	}
	if sawPath(d, "/v1.0/identityGovernance/entitlementManagement/assignmentPolicies") {
		t.Error("governance leg must be skipped when the live agent inventory is empty")
	}
}

func TestGatherPostureDeterminism(t *testing.T) {
	run := func(t *testing.T) []model.FindingReport {
		t.Helper()
		d := newStub(t)
		d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
		stubDriftEmpty(d)
		d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("live_agents_sponsorless_single.json"))
		d.on(http.MethodGet, "/beta/identity/conditionalAccess/policies", d.fixture("ca_policies_disabled_target.json"))
		d.on(http.MethodGet, "/beta/identityProtection/riskyAgents", d.fixture("risky_agents.json"))
		d.on(http.MethodGet, "/v1.0/identityGovernance/entitlementManagement/assignmentPolicies", d.fixture("assignment_policies_none.json"))
		d.on(http.MethodGet, "/servicePrincipals/agent-sponsorless/microsoft.graph.agentIdentity/owners", d.fixture("owners_sp_only.json"))
		d.on(http.MethodGet, "/servicePrincipals/agent-sponsorless/microsoft.graph.agentIdentity/sponsors", d.fixture("sponsors_sp_only.json"))

		s := openSource(t, d, nil)
		sink := &collectSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		return findingReports(t, sink)
	}

	first := run(t)
	second := run(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Gather findings are not deterministic:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	wantKinds := []string{
		findingAgentCAUnprotected,
		findingAgentRisky,
		findingAgentRisky,
		findingAgentRisky,
		findingAgentGovernanceNoAccessPackages,
		findingAgentNoSponsor,
	}
	if got := findingKinds(first); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("deterministic kind order = %v, want %v", got, wantKinds)
	}
}
