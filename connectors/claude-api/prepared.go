// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// prepared.go — the FROZEN, opaque forward artifact (F3, decision↔bytes binding).
//
// A governed decider normalizes and governs a request, then FREEZES the exact wire bytes it
// authorized into a PreparedRequest/PreparedBatch. The proxy forwards that artifact VERBATIM,
// with NO further preflight and NO re-marshal — so the octets sent upstream are provably the
// octets the decision was taken over and the ledger digest committed to. This closes the
// confused-deputy where preflight (inference.go) re-mutated the request AFTER governance and
// the forwarded bytes diverged from both the governed request and the recorded digest.
//
// The artifact is content-addressed: SHA256(Body) is the EffectiveRequestDigest (sdk/pdp.go).
// The Body is a defensive copy the caller cannot mutate after freezing; Beta/Stream travel
// with it because they alter request semantics even though Stream is in the body and the beta
// headers are not in the JSON at all.
package claudeapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
)

// PreparedRequest is a frozen /v1/messages artifact: the exact JSON body to forward, its
// stream flag, and the anthropic-beta header set derived from the governed request. The zero
// value is not forwardable (empty body); build it with MarshalPrepared.
type PreparedRequest struct {
	body   []byte
	stream bool
	beta   []string
}

// PreparedBatch is a frozen /v1/messages/batches submission: the exact envelope body to
// forward. Build it with MarshalPreparedBatch.
type PreparedBatch struct {
	body []byte
}

// MarshalPrepared serializes the governed request ONCE and freezes it. req MUST already be the
// effective request (NormalizeMessageRequest applied and any post-govern rewrites validated by
// ValidateForwardable) — this function does NOT normalize or validate; it only captures bytes.
// The returned artifact owns a private copy of the body, so a later mutation of req cannot
// change what is forwarded or digested.
func MarshalPrepared(req MessageRequest) (PreparedRequest, error) {
	buf, err := json.Marshal(req)
	if err != nil {
		return PreparedRequest{}, err
	}
	frozen := make([]byte, len(buf))
	copy(frozen, buf)
	return PreparedRequest{body: frozen, stream: req.Stream, beta: req.BetaHeaders()}, nil
}

// MarshalPreparedBatch serializes the governed batch submission ONCE and freezes the exact
// envelope bytes ({"requests":[...]}) the forward will send. requests MUST already be the
// governed, normalized entries.
func MarshalPreparedBatch(requests []BatchRequest) (PreparedBatch, error) {
	buf, err := json.Marshal(map[string]any{"requests": requests})
	if err != nil {
		return PreparedBatch{}, err
	}
	frozen := make([]byte, len(buf))
	copy(frozen, buf)
	return PreparedBatch{body: frozen}, nil
}

// Body returns a COPY of the frozen wire bytes (never the internal slice — the artifact stays
// immutable). Digest fingerprints these exact octets.
func (p PreparedRequest) Body() []byte {
	out := make([]byte, len(p.body))
	copy(out, p.body)
	return out
}

// Stream reports whether the frozen request is a stream:true request.
func (p PreparedRequest) Stream() bool { return p.stream }

// Digest is the EffectiveRequestDigest: SHA256 of the exact bytes that will be forwarded.
func (p PreparedRequest) Digest() [32]byte { return sha256.Sum256(p.body) }

// IsZero reports an unbuilt (non-forwardable) artifact.
func (p PreparedRequest) IsZero() bool { return len(p.body) == 0 }

// Body returns a COPY of the frozen batch envelope bytes.
func (p PreparedBatch) Body() []byte {
	out := make([]byte, len(p.body))
	copy(out, p.body)
	return out
}

// Digest is the EffectiveRequestDigest of the whole batch submission.
func (p PreparedBatch) Digest() [32]byte { return sha256.Sum256(p.body) }

// IsZero reports an unbuilt (non-forwardable) batch artifact.
func (p PreparedBatch) IsZero() bool { return len(p.body) == 0 }

// ForwardPrepared sends a frozen blocking /v1/messages artifact VERBATIM (no preflight, no
// re-marshal — the bytes go out exactly as frozen) and returns the decoded response. The body
// is transmitted as a json.RawMessage, so the transport's marshal is a byte-identity pass over
// the already-compact frozen bytes.
func (inf *Inference) ForwardPrepared(ctx context.Context, p PreparedRequest) (MessageResponse, error) {
	if inf.client == nil {
		return MessageResponse{}, ErrNotConfigured
	}
	if p.IsZero() {
		return MessageResponse{}, ErrNotConfigured
	}
	var resp MessageResponse
	if err := inf.client.PostJSON(ctx, messagesPath, json.RawMessage(p.body), &resp, betaHeaderMap(p.beta)); err != nil {
		return MessageResponse{}, err
	}
	return resp, nil
}

// ForwardPreparedStream sends a frozen streaming artifact VERBATIM and decodes the SSE
// response, invoking onEvent per event (the response half is unchanged from StreamMessage;
// only the request bytes are frozen).
func (inf *Inference) ForwardPreparedStream(ctx context.Context, p PreparedRequest, onEvent func(StreamEvent) error) (MessageResponse, error) {
	if inf.client == nil {
		return MessageResponse{}, ErrNotConfigured
	}
	if p.IsZero() {
		return MessageResponse{}, ErrNotConfigured
	}
	body, err := inf.client.PostStream(ctx, messagesPath, json.RawMessage(p.body), betaHeaderMap(p.beta))
	if err != nil {
		return MessageResponse{}, err
	}
	defer func() { _ = body.Close() }()
	return decodeMessageStream(body, onEvent)
}

// ForwardPreparedBatch submits a frozen batch envelope VERBATIM and returns both the decoded
// Batch (audit metadata) and the RAW upstream bytes (relayed verbatim), exactly like
// CreateBatchRaw — but with NO per-entry model-defaulting or re-marshal, so the forwarded
// bytes are the frozen, governed envelope.
func (inf *Inference) ForwardPreparedBatch(ctx context.Context, p PreparedBatch) (Batch, []byte, error) {
	if inf.client == nil {
		return Batch{}, nil, ErrNotConfigured
	}
	if p.IsZero() {
		return Batch{}, nil, ErrNotConfigured
	}
	raw, err := inf.client.PostJSONRaw(ctx, batchesPath, json.RawMessage(p.body), nil)
	if err != nil {
		return Batch{}, nil, err
	}
	var b Batch
	_ = json.Unmarshal(raw, &b) // best-effort: the relay uses raw; b carries only audit metadata
	return b, raw, nil
}
