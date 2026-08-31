// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"errors"
	"testing"
)

// sseTwoEvents is a LEGACY (pre-1.0) stream: bare events with the removed `final`
// flag — the lenient-parse fallback. The v1.0 shape is sseV1Events below.
const sseTwoEvents = "data: {\"id\":\"t1\",\"status\":{\"state\":\"TASK_STATE_WORKING\"}}\n\n" +
	"data: {\"id\":\"t1\",\"status\":{\"state\":\"TASK_STATE_COMPLETED\"},\"final\":true}\n\n"

// sseV1Events is a v1.0 JSON-RPC SSE stream (§9.4.2): each frame is a full JSON-RPC
// response whose result is the StreamResponse oneof — the Task snapshot first
// (§3.1.2), then status updates; the stream closes at the terminal state (no
// `final` field in v1.0 — removed by #1308). An artifactUpdate carries no lifecycle
// state and must be skipped.
const sseV1Events = "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"task\":{\"id\":\"t1\",\"contextId\":\"c\",\"status\":{\"state\":\"TASK_STATE_WORKING\"}}}}\n\n" +
	"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"artifactUpdate\":{\"taskId\":\"t1\",\"contextId\":\"c\",\"artifact\":{\"artifactId\":\"a1\",\"parts\":[{\"text\":\"x\"}]},\"lastChunk\":true}}}\n\n" +
	"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"statusUpdate\":{\"taskId\":\"t1\",\"contextId\":\"c\",\"status\":{\"state\":\"TASK_STATE_COMPLETED\"}}}}\n\n"

// TestDelegateStreamingGovernedSuccess: a governed streaming delegation yields the
// stream's events in order and stops on the final/terminal event.
func TestDelegateStreamingGovernedSuccess(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", sseTwoEvents)
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	var states []TaskState
	err := d.DelegateStreaming(context.Background(), okSpec(), func(ev StreamEvent) error {
		states = append(states, ev.State)
		return nil
	})
	if err != nil {
		t.Fatalf("streaming delegation: %v", err)
	}
	if len(states) != 2 || states[0] != TaskStateWorking || states[1] != TaskStateCompleted {
		t.Errorf("streamed states = %v, want [WORKING COMPLETED]", states)
	}
	if doer.postCount != 1 {
		t.Errorf("streaming should open exactly one stream POST, got %d", doer.postCount)
	}
}

// TestDelegateStreamingDenied: a non-approved gate must NOT open a stream.
func TestDelegateStreamingDenied(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", sseTwoEvents)
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusPending},
	})
	err := d.DelegateStreaming(context.Background(), okSpec(), func(StreamEvent) error { return nil })
	var de *DenyError
	if !errors.As(err, &de) {
		t.Fatalf("a non-approved gate must deny streaming, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a denied streaming delegation must open NO stream, got %d POSTs", doer.postCount)
	}
}

// TestSubscribeToTaskStreams: SubscribeToTask resumes a stream for an existing Task
// (card + allowlist verified, no fresh approval).
func TestSubscribeToTaskStreams(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", sseTwoEvents)
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	var n int
	err := d.SubscribeToTask(context.Background(), TaskRef{AgentName: "billing", AgentURL: "https://billing.example.com", TaskID: "t1"}, func(StreamEvent) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("SubscribeToTask: %v", err)
	}
	if n != 2 {
		t.Errorf("subscribe streamed %d events, want 2", n)
	}
}

// TestDelegateStreamingV1Frames: a governed streaming delegation over the v1.0 SSE
// framing (Task snapshot → bounded artifactUpdate → terminal statusUpdate) yields
// the lifecycle/reply events in order and stops at the terminal state without resubscribe.
func TestDelegateStreamingV1Frames(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", sseV1Events)
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	var states []TaskState
	var replies []*ReplyEvent
	err := d.DelegateStreaming(context.Background(), okSpec(), func(ev StreamEvent) error {
		if ev.State != "" {
			states = append(states, ev.State)
		}
		if ev.Reply != nil {
			replies = append(replies, ev.Reply)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("v1.0 streaming delegation: %v", err)
	}
	if len(states) != 2 || states[0] != TaskStateWorking || states[1] != TaskStateCompleted {
		t.Errorf("streamed states = %v, want [WORKING COMPLETED]", states)
	}
	if len(replies) != 1 || replies[0].Kind != ReplyEventArtifact ||
		replies[0].ArtifactID != "a1" || len(replies[0].Parts) != 1 ||
		replies[0].Parts[0].Text != "x" {
		t.Errorf("streamed reply projections = %+v, want one bounded artifact", replies)
	}
	if doer.postCount != 1 {
		t.Errorf("a terminal statusUpdate must end the stream without resubscribe, got %d POSTs", doer.postCount)
	}
}

// TestStreamInterruptEndsStream: an INPUT_REQUIRED update ends the stream cleanly
// (§11.7: streams run until a terminal or interrupted state) — the interrupt is
// surfaced to the caller (HITL) and the client does not loop on resubscribes.
func TestStreamInterruptEndsStream(t *testing.T) {
	sse := "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"statusUpdate\":{\"taskId\":\"t1\",\"contextId\":\"c\",\"status\":{\"state\":\"TASK_STATE_INPUT_REQUIRED\"}}}}\n\n"
	doer, jwks := verifiedDoer(t, "billing", sse)
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	var events []StreamEvent
	err := d.DelegateStreaming(context.Background(), okSpec(), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("an interrupted stream must end cleanly, got %v", err)
	}
	if len(events) != 1 || !events[0].Interrupt {
		t.Fatalf("want exactly the interrupt event, got %+v", events)
	}
	if doer.postCount != 1 {
		t.Errorf("an interrupt must not trigger resubscribes, got %d POSTs", doer.postCount)
	}
}

// TestStreamEventFromJSON parses the v1.0 StreamResponse oneof members, the
// JSON-RPC-wrapped legacy shape, and skips non-task payloads.
func TestStreamEventFromJSON(t *testing.T) {
	// Legacy bare shape inside a JSON-RPC envelope.
	ev, ok, err := streamEventFromJSON([]byte(`{"result":{"id":"t9","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}}`))
	if err != nil || !ok || ev.State != TaskStateInputReq || !ev.Interrupt || ev.Final {
		t.Errorf("input-required wrapped event = %+v ok=%v err=%v", ev, ok, err)
	}
	// v1.0 statusUpdate member.
	ev, ok, err = streamEventFromJSON([]byte(`{"result":{"statusUpdate":{"taskId":"t3","contextId":"c","status":{"state":"TASK_STATE_FAILED"}}}}`))
	if err != nil || !ok || ev.TaskID != "t3" || ev.State != TaskStateFailed || !ev.Terminal || !ev.Final {
		t.Errorf("statusUpdate event = %+v ok=%v err=%v", ev, ok, err)
	}
	// v1.0 task member (the first stream event is the Task snapshot, §3.1.6).
	ev, ok, err = streamEventFromJSON([]byte(`{"result":{"task":{"id":"t4","contextId":"c","status":{"state":"TASK_STATE_SUBMITTED"}}}}`))
	if err != nil || !ok || ev.TaskID != "t4" || ev.State != TaskStateSubmitted {
		t.Errorf("task snapshot event = %+v ok=%v err=%v", ev, ok, err)
	}
	// v1.0 message member: a message-only stream is a synchronous completion.
	ev, ok, err = streamEventFromJSON([]byte(`{"result":{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"done"}]}}}`))
	if err != nil || !ok || ev.State != TaskStateCompleted || !ev.Final ||
		ev.Reply == nil || ev.Reply.MessageID != "m1" || ev.Reply.Parts[0].Text != "done" {
		t.Errorf("message event = %+v ok=%v err=%v", ev, ok, err)
	}
	// v1.0 artifactUpdate member: no lifecycle state, but its bounded reply is surfaced.
	ev, ok, err = streamEventFromJSON([]byte(`{"result":{"artifactUpdate":{"taskId":"t5","contextId":"c","artifact":{"artifactId":"a","parts":[{"data":{"score":1}}]}}}}`))
	if err != nil || !ok || ev.State != "" || ev.Reply == nil ||
		ev.Reply.Kind != ReplyEventArtifact || ev.Reply.ArtifactID != "a" {
		t.Errorf("artifact event = %+v ok=%v err=%v", ev, ok, err)
	}
	if _, ok, err := streamEventFromJSON([]byte(`{"keepalive":true}`)); ok || err != nil {
		t.Error("a non-task SSE event must be skipped")
	}
	if _, ok, err := streamEventFromJSON([]byte(`{"result":{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"done"}]},"artifactUpdate":{"taskId":"t1","contextId":"c","artifact":{"artifactId":"a1","parts":[{"text":"x"}]}}}}`)); err == nil || ok {
		t.Fatalf("multiple StreamResponse oneof values: ok=%v err=%v", ok, err)
	}
}
