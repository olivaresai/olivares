// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// eventDomain binds the per-event signature preimage to its purpose and version,
// so a per-event signature can never be confused with a checkpoint signature (or
// any other Ed25519 signature the engine produces). It is distinct from
// checkpointDomain by design.
const eventDomain = "olivares.audit.event.v1"

// SignEvent produces the detached Ed25519 signature for one audit event, over the
// canonical (tenant, seq, hash) preimage. It is the store.AuditEventSigner the
// composition root injects via store.Config.SignEvent so EVERY appended event is
// signed at write time (not only the periodic checkpoints). The store calls it
// after computing the event's chain hash and stores the result as the event's
// Sig; the signature is excluded from the chain-hash preimage by design, so it
// attests the hash without altering it. Because *Signer may wrap an HSM/KMS key,
// this is the seam against the local-disk-key limitation (docs/SECURITY-HARDENING.md).
func (s *Signer) SignEvent(tenant string, seq int64, hash []byte) []byte {
	return ed25519.Sign(s.priv, eventPreimage(tenant, seq, hash))
}

// EventSigReport is the outcome of verifying the per-event signatures in a chain.
type EventSigReport struct {
	// Events is the number of non-checkpoint events seen (checkpoints are verified
	// by VerifyCheckpoints, under a different signature domain).
	Events int
	// Signed is how many of those events carried a VALID signature.
	Signed int
	// OK is true only when at least one non-checkpoint event was found and every
	// such event carried a valid signature (Events == Signed and none invalid).
	// A missing signature on a non-checkpoint event is a failure when verifying
	// with a key, because per-event signing is on by default: an absent signature
	// means the event predates signing or was stripped — either way the per-event
	// guarantee does not hold for it.
	OK bool
	// FirstBadSeq is the sequence of the first event that failed, or 0.
	FirstBadSeq int64
	// Reason describes the first failure ("no-events", "event-sig-invalid",
	// "event-sig-missing"), or "".
	Reason string
}

// VerifyEvents walks a tenant's chain and verifies every non-checkpoint event's
// detached Ed25519 signature against pub. Run it alongside store.AuditLog.Verify
// (chain integrity) and VerifyCheckpoints (checkpoint signatures): together they
// prove every event is authentic and was signed by the holder of the private key,
// so the tail cannot be rewritten — even between checkpoints or after the signed
// checkpoints are deleted — without that key. Checkpoint events are skipped here;
// they carry a signature under checkpointDomain and are VerifyCheckpoints' job.
func VerifyEvents(ctx context.Context, log store.AuditLog, pub ed25519.PublicKey) (EventSigReport, error) {
	// Single implementation: delegate to the multi-candidate verifier with one
	// key (preserves the pre behavior and signature exactly).
	return VerifyEventsWith(ctx, log, []ed25519.PublicKey{pub})
}

// VerifyEventsWith is VerifyEvents generalized to a SET of candidate Ed25519
// verification keys, accepting an event if ANY candidate verifies it — the
// per-event counterpart of VerifyCheckpointsWith, and what makes per-event key
// ROTATION verifiable: a chain whose signing key rotated mid-life (a
// `keys rotate` ceremony) verifies end-to-end by pinning the current key plus
// the prior generations' public keys (the non-secret rotation history a sealed
// envelope records in prior_public_keys). Per-event signatures are Ed25519 by
// design — the hot path never goes off-box (docs/SECURITY-HARDENING.md) — so the candidates are
// raw Ed25519 keys, not the CheckpointVerifier's multi-algorithm set.
func VerifyEventsWith(ctx context.Context, log store.AuditLog, pubs []ed25519.PublicKey) (EventSigReport, error) {
	return VerifyEventsWithFrom(ctx, log, 1, pubs)
}

// VerifyEventsWithFrom is the range-aware form used by the CLI's --from epoch
// verification. The original VerifyEventsWith contract remains a genesis walk.
//
// HONEST LIMIT (F-07): this form treats EVERY candidate key as unbounded —
// any key may verify any sequence. That is correct for a single key (genesis /
// DR) and for the current key, but for a multi-generation ROTATED chain it does
// NOT fence a retired key to its epoch: a retired key + a DB write could re-sign
// tail events and still pass. Callers that know the rotation boundaries (an
// in-chain audit.key.rotation marker, or an operator pin `key@last_seq`) MUST use
// VerifyEventsFenced instead, which restricts each generation to its sequence
// range. This entry point is kept for the single-key and no-boundary-known cases.
func VerifyEventsWithFrom(ctx context.Context, log store.AuditLog, fromSeq int64, pubs []ed25519.PublicKey) (EventSigReport, error) {
	if len(pubs) == 0 {
		return EventSigReport{}, fmt.Errorf("audit: no event verification key configured")
	}
	keys := make([]FencedKey, 0, len(pubs))
	for _, pub := range pubs {
		// LastSeq 0 == unbounded (current key): reproduces the pre flat set
		// exactly for single-key and current-key callers.
		keys = append(keys, FencedKey{Key: pub})
	}
	return VerifyEventsFenced(ctx, log, fromSeq, keys)
}

// FencedKey is one epoch-fenced per-event verification key: an Ed25519 public key
// plus the per-tenant sequence range it is trusted to have signed. LastSeq is the
// INCLUSIVE upper bound; LastSeq == 0 means "the CURRENT key" — no upper bound; it
// signs the tail from the last rotation boundary onward. FirstSeq is an OPTIONAL
// inclusive lower bound (0 = derive it by partitioning: a retired generation owns
// everything above the previous generation's boundary; the current key owns
// everything above the highest boundary). A retired generation carries the last
// sequence it legitimately signed (its `last_seq`), so a rotated chain verifies
// end-to-end while a retired key can NEVER validate an event outside its epoch —
// closing F-07 (retired-key + DB-write tail rewrite). The boundaries come from the
// in-chain audit.key.rotation markers (LocateKeyFences) or from an external
// auditor's `--event-pubkey key@last_seq` (or `key@lo:hi`) pins.
type FencedKey struct {
	Key      ed25519.PublicKey
	FirstSeq int64
	LastSeq  int64
}

// VerifyEventsFenced is the epoch-fencing per-event verifier. Unlike the
// flat VerifyEventsWith, it accepts an event at sequence S only if the ONE key
// whose epoch contains S verifies it: a bounded (retired) key with LastSeq=L
// covers the range (prevBound, L]; the current (unbounded, LastSeq==0) key covers
// everything above the highest bound. The sequence line is thus partitioned into
// contiguous per-generation epochs, so a retired key that could re-sign a
// current-epoch tail event is rejected, while a legitimate event signed by the
// old key WITHIN its range still passes. With every key unbounded it is byte-for-
// byte the old flat behavior (the single-key / no-boundary case). The epoch
// partition math lives in epochFence, shared verbatim with the archive verifier
// (VerifyArchiveDir) so the live and offline paths fence identically.
func VerifyEventsFenced(ctx context.Context, log store.AuditLog, fromSeq int64, keys []FencedKey) (EventSigReport, error) {
	rep := EventSigReport{}
	if len(keys) == 0 {
		return EventSigReport{}, fmt.Errorf("audit: no event verification key configured")
	}
	if err := validateFencedKeys(keys); err != nil {
		return EventSigReport{}, err
	}
	fence := newEpochFence(keys)
	if fromSeq < 1 {
		fromSeq = 1
	}
	err := log.Walk(ctx, fromSeq, func(ev model.AuditEvent) error {
		if ev.Action == ActionCheckpoint || ev.Action == store.ActionAuditRecover ||
			ev.Action == store.ActionAuditKeyRotation {
			// Checkpoints, recovery boundaries and key-rotation boundaries carry
			// signatures under their own off-box domains. They are verified
			// exhaustively by VerifyCheckpointsWith / VerifyRecoveryMarkersWith /
			// VerifyKeyRotationMarkersWith; treating one as a hot-path event signature
			// would make every legitimate rotated or recovered epoch look corrupt.
			return nil
		}
		rep.Events++
		if len(ev.Sig) == 0 {
			rep.fail(ev.Seq, "event-sig-missing")
			return nil
		}
		preimage := eventPreimage(ev.TenantID.String(), ev.Seq, ev.Hash)
		if !fence.verify(ev.Seq, preimage, ev.Sig) {
			rep.fail(ev.Seq, "event-sig-invalid")
			return nil
		}
		rep.Signed++
		return nil
	})
	if err != nil {
		return EventSigReport{}, err
	}
	if rep.Events == 0 {
		rep.Reason = "no-events"
	} else if rep.Reason == "" {
		rep.OK = true
	}
	return rep, nil
}

// validateFencedKeys checks each key's size and boundary sanity — the shared
// precondition of both the live (VerifyEventsFenced) and offline archive
// (VerifyArchiveDir) fenced verifiers. It deliberately does NOT reject an empty
// set: the live verifier treats empty as an error (no key configured) while the
// archive verifier treats it as "run no per-event check", so each caller decides.
func validateFencedKeys(keys []FencedKey) error {
	for _, k := range keys {
		if len(k.Key) != ed25519.PublicKeySize {
			return fmt.Errorf("audit: bad public key size %d", len(k.Key))
		}
		if k.LastSeq < 0 || k.FirstSeq < 0 {
			return fmt.Errorf("audit: negative fence boundary (first=%d last=%d)", k.FirstSeq, k.LastSeq)
		}
		if k.LastSeq > 0 && k.FirstSeq > k.LastSeq {
			return fmt.Errorf("audit: fence lower bound %d exceeds upper bound %d", k.FirstSeq, k.LastSeq)
		}
	}
	return nil
}

// epochFence partitions a set of FencedKeys into contiguous per-generation epochs
// and answers, for one sequence, which key's epoch owns it. It is the SINGLE
// definition of the F-07 epoch math, shared by the live VerifyEventsFenced
// and the offline VerifyArchiveDir per-event checks: the two paths MUST fence
// identically or the archive re-opens the hole the live path closes.
type epochFence struct {
	keys []FencedKey
	// maxBound is the highest retired-generation boundary; a current key with no
	// explicit lower bound owns every sequence strictly above it.
	maxBound int64
}

func newEpochFence(keys []FencedKey) epochFence {
	var maxBound int64
	for _, k := range keys {
		if k.LastSeq > maxBound {
			maxBound = k.LastSeq
		}
	}
	return epochFence{keys: keys, maxBound: maxBound}
}

// lowerBound is the previous epoch's boundary below hi (the highest retired
// LastSeq strictly less than hi), so a bounded key with no explicit FirstSeq owns
// exactly (lowerBound, LastSeq].
func (f epochFence) lowerBound(hi int64) int64 {
	var lo int64
	for _, k := range f.keys {
		if k.LastSeq > 0 && k.LastSeq < hi && k.LastSeq > lo {
			lo = k.LastSeq
		}
	}
	return lo
}

// covers reports whether key k's epoch owns seq. An explicit FirstSeq (the
// `@lo:hi` pin form) overrides the derived lower bound.
func (f epochFence) covers(k FencedKey, seq int64) bool {
	hi := k.LastSeq
	if hi == 0 {
		hi = 1<<63 - 1 // unbounded upper (math.MaxInt64)
	}
	loExclusive := f.maxBound
	switch {
	case k.FirstSeq > 0:
		loExclusive = k.FirstSeq - 1 // explicit inclusive lower
	case k.LastSeq > 0:
		loExclusive = f.lowerBound(k.LastSeq) // previous retired boundary
	}
	return seq > loExclusive && seq <= hi
}

// verify reports whether the ONE key whose epoch owns seq validates sig over
// preimage. A retired key re-signing a later epoch's event fails here even though
// its raw Ed25519 signature is valid — the epoch fence, not the math, rejects it.
func (f epochFence) verify(seq int64, preimage, sig []byte) bool {
	for _, k := range f.keys {
		if f.covers(k, seq) && ed25519.Verify(k.Key, preimage, sig) {
			return true
		}
	}
	return false
}

func (r *EventSigReport) fail(seq int64, reason string) {
	r.OK = false
	if r.FirstBadSeq == 0 {
		r.FirstBadSeq = seq
		r.Reason = reason
	}
}

// eventPreimage builds the canonical, domain-separated, length-prefixed preimage
// an external verifier reproduces: domain ‖ tenant ‖ seq(8 BE) ‖ hash. It mirrors
// checkpointPreimage but under eventDomain, so the two can never be confused.
func eventPreimage(tenant string, seq int64, hash []byte) []byte {
	var buf []byte
	buf = lenPrefix(buf, []byte(eventDomain))
	buf = lenPrefix(buf, []byte(tenant))
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], uint64(seq))
	buf = append(buf, s[:]...)
	buf = lenPrefix(buf, hash)
	return buf
}
