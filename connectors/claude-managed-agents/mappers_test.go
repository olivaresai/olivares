// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestVaultLateralFinding(t *testing.T) {
	f := vaultLateralFinding(Vault{ID: "vlt_1", WorkspaceID: "ws_1"}, testTime)
	if f.Kind != findingPosture || f.Severity != model.SeverityMedium || f.SubjectKind != kindVault {
		t.Fatalf("unexpected lateral finding %+v", f)
	}
	if len(f.OWASPASI) != 1 || f.OWASPASI[0] != asiIdentityAbuse {
		t.Errorf("lateral finding should carry ASI03, got %v", f.OWASPASI)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a hashed detail, never raw")
	}
}

func TestCredentialFindings(t *testing.T) {
	// static_bearer → a single posture finding.
	sb := VaultCredential{ID: "vcrd_sb"}
	sb.Auth.Type = "static_bearer"
	sb.Auth.MCPServerURL = "https://mcp.example/api"
	got := credentialFindings("vlt_1", sb, testTime, testTime)
	if len(got) != 1 || got[0].Severity != model.SeverityLow || got[0].SubjectKind != kindVaultCred {
		t.Fatalf("static_bearer findings = %+v", got)
	}

	// mcp_oauth expired → Medium governance.
	exp := VaultCredential{ID: "vcrd_x"}
	exp.Auth.Type = "mcp_oauth"
	exp.Auth.ExpiresAt = testTime.Add(-time.Hour).Format(time.RFC3339)
	got = credentialFindings("vlt_1", exp, testTime, testTime)
	if len(got) != 1 || got[0].Severity != model.SeverityMedium {
		t.Fatalf("expired mcp_oauth = %+v, want one Medium finding", got)
	}

	// mcp_oauth expiring within 24h → Low.
	soon := VaultCredential{ID: "vcrd_s"}
	soon.Auth.Type = "mcp_oauth"
	soon.Auth.ExpiresAt = testTime.Add(time.Hour).Format(time.RFC3339)
	got = credentialFindings("vlt_1", soon, testTime, testTime)
	if len(got) != 1 || got[0].Severity != model.SeverityLow {
		t.Fatalf("expiring-soon mcp_oauth = %+v, want one Low finding", got)
	}

	// mcp_oauth comfortably valid → no finding.
	ok := VaultCredential{ID: "vcrd_ok"}
	ok.Auth.Type = "mcp_oauth"
	ok.Auth.ExpiresAt = testTime.Add(72 * time.Hour).Format(time.RFC3339)
	if got = credentialFindings("vlt_1", ok, testTime, testTime); len(got) != 0 {
		t.Errorf("a valid mcp_oauth credential should produce no finding, got %+v", got)
	}
}

func TestCredentialEdge(t *testing.T) {
	cred := VaultCredential{ID: "vcrd_1"}
	cred.Auth.MCPServerURL = "https://user:secret@mcp.example/api?token=abc"
	e, ok := credentialEdge("vlt_1", cred, testTime)
	if !ok {
		t.Fatal("a credential with a server URL should produce an edge")
	}
	// The grant is the PERMITTED side: SignalPolicy, with the credential as the
	// identity origin (so the access map routes it) and the vault as provenance.
	if e.ResourceKind != mcpServerResourceKind || e.OriginKind != "identity" || e.OriginRef != "vcrd_1" {
		t.Errorf("unexpected edge %+v", e)
	}
	if e.Source != model.SignalPolicy || e.Mode != model.ModeReadWrite || e.ToolRef != "vlt_1" {
		t.Errorf("credential grant must be SignalPolicy rw with the vault as ToolRef, got %+v", e)
	}
	// The server ref must be sanitized: no userinfo, no query token.
	if strings.Contains(e.ResourceRef, "secret") || strings.Contains(e.ResourceRef, "token") {
		t.Errorf("credential edge leaked secret material in ResourceRef: %q", e.ResourceRef)
	}
	if _, ok := credentialEdge("vlt_1", VaultCredential{ID: "vcrd_2"}, testTime); ok {
		t.Error("a credential with no server URL should produce no edge")
	}
}

func TestCredentialInventoryEdge(t *testing.T) {
	e, ok := credentialInventoryEdge("vlt_1", VaultCredential{ID: "vcrd_1"}, testTime)
	if !ok || e.OriginKind != kindVault || e.ResourceKind != kindVaultCred || e.ResourceRef != "vcrd_1" {
		t.Fatalf("credential inventory edge = %+v ok=%v", e, ok)
	}
	if e.Source != model.SignalCMA {
		t.Errorf("the inventory carrier rides SignalCMA, got %v", e.Source)
	}
	if _, ok := credentialInventoryEdge("vlt_1", VaultCredential{}, testTime); ok {
		t.Error("a credential with no id should produce no inventory edge")
	}
}

func TestMemoryVersionFinding(t *testing.T) {
	at := testTime.Add(-time.Hour)
	atStr := at.Format(time.RFC3339)

	red := MemoryVersion{ID: "memver_r", MemoryID: "m1", Operation: "update", RedactedAt: atStr, CreatedAt: atStr}
	f, ok := memoryVersionFinding("memstore_1", red, testTime)
	if !ok || f.Kind != findingGovernance || f.SubjectKind != kindMemoryVersion {
		t.Fatalf("redacted version finding = %+v ok=%v", f, ok)
	}
	if !f.OccurredAt.Equal(at) {
		t.Errorf("redaction finding ObservedAt must be the version created_at (%v), got %v", at, f.OccurredAt)
	}

	del := MemoryVersion{ID: "memver_d", Operation: "delete", CreatedAt: atStr}
	if f, ok = memoryVersionFinding("memstore_1", del, testTime); !ok || f.Title == "" {
		t.Fatalf("delete version should produce a finding, got %+v ok=%v", f, ok)
	}

	create := MemoryVersion{ID: "memver_c", Operation: "create", CreatedAt: atStr}
	if _, ok = memoryVersionFinding("memstore_1", create, testTime); ok {
		t.Error("a normal create version is not erasure-relevant and should produce no finding")
	}
}

func TestOutcomeFinding(t *testing.T) {
	done := testTime.Add(-time.Hour)
	doneStr := done.Format(time.RFC3339)

	failed, ok := outcomeFinding("sesn_1", OutcomeEvaluation{OutcomeID: "outc_1", Result: "failed", CompletedAt: doneStr}, testTime)
	if !ok || failed.Severity != model.SeverityMedium || failed.SubjectKind != kindOutcome {
		t.Errorf("failed outcome should be Medium/outcome, got %+v ok=%v", failed, ok)
	}
	if !failed.OccurredAt.Equal(done) {
		t.Errorf("verdict OccurredAt must be the evaluation completed_at (%v) for stable de-dup, got %v", done, failed.OccurredAt)
	}
	sat, ok := outcomeFinding("sesn_1", OutcomeEvaluation{OutcomeID: "outc_1", Result: "satisfied", CompletedAt: doneStr}, testTime)
	if !ok || sat.Severity != model.SeverityInfo {
		t.Errorf("satisfied outcome should be Info, got %+v ok=%v", sat, ok)
	}
	budget, ok := outcomeFinding("sesn_1", OutcomeEvaluation{OutcomeID: "outc_1", Result: "max_iterations_reached", CompletedAt: doneStr}, testTime)
	if !ok || budget.Severity != model.SeverityLow {
		t.Errorf("max_iterations_reached should be Low, got %+v ok=%v", budget, ok)
	}

	// Non-terminal states (live result enum: pending|running|evaluating, or no
	// completed_at) must emit nothing — a verdict records a decision, not progress.
	for _, ev := range []OutcomeEvaluation{
		{OutcomeID: "outc_2", Result: "running"},
		{OutcomeID: "outc_2", Result: "evaluating", CompletedAt: ""},
		{OutcomeID: "outc_2", Result: "pending", CompletedAt: doneStr},
	} {
		if _, ok := outcomeFinding("sesn_1", ev, testTime); ok {
			t.Errorf("non-terminal evaluation %+v should produce no finding", ev)
		}
	}
}

func TestWorkQueueFinding(t *testing.T) {
	// backlog with no workers → High.
	f, ok := workQueueFinding("env_1", WorkQueueStats{Depth: 3, WorkersPolling: 0}, 1, testTime)
	if !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("backlog+no-workers = %+v ok=%v, want High", f, ok)
	}
	// backlog with workers → Low.
	f, ok = workQueueFinding("env_1", WorkQueueStats{Depth: 3, WorkersPolling: 2}, 1, testTime)
	if !ok || f.Severity != model.SeverityLow {
		t.Fatalf("backlog+workers = %+v ok=%v, want Low", f, ok)
	}
	// below threshold → no finding.
	if _, ok = workQueueFinding("env_1", WorkQueueStats{Depth: 0, WorkersPolling: 0}, 1, testTime); ok {
		t.Error("an empty queue should produce no backlog finding")
	}
}

func TestWorkItemEdge(t *testing.T) {
	item := WorkItem{ID: "work_1", EnvironmentID: "env_1"}
	item.Data.ID = "sesn_1"
	e, ok := workItemEdge(item, testTime)
	if !ok || e.OriginKind != originEnvironment || e.ResourceKind != kindManagedAgent || e.ToolRef != "work_1" {
		t.Fatalf("work item edge = %+v ok=%v", e, ok)
	}
	if _, ok = workItemEdge(WorkItem{ID: "work_2", EnvironmentID: "env_1"}, testTime); ok {
		t.Error("a work item with no session id should produce no edge")
	}
}

func TestSkillFinding(t *testing.T) {
	latest := SkillRef{Type: skillTypeCustom, SkillID: "skill_1", Version: "latest"}
	f, ok := skillFinding("agent_1", latest, testTime)
	if !ok || f.SubjectKind != kindSkill || len(f.OWASPASI) == 0 || f.OWASPASI[0] != asiSupplyChain {
		t.Fatalf("unpinned custom skill = %+v ok=%v", f, ok)
	}
	unset := SkillRef{Type: skillTypeCustom, SkillID: "skill_2"}
	if _, ok = skillFinding("agent_1", unset, testTime); !ok {
		t.Error("a custom skill with no version is unpinned and should produce a finding")
	}
	pinned := SkillRef{Type: skillTypeCustom, SkillID: "skill_3", Version: "3"}
	if _, ok = skillFinding("agent_1", pinned, testTime); ok {
		t.Error("a pinned custom skill is governed and should produce no finding")
	}
	prebuilt := SkillRef{Type: skillTypeAnthropic, SkillID: "xlsx"}
	if _, ok = skillFinding("agent_1", prebuilt, testTime); ok {
		t.Error("a pre-built anthropic skill should produce no finding")
	}
}

func TestToolConfirmationFinding(t *testing.T) {
	f := toolConfirmationFinding("sesn_1", 2, testTime)
	if f.SubjectKind != kindManagedAgent || f.Kind != findingGovernance || f.Severity != model.SeverityLow {
		t.Fatalf("awaiting-confirmation finding = %+v", f)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a hashed detail, never raw")
	}
	e := permissionPolicyEdge("sesn_1", testTime)
	if e.ResourceKind != kindPermPolicy || e.ResourceRef != policyAlwaysAsk || e.OriginRef != "sesn_1" {
		t.Fatalf("permission policy edge = %+v", e)
	}
	if e.Source != model.SignalCMA {
		t.Errorf("the gate OBSERVATION rides SignalCMA (the declared policy is agentToolEdges), got %v", e.Source)
	}
}

func TestAgentToolEdges(t *testing.T) {
	off := false
	a := Agent{
		ID: "agent_1",
		Tools: []AgentTool{
			{
				Type:          toolsetBuiltin,
				DefaultConfig: ToolConfig{PermissionPolicy: PermissionPolicy{Type: policyAlwaysAllow}},
				Configs: []NamedToolConfig{
					{Name: "bash", Enabled: &off},
					{Name: "write", PermissionPolicy: PermissionPolicy{Type: policyAlwaysAsk}},
				},
			},
			{
				Type:          toolsetMCP,
				MCPServerName: "github",
				DefaultConfig: ToolConfig{PermissionPolicy: PermissionPolicy{Type: policyAlwaysAsk}},
				Configs: []NamedToolConfig{
					{Name: "create_issue"},
					{Name: "delete_repo", Enabled: &off},
				},
			},
			{Type: toolsetCustom, Name: "lookup_invoice"},
		},
	}
	edges := agentToolEdges(a, testTime)
	for _, e := range edges {
		if e.Source != model.SignalPolicy || e.OriginKind != originAgent || e.OriginRef != "agent_1" {
			t.Fatalf("every declared-tool edge is a PERMITTED agent grant, got %+v", e)
		}
	}
	byRef := map[string]string{} // kind+ref → tool
	for _, e := range edges {
		byRef[e.ResourceKind+"|"+e.ResourceRef] = e.ToolRef
	}
	// Builtins: the verbatim 8-name enum minus the disabled bash (7 edges).
	for _, name := range []string{"edit", "read", "write", "glob", "grep", "web_fetch", "web_search"} {
		if _, ok := byRef[kindAgentTool+"|"+name]; !ok {
			t.Errorf("missing builtin permitted edge for %q", name)
		}
	}
	if _, ok := byRef[kindAgentTool+"|bash"]; ok {
		t.Error("a disabled builtin must emit no permitted edge")
	}
	// MCP: server-level edge + the explicitly configured enabled tool, byte-matching
	// the observed mcp.tool "server/tool" shape; the disabled tool emits nothing.
	if _, ok := byRef[mcpServerResourceKind+"|github"]; !ok {
		t.Error("missing mcp server-level permitted edge")
	}
	if tool, ok := byRef["mcp.tool|github/create_issue"]; !ok || tool != "create_issue" {
		t.Errorf("missing/wrong mcp per-tool permitted edge: %q ok=%v", tool, ok)
	}
	if _, ok := byRef["mcp.tool|github/delete_repo"]; ok {
		t.Error("a disabled mcp tool must emit no permitted edge")
	}
	// Custom tool.
	if _, ok := byRef[kindAgentTool+"|lookup_invoice"]; !ok {
		t.Error("missing custom-tool permitted edge")
	}
	if got := len(edges); got != 10 {
		t.Errorf("expected 10 permitted edges (7 builtins + server + 1 mcp tool + custom), got %d: %+v", got, edges)
	}
}

func TestRosterEdges(t *testing.T) {
	a := Agent{
		ID: "agent_coord",
		Multiagent: &MultiagentRoster{Type: "coordinator", Agents: []RosterEntry{
			{Type: "agent", ID: "agent_sub1"},
			{Type: "agent", ID: "agent_sub2", Version: 3},
			{Type: "self"},
		}},
	}
	edges := rosterEdges(a, testTime)
	if len(edges) != 2 {
		t.Fatalf("expected 2 roster edges (self emits none), got %d: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.Source != model.SignalPolicy || e.ResourceKind != kindAgentDef || e.OriginRef != "agent_coord" {
			t.Errorf("roster edge must be a PERMITTED agent→agent-definition grant, got %+v", e)
		}
	}
	if rosterEdges(Agent{ID: "agent_solo"}, testTime) != nil {
		t.Error("an agent without a roster emits no roster edges")
	}
}

func TestThreadEdge(t *testing.T) {
	created := testTime.Add(-time.Hour)
	sub := Thread{ID: "sthr_2", SessionID: "sesn_1", ParentThreadID: "sthr_1", CreatedAt: created.Format(time.RFC3339)}
	sub.Agent.ID = "agent_sub1"
	e, ok := threadEdge(sub, testTime)
	if !ok || e.OriginRef != "sesn_1" || e.ResourceKind != kindManagedAgent || e.ResourceRef != "sthr_2" || e.ToolRef != "agent_sub1" {
		t.Fatalf("thread edge = %+v ok=%v", e, ok)
	}
	if !e.ObservedAt.Equal(created) {
		t.Errorf("thread edge ObservedAt must be the thread created_at (%v), got %v", created, e.ObservedAt)
	}
	primary := Thread{ID: "sthr_1", SessionID: "sesn_1"}
	if _, ok := threadEdge(primary, testTime); ok {
		t.Error("the primary thread (no parent) is the session itself — no edge")
	}
}

func TestSessionObservations(t *testing.T) {
	created := testTime.Add(-2 * time.Hour)
	s := Session{
		ID:        "sesn_1",
		Status:    "running",
		CreatedAt: created.Format(time.RFC3339),
		Resources: []SessionResource{
			{Type: "memory_store", MemoryStoreID: "memstore_rw", Access: accessReadWrite},
			{Type: "memory_store", MemoryStoreID: "memstore_ro", Access: accessReadOnly},
			{Type: "file"},
		},
		VaultIDs: []string{"vlt_1"},
		Outcomes: []OutcomeEvaluation{
			{OutcomeID: "outc_1", Result: "satisfied", CompletedAt: created.Format(time.RFC3339)},
			{OutcomeID: "outc_2", Result: "running"},
		},
	}
	obs := sessionObservations(s, testTime)

	var edges []model.EdgeObservation
	var findings []model.FindingReport
	for _, o := range obs {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges = append(edges, v)
		case model.FindingReport:
			findings = append(findings, v)
		}
	}
	// Mount edges: rw store → ModeReadWrite, ro store → ModeRead, vault → ModeRead.
	modeOf := map[string]model.AccessMode{}
	for _, e := range edges {
		modeOf[e.ResourceKind+"|"+e.ResourceRef] = e.Mode
		if !e.ObservedAt.Equal(created) {
			t.Errorf("session edges must carry the session created_at for stable de-dup, got %v", e.ObservedAt)
		}
	}
	if modeOf[kindMemoryStore+"|memstore_rw"] != model.ModeReadWrite {
		t.Errorf("read_write mount must map to ModeReadWrite, got %v", modeOf[kindMemoryStore+"|memstore_rw"])
	}
	if modeOf[kindMemoryStore+"|memstore_ro"] != model.ModeRead {
		t.Errorf("read_only mount must map to ModeRead, got %v", modeOf[kindMemoryStore+"|memstore_ro"])
	}
	if modeOf[kindVault+"|vlt_1"] != model.ModeRead {
		t.Errorf("vault use must map to a session→vault read edge, got %v", modeOf[kindVault+"|vlt_1"])
	}
	// Findings: ONE read_write poisoning posture (ASI06) + ONE terminal outcome verdict.
	var rwPosture, outcome int
	for _, f := range findings {
		switch {
		case f.Kind == findingPosture && f.SubjectRef == "memstore_rw":
			rwPosture++
			if len(f.OWASPASI) != 1 || f.OWASPASI[0] != asiMemoryPoison {
				t.Errorf("read_write mount posture must carry ASI06, got %v", f.OWASPASI)
			}
		case f.Kind == findingGovernance && f.SubjectKind == kindOutcome:
			outcome++
		}
	}
	if rwPosture != 1 || outcome != 1 {
		t.Errorf("expected 1 rw-mount posture + 1 terminal outcome verdict, got %d/%d (%+v)", rwPosture, outcome, findings)
	}
}
