// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Management layer (modules VI, VII, VIII, XIV): the actionable governance,
// deploy, knowledge and catalog flows driven through their real write APIs. Where
// a seam is deny-closed by design in the composition root (deploy executor /
// approval gate), the test asserts the honest fail-closed shape, never a faked
// success — and the gap is reported to its owning session, not patched here.

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// agentID resolves a seeded agent's core id by its external id.
func (h *harness) agentID(externalID string) string {
	h.t.Helper()
	list := h.getJSON(h.adminToken, h.tenantA, "/v1/agents?limit=100")
	for _, a := range items(list) {
		if a["external_id"] == externalID {
			id, _ := a["id"].(string)
			return id
		}
	}
	h.t.Fatalf("agent %q not found", externalID)
	return ""
}

func TestE2E_Governance_HITLApprovalAndBinding(t *testing.T) {
	h := newHarness(t)
	// Separation of duty needs a second human with the approval-admin tier — and
	// deploy.apply is in the default CRITICAL set, so releasing it takes TWO
	// distinct humans (the engine-floored dual authorization, NIST AC-3(2)).
	decider := h.newUser("approver@e2e.test", "approver-pass-1", h.tenantA, "admin")
	decider2 := h.newUser("approver2@e2e.test", "approver-pass-2", h.tenantA, "admin")

	// The superadmin (requester) opens an approval request asking for a single
	// approval — the engine floors a CRITICAL action at 2 (a requester can never
	// lower their own bar).
	var ap struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Required int    `json:"required_approvals"`
		RiskTier string `json:"risk_tier"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/approvals", h.adminToken, h.tenantA, map[string]any{
		"subject_kind": "deployment", "subject_ref": "svc-1",
		"action": "deploy.apply", "reason": "ship v2", "required_approvals": 1,
	}, &ap); code != http.StatusCreated || ap.ID == "" {
		t.Fatalf("create approval = %d", code)
	}
	assertEq(t, "approval.status", ap.Status, "pending")
	assertEq(t, "approval.risk_tier", ap.RiskTier, "critical")
	if ap.Required != 2 {
		t.Fatalf("approval.required_approvals = %d, want the critical floor 2", ap.Required)
	}

	// Separation of duty: the requester cannot decide their own request.
	if code, _ := h.req("POST", "/v1/m/governance/approvals/"+ap.ID+"/decisions", h.adminToken, h.tenantA, map[string]any{
		"decision": "approve",
	}); code != http.StatusForbidden {
		t.Errorf("self-approval = %d, want 403 (separation of duty)", code)
	}

	// A distinct admin approves → one of two: still pending.
	if code, raw := h.req("POST", "/v1/m/governance/approvals/"+ap.ID+"/decisions", decider, h.tenantA, map[string]any{
		"decision": "approve", "note": "lgtm",
	}); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("approve = %d: %s", code, raw)
	}
	got := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+ap.ID)
	assertEq(t, "approval.after-one", got["status"], "pending")

	// The second distinct admin crosses the dual-control threshold.
	if code, raw := h.req("POST", "/v1/m/governance/approvals/"+ap.ID+"/decisions", decider2, h.tenantA, map[string]any{
		"decision": "approve", "note": "lgtm too",
	}); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("second approve = %d: %s", code, raw)
	}
	got = h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+ap.ID)
	assertEq(t, "approval.final", got["status"], "approved")

	// Bind an agent to a freshly-minted NHI (resolves attribution for the graph).
	var bind struct {
		Minted bool `json:"minted"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/agents/"+h.agentID(seed.AgentCoder)+"/identity", h.adminToken, h.tenantA, map[string]any{
		"mint": true,
	}, &bind); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("bind identity = %d", code)
	}
	if !bind.Minted {
		t.Error("identity bind did not mint an NHI")
	}
}

func TestE2E_Deploy_ControlPlaneAndFailClosed(t *testing.T) {
	h := newHarness(t)

	var def struct {
		ID             string  `json:"id"`
		CurrentVersion float64 `json:"current_version"`
		AppliedVersion float64 `json:"applied_version"`
	}
	if code := h.reqInto("POST", "/v1/m/deploy/definitions", h.adminToken, h.tenantA, map[string]any{
		"subject_kind": "agent", "subject_ref": seed.AgentCoder,
		"name": "billing-svc", "environment": "prod", "target": "docker://host", "runtime": "docker",
		"spec": map[string]any{"image": "billing:1", "replicas": 1},
	}, &def); code != http.StatusCreated || def.ID == "" {
		t.Fatalf("create definition = %d", code)
	}
	assertEq(t, "current_version", def.CurrentVersion, float64(1))
	assertEq(t, "applied_version (never applied)", def.AppliedVersion, float64(0))

	// A new revision bumps the version; rollback declares another.
	if code, raw := h.req("PUT", "/v1/m/deploy/definitions/"+def.ID, h.adminToken, h.tenantA, map[string]any{
		"spec": map[string]any{"image": "billing:2", "replicas": 1},
	}); code != http.StatusOK {
		t.Fatalf("update definition = %d: %s", code, raw)
	}
	if code, raw := h.req("POST", "/v1/m/deploy/definitions/"+def.ID+"/rollback", h.adminToken, h.tenantA, map[string]any{
		"to_version": 1,
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("rollback = %d: %s", code, raw)
	}
	revs := h.getJSON(h.adminToken, h.tenantA, "/v1/m/deploy/definitions/"+def.ID+"/revisions")
	if len(items(revs)) < 3 {
		t.Errorf("revisions = %d, want >=3 (create, update, rollback)", len(items(revs)))
	}

	// HONEST FAIL-CLOSED: no executor is wired in the composition root, so plan
	// returns 503, never a faked success. (Gap reported to composition root.)
	if code, _ := h.req("POST", "/v1/m/deploy/definitions/"+def.ID+"/plan", h.adminToken, h.tenantA, nil); code != http.StatusServiceUnavailable {
		t.Errorf("plan = %d, want 503 (no executor wired — deny-closed by design)", code)
	}
}

func TestE2E_Knowledge_RAGAndLineage(t *testing.T) {
	h := newHarness(t)

	var kb struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA, map[string]any{
		"name": "runbooks", "classification": "public", "embed_policy": "auto",
	}, &kb); code != http.StatusCreated || kb.ID == "" {
		t.Fatalf("create kb = %d", code)
	}
	if code, raw := h.req("POST", "/v1/m/knowledge/kbs/"+kb.ID+"/ingest", h.adminToken, h.tenantA, map[string]any{
		"documents": []map[string]any{{
			"source_doc_id": "d1", "title": "rotate keys",
			"body": "alpha beta gamma rotation runbook", "classification": "public", "acl": []any{},
		}},
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", code, raw)
	}

	var q struct {
		LineageID string `json:"lineage_id"`
		Count     float64
		Results   []map[string]any `json:"results"`
		Egress    bool             `json:"egress"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs/"+kb.ID+"/query", h.adminToken, h.tenantA, map[string]any{
		"query": "alpha", "top_k": 5,
	}, &q); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("query = %d", code)
	}
	if q.Count < 1 || len(q.Results) < 1 {
		t.Fatalf("RAG returned no public chunk: count=%v results=%d", q.Count, len(q.Results))
	}
	assertEq(t, "query.egress (data never leaves)", q.Egress, false)

	// Every query writes an immutable lineage row proving the data stayed inside.
	lin := h.getJSON(h.adminToken, h.tenantA, "/v1/m/knowledge/lineage?kb_id="+kb.ID)
	rows := items(lin)
	if len(rows) == 0 {
		t.Fatal("no lineage row written for the query")
	}
	assertEq(t, "lineage.decision", rows[0]["decision"], "allowed")
	assertEq(t, "lineage.egress", rows[0]["egress"], false)
}

func TestE2E_Catalog_GovernedLifecycle(t *testing.T) {
	h := newHarness(t)

	var e struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if code := h.reqInto("POST", "/v1/m/catalog/entries", h.adminToken, h.tenantA, map[string]any{
		"kind": "agent", "name": "Indexer Template", "slug": "indexer-template",
		"version": "1.0.0", "summary": "a reusable indexer", "spec": map[string]any{"role": "indexer"},
	}, &e); code != http.StatusCreated || e.ID == "" {
		t.Fatalf("create entry = %d", code)
	}
	assertEq(t, "entry.draft", e.Status, "draft")

	for _, step := range []string{"submit", "approve"} {
		if code, raw := h.req("POST", "/v1/m/catalog/entries/"+e.ID+"/"+step, h.adminToken, h.tenantA, nil); code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("%s = %d: %s", step, code, raw)
		}
	}

	// Approved entries are content-hash pinned and ledger-attested; signing is
	// honestly OFF in the composition root (no key configured) → signed:false but
	// verified:true via the hash pin.
	v := h.getJSON(h.adminToken, h.tenantA, "/v1/m/catalog/entries/"+e.ID+"/verify")
	assertEq(t, "verify.hash_ok", v["hash_ok"], true)
	assertEq(t, "verify.verified", v["verified"], true)
	assertEq(t, "verify.signed (no key wired)", v["signed"], false)

	// Only an approved entry can be instantiated; the instance is governed.
	var inst struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/catalog/entries/"+e.ID+"/instantiate", h.adminToken, h.tenantA, map[string]any{
		"name": "indexer-prod-1",
	}, &inst); code != http.StatusCreated || inst.ID == "" {
		t.Fatalf("instantiate = %d", code)
	}
	if code, raw := h.req("POST", "/v1/m/catalog/instances/"+inst.ID+"/transition", h.adminToken, h.tenantA, map[string]any{
		"status": "approved",
	}); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("transition = %d: %s", code, raw)
	}
}
