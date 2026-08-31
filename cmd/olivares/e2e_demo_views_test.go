// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// TestDemoViewsWireProof boots the real composition root and proves the demo
// store seed reaches the same authenticated HTTP reads used by the knowledge,
// workspace, evals and disaster-recovery console views.
func TestDemoViewsWireProof(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true,
	})
	if err != nil {
		t.Fatalf("boot demo estate: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.demoTenant.IsZero() {
		t.Fatal("boot did not return the demo tenant")
	}

	if _, err := eng.authr.BootstrapSuperadmin(ctx, demoEmail, demoPassword); err != nil {
		t.Fatalf("bootstrap demo superadmin: %v", err)
	}
	handler := eng.api.Handler()
	code, login, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": demoEmail, "password": demoPassword,
	})
	if code != http.StatusOK {
		t.Fatalf("demo login = %d: %s", code, raw)
	}
	token, ok := login["token"].(string)
	if !ok || token == "" {
		t.Fatalf("demo login returned no token: %s", raw)
	}
	tenant := eng.demoTenant.String()

	// Knowledge: the named KB is listed and its document reader resolves both
	// seeded documents through the production module route.
	code, kbs, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/m/knowledge/kbs", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo knowledge bases = %d: %s", code, raw)
	}
	kb := findDemoViewItem(t, demoViewItems(t, kbs), "name", seed.KnowledgeBaseName)
	kbID, _ := kb["id"].(string)
	if kbID == "" {
		t.Fatalf("demo knowledge base has no id: %v", kb)
	}
	code, documents, raw := doDemoViewJSON(t, handler, http.MethodGet,
		"/v1/m/knowledge/kbs/"+kbID+"/documents", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo knowledge documents = %d: %s", code, raw)
	}
	documentItems := demoViewItems(t, documents)
	if len(documentItems) < 2 {
		t.Fatalf("demo knowledge documents = %d, want at least 2", len(documentItems))
	}
	findDemoViewItem(t, documentItems, "title", seed.KnowledgeBillingDocumentTitle)
	findDemoViewItem(t, documentItems, "title", seed.KnowledgeIncidentDocumentTitle)
	code, prompts, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/m/knowledge/prompts", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo knowledge prompts = %d: %s", code, raw)
	}
	findDemoViewItem(t, demoViewItems(t, prompts), "name", seed.KnowledgeReviewerPromptName)
	code, memories, raw := doDemoViewJSON(t, handler, http.MethodGet,
		"/v1/m/knowledge/memory?agent_ref="+seed.AgentReviewer, token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo knowledge memory = %d: %s", code, raw)
	}
	findDemoViewItem(t, demoViewItems(t, memories), "key", seed.KnowledgeReviewerMemoryKey)
	code, lineage, raw := doDemoViewJSON(t, handler, http.MethodGet,
		"/v1/m/knowledge/lineage?kb_id="+kbID, token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo knowledge lineage = %d: %s", code, raw)
	}
	if lineageItems := demoViewItems(t, lineage); len(lineageItems) < 2 {
		t.Fatalf("demo knowledge lineage = %d, want at least 2", len(lineageItems))
	}

	// Workspace: Billing is named, owns the reviewer, and exposes its group.
	code, workspaces, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/workspaces", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo workspaces = %d: %s", code, raw)
	}
	billing := findDemoViewItem(t, demoViewItems(t, workspaces), "name", seed.WorkspaceBillingName)
	billingID, _ := billing["id"].(string)
	if billingID == "" {
		t.Fatalf("Billing workspace has no id: %v", billing)
	}
	code, summary, raw := doDemoViewJSON(t, handler, http.MethodGet,
		"/v1/workspaces/"+billingID+"/summary", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("get Billing workspace summary = %d: %s", code, raw)
	}
	if count, _ := summary["agent_count"].(float64); count < 1 {
		t.Fatalf("Billing agent_count = %v, want at least 1", summary["agent_count"])
	}
	code, groups, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/agent-groups", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo agent groups = %d: %s", code, raw)
	}
	reviewerGroup := findDemoViewItem(t, demoViewItems(t, groups), "name", seed.AgentGroupReviewersName)
	reviewerGroupID, _ := reviewerGroup["id"].(string)
	code, groupMembers, raw := doDemoViewJSON(t, handler, http.MethodGet,
		"/v1/agent-groups/"+reviewerGroupID+"/members", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo reviewer group members = %d: %s", code, raw)
	}
	if memberItems := demoViewItems(t, groupMembers); len(memberItems) < 1 {
		t.Fatal("demo reviewer group has no members")
	}

	// Evals: resolve the stable suite name, then prove both completed run rows
	// feed a scorecard with a real two-point trend.
	code, suites, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/m/evals/suites", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo eval suites = %d: %s", code, raw)
	}
	suite := findDemoViewItem(t, demoViewItems(t, suites), "name", seed.EvalSuiteName)
	suiteID, _ := suite["id"].(string)
	if suiteID == "" {
		t.Fatalf("demo eval suite has no id: %v", suite)
	}
	code, runs, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/m/evals/runs", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo eval runs = %d: %s", code, raw)
	}
	if runItems := demoViewItems(t, runs); len(runItems) < 2 {
		t.Fatalf("demo eval runs = %d, want at least 2", len(runItems))
	}
	code, scorecards, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/m/evals/scorecards", token, tenant, nil)
	if code != http.StatusOK {
		t.Fatalf("list demo eval scorecards = %d: %s", code, raw)
	}
	card := findDemoViewItem(t, demoViewItems(t, scorecards), "suite_ref", suiteID)
	trend, _ := card["trend"].([]any)
	if len(trend) < 2 {
		t.Fatalf("demo eval scorecard trend = %d, want at least 2: %v", len(trend), card)
	}
	firstScore := trend[0].(map[string]any)["score"]
	lastScore := trend[len(trend)-1].(map[string]any)["score"]
	if firstScore == lastScore {
		t.Fatalf("demo eval scorecard trend is trivial: %v", trend)
	}

	// Backup: boot() does not run serve's post-announcement startup action, so
	// invoke the synchronous production seam directly and read the console list.
	if err := eng.api.RunStartupBackup(ctx, demoPassword, "e2e demo backup", "e2e"); err != nil {
		t.Fatalf("run demo startup backup: %v", err)
	}
	code, backups, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/console/dr/backups", token, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list demo backups = %d: %s", code, raw)
	}
	if backupItems := demoViewItems(t, backups); len(backupItems) < 1 {
		t.Fatal("demo backup list is empty after RunStartupBackup")
	}
}

func doDemoViewJSON(t *testing.T, handler http.Handler, method, path, token, tenant string, body any) (int, map[string]any, string) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("encode %s %s body: %v", method, path, err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.RemoteAddr = "10.0.0.1:1234"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded, recorder.Body.String()
}

func demoViewItems(t *testing.T, response map[string]any) []any {
	t.Helper()
	items, ok := response["items"].([]any)
	if !ok {
		t.Fatalf("response has no items list: %v", response)
	}
	return items
}

func findDemoViewItem(t *testing.T, items []any, field, value string) map[string]any {
	t.Helper()
	for _, item := range items {
		row, ok := item.(map[string]any)
		if ok && row[field] == value {
			return row
		}
	}
	t.Fatalf("no item with %s=%q in %v", field, value, items)
	return nil
}
