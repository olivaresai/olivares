// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// stream.go is the A2A v1.0 SSE half of the delegation client (AIP-05 §c): streaming
// Task updates over Server-Sent Events for SendStreamingMessage and SubscribeToTask
// (resume). Like the unary path it is governed — SendStreamingMessage passes the full
// PEP (verify card → allowlist → PlanHash → ApprovalGate) before any byte streams,
// because it is a delegation; SubscribeToTask only verifies the card + allowlist (it
// resumes observation of an EXISTING Task, it starts none). Both reconnect on a
// transient drop by re-subscribing to the Task id (bounded retries), so a flaky
// network does not silently lose lifecycle updates.

// maxStreamBody caps a single SSE event body (mirrors emitBodyCap for unary calls).
const maxStreamBody = 4 << 20 // 4 MiB per event

// maxResubscribes bounds reconnect attempts on a transient stream drop so a flapping
// peer cannot pin the client in an infinite resubscribe loop.
const maxResubscribes = 5

// StreamEvent is one normalized Task lifecycle or bounded reply projection from
// an SSE stream. Reply is populated only for Message/artifactUpdate variants;
// raw data, artifact bytes and unsanitized file URIs never leave the connector.
// Final marks the last event of the stream (the server signaled completion of
// THIS stream, not necessarily a terminal Task state).
type StreamEvent struct {
	TaskID    string
	ContextID string
	State     TaskState
	Interrupt bool
	Terminal  bool
	Final     bool
	Detail    string // short, non-sensitive
	Reply     *ReplyEvent
}

// DelegateStreaming is the governed streaming delegation (SendStreamingMessage, SSE).
// It runs the SAME PEP as Delegate — verify the signed card, enforce the deny-by-
// default allowlist, bind a PlanHash, require an ApprovalGate authorization — and only
// then opens the stream. onEvent is called for each lifecycle update; returning an
// error from onEvent stops the stream. The stream reconnects via SubscribeToTask on a
// transient drop (bounded). The audited decision carries no message text.
func (d *Delegator) DelegateStreaming(ctx context.Context, spec DelegateSpec, onEvent func(StreamEvent) error) error {
	streamCtx := withTraceParent(ctx, spec.TraceParent)
	inbound := d.inboundChain(streamCtx, spec)
	plan := PlanHash(spec.AgentName, spec.Skill, spec.Scope,
		hashParams(spec.Skill, spec.ContextID, len(spec.Text)))

	card, err := d.client.verifiedCard(streamCtx, SendSpec{AgentName: spec.AgentName, AgentURL: spec.AgentURL})
	if err != nil {
		d.record(streamCtx, spec, inbound, plan, false, "agent card not verified", "", TaskStateUnspecified)
		return err
	}
	// Capability binding: the skill must be in the signed card AND the card must
	// advertise the streaming capability — you cannot stream to an agent that does not
	// claim streaming. Deny-closed, before the allowlist.
	if err := requireDeclaredSkill(card, spec.AgentName, spec.Skill); err != nil {
		d.record(streamCtx, spec, inbound, plan, false, "capability deny (skill not in signed card)", "", TaskStateUnspecified)
		return err
	}
	if err := requireStreaming(card, spec.AgentName); err != nil {
		d.record(streamCtx, spec, inbound, plan, false, "capability deny (streaming not advertised)", "", TaskStateUnspecified)
		return err
	}
	if !d.allowlist.Allowed(spec.AgentName, spec.Skill, spec.Scope) {
		d.record(streamCtx, spec, inbound, plan, false, "allowlist deny (agent/skill/scope not permitted)", "", TaskStateUnspecified)
		return &DenyError{Reason: "agent/skill/scope not on the delegation allowlist", PlanHash: plan}
	}
	outbound, err := enforceChain(d.chainPolicy, inbound, spec.AgentName)
	if err != nil {
		d.record(streamCtx, spec, inbound, plan, false, "chain deny ("+chainReason(err)+")", "", TaskStateUnspecified)
		return err
	}
	streamCtx = withChain(streamCtx, outbound)
	dec, err := d.gate.Authorize(streamCtx, DelegationRequest{
		Tenant: spec.Tenant, AgentName: spec.AgentName, Skill: spec.Skill,
		Scope: spec.Scope, PlanHash: plan, RequestedBy: spec.RequestedBy,
	})
	if err != nil {
		d.record(streamCtx, spec, inbound, plan, false, "gate error (fail-closed)", "", TaskStateUnspecified)
		return fmt.Errorf("a2a: streaming delegation gate error (deny): %w", err)
	}
	if !dec.Allowed() || (dec.PlanHash != "" && dec.PlanHash != plan) {
		d.record(streamCtx, spec, inbound, plan, false, "gate not approved ("+string(dec.Status)+")", dec.ApprovalRef, TaskStateUnspecified)
		return &DenyError{Reason: "streaming delegation not approved by governance (" + string(dec.Status) + ")", PlanHash: plan}
	}

	endpoint, tenant, err := resolveJSONRPC(card, spec.AgentURL)
	if err != nil {
		return err
	}
	if err := d.client.requireSecure(endpoint); err != nil {
		return err
	}

	d.record(streamCtx, spec, inbound, plan, true, "streaming delegated", dec.ApprovalRef, TaskStateWorking)

	msg := a2aMessage{Role: roleUser, Parts: []a2aPart{{Text: spec.Text}}, MessageID: newID()}
	if spec.ContextID != "" {
		msg.ContextID = spec.ContextID
	}
	if spec.Skill != "" {
		msg.Metadata = map[string]any{"skill": spec.Skill}
	}
	env := jsonrpcRequest{JSONRPC: "2.0", ID: newID(), Method: methodSendStreamingMessage, Params: sendParams{Tenant: tenant, Message: msg}}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return d.streamWithResume(streamCtx, endpoint, tenant, raw, onEvent)
}

// SubscribeToTask resumes the SSE stream of an EXISTING Task (the v1.0 SubscribeToTask
// method). It verifies the agent card + allowlist (you may only resume on an agent you
// are permitted to reach) but requires NO fresh ApprovalGate: resuming observes a Task
// already started, it begins no new delegation. It reconnects on a transient drop.
func (d *Delegator) SubscribeToTask(ctx context.Context, ref TaskRef, onEvent func(StreamEvent) error) error {
	card, err := d.client.verifiedCard(ctx, SendSpec{AgentName: ref.AgentName, AgentURL: ref.AgentURL})
	if err != nil {
		return err
	}
	// Capability binding: resuming opens an SSE stream, so the agent's signed card
	// MUST advertise streaming (deny-closed) — same surface a SendStreamingMessage needs.
	if err := requireStreaming(card, ref.AgentName); err != nil {
		return err
	}
	// Allowlist still gates WHICH agents may be reached (least-privilege); resume is
	// not skill-specific, so the coarse per-agent check applies.
	if !d.allowlist.AllowedAgent(ref.AgentName) {
		return &DenyError{Reason: "agent not on the delegation allowlist (resume)", PlanHash: ""}
	}
	endpoint, tenant, err := resolveJSONRPC(card, ref.AgentURL)
	if err != nil {
		return err
	}
	if err := d.client.requireSecure(endpoint); err != nil {
		return err
	}
	params := map[string]any{"id": ref.TaskID}
	if tenant != "" {
		params["tenant"] = tenant
	}
	raw, err := json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: newID(), Method: methodSubscribeToTask, Params: params})
	if err != nil {
		return err
	}
	return d.consumeStream(ctx, endpoint, raw, onEvent)
}

// streamWithResume consumes an SSE stream and, on a transient drop before a Final/
// terminal event, re-subscribes to the Task id (SubscribeToTask) up to maxResubscribes
// times. tenant is the selected interface's routing id — the resubscribe request to
// that interface must echo it exactly like the original call did. A clean end (Final,
// terminal state, interrupt, or onEvent stop) returns nil; a policy/transport error
// that is not resumable returns it.
func (d *Delegator) streamWithResume(ctx context.Context, endpoint, tenant string, raw []byte, onEvent func(StreamEvent) error) error {
	var lastTask string
	wrapped := func(ev StreamEvent) error {
		if ev.TaskID != "" {
			lastTask = ev.TaskID
		}
		return onEvent(ev)
	}
	err := d.consumeStream(ctx, endpoint, raw, wrapped)
	for attempt := 0; err != nil && attempt < maxResubscribes && lastTask != "" && ctx.Err() == nil; attempt++ {
		if !resumableStreamErr(err) {
			return err
		}
		params := map[string]any{"id": lastTask}
		if tenant != "" {
			params["tenant"] = tenant
		}
		resub, merr := json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: newID(), Method: methodSubscribeToTask, Params: params})
		if merr != nil {
			return merr
		}
		err = d.consumeStream(ctx, endpoint, resub, wrapped)
	}
	return err
}

// consumeStream opens one SSE POST and dispatches each parsed Task update to onEvent
// until the stream ends, a Final event arrives, onEvent returns an error, or the
// context is canceled. A TERMINAL state ends the stream (v1.0 §3.1.2: "the stream
// MUST close when the task reaches a terminal state") and so does an INTERRUPT
// (§11.7: streams run until a terminal or interrupted state; an interrupted Task
// waits on the caller/HITL, so listening on is futile resubscribe churn). It is the
// single SSE read path.
func (d *Delegator) consumeStream(ctx context.Context, endpoint string, raw []byte, onEvent func(StreamEvent) error) error {
	resp, err := d.client.openStream(ctx, endpoint, raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return scanSSE(resp.Body, func(payload []byte) (bool, error) {
		ev, ok, parseErr := streamEventFromJSON(payload)
		if parseErr != nil {
			return true, parseErr
		}
		if !ok {
			return false, nil // a non-A2A SSE event (for example a keepalive) — skip
		}
		if cbErr := onEvent(ev); cbErr != nil {
			return true, cbErr
		}
		return ev.Final || ev.Terminal || ev.Interrupt, nil
	})
}

// openStream issues a streaming JSON-RPC POST (SendStreamingMessage / SubscribeToTask)
// and returns the live response for the caller to stream + Close. It enforces the same
// out-of-band credentials, TLS posture and auth-status mapping as the unary path, but
// requests text/event-stream and does NOT buffer the body.
func (c *Client) openStream(ctx context.Context, endpoint string, raw []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Mandatory A2A-Version service parameter (§3.6.1) — same as the unary path.
	req.Header.Set(a2aVersionHeader, a2aVersionWire)
	c.applyAuth(req)
	if tp := traceParentFrom(ctx); tp != "" {
		req.Header.Set("traceparent", tp)
	}
	// Propagate the (bounded, cycle-free) multi-agent delegation lineage out-of-band so a
	// cooperating downstream Olivares plane keeps the same governed chain (chain.go).
	if path := chainFrom(ctx).encode(); path != "" {
		req.Header.Set(delegationPathHeader, path)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	if err := httpAuthError(resp.StatusCode); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, &streamHTTPError{status: resp.StatusCode}
	}
	return resp, nil
}

// scanSSE reads an SSE stream, accumulating each event's `data:` lines until a blank
// line, and hands the concatenated payload to emit. emit returns (done, err): done
// stops the scan cleanly (Final/terminal/onEvent-stop), err aborts it. A read error
// before a clean stop is returned (so streamWithResume can resubscribe).
func scanSSE(r io.Reader, emit func(payload []byte) (bool, error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxStreamBody)
	var data strings.Builder
	flush := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		payload := data.String()
		data.Reset()
		return emit([]byte(payload))
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if done, err := flush(); done || err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// id:/event:/retry:/comment lines are ignored (we key off data + state).
		}
	}
	if err := sc.Err(); err != nil {
		return &streamHTTPError{transient: true, cause: err}
	}
	// Stream ended without a Final marker; flush any trailing event, then treat a
	// clean EOF as a transient end so a non-final stream can resubscribe.
	if done, err := flush(); err != nil {
		return err
	} else if done {
		return nil
	}
	return &streamHTTPError{transient: true, cause: io.ErrUnexpectedEOF}
}

// streamEventFromJSON parses one SSE event payload into a normalized StreamEvent.
// In v1.0 a JSON-RPC SSE frame is a full JSON-RPC response whose result is a
// StreamResponse ONEOF — exactly one of "task" (the Task snapshot, MUST be the
// first event), "message" (a message-only stream: one reply, then close),
// "statusUpdate" (TaskStatusUpdateEvent: taskId/contextId/status) or
// "artifactUpdate" (§9.4.2/§4.3.3; the v0.x `final` field was REMOVED in v1.0 —
// stream closure itself signals the end, so Final derives from the state). Only the
// lifecycle values stay reference-only. Message/artifact values are projected
// through the same bounded Part mapper as synchronous responses. Bare pre-1.0
// shapes (incl. `final`) are parsed as lenient fallback. ok is false for a
// payload with no recognizable A2A signal.
func streamEventFromJSON(payload []byte) (StreamEvent, bool, error) {
	// Unwrap a JSON-RPC envelope if present (streaming responses carry result).
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	body := payload
	if json.Unmarshal(payload, &env) == nil && len(env.Result) > 0 {
		body = env.Result
	}
	// v1.0 StreamResponse oneof.
	var sr struct {
		Task           json.RawMessage `json:"task"`
		Message        json.RawMessage `json:"message"`
		StatusUpdate   json.RawMessage `json:"statusUpdate"`
		ArtifactUpdate json.RawMessage `json:"artifactUpdate"`
	}
	if json.Unmarshal(body, &sr) == nil {
		members := 0
		for _, raw := range []json.RawMessage{sr.Task, sr.Message, sr.StatusUpdate, sr.ArtifactUpdate} {
			if len(raw) > 0 {
				members++
			}
		}
		if members > 1 {
			return StreamEvent{}, false, fmt.Errorf("a2a: StreamResponse has multiple oneof values")
		}
		switch {
		case len(sr.StatusUpdate) > 0:
			ev, ok := taskLifecycleEvent(sr.StatusUpdate)
			return ev, ok, nil
		case len(sr.Task) > 0:
			ev, ok := taskLifecycleEvent(sr.Task)
			return ev, ok, nil
		case len(sr.Message) > 0:
			// Message-only stream: exactly one reply, then the stream closes
			// (§3.1.2) — semantically a synchronous completion.
			reply, err := projectMessageReplyEvent(sr.Message, "")
			if err != nil {
				return StreamEvent{}, false, err
			}
			return StreamEvent{
				ContextID: reply.ContextID,
				State:     TaskStateCompleted,
				Terminal:  true,
				Final:     true,
				Detail:    "message reply",
				Reply:     &reply,
			}, true, nil
		case len(sr.ArtifactUpdate) > 0:
			reply, err := projectArtifactReplyEvent(sr.ArtifactUpdate, "")
			if err != nil {
				return StreamEvent{}, false, err
			}
			return StreamEvent{
				TaskID: reply.TaskID, ContextID: reply.ContextID,
				Detail: "artifact update", Reply: &reply,
			}, true, nil
		}
	}
	// Lenient fallback: a bare Task / pre-1.0 status-update shape.
	ev, ok := taskLifecycleEvent(body)
	return ev, ok, nil
}

// taskLifecycleEvent extracts a lifecycle StreamEvent from a Task or
// TaskStatusUpdateEvent JSON object (id vs taskId naming covered; the legacy v0.x
// `final` flag honored when a pre-1.0 peer still sends it).
func taskLifecycleEvent(body []byte) (StreamEvent, bool) {
	var u struct {
		ID        string `json:"id"`
		TaskID    string `json:"taskId"`
		ContextID string `json:"contextId"`
		Final     bool   `json:"final"` // REMOVED in v1.0; read for pre-1.0 peers only
		Status    struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &u) != nil {
		return StreamEvent{}, false
	}
	state := TaskState(strings.TrimSpace(u.Status.State))
	if state == "" {
		return StreamEvent{}, false
	}
	id := u.ID
	if id == "" {
		id = u.TaskID
	}
	return StreamEvent{
		TaskID:    id,
		ContextID: u.ContextID,
		State:     state,
		Interrupt: taskStateInterrupt(state),
		Terminal:  taskStateTerminal(state),
		Final:     u.Final || taskStateTerminal(state),
		Detail:    "task " + string(state),
	}, true
}

// streamHTTPError carries a stream failure, marking whether it is transient (a clean
// EOF / read error worth resubscribing on) vs a hard non-2xx the caller should not retry.
type streamHTTPError struct {
	status    int
	transient bool
	cause     error
}

func (e *streamHTTPError) Error() string {
	if e.transient {
		if e.cause != nil {
			return fmt.Sprintf("a2a: stream interrupted (resumable): %v", e.cause)
		}
		return "a2a: stream interrupted (resumable)"
	}
	return fmt.Sprintf("a2a: stream http %d", e.status)
}

// resumableStreamErr reports whether a stream error is a transient drop worth a
// SubscribeToTask resume (vs a hard error like a 4xx that should surface).
func resumableStreamErr(err error) bool {
	var se *streamHTTPError
	if e, ok := err.(*streamHTTPError); ok {
		se = e
		return se.transient
	}
	return false
}
