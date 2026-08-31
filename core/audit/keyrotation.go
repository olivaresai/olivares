// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// keyRotationDomain binds the key-transition marker's signature preimage to its
// purpose and version, so it can never be confused with a checkpoint, recovery or
// per-event signature.
const keyRotationDomain = "olivares.audit.keyrotation.v1"

// KeyRotationEvidence is the complete, non-secret evidence sealed into an
// audit.key.rotation epoch-boundary marker (F-07). The marker changes no
// event: it permanently records that, for this tenant, the RETIRING per-event
// signing key (PriorFingerprint) legitimately signed up to PriorLastSeq, and the
// key that takes over from PriorLastSeq+1 is NewFingerprint. That boundary is what
// lets the verifier FENCE the retired key to its epoch (VerifyEventsFenced) rather
// than trusting it for every sequence forever. It is signed off-box (the pinned
// checkpoint key), exactly like a recovery marker, so a host/DB attacker holding a
// retired ON-BOX key cannot forge a boundary that re-widens the retired key.
type KeyRotationEvidence struct {
	Tenant           string `json:"tenant"`
	PriorFingerprint string `json:"prior_fingerprint"`
	PriorLastSeq     int64  `json:"prior_last_seq"`
	NewFingerprint   string `json:"new_fingerprint"`
	OffBoxKeyID      string `json:"offbox_key_id"`
}

// KeyRotationMarkerReport is the outcome of verifying every audit.key.rotation
// marker in a ledger. Absence is neutral, but a present marker must carry a valid
// pinned off-box signature and occupy the exact sequence immediately after the
// boundary its signed evidence declares (prior_last_seq).
type KeyRotationMarkerReport struct {
	Markers     int    `json:"markers"`
	Valid       int    `json:"valid"`
	OK          bool   `json:"ok"`
	FirstBadSeq int64  `json:"first_bad_seq,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// KeyFingerprint is the stable, non-secret identifier of an Ed25519 verification
// key used in transition markers and operator pins: the hex SHA-256 of the raw
// 32-byte public key. It is what `keys status` and `--event-pubkey key@last_seq`
// name, and what LocateKeyFences maps to a boundary.
func KeyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// RecordKeyRotation appends one signed key-transition marker for the pinned
// tenant. Only the configured off-box checkpoint key may sign it: the on-box audit
// key is deliberately NOT a fallback, because the boundary is precisely the
// control that revokes a retired on-box key's verification power — signing it with
// an on-box key would let the very attacker it defends against forge it. The
// marker lands at PriorLastSeq+1 (the current tail+1 when the engine is stopped
// for rotation); a concurrent append that shifts the tail is rejected so a
// displaced, permanently-invalid boundary never commits.
// It is IDEMPOTENT on the retired/new key PAIR: if this ledger already carries a
// valid marker fencing the same PriorFingerprint to the same NewFingerprint, no
// second marker is appended and the existing one is returned with existed=true.
//
// The check lives here rather than in the command for the same reason the
// enumeration check lives in the store: a caller cannot forget it, and a caller
// written later inherits it. It is also what makes the ceremony RESUMABLE without
// any cursor — re-running it after a partial failure skips the tenants already
// fenced and continues with the rest, and a cursor file is one more piece of state
// to lose, desync or forge for a guarantee the immutable ledger already carries.
//
// Note what is NOT the key: the boundary. A re-run derives PriorLastSeq from the
// CURRENT head, so keying on the boundary would let every re-run through — which is
// exactly the de-revocation LocateKeyFences documents.
func RecordKeyRotation(ctx context.Context, log store.AuditLog, signer *Signer, ev KeyRotationEvidence) (model.AuditEvent, bool, error) {
	if log == nil {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation: nil audit log")
	}
	if signer == nil || !signer.OffBoxCheckpoints() || signer.CheckpointKey() == nil {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation marker requires an off-box checkpoint signer")
	}
	keyID := strings.TrimSpace(signer.CheckpointKey().KeyID())
	if ev.OffBoxKeyID == "" {
		ev.OffBoxKeyID = keyID
	} else if ev.OffBoxKeyID != keyID {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation off-box key id %q does not match signer %q", ev.OffBoxKeyID, keyID)
	}
	if err := validateKeyRotationEvidence(ev); err != nil {
		return model.AuditEvent{}, false, err
	}
	// Only a marker that VERIFIES suppresses a new one. An unsigned or forged
	// duplicate must never be able to stop a legitimate boundary from being recorded:
	// that would let an attacker who can write rows disable the fencing ceremony
	// itself by planting a decoy.
	verifier, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation: build verifier: %w", err)
	}
	existing, found, err := findKeyRotationMarker(ctx, log, verifier, ev.PriorFingerprint, ev.NewFingerprint)
	if err != nil {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation: scan for an existing boundary: %w", err)
	}
	if found {
		return existing, true, nil
	}
	sig, sigMeta, err := signer.signCheckpoint(ctx, keyRotationPreimage(ev))
	if err != nil {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation sign: %w", err)
	}
	meta := keyRotationMeta(ev)
	for k, value := range sigMeta {
		meta[k] = value
	}
	marker, err := log.Append(ctx, model.AuditDraft{
		Actor:      model.ActorSystem,
		ActorKind:  model.ActorSystem,
		Action:     store.ActionAuditKeyRotation,
		TargetKind: "core.audit_key_rotation",
		Meta:       meta,
		Sig:        sig,
	})
	if err != nil {
		return model.AuditEvent{}, false, err
	}
	// Append assigns the sequence only after the off-box signature exists. A
	// concurrent writer may have advanced the tail since the caller derived
	// PriorLastSeq; never let that displaced boundary commit. Returning an error
	// from the surrounding Mutate rolls the INSERT back for a clean retry.
	if marker.Seq < 1 || marker.Seq-1 != ev.PriorLastSeq {
		return model.AuditEvent{}, false, fmt.Errorf("audit: key rotation marker landed at seq %d but signed evidence declares boundary %d; a concurrent append shifted the tail — retry the rotation", marker.Seq, ev.PriorLastSeq)
	}
	return marker, false, nil
}

// findKeyRotationMarker returns the first VALID marker in this ledger that fences
// priorFP to newFP, if one is already recorded. It is the idempotence probe for
// RecordKeyRotation and deliberately reuses verifyKeyRotationMarker, so a marker
// only counts as "already recorded" under exactly the rules the verifier applies —
// a decoy the verifier would reject can never suppress a real boundary.
func findKeyRotationMarker(ctx context.Context, log store.AuditLog, v *CheckpointVerifier, priorFP, newFP string) (model.AuditEvent, bool, error) {
	var (
		found model.AuditEvent
		ok    bool
	)
	err := walkKeyRotationEvents(ctx, log, func(event model.AuditEvent, metaJSON []byte) error {
		if ok || event.Action != store.ActionAuditKeyRotation || len(event.Sig) == 0 {
			return nil
		}
		candidate, reason := verifyKeyRotationMarker(event, metaJSON, v)
		if reason != "" {
			return nil
		}
		if candidate.PriorFingerprint == priorFP && candidate.NewFingerprint == newFP {
			found, ok = event, true
		}
		return nil
	})
	if err != nil {
		return model.AuditEvent{}, false, err
	}
	return found, ok, nil
}

// LocateKeyFences returns the per-tenant sequence boundary of every RETIRED
// generation this ledger records, as fingerprint -> last_seq. An absent/empty
// verifier honors no marker: an auditor must pin the off-box public key
// independently from the possibly compromised engine and database. Invalid
// candidates are ignored here (like LocateRecoveryEvidence);
// VerifyKeyRotationMarkersWith is the fail-closed pass, and it REPORTS the
// duplicates this function resolves silently.
//
// When two valid markers name the same retired fingerprint, the SMALLEST boundary
// wins — the honest one, and the one that grants the retired key the least.
//
// This rule was reversed until and the comment that justified it said a
// re-recorded boundary "can only widen to the true tail, never shrink a key's
// already-attested epoch". That is false whenever the ceremony is re-entered after
// service resumes, which is the normal way an operator re-runs a runbook. Measured
// on a live chain: honest boundary 4, ordinary events appended to seq 8, ceremony
// re-entered at the current head, and LARGEST-wins moved the fence to 8. The retired
// key then verified 7 of 7 ordinary events (OK=true) instead of 4 of 7 (first bad at
// seq 6) — it was handed a valid epoch over events that belong to the NEW key. That
// is not an off-by-one on the boundary: it is DE-REVOCATION of the very key the
// boundary exists to retire, and the second marker is itself off-box-signed, so the
// verifier accepts it.
//
// The other two candidate rules were weighed against the same measurement and lost:
//   - LARGEST: measured above. Turns every re-run into a widening.
//   - REJECT duplicates outright: markers are immutable, so an honest operator who
//     ran the ceremony twice would leave the ledger permanently unverifiable with no
//     repair path — an operator slip converted into a bricked chain.
//
// SMALLEST has one honest cost, named rather than hidden: a key RE-ADOPTED after
// retirement (retire A, adopt B, re-adopt A, retire A again) has two disjoint
// legitimate epochs, and this map holds one scalar per fingerprint, so it cannot
// express them under ANY rule. Under SMALLEST that case fails CLOSED — A's second
// epoch is rejected, loudly and investigably. Under LARGEST it failed OPEN. For a
// revocation control the closed direction is the correct one, and re-adopting a
// retired signing key is an opsec error the runbook already forbids.
func LocateKeyFences(ctx context.Context, log store.AuditLog, v *CheckpointVerifier) (map[string]int64, error) {
	fences := map[string]int64{}
	if log == nil || v == nil || v.Empty() {
		return fences, nil
	}
	err := walkKeyRotationEvents(ctx, log, func(event model.AuditEvent, metaJSON []byte) error {
		if event.Action != store.ActionAuditKeyRotation || len(event.Sig) == 0 {
			return nil
		}
		candidate, reason := verifyKeyRotationMarker(event, metaJSON, v)
		if reason != "" {
			return nil
		}
		if cur, ok := fences[candidate.PriorFingerprint]; !ok || candidate.PriorLastSeq < cur {
			fences[candidate.PriorFingerprint] = candidate.PriorLastSeq
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fences, nil
}

// ReasonConflictingFence is the report Reason when a ledger carries more than one
// valid boundary for the SAME retired fingerprint. LocateKeyFences resolves that by
// keeping the smallest, which is safe but silent; this is where it becomes visible.
// Every duplicate is either a ceremony re-entered before RecordKeyRotation became
// idempotent, or an attempt to move a boundary that must never move — and an
// operator has to be able to tell which, so neither is waved through.
const ReasonConflictingFence = "keyrotation-conflicting-fence"

// VerifyKeyRotationMarkersWith verifies every reserved key-rotation marker,
// fail-closed: one bad marker fails the whole report. Callers run it even when
// verifying a later epoch, so a forged boundary anywhere in the immutable ledger
// cannot hide. Absence of markers is neutral (an un-rotated chain still verifies).
//
// Two valid markers that name the same retired fingerprint with DIFFERENT boundaries
// also fail it (ReasonConflictingFence). A duplicate is not a second opinion: the
// boundary is the fact that revokes a key, and a ledger holding two of them has
// recorded that the revocation moved. Reporting it is what keeps LocateKeyFences'
// smallest-wins from being a silent repair of evidence.
func VerifyKeyRotationMarkersWith(ctx context.Context, log store.AuditLog, v *CheckpointVerifier) (KeyRotationMarkerReport, error) {
	rep := KeyRotationMarkerReport{}
	if log == nil {
		return KeyRotationMarkerReport{}, fmt.Errorf("audit: nil audit log")
	}
	if v == nil || v.Empty() {
		return KeyRotationMarkerReport{}, fmt.Errorf("audit: no key rotation verification key configured")
	}
	// fingerprint -> the boundary the first valid marker declared, so a later,
	// different boundary for that same key can be named at the seq that introduced it.
	boundaries := map[string]int64{}
	err := walkKeyRotationEvents(ctx, log, func(event model.AuditEvent, metaJSON []byte) error {
		if event.Action != store.ActionAuditKeyRotation {
			return nil
		}
		rep.Markers++
		candidate, reason := verifyKeyRotationMarker(event, metaJSON, v)
		if reason != "" {
			rep.fail(event.Seq, reason)
			return nil
		}
		rep.Valid++
		if seen, ok := boundaries[candidate.PriorFingerprint]; ok && seen != candidate.PriorLastSeq {
			rep.fail(event.Seq, ReasonConflictingFence)
			return nil
		}
		boundaries[candidate.PriorFingerprint] = candidate.PriorLastSeq
		return nil
	})
	if err != nil {
		return KeyRotationMarkerReport{}, err
	}
	if rep.Reason == "" {
		rep.OK = true
	}
	return rep, nil
}

func (r *KeyRotationMarkerReport) fail(seq int64, reason string) {
	r.OK = false
	if r.FirstBadSeq == 0 {
		r.FirstBadSeq = seq
		r.Reason = reason
	}
}

func verifyKeyRotationMarker(event model.AuditEvent, metaJSON []byte, v *CheckpointVerifier) (KeyRotationEvidence, string) {
	candidate, err := decodeKeyRotationEvidence(metaJSON)
	if err != nil || validateKeyRotationEvidence(candidate) != nil || candidate.Tenant != event.TenantID.String() ||
		len(event.Sig) == 0 || !v.verify(keyRotationPreimage(candidate), event.Sig) {
		return KeyRotationEvidence{}, "keyrotation-sig-invalid"
	}
	// Subtraction rather than PriorLastSeq+1 so a hostile MaxInt64 boundary cannot
	// wrap while checking the same signed invariant: the marker sits immediately
	// after the boundary it declares.
	if event.Seq < 1 || candidate.PriorLastSeq != event.Seq-1 {
		return KeyRotationEvidence{}, "keyrotation-position-invalid"
	}
	return candidate, ""
}

// keyRotationPreimage is a versioned, domain-separated, length-prefixed encoding
// of every evidence field. Like recoverPreimage it excludes the marker's assigned
// seq: Append chooses that atomically after the off-box signature exists.
func keyRotationPreimage(ev KeyRotationEvidence) []byte {
	var buf []byte
	buf = lenPrefix(buf, []byte(keyRotationDomain))
	buf = lenPrefix(buf, []byte(ev.Tenant))
	buf = lenPrefix(buf, []byte(ev.PriorFingerprint))
	buf = appendKeyRotationInt64(buf, ev.PriorLastSeq)
	buf = lenPrefix(buf, []byte(ev.NewFingerprint))
	return lenPrefix(buf, []byte(ev.OffBoxKeyID))
}

func appendKeyRotationInt64(dst []byte, value int64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(value))
	return append(dst, raw[:]...)
}

func keyRotationMeta(ev KeyRotationEvidence) map[string]any {
	return map[string]any{
		"tenant":            ev.Tenant,
		"prior_fingerprint": ev.PriorFingerprint,
		"prior_last_seq":    ev.PriorLastSeq,
		"new_fingerprint":   ev.NewFingerprint,
		"offbox_key_id":     ev.OffBoxKeyID,
	}
}

func validateKeyRotationEvidence(ev KeyRotationEvidence) error {
	switch {
	case strings.TrimSpace(ev.Tenant) == "":
		return fmt.Errorf("audit: key rotation evidence tenant is required")
	case ev.PriorLastSeq < 1:
		return fmt.Errorf("audit: key rotation evidence prior_last_seq must be positive")
	case strings.TrimSpace(ev.OffBoxKeyID) == "":
		return fmt.Errorf("audit: key rotation evidence off-box key id is required")
	case ev.PriorFingerprint == ev.NewFingerprint:
		return fmt.Errorf("audit: key rotation evidence prior and new fingerprints are identical (not a rotation)")
	}
	if !isSHA256Hex(ev.PriorFingerprint) {
		return fmt.Errorf("audit: key rotation evidence prior_fingerprint must be a SHA-256 hex digest")
	}
	if !isSHA256Hex(ev.NewFingerprint) {
		return fmt.Errorf("audit: key rotation evidence new_fingerprint must be a SHA-256 hex digest")
	}
	return nil
}

func isSHA256Hex(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == sha256.Size
}

func decodeKeyRotationEvidence(raw []byte) (KeyRotationEvidence, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var ev KeyRotationEvidence
	if err := dec.Decode(&ev); err != nil {
		return KeyRotationEvidence{}, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return KeyRotationEvidence{}, fmt.Errorf("audit: key rotation metadata has trailing data")
	}
	return ev, nil
}

func walkKeyRotationEvents(ctx context.Context, log store.AuditLog, fn func(model.AuditEvent, []byte) error) error {
	if canonical, ok := log.(store.CanonicalWalker); ok {
		return canonical.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			return fn(ev, []byte(meta))
		})
	}
	return log.Walk(ctx, 1, func(ev model.AuditEvent) error {
		raw, err := json.Marshal(ev.Meta)
		if err != nil {
			return err
		}
		return fn(ev, raw)
	})
}
