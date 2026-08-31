// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// Forwarder is the seam for PUSHING the tamper-evident ledger off-box to a SIEM /
// OTLP collector (OBS-08(B)) — the inverse of the /v1/audit/export pull. A Forwarder
// receives each sealed AuditEvent and ships it in the operator's chosen SIEM format
// (CEF/LEEF/syslog/OCSF plus the OTLP tokens: `otlp` and its exact byte-for-byte alias
// `otlp_envelope` carry the complete, postable ExportLogsServiceRequest, while
// `otlp_log_record` carries the bare LogRecord projection — pull-export only, because a
// bare record is not a postable body, which is why the push renderer validates a stored
// format against the EVENTING-SINK vocabulary and not against the wider ledger set that
// FormatEvent's encoder serves; both live in sdk/siemwire) WITHOUT re-deriving or
// altering the hash chain: the integrity fields it carries (Seq/PrevHash/Hash/Sig) are
// emitted verbatim, so the consumer can check the chain linkage and a checkpoint
// signature offline, exactly as much as the pull export allows — and no more:
// re-deriving a record's hash from its exported content is NOT possible from either,
// because the stored MetaDigest is not projected (the canonical occurred_at text IS,
// since the dialect freeze; see the format doc in export.go). The archive is the
// artifact that carries every hash input. A Forwarder must never mutate ev.
//
// Makes the push REAL. The original blocker — "no server→collector transport"
// (deferred to CB-1) — is gone: the eventing platform provides a durable,
// SSRF-guarded, HMAC-signed, retrying/replaying/dead-lettering HTTP transport. The
// concrete Forwarder lives at the modules layer (modules/siemforward — core may not
// import modules), driven by a leader-gated cursor walk over the ledger that hands
// each sealed record to the eventing engine for SIEM control-tower delivery. core
// keeps only this interface and the NopForwarder default; the composition root wires
// the real one.
type Forwarder interface {
	Forward(ctx context.Context, ev model.AuditEvent) error
}

// NopForwarder is the default Forwarder for a deployment that forwards no ledger
// (the pull export remains the supported path). It accepts and drops every event
// (never mutating it), so the composition root can wire the seam unconditionally; the
// real, eventing-backed forwarder (modules/siemforward) replaces it when SIEM
// forwarding is configured.
type NopForwarder struct{}

// Forward discards the event; the pull export remains the supported path.
func (NopForwarder) Forward(context.Context, model.AuditEvent) error { return nil }

// Compile-time proof NopForwarder satisfies the seam.
var _ Forwarder = NopForwarder{}
