// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// AuditEvent is one sealed record in the tamper-evident evidence ledger
// (docs/SECURITY-HARDENING.md). The ledger is append-only and hash-chained per tenant: each
// event carries the hash of the previous event, so any silent alteration or
// deletion breaks the chain and is detectable on Verify.
//
// AuditEvent deliberately does NOT embed BaseFields: it has no version, no
// updated_at and no deleted_at, because it is immutable. Its order is the
// per-tenant monotonic Seq, not a timestamp.
type AuditEvent struct {
	// ID is the event's external identifier (UUIDv7).
	ID ID
	// TenantID is the chain this event belongs to (the reserved system tenant
	// holds cross-tenant/system events).
	TenantID TenantID
	// Seq is the per-tenant, monotonic sequence number (starts at 1). The
	// numbering is contiguous except across a signed audit.gap marker, the only
	// sanctioned discontinuity: it declares evidence dropped under the explicit
	// degrade spool policy (ADR-0024 Q2). Hash linkage is continuous regardless.
	Seq int64
	// OccurredAt is the server-assigned event time (display/filter only; chain
	// order is Seq).
	OccurredAt Timestamp
	// Actor is the principal that caused the event (e.g. "user:<id>",
	// "agent:<id>", "connector:<name>", "system") — never a secret.
	Actor string
	// ActorKind classifies the actor.
	ActorKind string
	// Action is the verb (e.g. "agent.create", "access_edge.upsert").
	Action string
	// TargetKind is the entity kind acted on (or empty).
	TargetKind Kind
	// TargetID is the entity acted on (or zero).
	TargetID ID
	// Meta is small, already-redacted, non-sensitive context.
	//
	// It is populated on the APPEND side and deliberately nil on every read path:
	// the stored canonical string is authoritative, so the row decoder drops the
	// map rather than re-parse it into a value that could differ. Treat a nil Meta
	// on a walked or exported event as "not carried here", never as "empty".
	Meta map[string]any
	// MetaCommitment is the 32-byte commitment to the STORED canonical metadata
	// string — the hash input the projections need and the one field that lets a
	// consumer recompute this event's chain hash from a single exported line.
	//
	// Unlike Meta it is carried on BOTH sides: the append path computes it from the
	// bytes it is about to store, and the row decoder resolves it from the stored
	// metadata and the stored blind. It is content-opaque and fixed-width, so it is
	// safe to project off-box where Meta is not — but ONLY when MetaBlinded says it
	// is hiding; see that field.
	MetaCommitment []byte
	// MetaBlinded reports WHICH rule produced MetaCommitment: true for the blinded
	// commitment, false for the legacy unblinded digest of a row sealed before
	// blinding existed.
	//
	// It exists because the two are indistinguishable once derived — both are 32
	// opaque bytes — so an event that carried only the value would have lost the one
	// fact a projection needs. And the projections need it: the unblinded digest is
	// a deterministic function of the metadata alone, so exporting it hands the
	// holder of a line a confirmation oracle over guessable content (a login IP, a
	// denial reason) and an equality relation across records. A projection that
	// emitted the commitment whenever it was non-empty would therefore leak exactly
	// on the records that predate the protection.
	//
	// The blind ITSELF is deliberately not a field here. A boolean is the whole of
	// what a reader needs, and carrying the blind would put the hiding material into
	// every value that flows to a sink. The one artifact that carries the blind is
	// the complete archive, which carries it as its own line field.
	MetaBlinded bool
	// PayloadHash is the SHA-256 of the canonical redacted payload — never the
	// raw payload (docs/SECURITY-HARDENING.md). 32 bytes.
	PayloadHash []byte
	// PrevHash is the chain hash of the previous event (all-zero at Seq 1).
	PrevHash []byte
	// Hash is this event's chain hash (docs/SECURITY-HARDENING.md).
	Hash []byte
	// Sig is an optional Ed25519 signature over Hash at checkpoints (nil if
	// unsigned).
	Sig []byte
}

// AuditDraft is the input to AuditLog.Append. It carries an already-redacted
// payload hash and non-sensitive meta — there is intentionally no field through
// which a raw secret or PII could enter the ledger (docs/SECURITY-HARDENING.md). The store
// assigns ID, Seq, OccurredAt, PrevHash and Hash.
type AuditDraft struct {
	// Actor is the principal that caused the event.
	Actor string
	// ActorKind classifies the actor.
	ActorKind string
	// Action is the verb.
	Action string
	// TargetKind is the entity kind acted on (or empty).
	TargetKind Kind
	// TargetID is the entity acted on (or zero).
	TargetID ID
	// PayloadHash is the SHA-256 of the canonical redacted payload (32 bytes, or
	// nil for events with no payload).
	PayloadHash []byte
	// Meta is small, already-redacted, non-sensitive context.
	Meta map[string]any
	// Sig is an optional detached Ed25519 signature carried onto the sealed
	// event's Sig column (docs/SECURITY-HARDENING.md). It is deliberately OUTSIDE the hash
	// preimage (see canon), so signing a checkpoint cannot perturb the very hash
	// it attests. Empty for ordinary events; set only by the audit checkpoint
	// path (core/audit), which signs the attested head hash.
	Sig []byte
}

// Actor kinds for AuditEvent.ActorKind / AuditDraft.ActorKind.
const (
	// ActorUser is a human operator.
	ActorUser = "user"
	// ActorAgent is an AI agent.
	ActorAgent = "agent"
	// ActorConnector is a collector/connector.
	ActorConnector = "connector"
	// ActorSystem is the engine itself (migrations, provisioning, verify).
	ActorSystem = "system"
)
