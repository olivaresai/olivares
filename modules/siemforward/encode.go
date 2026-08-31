// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package siemforward is the driver that ships the audit ledger and Findings to
// SIEM control towers OVER the eventing platform. It owns no delivery engine of
// its own: it (1) implements eventing.SinkRenderer, re-shaping a captured event into
// a control tower's native dialect (OCSF 1.8 ai_operation / CEF / LEEF / syslog /
// OTLP) and envelope (connectors/siemsink), and (2) walks the tamper-evident ledger
// from a per-tenant cursor and hands each sealed record to eventing.IngestAudit so it
// rides the SAME durable retry/backoff/DLQ/replay/kill-switch machinery. The ledger
// body is core/audit.FormatEvent (the SAME encoder the pull export uses, so the
// pushed bytes never drift from the pulled bytes); the findings body is the siemfmt
// encoders behind connectors/siemsink. Integrity fields (Seq/PrevHash/Hash/Sig) ride
// verbatim.
//
// Layering note (the Go internal rule): the SIEM dialect encoders live under
// connectors/internal/siemfmt and may be reached ONLY from connectors/*, so the
// findings encoding is delegated to the public connectors/siemsink wrapper; the
// ledger encoder core/audit.FormatEvent may be reached ONLY from core/modules/cmd, so
// it is called here. This module (modules/*) is the single layer that may bridge both
// halves.
package siemforward

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// auditWire is the format-neutral JSON shape of a sealed ledger record carried in
// the eventing event payload (built by the ledger forwarder, decoded by the
// renderer). Hashes are hex, the signature base64 — exactly as the pull export
// emits them — so the integrity fields survive the JSON round trip untouched and
// the renderer can reconstruct a model.AuditEvent and call the SAME FormatEvent the
// pull export uses (no re-derivation; docs/SECURITY-HARDENING.md "no format drift").
// metaCommitmentSchemeBlindedV1 names the blinded metadata-commitment rule on the
// wire. It is the only scheme this build emits; the legacy unblinded rule has no
// name because a record sealed under it carries no commitment off-box at all.
const metaCommitmentSchemeBlindedV1 = "blinded-v1"

type auditWire struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	Seq        int64  `json:"seq"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	// MetaCommitment is the blinded commitment to the record's stored metadata,
	// hex. It is carried VERBATIM and can never be recomputed here: the modules
	// layer cannot import the canonical package, and the blind that produced it
	// stays in the store. Omitting it when present would make the pushed bytes
	// differ from the pulled bytes for the same sealed event — the invariant this
	// file exists to hold.
	//
	// OMITEMPTY IS LOAD-BEARING, and this DTO is DURABLE, not a transient wire
	// shape: the eventing intake persists the marshaled payload and the dispatcher
	// re-reads it at claim time, so payloads written before this field existed are
	// still queued and still replayable. An absent key therefore means "a record
	// sealed before metadata blinding" — the same discriminator the ledger row uses
	// — and MUST decode successfully. Rejecting it would burn each delivery's retry
	// ladder into the dead-letter queue, and the forward cursor has already moved
	// past those records while the intake dedups by event id, so nothing would ever
	// re-enqueue them: evidence already captured would be lost permanently.
	MetaCommitment string `json:"meta_commitment,omitempty"`
	// MetaCommitmentScheme names the RULE that produced MetaCommitment, because the
	// value alone cannot say: the blinded commitment and the legacy unblinded digest
	// are both 32 opaque bytes. Inferring "blinded" from mere presence is wrong for
	// any payload written before the encoder learned to omit the legacy digest —
	// such a payload carries the dictionary-checkable value, and presence-inference
	// would faithfully forward it.
	//
	// A payload with a commitment but NO scheme is therefore irresolvably ambiguous
	// and is delivered WITHOUT the field. That is a deliberate trade: it costs
	// offline reconstruction for a handful of already-queued records, and it refuses
	// to gamble a metadata disclosure on the guess.
	MetaCommitmentScheme string `json:"meta_commitment_scheme,omitempty"`
	PayloadHash          string `json:"payload_hash"`
	PrevHash             string `json:"prev_hash"`
	Hash                 string `json:"hash"`
	Sig                  string `json:"sig"`
}

// auditWireFrom projects a sealed AuditEvent into the wire DTO (hex/base64 for the
// byte fields). It never includes Meta: FormatEvent (the encoder both halves use)
// does not emit Meta, so omitting it keeps the pushed bytes byte-identical to the
// pulled bytes and avoids carrying RTBF-sensitive context off-box. The metadata
// COMMITMENT is a different matter and must travel: it is content-opaque and
// fixed-width, the projections emit it, and a DTO that dropped it would render a
// different line than the pull export for the same event.
func auditWireFrom(ev model.AuditEvent) auditWire {
	// The commitment travels only when it is HIDING. A pre-blinding row arrives
	// here carrying its legacy unblinded digest, and this DTO is PERSISTED by the
	// eventing intake before any sink sees it — so encoding it unconditionally
	// would write a dictionary-checkable digest of that row's metadata into the
	// queue at rest, and ship it to every sink whose format passes the payload
	// through. Absence is the same discriminator the ledger row and the pull export
	// use, which is what keeps the pushed bytes identical to the pulled bytes.
	commitment, scheme := "", ""
	if ev.MetaBlinded {
		commitment, scheme = hex.EncodeToString(ev.MetaCommitment), metaCommitmentSchemeBlindedV1
	}
	return auditWire{
		ID:                   ev.ID.String(),
		TenantID:             ev.TenantID.String(),
		Seq:                  ev.Seq,
		OccurredAt:           ev.OccurredAt.String(),
		Actor:                ev.Actor,
		ActorKind:            ev.ActorKind,
		Action:               ev.Action,
		TargetKind:           string(ev.TargetKind),
		TargetID:             ev.TargetID.String(),
		MetaCommitment:       commitment,
		MetaCommitmentScheme: scheme,
		PayloadHash:          hex.EncodeToString(ev.PayloadHash),
		PrevHash:             hex.EncodeToString(ev.PrevHash),
		Hash:                 hex.EncodeToString(ev.Hash),
		Sig:                  base64.StdEncoding.EncodeToString(ev.Sig),
	}
}

// toEvent reconstructs a model.AuditEvent from the wire DTO so the renderer can call
// FormatEvent. The integrity bytes are decoded verbatim; a malformed field is an
// error (the delivery dead-letters honestly rather than ship a corrupt record).
func (w auditWire) toEvent() (model.AuditEvent, error) {
	prev, err := hex.DecodeString(w.PrevHash)
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("siemforward: bad prev_hash: %w", err)
	}
	h, err := hex.DecodeString(w.Hash)
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("siemforward: bad hash: %w", err)
	}
	ph, err := hex.DecodeString(w.PayloadHash)
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("siemforward: bad payload_hash: %w", err)
	}
	// ABSENT is legal and meaningful (see the field comment); WRONG-WIDTH is not. A
	// present-but-short value would still render a well-formed line whose
	// reconstruction fails at the consumer, which reads as tampering — dead-lettering
	// here names the real cause instead.
	mc, err := hex.DecodeString(w.MetaCommitment)
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("siemforward: bad meta_commitment: %w", err)
	}
	if len(mc) != 0 && len(mc) != sha256.Size {
		return model.AuditEvent{}, fmt.Errorf("siemforward: meta_commitment is %d bytes, want 0 or %d", len(mc), sha256.Size)
	}
	blinded := false
	switch w.MetaCommitmentScheme {
	case "":
		// No scheme: either a record with no commitment at all (a legacy row, the
		// ordinary case) or a payload from before the scheme existed. In the second
		// case the value cannot be told apart from a legacy digest, so it is dropped
		// rather than forwarded — see the field comment.
		mc = nil
	case metaCommitmentSchemeBlindedV1:
		if len(mc) != sha256.Size {
			return model.AuditEvent{}, fmt.Errorf("siemforward: meta_commitment_scheme %q with a %d-byte commitment, want %d",
				w.MetaCommitmentScheme, len(mc), sha256.Size)
		}
		blinded = true
	default:
		// An unknown scheme is a rule this build cannot reason about. Guessing would
		// mean publishing a value under a rule nobody here can name.
		return model.AuditEvent{}, fmt.Errorf("siemforward: unknown meta_commitment_scheme %q", w.MetaCommitmentScheme)
	}
	sig, err := base64.StdEncoding.DecodeString(w.Sig)
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("siemforward: bad sig: %w", err)
	}
	occurred, err := model.ParseTimestamp(w.OccurredAt)
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("siemforward: bad occurred_at: %w", err)
	}
	return model.AuditEvent{
		ID:             model.ID(w.ID),
		TenantID:       model.TenantID(w.TenantID),
		Seq:            w.Seq,
		OccurredAt:     occurred,
		Actor:          w.Actor,
		ActorKind:      w.ActorKind,
		Action:         w.Action,
		TargetKind:     model.Kind(w.TargetKind),
		TargetID:       model.ID(w.TargetID),
		MetaCommitment: mc,
		MetaBlinded:    blinded,
		PayloadHash:    ph,
		PrevHash:       prev,
		Hash:           h,
		Sig:            sig,
	}, nil
}

// isTextFormat reports whether a format is a raw SIEM text line (CEF/LEEF/syslog)
// rather than a JSON document (OCSF, either OTLP form, json). The text set is listed
// rather than the JSON set on purpose: every format added since has been JSON, so the
// closed list that must be maintained is the short one. modules/siemforward's bridge
// tests iterate core/audit.Formats() across both branches, so a future TEXT format
// that forgot this function fails there rather than silently shipping as JSON.
func isTextFormat(format string) bool {
	switch format {
	case "cef", "leef", "syslog":
		return true
	default:
		return false
	}
}

// auditBody shapes a ledger record (the auditWire payload) into the chosen dialect
// via core/audit.FormatEvent — the SAME encoder the pull export uses, so the pushed
// bytes never drift from the pulled bytes and the integrity fields ride verbatim. It
// returns the body, whether the body is JSON, a scalar message and the sink tags.
func auditBody(payload []byte, format string) (body []byte, isJSON bool, message string, tags map[string]string, err error) {
	var w auditWire
	if uerr := json.Unmarshal(payload, &w); uerr != nil {
		return nil, false, "", nil, fmt.Errorf("siemforward: decode audit payload: %w", uerr)
	}
	if format == "" {
		// The catalog's eventing-surface default (OCSF, the AI-aware schema);
		// json is intercepted upstream. Derived, not copied, so this site cannot
		// drift from the surface contract.
		format = string(siemwire.EventingSinkFormats().Default())
	}
	ev, terr := w.toEvent()
	if terr != nil {
		return nil, false, "", nil, terr
	}
	s, ferr := audit.FormatEvent(ev, audit.Format(format))
	if ferr != nil {
		return nil, false, "", nil, fmt.Errorf("siemforward: encode audit %q: %w", format, ferr)
	}
	tags = map[string]string{
		"tenant":     w.TenantID,
		"seq":        strconv.FormatInt(w.Seq, 10),
		"actor_kind": w.ActorKind,
		"event_type": "audit.recorded",
	}
	return []byte(s), !isTextFormat(format), w.Action, tags, nil
}

// findingNotification maps a finding.reported payload onto a minimal-data
// sdk.Notification (the siemfmt encoders' input) plus its sink tags. The detail is
// never included beyond its hash.
func findingNotification(payload []byte, tenant string, fallbackTime time.Time) (sdk.Notification, map[string]string, error) {
	var fr sdkmodel.FindingReport
	if err := json.Unmarshal(payload, &fr); err != nil {
		return sdk.Notification{}, nil, fmt.Errorf("siemforward: decode finding payload: %w", err)
	}
	occurred := fr.OccurredAt
	if occurred.IsZero() {
		occurred = fallbackTime
	}
	n := sdk.Notification{
		Type:     "finding.reported",
		Title:    fr.Title,
		Severity: fr.Severity,
		Tenant:   tenant,
		Time:     occurred,
		Fields:   findingFields(fr),
	}
	tags := map[string]string{
		"tenant":     tenant,
		"event_type": "finding.reported",
		"kind":       fr.Kind,
	}
	if s := string(fr.Severity); s != "" {
		tags["severity"] = s
	}
	return n, tags, nil
}

// findingFields builds the minimal-data Notification.Fields for a finding (refs,
// hashes and taxonomy only — never the raw detail).
func findingFields(fr sdkmodel.FindingReport) map[string]string {
	f := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			f[k] = v
		}
	}
	put("kind", fr.Kind)
	put("subject_kind", fr.SubjectKind)
	put("subject_ref", fr.SubjectRef)
	put("detail_hash", fr.DetailHash)
	put("owasp_llm", strings.Join(fr.OWASPLLM, ","))
	put("owasp_asi", strings.Join(fr.OWASPASI, ","))
	put("atlas", strings.Join(fr.ATLAS, ","))
	return f
}

// genericEvent is the structured-JSON projection used for the "json" format and for
// events without a dedicated encoder: the minimal-data event envelope (no raw
// payload beyond what the bus already carried).
type genericEvent struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Tenant  string          `json:"tenant"`
	Source  string          `json:"source,omitempty"`
	Time    string          `json:"time,omitempty"`
	Seq     int64           `json:"seq,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// genericNotification builds a best-effort Notification from any other cataloged
// event's flat payload fields, so a CEF/LEEF/OCSF shop still gets a record.
func genericNotification(ev genericEvent) (sdk.Notification, map[string]string) {
	n := sdk.Notification{Type: ev.Type, Title: ev.Type, Tenant: ev.Tenant, Fields: flatFields(ev.Payload)}
	if t, err := time.Parse(time.RFC3339, ev.Time); err == nil {
		n.Time = t
	}
	return n, map[string]string{"tenant": ev.Tenant, "event_type": ev.Type}
}

// flatFields decodes a payload object into a flat string map (best-effort), so a
// generic event's structural fields survive into a SIEM line. Non-string values are
// stringified; nested objects are skipped (the bus payload is already flat refs).
func flatFields(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		switch s := v.(type) {
		case string:
			out[k] = s
		case float64:
			out[k] = strconv.FormatFloat(s, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
