// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/orchestration"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fakeEngine records Apply calls so the runtime fire route can be exercised without a
// real backend (the executor itself is tested in core/runtime/executor).
type fakeEngine struct {
	calls int
	last  executor.Desired
	res   executor.Result
	err   error
}

func (f *fakeEngine) Apply(_ context.Context, d executor.Desired) (executor.Result, error) {
	f.calls++
	f.last = d
	return f.res, f.err
}

// fakeEmitter records A2A emissions for the A2A fire route.
type fakeEmitter struct {
	calls int
	last  a2a.SendSpec
	res   a2a.TaskResult
	err   error
}

func (f *fakeEmitter) SendMessageCapable(_ context.Context, s a2a.SendSpec) (a2a.TaskResult, error) {
	f.calls++
	f.last = s
	return f.res, f.err
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fireReq(kind, ref string) orchestration.FireRequest {
	return orchestration.FireRequest{Tenant: model.TenantID("tenant-1"), SubjectKind: kind, SubjectRef: ref, ScheduleRef: "sched-1", PlanHash: "ph-abc"}
}

func TestOrchDispatch_RuntimeRouteInvokesEngineOnce(t *testing.T) {
	eng := &fakeEngine{res: executor.Result{BackendID: "docker", Detail: "applied"}}
	d := &orchestrationDispatcher{
		exec: eng,
		runtimes: map[string]runtimeTarget{
			subjectKey("agent", "nightly"): {runtime: "docker", target: "docker.host/n1", environment: "prod", image: "ghcr.io/acme/x:1", replicas: 1},
		},
		log: discardLogger(),
	}
	res, err := d.Fire(context.Background(), fireReq("agent", "nightly"))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if eng.calls != 1 {
		t.Fatalf("expected exactly 1 Apply, got %d", eng.calls)
	}
	if eng.last.Runtime != "docker" || eng.last.Image != "ghcr.io/acme/x:1" || eng.last.SubjectRef != "nightly" || eng.last.Tenant != "tenant-1" {
		t.Fatalf("Desired not built from target+request: %+v", eng.last)
	}
	if res.Ref != "runtime:docker:nightly" {
		t.Fatalf("unexpected ref %q", res.Ref)
	}
}

func TestOrchDispatch_RuntimeRouteFailsClosedWithoutEngine(t *testing.T) {
	d := &orchestrationDispatcher{
		runtimes: map[string]runtimeTarget{subjectKey("agent", "nightly"): {runtime: "docker"}},
		log:      discardLogger(),
	}
	_, err := d.Fire(context.Background(), fireReq("agent", "nightly"))
	if err == nil || !strings.Contains(err.Error(), "no executor engine") {
		t.Fatalf("expected fail-closed without engine, got %v", err)
	}
}

func TestOrchDispatch_A2ARouteEmits(t *testing.T) {
	em := &fakeEmitter{res: a2a.TaskResult{TaskID: "t1", State: a2a.TaskStateSubmitted}}
	d := &orchestrationDispatcher{
		agents: map[string]a2aTarget{
			subjectKey("agent", "partner"): {name: "partner", url: "https://partner.example.com", skill: "summarize", text: "go", client: em},
		},
		log: discardLogger(),
	}
	res, err := d.Fire(context.Background(), fireReq("agent", "partner"))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if em.calls != 1 || em.last.AgentURL != "https://partner.example.com" || em.last.Skill != "summarize" || em.last.Text != "go" {
		t.Fatalf("emit not called with target spec: calls=%d last=%+v", em.calls, em.last)
	}
	if res.Ref != "a2a:t1:TASK_STATE_SUBMITTED" {
		t.Fatalf("unexpected ref %q", res.Ref)
	}
}

// fakeObsSink captures the observability (edge + finding) a governed A2A fire publishes.
type fakeObsSink struct {
	edges    []sdkmodel.EdgeObservation
	findings []sdkmodel.FindingReport
}

func (f *fakeObsSink) PublishEdge(_ context.Context, _ string, e sdkmodel.EdgeObservation) error {
	f.edges = append(f.edges, e)
	return nil
}

func (f *fakeObsSink) PublishFinding(_ context.Context, _ string, fr sdkmodel.FindingReport) error {
	f.findings = append(f.findings, fr)
	return nil
}

// TestOrchDispatch_A2AFireIsObservable: a successful governed A2A fire publishes the
// delegation to module IV's comm graph (an attributed a2a edge orchestrator→agent, skill
// as tool_ref) and the SOC feed (an a2a_delegation finding) — "who delegated what to whom"
// is visible, never silent. Emission is fail-open, so it is layered on AFTER the dispatch.
func TestOrchDispatch_A2AFireIsObservable(t *testing.T) {
	em := &fakeEmitter{res: a2a.TaskResult{TaskID: "t1", State: a2a.TaskStateSubmitted}}
	obs := &fakeObsSink{}
	d := &orchestrationDispatcher{
		agents: map[string]a2aTarget{
			subjectKey("agent", "partner"): {name: "partner", url: "https://partner.example.com", skill: "summarize", text: "go", client: em},
		},
		log: discardLogger(),
	}
	d.bindObservationSink(obs)
	if _, err := d.Fire(context.Background(), fireReq("agent", "partner")); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if len(obs.edges) != 1 {
		t.Fatalf("want one delegation edge, got %d", len(obs.edges))
	}
	e := obs.edges[0]
	if e.OriginRef != defaultOrchestratorRef || e.ResourceKind != "a2a.agent" || e.ResourceRef != "partner" || e.ToolRef != "summarize" {
		t.Errorf("delegation edge endpoints wrong: %+v", e)
	}
	if e.Source != sdkmodel.SignalA2A || e.Confidence != sdkmodel.ConfidenceAttributed {
		t.Errorf("a governed delegation must be an attributed a2a edge: %+v", e)
	}
	if len(obs.findings) != 1 || obs.findings[0].Kind != "a2a_delegation" {
		t.Fatalf("want one a2a_delegation finding, got %+v", obs.findings)
	}
}

func TestOrchDispatch_A2AErrorIsFailureNotFakeRef(t *testing.T) {
	em := &fakeEmitter{err: context.DeadlineExceeded}
	d := &orchestrationDispatcher{
		agents: map[string]a2aTarget{subjectKey("agent", "partner"): {name: "partner", url: "https://p", client: em}},
		log:    discardLogger(),
	}
	res, err := d.Fire(context.Background(), fireReq("agent", "partner"))
	if err == nil {
		t.Fatal("expected an emit error to surface")
	}
	if res.Ref != "" {
		t.Fatalf("must not return a fake ref on failure, got %q", res.Ref)
	}
}

func TestOrchDispatch_NoRouteIsExplicitError(t *testing.T) {
	d := &orchestrationDispatcher{
		runtimes: map[string]runtimeTarget{subjectKey("agent", "known"): {runtime: "docker"}},
		exec:     &fakeEngine{},
		log:      discardLogger(),
	}
	res, err := d.Fire(context.Background(), fireReq("agent", "unknown"))
	if err == nil || !strings.Contains(err.Error(), "no actuation route") {
		t.Fatalf("expected explicit no-route error, got %v", err)
	}
	if res.Ref != "" {
		t.Fatalf("must not return a ref for an unrouted subject, got %q", res.Ref)
	}
}

func TestOrchDispatch_EmptyPlanHashRefused(t *testing.T) {
	eng := &fakeEngine{}
	d := &orchestrationDispatcher{exec: eng, runtimes: map[string]runtimeTarget{subjectKey("agent", "x"): {runtime: "docker"}}, log: discardLogger()}
	req := fireReq("agent", "x")
	req.PlanHash = ""
	_, err := d.Fire(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "plan_hash") {
		t.Fatalf("expected plan_hash defense, got %v", err)
	}
	if eng.calls != 0 {
		t.Fatal("must not actuate when plan_hash is empty")
	}
}

func TestNewOrchestrationDispatcher_DenyClosedWhenUnconfigured(t *testing.T) {
	if d := newOrchestrationDispatcher(orchDispatchConfig{}, nil, discardLogger()); d != nil {
		t.Fatal("unconfigured dispatcher must be nil (module keeps deny-closed unwiredDispatcher)")
	}
}

// End-to-end deny-closed through the dispatcher with the REAL a2a connector: an agent
// with no trust anchor (and an offline card) must never emit a Task.
func TestOrchDispatch_A2AEndToEndDenyClosedNoAnchor(t *testing.T) {
	doer := &orchStubDoer{cardBytes: []byte(`{"name":"x","url":"https://x.example.com"}`)} // unsigned card
	client := a2a.NewClient(a2a.EmitConfig{Doer: doer})                                    // no TrustJWKS => nothing can verify
	d := &orchestrationDispatcher{
		agents: map[string]a2aTarget{subjectKey("agent", "x"): {name: "x", url: "https://x.example.com", client: client}},
		log:    discardLogger(),
	}
	_, err := d.Fire(context.Background(), fireReq("agent", "x"))
	if err == nil {
		t.Fatal("expected deny-closed (unverified card) end-to-end")
	}
	if doer.postCount != 0 {
		t.Fatalf("DENY-CLOSED VIOLATION: emitted %d Task(s) to an unverified agent", doer.postCount)
	}
}

// orchStubDoer is a minimal offline a2a.Transport for the end-to-end deny-closed test.
type orchStubDoer struct {
	cardBytes []byte
	postCount int
}

func (s *orchStubDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		s.postCount++
	}
	body := s.cardBytes
	if req.Method == http.MethodPost {
		body = []byte(`{"jsonrpc":"2.0","id":"1","result":{"id":"t","status":{"state":"TASK_STATE_WORKING"}}}`)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
}
