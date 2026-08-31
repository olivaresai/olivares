// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/sdk"
)

// orchworkflow_e2e_test.go — the governed DAG-workflow run, end to end across
// the REAL module boundary: the workflow engine's approvals travel through the
// real approval bridge into the real governance module, a real second
// human decides through the governed decision API, and only then does the run
// start / the mid-graph approval-gate step release. The engine's internal
// semantics are pinned in modules/orchestration; THIS test pins the seam.
//
// The bridge cannot be wired at buildModules time in-process (its service
// token only exists after bootstrap — the two-boot production reality), so the
// test follows the approvalbridge E2E convention: build the bridge against the
// running engine post-bootstrap, then stand a SECOND api server over the SAME
// store serving a fresh orchestration module wired to that real gate. Same
// tenants, same auth, same governance — only the gate wiring differs from the
// primary handler's deny-closed default.

func TestWorkflowGovernedRunEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, approverToken := h.createApprover(t, "approver-wf@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	bridge := buildBridge(t, h, svc)

	// A fresh orchestration module on the SHARED store, wired to the REAL gate.
	wfMod := orchestration.New(orchestration.WithApprovalGate(bridge.orchestrationGate()))
	bus := eventbus.NewInProc(eventbus.Options{})
	rt2 := runtime.New(runtime.Options{Bus: bus})
	if err := rt2.AddModule(wfMod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}
	if err := rt2.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = rt2.Stop(context.Background()); _ = bus.Close() })
	wfMod.UseData(api.NewModuleData(h.st))

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	setupTok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	if _, _, err := setupTok.Ensure(); err != nil {
		t.Fatalf("setup token: %v", err)
	}
	authz := auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants()))
	srv, err := api.New(api.Options{
		Store: h.st, Authenticator: h.authr, Authorizer: authz, Signer: signer,
		SetupToken: setupTok, Version: "e2e-wf", Modules: []api.Module{wfMod},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rdr *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = strings.NewReader(string(b))
		} else {
			rdr = strings.NewReader("")
		}
		r := httptest.NewRequest(method, path, rdr)
		r.RemoteAddr = "10.0.0.9:9999"
		r.Header.Set("Authorization", "Bearer "+h.adminToken)
		r.Header.Set("X-Olivares-Tenant", h.tenantA)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, r)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}

	// The pack's canonical graph: announce → HITL hold → finish.
	code, wf := call("POST", "/v1/m/orchestration/workflows", map[string]any{
		"name": "release-train",
		"steps": []map[string]any{
			{"ref": "announce", "kind": "eventing-emit", "config": map[string]any{"label": "starting"}, "depends_on": []string{}},
			{"ref": "hold", "kind": "approval-gate", "config": map[string]any{"reason": "release window"}, "depends_on": []string{"announce"}},
			{"ref": "finish", "kind": "eventing-emit", "config": map[string]any{"label": "done"}, "depends_on": []string{"hold"}},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create workflow = %d %v", code, wf)
	}
	wfID := wf["id"].(string)

	// Dry-run first: the plan a human reviews, zero effects.
	code, plan := call("POST", "/v1/m/orchestration/workflows/"+wfID+"/dry-run", nil)
	if code != http.StatusOK {
		t.Fatalf("dry-run = %d %v", code, plan)
	}
	if steps := plan["steps"].([]any); len(steps) != 3 ||
		steps[0].(map[string]any)["ref"] != "announce" || steps[2].(map[string]any)["ref"] != "finish" {
		t.Fatalf("dry-run plan = %v", plan["steps"])
	}

	// Phase 1: a REAL pending approval opens in governance through the bridge.
	code, p1 := call("POST", "/v1/m/orchestration/workflows/"+wfID+"/run", nil)
	if code != http.StatusAccepted {
		t.Fatalf("phase 1 = %d %v", code, p1)
	}
	runRef := p1["approval_ref"].(string)
	if p1["gate_status"].(string) != "pending" || runRef == "" {
		t.Fatalf("phase 1 gate = %v ref=%q, want a pending referenced approval", p1["gate_status"], runRef)
	}

	// Before any human decides, phase 2 must DENY (pending is a deny).
	if code, body := call("POST", "/v1/m/orchestration/workflows/"+wfID+"/run",
		map[string]any{"approval_ref": runRef}); code != http.StatusForbidden {
		t.Fatalf("undecided phase 2 = %d %v, want 403", code, body)
	}

	// A real human approves through the governed decision API.
	if code, body := h.decide(t, approverToken, runRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve run = %d: %s", code, body)
	}

	// Phase 2: the run starts; announce emits; the hold step opens ITS OWN
	// approval and pauses; finish stays pending behind it.
	code, p2 := call("POST", "/v1/m/orchestration/workflows/"+wfID+"/run",
		map[string]any{"approval_ref": runRef})
	if code != http.StatusOK {
		t.Fatalf("phase 2 = %d %v", code, p2)
	}
	run := p2["run"].(map[string]any)
	runID := run["id"].(string)
	if run["status"].(string) != "running" {
		t.Fatalf("run status = %v, want running (paused on the hold gate)", run["status"])
	}
	var holdRef string
	for _, it := range run["steps"].([]any) {
		s := it.(map[string]any)
		switch s["ref"].(string) {
		case "announce":
			if s["status"].(string) != "emitted" {
				t.Fatalf("announce = %v", s["status"])
			}
		case "hold":
			if s["status"].(string) != "waiting_approval" {
				t.Fatalf("hold = %v", s["status"])
			}
			holdRef, _ = s["approval_ref"].(string)
		case "finish":
			if s["status"].(string) != "pending" {
				t.Fatalf("finish = %v", s["status"])
			}
		}
	}
	if holdRef == "" || holdRef == runRef {
		t.Fatalf("hold approval ref = %q (run ref %q), want a DISTINCT approval", holdRef, runRef)
	}

	// The same human resolves the mid-graph gate; the pump seam advances.
	if code, body := h.decide(t, approverToken, holdRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve hold = %d: %s", code, body)
	}
	wfMod.AdvanceWorkflowRuns(ctx, api.ModuleContext{
		Tenant: model.TenantID(h.tenantA), Data: api.NewScopedData(h.st, model.TenantID(h.tenantA)),
	})

	code, got := call("GET", "/v1/m/orchestration/workflows/"+wfID+"/runs/"+runID, nil)
	if code != http.StatusOK {
		t.Fatalf("get run = %d", code)
	}
	if got["status"].(string) != "completed" {
		t.Fatalf("final run status = %v %v, want completed", got["status"], got["steps"])
	}

	// The append-only ledger tells the WHOLE story: request, start, three step
	// rows plus the hold gate's own request row, and the completed end.
	code, dec := call("GET", "/v1/m/orchestration/decisions?limit=100", nil)
	if code != http.StatusOK {
		t.Fatalf("decisions = %d", code)
	}
	byOp := map[string]int{}
	byOpStatus := map[string]int{}
	gatePassed := false
	for _, it := range dec["items"].([]any) {
		m := it.(map[string]any)
		if m["subject_kind"].(string) != "workflow" {
			continue
		}
		op, opStatus := m["op"].(string), m["op_status"].(string)
		byOp[op]++
		byOpStatus[op+"/"+opStatus]++
		if op == "run_step" && opStatus == "gate_passed" {
			gatePassed = true
		}
	}
	// run_request ×1, then TWO run rows — the undecided attempt was DENIED and
	// that denial is evidence too (deny-by-default is recorded, not silent) —
	// then the started run, four step rows (announce, the hold gate's request,
	// its gate_passed release, finish) and the completed end.
	if byOp["run_request"] != 1 || byOp["run"] != 2 || byOp["run_step"] != 4 || byOp["run_end"] != 1 || !gatePassed {
		t.Fatalf("ledger ops = %v (gate_passed=%v)", byOp, gatePassed)
	}
	if byOpStatus["run/blocked"] != 1 || byOpStatus["run/dispatched"] != 1 {
		t.Fatalf("the denied attempt and the started run must BOTH be evidenced: %v", byOpStatus)
	}
	if byOpStatus["run_end/completed"] != 1 {
		t.Fatalf("terminal evidence = %v, want one completed run_end", byOpStatus)
	}
}

// The chi-loopback defect broke a PRE-EXISTING, shipped capability too: the
// governed schedule fire opens its approval through the same bridge from inside
// its own chi-served handler, so phase 1 answered 502 "approval gate
// unavailable" for every fire. This drives that exact path over the real HTTP
// surface with the real gate, so the regression is pinned where an operator
// would feel it — not only on the workflow route that first surfaced it.
func TestScheduleFireOpensApprovalThroughLoopback(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, approverToken := h.createApprover(t, "approver-fire@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	bridge := buildBridge(t, h, svc)

	schedMod := orchestration.New(orchestration.WithApprovalGate(bridge.orchestrationGate()))
	bus := eventbus.NewInProc(eventbus.Options{})
	rt2 := runtime.New(runtime.Options{Bus: bus})
	if err := rt2.AddModule(schedMod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}
	if err := rt2.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = rt2.Stop(context.Background()); _ = bus.Close() })
	schedMod.UseData(api.NewModuleData(h.st))

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	setupTok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	if _, _, err := setupTok.Ensure(); err != nil {
		t.Fatalf("setup token: %v", err)
	}
	authz := auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants()))
	srv, err := api.New(api.Options{
		Store: h.st, Authenticator: h.authr, Authorizer: authz, Signer: signer,
		SetupToken: setupTok, Version: "e2e-fire", Modules: []api.Module{schedMod},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rdr *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = strings.NewReader(string(b))
		} else {
			rdr = strings.NewReader("")
		}
		r := httptest.NewRequest(method, path, rdr)
		r.RemoteAddr = "10.0.0.9:9999"
		r.Header.Set("Authorization", "Bearer "+h.adminToken)
		r.Header.Set("X-Olivares-Tenant", h.tenantA)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, r)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}

	code, sched := call("POST", "/v1/m/orchestration/schedules", map[string]any{
		"name": "nightly-sweep", "subject_kind": "agent", "subject_ref": "agent-1",
		"trigger_kind": "manual",
	})
	if code != http.StatusCreated {
		t.Fatalf("create schedule = %d %v", code, sched)
	}
	schedID := sched["id"].(string)

	// Phase 1 over the REAL handler: this is the call that answered 502.
	code, p1 := call("POST", "/v1/m/orchestration/schedules/"+schedID+"/fire", nil)
	if code != http.StatusAccepted {
		t.Fatalf("fire phase 1 = %d %v, want 202 (a 502 here is the loopback regression)", code, p1)
	}
	ref := p1["approval_ref"].(string)
	if p1["gate_status"].(string) != "pending" || ref == "" {
		t.Fatalf("fire phase 1 gate = %v ref=%q, want a pending referenced approval", p1["gate_status"], ref)
	}

	// And the decision round-trip works over the same loopback.
	if code, body := h.decide(t, approverToken, ref, "approve"); code != http.StatusOK {
		t.Fatalf("approve fire = %d: %s", code, body)
	}
	code, p2 := call("POST", "/v1/m/orchestration/schedules/"+schedID+"/fire",
		map[string]any{"approval_ref": ref})
	if code != http.StatusOK {
		t.Fatalf("fire phase 2 = %d %v", code, p2)
	}
	// No dispatcher is wired here, so the honest outcome is declared-not-fired —
	// never a pretended dispatch.
	if p2["op_status"].(string) != "declared_not_fired" {
		t.Fatalf("fire phase 2 op_status = %v, want declared_not_fired", p2["op_status"])
	}
}

// The loopback defect's widest blast radius was the flagship surface, not the
// new one: a privileged Claude Code session launch opens its governed approval
// through the same bridge from inside the sessions module's own chi-served
// handler, so EVERY bypassPermissions/dontAsk launch (and resume) was denied
// with "could not open a governed approval" on a credential-configured tenant.
// Deny-closed, so nothing ungoverned ever ran — but governed operation, the
// product's differentiator, could not run at all. This pins the path.
func TestPrivilegedSessionLaunchOpensApprovalThroughLoopback(t *testing.T) {
	h := newHarness(t)
	svc := h.mintBoundToken(t, auth.RoleEditor)
	bridge := buildBridge(t, h, svc)
	h.set.sessions.UseLaunchGate(&sessionLaunchGate{bridge: bridge, recordAvailable: true})

	code, body := h.req("POST", "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{
		"permission_mode": "bypassPermissions",
		"transport":       "stream-json",
	})
	// The launch is correctly DENIED — a privileged launch needs a human — but
	// it must be denied because an approval is PENDING, never because the
	// engine could not open one.
	if code != http.StatusForbidden {
		t.Fatalf("privileged launch = %d: %s", code, body)
	}
	msg := string(body)
	if strings.Contains(msg, "could not open a governed approval") {
		t.Fatalf("the launch gate could not reach governance — the loopback regression is back: %s", msg)
	}
	if !strings.Contains(msg, "requires human approval") {
		t.Fatalf("privileged launch denial = %s, want a pending-approval denial", msg)
	}
}
