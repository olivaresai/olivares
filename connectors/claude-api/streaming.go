// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file makes a Claude run Olivares CONDUCTS a real STREAM, not a blocking POST
// (FASE W; the portal renders tokens live, the inference PEP governs in
// flight). StreamMessage sends stream:true and decodes the server-sent-events wire shape
// (message_start / content_block_{start,delta,stop} / message_delta / message_stop, plus
// ping and mid-stream error events), surfacing each delta to a caller callback AND
// ACCUMULATING the full MessageResponse — the SAME struct CreateMessage returns. That
// accumulation is the point: every forensic/cost reading in forensic.go
// (IsBillable/RefusalSignal/RuntimeObservations/Fallback*) then works UNCHANGED on a
// streamed response, so a mid-stream refusal bills the streamed partial output and a
// pre-output refusal bills nothing — REAL billing derived from the stream, not reasoned.
//
// Minimal-data (docs/SECURITY-HARDENING.md): like the rest of the inference client this handles content
// in flight and never persists it; what it returns is the model output + token counts,
// and the caller decides what (a verdict / a redacted hash) reaches the ledger.
//
// Authority (verbatim, jun-2026): …/build-with-claude/streaming (event flow, delta
// types, cumulative message_delta usage, the error event); …/refusals-and-fallback
// (the fallback content block at a model boundary in a stream).
package claudeapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamEvent is one decoded SSE event surfaced to the StreamMessage callback as it
// arrives. TextDelta/ThinkingDelta carry the incremental assistant text / summarized-
// reasoning token for a content_block_delta (empty on every other event); Raw is the
// verbatim event JSON for a caller that needs more than the typed fields. A portal
// renders TextDelta; a governor can inspect Type/Raw (e.g. a message_delta carrying a
// refusal stop_reason) without waiting for the stream to end.
type StreamEvent struct {
	Type          string
	Index         int
	TextDelta     string
	ThinkingDelta string
	Raw           json.RawMessage
}

// StreamError is an error delivered IN the event stream (e.g. an overloaded_error that
// would be an HTTP 529 in a non-streaming context). It is distinct from a pre-stream
// modelprovider.APIError (a non-2xx before the stream opened): this one arrives after
// some events may already have been accumulated.
type StreamError struct {
	Type    string
	Message string
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("claudeapi: stream error %s: %s", e.Type, e.Message)
}

// StreamMessage invokes POST /v1/messages with stream:true and decodes the SSE response,
// calling onEvent (may be nil) for each event as it arrives and returning the fully
// ACCUMULATED MessageResponse — identical in shape to CreateMessage's, so the forensic
// helpers apply to it unchanged. It shares CreateMessage's client-side preflight (model
// default, sampling withholding, thinking normalization, the published-constraint
// guards). If onEvent returns an error the stream is abandoned and that error is
// returned (with the partial response accumulated so far); a mid-stream error event
// returns a *StreamError.
func (inf *Inference) StreamMessage(ctx context.Context, req MessageRequest, onEvent func(StreamEvent) error) (MessageResponse, error) {
	if inf.client == nil {
		return MessageResponse{}, ErrNotConfigured
	}
	req.Stream = true
	if err := inf.preflight(&req); err != nil {
		return MessageResponse{}, err
	}
	body, err := inf.client.PostStream(ctx, messagesPath, req, betaHeaderMap(req.BetaHeaders()))
	if err != nil {
		return MessageResponse{}, err
	}
	defer func() { _ = body.Close() }()
	return decodeMessageStream(body, onEvent)
}

// decodeMessageStream reads an SSE byte stream and accumulates a MessageResponse,
// dispatching each event payload to streamState.handle. It uses a bufio.Reader (not a
// Scanner) so an arbitrarily long data line — a big thinking_delta or an
// encrypted_content tool result — never overflows a fixed token buffer. Per the SSE
// grammar, consecutive data: lines of one event are joined with "\n" and processed at
// the blank-line boundary (and at EOF).
func decodeMessageStream(r io.Reader, onEvent func(StreamEvent) error) (MessageResponse, error) {
	st := &streamState{resp: &MessageResponse{}, blocks: map[int]*blockAccumulator{}}
	br := bufio.NewReader(r)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := data.String()
		data.Reset()
		return st.handle(payload, onEvent)
	}
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "": // event boundary
			if ferr := flush(); ferr != nil {
				return *st.resp, ferr
			}
		case strings.HasPrefix(trimmed, ":"): // SSE comment — ignore
		case strings.HasPrefix(trimmed, "data:"):
			d := strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(d)
		default: // "event:" / "id:" / "retry:" — the data payload is self-describing
		}
		if err != nil {
			if err == io.EOF {
				if ferr := flush(); ferr != nil {
					return *st.resp, ferr
				}
				return *st.resp, nil
			}
			return *st.resp, fmt.Errorf("claudeapi: read message stream: %w", err)
		}
	}
}

// streamState accumulates the response across events. blocks holds the in-progress
// content blocks by index; each is finalized into resp.Content at its content_block_stop.
type streamState struct {
	resp   *MessageResponse
	blocks map[int]*blockAccumulator
}

// blockAccumulator gathers one content block's streamed parts. base is the block object
// from content_block_start (carrying type + any non-delta fields like a tool_use id/name
// or a fallback's from/to); the builders gather the deltas; finalize assembles the block.
type blockAccumulator struct {
	typ         string
	base        map[string]json.RawMessage
	text        strings.Builder
	thinking    strings.Builder
	partialJSON strings.Builder
	signature   string
	hasSig      bool
}

// finalize assembles the accumulated block into a ContentBlock whose raw bytes round-trip
// when appended back into messages[] (AppendAssistantTurn) — a streamed tool_use or
// thinking block re-serializes faithfully. A text block also sets Text so Response.Text()
// works without re-parsing. A block with no deltas (web_search_tool_result, the fallback
// boundary block) keeps its content_block_start bytes verbatim.
func (ba *blockAccumulator) finalize() ContentBlock {
	if ba.base == nil {
		ba.base = map[string]json.RawMessage{}
	}
	setStr := func(key, val string) {
		b, _ := json.Marshal(val)
		ba.base[key] = b
	}
	switch ba.typ {
	case blockText:
		setStr(blockText, ba.text.String())
	case "thinking":
		setStr("thinking", ba.thinking.String())
		if ba.hasSig {
			setStr("signature", ba.signature)
		}
	default:
		// tool_use / server_tool_use / mcp_tool_use carry a streamed partial_json input;
		// the final input is always an object. An incomplete/invalid accumulation falls
		// back to {} rather than emitting malformed JSON.
		if ba.partialJSON.Len() > 0 {
			if pj := ba.partialJSON.String(); json.Valid([]byte(pj)) {
				ba.base["input"] = json.RawMessage(pj)
			} else {
				ba.base["input"] = json.RawMessage(`{}`)
			}
		}
	}
	raw, _ := json.Marshal(ba.base)
	cb := ContentBlock{Type: ba.typ, raw: raw}
	if ba.typ == blockText {
		cb.Text = ba.text.String()
	}
	return cb
}

// handle decodes one SSE data payload (self-describing via its "type") and folds it into
// the accumulating response, then surfaces a StreamEvent to onEvent. Unknown event types
// are ignored (the API versioning policy: handle unknown events gracefully).
func (s *streamState) handle(payload string, onEvent func(StreamEvent) error) error {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return fmt.Errorf("claudeapi: decode stream event: %w", err)
	}
	ev := StreamEvent{Type: env.Type, Raw: append(json.RawMessage(nil), payload...)}
	switch env.Type {
	case "message_start":
		var e struct {
			Message struct {
				ID    string        `json:"id"`
				Type  string        `json:"type"`
				Role  string        `json:"role"`
				Model string        `json:"model"`
				Usage *MessageUsage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return fmt.Errorf("claudeapi: decode message_start: %w", err)
		}
		s.resp.ID = e.Message.ID
		s.resp.Type = e.Message.Type
		s.resp.Role = e.Message.Role
		s.resp.Model = e.Message.Model
		if e.Message.Usage != nil {
			s.resp.Usage = *e.Message.Usage
		}
	case "content_block_start":
		var e struct {
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return fmt.Errorf("claudeapi: decode content_block_start: %w", err)
		}
		ba := &blockAccumulator{}
		if len(e.ContentBlock) > 0 {
			_ = json.Unmarshal(e.ContentBlock, &ba.base)
			var t struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(e.ContentBlock, &t)
			ba.typ = t.Type
		}
		s.blocks[e.Index] = ba
		ev.Index = e.Index
	case "content_block_delta":
		var e struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				Signature   string `json:"signature"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return fmt.Errorf("claudeapi: decode content_block_delta: %w", err)
		}
		ev.Index = e.Index
		ba := s.blocks[e.Index]
		switch e.Delta.Type {
		case "text_delta":
			if ba != nil {
				ba.text.WriteString(e.Delta.Text)
			}
			ev.TextDelta = e.Delta.Text
		case "thinking_delta":
			if ba != nil {
				ba.thinking.WriteString(e.Delta.Thinking)
			}
			ev.ThinkingDelta = e.Delta.Thinking
		case "input_json_delta":
			if ba != nil {
				ba.partialJSON.WriteString(e.Delta.PartialJSON)
			}
		case "signature_delta":
			if ba != nil {
				ba.signature = e.Delta.Signature
				ba.hasSig = true
			}
		}
	case "content_block_stop":
		var e struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return fmt.Errorf("claudeapi: decode content_block_stop: %w", err)
		}
		ev.Index = e.Index
		if ba := s.blocks[e.Index]; ba != nil {
			s.resp.Content = append(s.resp.Content, ba.finalize())
			delete(s.blocks, e.Index)
		}
	case "message_delta":
		var e struct {
			Delta struct {
				StopReason  string       `json:"stop_reason"`
				StopDetails *StopDetails `json:"stop_details"`
			} `json:"delta"`
			Usage *MessageUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return fmt.Errorf("claudeapi: decode message_delta: %w", err)
		}
		if e.Delta.StopReason != "" {
			s.resp.StopReason = e.Delta.StopReason
		}
		if e.Delta.StopDetails != nil {
			s.resp.StopDetails = e.Delta.StopDetails
		}
		if e.Usage != nil {
			mergeStreamUsage(&s.resp.Usage, *e.Usage)
		}
	case "error":
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(payload), &e)
		return &StreamError{Type: e.Error.Type, Message: e.Error.Message}
	case "message_stop", "ping":
		// terminal / keep-alive — nothing to accumulate.
	default:
		// Unknown event type — ignore (versioning policy).
	}
	if onEvent != nil {
		return onEvent(ev)
	}
	return nil
}

// mergeStreamUsage folds a message_delta usage object into the accumulating usage. The
// streamed message_delta usage is CUMULATIVE and authoritative for the running counts,
// so OutputTokens is OVERWRITTEN with the latest value (even when it lowers the small
// message_start placeholder — a pre-output refusal legitimately settles at 0, which is
// exactly what IsBillable reads). The input/cache/tier/geo/details/iterations fields are
// taken from the delta when it carries them (the final delta of a tool-using turn
// restates the full input), else the message_start values stand.
func mergeStreamUsage(dst *MessageUsage, src MessageUsage) {
	dst.OutputTokens = src.OutputTokens
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.CacheCreation != nil {
		dst.CacheCreation = src.CacheCreation
	}
	if src.ServiceTier != "" {
		dst.ServiceTier = src.ServiceTier
	}
	if src.InferenceGeo != "" {
		dst.InferenceGeo = src.InferenceGeo
	}
	if src.OutputTokensDetails != nil {
		dst.OutputTokensDetails = src.OutputTokensDetails
	}
	if len(src.Iterations) > 0 {
		dst.Iterations = src.Iterations
	}
}
