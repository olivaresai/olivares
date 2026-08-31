// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// findingsFor returns the captured bus findings of a kind for a subject ref.
func (h *harness) findingsFor(kind, subjectRef string) int {
	h.findMu.Lock()
	defer h.findMu.Unlock()
	n := 0
	for _, f := range h.findings {
		if f.Kind == kind && f.SubjectRef == subjectRef {
			n++
		}
	}
	return n
}

// TestDelegationAndMCPGraphDerived proves the comm graph is DERIVED from edge.observed
// (Task delegation + MCP topology), accumulates a repeated delegation, and carries the
// honest-coverage caveat. No payload is ever stored.
func TestDelegationAndMCPGraphDerived(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	now := time.Now()

	h.publishEdge(tenant, delegationEdge("sess-1", "code-reviewer", now))
	h.publishEdge(tenant, delegationEdge("sess-1", "code-reviewer", now.Add(time.Second))) // same link again → count bumps
	h.publishEdge(tenant, sdkmodel.EdgeObservation{
		OriginKind: "session", OriginRef: "sess-1", ResourceKind: "mcp.server", ResourceRef: "github",
		Mode: sdkmodel.ModeUnknown, Source: sdkmodel.SignalOTEL, Confidence: sdkmodel.ConfidenceAttributed, ObservedAt: now,
	})

	g := h.waitForGraphEdges(tok, tenant, 2)
	var deleg, mcp *edgeDTO
	for i := range g.Edges {
		switch g.Edges[i].LinkKind {
		case linkDelegation:
			deleg = &g.Edges[i]
		case linkMCPServer:
			mcp = &g.Edges[i]
		}
	}
	if deleg == nil || mcp == nil {
		t.Fatalf("expected a delegation and an mcp_server edge, got %+v", g.Edges)
	}
	if deleg.Source != "sess-1" || deleg.Target != "code-reviewer" || deleg.ToolRef != "Task" {
		t.Fatalf("delegation edge wrong: %+v", deleg)
	}
	if deleg.DelegationCount < 2 {
		t.Fatalf("delegation count should accumulate, got %d", deleg.DelegationCount)
	}
	if mcp.Target != "github" {
		t.Fatalf("mcp edge wrong: %+v", mcp)
	}
	if g.Coverage.Source != "edge.observed" || len(g.Coverage.Caveats) == 0 {
		t.Fatalf("graph must carry honest coverage, got %+v", g.Coverage)
	}
	// role derivation: sess-1 delegates → supervisor; code-reviewer only receives → worker.
	roles := map[string]string{}
	for _, n := range g.Nodes {
		roles[n.Ref] = n.Role
	}
	if roles["sess-1"] != roleSupervisor || roles["code-reviewer"] != roleWorker {
		t.Fatalf("role derivation wrong: %+v", roles)
	}
	// Minimal data: the serialized graph must not contain any payload/prompt/args field.
	raw, _ := json.Marshal(g)
	for _, banned := range []string{"payload", "prompt", "args", "arguments", "command", "transcript"} {
		if strings.Contains(strings.ToLower(string(raw)), banned) {
			t.Fatalf("graph JSON leaked a %q field: %s", banned, raw)
		}
	}
}

// TestTenantIsolationGraph proves one tenant never sees another's relations.
// TestA2AGraphSpansNonClaudeAgent proves AIP-05's wiring: an A2A edge from the a2a
// connector places a non-Claude agent in module IV's graph as an agent↔agent link,
// and the honest coverage no longer claims "no a2a connector".
func TestA2AGraphSpansNonClaudeAgent(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	now := time.Now()

	// A peer-to-peer A2A edge: planner (agent) → researcher (a non-Claude agent).
	h.publishEdge(tenant, sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "planner",
		ResourceKind: "a2a.agent", ResourceRef: "researcher",
		ToolRef: "summarize", Mode: sdkmodel.ModeUnknown,
		Source: sdkmodel.SignalA2A, Confidence: sdkmodel.ConfidenceAttributed, ObservedAt: now,
	})

	g := h.waitForGraphEdges(tok, tenant, 1)
	if len(g.Edges) != 1 || g.Edges[0].LinkKind != linkA2A {
		t.Fatalf("expected one a2a edge, got %+v", g.Edges)
	}
	e := g.Edges[0]
	if e.Source != "planner" || e.Target != "researcher" || e.ToolRef != "summarize" {
		t.Fatalf("a2a edge wrong: %+v", e)
	}
	if e.SignalSource != string(sdkmodel.SignalA2A) {
		t.Fatalf("a2a edge should carry the a2a signal source, got %q", e.SignalSource)
	}
	// The graph spans the non-Claude agents, both as AGENT nodes (not sessions).
	kinds := map[string]string{}
	for _, n := range g.Nodes {
		kinds[n.Ref] = n.Kind
	}
	if kinds["researcher"] != nodeAgent || kinds["planner"] != nodeAgent {
		t.Fatalf("A2A peers must be agent nodes, got %+v", kinds)
	}
	// Honest coverage: the caveat must NO LONGER lead with "no a2a connector".
	if len(g.Coverage.Caveats) == 0 || strings.HasPrefix(g.Coverage.Caveats[0], "no a2a connector") {
		t.Fatalf("coverage must no longer say 'no a2a connector', got %+v", g.Coverage.Caveats)
	}
	if !strings.Contains(g.Coverage.Caveats[0], "A2A") && !strings.Contains(g.Coverage.Caveats[0], "a2a") {
		t.Fatalf("coverage should mention A2A is now observed, got %+v", g.Coverage.Caveats)
	}
}

func TestTenantIsolationGraph(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	a := h.createOrg(admin, "tenant-a")
	b := h.createOrg(admin, "tenant-b")
	tokA := h.roleToken(admin, a, "a@x.io", "admin")
	tokB := h.roleToken(admin, b, "b@x.io", "admin")

	h.publishEdge(a, delegationEdge("sess-a", "worker-a", time.Now()))
	h.waitForGraphEdges(tokA, a, 1)

	r := h.do("GET", "/v1/m/orchestration/graph", tokB, nil, tenantHdr(b))
	var g graphResponse
	_ = json.Unmarshal([]byte(r.raw), &g)
	if len(g.Edges) != 0 {
		t.Fatalf("tenant B must not see tenant A relations, got %+v", g.Edges)
	}
}

// TestFireDenyClosedNoGate proves a fire is denied by default when no approval gate
// is wired, and the governance gap is surfaced as a Finding (never a silent fire).
func TestFireDenyClosedNoGate(t *testing.T) {
	h, _ := newHarness(t) // default: denyGate + unwiredDispatcher
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	// Phase 1: request → no_gate.
	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusAccepted || r.body["gate_status"] != string(StatusNoGate) {
		t.Fatalf("phase1 = %d %s", r.code, r.raw)
	}
	if !h.waitForFinding(busUngovernedFire) {
		t.Fatal("an un-governable fire must raise an ungoverned-fire finding")
	}
	// Phase 2: any approval_ref → still denied (deny-by-default).
	r = h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("phase2 must be blocked, got %d %s", r.code, r.raw)
	}
}

// TestFireApprovedDeclaredNotFired proves an approved fire with NO dispatcher is
// honestly "declared, not fired" — never a pretend actuation — and attributed to the
// REAL principal (not the system actor).
func TestFireApprovedDeclaredNotFired(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDeclaredNotFired {
		t.Fatalf("approved+no-dispatcher must be declared_not_fired, got %d %s", r.code, r.raw)
	}
	// The fire decision in the ledger is attributed to the real principal.
	dr := h.do("GET", "/v1/m/orchestration/schedules/"+id+"/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	fireRow := findDecision(ledger.Items, opFire)
	if fireRow == nil {
		t.Fatalf("no fire decision recorded: %s", dr.raw)
	}
	if fireRow.Actor == model.ActorSystem || fireRow.ActorKind == model.ActorSystem || !strings.HasPrefix(fireRow.Actor, "user:") {
		t.Fatalf("fire must be attributed to the real principal, got actor=%q kind=%q", fireRow.Actor, fireRow.ActorKind)
	}
}

// TestFireDispatched proves an approved fire with a wired dispatcher is dispatched.
func TestFireDispatched(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}), WithDispatcher(fakeDispatcher{ref: "run-42"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "run-42" {
		t.Fatalf("approved+dispatcher must dispatch, got %d %s", r.code, r.raw)
	}
}

// TestPlanHashMismatchBlocks proves an approval bound to a different plan cannot
// authorize this fire (anti-TOCTOU).
func TestPlanHashMismatchBlocks(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved, planHash: "stale-plan"}), WithDispatcher(fakeDispatcher{ref: "run-x"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("stale plan_hash must block, got %d %s", r.code, r.raw)
	}
}

// TestCadenceMissOnlyActiveRecurring proves the anti-evasion miss fires ONLY for an
// active recurring schedule gone overdue — never for a one-shot that simply finished.
func TestCadenceMissOnlyActiveRecurring(t *testing.T) {
	clock := newManualClock()
	h, _ := newHarness(t, WithClock(clock))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	h.createSchedule(tok, tenant, "recurring", "agent", "cron-agent", "cron", "*/5 * * * *", 60)
	h.createSchedule(tok, tenant, "oneshot", "agent", "oneshot-agent", "manual", "", 0) // interval 0 → no miss check

	clock.advance(300 * time.Second) // > 60*2 grace for the recurring one
	r := h.do("GET", "/v1/m/orchestration/schedules", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	if !h.waitForFinding(busCadenceMiss) {
		t.Fatal("an overdue active recurring schedule must raise a cadence-miss finding")
	}
	if got := h.findingsFor(busCadenceMiss, "oneshot-agent"); got != 0 {
		t.Fatalf("a one-shot/interval-0 schedule must NOT raise a cadence-miss, got %d", got)
	}
	if got := h.findingsFor(busCadenceMiss, "cron-agent"); got == 0 {
		t.Fatal("the recurring schedule must have a cadence-miss")
	}
	// The schedule shows derived health "stalled".
	var list listResponse[scheduleDTO]
	_ = json.Unmarshal([]byte(r.raw), &list)
	if s := findSchedule(list.Items, "recurring"); s == nil || s.Health != stateStalled {
		t.Fatalf("recurring schedule health must be stalled, got %+v", s)
	}
	// The cadence_miss is a SYSTEM detection (not a principal action).
	dr := h.do("GET", "/v1/m/orchestration/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	if d := findDecision(ledger.Items, opCadenceMiss); d == nil || d.Actor != model.ActorSystem {
		t.Fatalf("cadence_miss must be a system-attributed detection, got %+v", d)
	}
}

// TestCadenceRecovers proves a recovered subject (activity resumes) clears its sticky
// miss marker on the next read-time scan.
func TestCadenceRecovers(t *testing.T) {
	clock := newManualClock()
	h, _ := newHarness(t, WithClock(clock))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.createSchedule(tok, tenant, "recurring", "agent", "cron-agent", "cron", "*/5 * * * *", 60)

	clock.advance(300 * time.Second)
	h.do("GET", "/v1/m/orchestration/schedules", tok, nil, tenantHdr(tenant)) // → missed
	if !h.waitForFinding(busCadenceMiss) {
		t.Fatal("expected a miss first")
	}

	// Activity resumes at the current (advanced) time.
	h.publishEdge(tenant, delegationEdge("sess-9", "cron-agent", clock.Now().Time()))
	h.waitForGraphEdges(tok, tenant, 1)

	r := h.do("GET", "/v1/m/orchestration/schedules", tok, nil, tenantHdr(tenant))
	var list listResponse[scheduleDTO]
	_ = json.Unmarshal([]byte(r.raw), &list)
	s := findSchedule(list.Items, "recurring")
	if s == nil || s.MissedAt != "" || s.Health != "active" {
		t.Fatalf("recovered schedule must clear its miss, got %+v", s)
	}
}

// TestDecisionLedgerAppendOnly proves the governance-evidence ledger cannot be
// mutated (immutable chain-of-custody, docs/SECURITY-HARDENING.md).
func TestDecisionLedgerAppendOnly(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)
	// Produce one decision row (a fire_request under the deny gate).
	h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{}, tenantHdr(tenant))

	ctx := context.Background()
	err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(decisionKind)
		if e != nil {
			return e
		}
		recs, _, e := repo.List(ctx, model.Query{Limit: 1})
		if e != nil {
			return e
		}
		if len(recs) == 0 {
			t.Fatal("expected a decision row")
		}
		_, e = repo.Update(ctx, recs[0])
		return e
	})
	if err != store.ErrAppendOnly {
		t.Fatalf("decision ledger must be append-only, update got %v", err)
	}
}

// TestPermissionTiers proves the verb-tier gating: a viewer reads the graph but cannot
// declare a schedule; an editor declares but cannot fire (admin-tier).
func TestPermissionTiers(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@acme.io", "editor")

	if r := h.do("GET", "/v1/m/orchestration/graph", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer should read graph, got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/orchestration/schedules", viewer, map[string]any{"name": "x", "subject_kind": "agent", "subject_ref": "a", "trigger_kind": "manual"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer must not declare a schedule, got %d %s", r.code, r.raw)
	}
	id := h.createSchedule(editor, tenant, "nightly", "agent", "batch-agent", "manual", "", 0)
	if r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", editor, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor must not fire (admin-tier), got %d %s", r.code, r.raw)
	}
}

// TestFireEmptyPlanHashBlocked proves a gate that APPROVES but echoes NO plan hash
// cannot authorize a fire — the strict binding blocks a partial/buggy gate.
func TestFireEmptyPlanHashBlocked(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved, emptyHash: true}), WithDispatcher(fakeDispatcher{ref: "run-x"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)
	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("an approval echoing an empty plan hash must block, got %d %s", r.code, r.raw)
	}
}

// TestFireEmptyBodyIsPhase1 proves an empty fire body triggers phase 1 (request),
// not a 400 (the body is optional).
func TestFireEmptyBodyIsPhase1(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)
	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusAccepted || r.body["op"] != opFireRequest {
		t.Fatalf("an empty body must trigger phase-1, got %d %s", r.code, r.raw)
	}
}

// TestScheduleIntervalRequiresCron proves a cadence-miss interval is rejected on a
// non-cron trigger (no fabricated anti-evasion baseline).
func TestScheduleIntervalRequiresCron(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	r := h.do("POST", "/v1/m/orchestration/schedules", tok, map[string]any{
		"name": "bad", "subject_kind": "agent", "subject_ref": "a", "trigger_kind": "manual", "expected_interval_seconds": 60,
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("an interval on a non-cron trigger must be rejected, got %d %s", r.code, r.raw)
	}
}

// --- helpers shared by the tests ---

func (h *harness) createSchedule(token string, tenant model.TenantID, name, subjectKind, subjectRef, trigger, cadence string, interval int64) string {
	h.t.Helper()
	body := map[string]any{
		"name": name, "subject_kind": subjectKind, "subject_ref": subjectRef,
		"trigger_kind": trigger, "cadence_spec": cadence, "expected_interval_seconds": interval,
	}
	r := h.do("POST", "/v1/m/orchestration/schedules", token, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create schedule = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

func findDecision(items []decisionDTO, op string) *decisionDTO {
	for i := range items {
		if items[i].Op == op {
			return &items[i]
		}
	}
	return nil
}

func findSchedule(items []scheduleDTO, name string) *scheduleDTO {
	for i := range items {
		if items[i].Name == name {
			return &items[i]
		}
	}
	return nil
}
