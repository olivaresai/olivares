// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// Audit integrity action names. These are string twins of the core/audit
// constants because store cannot import core/audit without creating a cycle.
const (
	ActionAuditCheckpoint     = "audit.checkpoint"
	ActionAuditArchiveSegment = "audit.archive.segment"
	ActionAuditGap            = "audit.gap"
	ActionAuditRecover        = "audit.recover"
	// ActionAuditKeyRotation marks a signing-key epoch boundary (F-07): an
	// off-box-signed marker that records the retiring key's fingerprint and the
	// last per-tenant sequence it legitimately signed, so the per-event verifier
	// can FENCE a retired key to its epoch (audit.VerifyEventsFenced) instead of
	// trusting any current-or-prior key for any sequence forever. Like
	// ActionAuditRecover it is structural and off-box signed; an unsigned draft
	// naming it is a forged marker the engine refuses (Append).
	ActionAuditKeyRotation = "audit.key.rotation"
)

// Declared-gap metadata is a pinned vocabulary shared by the live-chain and
// archive verifiers. Changing these keys would make existing signed markers
// uninterpretable, so additions require a versioned verifier contract.
const (
	GapMetaFromSeq = "gap.from_seq"
	GapMetaToSeq   = "gap.to_seq"
	GapMetaCount   = "gap.count"
	GapMetaReason  = "gap.reason"
	GapMetaAt      = "gap.at"

	GapReasonSpoolFull = "spool_full"
)

// AuditSpoolStatus is the operator-visible state of a configured audit spool
// budget (ADR-0024 Q2).
type AuditSpoolStatus struct {
	// MaxBytes is the declared logical audit spool budget.
	MaxBytes int64
	// OnFull is the effective exhaustion policy (an unset mode reports block).
	OnFull AuditSpoolMode
	// UsedBytes is the exact incremental logical-byte counter — the same value
	// the writer's guard compares, not a periodic measurement.
	UsedBytes int64
	// Engaged reports whether the budget is currently met or exceeded, i.e. a
	// non-exempt governed write would be refused (block) or dropped (degrade).
	Engaged bool
	// PendingDropTenants is the number of tenants with unsealed evidence loss.
	PendingDropTenants int
	// PendingDrops is the number of evidence losses recorded in degrade mode
	// that have not yet been sealed as in-chain audit.gap markers.
	PendingDrops int64
}

// AuditSpoolStatuser is an optional Store capability asserted at the
// observability edge. The bool is false when no budget is configured, so the
// caller stays silent; store decorators must forward this capability.
type AuditSpoolStatuser interface {
	AuditSpoolStatus(ctx context.Context) (AuditSpoolStatus, bool, error)
}

// DeclaresGap reports whether metaCanonical declares exactly the sanctioned
// sequence hole [expectedSeq, markerSeq-1]. JSON numbers remain exact and any
// trailing data invalidates the declaration.
func DeclaresGap(metaCanonical string, expectedSeq, markerSeq int64) bool {
	if markerSeq <= expectedSeq {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(metaCanonical))
	dec.UseNumber()
	var meta map[string]any
	if err := dec.Decode(&meta); err != nil {
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false
	}
	from, ok := gapMetaInt64(meta, GapMetaFromSeq)
	if !ok {
		return false
	}
	to, ok := gapMetaInt64(meta, GapMetaToSeq)
	if !ok {
		return false
	}
	count, ok := gapMetaInt64(meta, GapMetaCount)
	if !ok || count < 1 {
		return false
	}
	return from == expectedSeq && to == markerSeq-1 && count == markerSeq-expectedSeq
}

func gapMetaInt64(meta map[string]any, key string) (int64, bool) {
	n, ok := meta[key].(json.Number)
	if !ok {
		return 0, false
	}
	v, err := n.Int64()
	return v, err == nil
}

// AuditLog is the append-only, hash-chained evidence ledger for the bound
// tenant (docs/SECURITY-HARDENING.md). It exposes no update or delete: the ledger is immutable
// by contract, and the engine enforces that in the database too (immutability
// triggers and, on Postgres, revoked UPDATE/DELETE grants).
type AuditLog interface {
	// Append seals one event: it assigns the next per-tenant sequence number,
	// links it to the previous event's hash, computes this event's hash, and
	// inserts it — all in the caller's transaction. The tenant is pinned by the
	// Scope. Under the explicit degrade policy, a zero-valued event with a nil
	// error means the evidence was dropped; callers that require the persisted
	// event must check ev.Seq != 0.
	Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error)
	// Verify walks the tenant's live chain from fromSeq and reports the first
	// structural break. Sanctioned sequence holes are accepted only through an
	// in-chain audit.gap marker; their authenticity is proved separately by the
	// marker's Ed25519 signature in audit.VerifyEvents/the archive verifier with
	// pinned keys. The olivares audit verify command runs all three checks.
	Verify(ctx context.Context, fromSeq int64) (VerifyReport, error)
	// Walk streams the tenant's events in sequence order, starting at fromSeq.
	Walk(ctx context.Context, fromSeq int64, fn func(model.AuditEvent) error) error
	// Head returns the current chain tip (highest sequence number and its hash)
	// for the bound tenant, or ok=false for an empty chain. It is what the audit
	// checkpoint signer notarizes (core/audit).
	Head(ctx context.Context) (HeadRef, bool, error)
}

// RecordedHeadReader is an OPTIONAL AuditLog capability: the chain tip the store
// RECORDS for the bound tenant (the audit_heads row), as opposed to Head, which
// reports the tip it can still SEE (the last surviving event row).
//
// The two agree on every healthy ledger and disagree in exactly one situation
// worth naming: the events are gone while the recorded head survives — a
// TRUNCATE, a wholesale DELETE, a botched restore. Head alone cannot express
// that, because "no events" is also what a brand-new tenant looks like, so a
// caller holding only Head must treat an emptied ledger as an empty one. That is
// precisely how a truncated chain used to pass through the scheduled
// checkpointer without a sound.
//
// A store that cannot distinguish the two simply does not implement this, and
// callers keep their previous behavior.
type RecordedHeadReader interface {
	// RecordedHead returns the tenant's recorded chain tip, or ok=false when the
	// store has never recorded one (a tenant with no events ever written).
	RecordedHead(ctx context.Context) (HeadRef, bool, error)
}

// AuditAppendLocker is an optional AuditLog capability for ceremonies that
// must take a final database-time observation and append without another writer
// interleaving or waiting on a later internal append lock. The lock is scoped to
// the surrounding Store.Mutate transaction: Postgres implementations serialize
// on the same per-tenant advisory lock and, when configured, the global spool
// budget row used by Append; SQLite is already single-writer and may implement
// this as a no-op.
// Correctness must not depend on this liveness aid: callers still validate the
// sequence assigned by Append before allowing the transaction to commit.
type AuditAppendLocker interface {
	LockAppends(ctx context.Context) error
}

// CanonicalWalker is an OPTIONAL capability of an AuditLog: Walk plus each
// event's STORED canonical meta string AND the stored blind of its metadata
// commitment. The chain hash commits to the meta through that commitment over the
// stored canonical string — never the in-memory map — so the stored string is the
// one authoritative input (core/internal/store/canon). The blind travels with it
// because a copy that carries the metadata but not its blind could recompute
// nothing: the commitment in the chain would be unverifiable against the metadata
// beside it. Together they are what makes an external copy a COMPLETE artifact.
// Walk deliberately drops it (callers get a nil Meta and re-parse if needed),
// which is why an external copy made through Walk could NOT re-derive
// canon.EventHash offline. Archival export (core/audit) type-asserts
// this capability to write a copy that re-verifies byte-for-byte; the ledger
// itself does not change — no schema, hash or signature is touched, the read
// just stops losing fidelity.
type CanonicalWalker interface {
	// WalkCanonical is Walk + the stored canonical meta string and metadata blind
	// per event, in sequence order from fromSeq. metaCanonical is the exact bytes
	// the commitment covers (at minimum the literal "{}"); metaBlind is the
	// record's 32-byte blind, or nil for a record sealed before blinding existed —
	// in which case the commitment follows the legacy unblinded rule, and nil is
	// the discriminator that says so (core/internal/store/canon.MetaCommitmentFor).
	WalkCanonical(ctx context.Context, fromSeq int64, fn func(ev model.AuditEvent, metaCanonical string, metaBlind []byte) error) error
}

// VerifiedAuditAnchorReader is an OPTIONAL, bounded audit capability for a
// durable receipt that already names one exact sequence. It reads that event
// and its immediate predecessor in one store snapshot, recomputes both
// canonical hashes, verifies the link, and returns the target's stored
// canonical metadata. It deliberately does not expose a range walk or make an
// old receipt depend on unrelated events appended after its anchor.
type VerifiedAuditAnchorReader interface {
	ReadVerifiedAuditAnchor(
		ctx context.Context,
		seq int64,
	) (event model.AuditEvent, metaCanonical string, found bool, err error)
}

// AuditEventSigner, when configured on a Store (Config.SignEvent), signs every
// appended event after its chain hash is computed. The returned bytes are stored
// as the event's detached Ed25519 signature — excluded from the chain-hash
// preimage by design (see core/internal/store/canon) — so an external verifier
// holding the OFF-BOX public key can confirm each event individually. This closes
// the residual where the ledger tail could be rewritten between the periodic
// signed checkpoints (or after deleting them): every event is its own anchor, and
// changing one requires the private key, not merely raw DB write. The concrete
// signer lives in core/audit and is injected by the composition root, keeping this
// package free of a crypto dependency. nil disables per-event signing (the chain
// + signed checkpoints still apply). It is never called for an event whose draft
// already carries a signature (a checkpoint), so the two signature domains never
// collide. Honest limit: an attacker who also holds the local signing key (full
// data-dir compromise) can re-sign; per-event signatures defend against DB-only
// compromise (injection, stolen backup/replica, an RLS-bypass role) and against
// checkpoint deletion — for the host-compromise case the off-box/HSM-backed signer
// seam plus off-box public-key verification remains the control (docs/SECURITY-HARDENING.md).
type AuditEventSigner func(tenantID string, seq int64, hash []byte) []byte

// HeadRef is a reference to a chain tip: a sequence number and its hash.
type HeadRef struct {
	// Seq is the tip's per-tenant sequence number.
	Seq int64
	// Hash is the tip event's chain hash.
	Hash []byte
}

// VerifyReport is the outcome of verifying a hash chain.
type VerifyReport struct {
	// OK is true when the whole checked range is intact.
	OK bool
	// Checked is the number of events verified.
	Checked int64
	// DeclaredGaps is the number of sanctioned, marker-declared holes crossed.
	// Declared gaps do not fail structural verification.
	DeclaredGaps int64
	// BreakAt is the sequence number of the first inconsistency, or 0 if none.
	BreakAt int64
	// Reason describes the first inconsistency (e.g. "hash-mismatch",
	// "prev-mismatch", "seq-gap", "gap-mismatch", "sig-invalid"), or "" if
	// intact.
	Reason string
}
