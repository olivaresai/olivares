// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// sse joins event blocks into one SSE body (each block already "event:\ndata:\n").
func sse(blocks ...string) string { return strings.Join(blocks, "\n") }

func TestStreamMessage_AccumulatesTextUsageAndSurfacesDeltas(t *testing.T) {
	body := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_s1","type":"message","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":25,"cache_read_input_tokens":10,"output_tokens":1}}}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
`,
		`event: ping
data: {"type":"ping"}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}
`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}
`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":15}}
`,
		`event: message_stop
data: {"type":"message_stop"}
`,
	)
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": body}}
	inf := newInf(d, model.GatewayDirect)

	var live strings.Builder
	var startSeen, stopSeen bool
	resp, err := inf.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 64,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}, func(ev StreamEvent) error {
		if ev.TextDelta != "" {
			live.WriteString(ev.TextDelta)
		}
		switch ev.Type {
		case "content_block_start":
			startSeen = true
		case "content_block_stop":
			stopSeen = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if resp.Text() != "Hello!" {
		t.Errorf("accumulated text = %q, want Hello!", resp.Text())
	}
	if live.String() != "Hello!" {
		t.Errorf("live deltas = %q, want Hello!", live.String())
	}
	if !startSeen || !stopSeen {
		t.Errorf("block start/stop events not surfaced: start=%v stop=%v", startSeen, stopSeen)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	// input/cache from message_start; output from the cumulative message_delta.
	if resp.Usage.InputTokens != 25 || resp.Usage.CacheReadInputTokens != 10 || resp.Usage.OutputTokens != 15 {
		t.Errorf("usage merge wrong: %+v", resp.Usage)
	}
	// stream:true and the defaulted model went on the wire.
	if !strings.Contains(d.lastBody, `"stream":true`) {
		t.Errorf("stream:true not sent: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, `"claude-opus-4-8"`) {
		t.Errorf("model not defaulted: %s", d.lastBody)
	}
}

func TestStreamMessage_ToolUseAccumulatesAndRoundTrips(t *testing.T) {
	body := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_t","type":"message","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":472,"output_tokens":2}}}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}
`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" \"San Francisco, CA\"}"}}
`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":1}
`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}
`,
		`event: message_stop
data: {"type":"message_stop"}
`,
	)
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": body}}
	inf := newInf(d, model.GatewayDirect)
	resp, err := inf.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("weather?")}}},
	}, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(resp.Content))
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.Content[1].Type != "tool_use" {
		t.Errorf("block[1].type = %q, want tool_use", resp.Content[1].Type)
	}
	// The streamed tool_use block round-trips when appended back into messages[].
	turn := AppendAssistantTurn(nil, resp)
	raw, err := json.Marshal(turn)
	if err != nil {
		t.Fatalf("marshal assistant turn: %v", err)
	}
	js := string(raw)
	if !strings.Contains(js, `"name":"get_weather"`) || !strings.Contains(js, `"San Francisco, CA"`) {
		t.Errorf("tool_use input did not round-trip: %s", js)
	}
}

func TestStreamMessage_ThinkingDeltasSurfacedAndAccumulated(t *testing.T) {
	body := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_th","type":"message","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":30,"output_tokens":1}}}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Step 1."}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}
`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer."}}
`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":1}
`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}
`,
		`event: message_stop
data: {"type":"message_stop"}
`,
	)
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": body}}
	inf := newInf(d, model.GatewayDirect)
	var think strings.Builder
	resp, err := inf.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 20000,
		Thinking:  AdaptiveThinking(ThinkingDisplaySummarized),
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("q")}}},
	}, func(ev StreamEvent) error {
		think.WriteString(ev.ThinkingDelta)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if think.String() != "Step 1." {
		t.Errorf("thinking deltas = %q", think.String())
	}
	if resp.Text() != "Answer." {
		t.Errorf("text = %q", resp.Text())
	}
	// The thinking config (adaptive + display) went on the wire.
	if !strings.Contains(d.lastBody, `"thinking":{"type":"adaptive","display":"summarized"}`) {
		t.Errorf("thinking config not sent: %s", d.lastBody)
	}
	// The thinking block round-trips (signature preserved) for a same-model continuation.
	raw, _ := json.Marshal(AppendAssistantTurn(nil, resp))
	if !strings.Contains(string(raw), `"signature":"sig123"`) {
		t.Errorf("thinking signature not round-tripped: %s", raw)
	}
}

func TestStreamMessage_MidStreamRefusalIsBillablePreOutputIsNot(t *testing.T) {
	mid := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_mr","type":"message","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":50,"output_tokens":1}}}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}
`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}
`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"}},"usage":{"output_tokens":12}}
`,
		`event: message_stop
data: {"type":"message_stop"}
`,
	)
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": mid}}
	inf := newInf(d, model.GatewayDirect)
	resp, err := inf.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 64,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("x")}}},
	}, nil)
	if err != nil {
		t.Fatalf("StreamMessage (mid): %v", err)
	}
	if !resp.IsRefusal() {
		t.Errorf("mid-stream: IsRefusal()=false")
	}
	if !resp.IsBillable() {
		t.Errorf("mid-stream refusal must be billable (streamed partial output)")
	}
	if resp.StopDetails == nil || resp.StopDetails.Category != "cyber" {
		t.Errorf("stop_details not accumulated: %+v", resp.StopDetails)
	}

	pre := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_pr","type":"message","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":50,"output_tokens":1}}}
`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"bio"}},"usage":{"output_tokens":0}}
`,
		`event: message_stop
data: {"type":"message_stop"}
`,
	)
	d2 := &routeDoer{routes: map[string]string{"POST /v1/messages": pre}}
	inf2 := newInf(d2, model.GatewayDirect)
	resp2, err := inf2.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 64,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("x")}}},
	}, nil)
	if err != nil {
		t.Fatalf("StreamMessage (pre): %v", err)
	}
	if !resp2.IsRefusal() {
		t.Errorf("pre-output: IsRefusal()=false")
	}
	if resp2.IsBillable() {
		t.Errorf("pre-output refusal must NOT be billable (output_tokens=0), got %d", resp2.Usage.OutputTokens)
	}
}

func TestStreamMessage_MidStreamErrorEventIsStreamError(t *testing.T) {
	body := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_e","type":"message","role":"assistant","model":"claude-opus-4-8"}}
`,
		`event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}
`,
	)
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": body}}
	inf := newInf(d, model.GatewayDirect)
	_, err := inf.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 8,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("x")}}},
	}, nil)
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("want *StreamError, got %T: %v", err, err)
	}
	if se.Type != "overloaded_error" {
		t.Errorf("stream error type = %q", se.Type)
	}
}

func TestStreamMessage_OnEventErrorAbortsStream(t *testing.T) {
	body := sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"claude-opus-4-8"}}
`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stop here"}}
`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" never reached"}}
`,
	)
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": body}}
	inf := newInf(d, model.GatewayDirect)
	sentinel := fmt.Errorf("caller canceled")
	_, err := inf.StreamMessage(context.Background(), MessageRequest{
		MaxTokens: 8,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("x")}}},
	}, func(ev StreamEvent) error {
		if ev.TextDelta == "stop here" {
			return sentinel
		}
		return nil
	})
	if err != sentinel {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestCreateMessage_RejectsStreamRequest(t *testing.T) {
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": `{}`}}
	inf := newInf(d, model.GatewayDirect)
	_, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens: 8,
		Stream:    true,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("x")}}},
	})
	if err == nil || !strings.Contains(err.Error(), "StreamMessage") {
		t.Fatalf("want stream-rejection error, got %v", err)
	}
}
