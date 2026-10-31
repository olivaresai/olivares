// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPipelineCostCapStopsRun(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":1}}`)}
	waitFor(t, "cost cap stops process", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.State == stateStopped
	})
}

func TestPipelineRepositoryRefusal(t *testing.T) {
	for _, source := range []string{"file:///etc", "https://name:password@example.com/repo", "ext::sh -c true", "https://example.com/repo?token=secret", "http://example.com/repo"} {
		if err := validateRepositorySource(source, "main"); err == nil {
			t.Fatalf("accepted %q", source)
		}
	}
	if err := validateRepositorySource("https://example.com/repo", "main"); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCapSurvivesResume(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":1}}`)}
	waitFor(t, "resumed cost cap stops process", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.State == stateStopped })
}

func TestPipelineTimeoutUsesRuntimeStop(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	timer := &fakeTimer{}
	m.rt.afterFunc = func(d time.Duration, f func()) runTimer {
		timer.mu.Lock()
		timer.d, timer.fire = d, f
		timer.mu.Unlock()
		return timer
	}
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", TimeoutSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	timer.mu.Lock()
	d := timer.d
	timer.mu.Unlock()
	if d != 2*time.Second {
		t.Fatalf("deadline=%v", d)
	}
	timer.trigger()
	waitFor(t, "run deadline stops process", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.State == stateStopped
	})
}

func TestPipelineInvalidLimitsLaunchNothing(t *testing.T) {
	for _, p := range []CreateRunParams{{CostCapMicroUSD: -1}, {TimeoutSeconds: -1}, {TimeoutSeconds: 3000000}} {
		fr := &fakeRunner{}
		m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
		p.Actor, p.ActorKind = "user:u1", "user"
		if _, err := createProfiledTestRun(t, m, context.Background(), tenant, p); err == nil {
			t.Fatal("invalid limit accepted")
		}
		fr.mu.Lock()
		n := len(fr.procs)
		fr.mu.Unlock()
		if n != 0 {
			t.Fatal("invalid limits started a process")
		}
	}
}

func TestPipelineCostCapCannotBeResumedAfterSpending(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":1}}`)}
	waitFor(t, "cost cap ends generation", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.State == stateStopped })
	if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err == nil || !strings.Contains(err.Error(), "run cost cap reached") {
		t.Fatalf("spent cap resume: %v", err)
	}
}

func TestPipelineLimitsThroughHTTP(t *testing.T) {
	fr := &fakeRunner{}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pipeline-limits")
	response := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "cost_cap_micro_usd": 100, "timeout_seconds": 60,
	}, tenantHdr(tenant))
	deadline, _ := response.body["run_deadline_at"].(string)
	if response.code != http.StatusCreated || response.body["cost_cap_micro_usd"] != float64(100) || deadline == "" {
		t.Fatalf("limits response: %d %s", response.code, response.raw)
	}
	ref := response.body["run_ref"].(string)
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":1}}`)}
	waitFor(t, "HTTP cap stops process", func() bool { d, _ := m.getRun(context.Background(), tenant, ref); return d.State == stateStopped })
}

func TestPipelineUnknownCostStopsRun(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","usage":{"input_tokens":1,"output_tokens":1}}`)}
	waitFor(t, "unknown spending stops capped process", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped && d.Reason == "run cost unavailable"
	})
}

func TestPipelineCostWriteFailureStopsRun(t *testing.T) {
	fr := &fakeRunner{}
	sink := &recordingCostSink{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithSessionCostSink(sink))
	fault := &failNthMutateData{inner: m.Data, err: errors.New("cost counter commit fixture")}
	m.Data = fault
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	fault.mu.Lock()
	fault.failAt = fault.calls + 1
	fault.mu.Unlock()
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","total_cost_usd":0.00001,"usage":{"input_tokens":1,"output_tokens":1}}`)}
	waitFor(t, "cost counter failure stops capped process", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped && d.Reason == "run cost unavailable"
	})
	sink.mu.Lock()
	n := len(sink.samples)
	sink.mu.Unlock()
	if n != 1 {
		t.Fatalf("spend rows=%d, want one retained cost", n)
	}
	if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err == nil || !strings.Contains(err.Error(), "prior run cost is unknown") {
		t.Fatalf("unknown cost resume: %v", err)
	}
}

func TestPipelinePartialPriceIsUnknown(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithListPricer(func(provider, model string, tokens TurnTokens) (int64, bool) { return 1, model == "priced" }))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","modelUsage":{"priced":{"inputTokens":1},"unknown":{"inputTokens":1}}}`)}
	waitFor(t, "partially priced spending stops capped process", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped && d.Reason == "run cost unavailable"
	})
}
func TestPipelineExpiredDeadlineRefusesResume(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, clock := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	clock.advance(61 * time.Second)
	if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err == nil || !strings.Contains(err.Error(), "run timeout reached") {
		t.Fatalf("expired deadline resume: %v", err)
	}
}

func TestPipelineFractionalTimeoutUsesRuntimeStop(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	timer := &fakeTimer{}
	m.rt.afterFunc = func(d time.Duration, f func()) runTimer {
		timer.mu.Lock()
		timer.d, timer.fire = d, f
		timer.mu.Unlock()
		return timer
	}
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", TimeoutSeconds: 0.125})
	if err != nil {
		t.Fatal(err)
	}
	timer.mu.Lock()
	d := timer.d
	timer.mu.Unlock()
	if d != 125*time.Millisecond {
		t.Fatalf("deadline rounded to %v", d)
	}
	timer.trigger()
	waitFor(t, "fractional run deadline stops process", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.State == stateStopped && d.Reason == "run timeout reached"
	})
}

func TestPipelineReportedFreeTurnKeepsRunning(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","total_cost_usd":0}`)}
	waitFor(t, "explicit free result is known zero", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.CostMicroUSD != nil })
	d, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil || d.CostMicroUSD == nil || *d.CostMicroUSD != 0 || d.ProcessState != stateRunning {
		t.Fatalf("free turn stopped or unpriced: %+v %v", d, err)
	}
}

func TestPipelineCostSumOverflowStopsRun(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","modelUsage":{"priced-a":{"inputTokens":1,"costUSD":5000000000000},"priced-b":{"inputTokens":1,"costUSD":5000000000000}}}`)}
	waitFor(t, "overflowing reported spending stops process", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped && d.Reason == "run cost cap reached"
	})
	if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err == nil || !strings.Contains(err.Error(), "run cost cap reached") {
		t.Fatalf("overflowed cap resume: %v", err)
	}
}

func TestPipelineFetchUsesIsolatedHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NETRC", "ambient-netrc-must-not-reach-git")
	out, code, stderr, err := gitRunWithHome(context.Background(), dir, dir, "-c", `alias.pipeline-env=!printf '%s|%s' "$HOME" "$NETRC"`, "pipeline-env")
	if err != nil || code != 0 || out != dir+"|" {
		t.Fatalf("git environment=%q code=%d err=%v %s", out, code, err, stderr)
	}
}

func TestPipelineCostPositiveWrapStopsRun(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 9223372036854775807})
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","modelUsage":{"priced-a":{"inputTokens":1,"costUSD":7000000000000},"priced-b":{"inputTokens":1,"costUSD":7000000000000},"priced-c":{"inputTokens":1,"costUSD":7000000000000}}}`)}
	waitFor(t, "overflowing reported spending stops process", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped && d.Reason == "run cost cap reached"
	})
	if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err == nil || !strings.Contains(err.Error(), "run cost cap reached") {
		t.Fatalf("overflowed cap resume: %v", err)
	}
}

func TestPipelineLimitReasonSurvivesOperatorStop(t *testing.T) {
	for _, tc := range []struct{ name, frame, reason string }{
		{"cap", `{"type":"result","subtype":"success","total_cost_usd":0.01}`, "run cost cap reached"},
		{"unknown", `{"type":"result","subtype":"success"}`, "run cost unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ctx := context.Background()
			dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 100})
			if err != nil {
				t.Fatal(err)
			}
			proc := fr.lastProc()
			var once sync.Once
			proc.onStop = func() { once.Do(func() { proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(tc.frame)} }) }
			stopped, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user")
			if err != nil || stopped.State != stateStopped || stopped.Reason != tc.reason {
				t.Fatalf("stop=%+v err=%v", stopped, err)
			}
			if _, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err == nil {
				t.Fatal("a bounded run with exhausted or unknown spending resumed")
			}
		})
	}
}

func TestPipelineCumulativeCostOverflowStaysUnknown(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user", CostCapMicroUSD: 9223372036854775807})
	if err != nil {
		t.Fatal(err)
	}
	proc := fr.lastProc()
	var once sync.Once
	proc.onStop = func() {
		once.Do(func() {
			proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","modelUsage":{"a":{"inputTokens":1,"costUSD":7000000000000},"b":{"inputTokens":1,"costUSD":7000000000000},"tail":{"inputTokens":1,"costUSD":0.000001}}}`)}
		})
	}
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","modelUsage":{"a":{"inputTokens":1,"costUSD":7000000000000}}}`)}
	waitFor(t, "first large cost committed", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.CostMicroUSD != nil && *d.CostMicroUSD == 7000000000000000000
	})
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success","modelUsage":{"a":{"inputTokens":1,"costUSD":7000000000000},"b":{"inputTokens":1,"costUSD":7000000000000}}}`)}
	waitFor(t, "cumulative cap stopped", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.State == stateStopped })
	stopped, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil || stopped.Reason != "run cost cap reached" || stopped.CostMicroUSD != nil {
		t.Fatalf("unrepresentable cumulative cost must stay unknown: cost=%v reason=%q err=%v", stopped.CostMicroUSD, stopped.Reason, err)
	}
}
