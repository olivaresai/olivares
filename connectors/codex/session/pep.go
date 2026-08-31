// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// pep.go is the governed enforcement endpoint for Codex hook calls. It owns the wire
// protocol and the deny-closed defaults; the DECISION belongs to the injected Decider,
// which lives on the AGPL side because anchoring needs /core and this package may not
// import it.
//
// The split is not politeness. scripts/check-boundary.sh fails the build if a connectors/*
// package's transitive dependencies reach /core, so "the ledger entry is written by exactly
// one party" (R-01) is enforced by the compiler rather than by everyone remembering.

// Identity-hint headers the hook client stamps from its environment. Codex's hook payload
// carries no tenant/agent/org/account, so the client supplies them. They are HINTS: the
// authoritative principal is the bearer the decider resolves. A hint that disagrees with a
// policy requiring firm identity is the decider's problem to reject, not ours to trust.
const (
	hdrTenant  = "X-Olivares-Codex-Tenant"
	hdrAgent   = "X-Olivares-Codex-Agent"
	hdrOrg     = "X-Olivares-Codex-Org"
	hdrAccount = "X-Olivares-Codex-Account"
)

// Identity is the attribution context for one hook call.
type Identity struct {
	Tenant  string
	Agent   string
	Org     string
	Account string
}

// Request is the redacted, minimal-data view of one hook call that the governed decider
// sees. It carries NO raw tool arguments: only the derived, sanitized resource reference
// and the structural fields. The raw input is held privately and read only to derive that
// reference, so raw arguments never reach an audited or logged path.
type Request struct {
	Event string
	// ExternalSessionID is Codex's own UUIDv7. It is an ALIAS, never our key: see the
	// SG-00 contract and identity.go. It is what the decider resolves.
	ExternalSessionID string
	TurnID            string
	Tool              string
	// ToolUseID is Codex's per-call id (measured shape: "exec-<uuid>"). It is the precise
	// correlation join AND part of the idempotency key, so a retried delivery of the same
	// hook call cannot become a second recorded fact. It is present on the tool events and
	// ABSENT on the lifecycle ones — which is why PayloadDigest exists.
	ToolUseID    string
	ResourceKind string // shell | file | mcp.tool | codex.tool
	ResourceRef  string // sanitized, bounded
	Mode         string // read | write | unknown
	Model        string
	// PermissionMode is Codex's own posture for the call (default | acceptEdits | plan |
	// dontAsk | bypassPermissions). It is reported, never trusted as authorization.
	PermissionMode string
	Identity       Identity
	At             time.Time
	// PayloadDigest is a SHA-256 over the EXACT bytes Codex delivered. It is the
	// idempotency discriminator for every event that carries no tool_use_id: two
	// SessionStarts in one session (a startup and a resume) differ in their payload and so
	// must not collapse into one recorded fact, while a genuine REDELIVERY of the same
	// call is byte-identical and must. A server-side timestamp cannot do this job — a
	// redelivery would get a different one and defeat the whole guarantee.
	PayloadDigest string

	rawInput map[string]any
}

// RawInputKeys returns the top-level key names of the tool input, sorted-insensitive. It
// exists so a decider can reason about SHAPE without ever seeing values — the values are
// the part that can contain secrets.
func (r Request) RawInputKeys() []string {
	out := make([]string, 0, len(r.rawInput))
	for k := range r.rawInput {
		out = append(out, k)
	}
	return out
}

// Decider is the governed decision seam. The composition root implements it against the
// live PDP, the session identity plane and the tamper-evident ledger. bearer is the
// inbound credential; it is opaque here and MUST NOT be logged or stored. A nil decider,
// or any returned error, is a DENY.
type Decider interface {
	Decide(ctx context.Context, req Request, bearer string) (Decision, error)
}

// Observer receives one fact per governed hook call so the connector can lift it onto the
// bus. It is separate from Decider because emitting is the connector's job and deciding is
// not; keeping them apart is what stops the emit path from growing a second ledger write.
type Observer func(req Request, dec Decision)

// PEP is the HTTP surface the hook client posts to.
type PEP struct {
	decider Decider
	observe Observer
	now     func() time.Time
	maxBody int64
	// onEmitPanic reports a panic escaping the observer. nil = the loss is contained but
	// unreported, which is why the composition root should always set it.
	onEmitPanic func(event string, cause any)
}

// OnEmitPanic registers the sink for a panic escaping the observer. It exists so a
// swallowed emit is at least a LOGGED swallowed emit: a missing observation is a hole in
// the evidence, and a hole nobody is told about is the worst kind.
func (p *PEP) OnEmitPanic(fn func(event string, cause any)) { p.onEmitPanic = fn }

var _ http.Handler = (*PEP)(nil)

// NewPEP builds the endpoint. A nil decider is allowed and denies every call — a visible
// deny-closed posture, never a silent open door.
func NewPEP(d Decider, observe Observer, now func() time.Time) *PEP {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PEP{decider: d, observe: observe, now: now, maxBody: maxHookBody}
}

// ServeHTTP gates one Codex hook call. POST only. Anything it cannot understand is denied
// in the shape that event honors — never waved through, and never denied in a shape the
// event ignores, which would amount to the same thing.
func (p *PEP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, p.maxBody))
	if err != nil {
		p.write(w, "", DenyClosed("", "could not read the hook request body"))
		return
	}
	payload, ok := ParseHookPayload(body)
	if !ok {
		p.write(w, payload.HookEventName, DenyClosed(payload.HookEventName, "unreadable hook payload (deny-closed)"))
		return
	}
	if !IsKnownEvent(payload.HookEventName) {
		// Not an error: a Codex release that adds an event must not thereby acquire an
		// ungoverned path. Render.ExitCodeFor gives the client the second channel.
		p.write(w, payload.HookEventName, DenyClosed(payload.HookEventName, "unknown hook event (deny-closed)"))
		return
	}

	req := p.requestFrom(payload, r, body)

	if p.decider == nil {
		p.write(w, req.Event, DenyClosed(req.Event, "no governed decider is wired (deny-closed)"))
		return
	}
	dec, derr := p.decider.Decide(r.Context(), req, bearerOf(r))
	if derr != nil {
		p.write(w, req.Event, DenyClosed(req.Event, "the governed decision failed (deny-closed)"))
		return
	}
	// A decision that resolved no canonical session is not a session fact. We still answer
	// the verdict — the agent must be told — but nothing is emitted, because emitting with
	// an empty SessionRef writes a row the live view discards anyway (modules/sessions
	// live.go drops edges whose OriginRef is empty) and would look like a delivered fact.
	if p.observe != nil && dec.SessionSID != "" {
		p.emit(req, dec)
	}
	p.write(w, req.Event, Render(req.Event, dec))
}

// emit hands the fact to the observer with a recover around it.
//
// The reason is a failure mode worth naming: emitting is TELEMETRY and deciding is
// SAFETY, and they must not share a fate. Without this, a panic in the emit path would
// abort the handler before the verdict was written; the client reads an empty body as a
// fault and denies; and a legitimate ALLOW would become a DENY because a label was
// malformed. Failing closed is right when the GOVERNANCE is uncertain — not when the
// bookkeeping is.
//
// The loss is not swallowed silently either: an observation that never happened is a gap
// in the evidence, and the panic surfaces through the recover rather than being ignored.
func (p *PEP) emit(req Request, dec Decision) {
	defer func() {
		if r := recover(); r != nil && p.onEmitPanic != nil {
			p.onEmitPanic(req.Event, r)
		}
	}()
	p.observe(req, dec)
}

// requestFrom builds the redacted view. Every field it copies is structural; tool_input is
// parsed only to derive the sanitized resource reference.
func (p *PEP) requestFrom(h HookPayload, r *http.Request, raw []byte) Request {
	rawBody := raw
	input := map[string]any{}
	if len(h.ToolInput) > 0 {
		_ = json.Unmarshal(h.ToolInput, &input)
	}
	kind, ref, mode := resourceFromTool(h.ToolName, input)
	return Request{
		Event:             h.HookEventName,
		ExternalSessionID: h.SessionID,
		TurnID:            h.TurnID,
		Tool:              h.ToolName,
		ToolUseID:         h.ToolUseID,
		ResourceKind:      kind,
		ResourceRef:       ref,
		Mode:              mode,
		Model:             h.Model,
		PermissionMode:    h.PermissionMode,
		Identity: Identity{
			Tenant:  strings.TrimSpace(r.Header.Get(hdrTenant)),
			Agent:   strings.TrimSpace(r.Header.Get(hdrAgent)),
			Org:     strings.TrimSpace(r.Header.Get(hdrOrg)),
			Account: strings.TrimSpace(r.Header.Get(hdrAccount)),
		},
		At:            p.now(),
		PayloadDigest: payloadDigest(rawBody),
		rawInput:      input,
	}
}

// payloadDigest hashes the delivered bytes verbatim.
func payloadDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// write answers 200 with the decision in the body. The verdict travels in the BODY, not in
// the status code: the client relays the body to Codex verbatim, and a non-2xx would make
// the client synthesize its own deny-closed instead of relaying the governed one.
func (p *PEP) write(w http.ResponseWriter, _ string, out []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func bearerOf(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}
