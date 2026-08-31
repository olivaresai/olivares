// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

// mcpgateway_task_evidence_test.go — Stage 4 composition-root proofs of
// the evidence-enforced TASK surface over the REAL durable journal: the
// per-action claim/settle ledger events, the full track/get/cancel/update
// lifecycle through ServeHTTP on a verified signed chain, the degrade Seq==0
// loss accounting on a task action, and the kill-switch interplay (a stopped
// estate settles a blocked task fetch durably while tasks/cancel still passes).
//
// RED note: these tests are seam-dependent — ToolDecision.EffectAction and the
// enforced task handlers did not exist before this stage, so they could not
// compile against the stage-3 code; their RED is the syntactic impossibility
// (the behavioral RED for the same exploits was captured connector-level in
// connectors/mcp/task_evidence_test.go — see sessions log).

func mcpTaskAllowDecision(f *mcpLedgerFixture, idKind, action string) mcpc.ToolDecision {
	return mcpc.ToolDecision{
		Tenant: f.tenant.String(), Subject: "agent:mcp-task", Tool: "search",
		RequiredScope: "tools:read", Allowed: true, Reason: "task effect authorized",
		TaskID: "task-adapter-1", MCPTag: "MCP07", TokenBinding: "dpop",
		OperationIDKind: idKind, EffectAction: action,
	}
}

// TestMCPEvidenceTaskActionsJournalLifecycle drives one claim+settle per task
// action against the REAL store and pins the paired "<action>.claim" /
// "<action>.settle" ledger events, the journal row, and the verified chain.
func TestMCPEvidenceTaskActionsJournalLifecycle(t *testing.T) {
	for _, tc := range []struct {
		idKind string
		action string
	}{
		{"keyed", "mcp.task.get.keyed"},
		{"request_instance", "mcp.task.get.request_instance"},
		{"keyed", "mcp.task.cancel.keyed"},
		{"keyed", "mcp.task.update.keyed"},
		{"", "mcp.task.track"},
		{"", "mcp.task.cancel.compensation"},
		{"request_instance", "mcp.task.cancel.sweep"},
		// Review ROUND-4: the operator reconciliation surface. Every MUTATING
		// reconciliation action is evidence-bound like the rest of stage 4, so its
		// journal action must produce the same paired claim/settle ledger events on
		// the real store.
		{"keyed", "mcp.task.reconcile.status.keyed"},
		{"request_instance", "mcp.task.reconcile.clear.request_instance"},
		{"keyed", "mcp.task.reconcile.retire.keyed"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			f := newMCPLedgerFixture(t)
			a := mcpEvidenceAuditor(f)
			ctx := context.Background()
			binding := mcpEvidenceBinding("op-"+tc.action, "digest-a")
			before := mcpLedgerHead(t, f.store, f.tenant)

			rec := a.Record(ctx, mcpTaskAllowDecision(f, tc.idKind, tc.action), binding)
			if rec.State != mcpc.GateRecordFresh || !rec.MayEmit(binding) {
				t.Fatalf("fresh claim record = %+v, want fresh+emittable", rec)
			}
			events := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
			if len(events) != 1 || events[0].Action != tc.action+".claim" {
				t.Fatalf("claim events = %+v, want exactly one %s.claim", events, tc.action)
			}
			settlement := a.Settle(ctx, mcpc.GateOutcome{
				Record: rec, State: mcpc.DispatchCompleted, ResultDigest: "res-1", DispatchRef: "disp-1",
			})
			if settlement.FailureClass != sdk.FailureNone {
				t.Fatalf("settlement = %+v", settlement)
			}
			events = mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
			if len(events) != 2 || events[1].Action != tc.action+".settle" {
				t.Fatalf("post-settle events = %+v, want claim+settle of %s", events, tc.action)
			}
			row, ok := mcpJournalRow(t, f, "op-"+tc.action)
			if !ok || row.State != model.EvidenceOpCompleted {
				t.Fatalf("journal row = %+v ok=%t, want settled completed", row, ok)
			}
			verifyMCPLedger(t, f)
		})
	}
}

// newMCPDegradeStore provisions a file-backed signed store with a tenant (no
// spool budget so provisioning is unaffected), then reopens it with a 1-byte
// DEGRADE spool budget: every governed append drops with durable loss
// accounting (the openHookSpoolStore technique of the F9 fixtures).
func newMCPDegradeStore(t *testing.T) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}
	dsn := filepath.Join(t.TempDir(), "mcp-task-degrade.db")
	seed, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	var tenant model.TenantID
	if err := seed.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, cerr := sys.CreateOrg(ctx, model.Org{Name: "mcp-task-degrade", Slug: "mcp-task-degrade", Status: model.StatusActive})
		if cerr == nil {
			tenant = org.TenantID
		}
		return cerr
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return openHookSpoolStore(t, dsn, signer, 1, store.AuditSpoolDegrade), tenant
}

// TestMCPEvidenceTaskDegradeSeqZeroRefusesAndCountsLoss — the degrade exploit
// on a TASK action: under the DEGRADE spool policy the task-effect claim whose
// evidence drops (Seq==0) refuses spool_degraded AND durably advances the
// pending-drops loss accounting; no journal row is created.
func TestMCPEvidenceTaskDegradeSeqZeroRefusesAndCountsLoss(t *testing.T) {
	ctx := context.Background()
	st, tenant := newMCPDegradeStore(t)
	a := mcpGateAuditor{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), store: st, tenant: tenant,
	}
	base := hookPendingDrops(t, st)
	binding := mcpEvidenceBinding("op-task-degrade-1", "digest-a")
	rec := a.Record(ctx, mcpc.ToolDecision{
		Tenant: tenant.String(), Subject: "agent:degrade", Tool: "search", Allowed: true,
		TaskID: "task-degrade", EffectAction: "mcp.task.cancel.sweep",
		OperationIDKind: "request_instance",
	}, binding)
	if rec.State != mcpc.GateRecordRefused || rec.Receipt.Fault != sdk.EvidenceFaultSpoolDegraded {
		t.Fatalf("degrade task claim = state %q fault %q, want refused/spool_degraded", rec.State, rec.Receipt.Fault)
	}
	if rec.MayEmit(binding) {
		t.Fatal("degrade-dropped task claim must never be emittable")
	}
	if got := hookPendingDrops(t, st) - base; got != 1 {
		t.Fatalf("durable pending drops advanced by %d, want exactly 1 (commit the gap, THEN refuse)", got)
	}
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, gerr := sc.EvidenceOperations().Get(ctx, "op-task-degrade-1")
		if gerr == store.ErrNotFound {
			return nil
		}
		if gerr == nil {
			t.Fatal("degrade drop must not create a journal row")
		}
		return gerr
	})
	if err != nil {
		t.Fatalf("journal check: %v", err)
	}
}

// --- full-path lifecycle + kill-switch interplay -------------------------------

// conformingGetTaskResultJSON renders a COMPLETE SEP-2663 `GetTaskResult`
// (= `Result` & `DetailedTask`): the mandatory `complete` discriminator, the task
// identity, the mandatory createdAt/lastUpdatedAt/ttlMs Task fields and the
// status-specific payload. Review round-5 R5-01 — the gateway now refuses to
// treat anything less as authoritative proof that a task finished, because that
// proof is what authorizes the operator retirement that DELETES a record.
func conformingGetTaskResultJSON(taskID, status string) string {
	payload := ""
	switch status {
	case "completed":
		payload = `,"result":{"content":[]}`
	case "failed":
		payload = `,"error":{"code":-32000,"message":"upstream execution failed"}`
	case "input_required":
		payload = `,"inputRequests":{"req-1":{"method":"elicitation/create"}}`
	}
	return `{"resultType":"complete","taskId":"` + taskID + `","status":"` + status + `",` +
		`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null` + payload + `}`
}

// mcpTaskUpstreamServer serves strictly valid correlated JSON-RPC responses per
// method and counts the forwards it observes per method.
func mcpTaskUpstreamServer(t *testing.T, taskID string, counts map[string]*int32) *httptest.Server {
	t.Helper()
	results := map[string]string{
		"tools/call": `{"resultType":"task","taskId":"` + taskID + `","status":"working"}`,
		// Round-3 R3-01 / round-4 R4-02: SEP-2663 defines UpdateTaskResult as an
		// acknowledgement that carries NO TASK STATE — task state is observed
		// through tasks/get or task notifications, never through the update result
		// — but it is still `Result`, so `resultType:"complete"` is MANDATORY on it
		// and `{}` is NOT conformant. The gateway neither applies nor relays a
		// state-reporting update body, and it refuses one missing the mandatory
		// discriminator; the state-report refusal itself is pinned by
		// TestMCPTaskUpdateAckOnlyNeverConfirmsStatus below.
		"tasks/update": `{"resultType":"complete"}`,
		// Round-5 R5-01: `GetTaskResult = Result & DetailedTask` — the discriminator,
		// the task identity AND the mandatory Task fields. The abbreviated round-4
		// body is exactly what the gateway was accepting as authoritative proof.
		"tasks/get":    conformingGetTaskResultJSON(taskID, "working"),
		"tasks/cancel": `{}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if c, ok := counts[body.Method]; ok {
			atomic.AddInt32(c, 1)
		}
		result, ok := results[body.Method]
		if !ok {
			result = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
}

func countMCPActionEvents(t *testing.T, f *mcpLedgerFixture, action string) (claims, settles int, opID string) {
	t.Helper()
	for _, ev := range mcpLedgerEventsFrom(t, f.store, f.tenant, 0) {
		switch ev.Action {
		case action + ".claim":
			claims++
			opID = string(ev.TargetID)
		case action + ".settle":
			settles++
		}
	}
	return claims, settles, opID
}

func postMCP(t *testing.T, rs *mcpc.ResourceServer, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	return w
}

// mcpTaskToolsCallBody renders the CONFORMING task-creating tools/call these
// lifecycle tests drive. Stage 5 (design adjudication §1/§2/§6): the RC pin
// makes `io.modelcontextprotocol/clientCapabilities` Required on EVERY request
// and forbids inferring capabilities from prior requests
// (an internal design note (not shipped):88-98), and the task-handle response
// contract is selected ONLY by that exact per-request declaration plus the exact
// `resultType:"task"` discriminator (rs.go tools/call site + tasks.go
// selectDeclaredTaskHandle). The stage-4 fixtures omitted the declaration — a
// NON-CONFORMING request whose task-shaped result is open-Result extension data
// the redesigned gateway correctly relays without registering. Declaring the
// capability restores the exact contract these tests pin; no assertion changed.
func mcpTaskToolsCallBody(opID string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"q":"x"},` +
		`"_meta":{"ai.olivares/operationId":"` + opID + `",` +
		`"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
}

// TestMCPTaskLifecycleFullPathJournalsTaskEffects drives the WHOLE task
// lifecycle through ServeHTTP against the real store: tools/call creating the
// task (parent claim/settle + the mcp.task.track child), then keyed
// tasks/update, tasks/get and tasks/cancel — each with exactly one claim and
// one settle event, every journal row settled completed, on a verified chain.
func TestMCPTaskLifecycleFullPathJournalsTaskEffects(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	counts := map[string]*int32{
		"tools/call": new(int32), "tasks/update": new(int32),
		"tasks/get": new(int32), "tasks/cancel": new(int32),
	}
	up := mcpTaskUpstreamServer(t, "task-e2e", counts)
	defer up.Close()
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	if w := postMCP(t, rs, token, mcpTaskToolsCallBody("create-key-1")); w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
	}
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/update","params":{"taskId":"task-e2e","inputResponses":{},"_meta":{"ai.olivares/operationId":"update-key-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("tasks/update status = %d; body=%s", w.Code, w.Body.String())
	}
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"task-e2e","_meta":{"ai.olivares/operationId":"get-key-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("tasks/get status = %d; body=%s", w.Code, w.Body.String())
	}
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/cancel","params":{"taskId":"task-e2e","_meta":{"ai.olivares/operationId":"cancel-key-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("tasks/cancel status = %d; body=%s", w.Code, w.Body.String())
	}

	for method, want := range map[string]int32{
		"tools/call": 1, "tasks/update": 1, "tasks/get": 1, "tasks/cancel": 1,
	} {
		if got := atomic.LoadInt32(counts[method]); got != want {
			t.Errorf("upstream %s forwards = %d, want %d", method, got, want)
		}
	}
	for _, action := range []string{
		"mcp.tool.call.keyed", "mcp.task.track",
		"mcp.task.update.keyed", "mcp.task.get.keyed", "mcp.task.cancel.keyed",
	} {
		claims, settles, opID := countMCPActionEvents(t, f, action)
		if claims != 1 || settles != 1 {
			t.Errorf("%s events: claims=%d settles=%d, want exactly 1/1", action, claims, settles)
			continue
		}
		row, ok := mcpJournalRow(t, f, opID)
		if !ok || row.State != model.EvidenceOpCompleted {
			t.Errorf("%s journal row %s = %+v ok=%t, want settled completed", action, opID, row, ok)
		}
	}
	verifyMCPLedger(t, f)

	// Exact replay of a task method: recorded state, no re-dispatch.
	w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"task-e2e","_meta":{"ai.olivares/operationId":"get-key-1"}}}`)
	if w.Code != http.StatusConflict {
		t.Errorf("tasks/get exact replay status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(counts["tasks/get"]); got != 1 {
		t.Errorf("tasks/get forwards after replay = %d, want EXACTLY 1", got)
	}
}

// TestMCPTaskSweepUpstreamRPCErrorIsNotACancellation drives the round-1
// F-02 exploit through the REAL production forwarder: the upstream answers the
// sweep's tasks/cancel with a strictly valid HTTP 2xx JSON-RPC ERROR, which
// mcpUpstreamForwarder correctly classifies as {State: completed} plus a non-nil
// error. The round-1 gateway looked only at `state == completed`, marked the
// task locally canceled, counted it as a success and thereby removed a LIVE
// task from every future emergency sweep.
//
// The evidence stays honest either way (the round trip settles `completed`); it
// is the SUCCESS verdict that may not be inferred from it.
func TestMCPTaskSweepUpstreamRPCErrorIsNotACancellation(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	var cancels int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body.Method == "tasks/cancel" {
			atomic.AddInt32(&cancels, 1)
			// A strictly valid, correlated JSON-RPC error: a COMPLETED round trip
			// that explicitly refuses the cancellation.
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"task cannot be canceled"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"task","taskId":"task-rpcerr","status":"working"}}`))
	}))
	defer up.Close()
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	if w := postMCP(t, rs, token, mcpTaskToolsCallBody("rpcerr-create-1")); w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
	}

	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop rpcerr")
	if canceled != 0 {
		t.Errorf("canceled = %d, want 0: the upstream explicitly refused the cancellation", canceled)
	}
	if err == nil {
		t.Error("a refused upstream cancellation must be reported to the sweep caller")
	}
	if got := atomic.LoadInt32(&cancels); got != 1 {
		t.Errorf("upstream tasks/cancel forwards = %d, want 1", got)
	}
	// The task is STILL live: a second sweep must not silently succeed either,
	// and the task must never have been counted as canceled.
	canceled2, err2 := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop rpcerr 2")
	if canceled2 != 0 || err2 == nil {
		t.Errorf("second sweep = %d, %v; want 0 and a reported fault", canceled2, err2)
	}
	if got := atomic.LoadInt32(&cancels); got != 1 {
		t.Errorf("upstream tasks/cancel forwards after the second sweep = %d, want EXACTLY 1 "+
			"(a delivered cancellation is never automatically re-emitted)", got)
	}
	// The sweep operation itself settled honestly as a completed round trip.
	claims, settles, opID := countMCPActionEvents(t, f, "mcp.task.cancel.sweep")
	if claims != 1 || settles != 1 {
		t.Fatalf("sweep events: claims=%d settles=%d, want exactly 1/1", claims, settles)
	}
	row, ok := mcpJournalRow(t, f, opID)
	if !ok || row.State != model.EvidenceOpCompleted {
		t.Errorf("sweep journal row = %+v ok=%t, want settled completed (the round trip DID complete)", row, ok)
	}
	verifyMCPLedger(t, f)
}

// toggleStopGuard is a mutable kill-switch guard: not stopped until flipped.
type toggleStopGuard struct{ stopped atomic.Bool }

func (g *toggleStopGuard) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	if g.stopped.Load() {
		return governance.StopState{EstateStopped: true, EstateStopID: model.ID("11111111-1111-7111-8111-111111111111")}, nil
	}
	return governance.StopState{}, nil
}

// TestMCPTaskKillSwitchStopSettlesBlockedButCancelPasses: with an estate stop
// active, a task fetch is BLOCKED by the kill-switch upstream wrap and that
// outcome settles DURABLY (journal row blocked — evidence of the stopped
// effect), while tasks/cancel still passes through the stop (the deliberate
// kill-switch exemption) and settles completed. Evidence is never bypassed in
// either direction.
func TestMCPTaskKillSwitchStopSettlesBlockedButCancelPasses(t *testing.T) {
	f := newMCPLedgerFixture(t)
	guard := &toggleStopGuard{}
	eng := &engine{store: f.store, log: discardLogger(), killSwitch: guard}
	counts := map[string]*int32{
		"tools/call": new(int32), "tasks/get": new(int32), "tasks/cancel": new(int32),
	}
	up := mcpTaskUpstreamServer(t, "task-ks", counts)
	defer up.Close()
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	// Create + track the task BEFORE the stop.
	if w := postMCP(t, rs, token, mcpTaskToolsCallBody("ks-create-1")); w.Code != http.StatusOK {
		t.Fatalf("pre-stop tools/call status = %d; body=%s", w.Code, w.Body.String())
	}

	guard.stopped.Store(true)

	// tasks/get under the stop: the wrap blocks the forward BEFORE any transport
	// write; the blocked outcome settles durably against the claim.
	w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"task-ks","_meta":{"ai.olivares/operationId":"ks-get-1"}}}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("under-stop tasks/get status = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(counts["tasks/get"]); got != 0 {
		t.Errorf("tasks/get reached the upstream under a stop: %d forwards, want 0", got)
	}
	claims, settles, opID := countMCPActionEvents(t, f, "mcp.task.get.keyed")
	if claims != 1 || settles != 1 {
		t.Fatalf("under-stop tasks/get events: claims=%d settles=%d, want 1/1", claims, settles)
	}
	row, ok := mcpJournalRow(t, f, opID)
	if !ok || row.State != model.EvidenceOpBlocked {
		t.Fatalf("under-stop tasks/get journal row = %+v ok=%t, want settled blocked", row, ok)
	}

	// tasks/cancel PASSES during the stop (killswitchgate exemption) — and it is
	// still evidence-gated: claim + settle completed.
	w = postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/cancel","params":{"taskId":"task-ks","_meta":{"ai.olivares/operationId":"ks-cancel-1"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("under-stop tasks/cancel status = %d, want 200 (the kill-switch cancel exemption); body=%s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(counts["tasks/cancel"]); got != 1 {
		t.Errorf("tasks/cancel forwards under stop = %d, want 1", got)
	}
	claims, settles, opID = countMCPActionEvents(t, f, "mcp.task.cancel.keyed")
	if claims != 1 || settles != 1 {
		t.Fatalf("under-stop tasks/cancel events: claims=%d settles=%d, want 1/1", claims, settles)
	}
	row, ok = mcpJournalRow(t, f, opID)
	if !ok || row.State != model.EvidenceOpCompleted {
		t.Fatalf("under-stop tasks/cancel journal row = %+v ok=%t, want settled completed", row, ok)
	}
	verifyMCPLedger(t, f)
}

// TestMCPTaskUpdateAckOnlyNeverConfirmsStatus is review round-3 R3-01 over
// the REAL production forwarder: SEP-2663 defines UpdateTaskResult as an empty,
// eventually-consistent ACKNOWLEDGEMENT and directs clients to observe status
// through tasks/get or task notifications. mcpUpstreamForwarder validates the
// JSON-RPC ENVELOPE, not the method-specific result shape, so a broken or hostile
// upstream can answer a tasks/update with a strictly valid, correlated success
// whose body claims `{"resultType":"complete","status":"canceled"}`. Round-2 fed
// that body to the authoritative confirmation path (confirmStatus), which cleared
// TerminalUnconfirmed and removed a live task from active() and from every later
// kill-switch sweep — with no authoritative read and no cancellation anywhere.
//
// The update result may now neither mutate the governance view nor be relayed:
// handing the client an authoritative-looking status the gateway deliberately
// ignores is the same two-consumer differential this surface refuses elsewhere.
func TestMCPTaskUpdateAckOnlyNeverConfirmsStatus(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	var updates, cancels int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch body.Method {
		case "tasks/update":
			atomic.AddInt32(&updates, 1)
			// A strictly valid JSON-RPC SUCCESS whose result lies about task state.
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","taskId":"task-ackonly","status":"cancelled"}}`))
		case "tasks/cancel":
			atomic.AddInt32(&cancels, 1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"task","taskId":"task-ackonly","status":"working"}}`))
		}
	}))
	defer up.Close()
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	if w := postMCP(t, rs, token, mcpTaskToolsCallBody("ackonly-create-1")); w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
	}

	w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/update","params":{"taskId":"task-ackonly","inputResponses":{},"_meta":{"ai.olivares/operationId":"ackonly-update-1"}}}`)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status-bearing tasks/update = %d, want 502 (the ack-only shape is refused); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "cancelled") {
		t.Errorf("the unauthoritative status reached the client: %s", w.Body.String())
	}
	if got := atomic.LoadInt32(&updates); got != 1 {
		t.Errorf("upstream tasks/update forwards = %d, want 1", got)
	}

	// THE invariant: the task is still live for the kill switch. Round-2 the
	// update result had already confirmed a terminal `canceled` status, so this
	// sweep found nothing to cancel.
	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch after an ack-only violation")
	if canceled != 1 || err != nil {
		t.Errorf("sweep after the status-bearing update = %d, %v; want 1, nil "+
			"(only tasks/get may retire a task — an update result never can)", canceled, err)
	}
	if got := atomic.LoadInt32(&cancels); got != 1 {
		t.Errorf("upstream cancels = %d, want 1", got)
	}
	// The update operation itself settled honestly: the round trip DID complete.
	claims, settles, opID := countMCPActionEvents(t, f, "mcp.task.update.keyed")
	if claims != 1 || settles != 1 {
		t.Fatalf("update events: claims=%d settles=%d, want exactly 1/1", claims, settles)
	}
	row, ok := mcpJournalRow(t, f, opID)
	if !ok || row.State != model.EvidenceOpCompleted {
		t.Errorf("update journal row = %+v ok=%t, want settled completed", row, ok)
	}
	verifyMCPLedger(t, f)
}

// TestMCPTaskUpdateAckRequiresTheNormativeDiscriminator is review ROUND-4
// R4-02 over the REAL production forwarder. Round-3 read SEP-2663's "empty
// acknowledgement" as "an empty object" and made `resultType` FORBIDDEN — so a
// CONFORMANT upstream, which must answer `{"resultType":"complete"}` because the
// extension defines `UpdateTaskResult = Result` and mandates the discriminator on
// it, was answered 502 while the non-conformant `{}` was accepted. All three
// fakes encoded `{}`, so the green suite entrenched the interoperability failure.
//
// Both directions are pinned here: the normative success relays, and the bare
// object the round-3 code blessed is refused. Neither shape may ever mutate the
// governance view (only tasks/get may), so the task stays live for the sweep.
func TestMCPTaskUpdateAckRequiresTheNormativeDiscriminator(t *testing.T) {
	for name, tc := range map[string]struct {
		result string
		want   int
	}{
		"normative UpdateTaskResult": {`{"resultType":"complete"}`, http.StatusOK},
		"round-3 bare object":        {`{}`, http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMCPLedgerFixture(t)
			eng := &engine{store: f.store, log: discardLogger()}
			var updates, cancels int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				switch body.Method {
				case "tasks/update":
					atomic.AddInt32(&updates, 1)
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + tc.result + `}`))
				case "tasks/cancel":
					atomic.AddInt32(&cancels, 1)
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
				default:
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"task","taskId":"task-ackshape","status":"working"}}`))
				}
			}))
			defer up.Close()
			token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
			rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

			if w := postMCP(t, rs, token, mcpTaskToolsCallBody("ackshape-create-1")); w.Code != http.StatusOK {
				t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
			}
			w := postMCP(t, rs, token,
				`{"jsonrpc":"2.0","id":1,"method":"tasks/update","params":{"taskId":"task-ackshape","inputResponses":{},"_meta":{"ai.olivares/operationId":"ackshape-update-1"}}}`)
			if w.Code != tc.want {
				t.Errorf("tasks/update over the real forwarder = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			if got := atomic.LoadInt32(&updates); got != 1 {
				t.Errorf("upstream tasks/update forwards = %d, want 1", got)
			}
			// The update never mutates the governance view in either direction.
			canceled, cerr := rs.CancelActiveTasks(context.Background(), nil, "kill-switch after the ack")
			if canceled != 1 || cerr != nil {
				t.Errorf("sweep after the update = %d, %v; want 1, nil (only tasks/get may retire a task)", canceled, cerr)
			}
			if got := atomic.LoadInt32(&cancels); got != 1 {
				t.Errorf("upstream cancels = %d, want 1", got)
			}
			// The update operation settled honestly: the round trip DID complete.
			claims, settles, opID := countMCPActionEvents(t, f, "mcp.task.update.keyed")
			if claims != 1 || settles != 1 {
				t.Fatalf("update events: claims=%d settles=%d, want exactly 1/1", claims, settles)
			}
			if row, ok := mcpJournalRow(t, f, opID); !ok || row.State != model.EvidenceOpCompleted {
				t.Errorf("update journal row = %+v ok=%t, want settled completed", row, ok)
			}
			verifyMCPLedger(t, f)
		})
	}
}

// TestMCPTaskReconcileFullPathJournalsOperatorActions drives the round-4
// OPERATOR RECONCILIATION SURFACE end to end through ServeHTTP against the REAL
// durable journal: a cooperatively canceled task is retained
// cancellation-unconfirmed, appears in the inventory, is confirmed terminal by
// the authoritative read and is then retired — with one claim + one settle event
// per MUTATING action on a verified signed chain. Round-3 had no such surface at
// all: `reconciliationRecords` and `clearCancelBar` had no production caller, so
// a retained record could never leave the ledger.
func TestMCPTaskReconcileFullPathJournalsOperatorActions(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	var gets int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch body.Method {
		case "tasks/get":
			atomic.AddInt32(&gets, 1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` +
				conformingGetTaskResultJSON("task-recon-e2e", "cancelled") + `}`))
		case "tasks/cancel":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"task","taskId":"task-recon-e2e","status":"working"}}`))
		}
	}))
	defer up.Close()
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read tasks:reconcile")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	if w := postMCP(t, rs, token, mcpTaskToolsCallBody("recon-create-1")); w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
	}
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/cancel","params":{"taskId":"task-recon-e2e","_meta":{"ai.olivares/operationId":"recon-cancel-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("tasks/cancel status = %d; body=%s", w.Code, w.Body.String())
	}

	// (i) the inventory reports the retained record with the exact generation and
	// owner digest the mutating actions require.
	w := postMCP(t, rs, token, `{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/list","params":{}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation list status = %d; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Result struct {
			Records []struct {
				TaskID      string `json:"taskId"`
				Generation  string `json:"generation"`
				OwnerDigest string `json:"ownerDigest"`
				Retirable   bool   `json:"retirable"`
			} `json:"records"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode inventory: %v; body=%s", err, w.Body.String())
	}
	if len(env.Result.Records) != 1 || env.Result.Records[0].TaskID != "task-recon-e2e" {
		t.Fatalf("reconciliation inventory = %+v, want the retained task", env.Result.Records)
	}
	row := env.Result.Records[0]
	if row.Retirable {
		t.Error("a record with no CONFIRMED terminal status must not be reported as retirable")
	}
	target := `"taskId":"task-recon-e2e","generation":"` + row.Generation + `","ownerDigest":"` + row.OwnerDigest + `"`

	// (ii) the authoritative read confirms the terminal status.
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/status","params":{`+target+`,"_meta":{"ai.olivares/operationId":"recon-status-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d; body=%s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&gets); got != 1 {
		t.Errorf("upstream tasks/get forwards = %d, want 1", got)
	}
	// ROUND-7: proving the task terminal is only HALF the retirement precondition
	// for a client-readable record. Retiring now would destroy a final tool result
	// its owner has never received, through the gateway that is the owner's only
	// route to it (R6-02) — so the drain is refused, and the row stays visible,
	// counted and explicitly `terminal-awaiting-owner`.
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/retire","params":{`+target+`,"_meta":{"ai.olivares/operationId":"recon-retire-early"}}}`); w.Code != http.StatusConflict {
		t.Fatalf("retire before the owner collected = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	uncollected := postMCP(t, rs, token, `{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/list","params":{}}`)
	var pending struct {
		Result struct {
			Records []struct {
				TaskID         string `json:"taskId"`
				Class          string `json:"class"`
				Actionable     bool   `json:"actionable"`
				Retirable      bool   `json:"retirable"`
				OwnerCollected bool   `json:"ownerCollected"`
			} `json:"records"`
			Retained int `json:"retained"`
		} `json:"result"`
	}
	if err := json.Unmarshal(uncollected.Body.Bytes(), &pending); err != nil {
		t.Fatalf("decode inventory before collection: %v; body=%s", err, uncollected.Body.String())
	}
	if len(pending.Result.Records) != 1 || pending.Result.Retained != 1 {
		t.Fatalf("inventory before collection = %+v retained=%d, want the single counted row",
			pending.Result.Records, pending.Result.Retained)
	}
	if r := pending.Result.Records[0]; r.Class != "terminal-awaiting-owner" || !r.Actionable ||
		r.Retirable || r.OwnerCollected {
		t.Errorf("uncollected row = %+v, want the actionable terminal-awaiting-owner class with retirable=false", r)
	}
	// (iv) the OWNER collects its result, and only then may the record be retired.
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"task-recon-e2e","_meta":{"ai.olivares/operationId":"recon-collect-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("the owner's collecting read = %d; body=%s", w.Code, w.Body.String())
	}
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/retire","params":{`+target+`,"_meta":{"ai.olivares/operationId":"recon-retire-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("reconciliation retire = %d; body=%s", w.Code, w.Body.String())
	}
	// The drain is then COMPLETE: no row, no retained count — and the owner already
	// holds its final tool result, which is what makes the deletion safe.
	w = postMCP(t, rs, token, `{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/list","params":{}}`)
	var drained struct {
		Result struct {
			Records []struct {
				TaskID     string `json:"taskId"`
				Class      string `json:"class"`
				Actionable bool   `json:"actionable"`
			} `json:"records"`
			Retained int `json:"retained"`
			Total    int `json:"total"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &drained); err != nil {
		t.Fatalf("decode inventory after retirement: %v; body=%s", err, w.Body.String())
	}
	if drained.Result.Retained != 0 || drained.Result.Total != 0 || len(drained.Result.Records) != 0 {
		t.Errorf("inventory after the retirement = %+v retained=%d total=%d, want empty; body=%s",
			drained.Result.Records, drained.Result.Retained, drained.Result.Total, w.Body.String())
	}

	// Every MUTATING reconciliation ATTEMPT is claimed and settled exactly once —
	// including the one that was REFUSED. ROUND-7: this test now performs two
	// retirements (the refused pre-collection attempt and the accepted one), and
	// asserting 2/2 rather than dropping the refusal is deliberate: a refused
	// reconciliation must leave a durable, honest record that it stopped before its
	// effect, not silently disappear.
	for _, expect := range []struct {
		action   string
		attempts int
	}{
		{"mcp.task.reconcile.status.keyed", 1},
		{"mcp.task.reconcile.retire.keyed", 2},
	} {
		claims, settles, opID := countMCPActionEvents(t, f, expect.action)
		if claims != expect.attempts || settles != expect.attempts {
			t.Errorf("%s events: claims=%d settles=%d, want exactly %d/%d",
				expect.action, claims, settles, expect.attempts, expect.attempts)
			continue
		}
		// countMCPActionEvents reports the LAST claim's operation id: the attempt that
		// was allowed to complete.
		if r, ok := mcpJournalRow(t, f, opID); !ok || r.State != model.EvidenceOpCompleted {
			t.Errorf("%s journal row %s = %+v ok=%t, want settled completed", expect.action, opID, r, ok)
		}
	}
	verifyMCPLedger(t, f)
}

// TestMCPTaskReconcileRequestIsConformingOnTheRealTransport is review
// ROUND-5 R5-05 over the REAL production forwarder, against a STRICT upstream
// that answers like a conforming RC Tasks server: it REFUSES (-32021 / -32020)
// any tasks/* request that does not declare the tasks extension in its
// per-request client capabilities, or that arrives without the MCP routing
// mirrors (Mcp-Method, Mcp-Name = params.taskId, MCP-Protocol-Version).
//
// Round-4 synthesized a bare `{"taskId":...}` and the forwarder emitted content,
// credential, trace and evidence headers but NO MCP routing headers — so this
// upstream would have refused the read, the terminal status could never be
// confirmed, and the retained record could never be drained. The test therefore
// fails RED on the round-4 code by construction: the strict upstream answers an
// error and the reconciliation cannot complete.
func TestMCPTaskReconcileRequestIsConformingOnTheRealTransport(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	var missing []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Method string `json:"method"`
			Params struct {
				TaskID string `json:"taskId"`
				Meta   struct {
					Version      string `json:"io.modelcontextprotocol/protocolVersion"`
					Capabilities struct {
						Extensions map[string]json.RawMessage `json:"extensions"`
					} `json:"io.modelcontextprotocol/clientCapabilities"`
				} `json:"_meta"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(body.Method, "tasks/") {
			// The extension capability declaration a conforming server REQUIRES.
			if _, ok := body.Params.Meta.Capabilities.Extensions["io.modelcontextprotocol/tasks"]; !ok {
				missing = append(missing, body.Method+": client capability declaration")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32021,"message":"extension not declared on the request"}}`))
				return
			}
			// The RC Streamable HTTP routing mirrors.
			for header, want := range map[string]string{
				"Mcp-Method":           body.Method,
				"Mcp-Name":             body.Params.TaskID,
				"MCP-Protocol-Version": body.Params.Meta.Version,
			} {
				if got := r.Header.Get(header); got == "" || got != want {
					missing = append(missing, body.Method+": "+header+" = "+got+", want "+want)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"header mismatch"}}`))
					return
				}
			}
		}
		switch body.Method {
		case "tasks/get":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` +
				conformingGetTaskResultJSON("task-conform-e2e", "cancelled") + `}`))
		case "tasks/cancel":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"task","taskId":"task-conform-e2e","status":"working"}}`))
		}
	}))
	defer up.Close()
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read tasks:reconcile")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	if w := postMCP(t, rs, token, mcpTaskToolsCallBody("conform-create-1")); w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
	}
	// A CONFORMING RC client declares the extension and its protocol version on its
	// own tasks/* request; the gateway forwards those params verbatim, and the
	// forwarder mirrors them into the routing headers. (What round-5 R5-05 is about
	// is the request the GATEWAY synthesizes for reconciliation, below, where there
	// is no client to supply them.)
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/cancel","params":{"taskId":"task-conform-e2e","_meta":{`+
			`"ai.olivares/operationId":"conform-cancel-1",`+
			`"io.modelcontextprotocol/protocolVersion":"2026-07-28",`+
			`"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`); w.Code != http.StatusOK {
		t.Fatalf("tasks/cancel status = %d; body=%s", w.Code, w.Body.String())
	}
	w := postMCP(t, rs, token, `{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/list","params":{}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation list status = %d; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Result struct {
			Instance string `json:"instance"`
			Records  []struct {
				TaskID      string `json:"taskId"`
				Generation  string `json:"generation"`
				OwnerDigest string `json:"ownerDigest"`
			} `json:"records"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode inventory: %v; body=%s", err, w.Body.String())
	}
	if env.Result.Instance == "" {
		t.Error("the inventory must name the gateway instance whose process-local ledger it came from")
	}
	if len(env.Result.Records) != 1 {
		t.Fatalf("reconciliation inventory = %+v, want the retained task", env.Result.Records)
	}
	row := env.Result.Records[0]
	target := `"taskId":"task-conform-e2e","generation":"` + row.Generation + `","ownerDigest":"` + row.OwnerDigest + `"`

	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/status","params":{`+target+`,"_meta":{"ai.olivares/operationId":"conform-status-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("R5-05: the reconciliation read was REFUSED by a conforming upstream = %d; body=%s; upstream faults=%v",
			w.Code, w.Body.String(), missing)
	}
	if len(missing) != 0 {
		t.Fatalf("R5-05: the strict upstream rejected %d request(s): %v", len(missing), missing)
	}
	// The record is now provably terminal. ROUND-7: draining a CLIENT-READABLE
	// record additionally requires proof that its OWNER received the terminal
	// result, so the owner's own tasks/get runs first — and it too must be a
	// CONFORMING Tasks-extension request on the real transport, which is exactly
	// what this test exists to prove.
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"task-conform-e2e","_meta":{`+
			`"ai.olivares/operationId":"conform-collect-1",`+
			`"io.modelcontextprotocol/protocolVersion":"2026-07-28",`+
			`"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`); w.Code != http.StatusOK {
		t.Fatalf("the owner's collecting read = %d; body=%s; upstream faults=%v", w.Code, w.Body.String(), missing)
	}
	if len(missing) != 0 {
		t.Fatalf("R5-05: the strict upstream rejected %d request(s) on the owner's collecting read: %v", len(missing), missing)
	}
	if w := postMCP(t, rs, token,
		`{"jsonrpc":"2.0","id":1,"method":"tasks/reconcile/retire","params":{`+target+`,"_meta":{"ai.olivares/operationId":"conform-retire-1"}}}`); w.Code != http.StatusOK {
		t.Fatalf("reconciliation retire = %d; body=%s", w.Code, w.Body.String())
	}
	// The reconciliation response carries the governance projection ONLY: no raw
	// upstream body (round-5 R5-04).
	if strings.Contains(w.Body.String(), "upstreamResult\":{") {
		t.Errorf("R5-04: a raw upstream body reached the operator: %s", w.Body.String())
	}
	verifyMCPLedger(t, f)
}
